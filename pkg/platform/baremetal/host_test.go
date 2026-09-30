package baremetal

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	mock_helper "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper/mock"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host"
	hostTypes "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	plugin "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

const ovsDropinPath = "/etc/systemd/system/ovs-vswitchd.service.d/10-hw-offload.conf"

func setHostTestMode(clusterType consts.ClusterType, systemdMode bool) {
	origCluster := vars.ClusterType
	origSystemd := vars.UsingSystemdMode
	origForce := vars.ForcePciRealloc
	DeferCleanup(func() {
		vars.ClusterType = origCluster
		vars.UsingSystemdMode = origSystemd
		vars.ForcePciRealloc = origForce
	})
	vars.ClusterType = clusterType
	vars.UsingSystemdMode = systemdMode
	vars.ForcePciRealloc = false
}

func newServiceNameMatcher(name string) gomock.Matcher {
	return &serviceNameMatcher{name: name}
}

type serviceNameMatcher struct {
	name string
}

func (snm *serviceNameMatcher) Matches(x interface{}) bool {
	s, ok := x.(*hostTypes.Service)
	if !ok {
		return false
	}
	return snm.name == s.Name
}

func (snm *serviceNameMatcher) String() string {
	return "service name match: " + snm.name
}

func switchdevState() *sriovnetworkv1.SriovNetworkNodeState {
	return &sriovnetworkv1.SriovNetworkNodeState{
		Spec: sriovnetworkv1.SriovNetworkNodeStateSpec{
			Interfaces: []sriovnetworkv1.Interface{{EswitchMode: "switchdev"}},
		},
	}
}

func vfioState() *sriovnetworkv1.SriovNetworkNodeState {
	return &sriovnetworkv1.SriovNetworkNodeState{
		Spec: sriovnetworkv1.SriovNetworkNodeStateSpec{
			Interfaces: []sriovnetworkv1.Interface{{
				VfGroups: []sriovnetworkv1.VfGroup{{
					DeviceType: consts.DeviceTypeVfioPci,
				}},
			}},
		},
	}
}

var _ = Describe("host config", func() {
	var (
		hostPlugin *hostConfig
		testCtrl   *gomock.Controller
		hostHelper *mock_helper.MockHostHelpersInterface
		rdmaMode   string
	)

	BeforeEach(func() {
		setHostTestMode(consts.ClusterTypeKubernetes, false)
		testCtrl = gomock.NewController(GinkgoT())
		hostHelper = mock_helper.NewMockHostHelpersInterface(testCtrl)

		hostHelper.EXPECT().GetCurrentKernelArgs().Return("", nil).AnyTimes()
		hostHelper.EXPECT().IsKernelArgsSet(gomock.Any(), gomock.Any()).DoAndReturn(
			func(current, karg string) bool { return strings.Contains(current, karg) },
		).AnyTimes()
		hostHelper.EXPECT().SetRDMASubsystem(gomock.Any()).DoAndReturn(func(mode string) error {
			rdmaMode = mode
			return nil
		}).AnyTimes()
		hostHelper.EXPECT().RunCommand(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("", "", nil).AnyTimes()

		realHostMgr, err := host.NewHostManager(hostHelper)
		Expect(err).NotTo(HaveOccurred())
		hostHelper.EXPECT().ReadServiceManifestFile(sriovUnitFile).DoAndReturn(realHostMgr.ReadServiceManifestFile).AnyTimes()
		hostHelper.EXPECT().ReadServiceManifestFile(sriovPostNetworkUnitFile).DoAndReturn(realHostMgr.ReadServiceManifestFile).AnyTimes()
		hostHelper.EXPECT().ReadOvsServiceInjectionManifestFile(ovsUnitFile, gomock.Any()).
			DoAndReturn(realHostMgr.ReadOvsServiceInjectionManifestFile).AnyTimes()

		hostPlugin, err = newHostConfig(hostHelper)
		Expect(err).NotTo(HaveOccurred())
		Expect(hostPlugin.Name()).To(Equal(hostPluginName))
	})

	AfterEach(func() {
		testCtrl.Finish()
	})

	It("renders the OVS drop-in under /etc/systemd and defaults hw-offload", func() {
		_, _, err := hostPlugin.OnNodeStateChange(&sriovnetworkv1.SriovNetworkNodeState{})
		Expect(err).NotTo(HaveOccurred())
		Expect(hostPlugin.openVSwitchService.Path).To(Equal(ovsDropinPath))
		Expect(hostPlugin.openVSwitchService.Content).To(ContainSubstring("hw-offload"))
	})

	It("does not drain or reboot when kernel args and systemd files are unchanged", func() {
		needDrain, needReboot, err := hostPlugin.OnNodeStateChange(&sriovnetworkv1.SriovNetworkNodeState{})
		Expect(err).NotTo(HaveOccurred())
		Expect(needDrain).To(BeFalse())
		Expect(needReboot).To(BeFalse())
		Expect(hostPlugin.Apply()).To(Succeed())
	})

	It("requests drain and reboot when a kernel argument must be added", func() {
		hostHelper.EXPECT().GetCPUVendor().Return(hostTypes.CPUVendorIntel, nil)
		needDrain, needReboot, err := hostPlugin.OnNodeStateChange(vfioState())
		Expect(err).NotTo(HaveOccurred())
		Expect(needDrain).To(BeTrue())
		Expect(needReboot).To(BeTrue())
		Expect(hostPlugin.desiredKargs[consts.KernelArgIntelIommu]).To(BeTrue())
		Expect(hostPlugin.desiredKargs[consts.KernelArgIommuPt]).To(BeTrue())
	})

	It("enables only iommu=pt for AMD vfio", func() {
		hostHelper.EXPECT().GetCPUVendor().Return(hostTypes.CPUVendorAMD, nil)
		_, _, err := hostPlugin.OnNodeStateChange(vfioState())
		Expect(err).NotTo(HaveOccurred())
		Expect(hostPlugin.desiredKargs[consts.KernelArgIntelIommu]).To(BeFalse())
		Expect(hostPlugin.desiredKargs[consts.KernelArgIommuPt]).To(BeTrue())
	})

	It("sets RDMA kernel args and the modprobe mode", func() {
		state := &sriovnetworkv1.SriovNetworkNodeState{
			Spec: sriovnetworkv1.SriovNetworkNodeStateSpec{
				System: sriovnetworkv1.System{RdmaMode: consts.RdmaSubsystemModeShared},
			},
		}
		needDrain, needReboot, err := hostPlugin.OnNodeStateChange(state)
		Expect(err).NotTo(HaveOccurred())
		Expect(needDrain).To(BeTrue())
		Expect(needReboot).To(BeTrue())
		Expect(rdmaMode).To(Equal(consts.RdmaSubsystemModeShared))
		Expect(hostPlugin.desiredKargs[consts.KernelArgRdmaShared]).To(BeTrue())
		Expect(hostPlugin.desiredKargs[consts.KernelArgRdmaExclusive]).To(BeFalse())
	})

	It("reports RDMA drift from CheckStatusChanges", func() {
		state := &sriovnetworkv1.SriovNetworkNodeState{
			Spec: sriovnetworkv1.SriovNetworkNodeStateSpec{
				System: sriovnetworkv1.System{RdmaMode: consts.RdmaSubsystemModeShared},
			},
			Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
				System: sriovnetworkv1.System{RdmaMode: consts.RdmaSubsystemModeExclusive},
			},
		}
		changed, err := hostPlugin.CheckStatusChanges(state)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
	})

	It("reports kernel arg drift from CheckStatusChanges", func() {
		hostHelper.EXPECT().GetCPUVendor().Return(hostTypes.CPUVendorIntel, nil)
		_, _, err := hostPlugin.OnNodeStateChange(vfioState())
		Expect(err).NotTo(HaveOccurred())
		changed, err := hostPlugin.CheckStatusChanges(vfioState())
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
	})

	It("keeps pci=realloc sticky after ENOMEM", func() {
		vars.ForcePciRealloc = true
		pluginWithSticky, err := newHostConfig(hostHelper)
		Expect(err).NotTo(HaveOccurred())
		Expect(pluginWithSticky.desiredKargs[consts.KernelArgPciRealloc]).To(BeTrue())

		needDrain, needReboot, err := pluginWithSticky.OnNodeStateChange(&sriovnetworkv1.SriovNetworkNodeState{})
		Expect(err).NotTo(HaveOccurred())
		Expect(needDrain).To(BeTrue())
		Expect(needReboot).To(BeTrue())
	})

	It("loads vfio_pci, virtio_vdpa, and vhost_vdpa when the spec needs them", func() {
		state := &sriovnetworkv1.SriovNetworkNodeState{
			Spec: sriovnetworkv1.SriovNetworkNodeStateSpec{
				Interfaces: []sriovnetworkv1.Interface{{
					VfGroups: []sriovnetworkv1.VfGroup{
						{DeviceType: consts.DeviceTypeVfioPci},
						{DeviceType: consts.DeviceTypeNetDevice, VdpaType: consts.VdpaTypeVirtio},
						{DeviceType: consts.DeviceTypeNetDevice, VdpaType: consts.VdpaTypeVhost},
					},
				}},
			},
		}
		hostHelper.EXPECT().GetCPUVendor().Return(hostTypes.CPUVendorIntel, nil)
		_, _, err := hostPlugin.OnNodeStateChange(state)
		Expect(err).NotTo(HaveOccurred())

		hostHelper.EXPECT().LoadKernelModule(vfioPciDriver).Return(nil)
		hostHelper.EXPECT().LoadKernelModule(virtioVdpaDriver).Return(nil)
		hostHelper.EXPECT().LoadKernelModule(vhostVdpaDriver).Return(nil)
		Expect(hostPlugin.Apply()).To(Succeed())
	})

	It("requests drain and reboot when the OVS drop-in changes", func() {
		hostHelper.EXPECT().IsServiceExist(ovsMainServicePath).Return(true, nil)
		hostHelper.EXPECT().ReadService(ovsDropinPath).Return(&hostTypes.Service{Name: "ovs-vswitchd.service"}, nil)
		hostHelper.EXPECT().CompareServices(gomock.Any(), newServiceNameMatcher("ovs-vswitchd.service")).Return(true, nil)
		hostHelper.EXPECT().WriteServiceDropin(newServiceNameMatcher("ovs-vswitchd.service")).Return(nil)

		needDrain, needReboot, err := hostPlugin.OnNodeStateChange(switchdevState())
		Expect(err).NotTo(HaveOccurred())
		Expect(needDrain).To(BeTrue())
		Expect(needReboot).To(BeTrue())
		Expect(hostPlugin.Apply()).To(Succeed())
	})

	It("does not reboot when the OVS drop-in is already current", func() {
		hostHelper.EXPECT().IsServiceExist(ovsMainServicePath).Return(true, nil)
		hostHelper.EXPECT().ReadService(ovsDropinPath).Return(&hostTypes.Service{Name: "ovs-vswitchd.service"}, nil)
		hostHelper.EXPECT().CompareServices(gomock.Any(), newServiceNameMatcher("ovs-vswitchd.service")).Return(false, nil)

		needDrain, needReboot, err := hostPlugin.OnNodeStateChange(switchdevState())
		Expect(err).NotTo(HaveOccurred())
		Expect(needDrain).To(BeFalse())
		Expect(needReboot).To(BeFalse())
		Expect(hostPlugin.Apply()).To(Succeed())
	})

	It("skips sriov-config units on OpenShift", func() {
		setHostTestMode(consts.ClusterTypeOpenshift, true)
		needDrain, needReboot, err := hostPlugin.OnNodeStateChange(&sriovnetworkv1.SriovNetworkNodeState{})
		Expect(err).NotTo(HaveOccurred())
		Expect(needDrain).To(BeFalse())
		Expect(needReboot).To(BeFalse())
		Expect(hostPlugin.Apply()).To(Succeed())
	})

	It("enables sriov-config units on Kubernetes systemd mode", func() {
		setHostTestMode(consts.ClusterTypeKubernetes, true)
		hostHelper.EXPECT().IsServiceEnabled("/etc/systemd/system/sriov-config.service").Return(false, nil)
		hostHelper.EXPECT().IsServiceEnabled("/etc/systemd/system/sriov-config-post-network.service").Return(false, nil)
		hostHelper.EXPECT().EnableService(newServiceNameMatcher("sriov-config.service")).Return(nil)
		hostHelper.EXPECT().EnableService(newServiceNameMatcher("sriov-config-post-network.service")).Return(nil)

		needDrain, needReboot, err := hostPlugin.OnNodeStateChange(&sriovnetworkv1.SriovNetworkNodeState{})
		Expect(err).NotTo(HaveOccurred())
		Expect(needDrain).To(BeTrue())
		Expect(needReboot).To(BeTrue())
		Expect(hostPlugin.Apply()).To(Succeed())
	})
})

type orderPlugin struct {
	name   string
	order  *[]string
	drain  bool
	reboot bool
}

func (o *orderPlugin) Name() string { return o.name }

func (o *orderPlugin) OnNodeStateChange(*sriovnetworkv1.SriovNetworkNodeState) (bool, bool, error) {
	*o.order = append(*o.order, o.name+"-change")
	return o.drain, o.reboot, nil
}

func (o *orderPlugin) Apply() error {
	*o.order = append(*o.order, o.name+"-apply")
	return nil
}

func (o *orderPlugin) CheckStatusChanges(*sriovnetworkv1.SriovNetworkNodeState) (bool, error) {
	*o.order = append(*o.order, o.name+"-check")
	return false, nil
}

var _ = Describe("systemd plugin chain", func() {
	It("runs host config before generic and ORs reboot", func() {
		var order []string
		hostP := &orderPlugin{name: hostPluginName, order: &order, reboot: true}
		genericP := &orderPlugin{name: "generic", order: &order, drain: true}
		chain := newPluginChain(systemdChainName, hostP, genericP)

		Expect(chain.Name()).To(Equal(systemdChainName))
		needDrain, needReboot, err := chain.OnNodeStateChange(&sriovnetworkv1.SriovNetworkNodeState{})
		Expect(err).NotTo(HaveOccurred())
		Expect(needDrain).To(BeTrue())
		Expect(needReboot).To(BeTrue())
		Expect(chain.Apply()).To(Succeed())
		_, err = chain.CheckStatusChanges(&sriovnetworkv1.SriovNetworkNodeState{})
		Expect(err).NotTo(HaveOccurred())
		Expect(order).To(Equal([]string{
			"host-change", "generic-change",
			"host-apply", "generic-apply",
			"host-check", "generic-check",
		}))
	})
})

// Ensure orderPlugin satisfies VendorPlugin at compile time.
var _ plugin.VendorPlugin = (*orderPlugin)(nil)
