package controllers

import (
	"context"
	"encoding/json"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/orchestrator"
)

// SriovNetworkPoolConfigReconciler reconciles a SriovNetworkPoolConfig object.
type SriovNetworkPoolConfigReconciler struct {
	client.Client
	Orchestrator orchestrator.Interface
}

//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworkpoolconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworkpoolconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworkpoolconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups=machineconfiguration.openshift.io,resources=machineconfigs,verbs=get;list;watch;delete
//+kubebuilder:rbac:groups=machineconfiguration.openshift.io,resources=machineconfigpools,verbs=get;list;watch

func (r *SriovNetworkPoolConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pool := &sriovnetworkv1.SriovNetworkPoolConfig{}
	if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	ovs := pool.Spec.OvsHardwareOffloadConfig
	removedFinalizer := controllerutil.RemoveFinalizer(pool, sriovnetworkv1.POOLCONFIGFINALIZERNAME)
	if r.Orchestrator.ClusterType() != consts.ClusterTypeOpenshift || ovs == nil || ovs.Name == "" || ovs.Name == "master" {
		if removedFinalizer {
			return ctrl.Result{}, client.IgnoreNotFound(r.Update(ctx, pool))
		}
		return ctrl.Result{}, nil
	}

	mcKey := client.ObjectKey{Name: "00-" + ovs.Name + "-" + consts.OVSHWOLMachineConfigNameSuffix}
	mc := &mcfgv1.MachineConfig{}
	switch err := r.Get(ctx, mcKey, mc); {
	case err == nil:
		var source sriovnetworkv1.SourceRevision
		stamp := mc.Annotations[consts.PoolRevisionAnnotation]
		if json.Unmarshal([]byte(stamp), &source) == nil && source.Name == pool.Name && source.UID == pool.UID {
			if err := client.IgnoreNotFound(r.Delete(ctx, mc)); err != nil && !meta.IsNoMatchError(err) {
				return ctrl.Result{}, fmt.Errorf("delete legacy MachineConfig %s: %w", mc.Name, err)
			}
		}
	case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
		// The MachineConfig or its API is absent. Continue with pool cleanup.
	default:
		return ctrl.Result{}, fmt.Errorf("get legacy MachineConfig %s: %w", mcKey.Name, err)
	}
	if !pool.DeletionTimestamp.IsZero() || pool.Spec.NodeSelector != nil {
		if removedFinalizer {
			return ctrl.Result{}, client.IgnoreNotFound(r.Update(ctx, pool))
		}
		return ctrl.Result{}, nil
	}

	mcp := &mcfgv1.MachineConfigPool{}
	if err := r.Get(ctx, client.ObjectKey{Name: ovs.Name}, mcp); err != nil {
		return ctrl.Result{}, fmt.Errorf("get MachineConfigPool %s: %w", ovs.Name, err)
	}
	selector := mcp.Spec.NodeSelector
	if selector == nil || len(selector.MatchLabels)+len(selector.MatchExpressions) == 0 {
		return ctrl.Result{}, fmt.Errorf("MachineConfigPool %s has no node selector", ovs.Name)
	}
	if _, err := metav1.LabelSelectorAsSelector(selector); err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid node selector on MachineConfigPool %s: %w", ovs.Name, err)
	}
	pool.Spec.NodeSelector = selector.DeepCopy()
	if pool.Spec.MaxUnavailable == nil {
		// Migration sets a conservative limit of one drain at a time.
		// Administrators may adjust maxUnavailable after migration if needed.
		maxUnavailable := oneNode
		pool.Spec.MaxUnavailable = &maxUnavailable
	}
	ovs.Name = ""
	return ctrl.Result{}, client.IgnoreNotFound(r.Update(ctx, pool))
}

// SetupWithManager sets up the controller with the Manager.
func (r *SriovNetworkPoolConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovnetworkv1.SriovNetworkPoolConfig{}).
		Complete(r)
}
