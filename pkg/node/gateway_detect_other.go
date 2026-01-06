//go:build !linux
// +build !linux

// Package node provides gateway auto-detection functionality.
// This is a stub file for non-Linux platforms.
package node

import (
	"fmt"
	"net"

	corev1 "k8s.io/api/core/v1"
)

// L3GatewayConfig represents the gateway configuration for a node.
type L3GatewayConfig struct {
	Mode           string   `json:"mode"`
	ChassisID      string   `json:"chassis-id,omitempty"`
	InterfaceID    string   `json:"interface-id,omitempty"`
	BridgeID       string   `json:"bridge-id,omitempty"`
	MACAddress     string   `json:"mac-address,omitempty"`
	IPAddresses    []string `json:"ip-addresses,omitempty"`
	NextHops       []string `json:"next-hops,omitempty"`
	NodePortEnable bool     `json:"node-port-enable,omitempty"`
	VLANID         int      `json:"vlan-id,omitempty"`
}

// L3GatewayAnnotation is the annotation key for L3 gateway config
const L3GatewayAnnotation = "zstack.io/l3-gateway-config"

// ChassisIDAnnotation is the annotation key for chassis ID
const ChassisIDAnnotation = "zstack.io/node-chassis-id"

// GatewayInfo contains auto-detected gateway information
type GatewayInfo struct {
	InterfaceName string
	GatewayIP     net.IP
	NodeIP        net.IP
	NodeIPNet     *net.IPNet
	BridgeName    string
	MACAddress    net.HardwareAddr
}

// DetectGatewayInfo is not supported on non-Linux platforms.
func DetectGatewayInfo() (*GatewayInfo, error) {
	return nil, fmt.Errorf("gateway detection is only supported on Linux")
}

// GetOrCreateExternalBridge is not supported on non-Linux platforms.
func GetOrCreateExternalBridge(bridgeName, physicalIntf string) error {
	return fmt.Errorf("OVS bridge management is only supported on Linux")
}

// EnsureBridgeMapping is not supported on non-Linux platforms.
func EnsureBridgeMapping(physicalNetwork, bridgeName string) error {
	return fmt.Errorf("OVS bridge mapping is only supported on Linux")
}

// AutoConfigureGateway is not supported on non-Linux platforms.
func AutoConfigureGateway(physicalNetwork string) (*GatewayInfo, error) {
	return nil, fmt.Errorf("gateway configuration is only supported on Linux")
}

// MigrateInterfaceToBridge is deprecated and not supported on non-Linux platforms.
func MigrateInterfaceToBridge(physicalIntf, bridgeName string) error {
	return fmt.Errorf("interface migration is not supported on non-Linux platforms")
}

// EnsureExternalBridgeWithInterface is deprecated.
func EnsureExternalBridgeWithInterface(physicalNetwork string) (*GatewayInfo, error) {
	return AutoConfigureGateway(physicalNetwork)
}

// BuildL3GatewayConfig builds an L3GatewayConfig from detected gateway info.
func BuildL3GatewayConfig(info *GatewayInfo, chassisID string, vlanID int) *L3GatewayConfig {
	return &L3GatewayConfig{
		Mode:      "disabled",
		ChassisID: chassisID,
		VLANID:    vlanID,
	}
}

// SetL3GatewayAnnotation sets the L3 gateway config annotation on a node.
func SetL3GatewayAnnotation(node *corev1.Node, cfg *L3GatewayConfig) error {
	return fmt.Errorf("L3 gateway annotation is only supported on Linux")
}

// ParseL3GatewayAnnotation parses the L3 gateway config from node annotations.
func ParseL3GatewayAnnotation(node *corev1.Node) (*L3GatewayConfig, error) {
	return nil, fmt.Errorf("L3 gateway annotation parsing is only supported on Linux")
}

// GetChassisID gets the OVN chassis ID for this node.
func GetChassisID() (string, error) {
	return "", fmt.Errorf("chassis ID retrieval is only supported on Linux")
}
