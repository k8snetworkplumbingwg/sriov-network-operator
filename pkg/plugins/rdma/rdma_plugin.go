package main

import (
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	"github.com/vishvananda/netlink"
	"io/ioutil"
	"os"
	"os/exec"
	"regexp"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
)

type RDMAPlugin struct {
	PluginName     string
	SpecVersion    string
	DesireState    *sriovnetworkv1.SriovNetworkNodeState
	updateRequired bool
}

const (
	chroot       = "/host"
	modprobeConf = chroot + "/etc/modprobe.d/ib_core.conf"
	optionName   = "options ib_core netns_mode="
)

var (
	Plugin RDMAPlugin
)

// Initialize our plugin and set up initial values
func init() {
	Plugin = RDMAPlugin{
		PluginName:  "rdma_plugin",
		SpecVersion: "1.0",
	}
}

// Name returns the name of the plugin
func (p *RDMAPlugin) Name() string {
	return p.PluginName
}

// Spec returns the version of the spec expected by the plugin
func (p *RDMAPlugin) Spec() string {
	return p.SpecVersion
}

// OnNodeStateAdd Invoked when SriovNetworkNodeState CR is created, return if need dain and/or reboot node
func (p *RDMAPlugin) OnNodeStateAdd(state *sriovnetworkv1.SriovNetworkNodeState) (needDrain bool, needReboot bool, err error) {
	glog.Info("rdma-plugin OnNodeStateAdd()")
	return p.OnNodeStateChange(nil, state)
}

// OnNodeStateChange Invoked when SriovNetworkNodeState CR is updated, return if need dain and/or reboot node
func (p *RDMAPlugin) OnNodeStateChange(_, new *sriovnetworkv1.SriovNetworkNodeState) (needDrain bool, needReboot bool, err error) {
	glog.Info("rdma-plugin OnNodeStateChange()")
	p.DesireState = new
	err = p.checkUpdateRequired(new.Spec.NodeSettings.RDMAExclusiveMode)
	if err != nil {
		glog.Errorf("rdmaMode OnNodeStateChange(): failed : %v", err)
		return
	}

	return p.updateRequired, p.updateRequired, nil
}

// Apply config change
func (p *RDMAPlugin) Apply() error {
	glog.Info("rdma-plugin Apply()")
	if !p.updateRequired {
		return nil
	}
	var f func() error
	if p.DesireState.Spec.NodeSettings.RDMAExclusiveMode {
		glog.Info("rdma-plugin Apply(): enable exclusive mode")
		f = p.enable
	} else {
		glog.Info("rdma-plugin Apply(): disable exclusive mode")
		f = p.disable
	}
	err := f()
	if err != nil {
		return err
	}
	return nil
}

func (p *RDMAPlugin) checkUpdateRequired(shouldEnable bool) error {
	glog.Infof("rdma-plugin checkUpdateRequired(), shouldEnable: %t", shouldEnable)
	state, err := p.getCurrentState()
	if err != nil {
		return err
	}
	if shouldEnable {
		p.updateRequired = !(state.lineFound && state.runtimeModeExclusive)
		return nil
	}
	p.updateRequired = state.lineFound || state.runtimeModeExclusive
	return nil
}

type currentState struct {
	lineFound            bool
	runtimeModeExclusive bool
}

func (p *RDMAPlugin) getCurrentState() (currentState, error) {
	state := currentState{}
	var fileExist bool
	data, err := ioutil.ReadFile(modprobeConf)
	if err == nil {
		fileExist = true
	} else {
		if !os.IsNotExist(err) {
			return currentState{}, err
		}
		fileExist = false
	}
	if fileExist {
		found, err := regexp.Match(optionName+"0", data)
		if err != nil {
			return currentState{}, err
		}
		state.lineFound = found
	}
	state.runtimeModeExclusive, err = p.isRDMARuntimeModeExclusive()
	if err != nil {
		return currentState{}, err
	}
	glog.Infof("rdma-plugin getCurrentState(): %+v", state)
	return state, nil
}

func (p *RDMAPlugin) isRDMARuntimeModeExclusive() (bool, error) {
	mode, err := netlink.RdmaSystemGetNetnsMode()
	if err != nil {
		return false, err
	}
	return mode == "exclusive", nil
}

func (p *RDMAPlugin) enable() error {
	if err := p.enableInModprobeConf(); err != nil {
		return err
	}
	return p.updateInitRAMFS()
}

func (p *RDMAPlugin) disable() error {
	if err := p.disableInModprobeConf(); err != nil {
		return err
	}
	return p.updateInitRAMFS()
}

func (p *RDMAPlugin) enableInModprobeConf() error {
	var fileExist bool
	data, err := ioutil.ReadFile(modprobeConf)
	if err == nil {
		fileExist = true
	} else {
		if !os.IsNotExist(err) {
			return err
		}
		fileExist = false
	}
	if !fileExist {
		return ioutil.WriteFile(modprobeConf, []byte(optionName+"0\n"), 0644)
	}
	optionRE, err := regexp.Compile(optionName + "\\d")
	if err != nil {
		return err
	}
	if optionRE.Match(data) {
		return ioutil.WriteFile(modprobeConf, optionRE.ReplaceAll(data, []byte(optionName+"0")), 0644)
	}
	return ioutil.WriteFile(modprobeConf, append(data, []byte(optionName+"0\n")...), 0644)
}

func (p *RDMAPlugin) disableInModprobeConf() error {
	data, err := ioutil.ReadFile(modprobeConf)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	optionRE, err := regexp.Compile(optionName + "0")
	if err != nil {
		return err
	}
	if optionRE.Match(data) {
		return ioutil.WriteFile(modprobeConf, optionRE.ReplaceAll(data, []byte{}), 0644)
	}
	return nil
}

func (p *RDMAPlugin) updateInitRAMFS() error {
	exit, err := utils.Chroot(chroot)
	if err != nil {
		return err
	}
	defer exit()

	// TODO FIXME ubuntu specific code
	cmd := exec.Command("update-initramfs", "-k", "all", "-u")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
