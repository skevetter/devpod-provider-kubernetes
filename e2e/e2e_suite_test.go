package e2e

import (
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	_ "github.com/skevetter/devpod-provider-kubernetes/e2e/pullsecrets" // Register tests.
)

func TestRunE2ETests(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Devpod provider kubernetes e2e suite")
}
