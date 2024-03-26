package infiniband

import (
	"fmt"
	"net"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kernelMockPkg "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/kernel/mock"
	netlinkLibPkg "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/netlink"
	netlinkMockPkg "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/netlink/mock"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

var _ = Describe("infiniband interface implementation", func() {
	It("should create infiniband helper if guid config path is empty", func() {
		testCtrl := gomock.NewController(GinkgoT())
		netlinkLibMock := netlinkMockPkg.NewMockNetlinkLib(testCtrl)
		netlinkLibMock.EXPECT().LinkList().Return([]netlinkLibPkg.Link{}, nil).AnyTimes()

		_, err := New(netlinkLibMock, nil, nil)
		Expect(err).NotTo(HaveOccurred())
	})
	It("should assign guids if guid pool is nil", func() {
		testCtrl := gomock.NewController(GinkgoT())
		netlinkLibMock := netlinkMockPkg.NewMockNetlinkLib(testCtrl)
		netlinkLibMock.EXPECT().LinkList().Return([]netlinkLibPkg.Link{}, nil).AnyTimes()
		netlinkLibMock.EXPECT().LinkSetVfNodeGUID(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(link netlinkLibPkg.Link, vf int, nodeguid net.HardwareAddr) error {
			return fmt.Errorf(nodeguid.String())
		}).Times(1)
		netlinkLibMock.EXPECT().LinkSetVfPortGUID(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
		kernelHelperMock := kernelMockPkg.NewMockKernelInterface(testCtrl)
		kernelHelperMock.EXPECT().Unbind(gomock.Any()).Return(nil).Times(1)

		ib, err := New(netlinkLibMock, nil, nil)
		Expect(err).NotTo(HaveOccurred())

		err = ib.ConfigureVfGUID("randomAddr", "randomAddr", 0, nil)
		Expect(err).To(HaveOccurred())
		_, err = utils.ParseGUID(err.Error())
		Expect(err).NotTo(HaveOccurred())
	})
})
