package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SriovOperatorConfigSpec defines the desired state of SriovOperatorConfig
// +k8s:openapi-gen=true
type SriovOperatorConfigSpec struct {
	// NodeSelector selects the nodes to be configured
	ConfigDaemonNodeSelector map[string]string `json:"configDaemonNodeSelector,omitempty"`
	// Flag to control whether the network resource injector webhook shall be deployed
	EnableInjector *bool `json:"enableInjector,omitempty"`
	// Flag to control whether the operator admission controller webhook shall be deployed
	EnableOperatorWebhook *bool `json:"enableOperatorWebhook,omitempty"`
	// Flag to control the log verbose level of the operator. Set to '0' to show only the basic logs. And set to '2' to show all the available logs.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2
	LogLevel int `json:"logLevel,omitempty"`
	// Flag to disable nodes drain during debugging
	DisableDrain bool `json:"disableDrain,omitempty"`
	// Flag to enable OVS hardware offload. Set to 'true' to provision switchdev-configuration.service and enable OpenvSwitch hw-offload on nodes.
	EnableOvsOffload bool `json:"enableOvsOffload,omitempty"`
	// OvsHardwareOffload describes the OVS HWOL configuration for selected Nodes
	OvsHardwareOffload []OvsHardwareOffloadConfig `json:"ovsHardwareOffload,omitempty"`
}

type OvsHardwareOffloadConfig struct {
	// On Kubernetes:
	// NodeSelector selects Kubernetes Nodes to be configured with OVS HWOL configurations
	// OVS HWOL configurations are generated automatically by Operator
	// Labels in NodeSelector are ANDed when selecting Kubernetes Nodes
	// On OpenShift:
	// NodeSelector matches on Labels defined in MachineConfigPoolSpec.NodeSelector
	// OVS HWOL MachineConfigs are generated and applied to Nodes in MachineConfigPool
	// Labels in NodeSelector are ANDed when matching on MachineConfigPoolSpec.NodeSelector
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

type OvsHardwareOffloadConfigStatus struct {
	// On Kubernetes:
	// Nodes shows the selected names of Kubernetes Nodes that are configured with OVS HWOL
	// On OpenShift:
	// Nodes shows the selected names of MachineConfigPools that are configured with OVS HWOL
	Nodes []string `json:"nodes,omitempty"`
}

// SriovOperatorConfigStatus defines the observed state of SriovOperatorConfig
// +k8s:openapi-gen=true
type SriovOperatorConfigStatus struct {
	// Show the runtime status of the network resource injector webhook
	Injector string `json:"injector,omitempty"`
	// Show the runtime status of the operator admission controller webhook
	OperatorWebhook string `json:"operatorWebhook,omitempty"`
	// Show the runtime status of OvsHardwareOffload
	OvsHardwareOffload []OvsHardwareOffloadConfigStatus `json:"ovsHardwareOffload,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SriovOperatorConfig is the Schema for the sriovoperatorconfigs API
// +genclient
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=sriovoperatorconfigs,scope=Namespaced
type SriovOperatorConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SriovOperatorConfigSpec   `json:"spec,omitempty"`
	Status SriovOperatorConfigStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SriovOperatorConfigList contains a list of SriovOperatorConfig
type SriovOperatorConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SriovOperatorConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SriovOperatorConfig{}, &SriovOperatorConfigList{})
}
