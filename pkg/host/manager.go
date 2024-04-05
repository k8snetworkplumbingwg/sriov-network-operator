package host

import (
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/bridge"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/kernel"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/dputils"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/ethtool"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/netlink"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/lib/sriovnet"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/network"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/service"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/sriov"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/udev"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/internal/vdpa"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

// Contains all the host manipulation functions
//
//go:generate ../../bin/mockgen -destination mock/mock_host.go -source manager.go
type HostManagerInterface interface {
	types.KernelInterface
	types.NetworkInterface
	types.ServiceInterface
	types.UdevInterface
	types.SriovInterface
	types.VdpaInterface
	types.BridgeInterface
}

type hostManager struct {
	utils.CmdInterface
	types.KernelInterface
	types.NetworkInterface
	types.ServiceInterface
	types.UdevInterface
	types.SriovInterface
	types.VdpaInterface
	types.BridgeInterface
}

func NewHostManager(utilsInterface utils.CmdInterface) (HostManagerInterface, error) {
	dpUtils := dputils.New()
	netlinkLib := netlink.New()
	ethtoolLib := ethtool.New()
	sriovnetLib := sriovnet.New()
	k := kernel.New(utilsInterface)
	n := network.New(utilsInterface, dpUtils, netlinkLib, ethtoolLib)
	sv := service.New(utilsInterface)
	u := udev.New(utilsInterface)
	v := vdpa.New(k, netlinkLib)
	br, err := bridge.New()
	if err != nil {
		return nil, err
	}
	sr := sriov.New(utilsInterface, k, n, u, v, br, netlinkLib, dpUtils, sriovnetLib)

	return &hostManager{
		utilsInterface,
		k,
		n,
		sv,
		u,
		sr,
		v,
		br,
	}, nil
}
