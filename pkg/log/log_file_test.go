package log

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

func resetFileLogger() {
	CloseFileLogger()
}

var defaultTestCfg = vars.LogFileSettings{
	MaxSizeMB:  consts.LogCfgMaxSizeMBDefault,
	MaxFiles:   consts.LogCfgMaxFilesDefault,
	MaxAgeDays: consts.LogCfgMaxAgeDaysDefault,
	Compress:   consts.LogCfgCompressDefault,
}

func splitLines(content []byte) []string {
	var lines []string
	for _, line := range strings.Split(string(content), "\n") {
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

var _ = Describe("File Logging", func() {
	var tmpDir string
	var logFile string

	BeforeEach(func() {
		tmpDir = GinkgoT().TempDir()
		logFile = filepath.Join(tmpDir, "config-daemon.log")
	})

	AfterEach(func() {
		resetFileLogger()
	})

	It("InitLogWithFile writes enabled status line to the log file", func() {
		Expect(initLogWithFileAt(logFile, defaultTestCfg)).To(Succeed())
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("persistent file logging enabled"))
		Expect(string(content)).To(ContainSubstring(logFile))
	})

	It("InitLogWithFile creates tee writing to both console and file", func() {
		err := initLogWithFileAt(logFile, defaultTestCfg)
		Expect(err).NotTo(HaveOccurred())

		log.Log.Info("tee-test-message")
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("tee-test-message"))

		Expect(currentLumberjack()).NotTo(BeNil())
	})

	It("rotation parameters are correctly set on lumberjack Logger", func() {
		err := initLogWithFileAt(logFile, vars.LogFileSettings{MaxSizeMB: 50, MaxFiles: 3, MaxAgeDays: 7, Compress: consts.LogCfgCompressDefault})
		Expect(err).NotTo(HaveOccurred())

		lj := currentLumberjack()
		Expect(lj).NotTo(BeNil())
		Expect(lj.MaxSize).To(Equal(50))
		Expect(lj.MaxBackups).To(Equal(3))
		Expect(lj.MaxAge).To(Equal(7))
		Expect(lj.Compress).To(BeTrue())
		Expect(lj.Filename).To(Equal(logFile))
	})

	It("file output is plain human-readable text", func() {
		err := initLogWithFileAt(logFile, defaultTestCfg)
		Expect(err).NotTo(HaveOccurred())

		log.Log.Info("plain-format-check", "key1", "value1")
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(content)).To(BeNumerically(">", 0))

		lines := splitLines(content)
		Expect(len(lines)).To(BeNumerically(">=", 1))
		lastLine := lines[len(lines)-1]

		var entry map[string]interface{}
		Expect(json.Unmarshal([]byte(lastLine), &entry)).To(HaveOccurred(),
			"file output must be plain human-readable text")

		Expect(lastLine).To(ContainSubstring("INFO"))
		Expect(lastLine).To(ContainSubstring("plain-format-check"))
		Expect(lastLine).To(ContainSubstring(`"key1": "value1"`))
	})

	It("SyncFileLogger ensures all entries are on disk before returning", func() {
		err := initLogWithFileAt(logFile, defaultTestCfg)
		Expect(err).NotTo(HaveOccurred())

		const numEntries = 50
		for i := 0; i < numEntries; i++ {
			log.Log.Info("sync-test-entry", "index", i)
		}
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		Expect(err).NotTo(HaveOccurred())
		lines := splitLines(content)
		Expect(len(lines)).To(BeNumerically(">=", numEntries),
			"at least %d entries should be on disk after SyncFileLogger", numEntries)
		for i := 0; i < numEntries; i++ {
			Expect(string(content)).To(ContainSubstring(fmt.Sprintf(`"index": %d`, i)))
		}
	})

	It("CloseFileLogger flushes pending entries and shuts down cleanly", func() {
		err := initLogWithFileAt(logFile, defaultTestCfg)
		Expect(err).NotTo(HaveOccurred())

		log.Log.Info("before-close-1")
		log.Log.Info("before-close-2")

		CloseFileLogger()

		Expect(currentLumberjack()).To(BeNil())

		content, err := os.ReadFile(logFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("before-close-1"))
		Expect(string(content)).To(ContainSubstring("before-close-2"))

		Expect(func() { log.Log.Info("after-close-safe") }).NotTo(Panic())
	})

	It("calling InitLogWithFile twice switches to the new file", func() {
		file2 := filepath.Join(tmpDir, "second.log")

		err := initLogWithFileAt(logFile, defaultTestCfg)
		Expect(err).NotTo(HaveOccurred())
		log.Log.Info("written-to-first-file")
		SyncFileLogger()

		err = initLogWithFileAt(file2, vars.LogFileSettings{MaxSizeMB: 50, MaxFiles: 2, MaxAgeDays: 10})
		Expect(err).NotTo(HaveOccurred())
		log.Log.Info("written-to-second-file")
		SyncFileLogger()

		content2, err := os.ReadFile(file2)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content2)).To(ContainSubstring("written-to-second-file"))
		Expect(string(content2)).NotTo(ContainSubstring("written-to-first-file"))

		Expect(currentLumberjack().Filename).To(Equal(file2))
	})

	It("creates parent directory with 0750 permissions when missing", func() {
		nestedFile := filepath.Join(tmpDir, "nested", "deep", "config-daemon.log")

		err := initLogWithFileAt(nestedFile, defaultTestCfg)
		Expect(err).NotTo(HaveOccurred())

		dirPath := filepath.Join(tmpDir, "nested", "deep")
		info, err := os.Stat(dirPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.IsDir()).To(BeTrue())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o750)))

		log.Log.Info("nested-dir-entry")
		SyncFileLogger()
		content, err := os.ReadFile(nestedFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("nested-dir-entry"))
	})

	It("With() adds structured fields that appear in every file entry", func() {
		err := initLogWithFileAt(logFile, defaultTestCfg)
		Expect(err).NotTo(HaveOccurred())

		childLogger := log.Log.WithValues("component", "test-component")
		childLogger.Info("with-field-msg")
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("with-field-msg"))
		Expect(string(content)).To(ContainSubstring("test-component"))
	})

	It("InitLogWithFile returns an error when the log directory cannot be created", func() {
		f, err := os.CreateTemp(tmpDir, "notadir")
		Expect(err).NotTo(HaveOccurred())
		f.Close()

		impossiblePath := filepath.Join(f.Name(), "subdir", "config-daemon.log")
		err = initLogWithFileAt(impossiblePath, defaultTestCfg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("create log directory"))
	})

	It("InitLogWithFile rejects an empty logFilePath", func() {
		err := initLogWithFileAt("", defaultTestCfg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("logFilePath must not be empty"))
	})

	It("InitLogWithFile clamps out-of-range values to defaults", func() {
		err := initLogWithFileAt(logFile, vars.LogFileSettings{MaxSizeMB: -1, MaxFiles: -999, MaxAgeDays: -42})
		Expect(err).NotTo(HaveOccurred(), "negative values should be clamped, not rejected")
		log.Log.Info("clamped-negative")
		SyncFileLogger()
		content, _ := os.ReadFile(logFile)
		Expect(string(content)).To(ContainSubstring("clamped-negative"))
	})

	It("InitLogWithFile rejects root path", func() {
		err := initLogWithFileAt("/", defaultTestCfg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unsafe root path"))
	})

	It("InitLogWithFile clamps zero and over-maximum to defaults", func() {
		err := initLogWithFileAt(logFile, vars.LogFileSettings{})
		Expect(err).NotTo(HaveOccurred())
		log.Log.Info("clamped-zero")
		SyncFileLogger()
		content, _ := os.ReadFile(logFile)
		Expect(string(content)).To(ContainSubstring("clamped-zero"))

		err = initLogWithFileAt(logFile, vars.LogFileSettings{MaxSizeMB: 99999, MaxFiles: 9999, MaxAgeDays: 9999})
		Expect(err).NotTo(HaveOccurred())
		log.Log.Info("clamped-overmax")
		SyncFileLogger()
		content, _ = os.ReadFile(logFile)
		Expect(string(content)).To(ContainSubstring("clamped-overmax"))
	})

	It("prunes extra backup files immediately when MaxFiles shrinks", func() {
		for day := 1; day <= 5; day++ {
			ts := time.Date(2020, 1, day, 0, 0, 0, 0, time.UTC).Format(lumberjackBackupTimeFormat)
			backup := filepath.Join(tmpDir, "config-daemon-"+ts+".log")
			Expect(os.WriteFile(backup, []byte("old"), 0o644)).To(Succeed())
		}

		err := initLogWithFileAt(logFile, vars.LogFileSettings{MaxSizeMB: consts.LogCfgMaxSizeMBDefault, MaxFiles: 2, MaxAgeDays: 0})
		Expect(err).NotTo(HaveOccurred())

		entries, err := os.ReadDir(tmpDir)
		Expect(err).NotTo(HaveOccurred())
		var backups []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "config-daemon-") && strings.HasSuffix(e.Name(), ".log") {
				backups = append(backups, e.Name())
			}
		}
		Expect(backups).To(HaveLen(2))
		Expect(backups).To(ContainElement("config-daemon-2020-01-05T00-00-00.000.log"))
		Expect(backups).To(ContainElement("config-daemon-2020-01-04T00-00-00.000.log"))
	})

	It("old logs are preserved when path changes (no cleanup)", func() {
		oldDir := filepath.Join(tmpDir, "old-loc")
		newDir := filepath.Join(tmpDir, "new-loc")
		oldFile := filepath.Join(oldDir, "config-daemon.log")
		newFile := filepath.Join(newDir, "config-daemon.log")
		backupFile := filepath.Join(oldDir, "config-daemon-2020-01-01T00-00-00.000.log")

		Expect(os.MkdirAll(oldDir, 0o750)).To(Succeed())
		Expect(os.WriteFile(oldFile, []byte("current"), 0o644)).To(Succeed())
		Expect(os.WriteFile(backupFile, []byte("bak"), 0o644)).To(Succeed())

		// Verify files exist before logger operations
		_, err := os.Stat(oldFile)
		Expect(err).NotTo(HaveOccurred(), "oldFile should exist before test")
		_, err = os.Stat(backupFile)
		Expect(err).NotTo(HaveOccurred(), "backupFile should exist before test")

		// MaxAgeDays=0 so lumberjack does not delete the pre-existing backup by age on open.
		noAgeCfg := vars.LogFileSettings{MaxSizeMB: consts.LogCfgMaxSizeMBDefault, MaxFiles: consts.LogCfgMaxFilesDefault, MaxAgeDays: 0, Compress: consts.LogCfgCompressDefault}
		Expect(initLogWithFileAt(oldFile, noAgeCfg)).To(Succeed())
		SyncFileLogger()

		Expect(initLogWithFileAt(newFile, noAgeCfg)).To(Succeed())
		SyncFileLogger()

		// Verify old files still exist after switching paths
		_, err = os.Stat(oldFile)
		Expect(err).NotTo(HaveOccurred(), "old log file should be preserved after path change")
		_, err = os.Stat(backupFile)
		Expect(err).NotTo(HaveOccurred(), "old backup file should be preserved after path change")

		// Verify old file still has content
		content, err := os.ReadFile(oldFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("current"), "original content should be preserved")
	})

	It("InitLogWithFile reads configuration from vars", func() {
		savedCfg := vars.GetLogCfg()
		defer vars.SetLogCfg(savedCfg)

		cfg := vars.DefaultLogCfg()
		cfg.Enabled = true
		cfg.HostPath = tmpDir
		vars.SetLogCfg(cfg)

		Expect(InitLogWithFile()).To(Succeed())

		expected := filepath.Join(tmpDir, consts.ConfigDaemonLogSubDir, LogFileName)
		lj := currentLumberjack()
		Expect(lj).NotTo(BeNil())
		Expect(lj.Filename).To(Equal(expected))

		log.Log.Info("from-vars-cfg")
		SyncFileLogger()
		content, err := os.ReadFile(expected)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("from-vars-cfg"))
	})

	It("InitLogWithFile disables file logging when vars.Enabled is false", func() {
		Expect(initLogWithFileAt(logFile, defaultTestCfg)).To(Succeed())
		Expect(currentLumberjack()).NotTo(BeNil())

		savedCfg := vars.GetLogCfg()
		defer vars.SetLogCfg(savedCfg)

		cfg := vars.DefaultLogCfg()
		cfg.Enabled = false
		vars.SetLogCfg(cfg)
		Expect(InitLogWithFile()).To(Succeed())
		Expect(currentLumberjack()).To(BeNil())
		Expect(IsFileLoggerActive()).To(BeFalse())
	})

	It("InitLogWithFile disables logging at startup when logging is off by default", func() {
		savedCfg := vars.GetLogCfg()
		defer vars.SetLogCfg(savedCfg)

		vars.SetLogCfg(vars.DefaultLogCfg())
		Expect(InitLogWithFile()).To(Succeed())
		Expect(currentLumberjack()).To(BeNil())
		Expect(IsFileLoggerActive()).To(BeFalse())
	})

	It("CloseFileLogger is idempotent", func() {
		err := initLogWithFileAt(logFile, defaultTestCfg)
		Expect(err).NotTo(HaveOccurred())

		Expect(func() {
			CloseFileLogger()
			CloseFileLogger()
		}).NotTo(Panic())
	})

	It("SyncFileLogger is safe when no file logger is active", func() {
		Expect(func() {
			SyncFileLogger()
		}).NotTo(Panic())
	})

	It("reconfigure flushes old writer before swap", func() {
		file2 := filepath.Join(tmpDir, "second.log")

		err := initLogWithFileAt(logFile, defaultTestCfg)
		Expect(err).NotTo(HaveOccurred())
		log.Log.Info("must-be-flushed")

		err = initLogWithFileAt(file2, defaultTestCfg)
		Expect(err).NotTo(HaveOccurred())

		content, err := os.ReadFile(logFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("must-be-flushed"),
			"old writer must be flushed before swap")
	})
})
