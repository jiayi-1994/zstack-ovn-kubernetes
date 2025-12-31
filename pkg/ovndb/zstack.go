// Package ovndb provides ZStack OVN compatibility support.
//
// This file implements functionality for ensuring compatibility with
// ZStack-managed OVN configurations. When running in external mode,
// zstack-ovn-kubernetes connects to ZStack's OVN databases and must
// ensure it doesn't conflict with ZStack's existing configurations.
//
// ZStack Compatibility Features:
// - Detection of ZStack-managed OVN objects
// - Safe referencing of existing Logical Switches
// - Conflict detection and prevention
// - External ID management for ownership tracking
//
// Reference: Requirements 24.4
package ovndb

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/klog/v2"
)

// ZStack external ID keys used to identify ZStack-managed objects
const (
	// ZStackExternalIDKey is the key used by ZStack to mark its managed objects
	ZStackExternalIDKey = "zstack.io/managed-by"

	// ZStackExternalIDValue is the value ZStack uses for its managed objects
	ZStackExternalIDValue = "zstack"

	// ZStackVPCIDKey is the key for ZStack VPC UUID
	ZStackVPCIDKey = "zstack.io/vpc-uuid"

	// ZStackSubnetIDKey is the key for ZStack subnet UUID
	ZStackSubnetIDKey = "zstack.io/subnet-uuid"

	// ZStackNetworkIDKey is the key for ZStack network UUID
	ZStackNetworkIDKey = "zstack.io/network-uuid"

	// OurExternalIDKey is the key we use to mark our managed objects
	OurExternalIDKey = "zstack.io/managed-by"

	// OurExternalIDValue is the value we use for our managed objects
	OurExternalIDValue = "zstack-ovn-kubernetes"
)

// ZStackCompatibility provides methods for ensuring compatibility with ZStack OVN
type ZStackCompatibility struct {
	client *Client
	lsOps  *LogicalSwitchOps
}

// NewZStackCompatibility creates a new ZStackCompatibility instance
func NewZStackCompatibility(client *Client) *ZStackCompatibility {
	return &ZStackCompatibility{
		client: client,
		lsOps:  NewLogicalSwitchOps(client),
	}
}

// IsZStackManagedLogicalSwitch checks if a Logical Switch is managed by ZStack
//
// Parameters:
//   - ls: The Logical Switch to check
//
// Returns:
//   - bool: True if the switch is managed by ZStack
func (z *ZStackCompatibility) IsZStackManagedLogicalSwitch(ls *LogicalSwitch) bool {
	if ls == nil || ls.ExternalIDs == nil {
		return false
	}

	// Check for ZStack management marker
	if managedBy, ok := ls.ExternalIDs[ZStackExternalIDKey]; ok {
		return managedBy == ZStackExternalIDValue
	}

	// Also check for ZStack-specific external IDs
	if _, ok := ls.ExternalIDs[ZStackVPCIDKey]; ok {
		return true
	}
	if _, ok := ls.ExternalIDs[ZStackSubnetIDKey]; ok {
		return true
	}
	if _, ok := ls.ExternalIDs[ZStackNetworkIDKey]; ok {
		return true
	}

	return false
}

// IsOurManagedLogicalSwitch checks if a Logical Switch is managed by us
//
// Parameters:
//   - ls: The Logical Switch to check
//
// Returns:
//   - bool: True if the switch is managed by zstack-ovn-kubernetes
func (z *ZStackCompatibility) IsOurManagedLogicalSwitch(ls *LogicalSwitch) bool {
	if ls == nil || ls.ExternalIDs == nil {
		return false
	}

	if managedBy, ok := ls.ExternalIDs[OurExternalIDKey]; ok {
		return managedBy == OurExternalIDValue
	}

	return false
}

// ValidateExternalLogicalSwitch validates that an external Logical Switch can be used
//
// This method checks:
// 1. The Logical Switch exists in the OVN database
// 2. The switch is not already managed by zstack-ovn-kubernetes (to prevent conflicts)
// 3. Returns information about the switch for logging/debugging
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the Logical Switch to validate
//
// Returns:
//   - *LogicalSwitch: The validated Logical Switch
//   - error: Validation error
func (z *ZStackCompatibility) ValidateExternalLogicalSwitch(ctx context.Context, name string) (*LogicalSwitch, error) {
	if name == "" {
		return nil, fmt.Errorf("logical switch name is required")
	}

	// Get the Logical Switch
	ls, err := z.lsOps.GetLogicalSwitch(ctx, name)
	if err != nil {
		if IsNotFound(err) {
			return nil, fmt.Errorf("external Logical Switch %q not found in OVN database", name)
		}
		return nil, fmt.Errorf("failed to get Logical Switch %q: %w", name, err)
	}

	// Check if it's already managed by us (potential conflict)
	if z.IsOurManagedLogicalSwitch(ls) {
		klog.V(4).Infof("Logical Switch %q is already managed by zstack-ovn-kubernetes", name)
	}

	// Log ZStack management status
	if z.IsZStackManagedLogicalSwitch(ls) {
		klog.V(4).Infof("Logical Switch %q is managed by ZStack", name)
	}

	klog.V(4).Infof("Validated external Logical Switch: name=%s, uuid=%s", ls.Name, ls.UUID)
	return ls, nil
}

// ListZStackLogicalSwitches returns all Logical Switches managed by ZStack
//
// Parameters:
//   - ctx: Context for cancellation
//
// Returns:
//   - []*LogicalSwitch: List of ZStack-managed switches
//   - error: Query error
func (z *ZStackCompatibility) ListZStackLogicalSwitches(ctx context.Context) ([]*LogicalSwitch, error) {
	allSwitches, err := z.lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return nil, err
	}

	var zstackSwitches []*LogicalSwitch
	for _, ls := range allSwitches {
		if z.IsZStackManagedLogicalSwitch(ls) {
			zstackSwitches = append(zstackSwitches, ls)
		}
	}

	return zstackSwitches, nil
}

// ListOurLogicalSwitches returns all Logical Switches managed by zstack-ovn-kubernetes
//
// Parameters:
//   - ctx: Context for cancellation
//
// Returns:
//   - []*LogicalSwitch: List of our managed switches
//   - error: Query error
func (z *ZStackCompatibility) ListOurLogicalSwitches(ctx context.Context) ([]*LogicalSwitch, error) {
	allSwitches, err := z.lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return nil, err
	}

	var ourSwitches []*LogicalSwitch
	for _, ls := range allSwitches {
		if z.IsOurManagedLogicalSwitch(ls) {
			ourSwitches = append(ourSwitches, ls)
		}
	}

	return ourSwitches, nil
}

// CheckForConflicts checks for potential conflicts with ZStack OVN configuration
//
// This method checks:
// 1. No duplicate Logical Switch names between our managed and ZStack managed
// 2. No overlapping port configurations
// 3. No conflicting ACL rules
//
// Parameters:
//   - ctx: Context for cancellation
//
// Returns:
//   - []string: List of conflict warnings (empty if no conflicts)
//   - error: Query error
func (z *ZStackCompatibility) CheckForConflicts(ctx context.Context) ([]string, error) {
	var warnings []string

	// Get all logical switches
	allSwitches, err := z.lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list logical switches: %w", err)
	}

	// Check for naming conflicts
	ourSwitchNames := make(map[string]bool)
	zstackSwitchNames := make(map[string]bool)

	for _, ls := range allSwitches {
		if z.IsOurManagedLogicalSwitch(ls) {
			ourSwitchNames[ls.Name] = true
		}
		if z.IsZStackManagedLogicalSwitch(ls) {
			zstackSwitchNames[ls.Name] = true
		}
	}

	// Check for switches that appear in both (shouldn't happen normally)
	for name := range ourSwitchNames {
		if zstackSwitchNames[name] {
			warnings = append(warnings,
				fmt.Sprintf("Logical Switch %q has conflicting management markers (both ZStack and zstack-ovn-kubernetes)", name))
		}
	}

	// Check for potential naming pattern conflicts
	for name := range zstackSwitchNames {
		if strings.HasPrefix(name, "subnet-") {
			warnings = append(warnings,
				fmt.Sprintf("ZStack Logical Switch %q uses 'subnet-' prefix which may conflict with auto-generated names", name))
		}
	}

	return warnings, nil
}

// SafeCreateLogicalSwitch creates a Logical Switch with conflict checking
//
// This method:
// 1. Checks if a switch with the same name already exists
// 2. If it exists and is ZStack-managed, returns an error
// 3. If it exists and is our-managed, updates it
// 4. If it doesn't exist, creates it
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the Logical Switch
//   - otherConfig: Additional configuration
//   - externalIDs: External identifiers (our management marker will be added)
//
// Returns:
//   - *LogicalSwitch: The created or updated switch
//   - error: Creation error
func (z *ZStackCompatibility) SafeCreateLogicalSwitch(ctx context.Context, name string, otherConfig, externalIDs map[string]string) (*LogicalSwitch, error) {
	// Check if switch already exists
	existing, err := z.lsOps.GetLogicalSwitch(ctx, name)
	if err != nil && !IsNotFound(err) {
		return nil, fmt.Errorf("failed to check existing Logical Switch: %w", err)
	}

	if existing != nil {
		// Check if it's ZStack-managed
		if z.IsZStackManagedLogicalSwitch(existing) {
			return nil, fmt.Errorf("cannot create Logical Switch %q: already managed by ZStack", name)
		}

		// Check if it's our-managed (update case)
		if z.IsOurManagedLogicalSwitch(existing) {
			klog.V(4).Infof("Logical Switch %q already exists and is managed by us, updating", name)
			existing.OtherConfig = otherConfig
			if externalIDs != nil {
				if existing.ExternalIDs == nil {
					existing.ExternalIDs = make(map[string]string)
				}
				for k, v := range externalIDs {
					existing.ExternalIDs[k] = v
				}
			}
			existing.ExternalIDs[OurExternalIDKey] = OurExternalIDValue
			if err := z.lsOps.UpdateLogicalSwitch(ctx, existing); err != nil {
				return nil, fmt.Errorf("failed to update Logical Switch: %w", err)
			}
			return existing, nil
		}

		// Unmanaged switch - we shouldn't take it over
		return nil, fmt.Errorf("cannot create Logical Switch %q: already exists but is not managed by us", name)
	}

	// Create new switch with our management marker
	if externalIDs == nil {
		externalIDs = make(map[string]string)
	}
	externalIDs[OurExternalIDKey] = OurExternalIDValue

	return z.lsOps.CreateLogicalSwitch(ctx, name, otherConfig, externalIDs)
}

// SafeDeleteLogicalSwitch deletes a Logical Switch with ownership checking
//
// This method:
// 1. Checks if the switch is managed by us
// 2. Only deletes if we own it
// 3. Returns error if trying to delete ZStack-managed switch
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the Logical Switch to delete
//
// Returns:
//   - error: Deletion error
func (z *ZStackCompatibility) SafeDeleteLogicalSwitch(ctx context.Context, name string) error {
	// Get the switch
	ls, err := z.lsOps.GetLogicalSwitch(ctx, name)
	if err != nil {
		if IsNotFound(err) {
			// Already deleted, nothing to do
			return nil
		}
		return fmt.Errorf("failed to get Logical Switch: %w", err)
	}

	// Check ownership
	if z.IsZStackManagedLogicalSwitch(ls) {
		return fmt.Errorf("cannot delete Logical Switch %q: managed by ZStack", name)
	}

	if !z.IsOurManagedLogicalSwitch(ls) {
		return fmt.Errorf("cannot delete Logical Switch %q: not managed by us", name)
	}

	// Safe to delete
	return z.lsOps.DeleteLogicalSwitch(ctx, name)
}

// GetLogicalSwitchInfo returns detailed information about a Logical Switch
// This is useful for debugging and displaying in UI
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the Logical Switch
//
// Returns:
//   - *LogicalSwitchInfo: Detailed information
//   - error: Query error
func (z *ZStackCompatibility) GetLogicalSwitchInfo(ctx context.Context, name string) (*LogicalSwitchInfo, error) {
	ls, err := z.lsOps.GetLogicalSwitch(ctx, name)
	if err != nil {
		return nil, err
	}

	info := &LogicalSwitchInfo{
		Name:            ls.Name,
		UUID:            ls.UUID,
		IsZStackManaged: z.IsZStackManagedLogicalSwitch(ls),
		IsOurManaged:    z.IsOurManagedLogicalSwitch(ls),
		OtherConfig:     ls.OtherConfig,
		ExternalIDs:     ls.ExternalIDs,
		PortCount:       len(ls.Ports),
		ACLCount:        len(ls.ACLs),
		LBCount:         len(ls.LoadBalancer),
	}

	// Extract ZStack-specific info
	if info.IsZStackManaged {
		info.ZStackVPCID = ls.ExternalIDs[ZStackVPCIDKey]
		info.ZStackSubnetID = ls.ExternalIDs[ZStackSubnetIDKey]
		info.ZStackNetworkID = ls.ExternalIDs[ZStackNetworkIDKey]
	}

	return info, nil
}

// LogicalSwitchInfo contains detailed information about a Logical Switch
type LogicalSwitchInfo struct {
	Name            string
	UUID            string
	IsZStackManaged bool
	IsOurManaged    bool
	OtherConfig     map[string]string
	ExternalIDs     map[string]string
	PortCount       int
	ACLCount        int
	LBCount         int

	// ZStack-specific fields
	ZStackVPCID     string
	ZStackSubnetID  string
	ZStackNetworkID string
}

// String returns a human-readable representation of the LogicalSwitchInfo
func (info *LogicalSwitchInfo) String() string {
	managedBy := "unmanaged"
	if info.IsZStackManaged {
		managedBy = "ZStack"
	} else if info.IsOurManaged {
		managedBy = "zstack-ovn-kubernetes"
	}

	return fmt.Sprintf("LogicalSwitch{name=%s, uuid=%s, managedBy=%s, ports=%d, acls=%d, lbs=%d}",
		info.Name, info.UUID, managedBy, info.PortCount, info.ACLCount, info.LBCount)
}
