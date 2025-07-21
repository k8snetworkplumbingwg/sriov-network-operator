---
title: Kubernetes Conditions Integration for SR-IOV Network Operator CRDs
authors:
  - SR-IOV Network Operator Team
reviewers:
  - TBD
creation-date: 21-07-2025
last-updated: 21-07-2025
---

# Kubernetes Conditions Integration for SR-IOV Network Operator CRDs

## Summary

This proposal enhances the observability and operational transparency of the SR-IOV Network Operator by integrating standard Kubernetes conditions into the status of its key Custom Resource Definitions (CRDs). This will enable users and automated systems to easily understand the current state, progress, and health of SR-IOV network configurations and components directly through Kubernetes API objects.

## Motivation

Adding Kubernetes conditions to the SR-IOV Network Operator's CRDs is crucial for several reasons:

* **Improved Observability:** Conditions provide a standardized, machine-readable way to convey the state of a resource, including its readiness, progress, and any encountered issues. This allows for better monitoring and debugging.

* **Enhanced User Experience:** Users can quickly ascertain the health and status of their `SriovNetwork`, `SriovIBNetwork`, `OVSNetwork`, `SriovNetworkNodeState`, `SriovOperatorConfig`, and `SriovNetworkPoolConfig` resources without needing to delve into logs or complex operator-specific status fields.

* **Standardized API Interaction:** Aligning with Kubernetes' best practices for API object status makes the SR-IOV operator more consistent with other Kubernetes operators and native resources, simplifying integration with existing tooling (e.g., `kubectl wait`, Prometheus alerts).

* **Automated Remediation and Orchestration:** External controllers or automation tools can reliably react to changes in resource conditions, enabling more robust and intelligent orchestration workflows and automated problem resolution.

* **Clearer Error Reporting:** Specific conditions can indicate different types of errors (e.g., `Degraded`, `Available`, `Progressing`), providing more granular insight into failures.

* **Simplified Troubleshooting:** When a resource is not in the desired state, conditions can point directly to the reason, accelerating troubleshooting.

### Use Cases

1. **Network Resource Provisioning Status:**
   - A user creates a `SriovNetwork`, `SriovIBNetwork`, or `OVSNetwork` custom resource
   - Condition `Ready` is set to `True` once the network is successfully provisioned and ready for use by pods
   - Condition `Degraded` is set to `True` with a reason if the network provisioning fails

2. **Node Configuration Health:**
   - The operator updates the `SriovNetworkNodeState` for a node
   - Condition `Progressing` when the operator is applying changes to the node's SR-IOV configuration
   - Condition `Degraded` if a node's SR-IOV configuration is incorrect
   - Condition `Ready` indicating the overall readiness of the SR-IOV components on that specific node

3. **Operator Configuration Status:**
   - An administrator modifies the `SriovOperatorConfig`
   - Condition `Ready` indicates that the operator's components are running and healthy
   - Condition `Degraded` if the operator itself encounters issues

4. **Pool Configuration Management:**
   - An administrator creates or updates a `SriovNetworkPoolConfig`
   - Condition `Ready` indicates that the pool configuration has been successfully applied to all target nodes
   - Condition `Progressing` when the pool configuration is being applied to the selected nodes
   - Condition `Degraded` if the pool configuration fails to apply or conflicts with existing configurations

### Goals

* Add standard Kubernetes conditions to all major SR-IOV CRDs (`SriovNetwork`, `SriovIBNetwork`, `OVSNetwork`, `SriovNetworkNodeState`, `SriovOperatorConfig`, `SriovNetworkPoolConfig`)
* Implement consistent condition types across all CRDs where applicable
* Ensure conditions are updated in real-time as resource states change
* Maintain backward compatibility with existing status fields
* Provide comprehensive documentation and examples for condition usage
* Enable `kubectl wait` functionality for all resources

### Non-Goals

* Modifying existing status field structures (maintaining backward compatibility)
* Adding conditions to deprecated or legacy CRDs
* Implementing custom condition types beyond standard Kubernetes patterns
* Changing existing controller reconciliation logic beyond condition updates

## Proposal

### Workflow Description

The implementation will follow a phased approach to add conditions to each CRD:

#### Phase 1: API Definition Updates
1. Update CRD status structures to include `conditions []metav1.Condition` field
2. Define standard condition types and their semantics for each CRD

#### Phase 2: Controller Implementation
1. Modify existing controllers to set and update conditions during reconciliation
2. Implement condition helper functions for consistent condition management
3. Ensure conditions are updated atomically with other status changes

#### Phase 3: Integration and Testing
1. Add comprehensive unit and integration tests for condition behavior
2. Update documentation with condition examples and usage patterns
3. Validate `kubectl wait` functionality

### API Extensions

#### Common Condition Types

The following condition types will be used consistently across applicable CRDs:

```go
const (
    // Progressing indicates that the resource is being actively reconciled
    ConditionProgressing = "Progressing"
    
    // Degraded indicates that the resource is not functioning as expected
    ConditionDegraded = "Degraded"
    
    // Ready indicates that the resource has reached its desired state and is fully functional
    ConditionReady = "Ready"
)
```

#### CRD-Specific Updates

##### SriovNetwork Status Enhancement

```go
type SriovNetworkStatus struct {
    // +patchMergeKey=type
    // +patchStrategy=merge
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

**Conditions:**
- `Ready`: NetworkAttachmentDefinition is created and network is ready for use
- `Degraded`: NetworkAttachmentDefinition creation failed or configuration is invalid

##### SriovIBNetwork Status Enhancement

```go
type SriovIBNetworkStatus struct {
    // +patchMergeKey=type
    // +patchStrategy=merge
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

**Conditions:**
- `Ready`: NetworkAttachmentDefinition is created and network is ready for use
- `Degraded`: NetworkAttachmentDefinition creation failed or configuration is invalid

##### OVSNetwork Status Enhancement

```go
type OVSNetworkStatus struct {
    // +patchMergeKey=type
    // +patchStrategy=merge
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

**Conditions:**
- `Ready`: NetworkAttachmentDefinition is created and network is ready for use
- `Degraded`: NetworkAttachmentDefinition creation failed or configuration is invalid

##### SriovNetworkNodeState Status Enhancement

```go
type SriovNetworkNodeStateStatus struct {
    Interfaces    InterfaceExts `json:"interfaces,omitempty"`
    Bridges       Bridges       `json:"bridges,omitempty"`
    System        System        `json:"system,omitempty"`
    SyncStatus    string        `json:"syncStatus,omitempty"`
    LastSyncError string        `json:"lastSyncError,omitempty"`
    
    // +patchMergeKey=type
    // +patchStrategy=merge
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

**Conditions:**
- `Ready`: Node's SR-IOV configuration is complete and functional
- `Progressing`: Node is being configured (VF creation, driver loading, node draining, etc.)
- `Degraded`: Node configuration failed or hardware issues detected

##### SriovOperatorConfig Status Enhancement

```go
type SriovOperatorConfigStatus struct {
    Injector        string `json:"injector,omitempty"`
    OperatorWebhook string `json:"operatorWebhook,omitempty"`
    
    // +patchMergeKey=type
    // +patchStrategy=merge
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

**Conditions:**
- `Ready`: Operator components are running and healthy
- `Degraded`: Operator components are failing or misconfigured
- `Progressing`: Operator configuration is being applied

##### SriovNetworkPoolConfig Status Enhancement

```go
type SriovNetworkPoolConfigStatus struct {
    // +patchMergeKey=type
    // +patchStrategy=merge
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

**Conditions:**
- `Ready`: Pool configuration has been successfully applied to all target nodes
- `Progressing`: Pool configuration is being applied to selected nodes
- `Degraded`: Pool configuration failed to apply or conflicts with existing configurations

### Implementation Details/Notes/Constraints

#### Condition Management Helper Functions

```go
// ConditionManager provides helper functions for managing conditions
type ConditionManager struct{}

func (cm *ConditionManager) SetCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string, generation int64) {
    condition := metav1.Condition{
        Type:               conditionType,
        Status:             status,
        Reason:             reason,
        Message:            message,
        ObservedGeneration: generation,
    }
    meta.SetStatusCondition(conditions, condition)
}

func (cm *ConditionManager) IsConditionTrue(conditions []metav1.Condition, conditionType string) bool {
    condition := meta.FindStatusCondition(conditions, conditionType)
    return condition != nil && condition.Status == metav1.ConditionTrue
}
```

#### Controller Integration Pattern

```go
func (r *SriovNetworkReconciler) updateConditions(ctx context.Context, sriovNetwork *sriovnetworkv1.SriovNetwork) error {
    cm := &ConditionManager{}
    
    // Check if NetworkAttachmentDefinition exists and is valid
    if nad, err := r.getNetworkAttachmentDefinition(ctx, sriovNetwork); err != nil {
        cm.SetCondition(&sriovNetwork.Status.Conditions, 
            ConditionReady, metav1.ConditionFalse, 
            "NetworkAttachmentDefinitionNotFound", err.Error(), 
            sriovNetwork.Generation)
        cm.SetCondition(&sriovNetwork.Status.Conditions, 
            ConditionDegraded, metav1.ConditionTrue, 
            "ProvisioningFailed", err.Error(), 
            sriovNetwork.Generation)
    } else {
        cm.SetCondition(&sriovNetwork.Status.Conditions, 
            ConditionReady, metav1.ConditionTrue, 
            "NetworkReady", "Network is successfully provisioned and ready for use", 
            sriovNetwork.Generation)
        cm.SetCondition(&sriovNetwork.Status.Conditions, 
            ConditionDegraded, metav1.ConditionFalse, 
            "NetworkHealthy", "Network is functioning correctly", 
            sriovNetwork.Generation)
    }
    
    return r.Status().Update(ctx, sriovNetwork)
}
```

#### Backward Compatibility

* Existing status fields will be preserved
* Conditions will be added as optional fields
* Controllers will continue to update legacy status fields alongside conditions
* Client code relying on existing status fields will not be affected

#### Error Handling

* Condition updates will not block main reconciliation logic
* Failed condition updates will be logged but won't cause reconciliation failure
* Conditions will be updated atomically with other status changes when possible

### Upgrade & Downgrade considerations

#### Upgrade Considerations

* New CRD versions with condition fields will be backward compatible
* Existing CR instances will continue to function without conditions
* Controllers will start populating conditions immediately after upgrade
* No manual intervention required from users

#### Downgrade Considerations

* Conditions will be ignored by older controller versions
* Existing status fields will continue to be populated
* No data loss or functionality degradation during downgrade
* CRD structure remains compatible with older API versions

This proposal provides a comprehensive foundation for integrating Kubernetes conditions into the SR-IOV Network Operator, significantly improving observability and operational experience while maintaining full backward compatibility. 