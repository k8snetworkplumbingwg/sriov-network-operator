package kubernetes_test

import (
	"context"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/orchestrator/kubernetes"
)

func TestBaremetal(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Kubernetes Suite")
}

var _ = Describe("Kubernetes Platform", func() {
	var (
		k8s  *kubernetes.Kubernetes
		node *corev1.Node
		err  error
	)

	BeforeEach(func() {
		k8s, err = kubernetes.New()
		Expect(err).NotTo(HaveOccurred())
		Expect(k8s).NotTo(BeNil())

		node = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
			},
		}
	})

	Context("Platform Identification", func() {
		It("should correctly identify the cluster type as Kubernetes", func() {
			Expect(k8s.ClusterType()).To(Equal(consts.ClusterTypeKubernetes))
		})

		It("should correctly identify the cluster flavor as Vanilla Kubernetes", func() {
			Expect(k8s.Flavor()).To(Equal(consts.ClusterFlavorDefault))
		})
	})

	Context("Node Drain Hooks", func() {
		It("should return true for the BeforeDrainNode hook", func() {
			shouldDrain, err := k8s.BeforeDrainNode(context.Background(), node)
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldDrain).To(BeTrue())
		})

		It("should return true for the AfterCompleteDrainNode hook", func() {
			shouldContinue, err := k8s.AfterCompleteDrainNode(context.Background(), node)
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldContinue).To(BeTrue())
		})
	})

	Context("TLS Configuration", Ordered, func() {
		BeforeAll(func() {
			origCipherSuites, hasCipherSuites := os.LookupEnv("TLS_CIPHER_SUITES")
			origMinVersion, hasMinVersion := os.LookupEnv("TLS_MIN_VERSION")

			DeferCleanup(func() {
				if hasCipherSuites {
					os.Setenv("TLS_CIPHER_SUITES", origCipherSuites)
				} else {
					os.Unsetenv("TLS_CIPHER_SUITES")
				}
				if hasMinVersion {
					os.Setenv("TLS_MIN_VERSION", origMinVersion)
				} else {
					os.Unsetenv("TLS_MIN_VERSION")
				}
			})
		})

		BeforeEach(func() {
			os.Unsetenv("TLS_CIPHER_SUITES")
			os.Unsetenv("TLS_MIN_VERSION")
		})

		It("should return nil when no TLS environment variables are set", func() {
			tlsConfig, err := k8s.GetTLSConfig(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).To(BeNil())
		})

		It("should return TLSConfig with all values when all env vars are set", func() {
			os.Setenv("TLS_CIPHER_SUITES", "ECDHE-RSA-AES128-GCM-SHA256,ECDHE-RSA-AES256-GCM-SHA384")
			os.Setenv("TLS_MIN_VERSION", "VersionTLS12")

			tlsConfig, err := k8s.GetTLSConfig(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).NotTo(BeNil())
			Expect(tlsConfig.CipherSuites).To(Equal("ECDHE-RSA-AES128-GCM-SHA256,ECDHE-RSA-AES256-GCM-SHA384"))
			Expect(tlsConfig.MinTLSVersion).To(Equal("VersionTLS12"))
		})

		It("should return TLSConfig with only cipher suites when only that env var is set", func() {
			os.Setenv("TLS_CIPHER_SUITES", "TLS_AES_128_GCM_SHA256")

			tlsConfig, err := k8s.GetTLSConfig(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).NotTo(BeNil())
			Expect(tlsConfig.CipherSuites).To(Equal("TLS_AES_128_GCM_SHA256"))
			Expect(tlsConfig.MinTLSVersion).To(BeEmpty())
		})

		It("should return TLSConfig with only min version when only that env var is set", func() {
			os.Setenv("TLS_MIN_VERSION", "VersionTLS13")

			tlsConfig, err := k8s.GetTLSConfig(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).NotTo(BeNil())
			Expect(tlsConfig.CipherSuites).To(BeEmpty())
			Expect(tlsConfig.MinTLSVersion).To(Equal("VersionTLS13"))
		})
	})
})
