// Package node provides property-based tests for tunnel configuration.
//
// Feature: zstack-ovn-kubernetes-cni, Property 10: VXLAN 隧道配置正确性
// Validates: Requirements 25.3, 25.4
//
// This file contains property-based tests that verify:
// - VXLAN tunnel configuration correctness
// - Tunnel type validation
// - Port configuration validation
// - Local IP detection consistency
package node

import (
	"net"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"

	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
)

// TestProperty_TunnelTypeValidation verifies that tunnel type validation works correctly.
// Property 10: For any tunnel configuration, the tunnel type must be either 'vxlan' or 'geneve'.
// Validates: Requirements 25.3
func TestProperty_TunnelTypeValidation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Valid tunnel types (vxlan, geneve) should be accepted
	properties.Property("valid tunnel types are accepted", prop.ForAll(
		func(tunnelType string) bool {
			cfg := &config.Config{
				Tunnel: config.TunnelConfig{
					Type: tunnelType,
					Port: 4789,
				},
				Gateway: config.GatewayConfig{
					Mode: "local",
				},
			}

			tc, err := NewTunnelController(cfg, "test-node")
			if err != nil {
				return false
			}

			// Verify the tunnel type is correctly set
			actualType := tc.GetTunnelType()
			if tunnelType == "geneve" {
				return actualType == TunnelTypeGeneve
			}
			// Default to VXLAN for any other value
			return actualType == TunnelTypeVXLAN
		},
		gen.OneConstOf("vxlan", "geneve"),
	))

	properties.TestingRun(t)
}

// TestProperty_TunnelPortConfiguration verifies that tunnel port configuration is correct.
// Property 10: For any valid port number, the tunnel controller should use it correctly.
// Validates: Requirements 25.4
func TestProperty_TunnelPortConfiguration(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Specified port should be used, default port should be used when not specified
	properties.Property("tunnel port is correctly configured", prop.ForAll(
		func(port int, tunnelType string) bool {
			cfg := &config.Config{
				Tunnel: config.TunnelConfig{
					Type: tunnelType,
					Port: port,
				},
				Gateway: config.GatewayConfig{
					Mode: "local",
				},
			}

			tc, err := NewTunnelController(cfg, "test-node")
			if err != nil {
				return false
			}

			actualPort := tc.GetTunnelPort()

			// If port is 0, default should be used
			if port == 0 {
				if tunnelType == "geneve" {
					return actualPort == DefaultGenevePort
				}
				return actualPort == DefaultVXLANPort
			}

			// Otherwise, specified port should be used
			return actualPort == port
		},
		gen.IntRange(0, 65535),
		gen.OneConstOf("vxlan", "geneve"),
	))

	properties.TestingRun(t)
}

// TestProperty_TunnelConfigValidation verifies that tunnel configuration validation works.
// Property 10: For any tunnel configuration, validation should correctly identify invalid configs.
// Validates: Requirements 25.3, 25.4
func TestProperty_TunnelConfigValidation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Valid configurations should pass validation
	properties.Property("valid tunnel configurations pass validation", prop.ForAll(
		func(port int, tunnelType string) bool {
			cfg := &config.Config{
				Tunnel: config.TunnelConfig{
					Type: tunnelType,
					Port: port,
				},
				Gateway: config.GatewayConfig{
					Mode: "local",
				},
			}

			tc, err := NewTunnelController(cfg, "test-node")
			if err != nil {
				return false
			}

			// Validation should pass for valid configs
			err = tc.ValidateTunnelConfig()
			return err == nil
		},
		gen.IntRange(1, 65535), // Valid port range
		gen.OneConstOf("vxlan", "geneve"),
	))

	// Property: Invalid port numbers should fail validation
	properties.Property("invalid port numbers fail validation", prop.ForAll(
		func(port int) bool {
			cfg := &config.Config{
				Tunnel: config.TunnelConfig{
					Type: "vxlan",
					Port: port,
				},
				Gateway: config.GatewayConfig{
					Mode: "local",
				},
			}

			tc, err := NewTunnelController(cfg, "test-node")
			if err != nil {
				return false
			}

			// Manually set invalid port for testing
			tc.config.Port = port

			err = tc.ValidateTunnelConfig()
			// Should fail for invalid ports
			return err != nil
		},
		gen.OneConstOf(-1, 0, 65536, 100000), // Invalid port values
	))

	properties.TestingRun(t)
}

// TestProperty_TunnelInfoConsistency verifies that tunnel info is consistent.
// Property 10: For any created tunnel, the tunnel info should match the configuration.
// Validates: Requirements 25.3, 25.4
func TestProperty_TunnelInfoConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Tunnel controller state should be consistent
	properties.Property("tunnel controller state is consistent", prop.ForAll(
		func(port int, tunnelType string, encapIP string) bool {
			var localIP net.IP
			if encapIP != "" {
				localIP = net.ParseIP(encapIP)
			}

			cfg := &config.Config{
				Tunnel: config.TunnelConfig{
					Type:    tunnelType,
					Port:    port,
					EncapIP: encapIP,
				},
				Gateway: config.GatewayConfig{
					Mode: "local",
				},
			}

			tc, err := NewTunnelController(cfg, "test-node")
			if err != nil {
				// Invalid encap IP should cause error
				if encapIP != "" && localIP == nil {
					return true // Expected error
				}
				return false
			}

			// Verify consistency
			if tc.GetTunnelPort() != port && port != 0 {
				return false
			}

			expectedType := TunnelTypeVXLAN
			if tunnelType == "geneve" {
				expectedType = TunnelTypeGeneve
			}
			if tc.GetTunnelType() != expectedType {
				return false
			}

			// If encap IP was specified and valid, it should be set
			if encapIP != "" && localIP != nil {
				configuredIP := tc.GetLocalIP()
				if configuredIP == nil || !configuredIP.Equal(localIP) {
					return false
				}
			}

			return true
		},
		gen.IntRange(1, 65535),
		gen.OneConstOf("vxlan", "geneve"),
		gen.OneConstOf("", "192.168.1.100", "10.0.0.1", "172.16.0.1"),
	))

	properties.TestingRun(t)
}

// TestProperty_DefaultPortSelection verifies default port selection based on tunnel type.
// Property 10: When port is not specified, the correct default port should be used.
// Validates: Requirements 25.4
func TestProperty_DefaultPortSelection(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Default port should match tunnel type
	properties.Property("default port matches tunnel type", prop.ForAll(
		func(tunnelType string) bool {
			cfg := &config.Config{
				Tunnel: config.TunnelConfig{
					Type: tunnelType,
					Port: 0, // Use default
				},
				Gateway: config.GatewayConfig{
					Mode: "local",
				},
			}

			tc, err := NewTunnelController(cfg, "test-node")
			if err != nil {
				return false
			}

			actualPort := tc.GetTunnelPort()

			if tunnelType == "geneve" {
				return actualPort == DefaultGenevePort
			}
			return actualPort == DefaultVXLANPort
		},
		gen.OneConstOf("vxlan", "geneve", ""), // Empty string defaults to vxlan
	))

	properties.TestingRun(t)
}

// TestProperty_TunnelTypeImmutability verifies that tunnel type cannot change after creation.
// Property 10: Once a tunnel controller is created, its type should remain constant.
// Validates: Requirements 25.3
func TestProperty_TunnelTypeImmutability(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Tunnel type should be immutable after creation
	properties.Property("tunnel type is immutable after creation", prop.ForAll(
		func(tunnelType string, numReads int) bool {
			cfg := &config.Config{
				Tunnel: config.TunnelConfig{
					Type: tunnelType,
					Port: 4789,
				},
				Gateway: config.GatewayConfig{
					Mode: "local",
				},
			}

			tc, err := NewTunnelController(cfg, "test-node")
			if err != nil {
				return false
			}

			// Read tunnel type multiple times
			firstType := tc.GetTunnelType()
			for i := 0; i < numReads; i++ {
				currentType := tc.GetTunnelType()
				if currentType != firstType {
					return false // Type changed unexpectedly
				}
			}

			return true
		},
		gen.OneConstOf("vxlan", "geneve"),
		gen.IntRange(1, 100),
	))

	properties.TestingRun(t)
}
