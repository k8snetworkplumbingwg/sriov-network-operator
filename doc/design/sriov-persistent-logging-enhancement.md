# Persistent Log Storage for Config Daemon

## Summary

Add persistent, host-based log storage with rotation for the sriov-network-config-daemon so that logs survive container restarts and node reboots. Today, when the config daemon triggers a node reboot (e.g., after SR-IOV firmware changes or VF reconfiguration), all container logs are lost because they only exist in the container's stdout/stderr stream. This makes it extremely difficult to debug why a reboot was initiated.

## Motivation

The config daemon is responsible for applying SR-IOV configuration on each node. Part of that process may require a node reboot (e.g., Mellanox firmware changes, enabling/disabling SR-IOV, totalVfs changes). The reboot is performed via systemd-run reboot after chrooting into `/host`. Once the node reboots, the kubelet restarts, the DaemonSet pod is recreated, and all previous container logs are gone.

This creates a significant debugging gap: operators cannot determine what the config daemon was doing or why it decided to reboot the node.

## Use Cases

1. **Post-reboot debugging**: After a config daemon-initiated reboot, the operator or cluster admin needs to inspect pre-reboot logs to understand what configuration was being applied and why the reboot was required.
2. **Crash investigation**: If the config daemon crashes or is OOM-killed, previous log history on the host helps diagnose the issue.
3. **Audit trail**: Maintaining a persistent log of all SR-IOV configuration changes applied to a node over time, across multiple daemon restarts.

## Goals

- Persist config daemon logs to the host filesystem so they survive container restarts and node reboots.
- Implement log rotation with configurable maximum file size and number of retained files to prevent unbounded disk usage on the host.
- Ensure the logging mechanism works correctly with the daemon's chroot operations (chroot to `/host` for reboot, device configuration, systemd operations).
- Make log persistence opt-in or enabled by default with sensible defaults.
- Maintain existing stdout/stderr logging behavior (logs still appear in `kubectl logs`).

## Non-Goals

- Replacing the existing stdout/stderr logging (this is additive).
- Implementing a centralized log aggregation system.
- Persisting logs for the operator controller or webhook components.
- Changing the logging framework (logr/zap) itself.

## Proposal

### Overview

Add a secondary zap log sink that writes to a file on the host filesystem. The file writer will use the lumberjack library (`gopkg.in/natefinez/lumberjack.v2`) for automatic log rotation. The log file will be written to a path under `/host/var/log/sriov-network-config-daemon/` (which corresponds to `/var/log/sriov-network-config-daemon/` on the host).

### Workflow Description

1. On startup, the config daemon initializes logging as it does today (zap via controller-runtime).
2. Additionally, a file-based zap core is created using lumberjack as the underlying writer, pointing to `/host/var/log/sriov-network-config-daemon/config-daemon.log`.
3. The zap logger is configured with a `zapcore.NewTee` to write to both stdout (existing) and the log file (new).
4. Log rotation is handled by lumberjack:
   - Maximum file size before rotation (default: 100 MB).
   - Maximum number of old log files to retain (default: 5).
   - Maximum age of old log files in days (default: 30).
   - Compression of rotated files (default: enabled).
5. When the daemon chroots into `/host` (e.g., for reboot or device configuration), the log file handle remains valid because it was opened using the `/host/...` path (the mount point), not a path relative to the chroot.

### Chroot Considerations

The config daemon uses chroot in several places:

- `rebootNode()` in `pkg/daemon/daemon.go` — chroots to `/host` before running `systemd-run reboot`.
- `Apply()` in `pkg/plugins/generic/generic_plugin.go` — chroots to `/host` for interface configuration.
- Various host helper functions for kernel modules, systemd services, etc.

Since the log file is opened via the `/host/var/log/...` mount path (an absolute path in the container's filesystem), and the file descriptor is kept open by the lumberjack writer, chroot operations do not affect logging. The open file descriptor remains valid across chroot boundaries because:

- The file is opened before chroot occurs.
- File descriptors survive chroot — only path resolution is affected by chroot, not existing open FDs.
- On rotation, lumberjack will need to create new files. Since rotation could theoretically happen during a chroot, we mitigate this by using the absolute path `/host/var/log/...` and ensuring the writer is initialized before any chroot call.

**Important**: If log rotation triggers during a chroot (the daemon is chrooted to `/host`), the path `/host/var/log/...` would resolve incorrectly (it would look for `/host/host/var/log/...` on the actual host). To handle this:

- **Option A**: Use a dedicated goroutine for log writing that never enters chroot, or buffer log writes and flush outside chroot sections.
- **Option B**: Write the log file to a path that resolves correctly in both contexts. Since `/host` maps to `/` on the host, the log path inside chroot would be `/var/log/sriov-network-config-daemon/config-daemon.log`, which is valid. We can open the file using this host-relative path before chroot, and lumberjack rotation will work because chroot sections are short-lived and the file is re-opened at the original path when chroot exits. However, this relies on timing assumptions and could fail if rotation occurs during chroot.
- **Option C**: Write to a tmpfs or emptyDir mount that is also bind-mounted to the host. However, this does not survive pod restarts.

**Recommended approach**: Option A — Use a dedicated logging mechanism that is isolated from chroot operations. This provides a robust solution that avoids race conditions entirely. The zap logger will buffer writes in memory, and the underlying file writer (lumberjack) will operate in the main namespace, never entering the chrooted environment. This eliminates the risk of path resolution failures during log rotation, regardless of when rotation occurs relative to chroot operations.

### API Extensions

Extend the `SriovOperatorConfig` CRD to expose log persistence configuration:

```go
type SriovOperatorConfigSpec struct {
    // ...existing fields...

    // logConfig contains configuration for config daemon log persistence
    // +optional
    LogConfig *LogConfig `json:"logConfig,omitempty"`
}

type LogConfig struct {
    // enabled controls whether persistent log storage is active.
    // Defaults to true.
    // +optional
    Enabled *bool `json:"enabled,omitempty"`

    // maxSizeMB is the maximum size in megabytes of a log file before rotation.
    // Defaults to 100.
    // +optional
    MaxSizeMB *int `json:"maxSizeMB,omitempty"`

    // maxFiles is the maximum number of old log files to retain.
    // Defaults to 5.
    // +optional
    MaxFiles *int `json:"maxFiles,omitempty"`

    // maxAgeDays is the maximum number of days to retain old log files.
    // Defaults to 30. Set to 0 to disable age-based cleanup.
    // +optional
    MaxAgeDays *int `json:"maxAgeDays,omitempty"`

    // compress controls whether rotated log files are compressed using gzip.
    // Defaults to true.
    // +optional
    Compress *bool `json:"compress,omitempty"`

    // hostPath is the directory on the host where log files are stored.
    // Defaults to "/var/log/sriov-network-config-daemon".
    // +optional
    HostPath *string `json:"hostPath,omitempty"`
}
```

Example usage:

```yaml
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovOperatorConfig
metadata:
  name: default
  namespace: sriov-network-operator
spec:
  logConfig:
    enabled: true
    maxSizeMB: 100
    maxFiles: 5
    maxAgeDays: 30
    compress: true
```

## Implementation Details/Notes/Constraints

### 1. Dependency: lumberjack

Add `gopkg.in/natefinez/lumberjack.v2` as a dependency. This is a well-established Go library for log rotation, used widely in the Kubernetes ecosystem (e.g., kubelet itself uses it).

### 2. Changes to `pkg/log/log.go`

Modify `InitLog()` to accept an optional file writer configuration. Create a new function:

```go
func InitLogWithFile(logFilePath string, maxSizeMB, maxFiles, maxAgeDays int, compress bool) {
    fileWriter := &lumberjack.Logger{
        Filename:   logFilePath,
        MaxSize:    maxSizeMB,
        MaxBackups: maxFiles,
        MaxAge:     maxAgeDays,
        Compress:   compress,
    }

    // Create a zap core for file output
    fileEncoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
    fileCore := zapcore.NewCore(fileEncoder, zapcore.AddSync(fileWriter), zapcore.DebugLevel)

    // Create the console core (existing behavior)
    consoleCore := // ... existing zap setup ...

    // Combine both cores
    combinedCore := zapcore.NewTee(consoleCore, fileCore)

    logger := zap.New(combinedCore)
    log.SetLogger(zapr.NewLogger(logger))
}
```

### 3. Changes to DaemonSet manifest

Update `bindata/manifests/daemon/daemonset.yaml` to add a volume mount for the log directory:

```yaml
volumeMounts:
  - name: sriov-logs
    mountPath: /host/var/log/sriov-network-config-daemon
volumes:
  - name: sriov-logs
    hostPath:
      path: /var/log/sriov-network-config-daemon
      type: DirectoryOrCreate
```

**Note**: We already have the `/host` volume mount mapping the entire host root. The dedicated log volume mount is preferred for clarity and to ensure the directory is created automatically via `DirectoryOrCreate`.

### 4. Changes to config daemon startup (`cmd/sriov-network-config-daemon/start.go`)

Read the `LogConfig` from `SriovOperatorConfig` and pass it to the log initialization:

```go
func init() {
    // ... existing flag parsing ...

    snolog.InitLog()
    // File-based logging initialized later after reading operator config
}

func runStartCmd(cmd *cobra.Command, args []string) {
    // ... read SriovOperatorConfig ...

    if operatorConfig.Spec.LogConfig != nil && operatorConfig.Spec.LogConfig.Enabled {
        snolog.InitLogWithFile(
            filepath.Join("/host/var/log/sriov-network-config-daemon", "config-daemon.log"),
            operatorConfig.Spec.LogConfig.MaxSizeMB,
            operatorConfig.Spec.LogConfig.MaxFiles,
            operatorConfig.Spec.LogConfig.MaxAgeDays,
            operatorConfig.Spec.LogConfig.Compress,
        )
    }
}
```

### 5. Log format for files

File logs will use JSON format for structured logging, making them easy to parse with standard log analysis tools. Console logs will continue using the existing human-readable format.

### 6. Disk space considerations

With the default configuration:

- Max single file: 100 MB
- Max retained files: 5
- Maximum disk usage: ~500 MB per node (plus compressed backups)

This is acceptable for most environments. Cluster admins can tune these values via `SriovOperatorConfig`.

### 7. Security considerations

- The log directory on the host (`/var/log/sriov-network-config-daemon/`) will be created with restricted permissions (0750).
- Log files will not contain secrets (SR-IOV configuration is not sensitive in most environments), but care should be taken not to log raw API responses that may contain sensitive fields.

## Upgrade & Downgrade considerations

- **Upgrade**: The feature is additive. On upgrade, if `LogConfig` is not set, persistent logging defaults to enabled with default values. Existing deployments will start persisting logs automatically. If this is undesirable, `LogConfig.Enabled` can be set to false.
- **Downgrade**: On downgrade to a version without this feature, the log files remain on the host but are no longer written to. The `SriovOperatorConfig` CRD field will be ignored by older versions. An admin may want to clean up `/var/log/sriov-network-config-daemon/` manually.
- **DaemonSet changes**: The additional volume mount in the DaemonSet is backward-compatible (the `DirectoryOrCreate` type creates the directory if it doesn't exist).

## Test Plan

### Unit tests:

- Verify that `InitLogWithFile` correctly creates a tee logger writing to both stdout and file.
- Verify that log rotation parameters are correctly applied.
- Verify default values are applied when `LogConfig` is nil or partially specified.

### Integration / E2E tests:

- Deploy the operator with persistent logging enabled.
- Trigger a configuration change that causes a node reboot.
- After the reboot, verify that the log file exists on the host at the expected path.
- Verify that the log file contains pre-reboot log entries (the reboot reason should be present).
- Verify log rotation by writing enough data to trigger a rotation event.
- Verify that the number of retained log files does not exceed the configured maximum.

### Manual testing:

- Verify logs persist across config daemon pod restarts (e.g., after `kubectl delete pod`).
- Verify logs persist across node reboots triggered by the daemon.
- Verify chroot operations do not disrupt log writing.
- Verify disk usage stays within configured limits over time.
