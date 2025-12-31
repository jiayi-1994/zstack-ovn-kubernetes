// Package ovndb provides OVN database transaction helpers.
//
// This file contains helper functions for executing OVN database transactions
// with proper error handling and retry logic.
//
// Transaction Patterns:
// 1. Single operation: Use TransactAndCheck for simple CRUD operations
// 2. Multiple operations: Build ops slice and execute atomically
// 3. Conditional operations: Use predicates to filter objects
//
// Reference: OVN-Kubernetes pkg/libovsdb/ops/transact.go
package ovndb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/ovsdb"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

// ErrNotFound is returned when an object is not found in the database
var ErrNotFound = client.ErrNotFound

// TransactWithRetry executes a transaction with retry on connection errors
//
// This function will retry the transaction if the client is disconnected,
// using polling with a 200ms interval until the context is cancelled.
//
// Parameters:
//   - ctx: Context for cancellation and timeout
//   - c: OVN database client
//   - ops: List of OVSDB operations to execute
//
// Returns:
//   - []ovsdb.OperationResult: Results of each operation
//   - error: Transaction error
func TransactWithRetry(ctx context.Context, c client.Client, ops []ovsdb.Operation) ([]ovsdb.OperationResult, error) {
	var results []ovsdb.OperationResult
	resultErr := wait.PollUntilContextCancel(ctx, 200*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		var err error
		results, err = c.Transact(ctx, ops...)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, client.ErrNotConnected) {
			klog.V(5).Infof("Unable to execute transaction: %+v. Client is disconnected, will retry...", ops)
			return false, nil
		}
		return false, err
	})
	return results, resultErr
}

// TransactAndCheck executes a transaction and checks for errors
//
// This function:
// 1. Executes the transaction with retry
// 2. Checks operation results for errors
// 3. Returns detailed error information if any operation fails
//
// Parameters:
//   - c: OVN database client
//   - ops: List of OVSDB operations to execute
//   - timeout: Transaction timeout
//
// Returns:
//   - []ovsdb.OperationResult: Results of each operation
//   - error: Transaction or operation error
func TransactAndCheck(c client.Client, ops []ovsdb.Operation, timeout time.Duration) ([]ovsdb.OperationResult, error) {
	if len(ops) == 0 {
		return []ovsdb.OperationResult{{}}, nil
	}

	klog.V(5).Infof("Executing OVN transaction: %+v", ops)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	results, err := TransactWithRetry(ctx, c, ops)
	if err != nil {
		return nil, fmt.Errorf("transaction failed with ops %+v: %w", ops, err)
	}

	// Check for operation-level errors
	opErrors, err := ovsdb.CheckOperationResults(results, ops)
	if err != nil {
		return nil, fmt.Errorf("operation failed with ops %+v results %+v errors %+v: %w", ops, results, opErrors, err)
	}

	return results, nil
}

// BuildNamedUUID generates a named UUID for insert operations
//
// Named UUIDs allow referencing newly inserted rows in the same transaction.
// The format is "named-uuid-<name>" which libovsdb recognizes as a named UUID.
//
// Parameters:
//   - name: Base name for the UUID
//
// Returns:
//   - string: Named UUID string
func BuildNamedUUID(name string) string {
	return fmt.Sprintf("named-uuid-%s", name)
}

// IsNamedUUID checks if a UUID is a named UUID
func IsNamedUUID(uuid string) bool {
	return len(uuid) > 11 && uuid[:11] == "named-uuid-"
}

// GetUUIDFromResult extracts the UUID from an insert operation result
//
// Parameters:
//   - result: Operation result from an insert operation
//
// Returns:
//   - string: The UUID of the inserted row
func GetUUIDFromResult(result ovsdb.OperationResult) string {
	return result.UUID.GoUUID
}

// OperationBuilder helps build OVSDB operations
type OperationBuilder struct {
	ops []ovsdb.Operation
}

// NewOperationBuilder creates a new operation builder
func NewOperationBuilder() *OperationBuilder {
	return &OperationBuilder{
		ops: make([]ovsdb.Operation, 0),
	}
}

// Add adds an operation to the builder
func (b *OperationBuilder) Add(op ovsdb.Operation) *OperationBuilder {
	b.ops = append(b.ops, op)
	return b
}

// AddAll adds multiple operations to the builder
func (b *OperationBuilder) AddAll(ops []ovsdb.Operation) *OperationBuilder {
	b.ops = append(b.ops, ops...)
	return b
}

// Build returns the built operations
func (b *OperationBuilder) Build() []ovsdb.Operation {
	return b.ops
}

// Len returns the number of operations
func (b *OperationBuilder) Len() int {
	return len(b.ops)
}

// Clear clears all operations
func (b *OperationBuilder) Clear() *OperationBuilder {
	b.ops = b.ops[:0]
	return b
}
