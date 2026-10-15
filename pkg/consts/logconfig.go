package consts

// LogConfig validation constants — shared between api/v1 and pkg/log.
const (
	// LogCfgMaxSizeMB constraints
	LogCfgMaxSizeMBMin     = 1
	LogCfgMaxSizeMBMax     = 1024
	LogCfgMaxSizeMBDefault = 100

	// LogCfgMaxFiles constraints
	LogCfgMaxFilesMin     = 1
	LogCfgMaxFilesMax     = 20
	LogCfgMaxFilesDefault = 5

	// LogCfgMaxAgeDays constraints
	LogCfgMaxAgeDaysMin     = 0
	LogCfgMaxAgeDaysMax     = 365
	LogCfgMaxAgeDaysDefault = 30

	// LogCfgEnabledDefault is the default for persistent file logging (off until explicitly enabled).
	LogCfgEnabledDefault = false
	// LogCfgCompressDefault controls whether rotated log files are gzip-compressed.
	LogCfgCompressDefault = true
	// LogHostPathRoot is the default base host log directory for persistent logging.
	LogHostPathRoot = "/var/log"

	// Host log subdirectory for sriov-network-config-daemon
	ConfigDaemonLogSubDir = "sriov-network-config-daemon"
	// Host log subdirectory for sriov-network-device-plugin
	DevicePluginLogSubDir = "sriovdp"
)
