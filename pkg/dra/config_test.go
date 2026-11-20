package dra

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
)

var _ = Describe("PolicyResourceDrivers", func() {
	DescribeTable("ignores the deprecated default policy while validating active policies", func(draEnabled bool) {
		validator := NewPolicyResourceValidator(draEnabled)
		deprecated := &sriovnetworkv1.SriovNetworkNodePolicy{ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultPolicyName}}
		Expect(validator.Add(deprecated)).To(Succeed())
		deprecated.Spec = sriovnetworkv1.SriovNetworkNodePolicySpec{
			ResourceName: "SHARED", DeviceType: consts.DeviceTypeVfioPci,
			NicSelector: sriovnetworkv1.SriovNetworkNicSelector{PfNames: []string{"ens1f0#2-5"}, NetFilter: "aws/NetworkID:net-1"},
		}
		Expect(validator.Add(deprecated)).To(Succeed())
		Expect(validator.drivers).To(BeEmpty())
		Expect(validator.owners).To(BeEmpty())
		Expect(validator.classNames).To(BeEmpty())

		active := &sriovnetworkv1.SriovNetworkNodePolicy{ObjectMeta: metav1.ObjectMeta{Name: "active"}}
		Expect(validator.Add(active)).To(MatchError(ContainSubstring("resource name must not be empty")))
		active.Spec.ResourceName = "shared"
		Expect(validator.Add(active)).To(Succeed())
		colliding := active.DeepCopy()
		colliding.Name = "colliding"
		colliding.Spec.ResourceName = "SHARED"
		Expect(validator.Add(colliding)).To(MatchError(ContainSubstring("normalizes to the same DRA device class name")))

		drivers, err := PolicyResourceDrivers([]sriovnetworkv1.SriovNetworkNodePolicy{*deprecated, *active})
		Expect(err).NotTo(HaveOccurred())
		Expect(drivers).To(Equal(map[string]string{"shared": ""}))
	},
		Entry("DRA", true),
		Entry("device plugin", false),
	)

	DescribeTable("validates PF selectors for the selected provider",
		func(draEnabled bool, pfName string, wantError bool) {
			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "policy"},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "nic", NumVfs: 8,
					NicSelector: sriovnetworkv1.SriovNetworkNicSelector{PfNames: []string{pfName}},
				},
			}
			validator := NewPolicyResourceValidator(draEnabled)
			err := validator.Add(policy)
			if wantError {
				Expect(err).To(MatchError(ContainSubstring("VF range")))
				Expect(validator.drivers).To(BeEmpty())
				policy.Spec.NicSelector.PfNames = []string{"ens1f0"}
				Expect(validator.Add(policy)).To(Succeed())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("DRA whole PF", true, "ens1f0", false),
		Entry("DRA ranged PF", true, "ens1f0#2-5", true),
		Entry("DRA ranged alternative PF name", true, "enp3s0np0#2-5", true),
		Entry("device plugin ranged PF", false, "ens1f0#2-5", false),
	)

	DescribeTable("validates netFilter for the selected provider",
		func(draEnabled bool, netFilter string, wantError bool) {
			policy := &sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "policy"},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "nic", NumVfs: 8,
					NicSelector: sriovnetworkv1.SriovNetworkNicSelector{NetFilter: netFilter},
				},
			}
			validator := NewPolicyResourceValidator(draEnabled)
			err := validator.Add(policy)
			if wantError {
				Expect(err).To(MatchError(ContainSubstring("netFilter selector is not supported in DRA mode")))
				Expect(validator.drivers).To(BeEmpty())
				Expect(validator.owners).To(BeEmpty())
				Expect(validator.classNames).To(BeEmpty())
				policy.Spec.NicSelector = sriovnetworkv1.SriovNetworkNicSelector{PfNames: []string{"ens1f0"}}
				Expect(validator.Add(policy)).To(Succeed())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("DRA rejects netFilter", true, "aws/NetworkID:net-1", true),
		Entry("DRA accepts an empty netFilter", true, "", false),
		Entry("device plugin accepts netFilter", false, "aws/NetworkID:net-1", false),
	)

	It("allows Intel and Mellanox netdevices to share a resource", func() {
		policies := []sriovnetworkv1.SriovNetworkNodePolicy{}
		for _, vendor := range []string{"8086", "15b3"} {
			policies = append(policies, sriovnetworkv1.SriovNetworkNodePolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "vendor-" + vendor},
				Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{
					ResourceName: "shared",
					DeviceType:   "netdevice",
					NicSelector:  sriovnetworkv1.SriovNetworkNicSelector{Vendor: vendor},
				},
			})
		}

		drivers, err := PolicyResourceDrivers(policies)
		Expect(err).NotTo(HaveOccurred())
		Expect(drivers).To(Equal(map[string]string{"shared": ""}))
	})

	DescribeTable("collects compatible allocation drivers for a shared resource",
		func(deviceTypes []string, wantDriver string, wantError bool) {
			policies := []sriovnetworkv1.SriovNetworkNodePolicy{}
			for i, deviceType := range deviceTypes {
				name := fmt.Sprintf("worker-%d", i)
				policies = append(policies, sriovnetworkv1.SriovNetworkNodePolicy{
					ObjectMeta: metav1.ObjectMeta{Name: name},
					Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "shared", DeviceType: deviceType,
						NodeSelector: map[string]string{"kubernetes.io/hostname": name}},
				})
			}
			drivers, err := PolicyResourceDrivers(policies)
			if wantError {
				Expect(err).To(MatchError(ContainSubstring("incompatible deviceType")))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(drivers).To(Equal(map[string]string{"shared": wantDriver}))
		},
		Entry("VFIO", []string{"vfio-pci"}, "vfio-pci", false),
		Entry("shared VFIO", []string{"vfio-pci", "vfio-pci"}, "vfio-pci", false),
		Entry("default and explicit netdevice", []string{"", "netdevice"}, "", false),
		Entry("mixed", []string{"netdevice", "vfio-pci"}, "", true),
		Entry("mixed reversed", []string{"vfio-pci", "netdevice"}, "", true),
		Entry("mixed with default", []string{"vfio-pci", ""}, "", true),
	)
})
