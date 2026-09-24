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

func ptr[T any](v T) *T { return &v }

func TestGetEffectiveLogConfig(t *testing.T) {
	t.Run("nil LogConfig returns defaults", func(t *testing.T) {
		got, err := v1.GetEffectiveLogConfig(nil)
		require.NoError(t, err)
		assert.Equal(t, vars.DefaultLogCfg(), got)
	})

	t.Run("empty LogConfig returns defaults", func(t *testing.T) {
		got, err := v1.GetEffectiveLogConfig(&v1.LogConfig{})
		require.NoError(t, err)
		assert.Equal(t, vars.DefaultLogCfg(), got)
	})

	t.Run("partial override keeps remaining defaults", func(t *testing.T) {
		got, err := v1.GetEffectiveLogConfig(&v1.LogConfig{MaxSizeMB: ptr(25)})
		require.NoError(t, err)
		d := vars.DefaultLogCfg()
		assert.Equal(t, 25, got.MaxSizeMB)
		assert.Equal(t, d.MaxFiles, got.MaxFiles)
		assert.Equal(t, d.MaxAgeDays, got.MaxAgeDays)
		assert.True(t, got.Enabled)
	})

	t.Run("folder-name HostPath resolves under /var/log", func(t *testing.T) {
		got, err := v1.GetEffectiveLogConfig(&v1.LogConfig{HostPath: ptr("custom-sriov-network-operator")})
		require.NoError(t, err)
		assert.Equal(t, "/var/log/custom-sriov-network-operator", got.HostPath)
	})

	t.Run("rejects HostPath outside /var/log", func(t *testing.T) {
		_, err := v1.GetEffectiveLogConfig(&v1.LogConfig{HostPath: ptr("/custom/log")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "/var/log")
	})

	t.Run("rejects path traversal", func(t *testing.T) {
		_, err := v1.GetEffectiveLogConfig(&v1.LogConfig{HostPath: ptr("/../escape")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "path traversal")
	})

	t.Run("rejects filesystem root", func(t *testing.T) {
		_, err := v1.GetEffectiveLogConfig(&v1.LogConfig{HostPath: ptr("/")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "filesystem root")
	})

	t.Run("SriovOperatorConfig method uses Spec.LogConfig", func(t *testing.T) {
		soc := &v1.SriovOperatorConfig{}
		got, err := soc.GetEffectiveLogConfig()
		require.NoError(t, err)
		assert.Equal(t, vars.DefaultLogCfg(), got)
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
