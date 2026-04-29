package webhook

import (
	"crypto/tls"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	cliflag "k8s.io/component-base/cli/flag"
)

var _ = Describe("TLS Config", func() {
	Describe("CipherNamesToIDs", func() {
		It("should convert OpenSSL and IANA cipher names to IDs", func() {
			names := []string{"ECDHE-ECDSA-AES128-GCM-SHA256", "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384"}
			ids, err := CipherNamesToIDs(names)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			}))
		})

		It("should handle TLS 1.3 cipher names", func() {
			names := []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384", "TLS_CHACHA20_POLY1305_SHA256"}
			ids, err := CipherNamesToIDs(names)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]uint16{
				tls.TLS_AES_128_GCM_SHA256,
				tls.TLS_AES_256_GCM_SHA384,
				tls.TLS_CHACHA20_POLY1305_SHA256,
			}))
		})

		It("should trim whitespace from names", func() {
			ids, err := CipherNamesToIDs([]string{" ECDHE-ECDSA-AES128-GCM-SHA256 "})
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}))
		})

		It("should return an error for unknown cipher names", func() {
			_, err := CipherNamesToIDs([]string{"UNKNOWN-CIPHER"})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not supported"))
			Expect(err.Error()).To(ContainSubstring("UNKNOWN-CIPHER"))
		})

		It("should reject insecure cipher names", func() {
			insecureCiphers := cliflag.InsecureTLSCiphers()
			if len(insecureCiphers) == 0 {
				Skip("current Go runtime does not expose insecure cipher suites")
			}

			insecureName := ""
			for name := range insecureCiphers {
				insecureName = name
				break
			}

			_, err := CipherNamesToIDs([]string{insecureName})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("insecure and not allowed"))
		})

		It("should return an error for empty cipher names", func() {
			_, err := CipherNamesToIDs([]string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", ""})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("empty TLS cipher suite name"))
		})

		It("should return nil for empty input", func() {
			ids, err := CipherNamesToIDs([]string{})
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(BeNil())
		})
	})

	Describe("ParseCipherSuitesFlag", func() {
		It("should return nil for empty input", func() {
			ids, err := ParseCipherSuitesFlag("")
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(BeNil())
		})

		It("should return nil for whitespace-only input", func() {
			ids, err := ParseCipherSuitesFlag("  ")
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(BeNil())
		})

		It("should parse comma-separated names", func() {
			ids, err := ParseCipherSuitesFlag("ECDHE-ECDSA-AES128-GCM-SHA256,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384")
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			}))
		})

		It("should return an error for trailing separators", func() {
			_, err := ParseCipherSuitesFlag("ECDHE-ECDSA-AES128-GCM-SHA256,")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("empty TLS cipher suite name"))
		})
	})

	Describe("TLSVersionToGo", func() {
		It("should return default for empty string", func() {
			v, err := TLSVersionToGo("")
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal(uint16(tls.VersionTLS12)))
		})

		It("should trim whitespace", func() {
			v, err := TLSVersionToGo("  VersionTLS12  ")
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal(uint16(tls.VersionTLS12)))
		})

		It("should convert VersionTLS13", func() {
			v, err := TLSVersionToGo("VersionTLS13")
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal(uint16(tls.VersionTLS13)))
		})

		It("should reject VersionTLS10", func() {
			_, err := TLSVersionToGo("VersionTLS10")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("VersionTLS12 or higher"))
		})

		It("should reject VersionTLS11", func() {
			_, err := TLSVersionToGo("VersionTLS11")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("VersionTLS12 or higher"))
		})

		It("should return error for unknown version", func() {
			_, err := TLSVersionToGo("VersionTLS99")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unknown tls version"))
		})
	})
})

func TestTLSConfig(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "TLS Config Suite")
}
