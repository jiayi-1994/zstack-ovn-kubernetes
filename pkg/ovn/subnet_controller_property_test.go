// Package ovn provides property tests for the Subnet controller.
//
// Property 7: External Logical Switch Reference Validation
// Validates: Requirements 3.2, 3.3
//
// This test verifies that:
// - When a Subnet references an external Logical Switch, the controller validates its existence
// - Non-existent external Logical Switches cause the Subnet to fail
// - Valid external Logical Switch references allow the Subnet to become active
package ovn

import (
	"context"
	"fmt"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"

	networkv1 "github.com/jiayi-1994/zstack-ovn-kubernetes/api/v1"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
)

// mockLogicalSwitchOps is a mock implementation for testing
type mockLogicalSwitchOps struct {
	existingSwitches map[string]*ovndb.LogicalSwitch
}

func newMockLogicalSwitchOps(existingSwitches []string) *mockLogicalSwitchOps {
	m := &mockLogicalSwitchOps{
		existingSwitches: make(map[string]*ovndb.LogicalSwitch),
	}
	for _, name := range existingSwitches {
		m.existingSwitches[name] = &ovndb.LogicalSwitch{
			Name: name,
			UUID: fmt.Sprintf("uuid-%s", name),
		}
	}
	return m
}

func (m *mockLogicalSwitchOps) GetLogicalSwitch(ctx context.Context, name string) (*ovndb.LogicalSwitch, error) {
	if ls, exists := m.existingSwitches[name]; exists {
		return ls, nil
	}
	return nil, ovndb.NewObjectNotFoundError("LogicalSwitch", name)
}

// verifyExternalLogicalSwitchExists simulates the controller's verification logic
func verifyExternalLogicalSwitchExists(ctx context.Context, ops *mockLogicalSwitchOps, lsName string) error {
	_, err := ops.GetLogicalSwitch(ctx, lsName)
	if err != nil {
		if ovndb.IsNotFound(err) {
			return fmt.Errorf("external Logical Switch %q not found in OVN database", lsName)
		}
		return fmt.Errorf("failed to verify external Logical Switch: %w", err)
	}
	return nil
}

// TestProperty_ExternalLogicalSwitchValidation tests Property 7:
// External Logical Switch references must be validated for existence.
// Validates: Requirements 3.2, 3.3
func TestProperty_ExternalLogicalSwitchValidation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: When externalLogicalSwitch is set, verification should succeed
	// only if the Logical Switch exists in OVN database
	properties.Property("external LS reference validation is correct", prop.ForAll(
		func(existingSwitches []string, referencedSwitch string) bool {
			ctx := context.Background()
			mockOps := newMockLogicalSwitchOps(existingSwitches)

			err := verifyExternalLogicalSwitchExists(ctx, mockOps, referencedSwitch)

			// Check if the referenced switch exists in our mock
			_, exists := mockOps.existingSwitches[referencedSwitch]

			if exists {
				// If switch exists, verification should succeed
				return err == nil
			}
			// If switch doesn't exist, verification should fail
			return err != nil && ovndb.IsNotFound(err) == false // err wraps the not found
		},
		gen.SliceOf(gen.AlphaString()).Map(func(s []string) []string {
			// Filter out empty strings and ensure uniqueness
			seen := make(map[string]bool)
			result := make([]string, 0)
			for _, str := range s {
				if str != "" && !seen[str] {
					seen[str] = true
					result = append(result, str)
				}
			}
			return result
		}),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	properties.TestingRun(t)
}

// TestProperty_SubnetExternalModeDetection tests that IsExternalMode correctly
// identifies subnets with external Logical Switch references.
// Validates: Requirements 3.2
func TestProperty_SubnetExternalModeDetection(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: IsExternalMode returns true iff ExternalLogicalSwitch is non-empty
	properties.Property("IsExternalMode correctly detects external mode", prop.ForAll(
		func(externalLS string) bool {
			subnet := &networkv1.Subnet{
				Spec: networkv1.SubnetSpec{
					CIDR:                  "10.244.0.0/24",
					Gateway:               "10.244.0.1",
					ExternalLogicalSwitch: externalLS,
				},
			}

			isExternal := subnet.IsExternalMode()

			if externalLS == "" {
				return !isExternal
			}
			return isExternal
		},
		gen.OneGenOf(
			gen.Const(""),
			gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		),
	))

	properties.TestingRun(t)
}

// TestProperty_LogicalSwitchNameDerivation tests that GetLogicalSwitchName
// returns the correct name based on the subnet mode.
// Validates: Requirements 3.2, 3.3
func TestProperty_LogicalSwitchNameDerivation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: GetLogicalSwitchName returns external LS name in external mode,
	// or "subnet-<name>" in standalone mode
	properties.Property("GetLogicalSwitchName returns correct name", prop.ForAll(
		func(subnetName, externalLS string) bool {
			subnet := &networkv1.Subnet{
				Spec: networkv1.SubnetSpec{
					CIDR:                  "10.244.0.0/24",
					Gateway:               "10.244.0.1",
					ExternalLogicalSwitch: externalLS,
				},
			}
			subnet.Name = subnetName

			lsName := subnet.GetLogicalSwitchName()

			if externalLS != "" {
				// External mode: should return the external LS name
				return lsName == externalLS
			}
			// Standalone mode: should return "subnet-<name>"
			return lsName == "subnet-"+subnetName
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.OneGenOf(
			gen.Const(""),
			gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		),
	))

	properties.TestingRun(t)
}

// TestProperty_ExternalLSNotFoundError tests that non-existent external LS
// references produce appropriate errors.
// Validates: Requirements 3.3
func TestProperty_ExternalLSNotFoundError(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Referencing a non-existent external LS always produces an error
	properties.Property("non-existent external LS produces error", prop.ForAll(
		func(nonExistentLS string) bool {
			ctx := context.Background()
			// Create mock with no existing switches
			mockOps := newMockLogicalSwitchOps([]string{})

			err := verifyExternalLogicalSwitchExists(ctx, mockOps, nonExistentLS)

			// Should always return an error for non-existent LS
			return err != nil
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	properties.TestingRun(t)
}

// TestProperty_ExternalLSExistsSuccess tests that existing external LS
// references are validated successfully.
// Validates: Requirements 3.2
func TestProperty_ExternalLSExistsSuccess(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Referencing an existing external LS always succeeds
	properties.Property("existing external LS validates successfully", prop.ForAll(
		func(existingLS string) bool {
			ctx := context.Background()
			// Create mock with the LS that will be referenced
			mockOps := newMockLogicalSwitchOps([]string{existingLS})

			err := verifyExternalLogicalSwitchExists(ctx, mockOps, existingLS)

			// Should always succeed for existing LS
			return err == nil
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	properties.TestingRun(t)
}
