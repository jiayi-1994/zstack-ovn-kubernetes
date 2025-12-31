// Package v1 contains API Schema definitions for the network v1 API group.
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SubnetPhase represents the current phase of a Subnet
type SubnetPhase string

const (
SubnetPhasePending SubnetPhase = "Pending"
SubnetPhaseActive  SubnetPhase = "Active"
SubnetPhaseFailed  SubnetPhase = "Failed"
)

// SubnetProtocol represents the IP protocol version
type SubnetProtocol string

const (
SubnetProtocolIPv4 SubnetProtocol = "IPv4"
SubnetProtocolIPv6 SubnetProtocol = "IPv6"
SubnetProtocolDual SubnetProtocol = "Dual"
)

// SubnetSpec defines the desired state of Subnet.
type SubnetSpec struct {
// CIDR is the IP range for this subnet in CIDR notation.
// +kubebuilder:validation:Required
CIDR string `json:"cidr"`

// Gateway is the default gateway IP for this subnet.
// +kubebuilder:validation:Required
Gateway string `json:"gateway"`

// ExcludeIPs is a list of IPs or IP ranges to exclude from allocation.
// +optional
ExcludeIPs []string `json:"excludeIPs,omitempty"`

// ExternalLogicalSwitch is the name of an existing OVN Logical Switch.
// +optional
ExternalLogicalSwitch string `json:"externalLogicalSwitch,omitempty"`

// VlanID is the VLAN ID for underlay networks.
// +optional
// +kubebuilder:validation:Minimum=1
// +kubebuilder:validation:Maximum=4094
VlanID int `json:"vlanID,omitempty"`

// Provider is the physical network provider name for underlay networks.
// +optional
Provider string `json:"provider,omitempty"`

// Protocol is the IP protocol version for this subnet.
// +kubebuilder:validation:Enum=IPv4;IPv6;Dual
// +kubebuilder:default=IPv4
// +optional
Protocol SubnetProtocol `json:"protocol,omitempty"`

// Default indicates whether this is the default subnet for the cluster.
// +optional
// +kubebuilder:default=false
Default bool `json:"default,omitempty"`

// Namespaces is a list of namespaces that can use this subnet.
// +optional
Namespaces []string `json:"namespaces,omitempty"`

// EnableDHCP enables DHCP for this subnet.
// +optional
// +kubebuilder:default=false
EnableDHCP bool `json:"enableDHCP,omitempty"`
}

// SubnetStatus defines the observed state of Subnet.
type SubnetStatus struct {
// Phase is the current phase of the subnet.
// +kubebuilder:validation:Enum=Pending;Active;Failed
Phase SubnetPhase `json:"phase,omitempty"`

// Reason provides additional information about the current phase.
Reason string `json:"reason,omitempty"`

// Message provides a human-readable description of the current state.
Message string `json:"message,omitempty"`

// AvailableIPs is the number of available IP addresses in the subnet.
AvailableIPs int `json:"availableIPs,omitempty"`

// UsedIPs is the number of IP addresses currently allocated to Pods.
UsedIPs int `json:"usedIPs,omitempty"`

// LogicalSwitch is the name of the associated OVN Logical Switch.
LogicalSwitch string `json:"logicalSwitch,omitempty"`

// Conditions represent the latest available observations of the subnet's state.
// +optional
Conditions []metav1.Condition `json:"conditions,omitempty"`

// LastUpdateTime is the timestamp of the last status update.
// +optional
LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`
}

// Subnet condition types
const (
SubnetConditionReady             = "Ready"
SubnetConditionLogicalSwitchReady = "LogicalSwitchReady"
SubnetConditionIPPoolReady       = "IPPoolReady"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=sn
// +kubebuilder:printcolumn:name="CIDR",type=string,JSONPath=`.spec.cidr`
// +kubebuilder:printcolumn:name="Gateway",type=string,JSONPath=`.spec.gateway`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Available",type=integer,JSONPath=`.status.availableIPs`
// +kubebuilder:printcolumn:name="Used",type=integer,JSONPath=`.status.usedIPs`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Subnet is the Schema for the subnets API.
type Subnet struct {
metav1.TypeMeta   `json:",inline"`
metav1.ObjectMeta `json:"metadata,omitempty"`

Spec   SubnetSpec   `json:"spec,omitempty"`
Status SubnetStatus `json:"status,omitempty"`
}

// IsExternalMode returns true if the subnet references an external Logical Switch
func (s *Subnet) IsExternalMode() bool {
return s.Spec.ExternalLogicalSwitch != ""
}

// IsUnderlayMode returns true if the subnet uses underlay networking
func (s *Subnet) IsUnderlayMode() bool {
return s.Spec.VlanID > 0 || s.Spec.Provider != ""
}

// GetLogicalSwitchName returns the name of the OVN Logical Switch for this subnet.
func (s *Subnet) GetLogicalSwitchName() string {
if s.IsExternalMode() {
return s.Spec.ExternalLogicalSwitch
}
return "subnet-" + s.Name
}

// +kubebuilder:object:root=true

// SubnetList contains a list of Subnet
type SubnetList struct {
metav1.TypeMeta `json:",inline"`
metav1.ListMeta `json:"metadata,omitempty"`
Items           []Subnet `json:"items"`
}

// DeepCopyInto copies the receiver into the given *Subnet.
func (in *Subnet) DeepCopyInto(out *Subnet) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy creates a deep copy of the Subnet.
func (in *Subnet) DeepCopy() *Subnet {
	if in == nil {
		return nil
	}
	out := new(Subnet)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject returns a deep copy as runtime.Object.
func (in *Subnet) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto copies the receiver into the given *SubnetSpec.
func (in *SubnetSpec) DeepCopyInto(out *SubnetSpec) {
	*out = *in
	if in.ExcludeIPs != nil {
		in, out := &in.ExcludeIPs, &out.ExcludeIPs
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	if in.Namespaces != nil {
		in, out := &in.Namespaces, &out.Namespaces
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
}

// DeepCopy creates a deep copy of the SubnetSpec.
func (in *SubnetSpec) DeepCopy() *SubnetSpec {
	if in == nil {
		return nil
	}
	out := new(SubnetSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into the given *SubnetStatus.
func (in *SubnetStatus) DeepCopyInto(out *SubnetStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]metav1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.LastUpdateTime != nil {
		in, out := &in.LastUpdateTime, &out.LastUpdateTime
		*out = (*in).DeepCopy()
	}
}

// DeepCopy creates a deep copy of the SubnetStatus.
func (in *SubnetStatus) DeepCopy() *SubnetStatus {
	if in == nil {
		return nil
	}
	out := new(SubnetStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into the given *SubnetList.
func (in *SubnetList) DeepCopyInto(out *SubnetList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]Subnet, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy creates a deep copy of the SubnetList.
func (in *SubnetList) DeepCopy() *SubnetList {
	if in == nil {
		return nil
	}
	out := new(SubnetList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject returns a deep copy as runtime.Object.
func (in *SubnetList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
