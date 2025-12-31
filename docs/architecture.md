# Architecture

## Overview

zstack-ovn-kubernetes is a Kubernetes CNI plugin based on OVN (Open Virtual Networking).
It provides network connectivity for Pods and integrates with ZStack IaaS platform.

## Components

### Control Plane

- **zstack-ovnkube-controller**: Deployment running on master nodes
  - Watches Kubernetes resources (Pods, Services, NetworkPolicies)
  - Manages OVN logical objects (Logical Switches, Routers, Load Balancers)
  - Handles IP allocation

### Data Plane

- **zstack-ovnkube-node**: DaemonSet running on all nodes
  - Configures local OVS bridges
  - Manages tunnels (VXLAN/Geneve)
  - Runs CNI Server

- **zstack-ovn-cni**: CNI binary
  - Handles Pod network setup/teardown
  - Communicates with CNI Server via Unix Socket

### OVN Components

- **OVN NB DB**: Northbound Database (high-level config)
- **OVN SB DB**: Southbound Database (flow tables)
- **ovn-northd**: Translates NB to SB
- **ovn-controller**: Programs OVS flows

## Deployment Modes

### Standalone Mode

Self-managed OVN databases for independent Kubernetes clusters.

### External Mode

Connects to ZStack-managed OVN databases for VM-Pod integration.
