package infiniband

import (
	"fmt"
	"net"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/netlink"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

// ibGUIDPool is an interface that returns the next free GUID, allocated for VFs of the specific PF
type ibGUIDPool interface {
	// GetNextFreeGUID returns the next free GUID, allocated for VFs of the specific PF
	// If no guid pool exists for the given pfPciAddr, returns an error
	// If no free guids are left for the given pfPciAddr, returns an error
	GetNextFreeGUID(pfPciAddr string, vfID int) (net.HardwareAddr, error)
}

type ibGUIDPoolImpl struct {
	guidConfigs map[string]ibPfGUIDConfig
}

// newIbGUIDPool returns an instance of ibGUIDPool, that synchronizes the access to the pool
func newIbGUIDPool(configPath string, netlinkLib netlink.NetlinkLib, networkHelper types.NetworkInterface) (ibGUIDPool, error) {
	// All validation for the config file is done in the getIbGUIDConfig function
	configs, err := getIbGUIDConfig(configPath, netlinkLib, networkHelper)
	if err != nil {
		return nil, fmt.Errorf("failed to create ib guid pool: %w", err)
	}

	return &ibGUIDPoolImpl{guidConfigs: configs}, nil
}

func (p *ibGUIDPoolImpl) GetNextFreeGUID(pfPciAddr string, vfID int) (net.HardwareAddr, error) {
	config, exists := p.guidConfigs[pfPciAddr]
	if !exists {
		return nil, fmt.Errorf("no guid pool for pci address: %s", pfPciAddr)
	}

	if len(config.GUIDs) != 0 {
		if vfID >= len(config.GUIDs) {
			return nil, fmt.Errorf("guid pool exhausted for pci address: %s", pfPciAddr)
		}

		guid := config.GUIDs[vfID]

		return guid, nil
	} else {
		nextGUID := config.RangeStart + utils.GUID(vfID)
		if nextGUID > config.RangeEnd {
			return nil, fmt.Errorf("guid pool exhausted for pci address: %s", pfPciAddr)
		}

		return nextGUID.HardwareAddr(), nil
	}
}
