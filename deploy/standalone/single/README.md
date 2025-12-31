# OVN Standalone Single-Node Deployment

This directory contains Kubernetes manifests for deploying OVN in standalone single-node mode.

## Overview

Single-node mode deploys OVN components as single instances, suitable for:
- Development and testing environments
- Small-scale deployments
- Learning and experimentation

**Note:** This mode does NOT provide high availability. For production deployments, use the HA mode in `../ha/`.

## Components

| Component | Type | Description |
|-----------|------|-------------|
| ovn-nb-db | Deployment (1 replica) | OVN Northbound Database - stores logical network config |
| ovn-sb-db | Deployment (1 replica) | OVN Southbound Database - stores logical flows |
| ovn-northd | Deployment (1 replica) | Translates NB config to SB flows |
| ovn-controller | DaemonSet | Programs OVS on each node |

## Prerequisites

1. **Kubernetes Cluster**: A running Kubernetes cluster (v1.20+)

2. **Open vSwitch**: OVS must be installed and running on all nodes:
   ```bash
   # Ubuntu/Debian
   apt-get install -y openvswitch-switch
   systemctl enable --now openvswitch-switch
   
   # CentOS/RHEL
   yum install -y openvswitch
   systemctl enable --now openvswitch
   ```

3. **Kernel Modules**: Ensure required kernel modules are loaded:
   ```bash
   modprobe openvswitch
   modprobe vxlan
   ```

## Deployment

### Using Kustomize

```bash
# Deploy all components
kubectl apply -k deploy/standalone/single/

# Verify deployment
kubectl -n ovn-kubernetes get pods

# Check services
kubectl -n ovn-kubernetes get svc
```

### Manual Deployment

```bash
# Create namespace
kubectl apply -f namespace.yaml

# Deploy databases
kubectl apply -f ovn-nb-db.yaml
kubectl apply -f ovn-sb-db.yaml

# Wait for databases to be ready
kubectl -n ovn-kubernetes wait --for=condition=ready pod -l app.kubernetes.io/name=ovn-nb-db --timeout=120s
kubectl -n ovn-kubernetes wait --for=condition=ready pod -l app.kubernetes.io/name=ovn-sb-db --timeout=120s

# Deploy northd
kubectl apply -f ovn-northd.yaml

# Deploy ovn-controller
kubectl apply -f ovn-controller.yaml
```

## Verification

### Check Pod Status

```bash
kubectl -n ovn-kubernetes get pods -o wide
```

Expected output:
```
NAME                          READY   STATUS    RESTARTS   AGE
ovn-nb-db-xxx                 1/1     Running   0          5m
ovn-sb-db-xxx                 1/1     Running   0          5m
ovn-northd-xxx                1/1     Running   0          4m
ovn-controller-xxx            1/1     Running   0          3m
ovn-controller-yyy            1/1     Running   0          3m
```

### Check Database Connectivity

```bash
# Check NB DB
kubectl -n ovn-kubernetes exec -it deploy/ovn-nb-db -- ovsdb-client list-dbs tcp:127.0.0.1:6641

# Check SB DB
kubectl -n ovn-kubernetes exec -it deploy/ovn-sb-db -- ovsdb-client list-dbs tcp:127.0.0.1:6642
```

### Check OVN Status

```bash
# Check northd status
kubectl -n ovn-kubernetes logs deploy/ovn-northd

# Check chassis registration
kubectl -n ovn-kubernetes exec -it deploy/ovn-sb-db -- ovn-sbctl show
```

## Service Endpoints

| Service | Port | Description |
|---------|------|-------------|
| ovn-nb-db | 6641 | Northbound DB client connections |
| ovn-nb-db | 6643 | Northbound DB Raft (unused in single mode) |
| ovn-sb-db | 6642 | Southbound DB client connections |
| ovn-sb-db | 6644 | Southbound DB Raft (unused in single mode) |

## Troubleshooting

### Database Not Starting

Check logs:
```bash
kubectl -n ovn-kubernetes logs deploy/ovn-nb-db
kubectl -n ovn-kubernetes logs deploy/ovn-sb-db
```

### ovn-controller Not Connecting

1. Verify OVS is running on the node:
   ```bash
   systemctl status openvswitch-switch
   ovs-vsctl show
   ```

2. Check OVS external-ids:
   ```bash
   ovs-vsctl get open . external-ids
   ```

3. Check ovn-controller logs:
   ```bash
   kubectl -n ovn-kubernetes logs ds/ovn-controller
   ```

### northd Not Processing

Check connectivity to both databases:
```bash
kubectl -n ovn-kubernetes exec -it deploy/ovn-northd -- ovn-nbctl show
kubectl -n ovn-kubernetes exec -it deploy/ovn-northd -- ovn-sbctl show
```

## Cleanup

```bash
kubectl delete -k deploy/standalone/single/
```

## Next Steps

After deploying OVN, you can:
1. Deploy the zstack-ovnkube-controller
2. Deploy the zstack-ovnkube-node DaemonSet
3. Install the CNI plugin on each node
4. Create Subnet CRDs to define networks
