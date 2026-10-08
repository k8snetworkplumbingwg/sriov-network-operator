/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package log

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	zzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

const (
	LogFileName                    = "config-daemon.log"
	Component                      = "sriov-network-config-daemon"
	SubsystemPersistentFileLogging = "persistent-file-logging"

	lumberjackBackupTimeFormat = "2006-01-02T15-04-05.000"
	lumberjackCompressSuffix   = ".gz"
	startupHeaderWidth         = 80
)

// Options stores controller-runtime (zap) log config.
var Options = &zap.Options{
	Development: true,
	// we dont log with panic level, so this essentially
	// disables stacktrace, for now, it avoids un-needed clutter in logs
	StacktraceLevel: zapcore.DPanicLevel,
	TimeEncoder:     zapcore.RFC3339NanoTimeEncoder,
	Level:           zzap.NewAtomicLevelAt(zapcore.InfoLevel),
	// log caller (file and line number) in "caller" key
	EncoderConfigOptions: []zap.EncoderConfigOption{func(ec *zapcore.EncoderConfig) { ec.CallerKey = "caller" }},
	// Skip 1 for controller-runtime logr - zapr frame. Config-daemon adds +1 when the file tee is installed.
	ZapOpts: []zzap.Option{zzap.AddCaller(), zzap.AddCallerSkip(1)},
}

// fileLogState holds all resources for the active file logger.
type fileLogState struct {
	cfg  vars.LogFileSettings
	path string
	core zapcore.Core
	bws  *zapcore.BufferedWriteSyncer
	lj   *lumberjack.Logger
}

// activeState is atomically swapped by InitLogWithFile / CloseFileLogger.
var activeState atomic.Pointer[fileLogState]

// globalArmedCore is a permanent zapcore.Core installed into the tee by InitLog().
var globalArmedCore = &globalArmedFileCore{}

// BindFlags binds controller-runtime logging flags to provided flag Set.
func BindFlags(fs *flag.FlagSet) {
	Options.BindFlags(fs)
}

// InitLog initializes controller-runtime log (zap log) for the config-daemon.
// It installs a permanent tee so log entries also route to the rotating file
// writer once InitLogWithFile arms it.
func InitLog() {
	base := zap.New(zap.UseFlagOptions(Options))

	sink := base.GetSink()
	underlier, ok := sink.(zapr.Underlier)
	if !ok {
		log.SetLogger(base)
		return
	}
	existingZap := underlier.GetUnderlying()

	combined := existingZap.WithOptions(
		zzap.WrapCore(func(core zapcore.Core) zapcore.Core {
			return zapcore.NewTee(core, globalArmedCore)
		}),
		zzap.AddCallerSkip(1),
	)
	log.SetLogger(zapr.NewLogger(combined))
}

// InitLogConsole initializes console-only logging for the operator and other
// components that do not write persistent host log files.
func InitLogConsole() {
	log.SetLogger(zap.New(zap.UseFlagOptions(Options)))
}

// InitLogWithFile applies vars.GetLogCfg() to the rotating file logger.
func InitLogWithFile() error {
	return applyFileLogger(vars.GetLogCfg(), "", false)
}

func applyFileLogger(cfg vars.LogFileSettings, overridePath string, pathOverridden bool) error {
	if !cfg.Enabled && !pathOverridden {
		closeFileLoggerInner()
		announceLogDisabled()
		return nil
	}

	logFilePath := overridePath
	if !pathOverridden {
		if cfg.HostPath == "" {
			err := fmt.Errorf("log HostPath must not be empty")
			logFileStatusFailure("resolve path", err, logFilePath)
			return err
		}
		logFilePath = filepath.Join(cfg.HostPath, consts.ConfigDaemonLogSubDir, LogFileName)
	}

	if err := validateLogFilePath(logFilePath); err != nil {
		logFileStatusFailure("validate path", err, logFilePath)
		return err
	}

	maxSizeMB := clampLogParam(cfg.MaxSizeMB, consts.LogCfgMaxSizeMBMin, consts.LogCfgMaxSizeMBMax, consts.LogCfgMaxSizeMBDefault)
	maxFiles := clampLogParam(cfg.MaxFiles, consts.LogCfgMaxFilesMin, consts.LogCfgMaxFilesMax, consts.LogCfgMaxFilesDefault)
	maxAgeDays := clampLogParam(cfg.MaxAgeDays, consts.LogCfgMaxAgeDaysMin, consts.LogCfgMaxAgeDaysMax, consts.LogCfgMaxAgeDaysDefault)

	logDir := filepath.Dir(logFilePath)
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		err = fmt.Errorf("create log directory %s: %w", logDir, err)
		logFileStatusFailure("create log directory", err, logFilePath)
		return err
	}

	lj := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    maxSizeMB,
		MaxBackups: maxFiles,
		MaxAge:     maxAgeDays,
		Compress:   cfg.Compress,
	}

	bws := &zapcore.BufferedWriteSyncer{
		WS:            zapcore.AddSync(lj),
		Size:          256 * 1024,
		FlushInterval: 30 * time.Second,
	}

	encCfg := zzap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.RFC3339NanoTimeEncoder
	core := zapcore.NewCore(zapcore.NewConsoleEncoder(encCfg), bws, Options.Level)

	newState := &fileLogState{cfg: cfg, path: logFilePath, core: core, bws: bws, lj: lj}
	oldState := activeState.Load()
	if oldState != nil {
		_ = oldState.bws.Stop()
		_ = oldState.lj.Close()
	}

	pruneExtraBackups(logFilePath, maxFiles)

	activeState.Store(newState)

	// Deduplicate by comparing with OLD state (nil on first startup)
	announceLogEnabled(oldState, logFilePath, maxSizeMB, maxFiles, maxAgeDays, cfg.Compress)
	SyncFileLogger()
	return nil
}

// SyncFileLogger flushes all buffered file-log entries to disk.
func SyncFileLogger() {
	if s := activeState.Load(); s != nil {
		_ = s.bws.Sync()
	}
}

// CloseFileLogger flushes and closes the active file logger.
func CloseFileLogger() {
	closeFileLoggerInner()
}

// IsFileLoggerActive reports whether a rotating file logger is currently armed.
func IsFileLoggerActive() bool {
	return activeState.Load() != nil
}

func closeFileLoggerInner() {
	if old := activeState.Swap(nil); old != nil {
		_ = old.bws.Stop()
		_ = old.lj.Close()
	}
}

// globalArmedFileCore — permanent no-op core that activates when activeState is set
type globalArmedFileCore struct {
	extraFields []zapcore.Field
}

func (a *globalArmedFileCore) Enabled(level zapcore.Level) bool {
	s := activeState.Load()
	return s != nil && s.core.Enabled(level)
}

func (a *globalArmedFileCore) With(fields []zapcore.Field) zapcore.Core {
	combined := make([]zapcore.Field, len(a.extraFields)+len(fields))
	copy(combined, a.extraFields)
	copy(combined[len(a.extraFields):], fields)
	return &globalArmedFileCore{extraFields: combined}
}

func (a *globalArmedFileCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if a.Enabled(entry.Level) {
		return ce.AddCore(entry, a)
	}
	return ce
}

func (a *globalArmedFileCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	s := activeState.Load()
	if s == nil {
		return nil
	}
	allFields := fields
	if len(a.extraFields) > 0 {
		allFields = make([]zapcore.Field, len(fields)+len(a.extraFields))
		copy(allFields, fields)
		copy(allFields[len(fields):], a.extraFields)
	}
	return s.core.Write(entry, allFields)
}

func (a *globalArmedFileCore) Sync() error {
	s := activeState.Load()
	if s == nil {
		return nil
	}
	return s.core.Sync()
}

// SetLogLevel provides conversion from the operators LogLevel value and sets
// the current logging level accordingly.
func SetLogLevel(operatorLevel int) {
	newLevel := operatorToZapLevel(operatorLevel)
	currLevel := Options.Level.(zzap.AtomicLevel).Level()
	if newLevel != currLevel {
		log.Log.Info("Set log verbose level", "new-level", operatorLevel, "current-level", zapToOperatorLevel(currLevel))
		Options.Level.(zzap.AtomicLevel).SetLevel(newLevel)
	}
}

// GetLogLevel returns the current operator log level.
func GetLogLevel() int {
	return zapToOperatorLevel(Options.Level.(zzap.AtomicLevel).Level())
}

func zapToOperatorLevel(zapLevel zapcore.Level) int {
	return int(zapLevel) * -1
}

func operatorToZapLevel(operatorLevel int) zapcore.Level {
	return zapcore.Level(operatorLevel * -1)
}

// validateLogFilePath rejects clearly unsafe paths (empty, root).
func validateLogFilePath(logFilePath string) error {
	if logFilePath == "" {
		return fmt.Errorf("logFilePath must not be empty")
	}
	cleaned := filepath.Clean(logFilePath)
	if cleaned == "/" || cleaned == "." {
		return fmt.Errorf("logFilePath resolves to an unsafe root path: %s", logFilePath)
	}
	return nil
}

func clampLogParam(v, lo, hi, dflt int) int {
	if v < lo || v > hi {
		return dflt
	}
	return v
}

// announceLogDisabled prints a banner and log entry when file logging is disabled.
func announceLogDisabled() {
	separator := strings.Repeat("=", startupHeaderWidth)
	nodeName := statusNodeName()
	ts := time.Now().Format(time.RFC3339)

	fmt.Fprintf(os.Stderr, "%s\n", separator)
	fmt.Fprintf(os.Stderr, "%s/%s: persistent file logging disabled\n", Component, SubsystemPersistentFileLogging)
	fmt.Fprintf(os.Stderr, "Node: %s | Start time: %s\n", nodeName, ts)
	fmt.Fprintf(os.Stderr, "%s\n", separator)
	_ = os.Stderr.Sync() // Flush stderr to ensure banner appears before structured log

	statusLogger().Info("persistent file logging disabled", "status", "disabled")
}

// announceLogEnabled prints a banner and log entry when file logging is enabled.
// Deduplicates announcements by comparing with oldState (state before this enable).
func announceLogEnabled(oldState *fileLogState, logFilePath string, maxSizeMB, maxFiles, maxAgeDays int, compress bool) {
	// Deduplicate enable announcements by comparing with previous state
	if oldState != nil {
		if oldState.path == logFilePath &&
			oldState.cfg.MaxSizeMB == maxSizeMB &&
			oldState.cfg.MaxFiles == maxFiles &&
			oldState.cfg.MaxAgeDays == maxAgeDays &&
			oldState.cfg.Compress == compress {
			return
		}
	}

	separator := strings.Repeat("=", startupHeaderWidth)
	nodeName := statusNodeName()
	ts := time.Now().Format(time.RFC3339)

	// Print banner to stderr (pod logs)
	fmt.Fprintf(os.Stderr, "%s\n", separator)
	fmt.Fprintf(os.Stderr, "%s/%s: persistent file logging enabled\n", Component, SubsystemPersistentFileLogging)
	fmt.Fprintf(os.Stderr, "Node: %s | Start time: %s\n", nodeName, ts)
	fmt.Fprintf(os.Stderr, "Path: %s | maxSizeMB=%d maxFiles=%d maxAgeDays=%d compress=%t\n",
		logFilePath, maxSizeMB, maxFiles, maxAgeDays, compress)
	fmt.Fprintf(os.Stderr, "%s\n", separator)
	_ = os.Stderr.Sync()

	// Write startup header directly to log file for consistency with device-plugin
	writeStartupHeaderToFile(logFilePath, nodeName, ts, maxSizeMB, maxFiles, maxAgeDays, compress)

	statusLogger().Info("persistent file logging enabled",
		"status", "enabled",
		"path", logFilePath,
		"maxSizeMB", maxSizeMB,
		"maxFiles", maxFiles,
		"maxAgeDays", maxAgeDays,
		"compress", compress,
	)
}

// logFileStatusFailure prints a banner and structured error when enable fails.
func logFileStatusFailure(stage string, err error, logFilePath string) {
	separator := strings.Repeat("=", startupHeaderWidth)
	nodeName := statusNodeName()
	ts := time.Now().Format(time.RFC3339)

	fmt.Fprintf(os.Stderr, "%s\n", separator)
	fmt.Fprintf(os.Stderr, "%s/%s: persistent file logging failed\n", Component, SubsystemPersistentFileLogging)
	fmt.Fprintf(os.Stderr, "Node: %s | Start time: %s\n", nodeName, ts)
	if logFilePath != "" {
		fmt.Fprintf(os.Stderr, "Stage: %s | Path: %s | Error: %v\n", stage, logFilePath, err)
	} else {
		fmt.Fprintf(os.Stderr, "Stage: %s | Error: %v\n", stage, err)
	}
	fmt.Fprintf(os.Stderr, "%s\n", separator)
	_ = os.Stderr.Sync() // Flush stderr to ensure banner appears before structured log

	statusLogger().Error(err, "persistent file logging failed",
		"status", "failed",
		"stage", stage,
		"path", logFilePath,
	)
}

func statusLogger() logr.Logger {
	return log.Log.WithName(SubsystemPersistentFileLogging).WithValues(
		"component", Component,
		"node", statusNodeName(),
	)
}

func statusNodeName() string {
	if vars.NodeName != "" {
		return vars.NodeName
	}
	return "unknown"
}

// writeStartupHeaderToFile writes a startup banner directly to the log file
// for consistency with device-plugin logging format.
func writeStartupHeaderToFile(logFilePath, nodeName, ts string, maxSizeMB, maxFiles, maxAgeDays int, compress bool) {
	s := activeState.Load()
	if s == nil || s.lj == nil {
		return
	}
	separator := strings.Repeat("=", startupHeaderWidth)
	header := fmt.Sprintf("%s\n%s/%s: persistent file logging enabled\nNode: %s | Start time: %s\nPath: %s | maxSizeMB=%d maxFiles=%d maxAgeDays=%d compress=%t\n%s\n",
		separator, Component, SubsystemPersistentFileLogging, nodeName, ts,
		logFilePath, maxSizeMB, maxFiles, maxAgeDays, compress, separator)
	_, _ = s.lj.Write([]byte(header))
}

// pruneExtraBackups immediately drops lumberjack backups beyond maxBackups.
func pruneExtraBackups(logFilePath string, maxBackups int) {
	if maxBackups <= 0 {
		return
	}
	dir := filepath.Dir(logFilePath)
	base := filepath.Base(logFilePath)
	ext := filepath.Ext(base)
	prefix := base[:len(base)-len(ext)] + "-"

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	type backup struct {
		name string
		ts   time.Time
		key  string
	}
	var backups []backup
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if ts, err := timeFromBackupName(name, prefix, ext); err == nil {
			backups = append(backups, backup{name: name, ts: ts, key: name})
			continue
		}
		if ts, err := timeFromBackupName(name, prefix, ext+lumberjackCompressSuffix); err == nil {
			backups = append(backups, backup{name: name, ts: ts, key: strings.TrimSuffix(name, lumberjackCompressSuffix)})
		}
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].ts.After(backups[j].ts)
	})

	preserved := map[string]struct{}{}
	var pruned int
	for _, b := range backups {
		if _, seen := preserved[b.key]; seen {
			continue
		}
		if len(preserved) >= maxBackups {
			_ = os.Remove(filepath.Join(dir, b.name))
			pruned++
			continue
		}
		preserved[b.key] = struct{}{}
	}
	if pruned > 0 {
		statusLogger().Info("pruned old log backups",
			"status", "pruned",
			"dir", dir,
			"pruned", pruned,
			"maxFiles", maxBackups,
		)
		fmt.Fprintf(os.Stderr, "%s/%s: pruned %d old log backup(s) in %s (maxFiles=%d)\n",
			Component, SubsystemPersistentFileLogging, pruned, dir, maxBackups)
	}
}

func timeFromBackupName(filename, prefix, ext string) (time.Time, error) {
	if !strings.HasPrefix(filename, prefix) || !strings.HasSuffix(filename, ext) {
		return time.Time{}, fmt.Errorf("not a backup")
	}
	ts := filename[len(prefix) : len(filename)-len(ext)]
	return time.Parse(lumberjackBackupTimeFormat, ts)
}

// initLogWithFileAt applies cfg to an explicit path.
func initLogWithFileAt(logFilePath string, cfg vars.LogFileSettings) error {
	cfg.Enabled = true
	return applyFileLogger(cfg, logFilePath, true)
}

// currentLumberjack returns the active lumberjack.Logger instance.
func currentLumberjack() *lumberjack.Logger {
	if s := activeState.Load(); s != nil {
		return s.lj
	}
	return nil
}
