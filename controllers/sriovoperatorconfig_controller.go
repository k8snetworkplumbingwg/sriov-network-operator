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
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrl_builder "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	machinev1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	apply "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/apply"
	consts "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
	snolog "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/log"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/platforms"
	render "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/render"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

// SriovOperatorConfigReconciler reconciles a SriovOperatorConfig object
type SriovOperatorConfigReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	PlatformHelper platforms.Interface
	FeatureGate    featuregate.FeatureGate
}

//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovoperatorconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovoperatorconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovoperatorconfigs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the SriovOperatorConfig object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *SriovOperatorConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("sriovoperatorconfig", req.NamespacedName)
	logger.Info("Reconciling SriovOperatorConfig")

	// Note: in SetupWithManager we setup manager to enqueue only default config obj
	defaultConfig := &sriovnetworkv1.SriovOperatorConfig{}
	err := r.Get(ctx, req.NamespacedName, defaultConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("default SriovOperatorConfig object not found. waiting for creation.")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get default SriovOperatorConfig object")
		return reconcile.Result{}, err
	}

	snolog.SetLogLevel(defaultConfig.Spec.LogLevel)

	r.FeatureGate.Init(defaultConfig.Spec.FeatureGates)
	logger.Info("enabled featureGates", "featureGates", r.FeatureGate.String())

	if !defaultConfig.Spec.EnableInjector {
		logger.Info("SR-IOV Network Resource Injector is disabled.")
	}

	if !defaultConfig.Spec.EnableOperatorWebhook {
		logger.Info("SR-IOV Network Operator Webhook is disabled.")
	}

	// Fetch the SriovNetworkNodePolicyList
	policyList := &sriovnetworkv1.SriovNetworkNodePolicyList{}
	err = r.List(ctx, policyList, &client.ListOptions{})
	if err != nil {
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// Render and sync webhook objects
	if err = r.syncWebhookObjs(ctx, defaultConfig); err != nil {
		return reconcile.Result{}, err
	}

	// Sync SriovNetworkConfigDaemon objects
	if err = r.syncConfigDaemonSet(ctx, defaultConfig); err != nil {
		return reconcile.Result{}, err
	}

	if err = syncPluginDaemonObjs(ctx, r.Client, r.Scheme, defaultConfig, policyList); err != nil {
		return reconcile.Result{}, err
	}

	// For Openshift we need to create the systemd files using a machine config
	if vars.ClusterType == consts.ClusterTypeOpenshift {
		// TODO: add support for hypershift as today there is no MCO on hypershift clusters
		if r.PlatformHelper.IsHypershift() {
			return ctrl.Result{}, fmt.Errorf("systemd mode is not supported on hypershift")
		}

		if err = r.syncOpenShiftSystemdService(ctx, defaultConfig); err != nil {
			return reconcile.Result{}, err
		}
	}

	logger.Info("Reconcile SriovOperatorConfig completed successfully")
	return reconcile.Result{RequeueAfter: consts.ResyncPeriod}, nil
}

// defaultConfigPredicate creates a predicate.Predicate that will return true
// only for the default sriovoperatorconfig obj.
func defaultConfigPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(object client.Object) bool {
		if object.GetName() == consts.DefaultConfigName && object.GetNamespace() == vars.Namespace {
			return true
		}
		return false
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *SriovOperatorConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovnetworkv1.SriovOperatorConfig{}, ctrl_builder.WithPredicates(defaultConfigPredicate())).
		Owns(&appsv1.DaemonSet{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

func (r *SriovOperatorConfigReconciler) syncConfigDaemonSet(ctx context.Context, dc *sriovnetworkv1.SriovOperatorConfig) error {
	logger := log.Log.WithName("syncConfigDaemonset")
	logger.V(1).Info("Start to sync config daemonset")

	data := render.MakeRenderData()
	data.Data["Image"] = os.Getenv("SRIOV_NETWORK_CONFIG_DAEMON_IMAGE")
	data.Data["Namespace"] = vars.Namespace
	data.Data["SRIOVCNIImage"] = os.Getenv("SRIOV_CNI_IMAGE")
	data.Data["SRIOVInfiniBandCNIImage"] = os.Getenv("SRIOV_INFINIBAND_CNI_IMAGE")
	data.Data["OVSCNIImage"] = os.Getenv("OVS_CNI_IMAGE")
	data.Data["ReleaseVersion"] = os.Getenv("RELEASEVERSION")
	data.Data["ClusterType"] = vars.ClusterType
	data.Data["DevMode"] = os.Getenv("DEV_MODE")
	data.Data["ImagePullSecrets"] = GetImagePullSecrets()
	if dc.Spec.ConfigurationMode == sriovnetworkv1.SystemdConfigurationMode {
		data.Data["UsedSystemdMode"] = true
	} else {
		data.Data["UsedSystemdMode"] = false
	}
	data.Data["ParallelNicConfig"] = r.FeatureGate.IsEnabled(consts.ParallelNicConfigFeatureGate)
	data.Data["ManageSoftwareBridges"] = r.FeatureGate.IsEnabled(consts.ManageSoftwareBridgesFeatureGate)

	envCniBinPath := os.Getenv("SRIOV_CNI_BIN_PATH")
	if envCniBinPath == "" {
		data.Data["CNIBinPath"] = "/var/lib/cni/bin"
	} else {
		logger.V(1).Info("New cni bin found", "CNIBinPath", envCniBinPath)
		data.Data["CNIBinPath"] = envCniBinPath
	}

	if len(dc.Spec.DisablePlugins) > 0 {
		logger.V(1).Info("DisablePlugins provided", "DisablePlugins", dc.Spec.DisablePlugins)
		data.Data["DisablePlugins"] = strings.Join(dc.Spec.DisablePlugins.ToStringSlice(), ",")
	}

	objs, err := render.RenderDir(consts.ConfigDaemonPath, &data)
	if err != nil {
		logger.Error(err, "Fail to render config daemon manifests")
		return err
	}
	// Sync DaemonSets
	for _, obj := range objs {
		if obj.GetKind() == "DaemonSet" && len(dc.Spec.ConfigDaemonNodeSelector) > 0 {
			scheme := kscheme.Scheme
			ds := &appsv1.DaemonSet{}
			err = scheme.Convert(obj, ds, nil)
			if err != nil {
				logger.Error(err, "Fail to convert to DaemonSet")
				return err
			}
			ds.Spec.Template.Spec.NodeSelector = dc.Spec.ConfigDaemonNodeSelector
			err = scheme.Convert(ds, obj, nil)
			if err != nil {
				logger.Error(err, "Fail to convert to Unstructured")
				return err
			}
		}
		err = r.syncK8sResource(ctx, dc, obj)
		if err != nil {
			logger.Error(err, "Couldn't sync SR-IoV daemons objects")
			return err
		}
	}
	return nil
}

func (r *SriovOperatorConfigReconciler) syncWebhookObjs(ctx context.Context, dc *sriovnetworkv1.SriovOperatorConfig) error {
	logger := log.Log.WithName("syncWebhookObjs")
	logger.V(1).Info("Start to sync webhook objects")

	for name, path := range webhooks {
		// Render Webhook manifests
		data := render.MakeRenderData()
		data.Data["Namespace"] = vars.Namespace
		data.Data["SRIOVMutatingWebhookName"] = name
		data.Data["NetworkResourcesInjectorImage"] = os.Getenv("NETWORK_RESOURCES_INJECTOR_IMAGE")
		data.Data["SriovNetworkWebhookImage"] = os.Getenv("SRIOV_NETWORK_WEBHOOK_IMAGE")
		data.Data["ReleaseVersion"] = os.Getenv("RELEASEVERSION")
		data.Data["ClusterType"] = vars.ClusterType
		data.Data["DevMode"] = os.Getenv("DEV_MODE")
		data.Data["ImagePullSecrets"] = GetImagePullSecrets()
		data.Data["CertManagerEnabled"] = strings.ToLower(os.Getenv("ADMISSION_CONTROLLERS_CERTIFICATES_CERT_MANAGER_ENABLED")) == trueString
		data.Data["OperatorWebhookSecretName"] = os.Getenv("ADMISSION_CONTROLLERS_CERTIFICATES_OPERATOR_SECRET_NAME")
		data.Data["OperatorWebhookCA"] = os.Getenv("ADMISSION_CONTROLLERS_CERTIFICATES_OPERATOR_CA_CRT")
		data.Data["InjectorWebhookSecretName"] = os.Getenv("ADMISSION_CONTROLLERS_CERTIFICATES_INJECTOR_SECRET_NAME")
		data.Data["InjectorWebhookCA"] = os.Getenv("ADMISSION_CONTROLLERS_CERTIFICATES_INJECTOR_CA_CRT")

		data.Data["ExternalControlPlane"] = false
		if r.PlatformHelper.IsOpenshiftCluster() {
			external := r.PlatformHelper.IsHypershift()
			data.Data["ExternalControlPlane"] = external
		}

		// check for ResourceInjectorMatchConditionFeatureGate feature gate
		data.Data[consts.ResourceInjectorMatchConditionFeatureGate] = r.FeatureGate.IsEnabled(consts.ResourceInjectorMatchConditionFeatureGate)

		objs, err := render.RenderDir(path, &data)
		if err != nil {
			logger.Error(err, "Fail to render webhook manifests")
			return err
		}

		// Delete injector webhook
		if !dc.Spec.EnableInjector && path == consts.InjectorWebHookPath {
			for _, obj := range objs {
				err = r.deleteWebhookObject(ctx, obj)
				if err != nil {
					return err
				}
			}
			logger.Info("SR-IOV Admission Controller is disabled.")
			logger.Info("To enable SR-IOV Admission Controller,")
			logger.Info("Set 'SriovOperatorConfig.Spec.EnableInjector' to true(bool).")
			continue
		}
		// Delete operator webhook
		if !dc.Spec.EnableOperatorWebhook && path == consts.OperatorWebHookPath {
			for _, obj := range objs {
				err = r.deleteWebhookObject(ctx, obj)
				if err != nil {
					return err
				}
			}
			logger.Info("Operator Admission Controller is disabled.")
			logger.Info("To enable Operator Admission Controller,")
			logger.Info("Set 'SriovOperatorConfig.Spec.EnableOperatorWebhook' to true(bool).")
			continue
		}

		// Sync Webhook
		for _, obj := range objs {
			err = r.syncK8sResource(ctx, dc, obj)
			if err != nil {
				logger.Error(err, "Couldn't sync webhook objects")
				return err
			}
		}
	}

	return nil
}

func (r *SriovOperatorConfigReconciler) deleteWebhookObject(ctx context.Context, obj *uns.Unstructured) error {
	if err := r.deleteK8sResource(ctx, obj); err != nil {
		return err
	}
	return nil
}

func (r *SriovOperatorConfigReconciler) deleteK8sResource(ctx context.Context, in *uns.Unstructured) error {
	if err := apply.DeleteObject(ctx, r.Client, in); err != nil {
		return fmt.Errorf("failed to delete object %v with err: %v", in, err)
	}
	return nil
}

func (r *SriovOperatorConfigReconciler) syncK8sResource(ctx context.Context, cr *sriovnetworkv1.SriovOperatorConfig, in *uns.Unstructured) error {
	switch in.GetKind() {
	case clusterRoleResourceName, clusterRoleBindingResourceName, mutatingWebhookConfigurationCRDName, validatingWebhookConfigurationCRDName, machineConfigCRDName:
	default:
		// set owner-reference only for namespaced objects
		if err := controllerutil.SetControllerReference(cr, in, r.Scheme); err != nil {
			return err
		}
	}
	if err := apply.ApplyObject(ctx, r.Client, in); err != nil {
		return fmt.Errorf("failed to apply object %v with err: %v", in, err)
	}
	return nil
}

// syncOpenShiftSystemdService creates the Machine Config to deploy the systemd service on openshift ONLY
func (r *SriovOperatorConfigReconciler) syncOpenShiftSystemdService(ctx context.Context, cr *sriovnetworkv1.SriovOperatorConfig) error {
	logger := log.Log.WithName("syncSystemdService")

	if cr.Spec.ConfigurationMode != sriovnetworkv1.SystemdConfigurationMode {
		obj := &machinev1.MachineConfig{}
		err := r.Get(context.TODO(), types.NamespacedName{Name: consts.SystemdServiceOcpMachineConfigName}, obj)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}

			logger.Error(err, "failed to get machine config for the sriov-systemd-service")
			return err
		}

		logger.Info("Systemd service was deployed but the operator is now operating on daemonset mode, removing the machine config")
		err = r.Delete(context.TODO(), obj)
		if err != nil {
			logger.Error(err, "failed to remove the systemd service machine config")
			return err
		}

		return nil
	}

	logger.Info("Start to sync config systemd machine config for openshift")
	data := render.MakeRenderData()
	data.Data["LogLevel"] = cr.Spec.LogLevel
	objs, err := render.RenderDir(consts.SystemdServiceOcpPath, &data)
	if err != nil {
		logger.Error(err, "Fail to render config daemon manifests")
		return err
	}

	// Sync machine config
	return r.setLabelInsideObject(ctx, cr, objs)
}

func (r SriovOperatorConfigReconciler) setLabelInsideObject(ctx context.Context, cr *sriovnetworkv1.SriovOperatorConfig, objs []*uns.Unstructured) error {
	logger := log.Log.WithName("setLabelInsideObject")
	for _, obj := range objs {
		if obj.GetKind() == machineConfigCRDName && len(cr.Spec.ConfigDaemonNodeSelector) > 0 {
			scheme := kscheme.Scheme
			mc := &machinev1.ControllerConfig{}
			err := scheme.Convert(obj, mc, nil)
			if err != nil {
				logger.Error(err, "Fail to convert to MachineConfig")
				return err
			}
			mc.Labels = cr.Spec.ConfigDaemonNodeSelector
			err = scheme.Convert(mc, obj, nil)
			if err != nil {
				logger.Error(err, "Fail to convert to Unstructured")
				return err
			}
		}
		err := r.syncK8sResource(ctx, cr, obj)
		if err != nil {
			logger.Error(err, "Couldn't sync SR-IoV daemons objects")
			return err
		}
	}

	return nil
}
