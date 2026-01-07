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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	networkv1 "github.com/jiayi-1994/zstack-ovn-kubernetes/api/v1"
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
// - Create Cluster Router and Router Ports for L3 connectivity
// - Configure distributed gateway for external access
// - Configure tunnel endpoints for cross-node communication
// - Manage gateway configuration and NAT rules
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

	// lrOps provides Logical Router operations
	lrOps *ovndb.LogicalRouterOps

	// clusterSubnetAllocator allocates per-node subnets from cluster CIDR
	clusterSubnetAllocator *ClusterSubnetAllocator

	// recorder is the event recorder for generating Kubernetes events
	recorder record.EventRecorder

	// mu protects concurrent access to controller state
	mu sync.RWMutex

	// nodeSubnets tracks allocated subnets per node
	// Key: node name, Value: allocated subnet CIDR
	nodeSubnets map[string]string

	// clusterRouterInitialized tracks if cluster router has been created
	clusterRouterInitialized bool

	// joinNetworkInitialized tracks if join network has been created
	joinNetworkInitialized bool

	// gatewayNodeIP tracks the current gateway node's IP for SNAT
	gatewayNodeIP string

	// nextJoinIP tracks the next available IP on join network (100.64.0.x)
	nextJoinIP int
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

	var lrOps *ovndb.LogicalRouterOps
	if ovnClient != nil {
		lrOps = ovndb.NewLogicalRouterOps(ovnClient)
	}

	return &NodeController{
		client:                 client,
		kubeClient:             kubeClient,
		config:                 cfg,
		ovnClient:              ovnClient,
		lrOps:                  lrOps,
		clusterSubnetAllocator: clusterAllocator,
		recorder:               recorder,
		nodeSubnets:            make(map[string]string),
		nextJoinIP:             2, // Start from 100.64.0.2 (100.64.0.1 is router)
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
// In per-node subnet mode (default, ovn-kubernetes compatible):
// 1. Ensure cluster router exists
// 2. Ensure join network exists (for distributed gateway)
// 3. Allocate per-node subnet from cluster CIDR
// 4. Create per-node logical switch
// 5. Create router port connecting switch to cluster router
// 6. Configure distributed gateway for external access
// 7. Update node annotations
//
// In Subnet CRD mode (perNodeSubnet=false):
// - Skip per-node subnet allocation and switch creation
// - Only handle cluster router, join network, and gateway configuration
// - Subnets are managed via Subnet CRD controller
func (c *NodeController) handleNodeCreateOrUpdate(ctx context.Context, node *corev1.Node) (ctrl.Result, error) {
	klog.V(4).Infof("Handling create/update of Node %s", node.Name)

	// Ensure cluster router exists (only once)
	if err := c.ensureClusterRouter(ctx); err != nil {
		klog.Errorf("Failed to ensure cluster router: %v", err)
		return ctrl.Result{}, err
	}

	// Ensure join network exists for distributed gateway
	if err := c.ensureJoinNetwork(ctx); err != nil {
		klog.Errorf("Failed to ensure join network: %v", err)
		return ctrl.Result{}, err
	}

	// Check subnet mode - default to per-node subnet (ovn-kubernetes compatible)
	perNodeSubnet := true
	if c.config != nil {
		perNodeSubnet = c.config.Network.PerNodeSubnet
	}

	if perNodeSubnet {
		// Per-node subnet mode (default, ovn-kubernetes compatible)
		return c.handleNodeWithPerNodeSubnet(ctx, node)
	}

	// Subnet CRD mode - skip per-node subnet allocation
	// The Subnet CRD controller (subnet_controller.go) manages subnets and creates
	// the logical switch (e.g., "subnet-default") with the correct CIDR.

	// Configure distributed gateway for this node (external network access)
	if err := c.ensureDistributedGateway(ctx, node); err != nil {
		klog.Errorf("Failed to ensure distributed gateway for node %s: %v", node.Name, err)
		// Don't fail the reconciliation for gateway errors
		klog.Warningf("Distributed gateway configuration failed, external access may not work: %v", err)
	}

	klog.Infof("Successfully reconciled node %s (Subnet CRD mode)", node.Name)
	return ctrl.Result{}, nil
}

// handleNodeWithPerNodeSubnet handles node with per-node subnet allocation.
// This is the default mode, compatible with ovn-kubernetes per-node subnet architecture.
// Each node gets its own /24 (or configured size) subnet from the cluster CIDR.
func (c *NodeController) handleNodeWithPerNodeSubnet(ctx context.Context, node *corev1.Node) (ctrl.Result, error) {
	klog.V(4).Infof("Handling node %s with per-node subnet mode", node.Name)

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

	// Create or update Subnet CRD for this node
	// This allows Pod Controller to find the subnet and allocate IPs
	if err := c.ensureNodeSubnetCRD(ctx, node, subnet, gatewayIP); err != nil {
		klog.Errorf("Failed to ensure Subnet CRD for node %s: %v", node.Name, err)
		c.recorder.Eventf(node, corev1.EventTypeWarning, "SubnetCRDFailed",
			"Failed to create/update Subnet CRD: %v", err)
		return ctrl.Result{}, err
	}

	// Create Router Port connecting switch to cluster router
	if err := c.ensureRouterPort(ctx, node, subnet, gatewayIP); err != nil {
		klog.Errorf("Failed to ensure Router Port for node %s: %v", node.Name, err)
		c.recorder.Eventf(node, corev1.EventTypeWarning, "RouterPortFailed",
			"Failed to create/update Router Port: %v", err)
		return ctrl.Result{}, err
	}

	// Configure distributed gateway for this node
	if err := c.ensureDistributedGateway(ctx, node); err != nil {
		klog.Errorf("Failed to ensure distributed gateway for node %s: %v", node.Name, err)
		// Don't fail the reconciliation for gateway errors
		klog.Warningf("Distributed gateway configuration failed, external access may not work: %v", err)
	}

	// Update node annotations
	if err := c.updateNodeAnnotations(ctx, node, subnet, gatewayIP); err != nil {
		klog.Errorf("Failed to update annotations for node %s: %v", node.Name, err)
		return ctrl.Result{}, err
	}

	klog.Infof("Successfully reconciled node %s with subnet %s (per-node subnet mode)", node.Name, subnet.String())
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
// This recovers the subnet allocation state from node annotations and
// cleans up stale OVN resources for nodes that no longer exist.
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

	// Build set of valid node names
	validNodes := make(map[string]bool)
	for _, node := range nodeList.Items {
		validNodes[node.Name] = true

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

	// Clean up stale OVN resources for nodes that no longer exist
	if err := c.cleanupStaleNodeResources(ctx, validNodes); err != nil {
		klog.Warningf("Failed to cleanup stale node resources: %v", err)
		// Don't fail the sync, continue with normal operation
	}

	klog.Infof("Synced %d existing nodes", len(nodeList.Items))
	return nil
}

// cleanupStaleNodeResources cleans up OVN resources for nodes that no longer exist.
func (c *NodeController) cleanupStaleNodeResources(ctx context.Context, validNodes map[string]bool) error {
	if c.ovnClient == nil || !c.ovnClient.IsConnected() {
		klog.V(4).Info("OVN client not connected, skipping stale resource cleanup")
		return nil
	}

	lsOps := ovndb.NewLogicalSwitchOps(c.ovnClient)

	// Get all logical switches from OVN
	switches, err := lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return fmt.Errorf("failed to list logical switches: %w", err)
	}

	// Find and clean up stale node switches
	cleanedCount := 0
	for _, ls := range switches {
		// Check if this is a node switch (format: node-<nodeName>)
		if len(ls.Name) <= 5 || ls.Name[:5] != "node-" {
			continue
		}

		nodeName := ls.Name[5:] // Remove "node-" prefix
		if nodeName == "" {
			continue
		}

		// Check if node still exists
		if validNodes[nodeName] {
			continue
		}

		klog.Infof("Cleaning up stale node switch: %s (node %s no longer exists)", ls.Name, nodeName)

		// Delete router port connecting to this switch
		if c.lrOps != nil {
			if err := c.lrOps.DeleteRouterPort(ctx, ls.Name); err != nil {
				if !ovndb.IsNotFound(err) {
					klog.Warningf("Failed to delete router port for node %s: %v", nodeName, err)
				}
			}
		}

		// Delete the node's logical switch
		if err := lsOps.DeleteLogicalSwitch(ctx, ls.Name); err != nil {
			if !ovndb.IsNotFound(err) {
				klog.Warningf("Failed to delete logical switch %s: %v", ls.Name, err)
				continue
			}
		}

		// Delete external switch if exists
		extSwitchName := ovndb.GetExternalSwitchName(nodeName)
		if err := lsOps.DeleteLogicalSwitch(ctx, extSwitchName); err != nil {
			if !ovndb.IsNotFound(err) {
				klog.V(4).Infof("Failed to delete external switch %s: %v", extSwitchName, err)
			}
		}

		cleanedCount++
		klog.Infof("Cleaned up OVN resources for stale node %s", nodeName)
	}

	if cleanedCount > 0 {
		klog.Infof("Cleaned up %d stale node resources", cleanedCount)
	}

	return nil
}

// ensureClusterRouter ensures the cluster-wide logical router exists.
// This is called once during the first node reconciliation.
func (c *NodeController) ensureClusterRouter(ctx context.Context) error {
	c.mu.Lock()
	if c.clusterRouterInitialized {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	if c.ovnClient == nil || !c.ovnClient.IsConnected() {
		klog.V(4).Info("OVN client not connected, skipping cluster router creation")
		return nil
	}

	if c.lrOps == nil {
		c.lrOps = ovndb.NewLogicalRouterOps(c.ovnClient)
	}

	_, err := c.lrOps.CreateClusterRouter(ctx)
	if err != nil {
		return fmt.Errorf("failed to create cluster router: %w", err)
	}

	c.mu.Lock()
	c.clusterRouterInitialized = true
	c.mu.Unlock()

	klog.Info("Cluster router initialized")
	return nil
}

// ensureJoinNetwork creates the join switch and router port for distributed gateway.
// The join network (100.64.0.0/16) connects the cluster router to gateway chassis.
func (c *NodeController) ensureJoinNetwork(ctx context.Context) error {
	c.mu.Lock()
	if c.joinNetworkInitialized {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	if c.ovnClient == nil || !c.ovnClient.IsConnected() {
		klog.V(4).Info("OVN client not connected, skipping join network creation")
		return nil
	}

	if c.lrOps == nil {
		return fmt.Errorf("logical router ops not initialized")
	}

	// Create join switch and router port in a single transaction
	// This avoids cache sync issues between separate transactions
	_, err := c.lrOps.EnsureJoinSwitchAndRouterPort(ctx)
	if err != nil {
		return fmt.Errorf("failed to create join network: %w", err)
	}

	c.mu.Lock()
	c.joinNetworkInitialized = true
	c.mu.Unlock()

	klog.Info("Join network initialized for distributed gateway")
	return nil
}

// ensureDistributedGateway configures the distributed gateway for a node.
// This enables pods to access external networks through SNAT using the node's physical IP.
//
// Architecture (simplified distributed gateway):
//
//	Pod -> subnet-switch -> ovn-cluster-router (SNAT to nodeIP) -> external-switch -> br-ex -> physical network
//
// This function automatically:
// 1. Detects the default gateway interface and IP (if not configured)
// 2. Configures OVS bridge mappings
// 3. Creates external switch with localnet port
// 4. Adds SNAT rules for the cluster CIDR
// 5. Adds default route via external gateway
//
// This implementation follows ovn-kubernetes patterns:
// - Node agent detects gateway config using netlink
// - Config is saved to Node annotation (L3GatewayConfig)
// - Controller reads annotation and configures OVN
//
// Reference: ovn-kubernetes pkg/node/gateway_init.go and pkg/node/gateway.go
func (c *NodeController) ensureDistributedGateway(ctx context.Context, node *corev1.Node) error {
	if c.ovnClient == nil || !c.ovnClient.IsConnected() {
		klog.V(4).Infof("OVN client not connected, skipping gateway configuration for node %s", node.Name)
		return nil
	}

	if c.lrOps == nil {
		return fmt.Errorf("logical router ops not initialized")
	}

	// Try to get L3 gateway config from node annotation first (ovn-kubernetes style)
	l3GwConfig, err := ParseL3GatewayAnnotation(node)
	if err != nil {
		klog.V(4).Infof("No L3 gateway annotation on node %s, will auto-detect: %v", node.Name, err)
		// Auto-detect and save to annotation
		l3GwConfig, err = c.detectAndSaveGatewayConfig(ctx, node)
		if err != nil {
			return fmt.Errorf("failed to detect gateway config: %w", err)
		}
	}

	// Get node IP from annotation or detect
	var nodeIP string
	if len(l3GwConfig.IPAddresses) > 0 {
		// Parse IP from CIDR format
		ip, _, err := net.ParseCIDR(l3GwConfig.IPAddresses[0])
		if err == nil {
			nodeIP = ip.String()
		}
	}
	if nodeIP == "" {
		nodeIP = c.getNodeInternalIP(node)
	}
	if nodeIP == "" {
		klog.Warningf("No IP found for node %s, skipping gateway configuration", node.Name)
		return nil
	}

	// Get next hop from annotation
	var nextHop string
	if len(l3GwConfig.NextHops) > 0 {
		nextHop = l3GwConfig.NextHops[0]
	}

	// Get physical network name
	physicalNetwork := c.config.Gateway.Interface
	if physicalNetwork == "" {
		physicalNetwork = "external"
	}

	// Set up default route and SNAT (only for first node / gateway node)
	c.mu.Lock()
	isFirstGateway := c.gatewayNodeIP == ""
	if isFirstGateway {
		c.gatewayNodeIP = nodeIP
	}
	c.mu.Unlock()

	if isFirstGateway {
		// Add SNAT rule for cluster CIDR
		if err := c.lrOps.EnsureSNATForCluster(ctx, nodeIP, c.config.Network.ClusterCIDR); err != nil {
			return fmt.Errorf("failed to add cluster SNAT: %w", err)
		}
		klog.Infof("Added SNAT rule: %s -> %s", c.config.Network.ClusterCIDR, nodeIP)

		// Configure external network connectivity
		if nextHop != "" {
			if err := c.ensureExternalConnectivity(ctx, node, nodeIP, nextHop, physicalNetwork); err != nil {
				klog.Warningf("Failed to configure external connectivity for node %s: %v", node.Name, err)
				// Fall back to join network routing
				joinRouterIP := "100.64.0.1"
				if err := c.lrOps.EnsureDefaultRoute(ctx, joinRouterIP); err != nil {
					klog.Warningf("Failed to add default route via join network: %v", err)
				}
			}
		} else {
			klog.Warningf("No next hop configured for node %s, using join network for routing", node.Name)
			joinRouterIP := "100.64.0.1"
			if err := c.lrOps.EnsureDefaultRoute(ctx, joinRouterIP); err != nil {
				klog.Warningf("Failed to add default route via join network: %v", err)
			}
		}

		klog.Infof("Node %s configured as primary gateway with SNAT IP %s", node.Name, nodeIP)
	} else {
		klog.V(4).Infof("Node %s is not the primary gateway, skipping SNAT configuration", node.Name)
	}

	// Setup OpenFlow rules on br-ex for proper return traffic handling
	// This is critical for making external connectivity work in shared gateway mode
	bridgeName := l3GwConfig.BridgeID
	if bridgeName == "" {
		bridgeName = "br-ex"
	}
	if err := SetupOpenFlowRules(node.Name, bridgeName, nodeIP, c.config.Network.ClusterCIDR); err != nil {
		klog.Warningf("Failed to setup OpenFlow rules on %s for node %s: %v", bridgeName, node.Name, err)
		// Don't fail - OpenFlow rules can be set up manually or retried later
	} else {
		klog.Infof("OpenFlow rules configured on %s for node %s", bridgeName, node.Name)
	}

	return nil
}

// detectAndSaveGatewayConfig auto-detects gateway configuration and saves it to node annotation.
// This follows the ovn-kubernetes pattern where node agent detects config and saves to annotation.
//
// Important: This does NOT modify the physical network interface. Traffic flows through
// host routing: Pod -> OVN SNAT -> host routing table -> physical interface -> external network
func (c *NodeController) detectAndSaveGatewayConfig(ctx context.Context, node *corev1.Node) (*L3GatewayConfig, error) {
	var gwInfo *GatewayInfo
	var err error

	physicalNetwork := c.config.Gateway.Interface
	if physicalNetwork == "" {
		physicalNetwork = "external"
	}

	// Check if gateway is explicitly configured
	if c.config.Gateway.NextHop != "" {
		// Use configured values
		gwInfo = &GatewayInfo{
			GatewayIP: net.ParseIP(c.config.Gateway.NextHop),
		}
		// Get node IP from Kubernetes node status
		nodeIP := c.getNodeInternalIP(node)
		if nodeIP != "" {
			gwInfo.NodeIP = net.ParseIP(nodeIP)
		}
		klog.V(4).Infof("Using configured gateway: nextHop=%s", c.config.Gateway.NextHop)
	} else {
		// Auto-detect gateway using netlink (does NOT modify physical interface)
		klog.Infof("Auto-detecting gateway for node %s using netlink", node.Name)

		gwInfo, err = AutoConfigureGateway(physicalNetwork)
		if err != nil {
			// Fall back to simple detection without bridge setup
			klog.Warningf("Failed to auto-configure gateway: %v, trying simple detection", err)
			gwInfo, err = DetectGatewayInfo()
			if err != nil {
				return nil, fmt.Errorf("failed to detect gateway info: %w", err)
			}
		}

		klog.Infof("Detected gateway for node %s: interface=%s, gateway=%s, nodeIP=%s",
			node.Name, gwInfo.InterfaceName, gwInfo.GatewayIP, gwInfo.NodeIP)
	}

	// Get chassis ID
	chassisID, err := GetChassisID()
	if err != nil {
		klog.Warningf("Failed to get chassis ID: %v", err)
		chassisID = ""
	}

	// Build L3GatewayConfig
	l3GwConfig := BuildL3GatewayConfig(gwInfo, chassisID, c.config.Gateway.VLANID)

	// Save to node annotation
	nodeCopy := node.DeepCopy()
	if err := SetL3GatewayAnnotation(nodeCopy, l3GwConfig); err != nil {
		return nil, fmt.Errorf("failed to set L3 gateway annotation: %w", err)
	}

	// Update node
	if err := c.client.Update(ctx, nodeCopy); err != nil {
		klog.Warningf("Failed to update node %s with L3 gateway annotation: %v", node.Name, err)
		// Don't fail - we can still use the detected config
	} else {
		klog.Infof("Saved L3 gateway config to node %s annotation", node.Name)
	}

	return l3GwConfig, nil
}

// ensureExternalConnectivity configures the node's OVS bridge for external traffic.
// This sets up the necessary OVN resources to allow traffic to reach the physical network.
//
// Architecture:
//
//	cluster-router -> external-switch (ext_<node>) -> localnet port -> br-ex -> physical network
//
// This creates (in a single transaction to avoid cache sync issues):
// 1. External switch with localnet port connecting to physical network
// 2. Router port on cluster router connecting to external switch
// 3. Default route via external gateway
func (c *NodeController) ensureExternalConnectivity(ctx context.Context, node *corev1.Node, nodeIP, nextHop, physicalNetwork string) error {
	if c.lrOps == nil {
		return fmt.Errorf("logical router ops not initialized")
	}

	// Use provided physical network name or default
	if physicalNetwork == "" {
		physicalNetwork = "external"
	}

	// Get VLAN ID from config (0 for untagged)
	vlanID := c.config.Gateway.VLANID

	// Generate MAC address for gateway port
	gatewayMAC := c.generateRouterPortMAC(net.ParseIP(nodeIP))

	// Use single transaction method to avoid cache sync issues between
	// creating external switch and connecting it to the router
	if err := c.lrOps.EnsureExternalConnectivityInSingleTx(
		ctx,
		node.Name,
		nodeIP,
		gatewayMAC,
		nextHop,
		physicalNetwork,
		vlanID,
	); err != nil {
		return fmt.Errorf("failed to configure external connectivity: %w", err)
	}

	klog.Infof("External connectivity configured for node %s: nodeIP=%s, nextHop=%s, physicalNetwork=%s",
		node.Name, nodeIP, nextHop, physicalNetwork)

	return nil
}

// ensureRouterPort creates or updates the router port connecting a node's switch to the cluster router.
func (c *NodeController) ensureRouterPort(ctx context.Context, node *corev1.Node, subnet *net.IPNet, gatewayIP net.IP) error {
	if c.ovnClient == nil || !c.ovnClient.IsConnected() {
		klog.V(4).Infof("OVN client not connected, skipping router port creation for node %s", node.Name)
		return nil
	}

	if c.lrOps == nil {
		return fmt.Errorf("logical router ops not initialized")
	}

	switchName := c.getNodeLogicalSwitchName(node.Name)

	// Generate MAC address for router port based on gateway IP
	// Format: 0a:58:xx:xx:xx:xx where xx:xx:xx:xx is the gateway IP
	mac := c.generateRouterPortMAC(gatewayIP)

	// Network in CIDR format: gatewayIP/prefixLen
	ones, _ := subnet.Mask.Size()
	network := fmt.Sprintf("%s/%d", gatewayIP.String(), ones)

	klog.V(4).Infof("Ensuring router port for node %s: switch=%s, network=%s, mac=%s",
		node.Name, switchName, network, mac)

	if err := c.lrOps.EnsureRouterPortToSwitch(ctx, switchName, gatewayIP.String(), subnet.String(), mac); err != nil {
		return fmt.Errorf("failed to ensure router port: %w", err)
	}

	klog.Infof("Ensured router port for node %s connecting to cluster router", node.Name)
	return nil
}

// generateRouterPortMAC generates a MAC address for a router port based on the gateway IP.
// Format: 0a:58:xx:xx:xx:xx where xx:xx:xx:xx is derived from the IP
func (c *NodeController) generateRouterPortMAC(ip net.IP) string {
	ip4 := ip.To4()
	if ip4 == nil {
		// For IPv6, use a different scheme
		return fmt.Sprintf("0a:58:00:00:00:01")
	}
	return fmt.Sprintf("0a:58:%02x:%02x:%02x:%02x", ip4[0], ip4[1], ip4[2], ip4[3])
}

// getNodeInternalIP returns the internal IP address of a node.
func (c *NodeController) getNodeInternalIP(node *corev1.Node) string {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}

// isDuplicateError checks if an error indicates a duplicate entry.
func isDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return contains(errStr, "duplicate") || contains(errStr, "already exists") || contains(errStr, "constraint violation")
}

// contains checks if a string contains a substring (case-insensitive).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && containsLower(s, substr)))
}

func containsLower(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if matchLower(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

func matchLower(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// ensureNodeSubnetCRD creates or updates a Subnet CRD for a node.
// This allows the Pod Controller to find the subnet and allocate IPs for pods on this node.
//
// The Subnet CRD is named "node-<nodeName>". In per-node subnet mode, the Node Controller
// already creates the OVN Logical Switch, so we DON'T set ExternalLogicalSwitch here.
// Instead, the Subnet Controller will skip creating a new switch because one with the
// matching name already exists (handled by CreateOrUpdateLogicalSwitch).
func (c *NodeController) ensureNodeSubnetCRD(ctx context.Context, node *corev1.Node, subnet *net.IPNet, gatewayIP net.IP) error {
	subnetName := fmt.Sprintf("node-%s", node.Name)

	// Build the Subnet CRD
	// NOTE: We do NOT set ExternalLogicalSwitch because:
	// 1. Node Controller already created the OVN switch "node-<nodeName>"
	// 2. Setting ExternalLogicalSwitch would trigger verifyExternalLogicalSwitch which may fail
	//    due to libovsdb cache sync delays
	// 3. Subnet Controller's ensureLogicalSwitch uses CreateOrUpdateLogicalSwitch which is idempotent
	//    and will just update the existing switch
	subnetCRD := &networkv1.Subnet{
		ObjectMeta: metav1.ObjectMeta{
			Name: subnetName,
			Labels: map[string]string{
				"zstack.io/node":        node.Name,
				"zstack.io/subnet-type": "per-node",
			},
		},
		Spec: networkv1.SubnetSpec{
			CIDR:       subnet.String(),
			Gateway:    gatewayIP.String(),
			ExcludeIPs: []string{gatewayIP.String()},
			// Don't set ExternalLogicalSwitch - let Subnet Controller handle it
			Protocol: networkv1.SubnetProtocolIPv4,
			Default:  false, // Per-node subnets are not default
		},
	}

	// Try to get existing subnet
	existing := &networkv1.Subnet{}
	err := c.client.Get(ctx, client.ObjectKey{Name: subnetName}, existing)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create new subnet
			if err := c.client.Create(ctx, subnetCRD); err != nil {
				if errors.IsAlreadyExists(err) {
					klog.V(4).Infof("Subnet CRD %s already exists", subnetName)
					return nil
				}
				return fmt.Errorf("failed to create Subnet CRD %s: %w", subnetName, err)
			}
			klog.Infof("Created Subnet CRD %s for node %s", subnetName, node.Name)
			return nil
		}
		return fmt.Errorf("failed to get Subnet CRD %s: %w", subnetName, err)
	}

	// Update existing subnet if needed
	needsUpdate := false
	if existing.Spec.CIDR != subnetCRD.Spec.CIDR {
		existing.Spec.CIDR = subnetCRD.Spec.CIDR
		needsUpdate = true
	}
	if existing.Spec.Gateway != subnetCRD.Spec.Gateway {
		existing.Spec.Gateway = subnetCRD.Spec.Gateway
		needsUpdate = true
	}
	if existing.Spec.ExternalLogicalSwitch != subnetCRD.Spec.ExternalLogicalSwitch {
		existing.Spec.ExternalLogicalSwitch = subnetCRD.Spec.ExternalLogicalSwitch
		needsUpdate = true
	}

	if needsUpdate {
		if err := c.client.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update Subnet CRD %s: %w", subnetName, err)
		}
		klog.Infof("Updated Subnet CRD %s for node %s", subnetName, node.Name)
	}

	return nil
}
