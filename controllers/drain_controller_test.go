package controllers

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"sigs.k8s.io/controller-runtime/pkg/client"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

var _ = Describe("Drain Controller", func() {
	BeforeEach(func() {
		Expect(k8sClient.DeleteAllOf(context.Background(), &corev1.Node{})).ToNot(HaveOccurred())
		Expect(k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovNetworkNodeState{}, client.InNamespace(namespace))).ToNot(HaveOccurred())
	})

	Context("when there is only one node", func() {

		It("should drain", func(ctx context.Context) {
			node, nodeState := createNode("node1")

			simulateDaemonSetAnnotation(node, constants.DrainRequired)

			expectNodeStateLabel(nodeState, constants.DrainComplete)
			expectNodeIsNotSchedulable(node)

			simulateDaemonSetAnnotation(node, constants.DrainIdle)

			expectNodeStateLabel(nodeState, constants.DrainIdle)
			expectNodeIsSchedulable(node)
		})
	})

	Context("when there are multiple nodes", func() {

		It("should drain nodes serially", func(ctx context.Context) {

			node1, nodeState1 := createNode("node1")
			node2, nodeState2 := createNode("node2")
			node3, nodeState3 := createNode("node3")

			// Two nodes require to drain at the same time
			simulateDaemonSetAnnotation(node1, constants.DrainRequired)
			simulateDaemonSetAnnotation(node2, constants.DrainRequired)

			// Only the first node drains
			expectNodeStateLabel(nodeState1, constants.DrainComplete)
			expectNodeStateLabel(nodeState2, constants.DrainIdle)
			expectNodeStateLabel(nodeState3, constants.DrainIdle)
			expectNodeIsNotSchedulable(node1)
			expectNodeIsSchedulable(node2)
			expectNodeIsSchedulable(node3)

			simulateDaemonSetAnnotation(node1, constants.DrainIdle)

			expectNodeStateLabel(nodeState1, constants.DrainIdle)
			expectNodeIsSchedulable(node1)

			// Second node starts draining
			expectNodeStateLabel(nodeState1, constants.DrainIdle)
			expectNodeStateLabel(nodeState2, constants.DrainComplete)
			expectNodeStateLabel(nodeState3, constants.DrainIdle)
			expectNodeIsSchedulable(node1)
			expectNodeIsNotSchedulable(node2)
			expectNodeIsSchedulable(node3)

			simulateDaemonSetAnnotation(node2, constants.DrainIdle)

			expectNodeStateLabel(nodeState1, constants.DrainIdle)
			expectNodeStateLabel(nodeState2, constants.DrainIdle)
			expectNodeStateLabel(nodeState3, constants.DrainIdle)
			expectNodeIsSchedulable(node1)
			expectNodeIsSchedulable(node2)
			expectNodeIsSchedulable(node3)
		})
	})
})

func expectNodeStateLabel(nodeState *sriovnetworkv1.SriovNetworkNodeState, expectedAnnotationValue string) {
	EventuallyWithOffset(1, func(g Gomega) {
		g.Expect(k8sClient.Get(context.Background(), types.NamespacedName{Namespace: nodeState.Namespace, Name: nodeState.Name}, nodeState)).
			ToNot(HaveOccurred())

		g.Expect(utils.ObjectHasLabel(nodeState, constants.NodeStateDrainLabelCurrent, expectedAnnotationValue)).
			To(BeTrue(),
				"Node[%s] annotation[%s] == '%s'. Expected '%s'", nodeState.Name, constants.NodeDrainAnnotation, nodeState.GetAnnotations()[constants.NodeStateDrainLabelCurrent], expectedAnnotationValue)

	}, "10s", "1s").Should(Succeed())
}

func expectNodeIsNotSchedulable(node *corev1.Node) {
	EventuallyWithOffset(1, func(g Gomega) {
		g.Expect(k8sClient.Get(context.Background(), types.NamespacedName{Name: node.Name}, node)).
			ToNot(HaveOccurred())

		g.Expect(node.Spec.Unschedulable).To(BeTrue())
	}, "10s", "1s").Should(Succeed())
}

func expectNodeIsSchedulable(node *corev1.Node) {
	EventuallyWithOffset(1, func(g Gomega) {
		g.Expect(k8sClient.Get(context.Background(), types.NamespacedName{Name: node.Name}, node)).
			ToNot(HaveOccurred())

		g.Expect(node.Spec.Unschedulable).To(BeFalse())
	}, "10s", "1s").Should(Succeed())
}

func simulateDaemonSetAnnotation(node *corev1.Node, drainAnnotationValue string) {
	ExpectWithOffset(1,
		utils.AnnotateObject(node, constants.NodeDrainAnnotation, drainAnnotationValue, k8sClient)).
		ToNot(HaveOccurred())
}

func createNode(nodeName string) (*corev1.Node, *sriovnetworkv1.SriovNetworkNodeState) {
	node := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				constants.NodeDrainAnnotation: constants.DrainIdle,
			},
		},
	}

	nodeState := sriovnetworkv1.SriovNetworkNodeState{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeName,
			Namespace: namespace,
			Labels: map[string]string{
				constants.NodeStateDrainLabelCurrent: constants.DrainIdle,
			},
		},
	}

	Expect(k8sClient.Create(ctx, &node)).ToNot(HaveOccurred())
	Expect(k8sClient.Create(ctx, &nodeState)).ToNot(HaveOccurred())

	return &node, &nodeState
}
