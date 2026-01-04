// Package config provides tests for configuration management.
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	// Verify default values
	if cfg.OVN.Mode != "standalone" {
		t.Errorf("expected OVN mode 'standalone', got '%s'", cfg.OVN.Mode)
	}
	if cfg.Network.ClusterCIDR != "10.244.0.0/16" {
		t.Errorf("expected cluster CIDR '10.244.0.0/16', got '%s'", cfg.Network.ClusterCIDR)
	}
	if cfg.Network.ServiceCIDR != "10.96.0.0/16" {
		t.Errorf("expected service CIDR '10.96.0.0/16', got '%s'", cfg.Network.ServiceCIDR)
	}
	if cfg.Gateway.Mode != "local" {
		t.Errorf("expected gateway mode 'local', got '%s'", cfg.Gateway.Mode)
	}
	if cfg.Tunnel.Type != "vxlan" {
		t.Errorf("expected tunnel type 'vxlan', got '%s'", cfg.Tunnel.Type)
	}
	if cfg.Tunnel.Port != 4789 {
		t.Errorf("expected tunnel port 4789, got %d", cfg.Tunnel.Port)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("expected log level 'info', got '%s'", cfg.Logging.Level)
	}
}

func TestLoadFromFile(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")

	configContent := `
ovn:
  mode: external
  nbdbAddress: "tcp:192.168.1.100:6641"
  sbdbAddress: "tcp:192.168.1.100:6642"
network:
  clusterCIDR: "10.100.0.0/16"
  serviceCIDR: "10.200.0.0/16"
  nodeSubnetSize: 26
gateway:
  mode: shared
tunnel:
  type: geneve
  port: 6081
logging:
  level: debug
  format: text
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg := DefaultConfig()
	if err := cfg.LoadFromFile(configFile); err != nil {
		t.Fatalf("failed to load config file: %v", err)
	}

	// Verify loaded values
	if cfg.OVN.Mode != "external" {
		t.Errorf("expected OVN mode 'external', got '%s'", cfg.OVN.Mode)
	}
	if cfg.OVN.NBDBAddress != "tcp:192.168.1.100:6641" {
		t.Errorf("expected NB DB address 'tcp:192.168.1.100:6641', got '%s'", cfg.OVN.NBDBAddress)
	}
	if cfg.Network.ClusterCIDR != "10.100.0.0/16" {
		t.Errorf("expected cluster CIDR '10.100.0.0/16', got '%s'", cfg.Network.ClusterCIDR)
	}
	if cfg.Gateway.Mode != "shared" {
		t.Errorf("expected gateway mode 'shared', got '%s'", cfg.Gateway.Mode)
	}
	if cfg.Tunnel.Type != "geneve" {
		t.Errorf("expected tunnel type 'geneve', got '%s'", cfg.Tunnel.Type)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("expected log level 'debug', got '%s'", cfg.Logging.Level)
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	// Set environment variables
	os.Setenv("ZSTACK_OVN_MODE", "external")
	os.Setenv("ZSTACK_OVN_NBDB_ADDRESS", "tcp:10.0.0.1:6641")
	os.Setenv("ZSTACK_OVN_SBDB_ADDRESS", "tcp:10.0.0.1:6642")
	os.Setenv("ZSTACK_OVN_CLUSTER_CIDR", "172.16.0.0/16")
	os.Setenv("ZSTACK_OVN_GATEWAY_MODE", "shared")
	os.Setenv("ZSTACK_OVN_TUNNEL_TYPE", "geneve")
	os.Setenv("ZSTACK_OVN_LOG_LEVEL", "debug")
	defer func() {
		os.Unsetenv("ZSTACK_OVN_MODE")
		os.Unsetenv("ZSTACK_OVN_NBDB_ADDRESS")
		os.Unsetenv("ZSTACK_OVN_SBDB_ADDRESS")
		os.Unsetenv("ZSTACK_OVN_CLUSTER_CIDR")
		os.Unsetenv("ZSTACK_OVN_GATEWAY_MODE")
		os.Unsetenv("ZSTACK_OVN_TUNNEL_TYPE")
		os.Unsetenv("ZSTACK_OVN_LOG_LEVEL")
	}()

	cfg := DefaultConfig()
	cfg.ApplyEnvOverrides()

	// Verify overridden values
	if cfg.OVN.Mode != "external" {
		t.Errorf("expected OVN mode 'external', got '%s'", cfg.OVN.Mode)
	}
	if cfg.OVN.NBDBAddress != "tcp:10.0.0.1:6641" {
		t.Errorf("expected NB DB address 'tcp:10.0.0.1:6641', got '%s'", cfg.OVN.NBDBAddress)
	}
	if cfg.Network.ClusterCIDR != "172.16.0.0/16" {
		t.Errorf("expected cluster CIDR '172.16.0.0/16', got '%s'", cfg.Network.ClusterCIDR)
	}
	if cfg.Gateway.Mode != "shared" {
		t.Errorf("expected gateway mode 'shared', got '%s'", cfg.Gateway.Mode)
	}
	if cfg.Tunnel.Type != "geneve" {
		t.Errorf("expected tunnel type 'geneve', got '%s'", cfg.Tunnel.Type)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("expected log level 'debug', got '%s'", cfg.Logging.Level)
	}
}

func TestValidate_StandaloneMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OVN.Mode = "standalone"

	if err := cfg.Validate(); err != nil {
		t.Errorf("standalone mode validation should pass: %v", err)
	}
}

func TestValidate_ExternalMode_Valid(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OVN.Mode = "external"
	cfg.OVN.NBDBAddress = "tcp:192.168.1.100:6641"
	cfg.OVN.SBDBAddress = "tcp:192.168.1.100:6642"

	if err := cfg.Validate(); err != nil {
		t.Errorf("external mode validation should pass: %v", err)
	}
}

func TestValidate_ExternalMode_MissingNBDB(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OVN.Mode = "external"
	cfg.OVN.SBDBAddress = "tcp:192.168.1.100:6642"

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for missing NB DB address")
	}
}

func TestValidate_ExternalMode_MissingSBDB(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OVN.Mode = "external"
	cfg.OVN.NBDBAddress = "tcp:192.168.1.100:6641"

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for missing SB DB address")
	}
}

func TestValidate_InvalidMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OVN.Mode = "invalid"

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for invalid mode")
	}
}

func TestValidate_InvalidGatewayMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Gateway.Mode = "invalid"

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for invalid gateway mode")
	}
}

func TestValidate_InvalidTunnelType(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tunnel.Type = "invalid"

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for invalid tunnel type")
	}
}

func TestValidate_InvalidLogLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Logging.Level = "invalid"

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for invalid log level")
	}
}

func TestValidate_SSLEnabled_MissingCerts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OVN.Mode = "external"
	cfg.OVN.NBDBAddress = "ssl:192.168.1.100:6641"
	cfg.OVN.SBDBAddress = "ssl:192.168.1.100:6642"
	cfg.OVN.SSL.Enabled = true

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for missing SSL certificates")
	}
}

func TestValidate_SSLEnabled_Valid(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OVN.Mode = "external"
	cfg.OVN.NBDBAddress = "ssl:192.168.1.100:6641"
	cfg.OVN.SBDBAddress = "ssl:192.168.1.100:6642"
	cfg.OVN.SSL.Enabled = true
	cfg.OVN.SSL.CACert = "/etc/ovn/ca.crt"
	cfg.OVN.SSL.ClientCert = "/etc/ovn/client.crt"
	cfg.OVN.SSL.ClientKey = "/etc/ovn/client.key"

	if err := cfg.Validate(); err != nil {
		t.Errorf("SSL validation should pass: %v", err)
	}
}

func TestIsStandaloneMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OVN.Mode = "standalone"

	if !cfg.IsStandaloneMode() {
		t.Error("expected IsStandaloneMode to return true")
	}
	if cfg.IsExternalMode() {
		t.Error("expected IsExternalMode to return false")
	}
}

func TestIsExternalMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.OVN.Mode = "external"

	if cfg.IsStandaloneMode() {
		t.Error("expected IsStandaloneMode to return false")
	}
	if !cfg.IsExternalMode() {
		t.Error("expected IsExternalMode to return true")
	}
}

// Tests for GetNBDBAddress/GetSBDBAddress - Requirements 1.1, 1.2, 1.3, 1.4

func TestGetNBDBAddress_EmptyReturnsDefaultUnixSocket(t *testing.T) {
	// Test that empty address returns default Unix socket (Requirement 1.3)
	cfg := DefaultConfig()
	cfg.OVN.NBDBAddress = ""

	addr := cfg.GetNBDBAddress()
	expected := "unix:/var/run/ovn/ovnnb_db.sock"
	if addr != expected {
		t.Errorf("expected '%s', got '%s'", expected, addr)
	}
}

func TestGetSBDBAddress_EmptyReturnsDefaultUnixSocket(t *testing.T) {
	// Test that empty address returns default Unix socket (Requirement 1.4)
	cfg := DefaultConfig()
	cfg.OVN.SBDBAddress = ""

	addr := cfg.GetSBDBAddress()
	expected := "unix:/var/run/ovn/ovnsb_db.sock"
	if addr != expected {
		t.Errorf("expected '%s', got '%s'", expected, addr)
	}
}

func TestGetNBDBAddress_ConfiguredAddressReturned(t *testing.T) {
	// Test that configured address is returned unchanged (Requirement 1.1)
	testCases := []struct {
		name    string
		address string
	}{
		{"tcp address", "tcp:192.168.1.100:6641"},
		{"ssl address", "ssl:10.0.0.1:6641"},
		{"unix socket", "unix:/custom/path/ovnnb_db.sock"},
		{"multiple addresses", "tcp:192.168.1.100:6641,tcp:192.168.1.101:6641"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.OVN.NBDBAddress = tc.address

			addr := cfg.GetNBDBAddress()
			if addr != tc.address {
				t.Errorf("expected '%s', got '%s'", tc.address, addr)
			}
		})
	}
}

func TestGetSBDBAddress_ConfiguredAddressReturned(t *testing.T) {
	// Test that configured address is returned unchanged (Requirement 1.2)
	testCases := []struct {
		name    string
		address string
	}{
		{"tcp address", "tcp:192.168.1.100:6642"},
		{"ssl address", "ssl:10.0.0.1:6642"},
		{"unix socket", "unix:/custom/path/ovnsb_db.sock"},
		{"multiple addresses", "tcp:192.168.1.100:6642,tcp:192.168.1.101:6642"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.OVN.SBDBAddress = tc.address

			addr := cfg.GetSBDBAddress()
			if addr != tc.address {
				t.Errorf("expected '%s', got '%s'", tc.address, addr)
			}
		})
	}
}

func TestGetNBDBAddress_StandaloneModeWithConfiguredAddress(t *testing.T) {
	// Test that configured address is returned even in standalone mode (Requirement 1.1)
	cfg := DefaultConfig()
	cfg.OVN.Mode = "standalone"
	cfg.OVN.NBDBAddress = "tcp:127.0.0.1:6641"

	addr := cfg.GetNBDBAddress()
	if addr != "tcp:127.0.0.1:6641" {
		t.Errorf("expected 'tcp:127.0.0.1:6641', got '%s'", addr)
	}
}

func TestGetSBDBAddress_StandaloneModeWithConfiguredAddress(t *testing.T) {
	// Test that configured address is returned even in standalone mode (Requirement 1.2)
	cfg := DefaultConfig()
	cfg.OVN.Mode = "standalone"
	cfg.OVN.SBDBAddress = "tcp:127.0.0.1:6642"

	addr := cfg.GetSBDBAddress()
	if addr != "tcp:127.0.0.1:6642" {
		t.Errorf("expected 'tcp:127.0.0.1:6642', got '%s'", addr)
	}
}

func TestGetNBDBAddress_ExternalModeWithConfiguredAddress(t *testing.T) {
	// Test that configured address is returned in external mode (Requirement 1.1)
	cfg := DefaultConfig()
	cfg.OVN.Mode = "external"
	cfg.OVN.NBDBAddress = "tcp:192.168.1.100:6641"

	addr := cfg.GetNBDBAddress()
	if addr != "tcp:192.168.1.100:6641" {
		t.Errorf("expected 'tcp:192.168.1.100:6641', got '%s'", addr)
	}
}

func TestGetSBDBAddress_ExternalModeWithConfiguredAddress(t *testing.T) {
	// Test that configured address is returned in external mode (Requirement 1.2)
	cfg := DefaultConfig()
	cfg.OVN.Mode = "external"
	cfg.OVN.SBDBAddress = "tcp:192.168.1.100:6642"

	addr := cfg.GetSBDBAddress()
	if addr != "tcp:192.168.1.100:6642" {
		t.Errorf("expected 'tcp:192.168.1.100:6642', got '%s'", addr)
	}
}

func TestGetNBDBAddress_StandaloneModeEmptyAddress(t *testing.T) {
	// Test that empty address in standalone mode returns Unix socket (Requirement 1.3)
	cfg := DefaultConfig()
	cfg.OVN.Mode = "standalone"
	cfg.OVN.NBDBAddress = ""

	addr := cfg.GetNBDBAddress()
	if addr != "unix:/var/run/ovn/ovnnb_db.sock" {
		t.Errorf("expected unix socket path, got '%s'", addr)
	}
}

func TestGetSBDBAddress_StandaloneModeEmptyAddress(t *testing.T) {
	// Test that empty address in standalone mode returns Unix socket (Requirement 1.4)
	cfg := DefaultConfig()
	cfg.OVN.Mode = "standalone"
	cfg.OVN.SBDBAddress = ""

	addr := cfg.GetSBDBAddress()
	if addr != "unix:/var/run/ovn/ovnsb_db.sock" {
		t.Errorf("expected unix socket path, got '%s'", addr)
	}
}

// Tests for environment variable precedence - Requirements 2.1, 2.2, 2.3, 2.4

func TestApplyEnvOverrides_OVN_NB_DB_OverridesConfig(t *testing.T) {
	// Test that OVN_NB_DB overrides config file value (Requirement 2.1)
	os.Setenv("OVN_NB_DB", "tcp:10.0.0.50:6641")
	defer os.Unsetenv("OVN_NB_DB")

	cfg := DefaultConfig()
	cfg.OVN.NBDBAddress = "tcp:192.168.1.100:6641" // Config file value
	cfg.ApplyEnvOverrides()

	if cfg.OVN.NBDBAddress != "tcp:10.0.0.50:6641" {
		t.Errorf("expected OVN_NB_DB to override config, got '%s'", cfg.OVN.NBDBAddress)
	}
}

func TestApplyEnvOverrides_OVN_SB_DB_OverridesConfig(t *testing.T) {
	// Test that OVN_SB_DB overrides config file value (Requirement 2.2)
	os.Setenv("OVN_SB_DB", "tcp:10.0.0.50:6642")
	defer os.Unsetenv("OVN_SB_DB")

	cfg := DefaultConfig()
	cfg.OVN.SBDBAddress = "tcp:192.168.1.100:6642" // Config file value
	cfg.ApplyEnvOverrides()

	if cfg.OVN.SBDBAddress != "tcp:10.0.0.50:6642" {
		t.Errorf("expected OVN_SB_DB to override config, got '%s'", cfg.OVN.SBDBAddress)
	}
}

func TestApplyEnvOverrides_ZSTACK_OVN_NBDB_TakesPrecedence(t *testing.T) {
	// Test that ZSTACK_OVN_NBDB_ADDRESS takes precedence over OVN_NB_DB (Requirement 2.3)
	os.Setenv("ZSTACK_OVN_NBDB_ADDRESS", "tcp:172.16.0.1:6641")
	os.Setenv("OVN_NB_DB", "tcp:10.0.0.50:6641")
	defer func() {
		os.Unsetenv("ZSTACK_OVN_NBDB_ADDRESS")
		os.Unsetenv("OVN_NB_DB")
	}()

	cfg := DefaultConfig()
	cfg.ApplyEnvOverrides()

	if cfg.OVN.NBDBAddress != "tcp:172.16.0.1:6641" {
		t.Errorf("expected ZSTACK_OVN_NBDB_ADDRESS to take precedence, got '%s'", cfg.OVN.NBDBAddress)
	}
}

func TestApplyEnvOverrides_ZSTACK_OVN_SBDB_TakesPrecedence(t *testing.T) {
	// Test that ZSTACK_OVN_SBDB_ADDRESS takes precedence over OVN_SB_DB (Requirement 2.4)
	os.Setenv("ZSTACK_OVN_SBDB_ADDRESS", "tcp:172.16.0.1:6642")
	os.Setenv("OVN_SB_DB", "tcp:10.0.0.50:6642")
	defer func() {
		os.Unsetenv("ZSTACK_OVN_SBDB_ADDRESS")
		os.Unsetenv("OVN_SB_DB")
	}()

	cfg := DefaultConfig()
	cfg.ApplyEnvOverrides()

	if cfg.OVN.SBDBAddress != "tcp:172.16.0.1:6642" {
		t.Errorf("expected ZSTACK_OVN_SBDB_ADDRESS to take precedence, got '%s'", cfg.OVN.SBDBAddress)
	}
}

func TestApplyEnvOverrides_OnlyOVN_NB_DB_Set(t *testing.T) {
	// Test that OVN_NB_DB is used when ZSTACK_OVN_NBDB_ADDRESS is not set (Requirement 2.1)
	os.Setenv("OVN_NB_DB", "tcp:10.0.0.100:6641")
	defer os.Unsetenv("OVN_NB_DB")

	cfg := DefaultConfig()
	cfg.ApplyEnvOverrides()

	if cfg.OVN.NBDBAddress != "tcp:10.0.0.100:6641" {
		t.Errorf("expected OVN_NB_DB value, got '%s'", cfg.OVN.NBDBAddress)
	}
}

func TestApplyEnvOverrides_OnlyOVN_SB_DB_Set(t *testing.T) {
	// Test that OVN_SB_DB is used when ZSTACK_OVN_SBDB_ADDRESS is not set (Requirement 2.2)
	os.Setenv("OVN_SB_DB", "tcp:10.0.0.100:6642")
	defer os.Unsetenv("OVN_SB_DB")

	cfg := DefaultConfig()
	cfg.ApplyEnvOverrides()

	if cfg.OVN.SBDBAddress != "tcp:10.0.0.100:6642" {
		t.Errorf("expected OVN_SB_DB value, got '%s'", cfg.OVN.SBDBAddress)
	}
}

func TestApplyEnvOverrides_NoEnvVars_KeepsConfigValue(t *testing.T) {
	// Test that config value is preserved when no env vars are set
	// Clear any existing env vars
	os.Unsetenv("ZSTACK_OVN_NBDB_ADDRESS")
	os.Unsetenv("ZSTACK_OVN_SBDB_ADDRESS")
	os.Unsetenv("OVN_NB_DB")
	os.Unsetenv("OVN_SB_DB")

	cfg := DefaultConfig()
	cfg.OVN.NBDBAddress = "tcp:192.168.1.100:6641"
	cfg.OVN.SBDBAddress = "tcp:192.168.1.100:6642"
	cfg.ApplyEnvOverrides()

	if cfg.OVN.NBDBAddress != "tcp:192.168.1.100:6641" {
		t.Errorf("expected config value preserved for NBDBAddress, got '%s'", cfg.OVN.NBDBAddress)
	}
	if cfg.OVN.SBDBAddress != "tcp:192.168.1.100:6642" {
		t.Errorf("expected config value preserved for SBDBAddress, got '%s'", cfg.OVN.SBDBAddress)
	}
}

func TestApplyEnvOverrides_EmptyZSTACK_FallsBackToOVN(t *testing.T) {
	// Test that empty ZSTACK_OVN_* falls back to OVN_* env vars
	os.Setenv("ZSTACK_OVN_NBDB_ADDRESS", "")
	os.Setenv("OVN_NB_DB", "tcp:10.0.0.200:6641")
	os.Setenv("ZSTACK_OVN_SBDB_ADDRESS", "")
	os.Setenv("OVN_SB_DB", "tcp:10.0.0.200:6642")
	defer func() {
		os.Unsetenv("ZSTACK_OVN_NBDB_ADDRESS")
		os.Unsetenv("OVN_NB_DB")
		os.Unsetenv("ZSTACK_OVN_SBDB_ADDRESS")
		os.Unsetenv("OVN_SB_DB")
	}()

	cfg := DefaultConfig()
	cfg.ApplyEnvOverrides()

	// Empty ZSTACK_OVN_* should fall back to OVN_*
	if cfg.OVN.NBDBAddress != "tcp:10.0.0.200:6641" {
		t.Errorf("expected OVN_NB_DB fallback, got '%s'", cfg.OVN.NBDBAddress)
	}
	if cfg.OVN.SBDBAddress != "tcp:10.0.0.200:6642" {
		t.Errorf("expected OVN_SB_DB fallback, got '%s'", cfg.OVN.SBDBAddress)
	}
}

func TestValidate_InvalidNodeSubnetSize(t *testing.T) {
	tests := []struct {
		name string
		size int
	}{
		{"too small", 15},
		{"too large", 31},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Network.NodeSubnetSize = tt.size

			err := cfg.Validate()
			if err == nil {
				t.Errorf("expected validation error for node subnet size %d", tt.size)
			}
		})
	}
}

// DPDK Configuration Tests

func TestDefaultConfig_DPDK(t *testing.T) {
	cfg := DefaultConfig()

	// Verify DPDK default values
	if cfg.DPDK.Enabled {
		t.Error("expected DPDK to be disabled by default")
	}
	if cfg.DPDK.SocketDir != "/var/run/openvswitch" {
		t.Errorf("expected socket dir '/var/run/openvswitch', got '%s'", cfg.DPDK.SocketDir)
	}
	if cfg.DPDK.SocketMode != "client" {
		t.Errorf("expected socket mode 'client', got '%s'", cfg.DPDK.SocketMode)
	}
	if cfg.DPDK.Queues != 1 {
		t.Errorf("expected queues 1, got %d", cfg.DPDK.Queues)
	}
	if cfg.DPDK.MinHugepagesMB != 1024 {
		t.Errorf("expected min hugepages 1024, got %d", cfg.DPDK.MinHugepagesMB)
	}
}

func TestValidate_DPDK_Valid(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DPDK.Enabled = true
	cfg.DPDK.SocketDir = "/var/run/openvswitch"
	cfg.DPDK.SocketMode = "client"
	cfg.DPDK.Queues = 2

	if err := cfg.Validate(); err != nil {
		t.Errorf("DPDK validation should pass: %v", err)
	}
}

func TestValidate_DPDK_InvalidSocketMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DPDK.Enabled = true
	cfg.DPDK.SocketMode = "invalid"

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for invalid DPDK socket mode")
	}
}

func TestValidate_DPDK_InvalidQueues(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DPDK.Enabled = true
	cfg.DPDK.Queues = 0

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for invalid DPDK queues")
	}
}

func TestValidate_DPDK_MissingSocketDir(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DPDK.Enabled = true
	cfg.DPDK.SocketDir = ""

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for missing DPDK socket directory")
	}
}

func TestApplyEnvOverrides_DPDK(t *testing.T) {
	// Set DPDK environment variables
	os.Setenv("ZSTACK_OVN_DPDK_ENABLED", "true")
	os.Setenv("ZSTACK_OVN_DPDK_SOCKET_DIR", "/custom/socket/dir")
	os.Setenv("ZSTACK_OVN_DPDK_SOCKET_MODE", "server")
	os.Setenv("ZSTACK_OVN_DPDK_QUEUES", "4")
	os.Setenv("ZSTACK_OVN_DPDK_MIN_HUGEPAGES_MB", "2048")
	defer func() {
		os.Unsetenv("ZSTACK_OVN_DPDK_ENABLED")
		os.Unsetenv("ZSTACK_OVN_DPDK_SOCKET_DIR")
		os.Unsetenv("ZSTACK_OVN_DPDK_SOCKET_MODE")
		os.Unsetenv("ZSTACK_OVN_DPDK_QUEUES")
		os.Unsetenv("ZSTACK_OVN_DPDK_MIN_HUGEPAGES_MB")
	}()

	cfg := DefaultConfig()
	cfg.ApplyEnvOverrides()

	// Verify DPDK overridden values
	if !cfg.DPDK.Enabled {
		t.Error("expected DPDK to be enabled")
	}
	if cfg.DPDK.SocketDir != "/custom/socket/dir" {
		t.Errorf("expected socket dir '/custom/socket/dir', got '%s'", cfg.DPDK.SocketDir)
	}
	if cfg.DPDK.SocketMode != "server" {
		t.Errorf("expected socket mode 'server', got '%s'", cfg.DPDK.SocketMode)
	}
	if cfg.DPDK.Queues != 4 {
		t.Errorf("expected queues 4, got %d", cfg.DPDK.Queues)
	}
	if cfg.DPDK.MinHugepagesMB != 2048 {
		t.Errorf("expected min hugepages 2048, got %d", cfg.DPDK.MinHugepagesMB)
	}
}

func TestIsDPDKEnabled(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.IsDPDKEnabled() {
		t.Error("expected IsDPDKEnabled to return false by default")
	}

	cfg.DPDK.Enabled = true
	if !cfg.IsDPDKEnabled() {
		t.Error("expected IsDPDKEnabled to return true when enabled")
	}
}

func TestGetDPDKHelpers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DPDK.SocketDir = "/test/socket/dir"
	cfg.DPDK.SocketMode = "server"
	cfg.DPDK.Queues = 8
	cfg.DPDK.MinHugepagesMB = 4096

	if cfg.GetDPDKSocketDir() != "/test/socket/dir" {
		t.Errorf("expected socket dir '/test/socket/dir', got '%s'", cfg.GetDPDKSocketDir())
	}
	if cfg.GetDPDKSocketMode() != "server" {
		t.Errorf("expected socket mode 'server', got '%s'", cfg.GetDPDKSocketMode())
	}
	if cfg.GetDPDKQueues() != 8 {
		t.Errorf("expected queues 8, got %d", cfg.GetDPDKQueues())
	}
	if cfg.GetDPDKMinHugepagesMB() != 4096 {
		t.Errorf("expected min hugepages 4096, got %d", cfg.GetDPDKMinHugepagesMB())
	}
}
