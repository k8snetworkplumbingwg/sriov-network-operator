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
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/zapr"
	zzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

// LogFileName is the rotating log file written under LogConfig.HostPath.
const LogFileName = "config-daemon.log"

const (
	lumberjackBackupTimeFormat = "2006-01-02T15-04-05.000"
	lumberjackCompressSuffix   = ".gz"
)

type appliedFileLogState struct {
	cfg  vars.LogFileSettings
	path string
}

// appliedFileLog is the last successfully applied file-log config (under HostFSLock).
var appliedFileLog atomic.Pointer[appliedFileLogState]

// Options stores controller-runtime (zap) log config
var Options = &zap.Options{
	Development: true,
	// we dont log with panic level, so this essentially
	// disables stacktrace, for now, it avoids un-needed clutter in logs
	StacktraceLevel: zapcore.DPanicLevel,
	TimeEncoder:     zapcore.RFC3339NanoTimeEncoder,
	Level:           zzap.NewAtomicLevelAt(zapcore.InfoLevel),
	// log caller (file and line number) in "caller" key
	EncoderConfigOptions: []zap.EncoderConfigOption{func(ec *zapcore.EncoderConfig) { ec.CallerKey = "caller" }},
	ZapOpts:              []zzap.Option{zzap.AddCaller(), zzap.AddCallerSkip(1)},
}

// globalArmedCore is a permanent zapcore.Core installed into the tee by InitLog().
// It is a no-op when unarmed (activeAsyncCore == nil) and routes log entries to the
// current asyncFileCore when armed by InitLogWithFile().
var globalArmedCore = &armedFileCore{}

// activeAsyncCore is atomically swapped by InitLogWithFile / CloseFileLogger.
// armedFileCore reads this on every Check/Write/Enabled call so all logger
// clones created via With() always see the current file writer.
var activeAsyncCore atomic.Pointer[asyncFileCore]

// BindFlags binds controller-runtime logging flags to provided flag Set
func BindFlags(fs *flag.FlagSet) {
	Options.BindFlags(fs)
}

// InitLog initializes controller-runtime log (zap log).
// It installs a permanent tee that routes every log entry to both the console
// sink and the global armed file core (initially a no-op).
func InitLog() {
	vars.SetBeforeChroot(SyncFileLogger)

	base := zap.New(zap.UseFlagOptions(Options))

	sink := base.GetSink()
	underlier, ok := sink.(zapr.Underlier)
	if !ok {
		log.SetLogger(base)
		return
	}
	existingZap := underlier.GetUnderlying()

	combined := existingZap.WithOptions(zzap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewTee(core, globalArmedCore)
	}))
	log.SetLogger(zapr.NewLogger(combined))
}

// InitLogWithFile applies vars.GetLogCfg() to the rotating file logger.
// It validates the path, creates the directory, swaps the writer atomically,
// prunes extra backups immediately, and removes files from a previous HostPath.
// Safe to call again (no-op when already applied). Serialized against chroot
// via vars.HostFSLock so reconfigure cannot run inside a chroot window.
func InitLogWithFile() error {
	vars.HostFSLock.Lock()
	defer vars.HostFSLock.Unlock()
	return applyFileLoggerLocked(vars.GetLogCfg(), "", false)
}

// initLogWithFileAt applies cfg to an explicit path. Used by tests that write
// into a temp dir instead of the host /var/log path.
func initLogWithFileAt(logFilePath string, cfg vars.LogFileSettings) error {
	vars.HostFSLock.Lock()
	defer vars.HostFSLock.Unlock()
	cfg.Enabled = true
	return applyFileLoggerLocked(cfg, logFilePath, true)
}

func applyFileLoggerLocked(cfg vars.LogFileSettings, overridePath string, pathOverridden bool) error {
	if !cfg.Enabled && !pathOverridden {
		if alreadyApplied(cfg, "") {
			return nil
		}
		closeFileLoggerLocked()
		appliedFileLog.Store(&appliedFileLogState{cfg: cfg})
		fmt.Fprintf(os.Stderr, "sriov-operator: persistent file logging disabled\n")
		return nil
	}

	logFilePath := overridePath
	if !pathOverridden {
		if cfg.HostPath == "" {
			return fmt.Errorf("log HostPath must not be empty")
		}
		logFilePath = utils.GetHostExtensionPath(filepath.Join(cfg.HostPath, LogFileName))
	}

	if alreadyApplied(cfg, logFilePath) {
		return nil
	}

	if err := validateLogFilePath(logFilePath); err != nil {
		return err
	}

	maxSizeMB := clampLogParam(cfg.MaxSizeMB, logCfgMaxSizeMBMin, logCfgMaxSizeMBMax, logCfgMaxSizeMBDefault)
	maxFiles := clampLogParam(cfg.MaxFiles, logCfgMaxFilesMin, logCfgMaxFilesMax, logCfgMaxFilesDefault)
	maxAgeDays := clampLogParam(cfg.MaxAgeDays, logCfgMaxAgeDaysMin, logCfgMaxAgeDaysMax, logCfgMaxAgeDaysDefault)

	logDir := filepath.Dir(logFilePath)
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return fmt.Errorf("failed to create log directory %s: %w", logDir, err)
	}

	oldPath := ""
	if w := currentFileWriter(); w != nil {
		oldPath = w.lj.Filename
	}

	lj := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    maxSizeMB,
		MaxBackups: maxFiles,
		MaxAge:     maxAgeDays,
		Compress:   cfg.Compress,
	}

	encCfg := zzap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.RFC3339NanoTimeEncoder
	encoder := zapcore.NewJSONEncoder(encCfg)

	fw := newFileWriter(lj, encoder.Clone())

	fileCore := &asyncFileCore{
		encoder:      encoder,
		levelEnabler: Options.Level,
		writer:       fw,
	}

	if old := activeAsyncCore.Swap(fileCore); old != nil {
		old.writer.close()
	}

	pruneExtraBackups(logFilePath, maxFiles)
	cleanupOldLogDir(oldPath, logFilePath)

	appliedFileLog.Store(&appliedFileLogState{cfg: cfg, path: logFilePath})
	fmt.Fprintf(os.Stderr, "sriov-operator: persistent file logging enabled (path=%s maxSizeMB=%d maxFiles=%d maxAgeDays=%d)\n",
		logFilePath, maxSizeMB, maxFiles, maxAgeDays)
	return nil
}

func alreadyApplied(cfg vars.LogFileSettings, path string) bool {
	prev := appliedFileLog.Load()
	if prev == nil || prev.cfg != cfg || prev.path != path {
		return false
	}
	if cfg.Enabled {
		return activeAsyncCore.Load() != nil
	}
	return activeAsyncCore.Load() == nil
}

func validateLogFilePath(logFilePath string) error {
	if logFilePath == "" {
		return fmt.Errorf("logFilePath must not be empty")
	}
	cleaned := filepath.Clean(logFilePath)
	if cleaned == "/" || cleaned == "." {
		return fmt.Errorf("logFilePath resolves to an unsafe root path: %s", logFilePath)
	}
	if strings.Contains(logFilePath, "..") {
		return fmt.Errorf("logFilePath must not contain path traversal (..): %s", logFilePath)
	}
	return nil
}

// pruneExtraBackups immediately drops lumberjack backups beyond maxBackups.
// lumberjack itself only mills on rotation, so shrinking MaxFiles would otherwise
// leave extra files until the current log wraps.
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
	for _, b := range backups {
		if _, seen := preserved[b.key]; seen {
			continue
		}
		if len(preserved) >= maxBackups {
			_ = os.Remove(filepath.Join(dir, b.name))
			continue
		}
		preserved[b.key] = struct{}{}
	}
}

func timeFromBackupName(filename, prefix, ext string) (time.Time, error) {
	if !strings.HasPrefix(filename, prefix) || !strings.HasSuffix(filename, ext) {
		return time.Time{}, fmt.Errorf("not a backup")
	}
	ts := filename[len(prefix) : len(filename)-len(ext)]
	return time.Parse(lumberjackBackupTimeFormat, ts)
}

func cleanupOldLogDir(oldFilePath, newFilePath string) {
	if oldFilePath == "" || newFilePath == "" {
		return
	}
	oldDir := filepath.Dir(oldFilePath)
	newDir := filepath.Dir(newFilePath)
	if oldDir == newDir || oldDir == "" || oldDir == "." {
		return
	}
	base := strings.TrimSuffix(LogFileName, filepath.Ext(LogFileName))
	matches, _ := filepath.Glob(filepath.Join(oldDir, base+"*"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
	_ = os.Remove(oldDir)
}

// SyncFileLogger flushes all buffered file-log entries to disk.
func SyncFileLogger() {
	if c := activeAsyncCore.Load(); c != nil {
		c.writer.syncWait()
	}
}

// CloseFileLogger flushes and closes the active file logger.
func CloseFileLogger() {
	vars.HostFSLock.Lock()
	defer vars.HostFSLock.Unlock()
	closeFileLoggerLocked()
}

func closeFileLoggerLocked() {
	if old := activeAsyncCore.Swap(nil); old != nil {
		old.writer.close()
	}
	appliedFileLog.Store(nil)
}

// SetLogLevel provides conversion from the operators LogLevel value ({0,1,2} where 2 is the most verbose) and sets
// the current logging level accordingly.
func SetLogLevel(operatorLevel int) {
	newLevel := operatorToZapLevel(operatorLevel)
	currLevel := Options.Level.(zzap.AtomicLevel).Level()
	if newLevel != currLevel {
		log.Log.Info("Set log verbose level", "new-level", operatorLevel, "current-level", zapToOperatorLevel(currLevel))
		Options.Level.(zzap.AtomicLevel).SetLevel(newLevel)
	}
}

func GetLogLevel() int {
	return zapToOperatorLevel(Options.Level.(zzap.AtomicLevel).Level())
}

func zapToOperatorLevel(zapLevel zapcore.Level) int {
	return int(zapLevel) * -1
}

func operatorToZapLevel(operatorLevel int) zapcore.Level {
	return zapcore.Level(operatorLevel * -1)
}

// currentFileWriter returns the fileWriter backing the active asyncFileCore,
// or nil when file logging is disabled.  Used only by internal tests.
func currentFileWriter() *fileWriter {
	if c := activeAsyncCore.Load(); c != nil {
		return c.writer
	}
	return nil
}

// ---------------------------------------------------------------------------
// armedFileCore — permanent no-op core that activates when activeAsyncCore is set
// ---------------------------------------------------------------------------

type armedFileCore struct {
	extraFields []zapcore.Field
}

func (a *armedFileCore) Enabled(level zapcore.Level) bool {
	c := activeAsyncCore.Load()
	if c == nil {
		return false
	}
	return c.Enabled(level)
}

func (a *armedFileCore) With(fields []zapcore.Field) zapcore.Core {
	combined := make([]zapcore.Field, len(a.extraFields)+len(fields))
	copy(combined, a.extraFields)
	copy(combined[len(a.extraFields):], fields)
	return &armedFileCore{extraFields: combined}
}

func (a *armedFileCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if a.Enabled(entry.Level) {
		return ce.AddCore(entry, a)
	}
	return ce
}

func (a *armedFileCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	c := activeAsyncCore.Load()
	if c == nil {
		return nil
	}
	allFields := fields
	if len(a.extraFields) > 0 {
		allFields = make([]zapcore.Field, len(fields)+len(a.extraFields))
		copy(allFields, fields)
		copy(allFields[len(fields):], a.extraFields)
	}
	return c.Write(entry, allFields)
}

func (a *armedFileCore) Sync() error {
	c := activeAsyncCore.Load()
	if c == nil {
		return nil
	}
	return c.Sync()
}

// ---------------------------------------------------------------------------
// asyncFileCore — zapcore.Core implementation
// ---------------------------------------------------------------------------

type asyncFileCore struct {
	encoder      zapcore.Encoder
	levelEnabler zapcore.LevelEnabler
	writer       *fileWriter
}

func (a *asyncFileCore) Enabled(level zapcore.Level) bool {
	return a.levelEnabler.Enabled(level)
}

func (a *asyncFileCore) With(fields []zapcore.Field) zapcore.Core {
	clone := &asyncFileCore{
		encoder:      a.encoder.Clone(),
		levelEnabler: a.levelEnabler,
		writer:       a.writer,
	}
	for _, f := range fields {
		f.AddTo(clone.encoder)
	}
	return clone
}

func (a *asyncFileCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if a.Enabled(entry.Level) {
		return ce.AddCore(entry, a)
	}
	return ce
}

func (a *asyncFileCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	if a.writer.closed.Load() {
		return nil
	}
	buf, err := a.encoder.EncodeEntry(entry, fields)
	if err != nil {
		return err
	}
	data := make([]byte, buf.Len())
	copy(data, buf.Bytes())
	buf.Free()

	return a.writer.enqueue(data)
}

func (a *asyncFileCore) Sync() error {
	return a.writer.syncWait()
}

// ---------------------------------------------------------------------------
// ringBuffer — lock-protected circular buffer of []byte entries
// ---------------------------------------------------------------------------
//
// When full, push() overwrites the oldest entry (instead of dropping the newest).
// This preserves the most recent log entries which are typically the most valuable
// for post-mortem debugging.

const (
	ringBufSize        = 4096
	chrootPollInterval = 500 * time.Microsecond
)

type ringBuffer struct {
	mu          sync.Mutex
	buf         [][]byte
	size        int
	head        int // next write position
	count       int // number of entries currently in the ring
	overwritten uint64
	notifyCh    chan struct{} // signals the consumer that data is available
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{
		buf:      make([][]byte, size),
		size:     size,
		notifyCh: make(chan struct{}, 1),
	}
}

func (rb *ringBuffer) push(data []byte) {
	rb.mu.Lock()
	if rb.count == rb.size {
		rb.overwritten++
	} else {
		rb.count++
	}
	rb.buf[rb.head] = data
	rb.head = (rb.head + 1) % rb.size
	rb.mu.Unlock()

	select {
	case rb.notifyCh <- struct{}{}:
	default:
	}
}

func (rb *ringBuffer) pop() ([]byte, bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.count == 0 {
		return nil, false
	}
	tail := (rb.head - rb.count + rb.size) % rb.size
	data := rb.buf[tail]
	rb.buf[tail] = nil
	rb.count--
	return data, true
}

func (rb *ringBuffer) drainAll() [][]byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.count == 0 {
		return nil
	}
	result := make([][]byte, 0, rb.count)
	tail := (rb.head - rb.count + rb.size) % rb.size
	for i := 0; i < rb.count; i++ {
		idx := (tail + i) % rb.size
		result = append(result, rb.buf[idx])
		rb.buf[idx] = nil
	}
	rb.count = 0
	return result
}

func (rb *ringBuffer) resetOverwritten() uint64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	n := rb.overwritten
	rb.overwritten = 0
	return n
}

func (rb *ringBuffer) len() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.count
}

// ---------------------------------------------------------------------------
// fileWriter — goroutine-backed lumberjack writer using a ring buffer
// ---------------------------------------------------------------------------
//
// CHROOT SAFETY: safWrite() spin-waits on vars.InChroot before calling
// lj.Write() to prevent rotation-during-chroot races.

type fileWriter struct {
	lj      *lumberjack.Logger
	ring    *ringBuffer
	encoder zapcore.Encoder
	syncCh  chan chan error
	closeCh chan struct{}
	closed  atomic.Bool
	wg      sync.WaitGroup
}

func newFileWriter(lj *lumberjack.Logger, encoder zapcore.Encoder) *fileWriter {
	fw := &fileWriter{
		lj:      lj,
		ring:    newRingBuffer(ringBufSize),
		encoder: encoder,
		syncCh:  make(chan chan error, 1),
		closeCh: make(chan struct{}),
	}
	fw.wg.Add(1)
	go fw.run()
	fmt.Fprintf(os.Stderr, "sriov-operator: file logger started (ring buffer size=%d, file=%s)\n", ringBufSize, lj.Filename)
	return fw
}

func (fw *fileWriter) writeLogEntry(level zapcore.Level, msg string, fields ...zapcore.Field) {
	entry := zapcore.Entry{
		Level:      level,
		Time:       time.Now(),
		LoggerName: "sriov-operator",
		Caller:     zapcore.EntryCaller{Defined: true, File: "log/log.go"},
		Message:    msg,
	}
	buf, err := fw.encoder.Clone().EncodeEntry(entry, fields)
	if err != nil {
		return
	}
	_, _ = fw.lj.Write(buf.Bytes())
	buf.Free()
}

func (fw *fileWriter) enqueue(data []byte) error {
	if fw.closed.Load() {
		return nil
	}
	fw.ring.push(data)
	return nil
}

const (
	logCfgMaxSizeMBMin      = 1
	logCfgMaxSizeMBMax      = 1024
	logCfgMaxSizeMBDefault  = 100
	logCfgMaxFilesMin       = 1
	logCfgMaxFilesMax       = 20
	logCfgMaxFilesDefault   = 5
	logCfgMaxAgeDaysMin     = 0
	logCfgMaxAgeDaysMax     = 365
	logCfgMaxAgeDaysDefault = 30
)

func clampLogParam(v, lo, hi, dflt int) int {
	if v < lo || v > hi {
		return dflt
	}
	return v
}

var syncWaitTimeout = 30 * time.Second

func (fw *fileWriter) syncWait() error {
	if fw.closed.Load() {
		return nil
	}
	done := make(chan error, 1)
	timeout := time.NewTimer(syncWaitTimeout)
	defer timeout.Stop()

	select {
	case fw.syncCh <- done:
		select {
		case err := <-done:
			return err
		case <-timeout.C:
			fmt.Fprintf(os.Stderr, "sriov-operator: syncWait deadline (%s) exceeded, some log entries may be lost\n", syncWaitTimeout)
			return fmt.Errorf("syncWait timeout after %s", syncWaitTimeout)
		case <-fw.closeCh:
			return nil
		}
	case <-fw.closeCh:
		return nil
	case <-timeout.C:
		fmt.Fprintf(os.Stderr, "sriov-operator: syncWait could not enqueue sync request within %s\n", syncWaitTimeout)
		return fmt.Errorf("syncWait timeout after %s", syncWaitTimeout)
	}
}

func (fw *fileWriter) close() {
	if fw.closed.CompareAndSwap(false, true) {
		close(fw.closeCh)
		fw.wg.Wait()
		fw.emitOverwriteSummary()
		fmt.Fprintf(os.Stderr, "sriov-operator: file logger closed (file=%s)\n", fw.lj.Filename)
		_ = fw.lj.Close()
	}
}

func (fw *fileWriter) emitOverwriteSummary() {
	if n := fw.ring.resetOverwritten(); n > 0 {
		fw.writeLogEntry(zapcore.WarnLevel, "ring buffer overwrote oldest entries",
			zzap.Uint64("overwritten", n))
		fmt.Fprintf(os.Stderr, "sriov-operator: ring buffer overwrote %d oldest log entries\n", n)
	}
}

func (fw *fileWriter) drainPending() {
	entries := fw.ring.drainAll()
	for _, data := range entries {
		fw.safWrite(data)
	}
}

func (fw *fileWriter) run() {
	defer fw.wg.Done()
	for {
		select {
		case done := <-fw.syncCh:
			fw.drainPending()
			fw.emitOverwriteSummary()
			done <- nil
			continue
		case <-fw.closeCh:
			fw.drainPending()
			return
		default:
		}

		if data, ok := fw.ring.pop(); ok {
			fw.safWrite(data)
			continue
		}

		select {
		case <-fw.ring.notifyCh:
		case done := <-fw.syncCh:
			fw.drainPending()
			fw.emitOverwriteSummary()
			done <- nil
		case <-fw.closeCh:
			fw.drainPending()
			return
		}
	}
}

// safWrite defers the write until we are outside any temporary chroot window, then
// calls lj.Write which may transparently rotate the log file.
//
// In systemd mode the process runs on the host with InChroot permanently true for
// path resolution — that is not a temporary chroot window, so we must not wait.
func (fw *fileWriter) safWrite(data []byte) {
	if vars.InChroot.Load() && !vars.UsingSystemdMode {
		waitStart := time.Now()
		for vars.InChroot.Load() && !fw.closed.Load() {
			time.Sleep(chrootPollInterval)
		}
		if fw.closed.Load() {
			return
		}
		waited := time.Since(waitStart)
		fw.writeLogEntry(zapcore.InfoLevel, "safWrite resumed after chroot window",
			zzap.Float64("waitedMs", float64(waited.Microseconds())/1000.0))
	}
	if _, err := fw.lj.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "sriov-operator: file-log write error: %v\n", err)
	}
}
