package consts

import (
	"os"
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
