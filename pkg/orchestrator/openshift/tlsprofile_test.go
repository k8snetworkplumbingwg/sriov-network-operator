package openshift_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	openshiftconfigv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/orchestrator/openshift"
)

var _ = Describe("TLS Profile", func() {
	var orchestrator *openshift.OpenshiftOrchestrator
	var ctx context.Context

	BeforeEach(func() {
		var err error
		ctx = context.Background()
		orchestrator, err = openshift.New()
		Expect(err).NotTo(HaveOccurred())
	})

	Context("when APIServer has no tlsSecurityProfile", func() {
		BeforeEach(func() {
			apiServer := &openshiftconfigv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: openshiftconfigv1.APIServerSpec{
					Audit: openshiftconfigv1.Audit{
						Profile: openshiftconfigv1.DefaultAuditProfileType,
					},
				},
			}
			err := k8sClient.Create(ctx, apiServer)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				err := k8sClient.Delete(ctx, apiServer)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("should return nil TLSConfig (use component defaults)", func() {
			tlsConfig, err := orchestrator.GetTLSConfig(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).To(BeNil())
		})
	})

	Context("when APIServer has Intermediate profile", func() {
		BeforeEach(func() {
			apiServer := &openshiftconfigv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: openshiftconfigv1.APIServerSpec{
					TLSSecurityProfile: &openshiftconfigv1.TLSSecurityProfile{
						Type: openshiftconfigv1.TLSProfileIntermediateType,
					},
				},
			}
			err := k8sClient.Create(ctx, apiServer)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				err := k8sClient.Delete(ctx, apiServer)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("should return resolved Intermediate profile", func() {
			tlsConfig, err := orchestrator.GetTLSConfig(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).NotTo(BeNil())
			Expect(tlsConfig.MinTLSVersion).To(Equal("VersionTLS12"))
			Expect(tlsConfig.CipherSuites).NotTo(BeEmpty())
		})
	})

	Context("when APIServer has Modern profile", func() {
		BeforeEach(func() {
			apiServer := &openshiftconfigv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: openshiftconfigv1.APIServerSpec{
					TLSSecurityProfile: &openshiftconfigv1.TLSSecurityProfile{
						Type: openshiftconfigv1.TLSProfileModernType,
					},
				},
			}
			err := k8sClient.Create(ctx, apiServer)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				err := k8sClient.Delete(ctx, apiServer)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("should return resolved Modern profile with TLS 1.3", func() {
			tlsConfig, err := orchestrator.GetTLSConfig(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).NotTo(BeNil())
			Expect(tlsConfig.MinTLSVersion).To(Equal("VersionTLS13"))
		})
	})

	Context("when APIServer has Old profile", func() {
		BeforeEach(func() {
			apiServer := &openshiftconfigv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: openshiftconfigv1.APIServerSpec{
					TLSSecurityProfile: &openshiftconfigv1.TLSSecurityProfile{
						Type: openshiftconfigv1.TLSProfileOldType,
					},
				},
			}
			err := k8sClient.Create(ctx, apiServer)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				err := k8sClient.Delete(ctx, apiServer)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("should enforce TLS 1.2 floor for Old profile", func() {
			tlsConfig, err := orchestrator.GetTLSConfig(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).NotTo(BeNil())
			Expect(tlsConfig.MinTLSVersion).To(Equal("VersionTLS12"))
		})
	})

	Context("when APIServer has Custom profile with full config", func() {
		BeforeEach(func() {
			apiServer := &openshiftconfigv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: openshiftconfigv1.APIServerSpec{
					TLSSecurityProfile: &openshiftconfigv1.TLSSecurityProfile{
						Type: openshiftconfigv1.TLSProfileCustomType,
						Custom: &openshiftconfigv1.CustomTLSProfile{
							TLSProfileSpec: openshiftconfigv1.TLSProfileSpec{
								Ciphers:       []string{"ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES128-GCM-SHA256"},
								MinTLSVersion: openshiftconfigv1.VersionTLS12,
							},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, apiServer)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				err := k8sClient.Delete(ctx, apiServer)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("should return custom ciphers and minVersion", func() {
			tlsConfig, err := orchestrator.GetTLSConfig(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).NotTo(BeNil())
			Expect(tlsConfig.CipherSuites).To(Equal("TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"))
			Expect(tlsConfig.MinTLSVersion).To(Equal("VersionTLS12"))
		})
	})

	Context("when APIServer has Custom profile with ciphers only", func() {
		BeforeEach(func() {
			apiServer := &openshiftconfigv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: openshiftconfigv1.APIServerSpec{
					TLSSecurityProfile: &openshiftconfigv1.TLSSecurityProfile{
						Type: openshiftconfigv1.TLSProfileCustomType,
						Custom: &openshiftconfigv1.CustomTLSProfile{
							TLSProfileSpec: openshiftconfigv1.TLSProfileSpec{
								Ciphers: []string{"TLS_AES_128_GCM_SHA256"},
							},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, apiServer)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				err := k8sClient.Delete(ctx, apiServer)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("should return only CipherSuites set", func() {
			tlsConfig, err := orchestrator.GetTLSConfig(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).NotTo(BeNil())
			Expect(tlsConfig.CipherSuites).To(Equal("TLS_AES_128_GCM_SHA256"))
			Expect(tlsConfig.MinTLSVersion).To(BeEmpty())
		})
	})

	Context("when APIServer has Custom profile with minTLSVersion only", func() {
		BeforeEach(func() {
			apiServer := &openshiftconfigv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: openshiftconfigv1.APIServerSpec{
					TLSSecurityProfile: &openshiftconfigv1.TLSSecurityProfile{
						Type: openshiftconfigv1.TLSProfileCustomType,
						Custom: &openshiftconfigv1.CustomTLSProfile{
							TLSProfileSpec: openshiftconfigv1.TLSProfileSpec{
								MinTLSVersion: openshiftconfigv1.VersionTLS13,
							},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, apiServer)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				err := k8sClient.Delete(ctx, apiServer)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("should return only MinTLSVersion set", func() {
			tlsConfig, err := orchestrator.GetTLSConfig(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).NotTo(BeNil())
			Expect(tlsConfig.CipherSuites).To(BeEmpty())
			Expect(tlsConfig.MinTLSVersion).To(Equal("VersionTLS13"))
		})
	})

	Context("when APIServer custom profile requests weak TLS minimum", func() {
		BeforeEach(func() {
			apiServer := &openshiftconfigv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: openshiftconfigv1.APIServerSpec{
					TLSSecurityProfile: &openshiftconfigv1.TLSSecurityProfile{
						Type: openshiftconfigv1.TLSProfileCustomType,
						Custom: &openshiftconfigv1.CustomTLSProfile{
							TLSProfileSpec: openshiftconfigv1.TLSProfileSpec{
								MinTLSVersion: openshiftconfigv1.VersionTLS11,
							},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, apiServer)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				err := k8sClient.Delete(ctx, apiServer)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("should clamp to VersionTLS12", func() {
			tlsConfig, err := orchestrator.GetTLSConfig(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(tlsConfig).NotTo(BeNil())
			Expect(tlsConfig.MinTLSVersion).To(Equal("VersionTLS12"))
		})
	})
})
