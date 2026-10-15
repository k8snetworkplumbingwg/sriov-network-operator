package baremetal

import (
	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	plugins "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins"
)

const systemdChainName = "baremetal-systemd"

// pluginChain runs VendorPlugins in order. Systemd mode uses it so one plugin call
// applies host config before the generic NIC plugin.
type pluginChain struct {
	name    string
	plugins []plugins.VendorPlugin
}

// newPluginChain returns a VendorPlugin that runs plugins in the given order.
func newPluginChain(name string, pluginsToRun ...plugins.VendorPlugin) plugins.VendorPlugin {
	return &pluginChain{
		name:    name,
		plugins: pluginsToRun,
	}
}

// Name returns the chain name.
func (c *pluginChain) Name() string {
	return c.name
}

// OnNodeStateChange runs each plugin and ORs drain and reboot requirements.
func (c *pluginChain) OnNodeStateChange(state *sriovnetworkv1.SriovNetworkNodeState) (bool, bool, error) {
	needDrain := false
	needReboot := false
	for _, p := range c.plugins {
		drain, reboot, err := p.OnNodeStateChange(state)
		if err != nil {
			return false, false, err
		}
		needDrain = needDrain || drain
		needReboot = needReboot || reboot
	}
	return needDrain, needReboot, nil
}

// Apply runs each plugin in order. Host config is first so kernel modules exist before NIC configuration.
func (c *pluginChain) Apply() error {
	for _, p := range c.plugins {
		if err := p.Apply(); err != nil {
			return err
		}
	}
	return nil
}

// CheckStatusChanges returns true when any plugin reports drift.
func (c *pluginChain) CheckStatusChanges(state *sriovnetworkv1.SriovNetworkNodeState) (bool, error) {
	for _, p := range c.plugins {
		changed, err := p.CheckStatusChanges(state)
		if err != nil || changed {
			return changed, err
		}
	}
	return false, nil
}
