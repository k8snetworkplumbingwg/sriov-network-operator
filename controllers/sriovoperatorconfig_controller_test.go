package controllers

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.uber.org/mock/gomock"
	admv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
	orchestratorMock "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/orchestrator/mock"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util"
)

var _ = Describe("SriovOperatorConfig controller", Ordered, func() {
	var cancel context.CancelFunc
	var ctx context.Context

	BeforeAll(func() {
		By("Create SriovOperatorConfig controller k8s objs")
		config := makeDefaultSriovOpConfig()
		Expect(k8sClient.Create(context.Background(), config)).Should(Succeed())

		somePolicy := &sriovnetworkv1.SriovNetworkNodePolicy{}
		somePolicy.SetNamespace(testNamespace)
		somePolicy.SetName("some-policy")
		somePolicy.Spec = sriovnetworkv1.SriovNetworkNodePolicySpec{
			NumVfs:       5,
			NodeSelector: map[string]string{"foo": "bar"},
			NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{},
			Priority:     20,
		}
		Expect(k8sClient.Create(context.Background(), somePolicy)).ToNot(HaveOccurred())

		// setup controller manager
		By("Setup controller manager")
		k8sManager, err := setupK8sManagerForTest()
		Expect(err).ToNot(HaveOccurred())

		t := GinkgoT()
		mockCtrl := gomock.NewController(t)
		orchestrator := orchestratorMock.NewMockInterface(mockCtrl)

		orchestrator.EXPECT().ClusterType().DoAndReturn(func() consts.ClusterType {
			if vars.ClusterType == consts.ClusterTypeOpenshift {
				return consts.ClusterTypeOpenshift
			}
			return consts.ClusterTypeKubernetes
		}).AnyTimes()

		// TODO: Change this to add tests for hypershift
		orchestrator.EXPECT().Flavor().DoAndReturn(func() consts.ClusterFlavor {
			if vars.ClusterType == consts.ClusterTypeOpenshift {
				return consts.DefaultConfigName
			}
			return consts.ClusterFlavorVanillaK8s
		}).AnyTimes()

		err = (&SriovOperatorConfigReconciler{
			Client:            k8sManager.GetClient(),
			Scheme:            k8sManager.GetScheme(),
			Orchestrator:      orchestrator,
			FeatureGate:       featuregate.New(),
			UncachedAPIReader: k8sManager.GetAPIReader(),
		}).SetupWithManager(k8sManager)
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
			By("Shut down manager")
			cancel()
			wg.Wait()
		})
	})

	Context("When is up", func() {
		AfterAll(func() {
			err := k8sClient.DeleteAllOf(context.Background(), &corev1.Node{})
			Expect(err).ToNot(HaveOccurred())

			err = k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovNetworkNodePolicy{}, client.InNamespace(vars.Namespace))
			Expect(err).ToNot(HaveOccurred())

			err = k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovNetworkNodeState{}, client.InNamespace(vars.Namespace))
			Expect(err).ToNot(HaveOccurred())

			err = k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovOperatorConfig{}, client.InNamespace(vars.Namespace))
			Expect(err).ToNot(HaveOccurred())

			operatorConfigList := &sriovnetworkv1.SriovOperatorConfigList{}
			Eventually(func(g Gomega) {
				err = k8sClient.List(context.Background(), operatorConfigList, &client.ListOptions{Namespace: vars.Namespace})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(len(operatorConfigList.Items)).To(Equal(0))
			}, time.Minute, time.Second).Should(Succeed())
		})

		BeforeEach(func() {
			var err error
			config := &sriovnetworkv1.SriovOperatorConfig{}

			Eventually(func(g Gomega) {
				err = util.WaitForNamespacedObject(config, k8sClient, testNamespace, "default", util.RetryInterval, util.APITimeout)
				g.Expect(err).NotTo(HaveOccurred())
				// in case controller yet to add object's finalizer (e.g whenever test deferCleanup is creating new 'default' config object)
				g.Expect(config.Finalizers).ToNot(BeEmpty())

				config.Spec = sriovnetworkv1.SriovOperatorConfigSpec{
					EnableInjector:        true,
					EnableOperatorWebhook: true,
					LogLevel:              2,
					FeatureGates:          map[string]bool{},
				}
				err = k8sClient.Update(ctx, config)
				Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

		})

		It("should have webhook enable", func() {
			mutateCfg := &admv1.MutatingWebhookConfiguration{}
			err := util.WaitForNamespacedObject(mutateCfg, k8sClient, testNamespace, "sriov-operator-webhook-config", util.RetryInterval, util.APITimeout*3)
			Expect(err).NotTo(HaveOccurred())

			validateCfg := &admv1.ValidatingWebhookConfiguration{}
			err = util.WaitForNamespacedObject(validateCfg, k8sClient, testNamespace, "sriov-operator-webhook-config", util.RetryInterval, util.APITimeout*3)
			Expect(err).NotTo(HaveOccurred())
		})

		DescribeTable("should have daemonset enabled by default",
			func(dsName string) {
				// wait for sriov-network-operator to be ready
				daemonSet := &appsv1.DaemonSet{}
				err := util.WaitForNamespacedObject(daemonSet, k8sClient, testNamespace, dsName, util.RetryInterval, util.APITimeout)
				Expect(err).NotTo(HaveOccurred())
				Expect(daemonSet.OwnerReferences).To(HaveLen(1))
				Expect(daemonSet.OwnerReferences[0].Kind).To(Equal("SriovOperatorConfig"))
				Expect(daemonSet.OwnerReferences[0].Name).To(Equal(consts.DefaultConfigName))
			},
			Entry("operator-webhook", "operator-webhook"),
			Entry("network-resources-injector", "network-resources-injector"),
			Entry("sriov-network-config-daemon", "sriov-network-config-daemon"),
			Entry("sriov-device-plugin", "sriov-device-plugin"),
		)

		It("should be able to turn network-resources-injector on/off", func() {
			By("set disable to enableInjector")
			config := &sriovnetworkv1.SriovOperatorConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "default"}, config)).NotTo(HaveOccurred())

			config.Spec.EnableInjector = false
			err := k8sClient.Update(ctx, config)
			Expect(err).NotTo(HaveOccurred())

			daemonSet := &appsv1.DaemonSet{}
			err = util.WaitForNamespacedObjectDeleted(daemonSet, k8sClient, testNamespace, "network-resources-injector", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			networkPolicy := &networkv1.NetworkPolicy{}
			err = util.WaitForNamespacedObjectDeleted(networkPolicy, k8sClient, testNamespace, "network-resources-injector-allow-traffic-api-server", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			mutateCfg := &admv1.MutatingWebhookConfiguration{}
			err = util.WaitForNamespacedObjectDeleted(mutateCfg, k8sClient, testNamespace, "network-resources-injector-config", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			By("set enable to enableInjector")
			err = util.WaitForNamespacedObject(config, k8sClient, testNamespace, "default", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			config.Spec.EnableInjector = true
			err = k8sClient.Update(ctx, config)
			Expect(err).NotTo(HaveOccurred())

			daemonSet = &appsv1.DaemonSet{}
			err = util.WaitForNamespacedObject(daemonSet, k8sClient, testNamespace, "network-resources-injector", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			networkPolicy = &networkv1.NetworkPolicy{}
			err = util.WaitForNamespacedObject(networkPolicy, k8sClient, testNamespace, "network-resources-injector-allow-traffic-api-server", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			mutateCfg = &admv1.MutatingWebhookConfiguration{}
			err = util.WaitForNamespacedObject(mutateCfg, k8sClient, testNamespace, "network-resources-injector-config", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should be able to turn operator-webhook on/off", func() {

			By("set disable to enableOperatorWebhook")
			config := &sriovnetworkv1.SriovOperatorConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "default"}, config)).NotTo(HaveOccurred())

			config.Spec.EnableOperatorWebhook = false
			err := k8sClient.Update(ctx, config)
			Expect(err).NotTo(HaveOccurred())

			daemonSet := &appsv1.DaemonSet{}
			err = util.WaitForNamespacedObjectDeleted(daemonSet, k8sClient, testNamespace, "operator-webhook", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			networkPolicy := &networkv1.NetworkPolicy{}
			err = util.WaitForNamespacedObjectDeleted(networkPolicy, k8sClient, testNamespace, "operator-webhook-allow-traffic-api-server", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			mutateCfg := &admv1.MutatingWebhookConfiguration{}
			err = util.WaitForNamespacedObjectDeleted(mutateCfg, k8sClient, testNamespace, "sriov-operator-webhook-config", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			validateCfg := &admv1.ValidatingWebhookConfiguration{}
			err = util.WaitForNamespacedObjectDeleted(validateCfg, k8sClient, testNamespace, "sriov-operator-webhook-config", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			By("set enable to enableOperatorWebhook")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "default"}, config)).NotTo(HaveOccurred())

			config.Spec.EnableOperatorWebhook = true
			err = k8sClient.Update(ctx, config)
			Expect(err).NotTo(HaveOccurred())

			daemonSet = &appsv1.DaemonSet{}
			err = util.WaitForNamespacedObject(daemonSet, k8sClient, testNamespace, "operator-webhook", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			networkPolicy = &networkv1.NetworkPolicy{}
			err = util.WaitForNamespacedObject(networkPolicy, k8sClient, testNamespace, "operator-webhook-allow-traffic-api-server", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			mutateCfg = &admv1.MutatingWebhookConfiguration{}
			err = util.WaitForNamespacedObject(mutateCfg, k8sClient, testNamespace, "sriov-operator-webhook-config", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())

			validateCfg = &admv1.ValidatingWebhookConfiguration{}
			err = util.WaitForNamespacedObject(validateCfg, k8sClient, testNamespace, "sriov-operator-webhook-config", util.RetryInterval, util.APITimeout)
			Expect(err).NotTo(HaveOccurred())
		})

		// Namespaced resources are deleted via the `.ObjectMeta.OwnerReference` field. That logic can't be tested here because testenv doesn't have built-in controllers
		// (See https://book.kubebuilder.io/reference/envtest#testing-considerations). Since Service and DaemonSet are deleted when default/SriovOperatorConfig is no longer
		// present, it's important that webhook configurations are deleted as well.
		It("should delete the webhooks when SriovOperatorConfig/default is deleted", func() {
			DeferCleanup(k8sClient.Create, context.Background(), makeDefaultSriovOpConfig())

			err := k8sClient.Delete(context.Background(), &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			assertResourceDoesNotExist(
				schema.GroupVersionKind{Group: "admissionregistration.k8s.io", Kind: "MutatingWebhookConfiguration", Version: "v1"},
				client.ObjectKey{Name: "sriov-operator-webhook-config"})
			assertResourceDoesNotExist(
				schema.GroupVersionKind{Group: "admissionregistration.k8s.io", Kind: "ValidatingWebhookConfiguration", Version: "v1"},
				client.ObjectKey{Name: "sriov-operator-webhook-config"})

			assertResourceDoesNotExist(
				schema.GroupVersionKind{Group: "admissionregistration.k8s.io", Kind: "MutatingWebhookConfiguration", Version: "v1"},
				client.ObjectKey{Name: "network-resources-injector-config"})
		})

		It("should add/delete finalizer 'operatorconfig' when SriovOperatorConfig/default is added/deleted", func() {
			DeferCleanup(k8sClient.Create, context.Background(), makeDefaultSriovOpConfig())

			// verify that finalizer has been added upon object creation
			config := &sriovnetworkv1.SriovOperatorConfig{}
			Eventually(func() []string {
				// wait for SriovOperatorConfig flags to get updated
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: testNamespace}, config)
				if err != nil {
					return nil
				}
				return config.Finalizers
			}, util.APITimeout, util.RetryInterval).Should(Equal([]string{sriovnetworkv1.OPERATORCONFIGFINALIZERNAME}))

			err := k8sClient.Delete(context.Background(), &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			// verify that finalizer has been removed
			var empty []string
			config = &sriovnetworkv1.SriovOperatorConfig{}
			Eventually(func() []string {
				// wait for SriovOperatorConfig flags to get updated
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: testNamespace}, config)
				if err != nil {
					return nil
				}
				return config.Finalizers
			}, util.APITimeout, util.RetryInterval).Should(Equal(empty))
		})

		It("should not remove fields with default values when SriovOperatorConfig is created", func() {
			err := k8sClient.Delete(context.Background(), &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: consts.DefaultConfigName},
			})
			Expect(err).NotTo(HaveOccurred())

			config := &uns.Unstructured{}
			config.SetGroupVersionKind(sriovnetworkv1.GroupVersion.WithKind("SriovOperatorConfig"))
			config.SetName(consts.DefaultConfigName)
			config.SetNamespace(testNamespace)
			config.Object["spec"] = map[string]interface{}{
				"enableInjector":        false,
				"enableOperatorWebhook": false,
				"logLevel":              0,
				"disableDrain":          false,
			}

			Eventually(func() error {
				return k8sClient.Create(context.Background(), config)
			}).Should(Succeed())

			By("Wait for the operator to reconcile the object")
			Eventually(func(g Gomega) {
				err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: consts.DefaultConfigName}, config)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(config.GetFinalizers()).To(ContainElement(sriovnetworkv1.OPERATORCONFIGFINALIZERNAME))
			}, util.APITimeout, util.RetryInterval).Should(Succeed())

			By("Verify default values have not been omitted")
			obj := &uns.Unstructured{}
			obj.SetGroupVersionKind(sriovnetworkv1.GroupVersion.WithKind("SriovOperatorConfig"))
			err = k8sClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: consts.DefaultConfigName}, obj)
			Expect(err).NotTo(HaveOccurred())

			Expect(obj.Object["spec"]).To(HaveKeyWithValue("enableInjector", false))
			Expect(obj.Object["spec"]).To(HaveKeyWithValue("enableOperatorWebhook", false))
			Expect(obj.Object["spec"]).To(HaveKeyWithValue("logLevel", int64(0)))
			Expect(obj.Object["spec"]).To(HaveKeyWithValue("disableDrain", false))
		})

		It("should be able to update the node selector of sriov-network-config-daemon", func() {
			By("specify the configDaemonNodeSelector")
			nodeSelector := map[string]string{"node-role.kubernetes.io/worker": ""}
			restore := updateConfigDaemonNodeSelector(nodeSelector)
			DeferCleanup(restore)

			daemonSet := &appsv1.DaemonSet{}
			Eventually(func() map[string]string {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "sriov-network-config-daemon", Namespace: testNamespace}, daemonSet)
				if err != nil {
					return nil
				}
				return daemonSet.Spec.Template.Spec.NodeSelector
			}, util.APITimeout, util.RetryInterval).Should(Equal(nodeSelector))
		})

		It("should be able to update the node selector of sriov-network-device-plugin", func() {
			By("specify the configDaemonNodeSelector")
			daemonSet := &appsv1.DaemonSet{}
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "sriov-device-plugin", Namespace: testNamespace}, daemonSet)
				g.Expect(err).ToNot(HaveOccurred())
				_, exist := daemonSet.Spec.Template.Spec.NodeSelector["node-role.kubernetes.io/worker"]
				g.Expect(exist).To(BeFalse())
				_, exist = daemonSet.Spec.Template.Spec.NodeSelector[consts.SriovDevicePluginLabel]
				g.Expect(exist).To(BeTrue())
			}, util.APITimeout, util.RetryInterval).Should(Succeed())

			nodeSelector := map[string]string{"node-role.kubernetes.io/worker": ""}
			restore := updateConfigDaemonNodeSelector(nodeSelector)
			DeferCleanup(restore)

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "sriov-device-plugin", Namespace: testNamespace}, daemonSet)
				g.Expect(err).ToNot(HaveOccurred())
				_, exist := daemonSet.Spec.Template.Spec.NodeSelector["node-role.kubernetes.io/worker"]
				g.Expect(exist).To(BeTrue())
				_, exist = daemonSet.Spec.Template.Spec.NodeSelector[consts.SriovDevicePluginLabel]
				g.Expect(exist).To(BeTrue())
			}, util.APITimeout, util.RetryInterval).Should(Succeed())
		})

		It("should be able to do multiple updates to the node selector of sriov-network-config-daemon", func() {
			By("changing the configDaemonNodeSelector")
			firstNodeSelector := map[string]string{"labelA": "", "labelB": "", "labelC": ""}
			restore := updateConfigDaemonNodeSelector(firstNodeSelector)
			DeferCleanup(restore)

			secondNodeSelector := map[string]string{"labelA": "", "labelB": ""}
			updateConfigDaemonNodeSelector(secondNodeSelector)

			daemonSet := &appsv1.DaemonSet{}
			Eventually(func() map[string]string {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "sriov-network-config-daemon", Namespace: testNamespace}, daemonSet)
				if err != nil {
					return nil
				}
				return daemonSet.Spec.Template.Spec.NodeSelector
			}, util.APITimeout, util.RetryInterval).Should(Equal(secondNodeSelector))
		})

		It("should not render disable-plugins cmdline flag of sriov-network-config-daemon if disablePlugin not provided in spec", func() {
			config := &sriovnetworkv1.SriovOperatorConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "default"}, config)).NotTo(HaveOccurred())

			Eventually(func() string {
				daemonSet := &appsv1.DaemonSet{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "sriov-network-config-daemon", Namespace: testNamespace}, daemonSet)
				if err != nil {
					return ""
				}
				return strings.Join(daemonSet.Spec.Template.Spec.Containers[0].Args, " ")
			}, util.APITimeout*10, util.RetryInterval).Should(And(Not(BeEmpty()), Not(ContainSubstring("disable-plugins"))))
		})

		It("should render disable-plugins cmdline flag of sriov-network-config-daemon if disablePlugin provided in spec", func() {
			config := &sriovnetworkv1.SriovOperatorConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "default"}, config)).NotTo(HaveOccurred())

			config.Spec.DisablePlugins = sriovnetworkv1.PluginNameSlice{"mellanox"}
			err := k8sClient.Update(ctx, config)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() string {
				daemonSet := &appsv1.DaemonSet{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "sriov-network-config-daemon", Namespace: testNamespace}, daemonSet)
				if err != nil {
					return ""
				}
				return strings.Join(daemonSet.Spec.Template.Spec.Containers[0].Args, " ")
			}, util.APITimeout*10, util.RetryInterval).Should(ContainSubstring("disable-plugins=mellanox"))
		})

		It("should render the resourceInjectorMatchCondition in the mutation if feature flag is enabled and block only pods with the networks annotation", func() {
			By("set the feature flag")
			config := &sriovnetworkv1.SriovOperatorConfig{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "default"}, config)).NotTo(HaveOccurred())

			config.Spec.FeatureGates = map[string]bool{}
			config.Spec.FeatureGates[consts.ResourceInjectorMatchConditionFeatureGate] = true
			err := k8sClient.Update(ctx, config)
			Expect(err).NotTo(HaveOccurred())

			By("checking the webhook have all the needed configuration")
			mutateCfg := &admv1.MutatingWebhookConfiguration{}
			err = wait.PollUntilContextTimeout(ctx, util.RetryInterval, util.APITimeout, true, func(ctx context.Context) (done bool, err error) {
				err = k8sClient.Get(ctx, types.NamespacedName{Name: "network-resources-injector-config", Namespace: testNamespace}, mutateCfg)
				if err != nil {
					if errors.IsNotFound(err) {
						return false, nil
					}
					return false, err
				}
				if len(mutateCfg.Webhooks) != 1 {
					return false, nil
				}
				if *mutateCfg.Webhooks[0].FailurePolicy != admv1.Fail {
					return false, nil
				}
				if len(mutateCfg.Webhooks[0].MatchConditions) != 1 {
					return false, nil
				}

				if mutateCfg.Webhooks[0].MatchConditions[0].Name != "include-networks-annotation" {
					return false, nil
				}

				return true, nil
			})
			Expect(err).ToNot(HaveOccurred())
		})

		Context("metricsExporter feature gate", func() {
			When("is disabled", func() {
				It("should not deploy the daemonset", func() {
					daemonSet := &appsv1.DaemonSet{}
					err := k8sClient.Get(ctx, types.NamespacedName{Name: "sriov-metrics-exporter", Namespace: testNamespace}, daemonSet)
					Expect(err).To(HaveOccurred())
					Expect(errors.IsNotFound(err)).To(BeTrue())
				})
			})

			When("is enabled", func() {
				BeforeEach(func() {
					config := &sriovnetworkv1.SriovOperatorConfig{}
					Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "default"}, config)).NotTo(HaveOccurred())

					By("Turn `metricsExporter` feature gate on")
					config.Spec.FeatureGates = map[string]bool{consts.MetricsExporterFeatureGate: true}
					err := k8sClient.Update(ctx, config)
					Expect(err).NotTo(HaveOccurred())
				})

				It("should deploy the sriov-network-metrics-exporter DaemonSet", func() {
					err := util.WaitForNamespacedObject(&appsv1.DaemonSet{}, k8sClient, testNamespace, "sriov-network-metrics-exporter", util.RetryInterval, util.APITimeout)
					Expect(err).NotTo(HaveOccurred())

					err = util.WaitForNamespacedObject(&corev1.Service{}, k8sClient, testNamespace, "sriov-network-metrics-exporter-service", util.RetryInterval, util.APITimeout)
					Expect(err).ToNot(HaveOccurred())
				})

				It("should deploy the sriov-network-metrics-exporter using the Spec.ConfigDaemonNodeSelector field", func() {
					nodeSelector := map[string]string{
						"node-role.kubernetes.io/worker": "",
						"bool-key":                       "true",
					}

					restore := updateConfigDaemonNodeSelector(nodeSelector)
					DeferCleanup(restore)

					Eventually(func(g Gomega) {
						metricsDaemonset := appsv1.DaemonSet{}
						err := util.WaitForNamespacedObject(&metricsDaemonset, k8sClient, testNamespace, "sriov-network-metrics-exporter", util.RetryInterval, util.APITimeout)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(metricsDaemonset.Spec.Template.Spec.NodeSelector).To(Equal(nodeSelector))
					}, time.Minute, time.Second).Should(Succeed())
				})

				It("should deploy extra configuration when the Prometheus operator is installed", func() {
					DeferCleanup(os.Setenv, "METRICS_EXPORTER_PROMETHEUS_OPERATOR_ENABLED", os.Getenv("METRICS_EXPORTER_PROMETHEUS_OPERATOR_ENABLED"))
					os.Setenv("METRICS_EXPORTER_PROMETHEUS_OPERATOR_ENABLED", "true")
					DeferCleanup(os.Setenv, "METRICS_EXPORTER_PROMETHEUS_DEPLOY_RULES", os.Getenv("METRICS_EXPORTER_PROMETHEUS_DEPLOY_RULES"))
					os.Setenv("METRICS_EXPORTER_PROMETHEUS_DEPLOY_RULES", "true")

					err := util.WaitForNamespacedObject(&rbacv1.Role{}, k8sClient, testNamespace, "prometheus-k8s", util.RetryInterval, util.APITimeout)
					Expect(err).ToNot(HaveOccurred())

					err = util.WaitForNamespacedObject(&rbacv1.RoleBinding{}, k8sClient, testNamespace, "prometheus-k8s", util.RetryInterval, util.APITimeout)
					Expect(err).ToNot(HaveOccurred())

					assertResourceExists(
						schema.GroupVersionKind{
							Group:   "monitoring.coreos.com",
							Kind:    "ServiceMonitor",
							Version: "v1",
						},
						client.ObjectKey{Namespace: testNamespace, Name: "sriov-network-metrics-exporter"})

					assertResourceExists(
						schema.GroupVersionKind{
							Group:   "monitoring.coreos.com",
							Kind:    "PrometheusRule",
							Version: "v1",
						},
						client.ObjectKey{Namespace: testNamespace, Name: "sriov-vf-rules"})
				})
			})
		})

		// This test verifies that the CABundle field in the webhook configuration  added by third party components is not
		// removed during the reconciliation loop. This is important when dealing with OpenShift certificate mangement:
		// https://docs.openshift.com/container-platform/4.15/security/certificates/service-serving-certificate.html
		// and when CertManager is used
		It("should not remove the field Spec.ClientConfig.CABundle from webhook configuration when reconciling", func() {
			validateCfg := &admv1.ValidatingWebhookConfiguration{}
			err := util.WaitForNamespacedObject(validateCfg, k8sClient, testNamespace, "sriov-operator-webhook-config", util.RetryInterval, util.APITimeout*3)
			Expect(err).NotTo(HaveOccurred())

			By("Simulate a third party component updating the webhook CABundle")
			validateCfg.Webhooks[0].ClientConfig.CABundle = []byte("some-base64-ca-bundle-value")

			err = k8sClient.Update(ctx, validateCfg)
			Expect(err).NotTo(HaveOccurred())

			By("Trigger a controller reconciliation")
			err = util.TriggerSriovOperatorConfigReconcile(k8sClient, testNamespace)
			Expect(err).NotTo(HaveOccurred())

			By("Verify the operator did not remove the CABundle from the webhook configuration")
			Consistently(func(g Gomega) {
				err = k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "sriov-operator-webhook-config"}, validateCfg)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(validateCfg.Webhooks[0].ClientConfig.CABundle).To(Equal([]byte("some-base64-ca-bundle-value")))
			}, "1s").Should(Succeed())
		})

		It("should update the webhook CABundle if `ADMISSION_CONTROLLERS_CERTIFICATES environment variable are set` ", func() {
			DeferCleanup(os.Setenv, "ADMISSION_CONTROLLERS_CERTIFICATES_OPERATOR_CA_CRT", os.Getenv("ADMISSION_CONTROLLERS_CERTIFICATES_OPERATOR_CA_CRT"))
			// echo "ca-bundle-1" | base64 -w 0
			os.Setenv("ADMISSION_CONTROLLERS_CERTIFICATES_OPERATOR_CA_CRT", "Y2EtYnVuZGxlLTEK")

			DeferCleanup(os.Setenv, "ADMISSION_CONTROLLERS_CERTIFICATES_INJECTOR_CA_CRT", os.Getenv("ADMISSION_CONTROLLERS_CERTIFICATES_INJECTOR_CA_CRT"))
			// echo "ca-bundle-2" | base64 -w 0
			os.Setenv("ADMISSION_CONTROLLERS_CERTIFICATES_INJECTOR_CA_CRT", "Y2EtYnVuZGxlLTIK")

			DeferCleanup(func(old consts.ClusterType) { vars.ClusterType = old }, vars.ClusterType)
			vars.ClusterType = consts.ClusterTypeKubernetes

			err := util.TriggerSriovOperatorConfigReconcile(k8sClient, testNamespace)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				validateCfg := &admv1.ValidatingWebhookConfiguration{}
				err = k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "sriov-operator-webhook-config"}, validateCfg)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(validateCfg.Webhooks[0].ClientConfig.CABundle).To(Equal([]byte("ca-bundle-1\n")))

				mutateCfg := &admv1.MutatingWebhookConfiguration{}
				err = k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "sriov-operator-webhook-config"}, mutateCfg)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(mutateCfg.Webhooks[0].ClientConfig.CABundle).To(Equal([]byte("ca-bundle-1\n")))

				injectorCfg := &admv1.MutatingWebhookConfiguration{}
				err = k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "network-resources-injector-config"}, injectorCfg)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(injectorCfg.Webhooks[0].ClientConfig.CABundle).To(Equal([]byte("ca-bundle-2\n")))
			}, "1s").Should(Succeed())
		})
	})
})

func makeDefaultSriovOpConfig() *sriovnetworkv1.SriovOperatorConfig {
	config := &sriovnetworkv1.SriovOperatorConfig{}
	config.SetNamespace(testNamespace)
	config.SetName(consts.DefaultConfigName)
	config.Spec = sriovnetworkv1.SriovOperatorConfigSpec{
		EnableInjector:           true,
		EnableOperatorWebhook:    true,
		ConfigDaemonNodeSelector: map[string]string{},
		LogLevel:                 2,
	}
	return config
}

func assertResourceExists(gvk schema.GroupVersionKind, key client.ObjectKey) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	err := k8sClient.Get(context.Background(), key, u)
	Expect(err).NotTo(HaveOccurred())
}

func assertResourceDoesNotExist(gvk schema.GroupVersionKind, key client.ObjectKey) {
	Eventually(func(g Gomega) {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		err := k8sClient.Get(context.Background(), key, u)
		g.Expect(err).To(HaveOccurred())
		g.Expect(errors.IsNotFound(err)).To(BeTrue())
	}).
		WithOffset(1).
		WithPolling(100*time.Millisecond).
		WithTimeout(2*time.Second).
		Should(Succeed(), "Resource type[%s] name[%s] still present in the cluster", gvk.String(), key.String())
}

func updateConfigDaemonNodeSelector(newValue map[string]string) func() {
	config := &sriovnetworkv1.SriovOperatorConfig{}
	err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "default"}, config)
	Expect(err).NotTo(HaveOccurred())

	previousValue := config.Spec.ConfigDaemonNodeSelector
	ret := func() {
		updateConfigDaemonNodeSelector(previousValue)
	}

	config.Spec.ConfigDaemonNodeSelector = newValue
	err = k8sClient.Update(context.Background(), config)
	Expect(err).NotTo(HaveOccurred())

	return ret
}
