# 部署指南

本文档提供 zstack-ovn-kubernetes CNI 插件的完整部署指南，涵盖环境准备、两种部署模式以及验证步骤。

## 目录

1. [环境准备](#环境准备)
2. [部署模式选择](#部署模式选择)
3. [Standalone 模式部署](#standalone-模式部署)
4. [External 模式部署](#external-模式部署)
5. [验证部署](#验证部署)
6. [升级与卸载](#升级与卸载)

## 环境准备

### 1. Kubernetes 集群要求

| 组件 | 最低版本 | 推荐版本 |
|------|----------|----------|
| Kubernetes | 1.20+ | 1.25+ |
| Helm | 3.0+ | 3.10+ |
| kubectl | 与集群版本匹配 | - |

```bash
# 检查集群状态
kubectl cluster-info
kubectl get nodes

# 检查 Helm 版本
helm version
```

### 2. 节点要求

所有 Kubernetes 节点必须满足以下条件：

#### 2.1 安装 Open vSwitch

```bash
# Ubuntu/Debian
sudo apt-get update
sudo apt-get install -y openvswitch-switch
sudo systemctl enable --now openvswitch-switch

# CentOS/RHEL 7
sudo yum install -y epel-release
sudo yum install -y openvswitch
sudo systemctl enable --now openvswitch

# CentOS/RHEL 8+
sudo dnf install -y openvswitch
sudo systemctl enable --now openvswitch

# 验证安装
ovs-vsctl --version
ovs-vsctl show
```

#### 2.2 加载内核模块

```bash
# 加载必要的内核模块
sudo modprobe openvswitch
sudo modprobe vxlan
sudo modprobe geneve

# 设置开机自动加载
cat <<EOF | sudo tee /etc/modules-load.d/ovn-kubernetes.conf
openvswitch
vxlan
geneve
EOF
```

#### 2.3 网络配置

确保节点间网络连通：

```bash
# 检查节点间连通性（在每个节点上执行）
ping <other-node-ip>

# 确保 VXLAN 端口未被防火墙阻止
# 默认端口: 4789/UDP
sudo firewall-cmd --add-port=4789/udp --permanent
sudo firewall-cmd --reload

# 或使用 iptables
sudo iptables -A INPUT -p udp --dport 4789 -j ACCEPT
```

### 3. 存储要求（仅 HA 模式）

高可用模式需要持久化存储：

```bash
# 检查可用的 StorageClass
kubectl get storageclass

# 如果没有默认 StorageClass，需要先配置一个
# 例如使用 local-path-provisioner
kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/deploy/local-path-storage.yaml
```

## 部署模式选择

zstack-ovn-kubernetes 支持两种部署模式：

| 特性 | Standalone 模式 | External 模式 |
|------|-----------------|---------------|
| OVN 数据库 | 自管理 | 连接外部（ZStack） |
| 适用场景 | 独立 K8s 集群 | ZStack 平台集成 |
| VM-Pod 互通 | 不支持 | 支持 |
| 部署复杂度 | 较高 | 较低 |
| 依赖 | 无外部依赖 | 需要 ZStack 平台 |

### 选择建议

- **独立 Kubernetes 集群**: 选择 Standalone 模式
- **与 ZStack 平台集成**: 选择 External 模式
- **开发测试环境**: Standalone 单节点模式
- **生产环境**: Standalone HA 模式或 External 模式

## Standalone 模式部署

Standalone 模式会自动部署和管理 OVN 数据库组件。

### 单节点模式（开发/测试）

适用于开发、测试和学习环境：

```bash
# 方式一：使用 Helm
helm install zstack-ovn-kubernetes ./deploy/helm \
  -f ./deploy/helm/values-standalone.yaml \
  -n kube-system --create-namespace

# 方式二：使用 Kustomize
kubectl apply -k deploy/standalone/single/

# 方式三：使用原始 YAML
kubectl apply -f deploy/yaml/
```

### 高可用模式（生产环境）

适用于生产环境，提供 3 节点 Raft 集群：

```bash
# 使用 Helm
helm install zstack-ovn-kubernetes ./deploy/helm \
  -f ./deploy/helm/values-standalone-ha.yaml \
  -n kube-system --create-namespace

# 或使用 Kustomize
kubectl apply -k deploy/standalone/ha/
```

### 自定义配置

```bash
# 创建自定义 values 文件
cat <<EOF > my-values.yaml
ovn:
  mode: standalone
  standalone:
    haMode: ha  # 或 single

network:
  clusterCIDR: "10.244.0.0/16"
  serviceCIDR: "10.96.0.0/16"
  nodeSubnetSize: 24

gateway:
  mode: local

tunnel:
  type: vxlan
  port: 4789
EOF

# 使用自定义配置安装
helm install zstack-ovn-kubernetes ./deploy/helm \
  -f my-values.yaml \
  -n kube-system --create-namespace
```

## External 模式部署

External 模式连接到 ZStack 管理的 OVN 数据库。

### 前置条件

1. ZStack 平台已启用 OVN 网络
2. 获取 ZStack OVN 数据库地址
3. 确保 K8s 节点可以访问 ZStack OVN 数据库端口

### 获取 ZStack OVN 数据库地址

```bash
# 在 ZStack 管理节点上执行
# 查看 NB DB 地址
cat /etc/zstack/ovn/ovn-nb-db.conf

# 查看 SB DB 地址
cat /etc/zstack/ovn/ovn-sb-db.conf

# 或通过 ZStack API 获取
zstack-cli QueryOvnController
```

### 基础部署

```bash
# 设置 ZStack OVN 数据库地址
export ZSTACK_OVN_NB_DB="tcp:192.168.1.100:6641"
export ZSTACK_OVN_SB_DB="tcp:192.168.1.100:6642"

# 使用 Helm 安装
helm install zstack-ovn-kubernetes ./deploy/helm \
  -f ./deploy/helm/values-external.yaml \
  --set ovn.nbdbAddress="${ZSTACK_OVN_NB_DB}" \
  --set ovn.sbdbAddress="${ZSTACK_OVN_SB_DB}" \
  -n kube-system --create-namespace
```

### SSL 加密连接（推荐）

```bash
# 准备 SSL 证书
kubectl create secret generic ovn-ssl-certs \
  -n kube-system \
  --from-file=ca.crt=/path/to/ca.crt \
  --from-file=client.crt=/path/to/client.crt \
  --from-file=client.key=/path/to/client.key

# 使用 SSL 连接
helm install zstack-ovn-kubernetes ./deploy/helm \
  -f ./deploy/helm/values-external.yaml \
  --set ovn.nbdbAddress="ssl:192.168.1.100:6641" \
  --set ovn.sbdbAddress="ssl:192.168.1.100:6642" \
  --set ovn.ssl.enabled=true \
  --set ovn.ssl.caCert="/etc/ovn/ssl/ca.crt" \
  --set ovn.ssl.clientCert="/etc/ovn/ssl/client.crt" \
  --set ovn.ssl.clientKey="/etc/ovn/ssl/client.key" \
  -n kube-system --create-namespace
```

### 高可用配置

如果 ZStack 部署了多个 OVN 数据库节点：

```bash
helm install zstack-ovn-kubernetes ./deploy/helm \
  -f ./deploy/helm/values-external.yaml \
  --set ovn.nbdbAddress="tcp:192.168.1.100:6641,tcp:192.168.1.101:6641,tcp:192.168.1.102:6641" \
  --set ovn.sbdbAddress="tcp:192.168.1.100:6642,tcp:192.168.1.101:6642,tcp:192.168.1.102:6642" \
  -n kube-system --create-namespace
```

### 引用 ZStack Logical Switch

在 External 模式下，可以引用 ZStack 已有的 Logical Switch：

```yaml
# zstack-subnet.yaml
apiVersion: network.zstack.io/v1
kind: Subnet
metadata:
  name: zstack-vpc-subnet
spec:
  cidr: "10.244.0.0/24"
  gateway: "10.244.0.1"
  # 引用 ZStack 已有的 Logical Switch
  externalLogicalSwitch: "ls-zstack-vpc-subnet-uuid"
  excludeIPs:
    - "10.244.0.1"
    - "10.244.0.2-10.244.0.10"
```

```bash
kubectl apply -f zstack-subnet.yaml
```

## 验证部署

### 1. 检查组件状态

```bash
# 检查所有 Pod 状态
kubectl -n kube-system get pods -l app.kubernetes.io/name=zstack-ovn-kubernetes

# 检查 Controller 日志
kubectl -n kube-system logs -l app=zstack-ovnkube-controller --tail=50

# 检查 Node Agent 日志
kubectl -n kube-system logs -l app=zstack-ovnkube-node --tail=50
```

### 2. 验证 OVN 数据库连接

```bash
# Standalone 模式
kubectl -n kube-system exec -it deploy/ovn-nb-db -- ovn-nbctl show

# External 模式
kubectl -n kube-system exec -it deploy/zstack-ovnkube-controller -- \
  ovn-nbctl --db=tcp:ZSTACK_IP:6641 show
```

### 3. 验证节点注册

```bash
# 查看 Chassis 列表
kubectl -n kube-system exec -it deploy/ovn-sb-db -- ovn-sbctl show

# 或在 External 模式下
ovn-sbctl --db=tcp:ZSTACK_IP:6642 show
```

### 4. 创建测试 Pod

```bash
# 创建测试 Pod
kubectl run test-pod-1 --image=busybox --command -- sleep 3600
kubectl run test-pod-2 --image=busybox --command -- sleep 3600

# 等待 Pod 就绪
kubectl wait --for=condition=Ready pod/test-pod-1 pod/test-pod-2 --timeout=60s

# 检查 Pod IP
kubectl get pods -o wide

# 测试 Pod 间连通性
kubectl exec test-pod-1 -- ping -c 3 <test-pod-2-ip>
```

### 5. 验证 Service 负载均衡

```bash
# 创建 Deployment 和 Service
kubectl create deployment nginx --image=nginx --replicas=2
kubectl expose deployment nginx --port=80

# 测试 Service 访问
kubectl run curl-test --image=curlimages/curl --rm -it --restart=Never -- \
  curl -s http://nginx.default.svc.cluster.local

# 检查 OVN Load Balancer
kubectl -n kube-system exec -it deploy/ovn-nb-db -- ovn-nbctl lb-list
```

## 升级与卸载

### 升级

```bash
# 使用 Helm 升级
helm upgrade zstack-ovn-kubernetes ./deploy/helm \
  -f my-values.yaml \
  -n kube-system

# 查看升级状态
helm history zstack-ovn-kubernetes -n kube-system
```

### 卸载

```bash
# 使用 Helm 卸载
helm uninstall zstack-ovn-kubernetes -n kube-system

# 清理 CRD（可选）
kubectl delete crd subnets.network.zstack.io

# 清理残留资源
kubectl -n kube-system delete configmap zstack-ovn-config
kubectl -n kube-system delete secret ovn-ssl-certs
```

### 清理节点

在每个节点上执行：

```bash
# 删除 CNI 配置
sudo rm -f /etc/cni/net.d/10-zstack-ovn.conflist

# 删除 CNI 二进制
sudo rm -f /opt/cni/bin/zstack-ovn-cni

# 清理 OVS 配置（谨慎操作）
sudo ovs-vsctl del-br br-int
```

## 下一步

- [配置参考](configuration.md) - 详细配置说明
- [故障排查](troubleshooting.md) - 常见问题解决
- [网络策略](network-policy.md) - NetworkPolicy 使用指南
