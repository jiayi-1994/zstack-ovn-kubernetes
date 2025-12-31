# zstack-ovn-kubernetes Kubernetes Manifests

This directory contains raw Kubernetes YAML manifests for deploying zstack-ovn-kubernetes.

## Directory Structure

```
yaml/
├── namespace.yaml           # Namespace definition
├── subnet-crd.yaml          # Subnet Custom Resource Definition
├── configmap.yaml           # Configuration (CNI config, controller settings)
├── rbac.yaml                # ServiceAccounts, ClusterRoles, ClusterRoleBindings
├── ovn-databases.yaml       # OVN NB/SB DB and northd (standalone mode only)
├── controller-deployment.yaml  # Controller Deployment
├── node-daemonset.yaml      # Node Agent DaemonSet
├── kustomization.yaml       # Kustomize configuration
└── README.md                # This file
```

## Deployment Modes

### Standalone Mode (Default)

Self-managed OVN databases for independent Kubernetes clusters.

```bash
# Deploy all components
kubectl apply -k deploy/yaml/

# Or apply individually
kubectl apply -f deploy/yaml/namespace.yaml
kubectl apply -f deploy/yaml/subnet-crd.yaml
kubectl apply -f deploy/yaml/configmap.yaml
kubectl apply -f deploy/yaml/rbac.yaml
kubectl apply -f deploy/yaml/ovn-databases.yaml
kubectl apply -f deploy/yaml/controller-deployment.yaml
kubectl apply -f deploy/yaml/node-daemonset.yaml
```

### External Mode (ZStack Integration)

Connect to ZStack-managed OVN databases.

1. Edit `configmap.yaml`:
   ```yaml
   ovn:
     mode: external
     nbdbAddress: "tcp:192.168.1.100:6641"  # Your ZStack OVN NB DB
     sbdbAddress: "tcp:192.168.1.100:6642"  # Your ZStack OVN SB DB
   ```

2. Edit `kustomization.yaml` to remove `ovn-databases.yaml`:
   ```yaml
   resources:
     - namespace.yaml
     - subnet-crd.yaml
     - configmap.yaml
     - rbac.yaml
     # - ovn-databases.yaml  # Comment out or remove this line
     - controller-deployment.yaml
     - node-daemonset.yaml
   ```

3. Deploy:
   ```bash
   kubectl apply -k deploy/yaml/
   ```

## Prerequisites

1. **Kubernetes cluster** (v1.21+)
2. **Open vSwitch** installed on all nodes
3. **CNI plugins** directory exists: `/opt/cni/bin/`

### Install OVS on nodes

```bash
# Ubuntu/Debian
apt-get install -y openvswitch-switch

# CentOS/RHEL
yum install -y openvswitch
systemctl enable --now openvswitch
```

## Verification

```bash
# Check namespace
kubectl get ns zstack-ovn-kubernetes

# Check all pods
kubectl get pods -n zstack-ovn-kubernetes

# Check controller logs
kubectl logs -n zstack-ovn-kubernetes -l app.kubernetes.io/component=controller

# Check node agent logs
kubectl logs -n zstack-ovn-kubernetes -l app.kubernetes.io/component=node

# Check OVN database connectivity
kubectl exec -n zstack-ovn-kubernetes deploy/ovn-nb-db -- \
  ovsdb-client list-dbs tcp:127.0.0.1:6641
```

## Create a Subnet

```yaml
apiVersion: network.zstack.io/v1
kind: Subnet
metadata:
  name: default-subnet
spec:
  cidr: "10.244.0.0/24"
  gateway: "10.244.0.1"
  excludeIPs:
    - "10.244.0.1"
```

```bash
kubectl apply -f subnet.yaml
kubectl get subnets
```

## Cleanup

```bash
kubectl delete -k deploy/yaml/
```

## Customization

### Change Network CIDR

Edit `configmap.yaml`:
```yaml
network:
  clusterCIDR: "172.16.0.0/16"  # Your Pod CIDR
  serviceCIDR: "10.96.0.0/16"   # Your Service CIDR
```

### Change Tunnel Type

Edit `configmap.yaml`:
```yaml
tunnel:
  type: "geneve"  # or "vxlan"
  port: 6081      # Geneve default port
```

### Enable SSL

Edit `configmap.yaml`:
```yaml
ovn:
  ssl:
    enabled: true
    caCert: "/etc/ovn/ssl/ca.crt"
    clientCert: "/etc/ovn/ssl/client.crt"
    clientKey: "/etc/ovn/ssl/client.key"
```

Create SSL secret:
```bash
kubectl create secret generic zstack-ovn-kubernetes-ovn-ssl \
  -n zstack-ovn-kubernetes \
  --from-file=ca.crt=/path/to/ca.crt \
  --from-file=client.crt=/path/to/client.crt \
  --from-file=client.key=/path/to/client.key
```
