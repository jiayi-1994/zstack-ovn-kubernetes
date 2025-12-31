// Package node provides tunnel configuration for cross-node communication.
//
// This file implements VXLAN and Geneve tunnel configuration for OVN/OVS.
//
// Tunnel Architecture:
// - Each node has a tunnel endpoint (VTEP) for encapsulating cross-node traffic
// - VXLAN is the default tunnel type for ZStack compatibility
// - Geneve is supported as an alternative with more extensibility
//
// OVS Tunnel Configuration:
// - Tunnels are created as OVS ports on br-int
// - Each tunnel port connects to remote nodes
// - OVN manages tunnel creation automatically via ovn-controller
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

// TunnelType represents the type of tunnel encapsulation
type TunnelType string

const (
	// TunnelTypeVXLAN uses VXLAN encapsulation (default for ZStack compatibility)
	TunnelTypeVXLAN TunnelType = "vxlan"

	// TunnelTypeGeneve uses Geneve encapsulation (more extensible)
	TunnelTypeGeneve TunnelType = "geneve"
)

// Default tunnel configuration values
const (
	// DefaultVXLANPort is the default UDP port for VXLAN traffic
	DefaultVXLANPort = 4789

	// DefaultGenevePort is the default UDP port for Geneve traffic
	DefaultGenevePort = 6081

	// DefaultVNI is the default Virtual Network Identifier
	// OVN typically manages VNIs automatically
	DefaultVNI = 0

	// OVSTunnelInterface is the name of the tunnel interface in OVS
	OVSTunnelInterface = "ovn-tunnel"
)

// TunnelConfig holds the configuration for tunnel setup
type TunnelConfig struct {
	// Type is the tunnel encapsulation type (vxlan or geneve)
	Type TunnelType

	// LocalIP is the local tunnel endpoint IP address
	// This is typically the node's primary IP address
	LocalIP net.IP

	// Port is the UDP port for tunnel traffic
	Port int

	// VNI is the Virtual Network Identifier (optional, OVN manages this)
	VNI int

	// Interface is the physical interface for tunnel traffic
	// If empty, the default route interface is used
	Interface string
}

// TunnelController manages tunnel configuration on a node.
//
// Responsibilities:
// - Configure OVS tunnel ports
// - Set up encapsulation type and parameters
// - Manage tunnel endpoint IP
// - Configure OVN chassis encapsulation
type TunnelController struct {
	// config is the tunnel configuration
	config *TunnelConfig

	// globalConfig is the global configuration
	globalConfig *config.Config

	// nodeName is the name of this node
	nodeName string

	// mu protects concurrent access
	mu sync.Mutex

	// configured indicates if tunnels are configured
	configured bool
}

// NewTunnelController creates a new tunnel controller.
//
// Parameters:
//   - cfg: Global configuration
//   - nodeName: Name of this node
//
// Returns:
//   - *TunnelController: Tunnel controller instance
//   - error: Initialization error
func NewTunnelController(cfg *config.Config, nodeName string) (*TunnelController, error) {
	// Determine tunnel type
	tunnelType := TunnelTypeVXLAN
	if cfg.Tunnel.Type == string(TunnelTypeGeneve) {
		tunnelType = TunnelTypeGeneve
	}

	// Determine tunnel port
	port := cfg.Tunnel.Port
	if port == 0 {
		if tunnelType == TunnelTypeVXLAN {
			port = DefaultVXLANPort
		} else {
			port = DefaultGenevePort
		}
	}

	// Determine local IP
	var localIP net.IP
	if cfg.Tunnel.EncapIP != "" {
		localIP = net.ParseIP(cfg.Tunnel.EncapIP)
		if localIP == nil {
			return nil, fmt.Errorf("invalid tunnel encap IP: %s", cfg.Tunnel.EncapIP)
		}
	}

	tunnelConfig := &TunnelConfig{
		Type:      tunnelType,
		LocalIP:   localIP,
		Port:      port,
		VNI:       DefaultVNI,
		Interface: cfg.Gateway.Interface,
	}

	return &TunnelController{
		config:       tunnelConfig,
		globalConfig: cfg,
		nodeName:     nodeName,
	}, nil
}

// Configure sets up the tunnel configuration on this node.
//
// This method:
// 1. Detects the local tunnel endpoint IP if not specified
// 2. Configures OVS encapsulation type
// 3. Sets up the tunnel port on br-int
// 4. Configures OVN chassis encapsulation
//
// Parameters:
//   - ctx: Context for cancellation
//
// Returns:
//   - error: Configuration error
func (t *TunnelController) Configure(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.configured {
		klog.V(4).Infof("Tunnels already configured on node %s", t.nodeName)
		return nil
	}

	klog.Infof("Configuring %s tunnels on node %s", t.config.Type, t.nodeName)

	// Detect local IP if not specified
	if t.config.LocalIP == nil {
		localIP, err := t.detectLocalIP()
		if err != nil {
			return fmt.Errorf("failed to detect local IP: %w", err)
		}
		t.config.LocalIP = localIP
		klog.Infof("Detected local tunnel IP: %s", localIP)
	}

	// Configure OVS encapsulation
	if err := t.configureOVSEncapsulation(); err != nil {
		return fmt.Errorf("failed to configure OVS encapsulation: %w", err)
	}

	// Configure OVN chassis encapsulation
	if err := t.configureOVNChassisEncapsulation(); err != nil {
		return fmt.Errorf("failed to configure OVN chassis encapsulation: %w", err)
	}

	t.configured = true
	klog.Infof("Successfully configured %s tunnels on node %s with IP %s",
		t.config.Type, t.nodeName, t.config.LocalIP)

	return nil
}

// detectLocalIP detects the local IP address for tunnel encapsulation.
//
// Detection order:
// 1. IP of the specified interface (if configured)
// 2. IP of the default route interface
// 3. First non-loopback IPv4 address
func (t *TunnelController) detectLocalIP() (net.IP, error) {
	// If interface is specified, get its IP
	if t.config.Interface != "" {
		ip, err := t.getInterfaceIP(t.config.Interface)
		if err == nil {
			return ip, nil
		}
		klog.Warningf("Failed to get IP for interface %s: %v, trying default route", t.config.Interface, err)
	}

	// Try to get IP from default route interface
	defaultIface, err := t.getDefaultRouteInterface()
	if err == nil {
		ip, err := t.getInterfaceIP(defaultIface)
		if err == nil {
			return ip, nil
		}
		klog.Warningf("Failed to get IP for default interface %s: %v", defaultIface, err)
	}

	// Fall back to first non-loopback IPv4 address
	return t.getFirstNonLoopbackIP()
}

// getInterfaceIP returns the IPv4 address of a network interface.
func (t *TunnelController) getInterfaceIP(ifaceName string) (net.IP, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %w", ifaceName, err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("failed to get addresses for %s: %w", ifaceName, err)
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}

		ip := ipNet.IP.To4()
		if ip != nil && !ip.IsLoopback() {
			return ip, nil
		}
	}

	return nil, fmt.Errorf("no IPv4 address found on interface %s", ifaceName)
}

// getDefaultRouteInterface returns the interface used for the default route.
func (t *TunnelController) getDefaultRouteInterface() (string, error) {
	// Use 'ip route' to find the default route interface
	output, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return "", fmt.Errorf("failed to get default route: %w", err)
	}

	// Parse output: "default via 192.168.1.1 dev eth0 ..."
	fields := strings.Fields(string(output))
	for i, field := range fields {
		if field == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}

	return "", fmt.Errorf("could not parse default route interface")
}

// getFirstNonLoopbackIP returns the first non-loopback IPv4 address.
func (t *TunnelController) getFirstNonLoopbackIP() (net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to list interfaces: %w", err)
	}

	for _, iface := range ifaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			ip := ipNet.IP.To4()
			if ip != nil && !ip.IsLoopback() {
				return ip, nil
			}
		}
	}

	return nil, fmt.Errorf("no non-loopback IPv4 address found")
}

// configureOVSEncapsulation configures OVS for tunnel encapsulation.
//
// This sets up:
// - Encapsulation type (vxlan or geneve)
// - Encapsulation IP
// - Tunnel port on br-int
func (t *TunnelController) configureOVSEncapsulation() error {
	// Set encapsulation type in Open_vSwitch table
	encapType := string(t.config.Type)
	if err := t.ovsVsctl("set", "Open_vSwitch", ".",
		fmt.Sprintf("external_ids:ovn-encap-type=%s", encapType)); err != nil {
		return fmt.Errorf("failed to set encap type: %w", err)
	}

	// Set encapsulation IP
	if err := t.ovsVsctl("set", "Open_vSwitch", ".",
		fmt.Sprintf("external_ids:ovn-encap-ip=%s", t.config.LocalIP)); err != nil {
		return fmt.Errorf("failed to set encap IP: %w", err)
	}

	klog.V(4).Infof("Configured OVS encapsulation: type=%s, ip=%s", encapType, t.config.LocalIP)
	return nil
}

// configureOVNChassisEncapsulation configures OVN chassis encapsulation settings.
//
// This ensures ovn-controller knows how to set up tunnels to other nodes.
func (t *TunnelController) configureOVNChassisEncapsulation() error {
	// Set the remote OVN Southbound DB address if in external mode
	if t.globalConfig.IsExternalMode() {
		sbAddr := t.globalConfig.GetSBDBAddress()
		if err := t.ovsVsctl("set", "Open_vSwitch", ".",
			fmt.Sprintf("external_ids:ovn-remote=%s", sbAddr)); err != nil {
			return fmt.Errorf("failed to set ovn-remote: %w", err)
		}
		klog.V(4).Infof("Set OVN remote to %s", sbAddr)
	}

	// Set the bridge mapping for br-int
	if err := t.ovsVsctl("set", "Open_vSwitch", ".",
		fmt.Sprintf("external_ids:ovn-bridge=%s", types.BrInt)); err != nil {
		return fmt.Errorf("failed to set ovn-bridge: %w", err)
	}

	// Set encapsulation checksum option
	if err := t.ovsVsctl("set", "Open_vSwitch", ".",
		fmt.Sprintf("external_ids:ovn-encap-csum=true")); err != nil {
		klog.Warningf("Failed to set encap csum: %v", err)
	}

	klog.V(4).Infof("Configured OVN chassis encapsulation")
	return nil
}

// ovsVsctl executes an ovs-vsctl command.
func (t *TunnelController) ovsVsctl(args ...string) error {
	cmd := exec.Command("ovs-vsctl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ovs-vsctl %v failed: %w, output: %s", args, err, string(output))
	}
	return nil
}

// GetLocalIP returns the local tunnel endpoint IP.
func (t *TunnelController) GetLocalIP() net.IP {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.config.LocalIP
}

// GetTunnelType returns the configured tunnel type.
func (t *TunnelController) GetTunnelType() TunnelType {
	return t.config.Type
}

// GetTunnelPort returns the configured tunnel port.
func (t *TunnelController) GetTunnelPort() int {
	return t.config.Port
}

// IsConfigured returns whether tunnels are configured.
func (t *TunnelController) IsConfigured() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.configured
}


// TunnelInfo represents information about a tunnel endpoint.
type TunnelInfo struct {
	// Type is the tunnel encapsulation type
	Type TunnelType

	// LocalIP is the local tunnel endpoint IP
	LocalIP net.IP

	// RemoteIP is the remote tunnel endpoint IP
	RemoteIP net.IP

	// Port is the UDP port for tunnel traffic
	Port int

	// VNI is the Virtual Network Identifier
	VNI int

	// Status is the tunnel status
	Status string
}

// CreateVXLANPort creates a VXLAN tunnel port on OVS.
//
// This is typically managed by OVN automatically, but can be used
// for manual tunnel setup or debugging.
//
// Parameters:
//   - portName: Name of the OVS port
//   - remoteIP: Remote tunnel endpoint IP
//   - vni: Virtual Network Identifier (optional, 0 for auto)
//
// Returns:
//   - error: Creation error
func (t *TunnelController) CreateVXLANPort(portName string, remoteIP net.IP, vni int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.config.LocalIP == nil {
		return fmt.Errorf("local IP not configured")
	}

	// Build ovs-vsctl command
	args := []string{
		"--may-exist", "add-port", types.BrInt, portName,
		"--", "set", "interface", portName,
		"type=vxlan",
		fmt.Sprintf("options:remote_ip=%s", remoteIP),
		fmt.Sprintf("options:local_ip=%s", t.config.LocalIP),
		fmt.Sprintf("options:dst_port=%d", t.config.Port),
	}

	if vni > 0 {
		args = append(args, fmt.Sprintf("options:key=%d", vni))
	} else {
		args = append(args, "options:key=flow")
	}

	if err := t.ovsVsctl(args...); err != nil {
		return fmt.Errorf("failed to create VXLAN port %s: %w", portName, err)
	}

	klog.Infof("Created VXLAN port %s to %s", portName, remoteIP)
	return nil
}

// CreateGenevePort creates a Geneve tunnel port on OVS.
//
// Parameters:
//   - portName: Name of the OVS port
//   - remoteIP: Remote tunnel endpoint IP
//   - vni: Virtual Network Identifier (optional, 0 for auto)
//
// Returns:
//   - error: Creation error
func (t *TunnelController) CreateGenevePort(portName string, remoteIP net.IP, vni int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.config.LocalIP == nil {
		return fmt.Errorf("local IP not configured")
	}

	// Build ovs-vsctl command
	args := []string{
		"--may-exist", "add-port", types.BrInt, portName,
		"--", "set", "interface", portName,
		"type=geneve",
		fmt.Sprintf("options:remote_ip=%s", remoteIP),
		fmt.Sprintf("options:local_ip=%s", t.config.LocalIP),
		fmt.Sprintf("options:dst_port=%d", t.config.Port),
	}

	if vni > 0 {
		args = append(args, fmt.Sprintf("options:key=%d", vni))
	} else {
		args = append(args, "options:key=flow")
	}

	if err := t.ovsVsctl(args...); err != nil {
		return fmt.Errorf("failed to create Geneve port %s: %w", portName, err)
	}

	klog.Infof("Created Geneve port %s to %s", portName, remoteIP)
	return nil
}

// DeleteTunnelPort deletes a tunnel port from OVS.
//
// Parameters:
//   - portName: Name of the OVS port to delete
//
// Returns:
//   - error: Deletion error
func (t *TunnelController) DeleteTunnelPort(portName string) error {
	if err := t.ovsVsctl("--if-exists", "del-port", types.BrInt, portName); err != nil {
		return fmt.Errorf("failed to delete tunnel port %s: %w", portName, err)
	}

	klog.Infof("Deleted tunnel port %s", portName)
	return nil
}

// ListTunnelPorts lists all tunnel ports on br-int.
//
// Returns:
//   - []string: List of tunnel port names
//   - error: Query error
func (t *TunnelController) ListTunnelPorts() ([]string, error) {
	output, err := exec.Command("ovs-vsctl", "list-ports", types.BrInt).Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list ports: %w", err)
	}

	var tunnelPorts []string
	for _, port := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		port = strings.TrimSpace(port)
		if port == "" {
			continue
		}

		// Check if this is a tunnel port
		typeOutput, err := exec.Command("ovs-vsctl", "get", "interface", port, "type").Output()
		if err != nil {
			continue
		}

		portType := strings.TrimSpace(string(typeOutput))
		if portType == "vxlan" || portType == "geneve" {
			tunnelPorts = append(tunnelPorts, port)
		}
	}

	return tunnelPorts, nil
}

// GetTunnelInfo returns information about a tunnel port.
//
// Parameters:
//   - portName: Name of the tunnel port
//
// Returns:
//   - *TunnelInfo: Tunnel information
//   - error: Query error
func (t *TunnelController) GetTunnelInfo(portName string) (*TunnelInfo, error) {
	// Get port type
	typeOutput, err := exec.Command("ovs-vsctl", "get", "interface", portName, "type").Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get port type: %w", err)
	}

	portType := strings.Trim(strings.TrimSpace(string(typeOutput)), "\"")
	var tunnelType TunnelType
	switch portType {
	case "vxlan":
		tunnelType = TunnelTypeVXLAN
	case "geneve":
		tunnelType = TunnelTypeGeneve
	default:
		return nil, fmt.Errorf("port %s is not a tunnel port (type=%s)", portName, portType)
	}

	// Get remote IP
	remoteIPOutput, err := exec.Command("ovs-vsctl", "get", "interface", portName, "options:remote_ip").Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get remote IP: %w", err)
	}
	remoteIP := net.ParseIP(strings.Trim(strings.TrimSpace(string(remoteIPOutput)), "\""))

	// Get local IP
	localIPOutput, err := exec.Command("ovs-vsctl", "get", "interface", portName, "options:local_ip").Output()
	if err != nil {
		// Local IP might not be set explicitly
		localIPOutput = []byte("")
	}
	localIP := net.ParseIP(strings.Trim(strings.TrimSpace(string(localIPOutput)), "\""))

	return &TunnelInfo{
		Type:     tunnelType,
		LocalIP:  localIP,
		RemoteIP: remoteIP,
		Port:     t.config.Port,
		Status:   "active",
	}, nil
}

// ValidateTunnelConfig validates the tunnel configuration.
//
// Returns:
//   - error: Validation error
func (t *TunnelController) ValidateTunnelConfig() error {
	if t.config.Type != TunnelTypeVXLAN && t.config.Type != TunnelTypeGeneve {
		return fmt.Errorf("invalid tunnel type: %s (must be 'vxlan' or 'geneve')", t.config.Type)
	}

	if t.config.Port <= 0 || t.config.Port > 65535 {
		return fmt.Errorf("invalid tunnel port: %d (must be 1-65535)", t.config.Port)
	}

	return nil
}
