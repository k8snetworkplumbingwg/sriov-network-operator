package controllers

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"go.uber.org/mock/gomock"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	orchestratormock "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/orchestrator/mock"
)

var _ = Describe("Migrating a SriovNetworkPoolConfig from spec.ovsHardwareOffloadConfig.name to spec.nodeSelector", func() {
	It("removes only the legacy finalizer on Kubernetes", func(ctx context.Context) {
		const otherFinalizer = "example.com/other-finalizer"
		beforeCR := &sriovnetworkv1.SriovNetworkPoolConfig{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "nic-offload-", Namespace: testNamespace,
				Finalizers: []string{sriovnetworkv1.POOLCONFIGFINALIZERNAME, otherFinalizer},
			},
			Spec: sriovnetworkv1.SriovNetworkPoolConfigSpec{
				OvsHardwareOffloadConfig: &sriovnetworkv1.OvsHardwareOffloadConfig{
					Name: "worker", OvsConfig: map[string]string{"hw-offload": "true"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, beforeCR)).To(Succeed())
		DeferCleanup(func(ctx context.Context) error {
			pool := beforeCR.DeepCopy()
			pool.Finalizers = nil
			if err := k8sClient.Patch(ctx, pool, client.MergeFrom(beforeCR)); err != nil {
				return client.IgnoreNotFound(err)
			}
			return client.IgnoreNotFound(k8sClient.Delete(ctx, pool))
		})

		orch := orchestratormock.NewMockInterface(gomock.NewController(GinkgoT()))
		orch.EXPECT().ClusterType().Return(consts.ClusterTypeKubernetes)
		r := &SriovNetworkPoolConfigReconciler{Client: k8sClient, Orchestrator: orch}
		key := client.ObjectKeyFromObject(beforeCR)
		result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsZero()).To(BeTrue())

		afterCR := &sriovnetworkv1.SriovNetworkPoolConfig{}
		Expect(k8sClient.Get(ctx, key, afterCR)).To(Succeed())
		Expect(afterCR.Spec).To(Equal(beforeCR.Spec))
		Expect(afterCR.Finalizers).To(Equal([]string{otherFinalizer}))
	})

	It("copies the MCP selector on OpenShift and removes the MCP name, finalizer and MachineConfig", func(ctx context.Context) {

		beforeCR := &sriovnetworkv1.SriovNetworkPoolConfig{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "nic-offload-", Namespace: testNamespace,
				Finalizers: []string{sriovnetworkv1.POOLCONFIGFINALIZERNAME},
			},
			Spec: sriovnetworkv1.SriovNetworkPoolConfigSpec{
				OvsHardwareOffloadConfig: &sriovnetworkv1.OvsHardwareOffloadConfig{
					Name: "worker", OvsConfig: map[string]string{"hw-offload": "true", "tc-policy": "none"},
				},
			},
		}

		mcpMaxUnavailable := intstr.FromInt32(2)
		mcp := &mcfgv1.MachineConfigPool{
			ObjectMeta: metav1.ObjectMeta{Name: "worker"},
			Spec: mcfgv1.MachineConfigPoolSpec{
				MaxUnavailable: &mcpMaxUnavailable,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"node-role.kubernetes.io/worker": ""},
				},
			},
		}

		mc := &mcfgv1.MachineConfig{ObjectMeta: metav1.ObjectMeta{
			Name: "00-worker-ovs-hw-offload", Labels: map[string]string{mcfgv1.MachineConfigRoleLabelKey: "worker"},
		}}
		for _, obj := range []client.Object{mcp, mc, beforeCR} {
			Expect(k8sClient.Create(ctx, obj)).To(Succeed())
			DeferCleanup(func(ctx context.Context) error {
				return client.IgnoreNotFound(k8sClient.Delete(ctx, obj))
			})
		}

		Expect(beforeCR.SetRevisionOnObject(mc)).To(Succeed())
		Expect(k8sClient.Update(ctx, mc)).To(Succeed())

		orch := orchestratormock.NewMockInterface(gomock.NewController(GinkgoT()))
		orch.EXPECT().ClusterType().Return(consts.ClusterTypeOpenshift)
		r := &SriovNetworkPoolConfigReconciler{Client: k8sClient, Orchestrator: orch}
		key := client.ObjectKeyFromObject(beforeCR)
		result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsZero()).To(BeTrue())

		afterCR := &sriovnetworkv1.SriovNetworkPoolConfig{}
		Expect(k8sClient.Get(ctx, key, afterCR)).To(Succeed())
		Expect(afterCR.Spec).To(Equal(sriovnetworkv1.SriovNetworkPoolConfigSpec{
			MaxUnavailable: &oneNode,
			NodeSelector:   mcp.Spec.NodeSelector,
			OvsHardwareOffloadConfig: &sriovnetworkv1.OvsHardwareOffloadConfig{
				OvsConfig: beforeCR.Spec.OvsHardwareOffloadConfig.OvsConfig,
			},
		}))
		Expect(afterCR.Finalizers).To(BeEmpty())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(mc), mc))).To(BeTrue())
	})
})
