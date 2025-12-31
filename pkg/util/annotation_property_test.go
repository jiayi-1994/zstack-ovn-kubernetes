// Package util provides Pod annotation handling utilities.
//
// Property-based tests for Pod Annotation.
// Feature: zstack-ovn-kubernetes-cni, Property 8: Pod Annotation 完整性
// Validates: Requirements 4.6
package util

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestProperty_PodAnnotationCompleteness verifies that Pod annotations contain all required fields.
// Property 8: For any successfully configured Pod, the annotation should contain
// ip_addresses, mac_address, and gateway_ips fields, and all values should match
// the actual network configuration.
func TestProperty_PodAnnotationCompleteness(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("Pod annotation contains all required fields", prop.ForAll(
		func(lastOctet int, prefix int, gwLastOctet int) bool {
			// Generate valid IP with prefix
			ipWithPrefix := fmt.Sprintf("10.244.1.%d/%d", lastOctet, prefix)
			// Generate MAC address based on IP (OVN convention)
			mac := fmt.Sprintf("0a:58:0a:f4:01:%02x", lastOctet)
			// Generate gateway IP
			gateway := fmt.Sprintf("10.244.1.%d", gwLastOctet)

			// Create annotation
			annotation := NewPodAnnotation(
				ipWithPrefix,
				mac,
				gateway,
				"test-subnet",
				"ls-test",
				"default_test-pod",
			)

			// Verify all required fields are present
			if len(annotation.IPAddresses) == 0 {
				t.Logf("ip_addresses is empty")
				return false
			}
			if annotation.MACAddress == "" {
				t.Logf("mac_address is empty")
				return false
			}
			if len(annotation.GatewayIPs) == 0 {
				t.Logf("gateway_ips is empty")
				return false
			}

			// Verify values match input
			if annotation.IPAddresses[0] != ipWithPrefix {
				t.Logf("IP mismatch: expected %s, got %s", ipWithPrefix, annotation.IPAddresses[0])
				return false
			}
			if annotation.MACAddress != mac {
				t.Logf("MAC mismatch: expected %s, got %s", mac, annotation.MACAddress)
				return false
			}
			if annotation.GatewayIPs[0] != gateway {
				t.Logf("Gateway mismatch: expected %s, got %s", gateway, annotation.GatewayIPs[0])
				return false
			}

			return true
		},
		gen.IntRange(2, 254),  // Last octet of IP (avoid 0, 1, 255)
		gen.IntRange(16, 30),  // Prefix length
		gen.IntRange(1, 254),  // Gateway last octet
	))

	properties.TestingRun(t)
}

// TestProperty_AnnotationRoundTrip verifies that annotation serialization is lossless.
// Property: For any valid PodAnnotation, serializing to JSON and deserializing
// should produce an equivalent annotation.
func TestProperty_AnnotationRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("annotation round-trip preserves all fields", prop.ForAll(
		func(lastOctet int, prefix int, gwLastOctet int, subnetIdx int, lsIdx int) bool {
			// Use predefined names to avoid generator issues with string filtering
			subnetNames := []string{"subnet-a", "subnet-b", "subnet-c", "default", "test"}
			lsNames := []string{"ls-default", "ls-test", "ls-prod", "ls-dev", "ls-staging"}

			subnetName := subnetNames[subnetIdx%len(subnetNames)]
			lsName := lsNames[lsIdx%len(lsNames)]

			// Generate valid annotation
			ipWithPrefix := fmt.Sprintf("10.244.1.%d/%d", lastOctet, prefix)
			mac := fmt.Sprintf("0a:58:0a:f4:01:%02x", lastOctet)
			gateway := fmt.Sprintf("10.244.1.%d", gwLastOctet)
			lspName := fmt.Sprintf("default_%s-pod", subnetName)

			original := &PodAnnotation{
				IPAddresses:       []string{ipWithPrefix},
				MACAddress:        mac,
				GatewayIPs:        []string{gateway},
				Subnet:            subnetName,
				LogicalSwitch:     lsName,
				LogicalSwitchPort: lspName,
			}

			// Serialize to JSON
			jsonBytes, err := json.Marshal(original)
			if err != nil {
				t.Logf("Failed to marshal: %v", err)
				return false
			}

			// Deserialize from JSON
			var restored PodAnnotation
			if err := json.Unmarshal(jsonBytes, &restored); err != nil {
				t.Logf("Failed to unmarshal: %v", err)
				return false
			}

			// Verify all fields match
			if len(restored.IPAddresses) != len(original.IPAddresses) {
				t.Logf("IPAddresses length mismatch")
				return false
			}
			if len(restored.IPAddresses) > 0 && restored.IPAddresses[0] != original.IPAddresses[0] {
				t.Logf("IPAddresses[0] mismatch: %s vs %s", restored.IPAddresses[0], original.IPAddresses[0])
				return false
			}
			if restored.MACAddress != original.MACAddress {
				t.Logf("MACAddress mismatch")
				return false
			}
			if len(restored.GatewayIPs) != len(original.GatewayIPs) {
				t.Logf("GatewayIPs length mismatch")
				return false
			}
			if len(restored.GatewayIPs) > 0 && restored.GatewayIPs[0] != original.GatewayIPs[0] {
				t.Logf("GatewayIPs[0] mismatch")
				return false
			}
			if restored.Subnet != original.Subnet {
				t.Logf("Subnet mismatch")
				return false
			}
			if restored.LogicalSwitch != original.LogicalSwitch {
				t.Logf("LogicalSwitch mismatch")
				return false
			}
			if restored.LogicalSwitchPort != original.LogicalSwitchPort {
				t.Logf("LogicalSwitchPort mismatch")
				return false
			}

			return true
		},
		gen.IntRange(2, 254),
		gen.IntRange(16, 30),
		gen.IntRange(1, 254),
		gen.IntRange(0, 100), // Index for subnet name
		gen.IntRange(0, 100), // Index for logical switch name
	))

	properties.TestingRun(t)
}

// TestProperty_SetGetAnnotationConsistency verifies that SetPodAnnotation and GetPodAnnotation are consistent.
// Property: For any valid annotation, setting it on a Pod and then getting it back
// should return an equivalent annotation.
func TestProperty_SetGetAnnotationConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("SetPodAnnotation and GetPodAnnotation are consistent", prop.ForAll(
		func(lastOctet int, prefix int, gwLastOctet int) bool {
			// Create a Pod
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
			}

			// Generate valid annotation
			ipWithPrefix := fmt.Sprintf("10.244.1.%d/%d", lastOctet, prefix)
			mac := fmt.Sprintf("0a:58:0a:f4:01:%02x", lastOctet)
			gateway := fmt.Sprintf("10.244.1.%d", gwLastOctet)

			original := &PodAnnotation{
				IPAddresses:       []string{ipWithPrefix},
				MACAddress:        mac,
				GatewayIPs:        []string{gateway},
				Subnet:            "test-subnet",
				LogicalSwitch:     "ls-test",
				LogicalSwitchPort: "default_test-pod",
			}

			// Set annotation
			if err := SetPodAnnotation(pod, original); err != nil {
				t.Logf("Failed to set annotation: %v", err)
				return false
			}

			// Get annotation back
			retrieved, err := GetPodAnnotation(pod)
			if err != nil {
				t.Logf("Failed to get annotation: %v", err)
				return false
			}
			if retrieved == nil {
				t.Logf("Retrieved annotation is nil")
				return false
			}

			// Verify all fields match
			if len(retrieved.IPAddresses) == 0 || retrieved.IPAddresses[0] != original.IPAddresses[0] {
				t.Logf("IPAddresses mismatch")
				return false
			}
			if retrieved.MACAddress != original.MACAddress {
				t.Logf("MACAddress mismatch")
				return false
			}
			if len(retrieved.GatewayIPs) == 0 || retrieved.GatewayIPs[0] != original.GatewayIPs[0] {
				t.Logf("GatewayIPs mismatch")
				return false
			}

			return true
		},
		gen.IntRange(2, 254),
		gen.IntRange(16, 30),
		gen.IntRange(1, 254),
	))

	properties.TestingRun(t)
}

// TestProperty_SimplifiedAnnotationsConsistency verifies that simplified annotations match the full annotation.
// Property: After SetPodAnnotation, the simplified annotations (PodIPAnnotationKey, PodMACAnnotationKey, etc.)
// should contain values consistent with the full annotation.
func TestProperty_SimplifiedAnnotationsConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("simplified annotations are consistent with full annotation", prop.ForAll(
		func(lastOctet int, prefix int, gwLastOctet int) bool {
			// Create a Pod
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
			}

			// Generate valid annotation
			ipWithPrefix := fmt.Sprintf("10.244.1.%d/%d", lastOctet, prefix)
			ipWithoutPrefix := fmt.Sprintf("10.244.1.%d", lastOctet)
			mac := fmt.Sprintf("0a:58:0a:f4:01:%02x", lastOctet)
			gateway := fmt.Sprintf("10.244.1.%d", gwLastOctet)
			subnetName := "test-subnet"
			lsName := "ls-test"
			lspName := "default_test-pod"

			annotation := &PodAnnotation{
				IPAddresses:       []string{ipWithPrefix},
				MACAddress:        mac,
				GatewayIPs:        []string{gateway},
				Subnet:            subnetName,
				LogicalSwitch:     lsName,
				LogicalSwitchPort: lspName,
			}

			// Set annotation
			if err := SetPodAnnotation(pod, annotation); err != nil {
				t.Logf("Failed to set annotation: %v", err)
				return false
			}

			// Verify simplified annotations
			if pod.Annotations[PodIPAnnotationKey] != ipWithoutPrefix {
				t.Logf("PodIPAnnotationKey mismatch: expected %s, got %s",
					ipWithoutPrefix, pod.Annotations[PodIPAnnotationKey])
				return false
			}
			if pod.Annotations[PodMACAnnotationKey] != mac {
				t.Logf("PodMACAnnotationKey mismatch")
				return false
			}
			if pod.Annotations[PodSubnetAnnotationKey] != subnetName {
				t.Logf("PodSubnetAnnotationKey mismatch")
				return false
			}
			if pod.Annotations[PodLogicalSwitchAnnotationKey] != lsName {
				t.Logf("PodLogicalSwitchAnnotationKey mismatch")
				return false
			}
			if pod.Annotations[PodLogicalSwitchPortAnnotationKey] != lspName {
				t.Logf("PodLogicalSwitchPortAnnotationKey mismatch")
				return false
			}

			return true
		},
		gen.IntRange(2, 254),
		gen.IntRange(16, 30),
		gen.IntRange(1, 254),
	))

	properties.TestingRun(t)
}

// TestProperty_ValidationRejectsInvalidAnnotations verifies that Validate() correctly rejects invalid annotations.
// Property: Annotations with missing required fields or invalid formats should fail validation.
func TestProperty_ValidationRejectsInvalidAnnotations(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("validation rejects annotations with empty IP addresses", prop.ForAll(
		func(lastOctet int) bool {
			mac := fmt.Sprintf("0a:58:0a:f4:01:%02x", lastOctet)
			gateway := fmt.Sprintf("10.244.1.%d", lastOctet)

			annotation := &PodAnnotation{
				IPAddresses: []string{}, // Empty - should fail
				MACAddress:  mac,
				GatewayIPs:  []string{gateway},
			}

			err := annotation.Validate()
			if err == nil {
				t.Logf("Expected validation to fail for empty IPAddresses")
				return false
			}
			return true
		},
		gen.IntRange(1, 254),
	))

	properties.Property("validation rejects annotations with empty MAC address", prop.ForAll(
		func(lastOctet int, prefix int) bool {
			ipWithPrefix := fmt.Sprintf("10.244.1.%d/%d", lastOctet, prefix)
			gateway := fmt.Sprintf("10.244.1.%d", lastOctet)

			annotation := &PodAnnotation{
				IPAddresses: []string{ipWithPrefix},
				MACAddress:  "", // Empty - should fail
				GatewayIPs:  []string{gateway},
			}

			err := annotation.Validate()
			if err == nil {
				t.Logf("Expected validation to fail for empty MACAddress")
				return false
			}
			return true
		},
		gen.IntRange(2, 254),
		gen.IntRange(16, 30),
	))

	properties.Property("validation rejects annotations with empty gateway IPs", prop.ForAll(
		func(lastOctet int, prefix int) bool {
			ipWithPrefix := fmt.Sprintf("10.244.1.%d/%d", lastOctet, prefix)
			mac := fmt.Sprintf("0a:58:0a:f4:01:%02x", lastOctet)

			annotation := &PodAnnotation{
				IPAddresses: []string{ipWithPrefix},
				MACAddress:  mac,
				GatewayIPs:  []string{}, // Empty - should fail
			}

			err := annotation.Validate()
			if err == nil {
				t.Logf("Expected validation to fail for empty GatewayIPs")
				return false
			}
			return true
		},
		gen.IntRange(2, 254),
		gen.IntRange(16, 30),
	))

	properties.TestingRun(t)
}

// TestProperty_ValidAnnotationPassesValidation verifies that valid annotations pass validation.
// Property: Annotations with all required fields in valid format should pass validation.
func TestProperty_ValidAnnotationPassesValidation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("valid annotations pass validation", prop.ForAll(
		func(lastOctet int, prefix int, gwLastOctet int) bool {
			ipWithPrefix := fmt.Sprintf("10.244.1.%d/%d", lastOctet, prefix)
			mac := fmt.Sprintf("0a:58:0a:f4:01:%02x", lastOctet)
			gateway := fmt.Sprintf("10.244.1.%d", gwLastOctet)

			annotation := &PodAnnotation{
				IPAddresses: []string{ipWithPrefix},
				MACAddress:  mac,
				GatewayIPs:  []string{gateway},
			}

			err := annotation.Validate()
			if err != nil {
				t.Logf("Expected validation to pass, got error: %v", err)
				return false
			}
			return true
		},
		gen.IntRange(2, 254),
		gen.IntRange(16, 30),
		gen.IntRange(1, 254),
	))

	properties.TestingRun(t)
}

// TestProperty_GetIPHelperConsistency verifies that GetIP() returns the correct IP without prefix.
// Property: GetIP() should return the IP address without the prefix length.
func TestProperty_GetIPHelperConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("GetIP returns IP without prefix", prop.ForAll(
		func(lastOctet int, prefix int) bool {
			ipWithPrefix := fmt.Sprintf("10.244.1.%d/%d", lastOctet, prefix)
			ipWithoutPrefix := fmt.Sprintf("10.244.1.%d", lastOctet)

			annotation := &PodAnnotation{
				IPAddresses: []string{ipWithPrefix},
			}

			result := annotation.GetIP()
			if result != ipWithoutPrefix {
				t.Logf("GetIP mismatch: expected %s, got %s", ipWithoutPrefix, result)
				return false
			}
			return true
		},
		gen.IntRange(0, 255),
		gen.IntRange(16, 30),
	))

	properties.TestingRun(t)
}

// TestProperty_ClearAnnotationRemovesAllKeys verifies that ClearPodAnnotation removes all annotation keys.
// Property: After ClearPodAnnotation, all Pod network annotation keys should be removed.
func TestProperty_ClearAnnotationRemovesAllKeys(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("ClearPodAnnotation removes all network annotation keys", prop.ForAll(
		func(lastOctet int, prefix int, gwLastOctet int) bool {
			// Create a Pod with annotation
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
			}

			ipWithPrefix := fmt.Sprintf("10.244.1.%d/%d", lastOctet, prefix)
			mac := fmt.Sprintf("0a:58:0a:f4:01:%02x", lastOctet)
			gateway := fmt.Sprintf("10.244.1.%d", gwLastOctet)

			annotation := &PodAnnotation{
				IPAddresses:       []string{ipWithPrefix},
				MACAddress:        mac,
				GatewayIPs:        []string{gateway},
				Subnet:            "test-subnet",
				LogicalSwitch:     "ls-test",
				LogicalSwitchPort: "default_test-pod",
			}

			// Set annotation
			if err := SetPodAnnotation(pod, annotation); err != nil {
				t.Logf("Failed to set annotation: %v", err)
				return false
			}

			// Verify annotation is set
			if !HasPodAnnotation(pod) {
				t.Logf("Annotation should be set")
				return false
			}

			// Clear annotation
			ClearPodAnnotation(pod)

			// Verify all keys are removed
			if HasPodAnnotation(pod) {
				t.Logf("HasPodAnnotation should return false after clear")
				return false
			}
			if _, ok := pod.Annotations[PodIPAnnotationKey]; ok {
				t.Logf("PodIPAnnotationKey should be removed")
				return false
			}
			if _, ok := pod.Annotations[PodMACAnnotationKey]; ok {
				t.Logf("PodMACAnnotationKey should be removed")
				return false
			}
			if _, ok := pod.Annotations[PodSubnetAnnotationKey]; ok {
				t.Logf("PodSubnetAnnotationKey should be removed")
				return false
			}
			if _, ok := pod.Annotations[PodLogicalSwitchAnnotationKey]; ok {
				t.Logf("PodLogicalSwitchAnnotationKey should be removed")
				return false
			}
			if _, ok := pod.Annotations[PodLogicalSwitchPortAnnotationKey]; ok {
				t.Logf("PodLogicalSwitchPortAnnotationKey should be removed")
				return false
			}

			return true
		},
		gen.IntRange(2, 254),
		gen.IntRange(16, 30),
		gen.IntRange(1, 254),
	))

	properties.TestingRun(t)
}

// TestProperty_ConvenienceFunctionsConsistency verifies that GetPodIP and GetPodMAC return correct values.
// Property: GetPodIP and GetPodMAC should return values consistent with the annotation.
func TestProperty_ConvenienceFunctionsConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("GetPodIP and GetPodMAC return correct values", prop.ForAll(
		func(lastOctet int, prefix int, gwLastOctet int) bool {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
			}

			ipWithPrefix := fmt.Sprintf("10.244.1.%d/%d", lastOctet, prefix)
			ipWithoutPrefix := fmt.Sprintf("10.244.1.%d", lastOctet)
			mac := fmt.Sprintf("0a:58:0a:f4:01:%02x", lastOctet)
			gateway := fmt.Sprintf("10.244.1.%d", gwLastOctet)

			annotation := &PodAnnotation{
				IPAddresses: []string{ipWithPrefix},
				MACAddress:  mac,
				GatewayIPs:  []string{gateway},
			}

			if err := SetPodAnnotation(pod, annotation); err != nil {
				t.Logf("Failed to set annotation: %v", err)
				return false
			}

			// Verify GetPodIP
			if GetPodIP(pod) != ipWithoutPrefix {
				t.Logf("GetPodIP mismatch: expected %s, got %s", ipWithoutPrefix, GetPodIP(pod))
				return false
			}

			// Verify GetPodMAC
			if GetPodMAC(pod) != mac {
				t.Logf("GetPodMAC mismatch: expected %s, got %s", mac, GetPodMAC(pod))
				return false
			}

			return true
		},
		gen.IntRange(2, 254),
		gen.IntRange(16, 30),
		gen.IntRange(1, 254),
	))

	properties.TestingRun(t)
}

// Ensure net and strings packages are used (for validation tests)
var _ = net.ParseIP
var _ = strings.Split
