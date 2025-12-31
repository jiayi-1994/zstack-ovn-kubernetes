// Package ovn provides the Pod network controller implementation.
//
// The PodController watches Pod resources and manages their network configuration:
// - Allocates IP addresses from the appropriate subnet
// - Creates OVN Logical Switch Ports for each Pod
// - Sets Pod annotations with network configuration
// - Cleans up resources when Pods are deleted
//
// The controller works in coordination with the CNI handler:
// 1. Pod is created in Kubernetes
// 2. PodController allocates IP and creates OVN LSP
// 3. PodController sets Pod annotation with network info
// 4. CNI handler reads annotation and configures network interface
//
// Reference: OVN-Kubernetes pkg/ovn/pods.go
package ovn

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	networkv1 "github.com/jiayi-1994/zstack-ovn-kubernetes/api/v1"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/allocator"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/util"
)

const (
	// PodControllerName is the name of this controller
	PodControllerName = "pod-controller"

	// PodFinalizer is the finalizer added to Pods managed by this controller
	PodFinalizer = "pod.network.zstack.io/finalizer"

	// DefaultSubnetAnnotation is the annotation key for specifying a subnet
	DefaultSubnetAnnotation = "zstack.io/subnet"
)

// PodReconciler reconciles Pod objects for network configuration.
//
// The reconciler is responsible for:
// - Allocating IP addresses for new Pods
// - Creating OVN Logical Switch Ports
// - Setting Pod annotations with network configuration
// - Cleaning up resources when Pods are deleted
type PodReconciler struct {
	// client is the Kubernetes client
	client client.Client

	// scheme is the runtime scheme
	scheme *runtime.Scheme

	// recorder is the event recorder
	recorder record.EventRecorder

	// config is the global configuration
	config *config.Config

	// ovnClient is the OVN database client
	ovnClient *ovndb.Client

	// lspOps provides Logical Switch Port operations
	lspOps *ovndb.LogicalSwitchPortOps

	// subnetReconciler provides access to subnet allocators
	subnetReconciler *SubnetReconciler

	// podAllocations tracks IP allocations per Pod
	// Key: namespace/name, Value: allocated IP
	podAllocations map[string]string
	allocationsMu  sync.RWMutex
}

// NewPodReconciler creates a new PodReconciler.
//
// Parameters:
//   - c: Kubernetes client
//   - scheme: Runtime scheme
//   - recorder: Event recorder
//   - cfg: Global configuration
//   - ovnClient: OVN database client
//   - subnetReconciler: Subnet reconciler for IP allocation
//
// Returns:
//   - *PodReconciler: Pod reconciler instance
func NewPodReconciler(
	c client.Client,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
	cfg *config.Config,
	ovnClient *ovndb.Client,
	subnetReconciler *SubnetReconciler,
) *PodReconciler {
	return &PodReconciler{
		client:           c,
		scheme:           scheme,
		recorder:         recorder,
		config:           cfg,
		ovnClient:        ovnClient,
		lspOps:           ovndb.NewLogicalSwitchPortOps(ovnClient),
		subnetReconciler: subnetReconciler,
		podAllocations:   make(map[string]string),
	}
}

// Reconcile handles the reconciliation of a Pod resource.
//
// The reconciliation logic:
// 1. If Pod is being deleted, clean up OVN resources
// 2. If Pod already has network annotation, skip (already configured)
// 3. Find the appropriate subnet for the Pod
// 4. Allocate IP address from the subnet
// 5. Create OVN Logical Switch Port
// 6. Set Pod annotation with network configuration
func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("pod", req.NamespacedName)
	log.V(4).Info("Reconciling Pod")

	// Get the Pod
	pod := &corev1.Pod{}
	if err := r.client.Get(ctx, req.NamespacedName, pod); err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to get Pod")
			return ctrl.Result{}, err
		}
		// Pod was deleted, clean up any tracked allocations
		r.cleanupAllocation(req.NamespacedName.String())
		log.V(4).Info("Pod not found, likely deleted")
		return ctrl.Result{}, nil
	}

	// Skip Pods that don't need network configuration
	if !r.shouldManagePod(pod) {
		log.V(4).Info("Skipping Pod (host network or completed)")
		return ctrl.Result{}, nil
	}

	// Handle deletion
	if !pod.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, pod)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(pod, PodFinalizer) {
		log.V(4).Info("Adding finalizer to Pod")
		controllerutil.AddFinalizer(pod, PodFinalizer)
		if err := r.client.Update(ctx, pod); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Check if Pod already has network annotation
	if util.HasPodAnnotation(pod) {
		log.V(4).Info("Pod already has network annotation, skipping")
		return ctrl.Result{}, nil
	}

	// Configure network for the Pod
	result, err := r.configurePodNetwork(ctx, pod)
	if err != nil {
		log.Error(err, "Failed to configure Pod network")
		r.recorder.Event(pod, corev1.EventTypeWarning, "NetworkConfigFailed", err.Error())
		return result, err
	}

	log.Info("Pod network configured successfully")
	r.recorder.Event(pod, corev1.EventTypeNormal, "NetworkConfigured", "Pod network configured successfully")

	return ctrl.Result{}, nil
}

// shouldManagePod determines if a Pod should be managed by this controller.
//
// Pods are skipped if:
// - They use host network
// - They are in a terminal state (Succeeded/Failed)
// - They are static Pods (mirror Pods)
func (r *PodReconciler) shouldManagePod(pod *corev1.Pod) bool {
	// Skip host network Pods
	if pod.Spec.HostNetwork {
		return false
	}

	// Skip completed Pods
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false
	}

	// Skip mirror Pods (static Pods)
	if _, ok := pod.Annotations[corev1.MirrorPodAnnotationKey]; ok {
		return false
	}

	return true
}

// configurePodNetwork configures the network for a Pod.
//
// Steps:
// 1. Find the appropriate subnet
// 2. Allocate IP address
// 3. Generate MAC address
// 4. Create OVN Logical Switch Port
// 5. Set Pod annotation
func (r *PodReconciler) configurePodNetwork(ctx context.Context, pod *corev1.Pod) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("pod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))

	// Find the subnet for this Pod
	subnet, err := r.findSubnetForPod(ctx, pod)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to find subnet: %w", err)
	}
	if subnet == nil {
		log.Info("No subnet found for Pod, waiting for subnet to be created")
		return ctrl.Result{Requeue: true}, nil
	}

	log.V(4).Info("Found subnet for Pod", "subnet", subnet.Name)

	// Get the IP allocator for this subnet
	alloc := r.subnetReconciler.GetAllocator(subnet.Name)
	if alloc == nil {
		return ctrl.Result{Requeue: true}, fmt.Errorf("IP allocator not ready for subnet %s", subnet.Name)
	}

	// Allocate IP address
	ip, err := r.allocateIP(ctx, pod, alloc)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to allocate IP: %w", err)
	}

	// Calculate prefix length from subnet CIDR
	_, ipNet, _ := net.ParseCIDR(subnet.Spec.CIDR)
	prefixLen, _ := ipNet.Mask.Size()
	ipWithPrefix := fmt.Sprintf("%s/%d", ip.String(), prefixLen)

	// Generate MAC address from IP
	mac := util.GenerateMAC(ip)

	// Get logical switch name
	logicalSwitch := subnet.GetLogicalSwitchName()

	// Build port name
	portName := ovndb.BuildPortName(pod.Namespace, pod.Name)

	log.V(4).Info("Creating OVN Logical Switch Port",
		"port", portName,
		"switch", logicalSwitch,
		"ip", ipWithPrefix,
		"mac", mac)

	// Create OVN Logical Switch Port
	if err := r.createLogicalSwitchPort(ctx, pod, logicalSwitch, portName, mac, ip.String()); err != nil {
		// Release IP on failure
		_ = alloc.Release(ip)
		r.cleanupAllocation(fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
		return ctrl.Result{}, fmt.Errorf("failed to create OVN LSP: %w", err)
	}

	// Create Pod annotation
	annotation := util.NewPodAnnotation(
		ipWithPrefix,
		mac,
		subnet.Spec.Gateway,
		subnet.Name,
		logicalSwitch,
		portName,
	)

	// Set annotation on Pod
	if err := util.SetPodAnnotation(pod, annotation); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set Pod annotation: %w", err)
	}

	// Update Pod in Kubernetes
	if err := r.client.Update(ctx, pod); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update Pod: %w", err)
	}

	log.Info("Pod network configured",
		"ip", ipWithPrefix,
		"mac", mac,
		"gateway", subnet.Spec.Gateway,
		"logicalSwitch", logicalSwitch)

	return ctrl.Result{}, nil
}

// findSubnetForPod finds the appropriate subnet for a Pod.
//
// Subnet selection order:
// 1. Subnet specified in Pod annotation (zstack.io/subnet)
// 2. Subnet matching Pod's namespace
// 3. Default subnet (if configured)
func (r *PodReconciler) findSubnetForPod(ctx context.Context, pod *corev1.Pod) (*networkv1.Subnet, error) {
	// Check for explicit subnet annotation
	if pod.Annotations != nil {
		if subnetName, ok := pod.Annotations[DefaultSubnetAnnotation]; ok && subnetName != "" {
			subnet := &networkv1.Subnet{}
			if err := r.client.Get(ctx, types.NamespacedName{Name: subnetName}, subnet); err != nil {
				return nil, fmt.Errorf("specified subnet %s not found: %w", subnetName, err)
			}
			if subnet.Status.Phase != networkv1.SubnetPhaseActive {
				return nil, fmt.Errorf("subnet %s is not active", subnetName)
			}
			return subnet, nil
		}
	}

	// List all subnets
	subnetList := &networkv1.SubnetList{}
	if err := r.client.List(ctx, subnetList); err != nil {
		return nil, fmt.Errorf("failed to list subnets: %w", err)
	}

	// Find subnet matching namespace or default subnet
	var defaultSubnet *networkv1.Subnet
	for i := range subnetList.Items {
		subnet := &subnetList.Items[i]

		// Skip non-active subnets
		if subnet.Status.Phase != networkv1.SubnetPhaseActive {
			continue
		}

		// Check if subnet is for this namespace
		if len(subnet.Spec.Namespaces) > 0 {
			for _, ns := range subnet.Spec.Namespaces {
				if ns == pod.Namespace {
					return subnet, nil
				}
			}
		}

		// Track default subnet
		if subnet.Spec.Default {
			defaultSubnet = subnet
		}
	}

	// Return default subnet if found
	if defaultSubnet != nil {
		return defaultSubnet, nil
	}

	// Return first active subnet as fallback
	for i := range subnetList.Items {
		subnet := &subnetList.Items[i]
		if subnet.Status.Phase == networkv1.SubnetPhaseActive {
			return subnet, nil
		}
	}

	return nil, nil
}

// allocateIP allocates an IP address for a Pod.
func (r *PodReconciler) allocateIP(ctx context.Context, pod *corev1.Pod, alloc *allocator.SubnetAllocator) (net.IP, error) {
	podKey := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)

	r.allocationsMu.Lock()
	defer r.allocationsMu.Unlock()

	// Check if we already allocated an IP for this Pod
	if ipStr, ok := r.podAllocations[podKey]; ok {
		ip := net.ParseIP(ipStr)
		if ip != nil {
			return ip, nil
		}
	}

	// Allocate new IP
	ip, err := alloc.AllocateNext()
	if err != nil {
		return nil, err
	}

	// Track allocation
	r.podAllocations[podKey] = ip.String()

	return ip, nil
}

// cleanupAllocation removes tracked IP allocation for a Pod.
func (r *PodReconciler) cleanupAllocation(podKey string) {
	r.allocationsMu.Lock()
	defer r.allocationsMu.Unlock()
	delete(r.podAllocations, podKey)
}

// createLogicalSwitchPort creates an OVN Logical Switch Port for a Pod.
func (r *PodReconciler) createLogicalSwitchPort(
	ctx context.Context,
	pod *corev1.Pod,
	switchName, portName, mac, ip string,
) error {
	// Check if port already exists
	existingPort, err := r.lspOps.GetLogicalSwitchPort(ctx, portName)
	if err != nil && !ovndb.IsNotFound(err) {
		return fmt.Errorf("failed to check existing port: %w", err)
	}

	if existingPort != nil {
		klog.V(4).Infof("Logical Switch Port %s already exists", portName)
		return nil
	}

	// Build external IDs
	externalIDs := map[string]string{
		ovndb.ExternalIDNamespace:    pod.Namespace,
		ovndb.ExternalIDPod:          pod.Name,
		ovndb.ExternalIDPodUID:       string(pod.UID),
		ovndb.ExternalIDOwner:        PodControllerName,
		"logical_switch":             switchName,
	}

	// Create the port
	_, err = r.lspOps.CreateLogicalSwitchPort(
		ctx,
		switchName,
		portName,
		mac,
		[]string{ip},
		externalIDs,
	)
	if err != nil {
		return fmt.Errorf("failed to create LSP: %w", err)
	}

	return nil
}

// handleDeletion handles Pod deletion.
//
// Steps:
// 1. Delete OVN Logical Switch Port
// 2. Release IP address
// 3. Remove finalizer
func (r *PodReconciler) handleDeletion(ctx context.Context, pod *corev1.Pod) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("pod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
	log.Info("Handling Pod deletion")

	// Get network annotation to find resources to clean up
	annotation, _ := util.GetPodAnnotation(pod)

	// Delete OVN Logical Switch Port
	if annotation != nil && annotation.LogicalSwitch != "" && annotation.LogicalSwitchPort != "" {
		log.V(4).Info("Deleting OVN Logical Switch Port",
			"port", annotation.LogicalSwitchPort,
			"switch", annotation.LogicalSwitch)

		if err := r.lspOps.DeleteLogicalSwitchPort(ctx, annotation.LogicalSwitch, annotation.LogicalSwitchPort); err != nil {
			if !ovndb.IsNotFound(err) {
				log.Error(err, "Failed to delete OVN LSP")
				// Continue with cleanup even if LSP deletion fails
			}
		}
	} else {
		// Try to delete by port name
		portName := ovndb.BuildPortName(pod.Namespace, pod.Name)
		existingPort, err := r.lspOps.GetLogicalSwitchPort(ctx, portName)
		if err == nil && existingPort != nil {
			switchName := existingPort.ExternalIDs["logical_switch"]
			if switchName != "" {
				_ = r.lspOps.DeleteLogicalSwitchPort(ctx, switchName, portName)
			}
		}
	}

	// Release IP address
	if annotation != nil && annotation.Subnet != "" {
		alloc := r.subnetReconciler.GetAllocator(annotation.Subnet)
		if alloc != nil {
			ipStr := annotation.GetIP()
			if ipStr != "" {
				ip := net.ParseIP(ipStr)
				if ip != nil {
					if err := alloc.Release(ip); err != nil {
						log.V(4).Info("Failed to release IP", "ip", ipStr, "error", err)
					} else {
						log.V(4).Info("Released IP", "ip", ipStr)
					}
				}
			}
		}
	}

	// Clean up tracked allocation
	r.cleanupAllocation(fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))

	// Remove finalizer
	if controllerutil.ContainsFinalizer(pod, PodFinalizer) {
		log.V(4).Info("Removing finalizer from Pod")
		controllerutil.RemoveFinalizer(pod, PodFinalizer)
		if err := r.client.Update(ctx, pod); err != nil {
			log.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
	}

	log.Info("Pod deletion completed")
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithEventFilter(r.podEventFilter()).
		Named(PodControllerName).
		Complete(r)
}

// podEventFilter returns a predicate that filters Pod events.
//
// We only care about:
// - Pod creation (to configure network)
// - Pod deletion (to clean up resources)
// - Pod updates that affect network configuration
func (r *PodReconciler) podEventFilter() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			pod, ok := e.Object.(*corev1.Pod)
			if !ok {
				return false
			}
			return r.shouldManagePod(pod)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, ok := e.ObjectOld.(*corev1.Pod)
			if !ok {
				return false
			}
			newPod, ok := e.ObjectNew.(*corev1.Pod)
			if !ok {
				return false
			}

			// Always process if being deleted
			if !newPod.DeletionTimestamp.IsZero() {
				return true
			}

			// Skip if we shouldn't manage this Pod
			if !r.shouldManagePod(newPod) {
				return false
			}

			// Process if annotation changed
			oldHasAnnotation := util.HasPodAnnotation(oldPod)
			newHasAnnotation := util.HasPodAnnotation(newPod)
			if oldHasAnnotation != newHasAnnotation {
				return true
			}

			// Process if finalizer changed
			oldHasFinalizer := controllerutil.ContainsFinalizer(oldPod, PodFinalizer)
			newHasFinalizer := controllerutil.ContainsFinalizer(newPod, PodFinalizer)
			if oldHasFinalizer != newHasFinalizer {
				return true
			}

			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// Always process deletions to clean up
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}

// GetPodAllocation returns the allocated IP for a Pod.
// This is useful for testing and debugging.
func (r *PodReconciler) GetPodAllocation(namespace, name string) string {
	r.allocationsMu.RLock()
	defer r.allocationsMu.RUnlock()
	return r.podAllocations[fmt.Sprintf("%s/%s", namespace, name)]
}

// AllocateIPForPod allocates an IP for a Pod from the specified subnet.
// This is a public method that can be called by other components.
//
// Parameters:
//   - ctx: Context for cancellation
//   - pod: The Pod to allocate IP for
//   - subnetName: Name of the subnet to allocate from
//
// Returns:
//   - string: Allocated IP address with prefix (e.g., "10.244.1.5/24")
//   - string: Generated MAC address
//   - error: Allocation error
func (r *PodReconciler) AllocateIPForPod(ctx context.Context, pod *corev1.Pod, subnetName string) (string, string, error) {
	// Get subnet
	subnet := &networkv1.Subnet{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: subnetName}, subnet); err != nil {
		return "", "", fmt.Errorf("subnet %s not found: %w", subnetName, err)
	}

	// Get allocator
	alloc := r.subnetReconciler.GetAllocator(subnetName)
	if alloc == nil {
		return "", "", fmt.Errorf("IP allocator not ready for subnet %s", subnetName)
	}

	// Allocate IP
	ip, err := r.allocateIP(ctx, pod, alloc)
	if err != nil {
		return "", "", err
	}

	// Calculate prefix
	_, ipNet, _ := net.ParseCIDR(subnet.Spec.CIDR)
	prefixLen, _ := ipNet.Mask.Size()
	ipWithPrefix := fmt.Sprintf("%s/%d", ip.String(), prefixLen)

	// Generate MAC
	mac := util.GenerateMAC(ip)

	return ipWithPrefix, mac, nil
}

// ReleaseIPForPod releases the IP allocated for a Pod.
//
// Parameters:
//   - ctx: Context for cancellation
//   - namespace: Pod namespace
//   - name: Pod name
//   - subnetName: Name of the subnet
//   - ip: IP address to release
//
// Returns:
//   - error: Release error
func (r *PodReconciler) ReleaseIPForPod(ctx context.Context, namespace, name, subnetName, ip string) error {
	alloc := r.subnetReconciler.GetAllocator(subnetName)
	if alloc == nil {
		return fmt.Errorf("IP allocator not found for subnet %s", subnetName)
	}

	// Remove prefix if present
	ipStr := strings.Split(ip, "/")[0]
	parsedIP := net.ParseIP(ipStr)
	if parsedIP == nil {
		return fmt.Errorf("invalid IP address: %s", ip)
	}

	if err := alloc.Release(parsedIP); err != nil {
		return err
	}

	r.cleanupAllocation(fmt.Sprintf("%s/%s", namespace, name))
	return nil
}
