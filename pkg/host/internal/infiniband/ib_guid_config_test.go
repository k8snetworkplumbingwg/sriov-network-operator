package infiniband

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/vishvananda/netlink"

	netlinkLibPkg "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/netlink"
	netlinkMockPkg "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/netlink/mock"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/network"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/fakefilesystem"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/helpers"
)

var _ = Describe("IbGuidConfig", func() {
	Describe("readJSONConfig", func() {
		var (
			tempDir    string
			configPath string
		)

		BeforeEach(func() {
			var err error
			tempDir, err = os.MkdirTemp("", "ibguidconfig")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			os.RemoveAll(tempDir)
		})
		It("should correctly decode a JSON configuration file with all fields present", func() {
			mockJsonConfig := `[{"pciAddress":"0000:00:00.0","pfGuid":"00:00:00:00:00:00:00:00","guids":["00:01:02:03:04:05:06:07", "00:01:02:03:04:05:06:08"],"rangeStart":"00:01:02:03:04:05:06:08","rangeEnd":"00:01:02:03:04:05:06:FF"}]`

			configPath = filepath.Join(tempDir, "config.json")
			err := os.WriteFile(configPath, []byte(mockJsonConfig), 0644)
			Expect(err).NotTo(HaveOccurred())

			configs, err := readJSONConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(configs).To(HaveLen(1))
			Expect(configs[0].PciAddress).To(Equal("0000:00:00.0"))
			Expect(configs[0].GUIDs).To(ContainElement("00:01:02:03:04:05:06:07"))
			Expect(configs[0].GUIDs).To(ContainElement("00:01:02:03:04:05:06:08"))
		})
		It("should correctly decode a JSON configuration file with one field present", func() {
			mockJsonConfig := `[{"pciAddress":"0000:00:00.0"}]`

			configPath = filepath.Join(tempDir, "config.json")
			err := os.WriteFile(configPath, []byte(mockJsonConfig), 0644)
			Expect(err).NotTo(HaveOccurred())

			configs, err := readJSONConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(configs).To(HaveLen(1))
			Expect(configs[0].PciAddress).To(Equal("0000:00:00.0"))
		})
		It("should correctly decode a JSON array with several elements", func() {
			mockJsonConfig := `[{"pciAddress":"0000:00:00.0","guids":["00:01:02:03:04:05:06:07"]},{"pfGuid":"00:00:00:00:00:00:00:00","rangeStart":"00:01:02:03:04:05:06:08","rangeEnd":"00:01:02:03:04:05:06:FF"}]`

			configPath = filepath.Join(tempDir, "config.json")
			err := os.WriteFile(configPath, []byte(mockJsonConfig), 0644)
			Expect(err).NotTo(HaveOccurred())

			configs, err := readJSONConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(configs).To(HaveLen(2))
			Expect(configs[0].PciAddress).To(Equal("0000:00:00.0"))
			Expect(configs[1].RangeStart).To(Equal("00:01:02:03:04:05:06:08"))
		})
		It("should fail on a non-array JSON", func() {
			mockJsonConfig := `{"pciAddress":"0000:00:00.0", "newField": "newValue"}`

			configPath = filepath.Join(tempDir, "config.json")
			err := os.WriteFile(configPath, []byte(mockJsonConfig), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, err = readJSONConfig(configPath)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("getPfPciAddressFromRawConfig", func() {
		var (
			networkHelper types.NetworkInterface
		)
		BeforeEach(func() {
			networkHelper = network.New(nil, nil, nil, nil)
		})
		It("should return same pci address when pci address is provided", func() {
			pci, err := getPfPciAddressFromRawConfig(ibPfGUIDJSONConfig{PciAddress: "pciAddress"}, nil, networkHelper)
			Expect(err).NotTo(HaveOccurred())
			Expect(pci).To(Equal("pciAddress"))
		})
		It("should find correct pci address when pf guid is given", func() {
			pfGuid := utils.GenerateRandomGUID()

			testCtrl := gomock.NewController(GinkgoT())
			//netlinkLibMock := netlinkMockPkg.NewMockNetlinkLib(testCtrl)
			pfLinkMock := netlinkMockPkg.NewMockLink(testCtrl)
			//netlinkLibMock.EXPECT().LinkList().Return([]netlinkLibPkg.Link{pfLinkMock}, nil)
			pfLinkMock.EXPECT().Attrs().Return(&netlink.LinkAttrs{Name: "ib216s0f0", HardwareAddr: pfGuid}).Times(2)

			helpers.GinkgoConfigureFakeFS(&fakefilesystem.FS{
				Dirs:     []string{"/sys/bus/pci/0000:3b:00.0", "/sys/class/net/ib216s0f0"},
				Symlinks: map[string]string{"/sys/class/net/ib216s0f0/device": "/sys/bus/pci/0000:3b:00.0"},
			})

			pci, err := getPfPciAddressFromRawConfig(ibPfGUIDJSONConfig{PfGUID: pfGuid.String()}, []netlinkLibPkg.Link{pfLinkMock}, networkHelper)
			Expect(err).NotTo(HaveOccurred())
			Expect(pci).To(Equal("0000:3b:00.0"))

			testCtrl.Finish()
		})
		It("should return an error when no matching link is found", func() {
			pfGuidDesired := utils.GenerateRandomGUID()
			pfGuidActual := utils.GenerateRandomGUID()

			testCtrl := gomock.NewController(GinkgoT())
			pfLinkMock := netlinkMockPkg.NewMockLink(testCtrl)
			pfLinkMock.EXPECT().Attrs().Return(&netlink.LinkAttrs{Name: "ib216s0f0", HardwareAddr: pfGuidActual}).Times(1)

			helpers.GinkgoConfigureFakeFS(&fakefilesystem.FS{
				Dirs:     []string{"/sys/bus/pci/0000:3b:00.0", "/sys/class/net/ib216s0f0"},
				Symlinks: map[string]string{"/sys/class/net/ib216s0f0/device": "/sys/bus/pci/0000:3b:00.0"},
			})

			_, err := getPfPciAddressFromRawConfig(ibPfGUIDJSONConfig{PfGUID: pfGuidDesired.String()}, []netlinkLibPkg.Link{pfLinkMock}, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(MatchRegexp(`no matching link found for pf guid:.*`)))

			testCtrl.Finish()
		})
		It("should return an error when too many parameters are provided", func() {
			_, err := getPfPciAddressFromRawConfig(ibPfGUIDJSONConfig{PfGUID: "pfGuid", PciAddress: "pciAddress"}, nil, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError("either PCI address or PF GUID required to describe an interface, both provided"))
		})
		It("should return an error when too few parameters are provided", func() {
			_, err := getPfPciAddressFromRawConfig(ibPfGUIDJSONConfig{}, nil, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError("either PCI address or PF GUID required to describe an interface, none provided"))
		})
	})

	Describe("getIbGUIDConfig", func() {
		var (
			tempDir        string
			netlinkLibMock *netlinkMockPkg.MockNetlinkLib
			testCtrl       *gomock.Controller

			createJsonConfig func(string) string

			networkHelper types.NetworkInterface
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

			networkHelper = network.New(nil, nil, nil, nil)
		})

		AfterEach(func() {
			os.RemoveAll(tempDir)
			testCtrl.Finish()
		})
		It("should parse correct json config and return a map", func() {
			helpers.GinkgoConfigureFakeFS(&fakefilesystem.FS{
				Dirs:     []string{"/sys/bus/pci/0000:3b:00.0", "/sys/class/net/ib216s0f0"},
				Symlinks: map[string]string{"/sys/class/net/ib216s0f0/device": "/sys/bus/pci/0000:3b:00.0"},
			})

			pfGuid := utils.GenerateRandomGUID()
			vfGuid1 := utils.GenerateRandomGUID()
			vfGuid2 := utils.GenerateRandomGUID()
			rangeStart, err := utils.ParseGUID("00:01:02:03:04:05:06:08")
			Expect(err).NotTo(HaveOccurred())
			rangeEnd, err := utils.ParseGUID("00:01:02:03:04:05:06:FF")
			Expect(err).NotTo(HaveOccurred())
			configPath := createJsonConfig(fmt.Sprintf(`[{"pciAddress":"0000:3b:00.1","guids":["%s", "%s"]},{"pfGuid":"%s","rangeStart":"%s","rangeEnd":"%s"}]`, vfGuid1.String(), vfGuid2.String(), pfGuid.String(), rangeStart.String(), rangeEnd.String()))

			pfLinkMock := netlinkMockPkg.NewMockLink(testCtrl)
			pfLinkMock.EXPECT().Attrs().Return(&netlink.LinkAttrs{Name: "ib216s0f0", HardwareAddr: pfGuid}).Times(2)

			netlinkLibMock := netlinkMockPkg.NewMockNetlinkLib(testCtrl)
			netlinkLibMock.EXPECT().LinkList().Return([]netlinkLibPkg.Link{pfLinkMock}, nil)

			config, err := getIbGUIDConfig(configPath, netlinkLibMock, networkHelper)
			Expect(err).NotTo(HaveOccurred())
			Expect(config["0000:3b:00.1"].GUIDs[0]).To(Equal(vfGuid1))
			Expect(config["0000:3b:00.1"].GUIDs[1]).To(Equal(vfGuid2))
			Expect(config["0000:3b:00.0"].RangeStart).To(Equal(rangeStart))
			Expect(config["0000:3b:00.0"].RangeEnd).To(Equal(rangeEnd))
		})
		It("should return an error when invalid json config is provided", func() {
			configPath := createJsonConfig(`[invalid file]`)

			_, err := getIbGUIDConfig(configPath, netlinkLibMock, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(MatchRegexp("failed to decode ib guid config from json.*")))
		})
		It("should return an error when failed to determine pf's pci address", func() {
			configPath := createJsonConfig(`[{"guids":["00:01:02:03:04:05:06:07"]}]`)

			_, err := getIbGUIDConfig(configPath, netlinkLibMock, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(MatchRegexp("failed to extract pci address from ib guid config.*")))
		})
		It("should return an error when both guids and rangeStart are provided", func() {
			configPath := createJsonConfig(`[{"pciAddress": "someaddress", "guids":["00:01:02:03:04:05:06:07"], "rangeStart": "00:01:02:03:04:05:06:AA"}]`)

			_, err := getIbGUIDConfig(configPath, netlinkLibMock, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(MatchRegexp("either guid list or guid range should be provided, got both.*")))
		})
		It("should return an error when both guids and rangeEnd are provided", func() {
			configPath := createJsonConfig(`[{"pciAddress": "someaddress", "guids":["00:01:02:03:04:05:06:07"], "rangeEnd": "00:01:02:03:04:05:06:AA"}]`)

			_, err := getIbGUIDConfig(configPath, netlinkLibMock, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(MatchRegexp("either guid list or guid range should be provided, got both.*")))
		})
		It("should return an error when neither guids nor range are provided", func() {
			configPath := createJsonConfig(`[{"pciAddress": "someaddress"}]`)

			_, err := getIbGUIDConfig(configPath, netlinkLibMock, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(MatchRegexp("either guid list or guid range should be provided, got none.*")))
		})
		It("should return an error when invalid guid list is provided", func() {
			configPath := createJsonConfig(`[{"pciAddress": "someaddress", "guids":["invalid_guid"]}]`)

			_, err := getIbGUIDConfig(configPath, netlinkLibMock, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(MatchRegexp("failed to parse ib guid invalid_guid.*")))
		})
		It("should return an error when invalid guid range start is provided", func() {
			configPath := createJsonConfig(`[{"pciAddress": "someaddress", "rangeStart":"invalid range start", "rangeEnd":"00:01:02:03:04:05:06:FF"}]`)

			_, err := getIbGUIDConfig(configPath, netlinkLibMock, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(MatchRegexp("failed to parse ib guid range start.*")))
		})
		It("should return an error when invalid guid range end is provided", func() {
			configPath := createJsonConfig(`[{"pciAddress": "someaddress", "rangeStart":"00:01:02:03:04:05:06:08", "rangeEnd":"invalid range end"}]`)

			_, err := getIbGUIDConfig(configPath, netlinkLibMock, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(MatchRegexp("failed to parse ib guid range end.*")))
		})
		It("should return an error when guid range end is less than range start", func() {
			configPath := createJsonConfig(`[{"pciAddress": "someaddress", "rangeStart":"00:01:02:03:04:05:06:FF", "rangeEnd":"00:01:02:03:04:05:06:AA"}]`)

			_, err := getIbGUIDConfig(configPath, netlinkLibMock, networkHelper)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(MatchRegexp("range end cannot be less then or equal to range start.*")))
		})
	})
})
