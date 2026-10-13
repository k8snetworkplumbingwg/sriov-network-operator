package controllers

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/orchestrator"
)

// SriovNetworkPoolConfigReconciler reconciles a SriovNetworkPoolConfig object
type SriovNetworkPoolConfigReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Orchestrator orchestrator.Interface
}

const (
	mcPriority     = "00-"
	masterPoolName = "master"
)

//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworkpoolconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworkpoolconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworkpoolconfigs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the SriovNetworkPoolConfig object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.6.4/pkg/reconcile
func (r *SriovNetworkPoolConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("sriovnetworkpoolconfig", req.NamespacedName)
	logger.Info("Reconciling")

	// Fetch SriovNetworkPoolConfig
	instance := &sriovnetworkv1.SriovNetworkPoolConfig{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			logger.Info("instance not found")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// OvsHardwareOffloadConfig.Name is deprecated. Hardware offload is applied by the
	// config daemon on switchdev nodes. Pools that still set Name are excluded from
	// node selection and only exist so leftover MachineConfigs can be deleted.
	if instance.Spec.OvsHardwareOffloadConfig.Name == "" {
		return ctrl.Result{}, nil
	}

	logger.Info("OvsHardwareOffloadConfig.Name is deprecated and ignored",
		"name", instance.Spec.OvsHardwareOffloadConfig.Name)

	if r.shouldDeleteOvsHardwareOffloadMachineConfig() {
		if err = r.deleteOvsHardwareOffloadMachineConfig(ctx, instance); err != nil {
			return reconcile.Result{}, err
		}
	}

	if sriovnetworkv1.StringInArray(sriovnetworkv1.POOLCONFIGFINALIZERNAME, instance.ObjectMeta.Finalizers) {
		var found bool
		instance.ObjectMeta.Finalizers, found = sriovnetworkv1.RemoveString(sriovnetworkv1.POOLCONFIGFINALIZERNAME, instance.ObjectMeta.Finalizers)
		if found {
			if err := r.Update(ctx, instance); err != nil {
				return reconcile.Result{}, err
			}
		}
	}

	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SriovNetworkPoolConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovnetworkv1.SriovNetworkPoolConfig{}).
		Complete(r)
}

// shouldDeleteOvsHardwareOffloadMachineConfig reports whether leftover HWOL MachineConfigs
// should be removed. Hypershift has no MachineConfig API for this object.
func (r *SriovNetworkPoolConfigReconciler) shouldDeleteOvsHardwareOffloadMachineConfig() bool {
	return r.Orchestrator.ClusterType() == constants.ClusterTypeOpenshift &&
		r.Orchestrator.Flavor() != constants.ClusterFlavorHypershift
}

// deleteOvsHardwareOffloadMachineConfig removes a MachineConfig previously created for OVS hardware offload.
func (r *SriovNetworkPoolConfigReconciler) deleteOvsHardwareOffloadMachineConfig(ctx context.Context, nc *sriovnetworkv1.SriovNetworkPoolConfig) error {
	logger := log.Log.WithName("deleteOvsHardwareOffloadMachineConfig")

	mcpName := nc.Spec.OvsHardwareOffloadConfig.Name
	if mcpName == "" || mcpName == masterPoolName {
		logger.Info("skip leftover MachineConfig delete", "pool", nc.Name, "mcp", mcpName)
		return nil
	}

	mcName := mcPriority + mcpName + "-" + constants.OVSHWOLMachineConfigNameSuffix
	foundMC := &mcfgv1.MachineConfig{}
	err := r.Get(ctx, types.NamespacedName{Name: mcName}, foundMC)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("leftover MachineConfig already absent", "machineConfig", mcName)
			return nil
		}
		return fmt.Errorf("failed to get MachineConfig %s: %w", mcName, err)
	}

	logger.Info("deleting leftover OVS hardware offload MachineConfig", "machineConfig", mcName, "mcp", mcpName)
	if err = r.Delete(ctx, foundMC); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("couldn't delete MachineConfig %s: %w", mcName, err)
	}
	return nil
}
