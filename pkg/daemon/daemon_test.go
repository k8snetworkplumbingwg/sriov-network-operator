package daemon_test

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/daemon"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
	mock_helper "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper/mock"
	hostTypes "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	snolog "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/log"
	mock_platform "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/platform/mock"
	plugin "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins/generic"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

const (
	waitTime  = 30 * time.Second
	retryTime = 100 * time.Millisecond
)

// daemonTestContext holds all state for a single daemon test group
type daemonTestContext struct {
	wg                  sync.WaitGroup
	k8sManager          manager.Manager
	kubeclient          *kubernetes.Clientset
	eventRecorder       *daemon.EventRecorder
	hostHelper          *mock_helper.MockHostHelpersInterface
	platformMock        *mock_platform.MockInterface
	genericPlugin       plugin.VendorPlugin
	discoverSriovReturn atomic.Pointer[[]sriovnetworkv1.InterfaceExt]
	nodeStateName       client.ObjectKey
	daemonReconciler    *daemon.NodeReconciler
	mockCtrl            *gomock.Controller
}

// setupDaemonTestContext creates and initializes a daemon test context
func setupDaemonTestContext(
	ctx context.Context,
	featureGatesConfig map[string]bool,
) *daemonTestContext {
	dtc := &daemonTestContext{}
	dtc.wg = sync.WaitGroup{}

	Expect(k8sClient.DeleteAllOf(ctx, &sriovnetworkv1.SriovOperatorConfig{},
		client.InNamespace(testNamespace))).ToNot(HaveOccurred())
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

	dtc.kubeclient = kubernetes.NewForConfigOrDie(cfg)
	dtc.eventRecorder = daemon.NewEventRecorder(k8sClient, dtc.kubeclient, scheme.Scheme)

	snolog.SetLogLevel(2)
	// Check if the environment variable CLUSTER_TYPE is set
	if clusterType, ok := os.LookupEnv("CLUSTER_TYPE"); ok &&
		constants.ClusterType(clusterType) == constants.ClusterTypeOpenshift {
		vars.ClusterType = constants.ClusterTypeOpenshift
	} else {
		vars.ClusterType = constants.ClusterTypeKubernetes
	}

	By("Init mock functions")
	dtc.mockCtrl = gomock.NewController(GinkgoT())
	dtc.hostHelper = mock_helper.NewMockHostHelpersInterface(dtc.mockCtrl)
	dtc.platformMock = mock_platform.NewMockInterface(dtc.mockCtrl)

	// daemon initialization default mocks
	dtc.hostHelper.EXPECT().CheckRDMAEnabled().Return(true, nil)
	dtc.hostHelper.EXPECT().CleanSriovFilesFromHost(
		vars.ClusterType == constants.ClusterTypeOpenshift).Return(nil)
	dtc.hostHelper.EXPECT().TryEnableTun()
	dtc.hostHelper.EXPECT().TryEnableVhostNet()
	dtc.hostHelper.EXPECT().PrepareNMUdevRule().Return(nil)
	dtc.hostHelper.EXPECT().PrepareVFRepUdevRule().Return(nil)
	dtc.hostHelper.EXPECT().WriteCheckpointFile(gomock.Any()).Return(nil)

	// general
	dtc.hostHelper.EXPECT().Chroot(gomock.Any()).Return(func() error { return nil }, nil).AnyTimes()
	dtc.hostHelper.EXPECT().RunCommand("/bin/sh", gomock.Any(), gomock.Any(), gomock.Any()).
		Return("", "", nil).AnyTimes()

	dtc.discoverSriovReturn.Store(&[]sriovnetworkv1.InterfaceExt{})

	dtc.hostHelper.EXPECT().LoadPfsStatus("0000:16:00.0").
		Return(&sriovnetworkv1.Interface{ExternallyManaged: false}, true, nil).AnyTimes()
	dtc.hostHelper.EXPECT().ClearPCIAddressFolder().Return(nil).AnyTimes()
	dtc.hostHelper.EXPECT().DiscoverRDMASubsystem().Return("shared", nil).AnyTimes()
	dtc.hostHelper.EXPECT().GetCurrentKernelArgs().Return("", nil).AnyTimes()
	dtc.hostHelper.EXPECT().IsKernelArgsSet("", constants.KernelArgPciRealloc).Return(true).AnyTimes()
	dtc.hostHelper.EXPECT().IsKernelArgsSet("", constants.KernelArgIntelIommu).Return(true).AnyTimes()
	dtc.hostHelper.EXPECT().IsKernelArgsSet("", constants.KernelArgIommuPt).Return(true).AnyTimes()
	dtc.hostHelper.EXPECT().IsKernelArgsSet("", constants.KernelArgRdmaExclusive).Return(false).AnyTimes()
	dtc.hostHelper.EXPECT().IsKernelArgsSet("", constants.KernelArgRdmaShared).Return(false).AnyTimes()
	dtc.hostHelper.EXPECT().SetRDMASubsystem("").Return(nil).AnyTimes()

	dtc.hostHelper.EXPECT().ConfigSriovInterfaces(gomock.Any(), gomock.Any(), gomock.Any(), false).
		Return(nil).AnyTimes()

	// k8s plugin for k8s cluster type
	if vars.ClusterType == constants.ClusterTypeKubernetes {
		dtc.hostHelper.EXPECT().ReadServiceManifestFile(gomock.Any()).
			Return(&hostTypes.Service{Name: "test"}, nil).AnyTimes()
		dtc.hostHelper.EXPECT().ReadServiceInjectionManifestFile(gomock.Any()).
			Return(&hostTypes.Service{Name: "test"}, nil).AnyTimes()
	}

	dtc.platformMock.EXPECT().Init().Return(nil)
	// TODO: remove this when adding unit tests for switchdev
	dtc.platformMock.EXPECT().DiscoverBridges().Return(sriovnetworkv1.Bridges{}, nil).AnyTimes()
	dtc.platformMock.EXPECT().DiscoverSriovDevices().DoAndReturn(func() ([]sriovnetworkv1.InterfaceExt, error) {
		return *dtc.discoverSriovReturn.Load(), nil
	}).AnyTimes()

	dtc.genericPlugin, err = generic.NewGenericPlugin(dtc.hostHelper)
	Expect(err).ToNot(HaveOccurred())
	dtc.platformMock.EXPECT().GetVendorPlugins(gomock.Any()).
		Return(dtc.genericPlugin, []plugin.VendorPlugin{}, nil)

	featureGates := featuregate.New()
	featureGates.Init(featureGatesConfig)
	dtc.daemonReconciler = createDaemon(dtc, featureGates, []string{})

	_, nodeState := createNode("node1")
	dtc.nodeStateName = client.ObjectKeyFromObject(nodeState)

	return dtc
}

// startDaemon starts the controller manager in a goroutine
func startDaemon(ctx context.Context, dtc *daemonTestContext) {
	By("start controller manager")
	dtc.wg.Add(1)
	go func() {
		defer dtc.wg.Done()
		defer GinkgoRecover()
		By("Start controller manager")
		err := dtc.k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred())
	}()
}

// cleanupDaemonTest cleans up test resources
func cleanupDaemonTest(dtc *daemonTestContext) {
	dtc.eventRecorder.Shutdown()
	Expect(k8sClient.DeleteAllOf(context.Background(), &corev1.Node{})).ToNot(HaveOccurred())
	Expect(k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovNetworkNodeState{},
		client.InNamespace(testNamespace))).ToNot(HaveOccurred())
	Expect(k8sClient.DeleteAllOf(context.Background(), &sriovnetworkv1.SriovOperatorConfig{},
		client.InNamespace(testNamespace))).ToNot(HaveOccurred())
	Expect(k8sClient.DeleteAllOf(context.Background(), &corev1.Pod{},
		client.InNamespace(testNamespace), client.GracePeriodSeconds(0))).ToNot(HaveOccurred())
}

// sharedDaemonTests defines tests that run for all feature gate configurations
func sharedDaemonTests(getDaemonTestContext func() *daemonTestContext) {
	var dtc *daemonTestContext

	BeforeEach(func() {
		dtc = getDaemonTestContext()
	})

	Context("Config Daemon generic flow", func() {
		It("Should expose nodeState Status section", func(ctx context.Context) {
			nodeState := &sriovnetworkv1.SriovNetworkNodeState{}
			err := k8sClient.Get(ctx, dtc.nodeStateName, nodeState)
			Expect(err).ToNot(HaveOccurred())
			dtc.discoverSriovReturn.Store(&[]sriovnetworkv1.InterfaceExt{
				{
					Name:           "eno1",
					Driver:         "ice",
					PciAddress:     "0000:16:00.0",
					DeviceID:       "1593",
					Vendor:         "8086",
					EswitchMode:    "legacy",
					LinkAdminState: "up",
					LinkSpeed:      "10000 Mb/s",
					LinkType:       "ETH",
					Mac:            "aa:bb:cc:dd:ee:ff",
					Mtu:            1500,
					TotalVfs:       2,
					NumVfs:         0,
				},
			})

			By("waiting for state to be succeeded")
			eventuallySyncStatusEqual(nodeState, constants.SyncStatusSucceeded)

			By("add spec to node state")

			nodeState.Spec.Interfaces = []sriovnetworkv1.Interface{
				{Name: "eno1",
					PciAddress: "0000:16:00.0",
					LinkType:   "eth",
					NumVfs:     2,
					VfGroups: []sriovnetworkv1.VfGroup{
						{ResourceName: "test",
							DeviceType: "netdevice",
							PolicyName: "test-policy",
							VfRange:    "eno1#0-1"},
					}},
			}

			dtc.discoverSriovReturn.Store(&[]sriovnetworkv1.InterfaceExt{
				{
					Name:           "eno1",
					Driver:         "ice",
					PciAddress:     "0000:16:00.0",
					DeviceID:       "1593",
					Vendor:         "8086",
					EswitchMode:    "legacy",
					LinkAdminState: "up",
					LinkSpeed:      "10000 Mb/s",
					LinkType:       "ETH",
					Mac:            "aa:bb:cc:dd:ee:ff",
					Mtu:            1500,
					TotalVfs:       2,
					NumVfs:         2,
					VFs: []sriovnetworkv1.VirtualFunction{
						{
							Name:       "eno1f0",
							PciAddress: "0000:16:00.1",
							VfID:       0,
						},
						{
							Name:       "eno1f1",
							PciAddress: "0000:16:00.2",
							VfID:       1,
						}},
				},
			})

			err = k8sClient.Update(ctx, nodeState)
			Expect(err).ToNot(HaveOccurred())

			By("waiting to require drain")
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(k8sClient.Get(context.Background(), dtc.nodeStateName, nodeState)).ToNot(HaveOccurred())
				g.Expect(dtc.daemonReconciler.GetLastAppliedGeneration()).To(Equal(int64(2)))
			}, waitTime, retryTime).Should(Succeed())

			err = k8sClient.Get(ctx, dtc.nodeStateName, nodeState)
			Expect(err).ToNot(HaveOccurred())
			nodeState.Spec.Interfaces = []sriovnetworkv1.Interface{}
			err = k8sClient.Update(ctx, nodeState)
			Expect(err).ToNot(HaveOccurred())

			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(k8sClient.Get(context.Background(), dtc.nodeStateName, nodeState)).ToNot(HaveOccurred())
				g.Expect(nodeState.Annotations[constants.NodeStateDrainAnnotation]).To(Equal(constants.DrainRequired))
			}, waitTime, retryTime).Should(Succeed())

			patchAnnotation(nodeState, constants.NodeStateDrainAnnotationCurrent, constants.DrainComplete)
			// Validate status
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(k8sClient.Get(context.Background(), dtc.nodeStateName, nodeState)).ToNot(HaveOccurred())
				g.Expect(nodeState.Annotations[constants.NodeStateDrainAnnotation]).To(Equal(constants.DrainIdle))
			}, waitTime, retryTime).Should(Succeed())
			patchAnnotation(nodeState, constants.NodeStateDrainAnnotationCurrent, constants.DrainIdle)

			// Validate status
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(k8sClient.Get(context.Background(), dtc.nodeStateName, nodeState)).ToNot(HaveOccurred())
				g.Expect(nodeState.Status.SyncStatus).To(Equal(constants.SyncStatusSucceeded))
			}, waitTime, retryTime).Should(Succeed())

			Expect(nodeState.Status.LastSyncError).To(Equal(""))
		})
		It("Should apply the reset configuration when disableDrain is true", func(ctx context.Context) {
			nodeState := &sriovnetworkv1.SriovNetworkNodeState{}
			err := k8sClient.Get(ctx, dtc.nodeStateName, nodeState)
			Expect(err).ToNot(HaveOccurred())

			DeferCleanup(func(x bool) { vars.DisableDrain = x }, vars.DisableDrain)
			vars.DisableDrain = true

			dtc.discoverSriovReturn.Store(&[]sriovnetworkv1.InterfaceExt{
				{
					Name:           "eno1",
					Driver:         "ice",
					PciAddress:     "0000:16:00.0",
					DeviceID:       "1593",
					Vendor:         "8086",
					EswitchMode:    "legacy",
					LinkAdminState: "up",
					LinkSpeed:      "10000 Mb/s",
					LinkType:       "ETH",
					Mac:            "aa:bb:cc:dd:ee:ff",
					Mtu:            1500,
					TotalVfs:       2,
					NumVfs:         2,
					VFs: []sriovnetworkv1.VirtualFunction{
						{
							Name:       "eno1f0",
							PciAddress: "0000:16:00.1",
							VfID:       0,
						},
						{
							Name:       "eno1f1",
							PciAddress: "0000:16:00.2",
							VfID:       1,
						}},
				},
			})

			nodeState.Spec.Interfaces = []sriovnetworkv1.Interface{
				{Name: "eno1",
					PciAddress: "0000:16:00.0",
					LinkType:   "eth",
					NumVfs:     2,
					VfGroups: []sriovnetworkv1.VfGroup{
						{ResourceName: "test",
							DeviceType: "netdevice",
							PolicyName: "test-policy",
							VfRange:    "eno1#0-1"},
					}},
			}
			err = k8sClient.Update(ctx, nodeState)
			Expect(err).ToNot(HaveOccurred())

			eventuallySyncStatusEqual(nodeState, constants.SyncStatusSucceeded)

			By("Simulate node policy removal")
			nodeState.Spec.Interfaces = []sriovnetworkv1.Interface{}
			err = k8sClient.Update(ctx, nodeState)
			Expect(err).ToNot(HaveOccurred())

			eventuallySyncStatusEqual(nodeState, constants.SyncStatusSucceeded)
			assertLastStatusTransitionsContains(nodeState, 2, constants.SyncStatusInProgress)
		})
	})
}

var _ = Describe("Daemon Controller tests", Ordered, func() {
	Context("With default configuration", Ordered, func() {
		var (
			dtc    *daemonTestContext
			ctx    context.Context
			cancel context.CancelFunc
		)

		BeforeAll(func() {
			ctx, cancel = context.WithCancel(context.Background())

			dtc = setupDaemonTestContext(ctx, map[string]bool{})
			startDaemon(ctx, dtc)
		})
		AfterAll(func() {
			cancel()
			dtc.wg.Wait()
			cleanupDaemonTest(dtc)
		})
		sharedDaemonTests(func() *daemonTestContext { return dtc })
	})
	Context("With blockDevicePluginUntilConfigured feature gate enabled", Ordered, func() {
		var (
			dtc                      *daemonTestContext
			ctx                      context.Context
			cancel                   context.CancelFunc
			devicePluginPodRecreator *podRecreator
			devicePluginPodName      client.ObjectKey
		)

		BeforeAll(func() {
			ctx, cancel = context.WithCancel(context.Background())

			dtc = setupDaemonTestContext(ctx, map[string]bool{
				constants.BlockDevicePluginUntilConfiguredFeatureGate: true,
			})
			devicePluginPodName = client.ObjectKey{
				Name:      "sriov-device-plugin-test",
				Namespace: testNamespace,
			}
			devicePluginPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      devicePluginPodName.Name,
					Namespace: devicePluginPodName.Namespace,
					Labels: map[string]string{
						"app": "sriov-device-plugin",
					},
					Annotations: map[string]string{
						constants.DevicePluginWaitConfigAnnotation: "true",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node1",
					Containers: []corev1.Container{
						{
							Name:  "device-plugin",
							Image: "test-image",
						},
					},
				},
			}
			devicePluginPodRecreator = newPodRecreator(k8sClient, devicePluginPod, retryTime)
			devicePluginPodRecreator.Start(ctx)

			startDaemon(ctx, dtc)
		})
		AfterAll(func() {
			cancel()
			devicePluginPodRecreator.Stop()
			dtc.wg.Wait()
			cleanupDaemonTest(dtc)
		})
		sharedDaemonTests(func() *daemonTestContext { return dtc })

		It("Should unblock the device plugin pod when configuration is finished", func(ctx context.Context) {
			nodeState := &sriovnetworkv1.SriovNetworkNodeState{}
			err := k8sClient.Get(ctx, dtc.nodeStateName, nodeState)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func(x bool) { vars.DisableDrain = x }, vars.DisableDrain)
			vars.DisableDrain = true

			dtc.discoverSriovReturn.Store(&[]sriovnetworkv1.InterfaceExt{
				{
					Name:           "eno1",
					Driver:         "ice",
					PciAddress:     "0000:16:00.0",
					DeviceID:       "1593",
					Vendor:         "8086",
					EswitchMode:    "legacy",
					LinkAdminState: "up",
					LinkSpeed:      "10000 Mb/s",
					LinkType:       "ETH",
					Mac:            "aa:bb:cc:dd:ee:ff",
					Mtu:            1500,
					TotalVfs:       2,
					NumVfs:         2,
					VFs: []sriovnetworkv1.VirtualFunction{
						{
							Name:       "eno1f0",
							PciAddress: "0000:16:00.1",
							VfID:       0,
						},
						{
							Name:       "eno1f1",
							PciAddress: "0000:16:00.2",
							VfID:       1,
						}},
				},
			})
			nodeState.Spec.Interfaces = []sriovnetworkv1.Interface{
				{Name: "eno1",
					PciAddress: "0000:16:00.0",
					LinkType:   "eth",
					NumVfs:     2,
					VfGroups: []sriovnetworkv1.VfGroup{
						{ResourceName: "test",
							DeviceType: "netdevice",
							PolicyName: "test-policy",
							VfRange:    "eno1#0-1"},
					}},
			}
			Expect(k8sClient.Update(ctx, nodeState)).ToNot(HaveOccurred())

			eventuallySyncStatusEqual(nodeState, constants.SyncStatusSucceeded)

			// Verify that the device plugin pod is present and the 'wait-for-config' annotation has been removed upon completion of configuration
			Eventually(func(g Gomega) {
				devicePluginPod := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, devicePluginPodName, devicePluginPod)).ToNot(HaveOccurred())
				g.Expect(devicePluginPod.Annotations).ToNot(HaveKey(constants.DevicePluginWaitConfigAnnotation))
			}, waitTime, retryTime).Should(Succeed())
		})
	})
})

func patchAnnotation(nodeState *sriovnetworkv1.SriovNetworkNodeState, key, value string) {
	originalNodeState := nodeState.DeepCopy()
	nodeState.Annotations[key] = value
	err := k8sClient.Patch(context.Background(), nodeState, client.MergeFrom(originalNodeState))
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
				constants.NodeStateDrainAnnotation:        constants.DrainIdle,
				constants.NodeStateDrainAnnotationCurrent: constants.DrainIdle,
			},
		},
	}

	Expect(k8sClient.Create(context.Background(), &node)).ToNot(HaveOccurred())
	Expect(k8sClient.Create(context.Background(), &nodeState)).ToNot(HaveOccurred())
	vars.NodeName = nodeName

	return &node, &nodeState
}

func createDaemon(
	dtc *daemonTestContext,
	featureGates featuregate.FeatureGate,
	disablePlugins []string) *daemon.NodeReconciler {
	kClient, err := client.New(
		cfg,
		client.Options{
			Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())

	By("Setup controller manager")
	dtc.k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		// Use SkipNameValidation to allow multiple controllers with the same name in tests
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	Expect(err).ToNot(HaveOccurred())

	configController := daemon.New(kClient, dtc.hostHelper, dtc.platformMock, dtc.eventRecorder,
		featureGates)
	err = configController.Init(disablePlugins)
	Expect(err).ToNot(HaveOccurred())
	err = configController.SetupWithManager(dtc.k8sManager)
	Expect(err).ToNot(HaveOccurred())

	return configController
}

func eventuallySyncStatusEqual(nodeState *sriovnetworkv1.SriovNetworkNodeState, expectedState string) {
	EventuallyWithOffset(1, func(g Gomega) {
		g.Expect(k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: nodeState.Namespace, Name: nodeState.Name}, nodeState)).
			ToNot(HaveOccurred())
		g.Expect(nodeState.Status.SyncStatus).To(Equal(expectedState))
	}, waitTime, retryTime).Should(Succeed())
}

func assertLastStatusTransitionsContains(
	nodeState *sriovnetworkv1.SriovNetworkNodeState, numberOfTransitions int, status string) {
	events := &corev1.EventList{}
	err := k8sClient.List(
		context.Background(),
		events,
		client.MatchingFields{
			"involvedObject.name": nodeState.Name,
			"reason":              "SyncStatusChanged",
		},
		client.Limit(numberOfTransitions),
	)
	Expect(err).ToNot(HaveOccurred())

	// Status transition events are in the form
	// `Status changed from: Succeed to: InProgress`
	Expect(events.Items).To(ContainElement(
		HaveField("Message", ContainSubstring("to: "+status))))
}

// podRecreator manages a pod lifecycle in the background, ensuring the pod
// is recreated if it gets deleted until explicitly stopped.
type podRecreator struct {
	client       client.Client
	podSpec      *corev1.Pod
	pollInterval time.Duration

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newPodRecreator creates a new podRecreator instance.
// podSpec must have Name and Namespace set.
func newPodRecreator(c client.Client, podSpec *corev1.Pod, pollInterval time.Duration) *podRecreator {
	return &podRecreator{
		client:       c,
		podSpec:      podSpec,
		pollInterval: pollInterval,
	}
}

// Start begins the background loop that ensures the pod exists.
// It creates the pod if it doesn't exist and periodically checks
// if the pod was removed, recreating it if necessary.
func (pr *podRecreator) Start(ctx context.Context) {
	ctx, pr.cancel = context.WithCancel(ctx)
	pr.ensurePodExists(ctx)
	pr.wg.Add(1)
	go func() {
		defer pr.wg.Done()
		pr.ensurePodExists(ctx)
		ticker := time.NewTicker(pr.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pr.ensurePodExists(ctx)
			}
		}
	}()
}

// Stop stops the background loop and waits for it to finish.
func (pr *podRecreator) Stop() {
	if pr.cancel != nil {
		pr.cancel()
	}
	pr.wg.Wait()
}

// ensurePodExists checks if the pod exists and creates it if not found.
func (pr *podRecreator) ensurePodExists(ctx context.Context) {
	pod := &corev1.Pod{}
	err := pr.client.Get(ctx, types.NamespacedName{
		Name:      pr.podSpec.Name,
		Namespace: pr.podSpec.Namespace,
	}, pod)
	if apiErrors.IsNotFound(err) {
		// Pod not found, create it
		newPod := pr.podSpec.DeepCopy()
		_ = pr.client.Create(ctx, newPod)
		return
	}
	if err == nil && pod.DeletionTimestamp != nil {
		// the pod has nodeName set, we need to force delete it to simulate kubelet behavior
		_ = pr.client.Delete(ctx, pod, client.GracePeriodSeconds(0))
	}
}
