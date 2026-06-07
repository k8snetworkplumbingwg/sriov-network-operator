/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PluginNameValue defines the plugin name
// +kubebuilder:validation:Enum=mellanox
type PluginNameValue string

// PluginNameSlice defines a slice of PluginNameValue
type PluginNameSlice []PluginNameValue

// ToStringSlice converts PluginNameSlice to string slice
func (pns PluginNameSlice) ToStringSlice() []string {
	ss := make([]string, 0, len(pns))
	for _, v := range pns {
		ss = append(ss, string(v))
	}
	return ss
}

// LogConfig contains configuration for config daemon log persistence on the host filesystem.
type LogConfig struct {
	// Enabled controls whether persistent log storage is active.
	// Defaults to true.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// MaxSizeMB is the maximum size in megabytes of a log file before rotation.
	// Defaults to 100. Minimum 1 MB; maximum 1024 MB (1 GB).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	// +optional
	MaxSizeMB *int `json:"maxSizeMB,omitempty"`

	// MaxFiles is the maximum number of old log files to retain after rotation.
	// Defaults to 5. Minimum 1; maximum 20.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	// +optional
	MaxFiles *int `json:"maxFiles,omitempty"`

	// MaxAgeDays is the maximum number of days to retain old log files.
	// Defaults to 30. Set to 0 to disable age-based cleanup (files are then
	// bounded only by MaxFiles). Maximum 365.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=365
	// +optional
	MaxAgeDays *int `json:"maxAgeDays,omitempty"`

	// Compress controls whether rotated log files are compressed using gzip.
	// Defaults to true.
	// +optional
	Compress *bool `json:"compress,omitempty"`

	// HostPath is the directory on the host where log files are stored.
	// Defaults to "/var/log/sriov-network-config-daemon".
	// +optional
	HostPath *string `json:"hostPath,omitempty"`
}

// SriovOperatorConfigSpec defines the desired state of SriovOperatorConfig
type SriovOperatorConfigSpec struct {
	// NodeSelector selects the nodes to be configured
	ConfigDaemonNodeSelector map[string]string `json:"configDaemonNodeSelector,omitempty"`
	// Flag to control whether the network resource injector webhook shall be deployed
	EnableInjector bool `json:"enableInjector,omitempty"`
	// Flag to control whether the operator admission controller webhook shall be deployed
	EnableOperatorWebhook bool `json:"enableOperatorWebhook,omitempty"`
	// Flag to control the log verbose level of the operator. Set to '0' to show only the basic logs. And set to '2' to show all the available logs.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2
	LogLevel int `json:"logLevel,omitempty"`
	// Flag to disable nodes drain during debugging
	DisableDrain bool `json:"disableDrain,omitempty"`
	// Flag to enable OVS hardware offload. Set to 'true' to provision switchdev-configuration.service and enable OpenvSwitch hw-offload on nodes.
	EnableOvsOffload bool `json:"enableOvsOffload,omitempty"`
	// Flag to enable the sriov-network-config-daemon to use a systemd service to configure SR-IOV devices on boot
	// Default mode: daemon
	// +kubebuilder:validation:Enum=daemon;systemd
	ConfigurationMode ConfigurationModeType `json:"configurationMode,omitempty"`
	// Flag to enable Container Device Interface mode for SR-IOV Network Device Plugin
	UseCDI bool `json:"useCDI,omitempty"`
	// DisablePlugins is a list of sriov-network-config-daemon plugins to disable
	DisablePlugins PluginNameSlice `json:"disablePlugins,omitempty"`
	// FeatureGates to enable experimental features
	FeatureGates map[string]bool `json:"featureGates,omitempty"`
	// ConfigDaemonEnvVars allows to specify custom environment variables
	// for the sriov-network-config-daemon
	ConfigDaemonEnvVars map[string]string `json:"configDaemonEnvVars,omitempty"`
	// LogConfig contains configuration for config daemon log persistence.
	// When unset, persistent logging is enabled with default values.
	// +optional
	LogConfig *LogConfig `json:"logConfig,omitempty"`
}

// SriovOperatorConfigStatus defines the observed state of SriovOperatorConfig
type SriovOperatorConfigStatus struct {
	// Show the runtime status of the network resource injector webhook
	Injector string `json:"injector,omitempty"`
	// Show the runtime status of the operator admission controller webhook
	OperatorWebhook string `json:"operatorWebhook,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// SriovOperatorConfig is the Schema for the sriovoperatorconfigs API
type SriovOperatorConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SriovOperatorConfigSpec   `json:"spec,omitempty"`
	Status SriovOperatorConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SriovOperatorConfigList contains a list of SriovOperatorConfig
type SriovOperatorConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SriovOperatorConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SriovOperatorConfig{}, &SriovOperatorConfigList{})
}
