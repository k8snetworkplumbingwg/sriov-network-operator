package conformance

import (
	"flag"
	"log"
	"path"
	"testing"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/clean"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/reporters"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"

	kniK8sReporter "github.com/openshift-kni/k8sreporter"

	// Test files in this package must not end with `_test.go` suffix, as they are imported as go package
	_ "github.com/k8snetworkplumbingwg/sriov-network-operator/test/conformance/tests"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/k8sreporter"
)

var (
	err            error
	junitPath      *string
	reportPath     *string
	customReporter *kniK8sReporter.KubernetesReporter
)

func init() {
	junitPath = flag.String("junit", "", "the path for the junit format report")
	reportPath = flag.String("report", "", "the path of the report file containing details for failed tests")
}

func TestTest(t *testing.T) {
	RegisterFailHandler(Fail)

	if *reportPath != "" {
		*reportPath = path.Join(*reportPath, "sriov_conformance_failure_report.log")
		customReporter, err = k8sreporter.New(*reportPath)
		if err != nil {
			log.Fatalf("Failed to create log reporter %s", err)
		}
	}

	RunSpecs(t, "SRIOV Operator conformance tests")
}

var _ = ReportAfterSuite("conformance", func(report types.Report) {
	if *junitPath != "" {
		junitFile := path.Join(*junitPath, "junit_sriov_conformance.xml")
		reporters.GenerateJUnitReportWithConfig(report, junitFile, reporters.JunitReportConfig{
			OmitTimelinesForSpecState: types.SpecStatePassed | types.SpecStateSkipped,
			OmitLeafNodeType:          true,
			OmitSuiteSetupNodes:       true,
		})
	}
})

var _ = ReportAfterEach(func(sr types.SpecReport) {
	if sr.Failed() == false {
		return
	}

	if *reportPath != "" {
		customReporter.Dump(util.LogsExtractDuration, sr.LeafNodeText)
	}
})

var _ = BeforeSuite(func() {
	err := clean.All()
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	err := clean.All()
	Expect(err).NotTo(HaveOccurred())
})
