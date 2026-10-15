package generic

import (
	"errors"
	"syscall"

	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper"
	plugin "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

var PluginName = "generic"

// GenericPlugin configures SR-IOV virtual functions and managed bridges on the node.
// Kernel command line, kernel modules, and RDMA modprobe policy live in the baremetal host config.
type GenericPlugin struct {
	PluginName              string
	DesireState             *sriovnetworkv1.SriovNetworkNodeState
	helpers                 helper.HostHelpersInterface
	skipVFConfiguration     bool
	skipBridgeConfiguration bool
}

// Option configures GenericPlugin construction.
type Option = func(c *genericPluginOptions)

// WithSkipVFConfiguration configures generic plugin to skip configuration of the VFs.
// In this case PFs will be configured and VFs are created only, VF configuration phase is skipped.
// VFs on the PF (if the PF is not ExternallyManaged) will have no driver after the plugin execution completes.
func WithSkipVFConfiguration() Option {
	return func(c *genericPluginOptions) {
		c.skipVFConfiguration = true
	}
}

// WithSkipBridgeConfiguration configures generic_plugin to skip configuration of the managed bridges
func WithSkipBridgeConfiguration() Option {
	return func(c *genericPluginOptions) {
		c.skipBridgeConfiguration = true
	}
}

type genericPluginOptions struct {
	skipVFConfiguration     bool
	skipBridgeConfiguration bool
}

// NewGenericPlugin builds the generic NIC and bridge plugin.
func NewGenericPlugin(helpers helper.HostHelpersInterface, options ...Option) (plugin.VendorPlugin, error) {
	cfg := &genericPluginOptions{}
	for _, o := range options {
		o(cfg)
	}

	return &GenericPlugin{
		PluginName:              PluginName,
		helpers:                 helpers,
		skipVFConfiguration:     cfg.skipVFConfiguration,
		skipBridgeConfiguration: cfg.skipBridgeConfiguration,
	}, nil
}

// Name returns the name of the plugin
func (p *GenericPlugin) Name() string {
	return p.PluginName
}

// OnNodeStateChange reports whether VF or bridge changes require a drain.
// Kernel argument reboots are reported by the baremetal host config plugin.
func (p *GenericPlugin) OnNodeStateChange(new *sriovnetworkv1.SriovNetworkNodeState) (needDrain bool, needReboot bool, err error) {
	log.Log.Info("generic plugin OnNodeStateChange()")
	p.DesireState = new
	return p.needDrainNode(new.Spec, new.Status), false, nil
}

// CheckStatusChanges verify whether SriovNetworkNodeState CR status present changes on configured VFs.
func (p *GenericPlugin) CheckStatusChanges(current *sriovnetworkv1.SriovNetworkNodeState) (bool, error) {
	log.Log.Info("generic-plugin CheckStatusChanges()")

	for _, iface := range current.Spec.Interfaces {
		found := false
		for _, ifaceStatus := range current.Status.Interfaces {
			// TODO: remove the check for ExternallyManaged - https://github.com/k8snetworkplumbingwg/sriov-network-operator/issues/632
			if iface.PciAddress == ifaceStatus.PciAddress && !iface.ExternallyManaged {
				found = true
				if sriovnetworkv1.NeedToUpdateSriov(&iface, &ifaceStatus) {
					log.Log.Info("CheckStatusChanges(): status changed for interface", "address", iface.PciAddress)
					return true, nil
				}
				break
			}
		}
		if !found {
			log.Log.Info("CheckStatusChanges(): no status found for interface", "address", iface.PciAddress)
		}
	}

	if p.shouldConfigureBridges() {
		if sriovnetworkv1.NeedToUpdateBridges(&current.Spec.Bridges, &current.Status.Bridges) {
			log.Log.Info("CheckStatusChanges(): bridge configuration needs to be updated")
			return true, nil
		}
	}

	return false, nil
}

// Apply config change
func (p *GenericPlugin) Apply() error {
	log.Log.Info("generic plugin Apply()", "desiredState", p.DesireState.Spec)

	// When calling from systemd do not try to chroot
	if !vars.UsingSystemdMode {
		exit, err := p.helpers.Chroot(consts.Host)
		if err != nil {
			return err
		}
		defer exit()
	}

	if err := p.helpers.ConfigSriovInterfaces(p.helpers, p.DesireState.Spec.Interfaces,
		p.DesireState.Status.Interfaces, p.skipVFConfiguration); err != nil {
		// ENOMEM is recovered by pci=realloc. The arg is sticky so the host config
		// plugin requests a reboot on the next reconcile once /proc/cmdline is still missing it.
		if errors.Is(err, syscall.ENOMEM) {
			log.Log.Info("generic plugin Apply(): ENOMEM, requesting pci=realloc kernel argument")
			if kargErr := utils.EditKernelArg(p.helpers, "add", consts.KernelArgPciRealloc); kargErr != nil {
				log.Log.Error(kargErr, "generic plugin Apply(): failed to add pci=realloc kernel argument")
			}
			vars.ForcePciRealloc = true
		}
		return err
	}

	if p.shouldConfigureBridges() {
		if err := p.helpers.ConfigureBridges(p.DesireState.Spec.Bridges, p.DesireState.Status.Bridges); err != nil {
			return err
		}
	}

	return nil
}

// needDrainNode reports whether VF or bridge changes require draining the node.
func (p *GenericPlugin) needDrainNode(desired sriovnetworkv1.SriovNetworkNodeStateSpec, current sriovnetworkv1.SriovNetworkNodeStateStatus) bool {
	log.Log.V(2).Info("generic plugin needDrainNode()", "current", current, "desired", desired)

	if p.needToUpdateVFs(desired, current) {
		return true
	}

	if p.shouldConfigureBridges() {
		if sriovnetworkv1.NeedToUpdateBridges(&desired.Bridges, &current.Bridges) {
			log.Log.V(2).Info("generic plugin needDrainNode(): need drain since bridge configuration needs to be updated")
			return true
		}
	}
	return false
}

// needToUpdateVFs reports whether configured virtual functions must change.
func (p *GenericPlugin) needToUpdateVFs(desired sriovnetworkv1.SriovNetworkNodeStateSpec, current sriovnetworkv1.SriovNetworkNodeStateStatus) bool {
	for _, ifaceStatus := range current.Interfaces {
		configured := false
		for _, iface := range desired.Interfaces {
			if iface.PciAddress == ifaceStatus.PciAddress {
				configured = true
				if ifaceStatus.NumVfs == 0 {
					log.Log.V(2).Info("generic plugin needToUpdateVFs(): no need drain, for PCI address, current NumVfs is 0",
						"address", iface.PciAddress)
					break
				}
				if sriovnetworkv1.NeedToUpdateSriov(&iface, &ifaceStatus) {
					log.Log.V(2).Info("generic plugin needToUpdateVFs(): need drain, for PCI address request update",
						"address", iface.PciAddress)
					return true
				}
				log.Log.V(2).Info("generic plugin needToUpdateVFs(): no need drain,for PCI address",
					"address", iface.PciAddress, "expected-vfs", iface.NumVfs, "current-vfs", ifaceStatus.NumVfs)
			}
		}
		if !configured && ifaceStatus.NumVfs > 0 {
			// load the PF info
			pfStatus, exist, err := p.helpers.LoadPfsStatus(ifaceStatus.PciAddress)
			if err != nil {
				log.Log.Error(err, "generic plugin needToUpdateVFs(): failed to load info about PF status for pci device",
					"address", ifaceStatus.PciAddress)
				continue
			}

			if !exist {
				log.Log.Info("generic plugin needToUpdateVFs(): PF name with pci address has VFs configured but they weren't created by the sriov operator. Skipping drain",
					"name", ifaceStatus.Name,
					"address", ifaceStatus.PciAddress)
				continue
			}

			if pfStatus.ExternallyManaged {
				log.Log.Info("generic plugin needToUpdateVFs(): PF name with pci address was externally created. Skipping drain",
					"name", ifaceStatus.Name,
					"address", ifaceStatus.PciAddress)
				continue
			}

			log.Log.V(2).Info("generic plugin needToUpdateVFs(): need drain since interface needs to be reset",
				"interface", ifaceStatus)
			return true
		}
	}
	return false
}

// shouldConfigureBridges reports whether this plugin should reconcile managed bridges.
func (p *GenericPlugin) shouldConfigureBridges() bool {
	return vars.ManageSoftwareBridges && !p.skipBridgeConfiguration
}
