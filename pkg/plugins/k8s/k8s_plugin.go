package k8s

import (
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper"
	hostTypes "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	plugins "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

var PluginName = "k8s"

type K8sPlugin struct {
	PluginName  string
	SpecVersion string
	hostHelper  helper.HostHelpersInterface

	sriovService            *hostTypes.Service
	sriovPostNetworkService *hostTypes.Service

	updateTarget *k8sUpdateTarget
}
type updateTargetReq struct {
	update bool
	reboot bool
}

// set need update flag for updateTargetReq
func (u *updateTargetReq) SetNeedUpdate() {
	u.update = true
}

// set need update and reboot flags for updateTargetReq
func (u *updateTargetReq) SetNeedReboot() {
	u.update = true
	u.reboot = true
}

// returns state of the update flag
func (u *updateTargetReq) NeedUpdate() bool {
	return u.update
}

// returns state of the reboot flag
func (u *updateTargetReq) NeedReboot() bool {
	return u.reboot
}

type k8sUpdateTarget struct {
	sriovScript            updateTargetReq
	sriovPostNetworkScript updateTargetReq
}

func (u *k8sUpdateTarget) String() string {
	var updateList []string
	if u.sriovScript.NeedReboot() {
		updateList = append(updateList, "sriov-config.service")
	}
	if u.sriovPostNetworkScript.NeedReboot() {
		updateList = append(updateList, "sriov-config-post-network.service")
	}
	return strings.Join(updateList, ",")
}

func (u *k8sUpdateTarget) needReboot() bool {
	return u.sriovScript.NeedReboot() || u.sriovPostNetworkScript.NeedReboot()
}

func (u *k8sUpdateTarget) reset() {
	u.sriovScript = updateTargetReq{}
	u.sriovPostNetworkScript = updateTargetReq{}
}

const (
	bindataManifestPath      = "bindata/manifests/"
	sriovUnits               = bindataManifestPath + "sriov-config-service/kubernetes/"
	sriovUnitFile            = sriovUnits + "sriov-config-service.yaml"
	sriovPostNetworkUnitFile = sriovUnits + "sriov-config-post-network-service.yaml"
)

// Initialize our plugin and set up initial values
func NewK8sPlugin(helper helper.HostHelpersInterface) plugins.VendorPlugin {
	k8sPluging := &K8sPlugin{
		PluginName:   PluginName,
		hostHelper:   helper,
		updateTarget: &k8sUpdateTarget{},
	}

	return k8sPluging
}

// Name returns the name of the plugin
func (p *K8sPlugin) Name() string {
	return p.PluginName
}

// OnNodeStateChange Invoked when SriovNetworkNodeState CR is created or updated, return if need drain and/or reboot node
func (p *K8sPlugin) OnNodeStateChange(*sriovnetworkv1.SriovNetworkNodeState) (bool, bool, error) {
	p.updateTarget.reset()
	if !vars.UsingSystemdMode {
		return false, false, nil
	}
	if err := p.readSriovServiceManifest(); err != nil {
		return false, false, err
	}
	if err := p.readSriovPostNetworkServiceManifest(); err != nil {
		return false, false, err
	}
	if err := p.sriovServicesStateUpdate(); err != nil {
		return false, false, err
	}
	needReboot := p.updateTarget.needReboot()
	if needReboot {
		log.Log.Info("k8s plugin OnNodeStateChange(): needReboot to update", "target", p.updateTarget)
	}
	return needReboot, needReboot, nil
}

// TODO: implement - https://github.com/k8snetworkplumbingwg/sriov-network-operator/issues/630
// OnNodeStatusChange verify whether SriovNetworkNodeState CR status present changes on configured VFs.
func (p *K8sPlugin) CheckStatusChanges(*sriovnetworkv1.SriovNetworkNodeState) (bool, error) {
	return false, nil
}

// Apply config change
func (p *K8sPlugin) Apply() error {
	log.Log.Info("k8s plugin Apply()")
	return p.updateSriovServices()
}

func (p *K8sPlugin) readSriovServiceManifest() error {
	sriovService, err := p.hostHelper.ReadServiceManifestFile(sriovUnitFile)
	if err != nil {
		return err
	}
	p.sriovService = sriovService
	return nil
}

func (p *K8sPlugin) readSriovPostNetworkServiceManifest() error {
	sriovService, err := p.hostHelper.ReadServiceManifestFile(sriovPostNetworkUnitFile)
	if err != nil {
		return err
	}
	p.sriovPostNetworkService = sriovService
	return nil
}

func (p *K8sPlugin) sriovServicesStateUpdate() error {
	for _, s := range []struct {
		srv    *hostTypes.Service
		update *updateTargetReq
	}{
		{srv: p.sriovService, update: &p.updateTarget.sriovScript},
		{srv: p.sriovPostNetworkService, update: &p.updateTarget.sriovPostNetworkScript},
	} {
		isServiceEnabled, err := p.hostHelper.IsServiceEnabled(s.srv.Path)
		if err != nil {
			return err
		}
		// create and enable the service if it doesn't exist or is not enabled
		if !isServiceEnabled {
			s.update.SetNeedReboot()
		} else {
			if p.isSystemDServiceNeedUpdate(s.srv) {
				s.update.SetNeedReboot()
			}
		}
	}
	return nil
}

func (p *K8sPlugin) updateSriovServices() error {
	for _, s := range []struct {
		srv    *hostTypes.Service
		update *updateTargetReq
	}{
		{srv: p.sriovService, update: &p.updateTarget.sriovScript},
		{srv: p.sriovPostNetworkService, update: &p.updateTarget.sriovPostNetworkScript},
	} {
		if s.update.NeedUpdate() {
			err := p.hostHelper.EnableService(s.srv)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *K8sPlugin) isSystemDServiceNeedUpdate(serviceObj *hostTypes.Service) bool {
	systemService, err := p.hostHelper.ReadService(serviceObj.Path)
	if err != nil {
		log.Log.Error(err, "k8s plugin isSystemDServiceNeedUpdate(): failed to read service file, ignoring",
			"path", serviceObj.Path)
		return false
	}
	if systemService != nil {
		needChange, err := p.hostHelper.CompareServices(systemService, serviceObj)
		if err != nil {
			log.Log.Error(err, "k8s plugin isSystemDServiceNeedUpdate(): failed to compare service, ignoring")
			return false
		}
		return needChange
	}
	return false
}
