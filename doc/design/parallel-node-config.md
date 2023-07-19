---
title: Parallel SR-IOV configuration
authors:
  - e0ne
reviewers:
  - adrianchiris
  - SchSeba
creation-date: 18-07-2023
last-updated: 18-07-2023
---

# Parallel SR-IOV configuration

## Summary
Allow SR-IOV Network Operator to configure more than one node at the moment.

## Motivation
SR-IOV Network Operator configures SR-IOV one node at a time and one nic at a same time. That means we’ll need to wait
hours or even days to configure all NICs  on large cluster deployments. Also moving all draining logic to a centralized
place which will reduce chances of race conditions and bugs that were encountered before in sriov-network-config-daemon
with draining.

### Use Cases

### Goals
* Number of drainable nodes should be 1 by default
* Number of drainable nodes should be configured by pool
* Nodes pool should be defined by node selector
* A node could be included into several pools. In this case pool should be 
  selected by it’s priority like it’s implemented for SriovNetworkNodePolicy
* Move all drain-related logic into the centralized place


### Non-Goals
Parallel NICs configuration on the same node is out of scope of this proposal

## Proposal
Introduce nodes pool drain configuration and controller to meet Goals targets.


### Workflow Description
A new Drain controller will be introduced to manage node drain and cordon procedures. That means we don't need to do
drain and use `drain lock` in config daemon anymore. The overall drain process will be covered by the following states:

```golang
DrainIdle        = "Idle"
DrainDisabled    = "Drain_Disabled"
DrainRequired    = "Drain_Required"
DrainMcpPausing  = "Draining_MCP_Pausing"
DrainMcpPaused   = "Draining_MCP_Paused"
Draining         = "Draining"
DrainingComplete = "Draining_Complete"
```

Drain controller will watch for node annotation, `SriovNetworkNodeState` and `SriovNetworkPoolConfig` changes:
```golang
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=SriovNetworkPoolConfig,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=SriovNetworkNodeState,verbs=get;list;watch;update;patch
```

Once `SriovNetworkNodeState.Status.DrainStatus` will be marked as `Drain_Required` by config daemon controller add will
start draining procedure.  Reconcile loop will check if current `Draining` nodes count is greater or equal than
`MaxParallelNodeConfiguration` and will mark it as `Draining` to start draining procedure. If current Draining nodes
count equals to `MaxParallelNodeConfiguration`, no `SriovNetworkNodeState` updates will be applied in the current
reconcile loop.

Once draining and configuration will be finished config daemon will update `SriovNetworkNodeState.Status.DrainStatus` to
`Idle`.

We can add a webhook to block the change of the configuration when the nodes are draining to a lower number. Without
validating webhook controller will proceed drain procedure for the current `Draining` nodes until the count of them
will decrease to specified `MaxParallelNodeConfiguration` number.

We need this logic in controller to have it centralized and not to have race condition issues during
`SriovNetworkNodeStates` update.

Config daemon will be responsible for setting `SriovNetworkNodeState.Status.DrainStatus=Drain_Required` and
`SriovNetworkNodeState.Status.DrainStatus=DrainComplete` only. It will simplify its implementation. 

### API Extensions

#### Extend existing CR SriovNetworkNodeState
SriovNetworkNodeState already contains current state of the configuration progress in a `status` field, so we don't need
to store drain status in node annotation. `SriovNetworkNodeStateStatus` will be extended to contain drain progress
instead of node annotation:

```golang
type DrainStatusType string

// SriovNetworkNodeStateStatus defines the observed state of SriovNetworkNodeState
type SriovNetworkNodeStateStatus struct {
	Interfaces    InterfaceExts     `json:"interfaces,omitempty"`
    // +kubebuilder:validation:Enum=Idle,Drain_Disabled,Drain_Required,Draining_MCP_Pausing,Draining_MCP_Paused,Draining,Draining_Complete
	DrainStatus    DrainStatusType  `json:"drainStatus,omitempty"`
    // +kubebuilder:validation:Enum=Idle,Succeeded,Failed,InProgress
	SyncStatus    string             `json:"syncStatus,omitempty"`
	LastSyncError string             `json:"lastSyncError,omitempty"`
}
```

#### Extend existing CR SriovNetworkPoolConfig
SriovNetworkPoolConfig is used only for OpenShift to provide configuration for
OVS Hardware Offloading. We can extend it to add configuration for the drain
pool. E.g.:

```golang
// DrainConfigSpec defines node pool drain configuration
type DrainConfigSpec struct {
    // Number of nodes can be configured in parallel
    // 0 means no limit, all nodes will be configured in parallel
    // +kubebuilder:default:=1
    // +kubebuilder:validation:Minimum:=0
    MaxParallelNodeConfiguration int `json:"maxParallelNodeConfiguration,omitempty"`
}

// SriovNetworkPoolConfigSpec defines the desired state of SriovNetworkPoolConfig
type SriovNetworkPoolConfigSpec struct {
    // +kubebuilder:validation:Minimum=0
    // +kubebuilder:validation:Maximum=99
    // Priority of the SriovNetworkPoolConfig, higher priority policies can override lower ones.
    Priority int `json:"priority,omitempty"`
    // OvsHardwareOffloadConfig describes the OVS HWOL configuration for selected Nodes
    OvsHardwareOffloadConfig OvsHardwareOffloadConfig `json:"ovsHardwareOffloadConfig,omitempty"`
    // NodeSelectorTerms is a list of node selectors to apply SriovNetworkPoolConfig
    NodeSelectorTerms *v1.NodeSelectorTerm `json:"nodeSelector,omitempty"`
    DrainConfig DrainConfigSpec `json:"drainConfig,omitempty"`
}
```

```yaml
apiVersion: v1
kind: SriovNetworkPoolConfig
metadata:
  name: pool-1
  namespace: network-operator
spec:
  priority:  44
  drainConfig:
    maxParallelNodeConfiguration: 5
  nodeSelectorTerms:
    - matchExpressions:
      - key: some-label
        operator: In
        values:
          - val-2
    - matchExpressions:
      - key: other-label
        operator: "Exists"
```

Default pool should be always created with the lowest priority:
```yaml
apiVersion: v1
kind: SriovNetworkPoolConfig
metadata:
  name: default
  namespace: network-operator
spec:
  priority: 0
  drainConfig:
    MaxParallelNodeConfiguration: 1
```

Once this change will be implemented `SriovNetworkPoolConfig` configuration will be applied both to vanilla Kubernetes
and OpenShift clusters.

### Implementation Details/Notes/Constraints
The implementation of this feature is very complex and requires a lot of changes to different parts of SR-IOV Network
Operator. To not introduce breaking changes we have to split this effort to several phases:
* implement new Drain controller without user-facing API changes:
  it will proceed only one node configuration at the same time and doesn't require API changes
* introduce new API changes to support pool of nodes to be drained in a parallel:
  at this phase we introduce new fields in SriovNetworkPoolConfig and modify Drain controller to watch for
  the specified CRs and proceed drain in a parallel per node pool configuration

All phases should be implemented one-by-one in a separate PRs in the order above.

### Upgrade & Downgrade considerations
After operator upgrade we have to support `sriovnetwork.openshift.io/state` node annotation to
`SriovNetworkNodeState.Status.DrainStatus` migration. This logic will be implemented in a Drain controller and should be
supported at least until next major version of the operator.

Node reconcile loop will check for `sriovnetwork.openshift.io/state` annotation and move its value to
`SriovNetworkNodeState.Status.DrainStatus` field. `sriovnetwork.openshift.io/state` annotation will be deleted by operator once
`SriovNetworkNodeState` is updated.

### Alternative APIs
#### Option 1: extend SriovOperatorConfig CRD
We can extend SriovOperatorConfig CRD to include drain pools configuration. E.g.:

```yaml
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovOperatorConfig
metadata:
name: default
namespace: network-operator
spec:
# Add fields here
enableInjector: false
enableOperatorWebhook: false
configDaemonNodeSelector: {}
disableDrain: false
drainConfig:
- name: default
  maxParallelNodeConfiguration: 1
  priority: 0 # the lowest priority
- name: pool-1
  maxParallelNodeConfiguration: 5
  priority: 44
  # empty nodeSelectorTerms means 'all nodes'
  nodeSelectorTerms:
  - matchExpressions:
    - key: some-label
      operator: In
      values:
      - val-1
      - val-2
  - matchExpressions:
    - key: other-label
      operator: "Exists"
```

We didn't choose this option because SriovOperatorConfig contains Config Daemon-specific options only while draing
configuration is node-specific.

#### Option 2:  New CRD
Add new `DrainConfiguration`CRD with fields mentioned in previous options.
We can extend SriovOperatorConfig CRD to include drain pools configuration. E.g.:
```yaml
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovDrainConfig
metadata:
  name: default
  namespace: network-operator
spec:
  maxParallelNodeConfiguration: 1
  priority: 0 # the lowest priority
  # empty nodeSelectorTerms means 'all nodes'
  nodeSelectorTerms:
  - matchExpressions:
  - key: some-label
  operator: In
```

We didn't choose this option because there is already defined `SriovNetworkPoolConfig` CRD wich could be uses for needed
configuration.

### Examples
Given a cluster with 5 worker nodes (A, B, C, D, E) and two `SriovNetworkPoolConfigs`:
- `pool1` that targets A,B,C, priority 1, maxParallelNodeConfiguration 1
- `pool2` that targets C,D,E, priority 99, maxParallelNodeConfiguration 2

When the user creates a policy that applies to C,D,E, then C, D, E starts configuring immediately, because:
- C belongs to `pool1` (as it has the highest priority), where there is no other node configuring at the moment
- D and E belongs to `pool2` and it has `maxParalleNodeConfiguration=2`

### Test Plan
* Unit tests will be implemented for new Drain Controller.
** E2E, manual or automation functional testing should have such test cases:
** to verify that we actually configure SR-IOV on `MaxParallelNodeConfiguration` nodes at the same time
** to check that we don't configure more than `MaxParallelNodeConfiguration` nodes at the same time
