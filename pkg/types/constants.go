// Package types provides type definitions and constants.
//
// This package contains:
// - Constant definitions
// - Common type definitions
// - Annotation keys
package types

const (
	// OVN Database Ports
	DefaultNBDBPort = 6641
	DefaultSBDBPort = 6642

	// OVS Bridge Names
	BrInt = "br-int" // Integration bridge for Pod traffic
	BrEx  = "br-ex"  // External bridge for gateway traffic

	// Tunnel Types
	TunnelTypeVXLAN  = "vxlan"
	TunnelTypeGeneve = "geneve"

	// Default Tunnel Ports
	DefaultVXLANPort  = 4789
	DefaultGenevePort = 6081

	// Deployment Modes
	ModeStandalone = "standalone" // Self-managed OVN databases
	ModeExternal   = "external"   // Connect to external OVN (e.g., ZStack)

	// Gateway Modes
	GatewayModeShared = "shared" // Centralized gateway
	GatewayModeLocal  = "local"  // Distributed gateway per node

	// Annotation Keys
	// Pod network configuration annotation
	PodNetworkAnnotation = "k8s.ovn.org/pod-networks"

	// Subnet annotation for external logical switch reference
	SubnetExternalLSAnnotation = "zstack.io/external-logical-switch"

	// CNI Configuration
	CNIConfDir    = "/etc/cni/net.d"
	CNIBinDir     = "/opt/cni/bin"
	CNIConfFile   = "10-zstack-ovn.conflist"
	CNISocketPath = "/var/run/zstack-ovn/cni-server.sock"

	// Default Network Configuration
	DefaultClusterCIDR    = "10.244.0.0/16"
	DefaultServiceCIDR    = "10.96.0.0/16"
	DefaultNodeSubnetSize = 24
)
