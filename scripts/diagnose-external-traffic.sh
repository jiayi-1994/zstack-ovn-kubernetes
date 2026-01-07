#!/bin/bash
# Diagnose external traffic flow through br-ex
# This script helps identify where traffic is getting stuck

echo "=== 1. Check br-ex configuration ==="
echo "Bridge ports:"
ovs-vsctl list-ports br-ex

echo ""
echo "Bridge MAC:"
ip link show br-ex | grep ether

echo ""
echo "=== 2. Check OVN topology ==="
echo "External switch and localnet port:"
kubectl -n zstack-ovn-kubernetes exec -it $(kubectl -n zstack-ovn-kubernetes get pod -l app=ovn-nb-db -o name | head -1) -- ovn-nbctl show | grep -A5 "ext_"

echo ""
echo "=== 3. Check patch port connection ==="
echo "Patch ports on br-ex:"
ovs-vsctl show | grep -A2 "patch-"

echo ""
echo "Patch port on br-int:"
ovs-vsctl show | grep -A2 "br-int" | head -20

echo ""
echo "=== 4. Check OpenFlow rules ==="
echo "Table 0 (classification):"
ovs-ofctl dump-flows br-ex table=0 --no-stats | head -15

echo ""
echo "Table 1 (conntrack processing):"
ovs-ofctl dump-flows br-ex table=1 --no-stats

echo ""
echo "=== 5. Check conntrack entries ==="
echo "Conntrack entries in zone 64000:"
conntrack -L -z 64000 2>/dev/null | head -10 || echo "conntrack command not available or no entries"

echo ""
echo "=== 6. Test traffic flow ==="
echo "Starting tcpdump on br-ex for 5 seconds..."
echo "Please run 'kubectl exec -it <pod> -- ping -c 2 8.8.8.8' in another terminal"
timeout 5 tcpdump -i br-ex -n icmp 2>/dev/null || echo "tcpdump finished"

echo ""
echo "=== 7. Check if traffic reaches physical interface ==="
echo "Starting tcpdump on ens3 for 5 seconds..."
timeout 5 tcpdump -i ens3 -n icmp 2>/dev/null || echo "tcpdump finished"

echo ""
echo "=== 8. Check OVN southbound flows ==="
echo "Checking if localnet port is properly bound..."
kubectl -n zstack-ovn-kubernetes exec -it $(kubectl -n zstack-ovn-kubernetes get pod -l app=ovn-sb-db -o name | head -1) -- ovn-sbctl show | grep -A3 "ln-"
