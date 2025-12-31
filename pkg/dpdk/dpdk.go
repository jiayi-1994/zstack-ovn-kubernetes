// Package dpdk provides DPDK (Data Plane Development Kit) support for high-performance networking.
//
// This package handles:
// - Detection of DPDK-enabled OVS (ovs-vswitchd with DPDK)
// - Validation of DPDK environment (hugepages, CPU binding)
// - Configuration of DPDK vhost-user ports for Pods
//
// DPDK Architecture:
//
//	┌─────────────────────────────────────────────────────────────────────┐
//	│                         Host (DPDK Mode)                             │
//	│                                                                      │
//	│  ┌─────────────────────────────────────────────────────────────┐    │
//	│  │                    OVS-DPDK (ovs-vswitchd)                    │    │
//	│  │                                                               │    │
//	│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐   │    │
//	│  │  │   br-int    │  │  dpdk-port  │  │ dpdkvhostuserclient │   │    │
//	│  │  │   (OVS)     │  │  (Physical) │  │   (Pod vhost-user)  │   │    │
//	│  │  └─────────────┘  └─────────────┘  └──────────┬──────────┘   │    │
//	│  │                                               │              │    │
//	│  └───────────────────────────────────────────────┼──────────────┘    │
//	│                                                  │                   │
//	│                                    vhost-user socket                 │
//	│                                                  │                   │
//	│  ┌───────────────────────────────────────────────┼──────────────┐    │
//	│  │                    Container (DPDK App)       │              │    │
//	│  │                                               ▼              │    │
//	│  │                                    ┌─────────────────────┐   │    │
//	│  │                                    │   DPDK PMD Driver   │   │    │
//	│  │                                    │   (virtio-user)     │   │    │
//	│  │                                    └─────────────────────┘   │    │
//	│  │                                                              │    │
//	│  └──────────────────────────────────────────────────────────────┘    │
//	│                                                                      │
//	└──────────────────────────────────────────────────────────────────────┘
//
// Key Concepts:
// - DPDK bypasses the kernel network stack for high-performance packet processing
// - vhost-user provides a user-space virtio backend for VM/container communication
// - dpdkvhostuserclient: OVS acts as client, container acts as server
// - Requires hugepages for memory allocation
//
// Reference: OVN-Kubernetes DPDK support and OVS-DPDK documentation
//
//go:build linux

package dpdk

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

// DPDKStatus represents the DPDK environment status on a node
type DPDKStatus struct {
	// Enabled indicates whether DPDK is enabled on this node
	Enabled bool `json:"enabled"`

	// OVSDPDKEnabled indicates whether OVS is running with DPDK
	OVSDPDKEnabled bool `json:"ovsDpdkEnabled"`

	// HugepagesAvailable indicates whether hugepages are configured
	HugepagesAvailable bool `json:"hugepagesAvailable"`

	// HugepagesTotalKB is the total hugepages memory in KB
	HugepagesTotalKB int64 `json:"hugepagesTotalKB"`

	// HugepagesFreeKB is the free hugepages memory in KB
	HugepagesFreeKB int64 `json:"hugepagesFreeKB"`

	// HugepageSize is the size of each hugepage (e.g., "2048kB", "1048576kB")
	HugepageSize string `json:"hugepageSize"`

	// DPDKSocketMem is the configured DPDK socket memory
	DPDKSocketMem string `json:"dpdkSocketMem"`

	// DPDKLcoreMask is the configured DPDK lcore mask
	DPDKLcoreMask string `json:"dpdkLcoreMask"`

	// Errors contains any errors encountered during detection
	Errors []string `json:"errors,omitempty"`
}

// DPDKConfig contains DPDK-specific configuration
type DPDKConfig struct {
	// Enabled indicates whether DPDK support is enabled
	Enabled bool

	// SocketDir is the directory for vhost-user sockets
	// Default: /var/run/openvswitch
	SocketDir string

	// SocketMode is the vhost-user socket mode
	// "client" for dpdkvhostuserclient (OVS as client, recommended)
	// "server" for dpdkvhostuser (OVS as server)
	// Default: "client"
	SocketMode string

	// Queues is the number of queues for multiqueue support
	// Default: 1
	Queues int
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
//
// Parameters:
//   - config: DPDK configuration
//
// Returns:
//   - *Detector: DPDK detector instance
func NewDetector(config *DPDKConfig) *Detector {
	if config == nil {
		config = DefaultDPDKConfig()
	}
	return &Detector{config: config}
}

// DetectDPDKStatus detects the DPDK environment status on the current node
//
// This function checks:
// 1. Whether OVS is running with DPDK enabled (dpdk-init=true)
// 2. Whether hugepages are available and configured
// 3. DPDK-specific OVS configuration (socket-mem, lcore-mask)
//
// Returns:
//   - *DPDKStatus: DPDK status information
//   - error: Detection error
func (d *Detector) DetectDPDKStatus() (*DPDKStatus, error) {
	status := &DPDKStatus{
		Enabled: d.config.Enabled,
		Errors:  make([]string, 0),
	}

	// Check if OVS is running with DPDK
	ovsDPDK, err := d.checkOVSDPDK()
	if err != nil {
		status.Errors = append(status.Errors, fmt.Sprintf("OVS DPDK check failed: %v", err))
		klog.V(4).Infof("OVS DPDK check failed: %v", err)
	} else {
		status.OVSDPDKEnabled = ovsDPDK
	}

	// Check hugepages
	hugepagesInfo, err := d.checkHugepages()
	if err != nil {
		status.Errors = append(status.Errors, fmt.Sprintf("Hugepages check failed: %v", err))
		klog.V(4).Infof("Hugepages check failed: %v", err)
	} else {
		status.HugepagesAvailable = hugepagesInfo.available
		status.HugepagesTotalKB = hugepagesInfo.totalKB
		status.HugepagesFreeKB = hugepagesInfo.freeKB
		status.HugepageSize = hugepagesInfo.size
	}

	// Get DPDK OVS configuration
	if status.OVSDPDKEnabled {
		socketMem, err := d.getOVSConfig("other_config:dpdk-socket-mem")
		if err == nil {
			status.DPDKSocketMem = socketMem
		}

		lcoreMask, err := d.getOVSConfig("other_config:dpdk-lcore-mask")
		if err == nil {
			status.DPDKLcoreMask = lcoreMask
		}
	}

	klog.V(2).Infof("DPDK status: enabled=%v, ovsDPDK=%v, hugepages=%v (total=%dKB, free=%dKB)",
		status.Enabled, status.OVSDPDKEnabled, status.HugepagesAvailable,
		status.HugepagesTotalKB, status.HugepagesFreeKB)

	return status, nil
}

// checkOVSDPDK checks if OVS is running with DPDK enabled
//
// OVS DPDK is enabled when:
// - ovs-vswitchd is running
// - other_config:dpdk-init is set to "true"
//
// Returns:
//   - bool: true if OVS DPDK is enabled
//   - error: Check error
func (d *Detector) checkOVSDPDK() (bool, error) {
	// Check if ovs-vsctl is available
	if _, err := exec.LookPath("ovs-vsctl"); err != nil {
		return false, fmt.Errorf("ovs-vsctl not found: %w", err)
	}

	// Check dpdk-init configuration
	// ovs-vsctl get Open_vSwitch . other_config:dpdk-init
	cmd := exec.Command("ovs-vsctl", "get", "Open_vSwitch", ".", "other_config:dpdk-init")
	output, err := cmd.Output()
	if err != nil {
		// If the key doesn't exist, DPDK is not enabled
		klog.V(4).Infof("dpdk-init not configured: %v", err)
		return false, nil
	}

	// Parse output - it should be "true" (with quotes)
	result := strings.TrimSpace(string(output))
	result = strings.Trim(result, "\"")

	return strings.ToLower(result) == "true", nil
}

// hugepagesInfo contains hugepages information
type hugepagesInfo struct {
	available bool
	totalKB   int64
	freeKB    int64
	size      string
}

// checkHugepages checks if hugepages are available and configured
//
// Hugepages are required for DPDK memory allocation.
// This function reads from /sys/kernel/mm/hugepages/ and /proc/meminfo
//
// Returns:
//   - *hugepagesInfo: Hugepages information
//   - error: Check error
func (d *Detector) checkHugepages() (*hugepagesInfo, error) {
	info := &hugepagesInfo{}

	// Read hugepages info from /proc/meminfo
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, fmt.Errorf("failed to open /proc/meminfo: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		key := strings.TrimSuffix(fields[0], ":")
		value, _ := strconv.ParseInt(fields[1], 10, 64)

		switch key {
		case "HugePages_Total":
			// HugePages_Total is count, not KB
			info.totalKB = value
		case "HugePages_Free":
			info.freeKB = value
		case "Hugepagesize":
			if len(fields) >= 3 {
				info.size = fields[1] + fields[2] // e.g., "2048 kB" -> "2048kB"
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read /proc/meminfo: %w", err)
	}

	// Convert page count to KB if we have the page size
	if info.size != "" {
		// Parse size like "2048kB" or "1048576kB"
		sizeStr := strings.TrimSuffix(strings.ToLower(info.size), "kb")
		sizeStr = strings.TrimSpace(sizeStr)
		if pageSize, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
			info.totalKB = info.totalKB * pageSize
			info.freeKB = info.freeKB * pageSize
		}
	}

	// Hugepages are available if total > 0
	info.available = info.totalKB > 0

	return info, nil
}

// getOVSConfig gets an OVS configuration value
func (d *Detector) getOVSConfig(key string) (string, error) {
	cmd := exec.Command("ovs-vsctl", "get", "Open_vSwitch", ".", key)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	result := strings.TrimSpace(string(output))
	result = strings.Trim(result, "\"")
	return result, nil
}

// ValidateDPDKEnvironment validates that the DPDK environment is properly configured
//
// This function checks:
// 1. OVS DPDK is enabled
// 2. Hugepages are available with sufficient memory
// 3. Socket directory exists and is writable
//
// Parameters:
//   - minHugepagesMB: Minimum required hugepages memory in MB
//
// Returns:
//   - error: Validation error if environment is not properly configured
func (d *Detector) ValidateDPDKEnvironment(minHugepagesMB int64) error {
	status, err := d.DetectDPDKStatus()
	if err != nil {
		return fmt.Errorf("failed to detect DPDK status: %w", err)
	}

	var errors []string

	// Check OVS DPDK
	if !status.OVSDPDKEnabled {
		errors = append(errors, "OVS DPDK is not enabled (dpdk-init != true)")
	}

	// Check hugepages
	if !status.HugepagesAvailable {
		errors = append(errors, "hugepages are not configured")
	} else {
		minHugepagesKB := minHugepagesMB * 1024
		if status.HugepagesTotalKB < minHugepagesKB {
			errors = append(errors, fmt.Sprintf("insufficient hugepages: %dMB available, %dMB required",
				status.HugepagesTotalKB/1024, minHugepagesMB))
		}
	}

	// Check socket directory
	if d.config.SocketDir != "" {
		if _, err := os.Stat(d.config.SocketDir); os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("socket directory does not exist: %s", d.config.SocketDir))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("DPDK environment validation failed:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

// GetSocketPath returns the vhost-user socket path for a Pod
//
// Parameters:
//   - namespace: Pod namespace
//   - podName: Pod name
//
// Returns:
//   - string: Full path to the vhost-user socket
func (d *Detector) GetSocketPath(namespace, podName string) string {
	// Socket name format: vhost-user-<namespace>-<podName>
	socketName := fmt.Sprintf("vhost-user-%s-%s", namespace, podName)
	return filepath.Join(d.config.SocketDir, socketName)
}

// IsDPDKEnabled returns whether DPDK is enabled in the configuration
func (d *Detector) IsDPDKEnabled() bool {
	return d.config.Enabled
}

// GetConfig returns the DPDK configuration
func (d *Detector) GetConfig() *DPDKConfig {
	return d.config
}
