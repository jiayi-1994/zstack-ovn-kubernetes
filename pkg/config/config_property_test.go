// Package config provides property-based tests for configuration management.
//
// This file contains property-based tests for validating OVN database address configuration.
// These tests verify that configured addresses are passed through correctly and that
// environment variable overrides work as expected.
//
// Feature: e2e-ovn-db-address-fix, Property 1: Configured Address Passthrough
// Validates: Requirements 1.1, 1.2
//
// Property 1 states:
// *For any* valid OVN database address (tcp:, ssl:, or unix: scheme), when the address
// is configured in the Config struct, GetNBDBAddress() and GetSBDBAddress() SHALL return
// that exact address unchanged.
//
// Feature: e2e-ovn-db-address-fix, Property 2: Environment Variable Override
// Validates: Requirements 2.1, 2.2
//
// Property 2 states:
// *For any* valid OVN database address set in the OVN_NB_DB or OVN_SB_DB environment
// variables, when ApplyEnvOverrides() is called, the Config struct SHALL contain that
// address in the corresponding field.
package config

import (
	"fmt"
	"os"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// genValidOVNAddress generates valid OVN database addresses with tcp:, ssl:, or unix: schemes.
func genValidOVNAddress() gopter.Gen {
	return gen.OneGenOf(
		// TCP addresses: tcp:IP:PORT
		gen.IntRange(1, 254).FlatMap(func(v interface{}) gopter.Gen {
			octet1 := v.(int)
			return gen.IntRange(0, 254).FlatMap(func(v interface{}) gopter.Gen {
				octet2 := v.(int)
				return gen.IntRange(0, 254).FlatMap(func(v interface{}) gopter.Gen {
					octet3 := v.(int)
					return gen.IntRange(1, 254).FlatMap(func(v interface{}) gopter.Gen {
						octet4 := v.(int)
						return gen.IntRange(1024, 65535).Map(func(port int) string {
							return fmt.Sprintf("tcp:%d.%d.%d.%d:%d", octet1, octet2, octet3, octet4, port)
						})
					}, nil)
				}, nil)
			}, nil)
		}, nil),
		// SSL addresses: ssl:IP:PORT
		gen.IntRange(1, 254).FlatMap(func(v interface{}) gopter.Gen {
			octet1 := v.(int)
			return gen.IntRange(0, 254).FlatMap(func(v interface{}) gopter.Gen {
				octet2 := v.(int)
				return gen.IntRange(0, 254).FlatMap(func(v interface{}) gopter.Gen {
					octet3 := v.(int)
					return gen.IntRange(1, 254).FlatMap(func(v interface{}) gopter.Gen {
						octet4 := v.(int)
						return gen.IntRange(1024, 65535).Map(func(port int) string {
							return fmt.Sprintf("ssl:%d.%d.%d.%d:%d", octet1, octet2, octet3, octet4, port)
						})
					}, nil)
				}, nil)
			}, nil)
		}, nil),
		// Unix socket addresses: unix:/path/to/socket.sock
		gen.IntRange(1, 100).Map(func(n int) string {
			return fmt.Sprintf("unix:/var/run/ovn/custom_%d.sock", n)
		}),
	)
}

// TestProperty_ConfiguredAddressPassthrough tests that configured addresses are returned unchanged.
//
// Feature: e2e-ovn-db-address-fix, Property 1: Configured Address Passthrough
// Validates: Requirements 1.1, 1.2
//
// Property 1: *For any* valid OVN database address (tcp:, ssl:, or unix: scheme),
// when the address is configured in the Config struct, GetNBDBAddress() and
// GetSBDBAddress() SHALL return that exact address unchanged.
func TestProperty_ConfiguredAddressPassthrough(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: GetNBDBAddress returns configured address unchanged
	properties.Property("GetNBDBAddress returns configured address unchanged", prop.ForAll(
		func(address string) bool {
			cfg := DefaultConfig()
			cfg.OVN.NBDBAddress = address

			result := cfg.GetNBDBAddress()
			return result == address
		},
		genValidOVNAddress(),
	))

	// Property: GetSBDBAddress returns configured address unchanged
	properties.Property("GetSBDBAddress returns configured address unchanged", prop.ForAll(
		func(address string) bool {
			cfg := DefaultConfig()
			cfg.OVN.SBDBAddress = address

			result := cfg.GetSBDBAddress()
			return result == address
		},
		genValidOVNAddress(),
	))

	// Property: GetNBDBAddress returns configured address regardless of mode (standalone)
	properties.Property("GetNBDBAddress returns configured address in standalone mode", prop.ForAll(
		func(address string) bool {
			cfg := DefaultConfig()
			cfg.OVN.Mode = "standalone"
			cfg.OVN.NBDBAddress = address

			result := cfg.GetNBDBAddress()
			return result == address
		},
		genValidOVNAddress(),
	))

	// Property: GetSBDBAddress returns configured address regardless of mode (standalone)
	properties.Property("GetSBDBAddress returns configured address in standalone mode", prop.ForAll(
		func(address string) bool {
			cfg := DefaultConfig()
			cfg.OVN.Mode = "standalone"
			cfg.OVN.SBDBAddress = address

			result := cfg.GetSBDBAddress()
			return result == address
		},
		genValidOVNAddress(),
	))

	// Property: GetNBDBAddress returns configured address regardless of mode (external)
	properties.Property("GetNBDBAddress returns configured address in external mode", prop.ForAll(
		func(address string) bool {
			cfg := DefaultConfig()
			cfg.OVN.Mode = "external"
			cfg.OVN.NBDBAddress = address

			result := cfg.GetNBDBAddress()
			return result == address
		},
		genValidOVNAddress(),
	))

	// Property: GetSBDBAddress returns configured address regardless of mode (external)
	properties.Property("GetSBDBAddress returns configured address in external mode", prop.ForAll(
		func(address string) bool {
			cfg := DefaultConfig()
			cfg.OVN.Mode = "external"
			cfg.OVN.SBDBAddress = address

			result := cfg.GetSBDBAddress()
			return result == address
		},
		genValidOVNAddress(),
	))

	// Property: Empty address returns default Unix socket for NB
	properties.Property("empty NBDBAddress returns default Unix socket", prop.ForAll(
		func(_ int) bool {
			cfg := DefaultConfig()
			cfg.OVN.NBDBAddress = ""

			result := cfg.GetNBDBAddress()
			return result == "unix:/var/run/ovn/ovnnb_db.sock"
		},
		gen.IntRange(0, 10), // Dummy generator to run multiple times
	))

	// Property: Empty address returns default Unix socket for SB
	properties.Property("empty SBDBAddress returns default Unix socket", prop.ForAll(
		func(_ int) bool {
			cfg := DefaultConfig()
			cfg.OVN.SBDBAddress = ""

			result := cfg.GetSBDBAddress()
			return result == "unix:/var/run/ovn/ovnsb_db.sock"
		},
		gen.IntRange(0, 10), // Dummy generator to run multiple times
	))

	properties.TestingRun(t)
}


// TestProperty_EnvironmentVariableOverride tests that environment variables correctly override config values.
//
// Feature: e2e-ovn-db-address-fix, Property 2: Environment Variable Override
// Validates: Requirements 2.1, 2.2
//
// Property 2: *For any* valid OVN database address set in the OVN_NB_DB or OVN_SB_DB
// environment variables, when ApplyEnvOverrides() is called, the Config struct SHALL
// contain that address in the corresponding field.
func TestProperty_EnvironmentVariableOverride(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: OVN_NB_DB environment variable overrides config value
	properties.Property("OVN_NB_DB overrides config value", prop.ForAll(
		func(address string) bool {
			// Clean up any existing env vars
			os.Unsetenv("ZSTACK_OVN_NBDB_ADDRESS")
			os.Unsetenv("OVN_NB_DB")
			defer func() {
				os.Unsetenv("OVN_NB_DB")
			}()

			// Set OVN_NB_DB
			os.Setenv("OVN_NB_DB", address)

			cfg := DefaultConfig()
			cfg.OVN.NBDBAddress = "tcp:192.168.1.1:6641" // Original config value
			cfg.ApplyEnvOverrides()

			return cfg.OVN.NBDBAddress == address
		},
		genValidOVNAddress(),
	))

	// Property: OVN_SB_DB environment variable overrides config value
	properties.Property("OVN_SB_DB overrides config value", prop.ForAll(
		func(address string) bool {
			// Clean up any existing env vars
			os.Unsetenv("ZSTACK_OVN_SBDB_ADDRESS")
			os.Unsetenv("OVN_SB_DB")
			defer func() {
				os.Unsetenv("OVN_SB_DB")
			}()

			// Set OVN_SB_DB
			os.Setenv("OVN_SB_DB", address)

			cfg := DefaultConfig()
			cfg.OVN.SBDBAddress = "tcp:192.168.1.1:6642" // Original config value
			cfg.ApplyEnvOverrides()

			return cfg.OVN.SBDBAddress == address
		},
		genValidOVNAddress(),
	))

	// Property: ZSTACK_OVN_NBDB_ADDRESS takes precedence over OVN_NB_DB
	properties.Property("ZSTACK_OVN_NBDB_ADDRESS takes precedence over OVN_NB_DB", prop.ForAll(
		func(zstackAddr string, ovnAddr string) bool {
			// Clean up any existing env vars
			os.Unsetenv("ZSTACK_OVN_NBDB_ADDRESS")
			os.Unsetenv("OVN_NB_DB")
			defer func() {
				os.Unsetenv("ZSTACK_OVN_NBDB_ADDRESS")
				os.Unsetenv("OVN_NB_DB")
			}()

			// Set both env vars
			os.Setenv("ZSTACK_OVN_NBDB_ADDRESS", zstackAddr)
			os.Setenv("OVN_NB_DB", ovnAddr)

			cfg := DefaultConfig()
			cfg.ApplyEnvOverrides()

			// ZSTACK_OVN_NBDB_ADDRESS should take precedence
			return cfg.OVN.NBDBAddress == zstackAddr
		},
		genValidOVNAddress(),
		genValidOVNAddress(),
	))

	// Property: ZSTACK_OVN_SBDB_ADDRESS takes precedence over OVN_SB_DB
	properties.Property("ZSTACK_OVN_SBDB_ADDRESS takes precedence over OVN_SB_DB", prop.ForAll(
		func(zstackAddr string, ovnAddr string) bool {
			// Clean up any existing env vars
			os.Unsetenv("ZSTACK_OVN_SBDB_ADDRESS")
			os.Unsetenv("OVN_SB_DB")
			defer func() {
				os.Unsetenv("ZSTACK_OVN_SBDB_ADDRESS")
				os.Unsetenv("OVN_SB_DB")
			}()

			// Set both env vars
			os.Setenv("ZSTACK_OVN_SBDB_ADDRESS", zstackAddr)
			os.Setenv("OVN_SB_DB", ovnAddr)

			cfg := DefaultConfig()
			cfg.ApplyEnvOverrides()

			// ZSTACK_OVN_SBDB_ADDRESS should take precedence
			return cfg.OVN.SBDBAddress == zstackAddr
		},
		genValidOVNAddress(),
		genValidOVNAddress(),
	))

	// Property: Config value preserved when no env vars set
	properties.Property("config value preserved when no env vars set", prop.ForAll(
		func(nbAddr string, sbAddr string) bool {
			// Clean up any existing env vars
			os.Unsetenv("ZSTACK_OVN_NBDB_ADDRESS")
			os.Unsetenv("ZSTACK_OVN_SBDB_ADDRESS")
			os.Unsetenv("OVN_NB_DB")
			os.Unsetenv("OVN_SB_DB")

			cfg := DefaultConfig()
			cfg.OVN.NBDBAddress = nbAddr
			cfg.OVN.SBDBAddress = sbAddr
			cfg.ApplyEnvOverrides()

			return cfg.OVN.NBDBAddress == nbAddr && cfg.OVN.SBDBAddress == sbAddr
		},
		genValidOVNAddress(),
		genValidOVNAddress(),
	))

	properties.TestingRun(t)
}
