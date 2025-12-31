// Package ovndb provides OVN database models and operations.
//
// This file defines the OVN database models for Northbound and Southbound databases.
// These models are used by libovsdb to interact with OVN databases.
//
// OVN Northbound Database Tables:
// - Logical_Switch: Virtual L2 network segment
// - Logical_Switch_Port: Port on a logical switch
// - Logical_Router: Virtual L3 router
// - Logical_Router_Port: Port on a logical router
// - Load_Balancer: L4 load balancer for services
// - ACL: Access control list for network policies
// - Address_Set: Set of IP addresses for ACL matching
// - Port_Group: Group of ports for ACL matching
//
// OVN Southbound Database Tables:
// - Chassis: Physical node information
// - Encap: Tunnel encapsulation configuration
// - Port_Binding: Logical port to chassis binding
// - Datapath_Binding: Datapath to logical switch/router binding
//
// Reference: OVN-Kubernetes pkg/nbdb/ and pkg/sbdb/
package ovndb

import (
	"github.com/ovn-org/libovsdb/model"
)

// ============================================================================
// OVN Northbound Database Models
// ============================================================================

// LogicalSwitch represents an OVN Logical Switch
// A logical switch is a virtual L2 network that connects multiple logical switch ports.
// In Kubernetes context, a logical switch typically represents a subnet.
//
// Key fields:
// - Name: Unique identifier for the switch
// - Ports: List of logical switch port UUIDs
// - ACLs: List of ACL UUIDs applied to this switch
// - LoadBalancer: List of load balancer UUIDs
// - OtherConfig: Additional configuration (e.g., subnet CIDR, exclude_ips)
// - ExternalIDs: External identifiers for integration (e.g., k8s namespace)
type LogicalSwitch struct {
	UUID              string            `ovsdb:"_uuid"`
	Name              string            `ovsdb:"name"`
	Ports             []string          `ovsdb:"ports"`
	ACLs              []string          `ovsdb:"acls"`
	LoadBalancer      []string          `ovsdb:"load_balancer"`
	LoadBalancerGroup []string          `ovsdb:"load_balancer_group"`
	QOSRules          []string          `ovsdb:"qos_rules"`
	DNSRecords        []string          `ovsdb:"dns_records"`
	OtherConfig       map[string]string `ovsdb:"other_config"`
	ExternalIDs       map[string]string `ovsdb:"external_ids"`
	ForwardingGroups  []string          `ovsdb:"forwarding_groups"`
	Copp              *string           `ovsdb:"copp"`
}

// LogicalSwitchPort represents an OVN Logical Switch Port
// A logical switch port is a virtual network interface attached to a logical switch.
// In Kubernetes context, each Pod has a logical switch port.
//
// Key fields:
// - Name: Unique identifier (format: namespace_podName)
// - Addresses: MAC and IP addresses (format: "MAC IP" or "dynamic")
// - Type: Port type ("" for normal, "router" for router port, "localnet" for physical network)
// - Options: Port-specific options (e.g., requested-chassis for scheduling)
// - PortSecurity: Security rules to prevent IP/MAC spoofing
// - ExternalIDs: External identifiers (e.g., pod namespace, pod name)
type LogicalSwitchPort struct {
	UUID             string            `ovsdb:"_uuid"`
	Name             string            `ovsdb:"name"`
	Addresses        []string          `ovsdb:"addresses"`
	Type             string            `ovsdb:"type"`
	Options          map[string]string `ovsdb:"options"`
	PortSecurity     []string          `ovsdb:"port_security"`
	ExternalIDs      map[string]string `ovsdb:"external_ids"`
	Enabled          *bool             `ovsdb:"enabled"`
	Up               *bool             `ovsdb:"up"`
	Dhcpv4Options    *string           `ovsdb:"dhcpv4_options"`
	Dhcpv6Options    *string           `ovsdb:"dhcpv6_options"`
	DynamicAddresses *string           `ovsdb:"dynamic_addresses"`
	HaChassisGroup   *string           `ovsdb:"ha_chassis_group"`
	MirrorRules      []string          `ovsdb:"mirror_rules"`
	ParentName       *string           `ovsdb:"parent_name"`
	Tag              *int              `ovsdb:"tag"`
	TagRequest       *int              `ovsdb:"tag_request"`
}

// LogicalRouter represents an OVN Logical Router
// A logical router provides L3 routing between logical switches.
// In Kubernetes context, it routes traffic between different subnets and to external networks.
type LogicalRouter struct {
	UUID         string            `ovsdb:"_uuid"`
	Name         string            `ovsdb:"name"`
	Ports        []string          `ovsdb:"ports"`
	StaticRoutes []string          `ovsdb:"static_routes"`
	Policies     []string          `ovsdb:"policies"`
	Nat          []string          `ovsdb:"nat"`
	LoadBalancer []string          `ovsdb:"load_balancer"`
	Options      map[string]string `ovsdb:"options"`
	ExternalIDs  map[string]string `ovsdb:"external_ids"`
	Enabled      *bool             `ovsdb:"enabled"`
	Copp         *string           `ovsdb:"copp"`
}

// LogicalRouterPort represents an OVN Logical Router Port
// A logical router port connects a logical router to a logical switch or another router.
type LogicalRouterPort struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	Networks    []string          `ovsdb:"networks"`
	MAC         string            `ovsdb:"mac"`
	Peer        *string           `ovsdb:"peer"`
	Options     map[string]string `ovsdb:"options"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
	Enabled     *bool             `ovsdb:"enabled"`
	GatewayChassis []string       `ovsdb:"gateway_chassis"`
	HaChassisGroup *string        `ovsdb:"ha_chassis_group"`
}

// LoadBalancer represents an OVN Load Balancer
// A load balancer implements L4 load balancing for Kubernetes Services.
//
// Key fields:
// - Name: Unique identifier (typically Service namespace/name)
// - Vips: VIP to backend mapping (format: "VIP:PORT" -> "BACKEND1:PORT,BACKEND2:PORT")
// - Protocol: TCP, UDP, or SCTP
// - Options: Load balancer options (e.g., hairpin_snat_ip)
// - ExternalIDs: External identifiers (e.g., k8s service info)
type LoadBalancer struct {
	UUID            string            `ovsdb:"_uuid"`
	Name            string            `ovsdb:"name"`
	Vips            map[string]string `ovsdb:"vips"`
	Protocol        *string           `ovsdb:"protocol"`
	Options         map[string]string `ovsdb:"options"`
	ExternalIDs     map[string]string `ovsdb:"external_ids"`
	HealthCheck     []string          `ovsdb:"health_check"`
	IPPortMappings  map[string]string `ovsdb:"ip_port_mappings"`
	SelectionFields []string          `ovsdb:"selection_fields"`
}

// LoadBalancer protocol constants
const (
	LoadBalancerProtocolTCP  = "tcp"
	LoadBalancerProtocolUDP  = "udp"
	LoadBalancerProtocolSCTP = "sctp"
)

// ACL represents an OVN Access Control List
// An ACL implements network policies by filtering traffic based on match conditions.
//
// Key fields:
// - Direction: "from-lport" (egress) or "to-lport" (ingress)
// - Priority: Higher priority rules are evaluated first (0-32767)
// - Match: OVN match expression (e.g., "ip4.src == 10.0.0.0/8")
// - Action: "allow", "allow-related", "drop", or "reject"
// - ExternalIDs: External identifiers (e.g., NetworkPolicy reference)
type ACL struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        *string           `ovsdb:"name"`
	Direction   string            `ovsdb:"direction"`
	Priority    int               `ovsdb:"priority"`
	Match       string            `ovsdb:"match"`
	Action      string            `ovsdb:"action"`
	Log         bool              `ovsdb:"log"`
	Severity    *string           `ovsdb:"severity"`
	Meter       *string           `ovsdb:"meter"`
	Options     map[string]string `ovsdb:"options"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
	Label       int               `ovsdb:"label"`
	Tier        int               `ovsdb:"tier"`
}

// ACL direction constants
const (
	ACLDirectionFromLport = "from-lport" // Egress traffic (from port)
	ACLDirectionToLport   = "to-lport"   // Ingress traffic (to port)
)

// ACL action constants
const (
	ACLActionAllow        = "allow"         // Allow the packet
	ACLActionAllowRelated = "allow-related" // Allow and track connection
	ACLActionDrop         = "drop"          // Silently drop the packet
	ACLActionReject       = "reject"        // Drop and send ICMP unreachable
	ACLActionPass         = "pass"          // Skip to next tier
)

// AddressSet represents an OVN Address Set
// An address set is a named group of IP addresses used in ACL match expressions.
type AddressSet struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	Addresses   []string          `ovsdb:"addresses"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
}

// PortGroup represents an OVN Port Group
// A port group is a named group of logical switch ports used in ACL match expressions.
type PortGroup struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	Ports       []string          `ovsdb:"ports"`
	ACLs        []string          `ovsdb:"acls"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
}

// NBGlobal represents the NB_Global table
// Contains global configuration for the OVN Northbound database.
type NBGlobal struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	NbCfg       int               `ovsdb:"nb_cfg"`
	Options     map[string]string `ovsdb:"options"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
	Connections []string          `ovsdb:"connections"`
	SSL         *string           `ovsdb:"ssl"`
	IPSec       bool              `ovsdb:"ipsec"`
}

// ============================================================================
// OVN Southbound Database Models
// ============================================================================

// Chassis represents an OVN Chassis (physical node)
// Each Kubernetes node running ovn-controller registers as a chassis.
type Chassis struct {
	UUID            string            `ovsdb:"_uuid"`
	Name            string            `ovsdb:"name"`
	Hostname        string            `ovsdb:"hostname"`
	Encaps          []string          `ovsdb:"encaps"`
	VtepLogicalSwitches []string      `ovsdb:"vtep_logical_switches"`
	ExternalIDs     map[string]string `ovsdb:"external_ids"`
	NbCfg           int               `ovsdb:"nb_cfg"`
	TransportZones  []string          `ovsdb:"transport_zones"`
	OtherConfig     map[string]string `ovsdb:"other_config"`
}

// Encap represents tunnel encapsulation configuration
type Encap struct {
	UUID        string            `ovsdb:"_uuid"`
	Type        string            `ovsdb:"type"`
	IP          string            `ovsdb:"ip"`
	Options     map[string]string `ovsdb:"options"`
	ChassisName string            `ovsdb:"chassis_name"`
}

// Encap type constants
const (
	EncapTypeGeneve = "geneve"
	EncapTypeVXLAN  = "vxlan"
	EncapTypeSTT    = "stt"
)

// PortBinding represents logical port to chassis binding
type PortBinding struct {
	UUID           string            `ovsdb:"_uuid"`
	LogicalPort    string            `ovsdb:"logical_port"`
	Chassis        *string           `ovsdb:"chassis"`
	Encap          *string           `ovsdb:"encap"`
	Type           string            `ovsdb:"type"`
	Options        map[string]string `ovsdb:"options"`
	MAC            []string          `ovsdb:"mac"`
	NatAddresses   []string          `ovsdb:"nat_addresses"`
	ExternalIDs    map[string]string `ovsdb:"external_ids"`
	Datapath       string            `ovsdb:"datapath"`
	TunnelKey      int               `ovsdb:"tunnel_key"`
	ParentPort     *string           `ovsdb:"parent_port"`
	Tag            *int              `ovsdb:"tag"`
	Up             *bool             `ovsdb:"up"`
	GatewayChassis []string          `ovsdb:"gateway_chassis"`
	HaChassisGroup *string           `ovsdb:"ha_chassis_group"`
	VirtualParent  *string           `ovsdb:"virtual_parent"`
	RequestedChassis *string         `ovsdb:"requested_chassis"`
}

// SBGlobal represents the SB_Global table
type SBGlobal struct {
	UUID        string            `ovsdb:"_uuid"`
	NbCfg       int               `ovsdb:"nb_cfg"`
	Options     map[string]string `ovsdb:"options"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
	Connections []string          `ovsdb:"connections"`
	SSL         *string           `ovsdb:"ssl"`
	IPSec       bool              `ovsdb:"ipsec"`
}

// ============================================================================
// Database Model Registration
// ============================================================================

// Table name constants
const (
	LogicalSwitchTable     = "Logical_Switch"
	LogicalSwitchPortTable = "Logical_Switch_Port"
	LogicalRouterTable     = "Logical_Router"
	LogicalRouterPortTable = "Logical_Router_Port"
	LoadBalancerTable      = "Load_Balancer"
	ACLTable               = "ACL"
	AddressSetTable        = "Address_Set"
	PortGroupTable         = "Port_Group"
	NBGlobalTable          = "NB_Global"
	ChassisTable           = "Chassis"
	EncapTable             = "Encap"
	PortBindingTable       = "Port_Binding"
	SBGlobalTable          = "SB_Global"
)

// NBDBModel returns the database model for OVN Northbound database
func NBDBModel() (model.ClientDBModel, error) {
	return model.NewClientDBModel("OVN_Northbound", map[string]model.Model{
		LogicalSwitchTable:     &LogicalSwitch{},
		LogicalSwitchPortTable: &LogicalSwitchPort{},
		LogicalRouterTable:     &LogicalRouter{},
		LogicalRouterPortTable: &LogicalRouterPort{},
		LoadBalancerTable:      &LoadBalancer{},
		ACLTable:               &ACL{},
		AddressSetTable:        &AddressSet{},
		PortGroupTable:         &PortGroup{},
		NBGlobalTable:          &NBGlobal{},
	})
}

// SBDBModel returns the database model for OVN Southbound database
func SBDBModel() (model.ClientDBModel, error) {
	return model.NewClientDBModel("OVN_Southbound", map[string]model.Model{
		ChassisTable:     &Chassis{},
		EncapTable:       &Encap{},
		PortBindingTable: &PortBinding{},
		SBGlobalTable:    &SBGlobal{},
	})
}
