// Package events provides Kubernetes Event recording for zstack-ovn-kubernetes.
//
// This package provides utilities for recording Kubernetes Events to track
// important operations and errors in the CNI plugin. Events are visible
// via kubectl describe and help with debugging and monitoring.
//
// Event Types:
// - Normal: Routine operations (e.g., network configured successfully)
// - Warning: Potential issues or errors (e.g., IP allocation failed)
//
// Usage:
//
//	recorder := events.NewRecorder(clientset, "zstack-ovn-kubernetes")
//	recorder.NetworkConfigured(pod)
//	recorder.NetworkConfigFailed(pod, err)
//
// Reference: Kubernetes client-go tools/record
package events

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
)

// Event reason constants
// These are used as the "reason" field in Kubernetes Events
const (
	// Network configuration events
	ReasonNetworkConfigured     = "NetworkConfigured"
	ReasonNetworkConfigFailed   = "NetworkConfigFailed"
	ReasonNetworkDeleted        = "NetworkDeleted"
	ReasonNetworkDeleteFailed   = "NetworkDeleteFailed"
	ReasonNetworkCheckFailed    = "NetworkCheckFailed"

	// IP allocation events
	ReasonIPAllocated           = "IPAllocated"
	ReasonIPAllocationFailed    = "IPAllocationFailed"
	ReasonIPReleased            = "IPReleased"
	ReasonIPReleaseFailed       = "IPReleaseFailed"
	ReasonSubnetExhausted       = "SubnetExhausted"

	// OVN operation events
	ReasonOVNOperationFailed    = "OVNOperationFailed"
	ReasonOVNConnectionFailed   = "OVNConnectionFailed"
	ReasonOVNConnectionRestored = "OVNConnectionRestored"
	ReasonOVNTransactionFailed  = "OVNTransactionFailed"

	// Subnet events
	ReasonSubnetCreated         = "SubnetCreated"
	ReasonSubnetCreateFailed    = "SubnetCreateFailed"
	ReasonSubnetDeleted         = "SubnetDeleted"
	ReasonSubnetDeleteFailed    = "SubnetDeleteFailed"
	ReasonSubnetValidationFailed = "SubnetValidationFailed"
	ReasonExternalLSNotFound    = "ExternalLogicalSwitchNotFound"
	ReasonExternalLSFound       = "ExternalLogicalSwitchFound"

	// Service events
	ReasonLoadBalancerCreated   = "LoadBalancerCreated"
	ReasonLoadBalancerUpdated   = "LoadBalancerUpdated"
	ReasonLoadBalancerDeleted   = "LoadBalancerDeleted"
	ReasonLoadBalancerFailed    = "LoadBalancerFailed"
	ReasonEndpointsUpdated      = "EndpointsUpdated"
	ReasonEndpointsUpdateFailed = "EndpointsUpdateFailed"

	// NetworkPolicy events
	ReasonACLCreated            = "ACLCreated"
	ReasonACLDeleted            = "ACLDeleted"
	ReasonACLFailed             = "ACLFailed"
	ReasonPolicyApplied         = "PolicyApplied"
	ReasonPolicyFailed          = "PolicyFailed"

	// Node events
	ReasonNodeJoined            = "NodeJoined"
	ReasonNodeLeft              = "NodeLeft"
	ReasonTunnelConfigured      = "TunnelConfigured"
	ReasonTunnelConfigFailed    = "TunnelConfigFailed"
	ReasonGatewayConfigured     = "GatewayConfigured"
	ReasonGatewayConfigFailed   = "GatewayConfigFailed"
)

// Recorder wraps the Kubernetes event recorder with CNI-specific methods
type Recorder struct {
	// recorder is the underlying Kubernetes event recorder
	recorder record.EventRecorder

	// component is the component name for events
	component string
}

// NewRecorder creates a new event recorder
//
// Parameters:
//   - clientset: Kubernetes clientset for creating events
//   - component: Component name (e.g., "zstack-ovnkube-controller")
//   - scheme: Runtime scheme for object type resolution
//
// Returns:
//   - *Recorder: Event recorder instance
func NewRecorder(clientset kubernetes.Interface, component string, scheme *runtime.Scheme) *Recorder {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{
		Interface: clientset.CoreV1().Events(""),
	})

	recorder := eventBroadcaster.NewRecorder(scheme, corev1.EventSource{
		Component: component,
	})

	return &Recorder{
		recorder:  recorder,
		component: component,
	}
}

// NewRecorderFromEventRecorder creates a Recorder from an existing event recorder
// This is useful when using controller-runtime's event recorder
func NewRecorderFromEventRecorder(recorder record.EventRecorder, component string) *Recorder {
	return &Recorder{
		recorder:  recorder,
		component: component,
	}
}

// ---- Network Configuration Events ----

// NetworkConfigured records a successful network configuration event
func (r *Recorder) NetworkConfigured(obj runtime.Object, ip, mac string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonNetworkConfigured,
		"Network configured successfully: IP=%s, MAC=%s", ip, mac)
}

// NetworkConfigFailed records a network configuration failure event
func (r *Recorder) NetworkConfigFailed(obj runtime.Object, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonNetworkConfigFailed,
		"Failed to configure network: %v", err)
}

// NetworkDeleted records a successful network deletion event
func (r *Recorder) NetworkDeleted(obj runtime.Object) {
	r.recorder.Event(obj, corev1.EventTypeNormal, ReasonNetworkDeleted,
		"Network configuration deleted successfully")
}

// NetworkDeleteFailed records a network deletion failure event
func (r *Recorder) NetworkDeleteFailed(obj runtime.Object, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonNetworkDeleteFailed,
		"Failed to delete network configuration: %v", err)
}

// NetworkCheckFailed records a network check failure event
func (r *Recorder) NetworkCheckFailed(obj runtime.Object, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonNetworkCheckFailed,
		"Network configuration check failed: %v", err)
}

// ---- IP Allocation Events ----

// IPAllocated records a successful IP allocation event
func (r *Recorder) IPAllocated(obj runtime.Object, ip, subnet string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonIPAllocated,
		"IP address %s allocated from subnet %s", ip, subnet)
}

// IPAllocationFailed records an IP allocation failure event
func (r *Recorder) IPAllocationFailed(obj runtime.Object, subnet string, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonIPAllocationFailed,
		"Failed to allocate IP from subnet %s: %v", subnet, err)
}

// IPReleased records a successful IP release event
func (r *Recorder) IPReleased(obj runtime.Object, ip, subnet string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonIPReleased,
		"IP address %s released to subnet %s", ip, subnet)
}

// IPReleaseFailed records an IP release failure event
func (r *Recorder) IPReleaseFailed(obj runtime.Object, ip string, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonIPReleaseFailed,
		"Failed to release IP address %s: %v", ip, err)
}

// SubnetExhausted records a subnet exhaustion event
func (r *Recorder) SubnetExhausted(obj runtime.Object, subnet string) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonSubnetExhausted,
		"Subnet %s has no available IP addresses", subnet)
}

// ---- OVN Operation Events ----

// OVNOperationFailed records an OVN operation failure event
func (r *Recorder) OVNOperationFailed(obj runtime.Object, operation string, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonOVNOperationFailed,
		"OVN operation '%s' failed: %v", operation, err)
}

// OVNConnectionFailed records an OVN connection failure event
func (r *Recorder) OVNConnectionFailed(obj runtime.Object, database string, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonOVNConnectionFailed,
		"Failed to connect to OVN %s database: %v", database, err)
}

// OVNConnectionRestored records an OVN connection restoration event
func (r *Recorder) OVNConnectionRestored(obj runtime.Object, database string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonOVNConnectionRestored,
		"Connection to OVN %s database restored", database)
}

// OVNTransactionFailed records an OVN transaction failure event
func (r *Recorder) OVNTransactionFailed(obj runtime.Object, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonOVNTransactionFailed,
		"OVN transaction failed: %v", err)
}

// ---- Subnet Events ----

// SubnetCreated records a successful subnet creation event
func (r *Recorder) SubnetCreated(obj runtime.Object, name, cidr string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonSubnetCreated,
		"Subnet %s created with CIDR %s", name, cidr)
}

// SubnetCreateFailed records a subnet creation failure event
func (r *Recorder) SubnetCreateFailed(obj runtime.Object, name string, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonSubnetCreateFailed,
		"Failed to create subnet %s: %v", name, err)
}

// SubnetDeleted records a successful subnet deletion event
func (r *Recorder) SubnetDeleted(obj runtime.Object, name string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonSubnetDeleted,
		"Subnet %s deleted", name)
}

// SubnetDeleteFailed records a subnet deletion failure event
func (r *Recorder) SubnetDeleteFailed(obj runtime.Object, name string, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonSubnetDeleteFailed,
		"Failed to delete subnet %s: %v", name, err)
}

// SubnetValidationFailed records a subnet validation failure event
func (r *Recorder) SubnetValidationFailed(obj runtime.Object, reason string) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonSubnetValidationFailed,
		"Subnet validation failed: %s", reason)
}

// ExternalLSNotFound records an external logical switch not found event
func (r *Recorder) ExternalLSNotFound(obj runtime.Object, lsName string) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonExternalLSNotFound,
		"External logical switch '%s' not found in OVN database", lsName)
}

// ExternalLSFound records an external logical switch found event
func (r *Recorder) ExternalLSFound(obj runtime.Object, lsName string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonExternalLSFound,
		"External logical switch '%s' found and validated", lsName)
}

// ---- Service Events ----

// LoadBalancerCreated records a successful load balancer creation event
func (r *Recorder) LoadBalancerCreated(obj runtime.Object, vip string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonLoadBalancerCreated,
		"OVN load balancer created for VIP %s", vip)
}

// LoadBalancerUpdated records a successful load balancer update event
func (r *Recorder) LoadBalancerUpdated(obj runtime.Object, vip string, backends int) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonLoadBalancerUpdated,
		"OVN load balancer updated for VIP %s with %d backends", vip, backends)
}

// LoadBalancerDeleted records a successful load balancer deletion event
func (r *Recorder) LoadBalancerDeleted(obj runtime.Object, vip string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonLoadBalancerDeleted,
		"OVN load balancer deleted for VIP %s", vip)
}

// LoadBalancerFailed records a load balancer operation failure event
func (r *Recorder) LoadBalancerFailed(obj runtime.Object, operation string, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonLoadBalancerFailed,
		"OVN load balancer %s failed: %v", operation, err)
}

// EndpointsUpdated records a successful endpoints update event
func (r *Recorder) EndpointsUpdated(obj runtime.Object, count int) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonEndpointsUpdated,
		"Service endpoints updated: %d backends", count)
}

// EndpointsUpdateFailed records an endpoints update failure event
func (r *Recorder) EndpointsUpdateFailed(obj runtime.Object, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonEndpointsUpdateFailed,
		"Failed to update service endpoints: %v", err)
}

// ---- NetworkPolicy Events ----

// ACLCreated records a successful ACL creation event
func (r *Recorder) ACLCreated(obj runtime.Object, direction string, priority int) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonACLCreated,
		"OVN ACL created: direction=%s, priority=%d", direction, priority)
}

// ACLDeleted records a successful ACL deletion event
func (r *Recorder) ACLDeleted(obj runtime.Object) {
	r.recorder.Event(obj, corev1.EventTypeNormal, ReasonACLDeleted,
		"OVN ACL deleted")
}

// ACLFailed records an ACL operation failure event
func (r *Recorder) ACLFailed(obj runtime.Object, operation string, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonACLFailed,
		"OVN ACL %s failed: %v", operation, err)
}

// PolicyApplied records a successful policy application event
func (r *Recorder) PolicyApplied(obj runtime.Object, ingressRules, egressRules int) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonPolicyApplied,
		"NetworkPolicy applied: %d ingress rules, %d egress rules", ingressRules, egressRules)
}

// PolicyFailed records a policy application failure event
func (r *Recorder) PolicyFailed(obj runtime.Object, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonPolicyFailed,
		"Failed to apply NetworkPolicy: %v", err)
}

// ---- Node Events ----

// NodeJoined records a node join event
func (r *Recorder) NodeJoined(obj runtime.Object, nodeName, subnet string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonNodeJoined,
		"Node %s joined cluster with subnet %s", nodeName, subnet)
}

// NodeLeft records a node leave event
func (r *Recorder) NodeLeft(obj runtime.Object, nodeName string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonNodeLeft,
		"Node %s left cluster", nodeName)
}

// TunnelConfigured records a successful tunnel configuration event
func (r *Recorder) TunnelConfigured(obj runtime.Object, tunnelType, remoteIP string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonTunnelConfigured,
		"%s tunnel configured to %s", tunnelType, remoteIP)
}

// TunnelConfigFailed records a tunnel configuration failure event
func (r *Recorder) TunnelConfigFailed(obj runtime.Object, tunnelType string, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonTunnelConfigFailed,
		"Failed to configure %s tunnel: %v", tunnelType, err)
}

// GatewayConfigured records a successful gateway configuration event
func (r *Recorder) GatewayConfigured(obj runtime.Object, mode, iface string) {
	r.recorder.Eventf(obj, corev1.EventTypeNormal, ReasonGatewayConfigured,
		"Gateway configured: mode=%s, interface=%s", mode, iface)
}

// GatewayConfigFailed records a gateway configuration failure event
func (r *Recorder) GatewayConfigFailed(obj runtime.Object, err error) {
	r.recorder.Eventf(obj, corev1.EventTypeWarning, ReasonGatewayConfigFailed,
		"Failed to configure gateway: %v", err)
}

// ---- Generic Events ----

// Event records a generic event
func (r *Recorder) Event(obj runtime.Object, eventType, reason, message string) {
	r.recorder.Event(obj, eventType, reason, message)
}

// Eventf records a generic formatted event
func (r *Recorder) Eventf(obj runtime.Object, eventType, reason, format string, args ...interface{}) {
	r.recorder.Eventf(obj, eventType, reason, format, args...)
}

// AnnotatedEventf records an event with annotations
func (r *Recorder) AnnotatedEventf(obj runtime.Object, annotations map[string]string, eventType, reason, format string, args ...interface{}) {
	r.recorder.AnnotatedEventf(obj, annotations, eventType, reason, fmt.Sprintf(format, args...))
}
