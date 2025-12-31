// Package ovn provides the OVN network controllers.
//
// This package contains controllers that watch Kubernetes resources
// and manage corresponding OVN logical objects.
//
// Controllers:
// - SubnetController: Manages Subnet CRD and OVN Logical Switches
// - PodController: Manages Pod network configuration and OVN LSPs
// - ServiceController: Manages Service load balancing via OVN Load Balancers
// - PolicyController: Manages NetworkPolicy via OVN ACLs
// - NodeController: Manages node network configuration
//
// Reference: OVN-Kubernetes pkg/ovn/
package ovn

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
)

// Controller is the main OVN network controller
type Controller struct {
	// config is the global configuration
	config *config.Config

	// ovnClient is the OVN database client
	ovnClient *ovndb.Client

	// manager is the controller-runtime manager
	manager manager.Manager
}

// NewController creates a new OVN controller
//
// Parameters:
//   - cfg: Global configuration
//   - mgr: Controller-runtime manager
//
// Returns:
//   - *Controller: OVN controller instance
//   - error: Initialization error
func NewController(cfg *config.Config, mgr manager.Manager) (*Controller, error) {
	// TODO: Initialize OVN client
	// TODO: Register sub-controllers
	return &Controller{
		config:  cfg,
		manager: mgr,
	}, nil
}

// Start starts the OVN controller
func (c *Controller) Start(ctx context.Context) error {
	// TODO: Start controller
	return nil
}
