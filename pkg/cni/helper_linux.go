// Package cni provides Linux-specific network interface configuration.
//
// This file implements the low-level network configuration for Pods:
// - Creating veth pairs (virtual ethernet devices)
// - Moving interfaces into container network namespaces
// - Configuring IP addresses and routes
// - Adding ports to OVS br-int bridge
//
// Network Configuration Flow:
//
//	┌─────────────────────────────────────────────────────────────────────┐
//	│                         Host Network Namespace                       │
//	│                                                                      │
//	│  ┌─────────────┐                              ┌─────────────────┐   │
//	│  │   br-int    │◄─────────────────────────────│  veth-host-xxx  │   │
//	│  │  (OVS)      │     OVS port                 │  (host end)     │   │
//	│  └─────────────┘                              └────────┬────────┘   │
//	│                                                        │            │
//	└────────────────────────────────────────────────────────┼────────────┘
//	                                                         │ veth pair
//	┌────────────────────────────────────────────────────────┼────────────┐
//	│                    Container Network Namespace          │            │
//	│                                                        │            │
//	│                                               ┌────────┴────────┐   │
//	│                                               │      eth0       │   │
//	│                                               │  (container end)│   │
//	│                                               │  IP: 10.244.x.x │   │
//	│                                               └─────────────────┘   │
//	│                                                                      │
//	└──────────────────────────────────────────────────────────────────────┘
//
// Reference: OVN-Kubernetes pkg/cni/helper_linux.go
//
//go:build linux

package cni

import (
	"crypto/rand"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"

	"github.com/containernetworking/plugins/pkg/ip"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"
)

const (
	// DefaultMTU is the default MTU for Pod interfaces
	// 1400 accounts for VXLAN overhead (50 bytes) from a 1500 MTU physical network
	DefaultMTU = 1400

	// OVSBridge is the name of the OVS integration bridge
	OVSBridge = "br-int"

	// VethHostPrefix is the prefix for host-side veth interfaces
	// Format: veth-<random>
	VethHostPrefix = "veth"

	// ContainerIfName is the interface name inside the container
	ContainerIfName = "eth0"
)

// InterfaceConfig contains the configuration for a Pod's network interface
type InterfaceConfig struct {
	// PodNamespace is the Pod's Kubernetes namespace
	PodNamespace string

	// PodName is the Pod's name
	PodName string

	// ContainerID is the container ID
	ContainerID string

	// NetNS is the path to the container's network namespace
	// Example: /var/run/netns/cni-xxxxx
	NetNS string

	// IfName is the interface name inside the container (usually "eth0")
	IfName string

	// IPAddress is the IP address with prefix length
	// Example: "10.244.1.5/24"
	IPAddress string

	// MACAddress is the MAC address
	// Example: "0a:58:0a:f4:01:05"
	MACAddress string

	// Gateway is the default gateway IP
	// Example: "10.244.1.1"
	Gateway string

	// MTU is the MTU for the interface
	MTU int

	// OVSPortName is the name of the OVS port (same as host veth name)
	OVSPortName string

	// PortUUID is the OVN Logical Switch Port UUID
	PortUUID string
}

// InterfaceInfo contains information about a configured interface
type InterfaceInfo struct {
	// HostIfName is the host-side veth interface name
	HostIfName string

	// ContainerIfName is the container-side interface name
	ContainerIfName string

	// MACAddress is the actual MAC address assigned
	MACAddress string
}

// SetupInterface creates and configures the network interface for a Pod
//
// This function:
// 1. Creates a veth pair
// 2. Moves one end into the container namespace
// 3. Configures IP address and routes in the container
// 4. Adds the host end to OVS br-int
//
// Parameters:
//   - cfg: Interface configuration
//
// Returns:
//   - *InterfaceInfo: Information about the configured interface
//   - error: Configuration error
func SetupInterface(cfg *InterfaceConfig) (*InterfaceInfo, error) {
	if cfg == nil {
		return nil, fmt.Errorf("interface config is nil")
	}

	// Validate required fields
	if cfg.NetNS == "" {
		return nil, fmt.Errorf("network namespace path is required")
	}
	if cfg.IPAddress == "" {
		return nil, fmt.Errorf("IP address is required")
	}
	if cfg.Gateway == "" {
		return nil, fmt.Errorf("gateway is required")
	}

	// Set defaults
	if cfg.IfName == "" {
		cfg.IfName = ContainerIfName
	}
	if cfg.MTU == 0 {
		cfg.MTU = DefaultMTU
	}

	// Generate host veth name
	hostIfName, err := generateVethName()
	if err != nil {
		return nil, fmt.Errorf("failed to generate veth name: %w", err)
	}

	// Store OVS port name for later use
	cfg.OVSPortName = hostIfName

	klog.V(4).Infof("Setting up interface for pod %s/%s: hostIf=%s, containerIf=%s, ip=%s",
		cfg.PodNamespace, cfg.PodName, hostIfName, cfg.IfName, cfg.IPAddress)

	// Get container network namespace
	containerNS, err := ns.GetNS(cfg.NetNS)
	if err != nil {
		return nil, fmt.Errorf("failed to open network namespace %s: %w", cfg.NetNS, err)
	}
	defer containerNS.Close()

	// Create veth pair and configure interface
	var containerMAC string
	err = containerNS.Do(func(hostNS ns.NetNS) error {
		// Create veth pair
		// The host end stays in the host namespace, container end is in container namespace
		hostVeth, containerVeth, err := ip.SetupVeth(cfg.IfName, cfg.MTU, cfg.MACAddress, hostNS)
		if err != nil {
			return fmt.Errorf("failed to create veth pair: %w", err)
		}

		// Rename host veth to our generated name
		hostLink, err := netlink.LinkByName(hostVeth.Name)
		if err != nil {
			return fmt.Errorf("failed to find host veth %s: %w", hostVeth.Name, err)
		}

		// Move to host namespace and rename
		err = hostNS.Do(func(_ ns.NetNS) error {
			link, err := netlink.LinkByName(hostVeth.Name)
			if err != nil {
				return fmt.Errorf("failed to find host veth in host ns: %w", err)
			}
			if err := netlink.LinkSetName(link, hostIfName); err != nil {
				return fmt.Errorf("failed to rename host veth to %s: %w", hostIfName, err)
			}
			return nil
		})
		if err != nil {
			return err
		}

		// Store container MAC address
		containerMAC = containerVeth.HardwareAddr.String()

		// Configure IP address on container interface
		if err := setupNetwork(cfg); err != nil {
			return fmt.Errorf("failed to configure network: %w", err)
		}

		_ = hostLink // Used above
		return nil
	})

	if err != nil {
		// Clean up on failure
		cleanupVeth(hostIfName)
		return nil, err
	}

	// Add host veth to OVS br-int
	if err := configureOVS(cfg, hostIfName); err != nil {
		// Clean up on failure
		cleanupVeth(hostIfName)
		return nil, fmt.Errorf("failed to configure OVS: %w", err)
	}

	// Use configured MAC if provided, otherwise use the one assigned by kernel
	mac := cfg.MACAddress
	if mac == "" {
		mac = containerMAC
	}

	return &InterfaceInfo{
		HostIfName:      hostIfName,
		ContainerIfName: cfg.IfName,
		MACAddress:      mac,
	}, nil
}

// setupNetwork configures IP address and routes inside the container
//
// This function runs inside the container's network namespace and:
// 1. Parses the IP address and prefix
// 2. Adds the IP address to the interface
// 3. Adds the default route via the gateway
//
// Parameters:
//   - cfg: Interface configuration
//
// Returns:
//   - error: Configuration error
func setupNetwork(cfg *InterfaceConfig) error {
	// Parse IP address
	ipAddr, ipNet, err := net.ParseCIDR(cfg.IPAddress)
	if err != nil {
		return fmt.Errorf("invalid IP address %s: %w", cfg.IPAddress, err)
	}

	// Get the container interface
	link, err := netlink.LinkByName(cfg.IfName)
	if err != nil {
		return fmt.Errorf("failed to find interface %s: %w", cfg.IfName, err)
	}

	// Add IP address to interface
	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   ipAddr,
			Mask: ipNet.Mask,
		},
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("failed to add IP address %s to %s: %w", cfg.IPAddress, cfg.IfName, err)
	}

	// Bring interface up
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to bring up interface %s: %w", cfg.IfName, err)
	}

	// Parse gateway
	gwIP := net.ParseIP(cfg.Gateway)
	if gwIP == nil {
		return fmt.Errorf("invalid gateway IP: %s", cfg.Gateway)
	}

	// Add default route via gateway
	// For IPv4: 0.0.0.0/0 via gateway
	// For IPv6: ::/0 via gateway
	var defaultDst *net.IPNet
	if gwIP.To4() != nil {
		defaultDst = &net.IPNet{
			IP:   net.IPv4zero,
			Mask: net.CIDRMask(0, 32),
		}
	} else {
		defaultDst = &net.IPNet{
			IP:   net.IPv6zero,
			Mask: net.CIDRMask(0, 128),
		}
	}

	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       defaultDst,
		Gw:        gwIP,
	}
	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("failed to add default route via %s: %w", cfg.Gateway, err)
	}

	klog.V(4).Infof("Configured network for %s: ip=%s, gw=%s", cfg.IfName, cfg.IPAddress, cfg.Gateway)
	return nil
}

// configureOVS adds the host veth to OVS br-int bridge
//
// This function:
// 1. Adds the host veth as a port to br-int
// 2. Sets the external_ids for OVN integration
// 3. Sets the iface-id to match the OVN Logical Switch Port name
//
// The iface-id is crucial for OVN - it links the OVS port to the OVN LSP.
// When ovn-controller sees a port with iface-id matching an LSP name,
// it programs the flows for that port.
//
// Parameters:
//   - cfg: Interface configuration
//   - hostIfName: Name of the host-side veth interface
//
// Returns:
//   - error: Configuration error
func configureOVS(cfg *InterfaceConfig, hostIfName string) error {
	// Build the iface-id (OVN Logical Switch Port name)
	// Format: namespace_podName
	ifaceID := fmt.Sprintf("%s_%s", cfg.PodNamespace, cfg.PodName)

	// Add port to OVS br-int
	// ovs-vsctl add-port br-int <hostIfName> -- set interface <hostIfName> external_ids:iface-id=<ifaceID>
	args := []string{
		"--may-exist", "add-port", OVSBridge, hostIfName,
		"--", "set", "interface", hostIfName,
		fmt.Sprintf("external_ids:iface-id=%s", ifaceID),
		fmt.Sprintf("external_ids:attached-mac=%s", cfg.MACAddress),
		fmt.Sprintf("external_ids:ip_addresses=%s", strings.Split(cfg.IPAddress, "/")[0]),
		fmt.Sprintf("external_ids:sandbox=%s", cfg.ContainerID),
	}

	klog.V(4).Infof("Adding OVS port: ovs-vsctl %s", strings.Join(args, " "))

	cmd := exec.Command("ovs-vsctl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add OVS port: %w, output: %s", err, string(output))
	}

	// Bring up the host interface
	link, err := netlink.LinkByName(hostIfName)
	if err != nil {
		return fmt.Errorf("failed to find host interface %s: %w", hostIfName, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to bring up host interface %s: %w", hostIfName, err)
	}

	klog.V(4).Infof("Added OVS port %s with iface-id=%s", hostIfName, ifaceID)
	return nil
}

// TeardownInterface removes the network interface for a Pod
//
// This function:
// 1. Removes the OVS port from br-int
// 2. Deletes the veth pair
//
// Parameters:
//   - cfg: Interface configuration (only PodNamespace, PodName, ContainerID needed)
//
// Returns:
//   - error: Cleanup error (nil if already cleaned up)
func TeardownInterface(cfg *InterfaceConfig) error {
	if cfg == nil {
		return nil
	}

	// Build the iface-id to find the OVS port
	ifaceID := fmt.Sprintf("%s_%s", cfg.PodNamespace, cfg.PodName)

	// Find the OVS port by iface-id
	portName, err := findOVSPortByIfaceID(ifaceID)
	if err != nil {
		klog.V(4).Infof("OVS port not found for iface-id %s (may already be cleaned up): %v", ifaceID, err)
		return nil
	}

	// Remove OVS port
	if portName != "" {
		if err := removeOVSPort(portName); err != nil {
			klog.Warningf("Failed to remove OVS port %s: %v", portName, err)
		}

		// Delete veth pair
		cleanupVeth(portName)
	}

	klog.V(4).Infof("Cleaned up interface for pod %s/%s", cfg.PodNamespace, cfg.PodName)
	return nil
}

// findOVSPortByIfaceID finds an OVS port by its iface-id external_id
func findOVSPortByIfaceID(ifaceID string) (string, error) {
	// ovs-vsctl --columns=name find interface external_ids:iface-id=<ifaceID>
	cmd := exec.Command("ovs-vsctl", "--columns=name", "find", "interface",
		fmt.Sprintf("external_ids:iface-id=%s", ifaceID))
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to find OVS port: %w", err)
	}

	// Parse output: name                : "veth-xxxxx"
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "name") {
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				name := strings.TrimSpace(parts[1])
				name = strings.Trim(name, "\"")
				return name, nil
			}
		}
	}

	return "", fmt.Errorf("port not found for iface-id %s", ifaceID)
}

// removeOVSPort removes a port from OVS br-int
func removeOVSPort(portName string) error {
	cmd := exec.Command("ovs-vsctl", "--if-exists", "del-port", OVSBridge, portName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove OVS port %s: %w, output: %s", portName, err, string(output))
	}
	klog.V(4).Infof("Removed OVS port %s", portName)
	return nil
}

// cleanupVeth deletes a veth interface
func cleanupVeth(ifName string) {
	link, err := netlink.LinkByName(ifName)
	if err != nil {
		// Interface doesn't exist, nothing to clean up
		return
	}

	if err := netlink.LinkDel(link); err != nil {
		klog.Warningf("Failed to delete veth %s: %v", ifName, err)
	} else {
		klog.V(4).Infof("Deleted veth %s", ifName)
	}
}

// generateVethName generates a unique veth interface name
// Format: veth<random-hex>
// The name must be <= 15 characters (Linux interface name limit)
func generateVethName() (string, error) {
	// Generate 4 random bytes (8 hex characters)
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Format: veth + 8 hex chars = 12 chars (within 15 char limit)
	return fmt.Sprintf("%s%x", VethHostPrefix, b), nil
}

// CheckInterface verifies that a Pod's network interface is correctly configured
//
// This function:
// 1. Checks that the OVS port exists
// 2. Checks that the veth pair exists
// 3. Checks that the container interface has the correct IP
//
// Parameters:
//   - cfg: Interface configuration
//
// Returns:
//   - error: Check error if configuration is incorrect
func CheckInterface(cfg *InterfaceConfig) error {
	if cfg == nil {
		return fmt.Errorf("interface config is nil")
	}

	// Build the iface-id
	ifaceID := fmt.Sprintf("%s_%s", cfg.PodNamespace, cfg.PodName)

	// Check OVS port exists
	portName, err := findOVSPortByIfaceID(ifaceID)
	if err != nil {
		return fmt.Errorf("OVS port not found for iface-id %s: %w", ifaceID, err)
	}

	// Check host veth exists
	_, err = netlink.LinkByName(portName)
	if err != nil {
		return fmt.Errorf("host veth %s not found: %w", portName, err)
	}

	// Check container interface if netns is provided
	if cfg.NetNS != "" {
		containerNS, err := ns.GetNS(cfg.NetNS)
		if err != nil {
			return fmt.Errorf("failed to open network namespace %s: %w", cfg.NetNS, err)
		}
		defer containerNS.Close()

		err = containerNS.Do(func(_ ns.NetNS) error {
			link, err := netlink.LinkByName(cfg.IfName)
			if err != nil {
				return fmt.Errorf("container interface %s not found: %w", cfg.IfName, err)
			}

			// Check IP address if provided
			if cfg.IPAddress != "" {
				addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
				if err != nil {
					return fmt.Errorf("failed to list addresses on %s: %w", cfg.IfName, err)
				}

				expectedIP, _, err := net.ParseCIDR(cfg.IPAddress)
				if err != nil {
					return fmt.Errorf("invalid expected IP %s: %w", cfg.IPAddress, err)
				}

				found := false
				for _, addr := range addrs {
					if addr.IP.Equal(expectedIP) {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("expected IP %s not found on interface %s", expectedIP, cfg.IfName)
				}
			}

			return nil
		})
		if err != nil {
			return err
		}
	}

	klog.V(4).Infof("Interface check passed for pod %s/%s", cfg.PodNamespace, cfg.PodName)
	return nil
}

// init ensures we're running on Linux
func init() {
	if runtime.GOOS != "linux" {
		panic("this package only works on Linux")
	}
}
