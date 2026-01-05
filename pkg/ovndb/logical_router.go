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
//
// This function handles duplicate creation by:
// 1. First checking the cache via Get() and WhereCache() as fallback
// 2. Using a Wait operation as a guard to prevent duplicate creation at database level
// 3. Handling constraint violation errors gracefully
func (o *LogicalRouterOps) CreateOrUpdateLogicalRouter(ctx context.Context, router *LogicalRouter) error {
	nbClient := o.client.nbClient

	// First, try to get the existing router by name from cache
	existing := &LogicalRouter{Name: router.Name}
	err := nbClient.Get(ctx, existing)

	if err == nil {
		// Router exists, update it if needed
		router.UUID = existing.UUID
		klog.V(4).Infof("Logical router %s already exists with UUID %s", router.Name, existing.UUID)
		return nil
	}

	if err != client.ErrNotFound {
		return fmt.Errorf("failed to check existing router: %w", err)
	}

	// Cache miss - also try WhereCache as fallback (handles cache sync delays)
	var routers []*LogicalRouter
	err = nbClient.WhereCache(func(lr *LogicalRouter) bool {
		return lr.Name == router.Name
	}).List(ctx, &routers)
	if err == nil && len(routers) > 0 {
		router.UUID = routers[0].UUID
		klog.V(4).Infof("Logical router %s found via WhereCache with UUID %s", router.Name, routers[0].UUID)
		return nil
	}

	// Router doesn't exist, create it with a named UUID
	router.UUID = BuildNamedUUID(router.Name)

	// Build a guard operation (Wait) to prevent duplicate creation
	// This is the same pattern used by ovn-kubernetes
	// See: https://bugzilla.redhat.com/show_bug.cgi?id=2042001
	var ops []ovsdb.Operation

	guardOps, err := o.buildFailOnDuplicateOps(router)
	if err != nil {
		klog.V(4).Infof("Failed to build guard ops for router %s: %v, proceeding without guard", router.Name, err)
	} else if len(guardOps) > 0 {
		ops = append(ops, guardOps...)
	}

	createOps, err := nbClient.Create(router)
	if err != nil {
		return fmt.Errorf("failed to create router operation: %w", err)
	}
	ops = append(ops, createOps...)

	results, err := nbClient.Transact(ctx, ops...)
	if err != nil {
		// Check if it's a duplicate error (another reconcile created it) or wait timeout
		if isDuplicateError(err) || isWaitTimeoutError(err) {
			klog.V(4).Infof("Router %s was created by another reconcile, this is expected", router.Name)
			// Try to get the existing router's UUID
			if existing, getErr := o.GetLogicalRouter(ctx, router.Name); getErr == nil {
				router.UUID = existing.UUID
			}
			return nil
		}
		return fmt.Errorf("failed to create router: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		// Check if it's a constraint violation (duplicate) or wait condition failed
		if isDuplicateError(err) || isWaitTimeoutError(err) {
			klog.V(4).Infof("Router %s already exists (constraint violation), this is expected", router.Name)
			// Try to get the existing router's UUID
			if existing, getErr := o.GetLogicalRouter(ctx, router.Name); getErr == nil {
				router.UUID = existing.UUID
			}
			return nil
		}
		return fmt.Errorf("router creation failed: %w", err)
	}

	// Set the real UUID from the transaction result
	// Skip the guard op result if present
	resultIdx := 0
	if len(guardOps) > 0 {
		resultIdx = len(guardOps)
	}
	if len(results) > resultIdx && results[resultIdx].UUID.GoUUID != "" {
		router.UUID = results[resultIdx].UUID.GoUUID
	}

	klog.Infof("Created logical router %s with UUID %s", router.Name, router.UUID)
	return nil
}

// buildFailOnDuplicateOps builds a Wait operation that fails if a duplicate router exists.
// This is the same pattern used by ovn-kubernetes to prevent duplicate creation.
// See: https://bugzilla.redhat.com/show_bug.cgi?id=2042001
func (o *LogicalRouterOps) buildFailOnDuplicateOps(router *LogicalRouter) ([]ovsdb.Operation, error) {
	if router.Name == "" {
		return nil, nil
	}

	timeout := 0 // 0 means check immediately, don't wait
	cond := model.Condition{
		Field:    &router.Name,
		Function: ovsdb.ConditionEqual,
		Value:    router.Name,
	}

	// Wait for condition: Name != router.Name (i.e., no router with this name exists)
	// If a router with this name already exists, the Wait will fail immediately
	return o.client.nbClient.WhereAny(router, cond).Wait(
		ovsdb.WaitConditionNotEqual,
		&timeout,
		router,
		&router.Name,
	)
}

// isWaitTimeoutError checks if an error is a Wait operation timeout/condition failure
func isWaitTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "timed out") ||
		strings.Contains(errStr, "wait condition") ||
		strings.Contains(errStr, "timeout")
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
// Uses WhereCache as a fallback if Get() fails due to cache sync issues.
func (o *LogicalRouterOps) GetLogicalRouter(ctx context.Context, name string) (*LogicalRouter, error) {
	nbClient := o.client.nbClient

	// Try Get first (uses index lookup)
	router := &LogicalRouter{Name: name}
	err := nbClient.Get(ctx, router)
	if err == nil {
		return router, nil
	}

	if err != client.ErrNotFound {
		return nil, fmt.Errorf("failed to get logical router %s: %w", name, err)
	}

	// Cache miss - try WhereCache as fallback (handles cache sync delays)
	var routers []*LogicalRouter
	err = nbClient.WhereCache(func(lr *LogicalRouter) bool {
		return lr.Name == name
	}).List(ctx, &routers)
	if err != nil {
		return nil, fmt.Errorf("failed to query logical router %s: %w", name, err)
	}
	if len(routers) > 0 {
		klog.V(4).Infof("Found router %s via WhereCache (UUID: %s)", name, routers[0].UUID)
		return routers[0], nil
	}

	return nil, &ObjectNotFoundError{ObjectType: "LogicalRouter", ObjectName: name}
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

		// Get the switch - use WhereCache as fallback if Get fails due to cache sync
		ls := &LogicalSwitch{Name: switchName}
		err = o.client.nbClient.Get(ctx, ls)
		if err != nil {
			if err == client.ErrNotFound {
				// Try WhereCache as fallback for cache sync issues
				var switches []*LogicalSwitch
				err = o.client.nbClient.WhereCache(func(s *LogicalSwitch) bool {
					return s.Name == switchName
				}).List(ctx, &switches)
				if err != nil || len(switches) == 0 {
					return fmt.Errorf("failed to get switch %s: object not found (cache may not be synced yet)", switchName)
				}
				ls = switches[0]
				klog.V(4).Infof("Found switch %s via WhereCache (UUID: %s)", switchName, ls.UUID)
			} else {
				return fmt.Errorf("failed to get switch %s: %w", switchName, err)
			}
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

// EnsureJoinSwitchAndRouterPort creates the join switch and router port in a single transaction.
// This avoids cache sync issues by creating all objects in one atomic operation.
// Returns the join switch for use by other functions that need to add ports to it.
func (o *LogicalRouterOps) EnsureJoinSwitchAndRouterPort(ctx context.Context) (*LogicalSwitch, error) {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	lrpName := JoinRouterPortName
	lspName := JoinSwitchPortPrefix + "GR"

	// Check if join switch already exists
	ls := &LogicalSwitch{Name: JoinSwitchName}
	err := nbClient.Get(ctx, ls)
	if err == nil {
		klog.V(4).Infof("Join switch %s already exists with UUID %s", JoinSwitchName, ls.UUID)
		// Switch exists, check if router port exists
		lrp := &LogicalRouterPort{Name: lrpName}
		if err := nbClient.Get(ctx, lrp); err == nil {
			klog.V(4).Infof("Join router port %s already exists", lrpName)
			return ls, nil
		}
		// Router port doesn't exist, create it
		return ls, o.createJoinRouterPortOnly(ctx, ls)
	}
	if err != client.ErrNotFound {
		return nil, fmt.Errorf("failed to check join switch: %w", err)
	}

	// Get the cluster router first
	router := &LogicalRouter{Name: ClusterRouterName}
	err = nbClient.Get(ctx, router)
	if err != nil {
		if err == client.ErrNotFound {
			var routers []*LogicalRouter
			err = nbClient.WhereCache(func(lr *LogicalRouter) bool {
				return lr.Name == ClusterRouterName
			}).List(ctx, &routers)
			if err != nil || len(routers) == 0 {
				return nil, fmt.Errorf("cluster router not found: %w", err)
			}
			router = routers[0]
		} else {
			return nil, fmt.Errorf("failed to get cluster router: %w", err)
		}
	}

	// Build all operations in a single transaction
	var ops []ovsdb.Operation

	// 1. Create join switch with named UUID
	ls = &LogicalSwitch{
		UUID:        BuildNamedUUID(JoinSwitchName),
		Name:        JoinSwitchName,
		OtherConfig: map[string]string{"subnet": "100.64.0.0/16", "exclude_ips": "100.64.0.1"},
		ExternalIDs: map[string]string{"k8s.ovn.org/kind": "join-switch", "k8s.ovn.org/owner": "zstack-ovn-kubernetes"},
	}
	createSwitchOps, err := nbClient.Create(ls)
	if err != nil {
		return nil, fmt.Errorf("failed to create join switch operation: %w", err)
	}
	ops = append(ops, createSwitchOps...)

	// 2. Create router port with named UUID
	lrp := &LogicalRouterPort{
		UUID:        BuildNamedUUID(lrpName),
		Name:        lrpName,
		MAC:         "0a:58:64:40:00:01",
		Networks:    []string{"100.64.0.1/16"},
		ExternalIDs: map[string]string{"k8s.ovn.org/kind": "join-router-port"},
	}
	createLrpOps, err := nbClient.Create(lrp)
	if err != nil {
		return nil, fmt.Errorf("failed to create router port operation: %w", err)
	}
	ops = append(ops, createLrpOps...)

	// 3. Add router port to cluster router
	mutateRouterOps, err := nbClient.Where(router).Mutate(router, model.Mutation{
		Field:   &router.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{lrp.UUID},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create router mutation: %w", err)
	}
	ops = append(ops, mutateRouterOps...)

	// 4. Create switch port on join switch with named UUID
	lsp := &LogicalSwitchPort{
		UUID:        BuildNamedUUID(lspName),
		Name:        lspName,
		Type:        "router",
		Options:     map[string]string{"router-port": lrpName},
		Addresses:   []string{"router"},
		ExternalIDs: map[string]string{"k8s.ovn.org/router-port": lrpName},
	}
	createLspOps, err := nbClient.Create(lsp)
	if err != nil {
		return nil, fmt.Errorf("failed to create switch port operation: %w", err)
	}
	ops = append(ops, createLspOps...)

	// 5. Add switch port to join switch using named UUID
	mutateSwitchOps, err := nbClient.Where(&LogicalSwitch{UUID: ls.UUID}).Mutate(ls, model.Mutation{
		Field:   &ls.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{lsp.UUID},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create switch mutation: %w", err)
	}
	ops = append(ops, mutateSwitchOps...)

	// Execute all operations in a single transaction
	results, err := nbClient.Transact(ctx, ops...)
	if err != nil {
		if isDuplicateError(err) {
			klog.V(4).Infof("Join switch/router port was created by another reconcile")
			existing := &LogicalSwitch{Name: JoinSwitchName}
			if getErr := nbClient.Get(ctx, existing); getErr == nil {
				return existing, nil
			}
			return ls, nil
		}
		return nil, fmt.Errorf("failed to create join network: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		if isDuplicateError(err) {
			klog.V(4).Infof("Join switch/router port already exists (constraint violation)")
			existing := &LogicalSwitch{Name: JoinSwitchName}
			if getErr := nbClient.Get(ctx, existing); getErr == nil {
				return existing, nil
			}
			return ls, nil
		}
		return nil, fmt.Errorf("join network creation failed: %w", err)
	}

	if len(results) > 0 && results[0].UUID.GoUUID != "" {
		ls.UUID = results[0].UUID.GoUUID
	}

	klog.Infof("Created join switch %s and router port %s in single transaction", JoinSwitchName, lrpName)
	return ls, nil
}

// createJoinRouterPortOnly creates only the router port when the switch already exists
func (o *LogicalRouterOps) createJoinRouterPortOnly(ctx context.Context, joinSwitch *LogicalSwitch) error {
	nbClient := o.client.NBClient()
	lrpName := JoinRouterPortName
	lspName := JoinSwitchPortPrefix + "GR"

	// Get the cluster router
	router := &LogicalRouter{Name: ClusterRouterName}
	err := nbClient.Get(ctx, router)
	if err != nil {
		if err == client.ErrNotFound {
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

	var ops []ovsdb.Operation

	// Create router port
	lrp := &LogicalRouterPort{
		UUID:     BuildNamedUUID(lrpName),
		Name:     lrpName,
		MAC:      "0a:58:64:40:00:01",
		Networks: []string{"100.64.0.1/16"},
		ExternalIDs: map[string]string{
			"k8s.ovn.org/kind": "join-router-port",
		},
	}
	createLrpOps, err := nbClient.Create(lrp)
	if err != nil {
		return fmt.Errorf("failed to create router port operation: %w", err)
	}
	ops = append(ops, createLrpOps...)

	// Add to router
	mutateRouterOps, err := nbClient.Where(router).Mutate(router, model.Mutation{
		Field:   &router.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{lrp.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create router mutation: %w", err)
	}
	ops = append(ops, mutateRouterOps...)

	// Create switch port
	lsp := &LogicalSwitchPort{
		UUID: BuildNamedUUID(lspName),
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

	// Add to switch - joinSwitch already has a real UUID
	mutateSwitchOps, err := nbClient.Where(joinSwitch).Mutate(joinSwitch, model.Mutation{
		Field:   &joinSwitch.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{lsp.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create switch mutation: %w", err)
	}
	ops = append(ops, mutateSwitchOps...)

	results, err := nbClient.Transact(ctx, ops...)
	if err != nil {
		if isDuplicateError(err) {
			klog.V(4).Infof("Join router port was created by another reconcile")
			return nil
		}
		return fmt.Errorf("failed to create join router port: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		if isDuplicateError(err) {
			return nil
		}
		return fmt.Errorf("join router port creation failed: %w", err)
	}

	klog.Infof("Created join router port %s", lrpName)
	return nil
}

// EnsureJoinSwitch is kept for backward compatibility but now delegates to EnsureJoinSwitchAndRouterPort
// Deprecated: Use EnsureJoinSwitchAndRouterPort instead
func (o *LogicalRouterOps) EnsureJoinSwitch(ctx context.Context) (*LogicalSwitch, error) {
	return o.EnsureJoinSwitchAndRouterPort(ctx)
}

// EnsureJoinRouterPort is kept for backward compatibility
// Deprecated: Use EnsureJoinSwitchAndRouterPort instead
func (o *LogicalRouterOps) EnsureJoinRouterPort(ctx context.Context, joinSwitch *LogicalSwitch) error {
	// Check if router port already exists
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	lrp := &LogicalRouterPort{Name: JoinRouterPortName}
	err := nbClient.Get(ctx, lrp)
	if err == nil {
		klog.V(4).Infof("Join router port %s already exists", JoinRouterPortName)
		return nil
	}
	if err != client.ErrNotFound {
		return fmt.Errorf("failed to check join router port: %w", err)
	}

	return o.createJoinRouterPortOnly(ctx, joinSwitch)
}

// EnsureGatewayChassisPort creates a gateway chassis port on the join switch for a node.
// This port enables external traffic routing through the node.
//
// For distributed gateway mode, we use a "router" type port that connects to
// a logical router port, enabling proper L3 routing to external networks.
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
		return nil
	}
	if err != client.ErrNotFound {
		return fmt.Errorf("failed to check gateway port: %w", err)
	}

	// Create gateway port on join switch
	// Use empty type (default) with static addresses for gateway functionality
	// This allows the port to participate in L3 routing
	lsp := &LogicalSwitchPort{
		Name:      lspName,
		Type:      "", // Default type for regular L2/L3 port
		Addresses: []string{fmt.Sprintf("%s %s", mac, gatewayIP)},
		ExternalIDs: map[string]string{
			"k8s.ovn.org/kind":    "gateway-port",
			"k8s.ovn.org/node":    nodeName,
			"k8s.ovn.org/node-ip": nodeIP,
			"k8s.ovn.org/chassis": chassisName,
		},
	}

	var ops []ovsdb.Operation

	createOps, err := nbClient.Create(lsp)
	if err != nil {
		return fmt.Errorf("failed to create gateway port operation: %w", err)
	}
	ops = append(ops, createOps...)

	// Get join switch - use WhereCache as fallback if Get fails due to cache sync
	ls := &LogicalSwitch{Name: JoinSwitchName}
	err = nbClient.Get(ctx, ls)
	if err != nil {
		if err == client.ErrNotFound {
			// Try WhereCache as fallback for cache sync issues
			var switches []*LogicalSwitch
			err = nbClient.WhereCache(func(s *LogicalSwitch) bool {
				return s.Name == JoinSwitchName
			}).List(ctx, &switches)
			if err != nil || len(switches) == 0 {
				return fmt.Errorf("failed to get join switch: object not found (cache may not be synced yet)")
			}
			ls = switches[0]
			klog.V(4).Infof("Found join switch %s via WhereCache (UUID: %s)", JoinSwitchName, ls.UUID)
		} else {
			return fmt.Errorf("failed to get join switch: %w", err)
		}
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

	klog.Infof("Created gateway port %s for node %s with IP %s", lspName, nodeName, gatewayIP)
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


// ============================================================================
// External Network Connectivity
// ============================================================================

// ExternalSwitchName returns the external switch name for a node
func GetExternalSwitchName(nodeName string) string {
	return ExternalSwitchName + nodeName
}

// LocalnetPortName returns the localnet port name for a node
func GetLocalnetPortName(nodeName string) string {
	return "ln-" + nodeName
}

// EnsureExternalSwitch creates an external switch for connecting to the physical network.
// This switch has a localnet port that bridges OVN to the physical network.
//
// Parameters:
//   - ctx: Context
//   - nodeName: Name of the node
//   - physicalNetwork: Name of the physical network (provider network)
//   - vlanID: Optional VLAN ID for tagged traffic (0 for untagged)
func (o *LogicalRouterOps) EnsureExternalSwitch(
	ctx context.Context,
	nodeName string,
	physicalNetwork string,
	vlanID int,
) (*LogicalSwitch, error) {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return nil, fmt.Errorf("NB client is not connected")
	}

	extSwitchName := GetExternalSwitchName(nodeName)
	localnetPortName := GetLocalnetPortName(nodeName)

	// Check if external switch already exists
	ls := &LogicalSwitch{Name: extSwitchName}
	err := nbClient.Get(ctx, ls)
	if err == nil {
		klog.V(4).Infof("External switch %s already exists", extSwitchName)
		return ls, nil
	}
	if err != client.ErrNotFound {
		return nil, fmt.Errorf("failed to check external switch: %w", err)
	}

	// Create external switch
	ls = &LogicalSwitch{
		UUID: BuildNamedUUID(extSwitchName),
		Name: extSwitchName,
		ExternalIDs: map[string]string{
			"k8s.ovn.org/kind": "external-switch",
			"k8s.ovn.org/node": nodeName,
		},
	}

	var ops []ovsdb.Operation

	createSwitchOps, err := nbClient.Create(ls)
	if err != nil {
		return nil, fmt.Errorf("failed to create external switch operation: %w", err)
	}
	ops = append(ops, createSwitchOps...)

	// Create localnet port to connect to physical network
	localnetPort := &LogicalSwitchPort{
		UUID: BuildNamedUUID(localnetPortName),
		Name: localnetPortName,
		Type: "localnet",
		Options: map[string]string{
			"network_name": physicalNetwork,
		},
		Addresses: []string{"unknown"},
		ExternalIDs: map[string]string{
			"k8s.ovn.org/kind": "localnet-port",
			"k8s.ovn.org/node": nodeName,
		},
	}

	// Set VLAN tag if specified
	if vlanID > 0 {
		localnetPort.TagRequest = &vlanID
	}

	createPortOps, err := nbClient.Create(localnetPort)
	if err != nil {
		return nil, fmt.Errorf("failed to create localnet port operation: %w", err)
	}
	ops = append(ops, createPortOps...)

	// Add localnet port to external switch
	mutateSwitchOps, err := nbClient.Where(&LogicalSwitch{UUID: ls.UUID}).Mutate(ls, model.Mutation{
		Field:   &ls.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{localnetPort.UUID},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create switch mutation: %w", err)
	}
	ops = append(ops, mutateSwitchOps...)

	// Execute transaction
	results, err := nbClient.Transact(ctx, ops...)
	if err != nil {
		if isDuplicateError(err) {
			klog.V(4).Infof("External switch %s was created by another reconcile", extSwitchName)
			existing := &LogicalSwitch{Name: extSwitchName}
			if getErr := nbClient.Get(ctx, existing); getErr == nil {
				return existing, nil
			}
			return ls, nil
		}
		return nil, fmt.Errorf("failed to create external switch: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		if isDuplicateError(err) {
			klog.V(4).Infof("External switch %s already exists (constraint violation)", extSwitchName)
			existing := &LogicalSwitch{Name: extSwitchName}
			if getErr := nbClient.Get(ctx, existing); getErr == nil {
				return existing, nil
			}
			return ls, nil
		}
		return nil, fmt.Errorf("external switch creation failed: %w", err)
	}

	if len(results) > 0 && results[0].UUID.GoUUID != "" {
		ls.UUID = results[0].UUID.GoUUID
	}

	klog.Infof("Created external switch %s with localnet port %s for physical network %s",
		extSwitchName, localnetPortName, physicalNetwork)
	return ls, nil
}

// EnsureGatewayRouterToExternal creates the connection between cluster router and external switch.
// This enables traffic to flow from the cluster router to the physical network.
//
// Parameters:
//   - ctx: Context
//   - nodeName: Name of the node
//   - gatewayIP: Gateway IP on the external network (node's physical IP)
//   - gatewayMAC: MAC address for the gateway port
//   - nextHop: Next hop IP for default route (external gateway)
func (o *LogicalRouterOps) EnsureGatewayRouterToExternal(
	ctx context.Context,
	nodeName string,
	gatewayIP string,
	gatewayMAC string,
	nextHop string,
) error {
	nbClient := o.client.NBClient()
	if nbClient == nil {
		return fmt.Errorf("NB client is not connected")
	}

	extSwitchName := GetExternalSwitchName(nodeName)
	lrpName := GatewayRouterPortPrefix + nodeName
	lspName := "etor-" + nodeName

	// Get cluster router
	router, err := o.GetLogicalRouter(ctx, ClusterRouterName)
	if err != nil {
		return fmt.Errorf("cluster router not found: %w", err)
	}

	// Check if router port already exists
	existingLRP := &LogicalRouterPort{Name: lrpName}
	err = nbClient.Get(ctx, existingLRP)
	if err == nil {
		klog.V(4).Infof("Gateway router port %s already exists", lrpName)
		return nil
	}
	if err != client.ErrNotFound {
		return fmt.Errorf("failed to check gateway router port: %w", err)
	}

	// Parse gateway IP to get network
	ip := net.ParseIP(gatewayIP)
	if ip == nil {
		return fmt.Errorf("invalid gateway IP: %s", gatewayIP)
	}

	// Assume /24 network for simplicity
	// In production, this should be configurable
	network := fmt.Sprintf("%s/24", gatewayIP)

	var ops []ovsdb.Operation

	// Create router port to external switch
	lrp := &LogicalRouterPort{
		UUID:     BuildNamedUUID(lrpName),
		Name:     lrpName,
		MAC:      gatewayMAC,
		Networks: []string{network},
		ExternalIDs: map[string]string{
			"k8s.ovn.org/kind": "gateway-router-port",
			"k8s.ovn.org/node": nodeName,
		},
	}

	createLrpOps, err := nbClient.Create(lrp)
	if err != nil {
		return fmt.Errorf("failed to create router port operation: %w", err)
	}
	ops = append(ops, createLrpOps...)

	// Add router port to cluster router
	mutateRouterOps, err := nbClient.Where(router).Mutate(router, model.Mutation{
		Field:   &router.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{lrp.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create router mutation: %w", err)
	}
	ops = append(ops, mutateRouterOps...)

	// Create switch port on external switch connecting to router
	lsp := &LogicalSwitchPort{
		UUID: BuildNamedUUID(lspName),
		Name: lspName,
		Type: "router",
		Options: map[string]string{
			"router-port": lrpName,
		},
		Addresses: []string{"router"},
		ExternalIDs: map[string]string{
			"k8s.ovn.org/router-port": lrpName,
			"k8s.ovn.org/node":        nodeName,
		},
	}

	createLspOps, err := nbClient.Create(lsp)
	if err != nil {
		return fmt.Errorf("failed to create switch port operation: %w", err)
	}
	ops = append(ops, createLspOps...)

	// Get external switch
	extSwitch := &LogicalSwitch{Name: extSwitchName}
	err = nbClient.Get(ctx, extSwitch)
	if err != nil {
		return fmt.Errorf("failed to get external switch %s: %w", extSwitchName, err)
	}

	// Add switch port to external switch
	mutateSwitchOps, err := nbClient.Where(extSwitch).Mutate(extSwitch, model.Mutation{
		Field:   &extSwitch.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{lsp.UUID},
	})
	if err != nil {
		return fmt.Errorf("failed to create switch mutation: %w", err)
	}
	ops = append(ops, mutateSwitchOps...)

	// Execute transaction
	results, err := nbClient.Transact(ctx, ops...)
	if err != nil {
		if isDuplicateError(err) {
			klog.V(4).Infof("Gateway router port %s was created by another reconcile", lrpName)
			return nil
		}
		return fmt.Errorf("failed to create gateway router to external: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		if isDuplicateError(err) {
			return nil
		}
		return fmt.Errorf("gateway router to external creation failed: %w", err)
	}

	klog.Infof("Created gateway router port %s connecting to external switch %s", lrpName, extSwitchName)

	// Add default route via external gateway
	if nextHop != "" {
		if err := o.EnsureDefaultRouteViaExternal(ctx, nextHop, lrpName); err != nil {
			return fmt.Errorf("failed to add default route: %w", err)
		}
	}

	return nil
}

// EnsureDefaultRouteViaExternal adds a default route via the external gateway port.
func (o *LogicalRouterOps) EnsureDefaultRouteViaExternal(ctx context.Context, nextHop, outputPort string) error {
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
		for _, routeUUID := range router.StaticRoutes {
			if routeUUID == route.UUID {
				if route.Nexthop == nextHop {
					klog.V(4).Infof("Default route already exists with nexthop %s", nextHop)
					return nil
				}
				// Update existing route
				route.Nexthop = nextHop
				route.OutputPort = &outputPort
				updateOps, err := nbClient.Where(route).Update(route, &route.Nexthop, &route.OutputPort)
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
				klog.Infof("Updated default route nexthop to %s via %s", nextHop, outputPort)
				return nil
			}
		}
	}

	// Create new default route
	route := &LogicalRouterStaticRoute{
		IPPrefix:   "0.0.0.0/0",
		Nexthop:    nextHop,
		OutputPort: &outputPort,
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

	klog.Infof("Added default route 0.0.0.0/0 -> %s via %s", nextHop, outputPort)
	return nil
}
