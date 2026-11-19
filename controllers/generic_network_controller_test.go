package controllers

import (
	"context"
	"sync"
	"time"

	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/status"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util"
)

var _ = Describe("All Network Controllers", Ordered, func() {
	var cancel context.CancelFunc
	var ctx context.Context
	BeforeAll(func() {
		By("Setup controller manager")
		k8sManager, err := setupK8sManagerForTest()
		Expect(err).ToNot(HaveOccurred())

		statusPatcher := status.NewPatcher(k8sManager.GetClient(), k8sManager.GetEventRecorder("test"), k8sManager.GetScheme(), "test")

		err = (&SriovNetworkReconciler{
			Client:        k8sManager.GetClient(),
			Scheme:        k8sManager.GetScheme(),
			StatusPatcher: statusPatcher,
		}).SetupWithManager(k8sManager)
		Expect(err).ToNot(HaveOccurred())

		err = (&SriovIBNetworkReconciler{
			Client:        k8sManager.GetClient(),
			Scheme:        k8sManager.GetScheme(),
			StatusPatcher: statusPatcher,
		}).SetupWithManager(k8sManager)
		Expect(err).ToNot(HaveOccurred())

		err = (&OVSNetworkReconciler{
			Client:        k8sManager.GetClient(),
			Scheme:        k8sManager.GetScheme(),
			StatusPatcher: statusPatcher,
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
			By("Shutdown controller manager")
			cancel()
			wg.Wait()
		})
	})

	Context("NAD cleanup with stale LASTNETWORKNAMESPACE annotation", func() {
		AfterEach(func() {
			cleanNetworksInNamespace(testNamespace)
			cleanNetworksInNamespace("default")
		})

		It("should create NAD even when old namespace NAD does not exist", func() {
			By("Creating a SriovNetwork with a stale LASTNETWORKNAMESPACE annotation")
			cr := sriovnetworkv1.SriovNetwork{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "stale-ns-net",
					Namespace: testNamespace,
					Annotations: map[string]string{
						sriovnetworkv1.LASTNETWORKNAMESPACE: "non-existent-namespace",
					},
				},
				Spec: sriovnetworkv1.SriovNetworkSpec{
					ResourceName:     "resource_1",
					NetworkNamespace: "default",
				},
			}

			err := k8sClient.Create(ctx, &cr)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that the NAD is created despite the stale annotation")
			netAttDef := &netattdefv1.NetworkAttachmentDefinition{}
			err = util.WaitForNamespacedObject(netAttDef, k8sClient, "default", cr.GetName(), util.RetryInterval, util.Timeout)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("owner-reference annotations", func() {
		AfterEach(func() {
			cleanNetworksInNamespace(testNamespace)
			cleanNetworksInNamespace("default")
		})

		It("applies to new netattachdef", func() {
			cr := sriovnetworkv1.SriovNetwork{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sriovnet-blue",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkSpec{
					NetworkNamespace: "default",
				},
			}

			err := k8sClient.Create(ctx, &cr)
			Expect(err).NotTo(HaveOccurred())

			netAttDef := &netattdefv1.NetworkAttachmentDefinition{}
			err = util.WaitForNamespacedObject(netAttDef, k8sClient, "default", cr.GetName(), util.RetryInterval, util.Timeout)
			Expect(err).NotTo(HaveOccurred())

			Expect(netAttDef.GetAnnotations()).To(HaveKeyWithValue(consts.OwnerRefAnnotation, "SriovNetwork.sriovnetwork.openshift.io/openshift-sriov-network-operator/sriovnet-blue"))
		})

		It("should migrate existing NetAttachDef to have a value for Owner annotation", func() {
			netAttachDef := netattdefv1.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "netuser", Namespace: "default"},
				Spec: netattdefv1.NetworkAttachmentDefinitionSpec{
					Config: "user created configuration, to be overridden",
				},
			}
			err := k8sClient.Create(ctx, &netAttachDef)
			Expect(err).NotTo(HaveOccurred())

			cr := sriovnetworkv1.SriovNetwork{
				ObjectMeta: metav1.ObjectMeta{Name: "netuser", Namespace: testNamespace},
				Spec:       sriovnetworkv1.SriovNetworkSpec{NetworkNamespace: "default"},
			}
			err = k8sClient.Create(ctx, &cr)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				netAttDef := &netattdefv1.NetworkAttachmentDefinition{}
				err = util.WaitForNamespacedObject(netAttDef, k8sClient, "default", cr.GetName(), util.RetryInterval, util.Timeout)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(netAttDef.GetAnnotations()).
					To(HaveKeyWithValue(consts.OwnerRefAnnotation, "SriovNetwork.sriovnetwork.openshift.io/openshift-sriov-network-operator/netuser"))
				g.Expect(netAttDef.Spec.Config).To(Equal(generateExpectedNetConfig(&cr)))
			}).WithPolling(30 * time.Millisecond).WithTimeout(300 * time.Millisecond).Should(Succeed())
		})

		Context("does not override the NetAttachDefinition if the Owner annotation does not match", func() {
			It("when using different network type with the same name", func() {
				cr := sriovnetworkv1.SriovNetwork{
					ObjectMeta: metav1.ObjectMeta{Name: "net1", Namespace: testNamespace},
					Spec:       sriovnetworkv1.SriovNetworkSpec{NetworkNamespace: "default"},
				}
				err := k8sClient.Create(ctx, &cr)
				Expect(err).NotTo(HaveOccurred())

				netAttDef := &netattdefv1.NetworkAttachmentDefinition{}
				err = util.WaitForNamespacedObject(netAttDef, k8sClient, "default", cr.GetName(), util.RetryInterval, util.Timeout)
				Expect(err).NotTo(HaveOccurred())

				cr2 := sriovnetworkv1.SriovIBNetwork{
					ObjectMeta: metav1.ObjectMeta{Name: "net1", Namespace: testNamespace},
					Spec:       sriovnetworkv1.SriovIBNetworkSpec{NetworkNamespace: "default"},
				}
				err = k8sClient.Create(ctx, &cr2)
				Expect(err).NotTo(HaveOccurred())

				Consistently(func(g Gomega) {
					netAttDef := &netattdefv1.NetworkAttachmentDefinition{}
					err = util.WaitForNamespacedObject(netAttDef, k8sClient, "default", cr.GetName(), util.RetryInterval, util.Timeout)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(netAttDef.Spec.Config).To(ContainSubstring(`"sriov"`))
					g.Expect(netAttDef.Spec.Config).ToNot(ContainSubstring(`"ib-sriov"`))
				}).WithPolling(30 * time.Millisecond).WithTimeout(300 * time.Millisecond).Should(Succeed())

				By("Check that the second network has Ready=False due to ownership conflict")
				Eventually(func(g Gomega) {
					network := &sriovnetworkv1.SriovIBNetwork{}
					err := k8sClient.Get(ctx, client.ObjectKey{Name: cr2.Name, Namespace: cr2.Namespace}, network)
					g.Expect(err).NotTo(HaveOccurred())

					conditions := network.Status.Conditions
					g.Expect(conditions).NotTo(BeEmpty())

					// The second network should have Ready=False since it couldn't provision
					readyCondition := findCondition(conditions, sriovnetworkv1.ConditionReady)
					g.Expect(readyCondition).NotTo(BeNil())
					g.Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
					g.Expect(readyCondition.Reason).To(Equal(sriovnetworkv1.ReasonNetworkAttachmentDefInvalid))
				}).WithTimeout(5 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())
			})

			It("when using the same network type with the same name, in different namespaces", func() {
				cr := sriovnetworkv1.SriovNetwork{
					ObjectMeta: metav1.ObjectMeta{Name: "net2", Namespace: testNamespace},
					Spec: sriovnetworkv1.SriovNetworkSpec{
						NetworkNamespace: "default",
						MinTxRate:        ptr.To(42),
					},
				}
				err := k8sClient.Create(ctx, &cr)
				Expect(err).NotTo(HaveOccurred())

				netAttDef := &netattdefv1.NetworkAttachmentDefinition{}
				err = util.WaitForNamespacedObject(netAttDef, k8sClient, "default", cr.GetName(), util.RetryInterval, util.Timeout)
				Expect(err).NotTo(HaveOccurred())

				cr2 := sriovnetworkv1.SriovNetwork{
					ObjectMeta: metav1.ObjectMeta{Name: "net2", Namespace: "default"},
					Spec: sriovnetworkv1.SriovNetworkSpec{
						MinTxRate: ptr.To(84),
					},
				}
				err = k8sClient.Create(ctx, &cr2)
				Expect(err).NotTo(HaveOccurred())

				Consistently(func(g Gomega) {
					netAttDef := &netattdefv1.NetworkAttachmentDefinition{}
					err = util.WaitForNamespacedObject(netAttDef, k8sClient, "default", cr.GetName(), util.RetryInterval, util.Timeout)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(netAttDef.Spec.Config).To(ContainSubstring(`"min_tx_rate": 42`))
					g.Expect(netAttDef.Spec.Config).ToNot(ContainSubstring(`"min_tx_rate": 84`))
				}).WithPolling(30 * time.Millisecond).WithTimeout(300 * time.Millisecond).Should(Succeed())

				By("Check that the second network has Ready=False due to same name conflict")
				Eventually(func(g Gomega) {
					network := &sriovnetworkv1.SriovNetwork{}
					err := k8sClient.Get(ctx, client.ObjectKey{Name: cr2.Name, Namespace: cr2.Namespace}, network)
					g.Expect(err).NotTo(HaveOccurred())

					conditions := network.Status.Conditions
					g.Expect(conditions).NotTo(BeEmpty())

					// The second network should have Ready=False since it couldn't provision
					readyCondition := findCondition(conditions, sriovnetworkv1.ConditionReady)
					g.Expect(readyCondition).NotTo(BeNil())
					g.Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
					g.Expect(readyCondition.Reason).To(Equal(sriovnetworkv1.ReasonNetworkAttachmentDefInvalid))
				}).WithTimeout(5 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())
			})
		})
	})

	Context("Conditions", func() {

		AfterEach(func() {
			cleanNetworksInNamespace(testNamespace)
			cleanNetworksInNamespace("default")
		})

		It("should set Ready condition when NetworkAttachmentDefinition is created", func() {
			cr := sriovnetworkv1.SriovNetwork{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ready-condition",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkSpec{
					NetworkNamespace: "default",
					ResourceName:     "test-resource",
				},
			}

			err := k8sClient.Create(ctx, &cr)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				network := &sriovnetworkv1.SriovNetwork{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: cr.Name, Namespace: cr.Namespace}, network)
				g.Expect(err).NotTo(HaveOccurred())

				// Check Ready condition is True
				g.Expect(network.Status.Conditions).ToNot(BeEmpty())
				readyCondition := findCondition(network.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCondition).ToNot(BeNil())
				g.Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(readyCondition.Reason).To(Equal(sriovnetworkv1.ReasonNetworkReady))
				g.Expect(readyCondition.ObservedGeneration).To(Equal(network.Generation))
			}).WithTimeout(5 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())
		})

		It("should set Ready=True immediately after NAD creation (not waiting state)", func() {
			// This test verifies the fix for the bug where after successfully creating
			// the NetworkAttachmentDefinition, the controller incorrectly set conditions
			// indicating it was waiting for the namespace instead of being ready.
			cr := sriovnetworkv1.SriovNetwork{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nad-creation-condition",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkSpec{
					NetworkNamespace: "default",
					ResourceName:     "test-resource-creation",
				},
			}

			err := k8sClient.Create(ctx, &cr)
			Expect(err).NotTo(HaveOccurred())

			// First, wait for the NAD to be created
			netAttDef := &netattdefv1.NetworkAttachmentDefinition{}
			err = util.WaitForNamespacedObject(netAttDef, k8sClient, "default", cr.GetName(), util.RetryInterval, util.Timeout)
			Expect(err).NotTo(HaveOccurred())

			// Now check that conditions are correctly set to Ready=True
			// This should happen immediately after NAD creation, not require a second reconcile
			Eventually(func(g Gomega) {
				network := &sriovnetworkv1.SriovNetwork{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: cr.Name, Namespace: cr.Namespace}, network)
				g.Expect(err).NotTo(HaveOccurred())

				// Conditions must be set
				g.Expect(network.Status.Conditions).ToNot(BeEmpty())

				// Ready must be True (not False with "waiting for namespace" reason)
				readyCondition := findCondition(network.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCondition).ToNot(BeNil())
				g.Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue),
					"Ready condition should be True after NAD creation, got %s with reason %s",
					readyCondition.Status, readyCondition.Reason)
				g.Expect(readyCondition.Reason).To(Equal(sriovnetworkv1.ReasonNetworkReady),
					"Ready reason should be NetworkReady, not waiting for namespace")

			}).WithTimeout(5 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())
		})

		It("should set Ready to False when waiting for namespace", func() {
			cr := sriovnetworkv1.OVSNetwork{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-waiting-condition",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.OVSNetworkSpec{
					NetworkNamespace: "nonexistent-namespace",
					ResourceName:     "test-resource",
				},
			}

			err := k8sClient.Create(ctx, &cr)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				network := &sriovnetworkv1.OVSNetwork{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: cr.Name, Namespace: cr.Namespace}, network)
				g.Expect(err).NotTo(HaveOccurred())

				// Check Ready condition is False with NotReady reason
				g.Expect(network.Status.Conditions).ToNot(BeEmpty())
				readyCondition := findCondition(network.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCondition).ToNot(BeNil())
				g.Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCondition.Reason).To(Equal(sriovnetworkv1.ReasonNamespaceNotFound))
				g.Expect(readyCondition.Message).To(ContainSubstring("does not exist"))
			}).WithTimeout(5 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())
		})

		It("should update ObservedGeneration when spec changes", func() {
			cr := sriovnetworkv1.SriovNetwork{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-generation",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkSpec{
					NetworkNamespace: "default",
					ResourceName:     "test-resource",
					Vlan:             100,
				},
			}

			err := k8sClient.Create(ctx, &cr)
			Expect(err).NotTo(HaveOccurred())

			var initialGeneration int64
			Eventually(func(g Gomega) {
				network := &sriovnetworkv1.SriovNetwork{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: cr.Name, Namespace: cr.Namespace}, network)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(network.Status.Conditions).ToNot(BeEmpty())

				readyCondition := findCondition(network.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCondition).ToNot(BeNil())
				initialGeneration = readyCondition.ObservedGeneration
			}).WithTimeout(5 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())

			// Update the spec
			Eventually(func(g Gomega) {
				network := &sriovnetworkv1.SriovNetwork{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: cr.Name, Namespace: cr.Namespace}, network)
				g.Expect(err).NotTo(HaveOccurred())

				network.Spec.Vlan = 200
				err = k8sClient.Update(ctx, network)
				g.Expect(err).NotTo(HaveOccurred())
			}).WithTimeout(5 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())

			// Verify ObservedGeneration is updated
			Eventually(func(g Gomega) {
				network := &sriovnetworkv1.SriovNetwork{}
				err := k8sClient.Get(ctx, client.ObjectKey{Name: cr.Name, Namespace: cr.Namespace}, network)
				g.Expect(err).NotTo(HaveOccurred())

				readyCondition := findCondition(network.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCondition).ToNot(BeNil())
				g.Expect(readyCondition.ObservedGeneration).To(BeNumerically(">", initialGeneration))
			}).WithTimeout(5 * time.Second).WithPolling(100 * time.Millisecond).Should(Succeed())
		})

		It("should work for all network types (SriovNetwork, SriovIBNetwork, OVSNetwork)", func() {
			networks := []struct {
				name string
				obj  client.Object
			}{
				{
					name: "sriov-test",
					obj: &sriovnetworkv1.SriovNetwork{
						ObjectMeta: metav1.ObjectMeta{Name: "sriov-test", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovNetworkSpec{NetworkNamespace: "default", ResourceName: "test"},
					},
				},
				{
					name: "sriovib-test",
					obj: &sriovnetworkv1.SriovIBNetwork{
						ObjectMeta: metav1.ObjectMeta{Name: "sriovib-test", Namespace: testNamespace},
						Spec:       sriovnetworkv1.SriovIBNetworkSpec{NetworkNamespace: "default", ResourceName: "test"},
					},
				},
				{
					name: "ovs-test",
					obj: &sriovnetworkv1.OVSNetwork{
						ObjectMeta: metav1.ObjectMeta{Name: "ovs-test", Namespace: testNamespace},
						Spec:       sriovnetworkv1.OVSNetworkSpec{NetworkNamespace: "default", ResourceName: "test"},
					},
				},
			}

			for _, network := range networks {
				err := k8sClient.Create(ctx, network.obj)
				Expect(err).NotTo(HaveOccurred())

				Eventually(func(g Gomega) {
					var conditions []metav1.Condition
					switch network.obj.(type) {
					case *sriovnetworkv1.SriovNetwork:
						n := &sriovnetworkv1.SriovNetwork{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: network.name, Namespace: testNamespace}, n)).To(Succeed())
						conditions = n.Status.Conditions
					case *sriovnetworkv1.SriovIBNetwork:
						n := &sriovnetworkv1.SriovIBNetwork{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: network.name, Namespace: testNamespace}, n)).To(Succeed())
						conditions = n.Status.Conditions
					case *sriovnetworkv1.OVSNetwork:
						n := &sriovnetworkv1.OVSNetwork{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: network.name, Namespace: testNamespace}, n)).To(Succeed())
						conditions = n.Status.Conditions
					}

					g.Expect(conditions).ToNot(BeEmpty())
					readyCondition := findCondition(conditions, sriovnetworkv1.ConditionReady)
					g.Expect(readyCondition).ToNot(BeNil())
					g.Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
					g.Expect(readyCondition.Reason).To(Equal(sriovnetworkv1.ReasonNetworkReady))
				}, 5*time.Second, 100*time.Millisecond).Should(Succeed())
			}
		})
	})

})

var _ = Describe("Generic network reconciler stale namespace cleanup", func() {
	AfterEach(func() {
		for _, name := range []string{"stale-ns-existing-target", "stale-ns-missing-target"} {
			key := client.ObjectKey{Name: name, Namespace: testNamespace}
			network := &sriovnetworkv1.SriovNetwork{}
			err := k8sClient.Get(context.Background(), key, network)
			if !apierrors.IsNotFound(err) {
				Expect(err).NotTo(HaveOccurred())
				if len(network.GetFinalizers()) > 0 {
					network.SetFinalizers(nil)
					Expect(k8sClient.Update(context.Background(), network)).To(Succeed())
				}
				Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), network))).To(Succeed())
			}

			target := &netattdefv1.NetworkAttachmentDefinition{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), target))).To(Succeed())
		}
	})

	DescribeTable("removes the stale namespace annotation before reconciling an existing target NAD", func(concurrent bool) {
		const (
			name           = "stale-ns-existing-target"
			oldNamespace   = "removed-network-namespace"
			unrelatedKey   = "example.com/unrelated"
			unrelatedValue = "preserve-me"
		)

		trackedClient := &deleteTrackingClient{Client: k8sClient}
		injected := false
		if concurrent {
			trackedClient.beforePatch = func(ctx context.Context, obj client.Object) {
				if _, ok := obj.(*sriovnetworkv1.SriovNetwork); !ok || injected {
					return
				}
				injected = true
				fresh := &sriovnetworkv1.SriovNetwork{}
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), fresh)).To(Succeed())
				Expect(fresh.Annotations).To(HaveLen(1))
				fresh.Annotations[unrelatedKey] = unrelatedValue
				Expect(k8sClient.Update(ctx, fresh)).To(Succeed())
			}
		}
		reconciler := newGenericNetworkReconciler(
			trackedClient,
			scheme.Scheme,
			&SriovNetworkReconciler{},
			status.NewPatcher(trackedClient, nil, scheme.Scheme, "generic-network-test"),
		)

		target := &netattdefv1.NetworkAttachmentDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		}
		Expect(k8sClient.Create(context.Background(), target)).To(Succeed())
		targetUID := target.UID

		network := &sriovnetworkv1.SriovNetwork{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNamespace,
				Annotations: map[string]string{
					sriovnetworkv1.LASTNETWORKNAMESPACE: oldNamespace,
					unrelatedKey:                        unrelatedValue,
				},
			},
			Spec: sriovnetworkv1.SriovNetworkSpec{NetworkNamespace: "default"},
		}
		if concurrent {
			delete(network.Annotations, unrelatedKey)
		}
		Expect(k8sClient.Create(context.Background(), network)).To(Succeed())

		req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(network)}
		_, err := reconciler.Reconcile(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())

		updatedNetwork := &sriovnetworkv1.SriovNetwork{}
		Expect(k8sClient.Get(context.Background(), req.NamespacedName, updatedNetwork)).To(Succeed())
		Expect(updatedNetwork.GetAnnotations()).NotTo(HaveKey(sriovnetworkv1.LASTNETWORKNAMESPACE))
		Expect(updatedNetwork.GetAnnotations()).To(HaveKeyWithValue(unrelatedKey, unrelatedValue))

		updatedTarget := &netattdefv1.NetworkAttachmentDefinition{}
		Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(target), updatedTarget)).To(Succeed())
		Expect(updatedTarget.UID).To(Equal(targetUID))

		_, err = reconciler.Reconcile(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())
		Expect(injected).To(Equal(concurrent))
		Expect(trackedClient.deletedNamespaces).To(Equal([]string{oldNamespace}))
		Expect(k8sClient.Get(context.Background(), req.NamespacedName, updatedNetwork)).To(Succeed())
		Expect(updatedNetwork.Annotations).NotTo(HaveKey(sriovnetworkv1.LASTNETWORKNAMESPACE))
		Expect(updatedNetwork.Annotations).To(HaveKeyWithValue(unrelatedKey, unrelatedValue))

		updatedTarget = &netattdefv1.NetworkAttachmentDefinition{}
		Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(target), updatedTarget)).To(Succeed())
		Expect(updatedTarget.UID).To(Equal(targetUID))
	}, Entry("pre-existing unrelated annotation", false), Entry("concurrent unrelated annotation", true))

	It("records the target namespace after creating a missing target NAD", func() {
		const (
			name         = "stale-ns-missing-target"
			oldNamespace = "removed-network-namespace"
		)

		trackedClient := &deleteTrackingClient{Client: k8sClient}
		reconciler := newGenericNetworkReconciler(
			trackedClient,
			scheme.Scheme,
			&SriovNetworkReconciler{},
			status.NewPatcher(trackedClient, nil, scheme.Scheme, "generic-network-test"),
		)

		network := &sriovnetworkv1.SriovNetwork{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNamespace,
				Annotations: map[string]string{
					sriovnetworkv1.LASTNETWORKNAMESPACE: oldNamespace,
				},
			},
			Spec: sriovnetworkv1.SriovNetworkSpec{NetworkNamespace: "default"},
		}
		Expect(k8sClient.Create(context.Background(), network)).To(Succeed())

		req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(network)}
		_, err := reconciler.Reconcile(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())

		updatedNetwork := &sriovnetworkv1.SriovNetwork{}
		Expect(k8sClient.Get(context.Background(), req.NamespacedName, updatedNetwork)).To(Succeed())
		Expect(updatedNetwork.GetAnnotations()).To(HaveKeyWithValue(sriovnetworkv1.LASTNETWORKNAMESPACE, "default"))

		target := &netattdefv1.NetworkAttachmentDefinition{}
		targetKey := client.ObjectKey{Namespace: "default", Name: name}
		Expect(k8sClient.Get(context.Background(), targetKey, target)).To(Succeed())
		targetUID := target.UID

		_, err = reconciler.Reconcile(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())
		Expect(trackedClient.deletedNamespaces).To(Equal([]string{oldNamespace}))

		target = &netattdefv1.NetworkAttachmentDefinition{}
		Expect(k8sClient.Get(context.Background(), targetKey, target)).To(Succeed())
		Expect(target.UID).To(Equal(targetUID))
	})
})

type deleteTrackingClient struct {
	client.Client
	deletedNamespaces []string
	beforePatch       func(context.Context, client.Object)
}

// Patch injects a deterministic concurrent write immediately before cleanup.
func (c *deleteTrackingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if c.beforePatch != nil {
		c.beforePatch(ctx, obj)
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c *deleteTrackingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, ok := obj.(*netattdefv1.NetworkAttachmentDefinition); ok {
		c.deletedNamespaces = append(c.deletedNamespaces, obj.GetNamespace())
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func cleanNetworksInNamespace(namespace string) {
	ctx := context.Background()
	EventuallyWithOffset(1, func(g Gomega) {
		err := k8sClient.DeleteAllOf(ctx, &sriovnetworkv1.SriovNetwork{}, client.InNamespace(namespace))
		g.Expect(err).NotTo(HaveOccurred())

		err = k8sClient.DeleteAllOf(ctx, &sriovnetworkv1.SriovIBNetwork{}, client.InNamespace(namespace))
		g.Expect(err).NotTo(HaveOccurred())

		err = k8sClient.DeleteAllOf(ctx, &sriovnetworkv1.OVSNetwork{}, client.InNamespace(namespace))
		g.Expect(err).NotTo(HaveOccurred())

		k8sClient.DeleteAllOf(ctx, &netattdefv1.NetworkAttachmentDefinition{}, client.InNamespace(namespace))
		g.Expect(err).NotTo(HaveOccurred())

		sriovNetworks := &sriovnetworkv1.SriovNetworkList{}
		err = k8sClient.List(ctx, sriovNetworks, client.InNamespace(namespace))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(sriovNetworks.Items).To(BeEmpty())

		sriovIBNetworks := &sriovnetworkv1.SriovIBNetworkList{}
		err = k8sClient.List(ctx, sriovIBNetworks, client.InNamespace(namespace))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(sriovIBNetworks.Items).To(BeEmpty())

		ovsNetworks := &sriovnetworkv1.OVSNetworkList{}
		err = k8sClient.List(ctx, ovsNetworks, client.InNamespace(namespace))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(ovsNetworks.Items).To(BeEmpty())

		netAttachDefs := &netattdefv1.NetworkAttachmentDefinitionList{}
		err = k8sClient.List(ctx, netAttachDefs, client.InNamespace(namespace))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(netAttachDefs.Items).To(BeEmpty())
	}).WithPolling(100 * time.Millisecond).WithTimeout(10 * time.Second).Should(Succeed())
}
