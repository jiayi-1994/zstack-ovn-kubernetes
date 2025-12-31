# External Mode Deployment Guide

This guide explains how to deploy zstack-ovn-kubernetes in external mode, connecting to ZStack-managed OVN databases.

## Overview

In external mode, zstack-ovn-kubernetes:
- Connects to ZStack's existing OVN NB/SB databases
- Does NOT start local OVN processes (NB DB, SB DB, northd)
- Can reference ZStack's existing Logical Switches
- Ensures no conflicts with ZStack OVN configuration

## Prerequisites

1. ZStack IaaS platform with OVN networking enabled
2. Network connectivity from Kubernetes nodes to ZStack OVN databases
3. OVN database ports accessible (default: 6641 for NB, 6642 for SB)

## Configuration

### Basic Configuration

```yaml
# values-external.yaml
ovn:
  mode: external
  nbdbAddress: "tcp:192.168.1.100:6641"
  sbdbAddress: "tcp:192.168.1.100:6642"
```

### SSL Configuration (Recommended for Production)

```yaml
ovn:
  mode: external
  nbdbAddress: "ssl:192.168.1.100:6641"
  sbdbAddress: "ssl:192.168.1.100:6642"
  ssl:
    enabled: true
    caCert: "/etc/ovn/ca.crt"
    clientCert: "/etc/ovn/client.crt"
    clientKey: "/etc/ovn/client.key"
```

### High Availability Configuration

For HA deployments with multiple OVN database servers:

```yaml
ovn:
  mode: external
  nbdbAddress: "tcp:192.168.1.100:6641,tcp:192.168.1.101:6641,tcp:192.168.1.102:6641"
  sbdbAddress: "tcp:192.168.1.100:6642,tcp:192.168.1.101:6642,tcp:192.168.1.102:6642"
```

## Referencing ZStack Logical Switches

In external mode, you can reference existing ZStack Logical Switches in your Subnet CRD:

```yaml
apiVersion: network.zstack.io/v1
kind: Subnet
metadata:
  name: zstack-vpc-subnet
spec:
  cidr: "10.244.0.0/24"
  gateway: "10.244.0.1"
  externalLogicalSwitch: "ls-zstack-vpc-subnet-uuid"  # ZStack LS name
  excludeIPs:
    - "10.244.0.1"
    - "10.244.0.2-10.244.0.10"
```

### Finding ZStack Logical Switch Names

You can find ZStack Logical Switch names using:

```bash
# On ZStack management node
ovn-nbctl ls-list
```

Or via ZStack API/UI in the VPC/Network configuration.

## ZStack Compatibility

### Ownership Tracking

zstack-ovn-kubernetes uses external_ids to track ownership:

- ZStack-managed objects: `zstack.io/managed-by=zstack`
- Our managed objects: `zstack.io/managed-by=zstack-ovn-kubernetes`

### Conflict Prevention

The controller automatically:
- Detects ZStack-managed OVN objects
- Prevents accidental modification of ZStack objects
- Uses safe creation/deletion with ownership checking

### Safe Operations

When creating Logical Switches in external mode:
1. Checks if switch already exists
2. If ZStack-managed, returns error (won't overwrite)
3. If our-managed, updates configuration
4. If unmanaged, returns error (won't take over)

## Deployment

### Using Helm

```bash
helm install zstack-ovn-kubernetes ./deploy/helm \
  -f ./deploy/helm/values-external.yaml \
  --namespace kube-system
```

### Environment Variables

You can also configure via environment variables:

```bash
export ZSTACK_OVN_MODE=external
export ZSTACK_OVN_NBDB_ADDRESS=tcp:192.168.1.100:6641
export ZSTACK_OVN_SBDB_ADDRESS=tcp:192.168.1.100:6642
```

## Troubleshooting

### Connection Issues

Check OVN database connectivity:

```bash
# Test NB DB connection
ovn-nbctl --db=tcp:192.168.1.100:6641 ls-list

# Test SB DB connection
ovn-sbctl --db=tcp:192.168.1.100:6642 chassis-list
```

### Viewing Logs

```bash
kubectl logs -n kube-system -l app=zstack-ovnkube-controller
```

### Checking External LS Validation

```bash
kubectl describe subnet <subnet-name>
```

Look for events like:
- `LogicalSwitchValidated`: External LS found and validated
- `LogicalSwitchValidationFailed`: External LS not found or validation failed

## Best Practices

1. **Use SSL in Production**: Always enable SSL for OVN database connections in production
2. **HA Configuration**: Use multiple OVN database addresses for high availability
3. **IP Range Planning**: Coordinate IP ranges with ZStack to avoid conflicts
4. **Monitoring**: Monitor OVN database connection health via metrics endpoint
