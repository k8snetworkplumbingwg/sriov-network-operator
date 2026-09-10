---
title: devlink-params-configuration
authors:
  - e0ne
reviewers:
  - SchSeba
  - adrianchiris
creation-date: 12-12-2025
last-updated: 12-12-2025
---

# Devlink Params configuration

## Summary
devlink params configuration provides capability for a driver to expose device parameters for low-level device functionality.

## Motivation

Devlink parameters allows to provide additional configuration options for NIC devices. SR-IOV Network operator
provides opportunity for firmware and OS-level configuration but misses additional devlink parameters configuration.

### Use Cases

* Add initial implementation to provide general [devlink parameters](https://docs.kernel.org/networking/devlink/devlink-params.html)
  into SR-IOV Network Operator
* Implement support of vendor-specific configuration

### Goals

* Provide API to configure generic devlink parameters
* Add initial implementation to allow implement vendor-specific features

### Non-Goals

* Initial implementation of this design proposal won't contain support of all available devlink parameters
* Vendor-specific should be implemented as a part of vendor plugins separately.

## Proposal

To extend the current API to provide users configure devlink parameters.

### Workflow Description

Devlink parameters are configured on OS level by `GenericPlugin`. Since vendor hardware could require firmware configuration
prior to devlink (e.g. `esw_multiport` requires `LAG_RESOURCE_ALLOCATION=1` firmware flag to be set for NVIDIA NICs)
vendor plugin will go over `DevlinkParams` list to configure firmware if needed.

#### Webhook changes
TBD

### API Extensions
#### Extend existing CR SriovNetworkNodePolicy
SriovNetworkNodePolicy will be extended to provide ability to pass devlink parameters to SR-IOV Operator plugins. 
E.g.:

```golang

// DevlinkParam defines the parameter for devlink configuration
type DevlinkParam struct {
    // Param name
    Name string `json:"name,omitempty"`
    // Param value
    Value string `json:"value,omitempty"`
    // cmode option: runtime (default) | driverinit | permanent (runtime is only supported now)
    // +kubebuilder:validation:Enum=runtime;driverinit;permanent
    Cmode string `json:"cmode,omitempty"`
    // Device to apply devlink parameter: pf (default)|vf|sf (Sub-Function)
    // +kubebuilder:validation:Enum=pf|vf|sf
    // +kubebuilder:validation:XValidationRule="self == '' || self in ['pf', 'vf', 'sf']"
    // +kubebuilder:default=pf
    ApplyOn string `json:"applyOn,omitempty"`
}


// SriovNetworkNodePolicySpec defines the desired state of SriovNetworkNodePolicy
type SriovNetworkNodePolicySpec struct {
    ...
	// contains devlink params for NIC devices
	DevlinkParams []DevlinkParam `json:"devlinkParams,omitempty"`
}

type Interface struct {
    ...
    DevlinkParams     DevlinkParams `json:"devlinkParams,omitempty"`
}


type InterfaceExt struct {
	...
    DevlinkParams     DevlinkParams     `json:"devlinkParams,omitempty"`
    VFs               []VirtualFunction `json:"Vfs,omitempty"`
}

DevlinkParams added to `SriovNetworkNodeState` both for Spec and Status fields describe PF/VF/SF depends on `ApplyOn` field.

```

### Upgrade & Downgrade considerations

New CRDs should be applied during upgrade or downgrade procedures. Backward compatibility will be handled by Kubernetes itself.

### Test Plan

* Unit-tests will be implemented for:
** changes in affected plugins
** webhook validation
* e2e tests will be implemented to cover new API and webhook changes
