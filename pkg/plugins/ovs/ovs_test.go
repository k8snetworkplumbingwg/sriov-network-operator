package ovs

import (
	"errors"
	"os"
	"testing"

	"github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	mockhelper "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper/mock"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
)

func TestOVSDropin(t *testing.T) {
	state := &sriovnetworkv1.SriovNetworkNodeState{}
	state.Spec.Interfaces = []sriovnetworkv1.Interface{{EswitchMode: sriovnetworkv1.ESwithModeSwitchDev}}
	desired := &types.Service{Path: "/etc/systemd/system/ovs-vswitchd.service.d/10-hw-offload.conf", Content: "[Service]\nExecStartPre=/desired\n"}
	cases := map[string]struct {
		existing *types.Service
		readErr  error
		update   bool
	}{
		"already configured": {existing: desired},
		"stale command":      {existing: &types.Service{Content: desired.Content + "ExecStartPre=/obsolete\n"}, update: true},
		"missing":            {readErr: os.ErrNotExist, update: true},
		"read error":         {readErr: os.ErrPermission},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			host := mockhelper.NewMockHostHelpersInterface(gomock.NewController(t))
			p := New(host)
			host.EXPECT().IsServiceExist(ovsServicePath).Return(true, nil)
			host.EXPECT().ReadOvsServiceInjectionManifestFile(dropinManifest, state.Spec.System.OvsConfig).Return(desired, nil)
			host.EXPECT().ReadService(desired.Path).Return(tc.existing, tc.readErr)
			drain, reboot, err := p.OnNodeStateChange(state)
			if tc.readErr != nil && !errors.Is(tc.readErr, os.ErrNotExist) {
				g.Expect(err).To(gomega.MatchError(tc.readErr))
			} else {
				g.Expect(err).NotTo(gomega.HaveOccurred())
			}
			g.Expect(drain).To(gomega.Equal(tc.update))
			g.Expect(reboot).To(gomega.Equal(tc.update))
			if tc.update {
				host.EXPECT().WriteServiceDropin(desired)
			}
			g.Expect(p.Apply()).To(gomega.Succeed())

			// A later generation without switchdev must discard any pending write.
			drain, reboot, err = p.OnNodeStateChange(&sriovnetworkv1.SriovNetworkNodeState{})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(drain || reboot).To(gomega.BeFalse())
			g.Expect(p.Apply()).To(gomega.Succeed())
		})
	}
	t.Run("OVS is absent", func(t *testing.T) {
		g := gomega.NewWithT(t)
		host := mockhelper.NewMockHostHelpersInterface(gomock.NewController(t))
		host.EXPECT().IsServiceExist(ovsServicePath).Return(false, nil)
		p := New(host)
		drain, reboot, err := p.OnNodeStateChange(state)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(drain || reboot).To(gomega.BeFalse())
		g.Expect(p.Apply()).To(gomega.Succeed())
	})
}
