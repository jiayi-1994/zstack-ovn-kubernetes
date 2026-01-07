#!/bin/bash
# Setup OpenFlow rules on br-ex for proper return traffic handling
# This script implements the same logic as ovn-kubernetes for shared gateway mode
#
# The key insight is that we need to use conntrack marks to distinguish between:
# - Traffic originating from OVN (pods) -> should return to OVN
# - Traffic originating from host -> should return to host
#
# Usage: ./setup-br-ex-flows.sh [NODE_NAME] [BRIDGE_NAME] [BRIDGE_MAC] [OVN_MAC]
#
# If arguments are not provided, the script will auto-detect them.

set -e

# Configuration
NODE_NAME="${1:-$(hostname)}"
BRIDGE_NAME="${2:-br-ex}"
BRIDGE_MAC="${3:-}"
OVN_MAC="${4:-}"  # MAC address of OVN router external port (rtoe-<node>)

# Constants (matching ovn-kubernetes)
CONNTRACK_ZONE=64000
CT_MARK_OVN="0x1"
CT_MARK_HOST="0x2"
COOKIE="0xdeff105"

echo "=== Setting up OpenFlow rules on $BRIDGE_NAME ==="
echo "Node: $NODE_NAME"

# Auto-detect bridge MAC if not provided
if [ -z "$BRIDGE_MAC" ]; then
    BRIDGE_MAC=$(ip link show $BRIDGE_NAME 2>/dev/null | grep -oP 'link/ether \K[0-9a-f:]+' || echo "")
    if [ -z "$BRIDGE_MAC" ]; then
        echo "ERROR: Could not detect MAC address for $BRIDGE_NAME"
        exit 1
    fi
fi
echo "Bridge MAC: $BRIDGE_MAC"

# Auto-detect OVN router port MAC if not provided
# This is the MAC of rtoe-<node> port on ovn-cluster-router
if [ -z "$OVN_MAC" ]; then
    echo "Detecting OVN router port MAC..."
    OVN_MAC=$(kubectl -n zstack-ovn-kubernetes exec -it $(kubectl -n zstack-ovn-kubernetes get pod -l app=ovn-nb-db -o name 2>/dev/null | head -1) -- ovn-nbctl get Logical_Router_Port rtoe-${NODE_NAME} mac 2>/dev/null | tr -d '"' | tr -d '\r' || echo "")
    if [ -z "$OVN_MAC" ]; then
        echo "WARNING: Could not detect OVN router port MAC, using bridge MAC"
        OVN_MAC="$BRIDGE_MAC"
    fi
fi
echo "OVN Router Port MAC: $OVN_MAC"

# Get patch port name (created by OVN for localnet port)
PATCH_PORT="patch-ln-${NODE_NAME}-to-br-int"
echo "Patch port: $PATCH_PORT"

# Get physical port (the interface that was migrated to br-ex)
PHYS_PORT=$(ovs-vsctl list-ports $BRIDGE_NAME | grep -v "^patch-" | head -1)
if [ -z "$PHYS_PORT" ]; then
    echo "ERROR: No physical port found on $BRIDGE_NAME"
    echo "Available ports:"
    ovs-vsctl list-ports $BRIDGE_NAME
    exit 1
fi
echo "Physical port: $PHYS_PORT"

# Get OpenFlow port numbers
get_ofport() {
    local port=$1
    ovs-vsctl --timeout=5 get Interface "$port" ofport 2>/dev/null || echo ""
}

PATCH_OFPORT=$(get_ofport $PATCH_PORT)
PHYS_OFPORT=$(get_ofport $PHYS_PORT)

if [ -z "$PATCH_OFPORT" ] || [ "$PATCH_OFPORT" = "-1" ]; then
    echo "WARNING: Patch port $PATCH_PORT not found or not ready (ofport=$PATCH_OFPORT)"
    echo "This may be because OVN hasn't created the localnet port yet."
    echo "Continuing with placeholder..."
    PATCH_OFPORT="1"  # Will be updated when port is created
fi

if [ -z "$PHYS_OFPORT" ] || [ "$PHYS_OFPORT" = "-1" ]; then
    echo "ERROR: Physical port $PHYS_PORT not found (ofport=$PHYS_OFPORT)"
    exit 1
fi

echo "OpenFlow ports: patch=$PATCH_OFPORT, phys=$PHYS_OFPORT"

# Delete existing flows with our cookie
echo ""
echo "=== Deleting existing flows with cookie $COOKIE ==="
ovs-ofctl del-flows $BRIDGE_NAME "cookie=$COOKIE/-1" 2>/dev/null || true

# Add flows
echo ""
echo "=== Adding OpenFlow rules ==="

add_flow() {
    local flow="$1"
    echo "  Adding: $flow"
    ovs-ofctl add-flow $BRIDGE_NAME "$flow"
}

# Table 0: Classification and initial processing

# Priority 200: Geneve/VXLAN encapsulated traffic - skip conntrack
add_flow "cookie=$COOKIE,priority=200,in_port=$PHYS_OFPORT,udp,udp_dst=6081,actions=output:LOCAL"
add_flow "cookie=$COOKIE,priority=200,in_port=LOCAL,udp,udp_dst=6081,actions=output:$PHYS_OFPORT"

# Priority 100: Traffic from OVN (patch port) going to external
# Use OVN router port MAC (rtoe-<node>) as source MAC
# Commit to conntrack with ct_mark=CT_MARK_OVN so return traffic comes back to OVN
add_flow "cookie=$COOKIE,priority=100,in_port=$PATCH_OFPORT,ip,dl_src=$OVN_MAC,actions=ct(commit,zone=$CONNTRACK_ZONE,exec(set_field:$CT_MARK_OVN->ct_mark)),output:$PHYS_OFPORT"

# Priority 100: Traffic from host (LOCAL port) going to external
# Commit to conntrack with ct_mark=CT_MARK_HOST so return traffic comes back to host
add_flow "cookie=$COOKIE,priority=100,in_port=LOCAL,ip,actions=ct(commit,zone=$CONNTRACK_ZONE,exec(set_field:$CT_MARK_HOST->ct_mark)),output:$PHYS_OFPORT"

# Priority 50: Traffic from external (physical port) destined to our MAC (bridge or OVN)
# Send through conntrack to determine where it should go (table 1)
add_flow "cookie=$COOKIE,priority=50,in_port=$PHYS_OFPORT,ip,dl_dst=$BRIDGE_MAC,actions=ct(zone=$CONNTRACK_ZONE,nat,table=1)"
add_flow "cookie=$COOKIE,priority=50,in_port=$PHYS_OFPORT,ip,dl_dst=$OVN_MAC,actions=ct(zone=$CONNTRACK_ZONE,nat,table=1)"

# Priority 10: Check if traffic from patch port has correct MAC (OVN MAC), allow NORMAL
add_flow "cookie=$COOKIE,priority=10,in_port=$PATCH_OFPORT,dl_src=$OVN_MAC,actions=output:NORMAL"

# Priority 9: Drop traffic from patch port with wrong MAC (security)
add_flow "cookie=$COOKIE,priority=9,in_port=$PATCH_OFPORT,actions=drop"

# Priority 0: Default - NORMAL action
add_flow "cookie=$COOKIE,priority=0,actions=NORMAL"

# Table 1: Conntrack state processing for return traffic

# Priority 100: Established/related connections with ct_mark=CT_MARK_OVN go to OVN
add_flow "cookie=$COOKIE,priority=100,table=1,ip,ct_state=+trk+est,ct_mark=$CT_MARK_OVN,actions=output:$PATCH_OFPORT"
add_flow "cookie=$COOKIE,priority=100,table=1,ip,ct_state=+trk+rel,ct_mark=$CT_MARK_OVN,actions=output:$PATCH_OFPORT"

# Priority 100: Established/related connections with ct_mark=CT_MARK_HOST go to host
add_flow "cookie=$COOKIE,priority=100,table=1,ip,ct_state=+trk+est,ct_mark=$CT_MARK_HOST,actions=output:LOCAL"
add_flow "cookie=$COOKIE,priority=100,table=1,ip,ct_state=+trk+rel,ct_mark=$CT_MARK_HOST,actions=output:LOCAL"

# Priority 10: Traffic destined to our MAC but not tracked - send to host
add_flow "cookie=$COOKIE,priority=10,table=1,dl_dst=$BRIDGE_MAC,actions=output:LOCAL"

# Priority 0: Default for table 1 - NORMAL action
add_flow "cookie=$COOKIE,priority=0,table=1,actions=output:NORMAL"

echo ""
echo "=== OpenFlow rules configured successfully ==="
echo ""
echo "Current flows on $BRIDGE_NAME:"
ovs-ofctl dump-flows $BRIDGE_NAME --no-stats | grep -v "cookie=0x0" | head -30

echo ""
echo "=== Testing connectivity ==="
echo "You can now test Pod external connectivity:"
echo "  kubectl exec -it <pod> -- ping -c 3 8.8.8.8"
echo "  kubectl exec -it <pod> -- curl -I https://www.google.com"
