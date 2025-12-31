// Package ovn provides the Subnet controller implementation.
//
// The SubnetController watches Subnet CRD resources and manages the corresponding
// OVN Logical Switches. It supports two modes:
//
// - Standalone mode: Creates and manages OVN Logical Switches automatically
// - External mode: References existing OVN Logical Switches (e.g., from ZStack)
//
// The controller is responsible for:
// - Creating/updating OVN Logical Switches for new Subnets
// - Validating external Logical Switch references
// - Initializing IP allocators for each subnet
// - Updating Subnet status with IP availability information
// - Cleaning up OVN resources when Subnets are deleted
//
// Reference: OVN-Kubernetes pkg/ovn/base_network_controller.go
package ovn

import (
	"context"
	"fmt"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	networkv1 "github.com/jiayi-1994/zstack-ovn-kubernetes/api/v1"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/allocator"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
)

const (
	// SubnetFinalizer is the finalizer added to Subnet resources
	SubnetFinalizer = "subnet.network.zstack.io/finalizer"

	// SubnetControllerName is the name of this controller
	SubnetControllerName = "subnet-controller"

	// ExternalID keys for OVN objects
	ExternalIDSubnetName      = "zstack.io/subnet"
	ExternalIDSubnetNamespace = "zstack.io/subnet-namespace"
	ExternalIDManagedBy       = "zstack.io/managed-by"
	ExternalIDManagedByValue  = "zstack-ovn-kubernetes"
)

// SubnetReconciler reconciles a Subnet object.
type SubnetReconciler struct {
	client       client.Client
	scheme       *runtime.Scheme
	recorder     record.EventRecorder
	config       *config.Config
	ovnClient    *ovndb.Client
	lsOps        *ovndb.LogicalSwitchOps
	zstackCompat *ovndb.ZStackCompatibility
	allocators   map[string]*allocator.SubnetAllocator
	allocatorsMu sync.RWMutex
}

// NewSubnetReconciler creates a new SubnetReconciler.
func NewSubnetReconciler(
	c client.Client,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
	cfg *config.Config,
	ovnClient *ovndb.Client,
) *SubnetReconciler {
	return &SubnetReconciler{
		client:       c,
		scheme:       scheme,
		recorder:     recorder,
		config:       cfg,
		ovnClient:    ovnClient,
		lsOps:        ovndb.NewLogicalSwitchOps(ovnClient),
		zstackCompat: ovndb.NewZStackCompatibility(ovnClient),
		allocators:   make(map[string]*allocator.SubnetAllocator),
	}
}

// Reconcile handles the reconciliation of a Subnet resource.
func (r *SubnetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("subnet", req.Name)
	log.V(4).Info("Reconciling Subnet")

	subnet := &networkv1.Subnet{}
	if err := r.client.Get(ctx, req.NamespacedName, subnet); err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to get Subnet")
			return ctrl.Result{}, err
		}
		log.V(4).Info("Subnet not found, likely deleted")
		return ctrl.Result{}, nil
	}

	if !subnet.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, subnet)
	}

	if !controllerutil.ContainsFinalizer(subnet, SubnetFinalizer) {
		log.V(4).Info("Adding finalizer to Subnet")
		controllerutil.AddFinalizer(subnet, SubnetFinalizer)
		if err := r.client.Update(ctx, subnet); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	result, err := r.reconcileSubnet(ctx, subnet)
	if err != nil {
		log.Error(err, "Failed to reconcile Subnet")
		r.updateStatusFailed(ctx, subnet, err.Error())
		return result, err
	}

	return result, nil
}

func (r *SubnetReconciler) reconcileSubnet(ctx context.Context, subnet *networkv1.Subnet) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("subnet", subnet.Name)

	if err := r.validateSubnet(subnet); err != nil {
		log.Error(err, "Subnet validation failed")
		r.recorder.Event(subnet, "Warning", "ValidationFailed", err.Error())
		return ctrl.Result{}, err
	}

	lsName := subnet.GetLogicalSwitchName()
	log.V(4).Info("Processing Logical Switch", "name", lsName)

	if subnet.IsExternalMode() {
		if err := r.verifyExternalLogicalSwitch(ctx, subnet, lsName); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		if err := r.ensureLogicalSwitch(ctx, subnet, lsName); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.ensureIPAllocator(subnet); err != nil {
		log.Error(err, "Failed to initialize IP allocator")
		return ctrl.Result{}, err
	}

	if err := r.updateStatusActive(ctx, subnet, lsName); err != nil {
		log.Error(err, "Failed to update Subnet status")
		return ctrl.Result{}, err
	}

	log.Info("Subnet reconciled successfully", "logicalSwitch", lsName)
	r.recorder.Event(subnet, "Normal", "Reconciled", fmt.Sprintf("Subnet %s reconciled successfully", subnet.Name))

	return ctrl.Result{}, nil
}

func (r *SubnetReconciler) validateSubnet(subnet *networkv1.Subnet) error {
	if subnet.Spec.CIDR == "" {
		return fmt.Errorf("CIDR is required")
	}

	_, err := allocator.NewSubnetAllocator(subnet.Spec.CIDR, subnet.Spec.ExcludeIPs)
	if err != nil {
		return fmt.Errorf("invalid subnet configuration: %w", err)
	}

	if subnet.Spec.Gateway == "" {
		return fmt.Errorf("gateway is required")
	}

	return nil
}

// verifyExternalLogicalSwitch verifies that an external Logical Switch exists.
// In external mode, this validates that the referenced ZStack Logical Switch
// exists and can be used by zstack-ovn-kubernetes.
func (r *SubnetReconciler) verifyExternalLogicalSwitch(ctx context.Context, subnet *networkv1.Subnet, lsName string) error {
	log := klog.FromContext(ctx).WithValues("subnet", subnet.Name, "logicalSwitch", lsName)
	log.V(4).Info("Verifying external Logical Switch exists")

	// Use ZStack compatibility module for validation
	ls, err := r.zstackCompat.ValidateExternalLogicalSwitch(ctx, lsName)
	if err != nil {
		errMsg := fmt.Sprintf("external Logical Switch validation failed: %v", err)
		log.Error(err, errMsg)
		r.recorder.Event(subnet, "Warning", "LogicalSwitchValidationFailed", errMsg)
		return fmt.Errorf(errMsg)
	}

	// Get detailed info for logging
	info, err := r.zstackCompat.GetLogicalSwitchInfo(ctx, lsName)
	if err != nil {
		log.V(4).Info("Could not get detailed Logical Switch info", "error", err)
	} else {
		log.V(4).Info("External Logical Switch details",
			"uuid", info.UUID,
			"isZStackManaged", info.IsZStackManaged,
			"portCount", info.PortCount,
			"aclCount", info.ACLCount)

		// Log ZStack-specific info if available
		if info.IsZStackManaged {
			log.V(4).Info("ZStack management info",
				"vpcID", info.ZStackVPCID,
				"subnetID", info.ZStackSubnetID,
				"networkID", info.ZStackNetworkID)
		}
	}

	// Record event for successful validation
	r.recorder.Event(subnet, "Normal", "LogicalSwitchValidated",
		fmt.Sprintf("External Logical Switch %s validated (UUID: %s)", lsName, ls.UUID))

	log.V(4).Info("External Logical Switch verified successfully")
	return nil
}

func (r *SubnetReconciler) ensureLogicalSwitch(ctx context.Context, subnet *networkv1.Subnet, lsName string) error {
	log := klog.FromContext(ctx).WithValues("subnet", subnet.Name, "logicalSwitch", lsName)
	log.V(4).Info("Ensuring Logical Switch exists")

	otherConfig := map[string]string{
		"subnet": subnet.Spec.CIDR,
	}
	if len(subnet.Spec.ExcludeIPs) > 0 {
		otherConfig["exclude_ips"] = strings.Join(subnet.Spec.ExcludeIPs, " ")
	}

	externalIDs := map[string]string{
		ExternalIDSubnetName: subnet.Name,
		ExternalIDManagedBy:  ExternalIDManagedByValue,
	}

	// Use ZStack compatibility module for safe creation
	// This ensures we don't conflict with ZStack-managed switches
	if r.config.IsExternalMode() {
		// In external mode, use safe creation with conflict checking
		ls, err := r.zstackCompat.SafeCreateLogicalSwitch(ctx, lsName, otherConfig, externalIDs)
		if err != nil {
			return fmt.Errorf("failed to safely create Logical Switch: %w", err)
		}
		log.Info("Logical Switch created/updated safely", "name", lsName, "uuid", ls.UUID)
		r.recorder.Event(subnet, "Normal", "LogicalSwitchCreated",
			fmt.Sprintf("Created OVN Logical Switch %s", lsName))
		return nil
	}

	// In standalone mode, use direct creation (no conflict checking needed)
	existingLS, err := r.lsOps.GetLogicalSwitch(ctx, lsName)
	if err != nil && !ovndb.IsNotFound(err) {
		return fmt.Errorf("failed to check existing Logical Switch: %w", err)
	}

	if existingLS != nil {
		log.V(4).Info("Logical Switch already exists, checking for updates")

		if existingLS.ExternalIDs[ExternalIDManagedBy] != ExternalIDManagedByValue {
			return fmt.Errorf("Logical Switch %s exists but is not managed by zstack-ovn-kubernetes", lsName)
		}

		needsUpdate := false
		if !mapsEqual(existingLS.OtherConfig, otherConfig) {
			existingLS.OtherConfig = otherConfig
			needsUpdate = true
		}
		if !mapsEqual(existingLS.ExternalIDs, externalIDs) {
			existingLS.ExternalIDs = externalIDs
			needsUpdate = true
		}

		if needsUpdate {
			log.V(4).Info("Updating Logical Switch configuration")
			if err := r.lsOps.UpdateLogicalSwitch(ctx, existingLS); err != nil {
				return fmt.Errorf("failed to update Logical Switch: %w", err)
			}
		}

		return nil
	}

	log.Info("Creating new Logical Switch", "name", lsName)
	_, err = r.lsOps.CreateLogicalSwitch(ctx, lsName, otherConfig, externalIDs)
	if err != nil {
		return fmt.Errorf("failed to create Logical Switch: %w", err)
	}

	r.recorder.Event(subnet, "Normal", "LogicalSwitchCreated",
		fmt.Sprintf("Created OVN Logical Switch %s", lsName))

	return nil
}

func (r *SubnetReconciler) ensureIPAllocator(subnet *networkv1.Subnet) error {
	r.allocatorsMu.Lock()
	defer r.allocatorsMu.Unlock()

	if _, exists := r.allocators[subnet.Name]; exists {
		return nil
	}

	excludeIPs := make([]string, len(subnet.Spec.ExcludeIPs))
	copy(excludeIPs, subnet.Spec.ExcludeIPs)

	gatewayExcluded := false
	for _, ip := range excludeIPs {
		if ip == subnet.Spec.Gateway {
			gatewayExcluded = true
			break
		}
	}
	if !gatewayExcluded {
		excludeIPs = append(excludeIPs, subnet.Spec.Gateway)
	}

	alloc, err := allocator.NewSubnetAllocator(subnet.Spec.CIDR, excludeIPs)
	if err != nil {
		return fmt.Errorf("failed to create IP allocator: %w", err)
	}

	r.allocators[subnet.Name] = alloc
	klog.V(4).Infof("Created IP allocator for subnet %s: %d available IPs", subnet.Name, alloc.Available())

	return nil
}

func (r *SubnetReconciler) handleDeletion(ctx context.Context, subnet *networkv1.Subnet) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("subnet", subnet.Name)
	log.Info("Handling Subnet deletion")

	if !subnet.IsExternalMode() {
		lsName := subnet.GetLogicalSwitchName()
		log.V(4).Info("Deleting Logical Switch", "name", lsName)

		// Use ZStack compatibility module for safe deletion in external mode
		if r.config.IsExternalMode() {
			if err := r.zstackCompat.SafeDeleteLogicalSwitch(ctx, lsName); err != nil {
				if !ovndb.IsNotFound(err) {
					log.Error(err, "Failed to safely delete Logical Switch")
					return ctrl.Result{}, err
				}
			} else {
				log.Info("Safely deleted Logical Switch", "name", lsName)
				r.recorder.Event(subnet, "Normal", "LogicalSwitchDeleted",
					fmt.Sprintf("Deleted OVN Logical Switch %s", lsName))
			}
		} else {
			// In standalone mode, use direct deletion with ownership check
			existingLS, err := r.lsOps.GetLogicalSwitch(ctx, lsName)
			if err != nil && !ovndb.IsNotFound(err) {
				log.Error(err, "Failed to check Logical Switch")
				return ctrl.Result{}, err
			}

			if existingLS != nil {
				if existingLS.ExternalIDs[ExternalIDManagedBy] == ExternalIDManagedByValue {
					if err := r.lsOps.DeleteLogicalSwitch(ctx, lsName); err != nil {
						log.Error(err, "Failed to delete Logical Switch")
						return ctrl.Result{}, err
					}
					log.Info("Deleted Logical Switch", "name", lsName)
					r.recorder.Event(subnet, "Normal", "LogicalSwitchDeleted",
						fmt.Sprintf("Deleted OVN Logical Switch %s", lsName))
				} else {
					log.V(4).Info("Logical Switch not managed by us, skipping deletion", "name", lsName)
				}
			}
		}
	}

	r.allocatorsMu.Lock()
	delete(r.allocators, subnet.Name)
	r.allocatorsMu.Unlock()

	if controllerutil.ContainsFinalizer(subnet, SubnetFinalizer) {
		log.V(4).Info("Removing finalizer from Subnet")
		controllerutil.RemoveFinalizer(subnet, SubnetFinalizer)
		if err := r.client.Update(ctx, subnet); err != nil {
			log.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
	}

	log.Info("Subnet deletion completed")
	return ctrl.Result{}, nil
}

func (r *SubnetReconciler) updateStatusActive(ctx context.Context, subnet *networkv1.Subnet, lsName string) error {
	r.allocatorsMu.RLock()
	alloc, exists := r.allocators[subnet.Name]
	r.allocatorsMu.RUnlock()

	availableIPs := 0
	usedIPs := 0
	if exists {
		availableIPs = alloc.Available()
		usedIPs = alloc.Used()
	}

	now := metav1.Now()
	subnet.Status.Phase = networkv1.SubnetPhaseActive
	subnet.Status.Reason = ""
	subnet.Status.Message = "Subnet is ready for use"
	subnet.Status.LogicalSwitch = lsName
	subnet.Status.AvailableIPs = availableIPs
	subnet.Status.UsedIPs = usedIPs
	subnet.Status.LastUpdateTime = &now

	subnet.Status.Conditions = updateCondition(subnet.Status.Conditions, metav1.Condition{
		Type:               networkv1.SubnetConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "SubnetReady",
		Message:            "Subnet is ready for use",
		LastTransitionTime: now,
	})
	subnet.Status.Conditions = updateCondition(subnet.Status.Conditions, metav1.Condition{
		Type:               networkv1.SubnetConditionLogicalSwitchReady,
		Status:             metav1.ConditionTrue,
		Reason:             "LogicalSwitchReady",
		Message:            fmt.Sprintf("Logical Switch %s is ready", lsName),
		LastTransitionTime: now,
	})
	subnet.Status.Conditions = updateCondition(subnet.Status.Conditions, metav1.Condition{
		Type:               networkv1.SubnetConditionIPPoolReady,
		Status:             metav1.ConditionTrue,
		Reason:             "IPPoolReady",
		Message:            fmt.Sprintf("IP pool initialized with %d available IPs", availableIPs),
		LastTransitionTime: now,
	})

	return r.client.Status().Update(ctx, subnet)
}

func (r *SubnetReconciler) updateStatusFailed(ctx context.Context, subnet *networkv1.Subnet, reason string) error {
	now := metav1.Now()
	subnet.Status.Phase = networkv1.SubnetPhaseFailed
	subnet.Status.Reason = reason
	subnet.Status.Message = fmt.Sprintf("Subnet configuration failed: %s", reason)
	subnet.Status.LastUpdateTime = &now

	subnet.Status.Conditions = updateCondition(subnet.Status.Conditions, metav1.Condition{
		Type:               networkv1.SubnetConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             "SubnetFailed",
		Message:            reason,
		LastTransitionTime: now,
	})

	if err := r.client.Status().Update(ctx, subnet); err != nil {
		klog.Errorf("Failed to update Subnet %s status to Failed: %v", subnet.Name, err)
		return err
	}

	return nil
}

// GetAllocator returns the IP allocator for a subnet.
func (r *SubnetReconciler) GetAllocator(subnetName string) *allocator.SubnetAllocator {
	r.allocatorsMu.RLock()
	defer r.allocatorsMu.RUnlock()
	return r.allocators[subnetName]
}

// SetupWithManager sets up the controller with the Manager.
func (r *SubnetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkv1.Subnet{}).
		Named(SubnetControllerName).
		Complete(r)
}

// mapsEqual compares two string maps for equality.
func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// updateCondition updates or adds a condition to the conditions slice.
func updateCondition(conditions []metav1.Condition, newCondition metav1.Condition) []metav1.Condition {
	for i, c := range conditions {
		if c.Type == newCondition.Type {
			if c.Status != newCondition.Status {
				newCondition.LastTransitionTime = metav1.Now()
			} else {
				newCondition.LastTransitionTime = c.LastTransitionTime
			}
			conditions[i] = newCondition
			return conditions
		}
	}
	return append(conditions, newCondition)
}
