package utils

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	mcclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
)

// OpenshiftFlavor holds metadata about the type of Openshift environment the operator is in.
type OpenshiftFlavor string

const (
	// Hypershift flavor of openshift: https://github.com/openshift/hypershift
	OpenshiftFlavorHypershift OpenshiftFlavor = "hypershift"
	// OpenshiftFlavorDefault covers all remaining flavors of openshift not explicitly called out above
	OpenshiftFlavorDefault OpenshiftFlavor = "default"
)

// OpenshiftContext contains metadata and structs utilized to interact with Openshift clusters
type OpenshiftContext struct {
	// McClient is a client for MachineConfigs in an Openshift environment
	McClient mcclientset.Interface

	// IsOpenShiftCluster boolean to point out if the cluster is an OpenShift cluster
	IsOpenShiftCluster bool

	// OpenshiftFlavor holds metadata about the type of Openshift environment the operator is in.
	OpenshiftFlavor OpenshiftFlavor
}

func NewOpenshiftContext(config *rest.Config, scheme *runtime.Scheme) (*OpenshiftContext, error) {
	if ClusterType != ClusterTypeOpenshift {
		return &OpenshiftContext{nil, false, ""}, nil
	}

	mcclient, err := mcclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	openshiftFlavor := OpenshiftFlavorDefault
	infraClient, err := client.New(config, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, err
	}

	isHypershift, err := IsExternalControlPlaneCluster(infraClient)
	if err != nil {
		return nil, err
	}

	if isHypershift {
		openshiftFlavor = OpenshiftFlavorHypershift
	}

	return &OpenshiftContext{mcclient, true, openshiftFlavor}, nil
}

func (c OpenshiftContext) IsOpenshiftCluster() bool {
	return c.IsOpenShiftCluster
}

func (c OpenshiftContext) IsHypershift() bool {
	return c.OpenshiftFlavor == OpenshiftFlavorHypershift
}

func (c OpenshiftContext) GetNodeMachinePoolName(node *corev1.Node) (string, error) {
	desiredConfig, ok := node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey]
	if !ok {
		log.Log.Error(nil, "getNodeMachinePool(): Failed to find the the desiredConfig Annotation")
		return "", fmt.Errorf("getNodeMachinePool(): Failed to find the the desiredConfig Annotation")
	}
	mc, err := c.McClient.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), desiredConfig, metav1.GetOptions{})
	if err != nil {
		log.Log.Error(err, "getNodeMachinePool(): Failed to get the desired Machine Config")
		return "", err
	}
	for _, owner := range mc.OwnerReferences {
		if owner.Kind == "MachineConfigPool" {
			return owner.Name, nil
		}
	}

	log.Log.Error(nil, "getNodeMachinePool(): Failed to find the MCP of the node")
	return "", fmt.Errorf("getNodeMachinePool(): Failed to find the MCP of the node")
}
