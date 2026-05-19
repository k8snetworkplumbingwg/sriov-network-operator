---
title: DRA (Dynamic Resource Allocation) Integration
authors:
  - rollandf
reviewers:
  - SchSeba
  - adrianchiris
creation-date: 11-02-2026
last-updated: 18-03-2026
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

5. **No Cross-Driver Resource Matching**: Device plugins operate in isolation and cannot coordinate resource allocation across different device types. DRA enables matching properties between resources from different DRA drivers, allowing workloads to request optimally aligned hardware. For example, a pod can request both a GPU and a SR-IOV VF that share the same PCIe root complex (matched by `pcieRoot` attribute) to minimize latency and maximize data transfer efficiency—critical for high-performance computing, AI/ML workloads, and network-intensive applications.

6. **Kubernetes Evolution**: DRA is the future direction for device resource management in Kubernetes (stable in 1.34+), and the device plugin framework may eventually be deprecated.

The DRA driver for SR-IOV (`dra-driver-sriov`) provides:
- Opt-in device advertisement via `SriovResourcePolicy` CRDs (devices only advertised when matching a policy)
- Flexible attribute assignment via `DeviceAttributes` CRDs (decoupled from device selection)
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
- Enable opt-in device advertisement via `SriovResourcePolicy` and attribute assignment via `DeviceAttributes` CRDs when in DRA mode
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

**2. Per-ResourceName DeviceClasses (implemented when DRA is enabled):**
- One DeviceClass per unique `SriovNetworkNodePolicy.Spec.ResourceName`
- Each has `extendedResourceName` set to `ResourcePrefix/resourceName` (same as device plugin extended resource name)
- CEL selector matches devices with that resourceName attribute (set via DeviceAttributes)
- When the Kubernetes `DRAExtendedResource` feature gate is enabled on the cluster, pods can request these via `resources.limits`; otherwise users request via ResourceClaimTemplate and the basic DeviceClass

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
5. **[NEW]** Operator automatically generates `SriovResourcePolicy` and `DeviceAttributes` CRs from `SriovNetworkNodePolicy` specs (one per node)
6. **[NEW]** DRA driver discovers VFs, advertises only devices matching policies, and applies attributes from `DeviceAttributes`
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
            # Select devices with specific resourceName attribute (applied via DeviceAttributes)
            expression: device.attributes["k8s.cni.cncf.io"].resourceName == "intel_nic"
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
- Operator auto-generates `SriovResourcePolicy` and `DeviceAttributes` CRs from policies
- DRA driver only advertises devices that match a `SriovResourcePolicy` (opt-in model)
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

Helm values (as implemented):
```yaml
# values.yaml
images:
  sriovDevicePlugin: <existing device plugin image>
  sriovDraDriver: ghcr.io/k8snetworkplumbingwg/dra-driver-sriov:latest  # DRA driver image

draDriver:
  cdiRoot: "/var/run/cdi"
  defaultInterfacePrefix: "net"
```

The operator passes the DRA driver image and these settings into the DRA driver DaemonSet (image, env vars, etc.). Resource policies and DeviceAttributes are created in the operator's namespace (`sriov-network-operator`); no separate `resourcePolicyNamespace` value.

#### New CRDs: SriovResourcePolicy and DeviceAttributes

The DRA driver project uses a **two-CRD architecture** that decouples device selection from attribute assignment:

- **`SriovResourcePolicy`**: Selects which devices to advertise based on node + NIC selectors. Devices are only advertised when they match a policy (opt-in model). Optionally references `DeviceAttributes` via a label selector.
- **`DeviceAttributes`**: Defines arbitrary key/value attributes (including `resourceName`) applied to devices matched by policies. Linked to policies via labels.

Both CRDs from the DRA driver project will be included in the operator's Helm chart under `deployment/sriov-network-operator-chart/crds/`, similar to how the `NetworkAttachmentDefinition` CRD from Multus is currently included (`k8s.cni.cncf.io_networkattachmentdefinitions_crd.yaml`).

The CRDs will be named:
- `sriovnetwork.k8snetworkplumbingwg.io_sriovresourcepolicies.yaml`
- `sriovnetwork.k8snetworkplumbingwg.io_deviceattributes.yaml`

These CRDs are already defined in `dra-driver-sriov` and will be copied/vendored from the DRA driver project into the operator's Helm chart.

**CRD Schema Summary:**

- **SriovResourcePolicy**: `spec.nodeSelector` (Kubernetes `corev1.NodeSelector`: `nodeSelectorTerms` / `matchExpressions`), `spec.configs[]` with `deviceAttributesSelector` (label selector) and `resourceFilters[]`. Each `ResourceFilter` supports: `vendors`, `devices`, `pfNames`, `pfPciAddresses`, `pciAddresses`, `drivers` (all string arrays). Note: there is no `rootDevices` field; policy `rootDevices` (PF PCI addresses) are mapped to `pfPciAddresses` by the operator. The operator generates a single-term selector matching `kubernetes.io/hostname` `In` `[<node name>]`.

- **DeviceAttributes**: `spec.attributes` is a map of qualified attribute name (e.g. `k8s.cni.cncf.io/resourceName`) to a value. Values use the Kubernetes resource API shape: exactly one of `bool`, `int`, `string`, or `version` (CRD schema keys are lowercase). The operator uses `resourceapi.QualifiedName` and `resourceapi.DeviceAttribute` from `k8s.io/api/resource/v1` when building these CRs.

**Key behavioral change:** A device matched by a `SriovResourcePolicy` is advertised **regardless** of whether any `DeviceAttributes` are attached. Attributes are purely additive metadata. If no policies exist, zero devices are advertised.

**Automatic Generation from SriovNetworkNodePolicy**

The operator will automatically generate `SriovResourcePolicy` and `DeviceAttributes` CRs from `SriovNetworkNodePolicy` CRs when the DRA feature gate is enabled. This is analogous to how the operator currently generates the `device-plugin-config` ConfigMap from policies in device plugin mode.

Current behavior (device plugin mode):
- `SriovNetworkNodePolicyReconciler.syncDevicePluginConfigMap()` reads all policies
- For each node, it calls `renderDevicePluginConfigData()` to convert policy specs into device plugin resource configs
- Generates a ConfigMap with per-node JSON configuration

New behavior (DRA mode):
- `SriovNetworkNodePolicyReconciler` will have new methods `syncSriovResourcePolicies()` and `syncDeviceAttributes()`
- For each unique `resourceName`, generate a `DeviceAttributes` CR with the `resourceName` attribute and a label
- For each node, generate a `SriovResourcePolicy` CR with device selection filters and a `deviceAttributesSelector` pointing to the corresponding `DeviceAttributes`
- Map `SriovNetworkNodePolicy` fields to `SriovResourcePolicy` + `DeviceAttributes` fields:

| SriovNetworkNodePolicy | SriovResourcePolicy | DeviceAttributes |
|------------------------|---------------------|------------------|
| `Spec.ResourceName` | `Spec.Configs[].DeviceAttributesSelector` (label ref) | `Spec.Attributes["k8s.cni.cncf.io/resourceName"]` (extended resource name: `ResourcePrefix/resourceName`) |
| `Spec.NicSelector.Vendor` | `Spec.Configs[].ResourceFilters[].Vendors` | - |
| `Spec.NicSelector.DeviceID` | `Spec.Configs[].ResourceFilters[].Devices` (VF device ID) | - |
| `Spec.NicSelector.PfNames` | `Spec.Configs[].ResourceFilters[].PfNames` | - |
| `Spec.NicSelector.RootDevices` | `Spec.Configs[].ResourceFilters[].PfPciAddresses` | - |
| `Spec.NodeSelector` (map label selector on policy) | `Spec.NodeSelector` (`corev1.NodeSelector` on generated CR; operator pins one node via `kubernetes.io/hostname` `In`) | - |
| Device driver (vfio-pci) | `Spec.Configs[].ResourceFilters[].Drivers` | - |

Example auto-generated `DeviceAttributes` (attribute value is the extended resource name, e.g. `ResourcePrefix/resourceName` or plain `resourceName` if no prefix):
```yaml
apiVersion: sriovnetwork.k8snetworkplumbingwg.io/v1alpha1
kind: DeviceAttributes
metadata:
  name: intel-nic-attrs  # resourceNameToDeviceClassName(resourceName) + "-attrs"
  namespace: sriov-network-operator
  labels:
    sriovnetwork.openshift.io/generated-by: sriov-network-operator
    sriovnetwork.openshift.io/resource-pool: intel-nic
spec:
  attributes:
    k8s.cni.cncf.io/resourceName:
      string: "openshift.io/intel_nic"   # extended resource name (ResourcePrefix/resourceName)
```

Example auto-generated `SriovResourcePolicy` (policy `rootDevices` are mapped to `pfPciAddresses`; no `rootDevices` field in the CRD):
```yaml
apiVersion: sriovnetwork.k8snetworkplumbingwg.io/v1alpha1
kind: SriovResourcePolicy
metadata:
  name: worker-1  # Same metadata.name as SriovNetworkNodeState / Node
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
    nodeSelectorTerms:
    - matchExpressions:
      - key: kubernetes.io/hostname
        operator: In
        values:
        - worker-1
  configs:
  - deviceAttributesSelector:
      matchLabels:
        sriovnetwork.openshift.io/resource-pool: intel-nic
    resourceFilters:
    - vendors: ["8086"]       # From policy's nicSelector.vendor
      devices: ["154c"]       # From policy's nicSelector.deviceID (VF device ID)
      pfNames: ["ens1f0"]     # From policy's nicSelector.pfNames
      pfPciAddresses: ["0000:08:00.0"]  # From policy's nicSelector.rootDevices (PF PCI addresses)
  # If multiple policies apply to worker-1, each gets its own config entry
```

**Note**: The CRDs will always be installed as part of the operator's Helm chart, regardless of whether the DRA feature gate is enabled. However, the operator will only create/manage `SriovResourcePolicy` and `DeviceAttributes` CRs when the DRA feature gate is enabled.

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

**Note:** The DRA driver will only advertise VFs that match `SriovResourcePolicy` CRs created by the operator, and will apply attributes from corresponding `DeviceAttributes` CRs (including `resourceName`). Users don't need to specify CEL selectors for basic usage - the device selection and attribute assignment are handled by the `SriovResourcePolicy` and `DeviceAttributes` CRs generated from `SriovNetworkNodePolicy` resources.

#### Changes to SriovNetwork

`SriovNetwork` and `SriovIBNetwork` CRs will continue to work in DRA mode, generating `NetworkAttachmentDefinition` resources. However, the `resourceName` field will have different semantics:

- **Device Plugin Mode**: `resourceName` maps to device plugin resource (e.g., `openshift.io/sriov-nic`)
- **DRA Mode**: `resourceName` is mapped to a `DeviceAttributes` CR and linked via `SriovResourcePolicy` to create resource pools

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
3. Vendor `SriovResourcePolicy` and `DeviceAttributes` CRDs from DRA driver project into Helm chart (`crds/` directory)
4. Import `SriovResourcePolicy` and `DeviceAttributes` types from DRA driver project into operator (for CR creation)
5. Add DRA driver configuration environment variables to operator deployment
6. Update Helm chart with DRA driver image and configuration values
7. Create DRA driver deployment manifests in `bindata/manifests/dra-driver/`:
   - DaemonSet for kubelet plugin
   - ServiceAccount
   - RBAC (ClusterRole, ClusterRoleBinding)
   - Basic DeviceClass (`sriovnetwork.k8snetworkplumbingwg.io`)
8. Add logic to operator to deploy DRA driver OR device plugin based on feature gate
9. Add validation to prevent switching modes on a running cluster with active workloads

**Phase 2: Policy to SriovResourcePolicy/DeviceAttributes Conversion & DRA Driver Protection**
1. Add `syncSriovResourcePolicies()` and `syncDeviceAttributes()` methods to `SriovNetworkNodePolicyReconciler`
2. Implement `DeviceAttributes` generation logic:
   - For each unique `resourceName` across all policies, create a `DeviceAttributes` CR
   - The `DeviceAttributes` CR contains the `resourceName` as a string attribute
   - Label each `DeviceAttributes` CR with a pool identifier (e.g., `sriovnetwork.openshift.io/resource-pool: <name>`)
3. Implement per-node policy generation logic:
   - Create `SriovResourcePolicy` CRs based on policies (stay in place)
   - Name policies with the same metadata.name as the Node / SriovNetworkNodeState
   - Each config entry uses `deviceAttributesSelector` to reference the corresponding `DeviceAttributes` by label
   - Update policies when node policies change (no deletion, just update)
4. Implement `renderSriovResourcePolicyForNode()` to convert policy specs to `SriovResourcePolicy` spec for a specific node
5. Add init container to DRA driver DaemonSet:
   - Run `sriov-network-config-daemon wait-for-config` (sets pod annotation; config daemon removes it when config is done)
   - Same mechanism as device plugin `BlockDevicePluginUntilConfiguredFeatureGate`
6. Implement DRA driver pod restart logic when configuration changes (config daemon restarts DRA driver pod before applying config, same as device plugin).
7. Handle owner references and lifecycle management for auto-generated `SriovResourcePolicy` and `DeviceAttributes` CRs
8. Add labels to distinguish auto-generated vs user-created CRs (including node label for policies)
9. Ensure proper cleanup when policies are deleted or when switching back to device plugin mode

**Phase 3: Integration & Testing**
1. Ensure required upstream PRs are merged:
   - Multus CNI DRA support ([multus-cni#1455](https://github.com/k8snetworkplumbingwg/multus-cni/pull/1455))
   - DRA driver Multus integration ([dra-driver-sriov#7](https://github.com/k8snetworkplumbingwg/dra-driver-sriov/pull/7))
2. Add e2e tests for DRA mode
3. Test automatic `SriovResourcePolicy` and `DeviceAttributes` generation from policies
4. Test synchronization: verify DRA driver pod init waits for config daemon to remove wait-for-config annotation before starting
5. Test configuration updates: verify DRA driver pod restart → reconfig → init container waits → driver starts flow
6. Test that init container properly blocks until config daemon removes the pod annotation
7. Test interoperability with existing `SriovNetwork` CRs
8. Validate CNI integration works correctly with Multus DRA support
9. Test both kernel driver and VFIO modes
10. Test race condition scenarios (verify init container prevents premature scheduling)
11. Performance testing and optimization

**Phase 4: Documentation & Migration**
1. Document DRA mode configuration
2. Document automatic `SriovResourcePolicy` and `DeviceAttributes` generation behavior
3. Provide migration guide from device plugin to DRA
4. Add troubleshooting guide
5. Update quickstart guide with DRA examples

**Phase 5: Advanced Features**
1. Support for user-created `SriovResourcePolicy` and `DeviceAttributes` CRs that coexist with auto-generated ones
2. Integration with operator metrics and monitoring
3. Enhanced validation and defaulting for DRA configurations
4. Support for merging multiple policies into a single `SriovResourcePolicy` CR where appropriate
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
- The `SriovResourcePolicy` and `DeviceAttributes` CRDs will be vendored from the DRA driver project into the operator's Helm chart
- Add the CRD files as:
  - `deployment/sriov-network-operator-chart/crds/sriovnetwork.k8snetworkplumbingwg.io_sriovresourcepolicies.yaml`
  - `deployment/sriov-network-operator-chart/crds/sriovnetwork.k8snetworkplumbingwg.io_deviceattributes.yaml`
- The CRDs will always be installed with the operator (like `NetworkAttachmentDefinition` from Multus)
- This approach is simpler than conditional CRD installation and allows users to prepare configurations before enabling DRA
- Consider adding a script or documentation for updating the vendored CRDs when the DRA driver project releases new versions

**3. Synchronization with Node Configuration**

This is a critical aspect of DRA integration. The DRA driver must not publish resources until the SR-IOV configuration is complete on each node.

**Problem Statement:**
- The SR-IOV config daemon applies configuration per-node (creates VFs, binds drivers, etc.)
- The DRA driver discovers VFs and advertises only those matching `SriovResourcePolicy` CRs, applying attributes from `DeviceAttributes` CRs
- If the DRA driver runs before configuration is applied, it may:
  - Publish incomplete or incorrect resource information
  - Not find any VFs because they haven't been created yet
  - Cause pods to be scheduled prematurely

**Solution: Reuse Device Plugin Protection Mechanism**

Instead of managing `SriovResourcePolicy`/`DeviceAttributes` CR lifecycle, reuse the existing proven mechanism from device plugin mode (controlled by `BlockDevicePluginUntilConfiguredFeatureGate`):

**Approach:**
1. **Create `SriovResourcePolicy` and `DeviceAttributes` CRs once** (policies per-node, attributes per-resourceName, based on node policies)
   - These CRs stay in place and don't get deleted/recreated
   - They define what resources should be discovered and how they should be attributed when the DRA driver runs

2. **Control DRA driver pod lifecycle** (same as device plugin)
   - When configuration changes, delete the DRA driver pod on affected nodes
   - Config daemon applies SR-IOV configuration
   - Config daemon updates node with completion status
   - DRA driver pod restarts with init container that waits for ready signal
   - Only after signal, DRA driver discovers VFs and publishes resources

**Implementation Details:**

**Init Container in DRA Driver DaemonSet (same pattern as device plugin):**

The init container runs the config daemon's `wait-for-config` command (no kubectl, no node label). The init container sets a pod annotation (`sriovnetwork.openshift.io/device-plugin-wait-config`) on the DRA driver pod; the config daemon removes this annotation after SR-IOV configuration is applied on the node. Once the annotation is removed, the init container exits and the DRA driver starts.

```yaml
initContainers:
- name: sriov-dra-driver-init
  image: <config-daemon-image>
  command:
  - sriov-network-config-daemon
  - wait-for-config
  - --pod-name=$(POD_NAME)
  - --pod-namespace=$(POD_NAMESPACE)
  env:
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: POD_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
```

**Workflow:**

1. **Initial Setup:**
   - Operator creates `DeviceAttributes` CRs (one per unique `resourceName`) and `SriovResourcePolicy` CRs (one per node) based on policies
   - DRA driver pods start; init container sets pod annotation `sriovnetwork.openshift.io/device-plugin-wait-config` and blocks
   - Config daemon completes configuration and removes that annotation from the DRA driver pod
   - Init container exits, DRA driver starts and discovers VFs

2. **Configuration Update:**
   - Config daemon detects policy change and restarts DRA driver pod (or operator/daemon triggers restart)
   - New DRA driver pod starts with init container setting the wait-for-config annotation and blocking
   - Config daemon applies new configuration, then removes the annotation
   - Init container exits, DRA driver discovers VFs with updated configuration

3. **Signal:**
   - Pod annotation `sriovnetwork.openshift.io/device-plugin-wait-config`: init container sets it; config daemon removes it when configuration is done (same as device plugin). No node label or kubectl in the wait path.

**Implementation Details:**

```go
// Example: Generate DeviceAttributes for a resourceName (operator uses resourceapi.QualifiedName and resourceapi.DeviceAttribute).
// Attribute value is the extended resource name (buildExtendedResourceName(resourceName)), e.g. ResourcePrefix/resourceName.
func generateDeviceAttributes(resourceName string) *sriovdrav1alpha1.DeviceAttributes {
    deviceClassName := resourceNameToDeviceClassName(resourceName)  // e.g. intel_nic -> intel-nic
    name := deviceClassName + "-attrs"
    extendedName := buildExtendedResourceName(resourceName)  // e.g. openshift.io/intel_nic
    return &sriovdrav1alpha1.DeviceAttributes{
        ObjectMeta: metav1.ObjectMeta{
            Name:      name,
            Namespace: vars.Namespace,
            Labels: map[string]string{
                "sriovnetwork.openshift.io/generated-by":  "sriov-network-operator",
                "sriovnetwork.openshift.io/resource-pool": deviceClassName,
            },
        },
        Spec: sriovdrav1alpha1.DeviceAttributesSpec{
            Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
                resourceapi.QualifiedName("k8s.cni.cncf.io/resourceName"): {StringValue: &extendedName},
            },
        },
    }
}

// Example: Generate per-node SriovResourcePolicy
func generateSriovResourcePolicyForNode(
    policies []*SriovNetworkNodePolicy,
    nodeState *SriovNetworkNodeState,
    node *corev1.Node) (*sriovdrav1alpha1.SriovResourcePolicy, error) {

    policy := &sriovdrav1alpha1.SriovResourcePolicy{
        ObjectMeta: metav1.ObjectMeta{
            Name: node.Name, // same as SriovNetworkNodeState metadata.name
            Namespace: vars.Namespace,
            Labels: map[string]string{
                "sriovnetwork.openshift.io/generated-by": "sriov-network-operator",
                "sriovnetwork.openshift.io/node":         node.Name,
            },
        },
        Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
            NodeSelector: &corev1.NodeSelector{
                NodeSelectorTerms: []corev1.NodeSelectorTerm{
                    {
                        MatchExpressions: []corev1.NodeSelectorRequirement{
                            {
                                Key:      corev1.LabelHostname,
                                Operator: corev1.NodeSelectorOpIn,
                                Values:   []string{node.Name},
                            },
                        },
                    },
                },
            },
            Configs: []sriovdrav1alpha1.Config{},
        },
    }

    for _, p := range policies {
        if p.Selected(node) {
            poolLabel := strings.ReplaceAll(p.Spec.ResourceName, "_", "-")
            config := sriovdrav1alpha1.Config{
                DeviceAttributesSelector: &metav1.LabelSelector{
                    MatchLabels: map[string]string{
                        "sriovnetwork.openshift.io/resource-pool": poolLabel,
                    },
                },
                ResourceFilters: convertPolicyToResourceFilters(p, nodeState),
            }
            policy.Spec.Configs = append(policy.Spec.Configs, config)
        }
    }

    return policy, nil
}
```

**Naming Convention:**
- One `SriovResourcePolicy` per node: `metadata.name` = node name (e.g. `worker-1`, same as `SriovNetworkNodeState`)
- One `DeviceAttributes` per unique `resourceName`: `<device-class-name>-attrs` (e.g., `intel-nic-attrs` where device-class-name is `resourceNameToDeviceClassName(resourceName)`)
- `SriovResourcePolicy` uses node selector to target exactly one node
- `DeviceAttributes` are linked to policies via label `sriovnetwork.openshift.io/resource-pool`

**State Machine:**

```
Initial State:
  1. Operator creates DeviceAttributes CRs (per unique resourceName) and
     SriovResourcePolicy CRs (per node, based on policies, stay in place)
  2. DRA driver pod starts with init container
  3. Init container sets wait-for-config annotation on pod and blocks
  4. Config daemon completes initial configuration, removes annotation
  5. Init container exits; DRA driver starts, discovers VFs, matches against policies,
     resolves DeviceAttributes, publishes only matched devices

When Policy Changes:
  1. Config daemon detects change (or operator triggers)
  2. DRA driver pod is restarted (by daemon or operator)
  3. New pod's init container sets wait-for-config annotation and blocks
  4. Config daemon applies new SR-IOV configuration, then removes annotation
  5. Init container exits; DRA driver starts, discovers VFs, matches against policies
  6. Operator updates SriovResourcePolicy and DeviceAttributes CRs if needed
```

**Benefits:**
- DRA driver only sees VFs that are properly configured
- Prevents race conditions similar to device plugin issue
- Clean synchronization using Kubernetes-native state tracking
- Per-node granularity matches SR-IOV configuration model

**Comparison to Device Plugin Approach:**

| Aspect | Device Plugin | DRA (implemented) |
|--------|--------------|-------------------|
| Signal mechanism | Init sets pod annotation; daemon removes it | Same: init sets pod annotation; daemon removes it |
| Blocking | Init waits until annotation removed | Same |
| State source | Pod annotation `sriovnetwork.openshift.io/device-plugin-wait-config` | Same annotation |
| Config resources | ConfigMap (per-node data) | SriovResourcePolicy CRs (per-node) + DeviceAttributes CRs (per-resourceName) |
| Config lifecycle | ConfigMap always present | SriovResourcePolicy + DeviceAttributes CRs always present |

**Key Benefits:**
- Reuses the same protection mechanism as the device plugin (pod annotation; config daemon removes it when config is done)
- `SriovResourcePolicy` and `DeviceAttributes` CRs stay in place; no delete/recreate on config change
- Consistent behavior for both device plugin and DRA modes

**4. Mode Switching and Resource Management**
- Switching between device plugin and DRA mode should be blocked if:
  - There are active pods using SR-IOV resources
  - There are existing ResourceClaims (when switching from DRA to device plugin)
- Validation webhook should enforce this
- When switching to DRA mode:
  - Operator stops updating `device-plugin-config` ConfigMap
  - Operator starts creating/updating `DeviceAttributes` CRs (per unique `resourceName`) and per-node `SriovResourcePolicy` CRs from policies (only for nodes with `SyncStatus == "Succeeded"`)
- When switching to device plugin mode:
  - Operator deletes all auto-generated `SriovResourcePolicy` and `DeviceAttributes` CRs
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
  - Number of `SriovResourcePolicy` and `DeviceAttributes` CRs managed
  - Per-node policy creation/deletion events
  - Time between config completion and policy creation
  - Resource claims and allocations

**7. RBAC Requirements**
- DRA driver requires RBAC permissions (similar to device plugin):
  - Access to `resourceslices.resource.k8s.io` API (for publishing devices)
  - Access to `resourceclaims.resource.k8s.io` API (for reading claims)
  - Read access to `deviceclasses.resource.k8s.io` API (to read the basic DeviceClass)
  - Read/List/Watch permissions for `SriovResourcePolicy` and `DeviceAttributes` CRDs
- Operator requires RBAC permissions:
  - Create/Update/Delete `DeviceClass` resources (for basic DeviceClass)
  - Create/Update/Delete `SriovResourcePolicy` CRs (for auto-generated policies)
  - Create/Update/Delete `DeviceAttributes` CRs (for auto-generated attributes)

**8. Node Configuration and Synchronization**
- Node configuration (VF creation, driver binding, etc.) remains unchanged
- The config daemon continues to work the same way in both modes:
  - Reads desired state from `SriovNetworkNodeState.Spec`
  - Applies SR-IOV configuration (creates VFs, binds drivers, etc.)
  - Removes the wait-for-config pod annotation when configuration is complete (so init container can exit)
- In DRA mode:
  - DRA driver DaemonSet includes init container (same pattern as device plugin)
  - Init container sets pod annotation and blocks until config daemon removes it
  - `SriovResourcePolicy` and `DeviceAttributes` CRs stay in place
  - When config changes, DRA driver pod is restarted; init container blocks until config complete
- **Advantage:** Reuses the same synchronization mechanism as the device plugin (pod annotation)

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
6. Operator auto-generates `SriovResourcePolicy` and `DeviceAttributes` CRs from existing policies (CRDs are already installed)

**Upgrading Operator (DRA Mode → DRA Mode)**
- DRA driver image may be updated
- Existing ResourceClaims continue to work
- `SriovResourcePolicy` and `DeviceAttributes` CRs are preserved

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
   - Note: `SriovResourcePolicy` and `DeviceAttributes` CRDs remain installed (users should manually delete CRs if desired)
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

When DRA is enabled, the operator automatically creates a `DeviceClass` for each unique `resourceName` in `SriovNetworkNodePolicy` CRs (in addition to the basic DeviceClass). Each has `extendedResourceName` set to `ResourcePrefix/resourceName` and a CEL selector that matches the resourceName attribute (which is set to the same extended name in `DeviceAttributes`). When the Kubernetes `DRAExtendedResource` feature gate is enabled on the cluster, pods can request these via `resources.limits`; otherwise users request via ResourceClaimTemplate.

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

Generate this DeviceClass (extended resource name uses `ResourcePrefix/resourceName`, e.g. `openshift.io/intel_nic`; CEL matches that same value):
```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: intel-nic  # resourceNameToDeviceClassName(resourceName)
  labels:
    sriovnetwork.openshift.io/generated-by: sriov-network-operator
    sriovnetwork.openshift.io/resource-name: intel_nic
spec:
  extendedResourceName: openshift.io/intel_nic   # buildExtendedResourceName(resourceName)
  selectors:
  - cel:
      expression: |
        device.driver == "sriovnetwork.k8snetworkplumbingwg.io" &&
        device.attributes["k8s.cni.cncf.io"].resourceName == "openshift.io/intel_nic"
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

No additional feature gate. When DRA is enabled (`dynamicResourceAllocation: true`), the operator creates per-resourceName DeviceClasses with `extendedResourceName`. Using them for pod `resources.limits` requires the Kubernetes `DRAExtendedResource` feature gate on the cluster.

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
- Created/updated when policies change (when DRA is enabled)
- Deleted when no policies use that `resourceName` anymore, or when DRA is disabled
- Includes extended resource mapping (`extendedResourceName`)
- Naming: convert `resourceName` to valid DeviceClass name (e.g., `intel_nic` → `intel-nic`)

**3. CEL Expression Generation**

The operator needs to generate CEL expressions that match the DRA driver's device attributes:
```go
func generateDeviceClassSelector(resourceName string) string {
    return fmt.Sprintf(
        `device.driver == "sriov.k8snetworkplumbingwg.io" && ` +
        `device.attributes["k8s.cni.cncf.io"].resourceName == "%s"`,
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
3. **Operator Complexity**: Requires managing `SriovResourcePolicy`, `DeviceAttributes`, and `DeviceClass` resources
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
5. Per-node `SriovResourcePolicy` and `DeviceAttributes` generation tests:
   - Test `DeviceAttributes` CR is created per unique `resourceName`
   - Test `SriovResourcePolicy` CR is created per node based on policies (stays in place)
   - Test `deviceAttributesSelector` correctly references `DeviceAttributes` labels
   - Test CRs are updated when policies change (not deleted/recreated)
   - Test per-node naming and nodeSelector generation
6. DRA driver pod protection tests:
   - Test init container blocks DRA driver start when node label not "Idle"
   - Test DRA driver starts after node label changes to "Idle"
   - Test DRA driver pod restart on configuration changes
   - Test init container behavior matches device plugin init container
6. Configuration generation tests for DRA driver with various environment variable configurations
7. Tests for reading DRA driver configuration from environment variables

#### Integration Tests

1. Deploy operator (verify `SriovResourcePolicy` and `DeviceAttributes` CRDs are installed via Helm)
2. Create `SriovNetworkNodePolicy` in device plugin mode (verify `device-plugin-config` ConfigMap is created)
3. Enable DRA feature gate
4. Verify DRA driver DaemonSet is deployed (device plugin should not be deployed)
5. Verify basic DeviceClass (`sriovnetwork.k8snetworkplumbingwg.io`) is created
6. Verify operator automatically generates `DeviceAttributes` CRs (per unique `resourceName`) and per-node `SriovResourcePolicy` CRs from existing policies
7. Verify CRs are only created for nodes where the config daemon has set `SriovNetworkNodeState.Status.SyncStatus == "Succeeded"`
8. Verify generated `SriovResourcePolicy` CRs have correct owner references, labels, per-node naming (same as node / SriovNetworkNodeState name), and `deviceAttributesSelector` referencing the correct `DeviceAttributes`
9. Verify each `SriovResourcePolicy` has a nodeSelector targeting exactly one node
10. Verify DRA driver discovers VFs, advertises only policy-matched devices, and applies attributes from `DeviceAttributes`
11. Create ResourceClaim referencing the basic DeviceClass
12. Create pod with ResourceClaim and verify VF allocation
11. Test CNI integration with DRA-allocated VFs
12. Test both kernel driver and VFIO modes
13. Update policy and verify:
    - Corresponding `SriovResourcePolicy` and `DeviceAttributes` CRs are updated
    - Config daemon applies new configuration
    - CRs are updated after `SyncStatus == "Succeeded"`
14. Delete policy and verify corresponding `SriovResourcePolicy` CRs are updated (config entries removed) and orphaned `DeviceAttributes` CRs are cleaned up
15. Test feature gate switching (device plugin ↔ DRA)
16. Test custom DRA driver configuration via Helm values (image, interface prefix, etc.)
17. Test user-created `SriovResourcePolicy` and `DeviceAttributes` CRs alongside auto-generated ones

#### E2E Tests

1. **Basic DRA Workflow**
   - Deploy operator with DRA enabled
   - Verify basic DeviceClass (`sriovnetwork.k8snetworkplumbingwg.io`) is created
   - Create `SriovNetworkNodePolicy` with `resourceName: intel_nic`
   - Create `SriovNetwork` CR (generates NetworkAttachmentDefinition)
   - Verify operator auto-generates corresponding `SriovResourcePolicy` and `DeviceAttributes` CRs
   - Verify DRA driver DaemonSet has init container waiting for node label
   - Wait for config daemon to complete and set node label to "Idle"
   - Verify DRA driver starts and discovers VFs
   - Create ResourceClaimTemplate
   - Deploy pod using ResourceClaim with network annotation
   - Verify network connectivity
   - Delete pod and verify VF cleanup

2. **Automatic Policy/Attributes Generation & Synchronization**
   - Create `SriovNetworkNodePolicy` CR
   - Verify operator creates `DeviceAttributes` CR (per `resourceName`) and `SriovResourcePolicy` CRs (per node, stay in place)
   - Verify one `SriovResourcePolicy` per node with correct nodeSelector and `deviceAttributesSelector`
   - Verify DRA driver pod has init container that blocks until node label is "Idle"
   - Monitor config daemon applying configuration and setting node label
   - Verify DRA driver starts only after label is set and advertises only matched devices
   - Update a policy and verify:
     - `SriovResourcePolicy` and `DeviceAttributes` CRs are updated (not deleted/recreated)
     - DRA driver pod is restarted
     - Init container blocks until config complete
     - DRA driver discovers updated configuration with correct attributes
   - Delete a policy and verify corresponding config entries and orphaned `DeviceAttributes` are cleaned up
   - Verify CRs have proper owner references and labels

3. **Advanced Filtering**
   - Create user-managed `SriovResourcePolicy` and `DeviceAttributes` CRs with advanced criteria
   - Verify they coexist with auto-generated CRs
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
   - Request resources that don't match any policy (should fail, no devices advertised)

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

4. ~~**Should SriovResourcePolicy/DeviceAttributes creation be automatic** based on SriovNetworkNodePolicy?~~
   - **Decision: Yes, auto-generate from policies** (similar to device-plugin-config ConfigMap)
   - Simplifies migration and provides consistent user experience
   - Users can still create additional manual `SriovResourcePolicy` and `DeviceAttributes` CRs for advanced scenarios

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
