package daemon

import (
	"context"
	"path/filepath"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	snolog "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/log"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

// OperatorConfigNodeReconcile represents the reconcile struct for the OperatorConfig.
type OperatorConfigNodeReconcile struct {
	client             client.Client
	latestFeatureGates map[string]bool
	latestLogCfg       vars.LogFileSettings
}

// NewOperatorConfigNodeReconcile creates a new instance of OperatorConfigNodeReconcile with the given client.
func NewOperatorConfigNodeReconcile(client client.Client) *OperatorConfigNodeReconcile {
	return &OperatorConfigNodeReconcile{
		client:             client,
		latestFeatureGates: make(map[string]bool),
		latestLogCfg:       vars.LogFileSettings{},
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

	// update log level
	snolog.SetLogLevel(operatorConfig.Spec.LogLevel)

	// update persistent file logging if LogConfig changed
	oc.reconcileLogConfig(reqLogger, operatorConfig.Spec.LogConfig)

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

// reconcileLogConfig re-initializes or closes the file logger.
func (oc *OperatorConfigNodeReconcile) reconcileLogConfig(reqLogger logr.Logger, lc *sriovnetworkv1.LogConfig) {
	newCfg := effectiveLogCfg(lc)
	if newCfg == oc.latestLogCfg {
		return
	}

	vars.LogCfg = newCfg

	if !newCfg.Enabled {
		oc.latestLogCfg = newCfg
		reqLogger.V(1).Info("persistent file logging disabled via LogConfig update")
		snolog.CloseFileLogger()
		return
	}

	logFilePath := utils.GetHostExtensionPath(filepath.Join(newCfg.HostPath, "config-daemon.log"))
	if err := snolog.InitLogWithFile(logFilePath, newCfg.MaxSizeMB, newCfg.MaxFiles, newCfg.MaxAgeDays, newCfg.Compress); err != nil {
		reqLogger.Error(err, "failed to reconfigure file logging after LogConfig update, will retry on next reconcile")
		return
	}

	oc.latestLogCfg = newCfg
	reqLogger.Info("persistent file logging reconfigured (async-buffered, entries are flushed before chroot windows)",
		"path", logFilePath, "maxSizeMB", newCfg.MaxSizeMB, "maxFiles", newCfg.MaxFiles)
}

// effectiveLogCfg derives a LogFileSettings from an operator LogConfig pointer,
// applying defaults for any field that is nil or zero.
func effectiveLogCfg(lc *sriovnetworkv1.LogConfig) vars.LogFileSettings {
	cfg := vars.DefaultLogCfg()
	if lc == nil {
		return cfg
	}
	if lc.Enabled != nil {
		cfg.Enabled = *lc.Enabled
	}
	if lc.MaxSizeMB != nil {
		cfg.MaxSizeMB = *lc.MaxSizeMB
	}
	if lc.MaxFiles != nil {
		cfg.MaxFiles = *lc.MaxFiles
	}
	if lc.MaxAgeDays != nil {
		cfg.MaxAgeDays = *lc.MaxAgeDays
	}
	if lc.Compress != nil {
		cfg.Compress = *lc.Compress
	}
	if lc.HostPath != nil && *lc.HostPath != "" {
		cfg.HostPath = *lc.HostPath
	}
	return cfg
}
