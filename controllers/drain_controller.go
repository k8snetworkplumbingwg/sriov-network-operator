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
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfginformers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubectl/pkg/drain"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

// TODO(e0ne): remove this constant once we'll support parallel multiple nodes configuration in a parallel
const (
	maxParallelNodeConfiguration = 1
)

// writer implements io.Writer interface as a pass-through for log.Log.
type writer struct {
	logFunc func(msg string, keysAndValues ...interface{})
}

// Write passes string(p) into writer's logFunc and always returns len(p)
func (w writer) Write(p []byte) (n int, err error) {
	w.logFunc(string(p))
	return len(p), nil
}

type DrainReconcile struct {
	client.Client
	Scheme                *runtime.Scheme
	kubeClient            kubernetes.Interface
	openshiftContext      *utils.OpenshiftContext
	mcpPauseDrainSelector labels.Selector
	drainingSelector      labels.Selector
}

func NewDrainReconcileController(client client.Client, Scheme *runtime.Scheme, kubeClient kubernetes.Interface, openshiftContext *utils.OpenshiftContext) (*DrainReconcile, error) {
	mcpPauseDrainSelector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchLabels: map[string]string{constants.NodeDrainAnnotation: constants.DrainMcpPaused}})
	if err != nil {
		return nil, err
	}

	drainingSelector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchLabels: map[string]string{constants.NodeDrainAnnotation: constants.Draining}})
	if err != nil {
		return nil, err
	}

	return &DrainReconcile{
		client,
		Scheme,
		kubeClient,
		openshiftContext,
		mcpPauseDrainSelector,
		drainingSelector}, nil
}

//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovoperatorconfigs,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (dr *DrainReconcile) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	req.Namespace = namespace
	reqLogger := log.FromContext(ctx)
	reqLogger.Info("Reconciling Drain")

	node := &corev1.Node{}
	err := dr.Get(ctx, req.NamespacedName, node)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	nodeNetworkState := &sriovnetworkv1.SriovNetworkNodeState{}
	err = dr.Get(ctx, req.NamespacedName, nodeNetworkState)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	// create the drain state annotation if it doesn't exist in the sriovNetworkNodeState object
	nodeStateDrainAnnotationCurrent, NodeStateDrainAnnotationCurrentExist := nodeNetworkState.Annotations[constants.NodeStateDrainAnnotationCurrent]
	if !NodeStateDrainAnnotationCurrentExist {
		err = utils.AnnotateObject(nodeNetworkState, constants.NodeStateDrainAnnotationCurrent, constants.DrainIdle, dr.Client)
		if err != nil {
			return ctrl.Result{}, err
		}
		nodeStateDrainAnnotationCurrent = constants.DrainIdle
	}

	// create the drain state annotation if it doesn't exist in the node object
	nodeDrainAnnotation, nodeDrainAnnotationExist := node.Annotations[constants.NodeDrainAnnotation]
	if !nodeDrainAnnotationExist {
		err = utils.AnnotateObject(nodeNetworkState, constants.NodeDrainAnnotation, constants.DrainIdle, dr.Client)
		if err != nil {
			return ctrl.Result{}, err
		}
		nodeDrainAnnotation = constants.DrainIdle
	}

	//TODO: change this to save it on runtime
	mcpPauseDrainNodeList := &corev1.NodeList{}
	err = dr.List(ctx, mcpPauseDrainNodeList, &client.ListOptions{LabelSelector: dr.mcpPauseDrainSelector})
	if err != nil {
		// Failed to get node list
		reqLogger.Error(err, "Error occurred on LIST nodes request from API server")
		return reconcile.Result{}, err
	}

	DrainingNodeList := &corev1.NodeList{}
	err = dr.List(ctx, DrainingNodeList, &client.ListOptions{LabelSelector: dr.mcpPauseDrainSelector})
	if err != nil {
		// Failed to get node list
		reqLogger.Error(err, "Error occurred on LIST nodes request from API server")
		return reconcile.Result{}, err
	}

	reqLogger.Info("Max node allowed to be draining at the same time", "MaxParallelNodeConfiguration", maxParallelNodeConfiguration)
	drainingNodes := len(mcpPauseDrainNodeList.Items) + len(DrainingNodeList.Items)
	reqLogger.Info("Count of draining", "drainingNodes", drainingNodes)

	// if both are Idle we don't need to do anything
	if nodeDrainAnnotation == constants.DrainIdle &&
		nodeStateDrainAnnotationCurrent == constants.DrainIdle {
		reqLogger.Info("node and nodeState are on idle nothing todo")
		return reconcile.Result{}, nil
	}

	if nodeDrainAnnotation == constants.DrainIdle &&
		nodeStateDrainAnnotationCurrent == constants.DrainComplete {
		err = dr.completeDrain(ctx, node)
		if err != nil {
			return ctrl.Result{}, err
		}

		err = utils.AnnotateObject(nodeNetworkState, constants.NodeStateDrainAnnotationCurrent, constants.DrainIdle, dr.Client)
		return ctrl.Result{}, err
	}

	if drainingNodes >= maxParallelNodeConfiguration {
		reqLogger.Info("MaxParallelNodeConfiguration limit reached for draining nodes re-enqueue the request")
		// TODO: make this time configurable
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// the node request to drain, but we are at the limit so we re-enqueue the request
	if nodeDrainAnnotation == constants.DrainRequired &&
		nodeStateDrainAnnotationCurrent == constants.DrainIdle {
		// pause MCP if we are on openshift but not hypershift deployment method as there is MCP in hypershift
		if !dr.openshiftContext.IsOpenshiftCluster() || (dr.openshiftContext.IsOpenshiftCluster() && dr.openshiftContext.IsHypershift()) {
			err = utils.AnnotateObject(nodeNetworkState, constants.NodeStateDrainAnnotationCurrent, constants.Draining, dr.Client)
			if err != nil {
				return ctrl.Result{}, err
			}
			nodeStateDrainAnnotationCurrent = constants.Draining

		} else if dr.openshiftContext.IsOpenshiftCluster() && !dr.openshiftContext.IsHypershift() {
			nodePoolName, err := dr.openshiftContext.GetNodeMachinePoolName(node)
			if err != nil {
				return reconcile.Result{}, err
			}

			if err := dr.pauseMCP(nodeNetworkState, nodePoolName); err != nil {
				return reconcile.Result{}, err
			}

			nodeStateDrainAnnotationCurrent = nodeNetworkState.Annotations[constants.NodeStateDrainAnnotationCurrent]
		}

	}

	// the node is MCP paused already, so we need to continue from here
	// TODO: with parallel draining we need to also check that there is no other reconcile loop that is working
	if nodeDrainAnnotation == constants.DrainRequired &&
		(nodeStateDrainAnnotationCurrent == constants.DrainMcpPaused || nodeStateDrainAnnotationCurrent == constants.Draining) {
		err = dr.drainNode(ctx, node)
		if err != nil {
			return ctrl.Result{}, err
		}

		err = utils.AnnotateObject(nodeNetworkState, constants.NodeStateDrainAnnotationCurrent, constants.DrainComplete, dr.Client)
		return ctrl.Result{}, err

	}

	if nodeDrainAnnotation == constants.DrainIdle &&
		nodeStateDrainAnnotationCurrent == constants.DrainMcpPaused {
		err = dr.completeDrain(ctx, node)
		if err != nil {
			return ctrl.Result{}, err
		}

		err = utils.AnnotateObject(nodeNetworkState, constants.NodeStateDrainAnnotationCurrent, constants.DrainIdle, dr.Client)
		return ctrl.Result{}, err

	}

	return reconcile.Result{}, nil
}

func (dr *DrainReconcile) drainNode(ctx context.Context, node *corev1.Node) error {
	reqLogger := log.FromContext(ctx).WithValues("drain node", node.Name)
	reqLogger.Info("drainNode(): Node drain requested", "node", node.Name)
	var err error

	drainer := &drain.Helper{
		Client:              dr.kubeClient,
		Force:               true,
		IgnoreAllDaemonSets: true,
		DeleteEmptyDirData:  true,
		GracePeriodSeconds:  -1,
		Timeout:             90 * time.Second,
		OnPodDeletedOrEvicted: func(pod *corev1.Pod, usingEviction bool) {
			verbStr := "Deleted"
			if usingEviction {
				verbStr = "Evicted"
			}
			log.Log.Info(fmt.Sprintf("%s pod from Node %s/%s", verbStr, pod.Namespace, pod.Name))
		},
		Ctx:    ctx,
		Out:    writer{log.Log.Info},
		ErrOut: writer{func(msg string, kv ...interface{}) { log.Log.Error(nil, msg, kv...) }},
	}

	backoff := wait.Backoff{
		Steps:    5,
		Duration: 10 * time.Second,
		Factor:   2,
	}
	var lastErr error

	reqLogger.Info("drainNode(): Start draining")
	if err = wait.ExponentialBackoff(backoff, func() (bool, error) {
		err := drain.RunCordonOrUncordon(drainer, node, true)
		if err != nil {
			lastErr = err
			reqLogger.Info("drainNode(): Cordon failed, retrying", "error", err)
			return false, nil
		}
		err = drain.RunNodeDrain(drainer, node.Name)
		if err == nil {
			return true, nil
		}
		lastErr = err
		reqLogger.Info("drainNode(): Draining failed, retrying", "error", err)
		return false, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			reqLogger.Info("drainNode(): failed to drain node", "steps", backoff.Steps, "error", lastErr)
		}
		reqLogger.Info("drainNode(): failed to drain node", "error", err)
		return err
	}
	reqLogger.Info("drainNode(): drain complete")
	return nil
}

func (dr *DrainReconcile) pauseMCP(nodeState *sriovnetworkv1.SriovNetworkNodeState, mcpName string) error {
	log.Log.Info("pauseMCP(): pausing MCP")
	var err error

	mcpInformerFactory := mcfginformers.NewSharedInformerFactory(dr.openshiftContext.McClient,
		time.Second*30,
	)
	mcpInformer := mcpInformerFactory.Machineconfiguration().V1().MachineConfigPools().Informer()

	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()
	paused := nodeState.Annotations[constants.NodeStateDrainAnnotationCurrent] == constants.DrainMcpPaused

	mcpEventHandler := func(obj interface{}) {
		mcp := obj.(*mcfgv1.MachineConfigPool)
		if mcp.GetName() != mcpName {
			return
		}
		// Always get the latest object
		newMcp, err := dr.openshiftContext.McClient.MachineconfigurationV1().MachineConfigPools().Get(ctx, mcpName, metav1.GetOptions{})
		if err != nil {
			log.Log.V(2).Error(err, "pauseMCP(): Failed to get MCP", "mcp-name", mcpName)
			return
		}
		if mcfgv1.IsMachineConfigPoolConditionFalse(newMcp.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) &&
			mcfgv1.IsMachineConfigPoolConditionTrue(newMcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) &&
			mcfgv1.IsMachineConfigPoolConditionFalse(newMcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) {
			log.Log.V(2).Info("pauseMCP(): MCP is ready", "mcp-name", mcpName)
			if paused {
				log.Log.V(2).Info("pauseMCP(): stop MCP informer")
				cancel()
				return
			}
			if newMcp.Spec.Paused {
				log.Log.V(2).Info("pauseMCP(): MCP was paused by other, wait...", "mcp-name", mcpName)
				return
			}
			log.Log.Info("pauseMCP(): pause MCP", "mcp-name", mcpName)
			pausePatch := []byte("{\"spec\":{\"paused\":true}}")
			_, err = dr.openshiftContext.McClient.MachineconfigurationV1().MachineConfigPools().Patch(context.Background(), mcpName, types.MergePatchType, pausePatch, metav1.PatchOptions{})
			if err != nil {
				log.Log.V(2).Error(err, "pauseMCP(): failed to pause MCP", "mcp-name", mcpName)
				return
			}
			err = utils.AnnotateObject(nodeState, constants.NodeStateDrainAnnotationCurrent, constants.DrainMcpPaused, dr.Client)
			if err != nil {
				log.Log.V(2).Error(err, "pauseMCP(): Failed to annotate node")
				return
			}
			nodeState.Annotations[constants.NodeStateDrainAnnotationCurrent] = constants.DrainMcpPaused
			paused = true
			return
		}
		if paused {
			log.Log.Info("pauseMCP(): MCP is processing, resume MCP", "mcp-name", mcpName)
			pausePatch := []byte("{\"spec\":{\"paused\":false}}")
			_, err = dr.openshiftContext.McClient.MachineconfigurationV1().MachineConfigPools().Patch(context.Background(), mcpName, types.MergePatchType, pausePatch, metav1.PatchOptions{})
			if err != nil {
				log.Log.V(2).Error(err, "pauseMCP(): fail to resume MCP", "mcp-name", mcpName)
				return
			}
			err = utils.AnnotateObject(nodeState, constants.NodeStateDrainAnnotationCurrent, constants.DrainIdle, dr.Client)
			if err != nil {
				log.Log.V(2).Error(err, "pauseMCP(): Failed to annotate node")
				return
			}
			nodeState.Annotations[constants.NodeStateDrainAnnotationCurrent] = constants.DrainIdle
			paused = false
		}
		log.Log.Info("pauseMCP():MCP is not ready, wait...",
			"mcp-name", newMcp.GetName(), "mcp-conditions", newMcp.Status.Conditions)
	}

	mcpInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: mcpEventHandler,
		UpdateFunc: func(old, new interface{}) {
			mcpEventHandler(new)
		},
	})

	// The Draining_MCP_Paused state means the MCP work has been paused by the config daemon in previous round.
	// Only check MCP state if the node is not in Draining_MCP_Paused state
	if !paused {
		mcpInformerFactory.Start(ctx.Done())
		mcpInformerFactory.WaitForCacheSync(ctx.Done())
		<-ctx.Done()
	}

	return err
}

func (dr *DrainReconcile) completeDrain(ctx context.Context, node *corev1.Node) error {
	drainer := &drain.Helper{
		Client:              dr.kubeClient,
		Force:               true,
		IgnoreAllDaemonSets: true,
		DeleteEmptyDirData:  true,
		GracePeriodSeconds:  -1,
		Timeout:             90 * time.Second,
		OnPodDeletedOrEvicted: func(pod *corev1.Pod, usingEviction bool) {
			verbStr := "Deleted"
			if usingEviction {
				verbStr = "Evicted"
			}
			log.Log.Info(fmt.Sprintf("%s pod from Node %s/%s", verbStr, pod.Namespace, pod.Name))
		},
		Ctx:    ctx,
		Out:    writer{log.Log.Info},
		ErrOut: writer{func(msg string, kv ...interface{}) { log.Log.Error(nil, msg, kv...) }},
	}

	//if !dn.disableDrain {
	//	if err := drain.RunCordonOrUncordon(dr.drainer, node, false); err != nil {
	//		return err
	//	}
	//}
	if err := drain.RunCordonOrUncordon(drainer, node, false); err != nil {
		return err
	}

	if dr.openshiftContext.IsOpenshiftCluster() && !dr.openshiftContext.IsHypershift() {
		mcpName, err := dr.openshiftContext.GetNodeMachinePoolName(node)
		if err != nil {
			return err
		}

		log.Log.Info("completeDrain(): resume MCP", "mcp-name", mcpName)
		pausePatch := []byte("{\"spec\":{\"paused\":false}}")
		if _, err := dr.openshiftContext.McClient.MachineconfigurationV1().MachineConfigPools().Patch(context.Background(),
			mcpName, types.MergePatchType, pausePatch, metav1.PatchOptions{}); err != nil {

			log.Log.Error(err, "completeDrain(): failed to resume MCP", "mcp-name", mcpName)
			return err
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (dr *DrainReconcile) SetupWithManager(mgr ctrl.Manager) error {
	createUpdateEnqueue := handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: namespace,
				Name:      e.Object.GetName(),
			}})

		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: namespace,
				Name:      e.ObjectNew.GetName(),
			}})
		},
	}

	// Watch for spec and annotation changes
	nodePredicates := builder.WithPredicates(DrainAnnotationPredicate{})
	nodeStatePredicates := builder.WithPredicates(DrainAnnotationPredicate{})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}, nodePredicates).
		Watches(&sriovnetworkv1.SriovNetworkNodeState{}, createUpdateEnqueue, nodeStatePredicates).
		Complete(dr)
}
