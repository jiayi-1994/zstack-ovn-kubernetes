#!/bin/bash
# fix-external-connectivity.sh
# 修复 OVN 外部网络连接问题
# 当 ext_<node> 交换机存在但没有连接到 ovn-cluster-router 时使用

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 配置参数
NODE_NAME="${NODE_NAME:-k8s-1}"
NODE_IP="${NODE_IP:-}"
NEXT_HOP="${NEXT_HOP:-}"
PHYSICAL_NETWORK="${PHYSICAL_NETWORK:-external}"
OVN_NB_POD="${OVN_NB_POD:-}"
NAMESPACE="${NAMESPACE:-zstack-ovn-kubernetes}"

echo -e "${GREEN}=== OVN 外部连接修复脚本 ===${NC}"

# 自动检测 OVN NB Pod
if [ -z "$OVN_NB_POD" ]; then
    OVN_NB_POD=$(kubectl -n $NAMESPACE get pods -l app=ovn-nb-db -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    if [ -z "$OVN_NB_POD" ]; then
        echo -e "${RED}错误: 无法找到 OVN NB Pod${NC}"
        exit 1
    fi
fi

# 执行 ovn-nbctl 命令的函数
ovn_nbctl() {
    kubectl -n $NAMESPACE exec -it $OVN_NB_POD -- ovn-nbctl "$@"
}

# 自动检测节点 IP
if [ -z "$NODE_IP" ]; then
    NODE_IP=$(kubectl get node $NODE_NAME -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || true)
    if [ -z "$NODE_IP" ]; then
        echo -e "${RED}错误: 无法获取节点 $NODE_NAME 的 IP 地址${NC}"
        exit 1
    fi
fi

# 自动推断网关
if [ -z "$NEXT_HOP" ]; then
    # 假设网关是 x.x.x.1
    NEXT_HOP=$(echo $NODE_IP | sed 's/\.[0-9]*$/.1/')
fi

echo "配置参数:"
echo "  节点名称: $NODE_NAME"
echo "  节点 IP: $NODE_IP"
echo "  下一跳网关: $NEXT_HOP"
echo "  物理网络: $PHYSICAL_NETWORK"
echo ""

# 检查当前状态
echo -e "${YELLOW}检查当前 OVN 状态...${NC}"

EXT_SWITCH="ext_$NODE_NAME"
ROUTER_PORT="rtoe-$NODE_NAME"
SWITCH_PORT="etor-$NODE_NAME"
LOCALNET_PORT="ln-$NODE_NAME"

# 检查外部交换机
echo -n "检查外部交换机 $EXT_SWITCH: "
if ovn_nbctl --if-exists get Logical_Switch $EXT_SWITCH _uuid > /dev/null 2>&1; then
    echo -e "${GREEN}存在${NC}"
    EXT_SWITCH_EXISTS=true
else
    echo -e "${YELLOW}不存在${NC}"
    EXT_SWITCH_EXISTS=false
fi

# 检查路由器端口
echo -n "检查路由器端口 $ROUTER_PORT: "
if ovn_nbctl --if-exists get Logical_Router_Port $ROUTER_PORT _uuid > /dev/null 2>&1; then
    echo -e "${GREEN}存在${NC}"
    ROUTER_PORT_EXISTS=true
else
    echo -e "${RED}不存在 (需要修复)${NC}"
    ROUTER_PORT_EXISTS=false
fi

# 检查交换机端口
echo -n "检查交换机端口 $SWITCH_PORT: "
if ovn_nbctl --if-exists get Logical_Switch_Port $SWITCH_PORT _uuid > /dev/null 2>&1; then
    echo -e "${GREEN}存在${NC}"
    SWITCH_PORT_EXISTS=true
else
    echo -e "${RED}不存在 (需要修复)${NC}"
    SWITCH_PORT_EXISTS=false
fi

# 检查默认路由
echo -n "检查默认路由 0.0.0.0/0: "
DEFAULT_ROUTE=$(ovn_nbctl --format=table --no-headings find Logical_Router_Static_Route ip_prefix="0.0.0.0/0" 2>/dev/null || true)
if [ -n "$DEFAULT_ROUTE" ]; then
    echo -e "${GREEN}存在${NC}"
    DEFAULT_ROUTE_EXISTS=true
else
    echo -e "${RED}不存在 (需要修复)${NC}"
    DEFAULT_ROUTE_EXISTS=false
fi

echo ""

# 如果一切正常，退出
if [ "$ROUTER_PORT_EXISTS" = true ] && [ "$SWITCH_PORT_EXISTS" = true ] && [ "$DEFAULT_ROUTE_EXISTS" = true ]; then
    echo -e "${GREEN}外部连接配置正常，无需修复${NC}"
    exit 0
fi

# 开始修复
echo -e "${YELLOW}开始修复外部连接...${NC}"

# 生成 MAC 地址
generate_mac() {
    local ip=$1
    IFS='.' read -r -a octets <<< "$ip"
    printf "0a:58:%02x:%02x:%02x:%02x" ${octets[0]} ${octets[1]} ${octets[2]} ${octets[3]}
}

GATEWAY_MAC=$(generate_mac $NODE_IP)
echo "网关 MAC 地址: $GATEWAY_MAC"

# 1. 创建外部交换机（如果不存在）
if [ "$EXT_SWITCH_EXISTS" = false ]; then
    echo "创建外部交换机 $EXT_SWITCH..."
    ovn_nbctl ls-add $EXT_SWITCH
    ovn_nbctl set Logical_Switch $EXT_SWITCH external_ids:k8s.ovn.org/kind=external-switch external_ids:k8s.ovn.org/node=$NODE_NAME
    
    # 创建 localnet 端口
    echo "创建 localnet 端口 $LOCALNET_PORT..."
    ovn_nbctl lsp-add $EXT_SWITCH $LOCALNET_PORT
    ovn_nbctl lsp-set-type $LOCALNET_PORT localnet
    ovn_nbctl lsp-set-addresses $LOCALNET_PORT unknown
    ovn_nbctl lsp-set-options $LOCALNET_PORT network_name=$PHYSICAL_NETWORK
fi

# 2. 创建路由器端口（如果不存在）
if [ "$ROUTER_PORT_EXISTS" = false ]; then
    echo "创建路由器端口 $ROUTER_PORT..."
    ovn_nbctl lrp-add ovn-cluster-router $ROUTER_PORT $GATEWAY_MAC "$NODE_IP/24"
    ovn_nbctl set Logical_Router_Port $ROUTER_PORT external_ids:k8s.ovn.org/kind=gateway-router-port external_ids:k8s.ovn.org/node=$NODE_NAME
fi

# 3. 创建交换机端口连接到路由器（如果不存在）
if [ "$SWITCH_PORT_EXISTS" = false ]; then
    echo "创建交换机端口 $SWITCH_PORT..."
    ovn_nbctl lsp-add $EXT_SWITCH $SWITCH_PORT
    ovn_nbctl lsp-set-type $SWITCH_PORT router
    ovn_nbctl lsp-set-addresses $SWITCH_PORT router
    ovn_nbctl lsp-set-options $SWITCH_PORT router-port=$ROUTER_PORT
fi

# 4. 添加默认路由（如果不存在）
if [ "$DEFAULT_ROUTE_EXISTS" = false ]; then
    echo "添加默认路由 0.0.0.0/0 -> $NEXT_HOP..."
    ovn_nbctl lr-route-add ovn-cluster-router 0.0.0.0/0 $NEXT_HOP $ROUTER_PORT
fi

echo ""
echo -e "${GREEN}=== 修复完成 ===${NC}"
echo ""

# 验证修复结果
echo -e "${YELLOW}验证修复结果...${NC}"
echo ""
echo "ovn-cluster-router 端口:"
ovn_nbctl lrp-list ovn-cluster-router | grep -E "(rtoe|rtos)" || true
echo ""
echo "ovn-cluster-router 路由:"
ovn_nbctl lr-route-list ovn-cluster-router || true
echo ""
echo "外部交换机 $EXT_SWITCH 端口:"
ovn_nbctl lsp-list $EXT_SWITCH || true
echo ""

echo -e "${GREEN}修复完成！请测试 Pod 是否可以访问外部网络。${NC}"
echo "测试命令: kubectl exec -it <pod-name> -- curl -I http://www.baidu.com"
