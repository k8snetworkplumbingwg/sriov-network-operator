package host

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/fakefilesystem"
)

func assertFileContentsEquals(path, expectedContent string) {
	d, err := os.ReadFile(filepath.Join(vars.FilesystemRoot, path))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, string(d)).To(Equal(expectedContent))
}

var _ = Describe("Kernel", func() {
	Context("Drivers", func() {
		var (
			k KernelInterface
		)
		configureFS := func(f *fakefilesystem.FS) {
			var (
				cleanFakeFs func()
				err         error
			)
			vars.FilesystemRoot, cleanFakeFs, err = f.Use()
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(cleanFakeFs)
		}
		BeforeEach(func() {
			k = newKernelInterface(nil)
		})
		Context("Unbind, UnbindDriverByBusAndDevice", func() {
			It("unknown device", func() {
				Expect(k.UnbindDriverByBusAndDevice(consts.BusPci, "unknown-dev")).NotTo(HaveOccurred())
			})
			It("known device, no driver", func() {
				configureFS(&fakefilesystem.FS{Dirs: []string{"/sys/bus/pci/devices/0000:d8:00.0"}})
				Expect(k.Unbind("0000:d8:00.0")).NotTo(HaveOccurred())
			})
			It("has driver, succeed", func() {
				configureFS(&fakefilesystem.FS{
					Dirs: []string{
						"/sys/bus/pci/devices/0000:d8:00.0",
						"/sys/bus/pci/drivers/test-driver"},
					Symlinks: map[string]string{
						"/sys/bus/pci/devices/0000:d8:00.0/driver": "../../../../bus/pci/drivers/test-driver"},
					Files: map[string][]byte{
						"/sys/bus/pci/drivers/test-driver/unbind": {}},
				})
				Expect(k.Unbind("0000:d8:00.0")).NotTo(HaveOccurred())
				// check that echo to unbind path was done
				assertFileContentsEquals("/sys/bus/pci/drivers/test-driver/unbind", "0000:d8:00.0")
			})
			It("has driver, failed to unbind", func() {
				configureFS(&fakefilesystem.FS{
					Dirs: []string{
						"/sys/bus/pci/devices/0000:d8:00.0"},
					Symlinks: map[string]string{
						"/sys/bus/pci/devices/0000:d8:00.0/driver": "../../../../bus/pci/drivers/test-driver"},
				})
				Expect(k.Unbind("0000:d8:00.0")).To(HaveOccurred())
			})
		})
		Context("HasDriver", func() {
			It("unknown device", func() {
				has, driver := k.HasDriver("unknown-dev")
				Expect(has).To(BeFalse())
				Expect(driver).To(BeEmpty())
			})
			It("known device, no driver", func() {
				configureFS(&fakefilesystem.FS{Dirs: []string{"/sys/bus/pci/devices/0000:d8:00.0"}})
				has, driver := k.HasDriver("0000:d8:00.0")
				Expect(has).To(BeFalse())
				Expect(driver).To(BeEmpty())
			})
			It("has driver", func() {
				configureFS(&fakefilesystem.FS{
					Dirs: []string{
						"/sys/bus/pci/devices/0000:d8:00.0",
						"/sys/bus/pci/drivers/test-driver"},
					Symlinks: map[string]string{
						"/sys/bus/pci/devices/0000:d8:00.0/driver": "../../../../bus/pci/drivers/test-driver"},
				})
				has, driver := k.HasDriver("0000:d8:00.0")
				Expect(has).To(BeTrue())
				Expect(driver).To(Equal("test-driver"))
			})
		})
		Context("BindDefaultDriver", func() {
			It("unknown device", func() {
				Expect(k.BindDefaultDriver("unknown-dev")).To(HaveOccurred())
			})
			It("no driver", func() {
				configureFS(&fakefilesystem.FS{
					Dirs: []string{
						"/sys/bus/pci/devices/0000:d8:00.0"},
					Files: map[string][]byte{
						"/sys/bus/pci/drivers_probe": {}, "/sys/bus/pci/devices/0000:d8:00.0/driver_override": {}},
				})
				Expect(k.BindDefaultDriver("0000:d8:00.0")).NotTo(HaveOccurred())
				// should probe driver for dev
				assertFileContentsEquals("/sys/bus/pci/drivers_probe", "0000:d8:00.0")
			})
			It("already bind to default driver", func() {
				configureFS(&fakefilesystem.FS{
					Dirs: []string{
						"/sys/bus/pci/devices/0000:d8:00.0"},
					Symlinks: map[string]string{
						"/sys/bus/pci/devices/0000:d8:00.0/driver": "../../../../bus/pci/drivers/test-driver"},
				})
				Expect(k.BindDefaultDriver("0000:d8:00.0")).NotTo(HaveOccurred())
			})
			It("bind to dpdk driver", func() {
				configureFS(&fakefilesystem.FS{
					Dirs: []string{
						"/sys/bus/pci/devices/0000:d8:00.0",
						"/sys/bus/pci/drivers/vfio-pci"},
					Symlinks: map[string]string{
						"/sys/bus/pci/devices/0000:d8:00.0/driver": "../../../../bus/pci/drivers/vfio-pci"},
					Files: map[string][]byte{
						"/sys/bus/pci/drivers_probe":           {},
						"/sys/bus/pci/drivers/vfio-pci/unbind": {}},
				})
				Expect(k.BindDefaultDriver("0000:d8:00.0")).NotTo(HaveOccurred())
				// should unbind from dpdk driver
				assertFileContentsEquals("/sys/bus/pci/drivers/vfio-pci/unbind", "0000:d8:00.0")
				// should probe driver for dev
				assertFileContentsEquals("/sys/bus/pci/drivers_probe", "0000:d8:00.0")
			})
		})
		Context("BindDpdkDriver", func() {
			It("unknown device", func() {
				Expect(k.BindDpdkDriver("unknown-dev", "vfio-pci")).To(HaveOccurred())
			})
			It("no driver", func() {
				configureFS(&fakefilesystem.FS{
					Dirs: []string{
						"/sys/bus/pci/devices/0000:d8:00.0",
						"/sys/bus/pci/drivers/vfio-pci"},
					Files: map[string][]byte{
						"/sys/bus/pci/devices/0000:d8:00.0/driver_override": {}},
				})
				Expect(k.BindDpdkDriver("0000:d8:00.0", "vfio-pci")).NotTo(HaveOccurred())
				// should reset driver override
				assertFileContentsEquals("/sys/bus/pci/devices/0000:d8:00.0/driver_override", "\x00")
			})
			It("already bind to required driver", func() {
				configureFS(&fakefilesystem.FS{
					Dirs: []string{
						"/sys/bus/pci/devices/0000:d8:00.0"},
					Symlinks: map[string]string{
						"/sys/bus/pci/devices/0000:d8:00.0/driver": "../../../../bus/pci/drivers/vfio-pci"},
				})
				Expect(k.BindDpdkDriver("0000:d8:00.0", "vfio-pci")).NotTo(HaveOccurred())
			})
			It("bind to wrong driver", func() {
				configureFS(&fakefilesystem.FS{
					Dirs: []string{
						"/sys/bus/pci/devices/0000:d8:00.0",
						"/sys/bus/pci/drivers/test-driver",
						"/sys/bus/pci/drivers/vfio-pci"},
					Symlinks: map[string]string{
						"/sys/bus/pci/devices/0000:d8:00.0/driver": "../../../../bus/pci/drivers/test-driver"},
					Files: map[string][]byte{
						"/sys/bus/pci/drivers/test-driver/unbind":           {},
						"/sys/bus/pci/drivers/vfio-pci/bind":                {},
						"/sys/bus/pci/devices/0000:d8:00.0/driver_override": {}},
				})
				Expect(k.BindDpdkDriver("0000:d8:00.0", "vfio-pci")).NotTo(HaveOccurred())
				// should unbind from driver1
				assertFileContentsEquals("/sys/bus/pci/drivers/test-driver/unbind", "0000:d8:00.0")
				// should bind to driver2
				assertFileContentsEquals("/sys/bus/pci/drivers/vfio-pci/bind", "0000:d8:00.0")
			})
			It("fail to bind", func() {
				configureFS(&fakefilesystem.FS{
					Dirs: []string{
						"/sys/bus/pci/devices/0000:d8:00.0",
						"/sys/bus/pci/drivers/test-driver"},
					Symlinks: map[string]string{
						"/sys/bus/pci/devices/0000:d8:00.0/driver": "../../../../bus/pci/drivers/test-driver"},
					Files: map[string][]byte{
						"/sys/bus/pci/drivers/test-driver/unbind":           {},
						"/sys/bus/pci/devices/0000:d8:00.0/driver_override": {}},
				})
				Expect(k.BindDpdkDriver("0000:d8:00.0", "vfio-pci")).To(HaveOccurred())
			})
		})
		Context("BindDriverByBusAndDevice", func() {
			It("device doesn't support driver_override", func() {
				configureFS(&fakefilesystem.FS{
					Dirs: []string{
						"/sys/bus/pci/devices/0000:d8:00.0",
						"/sys/bus/pci/drivers/test-driver",
						"/sys/bus/pci/drivers/vfio-pci"},
					Symlinks: map[string]string{
						"/sys/bus/pci/devices/0000:d8:00.0/driver": "../../../../bus/pci/drivers/test-driver"},
					Files: map[string][]byte{
						"/sys/bus/pci/drivers/test-driver/unbind": {},
						"/sys/bus/pci/drivers/vfio-pci/bind":      {}},
				})
				Expect(k.BindDriverByBusAndDevice(consts.BusPci, "0000:d8:00.0", "vfio-pci")).NotTo(HaveOccurred())
				// should unbind from driver1
				assertFileContentsEquals("/sys/bus/pci/drivers/test-driver/unbind", "0000:d8:00.0")
				// should bind to driver2
				assertFileContentsEquals("/sys/bus/pci/drivers/vfio-pci/bind", "0000:d8:00.0")
			})
		})
	})
})
