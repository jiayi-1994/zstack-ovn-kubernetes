// Package node provides gateway auto-detection functionality.
//
// This file implements automatic detection of:
// - Default gateway interface and IP
// - Node's external IP address
// - OVS bridge mappings
//
// Reference: OVN-Kubernetes pkg/node/helper_linux.go
package node

import (
	"fmt"
	"net"
	"os/exec"
	"strings"

	"k8s.io/klog/v2"
)

// GatewayInfo contains auto-detected gateway information
type GatewayInfo struct {
	// InterfaceName is the name of the interface with the default route
	InterfaceName string

	// GatewayIP is the default gateway IP address
	GatewayIP net.IP

	// NodeIP is the node's IP address on the gateway interface
	NodeIP net.IP

	// BridgeName is the OVS bridge name (if detected)
	BridgeName string
}

// DetectGatewayInfo automatically detects gateway configuration from the system.
// This reads the routing table to find the default gateway and interface.
//
// Returns:
//   - *GatewayInfo: Detected gateway information
//   - error: Detection error
func DetectGatewayInfo() (*GatewayInfo, error) {
	info := &GatewayInfo{}

	// Detect default gateway interface and IP
	intfName, gwIP, err := getDefaultGateway()
	if err != nil {
		return nil, fmt.Errorf("failed to detect default gateway: %w", err)
	}

	info.InterfaceName = intfName
	info.GatewayIP = gwIP

	// Get node's IP on the gateway interface
	nodeIP, err := getInterfaceIP(intfName)
	if err != nil {
		klog.Warningf("Failed to get IP for interface %s: %v", intfName, err)
	} else {
		info.NodeIP = nodeIP
	}

	// Try to detect OVS bridge
	bridgeName, err := detectOVSBridge(intfName)
	if err != nil {
		klog.V(4).Infof("No OVS bridge detected for interface %s: %v", intfName, err)
	} else {
		info.BridgeName = bridgeName
	}

	klog.Infof("Detected gateway info: interface=%s, gateway=%s, nodeIP=%s, bridge=%s",
		info.InterfaceName, info.GatewayIP, info.NodeIP, info.BridgeName)

	return info, nil
}

// getDefaultGateway reads the routing table to find the default gateway.
// Returns the interface name and gateway IP.
func getDefaultGateway() (string, net.IP, error) {
	// Use 'ip route' command to get default route
	// Format: default via 192.168.1.1 dev eth0 proto static metric 100
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

		// Parse: default via <gateway> dev <interface>
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
			klog.V(4).Infof("Found default route: gateway=%s, interface=%s", gwIP, intfName)
			return intfName, gwIP, nil
		}
	}

	return "", nil, fmt.Errorf("no default gateway found in routing table")
}

// getInterfaceIP gets the primary IP address of an interface.
func getInterfaceIP(intfName string) (net.IP, error) {
	iface, err := net.InterfaceByName(intfName)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %w", intfName, err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("failed to get addresses for interface %s: %w", intfName, err)
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}

		// Skip loopback and link-local addresses
		ip := ipNet.IP.To4()
		if ip == nil {
			continue // Skip IPv6 for now
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}

		return ip, nil
	}

	return nil, fmt.Errorf("no valid IP address found on interface %s", intfName)
}

// detectOVSBridge tries to detect if the interface is part of an OVS bridge.
func detectOVSBridge(intfName string) (string, error) {
	// Check if interface is an OVS bridge
	output, err := exec.Command("ovs-vsctl", "--timeout=5", "br-exists", intfName).CombinedOutput()
	if err == nil {
		return intfName, nil // Interface itself is a bridge
	}

	// Check if interface is a port on an OVS bridge
	output, err = exec.Command("ovs-vsctl", "--timeout=5", "port-to-br", intfName).Output()
	if err == nil {
		bridgeName := strings.TrimSpace(string(output))
		if bridgeName != "" {
			return bridgeName, nil
		}
	}

	// Check for br-ex (common external bridge name)
	output, err = exec.Command("ovs-vsctl", "--timeout=5", "br-exists", "br-ex").CombinedOutput()
	if err == nil {
		return "br-ex", nil
	}

	return "", fmt.Errorf("no OVS bridge detected")
}

// GetOrCreateExternalBridge ensures an external OVS bridge exists.
// If the bridge doesn't exist, it creates one and adds the physical interface to it.
//
// Parameters:
//   - bridgeName: Name of the bridge to create (e.g., "br-ex")
//   - physicalIntf: Physical interface to add to the bridge
//
// Returns:
//   - error: Creation error
func GetOrCreateExternalBridge(bridgeName, physicalIntf string) error {
	// Check if bridge already exists
	err := exec.Command("ovs-vsctl", "--timeout=5", "br-exists", bridgeName).Run()
	if err == nil {
		klog.V(4).Infof("OVS bridge %s already exists", bridgeName)
		return nil
	}

	klog.Infof("Creating OVS bridge %s with interface %s", bridgeName, physicalIntf)

	// Create the bridge
	if err := exec.Command("ovs-vsctl", "--timeout=15", "--may-exist", "add-br", bridgeName).Run(); err != nil {
		return fmt.Errorf("failed to create OVS bridge %s: %w", bridgeName, err)
	}

	// Add physical interface to bridge (if specified and not the bridge itself)
	if physicalIntf != "" && physicalIntf != bridgeName {
		if err := exec.Command("ovs-vsctl", "--timeout=15", "--may-exist", "add-port", bridgeName, physicalIntf).Run(); err != nil {
			klog.Warningf("Failed to add interface %s to bridge %s: %v", physicalIntf, bridgeName, err)
			// Don't fail - the interface might already be managed differently
		}
	}

	return nil
}

// EnsureBridgeMapping ensures the OVN bridge mapping is configured.
// This sets the ovn-bridge-mappings external-id on Open_vSwitch.
//
// Parameters:
//   - physicalNetwork: Name of the physical network (e.g., "external")
//   - bridgeName: Name of the OVS bridge (e.g., "br-ex")
//
// Returns:
//   - error: Configuration error
func EnsureBridgeMapping(physicalNetwork, bridgeName string) error {
	// Get current bridge mappings
	output, err := exec.Command("ovs-vsctl", "--timeout=5", "--if-exists", "get",
		"Open_vSwitch", ".", "external_ids:ovn-bridge-mappings").Output()
	if err != nil {
		klog.V(4).Infof("No existing bridge mappings found: %v", err)
	}

	currentMappings := strings.TrimSpace(strings.Trim(string(output), "\""))
	newMapping := fmt.Sprintf("%s:%s", physicalNetwork, bridgeName)

	// Check if mapping already exists
	if strings.Contains(currentMappings, newMapping) {
		klog.V(4).Infof("Bridge mapping %s already exists", newMapping)
		return nil
	}

	// Build new mappings string
	var mappings string
	if currentMappings != "" {
		// Check if this physical network already has a different mapping
		parts := strings.Split(currentMappings, ",")
		var filtered []string
		for _, part := range parts {
			if !strings.HasPrefix(part, physicalNetwork+":") {
				filtered = append(filtered, part)
			}
		}
		if len(filtered) > 0 {
			mappings = strings.Join(filtered, ",") + "," + newMapping
		} else {
			mappings = newMapping
		}
	} else {
		mappings = newMapping
	}

	// Set the bridge mapping
	klog.Infof("Setting OVN bridge mapping: %s", mappings)
	if err := exec.Command("ovs-vsctl", "--timeout=15", "set", "Open_vSwitch", ".",
		fmt.Sprintf("external_ids:ovn-bridge-mappings=%s", mappings)).Run(); err != nil {
		return fmt.Errorf("failed to set bridge mapping: %w", err)
	}

	return nil
}

// AutoConfigureGateway automatically configures the gateway for external network access.
// This is a convenience function that:
// 1. Detects the default gateway
// 2. Creates/ensures the external OVS bridge
// 3. Configures the OVN bridge mapping
//
// Parameters:
//   - physicalNetwork: Name of the physical network for OVN (e.g., "external")
//
// Returns:
//   - *GatewayInfo: Detected and configured gateway information
//   - error: Configuration error
func AutoConfigureGateway(physicalNetwork string) (*GatewayInfo, error) {
	// Detect gateway info
	info, err := DetectGatewayInfo()
	if err != nil {
		return nil, fmt.Errorf("failed to detect gateway: %w", err)
	}

	// Determine bridge name
	bridgeName := info.BridgeName
	if bridgeName == "" {
		bridgeName = "br-ex" // Default external bridge name
	}

	// Ensure bridge exists
	// Note: We don't automatically add the physical interface to avoid disrupting network
	// In production, this should be done carefully or via a separate setup script
	if err := GetOrCreateExternalBridge(bridgeName, ""); err != nil {
		klog.Warningf("Failed to create external bridge: %v", err)
		// Continue anyway - bridge might be managed externally
	}

	// Ensure bridge mapping
	if err := EnsureBridgeMapping(physicalNetwork, bridgeName); err != nil {
		return nil, fmt.Errorf("failed to configure bridge mapping: %w", err)
	}

	info.BridgeName = bridgeName
	return info, nil
}
