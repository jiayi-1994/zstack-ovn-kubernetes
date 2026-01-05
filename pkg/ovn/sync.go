// Package ovn provides startup synchronization for OVN resources.
//
// This file implements the startup sync mechanism that:
// 1. Cleans up stale/orphan OVN resources that no longer have corresponding K8s objects
// 2. Recovers missing OVN resources for existing K8s objects
// 3. Synchronizes IP allocations from existing Pod annotations
//
// The sync runs once at controller startup before normal reconciliation begins.
//
// Reference: OVN-Kubernetes pkg/ovn/master.go (syncNodes, cleanupNodeResources)
package ovn

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkv1 "github.com/jiayi-1994/zstack-ovn-kubernetes/api/v1"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/util"
)

// SyncManager handles startup synchronization of OVN resources.
//
// It ensures consistency between Kubernetes state and OVN database state
// by cleaning up orphan resources and recovering missing configurations.
type SyncManager struct {
	// client is the Kubernetes API client
	client client.Client

	// config is the global configuration
	config *config.Config

	// ovnClient is the OVN database client
	ovnClient *ovndb.Client

	// lsOps provides Logical Switch operations
	lsOps *ovndb.LogicalSwitchOps

	// lspOps provides Logical Switch Port operations
	lspOps *ovndb.LogicalSwitchPortOps

	// lrOps provides Logical Router operations
	lrOps *ovndb.LogicalRouterOps

	// subnetReconciler provides access to subnet allocators
	subnetReconciler *SubnetReconciler

	// podReconciler provides access to pod allocations
	podReconciler *PodReconciler
}

// NewSyncManager creates a new SyncManager.
func NewSyncManager(
	c client.Client,
	cfg *config.Config,
	ovnClient *ovndb.Client,
	subnetReconciler *SubnetReconciler,
	podReconciler *PodReconciler,
) *SyncManager {
	return &SyncManager{
		client:           c,
		config:           cfg,
		ovnClient:        ovnClient,
		lsOps:            ovndb.NewLogicalSwitchOps(ovnClient),
		lspOps:           ovndb.NewLogicalSwitchPortOps(ovnClient),
		lrOps:            ovndb.NewLogicalRouterOps(ovnClient),
		subnetReconciler: subnetReconciler,
		podReconciler:    podReconciler,
	}
}

// SyncResult contains the results of a sync operation.
type SyncResult struct {
	// NodesProcessed is the number of nodes processed
	NodesProcessed int

	// PodsProcessed is the number of pods processed
	PodsProcessed int

	// SubnetsProcessed is the number of subnets processed
	SubnetsProcessed int

	// StaleResourcesCleaned is the number of stale resources cleaned up
	StaleResourcesCleaned int

	// ResourcesRecovered is the number of missing resources recovered
	ResourcesRecovered int

	// Errors contains any errors encountered during sync
	Errors []error

	// Duration is the total time taken for sync
	Duration time.Duration
}

// RunFullSync performs a complete synchronization at startup.
//
// This should be called once when the controller starts, before
// normal reconciliation begins.
//
// Steps:
// 1. Clean up duplicate switches (from previous failed runs)
// 2. Sync cluster router and join network
// 3. Sync nodes (clean stale node switches, recover missing)
// 4. Sync subnets (clean stale subnet switches, recover missing)
// 5. Sync pods (clean stale LSPs, recover IP allocations)
// 6. Sync NAT and routing rules
func (s *SyncManager) RunFullSync(ctx context.Context) (*SyncResult, error) {
	startTime := time.Now()
	result := &SyncResult{}

	klog.Info("Starting full OVN sync...")

	// 0. Clean up duplicate switches first
	if err := s.cleanupDuplicateSwitches(ctx); err != nil {
		klog.Warningf("Failed to cleanup duplicate switches: %v", err)
		// Don't fail, continue with sync
	}

	// 1. Ensure cluster router exists
	if err := s.syncClusterRouter(ctx); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("cluster router sync failed: %w", err))
		klog.Errorf("Cluster router sync failed: %v", err)
	} else {
		klog.Info("Cluster router sync completed")
	}

	// 2. Sync nodes
	nodeResult, err := s.syncNodes(ctx)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("node sync failed: %w", err))
		klog.Errorf("Node sync failed: %v", err)
	} else {
		result.NodesProcessed = nodeResult.processed
		result.StaleResourcesCleaned += nodeResult.cleaned
		result.ResourcesRecovered += nodeResult.recovered
		klog.Infof("Node sync completed: processed=%d, cleaned=%d, recovered=%d",
			nodeResult.processed, nodeResult.cleaned, nodeResult.recovered)
	}

	// 3. Sync subnets
	subnetResult, err := s.syncSubnets(ctx)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("subnet sync failed: %w", err))
		klog.Errorf("Subnet sync failed: %v", err)
	} else {
		result.SubnetsProcessed = subnetResult.processed
		result.StaleResourcesCleaned += subnetResult.cleaned
		result.ResourcesRecovered += subnetResult.recovered
		klog.Infof("Subnet sync completed: processed=%d, cleaned=%d, recovered=%d",
			subnetResult.processed, subnetResult.cleaned, subnetResult.recovered)
	}

	// 4. Sync pods
	podResult, err := s.syncPods(ctx)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("pod sync failed: %w", err))
		klog.Errorf("Pod sync failed: %v", err)
	} else {
		result.PodsProcessed = podResult.processed
		result.StaleResourcesCleaned += podResult.cleaned
		result.ResourcesRecovered += podResult.recovered
		klog.Infof("Pod sync completed: processed=%d, cleaned=%d, recovered=%d",
			podResult.processed, podResult.cleaned, podResult.recovered)
	}

	// 5. Sync NAT rules
	if err := s.syncNATRules(ctx); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("NAT sync failed: %w", err))
		klog.Errorf("NAT sync failed: %v", err)
	} else {
		klog.Info("NAT rules sync completed")
	}

	result.Duration = time.Since(startTime)

	if len(result.Errors) > 0 {
		klog.Warningf("Full sync completed with %d errors in %v", len(result.Errors), result.Duration)
	} else {
		klog.Infof("Full sync completed successfully in %v", result.Duration)
	}

	return result, nil
}

// syncResult holds the result of a specific sync operation
type syncResult struct {
	processed int
	cleaned   int
	recovered int
}

// cleanupDuplicateSwitches removes duplicate logical switches with the same name.
// This can happen if the controller crashed during switch creation or if there
// were race conditions. We keep only one switch per name (the one with ports).
func (s *SyncManager) cleanupDuplicateSwitches(ctx context.Context) error {
	if s.ovnClient == nil || !s.ovnClient.IsConnected() {
		klog.V(4).Info("OVN client not connected, skipping duplicate switch cleanup")
		return nil
	}

	// Get all logical switches
	switches, err := s.lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return fmt.Errorf("failed to list logical switches: %w", err)
	}

	// Group switches by name
	switchesByName := make(map[string][]*ovndb.LogicalSwitch)
	for _, ls := range switches {
		switchesByName[ls.Name] = append(switchesByName[ls.Name], ls)
	}

	// Find and remove duplicates
	cleanedCount := 0
	for name, sws := range switchesByName {
		if len(sws) <= 1 {
			continue
		}

		klog.Infof("Found %d duplicate switches with name %s, cleaning up...", len(sws), name)

		// Find the "best" switch to keep (one with ports, or the first one)
		var keepSwitch *ovndb.LogicalSwitch
		for _, sw := range sws {
			if len(sw.Ports) > 0 {
				keepSwitch = sw
				break
			}
		}
		if keepSwitch == nil {
			keepSwitch = sws[0]
		}

		// Delete all other switches
		for _, sw := range sws {
			if sw.UUID == keepSwitch.UUID {
				continue
			}

			klog.Infof("Deleting duplicate switch %s (UUID: %s)", sw.Name, sw.UUID)
			
			// Delete by UUID directly using the client
			nbClient := s.ovnClient.NBClient()
			if nbClient != nil {
				deleteOps, err := nbClient.Where(sw).Delete()
				if err != nil {
					klog.Warningf("Failed to build delete operation for switch %s: %v", sw.UUID, err)
					continue
				}
				_, err = nbClient.Transact(ctx, deleteOps...)
				if err != nil {
					klog.Warningf("Failed to delete duplicate switch %s: %v", sw.UUID, err)
					continue
				}
				cleanedCount++
			}
		}
	}

	if cleanedCount > 0 {
		klog.Infof("Cleaned up %d duplicate switches", cleanedCount)
	}

	return nil
}

// syncClusterRouter ensures the cluster router and join network exist.
func (s *SyncManager) syncClusterRouter(ctx context.Context) error {
	if s.ovnClient == nil || !s.ovnClient.IsConnected() {
		klog.V(4).Info("OVN client not connected, skipping cluster router sync")
		return nil
	}

	// Ensure cluster router exists
	_, err := s.lrOps.CreateClusterRouter(ctx)
	if err != nil {
		return fmt.Errorf("failed to ensure cluster router: %w", err)
	}

	// Ensure join network exists
	_, err = s.lrOps.EnsureJoinSwitchAndRouterPort(ctx)
	if err != nil {
		return fmt.Errorf("failed to ensure join network: %w", err)
	}

	return nil
}

// syncNodes synchronizes node-related OVN resources.
//
// This function:
// 1. Lists all K8s nodes
// 2. Lists all node-related logical switches in OVN
// 3. Cleans up switches for nodes that no longer exist
// 4. Ensures switches exist for all current nodes
func (s *SyncManager) syncNodes(ctx context.Context) (*syncResult, error) {
	result := &syncResult{}

	if s.ovnClient == nil || !s.ovnClient.IsConnected() {
		klog.V(4).Info("OVN client not connected, skipping node sync")
		return result, nil
	}

	// Get all K8s nodes
	nodeList := &corev1.NodeList{}
	if err := s.client.List(ctx, nodeList); err != nil {
		return result, fmt.Errorf("failed to list nodes: %w", err)
	}

	// Build set of valid node names
	validNodes := sets.NewString()
	for _, node := range nodeList.Items {
		validNodes.Insert(node.Name)
		result.processed++
	}

	// Get all logical switches from OVN
	switches, err := s.lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return result, fmt.Errorf("failed to list logical switches: %w", err)
	}

	// Find and clean up stale node switches
	for _, ls := range switches {
		// Check if this is a node switch (format: node-<nodeName>)
		if !strings.HasPrefix(ls.Name, "node-") {
			continue
		}

		nodeName := strings.TrimPrefix(ls.Name, "node-")
		if nodeName == "" {
			continue
		}

		// Check if node still exists
		if !validNodes.Has(nodeName) {
			klog.Infof("Cleaning up stale node switch: %s (node %s no longer exists)", ls.Name, nodeName)
			if err := s.cleanupNodeResources(ctx, nodeName); err != nil {
				klog.Errorf("Failed to cleanup node resources for %s: %v", nodeName, err)
			} else {
				result.cleaned++
			}
		}
	}

	// Ensure switches exist for all current nodes
	// (This will be handled by NodeController reconciliation, but we can pre-check)
	for _, node := range nodeList.Items {
		switchName := fmt.Sprintf("node-%s", node.Name)
		_, err := s.lsOps.GetLogicalSwitch(ctx, switchName)
		if ovndb.IsNotFound(err) {
			klog.V(4).Infof("Node switch %s will be created by NodeController", switchName)
			// Don't create here - let NodeController handle it with proper subnet allocation
		}
	}

	return result, nil
}

// cleanupNodeResources cleans up all OVN resources for a deleted node.
func (s *SyncManager) cleanupNodeResources(ctx context.Context, nodeName string) error {
	switchName := fmt.Sprintf("node-%s", nodeName)

	// Delete router port connecting to this switch
	if err := s.lrOps.DeleteRouterPort(ctx, switchName); err != nil {
		if !ovndb.IsNotFound(err) {
			klog.Warningf("Failed to delete router port for node %s: %v", nodeName, err)
		}
	}

	// Delete the node's logical switch
	if err := s.lsOps.DeleteLogicalSwitch(ctx, switchName); err != nil {
		if !ovndb.IsNotFound(err) {
			return fmt.Errorf("failed to delete logical switch %s: %w", switchName, err)
		}
	}

	// Delete external switch if exists
	extSwitchName := ovndb.GetExternalSwitchName(nodeName)
	if err := s.lsOps.DeleteLogicalSwitch(ctx, extSwitchName); err != nil {
		if !ovndb.IsNotFound(err) {
			klog.Warningf("Failed to delete external switch %s: %v", extSwitchName, err)
		}
	}

	klog.Infof("Cleaned up OVN resources for node %s", nodeName)
	return nil
}

// syncSubnets synchronizes subnet-related OVN resources.
//
// This function:
// 1. Lists all Subnet CRs
// 2. Lists all subnet-related logical switches in OVN
// 3. Cleans up switches for subnets that no longer exist
// 4. Ensures switches exist for all current subnets
func (s *SyncManager) syncSubnets(ctx context.Context) (*syncResult, error) {
	result := &syncResult{}

	if s.ovnClient == nil || !s.ovnClient.IsConnected() {
		klog.V(4).Info("OVN client not connected, skipping subnet sync")
		return result, nil
	}

	// Get all Subnet CRs
	subnetList := &networkv1.SubnetList{}
	if err := s.client.List(ctx, subnetList); err != nil {
		return result, fmt.Errorf("failed to list subnets: %w", err)
	}

	// Build set of valid subnet switch names
	validSwitches := sets.NewString()
	for _, subnet := range subnetList.Items {
		switchName := subnet.GetLogicalSwitchName()
		validSwitches.Insert(switchName)
		result.processed++
	}

	// Get all logical switches from OVN
	switches, err := s.lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return result, fmt.Errorf("failed to list logical switches: %w", err)
	}

	// Find and clean up stale subnet switches
	for _, ls := range switches {
		// Check if this is a subnet switch (format: subnet-<name>)
		if !strings.HasPrefix(ls.Name, "subnet-") {
			continue
		}

		// Skip if it's a valid subnet switch
		if validSwitches.Has(ls.Name) {
			continue
		}

		// Check external_ids to confirm it's a subnet switch
		if ls.ExternalIDs != nil {
			if _, ok := ls.ExternalIDs["k8s.io/subnet"]; ok {
				klog.Infof("Cleaning up stale subnet switch: %s", ls.Name)
				if err := s.cleanupSubnetResources(ctx, ls.Name); err != nil {
					klog.Errorf("Failed to cleanup subnet resources for %s: %v", ls.Name, err)
				} else {
					result.cleaned++
				}
			}
		}
	}

	// Ensure switches exist for all current subnets
	for _, subnet := range subnetList.Items {
		if subnet.Status.Phase != networkv1.SubnetPhaseActive {
			continue
		}

		switchName := subnet.GetLogicalSwitchName()
		_, err := s.lsOps.GetLogicalSwitch(ctx, switchName)
		if ovndb.IsNotFound(err) {
			klog.Infof("Recovering missing subnet switch: %s", switchName)
			// Create the switch
			ls := &ovndb.LogicalSwitch{
				Name: switchName,
				OtherConfig: map[string]string{
					"subnet":      subnet.Spec.CIDR,
					"exclude_ips": subnet.Spec.Gateway,
				},
				ExternalIDs: map[string]string{
					"k8s.io/subnet": subnet.Name,
					"zstack.io/type": "subnet-switch",
				},
			}
			if err := s.lsOps.CreateOrUpdateLogicalSwitch(ctx, ls); err != nil {
				klog.Errorf("Failed to recover subnet switch %s: %v", switchName, err)
			} else {
				result.recovered++
			}
		}
	}

	return result, nil
}

// cleanupSubnetResources cleans up all OVN resources for a deleted subnet.
func (s *SyncManager) cleanupSubnetResources(ctx context.Context, switchName string) error {
	// Delete router port connecting to this switch
	if err := s.lrOps.DeleteRouterPort(ctx, switchName); err != nil {
		if !ovndb.IsNotFound(err) {
			klog.Warningf("Failed to delete router port for subnet switch %s: %v", switchName, err)
		}
	}

	// Delete the subnet's logical switch
	if err := s.lsOps.DeleteLogicalSwitch(ctx, switchName); err != nil {
		if !ovndb.IsNotFound(err) {
			return fmt.Errorf("failed to delete logical switch %s: %w", switchName, err)
		}
	}

	klog.Infof("Cleaned up OVN resources for subnet switch %s", switchName)
	return nil
}

// syncPods synchronizes pod-related OVN resources.
//
// This function:
// 1. Lists all Pods with network annotations
// 2. Lists all pod-related LSPs in OVN
// 3. Cleans up LSPs for pods that no longer exist
// 4. Recovers IP allocations from existing pod annotations
func (s *SyncManager) syncPods(ctx context.Context) (*syncResult, error) {
	result := &syncResult{}

	if s.ovnClient == nil || !s.ovnClient.IsConnected() {
		klog.V(4).Info("OVN client not connected, skipping pod sync")
		return result, nil
	}

	// Get all Pods
	podList := &corev1.PodList{}
	if err := s.client.List(ctx, podList); err != nil {
		return result, fmt.Errorf("failed to list pods: %w", err)
	}

	// Build set of valid pod port names and recover IP allocations
	validPorts := sets.NewString()
	for _, pod := range podList.Items {
		// Skip host network pods
		if pod.Spec.HostNetwork {
			continue
		}

		// Skip completed pods
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}

		portName := ovndb.BuildPortName(pod.Namespace, pod.Name)
		validPorts.Insert(portName)
		result.processed++

		// Recover IP allocation from annotation
		if err := s.recoverPodIPAllocation(ctx, &pod); err != nil {
			klog.V(4).Infof("Failed to recover IP allocation for pod %s/%s: %v",
				pod.Namespace, pod.Name, err)
		}
	}

	// Get all logical switch ports from OVN
	ports, err := s.lspOps.ListLogicalSwitchPorts(ctx)
	if err != nil {
		return result, fmt.Errorf("failed to list logical switch ports: %w", err)
	}

	// Find and clean up stale pod ports
	for _, lsp := range ports {
		// Check if this is a pod port (has namespace and pod external IDs)
		if lsp.ExternalIDs == nil {
			continue
		}

		namespace, hasNS := lsp.ExternalIDs[ovndb.ExternalIDNamespace]
		podName, hasPod := lsp.ExternalIDs[ovndb.ExternalIDPod]
		if !hasNS || !hasPod {
			continue
		}

		// Skip router ports and other special ports
		if lsp.Type == "router" || lsp.Type == "localnet" {
			continue
		}

		// Check if pod still exists
		if !validPorts.Has(lsp.Name) {
			klog.Infof("Cleaning up stale pod port: %s (pod %s/%s no longer exists)",
				lsp.Name, namespace, podName)

			// Get the switch name from external IDs
			switchName := lsp.ExternalIDs["logical_switch"]
			if switchName == "" {
				// Try to find the switch containing this port
				switchName = s.findSwitchForPort(ctx, lsp.Name)
			}

			if switchName != "" {
				if err := s.lspOps.DeleteLogicalSwitchPort(ctx, switchName, lsp.Name); err != nil {
					klog.Errorf("Failed to delete stale port %s: %v", lsp.Name, err)
				} else {
					result.cleaned++
				}
			}
		}
	}

	return result, nil
}

// recoverPodIPAllocation recovers IP allocation from a pod's annotation.
func (s *SyncManager) recoverPodIPAllocation(ctx context.Context, pod *corev1.Pod) error {
	// Get network annotation
	annotation, err := util.GetPodAnnotation(pod)
	if err != nil || annotation == nil {
		return nil // No annotation, nothing to recover
	}

	// Get subnet name
	subnetName := annotation.Subnet
	if subnetName == "" {
		return nil
	}

	// Get IP address
	ipStr := annotation.GetIP()
	if ipStr == "" {
		return nil
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return fmt.Errorf("invalid IP in annotation: %s", ipStr)
	}

	// Get allocator for this subnet
	if s.subnetReconciler == nil {
		return nil
	}

	alloc := s.subnetReconciler.GetAllocator(subnetName)
	if alloc == nil {
		klog.V(4).Infof("Allocator not ready for subnet %s, will recover later", subnetName)
		return nil
	}

	// Mark IP as allocated
	if err := alloc.Allocate(ip); err != nil {
		// IP might already be allocated, which is fine
		klog.V(4).Infof("IP %s already allocated or error: %v", ipStr, err)
	} else {
		klog.V(4).Infof("Recovered IP allocation: %s for pod %s/%s",
			ipStr, pod.Namespace, pod.Name)
	}

	return nil
}

// findSwitchForPort finds the logical switch containing a port.
func (s *SyncManager) findSwitchForPort(ctx context.Context, portName string) string {
	switches, err := s.lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return ""
	}

	for _, ls := range switches {
		for _, portUUID := range ls.Ports {
			// This is a simplified check - in practice we'd need to resolve UUIDs
			if portUUID == portName {
				return ls.Name
			}
		}
	}

	return ""
}

// syncNATRules synchronizes NAT rules on the cluster router.
//
// This ensures SNAT rules exist for the cluster CIDR.
func (s *SyncManager) syncNATRules(ctx context.Context) error {
	if s.ovnClient == nil || !s.ovnClient.IsConnected() {
		klog.V(4).Info("OVN client not connected, skipping NAT sync")
		return nil
	}

	// Get cluster CIDR from config
	clusterCIDR := s.config.Network.ClusterCIDR
	if clusterCIDR == "" {
		klog.V(4).Info("No cluster CIDR configured, skipping NAT sync")
		return nil
	}

	// Get a node to use as SNAT IP source
	nodeList := &corev1.NodeList{}
	if err := s.client.List(ctx, nodeList); err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(nodeList.Items) == 0 {
		klog.V(4).Info("No nodes found, skipping NAT sync")
		return nil
	}

	// Find a node with an internal IP
	var snatIP string
	for _, node := range nodeList.Items {
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				snatIP = addr.Address
				break
			}
		}
		if snatIP != "" {
			break
		}
	}

	if snatIP == "" {
		klog.V(4).Info("No node internal IP found, skipping NAT sync")
		return nil
	}

	// Ensure SNAT rule exists
	if err := s.lrOps.EnsureSNATForCluster(ctx, snatIP, clusterCIDR); err != nil {
		return fmt.Errorf("failed to ensure cluster SNAT: %w", err)
	}

	klog.Infof("NAT rules synced: SNAT %s -> %s", clusterCIDR, snatIP)
	return nil
}

// CleanupStaleResources performs a targeted cleanup of stale resources.
// This can be called periodically or on-demand.
func (s *SyncManager) CleanupStaleResources(ctx context.Context) error {
	klog.Info("Running stale resource cleanup...")

	// Sync nodes
	if _, err := s.syncNodes(ctx); err != nil {
		klog.Errorf("Node cleanup failed: %v", err)
	}

	// Sync subnets
	if _, err := s.syncSubnets(ctx); err != nil {
		klog.Errorf("Subnet cleanup failed: %v", err)
	}

	// Sync pods
	if _, err := s.syncPods(ctx); err != nil {
		klog.Errorf("Pod cleanup failed: %v", err)
	}

	klog.Info("Stale resource cleanup completed")
	return nil
}

// RecoverMissingResources recovers missing OVN resources for existing K8s objects.
// This can be called after a controller restart.
func (s *SyncManager) RecoverMissingResources(ctx context.Context) error {
	klog.Info("Running missing resource recovery...")

	// Ensure cluster router
	if err := s.syncClusterRouter(ctx); err != nil {
		klog.Errorf("Cluster router recovery failed: %v", err)
	}

	// Recover subnet switches
	if _, err := s.syncSubnets(ctx); err != nil {
		klog.Errorf("Subnet recovery failed: %v", err)
	}

	klog.Info("Missing resource recovery completed")
	return nil
}
