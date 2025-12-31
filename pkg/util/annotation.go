// Package util provides Pod annotation handling utilities.
//
// This file implements the PodAnnotation structure and helper functions
// for managing Pod network configuration annotations. The annotation format
// follows the OVN-Kubernetes convention for compatibility.
//
// Annotation Key: k8s.ovn.org/pod-networks
//
// The annotation is set by the Pod controller after allocating IP address
// and creating OVN Logical Switch Port. The CNI handler reads this annotation
// to configure the Pod's network interface.
//
// Reference: OVN-Kubernetes pkg/util/pod_annotation.go
package util

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// PodNetworkAnnotationKey is the annotation key for Pod network configuration.
	// This annotation is set by the controller and read by the CNI handler.
	PodNetworkAnnotationKey = "k8s.ovn.org/pod-networks"

	// PodIPAnnotationKey is a simplified annotation for just the Pod IP.
	PodIPAnnotationKey = "zstack.io/pod-ip"

	// PodMACAnnotationKey is a simplified annotation for just the Pod MAC.
	PodMACAnnotationKey = "zstack.io/pod-mac"

	// PodSubnetAnnotationKey stores the subnet name the Pod belongs to.
	PodSubnetAnnotationKey = "zstack.io/subnet"

	// PodLogicalSwitchAnnotationKey stores the OVN Logical Switch name.
	PodLogicalSwitchAnnotationKey = "zstack.io/logical-switch"

	// PodLogicalSwitchPortAnnotationKey stores the OVN Logical Switch Port name.
	PodLogicalSwitchPortAnnotationKey = "zstack.io/logical-switch-port"
)

// PodAnnotation represents the network configuration stored in Pod annotation.
// This structure is serialized to JSON and stored in the Pod's annotation.
//
// Example JSON:
//
//	{
//	  "ip_addresses": ["10.244.1.5/24"],
//	  "mac_address": "0a:58:0a:f4:01:05",
//	  "gateway_ips": ["10.244.1.1"],
//	  "routes": [],
//	  "logical_switch": "subnet-default",
//	  "logical_switch_port": "default_nginx-pod"
//	}
type PodAnnotation struct {
	// IPAddresses contains the allocated IP addresses with prefix length.
	// Format: ["10.244.1.5/24"] for single-stack, or
	//         ["10.244.1.5/24", "fd00::5/64"] for dual-stack
	IPAddresses []string `json:"ip_addresses"`

	// MACAddress is the allocated MAC address.
	// Format: "0a:58:0a:f4:01:05"
	// Generated using OVN convention: 0a:58:xx:xx:xx:xx where xx is from IP
	MACAddress string `json:"mac_address"`

	// GatewayIPs contains the gateway IP addresses for each IP family.
	// Format: ["10.244.1.1"] for single-stack
	GatewayIPs []string `json:"gateway_ips"`

	// Routes contains additional static routes to configure in the Pod.
	// These are routes beyond the default gateway route.
	Routes []PodRoute `json:"routes,omitempty"`

	// LogicalSwitch is the name of the OVN Logical Switch the Pod is attached to.
	// This is used for cleanup and debugging.
	LogicalSwitch string `json:"logical_switch,omitempty"`

	// LogicalSwitchPort is the name of the OVN Logical Switch Port for this Pod.
	// Format: namespace_podName
	LogicalSwitchPort string `json:"logical_switch_port,omitempty"`

	// Subnet is the name of the Subnet CRD this Pod belongs to.
	Subnet string `json:"subnet,omitempty"`
}

// PodRoute represents a static route to configure in the Pod.
type PodRoute struct {
	// Dest is the destination network in CIDR notation.
	// Example: "192.168.0.0/16"
	Dest string `json:"dest"`

	// NextHop is the next hop IP address.
	// Example: "10.244.1.1"
	NextHop string `json:"nextHop"`
}

// GetPodAnnotation retrieves and parses the Pod network annotation.
//
// Parameters:
//   - pod: The Pod to get annotation from
//
// Returns:
//   - *PodAnnotation: Parsed annotation, nil if not set
//   - error: Parse error if annotation is malformed
//
// Example:
//
//	annotation, err := GetPodAnnotation(pod)
//	if err != nil {
//	    return err
//	}
//	if annotation == nil {
//	    // Annotation not set yet
//	}
func GetPodAnnotation(pod *corev1.Pod) (*PodAnnotation, error) {
	if pod == nil {
		return nil, fmt.Errorf("pod is nil")
	}

	if pod.Annotations == nil {
		return nil, nil
	}

	annotationStr, ok := pod.Annotations[PodNetworkAnnotationKey]
	if !ok || annotationStr == "" {
		return nil, nil
	}

	var annotation PodAnnotation
	if err := json.Unmarshal([]byte(annotationStr), &annotation); err != nil {
		return nil, fmt.Errorf("failed to parse Pod annotation: %w", err)
	}

	return &annotation, nil
}

// SetPodAnnotation sets the Pod network annotation.
// This function modifies the Pod object in-place; the caller must
// update the Pod in Kubernetes API.
//
// Parameters:
//   - pod: The Pod to set annotation on
//   - annotation: The annotation to set
//
// Returns:
//   - error: Serialization error
//
// Example:
//
//	annotation := &PodAnnotation{
//	    IPAddresses: []string{"10.244.1.5/24"},
//	    MACAddress:  "0a:58:0a:f4:01:05",
//	    GatewayIPs:  []string{"10.244.1.1"},
//	}
//	if err := SetPodAnnotation(pod, annotation); err != nil {
//	    return err
//	}
//	// Now update the Pod in Kubernetes API
//	if err := client.Update(ctx, pod); err != nil {
//	    return err
//	}
func SetPodAnnotation(pod *corev1.Pod, annotation *PodAnnotation) error {
	if pod == nil {
		return fmt.Errorf("pod is nil")
	}
	if annotation == nil {
		return fmt.Errorf("annotation is nil")
	}

	annotationBytes, err := json.Marshal(annotation)
	if err != nil {
		return fmt.Errorf("failed to marshal annotation: %w", err)
	}

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	pod.Annotations[PodNetworkAnnotationKey] = string(annotationBytes)

	// Also set simplified annotations for easy access
	if len(annotation.IPAddresses) > 0 {
		// Store IP without prefix for simple lookups
		ip := strings.Split(annotation.IPAddresses[0], "/")[0]
		pod.Annotations[PodIPAnnotationKey] = ip
	}
	if annotation.MACAddress != "" {
		pod.Annotations[PodMACAnnotationKey] = annotation.MACAddress
	}
	if annotation.Subnet != "" {
		pod.Annotations[PodSubnetAnnotationKey] = annotation.Subnet
	}
	if annotation.LogicalSwitch != "" {
		pod.Annotations[PodLogicalSwitchAnnotationKey] = annotation.LogicalSwitch
	}
	if annotation.LogicalSwitchPort != "" {
		pod.Annotations[PodLogicalSwitchPortAnnotationKey] = annotation.LogicalSwitchPort
	}

	return nil
}

// ClearPodAnnotation removes the Pod network annotation.
// This function modifies the Pod object in-place.
//
// Parameters:
//   - pod: The Pod to clear annotation from
func ClearPodAnnotation(pod *corev1.Pod) {
	if pod == nil || pod.Annotations == nil {
		return
	}

	delete(pod.Annotations, PodNetworkAnnotationKey)
	delete(pod.Annotations, PodIPAnnotationKey)
	delete(pod.Annotations, PodMACAnnotationKey)
	delete(pod.Annotations, PodSubnetAnnotationKey)
	delete(pod.Annotations, PodLogicalSwitchAnnotationKey)
	delete(pod.Annotations, PodLogicalSwitchPortAnnotationKey)
}

// HasPodAnnotation checks if the Pod has network annotation set.
//
// Parameters:
//   - pod: The Pod to check
//
// Returns:
//   - bool: True if annotation is set
func HasPodAnnotation(pod *corev1.Pod) bool {
	if pod == nil || pod.Annotations == nil {
		return false
	}
	_, ok := pod.Annotations[PodNetworkAnnotationKey]
	return ok
}

// NewPodAnnotation creates a new PodAnnotation with the given parameters.
//
// Parameters:
//   - ipWithPrefix: IP address with prefix (e.g., "10.244.1.5/24")
//   - mac: MAC address (e.g., "0a:58:0a:f4:01:05")
//   - gateway: Gateway IP (e.g., "10.244.1.1")
//   - subnet: Subnet CRD name
//   - logicalSwitch: OVN Logical Switch name
//   - logicalSwitchPort: OVN Logical Switch Port name
//
// Returns:
//   - *PodAnnotation: New annotation instance
func NewPodAnnotation(ipWithPrefix, mac, gateway, subnet, logicalSwitch, logicalSwitchPort string) *PodAnnotation {
	return &PodAnnotation{
		IPAddresses:       []string{ipWithPrefix},
		MACAddress:        mac,
		GatewayIPs:        []string{gateway},
		Subnet:            subnet,
		LogicalSwitch:     logicalSwitch,
		LogicalSwitchPort: logicalSwitchPort,
	}
}

// Validate validates the PodAnnotation fields.
//
// Returns:
//   - error: Validation error if any field is invalid
func (a *PodAnnotation) Validate() error {
	if len(a.IPAddresses) == 0 {
		return fmt.Errorf("ip_addresses is required")
	}

	// Validate IP addresses
	for _, ipStr := range a.IPAddresses {
		ip, _, err := net.ParseCIDR(ipStr)
		if err != nil {
			// Try parsing as plain IP
			ip = net.ParseIP(strings.Split(ipStr, "/")[0])
			if ip == nil {
				return fmt.Errorf("invalid IP address: %s", ipStr)
			}
		}
	}

	if a.MACAddress == "" {
		return fmt.Errorf("mac_address is required")
	}

	// Validate MAC address format
	if _, err := net.ParseMAC(a.MACAddress); err != nil {
		return fmt.Errorf("invalid MAC address %s: %w", a.MACAddress, err)
	}

	if len(a.GatewayIPs) == 0 {
		return fmt.Errorf("gateway_ips is required")
	}

	// Validate gateway IPs
	for _, gwStr := range a.GatewayIPs {
		gw := net.ParseIP(gwStr)
		if gw == nil {
			return fmt.Errorf("invalid gateway IP: %s", gwStr)
		}
	}

	// Validate routes
	for _, route := range a.Routes {
		_, _, err := net.ParseCIDR(route.Dest)
		if err != nil {
			return fmt.Errorf("invalid route destination %s: %w", route.Dest, err)
		}
		if net.ParseIP(route.NextHop) == nil {
			return fmt.Errorf("invalid route next hop: %s", route.NextHop)
		}
	}

	return nil
}

// GetIP returns the first IP address without prefix.
//
// Returns:
//   - string: IP address without prefix, empty if not set
func (a *PodAnnotation) GetIP() string {
	if len(a.IPAddresses) == 0 {
		return ""
	}
	return strings.Split(a.IPAddresses[0], "/")[0]
}

// GetIPWithPrefix returns the first IP address with prefix.
//
// Returns:
//   - string: IP address with prefix, empty if not set
func (a *PodAnnotation) GetIPWithPrefix() string {
	if len(a.IPAddresses) == 0 {
		return ""
	}
	return a.IPAddresses[0]
}

// GetGateway returns the first gateway IP.
//
// Returns:
//   - string: Gateway IP, empty if not set
func (a *PodAnnotation) GetGateway() string {
	if len(a.GatewayIPs) == 0 {
		return ""
	}
	return a.GatewayIPs[0]
}

// AddRoute adds a route to the annotation.
//
// Parameters:
//   - dest: Destination network in CIDR notation
//   - nextHop: Next hop IP address
func (a *PodAnnotation) AddRoute(dest, nextHop string) {
	a.Routes = append(a.Routes, PodRoute{
		Dest:    dest,
		NextHop: nextHop,
	})
}

// GetPodIP is a convenience function to get the Pod IP from annotation.
// Returns empty string if annotation is not set or invalid.
//
// Parameters:
//   - pod: The Pod to get IP from
//
// Returns:
//   - string: Pod IP address without prefix
func GetPodIP(pod *corev1.Pod) string {
	// First try the simplified annotation
	if pod.Annotations != nil {
		if ip, ok := pod.Annotations[PodIPAnnotationKey]; ok && ip != "" {
			return ip
		}
	}

	// Fall back to parsing the full annotation
	annotation, err := GetPodAnnotation(pod)
	if err != nil || annotation == nil {
		return ""
	}
	return annotation.GetIP()
}

// GetPodMAC is a convenience function to get the Pod MAC from annotation.
// Returns empty string if annotation is not set or invalid.
//
// Parameters:
//   - pod: The Pod to get MAC from
//
// Returns:
//   - string: Pod MAC address
func GetPodMAC(pod *corev1.Pod) string {
	// First try the simplified annotation
	if pod.Annotations != nil {
		if mac, ok := pod.Annotations[PodMACAnnotationKey]; ok && mac != "" {
			return mac
		}
	}

	// Fall back to parsing the full annotation
	annotation, err := GetPodAnnotation(pod)
	if err != nil || annotation == nil {
		return ""
	}
	return annotation.MACAddress
}

// GetPodSubnet is a convenience function to get the Pod's subnet name.
// Returns empty string if annotation is not set.
//
// Parameters:
//   - pod: The Pod to get subnet from
//
// Returns:
//   - string: Subnet CRD name
func GetPodSubnet(pod *corev1.Pod) string {
	if pod.Annotations != nil {
		if subnet, ok := pod.Annotations[PodSubnetAnnotationKey]; ok {
			return subnet
		}
	}

	annotation, err := GetPodAnnotation(pod)
	if err != nil || annotation == nil {
		return ""
	}
	return annotation.Subnet
}
