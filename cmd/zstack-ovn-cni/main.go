// Package main provides the entry point for zstack-ovn-cni.
//
// zstack-ovn-cni is the CNI binary that:
// - Implements the CNI specification (ADD/DEL/CHECK/VERSION commands)
// - Communicates with zstack-ovnkube-node via Unix Socket
// - Is invoked by container runtime (containerd, CRI-O) during Pod creation/deletion
//
// The binary is installed at /opt/cni/bin/zstack-ovn-cni
//
// CNI Specification:
// The CNI (Container Network Interface) specification defines how container
// runtimes interact with network plugins. The runtime calls the CNI binary
// with specific environment variables and passes configuration via stdin.
//
// Environment Variables (set by container runtime):
// - CNI_COMMAND: The operation to perform (ADD, DEL, CHECK, VERSION)
// - CNI_CONTAINERID: Container ID
// - CNI_NETNS: Path to network namespace (e.g., /var/run/netns/cni-xxxxx)
// - CNI_IFNAME: Interface name to create (usually "eth0")
// - CNI_ARGS: Additional arguments (K8S_POD_NAMESPACE, K8S_POD_NAME, etc.)
// - CNI_PATH: Path to CNI plugin binaries
//
// Configuration (via stdin):
// The CNI configuration is passed as JSON via stdin. For zstack-ovn-cni:
//
//	{
//	  "cniVersion": "1.0.0",
//	  "name": "zstack-ovn",
//	  "type": "zstack-ovn-cni",
//	  "serverSocket": "/var/run/zstack-ovn/cni-server.sock",
//	  "logFile": "/var/log/zstack-ovn/cni.log",
//	  "logLevel": "info",
//	  "mtu": 1400
//	}
//
// Architecture:
//
//	┌─────────────────────────────────────────────────────────────────────┐
//	│                        Container Runtime                             │
//	│                    (containerd, CRI-O, etc.)                        │
//	└───────────────────────────────┬─────────────────────────────────────┘
//	                                │
//	                                │ Executes CNI binary with
//	                                │ env vars and stdin config
//	                                ▼
//	┌─────────────────────────────────────────────────────────────────────┐
//	│                        zstack-ovn-cni                                │
//	│                    /opt/cni/bin/zstack-ovn-cni                       │
//	│                                                                      │
//	│  1. Parse CNI config from stdin                                      │
//	│  2. Parse CNI_ARGS for Pod info                                      │
//	│  3. Send request to CNI Server via Unix Socket                       │
//	│  4. Return result to container runtime                               │
//	└───────────────────────────────┬─────────────────────────────────────┘
//	                                │
//	                                │ HTTP over Unix Socket
//	                                ▼
//	┌─────────────────────────────────────────────────────────────────────┐
//	│                        CNI Server                                    │
//	│              (running in zstack-ovnkube-node DaemonSet)             │
//	│                                                                      │
//	│  - Waits for Pod annotation (IP/MAC/Gateway)                        │
//	│  - Creates OVN Logical Switch Port                                   │
//	│  - Configures veth pair and OVS port                                │
//	│  - Returns CNI result                                                │
//	└─────────────────────────────────────────────────────────────────────┘
//
// Reference: OVN-Kubernetes cmd/ovn-k8s-cni-overlay/ovn-k8s-cni-overlay.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/version"

	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/cni"
)

const (
	// PluginName is the name of the CNI plugin
	PluginName = "zstack-ovn-cni"

	// DefaultLogFile is the default log file path
	DefaultLogFile = "/var/log/zstack-ovn/cni.log"
)

// Supported CNI versions
// We support CNI spec versions 0.3.0 through 1.0.0
var supportedVersions = version.PluginSupports("0.3.0", "0.3.1", "0.4.0", "1.0.0")

func main() {
	// Ensure we're running on Linux
	if runtime.GOOS != "linux" {
		fmt.Fprintf(os.Stderr, "zstack-ovn-cni only supports Linux\n")
		os.Exit(1)
	}

	// Initialize logging
	// CNI plugins should log to a file, not stdout/stderr
	// (stdout is used for CNI result, stderr for errors)
	initLogging()

	// Register CNI plugin with the CNI framework
	// The framework handles:
	// - Parsing CNI_COMMAND environment variable
	// - Calling the appropriate handler (ADD, DEL, CHECK)
	// - Formatting errors according to CNI spec
	skel.PluginMain(
		cni.CmdAdd,       // ADD command handler
		cni.CmdCheck,     // CHECK command handler
		cni.CmdDel,       // DEL command handler
		supportedVersions, // Supported CNI versions
		fmt.Sprintf("zstack-ovn-cni CNI plugin %s", getVersion()),
	)
}

// initLogging initializes logging for the CNI plugin
// CNI plugins should not write to stdout (used for result) or stderr (used for errors)
// Instead, we log to a file
func initLogging() {
	// Create log directory if it doesn't exist
	logDir := filepath.Dir(DefaultLogFile)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		// Can't create log directory, logging will be disabled
		return
	}

	// Open log file for appending
	logFile, err := os.OpenFile(DefaultLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		// Can't open log file, logging will be disabled
		return
	}

	// Note: We don't close the file here because the process will exit soon
	// and the OS will clean up. This is intentional for CNI plugins.
	_ = logFile
}

// getVersion returns the version of the CNI plugin
// This is used in the VERSION command response
func getVersion() string {
	// TODO: Set this from build flags
	return "0.1.0"
}
