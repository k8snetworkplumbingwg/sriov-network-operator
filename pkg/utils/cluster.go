package utils

import (
	"context"
	"fmt"
	"os"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
)

const (
	// default Infrastructure resource name for Openshift
	infraResourceName        = "cluster"
	workerRoleName           = "worker"
	masterRoleName           = "master"
	workerNodeLabelKey       = "node-role.kubernetes.io/worker"
	masterNodeLabelKey       = "node-role.kubernetes.io/master"
	controlPlaneNodeLabelKey = "node-role.kubernetes.io/control-plane"
)

var (
	oneNode     = intstr.FromInt32(1)
	defaultNpcl = &sriovnetworkv1.SriovNetworkPoolConfig{Spec: sriovnetworkv1.SriovNetworkPoolConfigSpec{
		MaxUnavailable: &oneNode,
		NodeSelector:   &metav1.LabelSelector{},
		RdmaMode:       ""}}
)

func getNodeRole(node corev1.Node) string {
	for k := range node.Labels {
		if k == workerNodeLabelKey {
			return workerRoleName
		} else if k == masterNodeLabelKey || k == controlPlaneNodeLabelKey {
			return masterRoleName
		}
	}
	return ""
}

func IsSingleNodeCluster(c client.Client) (bool, error) {
	if os.Getenv("CLUSTER_TYPE") == consts.ClusterTypeOpenshift {
		topo, err := openshiftControlPlaneTopologyStatus(c)
		if err != nil {
			return false, err
		}
		if topo == configv1.SingleReplicaTopologyMode {
			return true, nil
		}
		return false, nil
	}
	return k8sSingleNodeClusterStatus(c)
}

// IsExternalControlPlaneCluster detects control plane location of the cluster.
// On OpenShift, the control plane topology is configured in configv1.Infrastucture struct.
// On kubernetes, it is determined by which node the sriov operator is scheduled on. If operator
// pod is schedule on worker node, it is considered as external control plane.
func IsExternalControlPlaneCluster(c client.Client) (bool, error) {
	if os.Getenv("CLUSTER_TYPE") == consts.ClusterTypeOpenshift {
		topo, err := openshiftControlPlaneTopologyStatus(c)
		if err != nil {
			return false, err
		}
		if topo == "External" {
			return true, nil
		}
	} else if os.Getenv("CLUSTER_TYPE") == consts.ClusterTypeKubernetes {
		role, err := operatorNodeRole(c)
		if err != nil {
			return false, err
		}
		if role == workerRoleName {
			return true, nil
		}
	}
	return false, nil
}

func k8sSingleNodeClusterStatus(c client.Client) (bool, error) {
	nodeList := &corev1.NodeList{}
	err := c.List(context.TODO(), nodeList)
	if err != nil {
		log.Log.Error(err, "k8sSingleNodeClusterStatus(): Failed to list nodes")
		return false, err
	}

	if len(nodeList.Items) == 1 {
		log.Log.Info("k8sSingleNodeClusterStatus(): one node found in the cluster")
		return true, nil
	}
	return false, nil
}

// operatorNodeRole returns role of the node where operator is scheduled on
func operatorNodeRole(c client.Client) (string, error) {
	node := corev1.Node{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: os.Getenv("NODE_NAME")}, &node)
	if err != nil {
		log.Log.Error(err, "k8sIsExternalTopologyMode(): Failed to get node")
		return "", err
	}

	return getNodeRole(node), nil
}

func openshiftControlPlaneTopologyStatus(c client.Client) (configv1.TopologyMode, error) {
	infra := &configv1.Infrastructure{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: infraResourceName}, infra)
	if err != nil {
		return "", fmt.Errorf("openshiftControlPlaneTopologyStatus(): Failed to get Infrastructure (name: %s): %v", infraResourceName, err)
	}
	return infra.Status.ControlPlaneTopology, nil
}

// ObjectHasAnnotationKey checks if a kubernetes object already contains annotation
func ObjectHasAnnotationKey(obj metav1.Object, annoKey string) bool {
	_, hasKey := obj.GetAnnotations()[annoKey]
	return hasKey
}

// ObjectHasAnnotation checks if a kubernetes object already contains annotation
func ObjectHasAnnotation(obj metav1.Object, annoKey string, value string) bool {
	if anno, ok := obj.GetAnnotations()[annoKey]; ok && (anno == value) {
		return true
	}
	return false
}

// AnnotateObject adds annotation to a kubernetes object
func AnnotateObject(ctx context.Context, obj client.Object, key, value string, c client.Client) error {
	log.Log.V(2).Info("AnnotateObject(): Annotate object",
		"objectName", obj.GetName(),
		"objectKind", obj.GetObjectKind(),
		"annotation", value)
	newObj := obj.DeepCopyObject().(client.Object)
	if newObj.GetAnnotations() == nil {
		newObj.SetAnnotations(map[string]string{})
	}

	if newObj.GetAnnotations()[key] != value {
		newObj.GetAnnotations()[key] = value
		patch := client.MergeFrom(obj)
		err := c.Patch(ctx,
			newObj, patch)
		if err != nil {
			log.Log.Error(err, "annotateObject(): Failed to patch object")
			return err
		}
	}

	return nil
}

// AnnotateNode add annotation to a node
func AnnotateNode(ctx context.Context, nodeName string, key, value string, c client.Client) error {
	node := &corev1.Node{}
	err := c.Get(context.TODO(), client.ObjectKey{Name: nodeName}, node)
	if err != nil {
		return err
	}

	return AnnotateObject(ctx, node, key, value, c)
}

func FindNodePoolConfig(ctx context.Context, node *corev1.Node, c client.Client) (*sriovnetworkv1.SriovNetworkPoolConfig, []corev1.Node, error) {
	logger := log.FromContext(ctx)
	logger.Info("FindNodePoolConfig():")
	// get all the sriov network pool configs
	npcl := &sriovnetworkv1.SriovNetworkPoolConfigList{}
	err := c.List(ctx, npcl)
	if err != nil {
		logger.Error(err, "failed to list sriovNetworkPoolConfig")
		return nil, nil, err
	}

	selectedNpcl := []*sriovnetworkv1.SriovNetworkPoolConfig{}
	nodesInPools := map[string]interface{}{}

	for _, npc := range npcl.Items {
		// we skip hw offload objects
		if npc.Spec.OvsHardwareOffloadConfig.Name != "" {
			continue
		}

		if npc.Spec.NodeSelector == nil {
			npc.Spec.NodeSelector = &metav1.LabelSelector{}
		}

		selector, err := metav1.LabelSelectorAsSelector(npc.Spec.NodeSelector)
		if err != nil {
			logger.Error(err, "failed to create label selector from nodeSelector", "nodeSelector", npc.Spec.NodeSelector)
			return nil, nil, err
		}

		if selector.Matches(labels.Set(node.Labels)) {
			selectedNpcl = append(selectedNpcl, npc.DeepCopy())
		}

		nodeList := &corev1.NodeList{}
		err = c.List(ctx, nodeList, &client.ListOptions{LabelSelector: selector})
		if err != nil {
			logger.Error(err, "failed to list all the nodes matching the pool with label selector from nodeSelector",
				"machineConfigPoolName", npc,
				"nodeSelector", npc.Spec.NodeSelector)
			return nil, nil, err
		}

		for _, nodeName := range nodeList.Items {
			nodesInPools[nodeName.Name] = nil
		}
	}

	if len(selectedNpcl) > 1 {
		// don't allow the node to be part of multiple pools
		err = fmt.Errorf("node is part of more then one pool")
		logger.Error(err, "multiple pools founded for a specific node", "numberOfPools", len(selectedNpcl), "pools", selectedNpcl)
		return nil, nil, err
	} else if len(selectedNpcl) == 1 {
		// found one pool for our node
		logger.V(2).Info("found sriovNetworkPool", "pool", *selectedNpcl[0])
		selector, err := metav1.LabelSelectorAsSelector(selectedNpcl[0].Spec.NodeSelector)
		if err != nil {
			logger.Error(err, "failed to create label selector from nodeSelector", "nodeSelector", selectedNpcl[0].Spec.NodeSelector)
			return nil, nil, err
		}

		// list all the nodes that are also part of this pool and return them
		nodeList := &corev1.NodeList{}
		err = c.List(ctx, nodeList, &client.ListOptions{LabelSelector: selector})
		if err != nil {
			logger.Error(err, "failed to list nodes using with label selector", "labelSelector", selector)
			return nil, nil, err
		}

		return selectedNpcl[0], nodeList.Items, nil
	} else {
		// in this case we get all the nodes and remove the ones that already part of any pool
		logger.V(1).Info("node doesn't belong to any pool, using default drain configuration with MaxUnavailable of one", "pool", *defaultNpcl)
		nodeList := &corev1.NodeList{}
		err = c.List(ctx, nodeList)
		if err != nil {
			logger.Error(err, "failed to list all the nodes")
			return nil, nil, err
		}

		defaultNodeLists := []corev1.Node{}
		for _, nodeObj := range nodeList.Items {
			if _, exist := nodesInPools[nodeObj.Name]; !exist {
				defaultNodeLists = append(defaultNodeLists, nodeObj)
			}
		}
		return defaultNpcl, defaultNodeLists, nil
	}
}
