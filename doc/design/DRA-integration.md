---
title: DRA (Dynamic Resource Allocation) Integration
authors:
  - rollandf
reviewers:
  - SchSeba
  - adrianchiris
creation-date: 11-02-2026
last-updated: 11-02-2026
---

# DRA (Dynamic Resource Allocation) Integration

## Summary

This design proposes integrating Kubernetes Dynamic Resource Allocation (DRA) framework into the SR-IOV Network Operator as an alternative to the traditional device plugin approach. The integration will allow users to leverage the DRA driver for SR-IOV (`dra-driver-sriov`) instead of `sriov-network-device-plugin` for exposing and allocating SR-IOV Virtual Functions to workloads.

This will be implemented as an opt-in feature controlled by a feature flag, allowing gradual adoption and testing while maintaining backward compatibility with existing device plugin-based deployments.

## Motivation

The Kubernetes Device Plugin framework has several limitations that the Dynamic Resource Allocation (DRA) framework addresses:

1. **Limited Resource Modeling**: Device plugins can only expose simple countable resources (e.g., `intel.com/sriov: 10`). They cannot express complex device characteristics, NUMA topology, or filtering criteria.

2. **Static Allocation**: Device plugin resource allocation happens before pod scheduling decisions are made, leading to potential scheduling inefficiencies and race conditions.

3. **No Resource Sharing**: Device plugins don't support controlled sharing or partitioning of resources across multiple containers or pods.

4. **Limited Filtering**: Device plugin selectors are basic and don't support the advanced filtering capabilities needed for heterogeneous SR-IOV environments (vendor ID, PCI address, NUMA node, PF name, driver type, etc.).

5. **Kubernetes Evolution**: DRA is the future direction for device resource management in Kubernetes (stable in 1.34+), and the device plugin framework may eventually be deprecated.

The DRA driver for SR-IOV (`dra-driver-sriov`) provides:
- Advanced resource filtering via `SriovResourceFilter` CRDs
- Native Kubernetes resource claim model with `ResourceClaimTemplate`
- Better integration with container runtimes via CDI (Container Device Interface) and NRI (Node Resource Interface)
- More flexible device configuration and allocation policies
- Support for both kernel networking and userspace (VFIO/DPDK) use cases

### Use Cases

1. **As a cluster administrator**, I want to use DRA for SR-IOV resource management to benefit from advanced filtering and resource modeling capabilities.

2. **As a platform engineer**, I want to define fine-grained SR-IOV resource pools based on hardware characteristics (vendor, device ID, NUMA node, PF name) using declarative CRDs.

3. **As a workload developer**, I want to request SR-IOV VFs using native Kubernetes ResourceClaims instead of device plugin resource requests:
   ```yaml
   resourceClaims:
   - name: vf
     resourceClaimTemplateName: sriov-vf-claim
   ```

4. **As a system administrator**, I want to migrate from device plugin to DRA.

5. **As an operator maintainer**, I want to support both device plugin and DRA modes in the same operator to accommodate different user requirements and migration timelines.

6. **As a cluster administrator**, I want to leverage DRA's improved scheduling and allocation capabilities for complex multi-VF workloads.

### Goals

- Add support for deploying and managing the DRA driver for SR-IOV as an alternative to the device plugin
- Introduce a feature flag in `SriovOperatorConfig` to enable/disable DRA mode
- Maintain backward compatibility with existing device plugin-based deployments
- Support both device plugin and DRA modes in the same operator (not simultaneously on the same cluster)
- Enable advanced SR-IOV resource filtering via `SriovResourceFilter` CRDs when in DRA mode
- Document migration path from device plugin to DRA
- Ensure the operator continues to manage SR-IOV configuration on nodes (VF creation, driver binding, etc.) regardless of the mode

### Non-Goals

- Running device plugin and DRA driver simultaneously on the same cluster (at least in the initial implementation)
- Automatic migration of existing device plugin configurations to DRA configurations
- Support for Kubernetes versions below 1.34 (first stable DRA release)
- Changes to the core SR-IOV configuration functionality (VF creation, driver binding, etc.)
- Implementing DRA driver features within the operator itself (we will use the existing `dra-driver-sriov` project)
- Deprecating or removing device plugin support in the near term
- Supporting DRA extended resource allocation in the initial implementation (this is an optional enhancement for future phases)

## Proposal

### DeviceClass Management Strategy

The operator will manage DeviceClass resources in two tiers:

**1. Basic DeviceClass (Phase 1 - Core):**
- Single DeviceClass named `sriovnetwork.k8snetworkplumbingwg.io`
- Created automatically when DRA feature gate is enabled
- Matches all devices from the DRA driver: `device.driver == 'sriovnetwork.k8snetworkplumbingwg.io'`
- Enables users to create ResourceClaims with CEL selectors for specific resources
- This is the equivalent of deploying the DRA driver's helm chart DeviceClass
- **Recommendation:** Always create this in Phase 1 as part of DRA driver deployment

**2. Per-ResourceName DeviceClasses (Phase 6 - Optional):**
- One DeviceClass per `SriovNetworkNodePolicy.Spec.ResourceName`
- Includes `extendedResourceName` for backward compatibility with device plugin syntax
- Only created if extended resource allocation feature is enabled
- Allows pods to use traditional `resources.limits` syntax

### Workflow Description

#### Current Device Plugin Workflow

1. User creates `SriovNetworkNodePolicy` to configure SR-IOV devices
2. Operator configures nodes (creates VFs, binds drivers, etc.)
3. Operator deploys `sriov-network-device-plugin` DaemonSet
4. Device plugin discovers VFs and exposes them as node resources (e.g., `openshift.io/sriov-nic: 8`)
5. User creates `SriovNetwork` CR which generates `NetworkAttachmentDefinition`
6. User requests resources in Pod spec via `resources.limits`
7. Kubelet allocates devices via device plugin and launches Pod

#### Proposed DRA Workflow

1. User creates `SriovNetworkNodePolicy` to configure SR-IOV devices (unchanged)
2. Operator configures nodes (creates VFs, binds drivers, etc.) (unchanged)
3. **[NEW]** Operator deploys `dra-driver-sriov` DaemonSet instead of device plugin (when DRA mode enabled)
4. **[NEW]** Operator creates basic DeviceClass (`sriovnetwork.k8snetworkplumbingwg.io`)
5. **[NEW]** DRA driver discovers VFs and maintains device state
6. **[NEW]** User optionally creates `SriovResourceFilter` CRs to define resource pools with advanced filtering
7. User creates `SriovNetwork` or `SriovIBNetwork` CR which generates `NetworkAttachmentDefinition` (unchanged)
8. **[NEW]** User creates `ResourceClaimTemplate` to request SR-IOV VFs using the basic DeviceClass
9. **[NEW]** User references ResourceClaim in Pod spec via `resourceClaims` field
10. **[NEW]** Kubelet works with DRA driver to allocate VFs and launches Pod

**Example ResourceClaimTemplate:**

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  namespace: vf-test9
  name: vf-test9
spec:
  spec:
    devices:
      requests:
      - name: vf
        deviceClassName: sriovnetwork.k8snetworkplumbingwg.io
        count: 1
```

**Example with Resource Filtering (using CEL selector):**

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  namespace: default
  name: intel-nic-claim
spec:
  spec:
    devices:
      requests:
      - name: vf
        deviceClassName: sriovnetwork.k8snetworkplumbingwg.io
        selectors:
        - cel:
            # Select devices with specific resourceName from SriovResourceFilter
            expression: device.attributes["sriovnetwork.k8snetworkplumbingwg.io"].resourceName == "intel_nic"
        count: 2
```

**Example Pod using ResourceClaim:**

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: sriov-workload
  namespace: vf-test9
  annotations:
    k8s.v1.cni.cncf.io/networks: sriov-network  # NetworkAttachmentDefinition from SriovNetwork CR
spec:
  containers:
  - name: app
    image: your-app:latest
  resourceClaims:
  - name: vf
    resourceClaimTemplateName: vf-test9
```

#### Feature Flag Control

The DRA integration will be controlled via the existing `featureGates` mechanism in the `SriovOperatorConfig` CR:

```yaml
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovOperatorConfig
metadata:
  name: default
  namespace: sriov-network-operator
spec:
  featureGates:
    dynamicResourceAllocation: false  # default: false (use device plugin)
```

**Note**: DRA driver deployment configuration (image, interface prefix, etc.) will be set via Helm values and environment variables on the operator deployment, not in the API.

When `featureGates.dynamicResourceAllocation: true`:
- Operator deploys DRA driver instead of device plugin
- Operator creates/manages DRA-related resources (DeviceClass, ServiceAccount, RBAC, etc.)
- Users can create `SriovResourceFilter` CRs for advanced filtering
- `SriovNetwork` CRs still work but users must use ResourceClaims instead of device plugin resources

### API Extensions

#### SriovOperatorConfig Extension

**Feature Gate Addition**

Add a new feature gate constant to `pkg/consts/constants.go`:

```go
// DynamicResourceAllocationFeatureGate: enables DRA driver instead of device plugin
DynamicResourceAllocationFeatureGate = "dynamicResourceAllocation"
```

Add the feature gate to the default states in `pkg/featuregate/featuregate.go`:

```go
var DefaultFeatureStates = map[string]bool{
    consts.ParallelNicConfigFeatureGate:                false,
    consts.ResourceInjectorMatchConditionFeatureGate:   false,
    consts.MetricsExporterFeatureGate:                  false,
    consts.ManageSoftwareBridgesFeatureGate:            false,
    consts.BlockDevicePluginUntilConfiguredFeatureGate: true,
    consts.MellanoxFirmwareResetFeatureGate:            false,
    consts.DynamicResourceAllocationFeatureGate:        false, // NEW: default to device plugin mode
}
```

**DRA Driver Configuration via Helm/Environment Variables**

DRA driver configuration will be provided via Helm chart values and passed to the operator as environment variables, similar to how device plugin images and other deployment settings are currently configured. The operator will read these environment variables when deploying the DRA driver.

Proposed Helm values:
```yaml
# values.yaml
images:
  sriovDevicePlugin: <existing device plugin image>
  draDriver: ghcr.io/k8snetworkplumbingwg/dra-driver-sriov:v0.1.0  # NEW

draDriver:
  defaultInterfacePrefix: "net"
  resourceFilterNamespace: "sriov-network-operator"
  cdiRoot: "/var/run/cdi"
  configurationMode: "multus"
```

These would map to environment variables on the operator deployment:
- `DRA_DRIVER_IMAGE`
- `DRA_DRIVER_DEFAULT_INTERFACE_PREFIX`
- `DRA_DRIVER_RESOURCE_FILTER_NAMESPACE`
- `DRA_DRIVER_CDI_ROOT`
- `CONFIGURATION_MODE`

#### New CRD: SriovResourceFilter

The `SriovResourceFilter` CRD from the DRA driver project will be included in the operator's Helm chart under `deployment/sriov-network-operator-chart/crds/`, similar to how the `NetworkAttachmentDefinition` CRD from Multus is currently included (`k8s.cni.cncf.io_networkattachmentdefinitions_crd.yaml`).

The CRD will be named: `sriovnetwork.k8snetworkplumbingwg.io_sriovresourcefilters.yaml`

This CRD is already defined in `dra-driver-sriov` and allows advanced filtering of SR-IOV resources. It will be copied/vendored from the DRA driver project into the operator's Helm chart.

**Automatic Generation from SriovNetworkNodePolicy**

The operator will automatically generate `SriovResourceFilter` CRs from `SriovNetworkNodePolicy` CRs when the DRA feature gate is enabled. This is analogous to how the operator currently generates the `device-plugin-config` ConfigMap from policies in device plugin mode.

Current behavior (device plugin mode):
- `SriovNetworkNodePolicyReconciler.syncDevicePluginConfigMap()` reads all policies
- For each node, it calls `renderDevicePluginConfigData()` to convert policy specs into device plugin resource configs
- Generates a ConfigMap with per-node JSON configuration

New behavior (DRA mode):
- `SriovNetworkNodePolicyReconciler` will have a new method `syncSriovResourceFilters()`
- For each unique combination of node selector + resource name + filter criteria, generate a `SriovResourceFilter` CR
- Map `SriovNetworkNodePolicy` fields to `SriovResourceFilter` fields:

| SriovNetworkNodePolicy | SriovResourceFilter |
|------------------------|---------------------|
| `Spec.ResourceName` | `Spec.Configs[].ResourceName` |
| `Spec.NicSelector.Vendor` | `Spec.Configs[].ResourceFilters[].Vendors` |
| `Spec.NicSelector.DeviceID` | `Spec.Configs[].ResourceFilters[].Devices` |
| `Spec.NicSelector.PfNames` | `Spec.Configs[].ResourceFilters[].PfNames` |
| `Spec.NicSelector.RootDevices` | `Spec.Configs[].ResourceFilters[].RootDevices` |
| `Spec.NodeSelector` | `Spec.NodeSelector` |
| Device driver (vfio-pci) | `Spec.Configs[].ResourceFilters[].Drivers` |
| NUMA info from node state | `Spec.Configs[].ResourceFilters[].NumaNodes` |

Example auto-generated `SriovResourceFilter`:
```yaml
apiVersion: sriovnetwork.k8snetworkplumbingwg.io/v1alpha1
kind: SriovResourceFilter
metadata:
  name: node-worker-1  # Per-node naming: node-<nodename>
  namespace: sriov-network-operator
  labels:
    sriovnetwork.openshift.io/generated-by: sriov-network-operator
    sriovnetwork.openshift.io/node: worker-1
  ownerReferences:
    - apiVersion: sriovnetwork.openshift.io/v1
      kind: SriovOperatorConfig
      name: default
spec:
  nodeSelector:
    kubernetes.io/hostname: worker-1  # Targets exactly one node
  configs:
  - resourceName: "intel_nic"  # From policy's resourceName
    resourceFilters:
    - vendors: ["8086"]  # From policy's nicSelector.vendor
      devices: ["154c"]  # From policy's nicSelector.deviceID (VF ID)
      pfNames: ["ens1f0"]  # From policy's nicSelector.pfNames
  # If multiple policies apply to worker-1, they all get merged into this single filter
```

**Important:** This CR is created based on policies and stays in place. The DRA driver pod has an init container that waits for the node to be ready (via node label) before starting to discover VFs.

**Note**: The CRD will always be installed as part of the operator's Helm chart, regardless of whether the DRA feature gate is enabled. However, the operator will only create/manage `SriovResourceFilter` CRs when the DRA feature gate is enabled.

#### ResourceClaimTemplate Examples

Users request SR-IOV VFs by creating `ResourceClaimTemplate` resources. Here are several usage patterns:

**1. Basic Single VF Request:**

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  namespace: vf-test9
  name: vf-test9
spec:
  spec:
    devices:
      requests:
      - name: vf
        deviceClassName: sriovnetwork.k8snetworkplumbingwg.io
        count: 1
```

**2. Request Multiple VFs:**

Request multiple VFs for load balancing or high availability:
```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  namespace: default
  name: multi-vf
spec:
  spec:
    devices:
      requests:
      - name: vf
        deviceClassName: sriovnetwork.k8snetworkplumbingwg.io
        count: 4
```

**3. Using ResourceClaim in Pod:**

Reference the ResourceClaimTemplate in your Pod:
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: sriov-workload
  namespace: vf-test9
  annotations:
    k8s.v1.cni.cncf.io/networks: sriov-network
spec:
  containers:
  - name: app
    image: your-app:latest
  resourceClaims:
  - name: vf
    resourceClaimTemplateName: vf-test9
```

**4. Complete Example with Deployment:**

Using ResourceClaim with a Deployment:
```yaml
---
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  namespace: production
  name: web-server-nic
spec:
  spec:
    devices:
      requests:
      - name: vf
        deviceClassName: sriovnetwork.k8snetworkplumbingwg.io
        count: 1
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-server
  namespace: production
spec:
  replicas: 3
  selector:
    matchLabels:
      app: web-server
  template:
    metadata:
      labels:
        app: web-server
      annotations:
        k8s.v1.cni.cncf.io/networks: sriov-network
    spec:
      containers:
      - name: nginx
        image: nginx:latest
        ports:
        - containerPort: 80
      resourceClaims:
      - name: vf
        resourceClaimTemplateName: web-server-nic
```

**Note:** The DRA driver will automatically allocate VFs based on the `SriovResourceFilter` CRs created by the operator. Users don't need to specify CEL selectors - the filtering is handled by the `SriovResourceFilter` configuration generated from `SriovNetworkNodePolicy` resources.

#### Changes to SriovNetwork

`SriovNetwork` and `SriovIBNetwork` CRs will continue to work in DRA mode, generating `NetworkAttachmentDefinition` resources. However, the `resourceName` field will have different semantics:

- **Device Plugin Mode**: `resourceName` maps to device plugin resource (e.g., `openshift.io/sriov-nic`)
- **DRA Mode**: `resourceName` can be used in `SriovResourceFilter` configurations to create resource pools

The operator should add validation or warnings when users try to use device plugin-style resource requests in pods when DRA mode is enabled.

### Implementation Details/Notes/Constraints

#### Component Architecture

```
┌──────────────────────────────────────────────────────────┐
│                  SRIOV Network Operator                  │
│                                                          │
│  ┌─────────────────────────────────────────────────────┐ │
│  │         SriovOperatorConfig Controller              │ │
│  │  (watches enableDRA flag)                           │ │
│  └─────────────────┬───────────────────────────────────┘ │
│                     │                                    │
│         ┌───────────┴──────────┐                         │
│         │                      │                         │
│         v                      v                         │
│  ┌──────────────┐      ┌──────────────────┐              │
│  │ Device Plugin│      │   DRA Driver     │              │
│  │  Deployment  │      │   Deployment     │              │
│  │   (legacy)   │      │     (new)        │              │
│  └──────────────┘      └──────────────────┘              │
│                                                          │
│  ┌─────────────────────────────────────────────────────┐ │
│  │    Node Configuration Controllers (unchanged)       │ │
│  │  - SriovNetworkNodePolicy Controller                │ │
│  │  - Config Daemon (VF creation, driver binding)      │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

#### Implementation Phases

**Phase 1: Core Infrastructure**
1. Add `DynamicResourceAllocationFeatureGate` constant to `pkg/consts/constants.go`
2. Add feature gate to default states in `pkg/featuregate/featuregate.go`
3. Vendor `SriovResourceFilter` CRD from DRA driver project into Helm chart (`crds/` directory)
4. Import `SriovResourceFilter` types from DRA driver project into operator (for CR creation)
5. Add DRA driver configuration environment variables to operator deployment
6. Update Helm chart with DRA driver image and configuration values
7. Create DRA driver deployment manifests in `bindata/manifests/dra-driver/`:
   - DaemonSet for kubelet plugin
   - ServiceAccount
   - RBAC (ClusterRole, ClusterRoleBinding)
   - Basic DeviceClass (`sriovnetwork.k8snetworkplumbingwg.io`)
8. Add logic to operator to deploy DRA driver OR device plugin based on feature gate
9. Add validation to prevent switching modes on a running cluster with active workloads

**Phase 2: Policy to Filter Conversion & DRA Driver Protection**
1. Add `syncSriovResourceFilters()` method to `SriovNetworkNodePolicyReconciler`
2. Implement per-node filter generation logic:
   - Create `SriovResourceFilter` CRs based on policies (stay in place)
   - Name filters as `node-<nodename>` for clear per-node mapping
   - Update filters when policies change (no deletion, just update)
3. Implement `renderSriovResourceFilterForNode()` to convert policy specs to filter specs for a specific node
4. Add init container to DRA driver DaemonSet:
   - Wait for node label `sriovnetwork.openshift.io/state: Idle` (reuse existing label)
   - Same mechanism as device plugin `BlockDevicePluginUntilConfiguredFeatureGate`
5. Implement DRA driver pod restart logic when configuration changes:
   - Option A: Config daemon deletes DRA driver pod when starting configuration
   - Option B: DRA driver pod watches node label and restarts itself
   - Recommendation: Use Option A for consistency with device plugin
6. Handle owner references and lifecycle management for auto-generated filters
7. Add labels to distinguish auto-generated vs user-created `SriovResourceFilter` CRs (including node label)
8. Ensure proper cleanup when policies are deleted or when switching back to device plugin mode

**Phase 3: Integration & Testing**
1. Ensure required upstream PRs are merged:
   - Multus CNI DRA support ([multus-cni#1455](https://github.com/k8snetworkplumbingwg/multus-cni/pull/1455))
   - DRA driver Multus integration ([dra-driver-sriov#7](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/pull/7))
2. Add e2e tests for DRA mode
3. Test automatic `SriovResourceFilter` generation from policies
4. Test synchronization: verify DRA driver pod waits for node label before starting
5. Test configuration updates: verify DRA driver pod restart → reconfig → init container waits → driver starts flow
6. Test that init container properly waits for `sriovnetwork.openshift.io/state: Idle` label
7. Test interoperability with existing `SriovNetwork` CRs
8. Validate CNI integration works correctly with Multus DRA support
9. Test both kernel driver and VFIO modes
10. Test race condition scenarios (verify init container prevents premature scheduling)
11. Performance testing and optimization

**Phase 4: Documentation & Migration**
1. Document DRA mode configuration
2. Document automatic `SriovResourceFilter` generation behavior
3. Provide migration guide from device plugin to DRA
4. Add troubleshooting guide
5. Update quickstart guide with DRA examples

**Phase 5: Advanced Features**
1. Support for user-created `SriovResourceFilter` CRs that coexist with auto-generated ones
2. Integration with operator metrics and monitoring
3. Enhanced validation and defaulting for DRA configurations
4. Support for merging multiple policies into a single `SriovResourceFilter` CR where appropriate
5. (Optional) Extended resource allocation support (see dedicated section below)

#### Key Implementation Considerations

**1. Feature Gate Usage**
- The `SriovOperatorConfigReconciler` controller will check the feature gate using its `FeatureGate` interface:
  ```go
  if r.FeatureGate.IsEnabled(consts.DynamicResourceAllocationFeatureGate) {
      // Deploy DRA driver DaemonSet and related resources
      err = r.syncDRADriver(ctx, defaultConfig)
  } else {
      // Deploy device plugin DaemonSet (default behavior)
      err = r.syncPluginDaemonObjs(ctx, defaultConfig)
  }
  ```
- The feature gate is initialized from the `SriovOperatorConfig.Spec.FeatureGates` map during controller reconciliation
- Changes to the feature gate in the `SriovOperatorConfig` CR trigger reconciliation in the operator config controller
- The controller will also need to clean up the previously deployed resources when switching modes (remove device plugin when enabling DRA, remove DRA driver when disabling DRA)

**2. DRA Driver Integration and DeviceClass Management**

The operator will deploy the DRA driver similarly to how it deploys the device plugin, including all necessary resources.

**Basic DRA Driver Deployment:**
- Container image reference (configured via Helm)
- DaemonSet for kubelet plugin
- ServiceAccount and RBAC permissions
- **Basic DeviceClass** for the driver

**Basic DeviceClass:**
The operator should create the basic DeviceClass that the DRA driver helm chart defines:

```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: sriovnetwork.k8snetworkplumbingwg.io
  ownerReferences:
    - apiVersion: sriovnetwork.openshift.io/v1
      kind: SriovOperatorConfig
      name: default
spec:
  selectors:
  - cel: 
      expression: "device.driver == 'sriovnetwork.k8snetworkplumbingwg.io'"
```

This basic DeviceClass:
- Allows users to create ResourceClaims for any devices from the DRA driver
- Does NOT include extended resource mapping (that's Phase 6 optional)
- Matches all devices published by the DRA driver
- Is analogous to the device plugin DaemonSet and ConfigMap that are currently deployed

**Configuration:**
- The operator will read DRA driver configuration from environment variables (set via Helm)
- When deploying the DRA driver DaemonSet, the operator will pass configuration as:
  - Container image reference
  - Command-line arguments to the DRA driver
  - Environment variables in the DRA driver container spec
  - Volume mounts for CDI root and other paths

**2. CRD Management**
- The `SriovResourceFilter` CRD will be vendored from the DRA driver project into the operator's Helm chart
- Add the CRD file as `deployment/sriov-network-operator-chart/crds/sriovnetwork.k8snetworkplumbingwg.io_sriovresourcefilters.yaml`
- The CRD will always be installed with the operator (like `NetworkAttachmentDefinition` from Multus)
- This approach is simpler than conditional CRD installation and allows users to prepare configurations before enabling DRA
- Consider adding a script or documentation for updating the vendored CRD when the DRA driver project releases new versions

**3. Synchronization with Node Configuration**

This is a critical aspect of DRA integration. The DRA driver must not publish resources until the SR-IOV configuration is complete on each node.

**Problem Statement:**
- The SR-IOV config daemon applies configuration per-node (creates VFs, binds drivers, etc.)
- The DRA driver discovers VFs based on `SriovResourceFilter` CRs
- If the DRA driver runs before configuration is applied, it may:
  - Publish incomplete or incorrect resource information
  - Not find any VFs because they haven't been created yet
  - Cause pods to be scheduled prematurely

**Solution: Reuse Device Plugin Protection Mechanism**

Instead of managing `SriovResourceFilter` CR lifecycle, reuse the existing proven mechanism from device plugin mode (controlled by `BlockDevicePluginUntilConfiguredFeatureGate`):

**Approach:**
1. **Create `SriovResourceFilter` CRs once** (per-node, based on policies)
   - These CRs stay in place and don't get deleted/recreated
   - They define what resources should be discovered when the DRA driver runs

2. **Control DRA driver pod lifecycle** (same as device plugin)
   - When configuration changes, delete the DRA driver pod on affected nodes
   - Config daemon applies SR-IOV configuration
   - Config daemon updates node with completion status
   - DRA driver pod restarts with init container that waits for ready signal
   - Only after signal, DRA driver discovers VFs and publishes resources

**Implementation Details:**

**Init Container in DRA Driver DaemonSet:**
```yaml
initContainers:
- name: wait-for-config
  image: <operator-image>
  command:
  - /bin/sh
  - -c
  - |
    # Wait for sriov-config-daemon to complete configuration
    # Similar to device plugin init container
    while true; do
      if kubectl get node $NODE_NAME -o jsonpath='{.metadata.labels.sriovnetwork\.openshift\.io/state}' | grep -q "Idle"; then
        echo "Node configuration complete"
        exit 0
      fi
      echo "Waiting for node configuration..."
      sleep 5
    done
  env:
  - name: NODE_NAME
    valueFrom:
      fieldRef:
        fieldPath: spec.nodeName
```

**Workflow:**

1. **Initial Setup:**
   - Operator creates `SriovResourceFilter` CRs based on policies
   - DRA driver pods start with init container waiting for node label
   - Config daemon completes configuration and sets node label
   - Init container exits, DRA driver starts and discovers VFs

2. **Configuration Update:**
   - Config daemon detects policy change
   - Config daemon removes ready label from node
   - Operator or daemon deletes DRA driver pod (or pod detects label change and restarts)
   - Config daemon applies new configuration
   - Config daemon sets ready label when complete
   - DRA driver pod's init container waits for label
   - DRA driver discovers VFs with updated configuration

3. **Node Label:**
   - Use existing node label: `sriovnetwork.openshift.io/state: Idle`
   - Or create DRA-specific label if needed: `sriovnetwork.openshift.io/dra-ready: true`
   - Config daemon already manages these labels

**Implementation Details:**

```go
// Example: Generate per-node SriovResourceFilter
func generateSriovResourceFilterForNode(
    policies []*SriovNetworkNodePolicy,
    nodeState *SriovNetworkNodeState,
    node *corev1.Node) (*sriovdrav1alpha1.SriovResourceFilter, error) {
    
    // Note: SriovResourceFilter CRs are created regardless of configuration state
    // The DRA driver pod itself is blocked by init container until config is ready
    // This is simpler than managing CR lifecycle
    
    filter := &sriovdrav1alpha1.SriovResourceFilter{
        ObjectMeta: metav1.ObjectMeta{
            Name: fmt.Sprintf("node-%s", node.Name),
            Namespace: vars.Namespace,
            Labels: map[string]string{
                "sriovnetwork.openshift.io/generated-by": "sriov-network-operator",
                "sriovnetwork.openshift.io/node": node.Name,
            },
        },
        Spec: sriovdrav1alpha1.SriovResourceFilterSpec{
            NodeSelector: map[string]string{
                "kubernetes.io/hostname": node.Name,
            },
            Configs: []sriovdrav1alpha1.Config{},
        },
    }
    
    // Add configs from all policies applicable to this node
    for _, policy := range policies {
        if policy.Selected(node) {
            config := convertPolicyToFilterConfig(policy, nodeState)
            filter.Spec.Configs = append(filter.Spec.Configs, config)
        }
    }
    
    return filter, nil
}
```

**Naming Convention:**
- One `SriovResourceFilter` per node: `node-<nodename>` (e.g., `node-worker-1`)
- This allows clean lifecycle management tied to node state
- Uses node selector to target exactly one node

**State Machine:**

```
Initial State:
  1. Operator creates SriovResourceFilter CRs (based on policies, stay in place)
  2. DRA driver pod starts with init container
  3. Init container waits for node label (sriovnetwork.openshift.io/state: Idle)
  4. Config daemon completes initial configuration, sets label
  5. DRA driver starts, discovers VFs, publishes resources

When Policy Changes:
  1. Config daemon detects change (or operator triggers)
  2. Config daemon removes "Idle" label from node (or sets to "Drain_Required")
  3. DRA driver pod is deleted (by daemon or operator)
  4. Config daemon applies new SR-IOV configuration
  5. Config daemon sets "Idle" label when complete
  6. DRA driver pod restarts (managed by DaemonSet)
  7. Init container waits for "Idle" label
  8. Init container exits when label present
  9. DRA driver starts, discovers VFs with new configuration
 10. Operator updates SriovResourceFilter CRs if needed (only if resource names changed)
```

**Benefits:**
- DRA driver only sees VFs that are properly configured
- Prevents race conditions similar to device plugin issue
- Clean synchronization using Kubernetes-native state tracking
- Per-node granularity matches SR-IOV configuration model

**Comparison to Device Plugin Approach:**

| Aspect | Device Plugin | DRA (proposed) |
|--------|--------------|----------------|
| Signal mechanism | Init container + node label | Init container + node label (same!) |
| Granularity | Per-pod (device plugin pod) | Per-pod (DRA driver pod) |
| Blocking | Wait in init container for label | Wait in init container for label (same!) |
| State source | Node label (set by daemon) | Node label (set by daemon) (same!) |
| State consumer | Device plugin pod (waits) | DRA driver pod (waits) (same!) |
| Config resources | ConfigMap (per-node data) | SriovResourceFilter CRs (per-node CRs) |
| Config lifecycle | ConfigMap always present | SriovResourceFilter CRs always present |

**Key Benefits:**
- Reuses the exact same protection mechanism that already exists and is proven to work!
- Simpler: `SriovResourceFilter` CRs stay in place, no delete/recreate logic needed
- More robust: Pod lifecycle management is well-understood and battle-tested
- Consistent: Same user experience for both device plugin and DRA modes
- Easier to debug: Init container logs show exactly what the pod is waiting for

**4. Mode Switching and Resource Management**
- Switching between device plugin and DRA mode should be blocked if:
  - There are active pods using SR-IOV resources
  - There are existing ResourceClaims (when switching from DRA to device plugin)
- Validation webhook should enforce this
- When switching to DRA mode:
  - Operator stops updating `device-plugin-config` ConfigMap
  - Operator starts creating/updating per-node `SriovResourceFilter` CRs from policies (only for nodes with `SyncStatus == "Succeeded"`)
- When switching to device plugin mode:
  - Operator deletes all auto-generated `SriovResourceFilter` CRs
  - Operator resumes updating `device-plugin-config` ConfigMap

**4. Resource Naming**
- Device plugin uses format: `<prefix>/<resourceName>` (e.g., `openshift.io/intel-nic`)
- DRA uses `DeviceClass` and filtering expressions
- Need clear documentation on mapping between the two

**5. CNI Integration**
- Both modes use SR-IOV CNI plugin (unchanged)
- `NetworkAttachmentDefinition` generation remains the same
- DRA driver integrates with CNI via the `VfConfig` parameters
- **Requires:** Multus CNI with DRA support ([multus-cni#1455](https://github.com/k8snetworkplumbingwg/multus-cni/pull/1455))
- **Requires:** DRA driver with Multus integration ([dra-driver-sriov#7](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/pull/7))

**6. Metrics and Monitoring**
- DRA driver provides its own health check endpoints
- Operator should expose DRA driver metrics alongside existing metrics
- Consider adding metrics for DRA-specific events:
  - Number of `SriovResourceFilter` CRs managed
  - Per-node filter creation/deletion events
  - Time between config completion and filter creation
  - Resource claims and allocations

**7. RBAC Requirements**
- DRA driver requires RBAC permissions (similar to device plugin):
  - Access to `resourceslices.resource.k8s.io` API (for publishing devices)
  - Access to `resourceclaims.resource.k8s.io` API (for reading claims)
  - Read access to `deviceclasses.resource.k8s.io` API (to read the basic DeviceClass)
  - Read/List/Watch permissions for `SriovResourceFilter` CRD
- Operator requires RBAC permissions:
  - Create/Update/Delete `DeviceClass` resources (for basic DeviceClass)
  - Create/Update/Delete `SriovResourceFilter` CRs (for auto-generated filters)

**8. Node Configuration and Synchronization**
- Node configuration (VF creation, driver binding, etc.) remains unchanged
- The config daemon continues to work the same way in both modes:
  - Reads desired state from `SriovNetworkNodeState.Spec`
  - Applies SR-IOV configuration (creates VFs, binds drivers, etc.)
  - Updates node labels to reflect configuration state (e.g., `sriovnetwork.openshift.io/state: Idle`)
- Key difference in DRA mode: 
  - DRA driver DaemonSet includes init container (like device plugin)
  - Init container waits for node label to be "Idle" before allowing DRA driver to start
  - `SriovResourceFilter` CRs stay in place (don't need to be deleted/recreated)
  - When config changes, DRA driver pod is restarted, init container blocks until config complete
- Prevents the race condition where pods might be scheduled before VFs are ready
- **Advantage:** Reuses existing, proven synchronization mechanism from device plugin mode

#### Dependencies

- Kubernetes 1.34+ (stable DRA support)
- `dra-driver-sriov` container image
- Container runtime with CDI support (containerd, CRI-O)
- Container runtime with NRI support

**Required Upstream Changes for Multus Integration:**

1. **Multus CNI - DRA Support**
   - PR: [k8snetworkplumbingwg/multus-cni#1455](https://github.com/k8snetworkplumbingwg/multus-cni/pull/1455)
   - Description: Adds DRA support to Multus CNI for integration with DRA-allocated network devices
   - Status: Required for proper CNI integration with DRA driver

2. **DRA Driver SR-IOV - Multus Integration**
   - PR: [k8snetworkplumbingwg/dra-driver-sriov#7](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/pull/7)
   - Description: Enhances DRA driver to work seamlessly with Multus CNI
   - Status: Required for NetworkAttachmentDefinition integration

#### Constraints

1. **No Mixed Mode**: A cluster cannot run both device plugin and DRA driver simultaneously
2. **Kubernetes Version**: DRA mode requires Kubernetes 1.34 or later
3. **Runtime Requirements**: DRA driver requires CDI and NRI support in the container runtime
4. **Migration Complexity**: Migrating existing workloads from device plugin to DRA requires pod recreation
5. **Configuration Mapping**: Not all device plugin configurations map 1:1 to DRA configurations

### Upgrade & Downgrade Considerations

#### Upgrade Scenarios

**Upgrading Operator (Device Plugin Mode → Device Plugin Mode)**
- Standard upgrade path, no changes
- Device plugin continues to work as before

**Enabling DRA on Existing Cluster**
1. Ensure Kubernetes version is 1.34+
2. Drain nodes with SR-IOV workloads
3. Enable the feature gate in `SriovOperatorConfig`:
   ```yaml
   spec:
     featureGates:
       dynamicResourceAllocation: true
   ```
4. Operator will:
   - Remove device plugin DaemonSet
   - Deploy DRA driver DaemonSet
5. Recreate workloads using ResourceClaims instead of device plugin resources
6. Optionally create `SriovResourceFilter` CRs for advanced filtering (CRD is already installed)

**Upgrading Operator (DRA Mode → DRA Mode)**
- DRA driver image may be updated
- Existing ResourceClaims continue to work
- `SriovResourceFilter` CRs are preserved

#### Downgrade Scenarios

**Disabling DRA on Existing Cluster**
1. Drain nodes with SR-IOV workloads using DRA
2. Delete all ResourceClaims
3. Disable the feature gate in `SriovOperatorConfig`:
   ```yaml
   spec:
     featureGates:
       dynamicResourceAllocation: false
   ```
4. Operator will:
   - Remove DRA driver DaemonSet
   - Deploy device plugin DaemonSet
   - Note: `SriovResourceFilter` CRD remains installed (users should manually delete CRs if desired)
5. Recreate workloads using device plugin resource requests

**Rolling Back Operator Version**
- If rolling back to a version that doesn't support the DRA feature gate:
  - Must first disable DRA mode (`dynamicResourceAllocation: false`)
  - Wait for device plugin to be deployed
  - Then rollback operator version
- Validation should prevent rollback if DRA feature gate is enabled

#### Safety Measures

1. **Validation Webhook**: Prevent feature gate switching if active workloads exist
2. **Status Reporting**: Add status fields to `SriovOperatorConfig` to show current mode and any transition errors
3. **Feature Gate Consistency**: Ensure feature gate changes are properly synchronized across operator components
4. **Documentation**: Clear migration documentation with step-by-step instructions
5. **Backup Recommendations**: Recommend backing up ResourceClaim definitions before disabling DRA

### Extended Resource Allocation Support (Optional Enhancement)

#### Overview

Kubernetes 1.34 introduces an alpha feature called **Extended Resource Allocation by DRA** (controlled by the `DRAExtendedResource` feature gate). This feature allows `DeviceClass` resources to specify an `extendedResourceName`, enabling pods to request DRA-managed devices using traditional extended resource syntax instead of `ResourceClaim` objects.

**Key Benefits:**
- **Backward compatibility**: Existing pod specs using `resources.limits` can work with DRA without modification
- **Seamless migration**: Users can switch from device plugin to DRA without rewriting pod specifications
- **Coexistence**: Same extended resource name can be provided by device plugin on some nodes and DRA on others

#### Kubernetes Feature Details

**Feature State:** Kubernetes v1.34 [alpha] (disabled by default)

**Requirements:**
- Enable `DRAExtendedResource` feature gate in:
  - kube-apiserver
  - kube-scheduler
  - kubelet

**Two Usage Patterns:**

1. **Explicit Extended Resource Name in DeviceClass:**
```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: intel-sriov-nic
spec:
  selectors:
  - cel:
      expression: device.driver == 'sriov.k8snetworkplumbingwg.io'
  extendedResourceName: openshift.io/intel_nic  # Maps to device plugin resource name
```

Pods can then request:
```yaml
resources:
  limits:
    openshift.io/intel_nic: 2  # Works like device plugin!
```

2. **Implicit Extended Resource with Special Prefix:**
```yaml
resources:
  limits:
    deviceclass.resource.kubernetes.io/intel-sriov-nic: 2  # Uses DeviceClass name
```

#### Proposed Integration with SR-IOV Network Operator

**Note:** This is separate from the basic DeviceClass created in Phase 1. The basic DeviceClass (`sriovnetwork.k8snetworkplumbingwg.io`) enables ResourceClaim usage. This section describes optional per-resourceName DeviceClasses with extended resource mapping.

**Automatic Per-ResourceName DeviceClass Generation**

When extended resource allocation support is enabled (Phase 6), the operator should automatically create additional `DeviceClass` resources for each unique `resourceName` in `SriovNetworkNodePolicy` CRs, similar to how it generates `SriovResourceFilter` CRs.

**Mapping:**

| SriovNetworkNodePolicy | DeviceClass |
|------------------------|-------------|
| `Spec.ResourceName` | `metadata.name` and `spec.extendedResourceName` |
| Policy filtering criteria | `spec.selectors` CEL expressions |

**Example DeviceClass Generation:**

From this policy:
```yaml
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovNetworkNodePolicy
metadata:
  name: policy-intel-nic
spec:
  resourceName: intel_nic
  nodeSelector:
    feature.node.kubernetes.io/network-sriov.capable: "true"
  nicSelector:
    vendor: "8086"
    deviceID: "1572"
    pfNames: ["ens1f0"]
  numVfs: 8
```

Generate this DeviceClass:
```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: intel-nic  # Derived from resourceName
  labels:
    sriovnetwork.openshift.io/generated-by: sriov-network-operator
    sriovnetwork.openshift.io/resource-name: intel_nic
  ownerReferences:
    - apiVersion: sriovnetwork.openshift.io/v1
      kind: SriovOperatorConfig
      name: default
spec:
  # Allow pods to use traditional device plugin resource request syntax
  extendedResourceName: openshift.io/intel_nic
  selectors:
  - cel:
      # Match devices from DRA driver with the corresponding resourceName
      expression: |
        device.driver == "sriov.k8snetworkplumbingwg.io" &&
        device.attributes["sriovnetwork.k8snetworkplumbingwg.io"].resourceName == "intel_nic"
```

**Pod Compatibility:**

With this DeviceClass, existing pod specs continue to work:
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: sriov-pod
spec:
  containers:
  - name: app
    image: myapp:latest
    resources:
      limits:
        openshift.io/intel_nic: 2  # Same as device plugin!
      requests:
        openshift.io/intel_nic: 2
    # NetworkAttachmentDefinition still works the same way
  annotations:
    k8s.v1.cni.cncf.io/networks: sriov-network
```

#### Implementation Considerations

**1. Feature Gate Coordination**

Add a sub-feature gate for extended resource support:
```yaml
spec:
  featureGates:
    dynamicResourceAllocation: true
    draExtendedResourceAllocation: false  # Optional, requires DRAExtendedResource in K8s
```

**2. DeviceClass Lifecycle Management**

Two types of DeviceClass:

**A. Basic DeviceClass (Phase 1):**
- Name: `sriovnetwork.k8snetworkplumbingwg.io` (fixed)
- Created when DRA feature gate is enabled
- Deleted when DRA feature gate is disabled
- No extended resource mapping
- Allows users to create ResourceClaims for any SR-IOV devices

**B. Per-ResourceName DeviceClasses (Phase 6 - Optional):**
- One `DeviceClass` per unique `resourceName` across all policies
- Created/updated when policies change (if extended resource support is enabled)
- Deleted when no policies use that `resourceName` anymore
- Includes extended resource mapping (`extendedResourceName`)
- Naming: convert `resourceName` to valid DeviceClass name (e.g., `intel_nic` → `intel-nic`)

**3. CEL Expression Generation**

The operator needs to generate CEL expressions that match the DRA driver's device attributes:
```go
func generateDeviceClassSelector(resourceName string) string {
    return fmt.Sprintf(
        `device.driver == "sriov.k8snetworkplumbingwg.io" && ` +
        `device.attributes["sriovnetwork.k8snetworkplumbingwg.io"].resourceName == "%s"`,
        resourceName,
    )
}
```

**4. Conflict Prevention**

- Validate that `extendedResourceName` doesn't conflict with actual device plugin resources on mixed-mode clusters
- Add status field to indicate if DeviceClass creation succeeded or failed due to conflicts

**5. Documentation and Migration**

Document the migration path:
- **Zero-downtime migration**: Enable DRA with extended resource support, no pod spec changes needed
- **Gradual rollout**: Can enable on subset of nodes while keeping device plugin on others
- **Rollback safety**: Disable DRA feature gate and pods continue working with device plugin

#### Advantages

1. **Seamless Migration**: Users enable DRA without touching pod specifications
2. **Minimal Disruption**: Existing workloads continue running unchanged
3. **Gradual Adoption**: Can test DRA on some nodes while keeping device plugin on others
4. **Future-Proof**: Prepares for eventual device plugin deprecation without breaking changes

#### Limitations and Caveats

1. **Alpha Feature**: Requires enabling `DRAExtendedResource` feature gate (K8s 1.34+)
2. **No Advanced Features**: Pods using extended resource syntax don't benefit from DRA's advanced features:
   - Cannot specify complex device selection criteria
   - Cannot use multiple device classes in one claim
   - Limited to simple count-based allocation
3. **Operator Complexity**: Requires managing both `SriovResourceFilter` and `DeviceClass` resources
4. **CEL Expression Maintenance**: Must ensure CEL expressions stay in sync with DRA driver behavior

#### Future Enhancements

1. **Automatic Detection**: Detect if `DRAExtendedResource` feature gate is enabled in cluster
2. **Hybrid Mode**: Support both extended resource and ResourceClaim patterns simultaneously
3. **Migration Tool**: Provide tool to convert pods from extended resource to ResourceClaim when users want advanced features
4. **Metrics**: Track usage of extended resource vs ResourceClaim patterns

#### Recommendation

**Phase 1-4**: Implement core DRA functionality with `ResourceClaim` approach

**Phase 6 (Optional)**: Add extended resource allocation support if:
- User feedback indicates strong demand for backward compatibility
- Kubernetes stabilizes the `DRAExtendedResource` feature (moves to beta/stable)
- Migration challenges are significant enough to warrant this complexity

This feature should be considered an optional enhancement rather than a core requirement for the initial DRA integration.

---

### Test Plan

#### Unit Tests

1. Feature gate tests:
   - Test default state (DRA disabled)
   - Test enabling/disabling the feature gate
   - Test feature gate initialization from `SriovOperatorConfig`
2. Controller logic tests for DRA driver deployment when feature gate is enabled
3. Controller logic tests for device plugin deployment when feature gate is disabled
4. Mode switching validation tests
5. Per-node `SriovResourceFilter` generation tests:
   - Test filter is created based on policies (stays in place)
   - Test filter is updated when policies change (not deleted/recreated)
   - Test per-node naming and nodeSelector generation
6. DRA driver pod protection tests:
   - Test init container blocks DRA driver start when node label not "Idle"
   - Test DRA driver starts after node label changes to "Idle"
   - Test DRA driver pod restart on configuration changes
   - Test init container behavior matches device plugin init container
6. Configuration generation tests for DRA driver with various environment variable configurations
7. Tests for reading DRA driver configuration from environment variables

#### Integration Tests

1. Deploy operator (verify `SriovResourceFilter` CRD is installed via Helm)
2. Create `SriovNetworkNodePolicy` in device plugin mode (verify `device-plugin-config` ConfigMap is created)
3. Enable DRA feature gate
4. Verify DRA driver DaemonSet is deployed (device plugin should not be deployed)
5. Verify basic DeviceClass (`sriovnetwork.k8snetworkplumbingwg.io`) is created
6. Verify operator automatically generates per-node `SriovResourceFilter` CRs from existing policies
7. Verify filters are only created for nodes where the config daemon has set `SriovNetworkNodeState.Status.SyncStatus == "Succeeded"`
8. Verify generated filters have correct owner references, labels, and per-node naming (`node-<nodename>`)
9. Verify each filter has a nodeSelector targeting exactly one node
10. Verify DRA driver discovers and reports VFs based on generated filters
11. Create ResourceClaim referencing the basic DeviceClass
12. Create pod with ResourceClaim and verify VF allocation
11. Test CNI integration with DRA-allocated VFs
12. Test both kernel driver and VFIO modes
13. Update policy and verify:
    - Corresponding `SriovResourceFilter` CRs are deleted
    - Config daemon applies new configuration
    - Filters are recreated after `SyncStatus == "Succeeded"`
14. Delete policy and verify corresponding `SriovResourceFilter` CRs are deleted
15. Test feature gate switching (device plugin ↔ DRA)
16. Test custom DRA driver configuration via Helm values (image, interface prefix, etc.)
17. Test user-created `SriovResourceFilter` CRs alongside auto-generated ones

#### E2E Tests

1. **Basic DRA Workflow**
   - Deploy operator with DRA enabled
   - Verify basic DeviceClass (`sriovnetwork.k8snetworkplumbingwg.io`) is created
   - Create `SriovNetworkNodePolicy` with `resourceName: intel_nic`
   - Create `SriovNetwork` CR (generates NetworkAttachmentDefinition)
   - Verify operator auto-generates corresponding `SriovResourceFilter` CR
   - Verify DRA driver DaemonSet has init container waiting for node label
   - Wait for config daemon to complete and set node label to "Idle"
   - Verify DRA driver starts and discovers VFs
   - Create ResourceClaimTemplate
   - Deploy pod using ResourceClaim with network annotation
   - Verify network connectivity
   - Delete pod and verify VF cleanup

2. **Automatic Filter Generation & Synchronization**
   - Create `SriovNetworkNodePolicy` CR
   - Verify operator creates `SriovResourceFilter` CRs (stay in place)
   - Verify one filter per node with correct nodeSelector
   - Verify DRA driver pod has init container that blocks until node label is "Idle"
   - Monitor config daemon applying configuration and setting node label
   - Verify DRA driver starts only after label is set
   - Update a policy and verify:
     - Filters are updated (not deleted/recreated)
     - DRA driver pod is restarted
     - Init container blocks until config complete
     - DRA driver discovers updated configuration
   - Delete a policy and verify corresponding filters are deleted
   - Verify filters have proper owner references and labels

3. **Advanced Filtering**
   - Create user-managed `SriovResourceFilter` CRs with advanced criteria
   - Verify they coexist with auto-generated filters
   - Deploy pods requesting specific resource types
   - Verify correct VF allocation based on filters

3. **Migration Scenario**
   - Start with device plugin mode
   - Migrate to DRA mode
   - Verify workloads can be recreated successfully

4. **Multi-VF Scenario**
   - Request multiple VFs in a single ResourceClaim
   - Verify all VFs are allocated and configured correctly

5. **VFIO/DPDK Scenario**
   - Create ResourceClaim with VFIO driver configuration
   - Deploy DPDK workload
   - Verify vhost-user socket mounting works

6. **Negative Tests**
   - Attempt to switch modes with active workloads (should fail)
   - Deploy DRA mode on Kubernetes < 1.34 (should fail)
   - Request resources that don't match any filters (should fail)

#### Performance Tests

1. Measure allocation latency (DRA vs device plugin)
2. Test scalability with large numbers of VFs and ResourceClaims
3. Measure operator overhead when managing DRA driver

#### Upgrade/Downgrade Tests

1. Test operator upgrade with DRA mode enabled
2. Test enabling DRA on existing cluster
3. Test disabling DRA and reverting to device plugin
4. Test operator rollback scenarios

---

## Open Questions

1. **Feature Gate Naming**: Should the feature gate be named `dynamicResourceAllocation` or something shorter like `DRA`?
   - Current proposal: `dynamicResourceAllocation` (more descriptive, follows Kubernetes convention)
   - Alternative: `enableDRA` (shorter but less clear)
   - Need to align with Kubernetes feature gate naming conventions

2. **Should we support gradual migration** where some nodes use device plugin and others use DRA?
   - Likely too complex for initial implementation
   - Could be considered for future enhancement

3. **How should we handle the resourceName semantic difference** between device plugin and DRA modes?
   - Document clearly that they serve different purposes
   - Consider adding a new field specifically for DRA resource names

4. ~~**Should SriovResourceFilter creation be automatic** based on SriovNetworkNodePolicy?~~
   - **Decision: Yes, auto-generate from policies** (similar to device-plugin-config ConfigMap)
   - Simplifies migration and provides consistent user experience
   - Users can still create additional manual `SriovResourceFilter` CRs for advanced scenarios

5. **What's the recommended image versioning strategy** for dra-driver-sriov?
   - Pin to specific versions in operator releases
   - Allow override via configuration

6. **Should we add a migration tool** to convert device plugin configurations to DRA?
   - Nice to have but not essential for initial implementation
   - Document manual migration process first

7. **Feature Gate Stability**: Should the feature gate start as alpha, beta, or go directly to stable?
   - Recommendation: Start as alpha (disabled by default) for initial releases
   - Move to beta (consider enabling by default) after proving stability
   - Eventually move to stable and deprecate device plugin mode

8. **Extended Resource Allocation**: Should we implement extended resource allocation support in the initial release?
   - **Decision: Defer to Phase 6 (optional enhancement)**
   - Reasons:
     - It's a K8s alpha feature that may change
     - Adds significant complexity (managing DeviceClass resources, CEL expressions)
     - ResourceClaim approach is the "proper" DRA way and should be proven first
     - Can be added later if migration challenges warrant it
   - Re-evaluate based on:
     - User feedback during initial DRA adoption
     - Kubernetes feature stability (alpha → beta → stable)
     - Migration pain points observed in the field
