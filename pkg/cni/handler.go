// Package cni provides the CNI request handler implementation.
//
// This file implements the RequestHandler interface that processes
// CNI ADD, DEL, and CHECK commands. The handler coordinates between:
// - Kubernetes API (to get/update Pod annotations)
// - OVN NB DB (to create/delete Logical Switch Ports)
// - OVS (to configure br-int ports)
// - Linux networking (to create veth pairs and configure IPs)
//
// Request Flow for CNI ADD:
//
//	┌─────────────┐     ┌─────────────┐     ┌─────────────┐
//	│  CNI Binary │────▶│  CNI Server │────▶│   Handler   │
//	└─────────────┘     └─────────────┘     └──────┬──────┘
//	                                               │
//	       ┌───────────────────────────────────────┼───────────────────────────────────────┐
//	       │                                       │                                       │
//	       ▼                                       ▼                                       ▼
//	┌─────────────┐                         ┌─────────────┐                         ┌─────────────┐
//	│ Wait for    │                         │ Create OVN  │                         │ Configure   │
//	│ Pod Annot.  │                         │ LSP         │                         │ Interface   │
//	└─────────────┘                         └─────────────┘                         └─────────────┘
//
// Reference: OVN-Kubernetes pkg/cni/cni.go
package cni

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/util"
)

const (
	// PodAnnotationKey is the annotation key for Pod network configuration
	// This annotation is set by the controller after allocating IP
	PodAnnotationKey = "k8s.ovn.org/pod-networks"

	// PodAnnotationWaitTimeout is the timeout for waiting for Pod annotation
	PodAnnotationWaitTimeout = 60 * time.Second

	// PodAnnotationPollInterval is the interval for polling Pod annotation
	PodAnnotationPollInterval = 500 * time.Millisecond
)

// PodNetworkAnnotation is the structure stored in Pod annotation
// This is set by the controller and read by the CNI handler
type PodNetworkAnnotation struct {
	// IPAddresses contains the allocated IP addresses with prefix
	// Example: ["10.244.1.5/24"]
	IPAddresses []string `json:"ip_addresses"`

	// MACAddress is the allocated MAC address
	// Example: "0a:58:0a:f4:01:05"
	MACAddress string `json:"mac_address"`

	// GatewayIPs contains the gateway IP addresses
	// Example: ["10.244.1.1"]
	GatewayIPs []string `json:"gateway_ips"`

	// Routes contains additional routes
	Routes []Route `json:"routes,omitempty"`

	// LogicalSwitch is the name of the OVN Logical Switch
	LogicalSwitch string `json:"logical_switch,omitempty"`

	// LogicalSwitchPort is the name of the OVN Logical Switch Port
	LogicalSwitchPort string `json:"logical_switch_port,omitempty"`
}

// Handler implements the RequestHandler interface
// It processes CNI requests by coordinating with K8s API, OVN, and OVS
type Handler struct {
	// k8sClient is the Kubernetes client for accessing Pod resources
	k8sClient client.Client

	// ovnClient is the OVN database client
	ovnClient *ovndb.Client

	// mtu is the MTU for Pod interfaces
	mtu int
}

// NewHandler creates a new CNI request handler
//
// Parameters:
//   - k8sClient: Kubernetes client
//   - ovnClient: OVN database client
//   - mtu: MTU for Pod interfaces
//
// Returns:
//   - *Handler: Handler instance
func NewHandler(k8sClient client.Client, ovnClient *ovndb.Client, mtu int) *Handler {
	if mtu == 0 {
		mtu = DefaultMTU
	}
	return &Handler{
		k8sClient: k8sClient,
		ovnClient: ovnClient,
		mtu:       mtu,
	}
}

// HandleAdd handles CNI ADD command
//
// This function:
// 1. Waits for Pod annotation with IP/MAC/Gateway (set by controller)
// 2. Creates OVN Logical Switch Port if not exists
// 3. Configures network interface (veth, IP, routes)
// 4. Adds OVS port to br-int
//
// Parameters:
//   - ctx: Context for cancellation
//   - req: CNI request
//
// Returns:
//   - *PodNetworkInfo: Network configuration for the Pod
//   - error: Configuration error
func (h *Handler) HandleAdd(ctx context.Context, req *Request) (*PodNetworkInfo, error) {
	klog.V(2).Infof("HandleAdd: pod=%s/%s, container=%s",
		req.PodNamespace, req.PodName, req.ContainerID)

	// Wait for Pod annotation with network configuration
	annotation, err := h.waitForPodAnnotation(ctx, req.PodNamespace, req.PodName)
	if err != nil {
		return nil, fmt.Errorf("failed to get Pod annotation: %w", err)
	}

	// Validate annotation
	if len(annotation.IPAddresses) == 0 {
		return nil, fmt.Errorf("no IP addresses in Pod annotation")
	}
	if annotation.MACAddress == "" {
		return nil, fmt.Errorf("no MAC address in Pod annotation")
	}
	if len(annotation.GatewayIPs) == 0 {
		return nil, fmt.Errorf("no gateway IPs in Pod annotation")
	}

	// Use first IP and gateway for now (single-stack)
	ipAddress := annotation.IPAddresses[0]
	gateway := annotation.GatewayIPs[0]

	// Configure network interface
	cfg := &InterfaceConfig{
		PodNamespace: req.PodNamespace,
		PodName:      req.PodName,
		ContainerID:  req.ContainerID,
		NetNS:        req.Netns,
		IfName:       req.IfName,
		IPAddress:    ipAddress,
		MACAddress:   annotation.MACAddress,
		Gateway:      gateway,
		MTU:          h.mtu,
	}

	ifInfo, err := SetupInterface(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to setup interface: %w", err)
	}

	// Build response
	info := &PodNetworkInfo{
		IPAddress:         ipAddress,
		MACAddress:        ifInfo.MACAddress,
		Gateway:           gateway,
		Routes:            annotation.Routes,
		MTU:               h.mtu,
		SandboxID:         req.Netns,
		LogicalSwitchPort: annotation.LogicalSwitchPort,
	}

	klog.V(2).Infof("HandleAdd success: pod=%s/%s, ip=%s, mac=%s",
		req.PodNamespace, req.PodName, ipAddress, ifInfo.MACAddress)

	return info, nil
}

// HandleDel handles CNI DEL command
//
// This function:
// 1. Removes OVS port from br-int
// 2. Deletes veth pair
// 3. Deletes OVN Logical Switch Port
// 4. Releases IP address (done by controller)
//
// DEL is idempotent - it should succeed even if resources are already cleaned up.
//
// Parameters:
//   - ctx: Context for cancellation
//   - req: CNI request
//
// Returns:
//   - error: Cleanup error (nil if already cleaned up)
func (h *Handler) HandleDel(ctx context.Context, req *Request) error {
	klog.V(2).Infof("HandleDel: pod=%s/%s, container=%s",
		req.PodNamespace, req.PodName, req.ContainerID)

	// Clean up network interface (OVS port and veth)
	cfg := &InterfaceConfig{
		PodNamespace: req.PodNamespace,
		PodName:      req.PodName,
		ContainerID:  req.ContainerID,
	}

	if err := TeardownInterface(cfg); err != nil {
		// Log but don't fail - DEL should be idempotent
		klog.Warningf("HandleDel: failed to teardown interface for pod %s/%s: %v",
			req.PodNamespace, req.PodName, err)
	}

	// Delete OVN Logical Switch Port
	// The controller should handle this, but we try here as well for robustness
	portName := ovndb.BuildPortName(req.PodNamespace, req.PodName)
	if h.ovnClient != nil && h.ovnClient.IsConnected() {
		lspOps := ovndb.NewLogicalSwitchPortOps(h.ovnClient)

		// Try to find and delete the LSP
		// We need to find which switch it belongs to first
		lsp, err := lspOps.GetLogicalSwitchPort(ctx, portName)
		if err == nil && lsp != nil {
			// Get the switch name from external_ids or try to find it
			switchName := ""
			if lsp.ExternalIDs != nil {
				switchName = lsp.ExternalIDs["logical_switch"]
			}

			if switchName != "" {
				if err := lspOps.DeleteLogicalSwitchPort(ctx, switchName, portName); err != nil {
					klog.Warningf("HandleDel: failed to delete OVN LSP %s: %v", portName, err)
				} else {
					klog.V(4).Infof("HandleDel: deleted OVN LSP %s", portName)
				}
			}
		}
	}

	klog.V(2).Infof("HandleDel success: pod=%s/%s", req.PodNamespace, req.PodName)
	return nil
}

// HandleCheck handles CNI CHECK command
//
// This function verifies:
// 1. OVS port exists in br-int
// 2. Veth pair exists
// 3. Container interface has correct IP configuration
// 4. OVN LSP exists
//
// Parameters:
//   - ctx: Context for cancellation
//   - req: CNI request
//
// Returns:
//   - error: Check error if configuration is incorrect
func (h *Handler) HandleCheck(ctx context.Context, req *Request) error {
	klog.V(2).Infof("HandleCheck: pod=%s/%s, container=%s",
		req.PodNamespace, req.PodName, req.ContainerID)

	// Get Pod annotation to know expected configuration
	annotation, err := h.getPodAnnotation(ctx, req.PodNamespace, req.PodName)
	if err != nil {
		return fmt.Errorf("failed to get Pod annotation: %w", err)
	}

	if annotation == nil {
		return fmt.Errorf("Pod annotation not found")
	}

	// Validate annotation
	if len(annotation.IPAddresses) == 0 {
		return fmt.Errorf("no IP addresses in Pod annotation")
	}

	// Check interface configuration
	cfg := &InterfaceConfig{
		PodNamespace: req.PodNamespace,
		PodName:      req.PodName,
		ContainerID:  req.ContainerID,
		NetNS:        req.Netns,
		IfName:       req.IfName,
		IPAddress:    annotation.IPAddresses[0],
		MACAddress:   annotation.MACAddress,
	}

	if err := CheckInterface(cfg); err != nil {
		return fmt.Errorf("interface check failed: %w", err)
	}

	// Check OVN LSP exists
	if h.ovnClient != nil && h.ovnClient.IsConnected() {
		portName := ovndb.BuildPortName(req.PodNamespace, req.PodName)
		lspOps := ovndb.NewLogicalSwitchPortOps(h.ovnClient)

		_, err := lspOps.GetLogicalSwitchPort(ctx, portName)
		if err != nil {
			return fmt.Errorf("OVN LSP %s not found: %w", portName, err)
		}
	}

	klog.V(2).Infof("HandleCheck success: pod=%s/%s", req.PodNamespace, req.PodName)
	return nil
}

// waitForPodAnnotation waits for the Pod annotation to be set by the controller
//
// The controller allocates IP address and sets the annotation before the CNI
// plugin is called. However, there may be a race condition, so we poll.
//
// Parameters:
//   - ctx: Context for cancellation
//   - namespace: Pod namespace
//   - name: Pod name
//
// Returns:
//   - *PodNetworkAnnotation: Parsed annotation
//   - error: Timeout or parse error
func (h *Handler) waitForPodAnnotation(ctx context.Context, namespace, name string) (*PodNetworkAnnotation, error) {
	var annotation *PodNetworkAnnotation

	// Create a context with timeout
	waitCtx, cancel := context.WithTimeout(ctx, PodAnnotationWaitTimeout)
	defer cancel()

	err := wait.PollUntilContextCancel(waitCtx, PodAnnotationPollInterval, true, func(ctx context.Context) (bool, error) {
		ann, err := h.getPodAnnotation(ctx, namespace, name)
		if err != nil {
			klog.V(4).Infof("Waiting for Pod annotation %s/%s: %v", namespace, name, err)
			return false, nil // Keep polling
		}
		if ann == nil {
			klog.V(4).Infof("Pod annotation %s/%s not set yet", namespace, name)
			return false, nil // Keep polling
		}
		annotation = ann
		return true, nil // Done
	})

	if err != nil {
		return nil, fmt.Errorf("timeout waiting for Pod annotation: %w", err)
	}

	return annotation, nil
}

// getPodAnnotation gets and parses the Pod network annotation
func (h *Handler) getPodAnnotation(ctx context.Context, namespace, name string) (*PodNetworkAnnotation, error) {
	// Get Pod from Kubernetes API
	pod := &corev1.Pod{}
	if err := h.k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, pod); err != nil {
		return nil, fmt.Errorf("failed to get Pod: %w", err)
	}

	// Check for annotation
	annotationStr, ok := pod.Annotations[PodAnnotationKey]
	if !ok || annotationStr == "" {
		return nil, nil // Annotation not set yet
	}

	// Parse annotation
	var annotation PodNetworkAnnotation
	if err := json.Unmarshal([]byte(annotationStr), &annotation); err != nil {
		return nil, fmt.Errorf("failed to parse Pod annotation: %w", err)
	}

	return &annotation, nil
}

// SetPodAnnotation sets the Pod network annotation
// This is typically called by the controller, not the CNI handler
func SetPodAnnotation(ctx context.Context, k8sClient client.Client, namespace, name string, annotation *PodNetworkAnnotation) error {
	// Marshal annotation to JSON
	annotationBytes, err := json.Marshal(annotation)
	if err != nil {
		return fmt.Errorf("failed to marshal annotation: %w", err)
	}

	// Get Pod
	pod := &corev1.Pod{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, pod); err != nil {
		return fmt.Errorf("failed to get Pod: %w", err)
	}

	// Update annotation
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[PodAnnotationKey] = string(annotationBytes)

	// Update Pod
	if err := k8sClient.Update(ctx, pod); err != nil {
		return fmt.Errorf("failed to update Pod annotation: %w", err)
	}

	return nil
}

// BuildPodNetworkAnnotation builds a PodNetworkAnnotation from network info
func BuildPodNetworkAnnotation(ipAddress, macAddress, gateway, logicalSwitch, logicalSwitchPort string) *PodNetworkAnnotation {
	return &PodNetworkAnnotation{
		IPAddresses:       []string{ipAddress},
		MACAddress:        macAddress,
		GatewayIPs:        []string{gateway},
		LogicalSwitch:     logicalSwitch,
		LogicalSwitchPort: logicalSwitchPort,
	}
}

// GenerateMACFromIP generates a MAC address from an IP address
// Uses the OVN convention: 0a:58:xx:xx:xx:xx where xx is derived from IP
func GenerateMACFromIP(ipStr string) (string, error) {
	// Parse IP address (may include prefix)
	ipStr = strings.Split(ipStr, "/")[0]
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "", fmt.Errorf("invalid IP address: %s", ipStr)
	}

	return util.GenerateMAC(ip), nil
}
