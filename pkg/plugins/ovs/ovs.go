package ovs

import (
	"errors"
	"fmt"
	"os"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
)

const (
	ovsServicePath = "/usr/lib/systemd/system/ovs-vswitchd.service"
	dropinManifest = "bindata/manifests/switchdev-config/ovs-units/ovs-vswitchd.service.yaml"
)

// Plugin manages the host ovs-vswitchd drop-in for switchdev interfaces.
type Plugin struct {
	services types.ServiceInterface
	pending  *types.Service
}

func New(services types.ServiceInterface) *Plugin {
	return &Plugin{services: services}
}

func (*Plugin) Name() string { return "ovs" }

func (p *Plugin) OnNodeStateChange(state *sriovnetworkv1.SriovNetworkNodeState) (bool, bool, error) {
	p.pending = nil
	if !sriovnetworkv1.IsSwitchdevModeSpec(state.Spec) {
		return false, false, nil
	}
	installed, err := p.services.IsServiceExist(ovsServicePath)
	if err != nil || !installed {
		return false, false, err
	}
	desired, err := p.services.ReadOvsServiceInjectionManifestFile(dropinManifest, state.Spec.System.OvsConfig)
	if err != nil {
		return false, false, fmt.Errorf("render OVS drop-in: %w", err)
	}
	existing, err := p.services.ReadService(desired.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, false, fmt.Errorf("read OVS drop-in: %w", err)
	}
	if err == nil && existing.Content == desired.Content {
		return false, false, nil
	}
	p.pending = desired
	return true, true, nil
}

func (*Plugin) CheckStatusChanges(*sriovnetworkv1.SriovNetworkNodeState) (bool, error) {
	return false, nil
}

func (p *Plugin) Apply() error {
	if p.pending == nil {
		return nil
	}
	return p.services.WriteServiceDropin(p.pending)
}
