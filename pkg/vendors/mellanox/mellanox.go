package mlxutils

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

// BlueField mode representation
type BlueFieldMode int

const (
	BluefieldDpu BlueFieldMode = iota
	BluefieldConnectXMode

	internalCPUPageSupplier   = "INTERNAL_CPU_PAGE_SUPPLIER"
	internalCPUEswitchManager = "INTERNAL_CPU_ESWITCH_MANAGER"
	internalCPUIbVporto       = "INTERNAL_CPU_IB_VPORT0"
	internalCPUOffloadEngine  = "INTERNAL_CPU_OFFLOAD_ENGINE"
	internalCPUModel          = "INTERNAL_CPU_MODEL"

	ecpf        = "ECPF"
	extHostPf   = "EXT_HOST_PF"
	embeddedCPU = "EMBEDDED_CPU"

	disabled = "DISABLED"
	enabled  = "ENABLED"

	VendorMellanox        = "15b3"
	DeviceBF2             = "a2d6"
	DeviceBF3             = "a2dc"
	PreconfiguredLinkType = "Preconfigured"
	UnknownLinkType       = "Unknown"
)

const (
	TotalVfs         = "NUM_OF_VFS"
	EnableSriov      = "SRIOV_EN"
	LinkTypeP1       = "LINK_TYPE_P1"
	LinkTypeP2       = "LINK_TYPE_P2"
	MellanoxVendorID = "15b3"
)

type MlxNic struct {
	EnableSriov bool
	TotalVfs    int
	LinkTypeP1  string
	LinkTypeP2  string
}

// MlxNicFwDataCache holds all cached mstconfig query results for a single NIC.
type MlxNicFwDataCache struct {
	FwCurrent     *MlxNic
	FwNext        *MlxNic
	BlueFieldMode BlueFieldMode
	BfModeQueried bool // whether BlueFieldMode was populated (since zero-value is a valid mode)
	queryData     *mstConfigData
}

// mstConfigData holds parsed current and next values from a successful query.
// Keeping the parsed attributes lets both accessors share one mstconfig call.
type mstConfigData struct {
	current map[string]string
	next    map[string]string
}

//go:generate ../../../bin/mockgen -destination mock/mock_mellanox.go -source mellanox.go
type MellanoxInterface interface {
	MstConfigReadData(string) (string, string, error)
	GetMellanoxBlueFieldMode(string) (BlueFieldMode, error)
	GetMlxNicFwData(pciAddress string) (current, next *MlxNic, err error)

	MlxConfigFW(attributesToChange map[string]MlxNic) error
	MlxResetFW(pciAddresses []string, mellanoxNicsStatus map[string]map[string]sriovnetworkv1.InterfaceExt) error

	// SetGeneration sets the NodeState generation for cache invalidation.
	// If the generation differs from the cached one, the cache is fully invalidated.
	SetGeneration(gen int64)
	// InvalidateCache clears the entire mstconfig query cache.
	InvalidateCache()
}

type mellanoxHelper struct {
	utils           utils.CmdInterface
	hostHelper      host.HostManagerInterface
	cacheGeneration int64
	cache           map[string]*MlxNicFwDataCache // keyed by PCI address
}

func New(utilsHelper utils.CmdInterface, hostHelper host.HostManagerInterface) MellanoxInterface {
	return &mellanoxHelper{
		utils:      utilsHelper,
		hostHelper: hostHelper,
		cache:      make(map[string]*MlxNicFwDataCache),
	}
}

func (m *mellanoxHelper) SetGeneration(gen int64) {
	if m.cacheGeneration != gen {
		log.Log.V(2).Info("mellanox cache: generation changed, invalidating cache",
			"old", m.cacheGeneration, "new", gen)
		m.InvalidateCache()
		m.cacheGeneration = gen
	}
}

func (m *mellanoxHelper) InvalidateCache() {
	m.cache = make(map[string]*MlxNicFwDataCache)
}

// invalidateCacheEntry removes the PCI address and its paired port because firmware is shared by the NIC.
func (m *mellanoxHelper) invalidateCacheEntry(pciAddress string) {
	delete(m.cache, pciAddress)
	if strings.HasSuffix(pciAddress, ".0") || strings.HasSuffix(pciAddress, ".1") {
		delete(m.cache, getOtherPortPCIAddress(pciAddress))
	}
}

func (m *mellanoxHelper) MstConfigReadData(pciAddress string) (string, string, error) {
	log.Log.Info("MstConfigReadData()", "device", pciAddress)
	args := []string{"-e", "-d", pciAddress, "q"}
	stdout, stderr, err := m.utils.RunCommand("mstconfig", args...)
	return stdout, stderr, err
}

// queryMstConfigData returns parsed values for a PCI address, querying the NIC only on a cache miss.
func (m *mellanoxHelper) queryMstConfigData(pciAddress string) (*mstConfigData, string, error) {
	if entry := m.cache[pciAddress]; entry != nil && entry.queryData != nil {
		return entry.queryData, "", nil
	}

	stdout, stderr, err := m.MstConfigReadData(pciAddress)
	if err != nil {
		return nil, stderr, err
	}

	attrs := []string{
		TotalVfs, EnableSriov, LinkTypeP1, LinkTypeP2,
		internalCPUPageSupplier, internalCPUEswitchManager, internalCPUIbVporto,
		internalCPUOffloadEngine, internalCPUModel,
	}
	current, next, err := ParseMstconfigOutput(stdout, attrs)
	if err != nil {
		return nil, stderr, fmt.Errorf("failed to parse mstconfig output: %w", err)
	}
	data := &mstConfigData{current: current, next: next}
	return data, stderr, nil
}

// cacheMstConfigData stores a query after an accessor has successfully parsed its result.
func (m *mellanoxHelper) cacheMstConfigData(pciAddress string, data *mstConfigData) {
	entry := m.cacheEntry(pciAddress)
	if entry.queryData == nil {
		entry.queryData = data
	}
}

// cacheEntry returns the cache entry for a PCI address, creating it if needed.
func (m *mellanoxHelper) cacheEntry(pciAddress string) *MlxNicFwDataCache {
	if entry := m.cache[pciAddress]; entry != nil {
		return entry
	}

	entry := &MlxNicFwDataCache{}
	m.cache[pciAddress] = entry
	return entry
}

func (m *mellanoxHelper) GetMellanoxBlueFieldMode(PciAddress string) (BlueFieldMode, error) {
	log.Log.V(2).Info("MellanoxBlueFieldMode(): checking mode for device", "device", PciAddress)
	if entry, ok := m.cache[PciAddress]; ok && entry.BfModeQueried {
		log.Log.V(2).Info("MellanoxBlueFieldMode(): returning cached result", "device", PciAddress)
		return entry.BlueFieldMode, nil
	}

	queryData, stderr, err := m.queryMstConfigData(PciAddress)
	if err != nil {
		log.Log.Error(err, "MellanoxBlueFieldMode(): failed to get mlx nic fw data", "stderr", stderr)
		return -1, fmt.Errorf("failed to get mlx nic fw data %w", err)
	}

	internalCPUPageSupplierstatus, exist := queryData.current[internalCPUPageSupplier]
	if !exist {
		m.invalidateCacheEntry(PciAddress)
		return -1, fmt.Errorf("failed to find %s in the mstconfig output command", internalCPUPageSupplier)
	}
	internalCPUEswitchManagerStatus, exist := queryData.current[internalCPUEswitchManager]
	if !exist {
		m.invalidateCacheEntry(PciAddress)
		return -1, fmt.Errorf("failed to find %s in the mstconfig output command", internalCPUEswitchManager)
	}
	internalCPUIbVportoStatus, exist := queryData.current[internalCPUIbVporto]
	if !exist {
		m.invalidateCacheEntry(PciAddress)
		return -1, fmt.Errorf("failed to find %s in the mstconfig output command", internalCPUIbVporto)
	}
	internalCPUOffloadEngineStatus, exist := queryData.current[internalCPUOffloadEngine]
	if !exist {
		m.invalidateCacheEntry(PciAddress)
		return -1, fmt.Errorf("failed to find %s in the mstconfig output command", internalCPUOffloadEngine)
	}
	internalCPUModelStatus, exist := queryData.current[internalCPUModel]
	if !exist {
		m.invalidateCacheEntry(PciAddress)
		return -1, fmt.Errorf("failed to find %s in the mstconfig output command", internalCPUModel)
	}

	var bfMode BlueFieldMode
	if strings.Contains(internalCPUPageSupplierstatus, ecpf) &&
		strings.Contains(internalCPUEswitchManagerStatus, ecpf) &&
		strings.Contains(internalCPUIbVportoStatus, ecpf) &&
		strings.Contains(internalCPUOffloadEngineStatus, enabled) &&
		strings.Contains(internalCPUModelStatus, embeddedCPU) {
		log.Log.V(2).Info("MellanoxBlueFieldMode(): device in DPU mode", "device", PciAddress)
		bfMode = BluefieldDpu
	} else if strings.Contains(internalCPUPageSupplierstatus, extHostPf) &&
		strings.Contains(internalCPUEswitchManagerStatus, extHostPf) &&
		strings.Contains(internalCPUIbVportoStatus, extHostPf) &&
		strings.Contains(internalCPUOffloadEngineStatus, disabled) &&
		strings.Contains(internalCPUModelStatus, embeddedCPU) {
		log.Log.V(2).Info("MellanoxBlueFieldMode(): device in ConnectX mode", "device", PciAddress)
		bfMode = BluefieldConnectXMode
	} else {
		log.Log.Error(nil, "MellanoxBlueFieldMode(): unknown device status",
			"device", PciAddress, "mstconfig-output", "unrecognized")
		m.invalidateCacheEntry(PciAddress)
		return -1, fmt.Errorf("MellanoxBlueFieldMode(): unknown device status for %s", PciAddress)
	}

	m.cacheMstConfigData(PciAddress, queryData)
	entry := m.cacheEntry(PciAddress)
	entry.BlueFieldMode = bfMode
	entry.BfModeQueried = true
	return bfMode, nil
}

func (m *mellanoxHelper) MlxResetFW(pciAddresses []string, mellanoxNicsStatus map[string]map[string]sriovnetworkv1.InterfaceExt) error {
	log.Log.Info("mellanox-plugin resetFW()")
	var errs []error
	for _, pciAddress := range pciAddresses {
		err := m.hostHelper.SetSriovNumVfs(pciAddress, 0)
		if err != nil {
			log.Log.Error(err, "failed to set SR-IOV number of VFs to 0 before firmware reset", "pciAddress", pciAddress)
			return err
		}
		if IsDualPort(pciAddress, mellanoxNicsStatus) {
			otherPortPCIAddress := getOtherPortPCIAddress(pciAddress)
			if err := m.hostHelper.SetSriovNumVfs(otherPortPCIAddress, 0); err != nil {
				log.Log.Error(err, "failed to set SR-IOV number of VFs to 0 before firmware reset", "pciAddress", otherPortPCIAddress)
				return err
			}
		}

		cmdArgs := []string{"-d", pciAddress, "--skip_driver", "-l", "3", "-y", "reset"}
		log.Log.Info("mellanox-plugin: resetFW()", "cmd-args", cmdArgs)
		_, stderr, err := m.utils.RunCommand("mstfwreset", cmdArgs...)
		if err != nil {
			log.Log.Error(err, "mellanox-plugin resetFW(): mstfwreset failed, continuing as best-effort", "stderr", stderr, "pciAddress", pciAddress)
		} else {
			m.invalidateCacheEntry(pciAddress)
		}
	}

	return kerrors.NewAggregate(errs)
}

func (m *mellanoxHelper) MlxConfigFW(attributesToChange map[string]MlxNic) error {
	log.Log.Info("mellanox-plugin configFW()")
	for pciAddr, fwArgs := range attributesToChange {
		bfMode, err := m.GetMellanoxBlueFieldMode(pciAddr)
		if err != nil {
			log.Log.V(2).Info("mellanox-plugin: configFW(): can't get DPU mode for NIC", "pciAddress", pciAddr)
		}
		if bfMode == BluefieldDpu {
			return errors.Errorf("NIC %s is in DPU mode. Firmware configuration changes are not supported in this mode.", pciAddr)
		}
		cmdArgs := []string{"-d", pciAddr, "-y", "set"}
		if fwArgs.EnableSriov {
			cmdArgs = append(cmdArgs, fmt.Sprintf("%s=True", EnableSriov))
		} else if fwArgs.TotalVfs == 0 {
			cmdArgs = append(cmdArgs, fmt.Sprintf("%s=False", EnableSriov))
		}
		if fwArgs.TotalVfs > -1 {
			cmdArgs = append(cmdArgs, fmt.Sprintf("%s=%d", TotalVfs, fwArgs.TotalVfs))
		}
		if len(fwArgs.LinkTypeP1) > 0 {
			cmdArgs = append(cmdArgs, fmt.Sprintf("%s=%s", LinkTypeP1, fwArgs.LinkTypeP1))
		}
		if len(fwArgs.LinkTypeP2) > 0 {
			cmdArgs = append(cmdArgs, fmt.Sprintf("%s=%s", LinkTypeP2, fwArgs.LinkTypeP2))
		}

		log.Log.V(2).Info("mellanox-plugin: configFW()", "cmd-args", cmdArgs)
		if len(cmdArgs) <= 4 {
			continue
		}
		_, stderr, err := m.utils.RunCommand("mstconfig", cmdArgs...)
		if err != nil {
			log.Log.Error(err, "mellanox-plugin configFW(): failed", "stderr", stderr)
			return err
		}
		m.invalidateCacheEntry(pciAddr)
	}
	return nil
}

func (m *mellanoxHelper) GetMlxNicFwData(pciAddress string) (current, next *MlxNic, err error) {
	log.Log.Info("mellanox-plugin getMlnxNicFwData()", "device", pciAddress)
	if entry, ok := m.cache[pciAddress]; ok && entry.FwCurrent != nil && entry.FwNext != nil {
		log.Log.V(2).Info("mellanox-plugin getMlnxNicFwData(): returning cached result", "device", pciAddress)
		currentCopy := *entry.FwCurrent
		nextCopy := *entry.FwNext
		return &currentCopy, &nextCopy, nil
	}

	queryData, stderr, err := m.queryMstConfigData(pciAddress)
	if err != nil {
		log.Log.Error(err, "mellanox-plugin getMlnxNicFwData(): failed", "stderr", stderr)
		return nil, nil, err
	}
	current, err = mlnxNicFromMap(queryData.current)
	if err != nil {
		log.Log.Error(err, "mellanox-plugin mlnxNicFromMap() for current mstconfig data failed")
		m.invalidateCacheEntry(pciAddress)
		return
	}
	next, err = mlnxNicFromMap(queryData.next)
	if err != nil {
		log.Log.Error(err, "mellanox-plugin mlnxNicFromMap() for next mstconfig data failed")
		m.invalidateCacheEntry(pciAddress)
		return
	}

	m.cacheMstConfigData(pciAddress, queryData)
	entry := m.cacheEntry(pciAddress)
	currentCopy := *current
	nextCopy := *next
	entry.FwCurrent = &currentCopy
	entry.FwNext = &nextCopy
	return
}

func ParseMstconfigOutput(mstOutput string, attributes []string) (fwCurrent, fwNext map[string]string, err error) {
	log.Log.Info("ParseMstconfigOutput()", "attributes", attributes)
	fwCurrent = map[string]string{}
	fwNext = map[string]string{}
	formatRegex := regexp.MustCompile(`(?P<Attribute>\w+)\s+(?P<Default>\S+)\s+(?P<Current>\S+)\s+(?P<Next>\S+)`)
	mstOutputLines := strings.Split(mstOutput, "\n")
	for _, attr := range attributes {
		for _, line := range mstOutputLines {
			if strings.Contains(line, attr) {
				regexResult := formatRegex.FindStringSubmatch(line)
				if len(regexResult) < 5 {
					return nil, nil, fmt.Errorf("malformed mstconfig output for attribute %s", attr)
				}
				fwCurrent[attr] = regexResult[3]
				fwNext[attr] = regexResult[4]
				break
			}
		}
	}
	return fwCurrent, fwNext, nil
}

func GetPciAddressPrefix(pciAddress string) string {
	return pciAddress[:len(pciAddress)-1]
}

func IsDualPort(pciAddress string, mellanoxNicsStatus map[string]map[string]sriovnetworkv1.InterfaceExt) bool {
	log.Log.Info("mellanox-plugin IsDualPort()", "address", pciAddress)
	pciAddressPrefix := GetPciAddressPrefix(pciAddress)
	return len(mellanoxNicsStatus[pciAddressPrefix]) > 1
}

// handleTotalVfs return required total VFs or max (required VFs for dual port NIC) and needReboot if totalVfs will change
func HandleTotalVfs(fwCurrent, fwNext, attrs *MlxNic, ifaceSpec sriovnetworkv1.Interface, isDualPort bool, mellanoxNicsSpec map[string]sriovnetworkv1.Interface) (
	totalVfs int, needReboot, changeWithoutReboot bool) {
	totalVfs = ifaceSpec.NumVfs
	if isDualPort {
		otherPortPCIAddress := getOtherPortPCIAddress(ifaceSpec.PciAddress)
		if otherIfaceSpec, ok := mellanoxNicsSpec[otherPortPCIAddress]; ok && otherIfaceSpec.NumVfs > totalVfs {
			totalVfs = otherIfaceSpec.NumVfs
		}
	}

	if ifaceSpec.ExternallyManaged {
		if totalVfs > fwCurrent.TotalVfs {
			log.Log.Error(nil, "The nic is externallyManaged and TotalVfs configured on the system is lower then requested VFs, failing configuration",
				"current", fwCurrent.TotalVfs, "requested", totalVfs)
			attrs.TotalVfs = totalVfs
			needReboot = true
			changeWithoutReboot = false
		}
		return
	}

	if fwCurrent.TotalVfs != totalVfs {
		log.Log.V(2).Info("Changing TotalVfs, needs reboot", "current", fwCurrent.TotalVfs, "requested", totalVfs)
		attrs.TotalVfs = totalVfs
		needReboot = true
	}
	if !needReboot && fwNext.TotalVfs != totalVfs {
		log.Log.V(2).Info("Changing TotalVfs to same as Next Boot value, doesn't require rebooting",
			"current", fwCurrent.TotalVfs, "next", fwNext.TotalVfs, "requested", totalVfs)
		attrs.TotalVfs = totalVfs
		changeWithoutReboot = true
	}
	return
}

// handleEnableSriov based on totalVfs it decide to disable (totalVfs=0) or enable (totalVfs changed from 0) sriov
// and need reboot if enableSriov will change
func HandleEnableSriov(totalVfs int, fwCurrent, fwNext, attrs *MlxNic) (needReboot, changeWithoutReboot bool) {
	if totalVfs == 0 && fwCurrent.EnableSriov {
		log.Log.V(2).Info("disabling Sriov, needs reboot")
		attrs.EnableSriov = false
		return true, false
	} else if totalVfs > 0 && !fwCurrent.EnableSriov {
		log.Log.V(2).Info("enabling sriov, needs reboot")
		attrs.EnableSriov = true
		return true, false
	} else if totalVfs > 0 && !fwNext.EnableSriov {
		attrs.EnableSriov = true
		return false, true
	}
	return false, false
}

// handleLinkType based on existing linkType and requested link
func HandleLinkType(pciPrefix string, fwData, attr *MlxNic,
	mellanoxNicsSpec map[string]sriovnetworkv1.Interface,
	mellanoxNicsStatus map[string]map[string]sriovnetworkv1.InterfaceExt) (bool, error) {
	needReboot := false
	pciAddress := pciPrefix + "0"
	if firstPortSpec, ok := mellanoxNicsSpec[pciAddress]; ok {
		ifaceStatus := getIfaceStatus(pciAddress, mellanoxNicsStatus)
		needChange, err := isLinkTypeRequireChange(firstPortSpec, ifaceStatus, fwData.LinkTypeP1)
		if err != nil {
			return false, err
		}
		if needChange {
			log.Log.V(2).Info("Changing LinkTypeP1, needs reboot",
				"from", fwData.LinkTypeP1, "to", firstPortSpec.LinkType)
			attr.LinkTypeP1 = firstPortSpec.LinkType
			needReboot = true
		}
	}
	pciAddress = pciPrefix + "1"
	if secondPortSpec, ok := mellanoxNicsSpec[pciAddress]; ok {
		ifaceStatus := getIfaceStatus(pciAddress, mellanoxNicsStatus)
		needChange, err := isLinkTypeRequireChange(secondPortSpec, ifaceStatus, fwData.LinkTypeP2)
		if err != nil {
			return false, err
		}
		if needChange {
			log.Log.V(2).Info("Changing LinkTypeP2, needs reboot",
				"from", fwData.LinkTypeP2, "to", secondPortSpec.LinkType)
			attr.LinkTypeP2 = secondPortSpec.LinkType
			needReboot = true
		}
	}
	return needReboot, nil
}

func mlnxNicFromMap(mstData map[string]string) (*MlxNic, error) {
	log.Log.Info("mellanox-plugin mlnxNicFromMap()", "data", mstData)
	fwData := &MlxNic{}
	if strings.Contains(mstData[EnableSriov], "True") {
		fwData.EnableSriov = true
	}
	i, err := strconv.Atoi(mstData[TotalVfs])
	if err != nil {
		return nil, err
	}
	fwData.TotalVfs = i
	fwData.LinkTypeP1 = getLinkType(mstData[LinkTypeP1])
	if linkTypeP2, ok := mstData[LinkTypeP2]; ok {
		fwData.LinkTypeP2 = getLinkType(linkTypeP2)
	}
	return fwData, nil
}

func getLinkType(linkType string) string {
	log.Log.Info("mellanox-plugin getLinkType()", "link-type", linkType)
	if strings.Contains(linkType, consts.LinkTypeETH) {
		return consts.LinkTypeETH
	} else if strings.Contains(linkType, consts.LinkTypeIB) {
		return consts.LinkTypeIB
	} else if len(linkType) > 0 {
		log.Log.Error(nil, "mellanox-plugin getLinkType(): link type is not one of [ETH, IB], treating as unknown",
			"link-type", linkType)
		return UnknownLinkType
	}
	log.Log.Info("mellanox-plugin getLinkType(): LINK_TYPE_P* attribute was not found, treating as preconfigured link type")
	return PreconfiguredLinkType
}

func isLinkTypeRequireChange(iface sriovnetworkv1.Interface, ifaceStatus sriovnetworkv1.InterfaceExt, fwLinkType string) (bool, error) {
	log.Log.Info("mellanox-plugin isLinkTypeRequireChange()", "device", iface.PciAddress)
	if iface.LinkType != "" && !strings.EqualFold(ifaceStatus.LinkType, iface.LinkType) {
		if !strings.EqualFold(iface.LinkType, consts.LinkTypeETH) && !strings.EqualFold(iface.LinkType, consts.LinkTypeIB) {
			return false, fmt.Errorf("mellanox-plugin OnNodeStateChange(): Not supported link type: %s,"+
				" supported link types: [eth, ETH, ib, and IB]", iface.LinkType)
		}
		if fwLinkType == UnknownLinkType {
			return false, fmt.Errorf("mellanox-plugin OnNodeStateChange(): Unknown link type: %s", fwLinkType)
		}
		if fwLinkType == PreconfiguredLinkType {
			return false, fmt.Errorf("mellanox-plugin OnNodeStateChange(): Network card %s does not support link type change", iface.PciAddress)
		}
		return true, nil
	}
	return false, nil
}

func getIfaceStatus(pciAddress string, mellanoxNicsStatus map[string]map[string]sriovnetworkv1.InterfaceExt) sriovnetworkv1.InterfaceExt {
	pciAddressPrefix := GetPciAddressPrefix(pciAddress)
	return mellanoxNicsStatus[pciAddressPrefix][pciAddress]
}

func getOtherPortPCIAddress(pciAddress string) string {
	log.Log.Info("mellanox-plugin getOtherPortPCIAddress()", "device", pciAddress)
	if strings.HasSuffix(pciAddress, "0") {
		return pciAddress[:len(pciAddress)-1] + "1"
	}
	return pciAddress[:len(pciAddress)-1] + "0"
}
