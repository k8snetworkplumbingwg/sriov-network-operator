package v1_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

func TestGetEffectiveLogConfig(t *testing.T) {
	t.Run("nil LogConfig returns defaults", func(t *testing.T) {
		got, err := v1.GetEffectiveConfigDaemonLogConfig(nil)
		require.NoError(t, err)
		assert.Equal(t, vars.DefaultLogCfg(), got)
	})

	t.Run("empty LogConfig returns defaults", func(t *testing.T) {
		got, err := v1.GetEffectiveConfigDaemonLogConfig(&v1.LogConfig{})
		require.NoError(t, err)
		assert.Equal(t, vars.DefaultLogCfg(), got)
	})

	t.Run("partial override keeps remaining defaults", func(t *testing.T) {
		maxSizeMB := 25
		got, err := v1.GetEffectiveConfigDaemonLogConfig(&v1.LogConfig{ComponentLogConfig: v1.ComponentLogConfig{MaxSizeMB: &maxSizeMB}})
		require.NoError(t, err)
		d := vars.DefaultLogCfg()
		assert.Equal(t, 25, got.MaxSizeMB)
		assert.Equal(t, d.MaxFiles, got.MaxFiles)
		assert.Equal(t, d.MaxAgeDays, got.MaxAgeDays)
		assert.Equal(t, d.Enabled, got.Enabled)
	})

	t.Run("folder-name HostPath resolves under /var/log", func(t *testing.T) {
		hostPath := "custom-sriov-network-operator"
		got, err := v1.GetEffectiveConfigDaemonLogConfig(&v1.LogConfig{ComponentLogConfig: v1.ComponentLogConfig{HostPath: &hostPath}})
		require.NoError(t, err)
		assert.Equal(t, "/var/log/custom-sriov-network-operator", got.HostPath)
	})

	t.Run("rejects HostPath outside /var/log", func(t *testing.T) {
		hostPath := "/custom/log"
		_, err := v1.GetEffectiveConfigDaemonLogConfig(&v1.LogConfig{ComponentLogConfig: v1.ComponentLogConfig{HostPath: &hostPath}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "/var/log")
	})

	t.Run("rejects path traversal", func(t *testing.T) {
		hostPath := "/../escape"
		_, err := v1.GetEffectiveConfigDaemonLogConfig(&v1.LogConfig{ComponentLogConfig: v1.ComponentLogConfig{HostPath: &hostPath}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "path traversal")
	})

	t.Run("rejects filesystem root", func(t *testing.T) {
		hostPath := "/"
		_, err := v1.GetEffectiveConfigDaemonLogConfig(&v1.LogConfig{ComponentLogConfig: v1.ComponentLogConfig{HostPath: &hostPath}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "filesystem root")
	})
}

func TestGetEffectiveConfigDaemonLogConfig(t *testing.T) {
	t.Run("nil LogConfig returns defaults", func(t *testing.T) {
		got, err := v1.GetEffectiveConfigDaemonLogConfig(nil)
		require.NoError(t, err)
		assert.Equal(t, vars.DefaultLogCfg(), got)
	})

	t.Run("no configDaemon override uses global values", func(t *testing.T) {
		enabled := true
		maxSizeMB := 50
		got, err := v1.GetEffectiveConfigDaemonLogConfig(&v1.LogConfig{
			ComponentLogConfig: v1.ComponentLogConfig{
				Enabled:   &enabled,
				MaxSizeMB: &maxSizeMB,
			},
		})
		require.NoError(t, err)
		assert.True(t, got.Enabled)
		assert.Equal(t, 50, got.MaxSizeMB)
	})

	t.Run("configDaemon override takes precedence", func(t *testing.T) {
		enabled := true
		globalMaxSizeMB := 50
		daemonMaxSizeMB := 200
		got, err := v1.GetEffectiveConfigDaemonLogConfig(&v1.LogConfig{
			ComponentLogConfig: v1.ComponentLogConfig{
				Enabled:   &enabled,
				MaxSizeMB: &globalMaxSizeMB,
			},
			ConfigDaemon: &v1.ComponentLogConfig{
				MaxSizeMB: &daemonMaxSizeMB,
			},
		})
		require.NoError(t, err)
		assert.True(t, got.Enabled)
		assert.Equal(t, 200, got.MaxSizeMB) // override
	})

	t.Run("configDaemon can disable when global is enabled", func(t *testing.T) {
		enabled := true
		disabled := false
		got, err := v1.GetEffectiveConfigDaemonLogConfig(&v1.LogConfig{
			ComponentLogConfig: v1.ComponentLogConfig{Enabled: &enabled},
			ConfigDaemon: &v1.ComponentLogConfig{
				Enabled: &disabled,
			},
		})
		require.NoError(t, err)
		assert.False(t, got.Enabled)
	})

	t.Run("partial configDaemon override keeps other global values", func(t *testing.T) {
		enabled := true
		maxSizeMB := 50
		maxFiles := 3
		maxAgeDays := 7
		daemonMaxFiles := 10
		got, err := v1.GetEffectiveConfigDaemonLogConfig(&v1.LogConfig{
			ComponentLogConfig: v1.ComponentLogConfig{
				Enabled:    &enabled,
				MaxSizeMB:  &maxSizeMB,
				MaxFiles:   &maxFiles,
				MaxAgeDays: &maxAgeDays,
			},
			ConfigDaemon: &v1.ComponentLogConfig{
				MaxFiles: &daemonMaxFiles,
			},
		})
		require.NoError(t, err)
		assert.True(t, got.Enabled)
		assert.Equal(t, 50, got.MaxSizeMB) // global
		assert.Equal(t, 10, got.MaxFiles)  // override
		assert.Equal(t, 7, got.MaxAgeDays) // global
	})

	t.Run("configDaemon hostPath override", func(t *testing.T) {
		enabled := true
		globalHostPath := "global-logs"
		daemonHostPath := "daemon-logs"
		got, err := v1.GetEffectiveConfigDaemonLogConfig(&v1.LogConfig{
			ComponentLogConfig: v1.ComponentLogConfig{
				Enabled:  &enabled,
				HostPath: &globalHostPath,
			},
			ConfigDaemon: &v1.ComponentLogConfig{
				HostPath: &daemonHostPath,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "/var/log/daemon-logs", got.HostPath)
	})
}

func TestGetEffectiveDevicePluginLogConfig(t *testing.T) {
	t.Run("nil LogConfig returns defaults", func(t *testing.T) {
		got, err := v1.GetEffectiveDevicePluginLogConfig(nil)
		require.NoError(t, err)
		assert.Equal(t, vars.DefaultLogCfg(), got)
	})

	t.Run("no devicePlugin override uses global values", func(t *testing.T) {
		enabled := true
		maxSizeMB := 50
		got, err := v1.GetEffectiveDevicePluginLogConfig(&v1.LogConfig{
			ComponentLogConfig: v1.ComponentLogConfig{
				Enabled:   &enabled,
				MaxSizeMB: &maxSizeMB,
			},
		})
		require.NoError(t, err)
		assert.True(t, got.Enabled)
		assert.Equal(t, 50, got.MaxSizeMB)
	})

	t.Run("devicePlugin override takes precedence", func(t *testing.T) {
		enabled := true
		globalMaxSizeMB := 50
		devicePluginMaxSizeMB := 30
		got, err := v1.GetEffectiveDevicePluginLogConfig(&v1.LogConfig{
			ComponentLogConfig: v1.ComponentLogConfig{
				Enabled:   &enabled,
				MaxSizeMB: &globalMaxSizeMB,
			},
			DevicePlugin: &v1.ComponentLogConfig{
				MaxSizeMB: &devicePluginMaxSizeMB,
			},
		})
		require.NoError(t, err)
		assert.True(t, got.Enabled)
		assert.Equal(t, 30, got.MaxSizeMB) // override
	})

	t.Run("devicePlugin can disable when global is enabled", func(t *testing.T) {
		enabled := true
		disabled := false
		got, err := v1.GetEffectiveDevicePluginLogConfig(&v1.LogConfig{
			ComponentLogConfig: v1.ComponentLogConfig{Enabled: &enabled},
			DevicePlugin: &v1.ComponentLogConfig{
				Enabled: &disabled,
			},
		})
		require.NoError(t, err)
		assert.False(t, got.Enabled)
	})

	t.Run("independent configs for both components", func(t *testing.T) {
		enabled := true
		daemonMaxSizeMB := 50
		daemonMaxFiles := 3
		devicePluginMaxSizeMB := 30
		devicePluginMaxFiles := 1
		lc := &v1.LogConfig{
			ComponentLogConfig: v1.ComponentLogConfig{Enabled: &enabled},
			ConfigDaemon: &v1.ComponentLogConfig{
				MaxSizeMB: &daemonMaxSizeMB,
				MaxFiles:  &daemonMaxFiles,
			},
			DevicePlugin: &v1.ComponentLogConfig{
				MaxSizeMB: &devicePluginMaxSizeMB,
				MaxFiles:  &devicePluginMaxFiles,
			},
		}

		daemonCfg, err := v1.GetEffectiveConfigDaemonLogConfig(lc)
		require.NoError(t, err)
		assert.Equal(t, 50, daemonCfg.MaxSizeMB)
		assert.Equal(t, 3, daemonCfg.MaxFiles)

		dpCfg, err := v1.GetEffectiveDevicePluginLogConfig(lc)
		require.NoError(t, err)
		assert.Equal(t, 30, dpCfg.MaxSizeMB)
		assert.Equal(t, 1, dpCfg.MaxFiles)
	})
}

func TestResolveLogHostPath(t *testing.T) {
	t.Run("accepts absolute path under /var/log", func(t *testing.T) {
		got, err := v1.ResolveLogHostPath("/var/log/sriov")
		require.NoError(t, err)
		assert.Equal(t, "/var/log/sriov", got)
	})

	t.Run("resolves folder name under /var/log", func(t *testing.T) {
		got, err := v1.ResolveLogHostPath("custom-sriov-network-operator")
		require.NoError(t, err)
		assert.Equal(t, "/var/log/custom-sriov-network-operator", got)
	})

	t.Run("rejects empty, root, traversal, tilde, and separators", func(t *testing.T) {
		for _, p := range []string{"", "/", "/var/log/../../etc", "/var/log/~/logs", "relative/dir", "-bad-name"} {
			_, err := v1.ResolveLogHostPath(p)
			assert.Error(t, err, "path %q", p)
		}
	})

	t.Run("rejects symlink that escapes /var/log", func(t *testing.T) {
		dir, err := os.MkdirTemp("/var/log", "sriov-op-test-*")
		if err != nil {
			t.Skip("cannot create test directory under /var/log: " + err.Error())
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })

		link := filepath.Join(dir, "escape")
		require.NoError(t, os.Symlink("/etc", link))

		_, err = v1.ResolveLogHostPath(link)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be under")
	})

	t.Run("rejects ancestor symlink escape when leaf path does not exist", func(t *testing.T) {
		dir, err := os.MkdirTemp("/var/log", "sriov-op-test-*")
		if err != nil {
			t.Skip("cannot create test directory under /var/log: " + err.Error())
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })

		link := filepath.Join(dir, "escape-parent")
		require.NoError(t, os.Symlink("/etc", link))

		_, err = v1.ResolveLogHostPath(filepath.Join(link, "missing-child"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be under")
	})
}
