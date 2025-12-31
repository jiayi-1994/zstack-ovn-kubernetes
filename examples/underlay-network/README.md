# Underlay 网络示例

本目录包含 Underlay 网络配置示例。

## 概述

Underlay 网络让 Pod 直接使用物理网络 IP，适用于：
- 需要与物理网络直接通信的场景
- 对网络性能要求高的应用
- 与传统基础设施集成

## 前置条件

### 1. 物理网络配置

确保物理交换机配置了对应的 VLAN：

```
# 交换机配置示例 (Cisco)
interface GigabitEthernet0/1
  switchport mode trunk
  switchport trunk allowed vlan 10,20,100
```

### 2. 节点网络配置

在每个节点上配置 Provider Network：

```bash
# 创建 OVS 网桥映射
ovs-vsctl set open . external-ids:ovn-bridge-mappings="provider-net1:br-provider"

# 创建 provider 网桥
ovs-vsctl add-br br-provider

# 将物理网卡加入网桥
ovs-vsctl add-port br-provider eth1
```

## 文件说明

- `subnet.yaml` - Underlay 子网配置示例

## 使用方法

### 1. 配置 Provider Network

首先在所有节点上配置 Provider Network（见前置条件）。

### 2. 创建子网

```bash
kubectl apply -f subnet.yaml
```

### 3. 验证子网状态

```bash
kubectl get subnet underlay-subnet -o wide
kubectl describe subnet underlay-subnet
```

### 4. 创建使用 Underlay 网络的 Pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: underlay-pod
  annotations:
    network.zstack.io/subnet: "underlay-subnet"
spec:
  containers:
  - name: app
    image: nginx
```

```bash
kubectl apply -f underlay-pod.yaml
kubectl get pod underlay-pod -o wide
```

### 5. 验证网络连通性

```bash
# Pod 应该获得物理网络 IP (192.168.100.x)
kubectl get pod underlay-pod -o jsonpath='{.status.podIP}'

# 从物理网络可以直接访问 Pod
ping 192.168.100.x
```

## 配置说明

### VLAN 配置

| 参数 | 说明 | 示例 |
|------|------|------|
| vlanID | VLAN 标签 | 100 |
| provider | Provider Network 名称 | provider-net1 |

### IP 规划注意事项

1. **避免 IP 冲突**: 确保 Pod IP 范围不与现有设备冲突
2. **排除已用 IP**: 在 excludeIPs 中排除所有已使用的 IP
3. **预留 DHCP 范围**: 如果网络中有 DHCP，排除 DHCP 分配范围

### 多 VLAN 场景

可以创建多个 VLAN 子网用于不同用途：
- `management-subnet` (VLAN 10): 管理网络
- `storage-subnet` (VLAN 20): 存储网络
- `underlay-subnet` (VLAN 100): 业务网络

## 与 ZStack 集成

在 External 模式下，可以引用 ZStack 已有的扁平网络或 VLAN 网络：

```yaml
apiVersion: network.zstack.io/v1
kind: Subnet
metadata:
  name: zstack-flat-network
spec:
  cidr: "192.168.100.0/24"
  gateway: "192.168.100.1"
  # 引用 ZStack 已有的 Logical Switch
  externalLogicalSwitch: "ls-zstack-flat-network-uuid"
  excludeIPs:
    - "192.168.100.1"
    - "192.168.100.100-192.168.100.200"  # ZStack VM 使用的范围
```

## 故障排查

### Pod 无法获取 IP

1. 检查 Provider Network 配置：
```bash
ovs-vsctl get open . external-ids:ovn-bridge-mappings
```

2. 检查 VLAN 配置：
```bash
ovs-vsctl show | grep -A 5 br-provider
```

### 无法访问物理网络

1. 检查物理网卡是否加入网桥：
```bash
ovs-vsctl list-ports br-provider
```

2. 检查交换机 VLAN 配置是否正确
