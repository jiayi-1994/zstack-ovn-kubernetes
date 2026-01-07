//go:build linux
// +build linux

// Package node provides gateway auto-detection functionality.
//
// This file implements automatic detection of:
// - Default gateway interface and IP using netlink
// - Node's external IP address
// - OVS bridge mappings
//
// Reference: OVN-Kubernetes pkg/node/helper_linux.go and pkg/node/gateway_init.go
package node

import (
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/vishvananda/netlink"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// L3GatewayConfig represents the gateway configuration for a node.
// This is saved to node annotations and read by the controller.
// Reference: ovn-kubernetes util.L3GatewayConfig
type L3GatewayConfig struct {
	// Mode is the gateway mode (shared, local, disabled)
	Mode string `json:"mode"`

	// ChassisID is the OVN chassis ID for this node
	ChassisID string `json:"chassis-id,omitempty"`

	// InterfaceID is the OVN interface ID for the gateway port
	InterfaceID string `json:"interface-id,omitempty"`

	// BridgeID is the OVS bridge name (e.g., "br-ex")
	BridgeID string `json:"bridge-id,omitempty"`

	// MACAddress is the MAC address of the gateway interface
	MACAddress string `json:"mac-address,omitempty"`

	// IPAddresses are the IP addresses on the gateway interface (CIDR format)
	IPAddresses []string `json:"ip-addresses,omitempty"`

	// NextHops are the default gateway IPs
	NextHops []string `json:"next-hops,omitempty"`

	// NodePortEnable indicates if NodePort is enabled
	NodePortEnable bool `json:"node-port-enable,omitempty"`

	// VLANID is the VLAN ID for the external network (0 for untagged)
	VLANID int `json:"vlan-id,omitempty"`
}

// L3GatewayAnnotation is the annotation key for L3 gateway config
const L3GatewayAnnotation = "zstack.io/l3-gateway-config"

// ChassisIDAnnotation is the annotation key for chassis ID
const ChassisIDAnnotation = "zstack.io/node-chassis-id"

// GatewayInfo contains auto-detected gateway information
type GatewayInfo struct {
	// InterfaceName is the name of the interface with the default route
	InterfaceName string

	// GatewayIP is the default gateway IP address
	GatewayIP net.IP

	// NodeIP is the node's IP address on the gateway interface
	NodeIP net.IP

	// NodeIPNet is the node's IP address with subnet mask
	NodeIPNet *net.IPNet

	// BridgeName is the OVS bridge name (if detected)
	BridgeName string

	// MACAddress is the MAC address of the gateway interface
	MACAddress net.HardwareAddr
}

// DetectGatewayInfo automatically detects gateway configuration from the system
// using netlink to query the routing table.
//
// Returns:
//   - *GatewayInfo: Detected gateway information
//   - error: Detection error
func DetectGatewayInfo() (*GatewayInfo, error) {
	info := &GatewayInfo{}

	// Detect default gateway interface and IP using netlink
	intfName, gwIP, err := getDefaultGatewayNetlink()
	if err != nil {
		// Fall back to command-based detection
		klog.V(4).Infof("Netlink detection failed, falling back to command: %v", err)
		intfName, gwIP, err = getDefaultGatewayCommand()
		if err != nil {
			return nil, fmt.Errorf("failed to detect default gateway: %w", err)
		}
	}

	info.InterfaceName = intfName
	info.GatewayIP = gwIP

	// Get node's IP and MAC on the gateway interface
	nodeIPNet, mac, err := getInterfaceDetails(intfName)
	if err != nil {
		klog.Warningf("Failed to get details for interface %s: %v", intfName, err)
	} else {
		info.NodeIPNet = nodeIPNet
		info.NodeIP = nodeIPNet.IP
		info.MACAddress = mac
	}

	// Try to detect OVS bridge
	bridgeName, err := detectOVSBridge(intfName)
	if err != nil {
		klog.V(4).Infof("No OVS bridge detected for interface %s: %v", intfName, err)
	} else {
		info.BridgeName = bridgeName
	}

	klog.Infof("Detected gateway info: interface=%s, gateway=%s, nodeIP=%s, mac=%s, bridge=%s",
		info.InterfaceName, info.GatewayIP, info.NodeIP, info.MACAddress, info.BridgeName)

	return info, nil
}

// getDefaultGatewayNetlink uses netlink to find the default gateway.
// This is the preferred method as it's more reliable than parsing command output.
// Reference: ovn-kubernetes pkg/node/helper_linux.go getDefaultGatewayInterfaceByFamily
func getDefaultGatewayNetlink() (string, net.IP, error) {
	// Get IPv4 default route
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return "", nil, fmt.Errorf("failed to list routes: %w", err)
	}

	for _, route := range routes {
		// Default route has Dst == nil
		if route.Dst != nil {
			continue
		}

		// Skip routes without gateway
		if route.Gw == nil {
			continue
		}

		// Get interface name
		link, err := netlink.LinkByIndex(route.LinkIndex)
		if err != nil {
			klog.V(4).Infof("Failed to get link for index %d: %v", route.LinkIndex, err)
			continue
		}

		intfName := link.Attrs().Name
		klog.V(4).Infof("Found default route via netlink: gateway=%s, interface=%s", route.Gw, intfName)
		return intfName, route.Gw, nil
	}

	return "", nil, fmt.Errorf("no default gateway found via netlink")
}

// getDefaultGatewayCommand reads the routing table using 'ip route' command.
// This is a fallback method if netlink fails.
func getDefaultGatewayCommand() (string, net.IP, error) {
	output, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return "", nil, fmt.Errorf("failed to execute 'ip route show default': %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		if fields[0] != "default" {
			continue
		}

		var gwIP net.IP
		var intfName string

		for i := 1; i < len(fields)-1; i++ {
			switch fields[i] {
			case "via":
				gwIP = net.ParseIP(fields[i+1])
			case "dev":
				intfName = fields[i+1]
			}
		}

		if gwIP != nil && intfName != "" {
			klog.V(4).Infof("Found default route via command: gateway=%s, interface=%s", gwIP, intfName)
			return intfName, gwIP, nil
		}
	}

	return "", nil, fmt.Errorf("no default gateway found in routing table")
}

// getInterfaceDetails gets the IP address and MAC address of an interface.
func getInterfaceDetails(intfName string) (*net.IPNet, net.HardwareAddr, error) {
	link, err := netlink.LinkByName(intfName)
	if err != nil {
		return nil, nil, fmt.Errorf("interface %s not found: %w", intfName, err)
	}

	mac := link.Attrs().HardwareAddr

	// Get addresses
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil, mac, fmt.Errorf("failed to get addresses for %s: %w", intfName, err)
	}

	for _, addr := range addrs {
		if addr.IP.IsLoopback() || addr.IP.IsLinkLocalUnicast() {
			continue
		}
		return addr.IPNet, mac, nil
	}

	return nil, mac, fmt.Errorf("no valid IP address found on interface %s", intfName)
}

// getInterfaceIP gets the primary IP address of an interface (legacy function).
func getInterfaceIP(intfName string) (net.IP, error) {
	ipNet, _, err := getInterfaceDetails(intfName)
	if err != nil {
		return nil, err
	}
	return ipNet.IP, nil
}

// detectOVSBridge tries to detect if the interface is part of an OVS bridge.
func detectOVSBridge(intfName string) (string, error) {
	// Check if interface is an OVS bridge
	err := exec.Command("ovs-vsctl", "--timeout=5", "br-exists", intfName).Run()
	if err == nil {
		return intfName, nil
	}

	// Check if interface is a port on an OVS bridge
	output, err := exec.Command("ovs-vsctl", "--timeout=5", "port-to-br", intfName).Output()
	if err == nil {
		bridgeName := strings.TrimSpace(string(output))
		if bridgeName != "" {
			return bridgeName, nil
		}
	}

	// Check for br-ex (common external bridge name)
	err = exec.Command("ovs-vsctl", "--timeout=5", "br-exists", "br-ex").Run()
	if err == nil {
		return "br-ex", nil
	}

	return "", fmt.Errorf("no OVS bridge detected")
}

// GetOrCreateExternalBridge ensures an external OVS bridge exists.
func GetOrCreateExternalBridge(bridgeName, physicalIntf string) error {
	err := exec.Command("ovs-vsctl", "--timeout=5", "br-exists", bridgeName).Run()
	if err == nil {
		klog.V(4).Infof("OVS bridge %s already exists", bridgeName)
		return nil
	}

	klog.Infof("Creating OVS bridge %s with interface %s", bridgeName, physicalIntf)

	if err := exec.Command("ovs-vsctl", "--timeout=15", "--may-exist", "add-br", bridgeName).Run(); err != nil {
		return fmt.Errorf("failed to create OVS bridge %s: %w", bridgeName, err)
	}

	if physicalIntf != "" && physicalIntf != bridgeName {
		if err := exec.Command("ovs-vsctl", "--timeout=15", "--may-exist", "add-port", bridgeName, physicalIntf).Run(); err != nil {
			klog.Warningf("Failed to add interface %s to bridge %s: %v", physicalIntf, bridgeName, err)
		}
	}

	return nil
}

// EnsureBridgeMapping ensures the OVN bridge mapping is configured.
func EnsureBridgeMapping(physicalNetwork, bridgeName string) error {
	output, err := exec.Command("ovs-vsctl", "--timeout=5", "--if-exists", "get",
		"Open_vSwitch", ".", "external_ids:ovn-bridge-mappings").Output()
	if err != nil {
		klog.V(4).Infof("No existing bridge mappings found: %v", err)
	}

	currentMappings := strings.TrimSpace(string(output))
	currentMappings = strings.Trim(currentMappings, "\"")
	currentMappings = strings.ReplaceAll(currentMappings, "\\\"", "")
	currentMappings = strings.ReplaceAll(currentMappings, "\"", "")

	newMapping := fmt.Sprintf("%s:%s", physicalNetwork, bridgeName)

	var validMappings []string
	if currentMappings != "" {
		parts := strings.Split(currentMappings, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}

			colonIdx := strings.Index(part, ":")
			if colonIdx <= 0 || colonIdx >= len(part)-1 {
				klog.Warningf("Skipping invalid bridge mapping: %q", part)
				continue
			}

			network := part[:colonIdx]
			bridge := part[colonIdx+1:]

			if strings.ContainsAny(bridge, "\"'\\") {
				klog.Warningf("Skipping bridge mapping with invalid bridge name: %q", part)
				continue
			}

			if network == physicalNetwork {
				continue
			}

			if err := exec.Command("ovs-vsctl", "--timeout=5", "br-exists", bridge).Run(); err != nil {
				klog.Warningf("Skipping bridge mapping for non-existent bridge: %q", part)
				continue
			}

			validMappings = append(validMappings, part)
		}
	}

	validMappings = append(validMappings, newMapping)
	mappings := strings.Join(validMappings, ",")

	if currentMappings == mappings {
		klog.V(4).Infof("Bridge mapping %s already correctly configured", newMapping)
		return nil
	}

	klog.Infof("Setting OVN bridge mapping: %s (was: %s)", mappings, currentMappings)
	if err := exec.Command("ovs-vsctl", "--timeout=15", "set", "Open_vSwitch", ".",
		fmt.Sprintf("external_ids:ovn-bridge-mappings=%s", mappings)).Run(); err != nil {
		return fmt.Errorf("failed to set bridge mapping: %w", err)
	}

	return nil
}

// AutoConfigureGateway automatically configures the gateway for external network access.
// This does NOT migrate the physical interface to br-ex. Instead, it:
// 1. Detects the default gateway interface and IP using netlink
// 2. Creates br-ex bridge (empty, for OVN localnet port)
// 3. Configures bridge mapping for OVN
//
// Traffic flow relies on host routing:
// Pod -> OVN SNAT -> host routing table -> physical interface -> external network
func AutoConfigureGateway(physicalNetwork string) (*GatewayInfo, error) {
	info, err := DetectGatewayInfo()
	if err != nil {
		return nil, fmt.Errorf("failed to detect gateway: %w", err)
	}

	// Create br-ex bridge (without adding physical interface)
	// This is needed for OVN localnet port, but traffic will use host routing
	bridgeName := "br-ex"
	if err := GetOrCreateExternalBridge(bridgeName, ""); err != nil {
		klog.Warningf("Failed to create external bridge: %v", err)
	}

	// Configure bridge mapping for OVN
	if err := EnsureBridgeMapping(physicalNetwork, bridgeName); err != nil {
		return nil, fmt.Errorf("failed to configure bridge mapping: %w", err)
	}

	info.BridgeName = bridgeName
	return info, nil
}


// MigrateInterfaceToBridge is deprecated and no longer used.
// We now use host routing instead of migrating the physical interface to OVS bridge.
// This function is kept for backward compatibility but does nothing.
func MigrateInterfaceToBridge(physicalIntf, bridgeName string) error {
	klog.Warningf("MigrateInterfaceToBridge is deprecated, using host routing instead")
	return nil
}

// EnsureExternalBridgeWithInterface is deprecated.
// Use AutoConfigureGateway instead, which does not modify the physical interface.
func EnsureExternalBridgeWithInterface(physicalNetwork string) (*GatewayInfo, error) {
	klog.Warningf("EnsureExternalBridgeWithInterface is deprecated, using AutoConfigureGateway instead")
	return AutoConfigureGateway(physicalNetwork)
}

// BuildL3GatewayConfig builds an L3GatewayConfig from detected gateway info.
// This is saved to node annotations for the controller to read.
func BuildL3GatewayConfig(info *GatewayInfo, chassisID string, vlanID int) *L3GatewayConfig {
	cfg := &L3GatewayConfig{
		Mode:           "shared",
		ChassisID:      chassisID,
		BridgeID:       info.BridgeName,
		NodePortEnable: true,
		VLANID:         vlanID,
	}

	if info.BridgeName != "" {
		cfg.InterfaceID = fmt.Sprintf("%s_", info.BridgeName)
	}

	if info.MACAddress != nil {
		cfg.MACAddress = info.MACAddress.String()
	}

	if info.NodeIPNet != nil {
		cfg.IPAddresses = []string{info.NodeIPNet.String()}
	}

	if info.GatewayIP != nil {
		cfg.NextHops = []string{info.GatewayIP.String()}
	}

	return cfg
}

// SetL3GatewayAnnotation sets the L3 gateway config annotation on a node.
func SetL3GatewayAnnotation(node *corev1.Node, cfg *L3GatewayConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal L3GatewayConfig: %w", err)
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	node.Annotations[L3GatewayAnnotation] = string(data)

	if cfg.ChassisID != "" {
		node.Annotations[ChassisIDAnnotation] = cfg.ChassisID
	}

	return nil
}

// ParseL3GatewayAnnotation parses the L3 gateway config from node annotations.
func ParseL3GatewayAnnotation(node *corev1.Node) (*L3GatewayConfig, error) {
	if node.Annotations == nil {
		return nil, fmt.Errorf("node %s has no annotations", node.Name)
	}

	data, ok := node.Annotations[L3GatewayAnnotation]
	if !ok {
		return nil, fmt.Errorf("node %s has no L3 gateway annotation", node.Name)
	}

	cfg := &L3GatewayConfig{}
	if err := json.Unmarshal([]byte(data), cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal L3GatewayConfig: %w", err)
	}

	// Also get chassis ID from separate annotation if not in config
	if cfg.ChassisID == "" {
		if chassisID, ok := node.Annotations[ChassisIDAnnotation]; ok {
			cfg.ChassisID = chassisID
		}
	}

	return cfg, nil
}

// GetChassisID gets the OVN chassis ID for this node.
func GetChassisID() (string, error) {
	output, err := exec.Command("ovs-vsctl", "--timeout=5", "get", "Open_vSwitch", ".", "external_ids:system-id").Output()
	if err != nil {
		return "", fmt.Errorf("failed to get chassis ID: %w", err)
	}

	chassisID := strings.TrimSpace(string(output))
	chassisID = strings.Trim(chassisID, "\"")
	if chassisID == "" {
		return "", fmt.Errorf("empty chassis ID")
	}

	return chassisID, nil
}

// GetPatchPortName returns the patch port name connecting br-ex to br-int for a given localnet port.
// The patch port is created by OVN when a localnet port is configured.
// Format: patch-<localnet-port>-to-br-int
func GetPatchPortName(nodeName string) string {
	return fmt.Sprintf("patch-ln-%s-to-br-int", nodeName)
}

// GetPhysicalPortOnBridge returns the physical interface port on the bridge.
// This is the interface that was migrated to br-ex (e.g., ens3, eth0).
func GetPhysicalPortOnBridge(bridgeName string) (string, error) {
	output, err := exec.Command("ovs-vsctl", "--timeout=5", "list-ports", bridgeName).Output()
	if err != nil {
		return "", fmt.Errorf("failed to list ports on %s: %w", bridgeName, err)
	}

	ports := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, port := range ports {
		port = strings.TrimSpace(port)
		if port == "" {
			continue
		}
		// Skip patch ports and internal ports
		if strings.HasPrefix(port, "patch-") {
			continue
		}
		// This should be the physical interface
		return port, nil
	}

	return "", fmt.Errorf("no physical port found on bridge %s", bridgeName)
}

// GetBridgeMAC returns the MAC address of the bridge.
func GetBridgeMAC(bridgeName string) (string, error) {
	link, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return "", fmt.Errorf("failed to get link %s: %w", bridgeName, err)
	}
	return link.Attrs().HardwareAddr.String(), nil
}

// SetupOpenFlowRules configures OpenFlow rules on br-ex for proper traffic handling.
// This is the key to making return traffic work correctly:
// - Outbound traffic from OVN is marked with ct_mark=CtMarkOVN
// - Return traffic is checked against conntrack and forwarded to OVN if ct_mark=CtMarkOVN
//
// Parameters:
//   - nodeName: Name of the node (used to find patch port)
//   - bridgeName: Name of the external bridge (usually "br-ex")
//   - nodeIP: Node's external IP address
//   - clusterCIDR: Cluster CIDR for pod traffic
//
// Returns:
//   - error: Setup error
func SetupOpenFlowRules(nodeName, bridgeName, nodeIP, clusterCIDR string) error {
	// Get patch port name
	patchPort := GetPatchPortName(nodeName)

	// Verify patch port exists
	if err := exec.Command("ovs-vsctl", "--timeout=5", "get", "Interface", patchPort, "ofport").Run(); err != nil {
		klog.Warningf("Patch port %s not found, OpenFlow rules may not work correctly: %v", patchPort, err)
		// Don't fail - the patch port might be created later by OVN
	}

	// Get physical port
	physPort, err := GetPhysicalPortOnBridge(bridgeName)
	if err != nil {
		return fmt.Errorf("failed to get physical port: %w", err)
	}

	// Get bridge MAC
	bridgeMAC, err := GetBridgeMAC(bridgeName)
	if err != nil {
		return fmt.Errorf("failed to get bridge MAC: %w", err)
	}

	// Create and setup OpenFlow manager
	ofMgr := NewOpenFlowManager(bridgeName, patchPort, physPort, bridgeMAC, nodeIP, clusterCIDR)
	if err := ofMgr.SetupFlows(); err != nil {
		return fmt.Errorf("failed to setup OpenFlow rules: %w", err)
	}

	return nil
}
