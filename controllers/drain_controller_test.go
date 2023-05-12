package controllers

import (
	goctx "context"
	"time"

	v1 "k8s.io/api/core/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	consts "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util"
)

func createNodeObj(name, anno string) *v1.Node {
	node := &v1.Node{}
	node.Name = name
	node.Annotations = map[string]string{}
	node.Annotations[consts.NodeDrainAnnotation] = anno

	return node
}

func createNode(node *v1.Node) {
	Expect(k8sClient.Create(goctx.TODO(), node)).Should(Succeed())
}

var _ = Describe("Drain Controller", func() {

	BeforeEach(func() {
		node1 := createNodeObj("node1", "Drain_Required")
		node2 := createNodeObj("node2", "Drain_Required")
		createNode(node1)
		createNode(node2)
	})
	AfterEach(func() {
		node1 := createNodeObj("node1", "Drain_Required")
		node2 := createNodeObj("node2", "Drain_Required")
		err := k8sClient.Delete(goctx.TODO(), node1)
		Expect(err).NotTo(HaveOccurred())
		err = k8sClient.Delete(goctx.TODO(), node2)
		Expect(err).NotTo(HaveOccurred())
	})

	Context("Parallel nodes draining", func() {

		It("Should drain one node", func() {
			config := &sriovnetworkv1.SriovOperatorConfig{}
			err := util.WaitForNamespacedObject(config, k8sClient, testNamespace, "default", interval, timeout)
			Expect(err).NotTo(HaveOccurred())
			config.Spec = sriovnetworkv1.SriovOperatorConfigSpec{
				EnableInjector:               func() *bool { b := true; return &b }(),
				EnableOperatorWebhook:        func() *bool { b := true; return &b }(),
				MaxParallelNodeConfiguration: 1,
			}
			updateErr := k8sClient.Update(goctx.TODO(), config)
			Expect(updateErr).NotTo(HaveOccurred())
			time.Sleep(3 * time.Second)

			nodeList := &v1.NodeList{}
			listErr := k8sClient.List(ctx, nodeList)
			Expect(listErr).NotTo(HaveOccurred())

			drainingNodes := 0
			for _, node := range nodeList.Items {
				if utils.NodeHasAnnotation(node, "sriovnetwork.openshift.io/state", "Draining") {
					drainingNodes++
				}
			}
			Expect(drainingNodes).To(Equal(1))
		})

		It("Should drain two nodes", func() {
			config := &sriovnetworkv1.SriovOperatorConfig{}
			err := util.WaitForNamespacedObject(config, k8sClient, testNamespace, "default", interval, timeout)
			Expect(err).NotTo(HaveOccurred())
			config.Spec = sriovnetworkv1.SriovOperatorConfigSpec{
				EnableInjector:               func() *bool { b := true; return &b }(),
				EnableOperatorWebhook:        func() *bool { b := true; return &b }(),
				MaxParallelNodeConfiguration: 2,
			}
			updateErr := k8sClient.Update(goctx.TODO(), config)
			Expect(updateErr).NotTo(HaveOccurred())
			time.Sleep(3 * time.Second)

			nodeList := &v1.NodeList{}
			listErr := k8sClient.List(ctx, nodeList)
			Expect(listErr).NotTo(HaveOccurred())

			for _, node := range nodeList.Items {
				Expect(utils.NodeHasAnnotation(node, "sriovnetwork.openshift.io/state", "Draining")).To(BeTrue())
			}
		})

		It("Should drain all nodes", func() {
			config := &sriovnetworkv1.SriovOperatorConfig{}
			err := util.WaitForNamespacedObject(config, k8sClient, testNamespace, "default", interval, timeout)
			Expect(err).NotTo(HaveOccurred())
			config.Spec = sriovnetworkv1.SriovOperatorConfigSpec{
				EnableInjector:               func() *bool { b := true; return &b }(),
				EnableOperatorWebhook:        func() *bool { b := true; return &b }(),
				MaxParallelNodeConfiguration: 0,
			}
			updateErr := k8sClient.Update(goctx.TODO(), config)
			Expect(updateErr).NotTo(HaveOccurred())
			time.Sleep(3 * time.Second)

			nodeList := &v1.NodeList{}
			listErr := k8sClient.List(ctx, nodeList)
			Expect(listErr).NotTo(HaveOccurred())

			for _, node := range nodeList.Items {
				Expect(utils.NodeHasAnnotation(node, "sriovnetwork.openshift.io/state", "Draining")).To(BeTrue())
			}
		})
	})
})
