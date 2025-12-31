// Package ovndb provides Load Balancer operations.
//
// This file implements CRUD operations for OVN Load Balancers.
// A Load Balancer implements L4 load balancing for Kubernetes Services.
//
// In Kubernetes context:
// - Each Service maps to one or more Load Balancers
// - ClusterIP Service: One LB with VIP = ClusterIP
// - NodePort Service: Additional LBs for node ports
// - LoadBalancer Service: Additional LBs for external IPs
//
// Key OVN Load Balancer fields:
// - name: Unique identifier (format: namespace/serviceName or service-uuid)
// - vips: VIP to backend mapping (format: "VIP:PORT" -> "BACKEND1:PORT,BACKEND2:PORT")
// - protocol: TCP, UDP, or SCTP
// - options: Load balancer options (hairpin_snat_ip, skip_snat, etc.)
// - external_ids: External identifiers (k8s service info)
//
// VIP Format Examples:
// - IPv4: "10.96.0.1:80" -> "10.244.1.5:8080,10.244.2.6:8080"
// - IPv6: "[fd00::1]:80" -> "[fd00::5]:8080,[fd00::6]:8080"
//
// Reference: OVN-Kubernetes pkg/libovsdb/ops/loadbalancer.go
package ovndb

import (
	"context"
	"fmt"
	"strings"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"
)

// Load Balancer option keys
const (
	// LBOptionHairpinSNATIP enables hairpin SNAT with specified IP
	LBOptionHairpinSNATIP = "hairpin_snat_ip"

	// LBOptionSkipSNAT skips SNAT for load balanced traffic
	LBOptionSkipSNAT = "skip_snat"

	// LBOptionRejectAction specifies action for rejected connections
	LBOptionRejectAction = "reject"

	// LBOptionNeighborResponder enables neighbor responder for VIPs
	LBOptionNeighborResponder = "neighbor_responder"

	// LBOptionEventEnabled enables connection tracking events
	LBOptionEventEnabled = "event"

	// LBOptionAffinityTimeout sets session affinity timeout in seconds
	LBOptionAffinityTimeout = "affinity_timeout"
)

// External ID keys for Load Balancers
const (
	// LBExternalIDService is the key for Kubernetes Service reference
	LBExternalIDService = "k8s.ovn.org/service"

	// LBExternalIDNamespace is the key for Service namespace
	LBExternalIDNamespace = "k8s.ovn.org/namespace"

	// LBExternalIDKind is the key for LB kind (ClusterIP, NodePort, etc.)
	LBExternalIDKind = "k8s.ovn.org/kind"
)

// LoadBalancerOps provides operations on OVN Load Balancers
type LoadBalancerOps struct {
	client *Client
}

// NewLoadBalancerOps creates a new LoadBalancerOps
func NewLoadBalancerOps(c *Client) *LoadBalancerOps {
	return &LoadBalancerOps{client: c}
}

// CreateLoadBalancer creates a new Load Balancer
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Unique name for the load balancer
//   - protocol: Protocol (tcp, udp, sctp)
//   - vips: VIP to backend mapping
//   - options: Load balancer options
//   - externalIDs: External identifiers
//
// Returns:
//   - *LoadBalancer: The created load balancer with UUID populated
//   - error: Creation error
//
// Example:
//
//	lb, err := ops.CreateLoadBalancer(ctx, "default/nginx-svc", "tcp",
//	    map[string]string{"10.96.0.100:80": "10.244.1.5:8080,10.244.2.6:8080"},
//	    nil,
//	    map[string]string{"k8s.ovn.org/service": "default/nginx-svc"})
func (o *LoadBalancerOps) CreateLoadBalancer(
	ctx context.Context,
	name, protocol string,
	vips map[string]string,
	options, externalIDs map[string]string,
) (*LoadBalancer, error) {
	if name == "" {
		return nil, NewValidationError("name", name, "name is required")
	}
	if protocol == "" {
		protocol = LoadBalancerProtocolTCP
	}

	// Validate protocol
	if protocol != LoadBalancerProtocolTCP && protocol != LoadBalancerProtocolUDP && protocol != LoadBalancerProtocolSCTP {
		return nil, NewValidationError("protocol", protocol, "protocol must be tcp, udp, or sctp")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	lb := &LoadBalancer{
		UUID:        BuildNamedUUID(name),
		Name:        name,
		Protocol:    &protocol,
		Vips:        vips,
		Options:     options,
		ExternalIDs: externalIDs,
	}

	ops, err := nbClient.Create(lb)
	if err != nil {
		return nil, NewTransactionError("CreateLoadBalancer", err, name)
	}

	results, err := TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	if err != nil {
		return nil, err
	}

	if len(results) > 0 {
		lb.UUID = GetUUIDFromResult(results[0])
	}

	return lb, nil
}

// GetLoadBalancer retrieves a Load Balancer by name
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the load balancer to retrieve
//
// Returns:
//   - *LoadBalancer: The found load balancer
//   - error: ObjectNotFoundError if not found, or other error
func (o *LoadBalancerOps) GetLoadBalancer(ctx context.Context, name string) (*LoadBalancer, error) {
	if name == "" {
		return nil, NewValidationError("name", name, "name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	lb := &LoadBalancer{Name: name}
	err := nbClient.Get(ctx, lb)
	if err != nil {
		if err == client.ErrNotFound {
			return nil, NewObjectNotFoundError("LoadBalancer", name)
		}
		return nil, NewTransactionError("GetLoadBalancer", err, name)
	}

	return lb, nil
}

// GetLoadBalancerByUUID retrieves a Load Balancer by UUID
func (o *LoadBalancerOps) GetLoadBalancerByUUID(ctx context.Context, uuid string) (*LoadBalancer, error) {
	if uuid == "" {
		return nil, NewValidationError("uuid", uuid, "uuid is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	lb := &LoadBalancer{UUID: uuid}
	err := nbClient.Get(ctx, lb)
	if err != nil {
		if err == client.ErrNotFound {
			return nil, NewObjectNotFoundError("LoadBalancer", uuid)
		}
		return nil, NewTransactionError("GetLoadBalancerByUUID", err, uuid)
	}

	return lb, nil
}

// ListLoadBalancers lists all Load Balancers
func (o *LoadBalancerOps) ListLoadBalancers(ctx context.Context) ([]*LoadBalancer, error) {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	var lbs []*LoadBalancer
	err := nbClient.List(ctx, &lbs)
	if err != nil {
		return nil, NewTransactionError("ListLoadBalancers", err, "")
	}

	return lbs, nil
}

// ListLoadBalancersWithPredicate lists Load Balancers matching a predicate
//
// Example:
//
//	lbs, err := ops.ListLoadBalancersWithPredicate(ctx, func(lb *LoadBalancer) bool {
//	    return lb.ExternalIDs["k8s.ovn.org/namespace"] == "default"
//	})
func (o *LoadBalancerOps) ListLoadBalancersWithPredicate(ctx context.Context, predicate func(*LoadBalancer) bool) ([]*LoadBalancer, error) {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	var lbs []*LoadBalancer
	err := nbClient.WhereCache(func(lb *LoadBalancer) bool {
		return predicate(lb)
	}).List(ctx, &lbs)
	if err != nil {
		return nil, NewTransactionError("ListLoadBalancersWithPredicate", err, "")
	}

	return lbs, nil
}

// UpdateLoadBalancer updates a Load Balancer
func (o *LoadBalancerOps) UpdateLoadBalancer(ctx context.Context, lb *LoadBalancer, fields ...interface{}) error {
	if lb == nil || lb.Name == "" {
		return NewValidationError("lb", lb, "load balancer with name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	if len(fields) == 0 {
		fields = getLoadBalancerMutableFields(lb)
	}

	ops, err := nbClient.Where(lb).Update(lb, fields...)
	if err != nil {
		return NewTransactionError("UpdateLoadBalancer", err, lb.Name)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// DeleteLoadBalancer deletes a Load Balancer by name
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the load balancer to delete
//
// Returns:
//   - error: Deletion error (nil if load balancer doesn't exist)
func (o *LoadBalancerOps) DeleteLoadBalancer(ctx context.Context, name string) error {
	if name == "" {
		return NewValidationError("name", name, "name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	lb := &LoadBalancer{Name: name}
	ops, err := nbClient.Where(lb).Delete()
	if err != nil {
		return NewTransactionError("DeleteLoadBalancer", err, name)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// DeleteLoadBalancerOps returns operations to delete a Load Balancer
func (o *LoadBalancerOps) DeleteLoadBalancerOps(name string) ([]ovsdb.Operation, error) {
	if name == "" {
		return nil, NewValidationError("name", name, "name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	lb := &LoadBalancer{Name: name}
	return nbClient.Where(lb).Delete()
}

// CreateOrUpdateLoadBalancer creates or updates a Load Balancer
func (o *LoadBalancerOps) CreateOrUpdateLoadBalancer(ctx context.Context, lb *LoadBalancer) error {
	if lb == nil || lb.Name == "" {
		return NewValidationError("lb", lb, "load balancer with name is required")
	}

	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	existing, err := o.GetLoadBalancer(ctx, lb.Name)
	if err != nil && !IsNotFound(err) {
		return err
	}

	if existing != nil {
		lb.UUID = existing.UUID
		return o.UpdateLoadBalancer(ctx, lb)
	}

	protocol := LoadBalancerProtocolTCP
	if lb.Protocol != nil {
		protocol = *lb.Protocol
	}
	_, err = o.CreateLoadBalancer(ctx, lb.Name, protocol, lb.Vips, lb.Options, lb.ExternalIDs)
	return err
}

// SetVips sets the VIPs on a Load Balancer (replaces all existing VIPs)
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the load balancer
//   - vips: VIP to backend mapping
//
// Returns:
//   - error: Update error
func (o *LoadBalancerOps) SetVips(ctx context.Context, name string, vips map[string]string) error {
	lb, err := o.GetLoadBalancer(ctx, name)
	if err != nil {
		return err
	}

	lb.Vips = vips
	return o.UpdateLoadBalancer(ctx, lb, &lb.Vips)
}

// AddVip adds a VIP to a Load Balancer
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the load balancer
//   - vip: VIP address with port (e.g., "10.96.0.100:80")
//   - backends: Backend addresses with ports (e.g., "10.244.1.5:8080,10.244.2.6:8080")
//
// Returns:
//   - error: Update error
func (o *LoadBalancerOps) AddVip(ctx context.Context, name, vip, backends string) error {
	lb, err := o.GetLoadBalancer(ctx, name)
	if err != nil {
		return err
	}

	if lb.Vips == nil {
		lb.Vips = make(map[string]string)
	}
	lb.Vips[vip] = backends

	return o.UpdateLoadBalancer(ctx, lb, &lb.Vips)
}

// RemoveVip removes a VIP from a Load Balancer
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the load balancer
//   - vip: VIP address with port to remove
//
// Returns:
//   - error: Update error
func (o *LoadBalancerOps) RemoveVip(ctx context.Context, name, vip string) error {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	lb := &LoadBalancer{Name: name}
	ops, err := nbClient.Where(lb).Mutate(lb, model.Mutation{
		Field:   &lb.Vips,
		Mutator: ovsdb.MutateOperationDelete,
		Value:   map[string]string{vip: ""},
	})
	if err != nil {
		return NewTransactionError("RemoveVip", err, name)
	}

	_, err = TransactAndCheck(nbClient, ops, o.client.GetTxnTimeout())
	return err
}

// UpdateBackends updates the backends for a specific VIP
//
// Parameters:
//   - ctx: Context for cancellation
//   - name: Name of the load balancer
//   - vip: VIP address with port
//   - backends: New backend addresses
//
// Returns:
//   - error: Update error
func (o *LoadBalancerOps) UpdateBackends(ctx context.Context, name, vip string, backends []string) error {
	lb, err := o.GetLoadBalancer(ctx, name)
	if err != nil {
		return err
	}

	if lb.Vips == nil {
		lb.Vips = make(map[string]string)
	}
	lb.Vips[vip] = strings.Join(backends, ",")

	return o.UpdateLoadBalancer(ctx, lb, &lb.Vips)
}

// SetOptions sets options on a Load Balancer
// Empty values will delete the corresponding keys
func (o *LoadBalancerOps) SetOptions(ctx context.Context, name string, options map[string]string) error {
	lb, err := o.GetLoadBalancer(ctx, name)
	if err != nil {
		return err
	}

	if lb.Options == nil {
		lb.Options = make(map[string]string)
	}

	for k, v := range options {
		if v == "" {
			delete(lb.Options, k)
		} else {
			lb.Options[k] = v
		}
	}

	return o.UpdateLoadBalancer(ctx, lb, &lb.Options)
}

// SetExternalIDs sets external_ids on a Load Balancer
// Empty values will delete the corresponding keys
func (o *LoadBalancerOps) SetExternalIDs(ctx context.Context, name string, ids map[string]string) error {
	lb, err := o.GetLoadBalancer(ctx, name)
	if err != nil {
		return err
	}

	if lb.ExternalIDs == nil {
		lb.ExternalIDs = make(map[string]string)
	}

	for k, v := range ids {
		if v == "" {
			delete(lb.ExternalIDs, k)
		} else {
			lb.ExternalIDs[k] = v
		}
	}

	return o.UpdateLoadBalancer(ctx, lb, &lb.ExternalIDs)
}

// BuildVIP builds a VIP string from IP and port
// Handles both IPv4 and IPv6 addresses
func BuildVIP(ip string, port int) string {
	// Check if IPv6
	if strings.Contains(ip, ":") {
		return fmt.Sprintf("[%s]:%d", ip, port)
	}
	return fmt.Sprintf("%s:%d", ip, port)
}

// BuildBackends builds a backends string from a list of backend addresses
func BuildBackends(backends []string) string {
	return strings.Join(backends, ",")
}

// ParseVIP parses a VIP string into IP and port
func ParseVIP(vip string) (ip string, port string, err error) {
	// Handle IPv6 format: [ip]:port
	if strings.HasPrefix(vip, "[") {
		idx := strings.LastIndex(vip, "]:")
		if idx == -1 {
			return "", "", fmt.Errorf("invalid IPv6 VIP format: %s", vip)
		}
		return vip[1:idx], vip[idx+2:], nil
	}

	// Handle IPv4 format: ip:port
	idx := strings.LastIndex(vip, ":")
	if idx == -1 {
		return "", "", fmt.Errorf("invalid VIP format: %s", vip)
	}
	return vip[:idx], vip[idx+1:], nil
}

// ParseBackends parses a backends string into a list of backend addresses
func ParseBackends(backends string) []string {
	if backends == "" {
		return nil
	}
	return strings.Split(backends, ",")
}

// getLoadBalancerMutableFields returns the mutable fields of a LoadBalancer
func getLoadBalancerMutableFields(lb *LoadBalancer) []interface{} {
	fields := []interface{}{}
	if lb.Name != "" {
		fields = append(fields, &lb.Name)
	}
	if lb.Vips != nil {
		fields = append(fields, &lb.Vips)
	}
	if lb.Protocol != nil {
		fields = append(fields, &lb.Protocol)
	}
	if lb.Options != nil {
		fields = append(fields, &lb.Options)
	}
	if lb.ExternalIDs != nil {
		fields = append(fields, &lb.ExternalIDs)
	}
	if lb.SelectionFields != nil {
		fields = append(fields, &lb.SelectionFields)
	}
	if lb.IPPortMappings != nil {
		fields = append(fields, &lb.IPPortMappings)
	}
	if lb.HealthCheck != nil {
		fields = append(fields, &lb.HealthCheck)
	}
	return fields
}
