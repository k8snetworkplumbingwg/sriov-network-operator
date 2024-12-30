package service

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.uber.org/mock/gomock"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	mock_utils "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils/mock"
)

var _ = Describe("Systemd", func() {
	var (
		utilsMock = &mock_utils.MockCmdInterface{}
		s         types.ServiceInterface
		srv       = types.Service{
			Name:    "test-service",
			Path:    "",
			Content: "",
		}
		t        FullGinkgoTInterface
		mockCtrl *gomock.Controller
	)

	BeforeEach(func() {
		t = GinkgoT()
		mockCtrl = gomock.NewController(t)
		utilsMock = mock_utils.NewMockCmdInterface(mockCtrl)
		s = New(utilsMock)
	})

	Context("Service manage", func() {
		It("should restart service", func() {
			utilsMock.EXPECT().Chroot(gomock.Any()).Return(func() error { return nil }, nil)
			utilsMock.EXPECT().RunCommand("systemctl", "restart", srv.Name).Return("", "", nil)
			err := s.RestartService(&srv)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should reload service", func() {
			utilsMock.EXPECT().Chroot(gomock.Any()).Return(func() error { return nil }, nil)
			utilsMock.EXPECT().RunCommand("systemctl", "daemon-reload").Return("", "", nil)
			err := s.ReloadService()
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
