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
// Startup Flow:
// 1. Initialize OVN client connection
// 2. Run SyncManager.RunFullSync() to synchronize state
// 3. Start all sub-controllers for normal reconciliation
//
// Reference: OVN-Kubernetes pkg/ovn/
package ovn

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
)

// Controller is the main OVN network controller
type Controller struct {
	// client is the Kubernetes API client
	client client.Client

	// scheme is the runtime scheme
	scheme *runtime.Scheme

	// config is the global configuration
	config *config.Config

	// ovnClient is the OVN database client
	ovnClient *ovndb.Client

	// manager is the controller-runtime manager
	manager manager.Manager

	// recorder is the event recorder
	recorder record.EventRecorder

	// syncManager handles startup synchronization
	syncManager *SyncManager

	// subnetReconciler manages subnet resources
	subnetReconciler *SubnetReconciler

	// podReconciler manages pod network configuration
	podReconciler *PodReconciler

	// syncCompleted indicates if initial sync has completed
	syncCompleted bool
}

// NewController creates a new OVN controller
//
// Parameters:
//   - cfg: Global configuration
//   - mgr: Controller-runtime manager
//   - ovnClient: OVN database client (can be nil, will be created if needed)
//
// Returns:
//   - *Controller: OVN controller instance
//   - error: Initialization error
func NewController(cfg *config.Config, mgr manager.Manager, ovnClient *ovndb.Client) (*Controller, error) {
	c := &Controller{
		client:    mgr.GetClient(),
		scheme:    mgr.GetScheme(),
		config:    cfg,
		ovnClient: ovnClient,
		manager:   mgr,
		recorder:  mgr.GetEventRecorderFor("ovn-controller"),
	}

	return c, nil
}

// SetupWithManager sets up all sub-controllers with the manager.
//
// This should be called after NewController and before Start.
// It registers all sub-controllers with the controller-runtime manager.
func (c *Controller) SetupWithManager(mgr manager.Manager) error {
	// Create subnet reconciler
	c.subnetReconciler = NewSubnetReconciler(
		c.client,
		c.scheme,
		c.recorder,
		c.config,
		c.ovnClient,
	)

	// Create pod reconciler
	c.podReconciler = NewPodReconciler(
		c.client,
		c.scheme,
		c.recorder,
		c.config,
		c.ovnClient,
		c.subnetReconciler,
	)

	// Create sync manager
	c.syncManager = NewSyncManager(
		c.client,
		c.config,
		c.ovnClient,
		c.subnetReconciler,
		c.podReconciler,
	)

	// Setup subnet controller
	if err := c.subnetReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup subnet controller: %w", err)
	}

	// Setup pod controller
	if err := c.podReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup pod controller: %w", err)
	}

	klog.Info("All sub-controllers registered with manager")
	return nil
}

// Start starts the OVN controller.
//
// This is called by the controller-runtime manager when it starts.
// It performs initial synchronization before normal reconciliation begins.
func (c *Controller) Start(ctx context.Context) error {
	klog.Info("Starting OVN controller...")

	// Run initial sync
	if err := c.runInitialSync(ctx); err != nil {
		klog.Errorf("Initial sync failed: %v", err)
		// Don't fail startup, continue with normal reconciliation
		// The reconcilers will handle any inconsistencies
	}

	c.syncCompleted = true
	klog.Info("OVN controller started successfully")

	// Start periodic cleanup (optional)
	go c.runPeriodicCleanup(ctx)

	return nil
}

// runInitialSync performs the initial synchronization at startup.
func (c *Controller) runInitialSync(ctx context.Context) error {
	if c.syncManager == nil {
		klog.Warning("SyncManager not initialized, skipping initial sync")
		return nil
	}

	klog.Info("Running initial OVN sync...")
	startTime := time.Now()

	result, err := c.syncManager.RunFullSync(ctx)
	if err != nil {
		return fmt.Errorf("full sync failed: %w", err)
	}

	klog.Infof("Initial sync completed in %v: nodes=%d, subnets=%d, pods=%d, cleaned=%d, recovered=%d, errors=%d",
		result.Duration,
		result.NodesProcessed,
		result.SubnetsProcessed,
		result.PodsProcessed,
		result.StaleResourcesCleaned,
		result.ResourcesRecovered,
		len(result.Errors))

	if len(result.Errors) > 0 {
		for i, err := range result.Errors {
			klog.Warningf("Sync error %d: %v", i+1, err)
		}
	}

	klog.Infof("Initial sync took %v", time.Since(startTime))
	return nil
}

// runPeriodicCleanup runs periodic cleanup of stale resources.
func (c *Controller) runPeriodicCleanup(ctx context.Context) {
	// Run cleanup every 30 minutes
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			klog.Info("Stopping periodic cleanup")
			return
		case <-ticker.C:
			if c.syncManager != nil {
				klog.V(4).Info("Running periodic stale resource cleanup")
				if err := c.syncManager.CleanupStaleResources(ctx); err != nil {
					klog.Errorf("Periodic cleanup failed: %v", err)
				}
			}
		}
	}
}

// IsSyncCompleted returns whether the initial sync has completed.
func (c *Controller) IsSyncCompleted() bool {
	return c.syncCompleted
}

// GetSyncManager returns the sync manager for external use.
func (c *Controller) GetSyncManager() *SyncManager {
	return c.syncManager
}

// GetSubnetReconciler returns the subnet reconciler for external use.
func (c *Controller) GetSubnetReconciler() *SubnetReconciler {
	return c.subnetReconciler
}

// GetPodReconciler returns the pod reconciler for external use.
func (c *Controller) GetPodReconciler() *PodReconciler {
	return c.podReconciler
}

// NeedLeaderElection implements the LeaderElectionRunnable interface.
// Returns true because this controller should only run on the leader.
func (c *Controller) NeedLeaderElection() bool {
	return true
}
