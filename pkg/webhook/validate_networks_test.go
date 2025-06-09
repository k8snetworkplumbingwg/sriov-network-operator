package webhook

import (
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/controllers"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

func TestValidate_NetworkNamespace(t *testing.T) {
	g := NewGomegaWithT(t)
	defer func(previous string) { vars.Namespace = previous }(vars.Namespace)
	vars.Namespace = "operator-namespace"

	validNetworkNamespaces := []controllers.NetworkCRInstance{
		&SriovNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "operator-namespace"}, Spec: SriovNetworkSpec{NetworkNamespace: ""}},
		&SriovNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "operator-namespace"}, Spec: SriovNetworkSpec{NetworkNamespace: "xxx"}},
		&SriovNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "xxx"}, Spec: SriovNetworkSpec{NetworkNamespace: ""}},
		&SriovIBNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "operator-namespace"}, Spec: SriovIBNetworkSpec{NetworkNamespace: ""}},
		&SriovIBNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "operator-namespace"}, Spec: SriovIBNetworkSpec{NetworkNamespace: "xxx"}},
		&SriovIBNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "xxx"}, Spec: SriovIBNetworkSpec{NetworkNamespace: ""}},
		&OVSNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "operator-namespace"}, Spec: OVSNetworkSpec{NetworkNamespace: ""}},
		&OVSNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "operator-namespace"}, Spec: OVSNetworkSpec{NetworkNamespace: "xxx"}},
		&OVSNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "xxx"}, Spec: OVSNetworkSpec{NetworkNamespace: ""}},
	}

	var err error
	for _, n := range validNetworkNamespaces {
		err = validateNetworkNamespace(n)
		g.Expect(err).NotTo(HaveOccurred())
	}

	badNetworkNamespaces := []controllers.NetworkCRInstance{
		&SriovNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "xxx"}, Spec: SriovNetworkSpec{NetworkNamespace: "yyy"}},
		&SriovIBNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "xxx"}, Spec: SriovIBNetworkSpec{NetworkNamespace: "yyy"}},
		&OVSNetwork{ObjectMeta: metav1.ObjectMeta{Namespace: "xxx"}, Spec: OVSNetworkSpec{NetworkNamespace: "yyy"}},
	}

	for _, n := range badNetworkNamespaces {
		err = validateNetworkNamespace(n)
		g.Expect(err).To(HaveOccurred())
	}
}
