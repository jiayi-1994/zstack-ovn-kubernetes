// Package cni provides stub implementations for non-Linux platforms.
//
// This file provides stub implementations of the Linux-specific functions
// to allow the package to compile on non-Linux platforms (e.g., for testing
// or development on macOS/Windows).
//
// These functions will return errors when called on non-Linux platforms.
//
//go:build !linux

package cni

import (
	"fmt"
	"runtime"
)

const (
	// DefaultMTU is the default MTU for Pod interfaces
	DefaultMTU = 1400

	// OVSBridge is the name of the OVS integration bridge
	OVSBridge = "br-int"

	// VethHostPrefix is the prefix for host-side veth interfaces
	VethHostPrefix = "veth"

	// ContainerIfName is the interface name inside the container
	ContainerIfName = "eth0"
)

// InterfaceConfig contains the configuration for a Pod's network interface
type InterfaceConfig struct {
	PodNamespace string
	PodName      string
	ContainerID  string
	NetNS        string
	IfName       string
	IPAddress    string
	MACAddress   string
	Gateway      string
	MTU          int
	OVSPortName  string
	PortUUID     string
}

// InterfaceInfo contains information about a configured interface
type InterfaceInfo struct {
	HostIfName      string
	ContainerIfName string
	MACAddress      string
}

// SetupInterface is a stub for non-Linux platforms
func SetupInterface(cfg *InterfaceConfig) (*InterfaceInfo, error) {
	return nil, fmt.Errorf("SetupInterface is only supported on Linux (current OS: %s)", runtime.GOOS)
}

// TeardownInterface is a stub for non-Linux platforms
func TeardownInterface(cfg *InterfaceConfig) error {
	return fmt.Errorf("TeardownInterface is only supported on Linux (current OS: %s)", runtime.GOOS)
}

// CheckInterface is a stub for non-Linux platforms
func CheckInterface(cfg *InterfaceConfig) error {
	return fmt.Errorf("CheckInterface is only supported on Linux (current OS: %s)", runtime.GOOS)
}
