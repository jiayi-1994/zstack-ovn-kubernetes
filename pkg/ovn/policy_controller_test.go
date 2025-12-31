// Package ovn provides tests for the NetworkPolicy controller.
package ovn

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/jiayi-1994/zstack-ovn-kubernetes/pkg/ovndb"
)

// TestBuildPolicyACLName tests the ACL name building function.
func TestBuildPolicyACLName(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		policyName string
		direction string
		index     string
		expected  string
	}{
		{
			name:       "ingress default deny",
			namespace:  "default",
			policyName: "allow-http",
			direction:  "Ingress",
			index:      "default",
			expected:   "np_default/allow-http_ingress_default",
		},
		{
			name:       "egress default deny",
			namespace:  "production",
			policyName: "deny-all",
			direction:  "Egress",
			index:      "default",
			expected:   "np_production/deny-all_egress_default",
		},
		{
			name:       "ingress rule 0",
			namespace:  "default",
			policyName: "allow-http",
			direction:  "Ingress",
			index:      "0_0",
			expected:   "np_default/allow-http_ingress_0_0",
		},
		{
			name:       "egress rule 1",
			namespace:  "kube-system",
			policyName: "allow-dns",
			direction:  "Egress",
			index:      "1_0",
			expected:   "np_kube-system/allow-dns_egress_1_0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildPolicyACLName(tt.namespace, tt.policyName, tt.direction, tt.index)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestConvertIngressRuleToACLMatch tests Ingress rule to ACL match conversion.
func TestConvertIngressRuleToACLMatch(t *testing.T) {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	port80 := intstr.FromInt(80)
	port443 := intstr.FromInt(443)
	port53 := intstr.FromInt(53)

	tests := []struct {
		name           string
		selectedPodIPs []string
		sourceCIDRs    []string
		ports          []networkingv1.NetworkPolicyPort
		expected       string
	}{
		{
			name:           "single pod, single source, single port",
			selectedPodIPs: []string{"10.244.1.5"},
			sourceCIDRs:    []string{"10.244.2.0/24"},
			ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &port80},
			},
			expected: "ip4.dst == 10.244.1.5 && ip4.src == 10.244.2.0/24 && tcp.dst == 80",
		},
		{
			name:           "multiple pods, single source",
			selectedPodIPs: []string{"10.244.1.5", "10.244.1.6"},
			sourceCIDRs:    []string{"10.244.2.0/24"},
			ports:          nil,
			expected:       "(ip4.dst == 10.244.1.5 || ip4.dst == 10.244.1.6) && ip4.src == 10.244.2.0/24",
		},
		{
			name:           "single pod, multiple sources",
			selectedPodIPs: []string{"10.244.1.5"},
			sourceCIDRs:    []string{"10.244.2.0/24", "10.244.3.0/24"},
			ports:          nil,
			expected:       "ip4.dst == 10.244.1.5 && (ip4.src == 10.244.2.0/24 || ip4.src == 10.244.3.0/24)",
		},
		{
			name:           "multiple ports",
			selectedPodIPs: []string{"10.244.1.5"},
			sourceCIDRs:    nil,
			ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &port80},
				{Protocol: &tcp, Port: &port443},
			},
			expected: "ip4.dst == 10.244.1.5 && (tcp.dst == 80 || tcp.dst == 443)",
		},
		{
			name:           "UDP port",
			selectedPodIPs: []string{"10.244.1.5"},
			sourceCIDRs:    nil,
			ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &udp, Port: &port53},
			},
			expected: "ip4.dst == 10.244.1.5 && udp.dst == 53",
		},
		{
			name:           "no ports (allow all)",
			selectedPodIPs: []string{"10.244.1.5"},
			sourceCIDRs:    []string{"10.244.2.0/24"},
			ports:          nil,
			expected:       "ip4.dst == 10.244.1.5 && ip4.src == 10.244.2.0/24",
		},
		{
			name:           "no source (allow from anywhere)",
			selectedPodIPs: []string{"10.244.1.5"},
			sourceCIDRs:    nil,
			ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &port80},
			},
			expected: "ip4.dst == 10.244.1.5 && tcp.dst == 80",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertIngressRuleToACLMatch(tt.selectedPodIPs, tt.sourceCIDRs, tt.ports)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestConvertEgressRuleToACLMatch tests Egress rule to ACL match conversion.
func TestConvertEgressRuleToACLMatch(t *testing.T) {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	port80 := intstr.FromInt(80)
	port443 := intstr.FromInt(443)
	port53 := intstr.FromInt(53)

	tests := []struct {
		name           string
		selectedPodIPs []string
		destCIDRs      []string
		ports          []networkingv1.NetworkPolicyPort
		expected       string
	}{
		{
			name:           "single pod, single dest, single port",
			selectedPodIPs: []string{"10.244.1.5"},
			destCIDRs:      []string{"10.244.2.0/24"},
			ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &port80},
			},
			expected: "ip4.src == 10.244.1.5 && ip4.dst == 10.244.2.0/24 && tcp.dst == 80",
		},
		{
			name:           "multiple pods, single dest",
			selectedPodIPs: []string{"10.244.1.5", "10.244.1.6"},
			destCIDRs:      []string{"10.244.2.0/24"},
			ports:          nil,
			expected:       "(ip4.src == 10.244.1.5 || ip4.src == 10.244.1.6) && ip4.dst == 10.244.2.0/24",
		},
		{
			name:           "single pod, multiple dests",
			selectedPodIPs: []string{"10.244.1.5"},
			destCIDRs:      []string{"10.244.2.0/24", "10.244.3.0/24"},
			ports:          nil,
			expected:       "ip4.src == 10.244.1.5 && (ip4.dst == 10.244.2.0/24 || ip4.dst == 10.244.3.0/24)",
		},
		{
			name:           "DNS egress (UDP 53)",
			selectedPodIPs: []string{"10.244.1.5"},
			destCIDRs:      []string{"10.96.0.10/32"},
			ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &udp, Port: &port53},
			},
			expected: "ip4.src == 10.244.1.5 && ip4.dst == 10.96.0.10/32 && udp.dst == 53",
		},
		{
			name:           "HTTPS egress",
			selectedPodIPs: []string{"10.244.1.5"},
			destCIDRs:      []string{"0.0.0.0/0"},
			ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &port443},
			},
			expected: "ip4.src == 10.244.1.5 && ip4.dst == 0.0.0.0/0 && tcp.dst == 443",
		},
		{
			name:           "no dest (allow to anywhere)",
			selectedPodIPs: []string{"10.244.1.5"},
			destCIDRs:      nil,
			ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &port80},
			},
			expected: "ip4.src == 10.244.1.5 && tcp.dst == 80",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertEgressRuleToACLMatch(tt.selectedPodIPs, tt.destCIDRs, tt.ports)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestGetACLPriority tests ACL priority calculation.
func TestGetACLPriority(t *testing.T) {
	tests := []struct {
		name          string
		direction     string
		isDefaultDeny bool
		ruleIndex     int
		expected      int
	}{
		{
			name:          "ingress default deny",
			direction:     DirectionIngress,
			isDefaultDeny: true,
			ruleIndex:     0,
			expected:      ACLPriorityDefaultDenyIngress,
		},
		{
			name:          "egress default deny",
			direction:     DirectionEgress,
			isDefaultDeny: true,
			ruleIndex:     0,
			expected:      ACLPriorityDefaultDenyEgress,
		},
		{
			name:          "ingress allow rule 0",
			direction:     DirectionIngress,
			isDefaultDeny: false,
			ruleIndex:     0,
			expected:      ACLPriorityAllowRuleBase,
		},
		{
			name:          "ingress allow rule 1",
			direction:     DirectionIngress,
			isDefaultDeny: false,
			ruleIndex:     1,
			expected:      ACLPriorityAllowRuleBase + 1,
		},
		{
			name:          "egress allow rule 5",
			direction:     DirectionEgress,
			isDefaultDeny: false,
			ruleIndex:     5,
			expected:      ACLPriorityAllowRuleBase + 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetACLPriority(tt.direction, tt.isDefaultDeny, tt.ruleIndex)
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}

// TestGetACLDirection tests ACL direction mapping.
func TestGetACLDirection(t *testing.T) {
	tests := []struct {
		name      string
		direction string
		expected  string
	}{
		{
			name:      "ingress maps to to-lport",
			direction: DirectionIngress,
			expected:  ovndb.ACLDirectionToLport,
		},
		{
			name:      "egress maps to from-lport",
			direction: DirectionEgress,
			expected:  ovndb.ACLDirectionFromLport,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetACLDirection(tt.direction)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestPortRangeMatch tests port range match expression building.
func TestPortRangeMatch(t *testing.T) {
	tcp := corev1.ProtocolTCP
	port8000 := intstr.FromInt(8000)
	endPort9000 := int32(9000)

	ports := []networkingv1.NetworkPolicyPort{
		{Protocol: &tcp, Port: &port8000, EndPort: &endPort9000},
	}

	result := buildPortMatch(ports)
	expected := "tcp.dst >= 8000 && tcp.dst <= 9000"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestPriorityOrdering tests that allow rules have higher priority than default deny.
func TestPriorityOrdering(t *testing.T) {
	// Default deny should have lower priority than allow rules
	// This ensures allow rules are evaluated first
	defaultDenyPriority := GetACLPriority(DirectionIngress, true, 0)
	allowRulePriority := GetACLPriority(DirectionIngress, false, 0)

	if allowRulePriority <= defaultDenyPriority {
		t.Errorf("allow rule priority (%d) should be higher than default deny priority (%d)",
			allowRulePriority, defaultDenyPriority)
	}
}

// TestMultipleRulePriorities tests that multiple rules have increasing priorities.
func TestMultipleRulePriorities(t *testing.T) {
	// Rules should have increasing priorities based on their index
	rule0Priority := GetACLPriority(DirectionIngress, false, 0)
	rule1Priority := GetACLPriority(DirectionIngress, false, 1)
	rule2Priority := GetACLPriority(DirectionIngress, false, 2)

	if rule1Priority <= rule0Priority {
		t.Errorf("rule 1 priority (%d) should be higher than rule 0 priority (%d)",
			rule1Priority, rule0Priority)
	}
	if rule2Priority <= rule1Priority {
		t.Errorf("rule 2 priority (%d) should be higher than rule 1 priority (%d)",
			rule2Priority, rule1Priority)
	}
}

// TestEmptyInputs tests handling of empty inputs.
func TestEmptyInputs(t *testing.T) {
	// Empty pod IPs should return empty match
	result := ConvertIngressRuleToACLMatch(nil, []string{"10.0.0.0/8"}, nil)
	if result != "ip4.src == 10.0.0.0/8" {
		t.Errorf("expected only source match, got %q", result)
	}

	// Empty source CIDRs should return only pod match
	result = ConvertIngressRuleToACLMatch([]string{"10.244.1.5"}, nil, nil)
	if result != "ip4.dst == 10.244.1.5" {
		t.Errorf("expected only dest match, got %q", result)
	}

	// Both empty should return empty string
	result = ConvertIngressRuleToACLMatch(nil, nil, nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}
