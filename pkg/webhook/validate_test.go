package webhook

import (
	"fmt"
	"os"
	"testing"

	. "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestMain(m *testing.M) {
	NicIdMap = []string{
		"14e4 168e 16af", // Broadcom bnx2x BCM57810
		"14e4 16a1 16ad", // Broadcom bnx2x BCM57840
		"14e4 16d7 16c1", // Broadcom bnxt_en BCM57414 NetXtreme-E
		"14e4 1750 1806", // Broadcom bnxt_en BCM57508 NetXtreme-E
		"1425 4401 4801", // Chelsio cxgb4 T420-CR
		"1425 4402 4802", // Chelsio cxgb4 T422-CR
		"1425 5401 5801", // Chelsio cxgb4 T520-CR
		"1425 540d 580d", // Chelsio cxgb4 T580-CR
		"10df 0720 0720", // Emulex be2net OneConnect
		"8086 158a 154c", // I40e XXV710
		"8086 158b 154c", // I40e 25G SFP28
		"8086 1583 154c", // I40e 40G XL710 QSFP+
		"8086 1572 154c", // I40e 10G X710 SFP+
		"8086 37d0 37cd", // Intel i40e X722 10G
		"8086 0d58 154c", // I40e XXV710 N3000
		"8086 1592 1889", // Columbiaville E810-CQDA2/2CQDA2
		"8086 1593 1889", // Columbiaville E810-XXVDA4
		"8086 159b 1889", // Columbiaville E810-XXVDA2
		"8086 1521 1520", // Intel igb I350
		"8086 10fb 10ed", // Intel ixgbe 82599ES
		"8086 154d 10ed", // Intel ixgbe X520
		"8086 1528 1515", // Intel ixgbe X520-AT2
		"8086 1563 1565", // Intel ixgbe X550T
		"15b3 1007 1004", // Nvidia mlx4 ConnectX-3 Pro
		"15b3 1013 1014", // Nvidia mlx5 ConnectX-4
		"15b3 1015 1016", // Nvidia mlx5 ConnectX-4 LX
		"15b3 1017 1018", // Nvidia mlx5 ConnectX-5
		"15b3 1019 101a", // Nvidia mlx5 ConnectX-5 Ex
		"15b3 101b 101c", // Nvidia mlx5 ConnectX-6
		"15b3 101d 101e", // Nvidia mlx5 ConnectX-6 Dx
		"15b3 a2d6 101e", // Nvidia mlx5 MT42822 BlueField-2 integrated ConnectX-6 Dx
		"1dd8 1002 1003", // Pensando ionic DSC
		"1077 8070 8090", // Qlogic qede QL41000
		"1077 1656 1664", // Qlogic qede QL45000 25G
		"1077 1654 1664", // Qlogic qede QL45000 50G
		"8086 1591 1889", // Silicom ice STS
		"1924 0a03 1a03", // Solarflare sfc SFC9220
		"1924 0b03 1b03", // Solarflare sfc SFC9250
	}
	os.Exit(m.Run())
}

func newNodeState() *SriovNetworkNodeState {
	return &SriovNetworkNodeState{
		Spec: SriovNetworkNodeStateSpec{
			Interfaces: []Interface{
				{
					Name:       "ens803f1",
					NumVfs:     4,
					PciAddress: "0000:86:00.1",
					VfGroups: []VfGroup{
						{
							DeviceType:   "netdevice",
							ResourceName: "nic1",
							VfRange:      "0-3",
						},
					},
				},
			},
		},
		Status: SriovNetworkNodeStateStatus{
			Interfaces: []InterfaceExt{
				{
					VFs: []VirtualFunction{
						{},
					},
					DeviceID:   "158b",
					Driver:     "i40e",
					Mtu:        1500,
					Name:       "ens803f0",
					PciAddress: "0000:86:00.0",
					Vendor:     "8086",
					NumVfs:     4,
					TotalVfs:   64,
				},
				{
					VFs: []VirtualFunction{
						{},
					},
					DeviceID:   "158b",
					Driver:     "i40e",
					Mtu:        1500,
					Name:       "ens803f1",
					PciAddress: "0000:86:00.1",
					Vendor:     "8086",
					NumVfs:     4,
					TotalVfs:   64,
				},
				{
					VFs: []VirtualFunction{
						{},
					},
					DeviceID:   "1015",
					Driver:     "i40e",
					Mtu:        1500,
					Name:       "ens803f2",
					PciAddress: "0000:86:00.2",
					Vendor:     "8086",
					NumVfs:     4,
					TotalVfs:   64,
				},
			},
		},
	}
}

func newNodePolicy() *SriovNetworkNodePolicy {
	return &SriovNetworkNodePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p1",
		},
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				PfNames:     []string{"ens803f1#0-2"},
				RootDevices: []string{"0000:86:00.1"},
				Vendor:      "8086",
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       63,
			Priority:     99,
			ResourceName: "p1",
		},
	}
}

func TestValidateSriovOperatorConfigWithDefaultOperatorConfig(t *testing.T) {
	var err error
	var ok bool
	var w []string
	config := &SriovOperatorConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: SriovOperatorConfigSpec{
			ConfigDaemonNodeSelector: map[string]string{},
			DisableDrain:             true,
			EnableInjector:           func() *bool { b := true; return &b }(),
			EnableOperatorWebhook:    func() *bool { b := true; return &b }(),
			LogLevel:                 2,
		},
	}
	g := NewGomegaWithT(t)
	ok, _, err = validateSriovOperatorConfig(config, "DELETE")
	g.Expect(err).To(HaveOccurred())
	g.Expect(ok).To(Equal(false))

	ok, _, err = validateSriovOperatorConfig(config, "UPDATE")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(Equal(true))

	ok, w, err = validateSriovOperatorConfig(config, "UPDATE")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(Equal(true))
	g.Expect(w[0]).To(ContainSubstring("Node draining is disabled"))

	ok, _, err = validateSriovOperatorConfig(config, "CREATE")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(Equal(true))
}

func TestValidateSriovNetworkNodePolicyWithDefaultPolicy(t *testing.T) {
	var err error
	var ok bool
	policy := &SriovNetworkNodePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "openshift-sriov-network-operator",
		},
		Spec: SriovNetworkNodePolicySpec{
			NicSelector:  SriovNetworkNicSelector{},
			NodeSelector: map[string]string{},
			NumVfs:       1,
			ResourceName: "p0",
		},
	}
	os.Setenv("NAMESPACE", "openshift-sriov-network-operator")
	g := NewGomegaWithT(t)
	ok, _, err = validateSriovNetworkNodePolicy(policy, "DELETE")
	g.Expect(err).To(HaveOccurred())
	g.Expect(ok).To(Equal(false))

	ok, _, err = validateSriovNetworkNodePolicy(policy, "UPDATE")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(Equal(true))

	ok, _, err = validateSriovNetworkNodePolicy(policy, "CREATE")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(Equal(true))
}

func TestValidatePolicyForNodeStateWithValidPolicy(t *testing.T) {
	state := newNodeState()
	policy := &SriovNetworkNodePolicy{
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				PfNames:     []string{"ens803f0"},
				RootDevices: []string{"0000:86:00.0"},
				Vendor:      "8086",
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       63,
			Priority:     99,
			ResourceName: "p0",
		},
	}
	g := NewGomegaWithT(t)
	ok, err := validatePolicyForNodeState(policy, state)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(Equal(true))
}

func TestValidatePolicyForNodeStateWithInvalidNumVfsPolicy(t *testing.T) {
	state := newNodeState()
	policy := &SriovNetworkNodePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p1",
		},
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				PfNames:     []string{"ens803f0"},
				RootDevices: []string{"0000:86:00.0"},
				Vendor:      "8086",
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       65,
			Priority:     99,
			ResourceName: "p0",
		},
	}
	g := NewGomegaWithT(t)
	ok, err := validatePolicyForNodeState(policy, state)
	g.Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("numVfs(%d) in CR %s exceed the maximum allowed value(%d)", policy.Spec.NumVfs, policy.GetName(), state.Status.Interfaces[0].TotalVfs))))
	g.Expect(ok).To(Equal(false))
}

func TestValidatePolicyForNodePolicyWithOverlappedVfRange(t *testing.T) {
	appliedPolicy := newNodePolicy()
	policy := &SriovNetworkNodePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p0",
		},
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				PfNames: []string{"ens803f1#2-2"},
				Vendor:  "8086",
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       63,
			Priority:     99,
			ResourceName: "p0",
		},
	}
	g := NewGomegaWithT(t)
	ok, err := validatePolicyForNodePolicy(policy, appliedPolicy)
	g.Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("VF index range in %s is overlapped with existing policy %s", policy.Spec.NicSelector.PfNames[0], appliedPolicy.ObjectMeta.Name))))
	g.Expect(ok).To(Equal(false))
}

func TestValidatePolicyForNodeStateWithUpdatedExistingVfRange(t *testing.T) {
	appliedPolicy := newNodePolicy()
	policy := &SriovNetworkNodePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p1",
		},
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				PfNames:     []string{"ens803f1#1-2"},
				RootDevices: []string{"0000:86:00.1"},
				Vendor:      "8086",
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       63,
			Priority:     99,
			ResourceName: "p1",
		},
	}
	g := NewGomegaWithT(t)
	ok, err := validatePolicyForNodePolicy(policy, appliedPolicy)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(Equal(true))
}

func TestStaticValidateSriovNetworkNodePolicyWithValidVendorDevice(t *testing.T) {
	policy := &SriovNetworkNodePolicy{
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				Vendor:   "8086",
				DeviceID: "158b",
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       63,
			Priority:     99,
			ResourceName: "p0",
		},
	}
	g := NewGomegaWithT(t)
	ok, err := staticValidateSriovNetworkNodePolicy(policy)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(Equal(true))
}

func TestStaticValidateSriovNetworkNodePolicyWithInvalidVendor(t *testing.T) {
	policy := &SriovNetworkNodePolicy{
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				Vendor: "8087",
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       63,
			Priority:     99,
			ResourceName: "p0",
		},
	}
	g := NewGomegaWithT(t)
	ok, err := staticValidateSriovNetworkNodePolicy(policy)
	g.Expect(err).To(MatchError(ContainSubstring("vendor %s is not supported", policy.Spec.NicSelector.Vendor)))
	g.Expect(ok).To(Equal(false))
}

func TestStaticValidateSriovNetworkNodePolicyWithInvalidDevice(t *testing.T) {
	policy := &SriovNetworkNodePolicy{
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				DeviceID: "1234",
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       63,
			Priority:     99,
			ResourceName: "p0",
		},
	}
	g := NewGomegaWithT(t)
	ok, err := staticValidateSriovNetworkNodePolicy(policy)
	g.Expect(err).To(MatchError(ContainSubstring("device %s is not supported", policy.Spec.NicSelector.DeviceID)))
	g.Expect(ok).To(Equal(false))
}

func TestStaticValidateSriovNetworkNodePolicyWithInvalidVendorDevice(t *testing.T) {
	policy := &SriovNetworkNodePolicy{
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				Vendor:   "8086",
				DeviceID: "1015",
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       63,
			Priority:     99,
			ResourceName: "p0",
		},
	}
	g := NewGomegaWithT(t)
	ok, err := staticValidateSriovNetworkNodePolicy(policy)
	g.Expect(err).To(MatchError(ContainSubstring("vendor/device %s/%s is not supported", policy.Spec.NicSelector.Vendor, policy.Spec.NicSelector.DeviceID)))
	g.Expect(ok).To(Equal(false))
}

func TestStaticValidateSriovNetworkNodePolicyWithConflictIsRdmaAndDeviceType(t *testing.T) {
	policy := &SriovNetworkNodePolicy{
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "vfio-pci",
			NicSelector: SriovNetworkNicSelector{
				Vendor:   "8086",
				DeviceID: "158b",
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       1,
			Priority:     99,
			ResourceName: "p0",
			IsRdma:       true,
		},
	}
	g := NewGomegaWithT(t)
	ok, err := staticValidateSriovNetworkNodePolicy(policy)
	g.Expect(err).To(MatchError(ContainSubstring("'deviceType: vfio-pci' conflicts with 'isRdma: true'")))
	g.Expect(ok).To(Equal(false))
}

func TestValidatePolicyForNodeStateWithInvalidDevice(t *testing.T) {
	state := newNodeState()
	policy := &SriovNetworkNodePolicy{
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				DeviceID: "1015",
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       63,
			Priority:     99,
			ResourceName: "p0",
		},
	}
	g := NewGomegaWithT(t)
	var testEnv *envtest.Environment
	testEnv = &envtest.Environment{}

	cfg, err := testEnv.Start()
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(cfg).ToNot(BeNil())
	kubeclient = kubernetes.NewForConfigOrDie(cfg)
	ok, err := validatePolicyForNodeState(policy, state)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(Equal(true))
}

func TestValidatePolicyForNodeStateWithInvalidPfName(t *testing.T) {
	interfaceSelected = false
	state := newNodeState()
	policy := &SriovNetworkNodePolicy{
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				PfNames: []string{"ens803f2"},
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       63,
			Priority:     99,
			ResourceName: "p0",
		},
	}
	g := NewGomegaWithT(t)
	ok, err := validatePolicyForNodeState(policy, state)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(Equal(true))
	g.Expect(interfaceSelected).To(Equal(false))
}

func TestValidatePolicyForNodeStateWithValidPfName(t *testing.T) {
	interfaceSelected = false
	state := newNodeState()
	policy := &SriovNetworkNodePolicy{
		Spec: SriovNetworkNodePolicySpec{
			DeviceType: "netdevice",
			NicSelector: SriovNetworkNicSelector{
				PfNames: []string{"ens803f1"},
			},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			NumVfs:       63,
			Priority:     99,
			ResourceName: "p0",
		},
	}
	g := NewGomegaWithT(t)
	ok, err := validatePolicyForNodeState(policy, state)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(Equal(true))
	g.Expect(interfaceSelected).To(Equal(true))
}

func TestStaticValidateSriovNetworkNodePolicyWithInvalidNicSelector(t *testing.T) {
	policy := &SriovNetworkNodePolicy{
		Spec: SriovNetworkNodePolicySpec{
			DeviceType:  "netdevice",
			NicSelector: SriovNetworkNicSelector{},
			NodeSelector: map[string]string{
				"feature.node.kubernetes.io/network-sriov.capable": "true",
			},
			ResourceName: "p0",
		},
	}
	g := NewGomegaWithT(t)
	ok, err := staticValidateSriovNetworkNodePolicy(policy)
	g.Expect(err).To(HaveOccurred())
	g.Expect(ok).To(Equal(false))
}
