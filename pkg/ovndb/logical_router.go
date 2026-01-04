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
func (o *LogicalRouterOps) CreateClusterRouter(ctx context.Context) (*LogicalRouter, error) {
	// Check if router already exists
	existing, err := o.GetLogicalRouter(ctx, ClusterRouterName)
	if err == nil {
		klog.V(4).Infof("Cluster router %s already exists", ClusterRouterName)
		return existing, nil
	}
	if !IsNotFound(err) {
		return nil, fmt.Errorf("failed to check existing router: %w", err)
	}

	// Create the router
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

	ops, err := o.client.nbClient.Create(router)
	if err != nil {
		return nil, fmt.Errorf("failed to create router operation: %w", err)
	}

	results, err := o.client.nbClient.Transact(ctx, ops...)
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster router: %w", err)
	}

	if err := checkTransactResults(results); err != nil {
		return nil, fmt.Errorf("cluster router creation failed: %w", err)
	}

	klog.Infof("Created cluster router %s", ClusterRouterName)

	// Fetch and return the created router
	return o.GetLogicalRouter(ctx, ClusterRouterName)
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
