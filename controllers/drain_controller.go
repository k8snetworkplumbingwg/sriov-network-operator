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
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/kubectl/pkg/drain"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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

type DrainReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Drainer *drain.Helper
}

//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovoperatorconfigs,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (dr *DrainReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	req.Namespace = namespace
	reqLogger := log.FromContext(ctx).WithValues("drain", req.NamespacedName)
	reqLogger.Info("Reconciling Drain")

	nodeList := &corev1.NodeList{}
	err := dr.List(ctx, nodeList)
	if err != nil {
		// Failed to get node list
		reqLogger.Error(err, "Error occurred on LIST nodes request from API server")
		return reconcile.Result{}, err
	}

	// sort nodeList to iterate in the same order each reconcile loop
	sort.Slice(nodeList.Items, func(i, j int) bool {
		return strings.Compare(nodeList.Items[i].Name, nodeList.Items[j].Name) == -1
	})

	reqLogger.Info("Max node allowed to be draining at the same time", "MaxParallelNodeConfiguration", maxParallelNodeConfiguration)

	drainingNodes := 0
	for _, node := range nodeList.Items {
		if utils.NodeHasAnnotation(node, constants.NodeDrainAnnotation, constants.Draining) || utils.NodeHasAnnotation(node, constants.NodeDrainAnnotation, constants.DrainMcpPaused) {
			dr.drainNode(ctx, &node)
			drainingNodes++
		}
	}

	reqLogger.Info("Count of draining", "drainingNodes", drainingNodes)
	if drainingNodes >= maxParallelNodeConfiguration {
		reqLogger.Info("MaxParallelNodeConfiguration limit reached for draining nodes")
		return reconcile.Result{}, nil
	}

	for _, node := range nodeList.Items {
		if !utils.NodeHasAnnotation(node, constants.NodeDrainAnnotation, constants.DrainRequired) {
			continue
		}
		if drainingNodes < maxParallelNodeConfiguration {
			reqLogger.Info("Start draining node", "node", node.Name)
			patch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%s"}}}`, constants.NodeDrainAnnotation, constants.Draining))
			err = dr.Client.Patch(context.TODO(), &node, client.RawPatch(types.StrategicMergePatchType, patch))
			if err != nil {
				reqLogger.Error(err, "Failed to patch node annotations")
				return reconcile.Result{}, err
			}
			drainingNodes++
		} else {
			reqLogger.Info("Too many nodes to be draining at the moment. Skipping node %s", "node", node.Name)
			return reconcile.Result{}, nil
		}
	}

	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (dr *DrainReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// we always add object with a same(static) key to the queue to reduce
	// reconciliation count
	qHandler := func(q workqueue.RateLimitingInterface) {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: namespace,
			Name:      "drain-upgrade-reconcile-name",
		}})
	}

	createUpdateEnqueue := handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
			qHandler(q)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			qHandler(q)
		},
	}

	// Watch for spec and annotation changes
	nodePredicates := builder.WithPredicates(DrainAnnotationPredicate{})

	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovnetworkv1.SriovOperatorConfig{}).
		Watches(&corev1.Node{}, createUpdateEnqueue, nodePredicates).
		Complete(dr)
}

func (dr *DrainReconciler) drainNode(ctx context.Context, node *corev1.Node) error {
	reqLogger := log.FromContext(ctx).WithValues("drain node", node.Name)
	reqLogger.Info("drainNode(): Node drain requested", "node", node.Name)
	var err error

	backoff := wait.Backoff{
		Steps:    5,
		Duration: 10 * time.Second,
		Factor:   2,
	}
	var lastErr error

	reqLogger.Info("drainNode(): Start draining")
	if err = wait.ExponentialBackoff(backoff, func() (bool, error) {
		err := drain.RunCordonOrUncordon(dr.Drainer, node, true)
		if err != nil {
			lastErr = err
			reqLogger.Info("drainNode(): Cordon failed, retrying", "error", err)
			return false, nil
		}
		err = drain.RunNodeDrain(dr.Drainer, node.Name)
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
