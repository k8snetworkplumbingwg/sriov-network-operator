package utils

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	render "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/render"
)

var log = logf.Log.WithName("sriovnetwork")

// SriovIBNetworkRenderNetAttDef renders a net-att-def for ib-sriov CNI
func SriovIBNetworkRenderNetAttDef(cr *sriovnetworkv1.SriovIBNetwork) (*uns.Unstructured, error) {
	logger := log.WithName("renderNetAttDef")
	logger.Info("Start to render IB SRIOV CNI NetworkAttachementDefinition")

	// render RawCNIConfig manifests
	data := render.MakeRenderData()
	data.Data["CniType"] = "ib-sriov"
	data.Data["SriovNetworkName"] = cr.Name
	if cr.Spec.NetworkNamespace == "" {
		data.Data["SriovNetworkNamespace"] = cr.Namespace
	} else {
		data.Data["SriovNetworkNamespace"] = cr.Spec.NetworkNamespace
	}
	data.Data["SriovCniResourceName"] = os.Getenv("RESOURCE_PREFIX") + "/" + cr.Spec.ResourceName

	data.Data["StateConfigured"] = true
	switch cr.Spec.LinkState {
	case "enable":
		data.Data["SriovCniState"] = "enable"
	case "disable":
		data.Data["SriovCniState"] = "disable"
	case "auto":
		data.Data["SriovCniState"] = "auto"
	default:
		data.Data["StateConfigured"] = false
	}

	if cr.Spec.Capabilities == "" {
		data.Data["CapabilitiesConfigured"] = false
	} else {
		data.Data["CapabilitiesConfigured"] = true
		data.Data["SriovCniCapabilities"] = cr.Spec.Capabilities
	}

	if cr.Spec.IPAM != "" {
		data.Data["SriovCniIpam"] = "\"ipam\":" + strings.Join(strings.Fields(cr.Spec.IPAM), "")
	} else {
		data.Data["SriovCniIpam"] = "\"ipam\":{}"
	}

	// metaplugins for the infiniband cni
	data.Data["MetaPluginsConfigured"] = false
	if cr.Spec.MetaPluginsConfig != "" {
		data.Data["MetaPluginsConfigured"] = true
		data.Data["MetaPlugins"] = cr.Spec.MetaPluginsConfig
	}

	objs, err := render.RenderDir(sriovnetworkv1.ManifestsPath, &data)
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		raw, _ := json.Marshal(obj)
		logger.Info("render NetworkAttachementDefinition output", "raw", string(raw))
	}
	return objs[0], nil
}

// SriovIBNetworkDeleteNetAttDef deletes the generated net-att-def CR
func SriovIBNetworkDeleteNetAttDef(cr *sriovnetworkv1.SriovIBNetwork, c client.Client) error {
	// Fetch the NetworkAttachmentDefinition instance
	instance := &netattdefv1.NetworkAttachmentDefinition{}
	namespace := cr.GetNamespace()
	if cr.Spec.NetworkNamespace != "" {
		namespace = cr.Spec.NetworkNamespace
	}
	err := c.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: cr.GetName()}, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	err = c.Delete(context.TODO(), instance)
	if err != nil {
		return err
	}
	return nil
}

// SriovNetworkRenderNetAttDef renders a net-att-def for sriov CNI
func SriovNetworkRenderNetAttDef(cr *sriovnetworkv1.SriovNetwork) (*uns.Unstructured, error) {
	logger := log.WithName("renderNetAttDef")
	logger.Info("Start to render SRIOV CNI NetworkAttachementDefinition")

	// render RawCNIConfig manifests
	data := render.MakeRenderData()
	data.Data["CniType"] = "sriov"
	data.Data["SriovNetworkName"] = cr.Name
	if cr.Spec.NetworkNamespace == "" {
		data.Data["SriovNetworkNamespace"] = cr.Namespace
	} else {
		data.Data["SriovNetworkNamespace"] = cr.Spec.NetworkNamespace
	}
	data.Data["SriovCniResourceName"] = os.Getenv("RESOURCE_PREFIX") + "/" + cr.Spec.ResourceName
	data.Data["SriovCniVlan"] = cr.Spec.Vlan

	if cr.Spec.VlanQoS <= 7 && cr.Spec.VlanQoS >= 0 {
		data.Data["VlanQoSConfigured"] = true
		data.Data["SriovCniVlanQoS"] = cr.Spec.VlanQoS
	} else {
		data.Data["VlanQoSConfigured"] = false
	}

	if cr.Spec.Capabilities == "" {
		data.Data["CapabilitiesConfigured"] = false
	} else {
		data.Data["CapabilitiesConfigured"] = true
		data.Data["SriovCniCapabilities"] = cr.Spec.Capabilities
	}

	data.Data["SpoofChkConfigured"] = true
	switch cr.Spec.SpoofChk {
	case "off":
		data.Data["SriovCniSpoofChk"] = "off"
	case "on":
		data.Data["SriovCniSpoofChk"] = "on"
	default:
		data.Data["SpoofChkConfigured"] = false
	}

	data.Data["TrustConfigured"] = true
	switch cr.Spec.Trust {
	case "on":
		data.Data["SriovCniTrust"] = "on"
	case "off":
		data.Data["SriovCniTrust"] = "off"
	default:
		data.Data["TrustConfigured"] = false
	}

	data.Data["StateConfigured"] = true
	switch cr.Spec.LinkState {
	case "enable":
		data.Data["SriovCniState"] = "enable"
	case "disable":
		data.Data["SriovCniState"] = "disable"
	case "auto":
		data.Data["SriovCniState"] = "auto"
	default:
		data.Data["StateConfigured"] = false
	}

	data.Data["MinTxRateConfigured"] = false
	if cr.Spec.MinTxRate != nil {
		if *cr.Spec.MinTxRate >= 0 {
			data.Data["MinTxRateConfigured"] = true
			data.Data["SriovCniMinTxRate"] = *cr.Spec.MinTxRate
		}
	}

	data.Data["MaxTxRateConfigured"] = false
	if cr.Spec.MaxTxRate != nil {
		if *cr.Spec.MaxTxRate >= 0 {
			data.Data["MaxTxRateConfigured"] = true
			data.Data["SriovCniMaxTxRate"] = *cr.Spec.MaxTxRate
		}
	}

	if cr.Spec.IPAM != "" {
		data.Data["SriovCniIpam"] = "\"ipam\":" + strings.Join(strings.Fields(cr.Spec.IPAM), "")
	} else {
		data.Data["SriovCniIpam"] = "\"ipam\":{}"
	}

	data.Data["MetaPluginsConfigured"] = false
	if cr.Spec.MetaPluginsConfig != "" {
		data.Data["MetaPluginsConfigured"] = true
		data.Data["MetaPlugins"] = cr.Spec.MetaPluginsConfig
	}

	objs, err := render.RenderDir(sriovnetworkv1.ManifestsPath, &data)
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		raw, _ := json.Marshal(obj)
		logger.Info("render NetworkAttachementDefinition output", "raw", string(raw))
	}
	return objs[0], nil
}
