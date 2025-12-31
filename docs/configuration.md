# 配置参考

本文档详细说明 zstack-ovn-kubernetes 的所有配置参数及其使用方法。

## 目录

1. [配置方式](#配置方式)
2. [OVN 配置](#ovn-配置)
3. [网络配置](#网络配置)
4. [网关配置](#网关配置)
5. [隧道配置](#隧道配置)
6. [DPDK 配置](#dpdk-配置)
7. [组件配置](#组件配置)
8. [日志配置](#日志配置)
9. [示例配置](#示例配置)

## 配置方式

zstack-ovn-kubernetes 支持多种配置方式：

### 1. Helm Values 文件

推荐使用 Helm values 文件进行配置：

```bash
helm install zstack-ovn-kubernetes ./deploy/helm \
  -f my-values.yaml \
  -n kube-system
```

### 2. 命令行参数

通过 `--set` 参数覆盖配置：

```bash
helm install zstack-ovn-kubernetes ./deploy/helm \
  --set ovn.mode=external \
  --set ovn.nbdbAddress="tcp:192.168.1.100:6641" \
  -n kube-system
```

### 3. 环境变量

组件支持通过环境变量配置：

| 环境变量 | 对应配置 | 说明 |
|----------|----------|------|
| `ZSTACK_OVN_MODE` | `ovn.mode` | 部署模式 |
| `ZSTACK_OVN_NBDB_ADDRESS` | `ovn.nbdbAddress` | NB DB 地址 |
| `ZSTACK_OVN_SBDB_ADDRESS` | `ovn.sbdbAddress` | SB DB 地址 |
| `ZSTACK_CLUSTER_CIDR` | `network.clusterCIDR` | Pod 网络 CIDR |
| `ZSTACK_SERVICE_CIDR` | `network.serviceCIDR` | Service 网络 CIDR |
| `ZSTACK_GATEWAY_MODE` | `gateway.mode` | 网关模式 |
| `ZSTACK_TUNNEL_TYPE` | `tunnel.type` | 隧道类型 |
| `ZSTACK_LOG_LEVEL` | `logging.level` | 日志级别 |

### 4. ConfigMap

通过 ConfigMap 配置 CNI：

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: zstack-ovn-config
  namespace: kube-system
data:
  config.yaml: |
    ovn:
      mode: standalone
    network:
      clusterCIDR: "10.244.0.0/16"
```

## OVN 配置

### 基础配置

```yaml
ovn:
  # 部署模式
  # - standalone: 自管理 OVN 数据库
  # - external: 连接外部 OVN 数据库（如 ZStack）
  mode: standalone

  # Northbound Database 地址
  # 格式: tcp:IP:PORT 或 ssl:IP:PORT
  # 多地址用逗号分隔（用于 HA）
  nbdbAddress: "tcp:127.0.0.1:6641"

  # Southbound Database 地址
  sbdbAddress: "tcp:127.0.0.1:6642"
```

### SSL 配置

```yaml
ovn:
  ssl:
    # 是否启用 SSL
    enabled: false
    
    # CA 证书路径
    caCert: "/etc/ovn/ssl/ca.crt"
    
    # 客户端证书路径
    clientCert: "/etc/ovn/ssl/client.crt"
    
    # 客户端私钥路径
    clientKey: "/etc/ovn/ssl/client.key"
```

### Standalone 模式配置

```yaml
ovn:
  standalone:
    # 高可用模式
    # - single: 单实例（开发/测试）
    # - ha: 3 节点 Raft 集群（生产）
    haMode: single

    # OVN 镜像配置
    image:
      repository: ghcr.io/ovn-org/ovn-kubernetes/ovn-kube-ubuntu
      tag: master
      pullPolicy: IfNotPresent

    # NB DB 配置
    nbdb:
      # 副本数（single=1, ha=3）
      replicas: 1
      
      # 持久化存储
      storage:
        enabled: false
        size: 1Gi
        storageClass: ""
      
      # 资源限制
      resources:
        requests:
          cpu: 100m
          memory: 256Mi
        limits:
          cpu: 500m
          memory: 512Mi

    # SB DB 配置
    sbdb:
      replicas: 1
      storage:
        enabled: false
        size: 1Gi
        storageClass: ""
      resources:
        requests:
          cpu: 100m
          memory: 256Mi
        limits:
          cpu: 500m
          memory: 512Mi

    # northd 配置
    northd:
      # 副本数（HA 模式下建议 2）
      replicas: 1
      resources:
        requests:
          cpu: 100m
          memory: 256Mi
        limits:
          cpu: 500m
          memory: 512Mi
```

## 网络配置

```yaml
network:
  # Pod 网络 CIDR
  # 所有 Pod IP 将从此范围分配
  clusterCIDR: "10.244.0.0/16"

  # Service 网络 CIDR
  # Kubernetes Service ClusterIP 范围
  serviceCIDR: "10.96.0.0/16"

  # 每个节点的子网大小
  # 例如: 24 表示每个节点分配 /24 子网（254 个 Pod IP）
  nodeSubnetSize: 24

  # MTU 配置
  # 0 表示自动检测
  mtu: 0

  # 启用 IPv6
  ipv6:
    enabled: false
    clusterCIDR: "fd00:10:244::/48"
    serviceCIDR: "fd00:10:96::/112"
```

### 子网 CIDR 规划建议

| 集群规模 | 推荐 clusterCIDR | nodeSubnetSize | 最大节点数 | 每节点 Pod 数 |
|----------|------------------|----------------|------------|---------------|
| 小型 | 10.244.0.0/16 | 24 | 256 | 254 |
| 中型 | 10.244.0.0/14 | 24 | 1024 | 254 |
| 大型 | 10.0.0.0/8 | 22 | 16384 | 1022 |

## 网关配置

```yaml
gateway:
  # 网关模式
  # - shared: 集中式网关，所有出站流量经过指定节点
  # - local: 分布式网关，每个节点独立处理出站流量
  mode: local

  # 网关网络接口
  # 用于外部流量的物理网卡
  # 留空则自动检测默认路由接口
  interface: ""

  # 共享网关节点选择器（仅 shared 模式）
  nodeSelector:
    node-role.kubernetes.io/gateway: "true"

  # SNAT 配置
  snat:
    # 是否启用 SNAT
    enabled: true
    
    # 使用节点 IP 作为 SNAT 地址
    useNodeIP: true

  # Egress IP 配置
  egressIP:
    enabled: false
```

### 网关模式对比

| 特性 | Shared 模式 | Local 模式 |
|------|-------------|------------|
| 出站路径 | 集中到网关节点 | 本地节点直接出站 |
| 网络延迟 | 较高（需跨节点） | 较低 |
| 带宽瓶颈 | 网关节点 | 无 |
| 故障影响 | 网关故障影响全部 | 仅影响本节点 |
| 适用场景 | 需要固定出口 IP | 一般场景 |

## 隧道配置

```yaml
tunnel:
  # 隧道类型
  # - vxlan: VXLAN 隧道（推荐，与 ZStack 兼容）
  # - geneve: Geneve 隧道（支持更多元数据）
  type: vxlan

  # 隧道 UDP 端口
  # VXLAN 默认: 4789
  # Geneve 默认: 6081
  port: 4789

  # 隧道接口名称
  interface: "vxlan_sys_4789"

  # 隧道 MTU
  # 建议: 物理网卡 MTU - 50 (VXLAN 开销)
  mtu: 1400
```

### 隧道类型对比

| 特性 | VXLAN | Geneve |
|------|-------|--------|
| 标准 | RFC 7348 | RFC 8926 |
| 封装开销 | 50 字节 | 可变（最小 50 字节） |
| 元数据支持 | 有限 | 可扩展 |
| 硬件卸载 | 广泛支持 | 部分支持 |
| ZStack 兼容 | ✓ | ✗ |

## DPDK 配置

DPDK (Data Plane Development Kit) 通过绕过内核网络栈实现高性能数据包处理。

```yaml
dpdk:
  # 是否启用 DPDK
  # 启用后，DPDK 节点上的 Pod 将使用 vhost-user socket 而非 veth pair
  enabled: false

  # vhost-user socket 目录
  # 此目录必须对 OVS 和容器运行时都可访问
  socketDir: "/var/run/openvswitch"

  # vhost-user socket 模式
  # "client" - OVS 作为客户端，容器作为服务端 (dpdkvhostuserclient，推荐)
  # "server" - OVS 作为服务端，容器作为客户端 (dpdkvhostuser)
  socketMode: "client"

  # 多队列支持的队列数
  # 更高的值可以在多 CPU 核心时提升性能
  queues: 1

  # 最小 hugepages 内存要求 (MB)
  # 用于 DPDK 环境验证
  minHugepagesMB: 1024
```

### 环境变量配置

| 环境变量 | 说明 | 默认值 |
|---------|------|--------|
| `ZSTACK_OVN_DPDK_ENABLED` | 是否启用 DPDK | `false` |
| `ZSTACK_OVN_DPDK_SOCKET_DIR` | vhost-user socket 目录 | `/var/run/openvswitch` |
| `ZSTACK_OVN_DPDK_SOCKET_MODE` | socket 模式 (client/server) | `client` |
| `ZSTACK_OVN_DPDK_QUEUES` | 队列数 | `1` |
| `ZSTACK_OVN_DPDK_MIN_HUGEPAGES_MB` | 最小 hugepages (MB) | `1024` |

### DPDK 前置条件

1. 节点启用 hugepages：
```bash
# 配置 hugepages (2MB 页面)
echo 1024 > /sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages

# 或在 GRUB 中配置 (推荐，重启后生效)
GRUB_CMDLINE_LINUX="default_hugepagesz=2M hugepagesz=2M hugepages=1024"
```

2. OVS 启用 DPDK：
```bash
# 启用 DPDK 初始化
ovs-vsctl set Open_vSwitch . other_config:dpdk-init=true

# 配置 DPDK socket 内存 (每个 NUMA 节点的内存，单位 MB)
ovs-vsctl set Open_vSwitch . other_config:dpdk-socket-mem="1024"

# 可选：配置 lcore 掩码
ovs-vsctl set Open_vSwitch . other_config:dpdk-lcore-mask="0x1"
```

3. 验证 DPDK 状态：
```bash
# 检查 OVS DPDK 是否启用
ovs-vsctl get Open_vSwitch . other_config:dpdk-init

# 检查 hugepages 状态
cat /proc/meminfo | grep -i huge
```

### DPDK 与非 DPDK 混合部署

集群支持混合部署，部分节点使用 DPDK，部分节点使用内核态 OVS：

- DPDK 节点：Pod 使用 vhost-user socket 连接 OVS-DPDK
- 非 DPDK 节点：Pod 使用标准 veth pair 连接内核态 OVS
- CNI 会自动检测节点类型并选择合适的网络配置方式

## 组件配置

### Controller 配置

```yaml
controller:
  # 副本数
  replicas: 1

  # 镜像配置
  image:
    repository: ghcr.io/jiayi-1994/zstack-ovnkube-controller
    tag: "0.1.0"
    pullPolicy: IfNotPresent

  # 资源限制
  resources:
    limits:
      cpu: 500m
      memory: 512Mi
    requests:
      cpu: 100m
      memory: 128Mi

  # 节点选择器
  nodeSelector: {}

  # 容忍度
  tolerations:
    - key: node-role.kubernetes.io/master
      effect: NoSchedule
    - key: node-role.kubernetes.io/control-plane
      effect: NoSchedule

  # Leader Election 配置
  leaderElection:
    enabled: true
    leaseDuration: 15s
    renewDeadline: 10s
    retryPeriod: 2s
```

### Node Agent 配置

```yaml
node:
  # 镜像配置
  image:
    repository: ghcr.io/jiayi-1994/zstack-ovnkube-node
    tag: "0.1.0"
    pullPolicy: IfNotPresent

  # 资源限制
  resources:
    limits:
      cpu: 500m
      memory: 512Mi
    requests:
      cpu: 100m
      memory: 128Mi

  # 节点选择器
  nodeSelector: {}

  # 容忍度（默认容忍所有污点以在所有节点运行）
  tolerations:
    - operator: Exists

  # CNI Server 配置
  cniServer:
    # Unix Socket 路径
    socketPath: "/var/run/zstack-ovn/cni-server.sock"
    
    # 请求超时
    timeout: 30s
```

### CNI 配置

```yaml
cni:
  # CNI 二进制镜像
  image:
    repository: ghcr.io/jiayi-1994/zstack-ovn-cni
    tag: "0.1.0"
    pullPolicy: IfNotPresent

  # CNI 配置文件路径
  confDir: "/etc/cni/net.d"
  
  # CNI 二进制文件路径
  binDir: "/opt/cni/bin"

  # CNI 配置文件名
  confFile: "10-zstack-ovn.conflist"
```

## 日志配置

```yaml
logging:
  # 日志级别
  # - debug: 详细调试信息
  # - info: 一般信息
  # - warn: 警告信息
  # - error: 错误信息
  level: info

  # 日志格式
  # - json: JSON 格式（推荐生产环境）
  # - text: 文本格式（便于阅读）
  format: json

  # 是否输出调用位置
  addCaller: true

  # 是否输出堆栈跟踪（仅 error 级别）
  stacktrace: false
```

## 示例配置

### 开发环境配置

```yaml
# values-dev.yaml
ovn:
  mode: standalone
  standalone:
    haMode: single

network:
  clusterCIDR: "10.244.0.0/16"
  serviceCIDR: "10.96.0.0/16"

gateway:
  mode: local

tunnel:
  type: vxlan

logging:
  level: debug
  format: text

controller:
  replicas: 1
  resources:
    limits:
      cpu: 200m
      memory: 256Mi
```

### 生产环境配置

```yaml
# values-prod.yaml
ovn:
  mode: standalone
  standalone:
    haMode: ha
    nbdb:
      replicas: 3
      storage:
        enabled: true
        size: 5Gi
        storageClass: "fast-ssd"
      resources:
        limits:
          cpu: 1000m
          memory: 1Gi
    sbdb:
      replicas: 3
      storage:
        enabled: true
        size: 5Gi
        storageClass: "fast-ssd"
      resources:
        limits:
          cpu: 1000m
          memory: 1Gi
    northd:
      replicas: 2
      resources:
        limits:
          cpu: 500m
          memory: 512Mi

network:
  clusterCIDR: "10.244.0.0/14"
  serviceCIDR: "10.96.0.0/12"
  nodeSubnetSize: 24

gateway:
  mode: local

tunnel:
  type: vxlan
  mtu: 1400

logging:
  level: info
  format: json

controller:
  replicas: 2
  resources:
    limits:
      cpu: 1000m
      memory: 1Gi
```

### ZStack 集成配置

```yaml
# values-zstack.yaml
ovn:
  mode: external
  nbdbAddress: "tcp:192.168.1.100:6641,tcp:192.168.1.101:6641,tcp:192.168.1.102:6641"
  sbdbAddress: "tcp:192.168.1.100:6642,tcp:192.168.1.101:6642,tcp:192.168.1.102:6642"
  ssl:
    enabled: true
    caCert: "/etc/ovn/ssl/ca.crt"
    clientCert: "/etc/ovn/ssl/client.crt"
    clientKey: "/etc/ovn/ssl/client.key"

network:
  # 与 ZStack VPC 网络规划一致
  clusterCIDR: "10.100.0.0/16"
  serviceCIDR: "10.96.0.0/16"

gateway:
  mode: local

tunnel:
  type: vxlan

logging:
  level: info
  format: json
```

## 配置验证

安装后验证配置：

```bash
# 查看实际配置
kubectl -n kube-system get configmap zstack-ovn-config -o yaml

# 查看 Controller 启动参数
kubectl -n kube-system get deploy zstack-ovnkube-controller -o yaml | grep -A 20 "args:"

# 查看 Node Agent 配置
kubectl -n kube-system get daemonset zstack-ovnkube-node -o yaml | grep -A 20 "env:"
```
