package vars

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
)

var (
	// Namespace contains k8s namespace
	Namespace string

	// ClusterType used by the operator to specify the platform it's running on
	// supported values [kubernetes,openshift]
	ClusterType consts.ClusterType

	// DevMode controls the developer mode in the operator
	// developer mode allows the operator to use un-supported network devices
	DevMode bool

	// NodeName initialize and used by the config-daemon to identify the node it's running on
	NodeName = ""

	// Destdir destination directory for the checkPoint file on the host
	Destdir string

	// PlatformType specify the current platform the operator is running on
	PlatformType = consts.Baremetal
	// PlatformsMap contains supported platforms for virtual VF
	PlatformsMap = map[string]consts.PlatformTypes{
		"openstack": consts.VirtualOpenStack,
		"aws":       consts.AWS,
	}

	// SupportedVfIds list of supported virtual functions IDs
	// loaded on daemon initialization by reading the supported-nics configmap
	SupportedVfIds []string

	// DpdkDrivers supported DPDK drivers for virtual functions
	DpdkDrivers = []string{"igb_uio", "vfio-pci", "uio_pci_generic"}

	// InChroot global variable to mark that the config-daemon code is inside chroot on the host file system
	InChroot atomic.Bool

	// HostFSLock serializes syscall.Chroot windows with operations that open
	// host paths (file-log reconfigure). Held for the duration of Chroot().
	HostFSLock sync.Mutex

	// beforeChroot is an optional callback invoked by utils.Chroot before entering
	// the chroot window (e.g. flush async file logs). Set via SetBeforeChroot.
	beforeChroot atomic.Pointer[func()]

	// UsingSystemdMode global variable to mark the config-daemon is running on systemd mode
	UsingSystemdMode = false

	// ParallelNicConfig global variable to perform NIC configuration in parallel
	ParallelNicConfig = false

	// ManageSoftwareBridges global variable which reflects state of manageSoftwareBridges feature
	ManageSoftwareBridges = false

	// FilesystemRoot used by test to mock interactions with filesystem
	FilesystemRoot = ""

	// OVSDBSocketPath path to OVSDB socket
	OVSDBSocketPath = "unix:///var/run/openvswitch/db.sock"

	//Cluster variables
	Config *rest.Config    = nil
	Scheme *runtime.Scheme = nil

	// PfPhysPortNameRe regex to find switchdev devices on the host
	PfPhysPortNameRe = regexp.MustCompile(`p\d+`)

	// ResourcePrefix is the device plugin prefix we use to expose the devices to the nodes
	ResourcePrefix = ""

	// DisableablePlugins contains which plugins can be disabled in sriov config daemon
	DisableablePlugins = map[string]struct{}{"mellanox": {}}

	// DisableDrain controls if the daemon will drain the node before configuration
	DisableDrain = false

	// FeatureGates interface to interact with feature gates
	FeatureGate featuregate.FeatureGate

	// ErrOperationNotSupportedByPlatform is returned when a platform operation is not supported by the platform implementation.
	ErrOperationNotSupportedByPlatform = errors.New("operation not supported by the platform")

	// UseExternalDrainer controls if SRIOV operator will use an external drainer
	// for draining nodes or its internal drain controller (default)
	UseExternalDrainer bool

	// logCfg: effective file-log settings (use GetLogCfg / SetLogCfg).
	logCfg atomic.Value
)

// LogFileSettings is the daemon's on-disk log config.
type LogFileSettings struct {
	Enabled    bool
	MaxSizeMB  int
	MaxFiles   int
	MaxAgeDays int
	Compress   bool
	HostPath   string
}

// DefaultLogCfg returns production defaults.
func DefaultLogCfg() LogFileSettings {
	return LogFileSettings{
		Enabled:    true,
		MaxSizeMB:  100,
		MaxFiles:   5,
		MaxAgeDays: 30,
		Compress:   true,
		HostPath:   "/var/log/sriov-network-config-daemon",
	}
}

// GetLogCfg returns the current file-log settings.
func GetLogCfg() LogFileSettings { return logCfg.Load().(LogFileSettings) }

// SetLogCfg replaces the current file-log settings.
func SetLogCfg(cfg LogFileSettings) { logCfg.Store(cfg) }

// SetBeforeChroot registers a callback to run immediately before syscall.Chroot.
// Pass nil to clear. Used by the file logger to flush buffered entries.
func SetBeforeChroot(fn func()) {
	if fn == nil {
		beforeChroot.Store(nil)
		return
	}
	beforeChroot.Store(&fn)
}

// RunBeforeChroot invokes the registered before-chroot callback, if any.
func RunBeforeChroot() {
	if p := beforeChroot.Load(); p != nil && *p != nil {
		(*p)()
	}
}

func init() {
	logCfg.Store(DefaultLogCfg())

	Namespace = os.Getenv("NAMESPACE")

	ClusterType = consts.ClusterType(os.Getenv("CLUSTER_TYPE"))

	DevMode = false
	mode := os.Getenv("DEV_MODE")
	if mode == "TRUE" {
		DevMode = true
	}

	Destdir = "/host/tmp"
	destdir := os.Getenv("DEST_DIR")
	if destdir != "" {
		Destdir = destdir
	}

	ResourcePrefix = os.Getenv("RESOURCE_PREFIX")

	FeatureGate = featuregate.New()

	UseExternalDrainer = os.Getenv("USE_EXTERNAL_DRAINER") == "true"
}

func GetPlatformType(providerID string) consts.PlatformTypes {
	for key, pType := range PlatformsMap {
		if strings.Contains(strings.ToLower(providerID), strings.ToLower(key)) {
			return pType
		}
	}
	return consts.Baremetal
}
