package kubernetes

import (
	"context"
	"os"

	corev1 "k8s.io/api/core/v1"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
)

// Kubernetes implements the orchestrator.Interface for vanilla Kubernetes clusters.
type Kubernetes struct{}

// New creates a new Kubernetes orchestrator instance.
func New() (*Kubernetes, error) {
	return &Kubernetes{}, nil
}

// Name returns the name of the Kubernetes orchestrator.
func (k *Kubernetes) Name() string {
	return "Kubernetes"
}

// ClusterType returns the cluster type for Kubernetes.
func (k *Kubernetes) ClusterType() consts.ClusterType {
	return consts.ClusterTypeKubernetes
}

// Flavor returns the cluster flavor for vanilla Kubernetes.
func (k *Kubernetes) Flavor() consts.ClusterFlavor {
	return consts.ClusterFlavorDefault
}

// BeforeDrainNode is a no-op for vanilla Kubernetes as no special preparation is needed.
// Always returns true to allow the drain to proceed immediately.
func (k *Kubernetes) BeforeDrainNode(_ context.Context, _ *corev1.Node) (bool, error) {
	return true, nil
}

// AfterCompleteDrainNode is a no-op for vanilla Kubernetes as no cleanup is needed.
// Always returns true to indicate completion.
func (k *Kubernetes) AfterCompleteDrainNode(_ context.Context, _ *corev1.Node) (bool, error) {
	return true, nil
}

// GetTLSConfig returns TLS configuration from environment variables for vanilla Kubernetes.
// Returns nil if no TLS environment variables are set (components use their defaults).
// Environment variables:
//   - TLS_CIPHER_SUITES: comma-separated list of cipher suites
//   - TLS_MIN_VERSION: minimum TLS version (e.g., VersionTLS12, VersionTLS13)
func (k *Kubernetes) GetTLSConfig(_ context.Context) (*consts.TLSConfig, error) {
	cipherSuites := os.Getenv("TLS_CIPHER_SUITES")
	minVersion := os.Getenv("TLS_MIN_VERSION")

	// If no TLS env vars are set, return nil to use component defaults
	if cipherSuites == "" && minVersion == "" {
		return nil, nil
	}

	return &consts.TLSConfig{
		CipherSuites:  cipherSuites,
		MinTLSVersion: minVersion,
	}, nil
}
