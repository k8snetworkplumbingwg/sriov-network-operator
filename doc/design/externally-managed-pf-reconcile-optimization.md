---
title: Avoid Unnecessary Reconfiguration for Externally Managed PFs
authors:
  - ykulazhenkov
reviewers:
  - TBD
creation-date: 15-12-2024
last-updated: 15-12-2024
---

# Avoid Unnecessary Reconfiguration for Externally Managed PFs

## Summary

This proposal addresses unnecessary reconfiguration cycles for policies using externally managed
Physical Functions (PFs) after operator restarts. The goal is to make the controller skip
reconfiguration when the actual device configuration is already fully in sync, while still
ensuring that VF Admin MAC is properly configured when needed.


## Motivation

When a policy uses externally managed PFs, the current controller logic forces at least one
reconfiguration cycle unconditionally. This is implemented in `api/v1/helper.go` in the
`NeedToUpdateSriov()` function:

```go
// this is needed to be sure the admin mac address is configured as expected
if ifaceSpec.ExternallyManaged {
    log.V(0).Info("NeedToUpdateSriov(): need to update the device as it's externally manage",
        "device", ifaceStatus.PciAddress)
    return true
}
```

The comment explains this is done to ensure VF Admin MAC is set. However, this causes
unneeded reconfiguration after each daemon restart, even when the actual device
configuration is already fully in sync.

Additionally, in `pkg/plugins/generic/generic_plugin.go`, the `CheckStatusChanges()` function
skips externally managed interfaces entirely:

```go
// TODO: remove the check for ExternallyManaged - https://github.com/k8snetworkplumbingwg/sriov-network-operator/issues/632
if iface.PciAddress == ifaceStatus.PciAddress && !iface.ExternallyManaged {
```

This creates an inconsistency: externally managed PFs are always marked as needing
reconfiguration in the daemon's `OnNodeStateChange()` handler (which calls
`NeedToUpdateSriov()`), but skipped during the periodic configuration check in
`CheckStatusChanges()`.

This behavior results in unnecessary reconfiguration after every daemon restart, even when
the device configuration is already correct. Furthermore, if the VF configuration actually
changes after the daemon start, these changes may go undetected and will not be reconciled
by the config daemon.

### Use Cases

* As a cluster operator, I want externally managed PFs to not trigger reconfiguration cycles
  after daemon restart when the configuration is already correct.
* As a cluster operator using RDMA with externally managed PFs, I want the operator to still
  ensure VF Admin MAC and RDMA GUID are properly configured.

### Goals

* Remove the unconditional reconfiguration for externally managed PFs
* Ensure VF Admin MAC is still configured correctly when needed (Ethernet netdevice VFs)
* Make the reconciliation behavior consistent: if status shows "in sync", do not reconfigure
* Maintain backward compatibility with existing deployments

### Non-Goals

* Changing how externally managed PFs validate their prerequisites (numVfs, MTU, link type)
* Adding new user-facing configuration options for this behavior

## Proposal

Three options are proposed to address this issue. All options aim to remove the unconditional
`return true` for externally managed PFs while ensuring VF Admin MAC configuration is correct.

### Option 1: Extend API / Report Admin MAC in Node State

Extend the SriovNetworkNodeState API to report the VF Admin MAC in the status, and use this
information to make intelligent reconciliation decisions.

#### Workflow Description

1. During device discovery, the config daemon reads and reports each VF's Admin MAC address
   in the SriovNetworkNodeState status.
2. When checking if reconfiguration is needed (`NeedToUpdateSriov()`), trigger reconfiguration for any ethernet interface (including externally managed) if the VF Admin MAC is empty or uninitialized.
3. Remove the unconditional `return true` for externally managed interfaces.
4. Update `CheckStatusChanges()` in the generic plugin to include externally managed interfaces
   in the comparison logic.

#### API Extensions

Add a new field to the `VirtualFunction` struct in `api/v1/sriovnetworknodestate_types.go`:

```go
type VirtualFunction struct {
    Name            string `json:"name,omitempty"`
    Mac             string `json:"mac,omitempty"`
    AdminMac        string `json:"adminMac,omitempty"`  // NEW: Administrative MAC address
    Assigned        string `json:"assigned,omitempty"`
    Driver          string `json:"driver,omitempty"`
    PciAddress      string `json:"pciAddress"`
    Vendor          string `json:"vendor,omitempty"`
    DeviceID        string `json:"deviceID,omitempty"`
    Vlan            int    `json:"Vlan,omitempty"`
    Mtu             int    `json:"mtu,omitempty"`
    VfID            int    `json:"vfID"`
    VdpaType        string `json:"vdpaType,omitempty"`
    RepresentorName string `json:"representorName,omitempty"`
    GUID            string `json:"guid,omitempty"`
}
```

#### Implementation Details

1. **Discovery Changes** (`pkg/host/internal/sriov/sriov.go`):
   - In `DiscoverSriovDevices()`, read the VF Admin MAC from the PF using netlink
   - Populate the new `AdminMac` field in the VirtualFunction status

2. **Reconciliation Logic Changes** (`api/v1/helper.go`):
   - Remove the unconditional `return true` for externally managed PFs
   - Add Admin MAC check for Ethernet netdevice VFs.
   ```go
   // Check if Admin MAC needs to be configured
   if strings.EqualFold(ifaceStatus.LinkType, consts.LinkTypeETH) {
       if groupSpec.DeviceType == "" || groupSpec.DeviceType == consts.DeviceTypeNetDevice {
           if vfStatus.AdminMac == "" || vfStatus.AdminMac == consts.UninitializedMac {
               log.V(0).Info("NeedToUpdateSriov(): VF Admin MAC needs update",
                   "vf", vfStatus.VfID, "device", ifaceStatus.PciAddress)
               return true
           }
       }
   }
   ```

3. **Plugin Changes** (`pkg/plugins/generic/generic_plugin.go`):
   - Remove the `!iface.ExternallyManaged` check in `CheckStatusChanges()` and
     `needToUpdateVFs()` to include externally managed interfaces in the comparison logic.

#### Pros

- Clean, declarative approach: the status reflects the actual state
- Consistent with existing patterns (similar to GUID field)
- No runtime checks needed during reconciliation

#### Cons

- Requires API/CRD changes with upgrade considerations
- Reading Admin MAC during discovery adds minor overhead

### Option 2: Runtime Validation Check (No API Change)

Remove the unconditional reconcile for externally managed PFs and perform a runtime validation
check to verify Admin MAC is correctly set.

#### Workflow Description

1. Remove the unconditional `return true` for externally managed interfaces in
   `NeedToUpdateSriov()`.
2. Update the generic plugin to include externally managed interfaces in both
   `CheckStatusChanges()` and `needToUpdateVFs()` (called via `OnNodeStateChange()`).
3. Add a runtime validation check for all interfaces (not just externally managed) that
   verifies the VF Admin MAC is set and matches the VF's hardware address. If this
   validation does not succeed, reconfiguration should be triggered. This check is
   performed after `NeedToUpdateSriov()` returns false to ensure Admin MAC is correctly
   configured.

#### Implementation Details

1. **New Validation Function** (`pkg/host/internal/sriov/sriov.go`):
   ```go
   func (s *sriov) ValidateVfAdminMac(vfAddr string, pfLink netlink.Link) (bool, error) {
       vfID, err := s.dputilsLib.GetVFID(vfAddr)
       if err != nil {
           return false, err
       }

       vfLink, err := s.VFIsReady(vfAddr)
       if err != nil {
           return false, err
       }

       // Get current Admin MAC from PF
       adminMac, err := s.netlinkLib.LinkGetVfHardwareAddr(pfLink, vfID)
       if err != nil {
           return false, err
       }

       // Compare with VF's hardware address
       return bytes.Equal(adminMac, vfLink.Attrs().HardwareAddr), nil
   }
   ```

2. **Reconciliation Logic Changes** (`api/v1/helper.go`):
   - Remove the unconditional `return true` for externally managed PFs
   - The `NeedToUpdateSriov()` function cannot perform runtime checks as it only receives
     spec and status data, so the runtime validation is handled in the plugin

3. **Plugin Changes** (`pkg/plugins/generic/generic_plugin.go`):
   The runtime validation check needs to be added in two places. The check applies to all
   interfaces, not just externally managed ones, to ensure consistent Admin MAC configuration:

   a. **`CheckStatusChanges()`** - determines if configuration needs to be applied:
   ```go
   for _, iface := range current.Spec.Interfaces {
       for _, ifaceStatus := range current.Status.Interfaces {
           if iface.PciAddress == ifaceStatus.PciAddress {
               if sriovnetworkv1.NeedToUpdateSriov(&iface, &ifaceStatus) {
                   return true, nil
               }
               // Perform runtime validation for Admin MAC for all interfaces
               valid, err := p.helpers.ValidateVfAdminMac(...)
               if err != nil || !valid {
                   return true, nil
               }
               break
           }
       }
   }
   ```

   b. **`needToUpdateVFs()`** - is called part of the periodic configuration check:
   ```go
   for _, ifaceStatus := range current.Interfaces {
       for _, iface := range desired.Interfaces {
           if iface.PciAddress == ifaceStatus.PciAddress {
               if sriovnetworkv1.NeedToUpdateSriov(&iface, &ifaceStatus) {
                   return true
               }
               // Perform runtime validation for Admin MAC for all interfaces
               valid, err := p.helpers.ValidateVfAdminMac(...)
               if err != nil || !valid {
                   return true
               }
               break
           }
       }
   }
   ```

#### Pros

- No API/CRD changes required
- No upgrade considerations for the API
- Validation uses actual device state, not cached status

#### Cons

- Runtime checks add latency to reconciliation
- Mixing runtime checks with status-based decisions adds complexity


### Option 3: Use Existing Mac Field as Admin MAC Indicator

Reuse the existing `Mac` field in VirtualFunction status to indicate whether Admin MAC is
properly configured. If Admin MAC does not match the VF hardware address, report the Mac
field as empty.

#### Workflow Description

1. During device discovery, compare the VF Admin MAC (from PF) with the VF hardware address.
2. If they match, report the VF's Mac in the status as usual.
3. If they do not match, report the Mac field as empty string.
4. In `NeedToUpdateSriov()`, for PFs with netdevice name and Ethernet link type:
   - Check if the VF status Mac is empty
   - If empty, trigger reconfiguration (Admin MAC needs to be set)
   - If not empty, skip reconfiguration (Admin MAC is already correct)
5. Remove the unconditional `return true` for externally managed interfaces.

#### Implementation Details

1. **Discovery Changes** (`pkg/host/internal/sriov/sriov.go`):
   - In `DiscoverSriovDevices()`, when populating VF info:
   ```go
   // Get Admin MAC from PF
   adminMac, err := s.netlinkLib.LinkGetVfHardwareAddr(pfLink, vfID)
   if err != nil {
       // Handle error - may report empty Mac
       vf.Mac = ""
   } else if bytes.Equal(adminMac, vfLink.Attrs().HardwareAddr) {
       // Admin MAC matches VF hardware address - report the Mac
       vf.Mac = vfLink.Attrs().HardwareAddr.String()
   } else {
       // Admin MAC not set correctly - report empty to trigger reconciliation
       vf.Mac = ""
   }
   ```

2. **Reconciliation Logic Changes** (`api/v1/helper.go`):
   - Remove the unconditional `return true` for externally managed PFs
   - Add Admin MAC check for all VFs with Ethernet link type:
   ```go
   // For netdevice driver with Ethernet link type, check if Mac is set
   // Empty Mac indicates Admin MAC needs configuration
   if groupSpec.DeviceType == "" || groupSpec.DeviceType == consts.DeviceTypeNetDevice {
       if strings.EqualFold(ifaceStatus.LinkType, consts.LinkTypeETH) {
           if vfStatus.Mac == "" {
               log.V(0).Info("NeedToUpdateSriov(): VF Mac empty, Admin MAC needs config",
                   "vf", vfStatus.VfID, "device", ifaceStatus.PciAddress)
               return true
           }
       }
   }
   ```

3. **Plugin Changes** (`pkg/plugins/generic/generic_plugin.go`):
   - Remove the `!iface.ExternallyManaged` check in `CheckStatusChanges()` and
     `needToUpdateVFs()` to include externally managed interfaces in the comparison logic

#### Pros

- No API/CRD changes required - reuses existing `Mac` field
- No upgrade considerations for the API
- Simple logic: empty Mac means reconfiguration needed
- Follows existing discovery patterns

#### Cons

- Changes the semantics of the existing `Mac` field (now indicates Admin MAC status)
- VFs that are not fully configured or not selected by policy will report empty Mac
- May confuse users who expect Mac field to always show the VF's hardware address
- Less explicit than a dedicated `AdminMac` field


### Upgrade & Downgrade Considerations

#### Option 1

**Upgrade**: The new `AdminMac` field is optional. On upgrade:
- Existing SriovNetworkNodeState CRs will not have the field
- The daemon will populate it during the next discovery cycle
- Until populated, the behavior falls back to triggering reconfiguration (safe default)

**Downgrade**: On downgrade:
- The new field will be ignored by the older operator version
- The older operator will use its existing logic (unconditional reconfiguration)

#### Option 2

**Upgrade/Downgrade**: No special considerations as there are no API changes.

#### Option 3

**Upgrade**: On upgrade:
- The Mac field semantics change (empty means Admin MAC not configured)
- First discovery cycle after upgrade will report empty Mac for VFs needing reconfiguration
- This will trigger one reconfiguration cycle for affected VFs (expected behavior)

**Downgrade**: On downgrade:
- The older operator will report Mac field as before (always VF hardware address)
- This restores the unconditional reconfiguration behavior for externally managed PFs

### Test Plan

1. **Unit Tests**:
   - Test `NeedToUpdateSriov()` with externally managed PFs and various Admin MAC states
   - Test `CheckStatusChanges()` includes externally managed interfaces
   - Test Admin MAC discovery and reporting

2. **Integration Tests**:
   - Verify externally managed PF with correct Admin MAC does not trigger reconfiguration
   - Verify externally managed PF with missing Admin MAC triggers reconfiguration
   - Verify RDMA GUID is correctly derived after Admin MAC configuration

3. **E2E Tests**:
   - Deploy policy with externally managed PF
   - Verify initial configuration sets Admin MAC
   - Restart daemon and verify no reconfiguration occurs
   - Verify RDMA functionality works correctly
