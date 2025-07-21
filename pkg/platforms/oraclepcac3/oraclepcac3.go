/*
Copyright (c) 2025, Oracle and/or its affiliates.

Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl.
*/

package oraclepcac3

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/jaypipes/ghw"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dputils "github.com/k8snetworkplumbingwg/sriov-network-device-plugin/pkg/utils"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host"
)

const (
	oracleMetaDataBaseDir = "/opc/v2"
	oracleMetaDataBaseURL = "http://169.254.169.254" + oracleMetaDataBaseDir
	oracleVnicURL         = oracleMetaDataBaseURL + "/" + "vnics"
	metadataHeaders       = "Bearer Oracle"
	pcaC3Platform         = "Oraclecloud.com:PCAVM"
	firmwareDmiFile       = "/sys/firmware/dmi/tables/DMI"
)

type OraclePcaC3Interface interface {
	IsOraclePcaC3Platform() bool
	CreateOraclePcaC3DevicesInfo() error
	CreateOraclePcaC3DevicesInfoFromNodeStatus(*sriovnetworkv1.SriovNetworkNodeState)
	DiscoverSriovDevicesPcaC3Virtual() ([]sriovnetworkv1.InterfaceExt, error)
}

type OraclePcaC3Context struct {
	hostManager            host.HostManagerInterface
	oraclePcaC3DevicesInfo PcaC3DevicesInfo
}

type PcaC3VnicData struct {
	VnicID      string `json:"vnicId,omitempty"`
	PrivateIP   string `json:"privateIp,omitempty"`
	VlanTag     int    `json:"vlanTag,omitempty"`
	MacAddr     string `json:"macAddr,omitempty"`
	VirRouterIP string `json:"virtualRouterIp,omitempty"`
	SubnetCIDR  string `json:"subnetCidrBlock,omitempty"`
}

// PcaC3Network Network metadata
type PcaC3NetworkData struct {
	MacAddress string `json:"address"`
	PciAddress string `json:"parentdev"`
	IFName     string `json:"ifname"`
}

type PcaC3DevicesInfo map[string]*PcaC3DeviceInfo

type VfNicInfo struct {
	PciID         string
	InterfaceName string
}

type PcaC3DeviceInfo struct {
	MacAddress string
	MacID      string
}

// Interfaces and its implementation, to be used while fetching list of virtual functions
type VfDetector interface {
	ReadDir(dirname string) ([]os.FileInfo, error)
	ReadLink(name string) (string, error)
	RunLspci(pciID string) (string, error)
}

type DefaultVfDetector struct{}

func (d DefaultVfDetector) ReadDir(dirname string) ([]os.FileInfo, error) {
	return ioutil.ReadDir(dirname)
}

func (d DefaultVfDetector) ReadLink(name string) (string, error) {
	return os.Readlink(name)
}

func (d DefaultVfDetector) RunLspci(pciID string) (string, error) {
	out, err := exec.Command("lspci", "-mm", "-s", pciID).CombinedOutput()
	return string(out), err
}

// HTTPClient interface allows mocking in tests
type HTTPClient interface {
	Do(req *retryablehttp.Request) (*http.Response, error)
}

func New(hostManager host.HostManagerInterface) OraclePcaC3Interface {
	return &OraclePcaC3Context{
		hostManager: hostManager,
	}
}

func getBodyFromURL(url string, client *retryablehttp.Client) ([]byte, error) {
	log.Log.V(2).Info("Getting body from", "url", url)

	// Create a new GET request
	req, err := retryablehttp.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// Add the Authorization header
	req.Header.Set("Authorization", metadataHeaders)

	// Mae the HTTP request
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	rawBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return rawBytes, nil
}

// getOraclePcaC3DataFromMetadataService fetchs the metadata from the metadata service
func getOraclePcaC3DataFromMetadataService(client *retryablehttp.Client) (metaData []PcaC3VnicData, err error) {
	log.Log.Info("getting VNICs meta_data from metadata server")
	metaDataRawBytes, err := getBodyFromURL(oracleVnicURL, client)
	if err != nil {
		return metaData, fmt.Errorf("error getting Oracle meta_data from %s: %v", oracleVnicURL, err)
	}
	err = json.Unmarshal(metaDataRawBytes, &metaData)
	if err != nil {
		return metaData, fmt.Errorf("error unmarshalling raw bytes %v from %s", err, oracleVnicURL)
	}

	return metaData, nil
}

// getVirtualFunctionNicInfo returns the Virtual Function Interfaces PCI Address and MAC Address list
func getVirtualFunctionNicInfo(detector VfDetector) ([]VfNicInfo, error) {
	log.Log.Info("getVirtualFunctionNicInfo()")
	nicDir := "/sys/class/net/"
	files, err := detector.ReadDir(nicDir)
	if err != nil {
		return nil, err
	}

	vfnicInfos := make([]VfNicInfo, 0)

	for _, file := range files {
		nicPath := filepath.Join(nicDir, file.Name())
		link, err := detector.ReadLink(nicPath)
		if err != nil {
			continue
		}
		pciID := strings.TrimPrefix(link, "../../devices/")
		pciID = strings.Split(pciID, "/")[1]
		out, err := detector.RunLspci(pciID)
		if err != nil {
			continue
		}
		if strings.Contains(out, "Virtual Function") {
			vfnicInfos = append(vfnicInfos, VfNicInfo{
				PciID:         pciID,
				InterfaceName: file.Name(),
			})
		}
	}
	return vfnicInfos, nil
}

func mapMacAndPCIAddress(vfnicInfos []VfNicInfo, metaData []PcaC3VnicData, GetNetDevMac func(iface string) string) (PcaC3DevicesInfo, error) {
	devicesInfo := make(PcaC3DevicesInfo)
	for _, vnic := range metaData {
		for _, vfnicInfo := range vfnicInfos {
			MacAddr := GetNetDevMac(vfnicInfo.InterfaceName)
			if MacAddr == vnic.MacAddr {
				macID := sriovnetworkv1.OraclePcaC3MacAddress.String() + ":" + strings.ReplaceAll(vnic.MacAddr, ":", "-")
				devicesInfo[vfnicInfo.PciID] = &PcaC3DeviceInfo{MacAddress: vnic.MacAddr, MacID: macID}
			}
		}
	}
	return devicesInfo, nil
}

// CreateOraclePcaC3DevicesInfo create the oraclepcac3 device info map
func (o *OraclePcaC3Context) CreateOraclePcaC3DevicesInfo() error {
	log.Log.Info("CreateOraclePcaC3DevicesInfo()")

	client := retryablehttp.NewClient()
	client.RetryMax = 5
	metaData, err := getOraclePcaC3DataFromMetadataService(client)
	if err != nil {
		log.Log.Error(err, "failed to read Oraclepcac3 data")
		return err
	}

	// Define the virtual function detector
	vfDetector := DefaultVfDetector{}
	vfnicInfos, err := getVirtualFunctionNicInfo(vfDetector)
	if err != nil {
		log.Log.Error(err, "failed to get the Virtual Function Nic Info")
		return err
	}

	if metaData == nil || vfnicInfos == nil {
		o.oraclePcaC3DevicesInfo = make(PcaC3DevicesInfo)
		return nil
	}

	devicesInfo, err := mapMacAndPCIAddress(vfnicInfos, metaData, o.hostManager.GetNetDevMac)
	if err != nil {
		log.Log.Error(err, "Mapping MAC ID and PCI address failed")
		return err
	}
	o.oraclePcaC3DevicesInfo = devicesInfo
	return nil
}

// DiscoverSriovDevicesPcaC3Virtual discovers VFs on a virtual platform
func (o *OraclePcaC3Context) DiscoverSriovDevicesPcaC3Virtual() ([]sriovnetworkv1.InterfaceExt, error) {
	log.Log.V(2).Info("DiscoverSriovDevicesPcaC3Virtual()")
	pfList := []sriovnetworkv1.InterfaceExt{}

	pci, err := ghw.PCI()
	if err != nil {
		return nil, fmt.Errorf("DiscoverSriovDevicesPcaC3Virtual(): error getting PCI info: %v", err)
	}

	devices := pci.Devices
	if len(devices) == 0 {
		return nil, fmt.Errorf("DiscoverSriovDevicesPcaC3Virtual(): could not retrieve PCI devices")
	}

	for _, device := range devices {
		devClass, err := strconv.ParseInt(device.Class.ID, 16, 64)
		if err != nil {
			log.Log.Error(err, "DiscoverSriovDevicesPcaC3Virtual(): unable to parse device class for device, skipping",
				"device", device)
			continue
		}
		if devClass != consts.NetClass {
			// Not network device
			continue
		}

		deviceInfo, exist := o.oraclePcaC3DevicesInfo[device.Address]
		if !exist {
			log.Log.Error(nil, "DiscoverSriovDevicesPcaC3Virtual(): unable to find device in devicesInfo list, skipping",
				"device", device.Address)
			continue
		}
		netFilter := deviceInfo.MacID
		metaMac := deviceInfo.MacAddress

		driver, err := dputils.GetDriverName(device.Address)
		if err != nil {
			log.Log.Error(err, "DiscoverSriovDevicesPcaC3Virtual(): unable to parse device driver for device, skipping",
				"device", device)
			continue
		}
		iface := sriovnetworkv1.InterfaceExt{
			PciAddress: device.Address,
			Driver:     driver,
			Vendor:     device.Vendor.ID,
			DeviceID:   device.Product.ID,
			NetFilter:  netFilter,
		}
		if mtu := o.hostManager.GetNetdevMTU(device.Address); mtu > 0 {
			iface.Mtu = mtu
		}
		if name := o.hostManager.TryToGetVirtualInterfaceName(device.Address); name != "" {
			iface.Name = name
			if iface.Mac = o.hostManager.GetNetDevMac(name); iface.Mac == "" {
				iface.Mac = metaMac
			}
			iface.LinkSpeed = o.hostManager.GetNetDevLinkSpeed(name)
			iface.LinkType = o.hostManager.GetLinkType(name)
		}

		iface.TotalVfs = 1
		iface.NumVfs = 1

		vf := sriovnetworkv1.VirtualFunction{
			PciAddress: device.Address,
			Driver:     driver,
			VfID:       0,
			Vendor:     iface.Vendor,
			DeviceID:   iface.DeviceID,
			Mtu:        iface.Mtu,
			Mac:        iface.Mac,
		}
		iface.VFs = append(iface.VFs, vf)

		pfList = append(pfList, iface)
	}
	return pfList, nil
}

func (o *OraclePcaC3Context) CreateOraclePcaC3DevicesInfoFromNodeStatus(networkState *sriovnetworkv1.SriovNetworkNodeState) {
	devicesInfo := make(PcaC3DevicesInfo)
	for _, iface := range networkState.Status.Interfaces {
		devicesInfo[iface.PciAddress] = &PcaC3DeviceInfo{MacAddress: iface.Mac, MacID: iface.NetFilter}
	}

	o.oraclePcaC3DevicesInfo = devicesInfo
}

func (o *OraclePcaC3Context) IsOraclePcaC3Platform() bool {
	log.Log.Info("IsOraclePcaC3Platform()")
	data, err := os.ReadFile(firmwareDmiFile)
	if err != nil {
		return false
	}
	if strings.Contains(strings.ToLower(string(data)), strings.ToLower(pcaC3Platform)) {
		return true
	}
	return false
}
