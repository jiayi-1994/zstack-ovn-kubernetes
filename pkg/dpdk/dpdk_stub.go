// Package dpdk provides DPDK support stubs for non-Linux platforms.
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

// DPDKStatus represents the DPDK environment status on a node
type DPDKStatus struct {
	Enabled            bool     `json:"enabled"`
	OVSDPDKEnabled     bool     `json:"ovsDpdkEnabled"`
	HugepagesAvailable bool     `json:"hugepagesAvailable"`
	HugepagesTotalKB   int64    `json:"hugepagesTotalKB"`
	HugepagesFreeKB    int64    `json:"hugepagesFreeKB"`
	HugepageSize       string   `json:"hugepageSize"`
	DPDKSocketMem      string   `json:"dpdkSocketMem"`
	DPDKLcoreMask      string   `json:"dpdkLcoreMask"`
	Errors             []string `json:"errors,omitempty"`
}

// DPDKConfig contains DPDK-specific configuration
type DPDKConfig struct {
	Enabled    bool
	SocketDir  string
	SocketMode string
	Queues     int
}

// DefaultDPDKConfig returns the default DPDK configuration
func DefaultDPDKConfig() *DPDKConfig {
	return &DPDKConfig{
		Enabled:    false,
		SocketDir:  "/var/run/openvswitch",
		SocketMode: "client",
		Queues:     1,
	}
}

// Detector provides DPDK environment detection capabilities
type Detector struct {
	config *DPDKConfig
}

// NewDetector creates a new DPDK detector
func NewDetector(config *DPDKConfig) *Detector {
	if config == nil {
		config = DefaultDPDKConfig()
	}
	return &Detector{config: config}
}

// DetectDPDKStatus returns an error on non-Linux platforms
func (d *Detector) DetectDPDKStatus() (*DPDKStatus, error) {
	return nil, fmt.Errorf("DPDK is not supported on %s", runtime.GOOS)
}

// ValidateDPDKEnvironment returns an error on non-Linux platforms
func (d *Detector) ValidateDPDKEnvironment(minHugepagesMB int64) error {
	return fmt.Errorf("DPDK is not supported on %s", runtime.GOOS)
}

// GetSocketPath returns the vhost-user socket path for a Pod
func (d *Detector) GetSocketPath(namespace, podName string) string {
	return ""
}

// IsDPDKEnabled returns whether DPDK is enabled
func (d *Detector) IsDPDKEnabled() bool {
	return false
}

// GetConfig returns the DPDK configuration
func (d *Detector) GetConfig() *DPDKConfig {
	return d.config
}
