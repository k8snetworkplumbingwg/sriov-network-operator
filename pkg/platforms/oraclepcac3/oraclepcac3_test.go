/*
Copyright (c) 2025, Oracle and/or its affiliates.

Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl.
*/

package oraclepcac3

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-retryablehttp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// BEGIN - To read a JSON file and return loaded data
func loadJsonFromFile(filepath string) []byte {
	data, err := os.ReadFile(filepath)
	if err != nil {
		panic(fmt.Sprintf("failed to read JSON file: %v", err))
	}
	return data
}

// END - To read a JSON file and return loaded data

// BEGIN - Mock HTTP client to return a dummy response VNIC details
type MockRoundTripper struct {
	ResponseBody string
	StatusCode   int
}

func (m *MockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: m.StatusCode,
		Body:       io.NopCloser(strings.NewReader(m.ResponseBody)),
		Header:     make(http.Header),
	}, nil
}

// END - Mock HTTP client to return a dummy response VNIC details

// BEGIN - Data structure and function to get VF details
type MockVfDetector struct{}

type InterfaceData struct {
	Name    string `json:"name"`
	PciID   string `json:"pciID"`
	Link    string `json:"link"`
	MacAddr string `json:"macAddr"`
}

var interfaceData []InterfaceData
var interfaceDataMap map[string]InterfaceData

func loadMockInterfaces(filename string) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &interfaceData); err != nil {
		return err
	}

	// Populate map for quick lookup
	interfaceDataMap = make(map[string]InterfaceData)
	for _, iface := range interfaceData {
		interfaceDataMap[iface.Name] = iface
	}
	return nil
}

func (m MockVfDetector) ReadDir(dirname string) ([]os.FileInfo, error) {
	var mockFiles []os.FileInfo
	for _, iface := range interfaceData {
		mockFiles = append(mockFiles, mockFileInfo{name: iface.Name})
	}
	return mockFiles, nil
}

func (m MockVfDetector) ReadLink(name string) (string, error) {
	base := filepath.Base(name)
	if iface, exists := interfaceDataMap[base]; exists {
		return iface.Link, nil
	}
	return "", fmt.Errorf("interface not found: %s", base)
}

func (m MockVfDetector) RunLspci(pciID string) (string, error) {
	for _, iface := range interfaceData {
		if iface.PciID == pciID {
			return "Ethernet controller: Mellanox Technologies ConnectX Family mlx5Gen Virtual Function", nil
		}
	}
	return "", nil
}

type mockFileInfo struct{ name string }

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return 0 }
func (m mockFileInfo) Mode() os.FileMode  { return 0 }
func (m mockFileInfo) ModTime() time.Time { return time.Time{} }
func (m mockFileInfo) IsDir() bool        { return false }
func (m mockFileInfo) Sys() interface{}   { return nil }

// END - Data structure and function to get VF details

func TestUtilsVirtual(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Oracle PCA/C3 Platform Tests")
}

func init() {
	err := loadMockInterfaces("./testdata/interfaces.json")
	if err != nil {
		log.Fatalf("Failed to load interfaces.json: %v", err)
	}
}

var _ = Describe("Oracle PCA/C3 Platform", func() {
	Context("Get VF Interface Names and Get VNIC Details From Metadata Service", func() {
		It("Should return a list of virtual functions interfaces name and a dummy JSON from mocked HTTP client", func() {
			// Get VNIC metadata using API
			dummyJSON := loadJsonFromFile("./testdata/vnic_metadata.json")
			mockTransport := &MockRoundTripper{
				ResponseBody: string(dummyJSON),
				StatusCode:   200,
			}

			// Standard http.Client using the mock transport
			stdClient := &http.Client{
				Transport: mockTransport,
			}

			retryableClient := retryablehttp.NewClient()
			retryableClient.HTTPClient = stdClient
			retryableClient.RetryMax = 5

			metaData, err1 := getOraclePcaC3DataFromMetadataService(retryableClient)
			Expect(err1).To(BeNil())

			// Get Virtual Function Interfaces detail
			vfDetector := MockVfDetector{}
			vfnicInfos, err2 := getVirtualFunctionNicInfo(vfDetector)
			Expect(err2).ToNot(HaveOccurred())
			Expect(len(vfnicInfos)).To(Equal(8))

			// Try to check if pci address and mac address are properly mapped
			mockGetNetDevMac := func(iface string) string {
				if iface, exists := interfaceDataMap[iface]; exists {
					return iface.MacAddr
				}
				return ""
			}
			devicesInfo, err3 := mapMacAndPCIAddress(vfnicInfos, metaData, mockGetNetDevMac)
			Expect(err3).ToNot(HaveOccurred())
			Expect(len(devicesInfo)).To(Equal(8))

			j := 0
			fmt.Println(j)
			totalLength := len(devicesInfo)
			for j := 0; j < totalLength; j++ {
				pciAddr := vfnicInfos[j].PciID
				interfaceName := vfnicInfos[j].InterfaceName
				Expect(strings.ToLower(devicesInfo[pciAddr].MacAddress)).To(Equal(interfaceDataMap[interfaceName].MacAddr))
			}
		})
	})
})
