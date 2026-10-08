package daemon

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	snolog "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/log"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

// OperatorConfigNodeReconcile represents the reconcile struct for the OperatorConfig.
type OperatorConfigNodeReconcile struct {
	client             client.Client
	latestFeatureGates map[string]bool
}

// NewOperatorConfigNodeReconcile creates a new instance of OperatorConfigNodeReconcile with the given client.
func NewOperatorConfigNodeReconcile(client client.Client) *OperatorConfigNodeReconcile {
	return &OperatorConfigNodeReconcile{
		client:             client,
		latestFeatureGates: make(map[string]bool),
	}
}

// Reconcile reconciles the OperatorConfig resource. It updates log level and feature gates as necessary.
func (oc *OperatorConfigNodeReconcile) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLogger := log.FromContext(ctx).WithName("Reconcile")
	operatorConfig := &sriovnetworkv1.SriovOperatorConfig{}
	err := oc.client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, operatorConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			reqLogger.Info("OperatorConfig doesn't exist", "name", req.Name, "namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		reqLogger.Error(err, "Failed to get OperatorConfig", "name", req.Name, "namespace", req.Namespace)
		return ctrl.Result{}, err
	}

	snolog.SetLogLevel(operatorConfig.Spec.LogLevel)
	if err := oc.reconcileLogConfig(reqLogger, operatorConfig.Spec.LogConfig); err != nil {
		return ctrl.Result{}, err
	}

	newDisableDrain := operatorConfig.Spec.DisableDrain
	if vars.DisableDrain != newDisableDrain {
		vars.DisableDrain = newDisableDrain
		log.Log.Info("Set Disable Drain", "value", vars.DisableDrain)
	}

	if !equality.Semantic.DeepEqual(oc.latestFeatureGates, operatorConfig.Spec.FeatureGates) {
		vars.FeatureGate.Init(operatorConfig.Spec.FeatureGates)
		oc.latestFeatureGates = operatorConfig.Spec.FeatureGates
		log.Log.Info("Updated featureGates", "featureGates", vars.FeatureGate.String())
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the reconciliation logic for this controller using the given manager.
func (oc *OperatorConfigNodeReconcile) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovnetworkv1.SriovOperatorConfig{}).
		Complete(oc)
}

// reconcileLogConfig applies LogConfig changes to the file logger.
func (oc *OperatorConfigNodeReconcile) reconcileLogConfig(reqLogger logr.Logger, lc *sriovnetworkv1.LogConfig) error {
	newCfg, err := sriovnetworkv1.GetEffectiveConfigDaemonLogConfig(lc)
	if err != nil {
		reqLogger.Error(err, "invalid log configuration, skipping update",
			"component", snolog.Component,
			"subsystem", snolog.SubsystemPersistentFileLogging,
			"node", vars.NodeName,
		)
		return nil
	}
	curCfg := vars.GetLogCfg()
	if !needsLogReconcile(curCfg, newCfg) {
		return nil
	}
	savedCfg := curCfg
	vars.SetLogCfg(newCfg)
	if err := snolog.InitLogWithFile(); err != nil {
		vars.SetLogCfg(savedCfg)
		reqLogger.Error(err, "failed to reconfigure persistent file logging, will retry on next reconcile",
			"component", snolog.Component,
			"subsystem", snolog.SubsystemPersistentFileLogging,
			"node", vars.NodeName,
			"enabled", newCfg.Enabled,
		)
		return err
	}
	return nil
}

func needsLogReconcile(cur, new vars.LogFileSettings) bool {
	if cur != new {
		return true
	}

	loggerActive := snolog.IsFileLoggerActive()
	if new.Enabled && !loggerActive {
		return true
	}
	if !new.Enabled && loggerActive {
		return true
	}
	return false
}
