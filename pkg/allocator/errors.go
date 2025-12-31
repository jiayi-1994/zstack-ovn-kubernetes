// Package allocator provides IP address allocation algorithms.
package allocator

import "fmt"

// SubnetExhaustedError indicates that a subnet has no available IPs.
type SubnetExhaustedError struct {
	Subnet string
}

func (e *SubnetExhaustedError) Error() string {
	return fmt.Sprintf("subnet %s has no available IPs", e.Subnet)
}

// IPAlreadyAllocatedError indicates that an IP is already allocated.
type IPAlreadyAllocatedError struct {
	IP string
}

func (e *IPAlreadyAllocatedError) Error() string {
	return fmt.Sprintf("IP %s is already allocated", e.IP)
}

// IPOutOfRangeError indicates that an IP is not within the subnet range.
type IPOutOfRangeError struct {
	IP     string
	Subnet string
}

func (e *IPOutOfRangeError) Error() string {
	return fmt.Sprintf("IP %s is not in subnet %s", e.IP, e.Subnet)
}
