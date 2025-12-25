package daemon

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	mock_helper "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper/mock"
	hosttypes "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

var _ = Describe("Daemon checkSystemdStatus", func() {
	var (
		mockCtrl   *gomock.Controller
		hostHelper *mock_helper.MockHostHelpersInterface
		reconciler *NodeReconciler
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		hostHelper = mock_helper.NewMockHostHelpersInterface(mockCtrl)
		reconciler = &NodeReconciler{
			hostHelpers: hostHelper,
		}
		vars.UsingSystemdMode = true
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	Context("when systemd services are enabled", func() {
		BeforeEach(func() {
			hostHelper.EXPECT().IsServiceEnabled(consts.SriovServicePath).Return(true, nil)
			hostHelper.EXPECT().IsServiceEnabled(consts.SriovPostNetworkServicePath).Return(true, nil)
		})

		It("should return exist=true if sriov result file exists", func() {
			hostHelper.EXPECT().ReadSriovResult().Return(&hosttypes.SriovResult{}, nil)
			result, exist, err := reconciler.checkSystemdStatus()
			Expect(err).ToNot(HaveOccurred())
			Expect(exist).To(BeTrue())
			Expect(result).ToNot(BeNil())
		})

		It("should return exist=false if sriov result file does not exist", func() {
			hostHelper.EXPECT().ReadSriovResult().Return(nil, nil)
			result, exist, err := reconciler.checkSystemdStatus()
			Expect(err).ToNot(HaveOccurred())
			Expect(exist).To(BeFalse())
			Expect(result).To(BeNil())
		})
	})
})
