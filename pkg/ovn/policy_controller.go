// Package ovn provides the NetworkPolicy controller implementation.
//
// This file implements the NetworkPolicy controller which translates Kubernetes
// NetworkPolicy resources into OVN ACL (Access Control List) rules.
//
// NetworkPolicy to OVN ACL Mapping:
// - Ingress rules map to ACLs with direction "to-lport"
// - Egress rules map to ACLs with direction "from-lport"
// - Default deny rules are created when a policy selects pods
// - Allow rules are created for each rule in the policy
//
// Priority Scheme:
// - Default deny rules: 1000 (lower priority)
// - Allow rules: 1001+ (higher priority, evaluated first)
//
// Reference: OVN-Kubernetes pkg/ovn/controller/network_policy.go
package ovn

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
)

// Direction constants for NetworkPolicy rules
const (
	DirectionIngress = "Ingress"
	DirectionEgress  = "Egress"
)

// ACL Priority constants
// Higher priority rules are evaluated first in OVN
const (
	// ACLPriorityDefaultDenyIngress is the priority for default deny ingress rules
	ACLPriorityDefaultDenyIngress = 1000

	// ACLPriorityDefaultDenyEgress is the priority for default deny egress rules
	ACLPriorityDefaultDenyEgress = 1000

	// ACLPriorityAllowRuleBase is the base priority for allow rules
	// Allow rules have higher priority than default deny
	ACLPriorityAllowRuleBase = 1001
)

// PolicyController reconciles NetworkPolicy objects
// It watches NetworkPolicy resources and creates corresponding OVN ACLs
type PolicyController struct {
	client.Client
	Scheme    *runtime.Scheme
	OVNClient *ovndb.Client
}

// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies/status,verbs=get
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// Reconcile handles NetworkPolicy create/update/delete events
//
// The reconciliation logic:
//  1. Get the NetworkPolicy
//  2. If deleted, clean up all associated ACLs
//  3. If created/updated:
//     a. Find all pods selected by the policy
//     b. Create default deny ACLs for selected pods
//     c. Create allow ACLs for each ingress/egress rule
func (r *PolicyController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling NetworkPolicy", "namespace", req.Namespace, "name", req.Name)

	// Get the NetworkPolicy
	var policy networkingv1.NetworkPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if errors.IsNotFound(err) {
			// Policy was deleted, clean up ACLs
			logger.Info("NetworkPolicy deleted, cleaning up ACLs")
			return r.cleanupACLs(ctx, req.Namespace, req.Name)
		}
		logger.Error(err, "Failed to get NetworkPolicy")
		return ctrl.Result{}, err
	}

	// Process the NetworkPolicy
	if err := r.processNetworkPolicy(ctx, &policy); err != nil {
		logger.Error(err, "Failed to process NetworkPolicy")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled NetworkPolicy")
	return ctrl.Result{}, nil
}

// processNetworkPolicy creates OVN ACLs for a NetworkPolicy
func (r *PolicyController) processNetworkPolicy(ctx context.Context, policy *networkingv1.NetworkPolicy) error {
	logger := log.FromContext(ctx)

	// Find pods selected by this policy
	selectedPods, err := r.getSelectedPods(ctx, policy)
	if err != nil {
		return fmt.Errorf("failed to get selected pods: %w", err)
	}

	if len(selectedPods) == 0 {
		logger.Info("No pods selected by NetworkPolicy")
		return nil
	}

	// Get IPs of selected pods
	selectedPodIPs := getPodIPs(selectedPods)

	// Clean up existing ACLs for this policy
	if _, err := r.cleanupACLs(ctx, policy.Namespace, policy.Name); err != nil {
		return fmt.Errorf("failed to cleanup existing ACLs: %w", err)
	}

	// Create ACLs based on policy types
	for _, policyType := range policy.Spec.PolicyTypes {
		switch policyType {
		case networkingv1.PolicyTypeIngress:
			if err := r.createIngressACLs(ctx, policy, selectedPodIPs); err != nil {
				return fmt.Errorf("failed to create ingress ACLs: %w", err)
			}
		case networkingv1.PolicyTypeEgress:
			if err := r.createEgressACLs(ctx, policy, selectedPodIPs); err != nil {
				return fmt.Errorf("failed to create egress ACLs: %w", err)
			}
		}
	}

	return nil
}

// getSelectedPods returns pods that match the policy's pod selector
func (r *PolicyController) getSelectedPods(ctx context.Context, policy *networkingv1.NetworkPolicy) ([]corev1.Pod, error) {
	selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid pod selector: %w", err)
	}

	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(policy.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}

	return podList.Items, nil
}

// getPodIPs extracts IP addresses from pods
func getPodIPs(pods []corev1.Pod) []string {
	var ips []string
	for _, pod := range pods {
		if pod.Status.PodIP != "" {
			ips = append(ips, pod.Status.PodIP)
		}
	}
	return ips
}

// createIngressACLs creates ACLs for ingress rules
func (r *PolicyController) createIngressACLs(ctx context.Context, policy *networkingv1.NetworkPolicy, selectedPodIPs []string) error {
	aclOps := ovndb.NewACLOps(r.OVNClient)

	// Create default deny ACL for ingress
	defaultDenyName := BuildPolicyACLName(policy.Namespace, policy.Name, DirectionIngress, "default")
	defaultDenyMatch := buildDefaultDenyMatch(selectedPodIPs, DirectionIngress)

	if defaultDenyMatch != "" {
		_, err := aclOps.CreateACL(
			ctx,
			&defaultDenyName,
			ovndb.ACLDirectionToLport,
			ACLPriorityDefaultDenyIngress,
			defaultDenyMatch,
			ovndb.ACLActionDrop,
			buildPolicyExternalIDs(policy.Namespace, policy.Name, DirectionIngress),
		)
		if err != nil {
			return fmt.Errorf("failed to create default deny ingress ACL: %w", err)
		}
	}

	// Create allow ACLs for each ingress rule
	for ruleIdx, rule := range policy.Spec.Ingress {
		sourceCIDRs := getIngressSourceCIDRs(ctx, r.Client, policy.Namespace, rule.From)

		peerCount := getIngressPeerCount(rule.From)
		for peerIdx := 0; peerIdx < peerCount; peerIdx++ {
			aclName := BuildPolicyACLName(policy.Namespace, policy.Name, DirectionIngress, fmt.Sprintf("%d_%d", ruleIdx, peerIdx))
			match := ConvertIngressRuleToACLMatch(selectedPodIPs, sourceCIDRs, rule.Ports)

			if match != "" {
				priority := GetACLPriority(DirectionIngress, false, ruleIdx)
				_, err := aclOps.CreateACL(
					ctx,
					&aclName,
					ovndb.ACLDirectionToLport,
					priority,
					match,
					ovndb.ACLActionAllow,
					buildPolicyExternalIDs(policy.Namespace, policy.Name, DirectionIngress),
				)
				if err != nil {
					return fmt.Errorf("failed to create ingress allow ACL: %w", err)
				}
			}
		}
	}

	return nil
}

// createEgressACLs creates ACLs for egress rules
func (r *PolicyController) createEgressACLs(ctx context.Context, policy *networkingv1.NetworkPolicy, selectedPodIPs []string) error {
	aclOps := ovndb.NewACLOps(r.OVNClient)

	// Create default deny ACL for egress
	defaultDenyName := BuildPolicyACLName(policy.Namespace, policy.Name, DirectionEgress, "default")
	defaultDenyMatch := buildDefaultDenyMatch(selectedPodIPs, DirectionEgress)

	if defaultDenyMatch != "" {
		_, err := aclOps.CreateACL(
			ctx,
			&defaultDenyName,
			ovndb.ACLDirectionFromLport,
			ACLPriorityDefaultDenyEgress,
			defaultDenyMatch,
			ovndb.ACLActionDrop,
			buildPolicyExternalIDs(policy.Namespace, policy.Name, DirectionEgress),
		)
		if err != nil {
			return fmt.Errorf("failed to create default deny egress ACL: %w", err)
		}
	}

	// Create allow ACLs for each egress rule
	for ruleIdx, rule := range policy.Spec.Egress {
		destCIDRs := getEgressDestCIDRs(ctx, r.Client, policy.Namespace, rule.To)

		peerCount := getEgressPeerCount(rule.To)
		for peerIdx := 0; peerIdx < peerCount; peerIdx++ {
			aclName := BuildPolicyACLName(policy.Namespace, policy.Name, DirectionEgress, fmt.Sprintf("%d_%d", ruleIdx, peerIdx))
			match := ConvertEgressRuleToACLMatch(selectedPodIPs, destCIDRs, rule.Ports)

			if match != "" {
				priority := GetACLPriority(DirectionEgress, false, ruleIdx)
				_, err := aclOps.CreateACL(
					ctx,
					&aclName,
					ovndb.ACLDirectionFromLport,
					priority,
					match,
					ovndb.ACLActionAllow,
					buildPolicyExternalIDs(policy.Namespace, policy.Name, DirectionEgress),
				)
				if err != nil {
					return fmt.Errorf("failed to create egress allow ACL: %w", err)
				}
			}
		}
	}

	return nil
}

// cleanupACLs removes all ACLs associated with a NetworkPolicy
func (r *PolicyController) cleanupACLs(ctx context.Context, namespace, name string) (ctrl.Result, error) {
	aclOps := ovndb.NewACLOps(r.OVNClient)
	if err := aclOps.DeleteACLsByPolicy(ctx, namespace, name); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to delete ACLs: %w", err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *PolicyController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1.NetworkPolicy{}).
		Complete(r)
}

// ============================================================================
// Helper Functions for ACL Building
// ============================================================================

// BuildPolicyACLName builds a unique ACL name for a NetworkPolicy rule
//
// Format: np_<namespace>/<policyName>_<direction>_<index>
//
// Parameters:
//   - namespace: Policy namespace
//   - policyName: Policy name
//   - direction: "Ingress" or "Egress"
//   - index: Rule index or "default" for default deny
//
// Returns:
//   - string: ACL name
func BuildPolicyACLName(namespace, policyName, direction, index string) string {
	return fmt.Sprintf("np_%s/%s_%s_%s",
		namespace,
		policyName,
		strings.ToLower(direction),
		index,
	)
}

// GetACLPriority returns the ACL priority for a rule
//
// Parameters:
//   - direction: "Ingress" or "Egress"
//   - isDefaultDeny: true for default deny rules
//   - ruleIndex: Index of the rule (0-based)
//
// Returns:
//   - int: ACL priority
func GetACLPriority(direction string, isDefaultDeny bool, ruleIndex int) int {
	if isDefaultDeny {
		if direction == DirectionIngress {
			return ACLPriorityDefaultDenyIngress
		}
		return ACLPriorityDefaultDenyEgress
	}
	return ACLPriorityAllowRuleBase + ruleIndex
}

// GetACLDirection maps NetworkPolicy direction to OVN ACL direction
//
// Parameters:
//   - direction: "Ingress" or "Egress"
//
// Returns:
//   - string: OVN ACL direction ("to-lport" or "from-lport")
func GetACLDirection(direction string) string {
	if direction == DirectionIngress {
		return ovndb.ACLDirectionToLport
	}
	return ovndb.ACLDirectionFromLport
}

// ConvertIngressRuleToACLMatch converts an ingress rule to an OVN ACL match expression
//
// Parameters:
//   - selectedPodIPs: IPs of pods selected by the policy
//   - sourceCIDRs: Source CIDRs from the ingress rule
//   - ports: Ports from the ingress rule
//
// Returns:
//   - string: OVN match expression
func ConvertIngressRuleToACLMatch(selectedPodIPs, sourceCIDRs []string, ports []networkingv1.NetworkPolicyPort) string {
	var conditions []string

	// Build destination match (selected pods)
	destMatch := buildIPListMatch("ip4.dst", selectedPodIPs)
	if destMatch != "" {
		conditions = append(conditions, destMatch)
	}

	// Build source match
	srcMatch := buildIPListMatch("ip4.src", sourceCIDRs)
	if srcMatch != "" {
		conditions = append(conditions, srcMatch)
	}

	// Build port match
	portMatch := buildPortMatch(ports)
	if portMatch != "" {
		conditions = append(conditions, portMatch)
	}

	return strings.Join(conditions, " && ")
}

// ConvertEgressRuleToACLMatch converts an egress rule to an OVN ACL match expression
//
// Parameters:
//   - selectedPodIPs: IPs of pods selected by the policy
//   - destCIDRs: Destination CIDRs from the egress rule
//   - ports: Ports from the egress rule
//
// Returns:
//   - string: OVN match expression
func ConvertEgressRuleToACLMatch(selectedPodIPs, destCIDRs []string, ports []networkingv1.NetworkPolicyPort) string {
	var conditions []string

	// Build source match (selected pods)
	srcMatch := buildIPListMatch("ip4.src", selectedPodIPs)
	if srcMatch != "" {
		conditions = append(conditions, srcMatch)
	}

	// Build destination match
	destMatch := buildIPListMatch("ip4.dst", destCIDRs)
	if destMatch != "" {
		conditions = append(conditions, destMatch)
	}

	// Build port match
	portMatch := buildPortMatch(ports)
	if portMatch != "" {
		conditions = append(conditions, portMatch)
	}

	return strings.Join(conditions, " && ")
}

// buildIPListMatch builds a match expression for a list of IPs/CIDRs
func buildIPListMatch(field string, ips []string) string {
	if len(ips) == 0 {
		return ""
	}
	if len(ips) == 1 {
		return fmt.Sprintf("%s == %s", field, ips[0])
	}

	var parts []string
	for _, ip := range ips {
		parts = append(parts, fmt.Sprintf("%s == %s", field, ip))
	}
	return "(" + strings.Join(parts, " || ") + ")"
}

// buildPortMatch builds a match expression for ports
func buildPortMatch(ports []networkingv1.NetworkPolicyPort) string {
	if len(ports) == 0 {
		return ""
	}

	var parts []string
	for _, port := range ports {
		protocol := "tcp"
		if port.Protocol != nil {
			protocol = strings.ToLower(string(*port.Protocol))
		}

		if port.Port != nil {
			portNum := port.Port.IntValue()
			if port.EndPort != nil && *port.EndPort > int32(portNum) {
				// Port range
				parts = append(parts, fmt.Sprintf("%s.dst >= %d && %s.dst <= %d",
					protocol, portNum, protocol, *port.EndPort))
			} else {
				// Single port
				parts = append(parts, fmt.Sprintf("%s.dst == %d", protocol, portNum))
			}
		}
	}

	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, " || ") + ")"
}

// buildDefaultDenyMatch builds the match expression for default deny rules
func buildDefaultDenyMatch(podIPs []string, direction string) string {
	if len(podIPs) == 0 {
		return ""
	}

	if direction == DirectionIngress {
		return buildIPListMatch("ip4.dst", podIPs)
	}
	return buildIPListMatch("ip4.src", podIPs)
}

// buildPolicyExternalIDs builds external IDs for policy ACLs
func buildPolicyExternalIDs(namespace, policyName, direction string) map[string]string {
	return map[string]string{
		ovndb.ACLExternalIDPolicy:    fmt.Sprintf("%s/%s", namespace, policyName),
		ovndb.ACLExternalIDNamespace: namespace,
		ovndb.ACLExternalIDDirection: direction,
	}
}

// getIngressSourceCIDRs extracts source CIDRs from ingress peers
func getIngressSourceCIDRs(ctx context.Context, c client.Client, namespace string, peers []networkingv1.NetworkPolicyPeer) []string {
	var cidrs []string
	for _, peer := range peers {
		if peer.IPBlock != nil {
			cidrs = append(cidrs, peer.IPBlock.CIDR)
		}
		// TODO: Handle PodSelector and NamespaceSelector
	}
	return cidrs
}

// getEgressDestCIDRs extracts destination CIDRs from egress peers
func getEgressDestCIDRs(ctx context.Context, c client.Client, namespace string, peers []networkingv1.NetworkPolicyPeer) []string {
	var cidrs []string
	for _, peer := range peers {
		if peer.IPBlock != nil {
			cidrs = append(cidrs, peer.IPBlock.CIDR)
		}
		// TODO: Handle PodSelector and NamespaceSelector
	}
	return cidrs
}

// getIngressPeerCount returns the number of peers (minimum 1 for iteration)
func getIngressPeerCount(peers []networkingv1.NetworkPolicyPeer) int {
	if len(peers) == 0 {
		return 1
	}
	return len(peers)
}

// getEgressPeerCount returns the number of peers (minimum 1 for iteration)
func getEgressPeerCount(peers []networkingv1.NetworkPolicyPeer) int {
	if len(peers) == 0 {
		return 1
	}
	return len(peers)
}

// Ensure PolicyController implements the Reconciler interface
var _ reconcile.Reconciler = &PolicyController{}
