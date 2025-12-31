# Overlay 网络示例

本目录包含 Overlay 网络配置示例。

## 概述

Overlay 网络使用 VXLAN 隧道在节点间传输 Pod 流量，适用于：
- 标准 Kubernetes 集群
- 跨节点 Pod 通信
- 不需要直接访问物理网络的场景

## 文件说明

- `subnet.yaml` - 子网配置示例

## 使用方法

### 1. 创建子网

```bash
kubectl apply -f subnet.yaml
```

### 2. 验证子网状态

```bash
kubectl get subnet
kubectl describe subnet pod-subnet
```

### 3. 创建测试 Pod

```bash
kubectl run test-pod --image=busybox --command -- sleep 3600
kubectl get pod test-pod -o wide
```

### 4. 验证网络连通性

```bash
# 创建另一个 Pod
kubectl run test-pod-2 --image=busybox --command -- sleep 3600

# 获取 Pod IP
POD2_IP=$(kubectl get pod test-pod-2 -o jsonpath='{.status.podIP}')

# 测试连通性
kubectl exec test-pod -- ping -c 3 $POD2_IP
```

## 配置说明

### CIDR 规划

| 参数 | 说明 | 示例 |
|------|------|------|
| cidr | Pod IP 范围 | 10.244.0.0/16 |
| gateway | 网关 IP | 10.244.0.1 |
| excludeIPs | 排除的 IP | 10.244.0.1 |

### 多子网场景

可以创建多个子网用于不同用途：
- `app-subnet`: 应用 Pod
- `db-subnet`: 数据库 Pod

通过 Pod annotation 指定使用的子网：

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-app
  annotations:
    network.zstack.io/subnet: "app-subnet"
spec:
  containers:
  - name: app
    image: nginx
```
