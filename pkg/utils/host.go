package utils

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang/glog"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
)

const (
	SriovConfBasePath     = "/etc/sriov-operator"
	PfAppliedConfig       = SriovConfBasePath + "/pci"
	HostSriovConfBasePath = "/host" + SriovConfBasePath
	HostPfAppliedConfig   = HostSriovConfBasePath + "/pci"
)

type PfStatus struct {
	NumVfs            int    `json:"numVfs"`
	Mtu               int    `json:"mtu"`
	LinkType          string `json:"linkType"`
	EswitchMode       string `json:"eSwitchMode"`
	ExternallyCreated bool   `json:"externallyCreated"`
}

// create the operator base folder on the host together with the pci folder to save the PF status objects as json files
func CreateOperatorConfigFolderIfNeeded() error {
	_, err := os.Stat(SriovConfBasePath)
	if err != nil {
		if os.IsNotExist(err) {
			err = os.Mkdir(SriovConfBasePath, os.ModeDir)
			if err != nil {
				return fmt.Errorf("failed to create the sriov config folder on host in path %s: %v", SriovConfBasePath, err)
			}
		} else {
			return fmt.Errorf("failed to check if the sriov config folder on host in path %s exist: %v", SriovConfBasePath, err)
		}
	}

	_, err = os.Stat(PfAppliedConfig)
	if err != nil {
		if os.IsNotExist(err) {
			err = os.Mkdir(PfAppliedConfig, os.ModeDir)
			if err != nil {
				return fmt.Errorf("failed to create the pci folder on host in path %s: %v", PfAppliedConfig, err)
			}
		} else {
			return fmt.Errorf("failed to check if the pci folder on host in path %s exist: %v", PfAppliedConfig, err)
		}
	}

	return nil
}

func ClearPCIAddressFolder() error {
	_, err := os.Stat(HostPfAppliedConfig)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to check the pci address folder path %s", HostPfAppliedConfig)
	}

	err = os.RemoveAll(HostPfAppliedConfig)
	if err != nil {
		return fmt.Errorf("failed to remove the PCI address folder on path %s: %v", HostPfAppliedConfig, err)
	}

	err = os.Mkdir(HostPfAppliedConfig, os.ModeDir)
	if err != nil {
		return fmt.Errorf("failed to create the pci folder on host in path %s: %v", HostPfAppliedConfig, err)
	}

	return nil
}

func CreatePfAppliedStatusFromSpec(p *sriovnetworkv1.Interface) *PfStatus {
	return &PfStatus{
		ExternallyCreated: p.ExternallyCreated,
		NumVfs:            p.NumVfs,
		EswitchMode:       p.EswitchMode,
		Mtu:               p.Mtu,
		LinkType:          p.LinkType,
	}
}

// SaveLastPfAppliedStatus will save the PF object as a json into the /etc/sriov-operator/pci/<pci-address>
// this function must be called after running the chroot function
func SaveLastPfAppliedStatus(pciAddress string, pfStatus *PfStatus) error {
	if err := CreateOperatorConfigFolderIfNeeded(); err != nil {
		return err
	}

	data, err := json.Marshal(pfStatus)
	if err != nil {
		glog.Errorf("failed to marshal PF status %+v: %v", *pfStatus, err)
		return err
	}

	path := filepath.Join(PfAppliedConfig, pciAddress)
	err = os.WriteFile(path, data, 0644)
	return err
}

// LoadPfsStatus convert the /etc/sriov-operator/pci/<pci-address> json to pfstatus
// returns false if the file doesn't exist.
func LoadPfsStatus(pciAddress string, chroot bool) (*PfStatus, bool, error) {
	if chroot {
		exit, err := Chroot("/host")
		if err != nil {
			return nil, false, err
		}
		defer exit()
	}

	pfStatus := &PfStatus{}
	path := filepath.Join(PfAppliedConfig, pciAddress)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		glog.Errorf("failed to read PF status from path %s: %v", path, err)
	}

	err = json.Unmarshal(data, pfStatus)
	if err != nil {
		glog.Errorf("failed to unmarshal PF status %s: %v", data, err)
	}

	return pfStatus, true, nil
}
