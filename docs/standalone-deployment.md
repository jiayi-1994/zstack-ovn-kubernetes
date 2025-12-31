# ZStack OVN Kubernetes - Standalone 模式部署指南

本文档详细介绍如何在 Standalone 模式下部署 zstack-ovn-kubernetes CNI 插件。Standalone 模式适用于独立的 Kubernetes 集群，无需依赖外部 ZStack 平台。

## 目录

1. [概述](#概述)
2. [模式选择](#模式选择)
3. [前置条件](#前置条件)
4. [单节点模式部署](#单节点模式部署)
5. [高可用模式部署](#高可用模式部署)
6. [验证部署](#验证部署)
7. [故障恢复](#故障恢复)
8. [常见问题](#常见问题)

## 概述

Standalone 模式下，zstack-ovn-kubernetes 会自动部署和管理 OVN 数据库组件，包括：

- **OVN Northbound Database (NB DB)**: 存储高级网络配置（逻辑交换机、路由器、端口、ACL）
- **OVN Southbound Database (SB DB)**: 存储逻辑流表和 Chassis 绑定信息
- **OVN northd**: 将 NB DB 配置转换为 SB DB 逻辑流
- **ovn-controller**: 在每个节点上运行，将逻辑流编程到 OVS

### 架构图

```
┌─────────────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │                    Control Plane                            │ │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐                  │ │
│  │  │  NB DB   │  │  SB DB   │  │  northd  │                  │ │
│  │  │ (6641)   │  │ (6642)   │  │          │                  │ │
│  │  └────┬─────┘  └────┬─────┘  └────┬─────┘                  │ │
│  │       │             │             │                         │ │
│  │       └─────────────┴─────────────┘                         │ │
│  │                     │                                        │ │
│  │              ┌──────┴──────┐                                │ │
│  │              │ ClusterIP   │                                │ │
│  │              │  Services   │                                │ │
│  │              └──────┬──────┘                                │ │
│  └─────────────────────┼────────────────────────────────────────┘ │
│                        │                                         │
│  ┌─────────────────────┼────────────────────────────────────────┐ │
│  │                Worker Nodes                                   │ │
│  │  ┌──────────────────┼──────────────────┐                     │ │
│  │  │    ovn-controller (DaemonSet)       │                     │ │
│  │  │  ┌─────────┐  ┌─────────┐  ┌─────────┐                   │ │
│  │  │  │ node-1  │  │ node-2  │  │ node-3  │                   │ │
│  │  │  └─────────┘  └─────────┘  └─────────┘                   │ │
│  │  └─────────────────────────────────────────┘                 │ │
│  └───────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

## 模式选择

Standalone 模式提供两种部署方式：

| 特性 | 单节点模式 (Single) | 高可用模式 (HA) |
|------|---------------------|-----------------|
| 数据库副本数 | 1 | 3 (Raft 集群) |
| 故障容忍 | 无 | 可容忍 1 节点故障 |
| 数据持久化 | 可选 | 必需 (PVC) |
| 资源消耗 | 低 | 较高 |
| 复杂度 | 简单 | 较复杂 |
| 适用场景 | 开发、测试、学习 | 生产环境 |

### 选择建议

- **开发/测试环境**: 使用单节点模式，快速部署，资源消耗低
- **生产环境**: 使用高可用模式，确保服务可靠性
- **学习/实验**: 使用单节点模式，便于理解和调试

## 前置条件

### 1. Kubernetes 集群

- Kubernetes 版本 >= 1.20
- 对于 HA 模式，建议至少 3 个节点

```bash
# 检查集群状态
kubectl get nodes
kubectl cluster-info
```

### 2. Open vSwitch

所有节点必须安装并运行 OVS：

```bash
# Ubuntu/Debian
sudo apt-get update
sudo apt-get install -y openvswitch-switch
sudo systemctl enable --now openvswitch-switch

# CentOS/RHEL
sudo yum install -y openvswitch
sudo systemctl enable --now openvswitch

# 验证安装
ovs-vsctl --version
ovs-vsctl show
```

### 3. 内核模块

确保加载必要的内核模块：

```bash
# 加载模块
sudo modprobe openvswitch
sudo modprobe vxlan
sudo modprobe geneve

# 设置开机自动加载
echo "openvswitch" | sudo tee /etc/modules-load.d/openvswitch.conf
echo "vxlan" | sudo tee -a /etc/modules-load.d/openvswitch.conf
```

### 4. 存储类 (仅 HA 模式)

HA 模式需要 StorageClass 来创建 PersistentVolumeClaim：

```bash
# 检查可用的 StorageClass
kubectl get storageclass

# 如果没有默认 StorageClass，需要先配置一个
```

## 单节点模式部署

### 方式一：使用 Kustomize

```bash
# 克隆仓库
git clone https://github.com/jiayi-1994/zstack-ovn-kubernetes.git
cd zstack-ovn-kubernetes

# 部署
kubectl apply -k deploy/standalone/single/

# 查看部署状态
kubectl -n ovn-kubernetes get pods -w
```

### 方式二：手动部署

```bash
cd deploy/standalone/single/

# 1. 创建命名空间
kubectl apply -f namespace.yaml

# 2. 部署 NB 数据库
kubectl apply -f ovn-nb-db.yaml

# 3. 等待 NB DB 就绪
kubectl -n ovn-kubernetes wait --for=condition=ready pod -l app.kubernetes.io/name=ovn-nb-db --timeout=120s

# 4. 部署 SB 数据库
kubectl apply -f ovn-sb-db.yaml

# 5. 等待 SB DB 就绪
kubectl -n ovn-kubernetes wait --for=condition=ready pod -l app.kubernetes.io/name=ovn-sb-db --timeout=120s

# 6. 部署 northd
kubectl apply -f ovn-northd.yaml

# 7. 部署 ovn-controller
kubectl apply -f ovn-controller.yaml
```

### 方式三：使用 Helm

```bash
# 添加 Helm 仓库 (如果已发布)
# helm repo add zstack https://charts.zstack.io

# 使用本地 Chart
helm install zstack-ovn-kubernetes deploy/helm/ \
  -f deploy/helm/values-standalone.yaml \
  -n ovn-kubernetes --create-namespace
```

## 高可用模式部署

### 方式一：使用 Kustomize

```bash
# 部署 HA 模式
kubectl apply -k deploy/standalone/ha/

# 查看部署状态
kubectl -n ovn-kubernetes get pods -w

# 等待所有 Pod 就绪
kubectl -n ovn-kubernetes wait --for=condition=ready pod --all --timeout=300s
```

### 方式二：手动部署

```bash
cd deploy/standalone/ha/

# 1. 创建命名空间
kubectl apply -f namespace.yaml

# 2. 部署 NB DB 集群
kubectl apply -f ovn-nb-db.yaml

# 3. 等待 NB DB 集群形成
kubectl -n ovn-kubernetes rollout status statefulset/ovn-nb-db --timeout=300s

# 4. 部署 SB DB 集群
kubectl apply -f ovn-sb-db.yaml

# 5. 等待 SB DB 集群形成
kubectl -n ovn-kubernetes rollout status statefulset/ovn-sb-db --timeout=300s

# 6. 部署 northd
kubectl apply -f ovn-northd.yaml

# 7. 部署 ovn-controller
kubectl apply -f ovn-controller.yaml
```

### 方式三：使用 Helm

```bash
helm install zstack-ovn-kubernetes deploy/helm/ \
  -f deploy/helm/values-standalone-ha.yaml \
  -n ovn-kubernetes --create-namespace
```

## 验证部署

### 1. 检查 Pod 状态

```bash
kubectl -n ovn-kubernetes get pods -o wide
```

预期输出（单节点模式）：
```
NAME                          READY   STATUS    RESTARTS   AGE
ovn-nb-db-xxx                 1/1     Running   0          5m
ovn-sb-db-xxx                 1/1     Running   0          5m
ovn-northd-xxx                1/1     Running   0          4m
ovn-controller-node1          1/1     Running   0          3m
ovn-controller-node2          1/1     Running   0          3m
```

预期输出（HA 模式）：
```
NAME                          READY   STATUS    RESTARTS   AGE
ovn-nb-db-0                   1/1     Running   0          10m
ovn-nb-db-1                   1/1     Running   0          10m
ovn-nb-db-2                   1/1     Running   0          10m
ovn-sb-db-0                   1/1     Running   0          10m
ovn-sb-db-1                   1/1     Running   0          10m
ovn-sb-db-2                   1/1     Running   0          10m
ovn-northd-xxx                1/1     Running   0          9m
ovn-northd-yyy                1/1     Running   0          9m
ovn-controller-node1          1/1     Running   0          8m
ovn-controller-node2          1/1     Running   0          8m
```

### 2. 检查数据库连接

```bash
# 检查 NB DB
kubectl -n ovn-kubernetes exec -it deploy/ovn-nb-db -- \
  ovsdb-client list-dbs tcp:127.0.0.1:6641

# 检查 SB DB
kubectl -n ovn-kubernetes exec -it deploy/ovn-sb-db -- \
  ovsdb-client list-dbs tcp:127.0.0.1:6642
```

### 3. 检查 OVN 状态

```bash
# 查看逻辑交换机
kubectl -n ovn-kubernetes exec -it deploy/ovn-nb-db -- ovn-nbctl show

# 查看 Chassis 注册
kubectl -n ovn-kubernetes exec -it deploy/ovn-sb-db -- ovn-sbctl show
```

### 4. 检查 Raft 集群状态 (仅 HA 模式)

```bash
# NB DB 集群状态
kubectl -n ovn-kubernetes exec -it ovn-nb-db-0 -- \
  ovs-appctl -t /var/run/ovn/ovnnb_db.ctl cluster/status OVN_Northbound

# SB DB 集群状态
kubectl -n ovn-kubernetes exec -it ovn-sb-db-0 -- \
  ovs-appctl -t /var/run/ovn/ovnsb_db.ctl cluster/status OVN_Southbound
```

### 5. 检查节点 OVS 配置

在任意节点上执行：
```bash
# 查看 OVS 配置
ovs-vsctl show

# 查看 external-ids
ovs-vsctl get open . external-ids

# 查看 br-int 网桥
ovs-vsctl list-ports br-int
```

## 故障恢复

### 单节点模式故障恢复

由于单节点模式没有冗余，故障恢复需要重新部署：

```bash
# 1. 删除现有部署
kubectl delete -k deploy/standalone/single/

# 2. 重新部署
kubectl apply -k deploy/standalone/single/
```

**注意**: 单节点模式下，数据库故障会导致数据丢失。

### HA 模式故障恢复

#### 单个 Pod 故障

StatefulSet 会自动重建故障的 Pod：

```bash
# 查看 Pod 状态
kubectl -n ovn-kubernetes get pods

# 如果 Pod 卡在某个状态，可以手动删除
kubectl -n ovn-kubernetes delete pod ovn-nb-db-X

# StatefulSet 会自动创建新的 Pod
```

#### Raft 集群恢复

如果 Raft 集群出现问题：

```bash
# 1. 检查集群状态
kubectl -n ovn-kubernetes exec -it ovn-nb-db-0 -- \
  ovs-appctl -t /var/run/ovn/ovnnb_db.ctl cluster/status OVN_Northbound

# 2. 如果需要，强制移除故障节点
kubectl -n ovn-kubernetes exec -it ovn-nb-db-0 -- \
  ovs-appctl -t /var/run/ovn/ovnnb_db.ctl cluster/kick OVN_Northbound <server-id>
```

#### 完全重建集群

如果集群无法恢复，需要完全重建：

```bash
# 1. 删除所有资源
kubectl delete -k deploy/standalone/ha/

# 2. 删除 PVC（数据会丢失！）
kubectl -n ovn-kubernetes delete pvc --all

# 3. 重新部署
kubectl apply -k deploy/standalone/ha/
```

### ovn-controller 故障恢复

ovn-controller 是无状态的，重启即可恢复：

```bash
# 重启特定节点的 ovn-controller
kubectl -n ovn-kubernetes delete pod ovn-controller-<node-name>

# DaemonSet 会自动重建
```

## 常见问题

### Q1: OVS 未运行

**症状**: ovn-controller Pod 启动失败，日志显示无法连接 OVS

**解决方案**:
```bash
# 检查 OVS 状态
systemctl status openvswitch-switch

# 启动 OVS
systemctl start openvswitch-switch

# 重启 ovn-controller Pod
kubectl -n ovn-kubernetes delete pod -l app.kubernetes.io/name=ovn-controller
```

### Q2: 数据库连接超时

**症状**: northd 或 ovn-controller 无法连接数据库

**解决方案**:
```bash
# 检查 Service 是否正常
kubectl -n ovn-kubernetes get svc

# 检查 Endpoints
kubectl -n ovn-kubernetes get endpoints

# 检查 Pod 日志
kubectl -n ovn-kubernetes logs deploy/ovn-nb-db
```

### Q3: Raft 集群无法形成

**症状**: HA 模式下数据库 Pod 一直处于 CrashLoopBackOff

**解决方案**:
```bash
# 检查 DNS 解析
kubectl -n ovn-kubernetes exec -it ovn-nb-db-0 -- \
  nslookup ovn-nb-db-headless.ovn-kubernetes.svc.cluster.local

# 检查网络连通性
kubectl -n ovn-kubernetes exec -it ovn-nb-db-0 -- \
  nc -zv ovn-nb-db-1.ovn-nb-db-headless.ovn-kubernetes.svc.cluster.local 6643

# 如果 DNS 有问题，检查 CoreDNS
kubectl -n kube-system get pods -l k8s-app=kube-dns
```

### Q4: PVC 无法创建

**症状**: HA 模式下 StatefulSet Pod 卡在 Pending

**解决方案**:
```bash
# 检查 PVC 状态
kubectl -n ovn-kubernetes get pvc

# 检查 StorageClass
kubectl get storageclass

# 如果没有默认 StorageClass，需要配置一个或在 values 中指定
```

### Q5: 节点未注册到 OVN

**症状**: ovn-sbctl show 不显示某些节点

**解决方案**:
```bash
# 检查节点上的 OVS external-ids
ovs-vsctl get open . external-ids

# 确保 ovn-remote 设置正确
ovs-vsctl set open . external-ids:ovn-remote="tcp:ovn-sb-db.ovn-kubernetes.svc.cluster.local:6642"

# 重启 ovn-controller
kubectl -n ovn-kubernetes delete pod ovn-controller-<node-name>
```

## 下一步

部署完成后，您可以：

1. **部署 zstack-ovnkube-controller**: 管理 Kubernetes 网络资源
2. **部署 zstack-ovnkube-node**: 节点网络代理
3. **安装 CNI 插件**: 配置 Pod 网络
4. **创建 Subnet CRD**: 定义网络子网

详细信息请参考：
- [配置指南](configuration.md)
- [网络策略](network-policy.md)
- [故障排查](troubleshooting.md)
