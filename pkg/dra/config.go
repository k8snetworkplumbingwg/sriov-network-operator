package dra

import (
	"fmt"
	"strings"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
)

// PolicyResourceValidator checks resource names, supported selectors, and
// class-wide device types. Callers supply only policies in the operator namespace
// and exclude the old version of a policy being updated.
type PolicyResourceValidator struct {
	draEnabled bool
	drivers    map[string]string
	owners     map[string]string
	classNames map[string]string
}

// NewPolicyResourceValidator always checks normalized name collisions. Selector
// support and driver compatibility are checked only in DRA mode.
// Netdevice policies may share a resource across vendors and kernel drivers;
// only mixing netdevice and vfio-pci is incompatible.
func NewPolicyResourceValidator(draEnabled bool) *PolicyResourceValidator {
	return &PolicyResourceValidator{
		draEnabled: draEnabled,
		drivers:    make(map[string]string),
		owners:     make(map[string]string),
		classNames: make(map[string]string),
	}
}

// Add validates a policy before adding its resource to the shared configuration.
func (v *PolicyResourceValidator) Add(policy *sriovnetworkv1.SriovNetworkNodePolicy) error {
	// The deprecated default policy is ignored by both resource renderers.
	if policy.Name == consts.DefaultPolicyName {
		return nil
	}
	if v.draEnabled {
		if policy.Spec.NicSelector.NetFilter != "" {
			return fmt.Errorf("policy %q: netFilter selector is not supported in DRA mode; use device-plugin mode", policy.Name)
		}
		for _, pfName := range policy.Spec.NicSelector.PfNames {
			if strings.Contains(pfName, "#") {
				return fmt.Errorf("policy %q: VF range in pfNames selector %q is not supported in DRA mode; use a whole PF name or device-plugin mode", policy.Name, pfName)
			}
		}
	}
	resourceName := policy.Spec.ResourceName
	if resourceName == "" {
		return fmt.Errorf("policy %q: resource name must not be empty", policy.Name)
	}
	className := ResourceNameToDeviceClassName(resourceName)
	if previous, found := v.classNames[className]; found && previous != resourceName {
		return fmt.Errorf("resourceName %q in policy %q normalizes to the same DRA device class name %q as policy %q resourceName %q",
			resourceName, policy.Name, className, v.owners[previous], previous)
	}
	// An empty allocation driver preserves the kernel driver configured by
	// the config daemon, allowing different vendors' netdevices to share a resource.
	driver := ""
	if policy.Spec.DeviceType == consts.DeviceTypeVfioPci {
		driver = consts.DeviceTypeVfioPci
	}
	if previous, found := v.drivers[resourceName]; v.draEnabled && found && previous != driver {
		return fmt.Errorf("policies %q and %q have incompatible deviceType settings for DRA resource %q: vfio-pci and netdevice cannot share a DeviceClass",
			v.owners[resourceName], policy.Name, resourceName)
	}
	v.classNames[className] = resourceName
	v.drivers[resourceName] = driver
	v.owners[resourceName] = policy.Name
	return nil
}

// PolicyResourceDrivers validates all DRA resources and collects their allocation
// drivers. No partial configuration is returned when policies conflict.
func PolicyResourceDrivers(policies []sriovnetworkv1.SriovNetworkNodePolicy) (map[string]string, error) {
	validator := NewPolicyResourceValidator(true)
	for i := range policies {
		if err := validator.Add(&policies[i]); err != nil {
			return nil, err
		}
	}
	return validator.drivers, nil
}
