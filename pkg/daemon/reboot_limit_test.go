package daemon

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
	mock_helper "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper/mock"
	hostTypes "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	mock_platform "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/platform/mock"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

var _ = Describe("Reboot Limit", func() {
	var (
		mockCtrl     *gomock.Controller
		hostHelper   *mock_helper.MockHostHelpersInterface
		platMock     *mock_platform.MockInterface
		reconciler   *NodeReconciler
		fakeClient   client.Client
		nodeState    *sriovnetworkv1.SriovNetworkNodeState
		node         *corev1.Node
		ctx          context.Context
		featureGates featuregate.FeatureGate
	)

	BeforeEach(func() {
		ctx = context.Background()
		mockCtrl = gomock.NewController(GinkgoT())
		hostHelper = mock_helper.NewMockHostHelpersInterface(mockCtrl)
		platMock = mock_platform.NewMockInterface(mockCtrl)

		featureGates = featuregate.New()
		featureGates.Init(map[string]bool{})
		DeferCleanup(func(old featuregate.FeatureGate) { vars.FeatureGate = old }, vars.FeatureGate)
		vars.FeatureGate = featureGates
		DeferCleanup(func(old string) { vars.NodeName = old }, vars.NodeName)
		vars.NodeName = "test-node"
		DeferCleanup(func(old string) { vars.Namespace = old }, vars.Namespace)
		vars.Namespace = "test-ns"

		nodeState = &sriovnetworkv1.SriovNetworkNodeState{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-node",
				Namespace:  "test-ns",
				Generation: 1,
				Annotations: map[string]string{
					consts.NodeStateDrainAnnotation:        consts.DrainIdle,
					consts.NodeStateDrainAnnotationCurrent: consts.DrainIdle,
				},
			},
		}
		node = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
			},
		}

		Expect(sriovnetworkv1.AddToScheme(scheme.Scheme)).ToNot(HaveOccurred())
		fakeClient = fake.NewClientBuilder().
			WithScheme(scheme.Scheme).
			WithStatusSubresource(nodeState).
			WithObjects(nodeState, node).
			Build()

		kubeclient := k8sfake.NewClientset()
		er := NewEventRecorder(fakeClient, kubeclient, scheme.Scheme)
		DeferCleanup(er.Shutdown)

		reconciler = New(fakeClient, hostHelper, platMock, er, featureGates)
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	Context("apply with reqReboot=true", func() {
		It("should proceed to reboot when count is below the limit", func() {
			tracker := &hostTypes.RebootTrackerFile{
				Generation:  1,
				RebootCount: 2,
				BootID:      "old-boot",
			}
			hostHelper.EXPECT().ReadRebootTracker().Return(tracker, nil).Times(2)
			hostHelper.EXPECT().ReadBootID().Return("new-boot", nil)
			hostHelper.EXPECT().WriteRebootTracker(&hostTypes.RebootTrackerFile{
				Generation:  1,
				RebootCount: 3,
				BootID:      "new-boot",
			}).Return(nil)
			hostHelper.EXPECT().Chroot(gomock.Any()).Return(func() error { return nil }, nil)
			hostHelper.EXPECT().RunCommand("systemd-run", gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("", "", nil)

			result, err := reconciler.apply(ctx, nodeState, true, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should set sync status to failed when count reaches the limit", func() {
			hostHelper.EXPECT().ReadRebootTracker().Return(&hostTypes.RebootTrackerFile{
				Generation:  1,
				RebootCount: consts.MaxRebootsPerGeneration,
				BootID:      "some-boot",
			}, nil)

			result, err := reconciler.apply(ctx, nodeState, true, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
			Expect(nodeState.Status.SyncStatus).To(Equal(consts.SyncStatusFailed))
			Expect(nodeState.Status.LastSyncError).To(ContainSubstring("maximum number of allowed reboots"))
		})

		It("should requeue when reboot was already counted this boot", func() {
			tracker := &hostTypes.RebootTrackerFile{
				Generation:  1,
				RebootCount: 2,
				BootID:      "same-boot",
			}
			hostHelper.EXPECT().ReadRebootTracker().Return(tracker, nil).Times(2)
			hostHelper.EXPECT().ReadBootID().Return("same-boot", nil)

			result, err := reconciler.apply(ctx, nodeState, true, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{RequeueAfter: consts.DaemonRequeueTime}))
		})

		It("should start fresh counter when generation changes even with same boot ID", func() {
			hostHelper.EXPECT().ReadRebootTracker().Return(&hostTypes.RebootTrackerFile{
				Generation:  99,
				RebootCount: 4,
				BootID:      "same-boot",
			}, nil).Times(2)
			hostHelper.EXPECT().ReadBootID().Return("same-boot", nil)
			hostHelper.EXPECT().WriteRebootTracker(&hostTypes.RebootTrackerFile{
				Generation:  1,
				RebootCount: 1,
				BootID:      "same-boot",
			}).Return(nil)
			hostHelper.EXPECT().Chroot(gomock.Any()).Return(func() error { return nil }, nil)
			hostHelper.EXPECT().RunCommand("systemd-run", gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("", "", nil)

			result, err := reconciler.apply(ctx, nodeState, true, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("should create new tracker when no tracker file exists", func() {
			hostHelper.EXPECT().ReadRebootTracker().Return(nil, nil).Times(2)
			hostHelper.EXPECT().ReadBootID().Return("boot-1", nil)
			hostHelper.EXPECT().WriteRebootTracker(&hostTypes.RebootTrackerFile{
				Generation:  1,
				RebootCount: 1,
				BootID:      "boot-1",
			}).Return(nil)
			hostHelper.EXPECT().Chroot(gomock.Any()).Return(func() error { return nil }, nil)
			hostHelper.EXPECT().RunCommand("systemd-run", gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("", "", nil)

			result, err := reconciler.apply(ctx, nodeState, true, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	Context("apply with reqReboot=false (successful apply)", func() {
		It("should reset reboot counter on success", func() {
			hostHelper.EXPECT().DiscoverRDMASubsystem().Return("shared", nil)
			platMock.EXPECT().DiscoverSriovDevices().Return([]sriovnetworkv1.InterfaceExt{}, nil)
			hostHelper.EXPECT().WriteRebootTracker(&hostTypes.RebootTrackerFile{
				Generation:  1,
				RebootCount: 0,
			}).Return(nil)

			result, err := reconciler.apply(ctx, nodeState, false, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{RequeueAfter: consts.DaemonRequeueTime}))
		})
	})
})
