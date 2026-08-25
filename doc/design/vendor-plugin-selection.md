# Vendor Plugin Selection

## Overview

The sriov-network-config-daemon loads only one _vendor plugins_ besed on vendor ID.
Each plugin encapsulates the device-specific logic (VF configuration,
firmware parameter setting, eswitch mode, etc.) for a family of SR-IOV NICs.

## Proposed changes

This document describes how the daemon selects which plugin to run for a given
PCI vendor, and how an operator can switch between alternatives at runtime
without rebuilding the image.



### Plugin Roles

| Role               | Description |
|--------------------|---|
| **Generig plugin** | Always active; owns generic SR-IOV operations (e.g. `generic`). Cannot be disabled. |
| **Vendor plugins** | Loaded based on the PCI vendor IDs present in `SriovNetworkNodeState.Status.Interfaces`. Zero or more are active per reconcile. |

### Loading Flow

```
SriovOperatorConfig.spec.disablePlugins
          │
          ▼ (rendered into DaemonSet args by the operator controller)
--disable-plugins=<comma-separated list>
          │
          ▼ (cmd/sriov-network-config-daemon/start.go)
NodeReconciler.Init(disabledPlugins)
          │
          ▼ (pkg/daemon/plugin.go)
loadPlugins(ns, disabledPlugins)
    ├─ platform.GetVendorPlugins(ns)          ← loads candidates
    │     ├─ VendorPluginMap[vendor]           ← primary constructor
    │     └─ VendorPluginAlternativeMap[vendor]← fallback constructor
    ├─ filter out explicitly disabled plugins
    └─ enforceMutualExclusion(...)            ← drop lower-priority duplicates
```

## Vendor Plugin Maps (baremetal)

Two maps in `pkg/platform/baremetal/baremetal.go` carry plugin constructors keyed
by PCI vendor ID:

```go
// Primary — preferred plugin for each vendor.
var VendorPluginMap = map[string]func(...) (plugin.VendorPlugin, error){
    "8086": intelplugin.NewIntelPlugin,
    "15b3": mellanoxplugin.NewMellanoxPlugin,   // preferred for Mellanox/NVIDIA NICs
}

// Alternative — fallback loaded alongside the primary. This will contain an empty map by default
// It could be extended if there will be alternative upstream plugin implementation or done inside vendor fork
var VendorPluginAlternativeMap = map[string]func(...) (plugin.VendorPlugin, error){
    "15b3": vendor.NewProprietaryPlugin, // legacy fallback
}
```

`loadVendorPlugins` iterates over the detected interfaces and calls both
constructors for each vendor, so that downstream selection logic has both
candidates available.

### Mutually Exclusive Plugin Groups

Some vendor IDs map to two competing implementations. At most one should be
active at a time. The `mutuallyExclusivePlugins` table in
`pkg/daemon/plugin.go` declares these groups:

```go
// This will contain an empty list by default. The same usage pattern as for VendorPluginAlternativeMap.
var mutuallyExclusivePlugins = [][]string{
    ["UpstreamPlugin", "ProprietaryPlugin"],  // group for PCI vendor 15b3
}
```

Within each group the **first name takes precedence**. After the disabled-plugin
filter runs, `enforceMutualExclusion` scans every group and drops any
lower-priority member whose higher-priority sibling is still active.

#### Selection Logic

```
For the group ["UpstreamPlugin", "ProprietaryPlugin"]:

  Both loaded, neither disabled          → UpstreamPlugin active, mellanox suppressed
  "UpstreamPlugin" in disablePlugins     → ProprietaryPlugin active (UpstreamPlugin filtered
                                           before mutual-exclusion runs)
  "ProprietaryPlugin" in disablePlugins  → UpstreamPlugin active (already filtered)
  Both disabled                          → neither active
```

`enforceMutualExclusion` does not touch plugins that belong to no group, so
adding a group entry is the only change needed to make two plugins mutually
exclusive.

### Adding a New Alternative for an Existing Vendor

1. Implement `plugin.VendorPlugin` in a new package.
2. Register its constructor in `VendorPluginAlternativeMap` (or `VendorPluginMap`
   if it should become the new default).
3. Add a group entry to `mutuallyExclusivePlugins` that lists both the new
   plugin name and the one it competes with, with the preferred name first.

No changes to the daemon reconcile loop or the main plugin are required.

### Selecting a Plugin at Runtime

The `SriovOperatorConfig` CR exposes `spec.disablePlugins` (a string list).
The operator controller renders it into the DaemonSet's `--disable-plugins`
flag. To switch from `NvidiaPlugin` to the legacy `mellanox` plugin:

```yaml
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovOperatorConfig
metadata:
  name: default
  namespace: sriov-network-operator
spec:
  disablePlugins:
    - UpstreamPlugin
```

The daemon pods restart with the updated flag; on the next reconcile
`mellanox` becomes the active 15b3 plugin.

## Sequence Diagram

```
Operator controller          DaemonSet (daemon)
        │                          │
        │  watches SriovOperatorConfig
        │  renders --disable-plugins
        │──────────────────────────►│
        │                          │  Init(disabledPlugins)
        │                          │    loadPlugins()
        │                          │      GetVendorPlugins()
        │                          │        new UpstreamPlugin  ◄─ primary
        │                          │        new ProprietaryPlugin ◄─ alternative
        │                          │      filter disabled
        │                          │      enforceMutualExclusion()
        │                          │        → keep UpstreamPlugin
        │                          │        → suppress ProprietaryPlugin
        │                          │  reconcile with UpstreamPlugin active
```

### Files

| File                                              | Role |
|---------------------------------------------------|---|
| `pkg/daemon/plugin.go`                            | `loadPlugins`, `enforceMutualExclusion`, `mutuallyExclusivePlugins` |
| `pkg/platform/baremetal/baremetal.go`             | `VendorPluginMap`, `VendorPluginAlternativeMap`, `loadVendorPlugins` |
| `pkg/plugins/mellanox/upstream_plugin.go`         | Primary plugin for vendor  |
| `pkg/plugins/vendor/venror_proprietary_plugin.go` | Alternative/legacy plugin for vendor `15b3` |
| `api/v1/sriovoperatorconfig_types.go`             | `SriovOperatorConfigSpec.DisablePlugins` field |
| `bindata/manifests/daemon/daemonset.yaml`         | Renders `DisablePlugins` into `--disable-plugins` flag |
