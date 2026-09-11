package daemon

import (
	"testing"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

func ptr[T any](v T) *T { return &v }

func TestEffectiveLogCfg_EmptyStructReturnsDefaults(t *testing.T) {
	got := effectiveLogCfg(&sriovnetworkv1.LogConfig{})
	want := vars.DefaultLogCfg()
	if got != want {
		t.Errorf("effectiveLogCfg(&LogConfig{}) = %+v, want %+v", got, want)
	}
}

func TestEffectiveLogCfg_PartialOverride(t *testing.T) {
	got := effectiveLogCfg(&sriovnetworkv1.LogConfig{MaxSizeMB: ptr(25)})
	d := vars.DefaultLogCfg()
	if got.MaxSizeMB != 25 {
		t.Errorf("MaxSizeMB = %d, want 25", got.MaxSizeMB)
	}
	if got.MaxFiles != d.MaxFiles {
		t.Errorf("MaxFiles = %d, want default %d", got.MaxFiles, d.MaxFiles)
	}
	if got.MaxAgeDays != d.MaxAgeDays {
		t.Errorf("MaxAgeDays = %d, want default %d", got.MaxAgeDays, d.MaxAgeDays)
	}
	if got.Compress != d.Compress {
		t.Errorf("Compress = %v, want default %v", got.Compress, d.Compress)
	}
	if !got.Enabled {
		t.Error("Enabled should be true (default)")
	}
}

func TestEffectiveLogCfg_FullOverride(t *testing.T) {
	got := effectiveLogCfg(&sriovnetworkv1.LogConfig{
		Enabled:    ptr(false),
		MaxSizeMB:  ptr(50),
		MaxFiles:   ptr(3),
		MaxAgeDays: ptr(7),
		Compress:   ptr(false),
		HostPath:   ptr("/custom/log"),
	})
	if got.Enabled {
		t.Error("Enabled should be false")
	}
	if got.MaxSizeMB != 50 {
		t.Errorf("MaxSizeMB = %d, want 50", got.MaxSizeMB)
	}
	if got.MaxFiles != 3 {
		t.Errorf("MaxFiles = %d, want 3", got.MaxFiles)
	}
	if got.MaxAgeDays != 7 {
		t.Errorf("MaxAgeDays = %d, want 7", got.MaxAgeDays)
	}
	if got.Compress {
		t.Error("Compress should be false")
	}
	if got.HostPath != "/custom/log" {
		t.Errorf("HostPath = %q, want /custom/log", got.HostPath)
	}
}

func TestEffectiveLogCfg_EmptyHostPathNotOverridden(t *testing.T) {
	got := effectiveLogCfg(&sriovnetworkv1.LogConfig{HostPath: ptr("")})
	if got.HostPath != vars.DefaultLogCfg().HostPath {
		t.Errorf("HostPath = %q, want default %q", got.HostPath, vars.DefaultLogCfg().HostPath)
	}
}

func TestEffectiveLogCfg_AlwaysStartsFromDefaults(t *testing.T) {
	saved := vars.LogCfg
	vars.LogCfg.MaxSizeMB = 999
	defer func() { vars.LogCfg = saved }()

	got := effectiveLogCfg(nil)
	if got.MaxSizeMB != vars.DefaultLogCfg().MaxSizeMB {
		t.Errorf("MaxSizeMB = %d, want canonical default %d (not stale vars.LogCfg)",
			got.MaxSizeMB, vars.DefaultLogCfg().MaxSizeMB)
	}
}

func TestEffectiveLogCfg_MaxAgeDaysZeroAllowed(t *testing.T) {
	got := effectiveLogCfg(&sriovnetworkv1.LogConfig{MaxAgeDays: ptr(0)})
	if got.MaxAgeDays != 0 {
		t.Errorf("MaxAgeDays = %d, want 0", got.MaxAgeDays)
	}
}
