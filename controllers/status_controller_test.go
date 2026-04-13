package controllers

import (
	"context"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

// ============================================================================
// SriovNetworkNodePolicyStatus Controller Tests
// ============================================================================

var _ = Describe("SriovNetworkNodePolicyStatus controller", Ordered, func() {
	var cancel context.CancelFunc
	var ctx context.Context

	BeforeAll(func() {
		By("Create SriovOperatorConfig controller k8s objs")
		config := makeDefaultSriovOpConfig()
		err := k8sClient.Create(context.Background(), config)
		if err != nil && !errors.IsAlreadyExists(err) {
			Expect(err).ToNot(HaveOccurred())
		}
		DeferCleanup(func() {
			err := k8sClient.Delete(context.Background(), config)
			if err != nil && !errors.IsNotFound(err) {
				Expect(err).ToNot(HaveOccurred())
			}
		})

		// setup controller manager
		By("Setup controller manager")
		k8sManager, err := setupK8sManagerForTest()
		Expect(err).ToNot(HaveOccurred())

		err = (&SriovNetworkNodePolicyStatusReconciler{
			Client: k8sManager.GetClient(),
			Scheme: k8sManager.GetScheme(),
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

	AfterEach(func() {
		err := k8sClient.DeleteAllOf(context.Background(), &corev1.Node{}, client.GracePeriodSeconds(0))
		Expect(err).ToNot(HaveOccurred())

		err = k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovNetworkNodePolicy{}, client.InNamespace(vars.Namespace), client.GracePeriodSeconds(0))
		Expect(err).ToNot(HaveOccurred())

		err = k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovNetworkNodeState{}, client.InNamespace(vars.Namespace), client.GracePeriodSeconds(0))
		Expect(err).ToNot(HaveOccurred())
	})

	Context("Policy status conditions", func() {
		It("should set NoMatchingNodes condition when no nodes match the policy", func() {
			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy-no-match",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					NodeSelector: map[string]string{"non-existent-label": "value"},
					NumVfs:       5,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(policy.Status.MatchedNodeCount).To(Equal(0))
				g.Expect(policy.Status.ReadyNodeCount).To(Equal(0))

				readyCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCond).ToNot(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonNoMatchingNodes))

				progressingCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionProgressing)
				g.Expect(progressingCond).ToNot(BeNil())
				g.Expect(progressingCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(progressingCond.Reason).To(Equal(sriovnetworkv1.ReasonNoMatchingNodes))

				degradedCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionDegraded)
				g.Expect(degradedCond).ToNot(BeNil())
				g.Expect(degradedCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(degradedCond.Reason).To(Equal(sriovnetworkv1.ReasonNoMatchingNodes))
			}, time.Minute, time.Second).Should(Succeed())
		})

		It("should set Ready condition when all matched nodes are ready", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "ready-node",
					Labels: map[string]string{
						"kubernetes.io/os":               "linux",
						"node-role.kubernetes.io/worker": "",
						"test-ready":                     "true",
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())

			nodeState := &sriovnetworkv1.SriovNetworkNodeState{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ready-node",
					Namespace: testNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, nodeState)).To(Succeed())

			// Get the nodeState back to get the resourceVersion for status update
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeState), nodeState)).To(Succeed())
			nodeState.Status.Conditions = []metav1.Condition{
				{
					Type:               sriovnetworkv1.ConditionReady,
					Status:             metav1.ConditionTrue,
					Reason:             sriovnetworkv1.ReasonNodeReady,
					Message:            "Node is ready",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionProgressing,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotProgressing,
					Message:            "Not progressing",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionDegraded,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotDegraded,
					Message:            "Not degraded",
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(k8sClient.Status().Update(ctx, nodeState)).To(Succeed())

			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy-ready",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					NodeSelector: map[string]string{"test-ready": "true"},
					NumVfs:       5,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(policy.Status.MatchedNodeCount).To(Equal(1))
				g.Expect(policy.Status.ReadyNodeCount).To(Equal(1))

				readyCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCond).ToNot(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonPolicyReady))

				progressingCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionProgressing)
				g.Expect(progressingCond).ToNot(BeNil())
				g.Expect(progressingCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(progressingCond.Reason).To(Equal(sriovnetworkv1.ReasonNotProgressing))

				degradedCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionDegraded)
				g.Expect(degradedCond).ToNot(BeNil())
				g.Expect(degradedCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(degradedCond.Reason).To(Equal(sriovnetworkv1.ReasonNotDegraded))
			}, time.Minute, time.Second).Should(Succeed())
		})

		It("should set Progressing condition when some nodes are progressing", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "progressing-node",
					Labels: map[string]string{
						"kubernetes.io/os":               "linux",
						"node-role.kubernetes.io/worker": "",
						"test-progressing":               "true",
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())

			nodeState := &sriovnetworkv1.SriovNetworkNodeState{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "progressing-node",
					Namespace: testNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, nodeState)).To(Succeed())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeState), nodeState)).To(Succeed())
			nodeState.Status.Conditions = []metav1.Condition{
				{
					Type:               sriovnetworkv1.ConditionReady,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotReady,
					Message:            "Node is not ready",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionProgressing,
					Status:             metav1.ConditionTrue,
					Reason:             sriovnetworkv1.ReasonApplyingConfiguration,
					Message:            "Applying configuration",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionDegraded,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotDegraded,
					Message:            "Not degraded",
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(k8sClient.Status().Update(ctx, nodeState)).To(Succeed())

			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy-progressing",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					NodeSelector: map[string]string{"test-progressing": "true"},
					NumVfs:       5,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(policy.Status.MatchedNodeCount).To(Equal(1))
				g.Expect(policy.Status.ReadyNodeCount).To(Equal(0))

				readyCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCond).ToNot(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonPolicyNotReady))

				progressingCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionProgressing)
				g.Expect(progressingCond).ToNot(BeNil())
				g.Expect(progressingCond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(progressingCond.Reason).To(Equal(sriovnetworkv1.ReasonSomeNodesProgressing))

				degradedCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionDegraded)
				g.Expect(degradedCond).ToNot(BeNil())
				g.Expect(degradedCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(degradedCond.Reason).To(Equal(sriovnetworkv1.ReasonNotDegraded))
			}, time.Minute, time.Second).Should(Succeed())
		})

		It("should set Degraded condition when some nodes are degraded", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "degraded-node",
					Labels: map[string]string{
						"kubernetes.io/os":               "linux",
						"node-role.kubernetes.io/worker": "",
						"test-degraded":                  "true",
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())

			nodeState := &sriovnetworkv1.SriovNetworkNodeState{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "degraded-node",
					Namespace: testNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, nodeState)).To(Succeed())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeState), nodeState)).To(Succeed())
			nodeState.Status.Conditions = []metav1.Condition{
				{
					Type:               sriovnetworkv1.ConditionReady,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotReady,
					Message:            "Node is not ready",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionProgressing,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotProgressing,
					Message:            "Not progressing",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionDegraded,
					Status:             metav1.ConditionTrue,
					Reason:             sriovnetworkv1.ReasonConfigurationFailed,
					Message:            "Configuration failed",
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(k8sClient.Status().Update(ctx, nodeState)).To(Succeed())

			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy-degraded",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					NodeSelector: map[string]string{"test-degraded": "true"},
					NumVfs:       5,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(policy.Status.MatchedNodeCount).To(Equal(1))
				g.Expect(policy.Status.ReadyNodeCount).To(Equal(0))

				readyCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCond).ToNot(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonPolicyNotReady))

				progressingCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionProgressing)
				g.Expect(progressingCond).ToNot(BeNil())
				g.Expect(progressingCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(progressingCond.Reason).To(Equal(sriovnetworkv1.ReasonNotProgressing))

				degradedCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionDegraded)
				g.Expect(degradedCond).ToNot(BeNil())
				g.Expect(degradedCond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(degradedCond.Reason).To(Equal(sriovnetworkv1.ReasonSomeNodesFailed))
			}, time.Minute, time.Second).Should(Succeed())
		})

		It("should set PartiallyApplied reason when some nodes are ready and some are not", func() {
			// Create two nodes
			node1 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "partial-node1",
					Labels: map[string]string{
						"kubernetes.io/os":               "linux",
						"node-role.kubernetes.io/worker": "",
						"test-partial":                   "true",
					},
				},
			}
			Expect(k8sClient.Create(ctx, node1)).To(Succeed())

			node2 := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "partial-node2",
					Labels: map[string]string{
						"kubernetes.io/os":               "linux",
						"node-role.kubernetes.io/worker": "",
						"test-partial":                   "true",
					},
				},
			}
			Expect(k8sClient.Create(ctx, node2)).To(Succeed())

			// Node1 state - ready
			nodeState1 := &sriovnetworkv1.SriovNetworkNodeState{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "partial-node1",
					Namespace: testNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, nodeState1)).To(Succeed())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeState1), nodeState1)).To(Succeed())
			nodeState1.Status.Conditions = []metav1.Condition{
				{
					Type:               sriovnetworkv1.ConditionReady,
					Status:             metav1.ConditionTrue,
					Reason:             sriovnetworkv1.ReasonNodeReady,
					Message:            "Node is ready",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionProgressing,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotProgressing,
					Message:            "Not progressing",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionDegraded,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotDegraded,
					Message:            "Not degraded",
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(k8sClient.Status().Update(ctx, nodeState1)).To(Succeed())

			// Node2 state - not ready, progressing
			nodeState2 := &sriovnetworkv1.SriovNetworkNodeState{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "partial-node2",
					Namespace: testNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, nodeState2)).To(Succeed())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeState2), nodeState2)).To(Succeed())
			nodeState2.Status.Conditions = []metav1.Condition{
				{
					Type:               sriovnetworkv1.ConditionReady,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotReady,
					Message:            "Node is not ready",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionProgressing,
					Status:             metav1.ConditionTrue,
					Reason:             sriovnetworkv1.ReasonApplyingConfiguration,
					Message:            "Applying configuration",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionDegraded,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotDegraded,
					Message:            "Not degraded",
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(k8sClient.Status().Update(ctx, nodeState2)).To(Succeed())

			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy-partial",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					NodeSelector: map[string]string{"test-partial": "true"},
					NumVfs:       5,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(policy.Status.MatchedNodeCount).To(Equal(2))
				g.Expect(policy.Status.ReadyNodeCount).To(Equal(1))

				readyCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCond).ToNot(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonPartiallyApplied))
				g.Expect(readyCond.Message).To(ContainSubstring("1 of 2"))

				progressingCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionProgressing)
				g.Expect(progressingCond).ToNot(BeNil())
				g.Expect(progressingCond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(progressingCond.Reason).To(Equal(sriovnetworkv1.ReasonSomeNodesProgressing))
			}, time.Minute, time.Second).Should(Succeed())
		})

		It("should update status when node labels change", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "label-change-node",
					Labels: map[string]string{
						"kubernetes.io/os":               "linux",
						"node-role.kubernetes.io/worker": "",
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())

			nodeState := &sriovnetworkv1.SriovNetworkNodeState{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "label-change-node",
					Namespace: testNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, nodeState)).To(Succeed())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeState), nodeState)).To(Succeed())
			nodeState.Status.Conditions = []metav1.Condition{
				{
					Type:               sriovnetworkv1.ConditionReady,
					Status:             metav1.ConditionTrue,
					Reason:             sriovnetworkv1.ReasonNodeReady,
					Message:            "Node is ready",
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(k8sClient.Status().Update(ctx, nodeState)).To(Succeed())

			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy-label-change",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					NodeSelector: map[string]string{"test-label-change": "true"},
					NumVfs:       5,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			// Initially no nodes match
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(policy.Status.MatchedNodeCount).To(Equal(0))
			}, time.Minute, time.Second).Should(Succeed())

			// Add matching label to node
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(Succeed())
			node.Labels["test-label-change"] = "true"
			Expect(k8sClient.Update(ctx, node)).To(Succeed())

			// Now one node should match
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(policy.Status.MatchedNodeCount).To(Equal(1))
				g.Expect(policy.Status.ReadyNodeCount).To(Equal(1))

				readyCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCond).ToNot(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonPolicyReady))
			}, time.Minute, time.Second).Should(Succeed())
		})

		It("should update status when NodeState conditions change", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "state-change-node",
					Labels: map[string]string{
						"kubernetes.io/os":               "linux",
						"node-role.kubernetes.io/worker": "",
						"test-state-change":              "true",
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())

			nodeState := &sriovnetworkv1.SriovNetworkNodeState{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "state-change-node",
					Namespace: testNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, nodeState)).To(Succeed())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeState), nodeState)).To(Succeed())
			nodeState.Status.Conditions = []metav1.Condition{
				{
					Type:               sriovnetworkv1.ConditionReady,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotReady,
					Message:            "Node is not ready",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionProgressing,
					Status:             metav1.ConditionTrue,
					Reason:             sriovnetworkv1.ReasonApplyingConfiguration,
					Message:            "Applying configuration",
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(k8sClient.Status().Update(ctx, nodeState)).To(Succeed())

			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy-state-change",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					NodeSelector: map[string]string{"test-state-change": "true"},
					NumVfs:       5,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			// Initially node is progressing
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(policy.Status.ReadyNodeCount).To(Equal(0))

				progressingCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionProgressing)
				g.Expect(progressingCond).ToNot(BeNil())
				g.Expect(progressingCond.Status).To(Equal(metav1.ConditionTrue))
			}, time.Minute, time.Second).Should(Succeed())

			// Update NodeState to ready
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeState), nodeState)).To(Succeed())
			nodeState.Status.Conditions = []metav1.Condition{
				{
					Type:               sriovnetworkv1.ConditionReady,
					Status:             metav1.ConditionTrue,
					Reason:             sriovnetworkv1.ReasonNodeReady,
					Message:            "Node is ready",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               sriovnetworkv1.ConditionProgressing,
					Status:             metav1.ConditionFalse,
					Reason:             sriovnetworkv1.ReasonNotProgressing,
					Message:            "Not progressing",
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(k8sClient.Status().Update(ctx, nodeState)).To(Succeed())

			// Policy should now show ready
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(policy.Status.ReadyNodeCount).To(Equal(1))

				readyCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionReady)
				g.Expect(readyCond).ToNot(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonPolicyReady))

				progressingCond := findCondition(policy.Status.Conditions, sriovnetworkv1.ConditionProgressing)
				g.Expect(progressingCond).ToNot(BeNil())
				g.Expect(progressingCond.Status).To(Equal(metav1.ConditionFalse))
			}, time.Minute, time.Second).Should(Succeed())
		})
	})
})

// ============================================================================
// Shared Helper Function Tests
// ============================================================================

var _ = Describe("buildStatusConditions", func() {
	It("should return NoMatchingNodes conditions when matchedNodeCount is 0", func() {
		conditions := buildStatusConditions(1, 0, 0, 0, 0)

		readyCond := findCondition(conditions, sriovnetworkv1.ConditionReady)
		Expect(readyCond).ToNot(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonNoMatchingNodes))

		progressingCond := findCondition(conditions, sriovnetworkv1.ConditionProgressing)
		Expect(progressingCond).ToNot(BeNil())
		Expect(progressingCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(progressingCond.Reason).To(Equal(sriovnetworkv1.ReasonNoMatchingNodes))

		degradedCond := findCondition(conditions, sriovnetworkv1.ConditionDegraded)
		Expect(degradedCond).ToNot(BeNil())
		Expect(degradedCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(degradedCond.Reason).To(Equal(sriovnetworkv1.ReasonNoMatchingNodes))
	})

	It("should return Ready=True when all nodes are ready", func() {
		conditions := buildStatusConditions(1, 3, 3, 0, 0)

		readyCond := findCondition(conditions, sriovnetworkv1.ConditionReady)
		Expect(readyCond).ToNot(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
		Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonPolicyReady))
		Expect(readyCond.Message).To(ContainSubstring("All 3 matched nodes are ready"))
	})

	It("should return Ready=False with PolicyNotReady when no nodes are ready", func() {
		conditions := buildStatusConditions(1, 3, 0, 1, 0)

		readyCond := findCondition(conditions, sriovnetworkv1.ConditionReady)
		Expect(readyCond).ToNot(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonPolicyNotReady))
		Expect(readyCond.Message).To(ContainSubstring("0 of 3 matched nodes are ready"))
	})

	It("should return Ready=False with PartiallyApplied when some nodes are ready", func() {
		conditions := buildStatusConditions(1, 3, 2, 0, 0)

		readyCond := findCondition(conditions, sriovnetworkv1.ConditionReady)
		Expect(readyCond).ToNot(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonPartiallyApplied))
		Expect(readyCond.Message).To(ContainSubstring("2 of 3 matched nodes are ready"))
	})

	It("should return Progressing=True when some nodes are progressing", func() {
		conditions := buildStatusConditions(1, 3, 1, 2, 0)

		progressingCond := findCondition(conditions, sriovnetworkv1.ConditionProgressing)
		Expect(progressingCond).ToNot(BeNil())
		Expect(progressingCond.Status).To(Equal(metav1.ConditionTrue))
		Expect(progressingCond.Reason).To(Equal(sriovnetworkv1.ReasonSomeNodesProgressing))
		Expect(progressingCond.Message).To(ContainSubstring("2 of 3 matched nodes are progressing"))
	})

	It("should return Progressing=False when no nodes are progressing", func() {
		conditions := buildStatusConditions(1, 3, 3, 0, 0)

		progressingCond := findCondition(conditions, sriovnetworkv1.ConditionProgressing)
		Expect(progressingCond).ToNot(BeNil())
		Expect(progressingCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(progressingCond.Reason).To(Equal(sriovnetworkv1.ReasonNotProgressing))
	})

	It("should return Degraded=True when some nodes are degraded", func() {
		conditions := buildStatusConditions(1, 3, 1, 0, 2)

		degradedCond := findCondition(conditions, sriovnetworkv1.ConditionDegraded)
		Expect(degradedCond).ToNot(BeNil())
		Expect(degradedCond.Status).To(Equal(metav1.ConditionTrue))
		Expect(degradedCond.Reason).To(Equal(sriovnetworkv1.ReasonSomeNodesFailed))
		Expect(degradedCond.Message).To(ContainSubstring("2 of 3 matched nodes are degraded"))
	})

	It("should return Degraded=False when no nodes are degraded", func() {
		conditions := buildStatusConditions(1, 3, 3, 0, 0)

		degradedCond := findCondition(conditions, sriovnetworkv1.ConditionDegraded)
		Expect(degradedCond).ToNot(BeNil())
		Expect(degradedCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(degradedCond.Reason).To(Equal(sriovnetworkv1.ReasonNotDegraded))
	})

	It("should set ObservedGeneration correctly", func() {
		conditions := buildStatusConditions(5, 1, 1, 0, 0)

		for _, cond := range conditions {
			Expect(cond.ObservedGeneration).To(Equal(int64(5)))
		}
	})
})

var _ = Describe("isConditionTrue", func() {
	It("should return true when condition exists and has status True", func() {
		conditions := []metav1.Condition{
			{
				Type:   sriovnetworkv1.ConditionReady,
				Status: metav1.ConditionTrue,
			},
		}
		Expect(isConditionTrue(conditions, sriovnetworkv1.ConditionReady)).To(BeTrue())
	})

	It("should return false when condition exists but has status False", func() {
		conditions := []metav1.Condition{
			{
				Type:   sriovnetworkv1.ConditionReady,
				Status: metav1.ConditionFalse,
			},
		}
		Expect(isConditionTrue(conditions, sriovnetworkv1.ConditionReady)).To(BeFalse())
	})

	It("should return false when condition does not exist", func() {
		conditions := []metav1.Condition{
			{
				Type:   sriovnetworkv1.ConditionProgressing,
				Status: metav1.ConditionTrue,
			},
		}
		Expect(isConditionTrue(conditions, sriovnetworkv1.ConditionReady)).To(BeFalse())
	})

	It("should return false for empty conditions slice", func() {
		Expect(isConditionTrue([]metav1.Condition{}, sriovnetworkv1.ConditionReady)).To(BeFalse())
	})
})

// ============================================================================
// SriovNetworkNodePolicyStatusReconciler Unit Tests
// ============================================================================

var _ = Describe("SriovNetworkNodePolicyStatusReconciler unit tests", Ordered, func() {
	Context("with fake client", func() {
		var (
			ctx        context.Context
			reconciler *SriovNetworkNodePolicyStatusReconciler
			scheme     *runtime.Scheme
		)

		BeforeEach(func() {
			ctx = context.Background()
			scheme = runtime.NewScheme()
			utilruntime.Must(sriovnetworkv1.AddToScheme(scheme))
			utilruntime.Must(corev1.AddToScheme(scheme))
		})

		It("should handle policy not found gracefully", func() {
			reconciler = &SriovNetworkNodePolicyStatusReconciler{
				Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
				Scheme: scheme,
			}

			// Policy doesn't exist, should return without error
			result, err := reconciler.Reconcile(ctx, newReconcileRequest("non-existent", testNamespace))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should handle nodes without NodeState gracefully", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "no-state-node",
					Labels: map[string]string{"test": "true"},
				},
			}

			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					NodeSelector: map[string]string{"test": "true"},
					NumVfs:       5,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				},
			}

			reconciler = &SriovNetworkNodePolicyStatusReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(node, policy).
					WithStatusSubresource(policy).
					Build(),
				Scheme: scheme,
			}

			result, err := reconciler.Reconcile(ctx, newReconcileRequest("test-policy", testNamespace))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// Check that policy status was updated with matched node but no ready nodes
			updatedPolicy := &sriovnetworkv1.SriovNetworkNodePolicy{}
			Expect(reconciler.Get(ctx, client.ObjectKeyFromObject(policy), updatedPolicy)).To(Succeed())
			Expect(updatedPolicy.Status.MatchedNodeCount).To(Equal(1))
			Expect(updatedPolicy.Status.ReadyNodeCount).To(Equal(0))
		})

		It("should aggregate conditions from multiple nodes", func() {
			nodes := []client.Object{
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "multi-node1",
						Labels: map[string]string{"test-multi": "true"},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "multi-node2",
						Labels: map[string]string{"test-multi": "true"},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "multi-node3",
						Labels: map[string]string{"test-multi": "true"},
					},
				},
			}

			nodeStates := []client.Object{
				&sriovnetworkv1.SriovNetworkNodeState{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "multi-node1",
						Namespace: testNamespace,
					},
					Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
						Conditions: []metav1.Condition{
							{Type: sriovnetworkv1.ConditionReady, Status: metav1.ConditionTrue},
							{Type: sriovnetworkv1.ConditionProgressing, Status: metav1.ConditionFalse},
							{Type: sriovnetworkv1.ConditionDegraded, Status: metav1.ConditionFalse},
						},
					},
				},
				&sriovnetworkv1.SriovNetworkNodeState{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "multi-node2",
						Namespace: testNamespace,
					},
					Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
						Conditions: []metav1.Condition{
							{Type: sriovnetworkv1.ConditionReady, Status: metav1.ConditionFalse},
							{Type: sriovnetworkv1.ConditionProgressing, Status: metav1.ConditionTrue},
							{Type: sriovnetworkv1.ConditionDegraded, Status: metav1.ConditionFalse},
						},
					},
				},
				&sriovnetworkv1.SriovNetworkNodeState{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "multi-node3",
						Namespace: testNamespace,
					},
					Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
						Conditions: []metav1.Condition{
							{Type: sriovnetworkv1.ConditionReady, Status: metav1.ConditionFalse},
							{Type: sriovnetworkv1.ConditionProgressing, Status: metav1.ConditionFalse},
							{Type: sriovnetworkv1.ConditionDegraded, Status: metav1.ConditionTrue},
						},
					},
				},
			}

			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "multi-policy",
					Namespace:  testNamespace,
					Generation: 2,
				},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					NodeSelector: map[string]string{"test-multi": "true"},
					NumVfs:       5,
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: "8086"},
				},
			}

			allObjects := append(nodes, nodeStates...)
			allObjects = append(allObjects, policy)

			reconciler = &SriovNetworkNodePolicyStatusReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(allObjects...).
					WithStatusSubresource(policy).
					Build(),
				Scheme: scheme,
			}

			result, err := reconciler.Reconcile(ctx, newReconcileRequest("multi-policy", testNamespace))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			updatedPolicy := &sriovnetworkv1.SriovNetworkNodePolicy{}
			Expect(reconciler.Get(ctx, client.ObjectKeyFromObject(policy), updatedPolicy)).To(Succeed())

			// 3 nodes match, 1 is ready
			Expect(updatedPolicy.Status.MatchedNodeCount).To(Equal(3))
			Expect(updatedPolicy.Status.ReadyNodeCount).To(Equal(1))

			// Ready should be False with PartiallyApplied reason
			readyCond := findCondition(updatedPolicy.Status.Conditions, sriovnetworkv1.ConditionReady)
			Expect(readyCond).ToNot(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonPartiallyApplied))

			// Progressing should be True (1 node is progressing)
			progressingCond := findCondition(updatedPolicy.Status.Conditions, sriovnetworkv1.ConditionProgressing)
			Expect(progressingCond).ToNot(BeNil())
			Expect(progressingCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressingCond.Reason).To(Equal(sriovnetworkv1.ReasonSomeNodesProgressing))

			// Degraded should be True (1 node is degraded)
			degradedCond := findCondition(updatedPolicy.Status.Conditions, sriovnetworkv1.ConditionDegraded)
			Expect(degradedCond).ToNot(BeNil())
			Expect(degradedCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(degradedCond.Reason).To(Equal(sriovnetworkv1.ReasonSomeNodesFailed))

			// ObservedGeneration should be set correctly
			Expect(readyCond.ObservedGeneration).To(Equal(int64(2)))
		})
	})
})

// ============================================================================
// SriovNetworkPoolConfigStatusReconciler Unit Tests
// ============================================================================

var _ = Describe("SriovNetworkPoolConfigStatusReconciler unit tests", Ordered, func() {
	Context("with fake client", func() {
		var (
			ctx        context.Context
			reconciler *SriovNetworkPoolConfigStatusReconciler
			scheme     *runtime.Scheme
		)

		BeforeEach(func() {
			ctx = context.Background()
			scheme = runtime.NewScheme()
			utilruntime.Must(sriovnetworkv1.AddToScheme(scheme))
			utilruntime.Must(corev1.AddToScheme(scheme))
		})

		It("should handle pool config not found gracefully", func() {
			reconciler = &SriovNetworkPoolConfigStatusReconciler{
				Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
				Scheme: scheme,
			}

			// PoolConfig doesn't exist, should return without error
			result, err := reconciler.Reconcile(ctx, newReconcileRequest("non-existent", testNamespace))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should skip OVS hardware offload pool configs", func() {
			poolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovs-hwol-config",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkPoolConfigSpec{
					OvsHardwareOffloadConfig: sriovnetworkv1.OvsHardwareOffloadConfig{
						Name: "worker-hwol",
					},
				},
			}

			reconciler = &SriovNetworkPoolConfigStatusReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(poolConfig).
					WithStatusSubresource(poolConfig).
					Build(),
				Scheme: scheme,
			}

			result, err := reconciler.Reconcile(ctx, newReconcileRequest("ovs-hwol-config", testNamespace))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// Status should not be updated for OVS HWOL configs
			updatedPoolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{}
			Expect(reconciler.Get(ctx, client.ObjectKeyFromObject(poolConfig), updatedPoolConfig)).To(Succeed())
			Expect(updatedPoolConfig.Status.MatchedNodeCount).To(Equal(0))
			Expect(updatedPoolConfig.Status.Conditions).To(BeEmpty())
		})

		It("should match all nodes when nodeSelector is nil", func() {
			nodes := []client.Object{
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "node1",
						Labels: map[string]string{"role": "worker"},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "node2",
						Labels: map[string]string{"role": "master"},
					},
				},
			}

			nodeStates := []client.Object{
				&sriovnetworkv1.SriovNetworkNodeState{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "node1",
						Namespace: testNamespace,
					},
					Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
						Conditions: []metav1.Condition{
							{Type: sriovnetworkv1.ConditionReady, Status: metav1.ConditionTrue},
						},
					},
				},
				&sriovnetworkv1.SriovNetworkNodeState{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "node2",
						Namespace: testNamespace,
					},
					Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
						Conditions: []metav1.Condition{
							{Type: sriovnetworkv1.ConditionReady, Status: metav1.ConditionTrue},
						},
					},
				},
			}

			poolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-selector-pool",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkPoolConfigSpec{
					NodeSelector: nil, // nil means match all
				},
			}

			allObjects := append(nodes, nodeStates...)
			allObjects = append(allObjects, poolConfig)

			reconciler = &SriovNetworkPoolConfigStatusReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(allObjects...).
					WithStatusSubresource(poolConfig).
					Build(),
				Scheme: scheme,
			}

			result, err := reconciler.Reconcile(ctx, newReconcileRequest("nil-selector-pool", testNamespace))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			updatedPoolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{}
			Expect(reconciler.Get(ctx, client.ObjectKeyFromObject(poolConfig), updatedPoolConfig)).To(Succeed())
			Expect(updatedPoolConfig.Status.MatchedNodeCount).To(Equal(2))
			Expect(updatedPoolConfig.Status.ReadyNodeCount).To(Equal(2))
		})

		It("should match nodes using label selector with matchLabels", func() {
			nodes := []client.Object{
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "worker-node",
						Labels: map[string]string{"pool": "worker"},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "master-node",
						Labels: map[string]string{"pool": "master"},
					},
				},
			}

			nodeStates := []client.Object{
				&sriovnetworkv1.SriovNetworkNodeState{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "worker-node",
						Namespace: testNamespace,
					},
					Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
						Conditions: []metav1.Condition{
							{Type: sriovnetworkv1.ConditionReady, Status: metav1.ConditionTrue},
						},
					},
				},
			}

			poolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-pool",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkPoolConfigSpec{
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"pool": "worker"},
					},
				},
			}

			allObjects := append(nodes, nodeStates...)
			allObjects = append(allObjects, poolConfig)

			reconciler = &SriovNetworkPoolConfigStatusReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(allObjects...).
					WithStatusSubresource(poolConfig).
					Build(),
				Scheme: scheme,
			}

			result, err := reconciler.Reconcile(ctx, newReconcileRequest("worker-pool", testNamespace))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			updatedPoolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{}
			Expect(reconciler.Get(ctx, client.ObjectKeyFromObject(poolConfig), updatedPoolConfig)).To(Succeed())
			Expect(updatedPoolConfig.Status.MatchedNodeCount).To(Equal(1))
			Expect(updatedPoolConfig.Status.ReadyNodeCount).To(Equal(1))
		})

		It("should aggregate conditions from multiple nodes", func() {
			nodes := []client.Object{
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "pool-node1",
						Labels: map[string]string{"test-pool": "true"},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "pool-node2",
						Labels: map[string]string{"test-pool": "true"},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "pool-node3",
						Labels: map[string]string{"test-pool": "true"},
					},
				},
			}

			nodeStates := []client.Object{
				&sriovnetworkv1.SriovNetworkNodeState{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pool-node1",
						Namespace: testNamespace,
					},
					Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
						Conditions: []metav1.Condition{
							{Type: sriovnetworkv1.ConditionReady, Status: metav1.ConditionTrue},
							{Type: sriovnetworkv1.ConditionProgressing, Status: metav1.ConditionFalse},
							{Type: sriovnetworkv1.ConditionDegraded, Status: metav1.ConditionFalse},
						},
					},
				},
				&sriovnetworkv1.SriovNetworkNodeState{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pool-node2",
						Namespace: testNamespace,
					},
					Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
						Conditions: []metav1.Condition{
							{Type: sriovnetworkv1.ConditionReady, Status: metav1.ConditionFalse},
							{Type: sriovnetworkv1.ConditionProgressing, Status: metav1.ConditionTrue},
							{Type: sriovnetworkv1.ConditionDegraded, Status: metav1.ConditionFalse},
						},
					},
				},
				&sriovnetworkv1.SriovNetworkNodeState{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pool-node3",
						Namespace: testNamespace,
					},
					Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
						Conditions: []metav1.Condition{
							{Type: sriovnetworkv1.ConditionReady, Status: metav1.ConditionFalse},
							{Type: sriovnetworkv1.ConditionProgressing, Status: metav1.ConditionFalse},
							{Type: sriovnetworkv1.ConditionDegraded, Status: metav1.ConditionTrue},
						},
					},
				},
			}

			poolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "multi-node-pool",
					Namespace:  testNamespace,
					Generation: 3,
				},
				Spec: sriovnetworkv1.SriovNetworkPoolConfigSpec{
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"test-pool": "true"},
					},
				},
			}

			allObjects := append(nodes, nodeStates...)
			allObjects = append(allObjects, poolConfig)

			reconciler = &SriovNetworkPoolConfigStatusReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(allObjects...).
					WithStatusSubresource(poolConfig).
					Build(),
				Scheme: scheme,
			}

			result, err := reconciler.Reconcile(ctx, newReconcileRequest("multi-node-pool", testNamespace))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			updatedPoolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{}
			Expect(reconciler.Get(ctx, client.ObjectKeyFromObject(poolConfig), updatedPoolConfig)).To(Succeed())

			// 3 nodes match, 1 is ready
			Expect(updatedPoolConfig.Status.MatchedNodeCount).To(Equal(3))
			Expect(updatedPoolConfig.Status.ReadyNodeCount).To(Equal(1))

			// Ready should be False with PartiallyApplied reason
			readyCond := findCondition(updatedPoolConfig.Status.Conditions, sriovnetworkv1.ConditionReady)
			Expect(readyCond).ToNot(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonPartiallyApplied))

			// Progressing should be True (1 node is progressing)
			progressingCond := findCondition(updatedPoolConfig.Status.Conditions, sriovnetworkv1.ConditionProgressing)
			Expect(progressingCond).ToNot(BeNil())
			Expect(progressingCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressingCond.Reason).To(Equal(sriovnetworkv1.ReasonSomeNodesProgressing))

			// Degraded should be True (1 node is degraded)
			degradedCond := findCondition(updatedPoolConfig.Status.Conditions, sriovnetworkv1.ConditionDegraded)
			Expect(degradedCond).ToNot(BeNil())
			Expect(degradedCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(degradedCond.Reason).To(Equal(sriovnetworkv1.ReasonSomeNodesFailed))

			// ObservedGeneration should be set correctly
			Expect(readyCond.ObservedGeneration).To(Equal(int64(3)))
		})

		It("should set NoMatchingNodes when no nodes match the selector", func() {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "unmatched-node",
					Labels: map[string]string{"role": "other"},
				},
			}

			poolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-match-pool",
					Namespace: testNamespace,
				},
				Spec: sriovnetworkv1.SriovNetworkPoolConfigSpec{
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"pool": "nonexistent"},
					},
				},
			}

			reconciler = &SriovNetworkPoolConfigStatusReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(node, poolConfig).
					WithStatusSubresource(poolConfig).
					Build(),
				Scheme: scheme,
			}

			result, err := reconciler.Reconcile(ctx, newReconcileRequest("no-match-pool", testNamespace))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			updatedPoolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{}
			Expect(reconciler.Get(ctx, client.ObjectKeyFromObject(poolConfig), updatedPoolConfig)).To(Succeed())
			Expect(updatedPoolConfig.Status.MatchedNodeCount).To(Equal(0))
			Expect(updatedPoolConfig.Status.ReadyNodeCount).To(Equal(0))

			readyCond := findCondition(updatedPoolConfig.Status.Conditions, sriovnetworkv1.ConditionReady)
			Expect(readyCond).ToNot(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal(sriovnetworkv1.ReasonNoMatchingNodes))
		})
	})
})

// ============================================================================
// Helper Functions
// ============================================================================

func newReconcileRequest(name, namespace string) ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	}
}
