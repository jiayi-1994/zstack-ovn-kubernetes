# NetworkPolicy 示例

本目录包含 Kubernetes NetworkPolicy 配置示例，展示如何使用 zstack-ovn-kubernetes 实现网络隔离和访问控制。

## 概述

NetworkPolicy 是 Kubernetes 原生的网络安全机制，zstack-ovn-kubernetes 通过 OVN ACL 实现 NetworkPolicy 功能。

## 文件说明

- `default-deny.yaml` - 默认拒绝所有流量
- `allow-same-namespace.yaml` - 允许同命名空间内通信
- `allow-specific-pods.yaml` - 允许特定 Pod 访问
- `allow-external-egress.yaml` - 允许出站到外部网络
- `database-policy.yaml` - 数据库访问控制示例
- `multi-tier-app.yaml` - 多层应用网络策略

## 基础概念

### NetworkPolicy 工作原理

1. **默认行为**: 没有 NetworkPolicy 时，所有 Pod 可以互相通信
2. **选择性隔离**: NetworkPolicy 只影响被选中的 Pod
3. **白名单模式**: 一旦 Pod 被 NetworkPolicy 选中，只有明确允许的流量才能通过

### 策略类型

| 类型 | 说明 |
|------|------|
| Ingress | 控制入站流量（其他 Pod 访问本 Pod） |
| Egress | 控制出站流量（本 Pod 访问其他资源） |

## 使用方法

### 1. 创建测试环境

```bash
# 创建测试命名空间
kubectl create namespace policy-test

# 创建测试 Pod
kubectl -n policy-test run web --image=nginx --labels="app=web"
kubectl -n policy-test run api --image=nginx --labels="app=api"
kubectl -n policy-test run db --image=nginx --labels="app=db"

# 等待 Pod 就绪
kubectl -n policy-test wait --for=condition=Ready pod --all
```

### 2. 测试默认连通性

```bash
# 获取 Pod IP
WEB_IP=$(kubectl -n policy-test get pod web -o jsonpath='{.status.podIP}')
API_IP=$(kubectl -n policy-test get pod api -o jsonpath='{.status.podIP}')
DB_IP=$(kubectl -n policy-test get pod db -o jsonpath='{.status.podIP}')

# 测试连通性（应该都能通）
kubectl -n policy-test exec web -- curl -s --connect-timeout 3 http://$API_IP
kubectl -n policy-test exec api -- curl -s --connect-timeout 3 http://$DB_IP
```

### 3. 应用 NetworkPolicy

```bash
# 应用默认拒绝策略
kubectl apply -f default-deny.yaml

# 测试连通性（应该都不通）
kubectl -n policy-test exec web -- curl -s --connect-timeout 3 http://$API_IP
# 预期: 超时

# 应用允许策略
kubectl apply -f allow-specific-pods.yaml

# 再次测试（应该能通）
kubectl -n policy-test exec web -- curl -s --connect-timeout 3 http://$API_IP
```

### 4. 验证 OVN ACL

```bash
# 查看生成的 ACL
kubectl -n kube-system exec -it deploy/ovn-nb-db -- \
  ovn-nbctl acl-list <logical-switch-name>
```

## 示例详解

### 默认拒绝策略

```yaml
# default-deny.yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-all
  namespace: policy-test
spec:
  podSelector: {}  # 选择所有 Pod
  policyTypes:
  - Ingress
  - Egress
```

### 允许同命名空间通信

```yaml
# allow-same-namespace.yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-same-namespace
  namespace: policy-test
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  ingress:
  - from:
    - podSelector: {}  # 同命名空间的所有 Pod
```

### 允许特定 Pod 访问

```yaml
# allow-specific-pods.yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-web-to-api
  namespace: policy-test
spec:
  podSelector:
    matchLabels:
      app: api
  policyTypes:
  - Ingress
  ingress:
  - from:
    - podSelector:
        matchLabels:
          app: web
    ports:
    - protocol: TCP
      port: 80
```

## 常见场景

### 场景 1: 数据库隔离

只允许 API 服务访问数据库：

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: db-policy
spec:
  podSelector:
    matchLabels:
      app: database
  policyTypes:
  - Ingress
  ingress:
  - from:
    - podSelector:
        matchLabels:
          app: api
    ports:
    - protocol: TCP
      port: 3306
```

### 场景 2: 多层应用

前端 → API → 数据库的访问控制：

```yaml
# 见 multi-tier-app.yaml
```

### 场景 3: 允许外部访问

允许 Pod 访问外部 DNS 和 HTTPS：

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-external
spec:
  podSelector:
    matchLabels:
      app: web
  policyTypes:
  - Egress
  egress:
  - to:
    - ipBlock:
        cidr: 0.0.0.0/0
        except:
        - 10.0.0.0/8
        - 172.16.0.0/12
        - 192.168.0.0/16
    ports:
    - protocol: TCP
      port: 443
    - protocol: UDP
      port: 53
```

## 故障排查

### 策略不生效

1. 检查 Pod 标签：
```bash
kubectl get pods --show-labels
```

2. 检查 NetworkPolicy：
```bash
kubectl describe networkpolicy <policy-name>
```

3. 检查 OVN ACL：
```bash
kubectl -n kube-system exec -it deploy/ovn-nb-db -- ovn-nbctl acl-list <switch>
```

### 意外阻止流量

1. 检查是否有默认拒绝策略
2. 检查 Ingress 和 Egress 是否都配置了
3. 使用 `kubectl describe` 查看策略详情

## 清理

```bash
# 删除所有 NetworkPolicy
kubectl -n policy-test delete networkpolicy --all

# 删除测试命名空间
kubectl delete namespace policy-test
```
