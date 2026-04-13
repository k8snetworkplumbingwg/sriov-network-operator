package webhook

import (
	"crypto/tls"
	"fmt"
	"strings"

	openshiftcrypto "github.com/openshift/library-go/pkg/crypto"
	cliflag "k8s.io/component-base/cli/flag"
)

const DefaultMinTLSVersion = tls.VersionTLS12

// CipherNamesToIDs converts a slice of cipher names to Go cipher suite IDs.
// Cipher names may be provided in OpenSSL naming (from OpenShift TLS profiles)
// or IANA naming (used by Go crypto/tls and component-base).
func CipherNamesToIDs(names []string) ([]uint16, error) {
	if len(names) == 0 {
		return nil, nil
	}

	normalizedNames := make([]string, 0, len(names))
	for _, name := range names {
		trimmedName := strings.TrimSpace(name)
		if trimmedName == "" {
			return nil, fmt.Errorf("empty TLS cipher suite name")
		}
		normalizedNames = append(normalizedNames, openshiftCipherNameToIANA(trimmedName))
	}

	ids, err := cliflag.TLSCipherSuites(normalizedNames)
	if err != nil {
		return nil, err
	}

	insecureCipherIDs := map[uint16]struct{}{}
	for _, insecureID := range cliflag.InsecureTLSCiphers() {
		insecureCipherIDs[insecureID] = struct{}{}
	}

	for i, id := range ids {
		if _, found := insecureCipherIDs[id]; found {
			return nil, fmt.Errorf("TLS cipher suite %q is insecure and not allowed", normalizedNames[i])
		}
	}

	return ids, nil
}

// TLSVersionToGo converts an openshift/api TLSProtocolVersion string to a Go TLS version constant.
// Returns an error if the version string is not recognized.
func TLSVersionToGo(version string) (uint16, error) {
	trimmedVersion := strings.TrimSpace(version)
	if trimmedVersion == "" {
		return DefaultMinTLSVersion, nil
	}
	goVersion, err := cliflag.TLSVersion(trimmedVersion)
	if err != nil {
		return 0, err
	}
	if goVersion < tls.VersionTLS12 {
		return 0, fmt.Errorf("minimum TLS version must be VersionTLS12 or higher, got %q", trimmedVersion)
	}
	return goVersion, nil
}

// ParseCipherSuitesFlag parses a comma-separated string of cipher names into cipher suite IDs.
// Returns nil if the input is empty.
func ParseCipherSuitesFlag(cipherSuitesFlag string) ([]uint16, error) {
	trimmed := strings.TrimSpace(cipherSuitesFlag)
	if trimmed == "" {
		return nil, nil
	}
	return CipherNamesToIDs(strings.Split(trimmed, ","))
}

func openshiftCipherNameToIANA(cipherName string) string {
	converted := openshiftcrypto.OpenSSLToIANACipherSuites([]string{cipherName})
	if len(converted) == 1 {
		return converted[0]
	}
	return cipherName
}
