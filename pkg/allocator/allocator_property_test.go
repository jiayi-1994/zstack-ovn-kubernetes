// Package allocator provides IP address allocation algorithms.
//
// Property-based tests for IP allocator.
// Feature: zstack-ovn-kubernetes-cni, Property 3: IP 分配唯一性
// Validates: Requirements 3.4, 4.1, 12.1
package allocator

import (
	"net"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestProperty_IPAllocationUniqueness verifies that allocated IPs are unique within a subnet.
// Property: Each IP address in the same subnet can only be allocated to one Pod.
func TestProperty_IPAllocationUniqueness(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("allocated IPs are unique within subnet", prop.ForAll(
		func(numAllocations int) bool {
			allocator, err := NewSubnetAllocator("10.244.0.0/24", nil)
			if err != nil {
				return false
			}

			allocated := make(map[string]bool)

			for i := 0; i < numAllocations; i++ {
				ip, err := allocator.AllocateNext()
				if err != nil {
					// Subnet exhaustion is expected behavior
					_, ok := err.(*SubnetExhaustedError)
					return ok
				}
				ipStr := ip.String()
				if allocated[ipStr] {
					t.Logf("Duplicate IP allocated: %s", ipStr)
					return false // Found duplicate IP
				}
				allocated[ipStr] = true
			}
			return true
		},
		gen.IntRange(1, 254), // Generate 1-254 allocations (max for /24)
	))

	properties.TestingRun(t)
}

// TestProperty_AllocatedIPsInSubnetRange verifies that all allocated IPs are within the subnet CIDR.
// Property: Allocated IP addresses must be within the subnet CIDR range.
func TestProperty_AllocatedIPsInSubnetRange(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("allocated IPs are within subnet range", prop.ForAll(
		func(thirdOctet int, numAllocations int) bool {
			cidr := "10.244." + itoa(thirdOctet) + ".0/24"
			allocator, err := NewSubnetAllocator(cidr, nil)
			if err != nil {
				return false
			}

			_, subnet, _ := net.ParseCIDR(cidr)

			for i := 0; i < numAllocations; i++ {
				ip, err := allocator.AllocateNext()
				if err != nil {
					// Subnet exhaustion is expected
					_, ok := err.(*SubnetExhaustedError)
					return ok
				}
				if !subnet.Contains(ip) {
					t.Logf("IP %s not in subnet %s", ip, cidr)
					return false
				}
			}
			return true
		},
		gen.IntRange(0, 255),  // Third octet
		gen.IntRange(1, 100),  // Number of allocations
	))

	properties.TestingRun(t)
}

// TestProperty_ExcludedIPsNotAllocated verifies that excluded IPs are never allocated.
// Property: Excluded IP addresses will not be allocated.
func TestProperty_ExcludedIPsNotAllocated(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("excluded IPs are never allocated", prop.ForAll(
		func(excludeOffset int) bool {
			// Create exclude IP based on offset
			excludeIP := "10.244.1." + itoa(excludeOffset+1) // +1 to skip network address
			allocator, err := NewSubnetAllocator("10.244.1.0/24", []string{excludeIP})
			if err != nil {
				return false
			}

			// Allocate all available IPs
			allocated := make(map[string]bool)
			for {
				ip, err := allocator.AllocateNext()
				if err != nil {
					break // Subnet exhausted
				}
				allocated[ip.String()] = true
			}

			// Verify excluded IP was not allocated
			if allocated[excludeIP] {
				t.Logf("Excluded IP %s was allocated", excludeIP)
				return false
			}
			return true
		},
		gen.IntRange(1, 253), // Valid host addresses in /24 (1-254, excluding broadcast)
	))

	properties.TestingRun(t)
}

// TestProperty_ReleaseAndReallocate verifies that released IPs can be reallocated.
// Property: After releasing an IP, it should be available for reallocation.
func TestProperty_ReleaseAndReallocate(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("released IPs can be reallocated", prop.ForAll(
		func(numAllocations int) bool {
			allocator, err := NewSubnetAllocator("10.244.2.0/24", nil)
			if err != nil {
				return false
			}

			// Allocate some IPs
			var allocatedIPs []net.IP
			for i := 0; i < numAllocations; i++ {
				ip, err := allocator.AllocateNext()
				if err != nil {
					break
				}
				allocatedIPs = append(allocatedIPs, ip)
			}

			if len(allocatedIPs) == 0 {
				return true // Nothing to test
			}

			// Release all allocated IPs
			for _, ip := range allocatedIPs {
				if err := allocator.Release(ip); err != nil {
					t.Logf("Failed to release IP %s: %v", ip, err)
					return false
				}
			}

			// Verify all IPs can be reallocated
			for i := 0; i < len(allocatedIPs); i++ {
				_, err := allocator.AllocateNext()
				if err != nil {
					t.Logf("Failed to reallocate after release: %v", err)
					return false
				}
			}

			return true
		},
		gen.IntRange(1, 50),
	))

	properties.TestingRun(t)
}

// TestProperty_SpecificIPAllocation verifies that specific IP allocation works correctly.
// Property: Allocating a specific IP should succeed if available, fail if already allocated.
func TestProperty_SpecificIPAllocation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("specific IP allocation is idempotent-safe", prop.ForAll(
		func(ipOffset int) bool {
			allocator, err := NewSubnetAllocator("10.244.3.0/24", nil)
			if err != nil {
				return false
			}

			// Create specific IP
			ip := net.ParseIP("10.244.3." + itoa(ipOffset+1))

			// First allocation should succeed
			err = allocator.Allocate(ip)
			if err != nil {
				t.Logf("First allocation of %s failed: %v", ip, err)
				return false
			}

			// Second allocation should fail with IPAlreadyAllocatedError
			err = allocator.Allocate(ip)
			if err == nil {
				t.Logf("Second allocation of %s should have failed", ip)
				return false
			}
			_, ok := err.(*IPAlreadyAllocatedError)
			if !ok {
				t.Logf("Expected IPAlreadyAllocatedError, got %T", err)
				return false
			}

			return true
		},
		gen.IntRange(1, 253),
	))

	properties.TestingRun(t)
}

// itoa converts int to string (simple implementation to avoid strconv import)
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}
