package webhook

import (
	"encoding/json"
	"strings"

	v1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
)

const (
	patchOperationAdd = "add"
	patchOperationKey = "op"
	patchPathKey      = "path"
	patchValueKey     = "value"
	isRdmaPatchPath   = "/spec/isRdma"
)

var (
	defaultPriorityPatch  = map[string]interface{}{patchOperationKey: patchOperationAdd, patchPathKey: "/spec/priority", patchValueKey: 99}
	defaultIsRdmaPatch    = map[string]interface{}{patchOperationKey: patchOperationAdd, patchPathKey: isRdmaPatchPath, patchValueKey: false}
	InfiniBandIsRdmaPatch = map[string]interface{}{patchOperationKey: patchOperationAdd, patchPathKey: isRdmaPatchPath, patchValueKey: true}
)

// mutateSriovNetworkNodePolicy adds default policy values and enables RDMA for
// InfiniBand policies that do not use the vfio-pci device type.
func mutateSriovNetworkNodePolicy(cr map[string]interface{}) (*v1.AdmissionResponse, error) {
	log.Log.V(2).Info("mutateSriovNetworkNodePolicy(): set default value")
	reviewResponse := v1.AdmissionResponse{}
	reviewResponse.Allowed = true

	name := cr["metadata"].(map[string]interface{})["name"]
	// Note(adrianc): the "default" policy is deprecated, we keep this skip below
	// in case we encounter it in the cluster.
	if name == constants.DefaultPolicyName {
		// skip the default policy
		return &reviewResponse, nil
	}

	patchs := []map[string]interface{}{}
	specMap, ok := cr["spec"].(map[string]interface{})
	if !ok {
		return &reviewResponse, nil
	}
	if _, ok := specMap["priority"]; !ok {
		log.Log.V(2).Info("mutateSriovNetworkNodePolicy(): set default priority to lowest for", "policy-name", name)
		patchs = append(patchs, defaultPriorityPatch)
	}
	if _, ok := specMap["isRdma"]; !ok {
		log.Log.V(2).Info("mutateSriovNetworkNodePolicy(): set default isRdma to false for policy", "policy-name", name)
		patchs = append(patchs, defaultIsRdmaPatch)
	}
	// Device with InfiniBand link type requires isRdma to be true, except for vfio-pci device type
	if str, ok := specMap["linkType"].(string); ok && strings.EqualFold(str, constants.LinkTypeIB) {
		devType, _ := specMap["deviceType"].(string)
		if !strings.EqualFold(devType, constants.DeviceTypeVfioPci) {
			log.Log.V(2).Info("mutateSriovNetworkNodePolicy(): set isRdma to true for policy since ib link type is detected", "policy-name", name)
			patchs = append(patchs, InfiniBandIsRdmaPatch)
		}
	}
	var err error
	reviewResponse.Patch, err = json.Marshal(patchs)
	if err != nil {
		return nil, err
	}

	pt := v1.PatchTypeJSONPatch
	reviewResponse.PatchType = &pt
	return &reviewResponse, nil
}
