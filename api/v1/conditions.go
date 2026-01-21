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

// Condition types used across SR-IOV Network Operator CRDs
const (
	// ConditionProgressing indicates that the resource is being actively reconciled
	ConditionProgressing = "Progressing"

	// ConditionDegraded indicates that the resource is not functioning as expected
	ConditionDegraded = "Degraded"

	// ConditionReady indicates that the resource has reached its desired state and is fully functional
	ConditionReady = "Ready"

	// ConditionDrainProgressing indicates that the node is being actively drained
	ConditionDrainProgressing = "DrainProgressing"

	// ConditionDrainDegraded indicates that the drain process is not functioning as expected
	ConditionDrainDegraded = "DrainDegraded"

	// ConditionDrainComplete indicates that the drain operation completed successfully
	ConditionDrainComplete = "DrainComplete"
)

// Common condition reasons used across SR-IOV Network Operator CRDs
const (
	// Reasons for Ready condition
	ReasonNetworkReady   = "NetworkReady"
	ReasonNodeReady      = "NodeConfigurationReady"
	ReasonNodeDrainReady = "NodeDrainReady"
	ReasonOperatorReady  = "OperatorReady"
	ReasonNotReady       = "NotReady"

	// Reasons for Degraded condition
	ReasonProvisioningFailed           = "ProvisioningFailed"
	ReasonConfigurationFailed          = "ConfigurationFailed"
	ReasonNetworkAttachmentDefNotFound = "NetworkAttachmentDefinitionNotFound"
	ReasonNetworkAttachmentDefInvalid  = "NetworkAttachmentDefinitionInvalid"
	ReasonNamespaceNotFound            = "NamespaceNotFound"
	ReasonHardwareError                = "HardwareError"
	ReasonDriverError                  = "DriverError"
	ReasonOperatorComponentsNotHealthy = "OperatorComponentsNotHealthy"
	ReasonNotDegraded                  = "NotDegraded"

	// Reasons for Progressing condition
	ReasonConfiguringNode       = "ConfiguringNode"
	ReasonApplyingConfiguration = "ApplyingConfiguration"
	ReasonCreatingVFs           = "CreatingVFs"
	ReasonLoadingDriver         = "LoadingDriver"
	ReasonDrainingNode          = "DrainingNode"
	ReasonNotProgressing        = "NotProgressing"

	// Reasons for DrainComplete condition
	ReasonDrainCompleted = "DrainCompleted"
	ReasonDrainNotNeeded = "DrainNotNeeded"
	ReasonDrainPending   = "DrainPending"

	// Reasons for DrainDegraded condition
	ReasonDrainFailed = "DrainFailed"

	// Reasons for Policy conditions
	ReasonPolicyReady                = "PolicyReady"
	ReasonPolicyNotReady             = "PolicyNotReady"
	ReasonNoMatchingNodes            = "NoMatchingNodes"
	ReasonPartiallyApplied           = "PartiallyApplied"
	ReasonAllNodesConfigured         = "AllNodesConfigured"
	ReasonSomeNodesFailed            = "SomeNodesFailed"
	ReasonSomeNodesProgressing       = "SomeNodesProgressing"
	ReasonAllNodesProgressingOrReady = "AllNodesProgressingOrReady"
)

// DrainState represents the current state of a drain operation
type DrainState string

const (
	// DrainStateIdle indicates no drain is in progress
	DrainStateIdle DrainState = "Idle"
	// DrainStateDraining indicates drain is in progress with no errors
	DrainStateDraining DrainState = "Draining"
	// DrainStateDrainingWithErrors indicates drain is in progress but encountering errors
	DrainStateDrainingWithErrors DrainState = "DrainingWithErrors"
	// DrainStateComplete indicates drain completed successfully
	DrainStateComplete DrainState = "Complete"
)

// NetworkStatus defines the common observed state for network-type CRDs
type NetworkStatus struct {
	// Conditions represent the latest available observations of the network's state
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// ConditionsEqual compares two condition slices ignoring LastTransitionTime.
// This is useful to avoid unnecessary API updates when conditions haven't actually changed.
func ConditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}

	// Create maps for easier comparison
	aMap := make(map[string]metav1.Condition)
	for _, c := range a {
		aMap[c.Type] = c
	}

	for _, bc := range b {
		ac, exists := aMap[bc.Type]
		if !exists {
			return false
		}
		// Compare all fields except LastTransitionTime
		if ac.Status != bc.Status ||
			ac.Reason != bc.Reason ||
			ac.Message != bc.Message ||
			ac.ObservedGeneration != bc.ObservedGeneration {
			return false
		}
	}
	return true
}
