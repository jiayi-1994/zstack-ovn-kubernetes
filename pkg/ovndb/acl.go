// Package ovndb provides ACL (Access Control List) operations.
//
// This file implements CRUD operations for OVN ACLs.
// An ACL implements network policies by filtering traffic based on match conditions.
//
// In Kubernetes context:
// - NetworkPolicy rules are translated to OVN ACLs
// - Ingress rules use direction "to-lport"
// - Egress rules use direction "from-lport"
//
// Key OVN ACL fields:
// - direction: "from-lport" (egress) or "to-lport" (ingress)
// - priority: Higher priority rules are evaluated first (0-32767)
// - match: OVN match expression (e.g., "ip4.src == 10.0.0.0/8 && tcp.dst == 80")
// - action: "allow", "allow-related", "drop", or "reject"
// - external_ids: External identifiers (NetworkPolicy reference)
//
// Match Expression Examples:
// - Allow all from subnet: "ip4.src == 10.244.0.0/16"
// - Allow TCP port 80: "tcp.dst == 80"
// - Allow from specific pod: "inport == \"namespace_podname\""
// - Combined: "ip4.src == 10.244.0.0/16 && tcp.dst == 80"
//
// Priority Guidelines:
// - Default deny: 1000
// - Allow rules: 1001-2000
// - Admin policies: 2001-3000
//
// Reference: OVN-Kubernetes pkg/libovsdb/ops/acl.go
package ovndb

import (
	"context"
	"fmt"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/ovsdb"
)

// ACL severity constants for logging
const (
	ACLSeverityAlert   = "alert"
	ACLSeverityWarning = "warning"
	ACLSeverityNotice  = "notice"
	ACLSeverityInfo    = "info"
	ACLSeverityDebug   = "debug"
)

// External ID keys for ACLs
const (
	// ACLExternalIDPolicy is the key for NetworkPolicy reference
	ACLExternalIDPolicy = "k8s.ovn.org/policy"

	// ACLExternalIDNamespace is the key for Policy namespace
	ACLExternalIDNamespace = "k8s.ovn.org/namespace"

	// ACLExternalIDDirection is the key for rule direction
	ACLExternalIDDirection = "k8s.ovn.org/direction"

	// ACLExternalIDOwner is the key for owner reference
	ACLExternalIDOwner = "k8s.ovn.org/owner"

	// ACLExternalIDPrimaryID is the primary ID key for indexing
	ACLExternalIDPrimaryID = "k8s.ovn.org/id"
)

// Default ACL priorities
const (
	// ACLPriorityDefaultDeny is the priority for default deny rules
	ACLPriorityDefaultDeny = 1000

	// ACLPriorityAllowBase is the base priority for allow rules
	ACLPriorityAllowBase = 1001

	// ACLPriorityAdminBase is the base priority for admin policies
	ACLPriorityAdminBase = 2001
)

// ACLOps provides operations on OVN ACLs
type ACLOps struct {
	client *Client
}

// NewACLOps creates a new ACLOps
func NewACLOps(c *Client) *ACLOps {
	return &ACLOps{client: c}
}

// CreateACL creates a new ACL
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Optional name for the ACL (can be nil)
//   - direction: "from-lport" (egress) or "to-lport" (ingress)
//   - priority: Rule priority (0-32767, higher = evaluated first)
//   - match: OVN match expression
//   - action: "allow", "allow-related", "drop", or "reject"
//   - externalIDs: External identifiers
//
// Returns:
//   - *ACL: The created ACL with UUID populated
//   - error: Creation error
//
// Example:
//
//	acl, err := ops.CreateACL(ctx, nil, ACLDirectionToLport, 1001,
//	    "ip4.src == 10.244.0.0/16 && tcp.dst == 80", ACLActionAllow,
//	    map[string]string{"k8s.ovn.org/policy": "default/allow-http"})
func (o *ACLOps) CreateACL(
	ctx context.Context,
	name *string,
	direction string,
	priority int,
	match, action string,
	externalIDs map[string]string,
) (*ACL, error) {
	// Validate direction
	if direction != ACLDirectionFromLport && direction != ACLDirectionToLport {
		return nil, NewValidationError("direction", direction, "direction must be from-lport or to-lport")
	}

	// Validate priority
	if priority < 0 || priority > 32767 {
		return nil, NewValidationError("priority", priority, "priority must be between 0 and 32767")
	}

	// Validate action
	if action != ACLActionAllow && action != ACLActionAllowRelated &&
		action != ACLActionDrop && action != ACLActionReject && action != ACLActionPass {
		return nil, NewValidationError("action", action, "action must be allow, allow-related, drop, reject, or pass")
	}

	if match == "" {
		return nil, NewValidationError("match", match, "match expression is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	// Truncate name to 63 characters (OVN limit)
	var truncatedName *string
	if name != nil && *name != "" {
		n := *name
		if len(n) > 63 {
			n = n[:63]
		}
		truncatedName = &n
	}

	// Generate a unique ID for the ACL
	aclID := BuildNamedUUID(fmt.Sprintf("acl-%s-%d-%s", direction, priority, match[:min(20, len(match))]))

	acl := &ACL{
		UUID:        aclID,
		Name:        truncatedName,
		Direction:   direction,
		Priority:    priority,
		Match:       match,
		Action:      action,
		ExternalIDs: externalIDs,
		Log:         false,
	}

	ops, err := nbClient.Create(acl)
	if err != nil {
		return nil, NewTransactionError("CreateACL", err, fmt.Sprintf("%v", name))
	}

	results, err := TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	if err != nil {
		return nil, err
	}

	if len(results) > 0 {
		acl.UUID = GetUUIDFromResult(results[0])
	}

	return acl, nil
}

// CreateACLWithLogging creates an ACL with logging enabled
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Optional name for the ACL
//   - direction: "from-lport" or "to-lport"
//   - priority: Rule priority
//   - match: OVN match expression
//   - action: ACL action
//   - severity: Log severity (alert, warning, notice, info, debug)
//   - externalIDs: External identifiers
//
// Returns:
//   - *ACL: The created ACL
//   - error: Creation error
func (o *ACLOps) CreateACLWithLogging(
	ctx context.Context,
	name *string,
	direction string,
	priority int,
	match, action, severity string,
	externalIDs map[string]string,
) (*ACL, error) {
	// Validate direction
	if direction != ACLDirectionFromLport && direction != ACLDirectionToLport {
		return nil, NewValidationError("direction", direction, "direction must be from-lport or to-lport")
	}

	// Validate priority
	if priority < 0 || priority > 32767 {
		return nil, NewValidationError("priority", priority, "priority must be between 0 and 32767")
	}

	// Validate action
	if action != ACLActionAllow && action != ACLActionAllowRelated &&
		action != ACLActionDrop && action != ACLActionReject && action != ACLActionPass {
		return nil, NewValidationError("action", action, "action must be allow, allow-related, drop, reject, or pass")
	}

	if match == "" {
		return nil, NewValidationError("match", match, "match expression is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	var truncatedName *string
	if name != nil && *name != "" {
		n := *name
		if len(n) > 63 {
			n = n[:63]
		}
		truncatedName = &n
	}

	var sev *string
	if severity != "" {
		sev = &severity
	}

	aclID := BuildNamedUUID(fmt.Sprintf("acl-%s-%d-%s", direction, priority, match[:min(20, len(match))]))

	acl := &ACL{
		UUID:        aclID,
		Name:        truncatedName,
		Direction:   direction,
		Priority:    priority,
		Match:       match,
		Action:      action,
		ExternalIDs: externalIDs,
		Log:         true,
		Severity:    sev,
	}

	ops, err := nbClient.Create(acl)
	if err != nil {
		return nil, NewTransactionError("CreateACLWithLogging", err, fmt.Sprintf("%v", name))
	}

	results, err := TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	if err != nil {
		return nil, err
	}

	if len(results) > 0 {
		acl.UUID = GetUUIDFromResult(results[0])
	}

	return acl, nil
}

// GetACL retrieves an ACL by UUID
func (o *ACLOps) GetACL(ctx context.Context, uuid string) (*ACL, error) {
	if uuid == "" {
		return nil, NewValidationError("uuid", uuid, "uuid is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	acl := &ACL{UUID: uuid}
	err := nbClient.Get(ctx, acl)
	if err != nil {
		if err == client.ErrNotFound {
			return nil, NewObjectNotFoundError("ACL", uuid)
		}
		return nil, NewTransactionError("GetACL", err, uuid)
	}

	return acl, nil
}

// ListACLs lists all ACLs
func (o *ACLOps) ListACLs(ctx context.Context) ([]*ACL, error) {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	var acls []*ACL
	err := nbClient.List(ctx, &acls)
	if err != nil {
		return nil, NewTransactionError("ListACLs", err, "")
	}

	return acls, nil
}

// ListACLsWithPredicate lists ACLs matching a predicate
//
// Example:
//
//	acls, err := ops.ListACLsWithPredicate(ctx, func(acl *ACL) bool {
//	    return acl.ExternalIDs["k8s.ovn.org/namespace"] == "default"
//	})
func (o *ACLOps) ListACLsWithPredicate(ctx context.Context, predicate func(*ACL) bool) ([]*ACL, error) {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	var acls []*ACL
	err := nbClient.WhereCache(func(acl *ACL) bool {
		return predicate(acl)
	}).List(ctx, &acls)
	if err != nil {
		return nil, NewTransactionError("ListACLsWithPredicate", err, "")
	}

	return acls, nil
}

// ListACLsByPolicy lists ACLs for a specific NetworkPolicy
func (o *ACLOps) ListACLsByPolicy(ctx context.Context, namespace, policyName string) ([]*ACL, error) {
	policyRef := fmt.Sprintf("%s/%s", namespace, policyName)
	return o.ListACLsWithPredicate(ctx, func(acl *ACL) bool {
		return acl.ExternalIDs[ACLExternalIDPolicy] == policyRef
	})
}

// UpdateACL updates an ACL
func (o *ACLOps) UpdateACL(ctx context.Context, acl *ACL, fields ...interface{}) error {
	if acl == nil || acl.UUID == "" {
		return NewValidationError("acl", acl, "ACL with UUID is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	if len(fields) == 0 {
		fields = getACLMutableFields(acl)
	}

	ops, err := nbClient.Where(acl).Update(acl, fields...)
	if err != nil {
		return NewTransactionError("UpdateACL", err, acl.UUID)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// DeleteACL deletes an ACL by UUID
//
// Parameters:
//   - ctx: Context for cancellation
//   - uuid: UUID of the ACL to delete
//
// Returns:
//   - error: Deletion error (nil if ACL doesn't exist)
func (o *ACLOps) DeleteACL(ctx context.Context, uuid string) error {
	if uuid == "" {
		return NewValidationError("uuid", uuid, "uuid is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	acl := &ACL{UUID: uuid}
	ops, err := nbClient.Where(acl).Delete()
	if err != nil {
		return NewTransactionError("DeleteACL", err, uuid)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// DeleteACLOps returns operations to delete an ACL
func (o *ACLOps) DeleteACLOps(uuid string) ([]ovsdb.Operation, error) {
	if uuid == "" {
		return nil, NewValidationError("uuid", uuid, "uuid is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	acl := &ACL{UUID: uuid}
	return nbClient.Where(acl).Delete()
}

// DeleteACLsByPolicy deletes all ACLs for a specific NetworkPolicy
func (o *ACLOps) DeleteACLsByPolicy(ctx context.Context, namespace, policyName string) error {
	acls, err := o.ListACLsByPolicy(ctx, namespace, policyName)
	if err != nil {
		return err
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	var ops []ovsdb.Operation
	for _, acl := range acls {
		deleteOps, err := nbClient.Where(acl).Delete()
		if err != nil {
			return NewTransactionError("DeleteACLsByPolicy", err, fmt.Sprintf("%s/%s", namespace, policyName))
		}
		ops = append(ops, deleteOps...)
	}

	if len(ops) == 0 {
		return nil
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// SetLogging enables or disables logging on an ACL
func (o *ACLOps) SetLogging(ctx context.Context, uuid string, log bool, severity string) error {
	acl, err := o.GetACL(ctx, uuid)
	if err != nil {
		return err
	}

	acl.Log = log
	if severity != "" {
		acl.Severity = &severity
	} else {
		acl.Severity = nil
	}

	return o.UpdateACL(ctx, acl, &acl.Log, &acl.Severity)
}

// SetExternalIDs sets external_ids on an ACL
// Empty values will delete the corresponding keys
func (o *ACLOps) SetExternalIDs(ctx context.Context, uuid string, ids map[string]string) error {
	acl, err := o.GetACL(ctx, uuid)
	if err != nil {
		return err
	}

	if acl.ExternalIDs == nil {
		acl.ExternalIDs = make(map[string]string)
	}

	for k, v := range ids {
		if v == "" {
			delete(acl.ExternalIDs, k)
		} else {
			acl.ExternalIDs[k] = v
		}
	}

	return o.UpdateACL(ctx, acl, &acl.ExternalIDs)
}

// BuildACL is a helper to build an ACL struct
//
// Parameters:
//   - name: Optional name for the ACL
//   - direction: "from-lport" or "to-lport"
//   - priority: Rule priority
//   - match: OVN match expression
//   - action: ACL action
//   - externalIDs: External identifiers
//
// Returns:
//   - *ACL: The built ACL struct (not yet created in database)
func BuildACL(name *string, direction string, priority int, match, action string, externalIDs map[string]string) *ACL {
	var truncatedName *string
	if name != nil && *name != "" {
		n := *name
		if len(n) > 63 {
			n = n[:63]
		}
		truncatedName = &n
	}

	return &ACL{
		Name:        truncatedName,
		Direction:   direction,
		Priority:    priority,
		Match:       match,
		Action:      action,
		ExternalIDs: externalIDs,
		Log:         false,
	}
}

// BuildMatchExpression builds a match expression from components
//
// Example:
//
//	match := BuildMatchExpression(
//	    "ip4.src == 10.244.0.0/16",
//	    "tcp.dst == 80",
//	)
//	// Result: "ip4.src == 10.244.0.0/16 && tcp.dst == 80"
func BuildMatchExpression(conditions ...string) string {
	if len(conditions) == 0 {
		return ""
	}
	if len(conditions) == 1 {
		return conditions[0]
	}

	result := conditions[0]
	for i := 1; i < len(conditions); i++ {
		if conditions[i] != "" {
			result = fmt.Sprintf("%s && %s", result, conditions[i])
		}
	}
	return result
}

// BuildIPMatch builds an IP match expression
//
// Parameters:
//   - field: IP field (ip4.src, ip4.dst, ip6.src, ip6.dst)
//   - cidr: CIDR notation (e.g., "10.244.0.0/16")
//
// Returns:
//   - string: Match expression (e.g., "ip4.src == 10.244.0.0/16")
func BuildIPMatch(field, cidr string) string {
	return fmt.Sprintf("%s == %s", field, cidr)
}

// BuildPortMatch builds a port match expression
//
// Parameters:
//   - protocol: Protocol (tcp, udp, sctp)
//   - direction: Port direction (src, dst)
//   - port: Port number
//
// Returns:
//   - string: Match expression (e.g., "tcp.dst == 80")
func BuildPortMatch(protocol, direction string, port int) string {
	return fmt.Sprintf("%s.%s == %d", protocol, direction, port)
}

// BuildPortRangeMatch builds a port range match expression
//
// Parameters:
//   - protocol: Protocol (tcp, udp, sctp)
//   - direction: Port direction (src, dst)
//   - startPort: Start port
//   - endPort: End port
//
// Returns:
//   - string: Match expression (e.g., "tcp.dst >= 8000 && tcp.dst <= 9000")
func BuildPortRangeMatch(protocol, direction string, startPort, endPort int) string {
	return fmt.Sprintf("%s.%s >= %d && %s.%s <= %d",
		protocol, direction, startPort, protocol, direction, endPort)
}

// BuildInportMatch builds an inport match expression
//
// Parameters:
//   - portName: Logical switch port name
//
// Returns:
//   - string: Match expression (e.g., "inport == \"namespace_podname\"")
func BuildInportMatch(portName string) string {
	return fmt.Sprintf("inport == \"%s\"", portName)
}

// BuildOutportMatch builds an outport match expression
//
// Parameters:
//   - portName: Logical switch port name
//
// Returns:
//   - string: Match expression (e.g., "outport == \"namespace_podname\"")
func BuildOutportMatch(portName string) string {
	return fmt.Sprintf("outport == \"%s\"", portName)
}

// getACLMutableFields returns the mutable fields of an ACL
func getACLMutableFields(acl *ACL) []interface{} {
	return []interface{}{
		&acl.Action,
		&acl.Direction,
		&acl.ExternalIDs,
		&acl.Log,
		&acl.Match,
		&acl.Meter,
		&acl.Name,
		&acl.Options,
		&acl.Priority,
		&acl.Severity,
		&acl.Tier,
	}
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
