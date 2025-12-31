// Package util provides utility functions for network operations.
//
// This package contains helper functions for:
// - IP address manipulation
// - Network interface operations
// - OVS command execution
//
// Reference: OVN-Kubernetes pkg/util/
package util

import (
	"fmt"
	"net"
)

// ParseCIDR parses a CIDR string and returns the IP network
//
// Parameters:
//   - cidr: CIDR string (e.g., "10.244.0.0/16")
//
// Returns:
//   - *net.IPNet: Parsed IP network
//   - error: Parse error
func ParseCIDR(cidr string) (*net.IPNet, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %s: %v", cidr, err)
	}
	return ipNet, nil
}

// IPInRange checks if an IP is within a CIDR range
//
// Parameters:
//   - ip: IP address to check
//   - cidr: CIDR range
//
// Returns:
//   - bool: True if IP is in range
func IPInRange(ip net.IP, cidr *net.IPNet) bool {
	return cidr.Contains(ip)
}

// NextIP returns the next IP address
//
// Parameters:
//   - ip: Current IP address
//
// Returns:
//   - net.IP: Next IP address
func NextIP(ip net.IP) net.IP {
	next := make(net.IP, len(ip))
	copy(next, ip)

	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next
}

// GenerateMAC generates a MAC address from an IP address
// Uses the OVN convention: 0a:58:xx:xx:xx:xx where xx is derived from IP
//
// Parameters:
//   - ip: IP address
//
// Returns:
//   - string: MAC address string
func GenerateMAC(ip net.IP) string {
	ip4 := ip.To4()
	if ip4 == nil {
		return ""
	}
	return fmt.Sprintf("0a:58:%02x:%02x:%02x:%02x", ip4[0], ip4[1], ip4[2], ip4[3])
}
