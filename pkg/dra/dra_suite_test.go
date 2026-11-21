package dra

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDRA(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "DRA Suite")
}
