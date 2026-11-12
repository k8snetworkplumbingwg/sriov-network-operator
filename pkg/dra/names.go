package dra

import "strings"

// ResourceNameToDeviceClassName converts a policy resourceName to a DNS-subdomain-safe
// DeviceClass metadata.name (lowercase alnum + hyphens; leading/trailing hyphens stripped;
// empty falls back to "sriov").
func ResourceNameToDeviceClassName(resourceName string) string {
	s := strings.ReplaceAll(resourceName, "_", "-")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + 32)
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "sriov"
	}
	return name
}
