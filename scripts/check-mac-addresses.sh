#!/bin/bash
# Check MAC addresses in the traffic path

echo "=== MAC Address Analysis ==="

echo ""
echo "1. br-ex MAC address:"
ip link show br-ex | grep ether

echo ""
echo "2. OVN Router external port MAC (rtoe-k8s-1):"
kubectl -n zstack-ovn-kubernetes exec -it $(kubectl -n zstack-ovn-kubernetes get pod -l app=ovn-nb-db -o name | head -1) -- ovn-nbctl lrp-get-options rtoe-k8s-1 2>/dev/null || echo "Port not found"
kubectl -n zstack-ovn-kubernetes exec -it $(kubectl -n zstack-ovn-kubernetes get pod -l app=ovn-nb-db -o name | head -1) -- ovn-nbctl get Logical_Router_Port rtoe-k8s-1 mac 2>/dev/null || echo "Could not get MAC"

echo ""
echo "3. All router ports and their MACs:"
kubectl -n zstack-ovn-kubernetes exec -it $(kubectl -n zstack-ovn-kubernetes get pod -l app=ovn-nb-db -o name | head -1) -- ovn-nbctl show | grep -E "(port|mac:)"

echo ""
echo "4. Physical interface (ens3) MAC:"
ip link show ens3 | grep ether

echo ""
echo "=== The Problem ==="
echo "If the MAC from OVN router port (rtoe-k8s-1) is different from br-ex MAC,"
echo "then our OpenFlow rule 'dl_src=br-ex-mac' won't match the traffic!"
echo ""
echo "Solution: Update OpenFlow rules to match the OVN router port MAC instead."
