#!/bin/bash
# diagnose-internal-routing.sh - 诊断 Pod 到网关的内部路由问题
#
# 问题：Pod 无法 ping 通网关 10.244.0.1，即使 gateway_chassis 已配置

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

echo_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
echo_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
echo_error() { echo -e "${RED}[ERROR]${NC} $1"; }
echo_section() { echo -e "\n${CYAN}========== $1 ==========${NC}"; }

NB_POD=$(kubectl -n zstack-ovn-kubernetes get pods -l app=ovn-nb-db -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
SB_POD=$(kubectl -n zstack-ovn-kubernetes get pods -l app=ovn-sb-db -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

ovn_nbctl() { kubectl -n zstack-ovn-kubernetes exec -it "$NB_POD" -- ovn-nbctl "$@"; }
ovn_sbctl() { kubectl -n zstack-ovn-kubernetes exec -it "$SB_POD" -- ovn-sbctl "$@"; }

echo_section "1. 检查 Router Port 配置"
echo_info "Router port rtos-subnet-default (网关 10.244.0.1):"
ovn_nbctl list logical_router_port rtos-subnet-default 2>/dev/null || echo "未找到"

echo_section "2. 检查 Switch Port 到 Router 的连接"
echo_info "Switch port stor-subnet-default:"
ovn_nbctl list logical_switch_port stor-subnet-default 2>/dev/null || echo "未找到"

echo_section "3. 检查 Subnet Switch 的所有端口"
echo_info "subnet-default 上的所有端口:"
ovn_nbctl lsp-list subnet-default 2>/dev/null || echo "未找到"

echo_section "4. 检查 SB Port_Binding 状态"
echo_info "所有 Port_Binding (检查 chassis 绑定):"
ovn_sbctl --columns=logical_port,chassis,type find port_binding | head -40

echo_section "5. 检查 Router Port 的 Port_Binding"
echo_info "Router port 在 SB 中的绑定状态:"
ovn_sbctl find port_binding type=patch 2>/dev/null | head -30

echo_section "6. 检查 Datapath_Binding"
echo_info "所有 Datapath (logical switch/router):"
ovn_sbctl list datapath_binding 2>/dev/null | grep -E "^_uuid|^external_ids" | head -20

echo_section "7. 检查 OVS br-int 端口"
echo_info "br-int 上的所有端口:"
ovs-vsctl list-ports br-int

echo_info "\n端口详情 (external_ids):"
for port in $(ovs-vsctl list-ports br-int); do
    echo -n "$port: "
    ovs-vsctl get interface "$port" external_ids 2>/dev/null || echo "N/A"
done

echo_section "8. 检查 OpenFlow 流表 - ARP 处理"
echo_info "Table 0 (入口分类):"
ovs-ofctl -O OpenFlow15 dump-flows br-int table=0 2>/dev/null | head -10

echo_info "\nARP 相关流 (table 12-14):"
ovs-ofctl -O OpenFlow15 dump-flows br-int | grep -i "arp" | head -20

echo_section "9. 检查 Logical Flow (ARP responder)"
echo_info "Router 的 ARP responder 流:"
ovn_sbctl lflow-list ovn-cluster-router 2>/dev/null | grep -i "arp" | head -20

echo_section "10. 检查 MAC_Binding 表"
echo_info "MAC 绑定 (ARP 学习结果):"
ovn_sbctl list mac_binding 2>/dev/null || echo "无 MAC 绑定"

echo_section "11. 测试 ARP 解析"
COREDNS_PID=$(crictl inspect $(crictl ps --name coredns -q | head -1) 2>/dev/null | jq '.info.pid')
if [ -n "$COREDNS_PID" ] && [ "$COREDNS_PID" != "null" ]; then
    echo_info "CoreDNS PID: $COREDNS_PID"
    echo_info "Pod 的 ARP 表:"
    nsenter -t $COREDNS_PID -n ip neigh show 2>/dev/null || echo "无法获取"
    
    echo_info "\nPod 的路由表:"
    nsenter -t $COREDNS_PID -n ip route show 2>/dev/null || echo "无法获取"
    
    echo_info "\nPod 的 IP 地址:"
    nsenter -t $COREDNS_PID -n ip addr show 2>/dev/null | grep -E "inet |eth0" || echo "无法获取"
fi

echo_section "12. 抓包分析 - veth 接口"
VETH=$(ovs-vsctl list-ports br-int | grep veth | head -1)
if [ -n "$VETH" ]; then
    echo_info "在 $VETH 上抓取 ARP 包 (5秒)..."
    timeout 5 tcpdump -i "$VETH" -n arp -c 10 2>/dev/null &
    sleep 1
    # 触发 ARP
    if [ -n "$COREDNS_PID" ] && [ "$COREDNS_PID" != "null" ]; then
        nsenter -t $COREDNS_PID -n ping -c 1 -W 1 10.244.0.1 2>/dev/null &
    fi
    wait
fi

echo_section "13. 检查 ovn-controller 状态"
echo_info "ovn-controller 进程:"
ps aux | grep ovn-controller | grep -v grep || echo "未运行!"

echo_info "\novn-controller 日志 (最近错误):"
journalctl -u ovn-controller --no-pager -n 20 2>/dev/null | grep -iE "error|warn|fail" | tail -10 || \
    cat /var/log/openvswitch/ovn-controller.log 2>/dev/null | grep -iE "error|warn|fail" | tail -10 || \
    echo "无法获取日志"

echo_section "诊断总结"
echo_info "关键检查点:"
echo "  1. stor-subnet-default 是否存在且类型为 router"
echo "  2. rtos-subnet-default 是否有正确的 MAC 和 networks"
echo "  3. Port_Binding 是否都绑定到 chassis"
echo "  4. ovn-controller 是否正常运行"
echo "  5. OpenFlow 流表是否包含 ARP responder"
