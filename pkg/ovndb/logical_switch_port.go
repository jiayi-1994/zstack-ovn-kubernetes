// Package ovndb provides Logical Switch Port operations.
//
// This file implements CRUD operations for OVN Logical Switch Ports.
// A Logical Switch Port is a virtual network interface attached to a Logical Switch.
//
// In Kubernetes context:
// - Each Pod has one or more Logical Switch Ports
// - Port name format: <namespace>_<pod-name> (e.g., "default_nginx-pod")
// - Addresses contain MAC and IP (e.g., "0a:58:0a:f4:00:05 10.244.0.5")
//
// Key OVN Logical Switch Port fields:
// - name: Unique identifier (namespace_podName)
// - addresses: MAC and IP addresses
// - type: Port type ("" for normal, "router", "localnet", etc.)
// - options: Port-specific options (requested-chassis, etc.)
// - port_security: Security rules to prevent spoofing
// - external_ids: External identifiers (namespace, pod, etc.)
//
// Port Types:
// - "" (empty): Normal port for VMs/Pods
// - "router": Port connected to a Logical Router
// - "localnet": Port connected to physical network (for Underlay)
// - "patch": Port for connecting switches
//
// Reference: OVN-Kubernetes pkg/libovsdb/ops/switch.go
package ovndb

import (
	"context"
	"fmt"
	"strings"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"
)

// Port type constants
const (
	// PortTypeNormal is a normal port for VMs/Pods
	PortTypeNormal = ""

	// PortTypeRouter is a port connected to a Logical Router
	PortTypeRouter = "router"

	// PortTypeLocalnet is a port connected to physical network
	PortTypeLocalnet = "localnet"

	// PortTypePatch is a port for connecting switches
	PortTypePatch = "patch"

	// PortTypeVirtual is a virtual port for floating IPs
	PortTypeVirtual = "virtual"
)

// External ID keys for Logical Switch Ports
const (
	// ExternalIDNamespace is the key for Pod namespace
	ExternalIDNamespace = "namespace"

	// ExternalIDPod is the key for Pod name
	ExternalIDPod = "pod"

	// ExternalIDPodUID is the key for Pod UID
	ExternalIDPodUID = "pod-uid"

	// ExternalIDOwner is the key for owner reference
	ExternalIDOwner = "owner"
)

// Option keys for Logical Switch Ports
const (
	// OptionRequestedChassis specifies the chassis to bind the port
	OptionRequestedChassis = "requested-chassis"

	// OptionIfaceIDVer is the interface ID version (Pod UID)
	OptionIfaceIDVer = "iface-id-ver"

	// OptionNetworkName is the network name for localnet ports
	OptionNetworkName = "network_name"
)

// LogicalSwitchPortOps provides operations on OVN Logical Switch Ports
type LogicalSwitchPortOps struct {
	client *Client
}

// NewLogicalSwitchPortOps creates a new LogicalSwitchPortOps
func NewLogicalSwitchPortOps(c *Client) *LogicalSwitchPortOps {
	return &LogicalSwitchPortOps{client: c}
}

// CreateLogicalSwitchPort creates a new Logical Switch Port and adds it to a switch
//
// Parameters:
//   - ctx: Context for cancellation
//   - switchName: Name of the Logical Switch to add the port to
//   - portName: Unique name for the port (format: namespace_podName)
//   - mac: MAC address (format: "0a:58:0a:f4:00:05")
//   - ips: IP addresses (format: ["10.244.0.5"])
//   - externalIDs: External identifiers (namespace, pod, etc.)
//
// Returns:
//   - *LogicalSwitchPort: The created port with UUID populated
//   - error: Creation error
//
// Example:
//
//	lsp, err := ops.CreateLogicalSwitchPort(ctx, "subnet-default", "default_nginx",
//	    "0a:58:0a:f4:00:05", []string{"10.244.0.5"},
//	    map[string]string{"namespace": "default", "pod": "nginx"})
func (o *LogicalSwitchPortOps) CreateLogicalSwitchPort(
	ctx context.Context,
	switchName, portName, mac string,
	ips []string,
	externalIDs map[string]string,
) (*LogicalSwitchPort, error) {
	if switchName == "" {
		return nil, NewValidationError("switchName", switchName, "switch name is required")
	}
	if portName == "" {
		return nil, NewValidationError("portName", portName, "port name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	// Build addresses: "MAC IP1 IP2 ..."
	addresses := buildAddresses(mac, ips)

	// Build port security: "MAC IP1 IP2 ..."
	portSecurity := []string{}
	if mac != "" && len(ips) > 0 {
		portSecurity = []string{addresses}
	}

	enabled := true
	lsp := &LogicalSwitchPort{
		UUID:         BuildNamedUUID(portName),
		Name:         portName,
		Addresses:    []string{addresses},
		PortSecurity: portSecurity,
		ExternalIDs:  externalIDs,
		Enabled:      &enabled,
	}

	// Create the port
	createOps, err := nbClient.Create(lsp)
	if err != nil {
		return nil, NewTransactionError("CreateLogicalSwitchPort", err, portName)
	}

	// Add port to switch using mutation
	ls := &LogicalSwitch{Name: switchName}
	mutateOps, err := nbClient.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{lsp.UUID},
	})
	if err != nil {
		return nil, NewTransactionError("CreateLogicalSwitchPort", err, portName)
	}

	// Execute both operations atomically
	ops := append(createOps, mutateOps...)
	results, err := TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	if err != nil {
		return nil, err
	}

	// Set the real UUID from the result
	if len(results) > 0 {
		lsp.UUID = GetUUIDFromResult(results[0])
	}

	return lsp, nil
}

// CreateLogicalSwitchPortWithOptions creates a port with additional options
//
// Parameters:
//   - ctx: Context for cancellation
//   - switchName: Name of the Logical Switch
//   - portName: Unique name for the port
//   - mac: MAC address
//   - ips: IP addresses
//   - portType: Port type (PortTypeNormal, PortTypeRouter, etc.)
//   - options: Port options (requested-chassis, etc.)
//   - externalIDs: External identifiers
//
// Returns:
//   - *LogicalSwitchPort: The created port
//   - error: Creation error
func (o *LogicalSwitchPortOps) CreateLogicalSwitchPortWithOptions(
	ctx context.Context,
	switchName, portName, mac string,
	ips []string,
	portType string,
	options, externalIDs map[string]string,
) (*LogicalSwitchPort, error) {
	if switchName == "" {
		return nil, NewValidationError("switchName", switchName, "switch name is required")
	}
	if portName == "" {
		return nil, NewValidationError("portName", portName, "port name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	// Build addresses
	addresses := buildAddresses(mac, ips)

	// Build port security (only for normal ports)
	portSecurity := []string{}
	if portType == PortTypeNormal && mac != "" && len(ips) > 0 {
		portSecurity = []string{addresses}
	}

	enabled := true
	lsp := &LogicalSwitchPort{
		UUID:         BuildNamedUUID(portName),
		Name:         portName,
		Type:         portType,
		Addresses:    []string{addresses},
		PortSecurity: portSecurity,
		Options:      options,
		ExternalIDs:  externalIDs,
		Enabled:      &enabled,
	}

	// Create the port
	createOps, err := nbClient.Create(lsp)
	if err != nil {
		return nil, NewTransactionError("CreateLogicalSwitchPortWithOptions", err, portName)
	}

	// Add port to switch
	ls := &LogicalSwitch{Name: switchName}
	mutateOps, err := nbClient.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{lsp.UUID},
	})
	if err != nil {
		return nil, NewTransactionError("CreateLogicalSwitchPortWithOptions", err, portName)
	}

	ops := append(createOps, mutateOps...)
	results, err := TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	if err != nil {
		return nil, err
	}

	if len(results) > 0 {
		lsp.UUID = GetUUIDFromResult(results[0])
	}

	return lsp, nil
}

// GetLogicalSwitchPort retrieves a Logical Switch Port by name
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the port to retrieve
//
// Returns:
//   - *LogicalSwitchPort: The found port
//   - error: ObjectNotFoundError if not found, or other error
func (o *LogicalSwitchPortOps) GetLogicalSwitchPort(ctx context.Context, name string) (*LogicalSwitchPort, error) {
	if name == "" {
		return nil, NewValidationError("name", name, "name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	lsp := &LogicalSwitchPort{Name: name}
	err := nbClient.Get(ctx, lsp)
	if err != nil {
		if err == client.ErrNotFound {
			return nil, NewObjectNotFoundError("LogicalSwitchPort", name)
		}
		return nil, NewTransactionError("GetLogicalSwitchPort", err, name)
	}

	return lsp, nil
}

// GetLogicalSwitchPortByUUID retrieves a Logical Switch Port by UUID
func (o *LogicalSwitchPortOps) GetLogicalSwitchPortByUUID(ctx context.Context, uuid string) (*LogicalSwitchPort, error) {
	if uuid == "" {
		return nil, NewValidationError("uuid", uuid, "uuid is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	lsp := &LogicalSwitchPort{UUID: uuid}
	err := nbClient.Get(ctx, lsp)
	if err != nil {
		if err == client.ErrNotFound {
			return nil, NewObjectNotFoundError("LogicalSwitchPort", uuid)
		}
		return nil, NewTransactionError("GetLogicalSwitchPortByUUID", err, uuid)
	}

	return lsp, nil
}

// ListLogicalSwitchPorts lists all Logical Switch Ports
func (o *LogicalSwitchPortOps) ListLogicalSwitchPorts(ctx context.Context) ([]*LogicalSwitchPort, error) {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	var ports []*LogicalSwitchPort
	err := nbClient.List(ctx, &ports)
	if err != nil {
		return nil, NewTransactionError("ListLogicalSwitchPorts", err, "")
	}

	return ports, nil
}

// ListLogicalSwitchPortsWithPredicate lists ports matching a predicate
//
// Example:
//
//	ports, err := ops.ListLogicalSwitchPortsWithPredicate(ctx, func(lsp *LogicalSwitchPort) bool {
//	    return lsp.ExternalIDs["namespace"] == "default"
//	})
func (o *LogicalSwitchPortOps) ListLogicalSwitchPortsWithPredicate(ctx context.Context, predicate func(*LogicalSwitchPort) bool) ([]*LogicalSwitchPort, error) {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	var ports []*LogicalSwitchPort
	err := nbClient.WhereCache(func(lsp *LogicalSwitchPort) bool {
		return predicate(lsp)
	}).List(ctx, &ports)
	if err != nil {
		return nil, NewTransactionError("ListLogicalSwitchPortsWithPredicate", err, "")
	}

	return ports, nil
}

// ListLogicalSwitchPortsByNamespace lists ports in a specific namespace
func (o *LogicalSwitchPortOps) ListLogicalSwitchPortsByNamespace(ctx context.Context, namespace string) ([]*LogicalSwitchPort, error) {
	return o.ListLogicalSwitchPortsWithPredicate(ctx, func(lsp *LogicalSwitchPort) bool {
		return lsp.ExternalIDs[ExternalIDNamespace] == namespace
	})
}

// UpdateLogicalSwitchPort updates a Logical Switch Port
func (o *LogicalSwitchPortOps) UpdateLogicalSwitchPort(ctx context.Context, lsp *LogicalSwitchPort, fields ...interface{}) error {
	if lsp == nil || lsp.Name == "" {
		return NewValidationError("lsp", lsp, "logical switch port with name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	if len(fields) == 0 {
		fields = getLogicalSwitchPortMutableFields(lsp)
	}

	ops, err := nbClient.Where(lsp).Update(lsp, fields...)
	if err != nil {
		return NewTransactionError("UpdateLogicalSwitchPort", err, lsp.Name)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// DeleteLogicalSwitchPort deletes a Logical Switch Port and removes it from its switch
//
// Parameters:
//   - ctx: Context for cancellation
//   - switchName: Name of the Logical Switch containing the port
//   - portName: Name of the port to delete
//
// Returns:
//   - error: Deletion error (nil if port doesn't exist)
func (o *LogicalSwitchPortOps) DeleteLogicalSwitchPort(ctx context.Context, switchName, portName string) error {
	if switchName == "" {
		return NewValidationError("switchName", switchName, "switch name is required")
	}
	if portName == "" {
		return NewValidationError("portName", portName, "port name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	// Get the port to find its UUID
	lsp, err := o.GetLogicalSwitchPort(ctx, portName)
	if err != nil {
		if IsNotFound(err) {
			return nil // Port doesn't exist, nothing to delete
		}
		return err
	}

	// Remove port from switch
	ls := &LogicalSwitch{Name: switchName}
	mutateOps, err := nbClient.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.Ports,
		Mutator: ovsdb.MutateOperationDelete,
		Value:   []string{lsp.UUID},
	})
	if err != nil {
		return NewTransactionError("DeleteLogicalSwitchPort", err, portName)
	}

	// Delete the port
	deleteOps, err := nbClient.Where(lsp).Delete()
	if err != nil {
		return NewTransactionError("DeleteLogicalSwitchPort", err, portName)
	}

	ops := append(mutateOps, deleteOps...)
	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// SetAddresses updates the addresses of a Logical Switch Port
//
// Parameters:
//   - ctx: Context for cancellation
//   - portName: Name of the port
//   - mac: MAC address
//   - ips: IP addresses
//
// Returns:
//   - error: Update error
func (o *LogicalSwitchPortOps) SetAddresses(ctx context.Context, portName, mac string, ips []string) error {
	lsp, err := o.GetLogicalSwitchPort(ctx, portName)
	if err != nil {
		return err
	}

	addresses := buildAddresses(mac, ips)
	lsp.Addresses = []string{addresses}

	// Update port security as well
	if lsp.Type == PortTypeNormal && mac != "" && len(ips) > 0 {
		lsp.PortSecurity = []string{addresses}
	}

	return o.UpdateLogicalSwitchPort(ctx, lsp, &lsp.Addresses, &lsp.PortSecurity)
}

// SetOptions sets options on a Logical Switch Port
// Empty values will delete the corresponding keys
func (o *LogicalSwitchPortOps) SetOptions(ctx context.Context, portName string, options map[string]string) error {
	lsp, err := o.GetLogicalSwitchPort(ctx, portName)
	if err != nil {
		return err
	}

	if lsp.Options == nil {
		lsp.Options = make(map[string]string)
	}

	for k, v := range options {
		if v == "" {
			delete(lsp.Options, k)
		} else {
			lsp.Options[k] = v
		}
	}

	return o.UpdateLogicalSwitchPort(ctx, lsp, &lsp.Options)
}

// SetExternalIDs sets external_ids on a Logical Switch Port
// Empty values will delete the corresponding keys
func (o *LogicalSwitchPortOps) SetExternalIDs(ctx context.Context, portName string, ids map[string]string) error {
	lsp, err := o.GetLogicalSwitchPort(ctx, portName)
	if err != nil {
		return err
	}

	if lsp.ExternalIDs == nil {
		lsp.ExternalIDs = make(map[string]string)
	}

	for k, v := range ids {
		if v == "" {
			delete(lsp.ExternalIDs, k)
		} else {
			lsp.ExternalIDs[k] = v
		}
	}

	return o.UpdateLogicalSwitchPort(ctx, lsp, &lsp.ExternalIDs)
}

// SetPortSecurity sets port security on a Logical Switch Port
func (o *LogicalSwitchPortOps) SetPortSecurity(ctx context.Context, portName string, portSecurity []string) error {
	lsp, err := o.GetLogicalSwitchPort(ctx, portName)
	if err != nil {
		return err
	}

	lsp.PortSecurity = portSecurity
	return o.UpdateLogicalSwitchPort(ctx, lsp, &lsp.PortSecurity)
}

// SetEnabled enables or disables a Logical Switch Port
func (o *LogicalSwitchPortOps) SetEnabled(ctx context.Context, portName string, enabled bool) error {
	lsp, err := o.GetLogicalSwitchPort(ctx, portName)
	if err != nil {
		return err
	}

	lsp.Enabled = &enabled
	return o.UpdateLogicalSwitchPort(ctx, lsp, &lsp.Enabled)
}

// BuildPortName builds a port name from namespace and pod name
// Format: namespace_podName
func BuildPortName(namespace, podName string) string {
	return fmt.Sprintf("%s_%s", namespace, podName)
}

// ParsePortName parses a port name into namespace and pod name
func ParsePortName(portName string) (namespace, podName string, err error) {
	parts := strings.SplitN(portName, "_", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid port name format: %s", portName)
	}
	return parts[0], parts[1], nil
}

// buildAddresses builds the addresses string from MAC and IPs
// Format: "MAC IP1 IP2 ..."
func buildAddresses(mac string, ips []string) string {
	if mac == "" {
		return ""
	}
	if len(ips) == 0 {
		return mac
	}
	return fmt.Sprintf("%s %s", mac, strings.Join(ips, " "))
}

// getLogicalSwitchPortMutableFields returns the mutable fields of a LogicalSwitchPort
func getLogicalSwitchPortMutableFields(lsp *LogicalSwitchPort) []interface{} {
	fields := []interface{}{}
	if lsp.Addresses != nil {
		fields = append(fields, &lsp.Addresses)
	}
	if lsp.PortSecurity != nil {
		fields = append(fields, &lsp.PortSecurity)
	}
	if lsp.Options != nil {
		fields = append(fields, &lsp.Options)
	}
	if lsp.ExternalIDs != nil {
		fields = append(fields, &lsp.ExternalIDs)
	}
	if lsp.Enabled != nil {
		fields = append(fields, &lsp.Enabled)
	}
	return fields
}
