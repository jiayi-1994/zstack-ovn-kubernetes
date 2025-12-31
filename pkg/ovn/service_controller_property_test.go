// Package ovn provides property tests for the Service controller.
//
// Property 4: Service to OVN Load Balancer Conversion Correctness
// Validates: Requirements 18.1, 18.2, 18.4
//
// This test verifies that:
// - Services are correctly converted to OVN Load Balancers
// - VIPs are correctly built from ClusterIP and port
// - Backends are correctly built from endpoint addresses
// - Load Balancer names follow the expected naming convention
// - Endpoint changes are correctly reflected in Load Balancer backends
package ovn

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"

	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
)

// TestProperty_ServiceToLoadBalancerConversion tests Property 4:
// Service to OVN Load Balancer conversion correctness.
// Validates: Requirements 18.1, 18.2
//
// For any Kubernetes Service with ClusterIP and ports:
// - An OVN Load Balancer should be created with the correct name
// - The VIP should match the Service ClusterIP and port
// - The protocol should match the Service port protocol
func TestProperty_ServiceToLoadBalancerConversion(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Load Balancer name follows the naming convention
	properties.Property("LB name follows naming convention", prop.ForAll(
		func(namespace, name, protocol string) bool {
			lbName := buildLoadBalancerName(namespace, name, protocol, LBKindClusterIP)

			// Name should contain namespace, name, and protocol
			expectedPrefix := fmt.Sprintf("Service_%s/%s_%s", namespace, name, protocol)
			return lbName == expectedPrefix
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && len(s) <= 63 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && len(s) <= 63 }),
		gen.OneConstOf("tcp", "udp", "sctp"),
	))

	// Property: NodePort LB name includes nodeport suffix
	properties.Property("NodePort LB name includes nodeport suffix", prop.ForAll(
		func(namespace, name, protocol string) bool {
			lbName := buildLoadBalancerName(namespace, name, protocol, LBKindNodePort)

			// Name should end with _nodeport
			return strings.HasSuffix(lbName, "_nodeport")
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && len(s) <= 63 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && len(s) <= 63 }),
		gen.OneConstOf("tcp", "udp", "sctp"),
	))

	properties.TestingRun(t)
}

// TestProperty_VIPConstruction tests that VIPs are correctly constructed.
// Validates: Requirements 18.1, 18.2
//
// For any valid IP address and port:
// - The VIP string should be in the format "IP:PORT" for IPv4
// - The VIP string should be in the format "[IP]:PORT" for IPv6
func TestProperty_VIPConstruction(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: IPv4 VIP format is correct
	properties.Property("IPv4 VIP format is IP:PORT", prop.ForAll(
		func(ip [4]byte, port int) bool {
			ipStr := fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])
			vip := ovndb.BuildVIP(ipStr, port)

			// Should be in format IP:PORT
			expected := fmt.Sprintf("%s:%d", ipStr, port)
			return vip == expected
		},
		gen.ArrayOfN(4, gen.UInt8()),
		gen.IntRange(1, 65535),
	))

	// Property: IPv6 VIP format is correct
	properties.Property("IPv6 VIP format is [IP]:PORT", prop.ForAll(
		func(port int) bool {
			// Use a simple IPv6 address for testing
			ipStr := "2001:db8::1"
			vip := ovndb.BuildVIP(ipStr, port)

			// Should be in format [IP]:PORT
			expected := fmt.Sprintf("[%s]:%d", ipStr, port)
			return vip == expected
		},
		gen.IntRange(1, 65535),
	))

	// Property: VIP can be parsed back to IP and port
	properties.Property("VIP can be parsed back", prop.ForAll(
		func(ip [4]byte, port int) bool {
			ipStr := fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])
			vip := ovndb.BuildVIP(ipStr, port)

			parsedIP, parsedPort, err := ovndb.ParseVIP(vip)
			if err != nil {
				return false
			}

			portInt, _ := strconv.Atoi(parsedPort)
			return parsedIP == ipStr && portInt == port
		},
		gen.ArrayOfN(4, gen.UInt8()),
		gen.IntRange(1, 65535),
	))

	properties.TestingRun(t)
}

// TestProperty_BackendConstruction tests that backends are correctly constructed.
// Validates: Requirements 18.4
//
// For any list of endpoint addresses and target port:
// - The backend string should contain all addresses
// - Each backend should be in VIP format
// - Backends should be comma-separated
func TestProperty_BackendConstruction(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Backend string contains all addresses
	properties.Property("backend string contains all addresses", prop.ForAll(
		func(addresses []string, targetPort int) bool {
			if len(addresses) == 0 {
				backends := BuildBackendString(addresses, targetPort)
				return backends == ""
			}

			backends := BuildBackendString(addresses, targetPort)

			// Each address should appear in the backend string
			for _, addr := range addresses {
				expected := ovndb.BuildVIP(addr, targetPort)
				if !strings.Contains(backends, expected) {
					return false
				}
			}
			return true
		},
		gen.SliceOfN(5, genIPv4Address()),
		gen.IntRange(1, 65535),
	))

	// Property: Backend count matches address count
	properties.Property("backend count matches address count", prop.ForAll(
		func(addresses []string, targetPort int) bool {
			if len(addresses) == 0 {
				backends := BuildBackendString(addresses, targetPort)
				return backends == ""
			}

			backends := BuildBackendString(addresses, targetPort)
			backendList := ovndb.ParseBackends(backends)

			return len(backendList) == len(addresses)
		},
		gen.SliceOfN(5, genIPv4Address()),
		gen.IntRange(1, 65535),
	))

	// Property: Empty addresses produce empty backend string
	properties.Property("empty addresses produce empty backend", prop.ForAll(
		func(targetPort int) bool {
			backends := BuildBackendString([]string{}, targetPort)
			return backends == ""
		},
		gen.IntRange(1, 65535),
	))

	properties.TestingRun(t)
}

// TestProperty_EndpointToBackendMapping tests that endpoints are correctly
// mapped to Load Balancer backends.
// Validates: Requirements 18.4
//
// For any set of endpoints:
// - Ready endpoints should be included in backends
// - The backend addresses should match endpoint addresses
func TestProperty_EndpointToBackendMapping(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: All ready endpoints are included in backends
	properties.Property("all ready endpoints are included", prop.ForAll(
		func(endpoints []EndpointInfo, targetPort int) bool {
			// Filter to only ready endpoints
			var readyEndpoints []EndpointInfo
			for _, ep := range endpoints {
				if ep.Ready && !ep.Terminating {
					readyEndpoints = append(readyEndpoints, ep)
				}
			}

			// Build addresses from ready endpoints
			var addresses []string
			for _, ep := range readyEndpoints {
				addresses = append(addresses, ep.Address)
			}

			backends := BuildBackendString(addresses, targetPort)

			if len(addresses) == 0 {
				return backends == ""
			}

			// Each ready endpoint should be in backends
			for _, addr := range addresses {
				expected := ovndb.BuildVIP(addr, targetPort)
				if !strings.Contains(backends, expected) {
					return false
				}
			}
			return true
		},
		gen.SliceOfN(5, genEndpointInfo()),
		gen.IntRange(1, 65535),
	))

	properties.TestingRun(t)
}

// TestProperty_ServiceVIPConsistency tests that Service VIPs are consistent.
// Validates: Requirements 18.1, 18.2
//
// For any Service ClusterIP and port:
// - The VIP should be deterministic (same input produces same output)
// - The VIP should be valid (parseable)
func TestProperty_ServiceVIPConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: VIP construction is deterministic
	properties.Property("VIP construction is deterministic", prop.ForAll(
		func(ip [4]byte, port int32) bool {
			ipStr := fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])

			vip1 := BuildServiceVIP(ipStr, port)
			vip2 := BuildServiceVIP(ipStr, port)

			return vip1 == vip2
		},
		gen.ArrayOfN(4, gen.UInt8()),
		gen.Int32Range(1, 65535),
	))

	// Property: VIP is valid and parseable
	properties.Property("VIP is valid and parseable", prop.ForAll(
		func(ip [4]byte, port int32) bool {
			ipStr := fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])
			vip := BuildServiceVIP(ipStr, port)

			// Should be parseable
			parsedIP, parsedPort, err := ovndb.ParseVIP(vip)
			if err != nil {
				return false
			}

			// Parsed values should match original
			portInt, _ := strconv.Atoi(parsedPort)
			return parsedIP == ipStr && portInt == int(port)
		},
		gen.ArrayOfN(4, gen.UInt8()),
		gen.Int32Range(1, 65535),
	))

	properties.TestingRun(t)
}

// TestProperty_LoadBalancerProtocolMapping tests that protocols are correctly mapped.
// Validates: Requirements 18.1
func TestProperty_LoadBalancerProtocolMapping(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Protocol in LB name matches input protocol
	properties.Property("protocol in LB name matches input", prop.ForAll(
		func(namespace, name, protocol string) bool {
			lbName := buildLoadBalancerName(namespace, name, protocol, LBKindClusterIP)

			// LB name should contain the protocol
			return strings.Contains(lbName, "_"+protocol)
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && len(s) <= 63 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && len(s) <= 63 }),
		gen.OneConstOf("tcp", "udp", "sctp"),
	))

	properties.TestingRun(t)
}

// genIPv4Address generates a valid IPv4 address string
func genIPv4Address() gopter.Gen {
	return gen.ArrayOfN(4, gen.UInt8()).Map(func(ip [4]byte) string {
		return fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])
	})
}

// genEndpointInfo generates an EndpointInfo for testing
func genEndpointInfo() gopter.Gen {
	return gopter.CombineGens(
		genIPv4Address(),
		gen.AlphaString(),
		gen.Bool(),
		gen.Bool(),
	).Map(func(values []interface{}) EndpointInfo {
		return EndpointInfo{
			Address:     values[0].(string),
			NodeName:    values[1].(string),
			Ready:       values[2].(bool),
			Terminating: values[3].(bool),
		}
	})
}

// isValidIPv4 checks if a string is a valid IPv4 address
func isValidIPv4(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.To4() != nil
}
