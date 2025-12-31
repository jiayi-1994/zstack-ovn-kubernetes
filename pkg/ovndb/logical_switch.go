// Package ovndb provides Logical Switch operations.
//
// This file implements CRUD operations for OVN Logical Switches.
// A Logical Switch is a virtual L2 network that connects multiple ports.
//
// In Kubernetes context:
// - Each subnet typically maps to one Logical Switch
// - Pods in the same subnet share the same Logical Switch
// - The Logical Switch name usually follows the pattern: subnet-<name>
//
// Key OVN Logical Switch fields:
// - name: Unique identifier for the switch
// - ports: List of Logical Switch Port UUIDs
// - acls: List of ACL UUIDs for network policies
// - load_balancer: List of Load Balancer UUIDs for services
// - other_config: Additional configuration (subnet, exclude_ips, etc.)
// - external_ids: External identifiers for integration
//
// Reference: OVN-Kubernetes pkg/libovsdb/ops/switch.go
package ovndb

import (
	"context"
	"fmt"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"
)

// LogicalSwitchOps provides operations on OVN Logical Switches
type LogicalSwitchOps struct {
	client *Client
}

// NewLogicalSwitchOps creates a new LogicalSwitchOps
func NewLogicalSwitchOps(c *Client) *LogicalSwitchOps {
	return &LogicalSwitchOps{client: c}
}

// CreateLogicalSwitch creates a new Logical Switch
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Unique name for the switch
//   - otherConfig: Additional configuration (e.g., subnet CIDR, exclude_ips)
//   - externalIDs: External identifiers for integration
//
// Returns:
//   - *LogicalSwitch: The created switch with UUID populated
//   - error: Creation error
//
// Example:
//
//	ls, err := ops.CreateLogicalSwitch(ctx, "subnet-default",
//	    map[string]string{"subnet": "10.244.0.0/24", "exclude_ips": "10.244.0.1"},
//	    map[string]string{"k8s.io/subnet": "default"})
func (o *LogicalSwitchOps) CreateLogicalSwitch(ctx context.Context, name string, otherConfig, externalIDs map[string]string) (*LogicalSwitch, error) {
	if name == "" {
		return nil, NewValidationError("name", name, "name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	ls := &LogicalSwitch{
		UUID:        BuildNamedUUID(name),
		Name:        name,
		OtherConfig: otherConfig,
		ExternalIDs: externalIDs,
	}

	ops, err := nbClient.Create(ls)
	if err != nil {
		return nil, NewTransactionError("CreateLogicalSwitch", err, name)
	}

	results, err := TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	if err != nil {
		return nil, err
	}

	// Set the real UUID from the result
	if len(results) > 0 {
		ls.UUID = GetUUIDFromResult(results[0])
	}

	return ls, nil
}

// GetLogicalSwitch retrieves a Logical Switch by name
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the switch to retrieve
//
// Returns:
//   - *LogicalSwitch: The found switch
//   - error: ObjectNotFoundError if not found, or other error
func (o *LogicalSwitchOps) GetLogicalSwitch(ctx context.Context, name string) (*LogicalSwitch, error) {
	if name == "" {
		return nil, NewValidationError("name", name, "name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	ls := &LogicalSwitch{Name: name}
	err := nbClient.Get(ctx, ls)
	if err != nil {
		if err == client.ErrNotFound {
			return nil, NewObjectNotFoundError("LogicalSwitch", name)
		}
		return nil, NewTransactionError("GetLogicalSwitch", err, name)
	}

	return ls, nil
}

// GetLogicalSwitchByUUID retrieves a Logical Switch by UUID
//
// Parameters:
//   - ctx: Context for cancellation
//   - uuid: UUID of the switch to retrieve
//
// Returns:
//   - *LogicalSwitch: The found switch
//   - error: ObjectNotFoundError if not found, or other error
func (o *LogicalSwitchOps) GetLogicalSwitchByUUID(ctx context.Context, uuid string) (*LogicalSwitch, error) {
	if uuid == "" {
		return nil, NewValidationError("uuid", uuid, "uuid is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	ls := &LogicalSwitch{UUID: uuid}
	err := nbClient.Get(ctx, ls)
	if err != nil {
		if err == client.ErrNotFound {
			return nil, NewObjectNotFoundError("LogicalSwitch", uuid)
		}
		return nil, NewTransactionError("GetLogicalSwitchByUUID", err, uuid)
	}

	return ls, nil
}

// ListLogicalSwitches lists all Logical Switches
//
// Parameters:
//   - ctx: Context for cancellation
//
// Returns:
//   - []*LogicalSwitch: List of all switches
//   - error: Query error
func (o *LogicalSwitchOps) ListLogicalSwitches(ctx context.Context) ([]*LogicalSwitch, error) {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	var switches []*LogicalSwitch
	err := nbClient.List(ctx, &switches)
	if err != nil {
		return nil, NewTransactionError("ListLogicalSwitches", err, "")
	}

	return switches, nil
}

// ListLogicalSwitchesWithPredicate lists Logical Switches matching a predicate
//
// Parameters:
//   - ctx: Context for cancellation
//   - predicate: Function to filter switches
//
// Returns:
//   - []*LogicalSwitch: List of matching switches
//   - error: Query error
//
// Example:
//
//	switches, err := ops.ListLogicalSwitchesWithPredicate(ctx, func(ls *LogicalSwitch) bool {
//	    return ls.ExternalIDs["k8s.io/subnet"] == "default"
//	})
func (o *LogicalSwitchOps) ListLogicalSwitchesWithPredicate(ctx context.Context, predicate func(*LogicalSwitch) bool) ([]*LogicalSwitch, error) {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	var switches []*LogicalSwitch
	err := nbClient.WhereCache(func(ls *LogicalSwitch) bool {
		return predicate(ls)
	}).List(ctx, &switches)
	if err != nil {
		return nil, NewTransactionError("ListLogicalSwitchesWithPredicate", err, "")
	}

	return switches, nil
}

// UpdateLogicalSwitch updates a Logical Switch
//
// Parameters:
//   - ctx: Context for cancellation
//   - ls: Logical Switch with updated fields
//   - fields: Fields to update (if empty, updates all non-zero fields)
//
// Returns:
//   - error: Update error
func (o *LogicalSwitchOps) UpdateLogicalSwitch(ctx context.Context, ls *LogicalSwitch, fields ...interface{}) error {
	if ls == nil || ls.Name == "" {
		return NewValidationError("ls", ls, "logical switch with name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	// If no specific fields provided, update all mutable fields
	if len(fields) == 0 {
		fields = getLogicalSwitchMutableFields(ls)
	}

	ops, err := nbClient.Where(ls).Update(ls, fields...)
	if err != nil {
		return NewTransactionError("UpdateLogicalSwitch", err, ls.Name)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// DeleteLogicalSwitch deletes a Logical Switch by name
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the switch to delete
//
// Returns:
//   - error: Deletion error (nil if switch doesn't exist)
func (o *LogicalSwitchOps) DeleteLogicalSwitch(ctx context.Context, name string) error {
	if name == "" {
		return NewValidationError("name", name, "name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	ls := &LogicalSwitch{Name: name}
	ops, err := nbClient.Where(ls).Delete()
	if err != nil {
		return NewTransactionError("DeleteLogicalSwitch", err, name)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// DeleteLogicalSwitchOps returns operations to delete a Logical Switch
// This is useful for building composite transactions
//
// Parameters:
//   - name: Name of the switch to delete
//
// Returns:
//   - []ovsdb.Operation: Delete operations
//   - error: Operation building error
func (o *LogicalSwitchOps) DeleteLogicalSwitchOps(name string) ([]ovsdb.Operation, error) {
	if name == "" {
		return nil, NewValidationError("name", name, "name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	ls := &LogicalSwitch{Name: name}
	return nbClient.Where(ls).Delete()
}

// CreateOrUpdateLogicalSwitch creates or updates a Logical Switch
//
// If the switch exists, it updates the specified fields.
// If the switch doesn't exist, it creates a new one.
//
// Parameters:
//   - ctx: Context for cancellation
//   - ls: Logical Switch to create or update
//
// Returns:
//   - error: Operation error
func (o *LogicalSwitchOps) CreateOrUpdateLogicalSwitch(ctx context.Context, ls *LogicalSwitch) error {
	if ls == nil || ls.Name == "" {
		return NewValidationError("ls", ls, "logical switch with name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	// Check if switch exists
	existing, err := o.GetLogicalSwitch(ctx, ls.Name)
	if err != nil && !IsNotFound(err) {
		return err
	}

	if existing != nil {
		// Update existing switch
		ls.UUID = existing.UUID
		return o.UpdateLogicalSwitch(ctx, ls)
	}

	// Create new switch
	_, err = o.CreateLogicalSwitch(ctx, ls.Name, ls.OtherConfig, ls.ExternalIDs)
	return err
}

// SetOtherConfig sets other_config values on a Logical Switch
// Empty values will delete the corresponding keys
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the switch
//   - config: Configuration to set (empty values delete keys)
//
// Returns:
//   - error: Update error
func (o *LogicalSwitchOps) SetOtherConfig(ctx context.Context, name string, config map[string]string) error {
	ls, err := o.GetLogicalSwitch(ctx, name)
	if err != nil {
		return err
	}

	if ls.OtherConfig == nil {
		ls.OtherConfig = make(map[string]string)
	}

	for k, v := range config {
		if v == "" {
			delete(ls.OtherConfig, k)
		} else {
			ls.OtherConfig[k] = v
		}
	}

	return o.UpdateLogicalSwitch(ctx, ls, &ls.OtherConfig)
}

// SetExternalIDs sets external_ids values on a Logical Switch
// Empty values will delete the corresponding keys
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the switch
//   - ids: External IDs to set (empty values delete keys)
//
// Returns:
//   - error: Update error
func (o *LogicalSwitchOps) SetExternalIDs(ctx context.Context, name string, ids map[string]string) error {
	ls, err := o.GetLogicalSwitch(ctx, name)
	if err != nil {
		return err
	}

	if ls.ExternalIDs == nil {
		ls.ExternalIDs = make(map[string]string)
	}

	for k, v := range ids {
		if v == "" {
			delete(ls.ExternalIDs, k)
		} else {
			ls.ExternalIDs[k] = v
		}
	}

	return o.UpdateLogicalSwitch(ctx, ls, &ls.ExternalIDs)
}

// AddACLsToLogicalSwitch adds ACLs to a Logical Switch
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the switch
//   - aclUUIDs: UUIDs of ACLs to add
//
// Returns:
//   - error: Update error
func (o *LogicalSwitchOps) AddACLsToLogicalSwitch(ctx context.Context, name string, aclUUIDs ...string) error {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	ls := &LogicalSwitch{Name: name, ACLs: aclUUIDs}
	ops, err := nbClient.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.ACLs,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   aclUUIDs,
	})
	if err != nil {
		return NewTransactionError("AddACLsToLogicalSwitch", err, name)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// RemoveACLsFromLogicalSwitch removes ACLs from a Logical Switch
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the switch
//   - aclUUIDs: UUIDs of ACLs to remove
//
// Returns:
//   - error: Update error
func (o *LogicalSwitchOps) RemoveACLsFromLogicalSwitch(ctx context.Context, name string, aclUUIDs ...string) error {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	ls := &LogicalSwitch{Name: name}
	ops, err := nbClient.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.ACLs,
		Mutator: ovsdb.MutateOperationDelete,
		Value:   aclUUIDs,
	})
	if err != nil {
		return NewTransactionError("RemoveACLsFromLogicalSwitch", err, name)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// AddLoadBalancersToLogicalSwitch adds Load Balancers to a Logical Switch
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the switch
//   - lbUUIDs: UUIDs of Load Balancers to add
//
// Returns:
//   - error: Update error
func (o *LogicalSwitchOps) AddLoadBalancersToLogicalSwitch(ctx context.Context, name string, lbUUIDs ...string) error {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	ls := &LogicalSwitch{Name: name}
	ops, err := nbClient.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.LoadBalancer,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   lbUUIDs,
	})
	if err != nil {
		return NewTransactionError("AddLoadBalancersToLogicalSwitch", err, name)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// RemoveLoadBalancersFromLogicalSwitch removes Load Balancers from a Logical Switch
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the switch
//   - lbUUIDs: UUIDs of Load Balancers to remove
//
// Returns:
//   - error: Update error
func (o *LogicalSwitchOps) RemoveLoadBalancersFromLogicalSwitch(ctx context.Context, name string, lbUUIDs ...string) error {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	ls := &LogicalSwitch{Name: name}
	ops, err := nbClient.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.LoadBalancer,
		Mutator: ovsdb.MutateOperationDelete,
		Value:   lbUUIDs,
	})
	if err != nil {
		return NewTransactionError("RemoveLoadBalancersFromLogicalSwitch", err, name)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// getLogicalSwitchMutableFields returns the mutable fields of a LogicalSwitch
func getLogicalSwitchMutableFields(ls *LogicalSwitch) []interface{} {
	fields := []interface{}{}
	if ls.OtherConfig != nil {
		fields = append(fields, &ls.OtherConfig)
	}
	if ls.ExternalIDs != nil {
		fields = append(fields, &ls.ExternalIDs)
	}
	return fields
}
