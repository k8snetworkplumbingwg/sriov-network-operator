package infiniband

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/netlink"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

type ibPfGUIDJSONConfig struct {
	PciAddress string   `json:"pciAddress,omitempty"`
	PfGUID     string   `json:"pfGuid,omitempty"`
	GUIDs      []string `json:"guids,omitempty"`
	RangeStart string   `json:"rangeStart,omitempty"`
	RangeEnd   string   `json:"rangeEnd,omitempty"`
}

type ibPfGUIDConfig struct {
	GUIDs      []net.HardwareAddr
	RangeStart utils.GUID
	RangeEnd   utils.GUID
}

func getIbGUIDConfig(configPath string, netlinkLib netlink.NetlinkLib, networkHelper types.NetworkInterface) (map[string]ibPfGUIDConfig, error) {
	links, err := netlinkLib.LinkList()
	if err != nil {
		return nil, err
	}

	rawConfigs, err := readJSONConfig(configPath)
	if err != nil {
		return nil, err
	}

	resultConfigs := map[string]ibPfGUIDConfig{}

	// Parse JSON config into an internal struct
	for _, rawConfig := range rawConfigs {
		pciAddress, err := getPfPciAddressFromRawConfig(rawConfig, links, networkHelper)
		if err != nil {
			return nil, fmt.Errorf("failed to extract pci address from ib guid config: %w", err)
		}

		// GUID list and GUID range can't be set at the same time
		if len(rawConfig.GUIDs) != 0 && (rawConfig.RangeStart != "" || rawConfig.RangeEnd != "") {
			return nil, fmt.Errorf("either guid list or guid range should be provided, got both")
		} else if (rawConfig.RangeStart != "" && rawConfig.RangeEnd == "") || (rawConfig.RangeStart == "" && rawConfig.RangeEnd != "") {
			return nil, fmt.Errorf("both guid rangeStart and rangeEnd should be provided, got one")
		} else if len(rawConfig.GUIDs) != 0 {
			var guids []net.HardwareAddr

			for _, guidStr := range rawConfig.GUIDs {
				guid, err := net.ParseMAC(guidStr)
				if err != nil {
					return nil, fmt.Errorf("failed to parse ib guid %s: %w", guidStr, err)
				}
				guids = append(guids, guid)
			}
			resultConfigs[pciAddress] = ibPfGUIDConfig{
				GUIDs: guids,
			}
		} else if rawConfig.RangeStart != "" && rawConfig.RangeEnd != "" { // use guid range
			rangeStart, err := utils.ParseGUID(rawConfig.RangeStart)
			if err != nil {
				return nil, fmt.Errorf("failed to parse ib guid range start: %w", err)
			}

			rangeEnd, err := utils.ParseGUID(rawConfig.RangeEnd)
			if err != nil {
				return nil, fmt.Errorf("failed to parse ib guid range end: %w", err)
			}

			if rangeEnd <= rangeStart {
				return nil, fmt.Errorf("range end cannot be less then or equal to range start")
			}

			resultConfigs[pciAddress] = ibPfGUIDConfig{
				RangeStart: rangeStart,
				RangeEnd:   rangeEnd,
			}
		} else {
			return nil, fmt.Errorf("either guid list or guid range should be provided, got none")
		}
	}

	return resultConfigs, nil
}

// readJSONConfig reads the file at the given path and unmarshals the contents into an array of ibPfGUIDJSONConfig structs
func readJSONConfig(configPath string) ([]ibPfGUIDJSONConfig, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open ib guid config file: %w", err)
	}
	defer file.Close()

	var configs []ibPfGUIDJSONConfig
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&configs); err != nil {
		fmt.Println("Error decoding JSON:", err)
		return nil, fmt.Errorf("failed to decode ib guid config from json: %w", err)
	}

	return configs, nil
}

func getPfPciAddressFromRawConfig(pfRawConfig ibPfGUIDJSONConfig, links []netlink.Link, networkHelper types.NetworkInterface) (string, error) {
	if networkHelper == nil {
		return "", fmt.Errorf("network helper should not be nil")
	}

	if pfRawConfig.PciAddress != "" && pfRawConfig.PfGUID != "" {
		return "", fmt.Errorf("either PCI address or PF GUID required to describe an interface, both provided")
	} else if pfRawConfig.PciAddress != "" {
		return pfRawConfig.PciAddress, nil
	} else if pfRawConfig.PfGUID != "" { // If PF GUID is given, match the existing link and retrieve its pci address
		for _, link := range links {
			if link.Attrs().HardwareAddr.String() == pfRawConfig.PfGUID {
				return networkHelper.GetPciAddressFromInterfaceName(link.Attrs().Name)
			}
		}

		return "", fmt.Errorf("no matching link found for pf guid: %s", pfRawConfig.PfGUID)
	} else {
		return "", fmt.Errorf("either PCI address or PF GUID required to describe an interface, none provided")
	}
}
