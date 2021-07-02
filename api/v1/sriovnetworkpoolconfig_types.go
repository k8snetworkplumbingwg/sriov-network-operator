package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SriovNetworkPoolConfigSpec defines the desired state of SriovNetworkPoolConfig
type SriovNetworkPoolConfigSpec struct {
	// OvsHardwareOffloadConfig describes the OVS HWOL configuration for selected Nodes
	OvsHardwareOffloadConfig OvsHardwareOffloadConfig `json:"ovsHardwareOffloadConfig,omitempty"`
}

type OvsHardwareOffloadConfig struct {
	// Name is mandatory and must be unique.
	// On Kubernetes:
	// Name is the name of OvsHardwareOffloadConfig
	// On OpenShift:
	// Name is the name of MachineConfigPool to be enabled with OVS hardware offload
	Name string `json:"name,omitempty"`
}

// SriovNetworkPoolConfigStatus defines the observed state of SriovNetworkPoolConfig
type SriovNetworkPoolConfigStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// SriovNetworkPoolConfig is the Schema for the sriovnetworkpoolconfigs API
type SriovNetworkPoolConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SriovNetworkPoolConfigSpec   `json:"spec,omitempty"`
	Status SriovNetworkPoolConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SriovNetworkPoolConfigList contains a list of SriovNetworkPoolConfig
type SriovNetworkPoolConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SriovNetworkPoolConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SriovNetworkPoolConfig{}, &SriovNetworkPoolConfigList{})
}
