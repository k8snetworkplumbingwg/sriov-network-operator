package consts

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGetSriovConfBasePath(t *testing.T) {
	originalValue, hadOriginalValue := os.LookupEnv(sriovConfBasePathEnvVar)
	defer func() {
		if hadOriginalValue {
			_ = os.Setenv(sriovConfBasePathEnvVar, originalValue)
			return
		}

		_ = os.Unsetenv(sriovConfBasePathEnvVar)
	}()

	_ = os.Unsetenv(sriovConfBasePathEnvVar)
	if got := getSriovConfBasePath(); got != defaultSriovConfBasePath {
		t.Fatalf("expected default path %q, got %q", defaultSriovConfBasePath, got)
	}

	const customPath = "/tmp/custom"
	_ = os.Setenv(sriovConfBasePathEnvVar, customPath)
	if got := getSriovConfBasePath(); got != customPath {
		t.Fatalf("expected overridden path %q, got %q", customPath, got)
	}
}

func TestGetUdevBasePath(t *testing.T) {
	originalValue, hadOriginalValue := os.LookupEnv(udevBasePathEnvVar)
	defer func() {
		if hadOriginalValue {
			_ = os.Setenv(udevBasePathEnvVar, originalValue)
			return
		}

		_ = os.Unsetenv(udevBasePathEnvVar)
	}()

	_ = os.Unsetenv(udevBasePathEnvVar)
	if got := getUdevBasePath(); got != defaultUdevBasePath {
		t.Fatalf("expected default path %q, got %q", defaultUdevBasePath, got)
	}

	const customPath = "/run/udev"
	_ = os.Setenv(udevBasePathEnvVar, customPath)
	if got := getUdevBasePath(); got != customPath {
		t.Fatalf("expected overridden path %q, got %q", customPath, got)
	}
}

func TestDisableNMScriptHonorsUdevBasePath(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}

	scriptPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "bindata", "scripts", "udev-find-sriov-pf.sh")
	content, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("failed to read helper script: %v", err)
	}

	script := string(content)
	if !strings.Contains(script, `udev_base_path="${UDEV_BASE_PATH:-/etc/udev}"`) {
		t.Fatal("expected helper script to read UDEV_BASE_PATH")
	}
	if !strings.Contains(script, `target_file="${target_dir}/disable-nm-sriov.sh"`) {
		t.Fatal("expected helper script to write disable-nm-sriov.sh under the configured udev base path")
	}
}
