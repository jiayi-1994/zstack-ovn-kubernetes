# 故障排查指南

本文档提供 zstack-ovn-kubernetes 的故障排查方法，包括常见问题、调试命令和日志分析。

## 目录

1. [诊断流程](#诊断流程)
2. [常见问题](#常见问题)
3. [调试命令](#调试命令)
4. [日志分析](#日志分析)
5. [网络连通性排查](#网络连通性排查)
6. [性能问题排查](#性能问题排查)

## 诊断流程

遇到问题时，建议按以下流程排查：

```
1. 检查组件状态
   └── kubectl get pods -n kube-system
   
2. 检查组件日志
   └── kubectl logs -n kube-system <pod-name>
   
3. 检查 OVN 数据库状态
   └── ovn-nbctl show / ovn-sbctl show
   
4. 检查节点 OVS 配置
   └── ovs-vsctl show
   
5. 检查网络连通性
   └── ping / traceroute / tcpdump
```

## 常见问题

### 1. Pod 无法获取 IP 地址

**症状**: Pod 一直处于 `ContainerCreating` 状态

**排查步骤**:

```bash
# 1. 检查 Pod 事件
kubectl describe pod <pod-name>

# 2. 检查 CNI 日志
kubectl -n kube-system logs -l app=zstack-ovnkube-node --tail=100 | grep -i error

# 3. 检查 CNI 配置文件
cat /etc/cni/net.d/10-zstack-ovn.conflist

# 4. 检查 CNI Server 是否运行
ls -la /var/run/zstack-ovn/cni-server.sock

# 5. 检查子网状态
kubectl get subnet -o wide
```

**常见原因及解决方案**:

| 原因 | 解决方案 |
|------|----------|
| CNI 配置文件不存在 | 重新部署 Node DaemonSet |
| CNI Server 未运行 | 检查 Node Agent Pod 状态 |
| 子网 IP 耗尽 | 扩大子网 CIDR 或清理未使用的 IP |
| OVN 数据库连接失败 | 检查数据库地址和网络连通性 |

### 2. Pod 间无法通信

**症状**: 同节点或跨节点 Pod 无法 ping 通

**排查步骤**:

```bash
# 1. 确认 Pod IP 和节点
kubectl get pods -o wide

# 2. 检查 OVN Logical Switch Port
kubectl -n kube-system exec -it deploy/ovn-nb-db -- \
  ovn-nbctl lsp-list <logical-switch-name>

# 3. 检查端口绑定
kubectl -n kube-system exec -it deploy/ovn-sb-db -- \
  ovn-sbctl show

# 4. 检查 OVS 流表
ovs-ofctl dump-flows br-int | grep <pod-ip>

# 5. 检查隧道状态
ovs-vsctl show | grep -A 5 vxlan
```

**常见原因及解决方案**:

| 原因 | 解决方案 |
|------|----------|
| LSP 未创建 | 检查 Controller 日志 |
| 端口未绑定到 Chassis | 检查 ovn-controller 状态 |
| 隧道未建立 | 检查节点间网络连通性 |
| 流表未下发 | 重启 ovn-controller |

### 3. Service 无法访问

**症状**: 通过 ClusterIP 无法访问 Service

**排查步骤**:

```bash
# 1. 检查 Service 和 Endpoints
kubectl get svc <service-name>
kubectl get endpoints <service-name>

# 2. 检查 OVN Load Balancer
kubectl -n kube-system exec -it deploy/ovn-nb-db -- \
  ovn-nbctl lb-list

# 3. 检查 Load Balancer 详情
kubectl -n kube-system exec -it deploy/ovn-nb-db -- \
  ovn-nbctl lb-list | grep <service-cluster-ip>

# 4. 检查 Controller 日志
kubectl -n kube-system logs deploy/zstack-ovnkube-controller | grep -i service
```

**常见原因及解决方案**:

| 原因 | 解决方案 |
|------|----------|
| Endpoints 为空 | 检查 Pod 标签是否匹配 |
| Load Balancer 未创建 | 检查 Service Controller 日志 |
| VIP 映射错误 | 删除并重建 Service |

### 4. OVN 数据库连接失败

**症状**: Controller 或 Node Agent 无法连接 OVN 数据库

**排查步骤**:

```bash
# 1. 检查数据库 Pod 状态
kubectl -n kube-system get pods -l app.kubernetes.io/name=ovn-nb-db
kubectl -n kube-system get pods -l app.kubernetes.io/name=ovn-sb-db

# 2. 检查数据库 Service
kubectl -n kube-system get svc | grep ovn

# 3. 测试数据库连接
kubectl -n kube-system exec -it deploy/zstack-ovnkube-controller -- \
  ovsdb-client list-dbs tcp:ovn-nb-db:6641

# 4. 检查网络策略
kubectl get networkpolicy -n kube-system
```

**常见原因及解决方案**:

| 原因 | 解决方案 |
|------|----------|
| 数据库 Pod 未就绪 | 等待 Pod 启动或检查 PVC |
| Service 配置错误 | 检查 Service 端口映射 |
| 网络策略阻止 | 添加允许规则 |
| SSL 证书问题 | 检查证书有效性和路径 |

### 5. 节点未注册到 OVN

**症状**: `ovn-sbctl show` 不显示某些节点

**排查步骤**:

```bash
# 1. 检查节点上的 ovn-controller
systemctl status ovn-controller

# 2. 检查 OVS external-ids
ovs-vsctl get open . external-ids

# 3. 检查 ovn-remote 配置
ovs-vsctl get open . external-ids:ovn-remote

# 4. 检查 ovn-controller 日志
journalctl -u ovn-controller -n 100
```

**常见原因及解决方案**:

| 原因 | 解决方案 |
|------|----------|
| ovn-controller 未运行 | 启动 ovn-controller 服务 |
| ovn-remote 配置错误 | 更新 external-ids |
| 系统 ID 未设置 | 设置 system-id |
| 网络不通 | 检查防火墙规则 |

### 6. NetworkPolicy 不生效

**症状**: 配置了 NetworkPolicy 但流量未被阻止

**排查步骤**:

```bash
# 1. 检查 NetworkPolicy
kubectl get networkpolicy -n <namespace>
kubectl describe networkpolicy <policy-name> -n <namespace>

# 2. 检查 OVN ACL
kubectl -n kube-system exec -it deploy/ovn-nb-db -- \
  ovn-nbctl acl-list <logical-switch>

# 3. 检查 Policy Controller 日志
kubectl -n kube-system logs deploy/zstack-ovnkube-controller | grep -i policy

# 4. 检查 Pod 标签
kubectl get pods -n <namespace> --show-labels
```

**常见原因及解决方案**:

| 原因 | 解决方案 |
|------|----------|
| 标签选择器不匹配 | 检查 Pod 标签 |
| ACL 未创建 | 检查 Controller 日志 |
| ACL 优先级问题 | 调整 ACL 优先级 |

## 调试命令

### OVN 数据库命令

```bash
# 进入 NB DB Pod
kubectl -n kube-system exec -it deploy/ovn-nb-db -- bash

# 查看所有逻辑交换机
ovn-nbctl ls-list

# 查看逻辑交换机详情
ovn-nbctl show

# 查看逻辑交换机端口
ovn-nbctl lsp-list <switch-name>

# 查看端口详情
ovn-nbctl lsp-get-addresses <port-name>
ovn-nbctl lsp-get-port-security <port-name>

# 查看 Load Balancer
ovn-nbctl lb-list
ovn-nbctl lb-list | grep <vip>

# 查看 ACL
ovn-nbctl acl-list <switch-name>

# 查看逻辑路由器
ovn-nbctl lr-list
ovn-nbctl lr-route-list <router-name>
```

```bash
# 进入 SB DB Pod
kubectl -n kube-system exec -it deploy/ovn-sb-db -- bash

# 查看 Chassis 列表
ovn-sbctl show

# 查看端口绑定
ovn-sbctl list Port_Binding

# 查看逻辑流
ovn-sbctl lflow-list

# 查看 MAC 绑定
ovn-sbctl list MAC_Binding
```

### OVS 命令

```bash
# 查看 OVS 配置
ovs-vsctl show

# 查看网桥端口
ovs-vsctl list-ports br-int

# 查看端口详情
ovs-vsctl list interface <port-name>

# 查看流表
ovs-ofctl dump-flows br-int
ovs-ofctl dump-flows br-int | grep <ip-or-mac>

# 查看流表统计
ovs-ofctl dump-flows br-int --no-stats

# 查看端口统计
ovs-ofctl dump-ports br-int

# 查看连接跟踪
ovs-appctl dpctl/dump-conntrack
```

### 网络诊断命令

```bash
# 在 Pod 内测试连通性
kubectl exec -it <pod-name> -- ping <target-ip>
kubectl exec -it <pod-name> -- traceroute <target-ip>
kubectl exec -it <pod-name> -- nc -zv <target-ip> <port>

# 抓包分析
# 在节点上抓取 Pod 流量
tcpdump -i <veth-interface> -nn

# 抓取隧道流量
tcpdump -i <tunnel-interface> -nn port 4789

# 抓取 OVS 内部流量
ovs-tcpdump -i br-int
```

### Kubernetes 诊断命令

```bash
# 查看 Pod 详情
kubectl describe pod <pod-name>

# 查看 Pod 日志
kubectl logs <pod-name>

# 查看事件
kubectl get events --sort-by='.lastTimestamp'

# 查看 Subnet 状态
kubectl get subnet -o wide
kubectl describe subnet <subnet-name>

# 查看 CNI 配置
kubectl -n kube-system get configmap zstack-ovn-config -o yaml
```

## 日志分析

### Controller 日志

```bash
# 查看 Controller 日志
kubectl -n kube-system logs deploy/zstack-ovnkube-controller

# 过滤错误日志
kubectl -n kube-system logs deploy/zstack-ovnkube-controller | grep -i error

# 过滤特定组件日志
kubectl -n kube-system logs deploy/zstack-ovnkube-controller | grep -i subnet
kubectl -n kube-system logs deploy/zstack-ovnkube-controller | grep -i service
kubectl -n kube-system logs deploy/zstack-ovnkube-controller | grep -i policy

# 实时查看日志
kubectl -n kube-system logs -f deploy/zstack-ovnkube-controller
```

### Node Agent 日志

```bash
# 查看特定节点的 Node Agent 日志
kubectl -n kube-system logs -l app=zstack-ovnkube-node --field-selector spec.nodeName=<node-name>

# 过滤 CNI 相关日志
kubectl -n kube-system logs -l app=zstack-ovnkube-node | grep -i cni

# 过滤网关相关日志
kubectl -n kube-system logs -l app=zstack-ovnkube-node | grep -i gateway
```

### 日志级别调整

```bash
# 临时调整日志级别（需要重启 Pod）
kubectl -n kube-system set env deploy/zstack-ovnkube-controller ZSTACK_LOG_LEVEL=debug

# 通过 Helm 调整
helm upgrade zstack-ovn-kubernetes ./deploy/helm \
  --set logging.level=debug \
  -n kube-system
```

### 常见日志模式

| 日志模式 | 含义 | 处理方式 |
|----------|------|----------|
| `connection refused` | 数据库连接失败 | 检查数据库状态 |
| `timeout` | 操作超时 | 检查网络和负载 |
| `already exists` | 资源已存在 | 通常可忽略 |
| `not found` | 资源不存在 | 检查资源是否创建 |
| `permission denied` | 权限不足 | 检查 RBAC 配置 |

## 网络连通性排查

### Pod 到 Pod 连通性

```bash
# 1. 获取 Pod 信息
POD1=$(kubectl get pod -l app=test1 -o jsonpath='{.items[0].metadata.name}')
POD2=$(kubectl get pod -l app=test2 -o jsonpath='{.items[0].metadata.name}')
POD2_IP=$(kubectl get pod $POD2 -o jsonpath='{.status.podIP}')

# 2. 测试连通性
kubectl exec $POD1 -- ping -c 3 $POD2_IP

# 3. 如果失败，检查 OVN 配置
# 检查两个 Pod 的 LSP
kubectl -n kube-system exec -it deploy/ovn-nb-db -- ovn-nbctl show | grep -A 5 $POD1
kubectl -n kube-system exec -it deploy/ovn-nb-db -- ovn-nbctl show | grep -A 5 $POD2

# 4. 检查端口绑定
kubectl -n kube-system exec -it deploy/ovn-sb-db -- ovn-sbctl show | grep -A 3 $POD1
```

### Pod 到 Service 连通性

```bash
# 1. 获取 Service 信息
SVC_IP=$(kubectl get svc <service-name> -o jsonpath='{.spec.clusterIP}')
SVC_PORT=$(kubectl get svc <service-name> -o jsonpath='{.spec.ports[0].port}')

# 2. 测试连通性
kubectl exec <pod-name> -- curl -s http://$SVC_IP:$SVC_PORT

# 3. 检查 OVN Load Balancer
kubectl -n kube-system exec -it deploy/ovn-nb-db -- ovn-nbctl lb-list | grep $SVC_IP

# 4. 检查 Endpoints
kubectl get endpoints <service-name>
```

### Pod 到外部网络连通性

```bash
# 1. 测试外部连通性
kubectl exec <pod-name> -- ping -c 3 8.8.8.8
kubectl exec <pod-name> -- curl -s https://www.google.com

# 2. 检查网关配置
kubectl -n kube-system exec -it deploy/ovn-nb-db -- ovn-nbctl lr-route-list <router-name>

# 3. 检查 SNAT 规则
kubectl -n kube-system exec -it deploy/ovn-nb-db -- ovn-nbctl lr-nat-list <router-name>

# 4. 在节点上检查 NAT
iptables -t nat -L -n -v | grep <pod-cidr>
```

## 性能问题排查

### 网络延迟高

```bash
# 1. 测量延迟
kubectl exec <pod-name> -- ping -c 100 <target-ip> | tail -1

# 2. 检查隧道 MTU
ip link show <tunnel-interface>

# 3. 检查 OVS 流表数量
ovs-ofctl dump-flows br-int | wc -l

# 4. 检查 CPU 使用
top -p $(pgrep ovs-vswitchd)
top -p $(pgrep ovn-controller)
```

### 吞吐量低

```bash
# 1. 使用 iperf 测试
kubectl exec <pod1> -- iperf3 -s &
kubectl exec <pod2> -- iperf3 -c <pod1-ip>

# 2. 检查网卡队列
ethtool -S <interface> | grep -i queue

# 3. 检查 OVS 端口统计
ovs-ofctl dump-ports br-int

# 4. 检查是否启用硬件卸载
ethtool -k <interface> | grep offload
```

### 资源使用高

```bash
# 1. 检查 OVN 组件资源使用
kubectl -n kube-system top pods -l app.kubernetes.io/name=zstack-ovn-kubernetes

# 2. 检查 OVS 内存使用
ovs-appctl memory/show

# 3. 检查流表数量
ovs-ofctl dump-flows br-int | wc -l

# 4. 检查连接跟踪表
ovs-appctl dpctl/dump-conntrack | wc -l
```

## 获取帮助

如果以上方法无法解决问题：

1. 收集诊断信息：
```bash
# 收集所有相关日志和配置
kubectl -n kube-system logs deploy/zstack-ovnkube-controller > controller.log
kubectl -n kube-system logs -l app=zstack-ovnkube-node > node.log
kubectl get pods -A -o wide > pods.txt
kubectl get svc -A > services.txt
kubectl get subnet -o yaml > subnets.yaml
ovs-vsctl show > ovs.txt
```

2. 提交 Issue 时请包含：
   - Kubernetes 版本
   - zstack-ovn-kubernetes 版本
   - 部署模式（standalone/external）
   - 问题描述和复现步骤
   - 相关日志和配置
