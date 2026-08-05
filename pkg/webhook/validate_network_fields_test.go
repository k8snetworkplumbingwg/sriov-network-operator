package webhook

import (
	"testing"
)

func TestValidateCapabilities(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		shouldFail bool
	}{
		{"empty", "", false},
		{"valid single", `{"mac": true}`, false},
		{"valid multiple", `{"mac": true, "ips": true}`, false},
		{"not JSON", `not json`, true},
		{"JSON array", `[true]`, true},
		{"JSON string", `"mac"`, true},
		{"non-bool value", `{"mac": "yes"}`, true},
		{"injection via value", `{"mac": true}, "evil": true`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCapabilities(tc.input)
			if tc.shouldFail && err == nil {
				t.Error("expected error but got none")
			}
			if !tc.shouldFail && err != nil {
				t.Errorf("expected no error but got: %v", err)
			}
		})
	}
}

func TestValidateIPAM(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		shouldFail bool
	}{
		{"empty", "", false},
		{"valid host-local", `{"type": "host-local", "subnet": "10.10.0.0/16"}`, false},
		{"valid dhcp", `{"type": "dhcp"}`, false},
		{"not JSON", `not json`, true},
		{"missing type", `{"subnet": "10.10.0.0/16"}`, true},
		{"type not string", `{"type": 123}`, true},
		{"JSON array", `[{"type": "dhcp"}]`, true},
		{"empty type", `{"type": ""}`, true},
		{"injection trailing comma", `{"type": "dhcp"}, "evil": true`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateIPAM(tc.input)
			if tc.shouldFail && err == nil {
				t.Error("expected error but got none")
			}
			if !tc.shouldFail && err != nil {
				t.Errorf("expected no error but got: %v", err)
			}
		})
	}
}

func TestValidateMetaPlugins(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		shouldFail bool
	}{
		{"empty", "", false},
		{"valid single plugin", `[{"type": "vrf", "vrfname": "blue"}]`, false},
		{"valid two plugins", `[{"type": "vrf"}, {"type": "tuning"}]`, false},
		{"not JSON", `not json`, true},
		{"not an array", `{"type": "vrf"}`, true},
		{"element missing type", `[{"vrfname": "blue"}]`, true},
		{"element empty type", `[{"type": ""}]`, true},
		{"element type not string", `[{"type": 123}]`, true},
		{"injection via object", `{"type": "vrf"}, "evil": true`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMetaPlugins(tc.input)
			if tc.shouldFail && err == nil {
				t.Error("expected error but got none")
			}
			if !tc.shouldFail && err != nil {
				t.Errorf("expected no error but got: %v", err)
			}
		})
	}
}

func TestValidateLogFile(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		shouldFail bool
	}{
		{"empty", "", false},
		{"simple filename", "cni.log", false},
		{"relative path", "sriov/cni.log", false},
		{"quote injection", `cni","evil":"true`, true},
		{"backslash", `cni\log`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLogFile(tc.input)
			if tc.shouldFail && err == nil {
				t.Error("expected error but got none")
			}
			if !tc.shouldFail && err != nil {
				t.Errorf("expected no error but got: %v", err)
			}
		})
	}
}

func TestValidateBridgeName(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		shouldFail bool
	}{
		{"empty", "", false},
		{"valid", "br-sriov", false},
		{"valid with dot", "br0.1", false},
		{"too long", "abcdefghijklmnop", true},
		{"starts with dash", "-br0", true},
		{"special chars", "br;rm -rf /", true},
		{"quote injection", `br","evil":"x`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBridgeName(tc.input)
			if tc.shouldFail && err == nil {
				t.Error("expected error but got none")
			}
			if !tc.shouldFail && err != nil {
				t.Errorf("expected no error but got: %v", err)
			}
		})
	}
}

func TestValidateInterfaceType(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		shouldFail bool
	}{
		{"empty", "", false},
		{"system", "system", false},
		{"dpdk", "dpdk", false},
		{"internal", "internal", false},
		{"with underscore", "my_type", false},
		{"with dash", "my-type", false},
		{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
		{"special chars", "type;echo", true},
		{"quote injection", `type","evil":"x`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateInterfaceType(tc.input)
			if tc.shouldFail && err == nil {
				t.Error("expected error but got none")
			}
			if !tc.shouldFail && err != nil {
				t.Errorf("expected no error but got: %v", err)
			}
		})
	}
}
