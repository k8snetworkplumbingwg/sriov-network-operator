package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/daemon"
	snolog "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/log"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

var _ = Describe("Daemon OperatorConfig Controller", Ordered, func() {
	var cancel context.CancelFunc
	var ctx context.Context

	BeforeAll(func() {
		By("Setup controller manager")
		k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme.Scheme,
		})
		Expect(err).ToNot(HaveOccurred())

		configController := daemon.NewOperatorConfigNodeReconcile(k8sClient)
		err = configController.SetupWithManager(k8sManager)
		Expect(err).ToNot(HaveOccurred())

		ctx, cancel = context.WithCancel(context.Background())

		wg := sync.WaitGroup{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer GinkgoRecover()
			By("Start controller manager")
			err := k8sManager.Start(ctx)
			Expect(err).ToNot(HaveOccurred())
		}()

		DeferCleanup(func() {
			By("Shutdown controller manager")
			cancel()
			wg.Wait()
		})

		err = k8sClient.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovOperatorConfig{}, client.InNamespace(testNamespace))).ToNot(HaveOccurred())
	})

	Context("LogLevel", func() {
		It("should configure the log level base on sriovOperatorConfig", func() {
			soc := &sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{
				Name:      consts.DefaultConfigName,
				Namespace: testNamespace,
			},
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{
					LogLevel: 1,
				},
			}

			err := k8sClient.Create(ctx, soc)
			Expect(err).ToNot(HaveOccurred())
			validateExpectedLogLevel(1)

		})

		It("should update the log level in runtime", func() {
			soc := &sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{
				Name:      consts.DefaultConfigName,
				Namespace: testNamespace,
			},
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{
					LogLevel: 1,
				},
			}

			err := k8sClient.Create(ctx, soc)
			Expect(err).ToNot(HaveOccurred())
			validateExpectedLogLevel(1)

			soc.Spec.LogLevel = 2
			err = k8sClient.Update(ctx, soc)
			Expect(err).ToNot(HaveOccurred())
			validateExpectedLogLevel(2)
		})
	})

	Context("Disable Drain", func() {
		It("should update the skip drain flag", func() {
			soc := &sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{
				Name:      consts.DefaultConfigName,
				Namespace: testNamespace,
			},
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{
					DisableDrain: true,
				},
			}

			err := k8sClient.Create(ctx, soc)
			Expect(err).ToNot(HaveOccurred())
			validateExpectedDrain(true)

			soc.Spec.DisableDrain = false
			err = k8sClient.Update(ctx, soc)
			Expect(err).ToNot(HaveOccurred())
			validateExpectedDrain(false)
		})
	})

	Context("LogConfig", func() {
		var savedLogCfg vars.LogFileSettings

		BeforeEach(func() {
			savedLogCfg = vars.GetLogCfg()
		})

		AfterEach(func() {
			vars.SetLogCfg(savedLogCfg)
			snolog.CloseFileLogger()
		})

		It("should update LogCfg when LogConfig is set", func() {
			maxSize := 50
			maxFiles := 3
			maxAge := 7
			compress := false
			hostPath := "/var/log/sriov-test"

			soc := &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      consts.DefaultConfigName,
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{
					LogConfig: &sriovnetworkv1.LogConfig{
						MaxSizeMB:  &maxSize,
						MaxFiles:   &maxFiles,
						MaxAgeDays: &maxAge,
						Compress:   &compress,
						HostPath:   &hostPath,
					},
				},
			}
			Expect(k8sClient.Create(ctx, soc)).To(Succeed())

			EventuallyWithOffset(1, func(g Gomega) {
				cfg := vars.GetLogCfg()
				g.Expect(cfg.MaxSizeMB).To(Equal(50))
				g.Expect(cfg.MaxFiles).To(Equal(3))
				g.Expect(cfg.MaxAgeDays).To(Equal(7))
				g.Expect(cfg.Compress).To(BeFalse())
				g.Expect(cfg.HostPath).To(Equal("/var/log/sriov-test"))
				g.Expect(cfg.Enabled).To(BeTrue())
			}, "15s", "3s").Should(Succeed())
		})

		It("should disable file logging when Enabled=false is set", func() {
			enabled := false
			soc := &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      consts.DefaultConfigName,
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{
					LogConfig: &sriovnetworkv1.LogConfig{
						Enabled: &enabled,
					},
				},
			}
			Expect(k8sClient.Create(ctx, soc)).To(Succeed())

			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(vars.GetLogCfg().Enabled).To(BeFalse())
			}, "15s", "3s").Should(Succeed())
		})

		It("should revert to defaults when LogConfig is removed", func() {
			maxSize := 25
			soc := &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      consts.DefaultConfigName,
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{
					LogConfig: &sriovnetworkv1.LogConfig{
						MaxSizeMB: &maxSize,
					},
				},
			}
			Expect(k8sClient.Create(ctx, soc)).To(Succeed())

			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(vars.GetLogCfg().MaxSizeMB).To(Equal(25))
			}, "15s", "3s").Should(Succeed())

			soc.Spec.LogConfig = nil
			Expect(k8sClient.Update(ctx, soc)).To(Succeed())

			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(vars.GetLogCfg().MaxSizeMB).To(Equal(vars.DefaultLogCfg().MaxSizeMB))
			}, "15s", "3s").Should(Succeed())
		})
	})

	Context("Feature gates", func() {
		It("should update the feature gates struct", func() {
			soc := &sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{
				Name:      consts.DefaultConfigName,
				Namespace: testNamespace,
			},
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{
					FeatureGates: map[string]bool{
						"test": true,
						"bla":  true,
					},
				},
			}

			err := k8sClient.Create(ctx, soc)
			Expect(err).ToNot(HaveOccurred())
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(vars.FeatureGate.IsEnabled("test")).To(BeTrue())
			}, "15s", "3s").Should(Succeed())
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(vars.FeatureGate.IsEnabled("bla")).To(BeTrue())
			}, "15s", "3s").Should(Succeed())

			soc.Spec.FeatureGates["test"] = false
			err = k8sClient.Update(ctx, soc)
			Expect(err).ToNot(HaveOccurred())
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(vars.FeatureGate.IsEnabled("test")).To(BeFalse())
			}, "15s", "3s").Should(Succeed())
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(vars.FeatureGate.IsEnabled("bla")).To(BeTrue())
			}, "15s", "3s").Should(Succeed())
		})
	})
})

func validateExpectedLogLevel(level int) {
	EventuallyWithOffset(1, func(g Gomega) {
		g.Expect(snolog.GetLogLevel()).To(Equal(level))
	}, "15s", "3s").Should(Succeed())
}

func validateExpectedDrain(disableDrain bool) {
	EventuallyWithOffset(1, func(g Gomega) {
		g.Expect(vars.DisableDrain).To(Equal(disableDrain))
	}, "15s", "3s").Should(Succeed())
}

func ptr[T any](v T) *T { return &v }

var _ = Describe("GetEffectiveLogConfig", func() {
	It("returns defaults when LogConfig is nil", func() {
		got, err := sriovnetworkv1.GetEffectiveLogConfig(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(vars.DefaultLogCfg()))
	})

	It("is available as a method on SriovOperatorConfig", func() {
		soc := &sriovnetworkv1.SriovOperatorConfig{}
		got, err := soc.GetEffectiveLogConfig()
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(vars.DefaultLogCfg()))
	})

	It("returns defaults for an empty LogConfig struct", func() {
		got, err := sriovnetworkv1.GetEffectiveLogConfig(&sriovnetworkv1.LogConfig{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(vars.DefaultLogCfg()))
	})

	It("applies a partial override while keeping remaining defaults", func() {
		got, err := sriovnetworkv1.GetEffectiveLogConfig(&sriovnetworkv1.LogConfig{MaxSizeMB: ptr(25)})
		Expect(err).NotTo(HaveOccurred())
		d := vars.DefaultLogCfg()

		Expect(got.MaxSizeMB).To(Equal(25))
		Expect(got.MaxFiles).To(Equal(d.MaxFiles))
		Expect(got.MaxAgeDays).To(Equal(d.MaxAgeDays))
		Expect(got.Compress).To(Equal(d.Compress))
		Expect(got.Enabled).To(BeTrue())
	})

	It("applies a full override", func() {
		got, err := sriovnetworkv1.GetEffectiveLogConfig(&sriovnetworkv1.LogConfig{
			Enabled:    ptr(false),
			MaxSizeMB:  ptr(50),
			MaxFiles:   ptr(3),
			MaxAgeDays: ptr(7),
			Compress:   ptr(false),
			HostPath:   ptr("/var/log/custom/log"),
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(got.Enabled).To(BeFalse())
		Expect(got.MaxSizeMB).To(Equal(50))
		Expect(got.MaxFiles).To(Equal(3))
		Expect(got.MaxAgeDays).To(Equal(7))
		Expect(got.Compress).To(BeFalse())
		Expect(got.HostPath).To(Equal("/var/log/custom/log"))
	})

	It("resolves a folder-name HostPath under /var/log", func() {
		got, err := sriovnetworkv1.GetEffectiveLogConfig(&sriovnetworkv1.LogConfig{
			HostPath: ptr("custom-sriov-network-operator"),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.HostPath).To(Equal("/var/log/custom-sriov-network-operator"))
	})

	It("accepts a nested absolute path under /var/log", func() {
		got, err := sriovnetworkv1.GetEffectiveLogConfig(&sriovnetworkv1.LogConfig{
			HostPath: ptr("/var/log/custom-sriov-network-operator/first_rotation_logs/"),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.HostPath).To(Equal("/var/log/custom-sriov-network-operator/first_rotation_logs"))
	})

	It("rejects a HostPath outside /var/log", func() {
		_, err := sriovnetworkv1.GetEffectiveLogConfig(&sriovnetworkv1.LogConfig{HostPath: ptr("/custom/log")})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("/var/log"))
	})

	It("does not override HostPath when it is an empty string", func() {
		got, err := sriovnetworkv1.GetEffectiveLogConfig(&sriovnetworkv1.LogConfig{HostPath: ptr("")})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.HostPath).To(Equal(vars.DefaultLogCfg().HostPath))
	})

	It("always starts from canonical defaults regardless of current LogCfg state", func() {
		saved := vars.GetLogCfg()
		modified := saved
		modified.MaxSizeMB = 999
		vars.SetLogCfg(modified)
		defer func() { vars.SetLogCfg(saved) }()

		got, err := sriovnetworkv1.GetEffectiveLogConfig(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.MaxSizeMB).To(Equal(vars.DefaultLogCfg().MaxSizeMB))
	})

	It("allows MaxAgeDays of zero", func() {
		got, err := sriovnetworkv1.GetEffectiveLogConfig(&sriovnetworkv1.LogConfig{MaxAgeDays: ptr(0)})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.MaxAgeDays).To(Equal(0))
	})

	It("rejects a HostPath with path traversal", func() {
		_, err := sriovnetworkv1.GetEffectiveLogConfig(&sriovnetworkv1.LogConfig{HostPath: ptr("/../escape")})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("path traversal"))
	})

	It("rejects a HostPath that is the filesystem root", func() {
		_, err := sriovnetworkv1.GetEffectiveLogConfig(&sriovnetworkv1.LogConfig{HostPath: ptr("/")})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("filesystem root"))
	})

	It("rejects a folder-name HostPath that contains path separators", func() {
		_, err := sriovnetworkv1.GetEffectiveLogConfig(&sriovnetworkv1.LogConfig{HostPath: ptr("relative/path")})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("path separators"))
	})
})

var _ = Describe("ResolveLogHostPath", func() {
	It("accepts a valid absolute path under /var/log", func() {
		got, err := sriovnetworkv1.ResolveLogHostPath("/var/log/sriov")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("/var/log/sriov"))
	})

	It("resolves a folder name under /var/log", func() {
		got, err := sriovnetworkv1.ResolveLogHostPath("custom-sriov-network-operator")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("/var/log/custom-sriov-network-operator"))
	})

	It("normalizes a trailing slash on absolute paths", func() {
		got, err := sriovnetworkv1.ResolveLogHostPath("/var/log/custom-sriov-network-operator/first_rotation_logs/")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("/var/log/custom-sriov-network-operator/first_rotation_logs"))
	})

	It("rejects an empty path", func() {
		_, err := sriovnetworkv1.ResolveLogHostPath("")
		Expect(err).To(HaveOccurred())
	})

	It("rejects root path", func() {
		_, err := sriovnetworkv1.ResolveLogHostPath("/")
		Expect(err).To(HaveOccurred())
	})

	It("accepts /var/log itself", func() {
		got, err := sriovnetworkv1.ResolveLogHostPath("/var/log")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("/var/log"))
	})

	It("rejects path traversal with ..", func() {
		_, err := sriovnetworkv1.ResolveLogHostPath("/var/log/../../etc")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("path traversal"))
	})

	It("rejects a path outside /var/log", func() {
		_, err := sriovnetworkv1.ResolveLogHostPath("/tmp/logs")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("/var/log"))
	})

	It("rejects a folder name with path separators", func() {
		_, err := sriovnetworkv1.ResolveLogHostPath("relative/dir")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("path separators"))
	})

	It("rejects an invalid folder name", func() {
		_, err := sriovnetworkv1.ResolveLogHostPath("-bad-name")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid"))
	})

	It("rejects a tilde in the path", func() {
		_, err := sriovnetworkv1.ResolveLogHostPath("/var/log/~/logs")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("~"))
	})

	It("rejects a symlink that escapes /var/log", func() {
		dir, err := os.MkdirTemp("/var/log", "sriov-op-test-*")
		if err != nil {
			Skip("cannot create test directory under /var/log: " + err.Error())
		}
		DeferCleanup(func() { _ = os.RemoveAll(dir) })

		link := filepath.Join(dir, "escape")
		Expect(os.Symlink("/etc", link)).To(Succeed())

		_, err = sriovnetworkv1.ResolveLogHostPath(link)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must be under"))
	})
})
