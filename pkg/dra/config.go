package dra

import (
	"fmt"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
)

// PolicyResourceDrivers collects the allocation driver for each resource name.
// A DeviceClass applies one configuration to the entire resource, even when
// policies select different nodes. Mixing VFIO and netdevice would therefore
// risk rebinding a netdevice allocation to VFIO.
func PolicyResourceDrivers(policies []sriovnetworkv1.SriovNetworkNodePolicy) (map[string]string, error) {
	drivers := make(map[string]string)
	owners := make(map[string]string)
	for i := range policies {
		policy := &policies[i]
		resourceName := policy.Spec.ResourceName
		if policy.Name == consts.DefaultPolicyName || resourceName == "" {
			continue
		}
		driver := ""
		if policy.Spec.DeviceType == consts.DeviceTypeVfioPci {
			driver = consts.DeviceTypeVfioPci
		}
		if previous, found := drivers[resourceName]; found && previous != driver {
			return nil, fmt.Errorf("policies %q and %q have incompatible deviceType settings for DRA resource %q: vfio-pci and netdevice cannot share a DeviceClass",
				owners[resourceName], policy.Name, resourceName)
		}
		drivers[resourceName] = driver
		owners[resourceName] = policy.Name
	}
	return drivers, nil
}
