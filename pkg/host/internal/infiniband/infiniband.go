package infiniband

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"net"

	"github.com/vishvananda/netlink"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	netlinkLibPkg "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/netlink"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
)

// New creates and returns an InfinibandInterface object, that handles IB VF GUID configuration
func New(netlinkLib netlinkLibPkg.NetlinkLib, kernelHelper types.KernelInterface, networkHelper types.NetworkInterface) (types.InfinibandInterface, error) {
	guidPool, err := newIbGUIDPool(consts.InfinibandGUIDConfigFilePath, netlinkLib, networkHelper)
	if err != nil {
		// if config file doesn't exist, fallback to the random GUID generation
		if errors.Is(err, fs.ErrNotExist) {
			log.Log.Info("infiniband.New(): ib guid config doesn't exist, continuing without it", "config path", consts.InfinibandGUIDConfigFilePath)
			return &infiniband{guidPool: nil, netlinkLib: netlinkLib, kernelHelper: kernelHelper}, nil
		}

		return nil, fmt.Errorf("failed to create the ib guid pool: %w", err)
	}

	return &infiniband{guidPool: guidPool, netlinkLib: netlinkLib, kernelHelper: kernelHelper}, nil
}

type infiniband struct {
	guidPool     ibGUIDPool
	netlinkLib   netlinkLibPkg.NetlinkLib
	kernelHelper types.KernelInterface
}

// GetVfGUID gets a GUID for an IB VF device (checks pool first, then generates random)
func (i *infiniband) GetVfGUID(pfAddr string, vfID int) (net.HardwareAddr, error) {
	if i.guidPool != nil {
		guidFromPool, err := i.guidPool.GetVFGUID(pfAddr, vfID)
		if err != nil {
			log.Log.Error(err, "GetVfGUID(): failed to get GUID from IB GUID pool", "pfAddr", pfAddr, "vfID", vfID)
			return nil, err
		}
		return guidFromPool, nil
	}

	// Fallback to random GUID generation if pool is not available.
	// Using crypto/rand for cryptographically secure random numbers to avoid collisions.
	guid := make(net.HardwareAddr, 8)
	if _, err := rand.Read(guid); err != nil {
		return nil, fmt.Errorf("failed to generate random GUID: %w", err)
	}
	// Set U/L bit to indicate locally administered address.
	guid[0] |= 0x02
	// Unset I/G bit for unicast.
	guid[0] &^= 0x01
	return guid, nil
}

// ConfigureVfGUID configures and sets a GUID for an IB VF device
func (i *infiniband) ConfigureVfGUID(vfAddr string, pfAddr string, vfID int, pfLink netlink.Link) error {
	log.Log.Info("ConfigureVfGUID(): configure vf guid", "vfAddr", vfAddr, "pfAddr", pfAddr, "vfID", vfID)

	guid, err := i.GetVfGUID(pfAddr, vfID)
	if err != nil {
		return err
	}

	log.Log.Info("ConfigureVfGUID(): set vf guid", "address", vfAddr, "guid", guid)

	return i.applyVfGUIDToInterface(guid, vfAddr, vfID, pfLink)
}

func (i *infiniband) applyVfGUIDToInterface(guid net.HardwareAddr, vfAddr string, vfID int, pfLink netlink.Link) error {
	if err := i.netlinkLib.LinkSetVfNodeGUID(pfLink, vfID, guid); err != nil {
		return err
	}
	if err := i.netlinkLib.LinkSetVfPortGUID(pfLink, vfID, guid); err != nil {
		return err
	}

	return nil
}
