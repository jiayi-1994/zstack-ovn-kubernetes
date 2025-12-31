// Package dpdk provides DPDK port configuration for high-performance networking.
//
// This file implements DPDK vhost-user port configuration for Pods.
// When DPDK is enabled, Pods use vhost-user sockets instead of veth pairs
// for communication with OVS-DPDK.
//
// Port Types:
// - dpdkvhostuserclient: OVS acts as client, container acts as server (recommended)
// - dpdkvhostuser: OVS acts as server, container acts as client
//
// The dpdkvhostuserclient mode is recommended because:
// 1. Container can start before OVS connects
// 2. Better compatibility with container lifecycle
// 3. Easier socket permission management
//
// Reference: OVS-DPDK vhost-user documentation
//
//go:build linux

package dpdk

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"
)

const (
	// OVSBridge is the name of the OVS integration bridge
	OVSBridge = "br-int"

	// PortTypeDPDKVhostUserClient is the OVS port type for vhost-user client mode
	// In this mode, OVS acts as the client and connects to the socket created by the container
	PortTypeDPDKVhostUserClient = "dpdkvhostuserclient"

	// PortTypeDPDKVhostUser is the OVS port type for vhost-user server mode
	// In this mode, OVS creates the socket and the container connects to it
	PortTypeDPDKVhostUser = "dpdkvhostuser"

	// DefaultSocketPermissions is the default permission for vhost-user sockets
	DefaultSocketPermissions = 0666
)

// DPDKPortConfig contains configuration for a DPDK vhost-user port
type DPDKPortConfig struct {
	// PodNamespace is the Pod's Kubernetes namespace
	PodNamespace string

	// PodName is the Pod's name
	PodName string

	// ContainerID is the container ID
	ContainerID string

	// PortName is the OVS port name
	PortName string

	// SocketPath is the full path to the vhost-user socket
	SocketPath string

	// SocketMode is the vhost-user mode: "client" or "server"
	// "client" -> dpdkvhostuserclient (recommended)
	// "server" -> dpdkvhostuser
	SocketMode string

	// Queues is the number of queues for multiqueue support
	Queues int

	// MACAddress is the MAC address for the port
	MACAddress string

	// MTU is the MTU for the port
	MTU int
}

// DPDKPortInfo contains information about a configured DPDK port
type DPDKPortInfo struct {
	// PortName is the OVS port name
	PortName string

	// SocketPath is the vhost-user socket path
	SocketPath string

	// PortType is the OVS port type (dpdkvhostuserclient or dpdkvhostuser)
	PortType string

	// MACAddress is the MAC address assigned to the port
	MACAddress string
}

// PortManager manages DPDK vhost-user ports
type PortManager struct {
	config *DPDKConfig
}

// NewPortManager creates a new DPDK port manager
//
// Parameters:
//   - config: DPDK configuration
//
// Returns:
//   - *PortManager: Port manager instance
func NewPortManager(config *DPDKConfig) *PortManager {
	if config == nil {
		config = DefaultDPDKConfig()
	}
	return &PortManager{config: config}
}

// CreatePort creates a DPDK vhost-user port for a Pod
//
// This function:
// 1. Generates the socket path
// 2. Creates the socket directory if needed
// 3. Adds the dpdkvhostuserclient port to OVS br-int
// 4. Sets the external_ids for OVN integration
//
// Parameters:
//   - cfg: Port configuration
//
// Returns:
//   - *DPDKPortInfo: Information about the created port
//   - error: Creation error
func (m *PortManager) CreatePort(cfg *DPDKPortConfig) (*DPDKPortInfo, error) {
	if cfg == nil {
		return nil, fmt.Errorf("port config is nil")
	}

	// Validate required fields
	if cfg.PodNamespace == "" || cfg.PodName == "" {
		return nil, fmt.Errorf("pod namespace and name are required")
	}

	// Generate port name if not provided
	if cfg.PortName == "" {
		cfg.PortName = m.generatePortName(cfg.PodNamespace, cfg.PodName)
	}

	// Generate socket path if not provided
	if cfg.SocketPath == "" {
		cfg.SocketPath = m.generateSocketPath(cfg.PodNamespace, cfg.PodName)
	}

	// Set defaults
	if cfg.SocketMode == "" {
		cfg.SocketMode = m.config.SocketMode
	}
	if cfg.Queues == 0 {
		cfg.Queues = m.config.Queues
	}

	// Determine port type based on socket mode
	portType := PortTypeDPDKVhostUserClient
	if cfg.SocketMode == "server" {
		portType = PortTypeDPDKVhostUser
	}

	klog.V(4).Infof("Creating DPDK port for pod %s/%s: port=%s, socket=%s, type=%s",
		cfg.PodNamespace, cfg.PodName, cfg.PortName, cfg.SocketPath, portType)

	// Ensure socket directory exists
	socketDir := filepath.Dir(cfg.SocketPath)
	if err := os.MkdirAll(socketDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create socket directory %s: %w", socketDir, err)
	}

	// Build the iface-id (OVN Logical Switch Port name)
	ifaceID := fmt.Sprintf("%s_%s", cfg.PodNamespace, cfg.PodName)

	// Add DPDK port to OVS br-int
	// ovs-vsctl add-port br-int <portName> \
	//   -- set interface <portName> type=dpdkvhostuserclient options:vhost-server-path=<socketPath> \
	//   -- set interface <portName> external_ids:iface-id=<ifaceID>
	args := []string{
		"--may-exist", "add-port", OVSBridge, cfg.PortName,
		"--", "set", "interface", cfg.PortName,
		fmt.Sprintf("type=%s", portType),
		fmt.Sprintf("options:vhost-server-path=%s", cfg.SocketPath),
	}

	// Add multiqueue support if queues > 1
	if cfg.Queues > 1 {
		args = append(args, fmt.Sprintf("options:n_rxq=%d", cfg.Queues))
		args = append(args, fmt.Sprintf("options:n_txq=%d", cfg.Queues))
	}

	// Add external_ids for OVN integration
	args = append(args,
		"--", "set", "interface", cfg.PortName,
		fmt.Sprintf("external_ids:iface-id=%s", ifaceID),
		fmt.Sprintf("external_ids:sandbox=%s", cfg.ContainerID),
	)

	// Add MAC address if provided
	if cfg.MACAddress != "" {
		args = append(args, fmt.Sprintf("external_ids:attached-mac=%s", cfg.MACAddress))
	}

	klog.V(4).Infof("Adding DPDK OVS port: ovs-vsctl %s", strings.Join(args, " "))

	cmd := exec.Command("ovs-vsctl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to add DPDK OVS port: %w, output: %s", err, string(output))
	}

	// For server mode, OVS creates the socket, so we need to set permissions
	if portType == PortTypeDPDKVhostUser {
		if err := os.Chmod(cfg.SocketPath, DefaultSocketPermissions); err != nil {
			klog.Warningf("Failed to set socket permissions for %s: %v", cfg.SocketPath, err)
		}
	}

	klog.Infof("Created DPDK port %s with socket %s for pod %s/%s",
		cfg.PortName, cfg.SocketPath, cfg.PodNamespace, cfg.PodName)

	return &DPDKPortInfo{
		PortName:   cfg.PortName,
		SocketPath: cfg.SocketPath,
		PortType:   portType,
		MACAddress: cfg.MACAddress,
	}, nil
}

// DeletePort removes a DPDK vhost-user port
//
// This function:
// 1. Removes the OVS port from br-int
// 2. Removes the vhost-user socket file
//
// Parameters:
//   - namespace: Pod namespace
//   - podName: Pod name
//
// Returns:
//   - error: Deletion error (nil if already deleted)
func (m *PortManager) DeletePort(namespace, podName string) error {
	portName := m.generatePortName(namespace, podName)
	socketPath := m.generateSocketPath(namespace, podName)

	klog.V(4).Infof("Deleting DPDK port for pod %s/%s: port=%s", namespace, podName, portName)

	// Remove OVS port
	cmd := exec.Command("ovs-vsctl", "--if-exists", "del-port", OVSBridge, portName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Warningf("Failed to remove DPDK OVS port %s: %v, output: %s", portName, err, string(output))
	} else {
		klog.V(4).Infof("Removed DPDK OVS port %s", portName)
	}

	// Remove socket file
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		klog.Warningf("Failed to remove socket file %s: %v", socketPath, err)
	} else if err == nil {
		klog.V(4).Infof("Removed socket file %s", socketPath)
	}

	return nil
}

// GetPortInfo retrieves information about an existing DPDK port
//
// Parameters:
//   - namespace: Pod namespace
//   - podName: Pod name
//
// Returns:
//   - *DPDKPortInfo: Port information
//   - error: Error if port not found
func (m *PortManager) GetPortInfo(namespace, podName string) (*DPDKPortInfo, error) {
	portName := m.generatePortName(namespace, podName)

	// Check if port exists
	// ovs-vsctl get interface <portName> type
	cmd := exec.Command("ovs-vsctl", "get", "interface", portName, "type")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("port %s not found: %w", portName, err)
	}

	portType := strings.TrimSpace(string(output))
	portType = strings.Trim(portType, "\"")

	// Get socket path
	cmd = exec.Command("ovs-vsctl", "get", "interface", portName, "options:vhost-server-path")
	output, err = cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get socket path for port %s: %w", portName, err)
	}

	socketPath := strings.TrimSpace(string(output))
	socketPath = strings.Trim(socketPath, "\"")

	// Get MAC address
	cmd = exec.Command("ovs-vsctl", "get", "interface", portName, "external_ids:attached-mac")
	output, _ = cmd.Output() // MAC might not be set
	macAddress := strings.TrimSpace(string(output))
	macAddress = strings.Trim(macAddress, "\"")

	return &DPDKPortInfo{
		PortName:   portName,
		SocketPath: socketPath,
		PortType:   portType,
		MACAddress: macAddress,
	}, nil
}

// PortExists checks if a DPDK port exists for a Pod
//
// Parameters:
//   - namespace: Pod namespace
//   - podName: Pod name
//
// Returns:
//   - bool: true if port exists
func (m *PortManager) PortExists(namespace, podName string) bool {
	portName := m.generatePortName(namespace, podName)

	cmd := exec.Command("ovs-vsctl", "port-to-br", portName)
	err := cmd.Run()
	return err == nil
}

// generatePortName generates the OVS port name for a Pod
// Format: dpdk-<namespace>-<podName> (truncated to 15 chars for interface name limit)
func (m *PortManager) generatePortName(namespace, podName string) string {
	// OVS port names can be longer than Linux interface names
	// but we keep it reasonable
	name := fmt.Sprintf("dpdk-%s-%s", namespace, podName)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// generateSocketPath generates the vhost-user socket path for a Pod
func (m *PortManager) generateSocketPath(namespace, podName string) string {
	socketName := fmt.Sprintf("vhost-user-%s-%s", namespace, podName)
	return filepath.Join(m.config.SocketDir, socketName)
}

// GetSocketPath returns the socket path for a Pod
func (m *PortManager) GetSocketPath(namespace, podName string) string {
	return m.generateSocketPath(namespace, podName)
}
