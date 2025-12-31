// Package allocator provides IP address allocation algorithms.
//
// This package implements efficient IP allocation using bitmap algorithm,
// which is the same approach used by OVN-Kubernetes.
//
// Bitmap Algorithm:
// - Each bit represents an IP address in the subnet
// - Bit value 1 = allocated, 0 = available
// - Finding next available IP is O(n) where n is subnet size
// - Memory efficient: /24 subnet needs only 32 bytes
//
// Reference: OVN-Kubernetes pkg/allocator/bitmap/bitmap.go
package allocator

import (
	"fmt"
	"sync"
)

// Bitmap is a thread-safe bitmap for IP allocation
type Bitmap struct {
	// mu protects concurrent access
	mu sync.RWMutex

	// bits is the underlying byte slice
	// Each byte contains 8 bits, each bit represents one IP
	bits []byte

	// size is the total number of bits (IPs)
	size int

	// allocated is the count of allocated bits
	allocated int
}

// NewBitmap creates a new bitmap with the specified size
//
// Parameters:
//   - size: Number of bits (IPs) in the bitmap
//
// Returns:
//   - *Bitmap: Bitmap instance
func NewBitmap(size int) *Bitmap {
	// Calculate number of bytes needed (round up)
	numBytes := (size + 7) / 8
	return &Bitmap{
		bits: make([]byte, numBytes),
		size: size,
	}
}

// Set marks a bit as allocated
//
// Parameters:
//   - index: Bit index to set
//
// Returns:
//   - error: Error if index is out of range or already set
func (b *Bitmap) Set(index int) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if index < 0 || index >= b.size {
		return fmt.Errorf("index %d out of range [0, %d)", index, b.size)
	}

	byteIndex := index / 8
	bitIndex := uint(index % 8)

	if b.bits[byteIndex]&(1<<bitIndex) != 0 {
		return fmt.Errorf("bit %d is already set", index)
	}

	b.bits[byteIndex] |= 1 << bitIndex
	b.allocated++
	return nil
}

// Clear marks a bit as available
//
// Parameters:
//   - index: Bit index to clear
//
// Returns:
//   - error: Error if index is out of range
func (b *Bitmap) Clear(index int) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if index < 0 || index >= b.size {
		return fmt.Errorf("index %d out of range [0, %d)", index, b.size)
	}

	byteIndex := index / 8
	bitIndex := uint(index % 8)

	if b.bits[byteIndex]&(1<<bitIndex) != 0 {
		b.bits[byteIndex] &^= 1 << bitIndex
		b.allocated--
	}
	return nil
}

// IsSet checks if a bit is allocated
//
// Parameters:
//   - index: Bit index to check
//
// Returns:
//   - bool: True if allocated, false if available
func (b *Bitmap) IsSet(index int) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if index < 0 || index >= b.size {
		return false
	}

	byteIndex := index / 8
	bitIndex := uint(index % 8)
	return b.bits[byteIndex]&(1<<bitIndex) != 0
}

// FindFirstClear finds the first available (unset) bit
//
// Returns:
//   - int: Index of first available bit, or -1 if none available
func (b *Bitmap) FindFirstClear() int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for i := 0; i < b.size; i++ {
		byteIndex := i / 8
		bitIndex := uint(i % 8)
		if b.bits[byteIndex]&(1<<bitIndex) == 0 {
			return i
		}
	}
	return -1
}

// Size returns the total number of bits
func (b *Bitmap) Size() int {
	return b.size
}

// Allocated returns the number of allocated bits
func (b *Bitmap) Allocated() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.allocated
}

// Available returns the number of available bits
func (b *Bitmap) Available() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.size - b.allocated
}
