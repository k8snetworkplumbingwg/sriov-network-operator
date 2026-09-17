# SriovOperatorConfig API Reference

The `SriovOperatorConfig` Custom Resource configures global settings for the SR-IOV Network Operator. This resource controls operator behavior, feature gates, and component deployment settings across the entire cluster.

## Resource Overview

- **API Version**: `sriovnetwork.openshift.io/v1`
- **Kind**: `SriovOperatorConfig`
- **Scope**: Namespaced (typically `sriov-network-operator`)
- **Access**: Cluster Admin only
- **Instance**: Single instance named `default`

## Purpose

The `SriovOperatorConfig` resource allows administrators to:
- Configure global operator behavior and logging (verbosity and config-daemon host file logs)
- Enable or disable operator components (webhooks, injector)
- Control node selection for configuration management
- Enable experimental features through feature gates
- Configure SR-IOV device plugin operation mode

## Resource Structure

### Basic Configuration

```yaml
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovOperatorConfig
metadata:
  name: default
  namespace: sriov-network-operator
spec:
  # Global operator settings
  logLevel: 2
  disableDrain: false

  # Config-daemon host file logging (optional; defaults apply when omitted)
  # logConfig:
  #   enabled: true
  
  # Component control
  enableInjector: true
  enableOperatorWebhook: true
  
  # Node selection
  configDaemonNodeSelector:
    node-role.kubernetes.io/worker: ""
  
  # Advanced features
  configurationMode: daemon
  useCDI: false
  
  # Plugin management
  disablePlugins: []
  
  # Feature gates
  featureGates:
    parallelNicConfig: true
```

## Field Reference

### Spec Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `configDaemonNodeSelector` | map[string]string | `{}` | Node selector for sriov-config-daemon deployment |
| `enableInjector` | bool | `true` | Deploy network resource injector webhook |
| `enableOperatorWebhook` | bool | `true` | Deploy operator admission controller webhook |
| `logLevel` | int | `2` | Log verbosity level (0=basic, 2=detailed) |
| `logConfig` | object | defaults (enabled) | Config-daemon host file logging (see [LogConfig](#logconfig)) |
| `disableDrain` | bool | `false` | Disable node drain during configuration |
| `enableOvsOffload` | bool | `false` | Enable OVS hardware offload support |
| `configurationMode` | string | `daemon` | Configuration mode: "daemon" or "systemd" |
| `useCDI` | bool | `false` | Use Container Device Interface for device plugin |
| `disablePlugins` | []string | `[]` | List of plugins to disable |
| `featureGates` | map[string]bool | `{}` | Experimental feature toggles |
| `configDaemonEnvVars` | map[string]string | `{}` | Extra env vars for sriov-network-config-daemon |

### Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | []metav1.Condition | Kubernetes-standard status conditions. `Ready` is the primary health signal for `SriovOperatorConfig`. |
| `injector` | string | Runtime status of network resource injector |
| `operatorWebhook` | string | Runtime status of operator webhook |

## Configuration Options

### Log Level Settings

```yaml
spec:
  # Basic logging (errors and warnings only)
  logLevel: 0
  
  # Standard logging (includes info messages)
  logLevel: 1
  
  # Verbose logging (includes debug messages)
  logLevel: 2
```

### LogConfig

Persistent host logs for `sriov-network-config-daemon` (survive pod restart and node reboot).
When `logConfig` is omitted, logging is **enabled** with the defaults below. Stdout/`oc logs` are unchanged.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Write rotating logs on the host |
| `maxSizeMB` | int | `100` | Rotate after this many MiB (1–1024) |
| `maxFiles` | int | `5` | Max rotated backups to keep (1–20) |
| `maxAgeDays` | int | `30` | Max age of backups in days; `0` = no age limit (max 365) |
| `compress` | bool | `true` | Gzip rotated files |
| `hostPath` | string | `/var/log/sriov-network-config-daemon` | Host directory: folder name under `/var/log` or absolute path under `/var/log` |

Active file: `<hostPath>/config-daemon.log`.

```yaml
spec:
  logConfig:
    enabled: true
    maxSizeMB: 100
    maxFiles: 5
    maxAgeDays: 30
    compress: true
    hostPath: /var/log/sriov-network-config-daemon

  # Opt out of host file logging
  logConfig:
    enabled: false

  # Custom directory under /var/log (folder name form)
  logConfig:
    enabled: true
    hostPath: sriov-daemon-logs
```

Design notes: [Persistent Log Storage for Config Daemon](../design/sriov-persistent-logging.md).

### Node Selection

```yaml
spec:
  # Configure only worker nodes
  configDaemonNodeSelector:
    node-role.kubernetes.io/worker: ""
  
  # Configure specific nodes by label
  configDaemonNodeSelector:
    sriov-capable: "true"
  
  # Configure all nodes (use with caution)
  configDaemonNodeSelector: {}
```

### Component Control

```yaml
spec:
  # Disable network resource injector (not recommended)
  enableInjector: false
  
  # Disable admission controller webhook (not recommended)
  enableOperatorWebhook: false
  
  # Disable node drain during updates (use for debugging ONLY)
  disableDrain: true
```

### Advanced Features

```yaml
spec:
  # Use systemd for configuration (alternative to daemon)
  configurationMode: systemd
  
  # Enable Container Device Interface
  useCDI: true
```

### Plugin Management

```yaml
spec:
  # Disable specific plugins
  disablePlugins:
    - mellanox
```

Available plugins to disable:
- `mellanox`: Mellanox-specific configuration plugin

### Feature Gates

```yaml
spec:
  featureGates:
    # Enable parallel NIC configuration
    parallelNicConfig: true

  # switch injector to fail policy and add mactch condition this will make the mutating webhook to be called only when a pod has 'k8s.v1.cni.cncf.io/networks' annotation
  resourceInjectorMatchCondition: true

  # enables the SriovNetworkMetricsExporter on the same node as where the config-daemon run
  metricsExporter: true

  # enables management of software bridges by the operator
  manageSoftwareBridges: true

  # enables the firmware reset via mstfwreset before a reboot
  mellanoxFirmwareReset: true
```

## Configuration Modes

### Daemon Mode (Default)

```yaml
spec:
  configurationMode: daemon
```

In daemon mode:
- Configuration applied by long-running daemon
- Immediate configuration updates
- Better error reporting and recovery
- Recommended for most deployments

### Systemd Mode

```yaml
spec:
  configurationMode: systemd
```

In systemd mode:
- Configuration applied via systemd service on boot
- requires a reboot for every configuration update
- Useful for specific deployment scenarios

## Usage Examples

### Basic Operator Configuration

```yaml
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovOperatorConfig
metadata:
  name: default
  namespace: sriov-network-operator
spec:
  logLevel: 1
  configDaemonNodeSelector:
    node-role.kubernetes.io/worker: ""
  enableInjector: true
  enableOperatorWebhook: true
```

## Management Commands

### View Current Configuration

```bash
# Get the operator configuration
kubectl get sriovoperatorconfig default -n sriov-network-operator -o yaml
```

### Update Configuration

```bash
# Edit the configuration
kubectl edit sriovoperatorconfig default -n sriov-network-operator

# Apply configuration from file
kubectl apply -f operator-config.yaml
```

### Monitor Changes

```bash
# Watch for configuration changes
kubectl get sriovoperatorconfig default -n sriov-network-operator -w

# Check operator logs after configuration changes
kubectl logs deployment/sriov-network-operator -n sriov-network-operator
```

## Feature Gates Reference

| Feature Gate | Description | Status |
|--------------|-------------|--------|
| `parallelNicConfig` | Enable parallel configuration of NICs | Stable |
| `resourceInjectorMatchCondition` | Switch injector to fail policy and add match condition (only call webhook when pod has networks annotation) | Stable |
| `metricsExporter` | Enable SriovNetworkMetricsExporter on nodes where config-daemon runs | Beta   |
| `manageSoftwareBridges` | Enable management of software bridges by the operator | Beta  |
| `mellanoxFirmwareReset` | Enable firmware reset via mstfwreset before reboot | Beta  |

## Debug Commands

```bash
# Check daemon pod status
kubectl get pods -n sriov-network-operator -l app=sriov-config-daemon

# View daemon logs
kubectl logs daemonset/sriov-config-daemon -n sriov-network-operator

# Check webhook status
kubectl get validatingwebhookconfiguration sriov-operator-webhook-config

# Verify injector
kubectl get mutatingwebhookconfiguration network-resources-injector-config
```

## Related Resources

- [SriovNetworkNodePolicy](node-policies-api.md) - Per-node configuration policies
- [SriovNetworkPoolConfig](pool-config-api.md) - Node pool management
- [SriovNetworkNodeState](node-state-api.md) - Node configuration status