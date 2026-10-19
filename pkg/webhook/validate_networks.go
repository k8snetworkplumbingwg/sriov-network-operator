package webhook

import (
	"fmt"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/controllers"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

func validateSriovNetwork(cr *sriovnetworkv1.SriovNetwork) (bool, []string, error) {
	if err := validateNetworkNamespace(cr); err != nil {
		return false, nil, err
	}
	if err := validateCapabilities(cr.Spec.Capabilities); err != nil {
		return false, nil, err
	}
	if err := validateIPAM(cr.Spec.IPAM); err != nil {
		return false, nil, err
	}
	if err := validateMetaPlugins(cr.Spec.MetaPluginsConfig); err != nil {
		return false, nil, err
	}
	if err := validateLogFile(cr.Spec.LogFile); err != nil {
		return false, nil, err
	}
	return true, nil, nil
}

func validateSriovIBNetwork(cr *sriovnetworkv1.SriovIBNetwork) (bool, []string, error) {
	if err := validateNetworkNamespace(cr); err != nil {
		return false, nil, err
	}
	if err := validateCapabilities(cr.Spec.Capabilities); err != nil {
		return false, nil, err
	}
	if err := validateIPAM(cr.Spec.IPAM); err != nil {
		return false, nil, err
	}
	if err := validateMetaPlugins(cr.Spec.MetaPluginsConfig); err != nil {
		return false, nil, err
	}
	return true, nil, nil
}

func validateOVSNetwork(cr *sriovnetworkv1.OVSNetwork) (bool, []string, error) {
	if err := validateNetworkNamespace(cr); err != nil {
		return false, nil, err
	}
	if err := validateCapabilities(cr.Spec.Capabilities); err != nil {
		return false, nil, err
	}
	if err := validateIPAM(cr.Spec.IPAM); err != nil {
		return false, nil, err
	}
	if err := validateMetaPlugins(cr.Spec.MetaPluginsConfig); err != nil {
		return false, nil, err
	}
	if err := validateBridgeName(cr.Spec.Bridge); err != nil {
		return false, nil, err
	}
	if err := validateInterfaceType(cr.Spec.InterfaceType); err != nil {
		return false, nil, err
	}
	return true, nil, nil
}

func validateNetworkNamespace(cr controllers.NetworkCRInstance) error {
	if cr.GetNamespace() != vars.Namespace && cr.NetworkNamespace() != "" {
		return fmt.Errorf(".Spec.NetworkNamespace field can't be specified if the resource is not in the %s namespace", vars.Namespace)
	}

	return nil
}
