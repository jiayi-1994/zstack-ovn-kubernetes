// Package ovn provides the Service controller implementation.
//
// The ServiceController watches Kubernetes Service and EndpointSlice resources
// and manages the corresponding OVN Load Balancers. It implements L4 load balancing
// for Kubernetes Services using OVN's native load balancer functionality.
//
// The controller is responsible for:
// - Creating OVN Load Balancers for ClusterIP Services
// - Managing VIP to backend mappings based on EndpointSlices
// - Handling NodePort Services with per-node load balancers
// - Cleaning up OVN resources when Services are deleted
// - Attaching Load Balancers to appropriate Logical Switches
//
// Load Balancer Naming Convention:
// - ClusterIP: "Service_<namespace>/<name>_<protocol>"
// - NodePort: "Service_<namespace>/<name>_<protocol>_nodeport"
//
// Reference: OVN-Kubernetes pkg/ovn/controller/services/
package ovn

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	networkv1 "github.com/jiayi-1994/zstack-ovn-kubernetes/api/v1"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/config"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
)

const (
	// ServiceControllerName is the name of this controller
	ServiceControllerName = "service-controller"

	// Load Balancer external ID keys
	LBExternalIDService   = "k8s.ovn.org/service"
	LBExternalIDNamespace = "k8s.ovn.org/namespace"
	LBExternalIDKind      = "k8s.ovn.org/kind"
	LBExternalIDOwner     = "k8s.ovn.org/owner"

	// Load Balancer kinds
	LBKindClusterIP = "ClusterIP"
	LBKindNodePort  = "NodePort"
)

// ServiceReconciler reconciles Service objects for OVN Load Balancer management.
//
// The reconciler is responsible for:
// - Creating/updating OVN Load Balancers for Services
// - Managing VIP to backend mappings
// - Attaching Load Balancers to Logical Switches
// - Cleaning up resources when Services are deleted
type ServiceReconciler struct {
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

	// lbOps provides Load Balancer operations
	lbOps *ovndb.LoadBalancerOps

	// lsOps provides Logical Switch operations
	lsOps *ovndb.LogicalSwitchOps

	// serviceLBs tracks Load Balancer UUIDs per Service
	// Key: namespace/name, Value: map of protocol -> LB UUID
	serviceLBs   map[string]map[string]string
	serviceLBsMu sync.RWMutex
}

// NewServiceReconciler creates a new ServiceReconciler.
//
// Parameters:
//   - c: Kubernetes client
//   - scheme: Runtime scheme
//   - recorder: Event recorder
//   - cfg: Global configuration
//   - ovnClient: OVN database client
//
// Returns:
//   - *ServiceReconciler: Service reconciler instance
func NewServiceReconciler(
	c client.Client,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
	cfg *config.Config,
	ovnClient *ovndb.Client,
) *ServiceReconciler {
	return &ServiceReconciler{
		client:     c,
		scheme:     scheme,
		recorder:   recorder,
		config:     cfg,
		ovnClient:  ovnClient,
		lbOps:      ovndb.NewLoadBalancerOps(ovnClient),
		lsOps:      ovndb.NewLogicalSwitchOps(ovnClient),
		serviceLBs: make(map[string]map[string]string),
	}
}

// Reconcile handles the reconciliation of a Service resource.
//
// The reconciliation logic:
// 1. If Service is being deleted, clean up OVN Load Balancers
// 2. Skip Services that don't need load balancing (ExternalName, Headless)
// 3. Get EndpointSlices for the Service
// 4. Create/update OVN Load Balancers with VIP and backends
// 5. Attach Load Balancers to appropriate Logical Switches
func (r *ServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("service", req.NamespacedName)
	log.V(4).Info("Reconciling Service")

	// Get the Service
	svc := &corev1.Service{}
	if err := r.client.Get(ctx, req.NamespacedName, svc); err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to get Service")
			return ctrl.Result{}, err
		}
		// Service was deleted, clean up Load Balancers
		log.V(4).Info("Service not found, cleaning up Load Balancers")
		return r.handleDeletion(ctx, req.Namespace, req.Name)
	}

	// Handle deletion
	if !svc.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, svc.Namespace, svc.Name)
	}

	// Skip Services that don't need load balancing
	if !r.shouldManageService(svc) {
		log.V(4).Info("Skipping Service (ExternalName or Headless)")
		return ctrl.Result{}, nil
	}

	// Reconcile the Service
	result, err := r.reconcileService(ctx, svc)
	if err != nil {
		log.Error(err, "Failed to reconcile Service")
		r.recorder.Event(svc, corev1.EventTypeWarning, "ReconcileFailed", err.Error())
		return result, err
	}

	log.Info("Service reconciled successfully")
	return ctrl.Result{}, nil
}

// shouldManageService determines if a Service should be managed by this controller.
//
// Services are skipped if:
// - They are ExternalName type (no load balancing needed)
// - They are Headless (ClusterIP: None)
func (r *ServiceReconciler) shouldManageService(svc *corev1.Service) bool {
	// Skip ExternalName services
	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		return false
	}

	// Skip Headless services (ClusterIP: None)
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == corev1.ClusterIPNone {
		return false
	}

	return true
}

// reconcileService reconciles a Service by creating/updating OVN Load Balancers.
func (r *ServiceReconciler) reconcileService(ctx context.Context, svc *corev1.Service) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("service", fmt.Sprintf("%s/%s", svc.Namespace, svc.Name))

	// Get EndpointSlices for this Service
	endpoints, err := r.getEndpointsForService(ctx, svc)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get endpoints: %w", err)
	}

	// Process each port in the Service
	for _, port := range svc.Spec.Ports {
		protocol := string(port.Protocol)
		if protocol == "" {
			protocol = "TCP"
		}

		// Build VIP string
		vip := ovndb.BuildVIP(svc.Spec.ClusterIP, int(port.Port))

		// Build backends from endpoints
		backends := r.buildBackends(endpoints, port.TargetPort.IntValue(), protocol)

		// Create or update Load Balancer for ClusterIP
		if err := r.ensureClusterIPLoadBalancer(ctx, svc, port, vip, backends); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to ensure ClusterIP LB: %w", err)
		}

		// Handle NodePort if applicable
		if svc.Spec.Type == corev1.ServiceTypeNodePort || svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
			if port.NodePort > 0 {
				if err := r.ensureNodePortLoadBalancer(ctx, svc, port, backends); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to ensure NodePort LB: %w", err)
				}
			}
		}
	}

	// Clean up Load Balancers for removed ports
	if err := r.cleanupStaleLoadBalancers(ctx, svc); err != nil {
		log.V(4).Info("Failed to cleanup stale LBs", "error", err)
	}

	r.recorder.Event(svc, corev1.EventTypeNormal, "Reconciled",
		fmt.Sprintf("Service %s/%s load balancers configured", svc.Namespace, svc.Name))

	return ctrl.Result{}, nil
}

// getEndpointsForService retrieves all ready endpoints for a Service.
//
// This method handles Pod health status by:
// - Only including endpoints where Ready condition is true
// - Excluding endpoints that are terminating
// - Supporting both serving and ready conditions for graceful termination
//
// Reference: OVN-Kubernetes pkg/ovn/controller/services/endpoints.go
func (r *ServiceReconciler) getEndpointsForService(ctx context.Context, svc *corev1.Service) ([]EndpointInfo, error) {
	log := klog.FromContext(ctx).WithValues("service", fmt.Sprintf("%s/%s", svc.Namespace, svc.Name))

	// List EndpointSlices for this Service
	endpointSliceList := &discoveryv1.EndpointSliceList{}
	if err := r.client.List(ctx, endpointSliceList,
		client.InNamespace(svc.Namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: svc.Name}); err != nil {
		return nil, fmt.Errorf("failed to list EndpointSlices: %w", err)
	}

	var endpoints []EndpointInfo
	var skippedNotReady, skippedTerminating int

	for _, eps := range endpointSliceList.Items {
		// Skip EndpointSlices that don't match the address type we support
		if eps.AddressType != discoveryv1.AddressTypeIPv4 && eps.AddressType != discoveryv1.AddressTypeIPv6 {
			continue
		}

		for _, endpoint := range eps.Endpoints {
			// Check if endpoint is ready
			// Ready means the Pod is ready to receive traffic
			isReady := endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready

			// Check if endpoint is terminating
			// Terminating means the Pod is being deleted
			isTerminating := endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating

			// Check if endpoint is serving (can still serve traffic even if terminating)
			// This is used for graceful termination
			isServing := endpoint.Conditions.Serving == nil || *endpoint.Conditions.Serving

			// Skip endpoints that are not ready and not serving
			if !isReady && !isServing {
				skippedNotReady++
				continue
			}

			// Skip endpoints that are terminating (unless we want to support graceful termination)
			if isTerminating {
				skippedTerminating++
				continue
			}

			for _, addr := range endpoint.Addresses {
				info := EndpointInfo{
					Address:     addr,
					NodeName:    "",
					Ready:       isReady,
					Serving:     isServing,
					Terminating: isTerminating,
				}
				if endpoint.NodeName != nil {
					info.NodeName = *endpoint.NodeName
				}
				if endpoint.Zone != nil {
					info.Zone = *endpoint.Zone
				}
				endpoints = append(endpoints, info)
			}
		}
	}

	log.V(4).Info("Retrieved endpoints for Service",
		"ready", len(endpoints),
		"skippedNotReady", skippedNotReady,
		"skippedTerminating", skippedTerminating)

	return endpoints, nil
}

// getEndpointsForServicePort retrieves endpoints for a specific Service port.
// This is useful when different ports may have different target ports.
func (r *ServiceReconciler) getEndpointsForServicePort(ctx context.Context, svc *corev1.Service, port corev1.ServicePort) ([]EndpointInfo, error) {
	// List EndpointSlices for this Service
	endpointSliceList := &discoveryv1.EndpointSliceList{}
	if err := r.client.List(ctx, endpointSliceList,
		client.InNamespace(svc.Namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: svc.Name}); err != nil {
		return nil, fmt.Errorf("failed to list EndpointSlices: %w", err)
	}

	var endpoints []EndpointInfo

	for _, eps := range endpointSliceList.Items {
		// Find the port in the EndpointSlice that matches our Service port
		var targetPort int32
		for _, p := range eps.Ports {
			if p.Port == nil {
				continue
			}
			// Match by port name or port number
			if (p.Name != nil && port.Name != "" && *p.Name == port.Name) ||
				(port.TargetPort.IntVal > 0 && *p.Port == port.TargetPort.IntVal) {
				targetPort = *p.Port
				break
			}
		}

		// If no matching port found, use the first port or skip
		if targetPort == 0 && len(eps.Ports) > 0 && eps.Ports[0].Port != nil {
			targetPort = *eps.Ports[0].Port
		}

		for _, endpoint := range eps.Endpoints {
			// Check readiness
			isReady := endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready
			isTerminating := endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating

			if !isReady || isTerminating {
				continue
			}

			for _, addr := range endpoint.Addresses {
				info := EndpointInfo{
					Address:    addr,
					TargetPort: int(targetPort),
					Ready:      isReady,
				}
				if endpoint.NodeName != nil {
					info.NodeName = *endpoint.NodeName
				}
				endpoints = append(endpoints, info)
			}
		}
	}

	return endpoints, nil
}

// EndpointInfo contains information about a single endpoint.
type EndpointInfo struct {
	// Address is the IP address of the endpoint
	Address string

	// NodeName is the name of the node hosting this endpoint
	NodeName string

	// Zone is the zone of the endpoint (for topology-aware routing)
	Zone string

	// TargetPort is the target port for this endpoint
	TargetPort int

	// Ready indicates if the endpoint is ready to receive traffic
	Ready bool

	// Serving indicates if the endpoint can serve traffic (even if terminating)
	Serving bool

	// Terminating indicates if the endpoint is terminating
	Terminating bool
}

// buildBackends builds the backend string for OVN Load Balancer.
func (r *ServiceReconciler) buildBackends(endpoints []EndpointInfo, targetPort int, protocol string) string {
	if len(endpoints) == 0 {
		return ""
	}

	var backends []string
	for _, ep := range endpoints {
		backend := ovndb.BuildVIP(ep.Address, targetPort)
		backends = append(backends, backend)
	}

	return strings.Join(backends, ",")
}

// ensureClusterIPLoadBalancer creates or updates the ClusterIP Load Balancer.
func (r *ServiceReconciler) ensureClusterIPLoadBalancer(
	ctx context.Context,
	svc *corev1.Service,
	port corev1.ServicePort,
	vip, backends string,
) error {
	log := klog.FromContext(ctx).WithValues(
		"service", fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
		"port", port.Port,
		"protocol", port.Protocol,
	)

	protocol := strings.ToLower(string(port.Protocol))
	if protocol == "" {
		protocol = ovndb.LoadBalancerProtocolTCP
	}

	// Build Load Balancer name
	lbName := buildLoadBalancerName(svc.Namespace, svc.Name, protocol, LBKindClusterIP)

	// Build external IDs
	externalIDs := map[string]string{
		LBExternalIDService:   fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
		LBExternalIDNamespace: svc.Namespace,
		LBExternalIDKind:      LBKindClusterIP,
		LBExternalIDOwner:     ServiceControllerName,
	}

	// Build VIPs map
	vips := map[string]string{}
	if backends != "" {
		vips[vip] = backends
	}

	// Check if Load Balancer exists
	existingLB, err := r.lbOps.GetLoadBalancer(ctx, lbName)
	if err != nil && !ovndb.IsNotFound(err) {
		return fmt.Errorf("failed to get existing LB: %w", err)
	}

	if existingLB != nil {
		// Update existing Load Balancer
		log.V(4).Info("Updating existing Load Balancer", "name", lbName)

		// Update VIPs
		if err := r.lbOps.SetVips(ctx, lbName, vips); err != nil {
			return fmt.Errorf("failed to update LB VIPs: %w", err)
		}

		// Track the LB UUID
		r.trackLoadBalancer(svc.Namespace, svc.Name, protocol, existingLB.UUID)
	} else {
		// Create new Load Balancer
		log.Info("Creating new Load Balancer", "name", lbName, "vip", vip)

		lb, err := r.lbOps.CreateLoadBalancer(ctx, lbName, protocol, vips, nil, externalIDs)
		if err != nil {
			return fmt.Errorf("failed to create LB: %w", err)
		}

		// Track the LB UUID
		r.trackLoadBalancer(svc.Namespace, svc.Name, protocol, lb.UUID)

		// Attach Load Balancer to all Logical Switches
		if err := r.attachLoadBalancerToSwitches(ctx, lb.UUID); err != nil {
			log.V(4).Info("Failed to attach LB to switches", "error", err)
		}
	}

	return nil
}

// ensureNodePortLoadBalancer creates or updates the NodePort Load Balancer.
//
// NodePort Load Balancers are created with VIPs for each node's IP address.
// This allows external traffic to reach the Service through any node.
//
// externalTrafficPolicy handling:
// - Cluster: Traffic is load balanced to all backends (default)
// - Local: Traffic is only sent to backends on the same node (preserves source IP)
func (r *ServiceReconciler) ensureNodePortLoadBalancer(
	ctx context.Context,
	svc *corev1.Service,
	port corev1.ServicePort,
	backends string,
) error {
	log := klog.FromContext(ctx).WithValues(
		"service", fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
		"nodePort", port.NodePort,
		"protocol", port.Protocol,
	)

	protocol := strings.ToLower(string(port.Protocol))
	if protocol == "" {
		protocol = ovndb.LoadBalancerProtocolTCP
	}

	// Build Load Balancer name for NodePort
	lbName := buildLoadBalancerName(svc.Namespace, svc.Name, protocol, LBKindNodePort)

	// Build external IDs
	externalIDs := map[string]string{
		LBExternalIDService:   fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
		LBExternalIDNamespace: svc.Namespace,
		LBExternalIDKind:      LBKindNodePort,
		LBExternalIDOwner:     ServiceControllerName,
	}

	// Check externalTrafficPolicy
	isLocalPolicy := svc.Spec.ExternalTrafficPolicy == corev1.ServiceExternalTrafficPolicyTypeLocal

	// Get all node IPs for NodePort VIPs
	nodeIPs, err := r.getNodeIPs(ctx)
	if err != nil {
		return fmt.Errorf("failed to get node IPs: %w", err)
	}

	// Build VIPs map with all node IPs
	vips := map[string]string{}
	if backends != "" {
		if isLocalPolicy {
			// For Local policy, we need to get endpoints per node
			// and only route to local backends
			nodeBackends, err := r.getNodeLocalBackends(ctx, svc, port)
			if err != nil {
				log.V(4).Info("Failed to get node-local backends, falling back to all backends", "error", err)
				// Fall back to all backends
				for _, nodeIP := range nodeIPs {
					vip := ovndb.BuildVIP(nodeIP, int(port.NodePort))
					vips[vip] = backends
				}
			} else {
				for nodeIP, localBackends := range nodeBackends {
					if localBackends != "" {
						vip := ovndb.BuildVIP(nodeIP, int(port.NodePort))
						vips[vip] = localBackends
					}
				}
			}
		} else {
			// For Cluster policy, all nodes route to all backends
			for _, nodeIP := range nodeIPs {
				vip := ovndb.BuildVIP(nodeIP, int(port.NodePort))
				vips[vip] = backends
			}
		}
	}

	// Build options for the Load Balancer
	options := map[string]string{}
	if isLocalPolicy {
		// Skip SNAT to preserve source IP for Local policy
		options[ovndb.LBOptionSkipSNAT] = "true"
	}

	// Check if Load Balancer exists
	existingLB, err := r.lbOps.GetLoadBalancer(ctx, lbName)
	if err != nil && !ovndb.IsNotFound(err) {
		return fmt.Errorf("failed to get existing NodePort LB: %w", err)
	}

	if existingLB != nil {
		// Update existing Load Balancer
		log.V(4).Info("Updating existing NodePort Load Balancer", "name", lbName, "localPolicy", isLocalPolicy)

		if err := r.lbOps.SetVips(ctx, lbName, vips); err != nil {
			return fmt.Errorf("failed to update NodePort LB VIPs: %w", err)
		}

		// Update options if needed
		if len(options) > 0 {
			if err := r.lbOps.SetOptions(ctx, lbName, options); err != nil {
				log.V(4).Info("Failed to update LB options", "error", err)
			}
		}

		r.trackLoadBalancer(svc.Namespace, svc.Name, protocol+"_nodeport", existingLB.UUID)
	} else {
		// Create new Load Balancer
		log.Info("Creating new NodePort Load Balancer", "name", lbName, "localPolicy", isLocalPolicy)

		lb, err := r.lbOps.CreateLoadBalancer(ctx, lbName, protocol, vips, options, externalIDs)
		if err != nil {
			return fmt.Errorf("failed to create NodePort LB: %w", err)
		}

		r.trackLoadBalancer(svc.Namespace, svc.Name, protocol+"_nodeport", lb.UUID)

		// Attach Load Balancer to all Logical Switches
		if err := r.attachLoadBalancerToSwitches(ctx, lb.UUID); err != nil {
			log.V(4).Info("Failed to attach NodePort LB to switches", "error", err)
		}
	}

	return nil
}

// getNodeLocalBackends returns backends grouped by node IP for Local traffic policy.
// This is used when externalTrafficPolicy is set to Local.
func (r *ServiceReconciler) getNodeLocalBackends(ctx context.Context, svc *corev1.Service, port corev1.ServicePort) (map[string]string, error) {
	// Get endpoints with node information
	endpoints, err := r.getEndpointsForService(ctx, svc)
	if err != nil {
		return nil, err
	}

	// Get node IP mapping
	nodeIPMap, err := r.getNodeIPMap(ctx)
	if err != nil {
		return nil, err
	}

	// Group endpoints by node
	nodeEndpoints := make(map[string][]string)
	targetPort := port.TargetPort.IntValue()

	for _, ep := range endpoints {
		if ep.NodeName == "" {
			continue
		}

		nodeIP, ok := nodeIPMap[ep.NodeName]
		if !ok {
			continue
		}

		backend := ovndb.BuildVIP(ep.Address, targetPort)
		nodeEndpoints[nodeIP] = append(nodeEndpoints[nodeIP], backend)
	}

	// Convert to backend strings
	result := make(map[string]string)
	for nodeIP, backends := range nodeEndpoints {
		result[nodeIP] = strings.Join(backends, ",")
	}

	return result, nil
}

// getNodeIPMap returns a map of node name to internal IP.
func (r *ServiceReconciler) getNodeIPMap(ctx context.Context) (map[string]string, error) {
	nodeList := &corev1.NodeList{}
	if err := r.client.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	result := make(map[string]string)
	for _, node := range nodeList.Items {
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				result[node.Name] = addr.Address
				break
			}
		}
	}

	return result, nil
}

// getNodeIPs retrieves all node internal IPs.
func (r *ServiceReconciler) getNodeIPs(ctx context.Context) ([]string, error) {
	nodeList := &corev1.NodeList{}
	if err := r.client.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	var ips []string
	for _, node := range nodeList.Items {
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				ips = append(ips, addr.Address)
				break
			}
		}
	}

	return ips, nil
}

// attachLoadBalancerToSwitches attaches a Load Balancer to all Logical Switches.
func (r *ServiceReconciler) attachLoadBalancerToSwitches(ctx context.Context, lbUUID string) error {
	// List all subnets to get their Logical Switches
	subnetList := &networkv1.SubnetList{}
	if err := r.client.List(ctx, subnetList); err != nil {
		return fmt.Errorf("failed to list subnets: %w", err)
	}

	for _, subnet := range subnetList.Items {
		if subnet.Status.Phase != networkv1.SubnetPhaseActive {
			continue
		}

		lsName := subnet.GetLogicalSwitchName()
		if err := r.lsOps.AddLoadBalancersToLogicalSwitch(ctx, lsName, lbUUID); err != nil {
			klog.V(4).Infof("Failed to attach LB %s to switch %s: %v", lbUUID, lsName, err)
		}
	}

	return nil
}

// trackLoadBalancer tracks a Load Balancer UUID for a Service.
func (r *ServiceReconciler) trackLoadBalancer(namespace, name, protocol, uuid string) {
	r.serviceLBsMu.Lock()
	defer r.serviceLBsMu.Unlock()

	key := fmt.Sprintf("%s/%s", namespace, name)
	if r.serviceLBs[key] == nil {
		r.serviceLBs[key] = make(map[string]string)
	}
	r.serviceLBs[key][protocol] = uuid
}

// getTrackedLoadBalancers returns tracked Load Balancer UUIDs for a Service.
func (r *ServiceReconciler) getTrackedLoadBalancers(namespace, name string) map[string]string {
	r.serviceLBsMu.RLock()
	defer r.serviceLBsMu.RUnlock()

	key := fmt.Sprintf("%s/%s", namespace, name)
	if lbs, ok := r.serviceLBs[key]; ok {
		// Return a copy
		result := make(map[string]string)
		for k, v := range lbs {
			result[k] = v
		}
		return result
	}
	return nil
}

// cleanupStaleLoadBalancers removes Load Balancers for ports that no longer exist.
func (r *ServiceReconciler) cleanupStaleLoadBalancers(ctx context.Context, svc *corev1.Service) error {
	// Get current protocols from Service
	currentProtocols := make(map[string]bool)
	for _, port := range svc.Spec.Ports {
		protocol := strings.ToLower(string(port.Protocol))
		if protocol == "" {
			protocol = ovndb.LoadBalancerProtocolTCP
		}
		currentProtocols[protocol] = true

		// Also track NodePort protocols if applicable
		if (svc.Spec.Type == corev1.ServiceTypeNodePort || svc.Spec.Type == corev1.ServiceTypeLoadBalancer) && port.NodePort > 0 {
			currentProtocols[protocol+"_nodeport"] = true
		}
	}

	// Get tracked Load Balancers
	trackedLBs := r.getTrackedLoadBalancers(svc.Namespace, svc.Name)
	if trackedLBs == nil {
		return nil
	}

	// Delete Load Balancers for protocols that no longer exist
	for protocol := range trackedLBs {
		if !currentProtocols[protocol] {
			kind := LBKindClusterIP
			if strings.HasSuffix(protocol, "_nodeport") {
				kind = LBKindNodePort
				protocol = strings.TrimSuffix(protocol, "_nodeport")
			}
			lbName := buildLoadBalancerName(svc.Namespace, svc.Name, protocol, kind)
			if err := r.lbOps.DeleteLoadBalancer(ctx, lbName); err != nil && !ovndb.IsNotFound(err) {
				klog.V(4).Infof("Failed to delete stale LB %s: %v", lbName, err)
			}
		}
	}

	return nil
}

// handleDeletion handles Service deletion by cleaning up OVN Load Balancers.
func (r *ServiceReconciler) handleDeletion(ctx context.Context, namespace, name string) (ctrl.Result, error) {
	log := klog.FromContext(ctx).WithValues("service", fmt.Sprintf("%s/%s", namespace, name))
	log.Info("Handling Service deletion")

	// List all Load Balancers for this Service
	lbs, err := r.lbOps.ListLoadBalancersWithPredicate(ctx, func(lb *ovndb.LoadBalancer) bool {
		return lb.ExternalIDs[LBExternalIDService] == fmt.Sprintf("%s/%s", namespace, name) &&
			lb.ExternalIDs[LBExternalIDOwner] == ServiceControllerName
	})
	if err != nil {
		log.V(4).Info("Failed to list Load Balancers", "error", err)
	}

	// Delete each Load Balancer
	for _, lb := range lbs {
		log.V(4).Info("Deleting Load Balancer", "name", lb.Name)

		// First remove from all Logical Switches
		if err := r.detachLoadBalancerFromSwitches(ctx, lb.UUID); err != nil {
			log.V(4).Info("Failed to detach LB from switches", "error", err)
		}

		// Then delete the Load Balancer
		if err := r.lbOps.DeleteLoadBalancer(ctx, lb.Name); err != nil && !ovndb.IsNotFound(err) {
			log.Error(err, "Failed to delete Load Balancer", "name", lb.Name)
		}
	}

	// Clean up tracking
	r.serviceLBsMu.Lock()
	delete(r.serviceLBs, fmt.Sprintf("%s/%s", namespace, name))
	r.serviceLBsMu.Unlock()

	log.Info("Service deletion completed")
	return ctrl.Result{}, nil
}

// detachLoadBalancerFromSwitches removes a Load Balancer from all Logical Switches.
func (r *ServiceReconciler) detachLoadBalancerFromSwitches(ctx context.Context, lbUUID string) error {
	// List all Logical Switches
	switches, err := r.lsOps.ListLogicalSwitches(ctx)
	if err != nil {
		return fmt.Errorf("failed to list switches: %w", err)
	}

	for _, ls := range switches {
		// Check if this switch has the Load Balancer
		for _, lb := range ls.LoadBalancer {
			if lb == lbUUID {
				if err := r.lsOps.RemoveLoadBalancersFromLogicalSwitch(ctx, ls.Name, lbUUID); err != nil {
					klog.V(4).Infof("Failed to remove LB %s from switch %s: %v", lbUUID, ls.Name, err)
				}
				break
			}
		}
	}

	return nil
}

// buildLoadBalancerName builds the Load Balancer name.
func buildLoadBalancerName(namespace, name, protocol, kind string) string {
	if kind == LBKindNodePort {
		return fmt.Sprintf("Service_%s/%s_%s_nodeport", namespace, name, protocol)
	}
	return fmt.Sprintf("Service_%s/%s_%s", namespace, name, protocol)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}).
		Watches(
			&discoveryv1.EndpointSlice{},
			handler.EnqueueRequestsFromMapFunc(r.endpointSliceToService),
		).
		Named(ServiceControllerName).
		Complete(r)
}

// endpointSliceToService maps EndpointSlice events to Service reconcile requests.
func (r *ServiceReconciler) endpointSliceToService(ctx context.Context, obj client.Object) []reconcile.Request {
	eps, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return nil
	}

	// Get the Service name from the label
	serviceName, ok := eps.Labels[discoveryv1.LabelServiceName]
	if !ok {
		return nil
	}

	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Namespace: eps.Namespace,
				Name:      serviceName,
			},
		},
	}
}

// GetLoadBalancerForService returns the Load Balancer name for a Service.
// This is useful for testing and debugging.
func (r *ServiceReconciler) GetLoadBalancerForService(namespace, name, protocol string) string {
	return buildLoadBalancerName(namespace, name, strings.ToLower(protocol), LBKindClusterIP)
}

// GetLoadBalancerVIPs returns the VIPs for a Service's Load Balancer.
// This is useful for testing and debugging.
func (r *ServiceReconciler) GetLoadBalancerVIPs(ctx context.Context, namespace, name, protocol string) (map[string]string, error) {
	lbName := buildLoadBalancerName(namespace, name, strings.ToLower(protocol), LBKindClusterIP)
	lb, err := r.lbOps.GetLoadBalancer(ctx, lbName)
	if err != nil {
		return nil, err
	}
	return lb.Vips, nil
}

// BuildServiceVIP builds a VIP string for a Service port.
// Exported for use in property tests.
func BuildServiceVIP(clusterIP string, port int32) string {
	return ovndb.BuildVIP(clusterIP, int(port))
}

// BuildBackendString builds a backend string from endpoint addresses.
// Exported for use in property tests.
func BuildBackendString(addresses []string, targetPort int) string {
	if len(addresses) == 0 {
		return ""
	}

	var backends []string
	for _, addr := range addresses {
		backend := ovndb.BuildVIP(addr, targetPort)
		backends = append(backends, backend)
	}

	return strings.Join(backends, ",")
}

// ParseServicePort parses a port string to int.
func ParseServicePort(portStr string) (int, error) {
	return strconv.Atoi(portStr)
}
