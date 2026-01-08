/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

// ============================================================================
// Shared Types and Helper Functions
// ============================================================================

// enqueueFunc is a function type for enqueueing reconcile requests
type enqueueFunc func(ctx context.Context, w workqueue.TypedRateLimitingInterface[reconcile.Request])

// nodeConditionCounts holds aggregated condition counts from nodes
type nodeConditionCounts struct {
	readyCount       int
	progressingCount int
	degradedCount    int
}

// aggregatedStatus holds the common status fields for policy and pool config
type aggregatedStatus struct {
	matchedNodeCount int
	readyNodeCount   int
	conditions       []metav1.Condition
}

// aggregateNodeConditions aggregates conditions from NodeStates for the given nodes
func aggregateNodeConditions(ctx context.Context, c client.Client, matchedNodes []corev1.Node) (nodeConditionCounts, error) {
	logger := log.FromContext(ctx)
	counts := nodeConditionCounts{}

	for _, node := range matchedNodes {
		nodeState := &sriovnetworkv1.SriovNetworkNodeState{}
		err := c.Get(ctx, types.NamespacedName{Namespace: vars.Namespace, Name: node.Name}, nodeState)
		if err != nil {
			if errors.IsNotFound(err) {
				// NodeState doesn't exist yet, this node is not ready
				continue
			}
			logger.Error(err, "Failed to get SriovNetworkNodeState", "node", node.Name)
			return counts, err
		}

		// Check conditions on the NodeState
		if isConditionTrue(nodeState.Status.Conditions, sriovnetworkv1.ConditionReady) {
			counts.readyCount++
		}
		if isConditionTrue(nodeState.Status.Conditions, sriovnetworkv1.ConditionProgressing) {
			counts.progressingCount++
		}
		if isConditionTrue(nodeState.Status.Conditions, sriovnetworkv1.ConditionDegraded) {
			counts.degradedCount++
		}
	}

	return counts, nil
}

// computeAggregatedStatus computes the aggregated status from matched nodes
func computeAggregatedStatus(ctx context.Context, c client.Client, matchedNodes []corev1.Node, generation int64) (aggregatedStatus, error) {
	matchedNodeCount := len(matchedNodes)
	counts, err := aggregateNodeConditions(ctx, c, matchedNodes)
	if err != nil {
		return aggregatedStatus{}, err
	}

	conditions := buildStatusConditions(generation, matchedNodeCount, counts.readyCount, counts.progressingCount, counts.degradedCount)

	return aggregatedStatus{
		matchedNodeCount: matchedNodeCount,
		readyNodeCount:   counts.readyCount,
		conditions:       conditions,
	}, nil
}

// buildStatusConditions creates the conditions based on aggregated node state
func buildStatusConditions(generation int64, matchedNodeCount, readyNodeCount, progressingCount, degradedCount int) []metav1.Condition {
	conditions := []metav1.Condition{}

	// Handle case where no nodes match
	if matchedNodeCount == 0 {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:               sriovnetworkv1.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             sriovnetworkv1.ReasonNoMatchingNodes,
			Message:            "No nodes match the nodeSelector",
			ObservedGeneration: generation,
		})
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:               sriovnetworkv1.ConditionProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             sriovnetworkv1.ReasonNoMatchingNodes,
			Message:            "No nodes match the nodeSelector",
			ObservedGeneration: generation,
		})
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:               sriovnetworkv1.ConditionDegraded,
			Status:             metav1.ConditionFalse,
			Reason:             sriovnetworkv1.ReasonNoMatchingNodes,
			Message:            "No nodes match the nodeSelector",
			ObservedGeneration: generation,
		})
		return conditions
	}

	// Ready condition: True only if ALL matched nodes are ready
	if readyNodeCount == matchedNodeCount {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:               sriovnetworkv1.ConditionReady,
			Status:             metav1.ConditionTrue,
			Reason:             sriovnetworkv1.ReasonPolicyReady,
			Message:            fmt.Sprintf("All %d matched nodes are ready", matchedNodeCount),
			ObservedGeneration: generation,
		})
	} else {
		message := fmt.Sprintf("%d of %d matched nodes are ready", readyNodeCount, matchedNodeCount)
		reason := sriovnetworkv1.ReasonPolicyNotReady
		if readyNodeCount > 0 {
			reason = sriovnetworkv1.ReasonPartiallyApplied
		}
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:               sriovnetworkv1.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: generation,
		})
	}

	// Progressing condition: True if ANY node is progressing
	if progressingCount > 0 {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:               sriovnetworkv1.ConditionProgressing,
			Status:             metav1.ConditionTrue,
			Reason:             sriovnetworkv1.ReasonSomeNodesProgressing,
			Message:            fmt.Sprintf("%d of %d matched nodes are progressing", progressingCount, matchedNodeCount),
			ObservedGeneration: generation,
		})
	} else {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:               sriovnetworkv1.ConditionProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             sriovnetworkv1.ReasonNotProgressing,
			Message:            "No nodes are currently progressing",
			ObservedGeneration: generation,
		})
	}

	// Degraded condition: True if ANY node is degraded
	if degradedCount > 0 {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:               sriovnetworkv1.ConditionDegraded,
			Status:             metav1.ConditionTrue,
			Reason:             sriovnetworkv1.ReasonSomeNodesFailed,
			Message:            fmt.Sprintf("%d of %d matched nodes are degraded", degradedCount, matchedNodeCount),
			ObservedGeneration: generation,
		})
	} else {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:               sriovnetworkv1.ConditionDegraded,
			Status:             metav1.ConditionFalse,
			Reason:             sriovnetworkv1.ReasonNotDegraded,
			Message:            "No nodes are degraded",
			ObservedGeneration: generation,
		})
	}

	return conditions
}

// isConditionTrue checks if a condition with the given type exists and has status True
func isConditionTrue(conditions []metav1.Condition, conditionType string) bool {
	for _, c := range conditions {
		if c.Type == conditionType && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// createNodeStateEventHandler creates an event handler for SriovNetworkNodeState changes
func createNodeStateEventHandler(enqueue enqueueFunc) handler.Funcs {
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, w)
		},
		UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			oldState := e.ObjectOld.(*sriovnetworkv1.SriovNetworkNodeState)
			newState := e.ObjectNew.(*sriovnetworkv1.SriovNetworkNodeState)
			// Only enqueue if conditions changed
			if !equality.Semantic.DeepEqual(oldState.Status.Conditions, newState.Status.Conditions) {
				enqueue(ctx, w)
			}
		},
		DeleteFunc: func(ctx context.Context, e event.TypedDeleteEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, w)
		},
	}
}

// createNodeEventHandler creates an event handler for Node changes
func createNodeEventHandler(enqueue enqueueFunc) handler.Funcs {
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, w)
		},
		UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			oldNode := e.ObjectOld.(*corev1.Node)
			newNode := e.ObjectNew.(*corev1.Node)
			// Only enqueue if labels changed (which might affect selection)
			if !equality.Semantic.DeepEqual(oldNode.Labels, newNode.Labels) {
				enqueue(ctx, w)
			}
		},
		DeleteFunc: func(ctx context.Context, e event.TypedDeleteEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, w)
		},
	}
}

// listAllNodes returns all nodes in the cluster
func listAllNodes(ctx context.Context, c client.Client) (*corev1.NodeList, error) {
	nodeList := &corev1.NodeList{}
	if err := c.List(ctx, nodeList); err != nil {
		return nil, err
	}
	return nodeList, nil
}

// nodeMatchFunc is a function that determines if a node matches a selector
type nodeMatchFunc func(node *corev1.Node) bool

// findMatchingNodes returns nodes that match the given predicate function
func findMatchingNodes(nodes []corev1.Node, matchFunc nodeMatchFunc) []corev1.Node {
	matchedNodes := []corev1.Node{}
	for i := range nodes {
		if matchFunc(&nodes[i]) {
			matchedNodes = append(matchedNodes, nodes[i])
		}
	}
	return matchedNodes
}

// labelSelectorMatchFunc creates a node match function from a label selector
func labelSelectorMatchFunc(nodeSelector *metav1.LabelSelector) (nodeMatchFunc, error) {
	var selector labels.Selector
	if nodeSelector == nil {
		selector = labels.Everything()
	} else {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(nodeSelector)
		if err != nil {
			return nil, err
		}
	}
	return func(node *corev1.Node) bool {
		return selector.Matches(labels.Set(node.Labels))
	}, nil
}

// reconcileAggregatedStatus lists nodes, finds matches, and computes aggregated status
func reconcileAggregatedStatus(ctx context.Context, c client.Client, matchFunc nodeMatchFunc, generation int64) (aggregatedStatus, error) {
	nodeList, err := listAllNodes(ctx, c)
	if err != nil {
		return aggregatedStatus{}, err
	}

	matchedNodes := findMatchingNodes(nodeList.Items, matchFunc)
	return computeAggregatedStatus(ctx, c, matchedNodes, generation)
}

// reconcileAggregatedStatusWithSelector is like reconcileAggregatedStatus but takes a label selector
func reconcileAggregatedStatusWithSelector(ctx context.Context, c client.Client, nodeSelector *metav1.LabelSelector, generation int64) (aggregatedStatus, error) {
	matchFunc, err := labelSelectorMatchFunc(nodeSelector)
	if err != nil {
		return aggregatedStatus{}, err
	}
	return reconcileAggregatedStatus(ctx, c, matchFunc, generation)
}

// ============================================================================
// SriovNetworkNodePolicy Status Reconciler
// ============================================================================

// SriovNetworkNodePolicyStatusReconciler reconciles the status of SriovNetworkNodePolicy objects
// by aggregating conditions from matching SriovNetworkNodeState objects
type SriovNetworkNodePolicyStatusReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworknodepolicies,verbs=get;list;watch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworknodepolicies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworknodestates,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// Reconcile updates the status of SriovNetworkNodePolicy objects based on the aggregated
// conditions from matching SriovNetworkNodeState objects
func (r *SriovNetworkNodePolicyStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLogger := log.FromContext(ctx).WithValues("sriovnetworknodepolicyStatus", req.NamespacedName)
	reqLogger.Info("Reconciling SriovNetworkNodePolicyStatus")

	// Fetch the SriovNetworkNodePolicy
	policy := &sriovnetworkv1.SriovNetworkNodePolicy{}
	err := r.Get(ctx, req.NamespacedName, policy)
	if err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		reqLogger.Error(err, "Failed to get SriovNetworkNodePolicy")
		return reconcile.Result{}, err
	}

	// Compute aggregated status from matching nodes
	aggStatus, err := reconcileAggregatedStatus(ctx, r.Client, policy.Selected, policy.Generation)
	if err != nil {
		reqLogger.Error(err, "Failed to compute aggregated status")
		return reconcile.Result{}, err
	}

	// Build new status
	newStatus := sriovnetworkv1.SriovNetworkNodePolicyStatus{
		MatchedNodeCount: aggStatus.matchedNodeCount,
		ReadyNodeCount:   aggStatus.readyNodeCount,
		Conditions:       aggStatus.conditions,
	}

	// Update status only if it changed
	if policy.Status.MatchedNodeCount != newStatus.MatchedNodeCount ||
		policy.Status.ReadyNodeCount != newStatus.ReadyNodeCount ||
		!sriovnetworkv1.ConditionsEqual(policy.Status.Conditions, newStatus.Conditions) {
		policy.Status = newStatus
		if err := r.Status().Update(ctx, policy); err != nil {
			reqLogger.Error(err, "Failed to update SriovNetworkNodePolicy status")
			return reconcile.Result{}, err
		}
		reqLogger.Info("Updated policy status",
			"matchedNodeCount", aggStatus.matchedNodeCount,
			"readyNodeCount", aggStatus.readyNodeCount)
	}

	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SriovNetworkNodePolicyStatusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	nodeStateHandler := createNodeStateEventHandler(r.enqueueAllPolicies)
	nodeHandler := createNodeEventHandler(r.enqueueAllPolicies)

	return ctrl.NewControllerManagedBy(mgr).
		Named("sriovnetworknodepolicystatus").
		For(&sriovnetworkv1.SriovNetworkNodePolicy{}).
		Watches(&sriovnetworkv1.SriovNetworkNodeState{}, &nodeStateHandler).
		Watches(&corev1.Node{}, &nodeHandler).
		Complete(r)
}

// enqueueAllPolicies enqueues all policies for reconciliation
func (r *SriovNetworkNodePolicyStatusReconciler) enqueueAllPolicies(ctx context.Context, w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	logger := log.FromContext(ctx).WithName("enqueueAllPolicies")

	policyList := &sriovnetworkv1.SriovNetworkNodePolicyList{}
	if err := r.List(ctx, policyList, &client.ListOptions{Namespace: vars.Namespace}); err != nil {
		logger.Error(err, "Failed to list SriovNetworkNodePolicies")
		return
	}

	for _, policy := range policyList.Items {
		w.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: policy.Namespace,
				Name:      policy.Name,
			},
		})
	}
}

// ============================================================================
// SriovNetworkPoolConfig Status Reconciler
// ============================================================================

// SriovNetworkPoolConfigStatusReconciler reconciles the status of SriovNetworkPoolConfig objects
// by aggregating conditions from matching SriovNetworkNodeState objects
type SriovNetworkPoolConfigStatusReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworkpoolconfigs,verbs=get;list;watch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworkpoolconfigs/status,verbs=get;update;patch

// Reconcile updates the status of SriovNetworkPoolConfig objects based on the aggregated
// conditions from matching SriovNetworkNodeState objects
func (r *SriovNetworkPoolConfigStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLogger := log.FromContext(ctx).WithValues("sriovnetworkpoolconfigStatus", req.NamespacedName)
	reqLogger.Info("Reconciling SriovNetworkPoolConfigStatus")

	// Fetch the SriovNetworkPoolConfig
	poolConfig := &sriovnetworkv1.SriovNetworkPoolConfig{}
	err := r.Get(ctx, req.NamespacedName, poolConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		reqLogger.Error(err, "Failed to get SriovNetworkPoolConfig")
		return reconcile.Result{}, err
	}

	// Skip pool configs that are used for OVS hardware offload (they have a different purpose)
	if poolConfig.Spec.OvsHardwareOffloadConfig.Name != "" {
		reqLogger.V(2).Info("Skipping OVS hardware offload pool config")
		return reconcile.Result{}, nil
	}

	// Compute aggregated status from matching nodes
	aggStatus, err := reconcileAggregatedStatusWithSelector(ctx, r.Client, poolConfig.Spec.NodeSelector, poolConfig.Generation)
	if err != nil {
		reqLogger.Error(err, "Failed to compute aggregated status")
		return reconcile.Result{}, err
	}

	// Build new status
	newStatus := sriovnetworkv1.SriovNetworkPoolConfigStatus{
		MatchedNodeCount: aggStatus.matchedNodeCount,
		ReadyNodeCount:   aggStatus.readyNodeCount,
		Conditions:       aggStatus.conditions,
	}

	// Update status only if it changed
	if poolConfig.Status.MatchedNodeCount != newStatus.MatchedNodeCount ||
		poolConfig.Status.ReadyNodeCount != newStatus.ReadyNodeCount ||
		!sriovnetworkv1.ConditionsEqual(poolConfig.Status.Conditions, newStatus.Conditions) {
		poolConfig.Status = newStatus
		if err := r.Status().Update(ctx, poolConfig); err != nil {
			reqLogger.Error(err, "Failed to update SriovNetworkPoolConfig status")
			return reconcile.Result{}, err
		}
		reqLogger.Info("Updated pool config status",
			"matchedNodeCount", aggStatus.matchedNodeCount,
			"readyNodeCount", aggStatus.readyNodeCount)
	}

	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SriovNetworkPoolConfigStatusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	nodeStateHandler := createNodeStateEventHandler(r.enqueueAllPoolConfigs)
	nodeHandler := createNodeEventHandler(r.enqueueAllPoolConfigs)

	return ctrl.NewControllerManagedBy(mgr).
		Named("sriovnetworkpoolconfigstatus").
		For(&sriovnetworkv1.SriovNetworkPoolConfig{}).
		Watches(&sriovnetworkv1.SriovNetworkNodeState{}, &nodeStateHandler).
		Watches(&corev1.Node{}, &nodeHandler).
		Complete(r)
}

// enqueueAllPoolConfigs enqueues all pool configs for reconciliation
func (r *SriovNetworkPoolConfigStatusReconciler) enqueueAllPoolConfigs(ctx context.Context, w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	logger := log.FromContext(ctx).WithName("enqueueAllPoolConfigs")

	poolConfigList := &sriovnetworkv1.SriovNetworkPoolConfigList{}
	if err := r.List(ctx, poolConfigList, &client.ListOptions{Namespace: vars.Namespace}); err != nil {
		logger.Error(err, "Failed to list SriovNetworkPoolConfigs")
		return
	}

	for _, poolConfig := range poolConfigList.Items {
		// Skip OVS hardware offload configs
		if poolConfig.Spec.OvsHardwareOffloadConfig.Name != "" {
			continue
		}
		w.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: poolConfig.Namespace,
				Name:      poolConfig.Name,
			},
		})
	}
}
