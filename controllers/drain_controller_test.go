package controllers

import (
	"time"

	goctx "context"

	v1 "k8s.io/api/core/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
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
			nodeList := &v1.NodeList{}
			listErr := k8sClient.List(ctx, nodeList)
			Expect(listErr).NotTo(HaveOccurred())
			time.Sleep(5 * time.Second)

			drainingNodes := 0
			for _, node := range nodeList.Items {
				if utils.NodeHasAnnotation(node, "sriovnetwork.openshift.io/state", "Draining") {
					drainingNodes++
				}
			}
			Expect(drainingNodes).To(Equal(1))
		})
	})
})
