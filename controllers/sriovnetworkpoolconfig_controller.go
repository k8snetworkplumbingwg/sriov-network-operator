package controllers

import (
	"bytes"
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	render "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/render"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

// SriovNetworkPoolConfigReconciler reconciles a SriovNetworkPoolConfig object
type SriovNetworkPoolConfigReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworkpoolconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworkpoolconfigs/status,verbs=get;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the SriovNetworkPoolConfig object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.6.4/pkg/reconcile
func (r *SriovNetworkPoolConfigReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	_ = context.Background()
	reqLogger := r.Log.WithValues("sriovnetworkpoolconfig", req.NamespacedName)
	reqLogger.Info("Reconciling")

	// // Fetch SriovNetworkPoolConfig
	instance := &sriovnetworkv1.SriovNetworkPoolConfig{}
	err := r.Get(context.TODO(), req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			reqLogger.Info("instance not found")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// examine DeletionTimestamp to determine if object is under deletion
	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer and update the object. This is equivalent
		// registering our finalizer.
		if !sriovnetworkv1.StringInArray(sriovnetworkv1.POOLCONFIGFINALIZERNAME, instance.ObjectMeta.Finalizers) {
			instance.ObjectMeta.Finalizers = append(instance.ObjectMeta.Finalizers, sriovnetworkv1.POOLCONFIGFINALIZERNAME)
			if err := r.Update(context.Background(), instance); err != nil {
				return reconcile.Result{}, err
			}
		}
		if utils.ClusterType == utils.ClusterTypeOpenshift {
			if err = r.syncOvsHardwareOffloadMachineConfigs(instance, false); err != nil {
				return reconcile.Result{}, err
			}
		}
	} else {
		// The object is being deleted
		if sriovnetworkv1.StringInArray(sriovnetworkv1.POOLCONFIGFINALIZERNAME, instance.ObjectMeta.Finalizers) {
			// our finalizer is present, so lets handle any external dependency
			reqLogger.Info("delete SriovNetworkPoolConfig CR", "Namespace", instance.Namespace, "Name", instance.Name)
			if utils.ClusterType == utils.ClusterTypeOpenshift {
				if err = r.syncOvsHardwareOffloadMachineConfigs(instance, true); err != nil {
					// if fail to delete the external dependency here, return with error
					// so that it can be retried
					return reconcile.Result{}, err
				}
			}
			// remove our finalizer from the list and update it.
			instance.ObjectMeta.Finalizers = sriovnetworkv1.RemoveString(sriovnetworkv1.POOLCONFIGFINALIZERNAME, instance.ObjectMeta.Finalizers)
			if err := r.Update(context.Background(), instance); err != nil {
				return reconcile.Result{}, err
			}
		}
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SriovNetworkPoolConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovnetworkv1.SriovNetworkPoolConfig{}).
		Complete(r)
}

func (r *SriovNetworkPoolConfigReconciler) syncOvsHardwareOffloadMachineConfigs(nc *sriovnetworkv1.SriovNetworkPoolConfig, deletion bool) error {
	logger := r.Log.WithName("syncOvsHardwareOffloadMachineConfigs")

	data := render.MakeRenderData()
	mcpMap := make(map[string]bool)

	mcpList := &mcfgv1.MachineConfigPoolList{}
	err := r.List(context.TODO(), mcpList, &client.ListOptions{})
	if err != nil {
		return fmt.Errorf("Failed to get MachineConfigPoolList: %v", err)
	}

	for _, mcp := range mcpList.Items {
		// MachineConfigPool is selected when MachineConfigPool name matches with OvsHardwareOffloadConfig.Name
		if mcp.GetName() == nc.Spec.OvsHardwareOffloadConfig.Name {
			if mcp.GetName() == "master" {
				logger.Info("Master nodes are selected viby OvsHardwareOffloadConfig.Name, ignoring.")
				continue
			}
			if deletion {
				mcpMap[mcp.GetName()] = false
			} else {
				mcpMap[mcp.GetName()] = true
			}
			break
		}
	}

	for mcpName, enable := range mcpMap {
		mcName := "00-" + mcpName + "-" + OVS_HWOL_MACHINE_CONFIG_NAME_SUFFIX
		mc, err := render.GenerateMachineConfig("bindata/manifests/switchdev-config", mcName, mcpName, true, &data)
		if err != nil {
			return err
		}

		foundMC := &mcfgv1.MachineConfig{}
		err = r.Get(context.TODO(), types.NamespacedName{Name: mcName}, foundMC)
		if err != nil {
			if errors.IsNotFound(err) {
				if enable {
					err = r.Create(context.TODO(), mc)
					if err != nil {
						return fmt.Errorf("Couldn't create MachineConfig: %v", err)
					}
					logger.Info("Created MachineConfig CR in MachineConfigPool", mcName, mcpName)
				}
			} else {
				return fmt.Errorf("Failed to get MachineConfig: %v", err)
			}
		} else {
			if enable {
				if bytes.Compare(foundMC.Spec.Config.Raw, mc.Spec.Config.Raw) == 0 {
					logger.Info("MachineConfig already exists, updating")
					err = r.Update(context.TODO(), foundMC)
					if err != nil {
						return fmt.Errorf("Couldn't update MachineConfig: %v", err)
					}
				} else {
					logger.Info("No content change, skip updating MC")
				}
			} else {
				logger.Info("offload disabled, delete MachineConfig")
				err = r.Delete(context.TODO(), foundMC)
				if err != nil {
					return fmt.Errorf("Couldn't delete MachineConfig: %v", err)
				}
			}
		}
	}

	// Remove stale MCs for MCP that no longer exists in OvsHardwareOffloadConfig
	for _, mcp := range mcpList.Items {
		found := false
		for mcpName := range mcpMap {
			if mcp.Name == mcpName {
				found = true
				break
			}
		}
		if !found {
			mcName := "00-" + mcp.Name + "-" + OVS_HWOL_MACHINE_CONFIG_NAME_SUFFIX
			foundMC := &mcfgv1.MachineConfig{}
			err = r.Get(context.TODO(), types.NamespacedName{Name: mcName}, foundMC)
			if err == nil {
				delErr := r.Delete(context.TODO(), foundMC)
				if delErr != nil {
					return fmt.Errorf("Couldn't delete MachineConfig: %v", delErr)
				}
			} else {
				if !errors.IsNotFound(err) {
					return fmt.Errorf("Failed to get MachineConfig: %v", err)
				}
			}
		}
	}

	return nil
}
