// Package ovndb provides OVN Logical Router operations.
//
// This file implements operations for managing OVN Logical Routers and Router Ports.
// Logical Routers provide L3 routing between Logical Switches and to external networks.
//
// Architecture:
//
//	┌─────────────────────────────────────────────────────────────────────────┐
//	│                         OVN Cluster Router                               │
//	│                                                                          │
//	│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐              │
//	│  │  LRP-node1   │    │  LRP-node2   │    │  LRP-node3   │              │
//	│  │ 10.244.0.1   │    │ 10.244.1.1   │    │ 10.244.2.1   │              │
//	│  └──────┬───────┘    └──────┬───────┘    └──────┬───────┘              │
//	└─────────┼───────────────────┼───────────────────┼───────────────────────┘
//	          │                   │                   │
//	          ▼                   ▼                   ▼
//	┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
//	│  node-1 Switch  │  │  node-2 Switch  │  │  node-3 Switch  │
//	│  10.244.0.0/24  │  │  10.244.1.0/24  │  │  10.244.2.0/24  │
//	└─────────────────┘  └─────────────────┘  └─────────────────┘
//
// Reference: OVN-Kubernetes pkg/ovn/base_network_controller.go
package ovndb

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"
	"k8s.io/klog/v2"
)

const (
	// ClusterRouterName is the name of the cluster-wide logical router
	ClusterRouterName = "ovn-cluster-router"

	// RouterPortPrefix is the prefix for router ports connecting to node switches
	RouterPortPrefix = "rtos-"

	// SwitchPortToRouterPrefix is the prefix for switch ports connecting to the router
	SwitchPortToRouterPrefix = "stor-"

	// JoinSwitchName is the name of the join switch connecting router to external network
	JoinSwitchName = "join"

	// JoinRouterPortName is the router port connecting to join switch
	JoinRouterPortName = "rtoj-ovn-cluster-router"

	// JoinSwitchPortPrefix is the prefix for switch ports on join switch
	JoinSwitchPortPrefix = "jtor-"

	// ExternalSwitchName is the external switch for gateway
	ExternalSwitchName = "ext_"

	// GatewayRouterPortPrefix is the prefix for gateway router ports
	GatewayRouterPortPrefix = "rtoe-"
)

// LogicalRouterOps provides operations for OVN Logical Routers.
type LogicalRouterOps struct {
	client *Client
}

// NewLogicalRouterOps creates a new LogicalRouterOps instance.
func NewLogicalRouterOps(c *Client) *LogicalRouterOps {
	return &LogicalRouterOps{client: c}
}

// CreateClusterRouter creates the cluster-wide logical router if it doesn't exist.
//
// The cluster router is the central routing point for all node subnets.
// It enables Pod-to-Pod communication across nodes and provides the gateway
// for external traffic.
//
// This function is idempotent - it uses CreateOrUpdate pattern to avoid
// creating duplicate routers due to cache sync issues.
func (o *LogicalRouterOps) CreateClusterRouter(ctx context.Context) (*LogicalRouter, error) {
	router := &LogicalRouter{
		Name: ClusterRouterName,
		ExternalIDs: map[string]string{
			"k8s.ovn.org/kind":  "cluster-router",
			"k8s.ovn.org/owner": "zstack-ovn-kubernetes",
		},
		Options: map[string]string{
			"always_learn_from_arp_request": "false",
		},
	}

	// Use CreateOrUpdate pattern (like ovn-kubernetes) to ensure idempotency
	// This avoids duplicate creation due to cache sync issues
	if err := o.CreateOrUpdateLogicalRouter(ctx, router); err != nil {
		return nil, fmt.Errorf("failed to create/update cluster router: %w", err)
	}

	klog.Infof("Ensured cluster router %s", ClusterRouterName)
	return router, nil
}

// CreateOrUpdateLogicalRouter creates a logical router if it doesn't exist, or updates it if it does.
// This is the idempotent pattern used by ovn-kubernetes.
func (o *LogicalRouterOps) CreateOrUpdateLogicalRouter(ctx context.Context, router *LogicalRouter) error {
	// First, try to get the existing router by name
	existing := &LogicalRouter{Name: router.Name}
	err := o.client.nbClient.Get(ctx, existing)

	if err == nil {
		// Router exists, update it if needed
		router.UUID = existing.UUID
		klog.V(4).Infof("Logical router %s already exists with UUID %s", router.Name, existing.UUID)
		return nil
	}

	if err != client.ErrNotFound {
		return fmt.Errorf("failed to check existing router: %w", err)
	}

	// Router doesn't exist, create it with a named UUID
	router.UUID = BuildNamedUUID(router.Name)

	ops, err := o.client.nbClient.Create(router)
	if err != nil {
		return fmt.Errorf("failed to create router operation: %w", err)
	}

	results, err := o.client.nbClient.Transact(ctx, ops...)
	if err != nil {
		// Check if it's a duplicate error (another reconcile created it)
		if isDuplicateError(err) {
			klog.V(4).Infof("Router %s was created by another reconcile, this is expected", router.Name)
			return nil
		}
		return fmt.Errorf("failed to create router: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		// Check if it's a constraint violation (duplicate)
		if isDuplicateError(err) {
			klog.V(4).Infof("Router %s already exists (constraint violation), this is expected", router.Name)
			return nil
		}
		return fmt.Errorf("router creation failed: %w", err)
	}

	// Set the real UUID from the transaction result
	if len(results) > 0 && results[0].UUID.GoUUID != "" {
		router.UUID = results[0].UUID.GoUUID
	}

	klog.Infof("Created logical router %s with UUID %s", router.Name, router.UUID)
	return nil
}

// isDuplicateError checks if an error indicates a duplicate/constraint violation
func isDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "constraint violation") ||
		strings.Contains(errStr, "duplicate") ||
		strings.Contains(errStr, "already exists")
}

// GetLogicalRouter retrieves a logical router by name.
func (o *LogicalRouterOps) GetLogicalRouter(ctx context.Context, name string) (*LogicalRouter, error) {
	router := &LogicalRouter{Name: name}
	err := o.client.nbClient.Get(ctx, router)
	if err != nil {
		if err == client.ErrNotFound {
			return nil, &ObjectNotFoundError{ObjectType: "LogicalRouter", ObjectName: name}
		}
		return nil, fmt.Errorf("failed to get logical router %s: %w", name, err)
	}
	return router, nil
}

// EnsureRouterPortToSwitch ensures a router port exists connecting the cluster router
// to a node's logical switch.
//
// Parameters:
//   - ctx: Context
//   - switchName: Name of the node's logical switch
//   - gatewayIP: Gateway IP for the subnet (e.g., "10.244.0.1")
//   - subnetCIDR: Subnet CIDR (e.g., "10.244.0.0/24")
//   - mac: MAC address for the router port
//
// This creates:
// 1. A Logical Router Port on the cluster router
// 2. A Logical Switch Port on the node switch connecting to the router
func (o *LogicalRouterOps) EnsureRouterPortToSwitch(
	ctx context.Context,
	switchName string,
	gatewayIP string,
	subnetCIDR string,
	mac string,
) error {
	lrpName := RouterPortPrefix + switchName
	lspName := SwitchPortToRouterPrefix + switchName

	// Extract prefix length from subnetCIDR (e.g., "10.244.0.0/24" -> "24")
	prefixLen := ""
	if idx := strings.LastIndex(subnetCIDR, "/"); idx != -1 {
		prefixLen = subnetCIDR[idx+1:]
	} else {
		prefixLen = "24" // Default to /24 if not specified
	}

	// Network in CIDR format for the router port: gatewayIP/prefixLen
	network := fmt.Sprintf("%s/%s", gatewayIP, prefixLen)

	klog.V(4).Infof("Ensuring router port %s with network %s", lrpName, network)

	// Create or update the Logical Router Port
	lrp := &LogicalRouterPort{
		Name:     lrpName,
		MAC:      mac,
		Networks: []string{network},
		ExternalIDs: map[string]string{
			"k8s.ovn.org/switch": switchName,
		},
	}

	// Get the cluster router
	router, err := o.GetLogicalRouter(ctx, ClusterRouterName)
	if err != nil {
		return fmt.Errorf("cluster router not found: %w", err)
	}

	// Check if port already exists
	existingLRP := &LogicalRouterPort{Name: lrpName}
	err = o.client.nbClient.Get(ctx, existingLRP)
	if err != nil && err != client.ErrNotFound {
		return fmt.Errorf("failed to check existing router port: %w", err)
	}

	var ops []ovsdb.Operation

	if err == client.ErrNotFound {
		// Create new router port
		createOps, err := o.client.nbClient.Create(lrp)
		if err != nil {
			return fmt.Errorf("failed to create router port operation: %w", err)
		}
		ops = append(ops, createOps...)

		// Add port to router
		mutateOps, err := o.client.nbClient.Where(router).Mutate(router, model.Mutation{
			Field:   &router.Ports,
			Mutator: ovsdb.MutateOperationInsert,
			Value:   []string{lrp.UUID},
		})
		if err != nil {
			return fmt.Errorf("failed to create router mutation: %w", err)
		}
		ops = append(ops, mutateOps...)
	} else {
		// Update existing port if needed
		if existingLRP.MAC != mac || len(existingLRP.Networks) == 0 || existingLRP.Networks[0] != network {
			existingLRP.MAC = mac
			existingLRP.Networks = []string{network}
			updateOps, err := o.client.nbClient.Where(existingLRP).Update(existingLRP, &existingLRP.MAC, &existingLRP.Networks)
			if err != nil {
				return fmt.Errorf("failed to create update operation: %w", err)
			}
			ops = append(ops, updateOps...)
		}
	}

	// Create the corresponding switch port
	lsp := &LogicalSwitchPort{
		Name: lspName,
		Type: "router",
		Options: map[string]string{
			"router-port": lrpName,
		},
		Addresses: []string{"router"},
		ExternalIDs: map[string]string{
			"k8s.ovn.org/router-port": lrpName,
		},
	}

	lspOps := NewLogicalSwitchPortOps(o.client)
	existingLSP, err := lspOps.GetLogicalSwitchPort(ctx, lspName)
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("failed to check existing switch port: %w", err)
	}

	if IsNotFound(err) || existingLSP == nil {
		// Create switch port and add to switch
		createOps, err := o.client.nbClient.Create(lsp)
		if err != nil {
			return fmt.Errorf("failed to create switch port operation: %w", err)
		}
		ops = append(ops, createOps...)

		// Get the switch
		ls := &LogicalSwitch{Name: switchName}
		err = o.client.nbClient.Get(ctx, ls)
		if err != nil {
			return fmt.Errorf("failed to get switch %s: %w", switchName, err)
		}

		// Add port to switch
		mutateOps, err := o.client.nbClient.Where(ls).Mutate(ls, model.Mutation{
			Field:   &ls.Ports,
			Mutator: ovsdb.MutateOperationInsert,
			Value:   []string{lsp.UUID},
		})
		if err != nil {
			return fmt.Errorf("failed to create switch mutation: %w", err)
		}
		ops = append(ops, mutateOps...)
	}

	if len(ops) > 0 {
		results, err := o.client.nbClient.Transact(ctx, ops...)
		if err != nil {
			return fmt.Errorf("failed to transact router port operations: %w", err)
		}
		if err := checkTransactResults(results); err != nil {
			return fmt.Errorf("router port transaction failed: %w", err)
		}
		klog.Infof("Ensured router port %s -> switch %s", lrpName, switchName)
	}

	return nil
}

// AddStaticRoute adds a static route to the cluster router.
//
// Parameters:
//   - ctx: Context
//   - prefix: Destination prefix (e.g., "0.0.0.0/0" for default route)
//   - nextHop: Next hop IP address
//   - outputPort: Optional output port name
func (o *LogicalRouterOps) AddStaticRoute(ctx context.Context, prefix, nextHop string, outputPort string) error {
	router, err := o.GetLogicalRouter(ctx, ClusterRouterName)
	if err != nil {
		return fmt.Errorf("cluster router not found: %w", err)
	}

	route := &LogicalRouterStaticRoute{
		IPPrefix: prefix,
		Nexthop:  nextHop,
		ExternalIDs: map[string]string{
			"k8s.ovn.org/owner": "zstack-ovn-kubernetes",
		},
	}

	if outputPort != "" {
		route.OutputPort = &outputPort
	}

	// Create the route
	createOps, err := o.client.nbClient.Create(route)
	if err != nil {
		return fmt.Errorf("failed to create route operation: %w", err)
	}

	// Add to router
	mutateOps, err := o.client.nbClient.Where(router).Mutate(router, model.Mutation{
		Field:   &router.StaticRoutes,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{route.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create router mutation: %w", err)
	}

	ops := append(createOps, mutateOps...)
	results, err := o.client.nbClient.Transact(ctx, ops...)
	if err != nil {
		return fmt.Errorf("failed to add static route: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		return fmt.Errorf("static route addition failed: %w", err)
	}

	klog.Infof("Added static route %s -> %s to cluster router", prefix, nextHop)
	return nil
}

// AddNAT adds a NAT rule to the cluster router.
//
// Parameters:
//   - ctx: Context
//   - natType: Type of NAT ("snat", "dnat", or "dnat_and_snat")
//   - externalIP: External IP address
//   - logicalIP: Logical (internal) IP or subnet
func (o *LogicalRouterOps) AddNAT(ctx context.Context, natType, externalIP, logicalIP string) error {
	router, err := o.GetLogicalRouter(ctx, ClusterRouterName)
	if err != nil {
		return fmt.Errorf("cluster router not found: %w", err)
	}

	nat := &NAT{
		Type:       natType,
		ExternalIP: externalIP,
		LogicalIP:  logicalIP,
		ExternalIDs: map[string]string{
			"k8s.ovn.org/owner": "zstack-ovn-kubernetes",
		},
	}

	// Create the NAT
	createOps, err := o.client.nbClient.Create(nat)
	if err != nil {
		return fmt.Errorf("failed to create NAT operation: %w", err)
	}

	// Add to router
	mutateOps, err := o.client.nbClient.Where(router).Mutate(router, model.Mutation{
		Field:   &router.NAT,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{nat.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create router mutation: %w", err)
	}

	ops := append(createOps, mutateOps...)
	results, err := o.client.nbClient.Transact(ctx, ops...)
	if err != nil {
		return fmt.Errorf("failed to add NAT: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		return fmt.Errorf("NAT addition failed: %w", err)
	}

	klog.Infof("Added %s NAT: %s <-> %s", natType, externalIP, logicalIP)
	return nil
}

// DeleteRouterPort deletes a router port and its corresponding switch port.
func (o *LogicalRouterOps) DeleteRouterPort(ctx context.Context, switchName string) error {
	lrpName := RouterPortPrefix + switchName
	lspName := SwitchPortToRouterPrefix + switchName

	// Delete switch port first
	lspOps := NewLogicalSwitchPortOps(o.client)
	if err := lspOps.DeleteLogicalSwitchPort(ctx, switchName, lspName); err != nil && !IsNotFound(err) {
		klog.Warningf("Failed to delete switch port %s: %v", lspName, err)
	}

	// Delete router port
	lrp := &LogicalRouterPort{Name: lrpName}
	err := o.client.nbClient.Get(ctx, lrp)
	if err != nil {
		if err == client.ErrNotFound {
			return nil
		}
		return fmt.Errorf("failed to get router port: %w", err)
	}

	// Remove from router and delete
	router, err := o.GetLogicalRouter(ctx, ClusterRouterName)
	if err != nil {
		return fmt.Errorf("cluster router not found: %w", err)
	}

	mutateOps, err := o.client.nbClient.Where(router).Mutate(router, model.Mutation{
		Field:   &router.Ports,
		Mutator: ovsdb.MutateOperationDelete,
		Value:   []string{lrp.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create mutation: %w", err)
	}

	deleteOps, err := o.client.nbClient.Where(lrp).Delete()
	if err != nil {
		return fmt.Errorf("failed to create delete operation: %w", err)
	}

	ops := append(mutateOps, deleteOps...)
	results, err := o.client.nbClient.Transact(ctx, ops...)
	if err != nil {
		return fmt.Errorf("failed to delete router port: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		return fmt.Errorf("router port deletion failed: %w", err)
	}

	klog.Infof("Deleted router port %s", lrpName)
	return nil
}

// checkTransactResults checks transaction results for errors.
func checkTransactResults(results []ovsdb.OperationResult) error {
	for i, result := range results {
		if result.Error != "" {
			return fmt.Errorf("operation %d failed: %s - %s", i, result.Error, result.Details)
		}
	}
	return nil
}

// EnsureJoinSwitch creates the join switch that connects the cluster router to gateway chassis.
// The join switch acts as a transit network between the cluster router and external networks.
//
// Architecture:
//
//	┌─────────────────────────────────────────────────────────────────────────────┐
//	│                         ovn-cluster-router                                   │
//	│                                                                              │
//	│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐                  │
//	│  │ rtos-node1   │    │ rtos-node2   │    │ rtoj-router  │ (100.64.0.1)     │
//	│  └──────┬───────┘    └──────┬───────┘    └──────┬───────┘                  │
//	└─────────┼───────────────────┼───────────────────┼───────────────────────────┘
//	          │                   │                   │
//	          ▼                   ▼                   ▼
//	┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
//	│  node-1 Switch  │  │  node-2 Switch  │  │   join Switch   │
//	│  10.244.0.0/24  │  │  10.244.1.0/24  │  │  100.64.0.0/16  │
//	└─────────────────┘  └─────────────────┘  └────────┬────────┘
//	                                                   │
//	                                          ┌────────┴────────┐
//	                                          │ jtor-node1      │ (100.64.0.2)
//	                                          │ Gateway Chassis │
//	                                          └────────┬────────┘
//	                                                   │
//	                                          ┌────────┴────────┐
//	                                          │  Node (物理网卡) │
//	                                          │  192.168.x.x    │
//	                                          └─────────────────┘
func (o *LogicalRouterOps) EnsureJoinSwitch(ctx context.Context) error {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	// Check if join switch already exists using Get
	ls := &LogicalSwitch{Name: JoinSwitchName}
	err := nbClient.Get(ctx, ls)
	if err == nil {
		klog.V(4).Infof("Join switch %s already exists with UUID %s", JoinSwitchName, ls.UUID)
		return nil
	}
	if err != client.ErrNotFound {
		return fmt.Errorf("failed to check join switch: %w", err)
	}

	// Create join switch with named UUID
	ls = &LogicalSwitch{
		UUID: BuildNamedUUID(JoinSwitchName),
		Name: JoinSwitchName,
		OtherConfig: map[string]string{
			"subnet":      "100.64.0.0/16",
			"exclude_ips": "100.64.0.1",
		},
		ExternalIDs: map[string]string{
			"k8s.ovn.org/kind":  "join-switch",
			"k8s.ovn.org/owner": "zstack-ovn-kubernetes",
		},
	}

	ops, err := nbClient.Create(ls)
	if err != nil {
		return fmt.Errorf("failed to create join switch operation: %w", err)
	}

	results, err := nbClient.Transact(ctx, ops...)
	if err != nil {
		// Check if it's a duplicate error
		if isDuplicateError(err) {
			klog.V(4).Infof("Join switch %s was created by another reconcile", JoinSwitchName)
			return nil
		}
		return fmt.Errorf("failed to create join switch: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		if isDuplicateError(err) {
			klog.V(4).Infof("Join switch %s already exists (constraint violation)", JoinSwitchName)
			return nil
		}
		return fmt.Errorf("join switch creation failed: %w", err)
	}

	klog.Infof("Created join switch %s", JoinSwitchName)
	return nil
}

// EnsureJoinRouterPort creates the router port connecting cluster router to join switch.
// This port has IP 100.64.0.1 and serves as the gateway for the join network.
func (o *LogicalRouterOps) EnsureJoinRouterPort(ctx context.Context) error {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	lrpName := JoinRouterPortName
	lspName := JoinSwitchPortPrefix + "GR"

	// Check if router port already exists
	lrp := &LogicalRouterPort{Name: lrpName}
	err := nbClient.Get(ctx, lrp)
	if err == nil {
		klog.V(4).Infof("Join router port %s already exists", lrpName)
		return nil
	}
	if err != client.ErrNotFound {
		return fmt.Errorf("failed to check join router port: %w", err)
	}

	// Get the cluster router - use WhereCache as fallback if Get fails due to cache sync
	router := &LogicalRouter{Name: ClusterRouterName}
	err = nbClient.Get(ctx, router)
	if err != nil {
		if err == client.ErrNotFound {
			// Try WhereCache as fallback
			var routers []*LogicalRouter
			err = nbClient.WhereCache(func(lr *LogicalRouter) bool {
				return lr.Name == ClusterRouterName
			}).List(ctx, &routers)
			if err != nil || len(routers) == 0 {
				return fmt.Errorf("cluster router not found: %w", err)
			}
			router = routers[0]
		} else {
			return fmt.Errorf("failed to get cluster router: %w", err)
		}
	}

	// Create router port
	lrp = &LogicalRouterPort{
		Name:     lrpName,
		MAC:      "0a:58:64:40:00:01", // 100.64.0.1 encoded
		Networks: []string{"100.64.0.1/16"},
		ExternalIDs: map[string]string{
			"k8s.ovn.org/kind": "join-router-port",
		},
	}

	var ops []ovsdb.Operation

	createOps, err := nbClient.Create(lrp)
	if err != nil {
		return fmt.Errorf("failed to create router port operation: %w", err)
	}
	ops = append(ops, createOps...)

	// Add port to router
	mutateOps, err := nbClient.Where(router).Mutate(router, model.Mutation{
		Field:   &router.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{lrp.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create router mutation: %w", err)
	}
	ops = append(ops, mutateOps...)

	// Create corresponding switch port on join switch
	lsp := &LogicalSwitchPort{
		Name: lspName,
		Type: "router",
		Options: map[string]string{
			"router-port": lrpName,
		},
		Addresses: []string{"router"},
		ExternalIDs: map[string]string{
			"k8s.ovn.org/router-port": lrpName,
		},
	}

	createLspOps, err := nbClient.Create(lsp)
	if err != nil {
		return fmt.Errorf("failed to create switch port operation: %w", err)
	}
	ops = append(ops, createLspOps...)

	// Add port to join switch
	ls := &LogicalSwitch{Name: JoinSwitchName}
	err = nbClient.Get(ctx, ls)
	if err != nil {
		return fmt.Errorf("failed to get join switch: %w", err)
	}

	mutateLsOps, err := nbClient.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{lsp.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create switch mutation: %w", err)
	}
	ops = append(ops, mutateLsOps...)

	results, err := nbClient.Transact(ctx, ops...)
	if err != nil {
		return fmt.Errorf("failed to create join router port: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		return fmt.Errorf("join router port creation failed: %w", err)
	}

	klog.Infof("Created join router port %s", lrpName)
	return nil
}

// EnsureGatewayChassisPort creates a gateway chassis port on the join switch for a node.
// This port is bound to the node's chassis and provides the external gateway functionality.
//
// Parameters:
//   - ctx: Context
//   - nodeName: Name of the node
//   - chassisName: OVN chassis name for the node
//   - gatewayIP: Gateway IP on join network (e.g., "100.64.0.2")
//   - nodeIP: Node's physical IP for SNAT
func (o *LogicalRouterOps) EnsureGatewayChassisPort(
	ctx context.Context,
	nodeName string,
	chassisName string,
	gatewayIP string,
	nodeIP string,
) error {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	lspName := JoinSwitchPortPrefix + nodeName
	mac := generateMACFromIP(gatewayIP)

	// Check if port already exists
	existingLSP := &LogicalSwitchPort{Name: lspName}
	err := nbClient.Get(ctx, existingLSP)
	if err == nil {
		klog.V(4).Infof("Gateway chassis port %s already exists", lspName)
		// Update chassis binding if needed
		if existingLSP.Options == nil || existingLSP.Options[OptionRequestedChassis] != chassisName {
			existingLSP.Options = map[string]string{
				OptionRequestedChassis: chassisName,
			}
			updateOps, err := nbClient.Where(existingLSP).Update(existingLSP, &existingLSP.Options)
			if err != nil {
				return fmt.Errorf("failed to update gateway port options: %w", err)
			}
			results, err := nbClient.Transact(ctx, updateOps...)
			if err != nil {
				return fmt.Errorf("failed to update gateway port: %w", err)
			}
			if err := checkTransactResults(results); err != nil {
				return fmt.Errorf("gateway port update failed: %w", err)
			}
		}
		return nil
	}
	if err != client.ErrNotFound {
		return fmt.Errorf("failed to check gateway port: %w", err)
	}

	// Create gateway chassis port
	lsp := &LogicalSwitchPort{
		Name:      lspName,
		Type:      "localport", // localport type for gateway chassis
		Addresses: []string{fmt.Sprintf("%s %s", mac, gatewayIP)},
		Options: map[string]string{
			OptionRequestedChassis: chassisName,
		},
		ExternalIDs: map[string]string{
			"k8s.ovn.org/kind":    "gateway-chassis-port",
			"k8s.ovn.org/node":    nodeName,
			"k8s.ovn.org/node-ip": nodeIP,
		},
	}

	var ops []ovsdb.Operation

	createOps, err := nbClient.Create(lsp)
	if err != nil {
		return fmt.Errorf("failed to create gateway port operation: %w", err)
	}
	ops = append(ops, createOps...)

	// Add port to join switch
	ls := &LogicalSwitch{Name: JoinSwitchName}
	err = nbClient.Get(ctx, ls)
	if err != nil {
		return fmt.Errorf("failed to get join switch: %w", err)
	}

	mutateOps, err := nbClient.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{lsp.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create switch mutation: %w", err)
	}
	ops = append(ops, mutateOps...)

	results, err := nbClient.Transact(ctx, ops...)
	if err != nil {
		return fmt.Errorf("failed to create gateway chassis port: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		return fmt.Errorf("gateway chassis port creation failed: %w", err)
	}

	klog.Infof("Created gateway chassis port %s for node %s", lspName, nodeName)
	return nil
}

// EnsureDefaultRoute adds a default route (0.0.0.0/0) to the cluster router
// pointing to the gateway chassis on the join network.
func (o *LogicalRouterOps) EnsureDefaultRoute(ctx context.Context, nextHop string) error {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	router, err := o.GetLogicalRouter(ctx, ClusterRouterName)
	if err != nil {
		return fmt.Errorf("cluster router not found: %w", err)
	}

	// Check if default route already exists
	var routes []*LogicalRouterStaticRoute
	err = nbClient.WhereCache(func(r *LogicalRouterStaticRoute) bool {
		return r.IPPrefix == "0.0.0.0/0"
	}).List(ctx, &routes)
	if err != nil {
		return fmt.Errorf("failed to list routes: %w", err)
	}

	for _, route := range routes {
		// Check if this route belongs to our router
		for _, routeUUID := range router.StaticRoutes {
			if routeUUID == route.UUID {
				if route.Nexthop == nextHop {
					klog.V(4).Infof("Default route already exists with nexthop %s", nextHop)
					return nil
				}
				// Update existing route
				route.Nexthop = nextHop
				updateOps, err := nbClient.Where(route).Update(route, &route.Nexthop)
				if err != nil {
					return fmt.Errorf("failed to update route: %w", err)
				}
				results, err := nbClient.Transact(ctx, updateOps...)
				if err != nil {
					return fmt.Errorf("failed to update default route: %w", err)
				}
				if err := checkTransactResults(results); err != nil {
					return fmt.Errorf("default route update failed: %w", err)
				}
				klog.Infof("Updated default route nexthop to %s", nextHop)
				return nil
			}
		}
	}

	// Create new default route
	route := &LogicalRouterStaticRoute{
		IPPrefix: "0.0.0.0/0",
		Nexthop:  nextHop,
		ExternalIDs: map[string]string{
			"k8s.ovn.org/owner": "zstack-ovn-kubernetes",
			"k8s.ovn.org/kind":  "default-route",
		},
	}

	createOps, err := nbClient.Create(route)
	if err != nil {
		return fmt.Errorf("failed to create route operation: %w", err)
	}

	mutateOps, err := nbClient.Where(router).Mutate(router, model.Mutation{
		Field:   &router.StaticRoutes,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{route.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create router mutation: %w", err)
	}

	ops := append(createOps, mutateOps...)
	results, err := nbClient.Transact(ctx, ops...)
	if err != nil {
		return fmt.Errorf("failed to add default route: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		return fmt.Errorf("default route addition failed: %w", err)
	}

	klog.Infof("Added default route 0.0.0.0/0 -> %s", nextHop)
	return nil
}

// EnsureSNATForCluster adds a SNAT rule for the entire cluster CIDR.
// This allows all pods to access external networks using the gateway node's IP.
func (o *LogicalRouterOps) EnsureSNATForCluster(ctx context.Context, externalIP, clusterCIDR string) error {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	router, err := o.GetLogicalRouter(ctx, ClusterRouterName)
	if err != nil {
		return fmt.Errorf("cluster router not found: %w", err)
	}

	// Check if SNAT rule already exists
	var nats []*NAT
	err = nbClient.WhereCache(func(n *NAT) bool {
		return n.Type == NATTypeSNAT && n.LogicalIP == clusterCIDR
	}).List(ctx, &nats)
	if err != nil {
		return fmt.Errorf("failed to list NAT rules: %w", err)
	}

	for _, nat := range nats {
		for _, natUUID := range router.NAT {
			if natUUID == nat.UUID {
				if nat.ExternalIP == externalIP {
					klog.V(4).Infof("SNAT rule already exists for %s -> %s", clusterCIDR, externalIP)
					return nil
				}
				// Update existing NAT
				nat.ExternalIP = externalIP
				updateOps, err := nbClient.Where(nat).Update(nat, &nat.ExternalIP)
				if err != nil {
					return fmt.Errorf("failed to update NAT: %w", err)
				}
				results, err := nbClient.Transact(ctx, updateOps...)
				if err != nil {
					return fmt.Errorf("failed to update SNAT: %w", err)
				}
				if err := checkTransactResults(results); err != nil {
					return fmt.Errorf("SNAT update failed: %w", err)
				}
				klog.Infof("Updated SNAT rule: %s -> %s", clusterCIDR, externalIP)
				return nil
			}
		}
	}

	// Create new SNAT rule
	nat := &NAT{
		Type:       NATTypeSNAT,
		ExternalIP: externalIP,
		LogicalIP:  clusterCIDR,
		ExternalIDs: map[string]string{
			"k8s.ovn.org/owner": "zstack-ovn-kubernetes",
			"k8s.ovn.org/kind":  "cluster-snat",
		},
	}

	createOps, err := nbClient.Create(nat)
	if err != nil {
		return fmt.Errorf("failed to create NAT operation: %w", err)
	}

	mutateOps, err := nbClient.Where(router).Mutate(router, model.Mutation{
		Field:   &router.NAT,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{nat.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create router mutation: %w", err)
	}

	ops := append(createOps, mutateOps...)
	results, err := nbClient.Transact(ctx, ops...)
	if err != nil {
		return fmt.Errorf("failed to add SNAT: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		return fmt.Errorf("SNAT addition failed: %w", err)
	}

	klog.Infof("Added cluster SNAT rule: %s -> %s", clusterCIDR, externalIP)
	return nil
}

// generateMACFromIP generates a MAC address from an IP address.
// Format: 0a:58:xx:xx:xx:xx where xx:xx:xx:xx is derived from the IP
func generateMACFromIP(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "0a:58:00:00:00:01"
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "0a:58:00:00:00:01"
	}
	return fmt.Sprintf("0a:58:%02x:%02x:%02x:%02x", ip4[0], ip4[1], ip4[2], ip4[3])
}
