// Package node provides node-level network controllers.
//
// This package handles:
// - Node network initialization and subnet allocation
// - Gateway configuration (shared/local mode)
// - Tunnel configuration (VXLAN/Geneve)
// - OVS bridge management
//
// Node Controller Architecture:
// The NodeController watches Kubernetes Node resources and manages:
// 1. Per-node subnet allocation from the cluster CIDR
// 2. Node-level Logical Switch creation in OVN
// 3. Tunnel endpoint configuration for cross-node communication
// 4. Gateway configuration for external traffic
//
// Reference: OVN-Kubernetes pkg/node/
package node

import (
	"context"
	"fmt"
	"net"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/allocator"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
)

// Annotation keys for node network configuration
const (
	// NodeSubnetAnnotation stores the allocated subnet for a node
	// Format: "10.244.1.0/24"
	NodeSubnetAnnotation = "zstack.io/node-subnet"

	// NodeGatewayAnnotation stores the gateway IP for a node's subnet
	// Format: "10.244.1.1"
	NodeGatewayAnnotation = "zstack.io/node-gateway"

	// NodeTunnelIPAnnotation stores the tunnel endpoint IP for a node
	// Format: "192.168.1.100"
	NodeTunnelIPAnnotation = "zstack.io/node-tunnel-ip"

	// NodeLogicalSwitchAnnotation stores the OVN Logical Switch name for a node
	// Format: "node-worker-1"
	NodeLogicalSwitchAnnotation = "zstack.io/node-logical-switch"

	// NodeChassisAnnotation stores the OVN chassis ID for a node
	NodeChassisAnnotation = "zstack.io/node-chassis"
)

// NodeController manages node network configuration.
//
// Responsibilities:
// - Allocate per-node subnets from cluster CIDR
// - Create node-level Logical Switches in OVN
// - Configure tunnel endpoints for cross-node communication
// - Manage gateway configuration
//
// The controller watches Node resources and reconciles their network state.
type NodeController struct {
	// client is the Kubernetes API client
	client client.Client

	// kubeClient is the typed Kubernetes client for direct API access
	kubeClient kubernetes.Interface

	// config is the global configuration
	config *config.Config

	// ovnClient is the OVN database client
	ovnClient *ovndb.Client

	// clusterSubnetAllocator allocates per-node subnets from cluster CIDR
	clusterSubnetAllocator *ClusterSubnetAllocator

	// recorder is the event recorder for generating Kubernetes events
	recorder record.EventRecorder

	// mu protects concurrent access to controller state
	mu sync.RWMutex

	// nodeSubnets tracks allocated subnets per node
	// Key: node name, Value: allocated subnet CIDR
	nodeSubnets map[string]string
}

// ClusterSubnetAllocator manages allocation of per-node subnets from the cluster CIDR.
//
// For example, with cluster CIDR 10.244.0.0/16 and node subnet size /24:
// - Node 1 gets 10.244.0.0/24
// - Node 2 gets 10.244.1.0/24
// - Node 3 gets 10.244.2.0/24
// etc.
type ClusterSubnetAllocator struct {
	// clusterCIDR is the overall cluster network CIDR
	clusterCIDR *net.IPNet

	// nodeSubnetSize is the prefix length for per-node subnets
	nodeSubnetSize int

	// allocator tracks which subnets are allocated
	allocator *allocator.SubnetAllocator

	// mu protects concurrent access
	mu sync.Mutex
}

// NewClusterSubnetAllocator creates a new cluster subnet allocator.
//
// Parameters:
//   - clusterCIDR: The cluster-wide CIDR (e.g., "10.244.0.0/16")
//   - nodeSubnetSize: The prefix length for per-node subnets (e.g., 24 for /24)
//
// Returns:
//   - *ClusterSubnetAllocator: The allocator instance
//   - error: Error if CIDR is invalid or subnet size is incompatible
func NewClusterSubnetAllocator(clusterCIDR string, nodeSubnetSize int) (*ClusterSubnetAllocator, error) {
	_, cidr, err := net.ParseCIDR(clusterCIDR)
	if err != nil {
		return nil, fmt.Errorf("invalid cluster CIDR %q: %w", clusterCIDR, err)
	}

	// Validate subnet size
	ones, bits := cidr.Mask.Size()
	if nodeSubnetSize <= ones {
		return nil, fmt.Errorf("node subnet size /%d must be larger than cluster CIDR /%d", nodeSubnetSize, ones)
	}
	if nodeSubnetSize > bits-2 {
		return nil, fmt.Errorf("node subnet size /%d is too large for %d-bit addresses", nodeSubnetSize, bits)
	}

	// Calculate number of possible node subnets
	// For 10.244.0.0/16 with /24 node subnets: 2^(24-16) = 256 subnets
	numSubnets := 1 << (nodeSubnetSize - ones)

	// Create a bitmap allocator to track subnet allocation
	// We use the subnet index as the "IP" to allocate
	subnetAllocator, err := allocator.NewSubnetAllocator(
		fmt.Sprintf("0.0.0.0/%d", 32-intLog2(numSubnets)),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create subnet allocator: %w", err)
	}

	return &ClusterSubnetAllocator{
		clusterCIDR:    cidr,
		nodeSubnetSize: nodeSubnetSize,
		allocator:      subnetAllocator,
	}, nil
}

// AllocateSubnet allocates a new subnet for a node.
//
// Returns:
//   - *net.IPNet: The allocated subnet
//   - error: Error if no subnets are available
func (a *ClusterSubnetAllocator) AllocateSubnet() (*net.IPNet, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Get the next available subnet index
	indexIP, err := a.allocator.AllocateNext()
	if err != nil {
		return nil, fmt.Errorf("no available subnets: %w", err)
	}

	// Convert index to subnet
	index := int(indexIP[3]) | int(indexIP[2])<<8 | int(indexIP[1])<<16 | int(indexIP[0])<<24
	return a.indexToSubnet(index), nil
}

// AllocateSpecificSubnet allocates a specific subnet.
//
// Parameters:
//   - subnet: The subnet to allocate
//
// Returns:
//   - error: Error if subnet is already allocated or invalid
func (a *ClusterSubnetAllocator) AllocateSpecificSubnet(subnet *net.IPNet) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	index, err := a.subnetToIndex(subnet)
	if err != nil {
		return err
	}

	// Convert index to IP for the allocator
	indexIP := net.IPv4(byte(index>>24), byte(index>>16), byte(index>>8), byte(index))
	return a.allocator.Allocate(indexIP)
}

// ReleaseSubnet releases a previously allocated subnet.
//
// Parameters:
//   - subnet: The subnet to release
//
// Returns:
//   - error: Error if subnet was not allocated
func (a *ClusterSubnetAllocator) ReleaseSubnet(subnet *net.IPNet) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	index, err := a.subnetToIndex(subnet)
	if err != nil {
		return err
	}

	// Convert index to IP for the allocator
	indexIP := net.IPv4(byte(index>>24), byte(index>>16), byte(index>>8), byte(index))
	return a.allocator.Release(indexIP)
}

// indexToSubnet converts a subnet index to a subnet CIDR.
func (a *ClusterSubnetAllocator) indexToSubnet(index int) *net.IPNet {
	// Calculate the subnet base IP
	// For cluster CIDR 10.244.0.0/16 with /24 subnets:
	// Index 0 -> 10.244.0.0/24
	// Index 1 -> 10.244.1.0/24
	// Index 256 -> 10.245.0.0/24 (if cluster CIDR was larger)

	ones, _ := a.clusterCIDR.Mask.Size()
	_ = a.nodeSubnetSize - ones // subnetBits - used for documentation

	// Get base IP as uint32
	baseIP := a.clusterCIDR.IP.To4()
	baseInt := uint32(baseIP[0])<<24 | uint32(baseIP[1])<<16 | uint32(baseIP[2])<<8 | uint32(baseIP[3])

	// Add index shifted by the number of host bits in the node subnet
	hostBits := 32 - a.nodeSubnetSize
	subnetInt := baseInt + uint32(index)<<hostBits

	// Convert back to IP
	subnetIP := net.IPv4(
		byte(subnetInt>>24),
		byte(subnetInt>>16),
		byte(subnetInt>>8),
		byte(subnetInt),
	)

	return &net.IPNet{
		IP:   subnetIP,
		Mask: net.CIDRMask(a.nodeSubnetSize, 32),
	}
}

// subnetToIndex converts a subnet CIDR to a subnet index.
func (a *ClusterSubnetAllocator) subnetToIndex(subnet *net.IPNet) (int, error) {
	// Validate subnet is within cluster CIDR
	if !a.clusterCIDR.Contains(subnet.IP) {
		return -1, fmt.Errorf("subnet %s is not within cluster CIDR %s", subnet, a.clusterCIDR)
	}

	// Validate subnet size matches
	ones, _ := subnet.Mask.Size()
	if ones != a.nodeSubnetSize {
		return -1, fmt.Errorf("subnet size /%d does not match expected /%d", ones, a.nodeSubnetSize)
	}

	// Calculate index
	baseIP := a.clusterCIDR.IP.To4()
	baseInt := uint32(baseIP[0])<<24 | uint32(baseIP[1])<<16 | uint32(baseIP[2])<<8 | uint32(baseIP[3])

	subnetIP := subnet.IP.To4()
	subnetInt := uint32(subnetIP[0])<<24 | uint32(subnetIP[1])<<16 | uint32(subnetIP[2])<<8 | uint32(subnetIP[3])

	hostBits := 32 - a.nodeSubnetSize
	index := int((subnetInt - baseInt) >> hostBits)

	return index, nil
}

// intLog2 returns the integer log base 2 of n.
func intLog2(n int) int {
	log := 0
	for n > 1 {
		n >>= 1
		log++
	}
	return log
}


// NewNodeController creates a new node controller.
//
// Parameters:
//   - client: Kubernetes API client
//   - kubeClient: Typed Kubernetes client
//   - cfg: Global configuration
//   - ovnClient: OVN database client
//   - recorder: Event recorder
//
// Returns:
//   - *NodeController: Node controller instance
//   - error: Initialization error
func NewNodeController(
	client client.Client,
	kubeClient kubernetes.Interface,
	cfg *config.Config,
	ovnClient *ovndb.Client,
	recorder record.EventRecorder,
) (*NodeController, error) {
	// Create cluster subnet allocator
	clusterAllocator, err := NewClusterSubnetAllocator(
		cfg.Network.ClusterCIDR,
		cfg.Network.NodeSubnetSize,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster subnet allocator: %w", err)
	}

	return &NodeController{
		client:                 client,
		kubeClient:             kubeClient,
		config:                 cfg,
		ovnClient:              ovnClient,
		clusterSubnetAllocator: clusterAllocator,
		recorder:               recorder,
		nodeSubnets:            make(map[string]string),
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
//
// Parameters:
//   - mgr: Controller manager
//
// Returns:
//   - error: Setup error
func (c *NodeController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1, // Process nodes sequentially to avoid race conditions
		}).
		Complete(c)
}

// Reconcile handles Node create/update/delete events.
//
// The reconciliation logic:
// 1. Check if node exists
// 2. If deleted, clean up OVN resources and release subnet
// 3. If new, allocate subnet and create OVN Logical Switch
// 4. If existing, ensure OVN resources are in sync
//
// Parameters:
//   - ctx: Context for cancellation
//   - req: Reconcile request containing node name
//
// Returns:
//   - ctrl.Result: Reconciliation result
//   - error: Reconciliation error
func (c *NodeController) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	klog.V(4).Infof("Reconciling Node %s", req.Name)

	// Fetch the Node
	node := &corev1.Node{}
	err := c.client.Get(ctx, req.NamespacedName, node)
	if err != nil {
		if errors.IsNotFound(err) {
			// Node was deleted, clean up resources
			return c.handleNodeDelete(ctx, req.Name)
		}
		klog.Errorf("Failed to get Node %s: %v", req.Name, err)
		return ctrl.Result{}, err
	}

	// Handle node create/update
	return c.handleNodeCreateOrUpdate(ctx, node)
}

// handleNodeDelete handles node deletion.
//
// Cleanup steps:
// 1. Delete the node's Logical Switch from OVN
// 2. Release the allocated subnet
// 3. Remove from internal tracking
func (c *NodeController) handleNodeDelete(ctx context.Context, nodeName string) (ctrl.Result, error) {
	klog.Infof("Handling deletion of Node %s", nodeName)

	c.mu.Lock()
	subnetCIDR, exists := c.nodeSubnets[nodeName]
	if exists {
		delete(c.nodeSubnets, nodeName)
	}
	c.mu.Unlock()

	// Delete the node's Logical Switch
	lsName := c.getNodeLogicalSwitchName(nodeName)
	if c.ovnClient != nil && c.ovnClient.IsConnected() {
		lsOps := ovndb.NewLogicalSwitchOps(c.ovnClient)
		if err := lsOps.DeleteLogicalSwitch(ctx, lsName); err != nil {
			if !ovndb.IsNotFound(err) {
				klog.Errorf("Failed to delete Logical Switch %s: %v", lsName, err)
				return ctrl.Result{}, err
			}
		}
		klog.Infof("Deleted Logical Switch %s for node %s", lsName, nodeName)
	}

	// Release the subnet
	if exists && subnetCIDR != "" {
		_, subnet, err := net.ParseCIDR(subnetCIDR)
		if err == nil {
			if err := c.clusterSubnetAllocator.ReleaseSubnet(subnet); err != nil {
				klog.Warningf("Failed to release subnet %s for node %s: %v", subnetCIDR, nodeName, err)
			} else {
				klog.Infof("Released subnet %s for node %s", subnetCIDR, nodeName)
			}
		}
	}

	return ctrl.Result{}, nil
}

// handleNodeCreateOrUpdate handles node creation or update.
//
// Steps:
// 1. Check if node already has a subnet annotation
// 2. If not, allocate a new subnet
// 3. Create or update the node's Logical Switch in OVN
// 4. Update node annotations
func (c *NodeController) handleNodeCreateOrUpdate(ctx context.Context, node *corev1.Node) (ctrl.Result, error) {
	klog.V(4).Infof("Handling create/update of Node %s", node.Name)

	// Check if node already has a subnet
	existingSubnet := node.Annotations[NodeSubnetAnnotation]
	var subnet *net.IPNet
	var err error

	if existingSubnet != "" {
		// Parse existing subnet
		_, subnet, err = net.ParseCIDR(existingSubnet)
		if err != nil {
			klog.Errorf("Invalid existing subnet annotation %s on node %s: %v", existingSubnet, node.Name, err)
			// Clear invalid annotation and allocate new subnet
			existingSubnet = ""
		} else {
			// Try to mark this subnet as allocated (in case controller restarted)
			if err := c.clusterSubnetAllocator.AllocateSpecificSubnet(subnet); err != nil {
				klog.V(4).Infof("Subnet %s already tracked for node %s", existingSubnet, node.Name)
			}
		}
	}

	if existingSubnet == "" {
		// Allocate a new subnet for this node
		subnet, err = c.clusterSubnetAllocator.AllocateSubnet()
		if err != nil {
			klog.Errorf("Failed to allocate subnet for node %s: %v", node.Name, err)
			c.recorder.Eventf(node, corev1.EventTypeWarning, "SubnetAllocationFailed",
				"Failed to allocate subnet: %v", err)
			return ctrl.Result{}, err
		}
		klog.Infof("Allocated subnet %s for node %s", subnet.String(), node.Name)
	}

	// Track the subnet
	c.mu.Lock()
	c.nodeSubnets[node.Name] = subnet.String()
	c.mu.Unlock()

	// Calculate gateway IP (first usable IP in subnet)
	gatewayIP := c.calculateGatewayIP(subnet)

	// Create or update the node's Logical Switch in OVN
	if err := c.ensureNodeLogicalSwitch(ctx, node, subnet, gatewayIP); err != nil {
		klog.Errorf("Failed to ensure Logical Switch for node %s: %v", node.Name, err)
		c.recorder.Eventf(node, corev1.EventTypeWarning, "LogicalSwitchFailed",
			"Failed to create/update Logical Switch: %v", err)
		return ctrl.Result{}, err
	}

	// Update node annotations
	if err := c.updateNodeAnnotations(ctx, node, subnet, gatewayIP); err != nil {
		klog.Errorf("Failed to update annotations for node %s: %v", node.Name, err)
		return ctrl.Result{}, err
	}

	klog.Infof("Successfully reconciled node %s with subnet %s", node.Name, subnet.String())
	return ctrl.Result{}, nil
}

// ensureNodeLogicalSwitch creates or updates the OVN Logical Switch for a node.
//
// The Logical Switch is named "node-<nodeName>" and contains:
// - subnet: The node's allocated subnet CIDR
// - exclude_ips: The gateway IP (reserved)
func (c *NodeController) ensureNodeLogicalSwitch(ctx context.Context, node *corev1.Node, subnet *net.IPNet, gatewayIP net.IP) error {
	if c.ovnClient == nil || !c.ovnClient.IsConnected() {
		klog.V(4).Infof("OVN client not connected, skipping Logical Switch creation for node %s", node.Name)
		return nil
	}

	lsName := c.getNodeLogicalSwitchName(node.Name)
	lsOps := ovndb.NewLogicalSwitchOps(c.ovnClient)

	// Prepare other_config
	otherConfig := map[string]string{
		"subnet":      subnet.String(),
		"exclude_ips": gatewayIP.String(),
	}

	// Prepare external_ids
	externalIDs := map[string]string{
		"k8s.io/node":    node.Name,
		"zstack.io/type": "node-switch",
	}

	// Create or update the Logical Switch
	ls := &ovndb.LogicalSwitch{
		Name:        lsName,
		OtherConfig: otherConfig,
		ExternalIDs: externalIDs,
	}

	if err := lsOps.CreateOrUpdateLogicalSwitch(ctx, ls); err != nil {
		return fmt.Errorf("failed to create/update Logical Switch %s: %w", lsName, err)
	}

	klog.V(4).Infof("Ensured Logical Switch %s for node %s", lsName, node.Name)
	return nil
}

// updateNodeAnnotations updates the node's network annotations.
func (c *NodeController) updateNodeAnnotations(ctx context.Context, node *corev1.Node, subnet *net.IPNet, gatewayIP net.IP) error {
	// Check if annotations need updating
	needsUpdate := false
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
		needsUpdate = true
	}

	lsName := c.getNodeLogicalSwitchName(node.Name)

	updates := map[string]string{
		NodeSubnetAnnotation:        subnet.String(),
		NodeGatewayAnnotation:       gatewayIP.String(),
		NodeLogicalSwitchAnnotation: lsName,
	}

	for key, value := range updates {
		if node.Annotations[key] != value {
			node.Annotations[key] = value
			needsUpdate = true
		}
	}

	if !needsUpdate {
		return nil
	}

	// Update the node
	if err := c.client.Update(ctx, node); err != nil {
		return fmt.Errorf("failed to update node annotations: %w", err)
	}

	klog.V(4).Infof("Updated annotations for node %s", node.Name)
	return nil
}

// getNodeLogicalSwitchName returns the OVN Logical Switch name for a node.
func (c *NodeController) getNodeLogicalSwitchName(nodeName string) string {
	return fmt.Sprintf("node-%s", nodeName)
}

// calculateGatewayIP calculates the gateway IP for a subnet.
// The gateway is the first usable IP in the subnet (network address + 1).
func (c *NodeController) calculateGatewayIP(subnet *net.IPNet) net.IP {
	ip := subnet.IP.To4()
	if ip == nil {
		ip = subnet.IP.To16()
	}

	// Copy the IP to avoid modifying the original
	gateway := make(net.IP, len(ip))
	copy(gateway, ip)

	// Increment to get first usable IP
	for i := len(gateway) - 1; i >= 0; i-- {
		gateway[i]++
		if gateway[i] != 0 {
			break
		}
	}

	return gateway
}

// GetNodeSubnet returns the allocated subnet for a node.
//
// Parameters:
//   - nodeName: Name of the node
//
// Returns:
//   - string: Subnet CIDR or empty string if not allocated
func (c *NodeController) GetNodeSubnet(nodeName string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nodeSubnets[nodeName]
}

// SyncExistingNodes synchronizes existing nodes on controller startup.
// This recovers the subnet allocation state from node annotations.
//
// Parameters:
//   - ctx: Context for cancellation
//
// Returns:
//   - error: Sync error
func (c *NodeController) SyncExistingNodes(ctx context.Context) error {
	klog.Info("Syncing existing nodes...")

	nodeList := &corev1.NodeList{}
	if err := c.client.List(ctx, nodeList); err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	for _, node := range nodeList.Items {
		subnetCIDR := node.Annotations[NodeSubnetAnnotation]
		if subnetCIDR == "" {
			continue
		}

		_, subnet, err := net.ParseCIDR(subnetCIDR)
		if err != nil {
			klog.Warningf("Invalid subnet annotation %s on node %s: %v", subnetCIDR, node.Name, err)
			continue
		}

		// Mark subnet as allocated
		if err := c.clusterSubnetAllocator.AllocateSpecificSubnet(subnet); err != nil {
			klog.V(4).Infof("Subnet %s already allocated for node %s", subnetCIDR, node.Name)
		}

		// Track in memory
		c.mu.Lock()
		c.nodeSubnets[node.Name] = subnetCIDR
		c.mu.Unlock()

		klog.V(4).Infof("Recovered subnet %s for node %s", subnetCIDR, node.Name)
	}

	klog.Infof("Synced %d existing nodes", len(nodeList.Items))
	return nil
}
