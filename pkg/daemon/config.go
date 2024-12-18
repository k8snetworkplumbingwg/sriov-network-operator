package daemon

import (
	"context"
	"reflect"

	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	snolog "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/log"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

type OperatorConfigReconcile struct {
	client             client.Client
	latestFeatureGates map[string]bool
}

func NewOperatorConfigReconcile(client client.Client) *OperatorConfigReconcile {
	return &OperatorConfigReconcile{client: client, latestFeatureGates: make(map[string]bool)}
}

func (oc *OperatorConfigReconcile) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLogger := log.FromContext(ctx).WithName("Reconcile")
	operatorConfig := &sriovnetworkv1.SriovOperatorConfig{}
	err := oc.client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, operatorConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			reqLogger.Info("OperatorConfig doesn't exist", "name", req.Name, "namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		reqLogger.Error(err, "Failed to operatorConfig", "name", req.Name, "namespace", req.Namespace)
		return ctrl.Result{}, err
	}

	// update log level
	snolog.SetLogLevel(operatorConfig.Spec.LogLevel)

	newDisableDrain := operatorConfig.Spec.DisableDrain
	if vars.DisableDrain != newDisableDrain {
		vars.DisableDrain = newDisableDrain
		log.Log.Info("Set Disable Drain", "value", vars.DisableDrain)
	}

	if !reflect.DeepEqual(oc.latestFeatureGates, operatorConfig.Spec.FeatureGates) {
		vars.FeatureGate.Init(operatorConfig.Spec.FeatureGates)
		oc.latestFeatureGates = operatorConfig.Spec.FeatureGates
		log.Log.Info("Updated featureGates", "featureGates", vars.FeatureGate.String())
	}

	return ctrl.Result{}, nil
}

func (oc *OperatorConfigReconcile) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovnetworkv1.SriovOperatorConfig{}).
		Complete(oc)
}
