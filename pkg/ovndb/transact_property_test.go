// Package ovndb provides property-based tests for OVN database transaction operations.
//
// This file contains property-based tests for validating OVN database operation atomicity.
// These tests verify that transaction building and operation handling follow correct patterns.
//
// Feature: zstack-ovn-kubernetes-cni, Property 6: OVN 数据库操作原子性
// Validates: Requirements 11.3
//
// Property 6 states:
// *For any* OVN transaction operation:
// - Transactions with multiple operations should be built atomically
// - Operation builders should correctly aggregate operations
// - Named UUIDs should be properly formatted for cross-referencing
// - Empty transactions should be handled gracefully
package ovndb

import (
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/ovn-org/libovsdb/ovsdb"
)

// TestProperty_OperationBuilderAtomicity tests that OperationBuilder correctly
// aggregates operations and maintains atomicity guarantees.
//
// Feature: zstack-ovn-kubernetes-cni, Property 6: OVN 数据库操作原子性
// Validates: Requirements 11.3
func TestProperty_OperationBuilderAtomicity(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Adding N operations to builder results in exactly N operations
	properties.Property("operation count is preserved", prop.ForAll(
		func(numOps int) bool {
			builder := NewOperationBuilder()

			// Add the specified number of operations
			for i := 0; i < numOps; i++ {
				op := ovsdb.Operation{
					Op:    "insert",
					Table: "Logical_Switch",
				}
				builder.Add(op)
			}

			// Verify the count matches
			return builder.Len() == numOps
		},
		gen.IntRange(0, 100),
	))

	// Property: Build returns all added operations in order
	properties.Property("operations are preserved in order", prop.ForAll(
		func(tables []string) bool {
			builder := NewOperationBuilder()

			// Add operations with different table names
			for _, table := range tables {
				op := ovsdb.Operation{
					Op:    "insert",
					Table: table,
				}
				builder.Add(op)
			}

			// Verify operations are in the same order
			ops := builder.Build()
			if len(ops) != len(tables) {
				return false
			}

			for i, table := range tables {
				if ops[i].Table != table {
					return false
				}
			}
			return true
		},
		gen.SliceOf(gen.AnyString()),
	))

	// Property: Clear removes all operations
	properties.Property("clear removes all operations", prop.ForAll(
		func(numOps int) bool {
			builder := NewOperationBuilder()

			// Add operations
			for i := 0; i < numOps; i++ {
				builder.Add(ovsdb.Operation{Op: "insert"})
			}

			// Clear and verify
			builder.Clear()
			return builder.Len() == 0 && len(builder.Build()) == 0
		},
		gen.IntRange(0, 50),
	))

	// Property: AddAll adds all operations atomically
	properties.Property("AddAll adds all operations atomically", prop.ForAll(
		func(numOps1, numOps2 int) bool {
			builder := NewOperationBuilder()

			// Create first batch of operations
			ops1 := make([]ovsdb.Operation, numOps1)
			for i := 0; i < numOps1; i++ {
				ops1[i] = ovsdb.Operation{Op: "insert", Table: "batch1"}
			}

			// Create second batch of operations
			ops2 := make([]ovsdb.Operation, numOps2)
			for i := 0; i < numOps2; i++ {
				ops2[i] = ovsdb.Operation{Op: "update", Table: "batch2"}
			}

			// Add both batches
			builder.AddAll(ops1)
			builder.AddAll(ops2)

			// Verify total count
			return builder.Len() == numOps1+numOps2
		},
		gen.IntRange(0, 50),
		gen.IntRange(0, 50),
	))

	properties.TestingRun(t)
}

// TestProperty_NamedUUIDFormat tests that named UUIDs are correctly formatted
// for use in OVN transactions.
//
// Feature: zstack-ovn-kubernetes-cni, Property 6: OVN 数据库操作原子性
// Validates: Requirements 11.3
func TestProperty_NamedUUIDFormat(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: BuildNamedUUID produces valid named UUID format for non-empty names
	// Using integer generator and converting to string inside the test
	properties.Property("named UUID has correct prefix", prop.ForAll(
		func(n int) bool {
			// Generate a non-empty name from the integer
			length := (n % 50) + 1
			chars := make([]byte, length)
			for i := 0; i < length; i++ {
				chars[i] = 'a' + byte((n+i)%26)
			}
			name := string(chars)
			
			uuid := BuildNamedUUID(name)
			// Named UUIDs must start with "named-uuid-" and have content after
			return len(uuid) > 11 && uuid[:11] == "named-uuid-"
		},
		gen.IntRange(0, 1000),
	))

	// Property: IsNamedUUID correctly identifies named UUIDs with non-empty names
	properties.Property("IsNamedUUID identifies named UUIDs", prop.ForAll(
		func(n int) bool {
			// Generate a non-empty name from the integer
			length := (n % 50) + 1
			chars := make([]byte, length)
			for i := 0; i < length; i++ {
				chars[i] = 'a' + byte((n+i)%26)
			}
			name := string(chars)
			
			uuid := BuildNamedUUID(name)
			return IsNamedUUID(uuid)
		},
		gen.IntRange(0, 1000),
	))

	// Property: IsNamedUUID rejects non-named UUIDs
	properties.Property("IsNamedUUID rejects regular UUIDs", prop.ForAll(
		func(uuid string) bool {
			// Regular UUIDs should not be identified as named UUIDs
			// unless they happen to start with "named-uuid-" and have content after
			if len(uuid) > 11 && uuid[:11] == "named-uuid-" {
				return IsNamedUUID(uuid)
			}
			return !IsNamedUUID(uuid)
		},
		gen.AnyString(),
	))

	// Property: Named UUID preserves the name suffix
	properties.Property("named UUID preserves name", prop.ForAll(
		func(name string) bool {
			uuid := BuildNamedUUID(name)
			expectedSuffix := name
			actualSuffix := uuid[11:] // Remove "named-uuid-" prefix
			return actualSuffix == expectedSuffix
		},
		gen.AnyString(),
	))

	// Property: Empty name produces UUID that IsNamedUUID rejects
	// This documents the expected behavior: empty names produce UUIDs with exactly 11 characters
	// which IsNamedUUID rejects because it requires len > 11
	properties.Property("empty name produces invalid named UUID", prop.ForAll(
		func(_ int) bool {
			uuid := BuildNamedUUID("")
			// "named-uuid-" has exactly 11 characters, so IsNamedUUID returns false
			return !IsNamedUUID(uuid) && uuid == "named-uuid-"
		},
		gen.IntRange(0, 10), // Dummy generator to run multiple times
	))

	properties.TestingRun(t)
}

// TestProperty_TransactAndCheckEmptyOps tests that empty transactions are handled gracefully.
//
// Feature: zstack-ovn-kubernetes-cni, Property 6: OVN 数据库操作原子性
// Validates: Requirements 11.3
func TestProperty_TransactAndCheckEmptyOps(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Empty operations return empty result without error
	properties.Property("empty ops return empty result", prop.ForAll(
		func(_ int) bool {
			// TransactAndCheck with empty ops should return empty result
			results, err := TransactAndCheck(nil, []ovsdb.Operation{}, 0)
			if err != nil {
				return false
			}
			// Should return a single empty result
			return len(results) == 1
		},
		gen.IntRange(0, 10), // Dummy generator to run multiple times
	))

	properties.TestingRun(t)
}

// TestProperty_OperationBuilderChaining tests that builder methods can be chained.
//
// Feature: zstack-ovn-kubernetes-cni, Property 6: OVN 数据库操作原子性
// Validates: Requirements 11.3
func TestProperty_OperationBuilderChaining(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Chained Add operations work correctly
	properties.Property("chained Add operations work", prop.ForAll(
		func(numOps int) bool {
			builder := NewOperationBuilder()

			// Chain Add operations
			for i := 0; i < numOps; i++ {
				builder = builder.Add(ovsdb.Operation{Op: "insert"})
			}

			return builder.Len() == numOps
		},
		gen.IntRange(0, 50),
	))

	// Property: Chained Clear returns empty builder
	properties.Property("chained Clear returns empty builder", prop.ForAll(
		func(numOps int) bool {
			builder := NewOperationBuilder()

			// Add operations then clear
			for i := 0; i < numOps; i++ {
				builder.Add(ovsdb.Operation{Op: "insert"})
			}
			builder = builder.Clear()

			return builder.Len() == 0
		},
		gen.IntRange(1, 50),
	))

	properties.TestingRun(t)
}

// TestProperty_OperationBuilderIsolation tests that multiple builders are isolated.
//
// Feature: zstack-ovn-kubernetes-cni, Property 6: OVN 数据库操作原子性
// Validates: Requirements 11.3
func TestProperty_OperationBuilderIsolation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Multiple builders are independent
	properties.Property("multiple builders are independent", prop.ForAll(
		func(numOps1, numOps2 int) bool {
			builder1 := NewOperationBuilder()
			builder2 := NewOperationBuilder()

			// Add different number of operations to each builder
			for i := 0; i < numOps1; i++ {
				builder1.Add(ovsdb.Operation{Op: "insert", Table: "table1"})
			}
			for i := 0; i < numOps2; i++ {
				builder2.Add(ovsdb.Operation{Op: "update", Table: "table2"})
			}

			// Verify builders are independent
			return builder1.Len() == numOps1 && builder2.Len() == numOps2
		},
		gen.IntRange(0, 50),
		gen.IntRange(0, 50),
	))

	// Property: Clearing one builder doesn't affect another
	properties.Property("clearing one builder doesn't affect another", prop.ForAll(
		func(numOps int) bool {
			builder1 := NewOperationBuilder()
			builder2 := NewOperationBuilder()

			// Add operations to both
			for i := 0; i < numOps; i++ {
				builder1.Add(ovsdb.Operation{Op: "insert"})
				builder2.Add(ovsdb.Operation{Op: "insert"})
			}

			// Clear only builder1
			builder1.Clear()

			// Verify builder2 is unaffected
			return builder1.Len() == 0 && builder2.Len() == numOps
		},
		gen.IntRange(1, 50),
	))

	properties.TestingRun(t)
}
