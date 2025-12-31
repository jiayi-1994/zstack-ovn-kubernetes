// Package dpdk provides DPDK port stubs for non-Linux platforms.
//
// DPDK is only supported on Linux. This file provides stub implementations
// for other platforms to allow compilation.
//
//go:build !linux

package dpdk

import (
	"fmt"
	"runtime"
)

const (
	OVSBridge                   = "br-int"
	PortTypeDPDKVhostUserClient = "dpdkvhostuserclient"
	PortTypeDPDKVhostUser       = "dpdkvhostuser"
	DefaultSocketPermissions    = 0666
)

// DPDKPortConfig contains configuration for a DPDK vhost-user port
type DPDKPortConfig struct {
	PodNamespace string
	PodName      string
	ContainerID  string
	PortName     string
	SocketPath   string
	SocketMode   string
	Queues       int
	MACAddress   string
	MTU          int
}

// DPDKPortInfo contains information about a configured DPDK port
type DPDKPortInfo struct {
	PortName   string
	SocketPath string
	PortType   string
	MACAddress string
}

// PortManager manages DPDK vhost-user ports
type PortManager struct {
	config *DPDKConfig
}

// NewPortManager creates a new DPDK port manager
func NewPortManager(config *DPDKConfig) *PortManager {
	if config == nil {
		config = DefaultDPDKConfig()
	}
	return &PortManager{config: config}
}

// CreatePort returns an error on non-Linux platforms
func (m *PortManager) CreatePort(cfg *DPDKPortConfig) (*DPDKPortInfo, error) {
	return nil, fmt.Errorf("DPDK is not supported on %s", runtime.GOOS)
}

// DeletePort returns an error on non-Linux platforms
func (m *PortManager) DeletePort(namespace, podName string) error {
	return fmt.Errorf("DPDK is not supported on %s", runtime.GOOS)
}

// GetPortInfo returns an error on non-Linux platforms
func (m *PortManager) GetPortInfo(namespace, podName string) (*DPDKPortInfo, error) {
	return nil, fmt.Errorf("DPDK is not supported on %s", runtime.GOOS)
}

// PortExists returns false on non-Linux platforms
func (m *PortManager) PortExists(namespace, podName string) bool {
	return false
}

// GetSocketPath returns the socket path for a Pod
func (m *PortManager) GetSocketPath(namespace, podName string) string {
	return ""
}
