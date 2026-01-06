#!/bin/bash
# fix-gateway-chassis.sh - 快速验证并修复 Gateway Chassis 缺失问题
#
# 问题描述：
# Pod 无法访问网关和外部网络，因为 gateway router port 没有绑定到 chassis。
# OVN 不知道在哪个物理节点上处理外部网络流量。
#
# 使用方法：
#   ./fix-gateway-chassis.sh [node-name]
#
# 如果不指定 node-name，脚本会自动检测

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
echo_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
echo_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# 获取 OVN NB/SB pod 名称
get_nb_pod() {
    kubectl -n zstack-ovn-kubernetes get pods -l app=ovn-nb-db -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || \
    kubectl -n zstack-ovn-kubernetes get pods -o name | grep nb-db | head -1 | cut -d/ -f2
}

get_sb_pod() {
    kubectl -n zstack-ovn-kubernetes get pods -l app=ovn-sb-db -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || \
    kubectl -n zstack-ovn-kubernetes get pods -o name | grep sb-db | head -1 | cut -d/ -f2
}

NB_POD=$(get_nb_pod)
SB_POD=$(get_sb_pod)

if [ -z "$NB_POD" ]; then
    echo_error "无法找到 OVN NB DB pod"
    exit 1
fi

echo_info "使用 NB Pod: $NB_POD"
echo_info "使用 SB Pod: $SB_POD"

# 执行 ovn-nbctl 命令
ovn_nbctl() {
    kubectl -n zstack-ovn-kubernetes exec -it "$NB_POD" -- ovn-nbctl "$@"
}

# 执行 ovn-sbctl 命令
ovn_sbctl() {
    kubectl -n zstack-ovn-kubernetes exec -it "$SB_POD" -- ovn-sbctl "$@"
}

echo ""
echo "=========================================="
echo "  步骤 1: 诊断当前状态"
echo "=========================================="

# 检查 gateway router port
echo_info "检查 gateway router port..."
GATEWAY_PORTS=$(ovn_nbctl --columns=name,gateway_chassis find logical_router_port 2>/dev/null | grep -E "^name|gateway_chassis" || true)
echo "$GATEWAY_PORTS"

# 找到 rtoe- 开头的端口
RTOE_PORTS=$(ovn_nbctl list logical_router_port 2>/dev/null | grep -E "^name.*rtoe-" | awk '{print $3}' | tr -d '"' || true)

if [ -z "$RTOE_PORTS" ]; then
    echo_warn "没有找到 rtoe-* gateway router port"
    echo_info "检查所有 router ports..."
    ovn_nbctl list logical_router_port | grep -E "^name|^gateway_chassis"
else
    echo_info "找到 gateway router ports: $RTOE_PORTS"
fi

# 检查 Gateway_Chassis 表
echo ""
echo_info "检查 Gateway_Chassis 表..."
GW_CHASSIS=$(ovn_nbctl list gateway_chassis 2>/dev/null || echo "")
if [ -z "$GW_CHASSIS" ]; then
    echo_warn "Gateway_Chassis 表为空 - 这就是问题所在！"
else
    echo "$GW_CHASSIS"
fi

# 检查 chassis
echo ""
echo_info "检查 SB 数据库中的 chassis..."
ovn_sbctl list chassis | grep -E "^name|^hostname"

echo ""
echo "=========================================="
echo "  步骤 2: 验证问题"
echo "=========================================="

# 获取节点名称
NODE_NAME=${1:-$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')}
echo_info "目标节点: $NODE_NAME"

# 获取 chassis ID
CHASSIS_ID=$(ovn_sbctl --columns=name find chassis hostname="$NODE_NAME" 2>/dev/null | grep "name" | awk '{print $3}' | tr -d '"' || echo "$NODE_NAME")
if [ -z "$CHASSIS_ID" ]; then
    CHASSIS_ID="$NODE_NAME"
fi
echo_info "Chassis ID: $CHASSIS_ID"

# 检查 rtoe 端口的 gateway_chassis
LRP_NAME="rtoe-$NODE_NAME"
echo_info "检查 $LRP_NAME 的 gateway_chassis..."

GW_CHASSIS_REF=$(ovn_nbctl get logical_router_port "$LRP_NAME" gateway_chassis 2>/dev/null || echo "[]")
echo "当前 gateway_chassis: $GW_CHASSIS_REF"

if [ "$GW_CHASSIS_REF" = "[]" ]; then
    echo_error "问题确认: $LRP_NAME 没有 gateway_chassis 绑定！"
    echo ""
    echo "=========================================="
    echo "  步骤 3: 修复问题"
    echo "=========================================="
    
    read -p "是否要创建 Gateway_Chassis 并绑定到 $LRP_NAME? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        GW_CHASSIS_NAME="${LRP_NAME}-${CHASSIS_ID}"
        
        echo_info "创建 Gateway_Chassis: $GW_CHASSIS_NAME"
        
        # 创建 gateway chassis
        ovn_nbctl --id=@gwc create gateway_chassis \
            name="$GW_CHASSIS_NAME" \
            chassis_name="$CHASSIS_ID" \
            priority=1 \
            -- set logical_router_port "$LRP_NAME" gateway_chassis=@gwc
        
        echo_info "Gateway_Chassis 创建成功！"
        
        # 验证
        echo ""
        echo_info "验证修复结果..."
        ovn_nbctl get logical_router_port "$LRP_NAME" gateway_chassis
        ovn_nbctl list gateway_chassis
        
        echo ""
        echo "=========================================="
        echo "  步骤 4: 测试网络连通性"
        echo "=========================================="
        echo_info "等待 OVN 同步 (5秒)..."
        sleep 5
        
        # 找一个 Pod 测试
        TEST_POD=$(kubectl get pods -A -o jsonpath='{.items[?(@.status.phase=="Running")].metadata.name}' | awk '{print $1}')
        if [ -n "$TEST_POD" ]; then
            TEST_NS=$(kubectl get pods -A -o jsonpath='{.items[?(@.status.phase=="Running")].metadata.namespace}' | awk '{print $1}')
            echo_info "使用 Pod $TEST_NS/$TEST_POD 测试..."
            
            # 获取网关 IP
            GATEWAY_IP=$(ovn_nbctl get logical_router_port rtos-subnet-default networks | tr -d '[]"' | cut -d/ -f1)
            if [ -z "$GATEWAY_IP" ]; then
                GATEWAY_IP="10.244.0.1"
            fi
            
            echo_info "测试 ping 网关 $GATEWAY_IP..."
            kubectl exec -n "$TEST_NS" "$TEST_POD" -- ping -c 3 "$GATEWAY_IP" 2>/dev/null && \
                echo_info "网关连通性测试成功！" || \
                echo_warn "网关连通性测试失败，可能需要更多时间同步"
        fi
    else
        echo_info "跳过修复"
    fi
else
    echo_info "$LRP_NAME 已经有 gateway_chassis 绑定"
    echo_info "问题可能在其他地方，请检查："
    echo "  1. ovn-controller 是否运行正常"
    echo "  2. br-ex 网桥是否配置正确"
    echo "  3. bridge-mappings 是否正确"
fi

echo ""
echo "=========================================="
echo "  额外诊断信息"
echo "=========================================="

echo_info "OVN NB 拓扑概览:"
ovn_nbctl show | head -50

echo ""
echo_info "OVN SB Port_Binding 状态:"
ovn_sbctl show

echo ""
echo_info "检查 OpenFlow 流表 (在节点上运行):"
echo "  ovs-ofctl -O OpenFlow15 dump-flows br-int | grep 'rtoe\|gateway'"
