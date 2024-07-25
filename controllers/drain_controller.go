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
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/drain"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/platforms"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

var (
	oneNode     = intstr.FromInt32(1)
	defaultNpcl = &sriovnetworkv1.SriovNetworkPoolConfig{Spec: sriovnetworkv1.SriovNetworkPoolConfigSpec{
		MaxUnavailable: &oneNode,
		NodeSelector:   &metav1.LabelSelector{}}}
)

type DrainReconcile struct {
	client.Client
	Scheme   *runtime.Scheme
	recorder record.EventRecorder
	drainer  drain.DrainInterface

	drainCheckMutex sync.Mutex
}

func NewDrainReconcileController(client client.Client, Scheme *runtime.Scheme, recorder record.EventRecorder, platformHelper platforms.Interface) (*DrainReconcile, error) {
	drainer, err := drain.NewDrainer(platformHelper)
	if err != nil {
		return nil, err
	}

	return &DrainReconcile{
		client,
		Scheme,
		recorder,
		drainer,
		sync.Mutex{}}, nil
}

//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnodestates,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (dr *DrainReconcile) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLogger := log.FromContext(ctx)
	reqLogger.Info("Reconciling Drain")

	req.Namespace = vars.Namespace

	// get node object
	node := &corev1.Node{}
	found, err := dr.getObject(ctx, req, node)
	if err != nil {
		reqLogger.Error(err, "failed to get node object")
		return ctrl.Result{}, err
	}
	if !found {
		reqLogger.Info("node not found don't, requeue the request")
		return ctrl.Result{}, nil
	}

	// get sriovNodeNodeState object
	nodeNetworkState := &sriovnetworkv1.SriovNetworkNodeState{}
	found, err = dr.getObject(ctx, req, nodeNetworkState)
	if err != nil {
		reqLogger.Error(err, "failed to get sriovNetworkNodeState object")
		return ctrl.Result{}, err
	}
	if !found {
		reqLogger.Info("sriovNetworkNodeState not found, don't requeue the request")
		return ctrl.Result{}, nil
	}

	// create the drain state annotation if it doesn't exist in the sriovNetworkNodeState object
	nodeStateDrainAnnotationCurrent, err := dr.ensureAnnotationExists(ctx, nodeNetworkState, constants.NodeStateDrainAnnotationCurrent)
	if err != nil {
		reqLogger.Error(err, "failed to ensure nodeStateDrainAnnotation")
		return ctrl.Result{}, err
	}

	// create the drain state annotation if it doesn't exist in the node object
	nodeDrainAnnotation, err := dr.ensureAnnotationExists(ctx, node, constants.NodeDrainAnnotation)
	if err != nil {
		reqLogger.Error(err, "failed to ensure nodeStateDrainAnnotation")
		return ctrl.Result{}, err
	}

	reqLogger.V(2).Info("Drain annotations", "nodeAnnotation", nodeDrainAnnotation, "nodeStateAnnotation", nodeStateDrainAnnotationCurrent)

	// Check the node request
	if nodeDrainAnnotation == constants.DrainIdle {
		// this cover the case the node is on idle

		// node request to be on idle and the currect state is idle
		// we don't do anything
		if nodeStateDrainAnnotationCurrent == constants.DrainIdle {
			// in case we have policy there is nothing else to do
			if len(nodeNetworkState.Spec.Interfaces) > 0 {
				reqLogger.Info("node and nodeState are on idle nothing todo")
			} else {
				// if we don't have any policy
				// let's be sure the device plugin label doesn't exist on the node
				reqLogger.Info("remove Device plugin from node nodeState spec is empty")
				err = utils.LabelNode(ctx, node.Name, constants.SriovDevicePluginEnabledLabel, constants.SriovDevicePluginEnabledLabelDisabled, dr.Client)
				if err != nil {
					log.Log.Error(err, "failed to label node for device plugin label",
						"labelKey",
						constants.SriovDevicePluginEnabledLabel,
						"labelValue",
						constants.SriovDevicePluginEnabledLabelDisabled)
					return reconcile.Result{}, err
				}
			}
			return reconcile.Result{}, nil
		}

		// we have two options here:
		// 1. node request idle and the current status is drain complete
		// this means the daemon finish is work, so we need to clean the drain
		//
		// 2. the operator is still draining the node but maybe the sriov policy changed and the daemon
		//  doesn't need to drain anymore, so we can stop the drain
		if nodeStateDrainAnnotationCurrent == constants.DrainComplete ||
			nodeStateDrainAnnotationCurrent == constants.Draining {
			completed, err := dr.drainer.CompleteDrainNode(ctx, node)
			if err != nil {
				reqLogger.Error(err, "failed to complete drain on node")
				dr.recorder.Event(nodeNetworkState,
					corev1.EventTypeWarning,
					"DrainController",
					"failed to drain node")
				return ctrl.Result{}, err
			}

			// if we didn't manage to complete the un drain of the node we retry
			if !completed {
				reqLogger.Info("complete drain was not completed re queueing the request")
				dr.recorder.Event(nodeNetworkState,
					corev1.EventTypeWarning,
					"DrainController",
					"node complete drain was not completed")
				// TODO: make this time configurable
				return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
			}

			// check the device plugin exited and enable it again
			// only of we have something in the node state spec
			if len(nodeNetworkState.Spec.Interfaces) > 0 {
				completed, err = dr.enableSriovDevicePlugin(ctx, node)
				if err != nil {
					reqLogger.Error(err, "failed to enable SriovDevicePlugin")
					dr.recorder.Event(nodeNetworkState,
						corev1.EventTypeWarning,
						"DrainController",
						"failed to enable SriovDevicePlugin")
					return ctrl.Result{}, err
				}

				if !completed {
					reqLogger.Info("sriov device plugin enable was not completed")
					dr.recorder.Event(nodeNetworkState,
						corev1.EventTypeWarning,
						"DrainController",
						"sriov device plugin enable was not completed")
					return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
				}
			}

			// move the node state back to idle
			err = utils.AnnotateObject(ctx, nodeNetworkState, constants.NodeStateDrainAnnotationCurrent, constants.DrainIdle, dr.Client)
			if err != nil {
				reqLogger.Error(err, "failed to annotate node with annotation", "annotation", constants.DrainIdle)
				return ctrl.Result{}, err
			}

			reqLogger.Info("completed the un drain for node")
			dr.recorder.Event(nodeNetworkState,
				corev1.EventTypeWarning,
				"DrainController",
				"node un drain completed")
			return ctrl.Result{}, nil
		}
	} else if nodeDrainAnnotation == constants.DrainRequired || nodeDrainAnnotation == constants.RebootRequired {
		// this cover the case a node request to drain or reboot

		// nothing to do here we need to wait for the node to move back to idle
		if nodeStateDrainAnnotationCurrent == constants.DrainComplete {
			reqLogger.Info("node requested a drain and nodeState is on drain completed nothing todo")
			return ctrl.Result{}, nil
		}

		// we need to start the drain, but first we need to check that we can drain the node
		if nodeStateDrainAnnotationCurrent == constants.DrainIdle {
			result, err := dr.tryDrainNode(ctx, node)
			if err != nil {
				reqLogger.Error(err, "failed to check if we can drain the node")
				return ctrl.Result{}, err
			}

			// in case we need to wait because we just to the max number of draining nodes
			if result != nil {
				return *result, nil
			}
		}

		// call the drain function that will also call drain to other platform providers like openshift
		drained, err := dr.drainer.DrainNode(ctx, node, nodeDrainAnnotation == constants.RebootRequired)
		if err != nil {
			reqLogger.Error(err, "error trying to drain the node")
			dr.recorder.Event(nodeNetworkState,
				corev1.EventTypeWarning,
				"DrainController",
				"failed to drain node")
			return reconcile.Result{}, err
		}

		// if we didn't manage to complete the drain of the node we retry
		if !drained {
			reqLogger.Info("the nodes was not drained re queueing the request")
			dr.recorder.Event(nodeNetworkState,
				corev1.EventTypeWarning,
				"DrainController",
				"node drain operation was not completed")
			return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
		}

		reqLogger.Info("remove Device plugin from node")
		err = utils.LabelNode(ctx, node.Name, constants.SriovDevicePluginEnabledLabel, constants.SriovDevicePluginEnabledLabelDisabled, dr.Client)
		if err != nil {
			log.Log.Error(err, "failed to label node for device plugin label",
				"labelKey",
				constants.SriovDevicePluginEnabledLabel,
				"labelValue",
				constants.SriovDevicePluginEnabledLabelDisabled)
			return reconcile.Result{}, err
		}

		// if we manage to drain we label the node state with drain completed and finish
		err = utils.AnnotateObject(ctx, nodeNetworkState, constants.NodeStateDrainAnnotationCurrent, constants.DrainComplete, dr.Client)
		if err != nil {
			reqLogger.Error(err, "failed to annotate node with annotation", "annotation", constants.DrainComplete)
			return ctrl.Result{}, err
		}

		reqLogger.Info("node drained successfully")
		dr.recorder.Event(nodeNetworkState,
			corev1.EventTypeWarning,
			"DrainController",
			"node drain completed")
		return ctrl.Result{}, nil
	} else if nodeDrainAnnotation == constants.DevicePluginResetRequired {
		// nothing to do here we need to wait for the node to move back to idle
		if nodeStateDrainAnnotationCurrent == constants.DrainComplete {
			reqLogger.Info("node requested a drain and nodeState is on drain completed nothing todo")
			return ctrl.Result{}, nil
		}

		// if we are on idle state we move it to drain
		if nodeStateDrainAnnotationCurrent == constants.DrainIdle {
			err = utils.AnnotateObject(ctx, nodeNetworkState, constants.NodeStateDrainAnnotationCurrent, constants.Draining, dr.Client)
			if err != nil {
				reqLogger.Error(err, "failed to annotate node with annotation", "annotation", constants.Draining)
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}

		// This cover a case where we only need to reset the device plugin
		// for that we are going to cordon the node, so we don't get new pods allocated
		// to the node in the time we remove the device plugin
		err = dr.drainer.RunCordonOrUncordon(ctx, node, true)
		if err != nil {
			log.Log.Error(err, "failed to cordon on node")
			return reconcile.Result{}, err
		}

		// we switch the sriov label to disable and mark the drain as completed
		// no need to wait for the device plugin to exist here as we cordon the node,
		// and we want to config-daemon to start the configuration in parallel of the kube-controller to remove the pod
		// we check the device plugin was removed when the config-daemon moves is desire state to idle
		reqLogger.Info("disable Device plugin from node")
		err = utils.LabelNode(ctx, node.Name, constants.SriovDevicePluginEnabledLabel, constants.SriovDevicePluginEnabledLabelDisabled, dr.Client)
		if err != nil {
			log.Log.Error(err, "failed to label node for device plugin label",
				"labelKey",
				constants.SriovDevicePluginEnabledLabel,
				"labelValue",
				constants.SriovDevicePluginEnabledLabelDisabled)
			return reconcile.Result{}, err
		}

		// if we manage to cordon we label the node state with drain completed and finish
		err = utils.AnnotateObject(ctx, nodeNetworkState, constants.NodeStateDrainAnnotationCurrent, constants.DrainComplete, dr.Client)
		if err != nil {
			reqLogger.Error(err, "failed to annotate node with annotation", "annotation", constants.DrainComplete)
			return ctrl.Result{}, err
		}

		reqLogger.Info("node cordoned successfully and device plugin removed")
		dr.recorder.Event(nodeNetworkState,
			corev1.EventTypeWarning,
			"DrainController",
			"node cordoned and device plugin removed completed")
		return ctrl.Result{}, nil
	}

	reqLogger.Error(nil, "unexpected node drain annotation")
	return reconcile.Result{}, fmt.Errorf("unexpected node drain annotation")
}

func (dr *DrainReconcile) getObject(ctx context.Context, req ctrl.Request, object client.Object) (bool, error) {
	err := dr.Get(ctx, req.NamespacedName, object)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (dr *DrainReconcile) ensureAnnotationExists(ctx context.Context, object client.Object, key string) (string, error) {
	value, exist := object.GetAnnotations()[key]
	if !exist {
		err := utils.AnnotateObject(ctx, object, constants.NodeStateDrainAnnotationCurrent, constants.DrainIdle, dr.Client)
		if err != nil {
			return "", err
		}
		return constants.DrainIdle, nil
	}

	return value, nil
}

func (dr *DrainReconcile) tryDrainNode(ctx context.Context, node *corev1.Node) (*reconcile.Result, error) {
	// configure logs
	reqLogger := log.FromContext(ctx)
	reqLogger.Info("checkForNodeDrain():")

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
				reqLogger.V(2).Info("node doesn't have a sriovNetworkNodePolicy")
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
		return &reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if currentSnns == nil {
		return nil, fmt.Errorf("failed to find sriov network node state for requested node")
	}

	err = utils.AnnotateObject(ctx, currentSnns, constants.NodeStateDrainAnnotationCurrent, constants.Draining, dr.Client)
	if err != nil {
		reqLogger.Error(err, "failed to annotate node with annotation", "annotation", constants.Draining)
		return nil, err
	}

	return nil, nil
}

func (dr *DrainReconcile) findNodePoolConfig(ctx context.Context, node *corev1.Node) (*sriovnetworkv1.SriovNetworkPoolConfig, []corev1.Node, error) {
	logger := log.FromContext(ctx)
	logger.Info("findNodePoolConfig():")
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
		logger.V(1).Info("node doesn't belong to any pool, using default drain configuration with MaxUnavailable of one", "pool", *defaultNpcl)
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
		return defaultNpcl, defaultNodeLists, nil
	}
}

// enableSriovDevicePlugin change the device plugin label on the requested node to enable
// if there is a pod still running we will return false
func (dr *DrainReconcile) enableSriovDevicePlugin(ctx context.Context, node *corev1.Node) (bool, error) {
	logger := log.FromContext(ctx)
	logger.Info("enableSriovDevicePlugin():")

	// check if the device plugin is terminating only if the node annotation for device plugin is disabled
	if node.Annotations[constants.SriovDevicePluginEnabledLabel] == constants.SriovDevicePluginEnabledLabelDisabled {
		pods, err := dr.getDevicePluginPodsOnNode(node.Name)
		if err != nil {
			logger.Error(err, "failed to list device plugin pods running on node")
			return false, err
		}

		if len(pods.Items) != 0 {
			log.Log.V(2).Info("device plugin pod still terminating on node")
			return false, nil
		}
	}

	logger.Info("enable Device plugin from node")
	err := utils.LabelNode(ctx, node.Name, constants.SriovDevicePluginEnabledLabel, constants.SriovDevicePluginEnabledLabelEnabled, dr.Client)
	if err != nil {
		log.Log.Error(err, "failed to label node for device plugin label",
			"labelKey",
			constants.SriovDevicePluginEnabledLabel,
			"labelValue",
			constants.SriovDevicePluginEnabledLabelEnabled)
		return false, err
	}

	// check if the device plugin pod is running on the node
	pods, err := dr.getDevicePluginPodsOnNode(node.Name)
	if err != nil {
		logger.Error(err, "failed to list device plugin pods running on node")
		return false, err
	}

	if len(pods.Items) == 1 && pods.Items[0].Status.Phase == corev1.PodRunning {
		logger.Info("Device plugin pod running on node")
		return true, nil
	}

	logger.V(2).Info("Device plugin pod still not running on node")
	return false, nil
}

func (dr *DrainReconcile) getDevicePluginPodsOnNode(nodeName string) (*corev1.PodList, error) {
	pods := &corev1.PodList{}
	err := dr.List(context.Background(), pods, &client.ListOptions{
		Raw: &metav1.ListOptions{
			LabelSelector:   "app=sriov-device-plugin",
			FieldSelector:   "spec.nodeName=" + nodeName,
			ResourceVersion: "0"},
	})

	return pods, err
}

// SetupWithManager sets up the controller with the Manager.
func (dr *DrainReconcile) SetupWithManager(mgr ctrl.Manager) error {
	createUpdateEnqueue := handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: vars.Namespace,
				Name:      e.Object.GetName(),
			}})
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: vars.Namespace,
				Name:      e.ObjectNew.GetName(),
			}})
		},
	}

	// Watch for spec and annotation changes
	nodePredicates := builder.WithPredicates(DrainAnnotationPredicate{})
	nodeStatePredicates := builder.WithPredicates(DrainStateAnnotationPredicate{})

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: 50}).
		For(&corev1.Node{}, nodePredicates).
		Watches(&sriovnetworkv1.SriovNetworkNodeState{}, createUpdateEnqueue, nodeStatePredicates).
		Complete(dr)
}
