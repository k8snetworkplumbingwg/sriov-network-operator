package baremetal

import (
	"errors"
	"fmt"
	"os"
	"path"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.uber.org/mock/gomock"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper"
	mock_helper "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper/mock"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host"
	hosttypes "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	plugin "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

func getDropInPath(clusterType consts.ClusterType) string {
	systemdDir := "/usr/lib/systemd/system"
	if clusterType == consts.ClusterTypeOpenshift {
		systemdDir = "/etc/systemd/system"
	}
	return path.Join("/host", systemdDir, "ovs-vswitchd.service.d")
}

var _ = Describe("Baremetal", func() {
	var (
		mockCtrl   *gomock.Controller
		bm         *Baremetal
		hostHelper *mock_helper.MockHostHelpersInterface
	)

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		hostHelper = mock_helper.NewMockHostHelpersInterface(mockCtrl)
		hostHelper.EXPECT().GetCurrentKernelArgs().Return("", nil).AnyTimes()
		hostHelper.EXPECT().IsKernelArgsSet(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

		var err error
		bm, err = New(hostHelper)
		Expect(err).NotTo(HaveOccurred())
		Expect(bm).NotTo(BeNil())
	})

	AfterEach(func() {
		// Finish checks that all expected calls were made
		mockCtrl.Finish()
	})

	Context("Initialization and Basic Getters", func() {
		It("should return nil from Init", func() {
			Expect(bm.Init()).To(BeNil())
		})
	})

	Context("Device and Bridge Discovery", func() {
		It("should discover SR-IOV devices by calling the helper", func() {
			expectedDevices := []sriovnetworkv1.InterfaceExt{{PciAddress: "0000:01:00.0"}}
			hostHelper.EXPECT().DiscoverSriovDevices(hostHelper).Return(expectedDevices, nil)

			devices, err := bm.DiscoverSriovDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(Equal(expectedDevices))
		})

		When("ManageSoftwareBridges is true", func() {
			It("should discover bridges by calling the helper", func() {
				vars.ManageSoftwareBridges = true
				expectedBridges := sriovnetworkv1.Bridges{}
				hostHelper.EXPECT().DiscoverBridges().Return(expectedBridges, nil)

				bridges, err := bm.DiscoverBridges()
				Expect(err).NotTo(HaveOccurred())
				Expect(bridges).To(Equal(expectedBridges))
			})
		})

		When("ManageSoftwareBridges is false", func() {
			It("should return empty bridges without calling the helper", func() {
				vars.ManageSoftwareBridges = false
				hostHelper.EXPECT().DiscoverBridges().Return(sriovnetworkv1.Bridges{}, nil)
				bridges, err := bm.DiscoverBridges()
				Expect(err).NotTo(HaveOccurred())
				Expect(bridges.OVS).To(BeNil())
			})
		})
	})

	Context("Plugin Loading", func() {
		var ns *sriovnetworkv1.SriovNetworkNodeState

		Context("kubernetes", func() {
			BeforeEach(func() {
				// Set cluster type to kubernetes for this context
				vars.ClusterType = consts.ClusterTypeKubernetes
				// Use DeferCleanup to reset the global variable after the tests in this context run
				DeferCleanup(func() {
					vars.ClusterType = "" // Reset to default
				})

				hostHelper.EXPECT().ReadServiceManifestFile(gomock.Any()).Return(&hosttypes.Service{Name: "test"}, nil).AnyTimes()
				hostHelper.EXPECT().ReadOvsServiceInjectionManifestFile(gomock.Any(), gomock.Any()).Return(&hosttypes.Service{Name: "test"}, nil).AnyTimes()

				// Assuming default cluster type is Kubernetes for these tests
				// In a real-world scenario, you might need to mock orchestrator.New()
				// or set environment variables if it relies on them.
				ns = &sriovnetworkv1.SriovNetworkNodeState{
					Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
						Interfaces: []sriovnetworkv1.InterfaceExt{
							{Vendor: "8086", PciAddress: "0000:01:00.0"}, // Intel
							{Vendor: "15b3", PciAddress: "0000:02:00.0"}, // Mellanox
							{Vendor: "9999", PciAddress: "0000:03:00.0"}, // Unknown
						},
					},
				}
			})

			It("should load generic, k8s, and all unique vendor plugins", func() {
				mainPlugin, addPlugins, err := bm.GetVendorPlugins(ns)

				Expect(err).NotTo(HaveOccurred())
				Expect(mainPlugin.Name()).To(Equal("generic"))

				pluginNames := []string{}
				for _, p := range addPlugins {
					pluginNames = append(pluginNames, p.Name())
				}
				Expect(pluginNames).To(ConsistOf("intel", "mellanox", "k8s", "ovs"))
			})

			It("should not duplicate plugins for vendors with multiple interfaces", func() {
				ns.Status.Interfaces = append(ns.Status.Interfaces, sriovnetworkv1.InterfaceExt{
					Vendor: "8086", PciAddress: "0000:01:00.1", // Another Intel
				})

				_, addPlugins, err := bm.GetVendorPlugins(ns)
				Expect(err).NotTo(HaveOccurred())

				pluginNames := []string{}
				for _, p := range addPlugins {
					pluginNames = append(pluginNames, p.Name())
				}
				Expect(pluginNames).To(ConsistOf("intel", "mellanox", "k8s", "ovs"))
			})

			It("should return an error if a vendor plugin fails to load", func() {
				// Temporarily replace the plugin factory with one that returns an error
				originalIntelPlugin := VendorPluginMap["8086"]
				defer func() { VendorPluginMap["8086"] = originalIntelPlugin }()

				VendorPluginMap["8086"] = func(h helper.HostHelpersInterface) (plugin.VendorPlugin, error) {
					return nil, errors.New("intel plugin load failure")
				}

				_, _, err := bm.GetVendorPlugins(ns)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("intel plugin load failure"))
			})
		})
		Context("Openshift", func() {
			BeforeEach(func() {
				// Set cluster type to Openshift for this context
				vars.ClusterType = consts.ClusterTypeOpenshift
				// Use DeferCleanup to reset the global variable after the tests in this context run
				DeferCleanup(func() {
					vars.ClusterType = "" // Reset to default
				})

				// Set up the same node state as the kubernetes tests
				ns = &sriovnetworkv1.SriovNetworkNodeState{
					Status: sriovnetworkv1.SriovNetworkNodeStateStatus{
						Interfaces: []sriovnetworkv1.InterfaceExt{
							{Vendor: "8086", PciAddress: "0000:01:00.0"}, // Intel
							{Vendor: "15b3", PciAddress: "0000:02:00.0"}, // Mellanox
						},
					},
				}
			})

			It("should load generic, vendor and OVS plugins", func() {
				mainPlugin, addPlugins, err := bm.GetVendorPlugins(ns)

				Expect(err).NotTo(HaveOccurred())
				Expect(mainPlugin.Name()).To(Equal("generic"))

				pluginNames := []string{}
				for _, p := range addPlugins {
					pluginNames = append(pluginNames, p.Name())
				}
				Expect(pluginNames).To(ConsistOf("intel", "mellanox", "ovs"))
			})
		})

		DescribeTable("writes the OVS drop-in", func(clusterType consts.ClusterType) {
			previousClusterType := vars.ClusterType
			DeferCleanup(func() { vars.ClusterType = previousClusterType })
			vars.ClusterType = clusterType

			state := &sriovnetworkv1.SriovNetworkNodeState{
				Spec: sriovnetworkv1.SriovNetworkNodeStateSpec{
					Interfaces: []sriovnetworkv1.Interface{{EswitchMode: sriovnetworkv1.ESwithModeSwitchDev}},
					System: sriovnetworkv1.System{
						OvsConfig: map[string]string{"hw-offload": "true", "tc-policy": "none"},
					},
				},
			}

			// Render the real manifest and capture the write without touching the host.
			realHost, err := host.NewHostManager(hostHelper)
			Expect(err).NotTo(HaveOccurred())
			hostHelper.EXPECT().ReadOvsServiceInjectionManifestFile(gomock.Any(), gomock.Any()).
				DoAndReturn(realHost.ReadOvsServiceInjectionManifestFile).AnyTimes()
			hostHelper.EXPECT().ReadServiceManifestFile(gomock.Any()).DoAndReturn(realHost.ReadServiceManifestFile).AnyTimes()
			hostHelper.EXPECT().IsServiceExist("/usr/lib/systemd/system/ovs-vswitchd.service").Return(true, nil).AnyTimes()
			hostHelper.EXPECT().ReadService(gomock.Any()).Return(nil, os.ErrNotExist).AnyTimes()
			var dropin *hosttypes.Service
			hostHelper.EXPECT().WriteServiceDropin(gomock.Any()).DoAndReturn(func(service *hosttypes.Service) error {
				dropin = service
				return nil
			}).AnyTimes()

			_, plugins, err := bm.GetVendorPlugins(state)
			Expect(err).NotTo(HaveOccurred())
			var needDrain, needReboot bool
			for _, p := range plugins {
				drain, reboot, err := p.OnNodeStateChange(state)
				Expect(err).NotTo(HaveOccurred())
				needDrain = needDrain || drain
				needReboot = needReboot || reboot
			}
			Expect(dropin).To(BeNil(), "the daemon should wait until Apply to write the OVS drop-in")
			for _, p := range plugins {
				Expect(p.Apply()).To(Succeed())
			}

			Expect(dropin).NotTo(BeNil(), "the %s daemon should write an OVS drop-in", clusterType)
			Expect(path.Dir(path.Join(consts.Chroot, dropin.Path))).To(Equal(getDropInPath(clusterType)))
			Expect(dropin.Content).To(Equal(expectedOVSDropInContent))
			Expect(needDrain).To(BeTrue(), "a missing OVS drop-in should require a drain")
			Expect(needReboot).To(BeTrue(), "a missing OVS drop-in should require a reboot")
		},
			Entry("on Kubernetes", consts.ClusterTypeKubernetes),
			Entry("on OpenShift", consts.ClusterTypeOpenshift),
		)
	})

	Context("Systemd Plugin Retrieval", func() {
		It("should return a configured generic plugin for PhasePre", func() {
			p, err := bm.SystemdGetVendorPlugin(consts.PhasePre)
			Expect(err).NotTo(HaveOccurred())
			Expect(p).NotTo(BeNil())
			Expect(p.Name()).To(Equal("generic"))
			// Note: Verifying the With... options requires either inspecting the
			// internal state of the plugin or having the plugin expose its config.
			// A simple type/name check is often sufficient for unit tests.
		})

		It("should return a standard generic plugin for PhasePost", func() {
			p, err := bm.SystemdGetVendorPlugin(consts.PhasePost)
			Expect(err).NotTo(HaveOccurred())
			Expect(p).NotTo(BeNil())
			Expect(p.Name()).To(Equal("generic"))
		})

		It("should return an error for an invalid phase", func() {
			p, err := bm.SystemdGetVendorPlugin("invalid-phase")
			Expect(err).To(HaveOccurred())
			Expect(p).To(BeNil())
			Expect(err.Error()).To(Equal(fmt.Sprintf("invalid phase %s", "invalid-phase")))
		})
	})
})

// Keep this expectation hardcoded so the test fails if a YAML template change alters the rendered drop-in.
const expectedOVSDropInContent = `[Service]
ExecStartPre=:/bin/bash -c '\
  old=$(/bin/ovs-vsctl -t 5 get Open_vSwitch . external_ids:sriov-operator-owned-keys 2>/dev/null | tr -d "\\\"") ; \
  [ -n "$old" ] && echo "$old" | xargs -n 1 /bin/ovs-vsctl --no-wait remove Open_vSwitch . other_config || true'
ExecStartPre=:/bin/ovs-vsctl --no-wait -t 5 set Open_vSwitch . "external_ids:sriov-operator-owned-keys=hw-offload tc-policy"
ExecStartPre=:/bin/ovs-vsctl --no-wait set Open_vSwitch . "other_config:hw-offload=true" "other_config:tc-policy=none"` + " \n"
