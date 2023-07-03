package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

type DrainReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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

	config := &sriovnetworkv1.SriovOperatorConfig{}
	err := dr.Get(ctx, types.NamespacedName{
		Name: constants.DefaultConfigName, Namespace: namespace}, config)
	if err != nil {
		reqLogger.Error(err, "Error occurred on GET SriovOperatorConfig request from API server.")
		return reconcile.Result{}, err
	}

	nodeList := &corev1.NodeList{}
	err = dr.List(ctx, nodeList)
	if err != nil {
		// Failed to get node list
		reqLogger.Error(err, "Error occurred on LIST nodes request from API server")
		return reconcile.Result{}, err
	}

	// sort nodeList to iterate in the same order each reconcile loop
	sort.Slice(nodeList.Items, func(i, j int) bool {
		return strings.Compare(nodeList.Items[i].Name, nodeList.Items[j].Name) == -1
	})

	reqLogger.Info("Max node allowed to be draining at the same time", "MaxParallelNodeConfiguration", config.Spec.MaxParallelNodeConfiguration)

	drainingNodes := 0
	for _, node := range nodeList.Items {
		if utils.NodeHasAnnotation(node, constants.NodeDrainAnnotation, constants.AnnoDraining) || utils.NodeHasAnnotation(node, constants.NodeDrainAnnotation, constants.AnnoMcpPaused) {
			drainingNodes++
		}
	}

	reqLogger.Info("Count of draining", "drainingNodes", drainingNodes)
	if config.Spec.MaxParallelNodeConfiguration != 0 && drainingNodes >= config.Spec.MaxParallelNodeConfiguration {
		reqLogger.Info("MaxParallelNodeConfiguration limit reached for draining nodes")
		return reconcile.Result{}, nil
	}

	for _, node := range nodeList.Items {
		if !utils.NodeHasAnnotation(node, constants.NodeDrainAnnotation, constants.AnnoDrainRequired) {
			continue
		}
		if config.Spec.MaxParallelNodeConfiguration == 0 || drainingNodes < config.Spec.MaxParallelNodeConfiguration {
			reqLogger.Info("Start draining node", "node", node.Name)
			patch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%s"}}}`, constants.NodeDrainAnnotation, constants.AnnoDraining))
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
		CreateFunc: func(e event.CreateEvent, q workqueue.RateLimitingInterface) {
			qHandler(q)
		},
		UpdateFunc: func(e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			qHandler(q)
		},
	}

	// Watch for spec and annotation changes
	nodePredicates := builder.WithPredicates(DrainAnnotationPredicate{})

	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovnetworkv1.SriovOperatorConfig{}).
		Watches(&source.Kind{Type: &corev1.Node{}}, createUpdateEnqueue, nodePredicates).
		Complete(dr)
}
