---
title: Platform and Orchestrator Abstraction
authors:
  - sriov-network-operator team
reviewers:
  - TBD
creation-date: 21-07-2025
last-updated: 21-07-2025
---

# Platform and Orchestrator Abstraction

## Summary

This design document describes the introduction of platform and orchestrator abstraction layers in the SR-IOV Network Operator. These abstractions separate platform-specific (infrastructure provider) logic from orchestrator-specific (Kubernetes distribution) logic, making it easier to add support for new infrastructure platforms and Kubernetes distributions.

## Motivation

The SR-IOV Network Operator has historically been tightly coupled to specific infrastructure platforms and Kubernetes distributions, particularly OpenShift. As the operator expanded to support different virtualization platforms like OpenStack, AWS, Oracle and various Kubernetes distributions, the need for a clean abstraction layer became apparent.

### Use Cases

1. **Multi-Platform Support**: Enable the operator to run efficiently on different infrastructure platforms (bare metal, OpenStack, AWS,Oracle, etc.) with platform-specific optimizations
2. **Multi-Orchestrator Support**: Support different Kubernetes distributions (vanilla Kubernetes, OpenShift, etc.) with orchestrator-specific behaviors
3. **Extensibility**: Make it easy to add new platforms and orchestrators without modifying core operator logic
4. **Testing**: Enable better unit testing with mockable interfaces for platform and orchestrator specific operations

### Goals

* Create a clean abstraction layer that separates platform-specific logic from orchestrator-specific logic
* Implement support for bare metal and OpenStack platforms
* Implement support for Kubernetes and OpenShift orchestrators
* Provide a plugin architecture that makes it easy to add new platforms and orchestrators
* Maintain backward compatibility with existing functionality
* Enable better testability through interface-based design

### Non-Goals

* Support all possible infrastructure platforms in the initial implementation
* Change existing API structures or user-facing interfaces

## Proposal

### Workflow Description

The operator will use two main abstraction layers:

1. **Platform Interface**: Handles infrastructure-specific operations like device discovery, bridge management, and plugin selection
2. **Orchestrator Interface**: Handles Kubernetes distribution-specific operations like cluster type detection, additional node draining logic, and cluster-specific configurations

The platform is detected at startup based on node metadata and environment variables, while the orchestrator is detected based on cluster characteristics and available APIs.

### API Extensions

#### Platform Interface

```golang
type Interface interface {
    Init() error
    GetHostHelpers() helper.HostHelpersInterface
    
    DiscoverSriovDevices() ([]sriovnetworkv1.InterfaceExt, error)
    DiscoverBridges() (sriovnetworkv1.Bridges, error)
    
    GetPlugins(ns *sriovnetworkv1.SriovNetworkNodeState) (plugin.VendorPlugin, []plugin.VendorPlugin, error)
    SystemdGetPlugin(phase string) (plugin.VendorPlugin, error)
}
```

#### Orchestrator Interface

```golang
type Interface interface {
    ClusterType() consts.ClusterType
    Flavor() consts.ClusterFlavor
    BeforeDrainNode(context.Context, *corev1.Node) (bool, error)
    AfterCompleteDrainNode(context.Context, *corev1.Node) (bool, error)
}
```

### Implementation Details/Notes/Constraints

#### Platform Implementations

1. **Bare Metal Platform (`pkg/platform/baremetal/`)**:
   - Uses standard SR-IOV device discovery
   - Supports vendor-specific plugins (Intel, Mellanox)
   - Handles bridge discovery and management
   - Supports both daemon and systemd configuration modes

2. **OpenStack Platform (`pkg/platform/openstack/`)**:
   - Uses virtual device discovery based on OpenStack metadata
   - Reads device information from config-drive or metadata service
   - Uses virtual plugin for VF configuration
   - Does not support systemd mode or bridge management

#### Orchestrator Implementations

1. **Kubernetes Orchestrator (`pkg/orchestrator/kubernetes/`)**:
   - Simple implementation with minimal cluster-specific logic
   - No special drain handling (returns true for all drain operations)
   - Vanilla Kubernetes flavor

2. **OpenShift Orchestrator (`pkg/orchestrator/openshift/`)**:
   - Complex drain handling with Machine Config Pool management
   - Supports both regular OpenShift and Hypershift flavors
   - Manages MCP pausing during node operations

#### Platform Detection

Platform detection occurs in the daemon startup code based on:
- Node provider ID examination
- Environment variables
- Available metadata services

```golang
// Platform detection logic
for key, pType := range vars.PlatformsMap {
    if strings.Contains(strings.ToLower(nodeInfo.Spec.ProviderID), strings.ToLower(key)) {
        vars.PlatformType = pType
    }
}
```

#### Factory Pattern

Both platform and orchestrator use factory patterns for instantiation:

```golang
// Platform factory
func New(hostHelpers helper.HostHelpersInterface) (Interface, error) {
    switch vars.PlatformType {
    case consts.Baremetal:
        return baremetal.New(hostHelpers)
    case consts.VirtualOpenStack:
        return openstack.New(hostHelpers)
    default:
        return nil, fmt.Errorf("unknown platform type %s", vars.PlatformType)
    }
}

// Orchestrator factory
func New() (Interface, error) {
    switch vars.ClusterType {
    case consts.ClusterTypeOpenshift:
        return openshift.New()
    case consts.ClusterTypeKubernetes:
        return kubernetes.New()
    default:
        return nil, fmt.Errorf("unknown orchestration type: %s", vars.ClusterType)
    }
}
```

### Upgrade & Downgrade considerations

The abstraction layer is designed to be backward compatible. Existing configurations and behaviors are preserved, with the abstraction layer providing the same functionality through the new interface structure.

No user-facing API changes are required, and existing SR-IOV configurations will continue to work without modification.

### Test Plan

The implementation includes comprehensive unit tests for both platform and orchestrator abstractions:

1. **Platform Tests**: Test device discovery, plugin loading, and platform-specific behaviors for both bare metal and OpenStack platforms
2. **Orchestrator Tests**: Test cluster type detection, drain handling, and orchestrator-specific behaviors for both Kubernetes and OpenShift
3. **Integration Tests**: Ensure the abstractions work correctly with the existing daemon and operator logic
4. **Mock Interfaces**: Generated mock interfaces enable comprehensive unit testing of components that depend on platform and orchestrator abstractions

## Benefits for Adding New Platforms

### 1. Clear Separation of Concerns

The abstraction separates infrastructure-specific logic (platform) from Kubernetes distribution-specific logic (orchestrator), making it easier to reason about and implement support for new platforms.

### 2. Standardized Interface

New platforms only need to implement the well-defined `Platform Interface`, which includes:
- Device discovery methods
- Plugin selection logic
- Platform-specific initialization

### 3. Minimal Core Changes

Adding a new platform requires:
1. Creating a new package under `pkg/platform/<platform-name>/`
2. Implementing the `Platform Interface`
3. Adding the platform to the factory function
4. Adding platform detection logic

No changes to core operator logic, existing platforms, or user-facing APIs are required.

### 4. Plugin Architecture

The platform interface includes plugin selection methods, allowing each platform to:
- Choose appropriate vendor plugins
- Use platform-specific plugins (like the virtual plugin for OpenStack)
- Support different configuration modes (daemon vs systemd)

### 5. Independent Development and Testing

Each platform implementation is self-contained, enabling:
- Independent development of platform support
- Platform-specific unit tests
- Mock-based testing of platform interactions
- Easier debugging and maintenance

### Example: Adding a New Platform (AWS Implementation)

The AWS platform implementation demonstrates how to add support for a new cloud platform:

1. Create `pkg/platform/aws/aws.go`:
```golang
package aws

import (
    sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
    "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper"
    plugin "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins"
    virtualplugin "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins/virtual"
)

type Aws struct {
    hostHelpers       helper.HostHelpersInterface
    loadedDevicesInfo sriovnetworkv1.InterfaceExts
}

func New(hostHelpers helper.HostHelpersInterface) (*Aws, error) {
    return &Aws{hostHelpers: hostHelpers}, nil
}

func (a *Aws) GetPlugins(_ *sriovnetworkv1.SriovNetworkNodeState) (plugin.VendorPlugin, []plugin.VendorPlugin, error) {
    virtual, err := virtualplugin.NewVirtualPlugin(a.hostHelpers)
    return virtual, []plugin.VendorPlugin{}, err
}

func (a *Aws) DiscoverSriovDevices() ([]sriovnetworkv1.InterfaceExt, error) {
    // AWS-specific device discovery using EC2 metadata service
    // Fetches MAC addresses and subnet IDs from metadata service
    // Maps devices to AWS network configuration
}

func (a *Aws) SystemdGetPlugin(_ string) (plugin.VendorPlugin, error) {
    return nil, fmt.Errorf("aws platform not supported in systemd")
}

// Implement other interface methods...
```

2. Add to the platform factory in `pkg/platform/platform.go`:
```golang
func New(hostHelpers helper.HostHelpersInterface) (Interface, error) {
    switch vars.PlatformType {
    case consts.Baremetal:
        return baremetal.New(hostHelpers)
    case consts.VirtualOpenStack:
        return openstack.New(hostHelpers)
    case consts.VirtualAWS:  // New addition
        return aws.New(hostHelpers)
    default:
        return nil, fmt.Errorf("unknown platform type %s", vars.PlatformType)
    }
}
```

3. Add platform detection logic in `pkg/vars/vars.go`:
```golang
var PlatformsMap = map[string]consts.PlatformTypes{
    "openstack": consts.VirtualOpenStack,
    "aws":       consts.VirtualAWS,  // New addition
}
```

4. Add platform constant in `pkg/consts/platforms.go`:
```golang
const (
    Baremetal PlatformTypes = iota
    VirtualOpenStack
    VirtualAWS  // New addition
)
```

Key features of the AWS implementation:
- **Metadata Service Integration**: Uses AWS EC2 metadata service to discover network configuration
- **Virtual Plugin Usage**: Leverages the existing virtual plugin for SR-IOV VF management
- **Subnet ID Mapping**: Maps network interfaces to AWS subnet IDs for proper network filtering
- **Comprehensive Testing**: Includes extensive unit tests with mocked HTTP calls
- **Error Handling**: Robust error handling for metadata service failures

This approach makes the SR-IOV Network Operator truly platform-agnostic while maintaining clean, maintainable code. 