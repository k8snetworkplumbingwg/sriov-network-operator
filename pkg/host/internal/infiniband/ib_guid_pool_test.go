package infiniband

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	netlinkLibPkg "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/netlink"
	netlinkMockPkg "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/netlink/mock"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/network"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

var _ = Describe("ibGUIDPool", func() {
	var (
		tempDir        string
		netlinkLibMock *netlinkMockPkg.MockNetlinkLib
		testCtrl       *gomock.Controller

		createJsonConfig func(string) string
	)

	BeforeEach(func() {
		var err error
		tempDir, err = os.MkdirTemp("", "ibguidconfig")
		Expect(err).NotTo(HaveOccurred())

		createJsonConfig = func(content string) string {
			configPath := filepath.Join(tempDir, "config.json")
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			return configPath
		}

		testCtrl = gomock.NewController(GinkgoT())
		netlinkLibMock = netlinkMockPkg.NewMockNetlinkLib(testCtrl)
		netlinkLibMock.EXPECT().LinkList().Return([]netlinkLibPkg.Link{}, nil).AnyTimes()
	})

	AfterEach(func() {
		os.RemoveAll(tempDir)
		testCtrl.Finish()
	})
	It("should parse correct json config and return a map", func() {
		vfGuid1, err := utils.ParseGUID("00:00:00:00:00:00:00:00")
		Expect(err).NotTo(HaveOccurred())
		vfGuid2, err := utils.ParseGUID("00:00:00:00:00:00:00:01")
		Expect(err).NotTo(HaveOccurred())
		rangeStart, err := utils.ParseGUID("00:00:00:00:00:00:01:00")
		Expect(err).NotTo(HaveOccurred())
		rangeEnd, err := utils.ParseGUID("00:00:00:00:00:00:01:02")
		Expect(err).NotTo(HaveOccurred())

		configPath := createJsonConfig(fmt.Sprintf(`[{"pciAddress":"0000:3b:00.0","guids":["%s", "%s"]},{"pciAddress":"0000:3b:00.1","rangeStart":"%s","rangeEnd":"%s"}]`, vfGuid1.String(), vfGuid2.String(), rangeStart.String(), rangeEnd.String()))

		pool, err := newIbGUIDPool(configPath, netlinkLibMock, network.New(nil, nil, nil, nil))
		Expect(err).NotTo(HaveOccurred())

		guid, err := pool.GetNextFreeGUID("0000:3b:00.0", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(guid.String()).To(Equal(vfGuid1.String()))

		guid, err = pool.GetNextFreeGUID("0000:3b:00.0", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(guid.String()).To(Equal(vfGuid2.String()))

		guid, err = pool.GetNextFreeGUID("0000:3b:00.0", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(guid.String()).To(Equal(vfGuid1.String()))

		guid, err = pool.GetNextFreeGUID("0000:3b:00.0", 2)
		Expect(err).To(MatchError("guid pool exhausted for pci address: 0000:3b:00.0"))

		guid, err = pool.GetNextFreeGUID("0000:3b:00.0", 5)
		Expect(err).To(MatchError("guid pool exhausted for pci address: 0000:3b:00.0"))

		guid, err = pool.GetNextFreeGUID("0000:3b:00.1", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:00"))

		guid, err = pool.GetNextFreeGUID("0000:3b:00.1", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:01"))

		guid, err = pool.GetNextFreeGUID("0000:3b:00.1", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:02"))

		guid, err = pool.GetNextFreeGUID("0000:3b:00.1", 3)
		Expect(err).To(MatchError("guid pool exhausted for pci address: 0000:3b:00.1"))

		guid, err = pool.GetNextFreeGUID("0000:3b:00.1", 5)
		Expect(err).To(MatchError("guid pool exhausted for pci address: 0000:3b:00.1"))

		guid, err = pool.GetNextFreeGUID("0000:3b:00.1", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(guid.String()).To(Equal("00:00:00:00:00:00:01:01"))
	})
})
