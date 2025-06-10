package aws

import (
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper"
	plugin "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins"
	virtualplugin "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins/virtual"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

const (
	// metadataBaseURL is the base URL for the EC2 instance metadata service.
	metadataBaseURL = "http://169.254.169.254/latest/meta-data/"
	// macsPath is the path to list MAC addresses. Note the trailing slash,
	// which is standard for directory-like listings in the metadata service.
	macsPath = "network/interfaces/macs/"
	// subnetIDSuffix is appended to a specific MAC address path to get its subnet ID.
	subnetIDSuffix = "/subnet-id"
)

type Aws struct {
	hostHelpers       helper.HostHelpersInterface
	loadedDevicesInfo sriovnetworkv1.InterfaceExts
}

func New(hostHelpers helper.HostHelpersInterface) (*Aws, error) {
	return &Aws{
		hostHelpers: hostHelpers,
	}, nil
}

func (a *Aws) Init() error {
	ns, err := a.hostHelpers.GetCheckPointNodeState()
	if err != nil {
		return err
	}

	if ns == nil {
		err = a.createDevicesInfo()
		return err
	}

	a.createDevicesInfoFromNodeStatus(ns)
	return nil
}

func (a *Aws) Name() string {
	return string(consts.AWS)
}

func (a *Aws) GetVendorPlugins(_ *sriovnetworkv1.SriovNetworkNodeState) (plugin.VendorPlugin, []plugin.VendorPlugin, error) {
	virtual, err := virtualplugin.NewVirtualPlugin(a.hostHelpers)
	return virtual, []plugin.VendorPlugin{}, err
}

// SystemdGetVendorPlugin is not supported on AWS platform.
// Returns ErrOperationNotSupportedByPlatform as AWS does not support systemd.
func (a *Aws) SystemdGetVendorPlugin(_ string) (plugin.VendorPlugin, error) {
	return nil, vars.ErrOperationNotSupportedByPlatform
}

// DiscoverSriovDevices discovers VFs on a virtual platform
func (a *Aws) DiscoverSriovDevices() ([]sriovnetworkv1.InterfaceExt, error) {
	funcLog := log.Log.WithName("DiscoverSriovDevices")
	log.Log.V(2).Info("discovering sriov devices")

	// TODO: check if we want to support hot plug here in the future
	for idx, iface := range a.loadedDevicesInfo {
		hasDriver, driver := a.hostHelpers.HasDriver(iface.PciAddress)
		if !hasDriver {
			funcLog.Error(nil, "device doesn't have a driver, skipping",
				"iface", iface)
			continue
		}
		iface.Driver = driver

		if mtu := a.hostHelpers.GetNetdevMTU(iface.PciAddress); mtu > 0 {
			iface.Mtu = mtu
		}

		if name := a.hostHelpers.TryToGetVirtualInterfaceName(iface.PciAddress); name != "" {
			iface.Name = name
			if macAddr := a.hostHelpers.GetNetDevMac(name); macAddr != "" {
				iface.Mac = macAddr
			}
			iface.LinkSpeed = a.hostHelpers.GetNetDevLinkSpeed(name)
			iface.LinkType = a.hostHelpers.GetLinkType(name)
		}
		if len(iface.VFs) != 1 {
			log.Log.Error(nil, "only one vf should exist", "iface", iface)
			return nil, fmt.Errorf("unexpected number of vfs found for device %s", iface.Name)
		}
		iface.VFs[0] = sriovnetworkv1.VirtualFunction{
			PciAddress: iface.PciAddress,
			Driver:     driver,
			VfID:       0,
			Vendor:     iface.Vendor,
			DeviceID:   iface.DeviceID,
			Mtu:        iface.Mtu,
			Mac:        iface.Mac,
		}
		a.loadedDevicesInfo[idx] = iface
	}
	return a.loadedDevicesInfo, nil
}

// DiscoverBridges is not supported on AWS platform.
// Returns ErrOperationNotSupportedByPlatform as AWS does not support software bridge management.
func (a *Aws) DiscoverBridges() (sriovnetworkv1.Bridges, error) {
	if vars.ManageSoftwareBridges {
		return sriovnetworkv1.Bridges{}, vars.ErrOperationNotSupportedByPlatform
	}
	return sriovnetworkv1.Bridges{}, nil
}

// createDevicesInfo creates the AWS device info map
// This function is used to create the AWS device info map from the metadata server.
func (a *Aws) createDevicesInfo() error {
	funcLog := log.Log.WithName("getDataFromMetadataService")
	a.loadedDevicesInfo = make(sriovnetworkv1.InterfaceExts, 0)
	funcLog.Info("getting aws network info from metadata server")
	metaData, err := a.hostHelpers.HTTPGetFetchData(metadataBaseURL + macsPath)
	if err != nil {
		return fmt.Errorf("error getting aws meta_data from %s: %v", metadataBaseURL+macsPath, err)
	}

	// If the endpoint returns an empty body (e.g., no MACs available or an issue),
	// treat it as no MACs found rather than an error. The caller can then decide how to handle an empty list.
	if metaData == "" {
		return nil
	}

	// MAC addresses are returned separated by newlines, each ending with a '/'.
	// Example raw response: "0e:1a:95:aa:12:a1/\n0e:92:d3:ee:52:1b/"
	macEntries := strings.Split(metaData, "\n")
	if len(macEntries) == 0 {
		return nil
	}

	macAddressToSubNetID := map[string]string{}
	for _, macEntry := range macEntries {
		subnetIDURL := metadataBaseURL + macsPath + macEntry + subnetIDSuffix
		subnetIDData, err := a.hostHelpers.HTTPGetFetchData(subnetIDURL)
		if err != nil {
			return fmt.Errorf("error getting aws subnet_id from %s: %v", subnetIDURL, err)
		}

		if subnetIDData == "" {
			return fmt.Errorf("empty subnet_id from %s: %v", subnetIDURL, err)
		}
		macAddressToSubNetID[strings.ReplaceAll(macEntry, "/", "")] = subnetIDData
	}

	pfList, err := a.hostHelpers.DiscoverSriovVirtualDevices()
	if err != nil {
		return err
	}

	for _, iface := range pfList {
		subnetID, exist := macAddressToSubNetID[iface.Mac]
		if !exist {
			continue
		}

		subnetID = strings.TrimPrefix(subnetID, "subnet-")
		iface.NetFilter = fmt.Sprintf("aws/NetworkID:%s", subnetID)
		iface.TotalVfs = 1
		iface.NumVfs = 1

		vf := sriovnetworkv1.VirtualFunction{
			PciAddress: iface.PciAddress,
			Driver:     iface.Driver,
			VfID:       0,
			Vendor:     iface.Vendor,
			DeviceID:   iface.DeviceID,
			Mtu:        iface.Mtu,
			Mac:        iface.Mac,
		}
		iface.VFs = append(iface.VFs, vf)
		a.loadedDevicesInfo = append(a.loadedDevicesInfo, iface)
	}
	funcLog.V(2).Info("loaded devices info from metadata server", "devices", a.loadedDevicesInfo)
	return nil
}

func (a *Aws) createDevicesInfoFromNodeStatus(networkState *sriovnetworkv1.SriovNetworkNodeState) {
	a.loadedDevicesInfo = networkState.Status.Interfaces
}
