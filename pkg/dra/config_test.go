package dra

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
)

func TestPolicyResourceDrivers(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deviceTypes []string
		wantDriver  string
		wantError   bool
	}{
		{name: "VFIO", deviceTypes: []string{"vfio-pci"}, wantDriver: "vfio-pci"},
		{name: "shared VFIO", deviceTypes: []string{"vfio-pci", "vfio-pci"}, wantDriver: "vfio-pci"},
		{name: "default and explicit netdevice", deviceTypes: []string{"", "netdevice"}},
		{name: "mixed", deviceTypes: []string{"netdevice", "vfio-pci"}, wantError: true},
		{name: "mixed reversed", deviceTypes: []string{"vfio-pci", "netdevice"}, wantError: true},
		{name: "mixed with default", deviceTypes: []string{"vfio-pci", ""}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policies := []sriovnetworkv1.SriovNetworkNodePolicy{
				{ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultPolicyName}, Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "ignored"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "empty"}},
			}
			for i, deviceType := range tc.deviceTypes {
				name := []string{"worker-a", "worker-b"}[i]
				policies = append(policies, sriovnetworkv1.SriovNetworkNodePolicy{
					ObjectMeta: metav1.ObjectMeta{Name: name},
					Spec: sriovnetworkv1.SriovNetworkNodePolicySpec{ResourceName: "shared", DeviceType: deviceType,
						NodeSelector: map[string]string{"kubernetes.io/hostname": name}},
				})
			}
			drivers, err := PolicyResourceDrivers(policies)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "incompatible deviceType") {
					t.Fatalf("expected conflicting device types, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(drivers) != 1 || drivers["shared"] != tc.wantDriver {
				t.Fatalf("drivers = %v, want shared: %q", drivers, tc.wantDriver)
			}
		})
	}
}
