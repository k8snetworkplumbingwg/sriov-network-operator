package controllers

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/render"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

var _ = Describe("Resource provider", func() {
	var (
		ctx    context.Context
		scheme *runtime.Scheme
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(sriovnetworkv1.AddToScheme(scheme)).To(Succeed())
		Expect(appsv1.AddToScheme(scheme)).To(Succeed())
	})

	Context("accepted provider persistence", func() {
		It("initializes and switches both boolean fields through the API server", func() {
			previousNamespace := vars.Namespace
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "provider-status-"}}
			createAggregateObjects(namespace)
			vars.Namespace = namespace.Name
			DeferCleanup(func() { vars.Namespace = previousNamespace })
			config := testOperatorConfig()
			createAggregateObjects(config)
			r := &SriovOperatorConfigReconciler{Client: k8sClient}
			for _, dra := range []bool{true, false, true} {
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), config)).To(Succeed())
				config.Spec.FeatureGates = map[string]bool{consts.DynamicResourceAllocationFeatureGate: dra}
				Expect(k8sClient.Update(ctx, config)).To(Succeed())
				Expect(r.acceptResourceProvider(ctx, config)).To(Succeed())
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), config)).To(Succeed())
				Expect(config.Status.ResourceProvider).To(Equal(sriovnetworkv1.ResourceProvider{DevicePlugin: !dra, DRA: dra}))
			}
		})

		It("rejects simultaneous providers without changing accepted status", func() {
			config := &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: vars.Namespace},
				Status: sriovnetworkv1.SriovOperatorConfigStatus{
					ResourceProvider: sriovnetworkv1.ResourceProvider{DevicePlugin: true, DRA: true},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(config).WithObjects(config).Build()
			r := &SriovOperatorConfigReconciler{Client: cl}
			Expect(r.acceptResourceProvider(ctx, config)).To(MatchError(ContainSubstring("simultaneous device-plugin and DRA providers are not supported")))
			persisted := &sriovnetworkv1.SriovOperatorConfig{}
			Expect(cl.Get(ctx, client.ObjectKeyFromObject(config), persisted)).To(Succeed())
			Expect(persisted.Status.ResourceProvider).To(Equal(sriovnetworkv1.ResourceProvider{DevicePlugin: true, DRA: true}))
		})

		DescribeTable("blocks a switch across restarts until all policies are removed", func(current sriovnetworkv1.ResourceProvider) {
			desired := sriovnetworkv1.DRAResourceProvider()
			if current == desired {
				desired = sriovnetworkv1.DevicePluginResourceProvider()
			}
			config := &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: vars.Namespace},
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{FeatureGates: map[string]bool{
					consts.DynamicResourceAllocationFeatureGate: desired == sriovnetworkv1.DRAResourceProvider(),
				}},
				Status: sriovnetworkv1.SriovOperatorConfigStatus{ResourceProvider: current},
			}
			// Even an empty, deprecated default policy must block a switch.
			policy := &sriovnetworkv1.SriovNetworkNodePolicy{ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultPolicyName, Namespace: vars.Namespace}}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(config).WithObjects(config, policy).Build()
			for i := 0; i < 2; i++ {
				r := &SriovOperatorConfigReconciler{Client: cl, UncachedAPIReader: cl}
				fresh, found, err := sriovnetworkv1.GetOperatorConfig(ctx, cl)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(r.acceptResourceProvider(ctx, fresh)).To(HaveOccurred(), "switch with policies must be rejected")
				persisted, found, err := sriovnetworkv1.GetOperatorConfig(ctx, cl)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(persisted.Status.ResourceProvider).To(Equal(current), "blocked switch must preserve the accepted provider")
				gates, err := persisted.EffectiveFeatureGates()
				Expect(err).NotTo(HaveOccurred())
				Expect(gates[consts.DynamicResourceAllocationFeatureGate]).To(Equal(current == sriovnetworkv1.DRAResourceProvider()),
					"consumers must use the accepted provider after a restart")
			}
			Expect(cl.Delete(ctx, policy)).To(Succeed())
			fresh, found, err := sriovnetworkv1.GetOperatorConfig(ctx, cl)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			r := &SriovOperatorConfigReconciler{Client: cl, UncachedAPIReader: cl}
			Expect(r.acceptResourceProvider(ctx, fresh)).To(Succeed())
			persisted, found, err := sriovnetworkv1.GetOperatorConfig(ctx, cl)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persisted.Status.ResourceProvider).To(Equal(desired))
		},
			Entry("device plugin to DRA", sriovnetworkv1.DevicePluginResourceProvider()),
			Entry("DRA to device plugin", sriovnetworkv1.DRAResourceProvider()),
		)
	})

	Context("initialization", func() {
		DescribeTable("selects the provider using the installed DaemonSets and existing policies",
			func(installed []string, policies, desiredDRA bool, want sriovnetworkv1.ResourceProvider, wantError bool) {
				config := &sriovnetworkv1.SriovOperatorConfig{
					ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: vars.Namespace},
					Spec: sriovnetworkv1.SriovOperatorConfigSpec{FeatureGates: map[string]bool{
						consts.DynamicResourceAllocationFeatureGate: desiredDRA,
					}},
				}
				objects := []client.Object{config}
				for _, name := range installed {
					objects = append(objects, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: vars.Namespace}})
				}
				if policies {
					objects = append(objects, &sriovnetworkv1.SriovNetworkNodePolicy{ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: vars.Namespace}})
				}
				cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(config).WithObjects(objects...).Build()
				fresh, found, err := sriovnetworkv1.GetOperatorConfig(ctx, cl)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				r := &SriovOperatorConfigReconciler{Client: cl}
				err = r.acceptResourceProvider(ctx, fresh)
				if wantError {
					Expect(err).To(HaveOccurred())
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
				persisted, found, err := sriovnetworkv1.GetOperatorConfig(ctx, cl)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(persisted.Status.ResourceProvider).To(Equal(want))
			},
			Entry("fresh DRA", []string(nil), false, true, sriovnetworkv1.DRAResourceProvider(), false),
			Entry("fresh device plugin", []string(nil), false, false, sriovnetworkv1.DevicePluginResourceProvider(), false),
			Entry("existing policies without DaemonSets", []string(nil), true, true, sriovnetworkv1.DevicePluginResourceProvider(), true),
			Entry("upgrade device plugin", []string{devicePluginDaemonSetName}, true, true, sriovnetworkv1.DevicePluginResourceProvider(), true),
			Entry("upgrade DRA", []string{consts.DRADriverDaemonSetName}, true, false, sriovnetworkv1.DRAResourceProvider(), true),
			Entry("upgrade unchanged DRA", []string{consts.DRADriverDaemonSetName}, true, true, sriovnetworkv1.DRAResourceProvider(), false),
			Entry("ambiguous with policies", []string{devicePluginDaemonSetName, consts.DRADriverDaemonSetName}, true, true, sriovnetworkv1.ResourceProvider{}, true),
			Entry("ambiguous without policies", []string{devicePluginDaemonSetName, consts.DRADriverDaemonSetName}, false, true, sriovnetworkv1.DRAResourceProvider(), false),
		)
	})

	Context("fresh policies and optimistic locking", func() {
		DescribeTable("rejects unsafe switches without changing the persisted provider", func(failure string) {
			config := &sriovnetworkv1.SriovOperatorConfig{
				ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultConfigName, Namespace: vars.Namespace},
				Spec: sriovnetworkv1.SriovOperatorConfigSpec{FeatureGates: map[string]bool{
					consts.DynamicResourceAllocationFeatureGate: true,
				}},
				Status: sriovnetworkv1.SriovOperatorConfigStatus{ResourceProvider: sriovnetworkv1.DevicePluginResourceProvider()},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(config).WithObjects(config).Build()
			reader := interceptor.NewClient(cl, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				switch failure {
				case "uncached policy":
					list.(*sriovnetworkv1.SriovNetworkNodePolicyList).Items = []sriovnetworkv1.SriovNetworkNodePolicy{{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: vars.Namespace}}}
					return nil
				case "list failure":
					return fmt.Errorf("unavailable")
				default:
					updated, found, err := sriovnetworkv1.GetOperatorConfig(ctx, c)
					if err != nil {
						return err
					}
					if !found {
						return fmt.Errorf("operator config disappeared during concurrent update")
					}
					updated.Spec.FeatureGates = nil
					return c.Update(ctx, updated)
				}
			}})
			fresh, found, err := sriovnetworkv1.GetOperatorConfig(ctx, cl)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			r := &SriovOperatorConfigReconciler{Client: cl, UncachedAPIReader: reader}
			Expect(r.acceptResourceProvider(ctx, fresh)).To(HaveOccurred(), "unsafe switch must be rejected")
			persisted, found, err := sriovnetworkv1.GetOperatorConfig(ctx, cl)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persisted.Status.ResourceProvider).To(Equal(sriovnetworkv1.DevicePluginResourceProvider()))
		},
			Entry("policy visible only to the uncached reader", "uncached policy"),
			Entry("policy list failure", "list failure"),
			Entry("concurrent config update", "concurrent config update"),
		)
	})

	Context("provider rollout", func() {
		DescribeTable("waits for the live replacement rollout despite stale cached DaemonSets", func(provider sriovnetworkv1.ResourceProvider, previousMissingFromCache bool) {
			replacement, previous, path := consts.DRADriverDaemonSetName, devicePluginDaemonSetName, consts.DRADriverPath
			if provider.DevicePlugin {
				replacement, previous, path = previous, replacement, consts.PluginPath
			}
			old := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: previous, Namespace: vars.Namespace}}
			cached := &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{Name: replacement, Namespace: vars.Namespace, Generation: 1},
				Status: appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 2,
					NumberReady: 2, UpdatedNumberScheduled: 2},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(old, cached).Build()
			cachedClient := interceptor.NewClient(cl, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*appsv1.DaemonSet); ok && key.Name == previous && previousMissingFromCache {
						return apierrors.NewNotFound(appsv1.Resource("daemonsets"), key.Name)
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			live := cached.DeepCopy()
			live.Generation = 2
			reader := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(live).WithObjects(old, live).Build()
			cleanupCalls := 0
			r := &SriovOperatorConfigReconciler{Client: cachedClient, UncachedAPIReader: reader, Scheme: scheme, FeatureGate: featuregate.New(),
				renderManifestFn: func(gotPath string, _ *render.RenderData) ([]*unstructured.Unstructured, error) {
					if gotPath == path {
						return nil, nil
					}
					cleanupCalls++
					return []*unstructured.Unstructured{{Object: map[string]interface{}{
						"apiVersion": "apps/v1", "kind": "DaemonSet",
						"metadata": map[string]interface{}{"name": previous, "namespace": vars.Namespace},
					}}}, nil
				},
			}
			config := &sriovnetworkv1.SriovOperatorConfig{Status: sriovnetworkv1.SriovOperatorConfigStatus{ResourceProvider: provider}}
			ready, err := r.syncResourceProvider(ctx, config)
			Expect(err).NotTo(HaveOccurred())
			Expect(ready).To(BeFalse())
			Expect(cleanupCalls).To(BeZero())
			Expect(cl.Get(ctx, client.ObjectKeyFromObject(old), old)).To(Succeed())

			Expect(reader.Get(ctx, client.ObjectKeyFromObject(live), live)).To(Succeed())
			live.Status.ObservedGeneration = live.Generation
			Expect(reader.Status().Update(ctx, live)).To(Succeed())
			ready, err = r.syncResourceProvider(ctx, config)
			Expect(err).NotTo(HaveOccurred())
			Expect(ready).To(BeTrue())
			Expect(cleanupCalls).To(Equal(1))
			Expect(cl.Get(ctx, client.ObjectKeyFromObject(old), old)).To(MatchError(ContainSubstring("not found")))
		},
			Entry("device plugin to DRA", sriovnetworkv1.DRAResourceProvider(), false),
			Entry("DRA to device plugin", sriovnetworkv1.DevicePluginResourceProvider(), false),
			Entry("device plugin missing from cache", sriovnetworkv1.DRAResourceProvider(), true),
			Entry("DRA missing from cache", sriovnetworkv1.DevicePluginResourceProvider(), true),
		)

		DescribeTable("cleans up residual objects when the previous provider is absent from the live API", func(provider sriovnetworkv1.ResourceProvider) {
			previous, path := devicePluginDaemonSetName, consts.DRADriverPath
			if provider.DevicePlugin {
				previous, path = consts.DRADriverDaemonSetName, consts.PluginPath
			}
			old := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: previous, Namespace: vars.Namespace}}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(old).Build()
			reader := fake.NewClientBuilder().WithScheme(scheme).Build()
			cleanupCalls := 0
			r := &SriovOperatorConfigReconciler{Client: cl, UncachedAPIReader: reader, Scheme: scheme, FeatureGate: featuregate.New(),
				renderManifestFn: func(gotPath string, _ *render.RenderData) ([]*unstructured.Unstructured, error) {
					if gotPath != path {
						cleanupCalls++
					}
					return nil, nil
				},
				ensureProviderRolloutFn: func(context.Context, string) (bool, error) {
					Fail("a stale cached previous provider must not gate residual cleanup on rollout")
					return false, nil
				},
			}
			config := &sriovnetworkv1.SriovOperatorConfig{Status: sriovnetworkv1.SriovOperatorConfigStatus{ResourceProvider: provider}}
			ready, err := r.syncResourceProvider(ctx, config)
			Expect(err).NotTo(HaveOccurred())
			Expect(ready).To(BeTrue())
			Expect(cleanupCalls).To(Equal(1))
		},
			Entry("device plugin to DRA", sriovnetworkv1.DRAResourceProvider()),
			Entry("DRA to device plugin", sriovnetworkv1.DevicePluginResourceProvider()),
		)

		DescribeTable("keeps the previous provider until its replacement is ready", func(provider sriovnetworkv1.ResourceProvider) {
			replacement, previous, path := consts.DRADriverDaemonSetName, devicePluginDaemonSetName, consts.DRADriverPath
			if provider.DevicePlugin {
				replacement, previous, path = previous, replacement, consts.PluginPath
			}
			old := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: previous, Namespace: vars.Namespace}}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(old).Build()
			renders := 0
			r := &SriovOperatorConfigReconciler{Client: cl, Scheme: scheme, FeatureGate: featuregate.New(),
				renderManifestFn: func(gotPath string, _ *render.RenderData) ([]*unstructured.Unstructured, error) {
					renders++
					Expect(gotPath).To(Equal(path), "cleanup must wait until the replacement is ready")
					return nil, nil
				},
				ensureProviderRolloutFn: func(_ context.Context, name string) (bool, error) {
					Expect(name).To(Equal(replacement))
					return false, nil
				},
			}
			config := &sriovnetworkv1.SriovOperatorConfig{Status: sriovnetworkv1.SriovOperatorConfigStatus{ResourceProvider: provider}}
			ready, err := r.syncResourceProvider(ctx, config)
			Expect(err).NotTo(HaveOccurred())
			Expect(ready).To(BeFalse())
			Expect(renders).To(Equal(1), "only the replacement inventory should be rendered while waiting")
			Expect(cl.Get(ctx, client.ObjectKeyFromObject(old), old)).To(Succeed(), "the previous provider must remain installed")
		},
			Entry("device plugin to DRA", sriovnetworkv1.DRAResourceProvider()),
			Entry("DRA to device plugin", sriovnetworkv1.DevicePluginResourceProvider()),
		)
	})
})
