package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

func (dr *DrainReconcile) handleNodeIdleNodeStateDrainingOrCompleted(ctx context.Context,
	node *corev1.Node,
	nodeNetworkState *sriovnetworkv1.SriovNetworkNodeState) (ctrl.Result, error) {
	reqLogger := ctx.Value(constants.LoggerContextKey).(logr.Logger).WithName("handleNodeIdleNodeStateDrainingOrCompleted")
	completed, err := dr.drainer.CompleteDrainNode(ctx, node)
	if err != nil {
		reqLogger.Error(err, "failed to complete drain on node")
		dr.recorder.Eventf(nodeNetworkState, nil,
			corev1.EventTypeWarning,
			"DrainController",
			"CompleteDrain",
			"failed to drain node")
		return ctrl.Result{}, err
	}

	// if we didn't manage to complete the un drain of the node we retry
	if !completed {
		reqLogger.Info("complete drain was not completed re queueing the request")
		dr.recorder.Eventf(nodeNetworkState, nil,
			corev1.EventTypeWarning,
			"DrainController",
			"CompleteDrain",
			"node complete drain was not completed")
		// TODO: make this time configurable
		return reconcile.Result{RequeueAfter: constants.DrainControllerRequeueTime}, nil
	}

	// clear drain-action and move current-state back to idle in a single patch
	err = utils.AnnotateObjectMultiple(ctx, nodeNetworkState, map[string]string{
		constants.NodeStateDrainActionAnnotation:  "",
		constants.NodeStateDrainAnnotationCurrent: constants.DrainIdle,
	}, dr.Client)
	if err != nil {
		reqLogger.Error(err, "failed to clear drain-action and set current-state to idle")
		return ctrl.Result{}, err
	}

	reqLogger.Info("completed the un drain for node")
	dr.recorder.Eventf(nodeNetworkState, nil,
		corev1.EventTypeWarning,
		"DrainController",
		"CompleteDrain",
		"node un drain completed")
	return ctrl.Result{}, nil
}

func (dr *DrainReconcile) handleNodeDrainOrReboot(ctx context.Context,
	node *corev1.Node,
	nodeNetworkState *sriovnetworkv1.SriovNetworkNodeState) (ctrl.Result, error) {
	reqLogger := ctx.Value(constants.LoggerContextKey).(logr.Logger).WithName("handleNodeDrainOrReboot")

	// get the relevant annotations
	desiredDrainState := nodeNetworkState.GetAnnotations()[constants.NodeStateDrainAnnotation]
	nodeStateDrainAnnotationCurrent := nodeNetworkState.GetAnnotations()[constants.NodeStateDrainAnnotationCurrent]
	drainAction := nodeNetworkState.GetAnnotations()[constants.NodeStateDrainActionAnnotation]

	// if the node state is on drain complete we need to check if the drain action satisfies the desired drain type
	if nodeStateDrainAnnotationCurrent == constants.DrainComplete {
		escalated, err := dr.handleDrainEscalation(ctx, nodeNetworkState, drainAction, desiredDrainState)
		if err != nil {
			return ctrl.Result{}, err
		}
		if escalated {
			return reconcile.Result{Requeue: true}, nil
		}
		reqLogger.Info("node requested a drain and nodeState is on drain completed nothing todo")
		return ctrl.Result{}, nil
	}

	// we need to start the drain, but first we need to check that we can drain the node
	if nodeStateDrainAnnotationCurrent == constants.DrainIdle {
		result, err := dr.tryDrainNode(ctx, node, desiredDrainState)
		if err != nil {
			reqLogger.Error(err, "failed to check if we can drain the node")
			return ctrl.Result{}, err
		}

		// in case we need to wait because we just to the max number of draining nodes
		if result != nil {
			return *result, nil
		}
	}

	// Check if we are on a single node, and we require a reboot/full-drain we just return
	fullNodeDrain := desiredDrainState == constants.RebootRequired
	singleNode := false
	if fullNodeDrain {
		nodeList := &corev1.NodeList{}
		err := dr.Client.List(ctx, nodeList)
		if err != nil {
			reqLogger.Error(err, "failed to list nodes")
			return ctrl.Result{}, err
		}
		if len(nodeList.Items) == 1 {
			reqLogger.Info("drainNode(): FullNodeDrain requested and we are on Single node")
			singleNode = true
		}
	}

	// call the drain function that will also call drain to other platform providers like openshift
	drained, err := dr.drainer.DrainNode(ctx, node, fullNodeDrain, singleNode)
	if err != nil {
		reqLogger.Error(err, "error trying to drain the node")
		dr.recorder.Eventf(nodeNetworkState, nil,
			corev1.EventTypeWarning,
			"DrainController",
			"DrainNode",
			"failed to drain node")
		return reconcile.Result{}, err
	}

	// if we didn't manage to complete the drain of the node we retry
	if !drained {
		reqLogger.Info("the nodes was not drained re queueing the request")
		dr.recorder.Eventf(nodeNetworkState, nil,
			corev1.EventTypeWarning,
			"DrainController",
			"DrainNode",
			"node drain operation was not completed")
		return reconcile.Result{RequeueAfter: constants.DrainControllerRequeueTime}, nil
	}

	// After drain succeeds, re-read the NodeState to get the latest version.
	// The drain process can take minutes, so the original nodeNetworkState is likely stale.
	updatedNodeState := &sriovnetworkv1.SriovNetworkNodeState{}
	if err := dr.Client.Get(ctx, client.ObjectKeyFromObject(nodeNetworkState), updatedNodeState); err != nil {
		reqLogger.Error(err, "failed to re-read nodeState after drain")
		return ctrl.Result{}, err
	}
	currentDesiredState := updatedNodeState.GetAnnotations()[constants.NodeStateDrainAnnotation]
	escalated, err := dr.handleDrainEscalation(ctx, updatedNodeState, desiredDrainState, currentDesiredState)
	if err != nil {
		return ctrl.Result{}, err
	}
	if escalated {
		return reconcile.Result{Requeue: true}, nil
	}

	// if we manage to drain we set drain completed and ensure drain-action reflects what was performed
	err = utils.AnnotateObjectMultiple(ctx, updatedNodeState, map[string]string{
		constants.NodeStateDrainAnnotationCurrent: constants.DrainComplete,
		constants.NodeStateDrainActionAnnotation:  desiredDrainState,
	}, dr.Client)
	if err != nil {
		reqLogger.Error(err, "failed to set DrainComplete and drain-action annotations")
		return ctrl.Result{}, err
	}

	reqLogger.Info("node drained successfully")
	dr.recorder.Eventf(updatedNodeState, nil,
		corev1.EventTypeWarning,
		"DrainController",
		"DrainNode",
		"node drain completed")
	return ctrl.Result{}, nil
}

// handleDrainEscalation checks if the current desired drain state requires a stronger drain than what was performed.
// Returns true if escalation was detected and re-drain is needed, false if no escalation.
func (dr *DrainReconcile) handleDrainEscalation(ctx context.Context,
	nodeNetworkState *sriovnetworkv1.SriovNetworkNodeState,
	performedAction, currentDesiredState string) (bool, error) {
	reqLogger := ctx.Value(constants.LoggerContextKey).(logr.Logger).WithName("handleDrainEscalation")

	if sriovnetworkv1.DrainActionSatisfiesDesired(performedAction, currentDesiredState) {
		return false, nil
	}

	reqLogger.Info("drain escalation detected, re-draining",
		"performedAction", performedAction, "currentDesired", currentDesiredState)
	if err := utils.AnnotateObjectMultiple(ctx, nodeNetworkState, map[string]string{
		constants.NodeStateDrainActionAnnotation:  currentDesiredState,
		constants.NodeStateDrainAnnotationCurrent: constants.Draining,
	}, dr.Client); err != nil {
		return false, err
	}
	dr.recorder.Eventf(nodeNetworkState, nil,
		corev1.EventTypeWarning,
		"DrainController",
		"DrainEscalated",
		fmt.Sprintf("drain escalated from %s to %s, re-draining", performedAction, currentDesiredState))
	return true, nil
}

func (dr *DrainReconcile) tryDrainNode(ctx context.Context, node *corev1.Node, desiredDrainState string) (*reconcile.Result, error) {
	reqLogger := ctx.Value(constants.LoggerContextKey).(logr.Logger).WithName("tryDrainNode")

	//critical section we need to check if we can start the draining
	dr.drainCheckMutex.Lock()
	defer dr.drainCheckMutex.Unlock()

	// find the relevant node pool
	nodePool, nodeList, err := dr.findNodePoolConfig(ctx, node)
	if err != nil {
		reqLogger.Error(err, "failed to find the pool for the requested node")
		return nil, err
	}

	// check how many nodes we can drain in parallel for the specific pool
	maxUnv, err := nodePool.MaxUnavailable(len(nodeList))
	if err != nil {
		reqLogger.Error(err, "failed to calculate max unavailable")
		return nil, err
	}

	current := 0
	snns := &sriovnetworkv1.SriovNetworkNodeState{}

	var currentSnns *sriovnetworkv1.SriovNetworkNodeState
	for _, nodeObj := range nodeList {
		err = dr.Get(ctx, client.ObjectKey{Name: nodeObj.GetName(), Namespace: vars.Namespace}, snns)
		if err != nil {
			if errors.IsNotFound(err) {
				reqLogger.V(2).Info("node doesn't have a sriovNetworkNodeState, skipping")
				continue
			}
			return nil, err
		}

		if snns.GetName() == node.GetName() {
			currentSnns = snns.DeepCopy()
		}

		if utils.ObjectHasAnnotation(snns, constants.NodeStateDrainAnnotationCurrent, constants.Draining) ||
			utils.ObjectHasAnnotation(snns, constants.NodeStateDrainAnnotationCurrent, constants.DrainComplete) {
			current++
		}
	}
	reqLogger.Info("Max node allowed to be draining at the same time", "MaxParallelNodeConfiguration", maxUnv)
	reqLogger.Info("Count of draining", "drainingNodes", current)

	// if maxUnv is zero this means we drain all the nodes in parallel without a limit
	if maxUnv == -1 {
		reqLogger.Info("draining all the nodes in parallel")
	} else if current >= maxUnv {
		// the node requested to be drained, but we are at the limit so we re-enqueue the request
		reqLogger.Info("MaxParallelNodeConfiguration limit reached for draining nodes re-enqueue the request")
		// TODO: make this time configurable
		return &reconcile.Result{RequeueAfter: constants.DrainControllerRequeueTime}, nil
	}

	if currentSnns == nil {
		return nil, fmt.Errorf("failed to find sriov network node state for requested node")
	}

	// Set current-state=Draining and drain-action atomically in the same critical section
	err = utils.AnnotateObjectMultiple(ctx, currentSnns, map[string]string{
		constants.NodeStateDrainAnnotationCurrent: constants.Draining,
		constants.NodeStateDrainActionAnnotation:  desiredDrainState,
	}, dr.Client)
	if err != nil {
		reqLogger.Error(err, "failed to set draining and drain-action annotations")
		return nil, err
	}

	return nil, nil
}

func (dr *DrainReconcile) findNodePoolConfig(ctx context.Context, node *corev1.Node) (*sriovnetworkv1.SriovNetworkPoolConfig, []corev1.Node, error) {
	logger := ctx.Value(constants.LoggerContextKey).(logr.Logger).WithName("findNodePoolConfig")
	// get all the sriov network pool configs
	npcl := &sriovnetworkv1.SriovNetworkPoolConfigList{}
	err := dr.List(ctx, npcl)
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
		err = dr.List(ctx, nodeList, &client.ListOptions{LabelSelector: selector})
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
		err = dr.List(ctx, nodeList, &client.ListOptions{LabelSelector: selector})
		if err != nil {
			logger.Error(err, "failed to list nodes using with label selector", "labelSelector", selector)
			return nil, nil, err
		}

		return selectedNpcl[0], nodeList.Items, nil
	} else {
		// in this case we get all the nodes and remove the ones that already part of any pool
		logger.V(1).Info("node doesn't belong to any pool, using default drain configuration with MaxUnavailable of one", "pool", *defaultPoolConfig)
		nodeList := &corev1.NodeList{}
		err = dr.List(ctx, nodeList)
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
		return defaultPoolConfig, defaultNodeLists, nil
	}
}
