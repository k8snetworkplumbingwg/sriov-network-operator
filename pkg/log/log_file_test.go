package log

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

func resetFileLogger() {
	CloseFileLogger()
	vars.InChroot.Store(false)
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

var _ = g.Describe("File Logging", func() {
	var tmpDir string
	var logFile string

	g.BeforeEach(func() {
		tmpDir = g.GinkgoT().TempDir()
		logFile = filepath.Join(tmpDir, "config-daemon.log")
	})

	g.AfterEach(func() {
		resetFileLogger()
	})

	g.It("InitLogWithFile creates tee writing to both console and file", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		log.Log.Info("tee-test-message")
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(string(content)).To(o.ContainSubstring("tee-test-message"))

		o.Expect(currentFileWriter()).NotTo(o.BeNil())
		o.Expect(currentFileWriter().closed.Load()).To(o.BeFalse())
	})

	g.It("rotation parameters are correctly set on lumberjack Logger", func() {
		err := InitLogWithFile(logFile, 50, 3, 7, true)
		o.Expect(err).NotTo(o.HaveOccurred())

		o.Expect(currentFileWriter()).NotTo(o.BeNil())
		o.Expect(currentFileWriter().lj.MaxSize).To(o.Equal(50))
		o.Expect(currentFileWriter().lj.MaxBackups).To(o.Equal(3))
		o.Expect(currentFileWriter().lj.MaxAge).To(o.Equal(7))
		o.Expect(currentFileWriter().lj.Compress).To(o.BeTrue())
		o.Expect(currentFileWriter().lj.Filename).To(o.Equal(logFile))
	})

	g.It("file output is JSON with ts, level, msg, and structured key-value fields", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		log.Log.Info("json-format-check", "key1", "value1")
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(content)).To(o.BeNumerically(">", 0))

		lines := splitLines(content)
		o.Expect(len(lines)).To(o.BeNumerically(">=", 1))

		var entry map[string]interface{}
		err = json.Unmarshal([]byte(lines[len(lines)-1]), &entry)
		o.Expect(err).NotTo(o.HaveOccurred(), "each file line must be valid JSON")

		o.Expect(entry).To(o.HaveKey("ts"))
		o.Expect(entry).To(o.HaveKey("level"))
		o.Expect(entry).To(o.HaveKey("msg"))
		o.Expect(entry["msg"]).To(o.Equal("json-format-check"))
		o.Expect(entry["key1"]).To(o.Equal("value1"))
	})

	g.It("SyncFileLogger ensures all entries are on disk before returning", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		const numEntries = 50
		for i := 0; i < numEntries; i++ {
			log.Log.Info("sync-test-entry", "index", i)
		}
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		o.Expect(err).NotTo(o.HaveOccurred())
		lines := splitLines(content)
		o.Expect(len(lines)).To(o.Equal(numEntries),
			"all %d entries should be on disk after SyncFileLogger", numEntries)
	})

	g.It("CloseFileLogger flushes pending entries and shuts down cleanly", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		log.Log.Info("before-close-1")
		log.Log.Info("before-close-2")

		fw := currentFileWriter()
		CloseFileLogger()

		o.Expect(currentFileWriter()).To(o.BeNil())
		o.Expect(fw.closed.Load()).To(o.BeTrue())

		content, err := os.ReadFile(logFile)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(string(content)).To(o.ContainSubstring("before-close-1"))
		o.Expect(string(content)).To(o.ContainSubstring("before-close-2"))

		o.Expect(func() { log.Log.Info("after-close-safe") }).NotTo(o.Panic())
	})

	g.It("calling InitLogWithFile twice switches to the new file and writer", func() {
		file2 := filepath.Join(tmpDir, "second.log")

		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())
		log.Log.Info("written-to-first-file")
		SyncFileLogger()

		err = InitLogWithFile(file2, 50, 2, 10, false)
		o.Expect(err).NotTo(o.HaveOccurred())
		log.Log.Info("written-to-second-file")
		SyncFileLogger()

		content2, err := os.ReadFile(file2)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(string(content2)).To(o.ContainSubstring("written-to-second-file"))
		o.Expect(string(content2)).NotTo(o.ContainSubstring("written-to-first-file"))

		o.Expect(currentFileWriter().lj.Filename).To(o.Equal(file2))
	})

	g.It("creates parent directory with 0750 permissions when missing", func() {
		nestedFile := filepath.Join(tmpDir, "nested", "deep", "config-daemon.log")

		err := InitLogWithFile(nestedFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		dirPath := filepath.Join(tmpDir, "nested", "deep")
		info, err := os.Stat(dirPath)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(info.IsDir()).To(o.BeTrue())
		o.Expect(info.Mode().Perm()).To(o.Equal(os.FileMode(0o750)))

		log.Log.Info("nested-dir-entry")
		SyncFileLogger()
		content, err := os.ReadFile(nestedFile)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(string(content)).To(o.ContainSubstring("nested-dir-entry"))
	})

	g.It("writes during InChroot window are buffered and flushed after InChroot clears", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		vars.InChroot.Store(true)

		log.Log.Info("entry-written-during-chroot")

		released := make(chan struct{})
		go func() {
			defer close(released)
			time.Sleep(60 * time.Millisecond)
			vars.InChroot.Store(false)
		}()

		SyncFileLogger()
		<-released

		content, err := os.ReadFile(logFile)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(string(content)).To(o.ContainSubstring("entry-written-during-chroot"))
	})

	g.It("With() adds structured fields that appear in every file entry", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		childLogger := log.Log.WithValues("component", "test-component")
		childLogger.Info("with-field-msg")
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(string(content)).To(o.ContainSubstring("with-field-msg"))
		o.Expect(string(content)).To(o.ContainSubstring("test-component"))
	})

	g.It("InitLogWithFile returns an error when the log directory cannot be created", func() {
		f, err := os.CreateTemp(tmpDir, "notadir")
		o.Expect(err).NotTo(o.HaveOccurred())
		f.Close()

		impossiblePath := filepath.Join(f.Name(), "subdir", "config-daemon.log")
		err = InitLogWithFile(impossiblePath, 100, 5, 30, false)
		o.Expect(err).To(o.HaveOccurred())
		o.Expect(err.Error()).To(o.ContainSubstring("failed to create log directory"))
	})

	g.It("InitLogWithFile rejects an empty logFilePath", func() {
		err := InitLogWithFile("", 100, 5, 30, false)
		o.Expect(err).To(o.HaveOccurred())
		o.Expect(err.Error()).To(o.ContainSubstring("logFilePath must not be empty"))
	})

	g.It("InitLogWithFile clamps out-of-range values to defaults", func() {
		err := InitLogWithFile(logFile, -1, -999, -42, false)
		o.Expect(err).NotTo(o.HaveOccurred(), "negative values should be clamped, not rejected")
		log.Log.Info("clamped-negative")
		SyncFileLogger()
		content, _ := os.ReadFile(logFile)
		o.Expect(string(content)).To(o.ContainSubstring("clamped-negative"))
	})

	g.It("InitLogWithFile clamps zero and over-maximum to defaults", func() {
		err := InitLogWithFile(logFile, 0, 0, 0, false)
		o.Expect(err).NotTo(o.HaveOccurred())
		log.Log.Info("clamped-zero")
		SyncFileLogger()
		content, _ := os.ReadFile(logFile)
		o.Expect(string(content)).To(o.ContainSubstring("clamped-zero"))

		err = InitLogWithFile(logFile, 99999, 9999, 9999, false)
		o.Expect(err).NotTo(o.HaveOccurred())
		log.Log.Info("clamped-overmax")
		SyncFileLogger()
		content, _ = os.ReadFile(logFile)
		o.Expect(string(content)).To(o.ContainSubstring("clamped-overmax"))
	})
})

var _ = g.Describe("ringBuffer internals", func() {
	g.It("push/pop preserves FIFO order", func() {
		rb := newRingBuffer(4)
		rb.push([]byte("a"))
		rb.push([]byte("b"))
		rb.push([]byte("c"))

		d, ok := rb.pop()
		o.Expect(ok).To(o.BeTrue())
		o.Expect(string(d)).To(o.Equal("a"))

		d, ok = rb.pop()
		o.Expect(ok).To(o.BeTrue())
		o.Expect(string(d)).To(o.Equal("b"))

		d, ok = rb.pop()
		o.Expect(ok).To(o.BeTrue())
		o.Expect(string(d)).To(o.Equal("c"))

		_, ok = rb.pop()
		o.Expect(ok).To(o.BeFalse())
	})

	g.It("push overwrites oldest when full and tracks overwrite count", func() {
		rb := newRingBuffer(3)
		rb.push([]byte("1"))
		rb.push([]byte("2"))
		rb.push([]byte("3"))
		rb.push([]byte("4"))
		rb.push([]byte("5"))

		o.Expect(rb.resetOverwritten()).To(o.Equal(uint64(2)))

		d, ok := rb.pop()
		o.Expect(ok).To(o.BeTrue())
		o.Expect(string(d)).To(o.Equal("3"), "oldest surviving entry should be '3'")

		d, ok = rb.pop()
		o.Expect(ok).To(o.BeTrue())
		o.Expect(string(d)).To(o.Equal("4"))

		d, ok = rb.pop()
		o.Expect(ok).To(o.BeTrue())
		o.Expect(string(d)).To(o.Equal("5"))

		_, ok = rb.pop()
		o.Expect(ok).To(o.BeFalse())
	})

	g.It("drainAll returns oldest-first and resets the buffer", func() {
		rb := newRingBuffer(4)
		rb.push([]byte("x"))
		rb.push([]byte("y"))
		rb.push([]byte("z"))

		entries := rb.drainAll()
		o.Expect(len(entries)).To(o.Equal(3))
		o.Expect(string(entries[0])).To(o.Equal("x"))
		o.Expect(string(entries[1])).To(o.Equal("y"))
		o.Expect(string(entries[2])).To(o.Equal("z"))

		o.Expect(rb.len()).To(o.Equal(0))
	})

	g.It("drainAll after overwrite returns only surviving entries", func() {
		rb := newRingBuffer(3)
		rb.push([]byte("a"))
		rb.push([]byte("b"))
		rb.push([]byte("c"))
		rb.push([]byte("d"))

		entries := rb.drainAll()
		o.Expect(len(entries)).To(o.Equal(3))
		o.Expect(string(entries[0])).To(o.Equal("b"))
		o.Expect(string(entries[1])).To(o.Equal("c"))
		o.Expect(string(entries[2])).To(o.Equal("d"))
	})

	g.It("resetOverwritten returns count and resets to zero", func() {
		rb := newRingBuffer(2)
		rb.push([]byte("1"))
		rb.push([]byte("2"))
		rb.push([]byte("3"))

		o.Expect(rb.resetOverwritten()).To(o.Equal(uint64(1)))
		o.Expect(rb.resetOverwritten()).To(o.Equal(uint64(0)))
	})

	g.It("notifyCh is signaled on push", func() {
		rb := newRingBuffer(4)
		rb.push([]byte("data"))

		select {
		case <-rb.notifyCh:
		case <-time.After(100 * time.Millisecond):
			g.Fail("notifyCh was not signaled after push")
		}
	})
})

var _ = g.Describe("fileWriter (ring buffer) internals", func() {
	var tmpDir string
	var logFile string

	g.BeforeEach(func() {
		tmpDir = g.GinkgoT().TempDir()
		logFile = filepath.Join(tmpDir, "internal.log")
	})

	g.AfterEach(func() {
		resetFileLogger()
	})

	g.It("ring buffer overwrites oldest entries during InChroot stall — newest preserved", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		vars.InChroot.Store(true)

		totalEntries := ringBufSize + 100
		for i := 0; i < totalEntries; i++ {
			log.Log.Info("fill-ring", "i", i)
		}

		vars.InChroot.Store(false)
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		o.Expect(err).NotTo(o.HaveOccurred())
		lines := splitLines(content)
		o.Expect(len(lines)).To(o.BeNumerically(">", 0))
		o.Expect(len(lines)).To(o.BeNumerically("<=", totalEntries+10))

		lastUserLine := ""
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.Contains(lines[i], "fill-ring") {
				lastUserLine = lines[i]
				break
			}
		}
		o.Expect(lastUserLine).To(o.ContainSubstring(fmt.Sprintf(`"i":%d`, totalEntries-1)),
			"the very last entry should be preserved (ring buffer keeps newest)")
	})

	g.It("overwrite summary is emitted on sync when overwrites occurred", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		vars.InChroot.Store(true)
		for i := 0; i < ringBufSize+50; i++ {
			log.Log.Info("overflow-entry", "i", i)
		}
		vars.InChroot.Store(false)
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(string(content)).To(o.ContainSubstring("ring buffer overwrote oldest entries"))
	})

	g.It("no overwrite summary when buffer never overflows", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		for i := 0; i < 10; i++ {
			log.Log.Info("normal-entry", "i", i)
		}
		SyncFileLogger()

		content, err := os.ReadFile(logFile)
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(string(content)).NotTo(o.ContainSubstring("ring buffer overwrote"))
	})

	g.It("close() is idempotent — calling it twice does not panic", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		fw := currentFileWriter()
		o.Expect(func() {
			fw.close()
			fw.close()
		}).NotTo(o.Panic())
	})

	g.It("syncWait() returns nil immediately when writer is already closed", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		fw := currentFileWriter()
		fw.close()

		done := make(chan error, 1)
		go func() { done <- fw.syncWait() }()

		select {
		case syncErr := <-done:
			o.Expect(syncErr).NotTo(o.HaveOccurred())
		case <-time.After(200 * time.Millisecond):
			g.Fail("syncWait() on closed writer did not return promptly")
		}
	})

	g.It("enqueue on closed writer is a no-op", func() {
		err := InitLogWithFile(logFile, 100, 5, 30, false)
		o.Expect(err).NotTo(o.HaveOccurred())

		fw := currentFileWriter()
		fw.close()

		err = fw.enqueue([]byte("after-close"))
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(fw.ring.len()).To(o.Equal(0))
	})

})
