package dra

import (
	consts "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
)

// Label and attribute keys for operator-managed DRA objects (DeviceClass,
// DeviceAttributes, SriovResourcePolicy). The generated-by label value is
// consts.SriovNetworkOperatorIdentifier.
const (
	GeneratedByLabel             = "sriovnetwork.openshift.io/generated-by"
	DeviceClassResourceNameLabel = "sriovnetwork.openshift.io/resource-name"
	SriovResourcePolicyNodeLabel = "sriovnetwork.openshift.io/node"
	ResourcePoolLabel            = "sriovnetwork.openshift.io/resource-pool"
	ResourceNameAttributeKey     = "k8s.cni.cncf.io/resourceName"
)

// OperatorGeneratedByLabels returns a new label map for listing or tagging
// operator-managed DRA resources. Callers may add further labels to the map.
func OperatorGeneratedByLabels() map[string]string {
	return map[string]string{
		GeneratedByLabel: consts.SriovNetworkOperatorIdentifier,
	}
}
