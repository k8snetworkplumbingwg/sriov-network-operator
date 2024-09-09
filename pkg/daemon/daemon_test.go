package daemon_test

import (
	"context"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/golang/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	snclientset "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/client/clientset/versioned"
	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/daemon"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper"
	mock_helper "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper/mock"
	snolog "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/log"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/platforms"
	mock_platforms "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/platforms/mock"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

var (
	cancel        context.CancelFunc
	ctx           context.Context
	k8sManager    manager.Manager
	snclient      *snclientset.Clientset
	kubeclient    *kubernetes.Clientset
	eventRecorder *daemon.EventRecorder
	wg            sync.WaitGroup
	startDaemon   func(dc *daemon.DaemonReconcile)
)

const (
	waitTime  = 10 * time.Minute
	retryTime = 5 * time.Second
)

var _ = Describe("Daemon Controller", Ordered, func() {
	BeforeAll(func() {
		ctx, cancel = context.WithCancel(context.Background())
		wg = sync.WaitGroup{}
		startDaemon = func(dc *daemon.DaemonReconcile) {
			By("start controller manager")
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				By("Start controller manager")
				err := k8sManager.Start(ctx)
				Expect(err).ToNot(HaveOccurred())
			}()
		}

		Expect(k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovOperatorConfig{}, client.InNamespace(testNamespace))).ToNot(HaveOccurred())
		soc := &sriovnetworkv1.SriovOperatorConfig{ObjectMeta: metav1.ObjectMeta{
			Name:      constants.DefaultConfigName,
			Namespace: testNamespace,
		},
			Spec: sriovnetworkv1.SriovOperatorConfigSpec{
				LogLevel: 2,
			},
		}
		err := k8sClient.Create(ctx, soc)
		Expect(err).ToNot(HaveOccurred())

		snclient = snclientset.NewForConfigOrDie(cfg)
		kubeclient = kubernetes.NewForConfigOrDie(cfg)
		eventRecorder = daemon.NewEventRecorder(snclient, kubeclient, scheme.Scheme)
		DeferCleanup(func() {
			eventRecorder.Shutdown()
		})

		snolog.SetLogLevel(2)
		vars.ClusterType = constants.ClusterTypeOpenshift
	})

	BeforeEach(func() {
		Expect(k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovNetworkNodeState{}, client.InNamespace(testNamespace))).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		By("Shutdown controller manager")
		cancel()
		wg.Wait()
	})

	Context("Config Daemon", func() {
		It("Should expose nodeState Status section", func() {
			By("Init mock functions")
			t := GinkgoT()
			mockCtrl := gomock.NewController(t)
			hostHelper := mock_helper.NewMockHostHelpersInterface(mockCtrl)
			platformHelper := mock_platforms.NewMockInterface(mockCtrl)
			hostHelper.EXPECT().DiscoverSriovDevices(hostHelper).Return([]sriovnetworkv1.InterfaceExt{
				{
					Name:           "eno1",
					Driver:         "mlx5_core",
					PciAddress:     "0000:16:00.0",
					DeviceID:       "1015",
					Vendor:         "15b3",
					EswitchMode:    "legacy",
					LinkAdminState: "up",
					LinkSpeed:      "10000 Mb/s",
					LinkType:       "ETH",
					Mac:            "aa:bb:cc:dd:ee:ff",
					Mtu:            1500,
				},
			}, nil).AnyTimes()
			hostHelper.EXPECT().IsKernelLockdownMode().Return(false).AnyTimes()
			hostHelper.EXPECT().LoadPfsStatus("0000:16:00.0").Return(nil, false, nil).AnyTimes()
			hostHelper.EXPECT().MlxConfigFW(gomock.Any()).Return(nil)
			hostHelper.EXPECT().Chroot(gomock.Any()).Return(func() error { return nil }, nil)
			hostHelper.EXPECT().ConfigSriovInterfaces(gomock.Any(), gomock.Any(), gomock.Any(), false).Return(nil)
			hostHelper.EXPECT().ClearPCIAddressFolder().Return(nil).AnyTimes()

			featureGates := featuregate.New()
			featureGates.Init(map[string]bool{})
			dc := CreateDaemon(hostHelper, platformHelper, featureGates, []string{})
			startDaemon(dc)

			_, nodeState := createNode("node1")
			// state moves to in progress
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(k8sClient.Get(context.Background(), types.NamespacedName{Namespace: nodeState.Namespace, Name: nodeState.Name}, nodeState)).
					ToNot(HaveOccurred())

				g.Expect(nodeState.Status.SyncStatus).To(Equal(constants.SyncStatusInProgress))
			}, waitTime, retryTime).Should(Succeed())

			// daemon request to reset device plugin
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(k8sClient.Get(context.Background(), types.NamespacedName{Namespace: nodeState.Namespace, Name: nodeState.Name}, nodeState)).
					ToNot(HaveOccurred())

				g.Expect(nodeState.Annotations[constants.NodeStateDrainAnnotation]).To(Equal(constants.DevicePluginResetRequired))
			}, waitTime, retryTime).Should(Succeed())

			// finis drain
			patchAnnotation(nodeState, constants.NodeStateDrainAnnotationCurrent, constants.DrainComplete)
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(k8sClient.Get(context.Background(), types.NamespacedName{Namespace: nodeState.Namespace, Name: nodeState.Name}, nodeState)).
					ToNot(HaveOccurred())

				g.Expect(nodeState.Annotations[constants.NodeStateDrainAnnotation]).To(Equal(constants.DrainIdle))
			}, waitTime, retryTime).Should(Succeed())

			// mode current status to idle also (from the operator)
			patchAnnotation(nodeState, constants.NodeStateDrainAnnotationCurrent, constants.DrainIdle)

			// Validate status
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(k8sClient.Get(context.Background(), types.NamespacedName{Namespace: nodeState.Namespace, Name: nodeState.Name}, nodeState)).
					ToNot(HaveOccurred())

				g.Expect(nodeState.Status.SyncStatus).To(Equal(constants.SyncStatusSucceeded))
			}, waitTime, retryTime).Should(Succeed())
			Expect(nodeState.Status.LastSyncError).To(Equal(""))
			Expect(len(nodeState.Status.Interfaces)).To(Equal(1))
		})
	})
})

func patchAnnotation(nodeState *sriovnetworkv1.SriovNetworkNodeState, key, value string) {
	originalNodeState := nodeState.DeepCopy()
	nodeState.Annotations[key] = value
	err := k8sClient.Patch(ctx, nodeState, client.MergeFrom(originalNodeState))
	Expect(err).ToNot(HaveOccurred())
}

func createNode(nodeName string) (*corev1.Node, *sriovnetworkv1.SriovNetworkNodeState) {
	node := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				constants.NodeDrainAnnotation:                     constants.DrainIdle,
				"machineconfiguration.openshift.io/desiredConfig": "worker-1",
			},
			Labels: map[string]string{
				"test": "",
			},
		},
	}

	nodeState := sriovnetworkv1.SriovNetworkNodeState{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeName,
			Namespace: testNamespace,
			Annotations: map[string]string{
				constants.NodeDrainAnnotation:             constants.DrainIdle,
				constants.NodeStateDrainAnnotationCurrent: constants.DrainIdle,
			},
		},
	}

	Expect(k8sClient.Create(ctx, &node)).ToNot(HaveOccurred())
	Expect(k8sClient.Create(ctx, &nodeState)).ToNot(HaveOccurred())

	return &node, &nodeState
}

func CreateDaemon(
	hostHelper helper.HostHelpersInterface,
	platformHelper platforms.Interface,
	featureGates featuregate.FeatureGate,
	disablePlugins []string) *daemon.DaemonReconcile {
	kClient, err := client.New(
		cfg,
		client.Options{
			Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())

	By("Setup controller manager")
	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	configController := daemon.New(kClient, snclient, kubeclient, hostHelper, platformHelper, eventRecorder, featureGates, disablePlugins)
	err = configController.SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	return configController
}
