package controllers

import (
	"context"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	orchestratorMock "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/orchestrator/mock"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util"
)

var _ = Describe("SriovNetworkPoolConfig controller", Ordered, func() {
	var cancel context.CancelFunc
	var ctx context.Context

	BeforeAll(func() {
		By("Create default SriovNetworkPoolConfig k8s objs")
		poolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{}
		poolConfig.SetNamespace(testNamespace)
		poolConfig.SetName(consts.DefaultConfigName)
		poolConfig.Spec = sriovnetworkv1.SriovNetworkPoolConfigSpec{}
		Expect(k8sClient.Create(context.Background(), poolConfig)).Should(Succeed())
		DeferCleanup(func() {
			err := k8sClient.Delete(context.Background(), poolConfig)
			Expect(err).ToNot(HaveOccurred())
		})

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
		orchestrator.EXPECT().Flavor().Return(consts.ClusterFlavorDefault).AnyTimes()

		err = (&SriovNetworkPoolConfigReconciler{
			Client:       k8sManager.GetClient(),
			Scheme:       k8sManager.GetScheme(),
			Orchestrator: orchestrator,
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

	Context("When is up", func() {
		It("deletes a leftover ovs hardware offload MachineConfig and drops the finalizer", func() {
			if vars.ClusterType != consts.ClusterTypeOpenshift {
				Skip("test should only be executed with openshift cluster type")
			}

			mcpName := "worker-hwoffload"
			mcName := "00-" + mcpName + "-" + consts.OVSHWOLMachineConfigNameSuffix
			mc := &mcfgv1.MachineConfig{ObjectMeta: metav1.ObjectMeta{Name: mcName}}
			Expect(k8sClient.Create(ctx, mc)).To(Succeed())

			config := &sriovnetworkv1.SriovNetworkPoolConfig{}
			config.SetNamespace(testNamespace)
			config.SetName("ovs-hw-offload-config")
			config.Finalizers = []string{sriovnetworkv1.POOLCONFIGFINALIZERNAME}
			config.Spec.OvsHardwareOffloadConfig = sriovnetworkv1.OvsHardwareOffloadConfig{Name: mcpName}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())
			DeferCleanup(func() {
				err := k8sClient.Delete(ctx, config)
				Expect(err).ToNot(HaveOccurred())
			})

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mcName}, &mcfgv1.MachineConfig{})
				return errors.IsNotFound(err)
			}, util.APITimeout, util.RetryInterval).Should(BeTrue())

			Eventually(func() []string {
				latest := &sriovnetworkv1.SriovNetworkPoolConfig{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: config.Name, Namespace: testNamespace}, latest)).To(Succeed())
				return latest.Finalizers
			}, util.APITimeout, util.RetryInterval).ShouldNot(ContainElement(sriovnetworkv1.POOLCONFIGFINALIZERNAME))
		})

		It("does not create an ovs hardware offload MachineConfig", func() {
			config := &sriovnetworkv1.SriovNetworkPoolConfig{}
			config.SetNamespace(testNamespace)
			config.SetName("ovs-hw-offload-config-no-mc")
			config.Finalizers = []string{sriovnetworkv1.POOLCONFIGFINALIZERNAME}
			config.Spec.OvsHardwareOffloadConfig = sriovnetworkv1.OvsHardwareOffloadConfig{Name: "worker"}
			Expect(k8sClient.Create(ctx, config)).To(Succeed())
			DeferCleanup(func() {
				err := k8sClient.Delete(ctx, config)
				Expect(err).ToNot(HaveOccurred())
			})

			mcName := "00-worker-" + consts.OVSHWOLMachineConfigNameSuffix
			Consistently(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mcName}, &mcfgv1.MachineConfig{})
				return errors.IsNotFound(err)
			}, "3s", util.RetryInterval).Should(BeTrue())

			Eventually(func() []string {
				latest := &sriovnetworkv1.SriovNetworkPoolConfig{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: config.Name, Namespace: testNamespace}, latest)).To(Succeed())
				return latest.Finalizers
			}, util.APITimeout, util.RetryInterval).ShouldNot(ContainElement(sriovnetworkv1.POOLCONFIGFINALIZERNAME))
		})
	})
})
