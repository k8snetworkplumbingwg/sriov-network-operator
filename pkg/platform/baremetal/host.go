package baremetal

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper"
	hostTypes "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	plugins "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

const hostPluginName = "host"

const (
	vfioDriver = iota
	virtioVdpaDriverID
	vhostVdpaDriverID
)

const (
	vfioPciDriver    = "vfio_pci"
	virtioVdpaDriver = "virtio_vdpa"
	vhostVdpaDriver  = "vhost_vdpa"
)

const (
	bindataManifestPath      = "bindata/manifests/"
	switchdevManifestPath    = bindataManifestPath + "switchdev-config/"
	sriovUnits               = bindataManifestPath + "sriov-config-service/kubernetes/"
	sriovUnitFile            = sriovUnits + "sriov-config-service.yaml"
	sriovPostNetworkUnitFile = sriovUnits + "sriov-config-post-network-service.yaml"
	ovsUnitFile              = switchdevManifestPath + "ovs-units/ovs-vswitchd.service.yaml"
	// ovsMainServicePath is the packaged OVS unit. It stays under /usr/lib because that
	// tree is readable on RHCOS; the operator drop-in is written under /etc/systemd.
	ovsMainServicePath = "/usr/lib/systemd/system/ovs-vswitchd.service"
	// legacyOvsDropinPath is the pre-migration drop-in location used on vanilla Kubernetes.
	legacyOvsDropinPath = "/usr/lib/systemd/system/ovs-vswitchd.service.d/10-hw-offload.conf"
)

// needDriver reports whether a kernel module is required for the node spec.
type needDriver func(state *sriovnetworkv1.SriovNetworkNodeState, driverState *driverState) bool

// driverState tracks one kernel module the host config may load.
type driverState struct {
	DriverName     string
	DeviceType     string
	VdpaType       string
	NeedDriverFunc needDriver
	DriverLoaded   bool
}

// kargStateMap is the desired on/off state of managed kernel command-line arguments.
type kargStateMap map[string]bool

// hostConfig applies baremetal host OS configuration: kernel cmdline, kernel modules,
// RDMA modprobe policy, the OVS hardware-offload systemd drop-in, and (on Kubernetes
// systemd mode) the sriov-config units. It is not loaded on virtual platforms.
type hostConfig struct {
	pluginName     string
	hostHelper     helper.HostHelpersInterface
	desireState    *sriovnetworkv1.SriovNetworkNodeState
	driverStateMap map[uint]*driverState
	desiredKargs   kargStateMap

	openVSwitchService      *hostTypes.Service
	sriovService            *hostTypes.Service
	sriovPostNetworkService *hostTypes.Service
	updateTarget            *hostUpdateTarget
}

type updateTargetReq struct {
	update bool
	reboot bool
}

// SetNeedUpdate marks the target for an in-place file update without reboot.
func (u *updateTargetReq) SetNeedUpdate() {
	u.update = true
}

// SetNeedReboot marks the target for an update that requires a node reboot.
func (u *updateTargetReq) SetNeedReboot() {
	u.update = true
	u.reboot = true
}

// NeedUpdate reports whether the target file must be written.
func (u *updateTargetReq) NeedUpdate() bool {
	return u.update
}

// NeedReboot reports whether the target change requires a reboot.
func (u *updateTargetReq) NeedReboot() bool {
	return u.reboot
}

type hostUpdateTarget struct {
	sriovScript            updateTargetReq
	sriovPostNetworkScript updateTargetReq
	openVSwitch            updateTargetReq
}

func (u *hostUpdateTarget) String() string {
	var updateList []string
	if u.sriovScript.NeedReboot() {
		updateList = append(updateList, "sriov-config.service")
	}
	if u.sriovPostNetworkScript.NeedReboot() {
		updateList = append(updateList, "sriov-config-post-network.service")
	}
	if u.openVSwitch.NeedReboot() {
		updateList = append(updateList, "ovs-vswitchd.service")
	}
	return strings.Join(updateList, ",")
}

// needReboot reports whether any systemd file change requires a reboot.
func (u *hostUpdateTarget) needReboot() bool {
	return u.sriovScript.NeedReboot() || u.sriovPostNetworkScript.NeedReboot() || u.openVSwitch.NeedReboot()
}

// reset clears update flags from the previous reconcile.
func (u *hostUpdateTarget) reset() {
	u.sriovScript = updateTargetReq{}
	u.sriovPostNetworkScript = updateTargetReq{}
	u.openVSwitch = updateTargetReq{}
}

// NewHostConfig builds the baremetal host-config plugin.
// Kernel arguments that are already set (intel_iommu, iommu=pt, pci=realloc) are preserved.
func NewHostConfig(helpers helper.HostHelpersInterface) (plugins.VendorPlugin, error) {
	return newHostConfig(helpers)
}

// newHostConfig builds the concrete host config plugin.
func newHostConfig(helpers helper.HostHelpersInterface) (*hostConfig, error) {
	driverStateMap := map[uint]*driverState{
		vfioDriver: {
			DriverName:     vfioPciDriver,
			DeviceType:     consts.DeviceTypeVfioPci,
			NeedDriverFunc: needDriverCheckDeviceType,
		},
		virtioVdpaDriverID: {
			DriverName:     virtioVdpaDriver,
			DeviceType:     consts.DeviceTypeNetDevice,
			VdpaType:       consts.VdpaTypeVirtio,
			NeedDriverFunc: needDriverCheckVdpaType,
		},
		vhostVdpaDriverID: {
			DriverName:     vhostVdpaDriver,
			DeviceType:     consts.DeviceTypeNetDevice,
			VdpaType:       consts.VdpaTypeVhost,
			NeedDriverFunc: needDriverCheckVdpaType,
		},
	}

	// Preserve intel_iommu, iommu=pt and pci=realloc when they are already configured.
	kargs, err := helpers.GetCurrentKernelArgs()
	if err != nil {
		return nil, err
	}
	desiredKargs := kargStateMap{
		consts.KernelArgPciRealloc:    helpers.IsKernelArgsSet(kargs, consts.KernelArgPciRealloc),
		consts.KernelArgIntelIommu:    helpers.IsKernelArgsSet(kargs, consts.KernelArgIntelIommu),
		consts.KernelArgIommuPt:       helpers.IsKernelArgsSet(kargs, consts.KernelArgIommuPt),
		consts.KernelArgRdmaShared:    false,
		consts.KernelArgRdmaExclusive: false,
	}
	if vars.ForcePciRealloc {
		desiredKargs[consts.KernelArgPciRealloc] = true
	}

	return &hostConfig{
		pluginName:     hostPluginName,
		hostHelper:     helpers,
		driverStateMap: driverStateMap,
		desiredKargs:   desiredKargs,
		updateTarget:   &hostUpdateTarget{},
	}, nil
}

// Name returns the plugin name.
func (p *hostConfig) Name() string {
	return p.pluginName
}

// OnNodeStateChange compares host OS config with the desired node state.
// It returns needDrain and needReboot when kernel arguments or systemd files must change.
func (p *hostConfig) OnNodeStateChange(new *sriovnetworkv1.SriovNetworkNodeState) (needDrain bool, needReboot bool, err error) {
	log.Log.Info("host config OnNodeStateChange()")
	p.desireState = new

	if err = p.readManifestFiles(defaultOvsConfig(new.Spec.System.OvsConfig)); err != nil {
		log.Log.Error(err, "host config OnNodeStateChange(): failed to read manifests")
		return false, false, err
	}

	p.updateTarget.reset()

	kargReboot, err := p.syncKernelState(new)
	if err != nil {
		log.Log.Error(err, "host config OnNodeStateChange(): failed to sync kernel state")
		return false, false, err
	}

	if sriovnetworkv1.IsSwitchdevModeSpec(new.Spec) {
		if err = p.ovsServiceStateUpdate(); err != nil {
			log.Log.Error(err, "host config OnNodeStateChange(): failed to check OVS drop-in")
			return false, false, err
		}
	}

	if p.manageSriovSystemdUnits() {
		if err = p.sriovServicesStateUpdate(); err != nil {
			log.Log.Error(err, "host config OnNodeStateChange(): failed to check sriov systemd units")
			return false, false, err
		}
	}

	if kargReboot || p.updateTarget.needReboot() {
		log.Log.Info("host config OnNodeStateChange(): need reboot",
			"systemd", p.updateTarget.String(), "kernelArgs", kargReboot)
		return true, true, nil
	}
	return false, false, nil
}

// CheckStatusChanges reports drift in kernel arguments or RDMA subsystem mode.
func (p *hostConfig) CheckStatusChanges(current *sriovnetworkv1.SriovNetworkNodeState) (bool, error) {
	log.Log.Info("host config CheckStatusChanges()")
	if rdmaModeNeedsUpdate(current) {
		log.Log.Info("host config CheckStatusChanges(): rdma mode needs to be updated",
			"desired", current.Spec.System.RdmaMode,
			"current", current.Status.System.RdmaMode)
		return true, nil
	}

	shouldUpdate, err := p.shouldUpdateKernelArgs()
	if err != nil {
		log.Log.Error(err, "host config CheckStatusChanges(): failed to verify kernel arguments")
		return false, err
	}
	return shouldUpdate, nil
}

// Apply loads required kernel modules and writes systemd drop-ins and units.
func (p *hostConfig) Apply() error {
	log.Log.Info("host config Apply()")
	if err := p.syncDriverState(); err != nil {
		return err
	}
	if p.manageSriovSystemdUnits() {
		if err := p.updateSriovServices(); err != nil {
			return err
		}
	}
	return p.updateOVSService()
}

// manageSriovSystemdUnits reports whether this process should install sriov-config units.
// OpenShift receives those units from a MachineConfig, so the daemon must not write them.
func (p *hostConfig) manageSriovSystemdUnits() bool {
	return vars.UsingSystemdMode && vars.ClusterType != consts.ClusterTypeOpenshift
}

// defaultOvsConfig returns hw-offload=true when the node state has no OVS other_config.
func defaultOvsConfig(ovsConfig map[string]string) map[string]string {
	if len(ovsConfig) == 0 {
		return map[string]string{"hw-offload": "true"}
	}
	return ovsConfig
}

// rdmaModeNeedsUpdate reports whether spec RDMA mode differs from status.
// An empty spec mode means the operator is not managing RDMA.
func rdmaModeNeedsUpdate(state *sriovnetworkv1.SriovNetworkNodeState) bool {
	if state.Spec.System.RdmaMode == "" {
		return false
	}
	return state.Spec.System.RdmaMode != state.Status.System.RdmaMode
}

// syncDriverState loads vfio_pci, virtio_vdpa, or vhost_vdpa when the spec needs them.
func (p *hostConfig) syncDriverState() error {
	if p.desireState == nil {
		return nil
	}
	for _, driver := range p.driverStateMap {
		if !driver.DriverLoaded && driver.NeedDriverFunc(p.desireState, driver) {
			log.Log.V(2).Info("host config loading driver", "name", driver.DriverName)
			if err := p.hostHelper.LoadKernelModule(driver.DriverName); err != nil {
				log.Log.Error(err, "host config syncDriverState(): fail to load kmod", "name", driver.DriverName)
				return err
			}
			driver.DriverLoaded = true
		}
	}
	return nil
}

// needDriverCheckDeviceType reports whether any VF group requests the driver device type.
func needDriverCheckDeviceType(state *sriovnetworkv1.SriovNetworkNodeState, driver *driverState) bool {
	for _, iface := range state.Spec.Interfaces {
		for i := range iface.VfGroups {
			if iface.VfGroups[i].DeviceType == driver.DeviceType {
				return true
			}
		}
	}
	return false
}

// needDriverCheckVdpaType reports whether any VF group requests the driver vDPA type.
func needDriverCheckVdpaType(state *sriovnetworkv1.SriovNetworkNodeState, driver *driverState) bool {
	for _, iface := range state.Spec.Interfaces {
		for i := range iface.VfGroups {
			if iface.VfGroups[i].VdpaType == driver.VdpaType {
				return true
			}
		}
	}
	return false
}

// enableDesiredKernelArg marks a kernel argument as required.
func (p *hostConfig) enableDesiredKernelArg(karg string) {
	log.Log.Info("host config enableDesiredKernelArg()", "karg", karg)
	p.desiredKargs[karg] = true
}

// disableDesiredKernelArg marks a kernel argument as not required.
func (p *hostConfig) disableDesiredKernelArg(karg string) {
	log.Log.Info("host config disableDesiredKernelArg()", "karg", karg)
	p.desiredKargs[karg] = false
}

// applyStickyKernelArgs keeps pci=realloc enabled after an ENOMEM configuration failure.
func (p *hostConfig) applyStickyKernelArgs() {
	if vars.ForcePciRealloc {
		p.desiredKargs[consts.KernelArgPciRealloc] = true
	}
}

// shouldUpdateKernelArgs reports whether desired kernel arguments differ from the running cmdline.
func (p *hostConfig) shouldUpdateKernelArgs() (bool, error) {
	p.applyStickyKernelArgs()
	kargs, err := p.hostHelper.GetCurrentKernelArgs()
	if err != nil {
		return false, err
	}
	for karg, kargState := range p.desiredKargs {
		if kargState && !p.hostHelper.IsKernelArgsSet(kargs, karg) {
			return true, nil
		}
		if !kargState && p.hostHelper.IsKernelArgsSet(kargs, karg) {
			return true, nil
		}
	}
	return false, nil
}

// syncDesiredKernelArgs writes desired kernel arguments and reports whether a reboot is required.
func (p *hostConfig) syncDesiredKernelArgs() (bool, error) {
	p.applyStickyKernelArgs()
	kargs, err := p.hostHelper.GetCurrentKernelArgs()
	if err != nil {
		return false, err
	}

	needReboot := false
	for karg, kargState := range p.desiredKargs {
		if kargState {
			if err = utils.EditKernelArg(p.hostHelper, "add", karg); err != nil {
				log.Log.Error(err, "host config syncDesiredKernelArgs(): fail to set kernel arg", "karg", karg)
				return false, err
			}
			if !p.hostHelper.IsKernelArgsSet(kargs, karg) {
				needReboot = true
			}
			continue
		}
		if err = utils.EditKernelArg(p.hostHelper, "remove", karg); err != nil {
			log.Log.Error(err, "host config syncDesiredKernelArgs(): fail to remove kernel arg", "karg", karg)
			return false, err
		}
		if p.hostHelper.IsKernelArgsSet(kargs, karg) {
			needReboot = true
		}
	}
	return needReboot, nil
}

// addVfioDesiredKernelArg enables IOMMU kernel arguments when vfio-pci is required.
func (p *hostConfig) addVfioDesiredKernelArg(state *sriovnetworkv1.SriovNetworkNodeState) {
	driver := p.driverStateMap[vfioDriver]
	kernelArgFnByCPUVendor := map[hostTypes.CPUVendor]func(){
		hostTypes.CPUVendorIntel: func() {
			p.enableDesiredKernelArg(consts.KernelArgIntelIommu)
			p.enableDesiredKernelArg(consts.KernelArgIommuPt)
		},
		hostTypes.CPUVendorAMD: func() {
			p.enableDesiredKernelArg(consts.KernelArgIommuPt)
		},
	}

	if driver.DriverLoaded || !driver.NeedDriverFunc(state, driver) {
		return
	}
	cpuVendor, err := p.hostHelper.GetCPUVendor()
	if err != nil {
		log.Log.Error(err, "host config addVfioDesiredKernelArg(): can't get CPU vendor, falling back to Intel")
		cpuVendor = hostTypes.CPUVendorIntel
	}
	if addKernelArgFn := kernelArgFnByCPUVendor[cpuVendor]; addKernelArgFn != nil {
		addKernelArgFn()
	}
}

// configRdmaKernelArg sets RDMA kernel arguments and the ib_core modprobe file.
func (p *hostConfig) configRdmaKernelArg(state *sriovnetworkv1.SriovNetworkNodeState) error {
	switch state.Spec.System.RdmaMode {
	case "":
		p.disableDesiredKernelArg(consts.KernelArgRdmaExclusive)
		p.disableDesiredKernelArg(consts.KernelArgRdmaShared)
	case consts.RdmaSubsystemModeShared:
		p.enableDesiredKernelArg(consts.KernelArgRdmaShared)
		p.disableDesiredKernelArg(consts.KernelArgRdmaExclusive)
	case consts.RdmaSubsystemModeExclusive:
		p.enableDesiredKernelArg(consts.KernelArgRdmaExclusive)
		p.disableDesiredKernelArg(consts.KernelArgRdmaShared)
	default:
		err := fmt.Errorf("unexpected rdma mode: %s", state.Spec.System.RdmaMode)
		log.Log.Error(err, "host config configRdmaKernelArg(): failed to configure kernel arguments for rdma")
		return err
	}
	return p.hostHelper.SetRDMASubsystem(state.Spec.System.RdmaMode)
}

// syncKernelState applies VFIO and RDMA kernel policy and reports whether a reboot is required.
func (p *hostConfig) syncKernelState(state *sriovnetworkv1.SriovNetworkNodeState) (bool, error) {
	p.addVfioDesiredKernelArg(state)
	if err := p.configRdmaKernelArg(state); err != nil {
		return false, err
	}
	needReboot, err := p.syncDesiredKernelArgs()
	if err != nil {
		log.Log.Error(err, "host config syncKernelState(): failed to set the desired kernel arguments")
		return false, err
	}
	if needReboot {
		log.Log.V(2).Info("host config syncKernelState(): need reboot for updating kernel arguments")
	}
	return needReboot, nil
}

// readManifestFiles loads the OVS drop-in and sriov-config unit templates.
func (p *hostConfig) readManifestFiles(ovsConfig map[string]string) error {
	if err := p.readOpenVSwitchManifest(ovsConfig); err != nil {
		return err
	}
	if err := p.readSriovServiceManifest(); err != nil {
		return err
	}
	return p.readSriovPostNetworkServiceManifest()
}

// readOpenVSwitchManifest renders the OVS hardware-offload drop-in.
func (p *hostConfig) readOpenVSwitchManifest(ovsConfig map[string]string) error {
	openVSwitchService, err := p.hostHelper.ReadOvsServiceInjectionManifestFile(ovsUnitFile, ovsConfig)
	if err != nil {
		return err
	}
	p.openVSwitchService = openVSwitchService
	return nil
}

// readSriovServiceManifest loads the pre-network sriov-config unit.
func (p *hostConfig) readSriovServiceManifest() error {
	sriovService, err := p.hostHelper.ReadServiceManifestFile(sriovUnitFile)
	if err != nil {
		return err
	}
	p.sriovService = sriovService
	return nil
}

// readSriovPostNetworkServiceManifest loads the post-network sriov-config unit.
func (p *hostConfig) readSriovPostNetworkServiceManifest() error {
	sriovService, err := p.hostHelper.ReadServiceManifestFile(sriovPostNetworkUnitFile)
	if err != nil {
		return err
	}
	p.sriovPostNetworkService = sriovService
	return nil
}

// sriovServicesStateUpdate flags sriov-config units that are missing or stale.
func (p *hostConfig) sriovServicesStateUpdate() error {
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
		if !isServiceEnabled {
			s.update.SetNeedReboot()
			continue
		}
		if p.isSystemdServiceNeedUpdate(s.srv) {
			s.update.SetNeedReboot()
		}
	}
	return nil
}

// updateSriovServices writes and enables sriov-config units that were flagged for update.
func (p *hostConfig) updateSriovServices() error {
	for _, s := range []struct {
		srv    *hostTypes.Service
		update *updateTargetReq
	}{
		{srv: p.sriovService, update: &p.updateTarget.sriovScript},
		{srv: p.sriovPostNetworkService, update: &p.updateTarget.sriovPostNetworkScript},
	} {
		if !s.update.NeedUpdate() {
			continue
		}
		if err := p.hostHelper.EnableService(s.srv); err != nil {
			return err
		}
	}
	return nil
}

// ovsServiceStateUpdate flags the OVS drop-in when OVS is installed and the drop-in is stale.
func (p *hostConfig) ovsServiceStateUpdate() error {
	exist, err := p.hostHelper.IsServiceExist(ovsMainServicePath)
	if err != nil {
		return err
	}
	if !exist {
		log.Log.Info("host config ovsServiceStateUpdate(): openvswitch system service not found, skip update",
			"service", p.openVSwitchService.Name)
		return nil
	}
	if p.isDropinNeedUpdate(p.openVSwitchService) {
		p.updateTarget.openVSwitch.SetNeedReboot()
	}
	return nil
}

// isDropinNeedUpdate reports whether the on-disk drop-in differs from the desired content.
func (p *hostConfig) isDropinNeedUpdate(service *hostTypes.Service) bool {
	existing, err := p.hostHelper.ReadService(service.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		log.Log.Error(err, "host config isDropinNeedUpdate(): failed to read drop-in, assuming update needed",
			"path", service.Path)
		return true
	}
	needChange, err := p.hostHelper.CompareServices(existing, service)
	if err != nil {
		log.Log.Error(err, "host config isDropinNeedUpdate(): failed to compare drop-in, assuming update needed")
		return true
	}
	return needChange
}

// updateOVSService writes the OVS hardware-offload drop-in and drops a legacy /usr/lib copy when writable.
func (p *hostConfig) updateOVSService() error {
	if !p.updateTarget.openVSwitch.NeedUpdate() {
		return nil
	}
	if err := p.hostHelper.WriteServiceDropin(p.openVSwitchService); err != nil {
		log.Log.Error(err, "host config updateOVSService(): failed to write OVS drop-in")
		return err
	}
	p.removeLegacyOvsDropin()
	return nil
}

// removeLegacyOvsDropin deletes the old /usr/lib OVS drop-in. A read-only /usr on OpenShift is ignored.
func (p *hostConfig) removeLegacyOvsDropin() {
	legacyPath := path.Join(consts.Chroot, legacyOvsDropinPath)
	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Log.Info("host config updateOVSService(): could not remove legacy OVS drop-in",
			"path", legacyOvsDropinPath, "error", err)
	}
}

// isSystemdServiceNeedUpdate reports whether an installed unit file differs from the manifest.
func (p *hostConfig) isSystemdServiceNeedUpdate(serviceObj *hostTypes.Service) bool {
	systemService, err := p.hostHelper.ReadService(serviceObj.Path)
	if err != nil {
		log.Log.Error(err, "host config isSystemdServiceNeedUpdate(): failed to read service file, ignoring",
			"path", serviceObj.Path)
		return false
	}
	if systemService == nil {
		return false
	}
	needChange, err := p.hostHelper.CompareServices(systemService, serviceObj)
	if err != nil {
		log.Log.Error(err, "host config isSystemdServiceNeedUpdate(): failed to compare service, ignoring")
		return false
	}
	return needChange
}
