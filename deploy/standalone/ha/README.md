# OVN Standalone High Availability Deployment

This directory contains Kubernetes manifests for deploying OVN in standalone HA mode with Raft clustering.

## Overview

HA mode deploys OVN databases as 3-node Raft clusters, providing:
- **Fault tolerance**: Survives 1 node failure
- **Automatic failover**: Leader election is automatic
- **Data replication**: All data replicated across nodes
- **Production ready**: Suitable for production environments

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    OVN HA Architecture                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │              NB DB Raft Cluster (3 nodes)                 │   │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐                │   │
│  │  │ nb-db-0  │  │ nb-db-1  │  │ nb-db-2  │                │   │
│  │  │ (Leader) │  │(Follower)│  │(Follower)│                │   │
│  │  └────┬─────┘  └────┬─────┘  └────┬─────┘                │   │
│  │       └─────────────┼─────────────┘                       │   │
│  │                     │ Raft Replication                    │   │
│  └─────────────────────┼────────────────────────────────────┘   │
│                        │                                         │
│  ┌─────────────────────┼────────────────────────────────────┐   │
│  │              SB DB Raft Cluster (3 nodes)                 │   │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐                │   │
│  │  │ sb-db-0  │  │ sb-db-1  │  │ sb-db-2  │                │   │
│  │  │ (Leader) │  │(Follower)│  │(Follower)│                │   │
│  │  └────┬─────┘  └────┬─────┘  └────┬─────┘                │   │
│  │       └─────────────┼─────────────┘                       │   │
│  │                     │ Raft Replication                    │   │
│  └─────────────────────┼────────────────────────────────────┘   │
│                        │                                         │
│  ┌─────────────────────┼────────────────────────────────────┐   │
│  │           northd (2 replicas, active-standby)             │   │
│  │  ┌──────────┐  ┌──────────┐                               │   │
│  │  │ northd-0 │  │ northd-1 │                               │   │
│  │  │ (Active) │  │(Standby) │                               │   │
│  │  └──────────┘  └──────────┘                               │   │
│  └───────────────────────────────────────────────────────────┘   │
│                                                                  │
│  ┌───────────────────────────────────────────────────────────┐   │
│  │           ovn-controller (DaemonSet on all nodes)          │   │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐                 │   │
│  │  │  node-1  │  │  node-2  │  │  node-3  │  ...            │   │
│  │  └──────────┘  └──────────┘  └──────────┘                 │   │
│  └───────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
```

## Components

| Component | Type | Replicas | Description |
|-----------|------|----------|-------------|
| ovn-nb-db | StatefulSet | 3 | NB DB Raft cluster |
| ovn-sb-db | StatefulSet | 3 | SB DB Raft cluster |
| ovn-northd | Deployment | 2 | Active-standby northd |
| ovn-controller | DaemonSet | N | One per node |

## Prerequisites

1. **Kubernetes Cluster**: At least 3 nodes for proper Raft distribution

2. **Storage**: StorageClass available for PersistentVolumeClaims
   ```bash
   kubectl get storageclass
   ```

3. **Open vSwitch**: OVS must be installed on all nodes:
   ```bash
   # Ubuntu/Debian
   apt-get install -y openvswitch-switch
   systemctl enable --now openvswitch-switch
   ```

4. **Kernel Modules**:
   ```bash
   modprobe openvswitch
   modprobe vxlan
   ```

## Deployment

### Using Kustomize

```bash
# Deploy all components
kubectl apply -k deploy/standalone/ha/

# Watch deployment progress
kubectl -n ovn-kubernetes get pods -w

# Verify all pods are running
kubectl -n ovn-kubernetes get pods
```

### Manual Deployment

```bash
# Create namespace
kubectl apply -f namespace.yaml

# Deploy NB DB cluster
kubectl apply -f ovn-nb-db.yaml

# Wait for NB DB cluster to form
kubectl -n ovn-kubernetes rollout status statefulset/ovn-nb-db

# Deploy SB DB cluster
kubectl apply -f ovn-sb-db.yaml

# Wait for SB DB cluster to form
kubectl -n ovn-kubernetes rollout status statefulset/ovn-sb-db

# Deploy northd
kubectl apply -f ovn-northd.yaml

# Deploy ovn-controller
kubectl apply -f ovn-controller.yaml
```

## Verification

### Check Cluster Status

```bash
# Check all pods
kubectl -n ovn-kubernetes get pods -o wide

# Expected output (example):
# NAME                          READY   STATUS    RESTARTS   AGE
# ovn-nb-db-0                   1/1     Running   0          10m
# ovn-nb-db-1                   1/1     Running   0          10m
# ovn-nb-db-2                   1/1     Running   0          10m
# ovn-sb-db-0                   1/1     Running   0          10m
# ovn-sb-db-1                   1/1     Running   0          10m
# ovn-sb-db-2                   1/1     Running   0          10m
# ovn-northd-xxx                1/1     Running   0          9m
# ovn-northd-yyy                1/1     Running   0          9m
# ovn-controller-xxx            1/1     Running   0          8m
# ovn-controller-yyy            1/1     Running   0          8m
```

### Check Raft Cluster Status

```bash
# Check NB DB cluster status
kubectl -n ovn-kubernetes exec -it ovn-nb-db-0 -- ovs-appctl -t /var/run/ovn/ovnnb_db.ctl cluster/status OVN_Northbound

# Check SB DB cluster status
kubectl -n ovn-kubernetes exec -it ovn-sb-db-0 -- ovs-appctl -t /var/run/ovn/ovnsb_db.ctl cluster/status OVN_Southbound
```

### Check OVN Status

```bash
# List logical switches
kubectl -n ovn-kubernetes exec -it ovn-nb-db-0 -- ovn-nbctl show

# List chassis (registered nodes)
kubectl -n ovn-kubernetes exec -it ovn-sb-db-0 -- ovn-sbctl show
```

## Service Endpoints

| Service | Port | Description |
|---------|------|-------------|
| ovn-nb-db | 6641 | NB DB client connections |
| ovn-nb-db-headless | 6641, 6643 | NB DB Raft cluster discovery |
| ovn-sb-db | 6642 | SB DB client connections |
| ovn-sb-db-headless | 6642, 6644 | SB DB Raft cluster discovery |

## Failure Scenarios

### Single Node Failure

The Raft cluster can tolerate 1 node failure:
- Remaining 2 nodes maintain quorum
- Leader election occurs if leader fails
- No data loss

### Multiple Node Failure

If 2+ nodes fail:
- Cluster loses quorum
- Database becomes read-only
- Requires manual recovery

### Recovery Procedure

1. **Identify failed pods**:
   ```bash
   kubectl -n ovn-kubernetes get pods
   ```

2. **Check PVC status**:
   ```bash
   kubectl -n ovn-kubernetes get pvc
   ```

3. **Delete failed pod** (StatefulSet will recreate):
   ```bash
   kubectl -n ovn-kubernetes delete pod ovn-nb-db-X
   ```

4. **Verify cluster recovery**:
   ```bash
   kubectl -n ovn-kubernetes exec -it ovn-nb-db-0 -- ovs-appctl -t /var/run/ovn/ovnnb_db.ctl cluster/status OVN_Northbound
   ```

## Scaling

### Scaling Database Clusters

**Warning**: Raft clusters should have odd number of nodes (3, 5, 7).

To scale to 5 nodes:
```bash
kubectl -n ovn-kubernetes scale statefulset ovn-nb-db --replicas=5
kubectl -n ovn-kubernetes scale statefulset ovn-sb-db --replicas=5
```

### Scaling northd

northd can be scaled for redundancy:
```bash
kubectl -n ovn-kubernetes scale deployment ovn-northd --replicas=3
```

## Troubleshooting

### Database Not Forming Cluster

1. Check pod logs:
   ```bash
   kubectl -n ovn-kubernetes logs ovn-nb-db-0
   ```

2. Verify DNS resolution:
   ```bash
   kubectl -n ovn-kubernetes exec -it ovn-nb-db-0 -- nslookup ovn-nb-db-headless
   ```

3. Check network connectivity between pods

### Split Brain

If cluster experiences split brain:
1. Scale down to 0 replicas
2. Delete all PVCs
3. Redeploy from scratch

### Performance Issues

1. Check resource usage:
   ```bash
   kubectl -n ovn-kubernetes top pods
   ```

2. Increase resource limits if needed

## Cleanup

```bash
# Delete all resources
kubectl delete -k deploy/standalone/ha/

# Delete PVCs (data will be lost!)
kubectl -n ovn-kubernetes delete pvc --all
```

## Comparison with Single-Node Mode

| Feature | Single-Node | HA Mode |
|---------|-------------|---------|
| Fault Tolerance | None | 1 node failure |
| Data Replication | None | 3-way |
| Resource Usage | Low | Higher |
| Complexity | Simple | More complex |
| Use Case | Dev/Test | Production |
