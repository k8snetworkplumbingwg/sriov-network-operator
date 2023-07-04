package conformance

import (
	"flag"
	"log"
	"path"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"

	kniK8sReporter "github.com/openshift-kni/k8sreporter"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/test/util/k8sreporter"

	// Test files in this package must not end with `_test.go` suffix, as they are imported as go package
	_ "github.com/k8snetworkplumbingwg/sriov-network-operator/test/validation/tests"
)

var (
	reportPath     *string
	customReporter *kniK8sReporter.KubernetesReporter
	err            error
)

func init() {
	reportPath = flag.String("report", "", "the path of the report file containing details for failed tests")
}

func TestTest(t *testing.T) {
	RegisterFailHandler(Fail)

	if *reportPath != "" {
		*reportPath = path.Join(*reportPath, "sriov_validation_failure_report.log")
		customReporter, err = k8sreporter.New(*reportPath)
		if err != nil {
			log.Fatalf("Failed to create log reporter %s", err)
		}
	}

	RunSpecs(t, "SRIOV Operator validation tests")
}

var _ = ReportAfterEach(func(sr types.SpecReport) {
	if sr.Failed() == false {
		return
	}

	if *reportPath != "" {
		customReporter.Dump(util.LogsExtractDuration, sr.LeafNodeText)
	}
})
