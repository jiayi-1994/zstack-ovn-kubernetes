// Package allocator provides IP address allocation algorithms.
//
// SubnetAllocator implements efficient IP allocation for a subnet using bitmap algorithm.
// This is the same approach used by OVN-Kubernetes for IP address management.
//
// Reference: OVN-Kubernetes pkg/allocator/ip/subnet/allocator.go
package allocator

import (
	"fmt"
	"net"
	"strings"
	"sync"
)

// SubnetAllocator manages IP allocation for a single subnet.
// It uses a bitmap to track allocated IPs efficiently.
//
// Thread Safety: All methods are thread-safe.
//
// IP Allocation Rules:
//   - Network address (first IP) is never allocated
//   - Broadcast address (last IP for IPv4) is never allocated
//   - Gateway and other excluded IPs are pre-marked as allocated
//   - IPs are allocated sequentially from the first available
type SubnetAllocator struct {
	// mu protects concurrent access to the allocator
	mu sync.RWMutex

	// subnet is the CIDR network being managed
	subnet *net.IPNet

	// bitmap tracks allocated IPs
	// Index 0 = first usable IP (network address + 1)
	bitmap *Bitmap

	// excludeIPs is a set of IPs that should not be allocated
	// Key is the IP string representation
	excludeIPs map[string]struct{}

	// baseIP is the first IP in the subnet (network address)
	baseIP net.IP

	// size is the total number of usable IPs
	size int
}

// NewSubnetAllocator creates a new subnet allocator.
//
// Parameters:
//   - cidr: Subnet CIDR string (e.g., "10.244.1.0/24")
//   - excludeIPs: List of IPs to exclude from allocation (e.g., gateway)
//     Supports single IPs ("10.244.1.1") and ranges ("10.244.1.100-10.244.1.110")
//
// Returns:
//   - *SubnetAllocator: Allocator instance
//   - error: Error if CIDR is invalid
//
// Example:
//
//	allocator, err := NewSubnetAllocator("10.244.1.0/24", []string{"10.244.1.1"})
func NewSubnetAllocator(cidr string, excludeIPs []string) (*SubnetAllocator, error) {
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}

	// Calculate subnet size (number of usable IPs)
	// For IPv4: total IPs - network address - broadcast address
	ones, bits := subnet.Mask.Size()
	totalIPs := 1 << (bits - ones)

	// Usable IPs exclude network and broadcast addresses
	usableIPs := totalIPs - 2
	if usableIPs <= 0 {
		return nil, fmt.Errorf("subnet %s is too small for allocation", cidr)
	}

	allocator := &SubnetAllocator{
		subnet:     subnet,
		bitmap:     NewBitmap(usableIPs),
		excludeIPs: make(map[string]struct{}),
		baseIP:     subnet.IP.Mask(subnet.Mask),
		size:       usableIPs,
	}

	// Parse and mark excluded IPs
	for _, exclude := range excludeIPs {
		if err := allocator.parseAndExclude(exclude); err != nil {
			return nil, fmt.Errorf("invalid exclude IP %q: %w", exclude, err)
		}
	}

	return allocator, nil
}

// parseAndExclude parses an exclude IP specification and marks IPs as excluded.
// Supports single IPs and ranges (e.g., "10.244.1.100-10.244.1.110").
func (a *SubnetAllocator) parseAndExclude(exclude string) error {
	// Check if it's a range
	if strings.Contains(exclude, "-") {
		parts := strings.Split(exclude, "-")
		if len(parts) != 2 {
			return fmt.Errorf("invalid IP range format")
		}

		startIP := net.ParseIP(strings.TrimSpace(parts[0]))
		endIP := net.ParseIP(strings.TrimSpace(parts[1]))
		if startIP == nil || endIP == nil {
			return fmt.Errorf("invalid IP in range")
		}

		// Mark all IPs in range as excluded
		for ip := startIP; !ip.Equal(incrementIP(endIP)); ip = incrementIP(ip) {
			if err := a.excludeIP(ip); err != nil {
				// Ignore errors for IPs outside subnet
				continue
			}
		}
	} else {
		// Single IP
		ip := net.ParseIP(strings.TrimSpace(exclude))
		if ip == nil {
			return fmt.Errorf("invalid IP address")
		}
		if err := a.excludeIP(ip); err != nil {
			// Ignore errors for IPs outside subnet
			return nil
		}
	}
	return nil
}

// excludeIP marks a single IP as excluded (pre-allocated).
func (a *SubnetAllocator) excludeIP(ip net.IP) error {
	index, err := a.ipToIndex(ip)
	if err != nil {
		return err
	}

	a.excludeIPs[ip.String()] = struct{}{}
	// Ignore error if already set
	_ = a.bitmap.Set(index)
	return nil
}

// AllocateNext allocates the next available IP address.
//
// Returns:
//   - net.IP: Allocated IP address
//   - error: SubnetExhaustedError if no IPs available
//
// Example:
//
//	ip, err := allocator.AllocateNext()
//	if err != nil {
//	    // Handle subnet exhaustion
//	}
func (a *SubnetAllocator) AllocateNext() (net.IP, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	index := a.bitmap.FindFirstClear()
	if index == -1 {
		return nil, &SubnetExhaustedError{Subnet: a.subnet.String()}
	}

	if err := a.bitmap.Set(index); err != nil {
		return nil, fmt.Errorf("failed to allocate IP: %w", err)
	}

	return a.indexToIP(index), nil
}

// Allocate allocates a specific IP address.
//
// Parameters:
//   - ip: IP address to allocate
//
// Returns:
//   - error: IPAlreadyAllocatedError if IP is already allocated,
//     IPOutOfRangeError if IP is not in subnet
//
// Example:
//
//	err := allocator.Allocate(net.ParseIP("10.244.1.5"))
func (a *SubnetAllocator) Allocate(ip net.IP) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	index, err := a.ipToIndex(ip)
	if err != nil {
		return err
	}

	if a.bitmap.IsSet(index) {
		return &IPAlreadyAllocatedError{IP: ip.String()}
	}

	if err := a.bitmap.Set(index); err != nil {
		return fmt.Errorf("failed to allocate IP %s: %w", ip, err)
	}

	return nil
}

// Release releases an allocated IP address back to the pool.
//
// Parameters:
//   - ip: IP address to release
//
// Returns:
//   - error: Error if IP is not in subnet or is an excluded IP
//
// Example:
//
//	err := allocator.Release(net.ParseIP("10.244.1.5"))
func (a *SubnetAllocator) Release(ip net.IP) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Check if IP is in exclude list
	if _, excluded := a.excludeIPs[ip.String()]; excluded {
		return fmt.Errorf("cannot release excluded IP %s", ip)
	}

	index, err := a.ipToIndex(ip)
	if err != nil {
		return err
	}

	return a.bitmap.Clear(index)
}

// IsAllocated checks if an IP is currently allocated.
//
// Parameters:
//   - ip: IP address to check
//
// Returns:
//   - bool: True if allocated, false if available or out of range
func (a *SubnetAllocator) IsAllocated(ip net.IP) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	index, err := a.ipToIndex(ip)
	if err != nil {
		return false
	}

	return a.bitmap.IsSet(index)
}

// Available returns the number of available IPs.
func (a *SubnetAllocator) Available() int {
	return a.bitmap.Available()
}

// Used returns the number of allocated IPs.
func (a *SubnetAllocator) Used() int {
	return a.bitmap.Allocated()
}

// Size returns the total number of usable IPs in the subnet.
func (a *SubnetAllocator) Size() int {
	return a.size
}

// Subnet returns the subnet CIDR.
func (a *SubnetAllocator) Subnet() *net.IPNet {
	return a.subnet
}

// ipToIndex converts an IP address to a bitmap index.
// Index 0 corresponds to the first usable IP (network address + 1).
func (a *SubnetAllocator) ipToIndex(ip net.IP) (int, error) {
	// Normalize IP to 4-byte representation for IPv4
	ip = ip.To4()
	if ip == nil {
		ip = ip.To16()
	}

	if !a.subnet.Contains(ip) {
		return -1, &IPOutOfRangeError{IP: ip.String(), Subnet: a.subnet.String()}
	}

	// Calculate offset from base IP
	// Skip network address (index 0 = baseIP + 1)
	baseIP := a.baseIP.To4()
	if baseIP == nil {
		baseIP = a.baseIP.To16()
	}

	ipInt := ipToInt(ip)
	baseInt := ipToInt(baseIP)
	// Ensure we don't underflow
	if ipInt <= baseInt {
		return -1, &IPOutOfRangeError{IP: ip.String(), Subnet: a.subnet.String()}
	}
	offset := int(ipInt - baseInt - 1)
	if offset < 0 || offset >= a.size {
		return -1, &IPOutOfRangeError{IP: ip.String(), Subnet: a.subnet.String()}
	}

	return offset, nil
}

// indexToIP converts a bitmap index to an IP address.
func (a *SubnetAllocator) indexToIP(index int) net.IP {
	// Index 0 = baseIP + 1 (skip network address)
	baseInt := ipToInt(a.baseIP.To4())
	ipInt := baseInt + uint32(index) + 1
	return intToIP(ipInt)
}

// ipToInt converts an IPv4 address to a uint32.
func ipToInt(ip net.IP) uint32 {
	ip = ip.To4()
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

// intToIP converts a uint32 to an IPv4 address.
func intToIP(n uint32) net.IP {
	return net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

// incrementIP returns the next IP address.
func incrementIP(ip net.IP) net.IP {
	ip = ip.To4()
	if ip == nil {
		return nil
	}
	result := make(net.IP, 4)
	copy(result, ip)

	for i := 3; i >= 0; i-- {
		result[i]++
		if result[i] != 0 {
			break
		}
	}
	return result
}
