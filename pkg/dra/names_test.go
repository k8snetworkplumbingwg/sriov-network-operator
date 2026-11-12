package dra

import "testing"

func TestResourceNameToDeviceClassName(t *testing.T) {
	testCases := []struct {
		name         string
		resourceName string
		expected     string
	}{
		{"empty returns sriov", "", "sriov"},
		{"underscores to dashes", "intel_nic", "intel-nic"},
		{"uppercase to lowercase", "IntelNic", "intelnic"},
		{"mixed", "My_Resource_01", "my-resource-01"},
		{"trailing hyphen trimmed", "a1-", "a1"},
		{"non-DNS chars stripped", "foo.bar", "foobar"},
		{"leading/trailing hyphens trimmed", "-x-", "x"},
		{"leading underscores become hyphens then trimmed", "_foo", "foo"},
		{"only underscores/dashes becomes sriov", "___", "sriov"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResourceNameToDeviceClassName(tc.resourceName)
			if got != tc.expected {
				t.Errorf("ResourceNameToDeviceClassName(%q) = %q, want %q", tc.resourceName, got, tc.expected)
			}
		})
	}
}
