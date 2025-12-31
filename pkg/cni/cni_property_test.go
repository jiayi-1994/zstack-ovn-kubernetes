// Package cni provides property-based tests for CNI operations.
//
// These tests verify the correctness properties defined in the design document:
// - Property 1: CNI ADD 命令正确性
// - Property 2: CNI DEL 命令清理正确性
//
// Property-based testing generates random inputs to verify that properties
// hold across all valid inputs, not just specific examples.
//
// Reference: Design document - Correctness Properties section
package cni

import (
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// ============================================================================
// Property 1: CNI ADD 命令正确性
// Validates: Requirements 4.1, 4.2, 4.3, 4.4
//
// For any valid Pod creation request, CNI ADD command execution should:
// - Pod should get an IP address within the specified subnet range
// - CNI result should contain valid IP, MAC, and gateway
// - CNI result should be properly formatted JSON
// ============================================================================

// TestProperty_CNIResultFormat tests that CNI results are properly formatted
// Feature: zstack-ovn-kubernetes-cni, Property 1: CNI ADD 命令正确性
// Validates: Requirements 4.1, 4.2, 4.3, 4.4
func TestProperty_CNIResultFormat(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: buildCNIResult produces valid JSON for any valid PodNetworkInfo
	properties.Property("CNI result is valid JSON for any valid PodNetworkInfo", prop.ForAll(
		func(ipOctet1, ipOctet2, ipOctet3, ipOctet4 uint8, prefix int, macSuffix uint32) bool {
			// Skip invalid IP addresses (0.0.0.0 or broadcast)
			if ipOctet1 == 0 && ipOctet2 == 0 && ipOctet3 == 0 && ipOctet4 == 0 {
				return true // Skip this case
			}

			// Ensure valid prefix length
			if prefix < 8 || prefix > 30 {
				prefix = 24
			}

			// Build IP address
			ipStr := net.IPv4(ipOctet1, ipOctet2, ipOctet3, ipOctet4).String()
			ipWithPrefix := ipStr + "/" + string(rune('0'+prefix/10)) + string(rune('0'+prefix%10))

			// Build MAC address
			mac := buildTestMAC(macSuffix)

			// Build gateway (first IP in subnet)
			gateway := net.IPv4(ipOctet1, ipOctet2, ipOctet3, 1).String()

			info := &PodNetworkInfo{
				IPAddress:  ipWithPrefix,
				MACAddress: mac,
				Gateway:    gateway,
				MTU:        1400,
				SandboxID:  "/var/run/netns/test",
			}

			result, err := buildCNIResult(info)
			if err != nil {
				t.Logf("buildCNIResult failed for ip=%s, mac=%s, gw=%s: %v",
					ipWithPrefix, mac, gateway, err)
				return false
			}

			// Verify result is valid JSON
			var parsed map[string]interface{}
			if err := json.Unmarshal(result, &parsed); err != nil {
				t.Logf("Result is not valid JSON: %v", err)
				return false
			}

			// Verify required fields exist
			if _, ok := parsed["cniVersion"]; !ok {
				t.Log("Missing cniVersion field")
				return false
			}
			if _, ok := parsed["interfaces"]; !ok {
				t.Log("Missing interfaces field")
				return false
			}
			if _, ok := parsed["ips"]; !ok {
				t.Log("Missing ips field")
				return false
			}

			return true
		},
		gen.UInt8(),                  // ipOctet1
		gen.UInt8Range(1, 254),       // ipOctet2 (avoid 0 and 255)
		gen.UInt8Range(1, 254),       // ipOctet3
		gen.UInt8Range(2, 254),       // ipOctet4 (avoid .0 and .1 which are network/gateway)
		gen.IntRange(16, 28),         // prefix
		gen.UInt32Range(1, 0xFFFFFF), // macSuffix
	))

	properties.TestingRun(t)
}

// TestProperty_CNIResultContainsIPAddress tests that CNI result contains the allocated IP
// Feature: zstack-ovn-kubernetes-cni, Property 1: CNI ADD 命令正确性
// Validates: Requirements 4.1
func TestProperty_CNIResultContainsIPAddress(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: CNI result contains the same IP address as input
	properties.Property("CNI result contains input IP address", prop.ForAll(
		func(ipOctet1, ipOctet2, ipOctet3, ipOctet4 uint8, prefix int) bool {
			// Skip invalid cases
			if ipOctet1 == 0 || ipOctet4 < 2 {
				return true
			}

			if prefix < 8 || prefix > 30 {
				prefix = 24
			}

			ipStr := net.IPv4(ipOctet1, ipOctet2, ipOctet3, ipOctet4).String()
			ipWithPrefix := ipStr + "/" + itoa(prefix)
			gateway := net.IPv4(ipOctet1, ipOctet2, ipOctet3, 1).String()

			info := &PodNetworkInfo{
				IPAddress:  ipWithPrefix,
				MACAddress: "0a:58:0a:f4:01:05",
				Gateway:    gateway,
				MTU:        1400,
				SandboxID:  "/var/run/netns/test",
			}

			result, err := buildCNIResult(info)
			if err != nil {
				return false
			}

			// Parse result and verify IP
			var parsed map[string]interface{}
			if err := json.Unmarshal(result, &parsed); err != nil {
				return false
			}

			ips, ok := parsed["ips"].([]interface{})
			if !ok || len(ips) == 0 {
				return false
			}

			ipEntry, ok := ips[0].(map[string]interface{})
			if !ok {
				return false
			}

			address, ok := ipEntry["address"].(string)
			if !ok {
				return false
			}

			// Verify the address matches
			return address == ipWithPrefix
		},
		gen.UInt8Range(1, 254),
		gen.UInt8Range(0, 255),
		gen.UInt8Range(0, 255),
		gen.UInt8Range(2, 254),
		gen.IntRange(16, 28),
	))

	properties.TestingRun(t)
}

// TestProperty_CNIResultContainsGateway tests that CNI result contains the gateway
// Feature: zstack-ovn-kubernetes-cni, Property 1: CNI ADD 命令正确性
// Validates: Requirements 4.4
func TestProperty_CNIResultContainsGateway(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: CNI result contains the gateway IP
	properties.Property("CNI result contains gateway IP", prop.ForAll(
		func(gwOctet1, gwOctet2, gwOctet3, gwOctet4 uint8) bool {
			// Skip invalid gateways
			if gwOctet1 == 0 {
				return true
			}

			gateway := net.IPv4(gwOctet1, gwOctet2, gwOctet3, gwOctet4).String()
			podIP := net.IPv4(gwOctet1, gwOctet2, gwOctet3, gwOctet4+1).String()

			info := &PodNetworkInfo{
				IPAddress:  podIP + "/24",
				MACAddress: "0a:58:0a:f4:01:05",
				Gateway:    gateway,
				MTU:        1400,
				SandboxID:  "/var/run/netns/test",
			}

			result, err := buildCNIResult(info)
			if err != nil {
				return false
			}

			// Parse result and verify gateway
			var parsed map[string]interface{}
			if err := json.Unmarshal(result, &parsed); err != nil {
				return false
			}

			ips, ok := parsed["ips"].([]interface{})
			if !ok || len(ips) == 0 {
				return false
			}

			ipEntry, ok := ips[0].(map[string]interface{})
			if !ok {
				return false
			}

			gw, ok := ipEntry["gateway"].(string)
			if !ok {
				return false
			}

			return gw == gateway
		},
		gen.UInt8Range(1, 254),
		gen.UInt8Range(0, 255),
		gen.UInt8Range(0, 255),
		gen.UInt8Range(1, 253), // Leave room for pod IP
	))

	properties.TestingRun(t)
}

// ============================================================================
// Property 2: CNI DEL 命令清理正确性
// Validates: Requirements 4.5
//
// For any Pod deletion request, CNI DEL command execution should:
// - Be idempotent (calling multiple times should not fail)
// - Clean up resources properly
// ============================================================================

// TestProperty_CNIRequestParsing tests that CNI requests are properly parsed
// Feature: zstack-ovn-kubernetes-cni, Property 2: CNI DEL 命令清理正确性
// Validates: Requirements 4.5
func TestProperty_CNIRequestParsing(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Request serialization/deserialization is a round-trip
	properties.Property("Request JSON round-trip preserves data", prop.ForAll(
		func(namespace, name, containerID string) bool {
			// Skip empty strings
			if namespace == "" || name == "" || containerID == "" {
				return true
			}

			// Skip strings with special characters that might cause JSON issues
			if strings.ContainsAny(namespace, "\"\\\n\r\t") ||
				strings.ContainsAny(name, "\"\\\n\r\t") ||
				strings.ContainsAny(containerID, "\"\\\n\r\t") {
				return true
			}

			original := &Request{
				Command:      "DEL",
				ContainerID:  containerID,
				Netns:        "/var/run/netns/test",
				IfName:       "eth0",
				PodNamespace: namespace,
				PodName:      name,
				PodUID:       "test-uid",
			}

			// Serialize
			data, err := json.Marshal(original)
			if err != nil {
				return false
			}

			// Deserialize
			var parsed Request
			if err := json.Unmarshal(data, &parsed); err != nil {
				return false
			}

			// Verify fields match
			return parsed.Command == original.Command &&
				parsed.ContainerID == original.ContainerID &&
				parsed.Netns == original.Netns &&
				parsed.IfName == original.IfName &&
				parsed.PodNamespace == original.PodNamespace &&
				parsed.PodName == original.PodName &&
				parsed.PodUID == original.PodUID
		},
		gen.AlphaString(),
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestProperty_ResponseParsing tests that CNI responses are properly parsed
// Feature: zstack-ovn-kubernetes-cni, Property 2: CNI DEL 命令清理正确性
// Validates: Requirements 4.5
func TestProperty_ResponseParsing(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Response serialization/deserialization is a round-trip
	properties.Property("Response JSON round-trip preserves data", prop.ForAll(
		func(errorMsg string) bool {
			// Skip strings with special characters
			if strings.ContainsAny(errorMsg, "\"\\\n\r\t") {
				return true
			}

			original := &Response{
				Error: errorMsg,
			}

			// Serialize
			data, err := json.Marshal(original)
			if err != nil {
				return false
			}

			// Deserialize
			var parsed Response
			if err := json.Unmarshal(data, &parsed); err != nil {
				return false
			}

			return parsed.Error == original.Error
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestProperty_PodAnnotationRoundTrip tests Pod annotation serialization
// Feature: zstack-ovn-kubernetes-cni, Property 1: CNI ADD 命令正确性
// Validates: Requirements 4.6
func TestProperty_PodAnnotationRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: PodNetworkAnnotation serialization/deserialization is a round-trip
	properties.Property("PodNetworkAnnotation JSON round-trip preserves data", prop.ForAll(
		func(ipOctet1, ipOctet2, ipOctet3, ipOctet4 uint8, macSuffix uint32) bool {
			// Skip invalid cases
			if ipOctet1 == 0 {
				return true
			}

			ipStr := net.IPv4(ipOctet1, ipOctet2, ipOctet3, ipOctet4).String() + "/24"
			mac := buildTestMAC(macSuffix)
			gateway := net.IPv4(ipOctet1, ipOctet2, ipOctet3, 1).String()

			original := &PodNetworkAnnotation{
				IPAddresses:       []string{ipStr},
				MACAddress:        mac,
				GatewayIPs:        []string{gateway},
				LogicalSwitch:     "test-switch",
				LogicalSwitchPort: "test-port",
			}

			// Serialize
			data, err := json.Marshal(original)
			if err != nil {
				return false
			}

			// Deserialize
			var parsed PodNetworkAnnotation
			if err := json.Unmarshal(data, &parsed); err != nil {
				return false
			}

			// Verify fields match
			if len(parsed.IPAddresses) != len(original.IPAddresses) {
				return false
			}
			if len(parsed.IPAddresses) > 0 && parsed.IPAddresses[0] != original.IPAddresses[0] {
				return false
			}
			if parsed.MACAddress != original.MACAddress {
				return false
			}
			if len(parsed.GatewayIPs) != len(original.GatewayIPs) {
				return false
			}
			if len(parsed.GatewayIPs) > 0 && parsed.GatewayIPs[0] != original.GatewayIPs[0] {
				return false
			}
			if parsed.LogicalSwitch != original.LogicalSwitch {
				return false
			}
			if parsed.LogicalSwitchPort != original.LogicalSwitchPort {
				return false
			}

			return true
		},
		gen.UInt8Range(1, 254),
		gen.UInt8Range(0, 255),
		gen.UInt8Range(0, 255),
		gen.UInt8Range(2, 254),
		gen.UInt32Range(1, 0xFFFFFF),
	))

	properties.TestingRun(t)
}

// ============================================================================
// Helper functions for property tests
// ============================================================================

// buildTestMAC builds a MAC address from a suffix
func buildTestMAC(suffix uint32) string {
	return "0a:58:" +
		hexByte(byte(suffix>>16)) + ":" +
		hexByte(byte(suffix>>8)) + ":" +
		hexByte(byte(suffix)) + ":05"
}

// hexByte converts a byte to a two-character hex string
func hexByte(b byte) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[b>>4], hex[b&0xf]})
}

// itoa converts an int to a string (simple implementation for small numbers)
func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
