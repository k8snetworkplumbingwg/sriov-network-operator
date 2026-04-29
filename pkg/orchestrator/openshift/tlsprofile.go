package openshift

import (
	"context"
	"crypto/tls"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	cliflag "k8s.io/component-base/cli/flag"

	configv1 "github.com/openshift/api/config/v1"
	openshiftcrypto "github.com/openshift/library-go/pkg/crypto"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
)

// GetTLSConfig retrieves the cluster-wide TLS security profile from the OpenShift APIServer resource.
// Returns nil if no TLSSecurityProfile is configured on the cluster (preserving backward compatibility
// by using component defaults).
func (c *OpenshiftOrchestrator) GetTLSConfig(ctx context.Context) (*consts.TLSConfig, error) {
	apiServer := &configv1.APIServer{}
	err := c.kubeClient.Get(ctx, types.NamespacedName{Name: "cluster"}, apiServer)
	if err != nil {
		return nil, err
	}

	// If no TLSSecurityProfile is configured, return nil to preserve
	// existing component defaults (backward compatibility)
	if apiServer.Spec.TLSSecurityProfile == nil {
		return nil, nil
	}

	// Only resolve when explicitly configured
	profile := apiServer.Spec.TLSSecurityProfile
	var spec *configv1.TLSProfileSpec

	switch profile.Type {
	case configv1.TLSProfileCustomType:
		if profile.Custom != nil {
			spec = &profile.Custom.TLSProfileSpec
		}
	case configv1.TLSProfileOldType,
		configv1.TLSProfileIntermediateType,
		configv1.TLSProfileModernType:
		spec = configv1.TLSProfiles[profile.Type]
	}

	if spec == nil {
		return nil, nil // Unknown profile type or empty custom, use defaults
	}

	// Build result with only the fields that are actually configured
	// Empty strings mean "use component default" - don't pass flag
	result := &consts.TLSConfig{}

	// Ciphers - only set if configured (supports partial config)
	if len(spec.Ciphers) > 0 {
		convertedCiphers := openshiftCipherSuitesToIANAAndSecure(spec.Ciphers)
		if len(convertedCiphers) > 0 {
			result.CipherSuites = strings.Join(convertedCiphers, ",")
		}
	}

	// MinTLSVersion - only set if configured
	if spec.MinTLSVersion != "" {
		if minVersion, err := cliflag.TLSVersion(string(spec.MinTLSVersion)); err == nil && minVersion >= tls.VersionTLS12 {
			result.MinTLSVersion = string(spec.MinTLSVersion)
		} else {
			result.MinTLSVersion = "VersionTLS12"
		}
	}

	return result, nil
}

func openshiftCipherSuitesToIANAAndSecure(cipherSuites []string) []string {
	insecureCipherIDs := map[uint16]struct{}{}
	for _, insecureID := range cliflag.InsecureTLSCiphers() {
		insecureCipherIDs[insecureID] = struct{}{}
	}

	result := make([]string, 0, len(cipherSuites))
	for _, cipherSuite := range cipherSuites {
		normalized := openshiftCipherSuiteNameToIANA(cipherSuite)
		ids, err := cliflag.TLSCipherSuites([]string{normalized})
		if err != nil || len(ids) != 1 {
			continue
		}
		if _, found := insecureCipherIDs[ids[0]]; found {
			continue
		}
		result = append(result, normalized)
	}
	return result
}

func openshiftCipherSuiteNameToIANA(cipherSuite string) string {
	converted := openshiftcrypto.OpenSSLToIANACipherSuites([]string{cipherSuite})
	if len(converted) == 1 {
		return converted[0]
	}
	return cipherSuite
}
