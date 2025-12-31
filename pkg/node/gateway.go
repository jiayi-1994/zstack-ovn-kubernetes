// Package node provides gateway configuration for external traffic.
//
// This file implements gateway configuration for Pod external access.
//
// Gateway Modes:
// - Shared Gateway: All nodes share a centralized gateway on specific nodes
//   - Traffic from all nodes is routed through gateway nodes
//   - Simpler configuration, but potential bottleneck
//   - Good for smaller clusters
//
// - Local Gateway: Each node has its own distributed gateway
//   - Traffic exits directly from each node
//   - Better performance and scalability
//   - More complex configuration
//
// Gateway Functions:
// - SNAT: Source NAT for Pod outbound traffic (Pod IP -> Node IP)
// - Default Route: Route external traffic through the gateway
// - External Bridge: Connect to physical network
//
// Reference: OVN-Kubernetes pkg/node/gateway_init_linux.go
package node

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"

	"k8s.io/klog/v2"

	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/types"
)

// GatewayMode represents the gateway deployment mode
type GatewayMode string

const (
	// GatewayModeShared uses a centralized gateway on specific nodes
	GatewayModeShared GatewayMode = "shared"

	// GatewayModeLocal uses a distributed gateway on each node
	GatewayModeLocal GatewayMode = "local"
)

// GatewayConfig holds the configuration for gateway setup
type GatewayConfig struct {
	// Mode is the gateway mode (shared or local)
	Mode GatewayMode

	// Interface is the physical interface for external traffic
	Interface string

	// NextHop is the next hop IP for external traffic
	// If empty, uses the default gateway
	NextHop net.IP

	// NodeIP is the node's external IP address
	NodeIP net.IP

	// VLANID is the VLAN ID for external traffic (optional)
	VLANID int

	// ClusterCIDR is the Pod network CIDR (for SNAT rules)
	ClusterCIDR *net.IPNet

	// ServiceCIDR is the Service network CIDR
	ServiceCIDR *net.IPNet
}

// GatewayController manages gateway configuration on a node.
//
// Responsibilities:
// - Configure external bridge (br-ex)
// - Set up SNAT rules for Pod outbound traffic
// - Configure default routes for external access
// - Manage gateway port on OVN Logical Router
type GatewayController struct {
	// config is the gateway configuration
	config *GatewayConfig

	// globalConfig is the global configuration
	globalConfig *config.Config

	// nodeName is the name of this node
	nodeName string

	// mu protects concurrent access
	mu sync.Mutex

	// configured indicates if gateway is configured
	configured bool
}

// NewGatewayController creates a new gateway controller.
//
// Parameters:
//   - cfg: Global configuration
//   - nodeName: Name of this node
//
// Returns:
//   - *GatewayController: Gateway controller instance
//   - error: Initialization error
func NewGatewayController(cfg *config.Config, nodeName string) (*GatewayController, error) {
	// Determine gateway mode
	mode := GatewayModeLocal
	if cfg.Gateway.Mode == string(GatewayModeShared) {
		mode = GatewayModeShared
	}

	// Parse cluster CIDR
	_, clusterCIDR, err := net.ParseCIDR(cfg.Network.ClusterCIDR)
	if err != nil {
		return nil, fmt.Errorf("invalid cluster CIDR: %w", err)
	}

	// Parse service CIDR
	_, serviceCIDR, err := net.ParseCIDR(cfg.Network.ServiceCIDR)
	if err != nil {
		return nil, fmt.Errorf("invalid service CIDR: %w", err)
	}

	// Parse next hop if specified
	var nextHop net.IP
	if cfg.Gateway.NextHop != "" {
		nextHop = net.ParseIP(cfg.Gateway.NextHop)
		if nextHop == nil {
			return nil, fmt.Errorf("invalid gateway next hop: %s", cfg.Gateway.NextHop)
		}
	}

	gatewayConfig := &GatewayConfig{
		Mode:        mode,
		Interface:   cfg.Gateway.Interface,
		NextHop:     nextHop,
		VLANID:      cfg.Gateway.VLANID,
		ClusterCIDR: clusterCIDR,
		ServiceCIDR: serviceCIDR,
	}

	return &GatewayController{
		config:       gatewayConfig,
		globalConfig: cfg,
		nodeName:     nodeName,
	}, nil
}

// Configure sets up the gateway configuration on this node.
//
// This method:
// 1. Detects the node's external IP if not specified
// 2. Creates the external bridge (br-ex) if needed
// 3. Configures SNAT rules for Pod outbound traffic
// 4. Sets up default routes
//
// Parameters:
//   - ctx: Context for cancellation
//
// Returns:
//   - error: Configuration error
func (g *GatewayController) Configure(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.configured {
		klog.V(4).Infof("Gateway already configured on node %s", g.nodeName)
		return nil
	}

	klog.Infof("Configuring %s gateway on node %s", g.config.Mode, g.nodeName)

	// Detect node IP if not specified
	if g.config.NodeIP == nil {
		nodeIP, err := g.detectNodeIP()
		if err != nil {
			return fmt.Errorf("failed to detect node IP: %w", err)
		}
		g.config.NodeIP = nodeIP
		klog.Infof("Detected node IP: %s", nodeIP)
	}

	// Detect next hop if not specified
	if g.config.NextHop == nil {
		nextHop, err := g.detectDefaultGateway()
		if err != nil {
			klog.Warningf("Failed to detect default gateway: %v", err)
		} else {
			g.config.NextHop = nextHop
			klog.Infof("Detected default gateway: %s", nextHop)
		}
	}

	// Configure based on gateway mode
	switch g.config.Mode {
	case GatewayModeLocal:
		if err := g.configureLocalGateway(); err != nil {
			return fmt.Errorf("failed to configure local gateway: %w", err)
		}
	case GatewayModeShared:
		if err := g.configureSharedGateway(); err != nil {
			return fmt.Errorf("failed to configure shared gateway: %w", err)
		}
	default:
		return fmt.Errorf("unknown gateway mode: %s", g.config.Mode)
	}

	// Configure SNAT rules
	if err := g.configureSNAT(); err != nil {
		return fmt.Errorf("failed to configure SNAT: %w", err)
	}

	g.configured = true
	klog.Infof("Successfully configured %s gateway on node %s", g.config.Mode, g.nodeName)

	return nil
}

// configureLocalGateway configures a local (distributed) gateway.
//
// In local gateway mode:
// - Each node has its own gateway
// - Traffic exits directly from each node
// - SNAT is performed on each node
func (g *GatewayController) configureLocalGateway() error {
	klog.V(4).Infof("Configuring local gateway on node %s", g.nodeName)

	// Ensure br-ex exists
	if err := g.ensureExternalBridge(); err != nil {
		return fmt.Errorf("failed to ensure external bridge: %w", err)
	}

	// Configure OVS external_ids for local gateway
	if err := g.ovsVsctl("set", "Open_vSwitch", ".",
		"external_ids:ovn-bridge-mappings=physnet1:"+types.BrEx); err != nil {
		return fmt.Errorf("failed to set bridge mappings: %w", err)
	}

	// Set gateway mode in OVS
	if err := g.ovsVsctl("set", "Open_vSwitch", ".",
		"external_ids:ovn-gateway-mode=local"); err != nil {
		klog.Warningf("Failed to set gateway mode: %v", err)
	}

	klog.V(4).Infof("Local gateway configured on node %s", g.nodeName)
	return nil
}

// configureSharedGateway configures a shared (centralized) gateway.
//
// In shared gateway mode:
// - Gateway is on specific nodes only
// - Traffic from all nodes is routed through gateway nodes
// - This node may or may not be a gateway node
func (g *GatewayController) configureSharedGateway() error {
	klog.V(4).Infof("Configuring shared gateway on node %s", g.nodeName)

	// Ensure br-ex exists
	if err := g.ensureExternalBridge(); err != nil {
		return fmt.Errorf("failed to ensure external bridge: %w", err)
	}

	// Configure OVS external_ids for shared gateway
	if err := g.ovsVsctl("set", "Open_vSwitch", ".",
		"external_ids:ovn-bridge-mappings=physnet1:"+types.BrEx); err != nil {
		return fmt.Errorf("failed to set bridge mappings: %w", err)
	}

	// Set gateway mode in OVS
	if err := g.ovsVsctl("set", "Open_vSwitch", ".",
		"external_ids:ovn-gateway-mode=shared"); err != nil {
		klog.Warningf("Failed to set gateway mode: %v", err)
	}

	klog.V(4).Infof("Shared gateway configured on node %s", g.nodeName)
	return nil
}

// ensureExternalBridge ensures the external bridge (br-ex) exists.
func (g *GatewayController) ensureExternalBridge() error {
	// Check if br-ex exists
	if err := exec.Command("ovs-vsctl", "br-exists", types.BrEx).Run(); err == nil {
		klog.V(4).Infof("External bridge %s already exists", types.BrEx)
		return nil
	}

	// Create br-ex
	if err := g.ovsVsctl("--may-exist", "add-br", types.BrEx); err != nil {
		return fmt.Errorf("failed to create external bridge: %w", err)
	}

	klog.Infof("Created external bridge %s", types.BrEx)

	// Add physical interface to br-ex if specified
	if g.config.Interface != "" {
		if err := g.addPhysicalInterface(); err != nil {
			klog.Warningf("Failed to add physical interface: %v", err)
		}
	}

	return nil
}

// addPhysicalInterface adds the physical interface to br-ex.
func (g *GatewayController) addPhysicalInterface() error {
	iface := g.config.Interface

	// Check if interface exists
	_, err := net.InterfaceByName(iface)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", iface, err)
	}

	// Add interface to br-ex
	if err := g.ovsVsctl("--may-exist", "add-port", types.BrEx, iface); err != nil {
		return fmt.Errorf("failed to add interface %s to %s: %w", iface, types.BrEx, err)
	}

	klog.Infof("Added interface %s to %s", iface, types.BrEx)
	return nil
}

// configureSNAT configures SNAT rules for Pod outbound traffic.
//
// SNAT (Source NAT) translates Pod IPs to the node's external IP
// for traffic going outside the cluster.
func (g *GatewayController) configureSNAT() error {
	if g.config.NodeIP == nil {
		return fmt.Errorf("node IP not configured")
	}

	// Use iptables for SNAT
	// Rule: Packets from cluster CIDR going to external network get SNAT'd to node IP

	// First, check if the rule already exists
	checkArgs := []string{
		"-t", "nat", "-C", "POSTROUTING",
		"-s", g.config.ClusterCIDR.String(),
		"!", "-d", g.config.ClusterCIDR.String(),
		"-j", "MASQUERADE",
	}

	if err := exec.Command("iptables", checkArgs...).Run(); err == nil {
		klog.V(4).Infof("SNAT rule already exists")
		return nil
	}

	// Add the SNAT rule
	addArgs := []string{
		"-t", "nat", "-A", "POSTROUTING",
		"-s", g.config.ClusterCIDR.String(),
		"!", "-d", g.config.ClusterCIDR.String(),
		"-j", "MASQUERADE",
	}

	if err := exec.Command("iptables", addArgs...).Run(); err != nil {
		return fmt.Errorf("failed to add SNAT rule: %w", err)
	}

	klog.Infof("Added SNAT rule for cluster CIDR %s", g.config.ClusterCIDR)

	// Also add rule to not SNAT traffic to service CIDR
	checkServiceArgs := []string{
		"-t", "nat", "-C", "POSTROUTING",
		"-s", g.config.ClusterCIDR.String(),
		"-d", g.config.ServiceCIDR.String(),
		"-j", "RETURN",
	}

	if err := exec.Command("iptables", checkServiceArgs...).Run(); err != nil {
		// Add the rule
		addServiceArgs := []string{
			"-t", "nat", "-I", "POSTROUTING",
			"-s", g.config.ClusterCIDR.String(),
			"-d", g.config.ServiceCIDR.String(),
			"-j", "RETURN",
		}

		if err := exec.Command("iptables", addServiceArgs...).Run(); err != nil {
			klog.Warningf("Failed to add service CIDR exception rule: %v", err)
		}
	}

	return nil
}

// detectNodeIP detects the node's external IP address.
func (g *GatewayController) detectNodeIP() (net.IP, error) {
	// If interface is specified, get its IP
	if g.config.Interface != "" {
		iface, err := net.InterfaceByName(g.config.Interface)
		if err == nil {
			addrs, err := iface.Addrs()
			if err == nil {
				for _, addr := range addrs {
					ipNet, ok := addr.(*net.IPNet)
					if ok && ipNet.IP.To4() != nil && !ipNet.IP.IsLoopback() {
						return ipNet.IP, nil
					}
				}
			}
		}
	}

	// Fall back to default route interface
	defaultIface, err := g.getDefaultRouteInterface()
	if err == nil {
		iface, err := net.InterfaceByName(defaultIface)
		if err == nil {
			addrs, err := iface.Addrs()
			if err == nil {
				for _, addr := range addrs {
					ipNet, ok := addr.(*net.IPNet)
					if ok && ipNet.IP.To4() != nil && !ipNet.IP.IsLoopback() {
						return ipNet.IP, nil
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("could not detect node IP")
}

// detectDefaultGateway detects the default gateway IP.
func (g *GatewayController) detectDefaultGateway() (net.IP, error) {
	output, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get default route: %w", err)
	}

	// Parse output: "default via 192.168.1.1 dev eth0 ..."
	fields := strings.Fields(string(output))
	for i, field := range fields {
		if field == "via" && i+1 < len(fields) {
			ip := net.ParseIP(fields[i+1])
			if ip != nil {
				return ip, nil
			}
		}
	}

	return nil, fmt.Errorf("could not parse default gateway")
}

// getDefaultRouteInterface returns the interface used for the default route.
func (g *GatewayController) getDefaultRouteInterface() (string, error) {
	output, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return "", fmt.Errorf("failed to get default route: %w", err)
	}

	fields := strings.Fields(string(output))
	for i, field := range fields {
		if field == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}

	return "", fmt.Errorf("could not parse default route interface")
}

// ovsVsctl executes an ovs-vsctl command.
func (g *GatewayController) ovsVsctl(args ...string) error {
	cmd := exec.Command("ovs-vsctl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ovs-vsctl %v failed: %w, output: %s", args, err, string(output))
	}
	return nil
}


// GetNodeIP returns the node's external IP address.
func (g *GatewayController) GetNodeIP() net.IP {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.config.NodeIP
}

// GetNextHop returns the configured next hop IP.
func (g *GatewayController) GetNextHop() net.IP {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.config.NextHop
}

// GetGatewayMode returns the configured gateway mode.
func (g *GatewayController) GetGatewayMode() GatewayMode {
	return g.config.Mode
}

// IsConfigured returns whether the gateway is configured.
func (g *GatewayController) IsConfigured() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.configured
}

// AddRoute adds a route through the gateway.
//
// Parameters:
//   - dest: Destination network CIDR
//   - via: Next hop IP (optional, uses default gateway if nil)
//
// Returns:
//   - error: Route addition error
func (g *GatewayController) AddRoute(dest *net.IPNet, via net.IP) error {
	args := []string{"route", "add", dest.String()}

	if via != nil {
		args = append(args, "via", via.String())
	} else if g.config.NextHop != nil {
		args = append(args, "via", g.config.NextHop.String())
	}

	if g.config.Interface != "" {
		args = append(args, "dev", g.config.Interface)
	}

	output, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		// Check if route already exists
		if strings.Contains(string(output), "File exists") {
			return nil
		}
		return fmt.Errorf("failed to add route: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("Added route to %s", dest)
	return nil
}

// DeleteRoute deletes a route.
//
// Parameters:
//   - dest: Destination network CIDR
//
// Returns:
//   - error: Route deletion error
func (g *GatewayController) DeleteRoute(dest *net.IPNet) error {
	args := []string{"route", "del", dest.String()}

	output, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		// Ignore if route doesn't exist
		if strings.Contains(string(output), "No such process") {
			return nil
		}
		return fmt.Errorf("failed to delete route: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("Deleted route to %s", dest)
	return nil
}

// ConfigureDefaultRoute configures the default route through the gateway.
//
// This is typically used in shared gateway mode where traffic needs
// to be routed through specific gateway nodes.
func (g *GatewayController) ConfigureDefaultRoute() error {
	if g.config.NextHop == nil {
		return fmt.Errorf("next hop not configured")
	}

	// Add default route via next hop
	args := []string{"route", "replace", "default", "via", g.config.NextHop.String()}

	if g.config.Interface != "" {
		args = append(args, "dev", g.config.Interface)
	}

	output, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to configure default route: %w, output: %s", err, string(output))
	}

	klog.Infof("Configured default route via %s", g.config.NextHop)
	return nil
}

// CleanupSNAT removes SNAT rules configured by this controller.
func (g *GatewayController) CleanupSNAT() error {
	// Remove MASQUERADE rule
	args := []string{
		"-t", "nat", "-D", "POSTROUTING",
		"-s", g.config.ClusterCIDR.String(),
		"!", "-d", g.config.ClusterCIDR.String(),
		"-j", "MASQUERADE",
	}

	if err := exec.Command("iptables", args...).Run(); err != nil {
		klog.V(4).Infof("SNAT rule not found or already removed")
	} else {
		klog.Infof("Removed SNAT rule for cluster CIDR %s", g.config.ClusterCIDR)
	}

	// Remove service CIDR exception rule
	serviceArgs := []string{
		"-t", "nat", "-D", "POSTROUTING",
		"-s", g.config.ClusterCIDR.String(),
		"-d", g.config.ServiceCIDR.String(),
		"-j", "RETURN",
	}

	if err := exec.Command("iptables", serviceArgs...).Run(); err != nil {
		klog.V(4).Infof("Service CIDR exception rule not found or already removed")
	}

	return nil
}

// ValidateGatewayConfig validates the gateway configuration.
//
// Returns:
//   - error: Validation error
func (g *GatewayController) ValidateGatewayConfig() error {
	if g.config.Mode != GatewayModeLocal && g.config.Mode != GatewayModeShared {
		return fmt.Errorf("invalid gateway mode: %s (must be 'local' or 'shared')", g.config.Mode)
	}

	if g.config.ClusterCIDR == nil {
		return fmt.Errorf("cluster CIDR is required")
	}

	if g.config.ServiceCIDR == nil {
		return fmt.Errorf("service CIDR is required")
	}

	// Validate interface exists if specified
	if g.config.Interface != "" {
		_, err := net.InterfaceByName(g.config.Interface)
		if err != nil {
			return fmt.Errorf("gateway interface %s not found: %w", g.config.Interface, err)
		}
	}

	return nil
}

// GatewayStatus represents the current status of the gateway.
type GatewayStatus struct {
	// Mode is the gateway mode
	Mode GatewayMode

	// NodeIP is the node's external IP
	NodeIP net.IP

	// NextHop is the next hop IP
	NextHop net.IP

	// Interface is the gateway interface
	Interface string

	// Configured indicates if the gateway is configured
	Configured bool

	// SNATEnabled indicates if SNAT is enabled
	SNATEnabled bool
}

// GetStatus returns the current gateway status.
func (g *GatewayController) GetStatus() *GatewayStatus {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Check if SNAT rule exists
	snatEnabled := false
	checkArgs := []string{
		"-t", "nat", "-C", "POSTROUTING",
		"-s", g.config.ClusterCIDR.String(),
		"!", "-d", g.config.ClusterCIDR.String(),
		"-j", "MASQUERADE",
	}
	if err := exec.Command("iptables", checkArgs...).Run(); err == nil {
		snatEnabled = true
	}

	return &GatewayStatus{
		Mode:        g.config.Mode,
		NodeIP:      g.config.NodeIP,
		NextHop:     g.config.NextHop,
		Interface:   g.config.Interface,
		Configured:  g.configured,
		SNATEnabled: snatEnabled,
	}
}
