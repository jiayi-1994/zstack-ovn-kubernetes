// Package config provides CNI configuration parsing.
//
// This file handles parsing of CNI configuration from:
// - CNI config file (/etc/cni/net.d/10-zstack-ovn.conflist)
// - CNI_ARGS environment variable
//
// Reference: OVN-Kubernetes pkg/config/cni.go
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/containernetworking/cni/pkg/types"
)

// CNIConfig represents the CNI configuration
// This is the configuration passed to the CNI plugin via stdin
type CNIConfig struct {
	types.NetConf

	// ServerSocket is the path to the CNI server Unix socket
	// Default: "/var/run/zstack-ovn/cni-server.sock"
	ServerSocket string `json:"serverSocket,omitempty"`

	// LogFile is the path to the CNI log file
	// Default: "/var/log/zstack-ovn/cni.log"
	LogFile string `json:"logFile,omitempty"`

	// LogLevel is the CNI log level
	// Default: "info"
	LogLevel string `json:"logLevel,omitempty"`

	// MTU is the MTU for Pod interfaces
	// Default: 1400
	MTU int `json:"mtu,omitempty"`
}

// CNIArgs represents the CNI_ARGS environment variable
// Format: K8S_POD_NAMESPACE=xxx;K8S_POD_NAME=yyy;K8S_POD_INFRA_CONTAINER_ID=zzz
type CNIArgs struct {
	// PodNamespace is the Pod's namespace
	PodNamespace string

	// PodName is the Pod's name
	PodName string

	// PodUID is the Pod's UID
	PodUID string

	// ContainerID is the container ID
	ContainerID string

	// SandboxID is the sandbox ID (same as ContainerID for most runtimes)
	SandboxID string
}

// DefaultCNIConfig returns a CNIConfig with default values
func DefaultCNIConfig() *CNIConfig {
	return &CNIConfig{
		ServerSocket: "/var/run/zstack-ovn/cni-server.sock",
		LogFile:      "/var/log/zstack-ovn/cni.log",
		LogLevel:     "info",
		MTU:          1400,
	}
}

// ParseCNIConfig parses CNI configuration from JSON bytes
//
// Parameters:
//   - data: JSON configuration data (from stdin)
//
// Returns:
//   - *CNIConfig: Parsed CNI configuration
//   - error: Parsing error
func ParseCNIConfig(data []byte) (*CNIConfig, error) {
	cfg := DefaultCNIConfig()

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse CNI config: %w", err)
	}

	// Apply defaults for empty values
	if cfg.ServerSocket == "" {
		cfg.ServerSocket = "/var/run/zstack-ovn/cni-server.sock"
	}
	if cfg.LogFile == "" {
		cfg.LogFile = "/var/log/zstack-ovn/cni.log"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.MTU == 0 {
		cfg.MTU = 1400
	}

	return cfg, nil
}

// ParseCNIArgs parses the CNI_ARGS environment variable
//
// The CNI_ARGS format is: KEY1=VALUE1;KEY2=VALUE2;...
// Common keys:
//   - K8S_POD_NAMESPACE: Pod namespace
//   - K8S_POD_NAME: Pod name
//   - K8S_POD_INFRA_CONTAINER_ID: Container ID
//   - K8S_POD_UID: Pod UID
//
// Returns:
//   - *CNIArgs: Parsed CNI arguments
//   - error: Parsing error
func ParseCNIArgs() (*CNIArgs, error) {
	argsStr := os.Getenv("CNI_ARGS")
	if argsStr == "" {
		return nil, fmt.Errorf("CNI_ARGS environment variable is not set")
	}

	args := &CNIArgs{}

	// Parse key=value pairs separated by semicolons
	pairs := strings.Split(argsStr, ";")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}

		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])

		switch key {
		case "K8S_POD_NAMESPACE":
			args.PodNamespace = value
		case "K8S_POD_NAME":
			args.PodName = value
		case "K8S_POD_UID":
			args.PodUID = value
		case "K8S_POD_INFRA_CONTAINER_ID":
			args.ContainerID = value
		case "K8S_POD_SANDBOX_ID":
			args.SandboxID = value
		}
	}

	// Validate required fields
	if args.PodNamespace == "" {
		return nil, fmt.Errorf("K8S_POD_NAMESPACE is required in CNI_ARGS")
	}
	if args.PodName == "" {
		return nil, fmt.Errorf("K8S_POD_NAME is required in CNI_ARGS")
	}

	return args, nil
}

// GenerateCNIConfigFile generates the CNI configuration file content
//
// Parameters:
//   - cfg: Global configuration
//
// Returns:
//   - []byte: CNI configuration file content (JSON)
//   - error: Generation error
func GenerateCNIConfigFile(cfg *Config) ([]byte, error) {
	cniConfig := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       "zstack-ovn",
		"plugins": []map[string]interface{}{
			{
				"type":         "zstack-ovn-cni",
				"serverSocket": "/var/run/zstack-ovn/cni-server.sock",
				"logFile":      "/var/log/zstack-ovn/cni.log",
				"logLevel":     cfg.Logging.Level,
				"mtu":          cfg.Network.MTU,
			},
		},
	}

	return json.MarshalIndent(cniConfig, "", "  ")
}

// WriteCNIConfigFile writes the CNI configuration file
//
// Parameters:
//   - cfg: Global configuration
//   - path: Path to write the configuration file
//
// Returns:
//   - error: Write error
func WriteCNIConfigFile(cfg *Config, path string) error {
	data, err := GenerateCNIConfigFile(cfg)
	if err != nil {
		return fmt.Errorf("failed to generate CNI config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write CNI config file: %w", err)
	}

	return nil
}

// CNIConfigListSpec represents a CNI config list (conflist format)
type CNIConfigListSpec struct {
	CNIVersion   string                   `json:"cniVersion"`
	Name         string                   `json:"name"`
	DisableCheck bool                     `json:"disableCheck,omitempty"`
	Plugins      []map[string]interface{} `json:"plugins"`
}

// ParseCNIConfigList parses a CNI config list from JSON bytes
//
// Parameters:
//   - data: JSON configuration data
//
// Returns:
//   - *CNIConfigListSpec: Parsed CNI config list
//   - error: Parsing error
func ParseCNIConfigList(data []byte) (*CNIConfigListSpec, error) {
	var configList CNIConfigListSpec
	if err := json.Unmarshal(data, &configList); err != nil {
		return nil, fmt.Errorf("failed to parse CNI config list: %w", err)
	}
	return &configList, nil
}
