package service

import (
	"path"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.uber.org/mock/gomock"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host/types"
	mock_utils "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils/mock"
)

// ovsVsctlVerb splits an ovs-vsctl invocation into its command verb and the
// global options preceding it.
func ovsVsctlVerb(call string) (string, []string) {
	var flags []string
	tokens := strings.Fields(call)
	for i := 0; i < len(tokens); i++ {
		switch {
		case tokens[i] == "-t":
			i++ // skip the timeout value
		case strings.HasPrefix(tokens[i], "-"):
			flags = append(flags, tokens[i])
		default:
			return tokens[i], flags
		}
	}
	return "", flags
}

var _ = Describe("Systemd", func() {
	var (
		utilsMock = &mock_utils.MockCmdInterface{}
		s         types.ServiceInterface
		srv       = types.Service{
			Name:    "test-service",
			Path:    "",
			Content: "",
		}
		t        FullGinkgoTInterface
		mockCtrl *gomock.Controller
	)

	BeforeEach(func() {
		t = GinkgoT()
		mockCtrl = gomock.NewController(t)
		utilsMock = mock_utils.NewMockCmdInterface(mockCtrl)
		s = New(utilsMock)
	})

	Context("Service manage", func() {
		It("should enable service", func() {
			// Set srv.Path so that path.Join(consts.Chroot, srv.Path) resolves into
			// a writable temp dir rather than the non-existent /host directory.
			tmpDir := GinkgoT().TempDir()
			srv.Path = ".." + path.Join(tmpDir, srv.Name+".service")
			srv.Content = "[Unit]\nDescription=Test Service\n"

			utilsMock.EXPECT().Chroot(gomock.Any()).Return(func() error { return nil }, nil)
			utilsMock.EXPECT().RunCommand("systemctl", "reenable", srv.Name).Return("", "", nil)
			err := s.EnableService(&srv)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("ReadOvsServiceInjectionManifestFile", func() {
		const manifest = "../../../../bindata/manifests/switchdev-config/ovs-units/ovs-vswitchd.service.yaml"

		It("should render the drop-in for the requested OVS config", func() {
			dropin, err := s.ReadOvsServiceInjectionManifestFile(manifest, map[string]string{"hw-offload": "true"})
			Expect(err).ToNot(HaveOccurred())
			Expect(dropin.Name).To(Equal("ovs-vswitchd.service"))
			Expect(dropin.Path).To(Equal("/usr/lib/systemd/system/ovs-vswitchd.service.d/10-hw-offload.conf"))
			Expect(dropin.Content).To(ContainSubstring(`"other_config:hw-offload=true"`))
			Expect(dropin.Content).To(ContainSubstring(`"external_ids:sriov-operator-owned-keys=hw-offload"`))
		})

		It("should pass --no-wait to every modifying ovs-vsctl call", func() {
			// ExecStartPre runs before ovs-vswitchd, so a modifying call without
			// --no-wait blocks waiting for it and then fails on timeout.
			dropin, err := s.ReadOvsServiceInjectionManifestFile(manifest, map[string]string{"hw-offload": "true"})
			Expect(err).ToNot(HaveOccurred())

			// get, the xargs-driven remove, and the two sets
			calls := strings.Split(dropin.Content, "/bin/ovs-vsctl ")[1:]
			Expect(calls).To(HaveLen(4))
			for _, call := range calls {
				verb, flags := ovsVsctlVerb(call)
				if verb == "get" {
					continue
				}
				Expect(flags).To(ContainElement("--no-wait"),
					"modifying ovs-vsctl %q call must not wait for ovs-vswitchd: %s", verb, call)
			}
		})

		It("should disable environment expansion on every ExecStartPre", func() {
			dropin, err := s.ReadOvsServiceInjectionManifestFile(manifest, map[string]string{"hw-offload": "true", "doca-init": "true"})
			Expect(err).ToNot(HaveOccurred())
			// The cleanup script relies on bash variables, and values may carry '$',
			// so systemd must not expand them.
			Expect(strings.Count(dropin.Content, "ExecStartPre=")).To(Equal(3))
			Expect(strings.Count(dropin.Content, "ExecStartPre=:")).To(Equal(3))
		})

		It("should escape values that systemd would otherwise interpret", func() {
			dropin, err := s.ReadOvsServiceInjectionManifestFile(manifest,
				map[string]string{"k": "a$b%c\"d`e'f"})
			Expect(err).ToNot(HaveOccurred())
			// systemd unescapes this back to the original value and passes it to
			// ovs-vsctl as a single argument, without a shell.
			Expect(dropin.Content).To(ContainSubstring("\"other_config:k=a$b%%c\\\"d`e'f\""))
		})

		DescribeTable("should reject a value that cannot be carried in the command",
			func(value string) {
				_, err := s.ReadOvsServiceInjectionManifestFile(manifest, map[string]string{"k": value})
				Expect(err).To(HaveOccurred())
			},
			Entry("newline", "a\nb"),
			Entry("carriage return", "a\rb"),
			Entry("NUL", "a\x00b"),
		)
	})
})
