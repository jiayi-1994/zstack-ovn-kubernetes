# zstack-ovn-kubernetes

基于 OVN (Open Virtual Networking) 的 Kubernetes CNI 插件，专为 ZStack IaaS 平台设计。

[English](README_EN.md) | 中文

## 概述

zstack-ovn-kubernetes 是一个功能完整的 Kubernetes CNI 插件，使用 OVN/OVS 为 Pod 提供网络连接。它支持两种部署模式：

- **Standalone 模式**: 自管理 OVN 数据库，适用于独立的 Kubernetes 集群
- **External 模式**: 连接到 ZStack 管理的 OVN 数据库，实现 VM 与 Pod 的网络互通

## 特性

### 核心网络功能
- Pod 到 Pod 网络通信（同节点和跨节点）
- Pod 到 Service 负载均衡（基于 OVN Load Balancer）
- NetworkPolicy 支持（基于 OVN ACL）
- VXLAN 隧道实现 Overlay 网络
- Underlay 网络支持（VLAN 模式）

### ZStack 集成
- 与 ZStack VPC 和子网无缝对接
- 支持引用 ZStack 已有的 Logical Switch
- VM 与 Pod 在同一网络平面互通
- 安全组规则同步（可选）

### 高级特性
- DPDK 高性能网络支持
- 双栈网络（IPv4/IPv6）
- 多子网管理
- 网关模式可配置（shared/local）

## 架构

```
┌─────────────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │         zstack-ovnkube-controller (Deployment)              │ │
│  │  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐           │ │
│  │  │   Subnet    │ │   Service   │ │   Policy    │           │ │
│  │  │ Controller  │ │ Controller  │ │ Controller  │           │ │
│  │  └──────┬──────┘ └──────┬──────┘ └──────┬──────┘           │ │
│  │         └───────────────┴───────────────┘                   │ │
│  │                         │                                    │ │
│  │                  ┌──────┴──────┐                            │ │
│  │                  │  OVN Client │                            │ │
│  │                  │  (libovsdb) │                            │ │
│  │                  └──────┬──────┘                            │ │
│  └─────────────────────────┼────────────────────────────────────┘ │
│                            │                                      │
│                            ▼                                      │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │                    OVN Database Layer                       │  │
│  │  ┌─────────────────────┐  ┌─────────────────────────────┐  │  │
│  │  │   Standalone Mode   │  │   External Mode (ZStack)    │  │  │
│  │  │  ┌─────┐ ┌─────┐   │  │  ┌─────────────────────────┐ │  │  │
│  │  │  │NB DB│ │SB DB│   │  │  │ ZStack OVN NB/SB DB     │ │  │  │
│  │  │  └──┬──┘ └──┬──┘   │  │  └─────────────────────────┘ │  │  │
│  │  │     │       │      │  │                               │  │  │
│  │  │  ┌──┴───────┴──┐   │  │                               │  │  │
│  │  │  │   northd    │   │  │                               │  │  │
│  │  │  └─────────────┘   │  │                               │  │  │
│  │  └─────────────────────┘  └─────────────────────────────┘  │  │
│  └────────────────────────────────────────────────────────────┘  │
│                                                                   │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │              Worker Node (DaemonSet)                        │  │
│  │  ┌──────────────────────────────────────────────────────┐  │  │
│  │  │              zstack-ovnkube-node                      │  │  │
│  │  │  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐     │  │  │
│  │  │  │ CNI Server  │ │   Gateway   │ │   Tunnel    │     │  │  │
│  │  │  │(Unix Socket)│ │  Controller │ │  (VXLAN)    │     │  │  │
│  │  │  └──────┬──────┘ └─────────────┘ └─────────────┘     │  │  │
│  │  └─────────┼─────────────────────────────────────────────┘  │  │
│  │            │                                                 │  │
│  │  ┌─────────┴─────────────────────────────────────────────┐  │  │
│  │  │              OVS + ovn-controller                      │  │  │
│  │  │  ┌─────────┐  ┌─────────┐  ┌─────────────────────┐    │  │  │
│  │  │  │ br-int  │──│ br-ex   │──│ Physical NIC        │    │  │  │
│  │  │  └────┬────┘  └─────────┘  └─────────────────────┘    │  │  │
│  │  │       │                                                │  │  │
│  │  │  ┌────┴────┐  ┌─────────┐  ┌─────────┐                │  │  │
│  │  │  │ veth-xx │  │ veth-yy │  │ vxlan0  │                │  │  │
│  │  │  │ (Pod A) │  │ (Pod B) │  │(Tunnel) │                │  │  │
│  │  │  └─────────┘  └─────────┘  └─────────┘                │  │  │
│  │  └────────────────────────────────────────────────────────┘  │  │
│  └──────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────┘
```

### 组件说明

| 组件 | 部署方式 | 职责 |
|------|----------|------|
| **zstack-ovnkube-controller** | Deployment | 监听 K8s API，管理 OVN 逻辑对象 |
| **zstack-ovnkube-node** | DaemonSet | 节点网络配置，CNI Server，网关和隧道管理 |
| **zstack-ovn-cni** | 二进制文件 | CNI 插件，处理 ADD/DEL/CHECK 命令 |
| **OVN NB/SB DB** | 内置或外部 | 存储网络配置和逻辑流表 |
| **ovn-controller** | 每节点 | 读取 SB DB，编程 OVS 流表 |

## 快速开始

### 前置条件

- Kubernetes 1.25+
- Helm 3.0+
- 所有节点安装 OVS 2.13+

```bash
# 安装 OVS (Ubuntu/Debian)
sudo apt-get install -y openvswitch-switch
sudo systemctl enable --now openvswitch-switch

# 安装 OVS (CentOS/RHEL)
sudo yum install -y openvswitch
sudo systemctl enable --now openvswitch
```

### Standalone 模式安装

适用于独立的 Kubernetes 集群：

```bash
# 使用 Helm 安装
helm install zstack-ovn-kubernetes ./deploy/helm \
  -f ./deploy/helm/values-standalone.yaml \
  -n kube-system --create-namespace

# 验证安装
kubectl -n kube-system get pods -l app.kubernetes.io/name=zstack-ovn-kubernetes
```

### External 模式安装（ZStack 集成）

适用于与 ZStack 平台集成：

```bash
# 使用 Helm 安装
helm install zstack-ovn-kubernetes ./deploy/helm \
  -f ./deploy/helm/values-external.yaml \
  --set ovn.nbdbAddress="tcp:ZSTACK_IP:6641" \
  --set ovn.sbdbAddress="tcp:ZSTACK_IP:6642" \
  -n kube-system --create-namespace
```

### 创建子网

```yaml
# overlay-subnet.yaml
apiVersion: network.zstack.io/v1
kind: Subnet
metadata:
  name: pod-subnet
spec:
  cidr: "10.244.0.0/16"
  gateway: "10.244.0.1"
  excludeIPs:
    - "10.244.0.1"
```

```bash
kubectl apply -f overlay-subnet.yaml
```

## 配置

详细配置请参考 [配置文档](docs/configuration.md)。

### 主要配置参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `ovn.mode` | 部署模式 (standalone/external) | standalone |
| `ovn.nbdbAddress` | NB 数据库地址 | - |
| `ovn.sbdbAddress` | SB 数据库地址 | - |
| `network.clusterCIDR` | Pod 网络 CIDR | 10.244.0.0/16 |
| `network.serviceCIDR` | Service 网络 CIDR | 10.96.0.0/16 |
| `gateway.mode` | 网关模式 (shared/local) | local |
| `tunnel.type` | 隧道类型 (vxlan/geneve) | vxlan |

## 开发

### 构建

```bash
# 构建所有组件
make build

# 构建特定组件
make build-controller
make build-node
make build-cni

# 构建镜像
make docker-build
```

### 测试

```bash
# 运行单元测试
make test

# 运行属性测试
make test-property

# 运行集成测试
make test-integration

# 运行 E2E 测试
make test-e2e
```

## 文档

- [架构设计](docs/architecture.md) - 详细的架构说明
- [Standalone 部署指南](docs/standalone-deployment.md) - 独立模式部署
- [External 模式指南](docs/external-mode.md) - ZStack 集成模式
- [配置参考](docs/configuration.md) - 完整配置说明
- [故障排查](docs/troubleshooting.md) - 常见问题解决

## 示例

- [Overlay 网络配置](examples/overlay-network/) - 基础 Overlay 网络
- [Underlay 网络配置](examples/underlay-network/) - VLAN 直通网络
- [NetworkPolicy 示例](examples/network-policy/) - 网络策略配置

## 参考

本项目参考了 [OVN-Kubernetes](https://github.com/ovn-org/ovn-kubernetes) 的设计和实现。

## 许可证

Apache License 2.0
