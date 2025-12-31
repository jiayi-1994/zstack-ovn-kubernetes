// Package config provides configuration management for zstack-ovn-kubernetes.
//
// This package handles:
// - Configuration file parsing (YAML/JSON)
// - Environment variable overrides
// - Configuration validation
// - Support for standalone and external deployment modes
//
// Configuration Priority (highest to lowest):
// 1. Environment variables (ZSTACK_OVN_*)
// 2. Configuration file
// 3. Default values
//
// Reference: OVN-Kubernetes pkg/config/config.go
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the global configuration structure
// It contains all configuration options for zstack-ovn-kubernetes
type Config struct {
	// OVN contains OVN database connection settings
	OVN OVNConfig `json:"ovn" yaml:"ovn"`

	// Kubernetes contains Kubernetes-related settings
	Kubernetes KubernetesConfig `json:"kubernetes" yaml:"kubernetes"`

	// Network contains network configuration
	Network NetworkConfig `json:"network" yaml:"network"`

	// Gateway contains gateway configuration
	Gateway GatewayConfig `json:"gateway" yaml:"gateway"`

	// Tunnel contains tunnel configuration
	Tunnel TunnelConfig `json:"tunnel" yaml:"tunnel"`

	// DPDK contains DPDK configuration
	DPDK DPDKConfig `json:"dpdk" yaml:"dpdk"`

	// Logging contains logging configuration
	Logging LoggingConfig `json:"logging" yaml:"logging"`
}

// OVNConfig contains OVN database connection settings
type OVNConfig struct {
	// Mode is the deployment mode: "standalone" or "external"
	// - standalone: Self-managed OVN databases (CNI manages NB/SB DB and northd)
	// - external: Connect to external OVN databases (e.g., ZStack-managed)
	// Default: "standalone"
	Mode string `json:"mode" yaml:"mode"`

	// NBDBAddress is the Northbound Database address
	// Format: tcp:IP:PORT or ssl:IP:PORT
	// Example: "tcp:192.168.1.100:6641"
	// Required for external mode
	NBDBAddress string `json:"nbdbAddress" yaml:"nbdbAddress"`

	// SBDBAddress is the Southbound Database address
	// Format: tcp:IP:PORT or ssl:IP:PORT
	// Example: "tcp:192.168.1.100:6642"
	// Required for external mode
	SBDBAddress string `json:"sbdbAddress" yaml:"sbdbAddress"`

	// SSL contains SSL/TLS configuration for secure connections
	SSL SSLConfig `json:"ssl" yaml:"ssl"`

	// ConnectTimeout is the timeout for initial connection
	// Default: 30s
	ConnectTimeout time.Duration `json:"connectTimeout" yaml:"connectTimeout"`

	// ReconnectInterval is the initial interval between reconnection attempts
	// Uses exponential backoff up to MaxReconnectInterval
	// Default: 1s
	ReconnectInterval time.Duration `json:"reconnectInterval" yaml:"reconnectInterval"`

	// MaxReconnectInterval is the maximum interval between reconnection attempts
	// Default: 60s
	MaxReconnectInterval time.Duration `json:"maxReconnectInterval" yaml:"maxReconnectInterval"`
}

// SSLConfig contains SSL/TLS configuration
type SSLConfig struct {
	// Enabled indicates whether SSL is enabled
	Enabled bool `json:"enabled" yaml:"enabled"`

	// CACert is the path to the CA certificate file
	CACert string `json:"caCert" yaml:"caCert"`

	// ClientCert is the path to the client certificate file
	ClientCert string `json:"clientCert" yaml:"clientCert"`

	// ClientKey is the path to the client private key file
	ClientKey string `json:"clientKey" yaml:"clientKey"`
}

// KubernetesConfig contains Kubernetes-related settings
type KubernetesConfig struct {
	// Kubeconfig is the path to kubeconfig file
	// If empty, uses in-cluster config
	Kubeconfig string `json:"kubeconfig" yaml:"kubeconfig"`

	// APIServer is the Kubernetes API server address
	// If empty, uses in-cluster config
	APIServer string `json:"apiServer" yaml:"apiServer"`

	// ServiceAccount is the service account name
	// Default: "zstack-ovn-kubernetes"
	ServiceAccount string `json:"serviceAccount" yaml:"serviceAccount"`
}

// NetworkConfig contains network configuration
type NetworkConfig struct {
	// ClusterCIDR is the Pod network CIDR
	// Example: "10.244.0.0/16"
	// Default: "10.244.0.0/16"
	ClusterCIDR string `json:"clusterCIDR" yaml:"clusterCIDR"`

	// ServiceCIDR is the Service network CIDR
	// Example: "10.96.0.0/16"
	// Default: "10.96.0.0/16"
	ServiceCIDR string `json:"serviceCIDR" yaml:"serviceCIDR"`

	// NodeSubnetSize is the subnet prefix length for each node
	// Example: 24 means each node gets a /24 subnet
	// Default: 24
	NodeSubnetSize int `json:"nodeSubnetSize" yaml:"nodeSubnetSize"`

	// MTU is the MTU for Pod interfaces
	// Default: 1400 (accounting for VXLAN overhead)
	MTU int `json:"mtu" yaml:"mtu"`
}

// GatewayConfig contains gateway configuration
type GatewayConfig struct {
	// Mode is the gateway mode: "shared" or "local"
	// - shared: Centralized gateway on specific nodes
	// - local: Distributed gateway on each node
	// Default: "local"
	Mode string `json:"mode" yaml:"mode"`

	// Interface is the network interface for external traffic
	// Example: "eth0", "bond0"
	Interface string `json:"interface" yaml:"interface"`

	// NextHop is the next hop IP for external traffic
	// If empty, uses the default gateway
	NextHop string `json:"nextHop" yaml:"nextHop"`

	// VLANID is the VLAN ID for external traffic (optional)
	VLANID int `json:"vlanID" yaml:"vlanID"`
}

// TunnelConfig contains tunnel configuration
type TunnelConfig struct {
	// Type is the tunnel type: "vxlan" or "geneve"
	// VXLAN is recommended for ZStack compatibility
	// Default: "vxlan"
	Type string `json:"type" yaml:"type"`

	// Port is the UDP port for tunnel traffic
	// Default: 4789 for VXLAN, 6081 for Geneve
	Port int `json:"port" yaml:"port"`

	// EncapIP is the IP address for tunnel encapsulation
	// If empty, uses the node's primary IP
	EncapIP string `json:"encapIP" yaml:"encapIP"`
}

// DPDKConfig contains DPDK configuration
// DPDK (Data Plane Development Kit) enables high-performance packet processing
// by bypassing the kernel network stack.
//
// Prerequisites for DPDK:
// 1. Hugepages must be configured on the node
// 2. OVS must be running with DPDK enabled (dpdk-init=true)
// 3. Appropriate CPU cores must be allocated for DPDK
type DPDKConfig struct {
	// Enabled indicates whether DPDK is enabled
	// When enabled, Pods on DPDK-capable nodes will use vhost-user sockets
	// instead of veth pairs for network connectivity.
	// Default: false
	Enabled bool `json:"enabled" yaml:"enabled"`

	// SocketDir is the directory for vhost-user sockets
	// This directory must be accessible by both OVS and the container runtime.
	// Default: "/var/run/openvswitch"
	SocketDir string `json:"socketDir" yaml:"socketDir"`

	// SocketMode is the vhost-user socket mode
	// "client" - OVS acts as client, container acts as server (dpdkvhostuserclient)
	// "server" - OVS acts as server, container acts as client (dpdkvhostuser)
	// The "client" mode is recommended for better container lifecycle management.
	// Default: "client"
	SocketMode string `json:"socketMode" yaml:"socketMode"`

	// Queues is the number of queues for multiqueue support
	// Higher values can improve performance with multiple CPU cores.
	// Default: 1
	Queues int `json:"queues" yaml:"queues"`

	// MinHugepagesMB is the minimum required hugepages memory in MB
	// Used for validation during DPDK environment checks.
	// Default: 1024 (1GB)
	MinHugepagesMB int64 `json:"minHugepagesMB" yaml:"minHugepagesMB"`
}

// LoggingConfig contains logging configuration
type LoggingConfig struct {
	// Level is the log level: "debug", "info", "warn", "error"
	// Default: "info"
	Level string `json:"level" yaml:"level"`

	// Format is the log format: "json" or "text"
	// Default: "json"
	Format string `json:"format" yaml:"format"`

	// File is the log file path (optional)
	// If empty, logs to stdout
	File string `json:"file" yaml:"file"`
}

// DefaultConfig returns a Config with default values
func DefaultConfig() *Config {
	return &Config{
		OVN: OVNConfig{
			Mode:                 "standalone",
			ConnectTimeout:       30 * time.Second,
			ReconnectInterval:    1 * time.Second,
			MaxReconnectInterval: 60 * time.Second,
		},
		Kubernetes: KubernetesConfig{
			ServiceAccount: "zstack-ovn-kubernetes",
		},
		Network: NetworkConfig{
			ClusterCIDR:    "10.244.0.0/16",
			ServiceCIDR:    "10.96.0.0/16",
			NodeSubnetSize: 24,
			MTU:            1400,
		},
		Gateway: GatewayConfig{
			Mode: "local",
		},
		Tunnel: TunnelConfig{
			Type: "vxlan",
			Port: 4789,
		},
		DPDK: DPDKConfig{
			Enabled:        false,
			SocketDir:      "/var/run/openvswitch",
			SocketMode:     "client",
			Queues:         1,
			MinHugepagesMB: 1024,
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}
}

// LoadConfig loads configuration from file and environment variables
//
// Configuration is loaded in the following order:
// 1. Default values
// 2. Configuration file (if specified via ZSTACK_OVN_CONFIG_FILE env var)
// 3. Environment variable overrides
//
// Returns:
//   - *Config: Loaded configuration
//   - error: Loading or validation error
func LoadConfig() (*Config, error) {
	cfg := DefaultConfig()

	// Load from config file if specified
	configFile := os.Getenv("ZSTACK_OVN_CONFIG_FILE")
	if configFile != "" {
		if err := cfg.LoadFromFile(configFile); err != nil {
			return nil, fmt.Errorf("failed to load config file %s: %w", configFile, err)
		}
	}

	// Apply environment variable overrides
	cfg.ApplyEnvOverrides()

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return cfg, nil
}

// LoadFromFile loads configuration from a YAML or JSON file
//
// Parameters:
//   - path: Path to the configuration file
//
// Returns:
//   - error: File reading or parsing error
func (c *Config) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Try YAML first (also handles JSON since YAML is a superset)
	if err := yaml.Unmarshal(data, c); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	return nil
}

// ApplyEnvOverrides applies environment variable overrides to the configuration
//
// Environment variables follow the pattern: ZSTACK_OVN_<SECTION>_<KEY>
// Examples:
//   - ZSTACK_OVN_MODE=external
//   - ZSTACK_OVN_NBDB_ADDRESS=tcp:192.168.1.100:6641
//   - ZSTACK_OVN_SBDB_ADDRESS=tcp:192.168.1.100:6642
//   - ZSTACK_OVN_CLUSTER_CIDR=10.244.0.0/16
//   - ZSTACK_OVN_SERVICE_CIDR=10.96.0.0/16
//   - ZSTACK_OVN_TUNNEL_TYPE=vxlan
//   - ZSTACK_OVN_GATEWAY_MODE=local
//   - ZSTACK_OVN_LOG_LEVEL=debug
func (c *Config) ApplyEnvOverrides() {
	// OVN settings
	if v := os.Getenv("ZSTACK_OVN_MODE"); v != "" {
		c.OVN.Mode = v
	}
	if v := os.Getenv("ZSTACK_OVN_NBDB_ADDRESS"); v != "" {
		c.OVN.NBDBAddress = v
	}
	if v := os.Getenv("ZSTACK_OVN_SBDB_ADDRESS"); v != "" {
		c.OVN.SBDBAddress = v
	}

	// SSL settings
	if v := os.Getenv("ZSTACK_OVN_SSL_ENABLED"); v != "" {
		c.OVN.SSL.Enabled = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("ZSTACK_OVN_SSL_CA_CERT"); v != "" {
		c.OVN.SSL.CACert = v
	}
	if v := os.Getenv("ZSTACK_OVN_SSL_CLIENT_CERT"); v != "" {
		c.OVN.SSL.ClientCert = v
	}
	if v := os.Getenv("ZSTACK_OVN_SSL_CLIENT_KEY"); v != "" {
		c.OVN.SSL.ClientKey = v
	}

	// Network settings
	if v := os.Getenv("ZSTACK_OVN_CLUSTER_CIDR"); v != "" {
		c.Network.ClusterCIDR = v
	}
	if v := os.Getenv("ZSTACK_OVN_SERVICE_CIDR"); v != "" {
		c.Network.ServiceCIDR = v
	}

	// Gateway settings
	if v := os.Getenv("ZSTACK_OVN_GATEWAY_MODE"); v != "" {
		c.Gateway.Mode = v
	}
	if v := os.Getenv("ZSTACK_OVN_GATEWAY_INTERFACE"); v != "" {
		c.Gateway.Interface = v
	}

	// Tunnel settings
	if v := os.Getenv("ZSTACK_OVN_TUNNEL_TYPE"); v != "" {
		c.Tunnel.Type = v
	}

	// DPDK settings
	if v := os.Getenv("ZSTACK_OVN_DPDK_ENABLED"); v != "" {
		c.DPDK.Enabled = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("ZSTACK_OVN_DPDK_SOCKET_DIR"); v != "" {
		c.DPDK.SocketDir = v
	}
	if v := os.Getenv("ZSTACK_OVN_DPDK_SOCKET_MODE"); v != "" {
		c.DPDK.SocketMode = v
	}
	if v := os.Getenv("ZSTACK_OVN_DPDK_QUEUES"); v != "" {
		if queues, err := strconv.Atoi(v); err == nil && queues > 0 {
			c.DPDK.Queues = queues
		}
	}
	if v := os.Getenv("ZSTACK_OVN_DPDK_MIN_HUGEPAGES_MB"); v != "" {
		if mb, err := strconv.ParseInt(v, 10, 64); err == nil && mb > 0 {
			c.DPDK.MinHugepagesMB = mb
		}
	}

	// Logging settings
	if v := os.Getenv("ZSTACK_OVN_LOG_LEVEL"); v != "" {
		c.Logging.Level = v
	}
	if v := os.Getenv("ZSTACK_OVN_LOG_FORMAT"); v != "" {
		c.Logging.Format = v
	}
}

// Validate validates the configuration
//
// Returns:
//   - error: Validation error with details
func (c *Config) Validate() error {
	var errors []string

	// Validate OVN mode
	if c.OVN.Mode != "standalone" && c.OVN.Mode != "external" {
		errors = append(errors, fmt.Sprintf("invalid OVN mode: %s (must be 'standalone' or 'external')", c.OVN.Mode))
	}

	// Validate external mode requirements
	if c.OVN.Mode == "external" {
		if c.OVN.NBDBAddress == "" {
			errors = append(errors, "nbdbAddress is required for external mode")
		}
		if c.OVN.SBDBAddress == "" {
			errors = append(errors, "sbdbAddress is required for external mode")
		}
	}

	// Validate SSL configuration
	if c.OVN.SSL.Enabled {
		if c.OVN.SSL.CACert == "" {
			errors = append(errors, "SSL CA certificate path is required when SSL is enabled")
		}
		if c.OVN.SSL.ClientCert == "" {
			errors = append(errors, "SSL client certificate path is required when SSL is enabled")
		}
		if c.OVN.SSL.ClientKey == "" {
			errors = append(errors, "SSL client key path is required when SSL is enabled")
		}
	}

	// Validate network configuration
	if c.Network.ClusterCIDR == "" {
		errors = append(errors, "clusterCIDR is required")
	}
	if c.Network.ServiceCIDR == "" {
		errors = append(errors, "serviceCIDR is required")
	}
	if c.Network.NodeSubnetSize < 16 || c.Network.NodeSubnetSize > 30 {
		errors = append(errors, fmt.Sprintf("invalid nodeSubnetSize: %d (must be between 16 and 30)", c.Network.NodeSubnetSize))
	}

	// Validate gateway mode
	if c.Gateway.Mode != "shared" && c.Gateway.Mode != "local" {
		errors = append(errors, fmt.Sprintf("invalid gateway mode: %s (must be 'shared' or 'local')", c.Gateway.Mode))
	}

	// Validate tunnel type
	if c.Tunnel.Type != "vxlan" && c.Tunnel.Type != "geneve" {
		errors = append(errors, fmt.Sprintf("invalid tunnel type: %s (must be 'vxlan' or 'geneve')", c.Tunnel.Type))
	}

	// Validate log level
	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLogLevels[c.Logging.Level] {
		errors = append(errors, fmt.Sprintf("invalid log level: %s (must be 'debug', 'info', 'warn', or 'error')", c.Logging.Level))
	}

	// Validate log format
	if c.Logging.Format != "json" && c.Logging.Format != "text" {
		errors = append(errors, fmt.Sprintf("invalid log format: %s (must be 'json' or 'text')", c.Logging.Format))
	}

	// Validate DPDK configuration
	if c.DPDK.Enabled {
		if c.DPDK.SocketMode != "client" && c.DPDK.SocketMode != "server" {
			errors = append(errors, fmt.Sprintf("invalid DPDK socket mode: %s (must be 'client' or 'server')", c.DPDK.SocketMode))
		}
		if c.DPDK.Queues < 1 {
			errors = append(errors, fmt.Sprintf("invalid DPDK queues: %d (must be >= 1)", c.DPDK.Queues))
		}
		if c.DPDK.SocketDir == "" {
			errors = append(errors, "DPDK socket directory is required when DPDK is enabled")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("configuration errors:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

// IsStandaloneMode returns true if running in standalone mode
func (c *Config) IsStandaloneMode() bool {
	return c.OVN.Mode == "standalone"
}

// IsExternalMode returns true if running in external mode
func (c *Config) IsExternalMode() bool {
	return c.OVN.Mode == "external"
}

// GetNBDBAddress returns the Northbound DB address
// For standalone mode, returns the local address
func (c *Config) GetNBDBAddress() string {
	if c.IsStandaloneMode() {
		return "unix:/var/run/ovn/ovnnb_db.sock"
	}
	return c.OVN.NBDBAddress
}

// GetSBDBAddress returns the Southbound DB address
// For standalone mode, returns the local address
func (c *Config) GetSBDBAddress() string {
	if c.IsStandaloneMode() {
		return "unix:/var/run/ovn/ovnsb_db.sock"
	}
	return c.OVN.SBDBAddress
}

// ValidateExternalModeConfig performs additional validation for external mode
// This includes checking that:
// - Database addresses are properly formatted
// - SSL certificates exist if SSL is enabled
// - No conflicting standalone mode settings are present
//
// Returns:
//   - error: Validation error with details
func (c *Config) ValidateExternalModeConfig() error {
	if !c.IsExternalMode() {
		return nil
	}

	var errors []string

	// Validate NB DB address format
	if err := validateDBAddressFormat(c.OVN.NBDBAddress); err != nil {
		errors = append(errors, fmt.Sprintf("invalid NBDBAddress: %v", err))
	}

	// Validate SB DB address format
	if err := validateDBAddressFormat(c.OVN.SBDBAddress); err != nil {
		errors = append(errors, fmt.Sprintf("invalid SBDBAddress: %v", err))
	}

	// Validate SSL certificates exist if SSL is enabled
	if c.OVN.SSL.Enabled {
		if _, err := os.Stat(c.OVN.SSL.CACert); os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("SSL CA certificate not found: %s", c.OVN.SSL.CACert))
		}
		if _, err := os.Stat(c.OVN.SSL.ClientCert); os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("SSL client certificate not found: %s", c.OVN.SSL.ClientCert))
		}
		if _, err := os.Stat(c.OVN.SSL.ClientKey); os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("SSL client key not found: %s", c.OVN.SSL.ClientKey))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("external mode configuration errors:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

// validateDBAddressFormat validates the format of an OVN database address
func validateDBAddressFormat(address string) error {
	if address == "" {
		return fmt.Errorf("address is empty")
	}

	// Support multiple addresses separated by commas (for HA)
	for _, addr := range strings.Split(address, ",") {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}

		// Check for valid scheme
		if !strings.HasPrefix(addr, "tcp:") &&
			!strings.HasPrefix(addr, "ssl:") &&
			!strings.HasPrefix(addr, "unix:") {
			return fmt.Errorf("invalid address scheme: %s (must be tcp:, ssl:, or unix:)", addr)
		}

		// For tcp: and ssl:, validate IP:PORT format
		if strings.HasPrefix(addr, "tcp:") || strings.HasPrefix(addr, "ssl:") {
			hostPort := strings.TrimPrefix(addr, "tcp:")
			hostPort = strings.TrimPrefix(hostPort, "ssl:")

			if !strings.Contains(hostPort, ":") {
				return fmt.Errorf("invalid address format: %s (expected IP:PORT)", addr)
			}
		}
	}

	return nil
}

// ShouldStartLocalOVN returns true if local OVN processes should be started
// In external mode, local OVN processes (NB DB, SB DB, northd) should NOT be started
// as we connect to external ZStack-managed OVN databases
func (c *Config) ShouldStartLocalOVN() bool {
	return c.IsStandaloneMode()
}

// GetExternalDBConfig returns the configuration for external database connection
// Returns nil if not in external mode
func (c *Config) GetExternalDBConfig() *ExternalDBConfig {
	if !c.IsExternalMode() {
		return nil
	}

	return &ExternalDBConfig{
		NBDBAddress:          c.OVN.NBDBAddress,
		SBDBAddress:          c.OVN.SBDBAddress,
		SSLEnabled:           c.OVN.SSL.Enabled,
		CACert:               c.OVN.SSL.CACert,
		ClientCert:           c.OVN.SSL.ClientCert,
		ClientKey:            c.OVN.SSL.ClientKey,
		ConnectTimeout:       c.OVN.ConnectTimeout,
		ReconnectInterval:    c.OVN.ReconnectInterval,
		MaxReconnectInterval: c.OVN.MaxReconnectInterval,
	}
}

// ExternalDBConfig contains configuration for external OVN database connections
type ExternalDBConfig struct {
	NBDBAddress          string
	SBDBAddress          string
	SSLEnabled           bool
	CACert               string
	ClientCert           string
	ClientKey            string
	ConnectTimeout       time.Duration
	ReconnectInterval    time.Duration
	MaxReconnectInterval time.Duration
}

// IsDPDKEnabled returns true if DPDK is enabled in the configuration
func (c *Config) IsDPDKEnabled() bool {
	return c.DPDK.Enabled
}

// GetDPDKSocketDir returns the DPDK socket directory
func (c *Config) GetDPDKSocketDir() string {
	return c.DPDK.SocketDir
}

// GetDPDKSocketMode returns the DPDK socket mode
func (c *Config) GetDPDKSocketMode() string {
	return c.DPDK.SocketMode
}

// GetDPDKQueues returns the number of DPDK queues
func (c *Config) GetDPDKQueues() int {
	return c.DPDK.Queues
}

// GetDPDKMinHugepagesMB returns the minimum required hugepages in MB
func (c *Config) GetDPDKMinHugepagesMB() int64 {
	return c.DPDK.MinHugepagesMB
}
