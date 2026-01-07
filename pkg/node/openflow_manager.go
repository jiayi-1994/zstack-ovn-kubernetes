//go:build linux
// +build linux

// Package node provides OpenFlow management for br-ex bridge.
//
// This file implements OpenFlow rules for the external bridge (br-ex) to properly
// handle traffic between OVN and the physical network. The key insight is that
// we need to use conntrack marks to distinguish between:
// - Traffic originating from OVN (pods) -> should return to OVN
// - Traffic originating from host -> should return to host
//
// Reference: ovn-kubernetes pkg/node/bridgeconfig/bridgeflows.go
package node

import (
	"fmt"
	"os/exec"
	"strings"

	"k8s.io/klog/v2"
)

const (
	// ConntrackZone is the conntrack zone used for tracking connections
	ConntrackZone = 64000

	// CtMarkOVN marks connections originating from OVN (pods)
	CtMarkOVN = "0x1"

	// CtMarkHost marks connections originating from host
	CtMarkHost = "0x2"

	// DefaultOpenFlowCookie is the cookie used for our flows
	DefaultOpenFlowCookie = "0xdeff105"
)

// OpenFlowManager manages OpenFlow rules on br-ex bridge
type OpenFlowManager struct {
	bridgeName    string
	patchPort     string // patch port to br-int (OVN)
	physPort      string // physical interface port
	bridgeMAC     string // MAC address of br-ex
	nodeIP        string // Node's external IP
	clusterCIDR   string // Cluster CIDR for pod traffic
}

// NewOpenFlowManager creates a new OpenFlow manager
func NewOpenFlowManager(bridgeName, patchPort, physPort, bridgeMAC, nodeIP, clusterCIDR string) *OpenFlowManager {
	return &OpenFlowManager{
		bridgeName:  bridgeName,
		patchPort:   patchPort,
		physPort:    physPort,
		bridgeMAC:   bridgeMAC,
		nodeIP:      nodeIP,
		clusterCIDR: clusterCIDR,
	}
}

// SetupFlows configures all necessary OpenFlow rules on br-ex
func (m *OpenFlowManager) SetupFlows() error {
	klog.Infof("Setting up OpenFlow rules on %s: patchPort=%s, physPort=%s, bridgeMAC=%s, nodeIP=%s",
		m.bridgeName, m.patchPort, m.physPort, m.bridgeMAC, m.nodeIP)

	// Get OpenFlow port numbers
	patchOfPort, err := m.getOfPort(m.patchPort)
	if err != nil {
		return fmt.Errorf("failed to get ofport for %s: %w", m.patchPort, err)
	}

	physOfPort, err := m.getOfPort(m.physPort)
	if err != nil {
		return fmt.Errorf("failed to get ofport for %s: %w", m.physPort, err)
	}

	klog.Infof("OpenFlow ports: patch=%s, phys=%s", patchOfPort, physOfPort)

	// Build flow rules
	flows := m.buildFlows(patchOfPort, physOfPort)

	// Apply flows
	if err := m.applyFlows(flows); err != nil {
		return fmt.Errorf("failed to apply flows: %w", err)
	}

	klog.Infof("Successfully configured %d OpenFlow rules on %s", len(flows), m.bridgeName)
	return nil
}

// buildFlows generates all OpenFlow rules needed for proper traffic handling
func (m *OpenFlowManager) buildFlows(patchOfPort, physOfPort string) []string {
	var flows []string

	// Table 0: Classification and initial processing
	
	// Priority 200: Geneve/VXLAN encapsulated traffic - skip conntrack, go directly
	// This handles overlay traffic between nodes
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=200,in_port=%s,udp,udp_dst=6081,actions=output:LOCAL",
			DefaultOpenFlowCookie, physOfPort))
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=200,in_port=LOCAL,udp,udp_dst=6081,actions=output:%s",
			DefaultOpenFlowCookie, physOfPort))

	// Priority 100: Traffic from OVN (patch port) going to external
	// Commit to conntrack with ct_mark=CtMarkOVN so return traffic comes back to OVN
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=100,in_port=%s,ip,dl_src=%s,actions=ct(commit,zone=%d,exec(set_field:%s->ct_mark)),output:%s",
			DefaultOpenFlowCookie, patchOfPort, m.bridgeMAC, ConntrackZone, CtMarkOVN, physOfPort))

	// Priority 100: Traffic from host (LOCAL port) going to external
	// Commit to conntrack with ct_mark=CtMarkHost so return traffic comes back to host
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=100,in_port=LOCAL,ip,actions=ct(commit,zone=%d,exec(set_field:%s->ct_mark)),output:%s",
			DefaultOpenFlowCookie, ConntrackZone, CtMarkHost, physOfPort))

	// Priority 50: Traffic from external (physical port) destined to our MAC
	// Send through conntrack to determine where it should go (table 1)
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=50,in_port=%s,ip,dl_dst=%s,actions=ct(zone=%d,nat,table=1)",
			DefaultOpenFlowCookie, physOfPort, m.bridgeMAC, ConntrackZone))

	// Priority 10: Check if traffic from patch port has correct MAC, allow NORMAL
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=10,in_port=%s,dl_src=%s,actions=output:NORMAL",
			DefaultOpenFlowCookie, patchOfPort, m.bridgeMAC))

	// Priority 9: Drop traffic from patch port with wrong MAC (security)
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=9,in_port=%s,actions=drop",
			DefaultOpenFlowCookie, patchOfPort))

	// Priority 0: Default - NORMAL action (let OVS handle it)
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=0,actions=NORMAL", DefaultOpenFlowCookie))

	// Table 1: Conntrack state processing for return traffic
	
	// Priority 100: Established/related connections with ct_mark=CtMarkOVN go to OVN (patch port)
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=100,table=1,ip,ct_state=+trk+est,ct_mark=%s,actions=output:%s",
			DefaultOpenFlowCookie, CtMarkOVN, patchOfPort))
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=100,table=1,ip,ct_state=+trk+rel,ct_mark=%s,actions=output:%s",
			DefaultOpenFlowCookie, CtMarkOVN, patchOfPort))

	// Priority 100: Established/related connections with ct_mark=CtMarkHost go to host (LOCAL)
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=100,table=1,ip,ct_state=+trk+est,ct_mark=%s,actions=output:LOCAL",
			DefaultOpenFlowCookie, CtMarkHost))
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=100,table=1,ip,ct_state=+trk+rel,ct_mark=%s,actions=output:LOCAL",
			DefaultOpenFlowCookie, CtMarkHost))

	// Priority 10: Traffic destined to our MAC but not tracked - send to host
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=10,table=1,dl_dst=%s,actions=output:LOCAL",
			DefaultOpenFlowCookie, m.bridgeMAC))

	// Priority 0: Default for table 1 - NORMAL action
	flows = append(flows,
		fmt.Sprintf("cookie=%s,priority=0,table=1,actions=output:NORMAL", DefaultOpenFlowCookie))

	return flows
}

// getOfPort gets the OpenFlow port number for a given port name
func (m *OpenFlowManager) getOfPort(portName string) (string, error) {
	output, err := exec.Command("ovs-vsctl", "--timeout=5", "get", "Interface", portName, "ofport").Output()
	if err != nil {
		return "", fmt.Errorf("failed to get ofport for %s: %w", portName, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// applyFlows applies the given flows to the bridge
func (m *OpenFlowManager) applyFlows(flows []string) error {
	// First, delete existing flows with our cookie
	if err := exec.Command("ovs-ofctl", "del-flows", m.bridgeName, 
		fmt.Sprintf("cookie=%s/-1", DefaultOpenFlowCookie)).Run(); err != nil {
		klog.V(4).Infof("Failed to delete existing flows (may not exist): %v", err)
	}

	// Add each flow
	for _, flow := range flows {
		klog.V(4).Infof("Adding flow: %s", flow)
		if err := exec.Command("ovs-ofctl", "add-flow", m.bridgeName, flow).Run(); err != nil {
			return fmt.Errorf("failed to add flow %q: %w", flow, err)
		}
	}

	return nil
}

// DeleteFlows removes all flows with our cookie from the bridge
func (m *OpenFlowManager) DeleteFlows() error {
	return exec.Command("ovs-ofctl", "del-flows", m.bridgeName,
		fmt.Sprintf("cookie=%s/-1", DefaultOpenFlowCookie)).Run()
}

// DumpFlows returns all flows on the bridge for debugging
func (m *OpenFlowManager) DumpFlows() (string, error) {
	output, err := exec.Command("ovs-ofctl", "dump-flows", m.bridgeName).Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
