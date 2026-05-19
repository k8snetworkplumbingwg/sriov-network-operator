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
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	dptypes "github.com/k8snetworkplumbingwg/sriov-network-device-plugin/pkg/types"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

const nodePolicySyncEventName = "node-policy-sync-event"

// SriovNetworkNodePolicyReconciler reconciles a SriovNetworkNodePolicy object
type SriovNetworkNodePolicyReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	FeatureGate featuregate.FeatureGate
}

//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworknodepolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworknodepolicies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworknodepolicies/finalizers,verbs=update
//+kubebuilder:rbac:groups=resource.k8s.io,resources=deviceclasses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=sriovnetwork.k8snetworkplumbingwg.io,resources=sriovresourcepolicies;deviceattributes,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the SriovNetworkNodePolicy object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *SriovNetworkNodePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Only handle node-policy-sync-event
	if req.Name != nodePolicySyncEventName || req.Namespace != "" {
		return reconcile.Result{}, nil
	}

	reqLogger := log.FromContext(ctx)
	reqLogger.Info("Reconciling")

	// Fetch the default SriovOperatorConfig
	defaultOpConf := &sriovnetworkv1.SriovOperatorConfig{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: vars.Namespace, Name: constants.DefaultConfigName}, defaultOpConf); err != nil {
		if errors.IsNotFound(err) {
			reqLogger.Info("default SriovOperatorConfig object not found, cannot reconcile SriovNetworkNodePolicies. Requeue.")
			return reconcile.Result{RequeueAfter: constants.DrainControllerRequeueTime}, nil
		}
		return reconcile.Result{}, err
	}

	// Fetch the SriovNetworkNodePolicyList
	policyList := &sriovnetworkv1.SriovNetworkNodePolicyList{}
	err := r.List(ctx, policyList, &client.ListOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}
	// Fetch the Nodes
	nodeList := &corev1.NodeList{}
	lo := &client.MatchingLabels{
		"node-role.kubernetes.io/worker": "",
		"kubernetes.io/os":               "linux",
	}
	if len(defaultOpConf.Spec.ConfigDaemonNodeSelector) > 0 {
		labels := client.MatchingLabels(defaultOpConf.Spec.ConfigDaemonNodeSelector)
		lo = &labels
	}
	err = r.List(ctx, nodeList, lo)
	if err != nil {
		// Error reading the object - requeue the request.
		reqLogger.Error(err, "Fail to list nodes")
		return reconcile.Result{}, err
	}

	// Sort the policies with priority, higher priority ones is applied later
	// We need to use the sort so we always get the policies in the same order
	// That is needed so when we create the node Affinity for the sriov-device plugin
	// it will remain in the same order and not trigger a pod recreation
	sort.Sort(sriovnetworkv1.ByPriority(policyList.Items))
	// Sync SriovNetworkNodeState objects
	if err = r.syncAllSriovNetworkNodeStates(ctx, defaultOpConf, policyList, nodeList); err != nil {
		return reconcile.Result{}, err
	}

	// Sync either device plugin ConfigMap or DRA resources (DeviceAttributes + SriovResourcePolicy) based on feature gate
	if r.FeatureGate.IsEnabled(constants.DynamicResourceAllocationFeatureGate) {
		reqLogger.Info("DRA feature gate enabled, syncing DeviceAttributes and SriovResourcePolicy CRs")
		if err = r.syncDeviceAttributes(ctx, defaultOpConf, policyList); err != nil {
			return reconcile.Result{}, err
		}
		if err = r.syncSriovResourcePolicies(ctx, defaultOpConf, policyList, nodeList); err != nil {
			return reconcile.Result{}, err
		}
		if err = r.syncExtendedResourceDeviceClasses(ctx, defaultOpConf, policyList); err != nil {
			return reconcile.Result{}, err
		}
	} else {
		if err = r.cleanupExtendedResourceDeviceClasses(ctx); err != nil {
			return reconcile.Result{}, err
		}
		if err = r.cleanupSriovResourcePoliciesAndDeviceAttributes(ctx); err != nil {
			return reconcile.Result{}, err
		}
		// Sync Sriov device plugin ConfigMap object
		if err = r.syncDevicePluginConfigMap(ctx, defaultOpConf, policyList, nodeList); err != nil {
			return reconcile.Result{}, err
		}
	}

	// All was successful. Request that this be re-triggered after ResyncPeriod,
	// so we can reconcile state again.
	return reconcile.Result{RequeueAfter: constants.ResyncPeriod}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SriovNetworkNodePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	qHandler := func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: "",
			Name:      nodePolicySyncEventName,
		}}, time.Second)
	}

	delayedEventHandler := handler.Funcs{
		CreateFunc: func(c context.Context, e event.TypedCreateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.WithName("SriovNetworkNodePolicy").
				Info("Enqueuing sync for create event", "resource", e.Object.GetName(), "type", e.Object.GetObjectKind().GroupVersionKind().String())
			qHandler(w)
		},
		UpdateFunc: func(c context.Context, e event.TypedUpdateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.WithName("SriovNetworkNodePolicy").
				Info("Enqueuing sync for update event", "resource", e.ObjectNew.GetName(), "type", e.ObjectNew.GetObjectKind().GroupVersionKind().String())
			qHandler(w)
		},
		DeleteFunc: func(c context.Context, e event.TypedDeleteEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.WithName("SriovNetworkNodePolicy").
				Info("Enqueuing sync for delete event", "resource", e.Object.GetName(), "type", e.Object.GetObjectKind().GroupVersionKind().String())
			qHandler(w)
		},
		GenericFunc: func(c context.Context, e event.TypedGenericEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.WithName("SriovNetworkNodePolicy").
				Info("Enqueuing sync for generic event", "resource", e.Object.GetName(), "type", e.Object.GetObjectKind().GroupVersionKind().String())
			qHandler(w)
		},
	}

	// we want to act fast on new or deleted nodes
	nodeEvenHandler := handler.Funcs{
		CreateFunc: func(c context.Context, e event.TypedCreateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.WithName("SriovNetworkNodePolicy").
				Info("Enqueuing sync for create event", "resource", e.Object.GetName(), "type", e.Object.GetObjectKind().GroupVersionKind().String())
			qHandler(w)
		},
		UpdateFunc: func(c context.Context, e event.TypedUpdateEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if equality.Semantic.DeepEqual(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels()) {
				return
			}
			log.Log.WithName("SriovNetworkNodePolicy").
				Info("Enqueuing sync for create event", "resource", e.ObjectNew.GetName(), "type", e.ObjectNew.GetObjectKind().GroupVersionKind().String())
			qHandler(w)
		},
		DeleteFunc: func(c context.Context, e event.TypedDeleteEvent[client.Object], w workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.WithName("SriovNetworkNodePolicy").
				Info("Enqueuing sync for delete event", "resource", e.Object.GetName(), "type", e.Object.GetObjectKind().GroupVersionKind().String())
			qHandler(w)
		},
	}

	// send initial sync event to trigger reconcile when controller is started
	var eventChan = make(chan event.GenericEvent, 1)
	eventChan <- event.GenericEvent{Object: &sriovnetworkv1.SriovNetworkNodePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: nodePolicySyncEventName, Namespace: ""}}}
	close(eventChan)

	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovnetworkv1.SriovNetworkNodePolicy{}).
		Watches(&corev1.Node{}, nodeEvenHandler).
		Watches(&sriovnetworkv1.SriovNetworkNodePolicy{}, delayedEventHandler).
		Watches(&sriovnetworkv1.SriovNetworkPoolConfig{}, delayedEventHandler).
		WatchesRawSource(source.Channel(eventChan, &handler.EnqueueRequestForObject{})).
		Complete(r)
}

func (r *SriovNetworkNodePolicyReconciler) syncDevicePluginConfigMap(ctx context.Context, dc *sriovnetworkv1.SriovOperatorConfig,
	pl *sriovnetworkv1.SriovNetworkNodePolicyList, nl *corev1.NodeList) error {
	logger := log.Log.WithName("syncDevicePluginConfigMap")
	logger.V(1).Info("Start to sync device plugin ConfigMap")

	configData := make(map[string]string)
	for _, node := range nl.Items {
		data, err := r.renderDevicePluginConfigData(ctx, pl, &node)
		if err != nil {
			return err
		}
		config, err := json.Marshal(data)
		if err != nil {
			return err
		}
		configData[node.Name] = string(config)

		if len(data.ResourceList) == 0 {
			// if we don't have policies we should add the disabled label for the device plugin
			err = utils.LabelNode(ctx, node.Name, constants.SriovDevicePluginLabel, constants.SriovDevicePluginLabelDisabled, r.Client)
			if err != nil {
				logger.Error(err, "failed to label node for device plugin label",
					"labelKey",
					constants.SriovDevicePluginLabel,
					"labelValue",
					constants.SriovDevicePluginLabelDisabled)
				return err
			}
		} else {
			// if we have policies we should add the enabled label for the device plugin
			err = utils.LabelNode(ctx, node.Name, constants.SriovDevicePluginLabel, constants.SriovDevicePluginLabelEnabled, r.Client)
			if err != nil {
				logger.Error(err, "failed to label node for device plugin label",
					"labelKey",
					constants.SriovDevicePluginLabel,
					"labelValue",
					constants.SriovDevicePluginLabelEnabled)
				return err
			}
		}
	}

	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.ConfigMapName,
			Namespace: vars.Namespace,
		},
		Data: configData,
	}

	if err := controllerutil.SetControllerReference(dc, cm, r.Scheme); err != nil {
		return err
	}

	found := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name}, found)
	if err != nil {
		if errors.IsNotFound(err) {
			err = r.Create(ctx, cm)
			if err != nil {
				return fmt.Errorf("couldn't create ConfigMap: %v", err)
			}
			logger.V(1).Info("Created ConfigMap for", cm.Namespace, cm.Name)
		} else {
			return fmt.Errorf("failed to get ConfigMap: %v", err)
		}
	} else {
		logger.V(1).Info("ConfigMap already exists, updating")
		err = r.Update(ctx, cm)
		if err != nil {
			return fmt.Errorf("couldn't update ConfigMap: %v", err)
		}
	}
	return nil
}

func (r *SriovNetworkNodePolicyReconciler) syncAllSriovNetworkNodeStates(ctx context.Context, dc *sriovnetworkv1.SriovOperatorConfig, npl *sriovnetworkv1.SriovNetworkNodePolicyList, nl *corev1.NodeList) error {
	logger := log.Log.WithName("syncAllSriovNetworkNodeStates")
	logger.V(1).Info("Start to sync all SriovNetworkNodeState custom resource")
	found := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: vars.Namespace, Name: constants.ConfigMapName}, found); err != nil {
		logger.V(1).Info("Fail to get", "ConfigMap", constants.ConfigMapName)
	}
	for _, node := range nl.Items {
		logger.V(1).Info("Sync SriovNetworkNodeState CR", "name", node.Name)
		ns := &sriovnetworkv1.SriovNetworkNodeState{}
		ns.Name = node.Name
		ns.Namespace = vars.Namespace
		netPoolConfig, _, err := findNodePoolConfig(ctx, &node, r.Client)
		if err != nil {
			logger.Error(err, "failed to get SriovNetworkPoolConfig for the current node")
		}
		if netPoolConfig != nil {
			ns.Spec.System.RdmaMode = netPoolConfig.Spec.RdmaMode
		}
		j, _ := json.Marshal(ns)
		logger.V(2).Info("SriovNetworkNodeState CR", "content", j)
		if err := r.syncSriovNetworkNodeState(ctx, dc, npl, ns, &node); err != nil {
			logger.Error(err, "Fail to sync", "SriovNetworkNodeState", ns.Name)
			return err
		}
	}

	logger.V(1).Info("Remove SriovNetworkNodeState custom resource for unselected node")
	nsList := &sriovnetworkv1.SriovNetworkNodeStateList{}
	err := r.List(ctx, nsList, &client.ListOptions{})
	if err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "Fail to list SriovNetworkNodeState CRs")
			return err
		}
	} else {
		for _, ns := range nsList.Items {
			found := false
			for _, node := range nl.Items {
				if ns.GetName() == node.GetName() {
					found = true
					break
				}
			}
			if !found {
				// remove device plugin labels if the node doesn't exist we continue to handle the stale nodeState
				logger.Info("removing device plugin label from node as SriovNetworkNodeState doesn't exist", "nodeStateName", ns.Name)
				err = utils.RemoveLabelFromNode(ctx, ns.Name, constants.SriovDevicePluginLabel, r.Client)
				if err != nil && !errors.IsNotFound(err) {
					logger.Error(err, "Fail to remove device plugin label from node", "node", ns.Name)
					return err
				}
				if err := r.handleStaleNodeState(ctx, &ns); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// handleStaleNodeState handles stale SriovNetworkNodeState CR (the CR which no longer have a corresponding node with the daemon).
// If the CR has the "keep until time" annotation, indicating the earliest time the state object can be removed,
// this function will compare it to the current time to determine if deletion is permissible and do deletion if allowed.
// If the annotation is absent, the function will create one with a timestamp in future, using either the default or a configured offset.
// If STALE_NODE_STATE_CLEANUP_DELAY_MINUTES env variable is set to 0, removes the CR immediately
func (r *SriovNetworkNodePolicyReconciler) handleStaleNodeState(ctx context.Context, ns *sriovnetworkv1.SriovNetworkNodeState) error {
	logger := log.Log.WithName("handleStaleNodeState")

	var delayMinutes int
	var err error

	envValue, found := os.LookupEnv("STALE_NODE_STATE_CLEANUP_DELAY_MINUTES")
	if found {
		delayMinutes, err = strconv.Atoi(envValue)
		if err != nil || delayMinutes < 0 {
			delayMinutes = constants.DefaultNodeStateCleanupDelayMinutes
			logger.Error(err, "invalid value in STALE_NODE_STATE_CLEANUP_DELAY_MINUTES env variable, use default delay",
				"delay", delayMinutes)
		}
	} else {
		delayMinutes = constants.DefaultNodeStateCleanupDelayMinutes
	}

	if delayMinutes != 0 {
		now := time.Now().UTC()
		keepUntilTime := ns.GetKeepUntilTime()
		if keepUntilTime.IsZero() {
			keepUntilTime = now.Add(time.Minute * time.Duration(delayMinutes))
			logger.V(2).Info("SriovNetworkNodeState has no matching node, configure cleanup delay for the state object",
				"nodeStateName", ns.Name, "delay", delayMinutes, "keepUntilTime", keepUntilTime.String())
			ns.SetKeepUntilTime(keepUntilTime)
			if err := r.Update(ctx, ns); err != nil {
				logger.Error(err, "Fail to update SriovNetworkNodeState CR", "name", ns.GetName())
				return err
			}
			return nil
		}
		if now.Before(keepUntilTime) {
			return nil
		}
	}
	// remove the object if delayMinutes is 0 or if keepUntilTime is already passed
	logger.Info("Deleting SriovNetworkNodeState as node with that name doesn't exist", "nodeStateName", ns.Name)
	if err := r.Delete(ctx, ns, &client.DeleteOptions{}); err != nil {
		logger.Error(err, "Fail to delete SriovNetworkNodeState CR", "name", ns.GetName())
		return err
	}
	return nil
}

func (r *SriovNetworkNodePolicyReconciler) syncSriovNetworkNodeState(ctx context.Context,
	dc *sriovnetworkv1.SriovOperatorConfig,
	npl *sriovnetworkv1.SriovNetworkNodePolicyList,
	ns *sriovnetworkv1.SriovNetworkNodeState,
	node *corev1.Node) error {
	logger := log.Log.WithName("syncSriovNetworkNodeState")
	logger.V(1).Info("Start to sync SriovNetworkNodeState", "Name", ns.Name)

	if err := controllerutil.SetControllerReference(dc, ns, r.Scheme); err != nil {
		return err
	}
	found := &sriovnetworkv1.SriovNetworkNodeState{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ns.Namespace, Name: ns.Name}, found)
	if err != nil {
		logger.Error(err, "Fail to get SriovNetworkNodeState", "namespace", ns.Namespace, "name", ns.Name)
		if errors.IsNotFound(err) {
			err = r.Create(ctx, ns)
			if err != nil {
				return fmt.Errorf("couldn't create SriovNetworkNodeState: %v", err)
			}
			logger.Info("Created SriovNetworkNodeState for", ns.Namespace, ns.Name)
		} else {
			return fmt.Errorf("failed to get SriovNetworkNodeState: %v", err)
		}
	} else {
		keepUntilAnnotationUpdated := found.ResetKeepUntilTime()

		if len(found.Status.Interfaces) == 0 {
			logger.Info("SriovNetworkNodeState Status Interfaces are empty. Skip update of policies in spec",
				"namespace", ns.Namespace, "name", ns.Name)
			if keepUntilAnnotationUpdated {
				if err := r.Update(ctx, found); err != nil {
					return fmt.Errorf("couldn't update SriovNetworkNodeState: %v", err)
				}
			}
			return nil
		}

		logger.V(1).Info("SriovNetworkNodeState already exists, updating")
		newVersion := found.DeepCopy()
		newVersion.Spec = ns.Spec
		newVersion.OwnerReferences = ns.OwnerReferences

		// Previous Policy Priority(ppp) records the priority of previous evaluated policy in node policy list.
		// Since node policy list is already sorted with priority number, comparing current priority with ppp shall
		// be sufficient.
		// ppp is set to 100 as initial value to avoid matching with the first policy in policy list, although
		// it should not matter since the flag used in p.Apply() will only be applied when VF partition is detected.
		ppp := 100
		for _, p := range npl.Items {
			// Note(adrianc): default policy is deprecated and ignored.
			if p.Name == constants.DefaultPolicyName {
				continue
			}
			if p.Selected(node) {
				logger.Info("apply", "policy", p.Name, "node", node.Name)
				// Merging only for policies with the same priority (ppp == p.Spec.Priority)
				// This boolean flag controls merging of PF configuration (e.g. mtu, numvfs etc)
				// when VF partition is configured.
				err = p.Apply(newVersion, ppp == p.Spec.Priority)
				if err != nil {
					return err
				}
				if r.FeatureGate.IsEnabled(constants.ManageSoftwareBridgesFeatureGate) {
					err = p.ApplyBridgeConfig(newVersion)
					if err != nil {
						return err
					}
				}
				// record the evaluated policy priority for next loop
				ppp = p.Spec.Priority
			}
		}

		// Note(adrianc): we check same ownerReferences since SriovNetworkNodeState
		// was owned by a default SriovNetworkNodePolicy. if we encounter a descripancy
		// we need to update.
		if !keepUntilAnnotationUpdated && equality.Semantic.DeepEqual(newVersion.OwnerReferences, found.OwnerReferences) &&
			equality.Semantic.DeepEqual(newVersion.Spec, found.Spec) {
			logger.V(1).Info("SriovNetworkNodeState did not change, not updating")
			return nil
		}
		err = r.Update(ctx, newVersion)
		if err != nil {
			return fmt.Errorf("couldn't update SriovNetworkNodeState: %v", err)
		}
	}
	return nil
}

func (r *SriovNetworkNodePolicyReconciler) renderDevicePluginConfigData(ctx context.Context, pl *sriovnetworkv1.SriovNetworkNodePolicyList, node *corev1.Node) (dptypes.ResourceConfList, error) {
	logger := log.Log.WithName("renderDevicePluginConfigData")
	logger.V(1).Info("Start to render device plugin config data", "node", node.Name)
	rcl := dptypes.ResourceConfList{}
	for _, p := range pl.Items {
		// Note(adrianc): default policy is deprecated and ignored.
		if p.Name == constants.DefaultPolicyName {
			continue
		}

		// render node specific data for device plugin config
		if !p.Selected(node) {
			continue
		}

		nodeState := &sriovnetworkv1.SriovNetworkNodeState{}
		err := r.Get(ctx, types.NamespacedName{Namespace: vars.Namespace, Name: node.Name}, nodeState)
		if err != nil {
			return rcl, err
		}

		found, i := resourceNameInList(p.Spec.ResourceName, &rcl)
		if found {
			err := updateDevicePluginResource(&rcl.ResourceList[i], &p, nodeState)
			if err != nil {
				return rcl, err
			}
			logger.V(1).Info("Update resource", "Resource", rcl.ResourceList[i])
		} else {
			rc, err := createDevicePluginResource(&p, nodeState)
			if err != nil {
				return rcl, err
			}
			rcl.ResourceList = append(rcl.ResourceList, *rc)
			logger.V(1).Info("Add resource", "Resource", *rc)
		}
	}
	return rcl, nil
}

func resourceNameInList(name string, rcl *dptypes.ResourceConfList) (bool, int) {
	for i, rc := range rcl.ResourceList {
		if rc.ResourceName == name {
			return true, i
		}
	}
	return false, 0
}

// resolvePfNames resolves alternative interface names to actual interface names
// using the provided node state and returns the resolved names as a slice.
// If a pfName contains a VF range suffix (e.g., "ens0#0-9"), it resolves the
// interface name part and re-appends the range suffix to the resolved name.
func resolvePfNames(pfNames []string, nodeState *sriovnetworkv1.SriovNetworkNodeState) []string {
	resolvedPfNames := make([]string, 0, len(pfNames))
	for _, pfName := range pfNames {
		var rangeSuffix string
		nameToResolve := pfName

		// Check if pfName contains a VF range suffix (e.g., "ens0#0-9")
		if strings.Contains(pfName, "#") {
			parts := strings.SplitN(pfName, "#", 2)
			nameToResolve = parts[0]
			rangeSuffix = "#" + parts[1]
		}

		// Resolve the interface name part
		actualName := sriovnetworkv1.ResolveInterfaceName(nameToResolve, nodeState)

		// Append the range suffix back if it existed
		resolvedPfNames = append(resolvedPfNames, actualName+rangeSuffix)
	}
	return resolvedPfNames
}

func createDevicePluginResource(
	p *sriovnetworkv1.SriovNetworkNodePolicy,
	nodeState *sriovnetworkv1.SriovNetworkNodeState) (*dptypes.ResourceConfig, error) {
	netDeviceSelectors := dptypes.NetDeviceSelectors{}

	rc := &dptypes.ResourceConfig{
		ResourceName: p.Spec.ResourceName,
	}
	netDeviceSelectors.IsRdma = p.Spec.IsRdma
	netDeviceSelectors.NeedVhostNet = p.Spec.NeedVhostNet
	netDeviceSelectors.VdpaType = dptypes.VdpaType(p.Spec.VdpaType)

	if p.Spec.NicSelector.Vendor != "" {
		netDeviceSelectors.Vendors = append(netDeviceSelectors.Vendors, p.Spec.NicSelector.Vendor)
	}
	if p.Spec.NicSelector.DeviceID != "" {
		var deviceID string
		if p.Spec.NumVfs == 0 {
			deviceID = p.Spec.NicSelector.DeviceID
		} else {
			deviceID = sriovnetworkv1.GetVfDeviceID(p.Spec.NicSelector.DeviceID)
		}

		if !sriovnetworkv1.StringInArray(deviceID, netDeviceSelectors.Devices) && deviceID != "" {
			netDeviceSelectors.Devices = append(netDeviceSelectors.Devices, deviceID)
		}
	}
	if len(p.Spec.NicSelector.PfNames) > 0 {
		resolvedPfNames := resolvePfNames(p.Spec.NicSelector.PfNames, nodeState)
		netDeviceSelectors.PfNames = sriovnetworkv1.UniqueAppend(netDeviceSelectors.PfNames, resolvedPfNames...)
	}
	// vfio-pci device link type is not detectable
	if p.Spec.DeviceType != constants.DeviceTypeVfioPci {
		if p.Spec.LinkType != "" {
			linkType := constants.LinkTypeEthernet
			if strings.EqualFold(p.Spec.LinkType, constants.LinkTypeIB) {
				linkType = constants.LinkTypeInfiniband
			}
			netDeviceSelectors.LinkTypes = sriovnetworkv1.UniqueAppend(netDeviceSelectors.LinkTypes, linkType)
		}
	}
	if len(p.Spec.NicSelector.RootDevices) > 0 {
		netDeviceSelectors.RootDevices = append(netDeviceSelectors.RootDevices, p.Spec.NicSelector.RootDevices...)
	}

	// Enable the selection of devices using NetFilter
	if p.Spec.NicSelector.NetFilter != "" {
		// Loop through interfaces status to find a match for NetworkID or NetworkTag
		if len(nodeState.Status.Interfaces) == 0 {
			return nil, fmt.Errorf("node state %s doesn't contain interfaces data", nodeState.Name)
		}
		for _, intf := range nodeState.Status.Interfaces {
			if sriovnetworkv1.NetFilterMatch(p.Spec.NicSelector.NetFilter, intf.NetFilter) {
				// Found a match add the Interfaces PciAddress
				netDeviceSelectors.PciAddresses = sriovnetworkv1.UniqueAppend(netDeviceSelectors.PciAddresses, intf.PciAddress)
			}
		}
	}

	netDeviceSelectorsMarshal, err := json.Marshal(netDeviceSelectors)
	if err != nil {
		return nil, err
	}
	rawNetDeviceSelectors := json.RawMessage(netDeviceSelectorsMarshal)
	rc.Selectors = &rawNetDeviceSelectors

	rc.ExcludeTopology = p.Spec.ExcludeTopology

	return rc, nil
}

func updateDevicePluginResource(
	rc *dptypes.ResourceConfig,
	p *sriovnetworkv1.SriovNetworkNodePolicy,
	nodeState *sriovnetworkv1.SriovNetworkNodeState) error {
	netDeviceSelectors := dptypes.NetDeviceSelectors{}

	if err := json.Unmarshal(*rc.Selectors, &netDeviceSelectors); err != nil {
		return err
	}

	if p.Spec.NicSelector.Vendor != "" && !sriovnetworkv1.StringInArray(p.Spec.NicSelector.Vendor, netDeviceSelectors.Vendors) {
		netDeviceSelectors.Vendors = append(netDeviceSelectors.Vendors, p.Spec.NicSelector.Vendor)
	}
	if p.Spec.NicSelector.DeviceID != "" {
		var deviceID string
		if p.Spec.NumVfs == 0 {
			deviceID = p.Spec.NicSelector.DeviceID
		} else {
			deviceID = sriovnetworkv1.GetVfDeviceID(p.Spec.NicSelector.DeviceID)
		}

		if !sriovnetworkv1.StringInArray(deviceID, netDeviceSelectors.Devices) && deviceID != "" {
			netDeviceSelectors.Devices = append(netDeviceSelectors.Devices, deviceID)
		}
	}
	if len(p.Spec.NicSelector.PfNames) > 0 {
		resolvedPfNames := resolvePfNames(p.Spec.NicSelector.PfNames, nodeState)
		netDeviceSelectors.PfNames = sriovnetworkv1.UniqueAppend(netDeviceSelectors.PfNames, resolvedPfNames...)
	}
	// vfio-pci device link type is not detectable
	if p.Spec.DeviceType != constants.DeviceTypeVfioPci {
		if p.Spec.LinkType != "" {
			linkType := constants.LinkTypeEthernet
			if strings.EqualFold(p.Spec.LinkType, constants.LinkTypeIB) {
				linkType = constants.LinkTypeInfiniband
			}
			if !sriovnetworkv1.StringInArray(linkType, netDeviceSelectors.LinkTypes) {
				netDeviceSelectors.LinkTypes = sriovnetworkv1.UniqueAppend(netDeviceSelectors.LinkTypes, linkType)
			}
		}
	}
	if len(p.Spec.NicSelector.RootDevices) > 0 {
		netDeviceSelectors.RootDevices = sriovnetworkv1.UniqueAppend(netDeviceSelectors.RootDevices, p.Spec.NicSelector.RootDevices...)
	}

	// Enable the selection of devices using NetFilter
	if p.Spec.NicSelector.NetFilter != "" {
		// Loop through interfaces status to find a match for NetworkID or NetworkTag
		for _, intf := range nodeState.Status.Interfaces {
			if sriovnetworkv1.NetFilterMatch(p.Spec.NicSelector.NetFilter, intf.NetFilter) {
				// Found a match add the Interfaces PciAddress
				netDeviceSelectors.PciAddresses = sriovnetworkv1.UniqueAppend(netDeviceSelectors.PciAddresses, intf.PciAddress)
			}
		}
	}

	netDeviceSelectorsMarshal, err := json.Marshal(netDeviceSelectors)
	if err != nil {
		return err
	}
	rawNetDeviceSelectors := json.RawMessage(netDeviceSelectorsMarshal)
	rc.Selectors = &rawNetDeviceSelectors

	rc.ExcludeTopology = p.Spec.ExcludeTopology

	return nil
}

// syncDeviceAttributes creates/updates/deletes DeviceAttributes CRs for each unique resourceName from policies (DRA mode).
// Policies reference these via DeviceAttributesSelector; the driver merges attributes onto selected devices.
func (r *SriovNetworkNodePolicyReconciler) syncDeviceAttributes(ctx context.Context,
	dc *sriovnetworkv1.SriovOperatorConfig,
	pl *sriovnetworkv1.SriovNetworkNodePolicyList) error {
	logger := log.Log.WithName("syncDeviceAttributes")
	logger.V(1).Info("Start to sync DeviceAttributes CRs")

	desiredResourceNames := make(map[string]struct{})
	for i := range pl.Items {
		p := &pl.Items[i]
		if p.Name == constants.DefaultPolicyName {
			continue
		}
		if p.Spec.ResourceName != "" {
			desiredResourceNames[p.Spec.ResourceName] = struct{}{}
		}
	}

	attrList := &sriovdrav1alpha1.DeviceAttributesList{}
	if err := r.List(ctx, attrList, client.InNamespace(vars.Namespace),
		client.MatchingLabels{deviceClassGeneratedByLabel: deviceClassOperatorLabelVal}); err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("DeviceAttributes CRD not available, skipping sync")
			return nil
		}
		logger.Error(err, "Failed to list DeviceAttributes CRs")
		return err
	}

	for resourceName := range desiredResourceNames {
		deviceClassName := resourceNameToDeviceClassName(resourceName)
		name := deviceClassName + "-attrs"
		desired := buildDeviceAttributesCR(name, resourceName)
		if err := controllerutil.SetControllerReference(dc, desired, r.Scheme); err != nil {
			logger.Error(err, "Failed to set controller reference on DeviceAttributes", "name", name)
			return err
		}
		var existing *sriovdrav1alpha1.DeviceAttributes
		for i := range attrList.Items {
			if attrList.Items[i].Name == name {
				existing = &attrList.Items[i]
				break
			}
		}
		if existing != nil {
			if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
				logger.V(1).Info("Updating DeviceAttributes", "name", name)
				existing.Spec = desired.Spec
				if err := r.Update(ctx, existing); err != nil {
					logger.Error(err, "Failed to update DeviceAttributes", "name", name)
					return err
				}
			}
		} else {
			logger.V(1).Info("Creating DeviceAttributes", "name", name)
			if err := r.Create(ctx, desired); err != nil {
				logger.Error(err, "Failed to create DeviceAttributes", "name", name)
				return err
			}
		}
	}
	for i := range attrList.Items {
		item := &attrList.Items[i]
		pool := item.Labels[draResourcePoolLabel]
		// Match by resource-pool label: desired set uses resourceNameToDeviceClassName(rn) as pool
		found := false
		for resourceName := range desiredResourceNames {
			if resourceNameToDeviceClassName(resourceName) == pool {
				found = true
				break
			}
		}
		if !found {
			logger.V(1).Info("Deleting obsolete DeviceAttributes", "name", item.Name)
			if err := r.Delete(ctx, item); err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "Failed to delete DeviceAttributes", "name", item.Name)
				return err
			}
		}
	}
	return nil
}

func buildDeviceAttributesCR(name, resourceName string) *sriovdrav1alpha1.DeviceAttributes {
	deviceClassName := resourceNameToDeviceClassName(resourceName)
	// Use same extended resource name (prefix/resourceName) as device plugin for consistency
	extendedName := buildExtendedResourceName(resourceName)
	return &sriovdrav1alpha1.DeviceAttributes{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: vars.Namespace,
			Labels: map[string]string{
				deviceClassGeneratedByLabel: deviceClassOperatorLabelVal,
				draResourcePoolLabel:        deviceClassName,
			},
		},
		Spec: sriovdrav1alpha1.DeviceAttributesSpec{
			Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				resourceapi.QualifiedName(draResourceNameAttributeKey): {StringValue: &extendedName},
			},
		},
	}
}

// syncSriovResourcePolicies creates/updates SriovResourcePolicy CRs for DRA mode (one per node).
func (r *SriovNetworkNodePolicyReconciler) syncSriovResourcePolicies(ctx context.Context,
	dc *sriovnetworkv1.SriovOperatorConfig,
	pl *sriovnetworkv1.SriovNetworkNodePolicyList,
	nl *corev1.NodeList) error {
	logger := log.Log.WithName("syncSriovResourcePolicies")
	logger.V(1).Info("Start to sync SriovResourcePolicy CRs")

	desiredPolicies := make(map[string]*sriovdrav1alpha1.SriovResourcePolicy)
	for _, node := range nl.Items {
		policy, err := r.renderSriovResourcePolicyForNode(ctx, pl, &node)
		if err != nil {
			logger.Error(err, "Failed to render SriovResourcePolicy for node", "node", node.Name)
			return err
		}
		if policy != nil {
			desiredPolicies[node.Name] = policy
		}
	}

	policyList := &sriovdrav1alpha1.SriovResourcePolicyList{}
	if err := r.List(ctx, policyList, client.InNamespace(vars.Namespace),
		client.MatchingLabels{deviceClassGeneratedByLabel: deviceClassOperatorLabelVal}); err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("SriovResourcePolicy CRD not available, skipping sync")
			return nil
		}
		logger.Error(err, "Failed to list SriovResourcePolicy CRs")
		return err
	}

	for nodeName, desired := range desiredPolicies {
		found := false
		for i := range policyList.Items {
			existing := &policyList.Items[i]
			if existing.Name == desired.Name {
				found = true
				if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
					logger.V(1).Info("Updating SriovResourcePolicy", "name", desired.Name, "node", nodeName)
					existing.Spec = desired.Spec
					if err := r.Update(ctx, existing); err != nil {
						logger.Error(err, "Failed to update SriovResourcePolicy", "name", desired.Name)
						return err
					}
				}
				break
			}
		}
		if !found {
			logger.V(1).Info("Creating SriovResourcePolicy", "name", desired.Name, "node", nodeName)
			if err := controllerutil.SetControllerReference(dc, desired, r.Scheme); err != nil {
				logger.Error(err, "Failed to set controller reference", "name", desired.Name)
				return err
			}
			if err := r.Create(ctx, desired); err != nil {
				logger.Error(err, "Failed to create SriovResourcePolicy", "name", desired.Name)
				return err
			}
		}
	}

	for i := range policyList.Items {
		existing := &policyList.Items[i]
		nodeName := existing.Labels[sriovResourcePolicyNodeLabel]
		if _, exists := desiredPolicies[nodeName]; !exists {
			logger.V(1).Info("Deleting obsolete SriovResourcePolicy", "name", existing.Name, "node", nodeName)
			if err := r.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "Failed to delete SriovResourcePolicy", "name", existing.Name)
				return err
			}
		}
	}
	return nil
}

// sriovResourcePolicyNodeSelectorForHostname returns a NodeSelector that matches exactly
// one node by kubernetes.io/hostname (same intent as the former map nodeSelector).
func sriovResourcePolicyNodeSelectorForHostname(hostname string) *corev1.NodeSelector {
	return &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					{
						Key:      corev1.LabelHostname,
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{hostname},
					},
				},
			},
		},
	}
}

// renderSriovResourcePolicyForNode generates a SriovResourcePolicy CR for a specific node.
func (r *SriovNetworkNodePolicyReconciler) renderSriovResourcePolicyForNode(ctx context.Context,
	pl *sriovnetworkv1.SriovNetworkNodePolicyList,
	node *corev1.Node) (*sriovdrav1alpha1.SriovResourcePolicy, error) {
	logger := log.Log.WithName("renderSriovResourcePolicyForNode")
	logger.V(1).Info("Start to render SriovResourcePolicy for node", "node", node.Name)

	var applicablePolicies []*sriovnetworkv1.SriovNetworkNodePolicy
	for i := range pl.Items {
		p := &pl.Items[i]
		if p.Name == constants.DefaultPolicyName {
			continue
		}
		if p.Selected(node) {
			applicablePolicies = append(applicablePolicies, p)
		}
	}
	if len(applicablePolicies) == 0 {
		logger.V(1).Info("No policies apply to node, skipping policy creation", "node", node.Name)
		return nil, nil
	}

	policy := &sriovdrav1alpha1.SriovResourcePolicy{
		ObjectMeta: metav1.ObjectMeta{
			// Same metadata.name as SriovNetworkNodeState (syncAllSriovNetworkNodeStates: ns.Name = node.Name).
			Name:      node.Name,
			Namespace: vars.Namespace,
			Labels: map[string]string{
				deviceClassGeneratedByLabel:  deviceClassOperatorLabelVal,
				sriovResourcePolicyNodeLabel: node.Name,
			},
		},
		Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
			NodeSelector: sriovResourcePolicyNodeSelectorForHostname(node.Name),
			Configs:      []sriovdrav1alpha1.Config{},
		},
	}

	nodeState := &sriovnetworkv1.SriovNetworkNodeState{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: vars.Namespace, Name: node.Name}, nodeState); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("SriovNetworkNodeState not yet created, skipping node", "node", node.Name)
			return nil, nil
		}
		logger.Error(err, "Failed to get SriovNetworkNodeState", "node", node.Name)
		return nil, err
	}
	for _, p := range applicablePolicies {
		config, err := buildPolicyConfig(p, nodeState)
		if err != nil {
			logger.Error(err, "Failed to build policy config", "policy", p.Name)
			return nil, err
		}
		policy.Spec.Configs = append(policy.Spec.Configs, *config)
	}
	return policy, nil
}

// buildPolicyConfig converts a SriovNetworkNodePolicy to a SriovResourcePolicy Config
// (DeviceAttributesSelector + ResourceFilters; resource name is in DeviceAttributes).
func buildPolicyConfig(p *sriovnetworkv1.SriovNetworkNodePolicy,
	nodeState *sriovnetworkv1.SriovNetworkNodeState) (*sriovdrav1alpha1.Config, error) {
	pool := resourceNameToDeviceClassName(p.Spec.ResourceName)
	config := &sriovdrav1alpha1.Config{
		DeviceAttributesSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{draResourcePoolLabel: pool},
		},
		ResourceFilters: []sriovdrav1alpha1.ResourceFilter{},
	}
	resourceFilter := sriovdrav1alpha1.ResourceFilter{}

	// Map vendor
	if p.Spec.NicSelector.Vendor != "" {
		resourceFilter.Vendors = []string{p.Spec.NicSelector.Vendor}
	}

	// Map device ID (convert to VF device ID)
	if p.Spec.NicSelector.DeviceID != "" {
		var deviceID string
		if p.Spec.NumVfs == 0 {
			deviceID = p.Spec.NicSelector.DeviceID
		} else {
			deviceID = sriovnetworkv1.GetVfDeviceID(p.Spec.NicSelector.DeviceID)
		}
		if deviceID != "" {
			resourceFilter.Devices = []string{deviceID}
		}
	}

	// Map PF names (resolve alternative interface names via node state)
	if len(p.Spec.NicSelector.PfNames) > 0 {
		resourceFilter.PfNames = resolvePfNames(p.Spec.NicSelector.PfNames, nodeState)
	}

	// Map root devices (PF PCI addresses) to DRA filter PfPciAddresses
	if len(p.Spec.NicSelector.RootDevices) > 0 {
		resourceFilter.PfPciAddresses = p.Spec.NicSelector.RootDevices
	}

	// Map driver if VFIO
	if p.Spec.DeviceType == constants.DeviceTypeVfioPci {
		resourceFilter.Drivers = []string{"vfio-pci"}
	}

	// Add the filter if it has any criteria
	config.ResourceFilters = append(config.ResourceFilters, resourceFilter)

	return config, nil
}

// resourceNameToDeviceClassName converts a policy resourceName to a DNS-subdomain-safe DeviceClass metadata.name
// (lowercase alnum + hyphens; leading/trailing hyphens stripped; empty falls back to "sriov").
func resourceNameToDeviceClassName(resourceName string) string {
	s := strings.ReplaceAll(resourceName, "_", "-")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + 32)
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "sriov"
	}
	return name
}

// buildExtendedResourceName returns the extended resource name: ResourcePrefix/resourceName.
func buildExtendedResourceName(resourceName string) string {
	prefix := vars.ResourcePrefix
	if prefix == "" {
		return resourceName
	}
	return prefix + "/" + resourceName
}

// buildDeviceClassCEL returns a CEL expression matching devices with the given resourceName.
// Uses the extended resource name (ResourcePrefix/resourceName) so it matches the attribute set by DeviceAttributes.
func buildDeviceClassCEL(resourceName string) string {
	extendedName := buildExtendedResourceName(resourceName)
	escaped := strings.ReplaceAll(extendedName, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return fmt.Sprintf(`device.driver == "sriovnetwork.k8snetworkplumbingwg.io" && device.attributes["k8s.cni.cncf.io"].resourceName == "%s"`, escaped)
}

const (
	deviceClassGeneratedByLabel  = "sriovnetwork.openshift.io/generated-by"
	deviceClassResourceNameLabel = "sriovnetwork.openshift.io/resource-name"
	deviceClassOperatorLabelVal  = "sriov-network-operator"
	// sriovResourcePolicyNodeLabel is the node name on operator-generated SriovResourcePolicy (sync + cleanup must match).
	sriovResourcePolicyNodeLabel = "sriovnetwork.openshift.io/node"
	// DRA resource-pool label used by DeviceAttributes and SriovResourcePolicy DeviceAttributesSelector
	draResourcePoolLabel = "sriovnetwork.openshift.io/resource-pool"
	// Attribute key for resource name in DeviceAttributes (DRA driver expects this)
	draResourceNameAttributeKey = "k8s.cni.cncf.io/resourceName"
)

// syncExtendedResourceDeviceClasses creates/updates/deletes DeviceClass resources with extendedResourceName.
func (r *SriovNetworkNodePolicyReconciler) syncExtendedResourceDeviceClasses(ctx context.Context,
	dc *sriovnetworkv1.SriovOperatorConfig,
	pl *sriovnetworkv1.SriovNetworkNodePolicyList) error {
	logger := log.Log.WithName("syncExtendedResourceDeviceClasses")
	logger.V(1).Info("Start to sync extended resource DeviceClasses")
	desiredResourceNames := make(map[string]struct{})
	for i := range pl.Items {
		p := &pl.Items[i]
		if p.Name == constants.DefaultPolicyName {
			continue
		}
		if p.Spec.ResourceName != "" {
			desiredResourceNames[p.Spec.ResourceName] = struct{}{}
		}
	}
	gvk := schema.GroupVersionKind{Group: "resource.k8s.io", Version: "v1", Kind: "DeviceClassList"}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := r.List(ctx, list, client.MatchingLabels{deviceClassGeneratedByLabel: deviceClassOperatorLabelVal}); err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("DeviceClass CRD not available, skipping extended resource DeviceClass sync")
			return nil
		}
		return err
	}
	for resourceName := range desiredResourceNames {
		deviceClassName := resourceNameToDeviceClassName(resourceName)
		desired := buildDeviceClassUnstructured(deviceClassName, resourceName, buildExtendedResourceName(resourceName), buildDeviceClassCEL(resourceName))
		// Do not set controller reference: DeviceClass is cluster-scoped and dc (SriovOperatorConfig) is namespaced.
		// Operator-created DeviceClasses are identified by label and cleaned up in cleanupExtendedResourceDeviceClasses when DRA is disabled.
		var existing *unstructured.Unstructured
		for i := range list.Items {
			if list.Items[i].GetName() == deviceClassName {
				existing = &list.Items[i]
				break
			}
		}
		if existing != nil {
			desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
			existingSpec, _, _ := unstructured.NestedMap(existing.Object, "spec")
			if !equality.Semantic.DeepEqual(desiredSpec, existingSpec) {
				existing.Object["spec"] = desired.Object["spec"]
				if err := r.Update(ctx, existing); err != nil {
					return err
				}
			}
		} else {
			if err := r.Create(ctx, desired); err != nil {
				return err
			}
		}
	}
	for i := range list.Items {
		item := &list.Items[i]
		resourceName := item.GetLabels()[deviceClassResourceNameLabel]
		if _, desired := desiredResourceNames[resourceName]; !desired {
			if err := r.Delete(ctx, item); err != nil && !errors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func buildDeviceClassUnstructured(deviceClassName, resourceName, extendedResourceName, celExpression string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "resource.k8s.io", Version: "v1", Kind: "DeviceClass"})
	obj.SetName(deviceClassName)
	obj.SetLabels(map[string]string{
		deviceClassGeneratedByLabel:  deviceClassOperatorLabelVal,
		deviceClassResourceNameLabel: resourceName,
	})
	obj.Object["spec"] = map[string]interface{}{
		"extendedResourceName": extendedResourceName,
		"selectors": []interface{}{
			map[string]interface{}{"cel": map[string]interface{}{"expression": celExpression}},
		},
	}
	return obj
}

func (r *SriovNetworkNodePolicyReconciler) cleanupExtendedResourceDeviceClasses(ctx context.Context) error {
	logger := log.Log.WithName("cleanupExtendedResourceDeviceClasses")
	logger.V(1).Info("Cleaning up extended resource DeviceClasses")
	gvk := schema.GroupVersionKind{Group: "resource.k8s.io", Version: "v1", Kind: "DeviceClassList"}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := r.List(ctx, list, client.MatchingLabels{deviceClassGeneratedByLabel: deviceClassOperatorLabelVal}); err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("DeviceClass CRD not available, nothing to clean up")
			return nil
		}
		return err
	}
	for i := range list.Items {
		if err := r.Delete(ctx, &list.Items[i]); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// cleanupSriovResourcePoliciesAndDeviceAttributes deletes all operator-generated SriovResourcePolicy and DeviceAttributes CRs when DRA is disabled.
func (r *SriovNetworkNodePolicyReconciler) cleanupSriovResourcePoliciesAndDeviceAttributes(ctx context.Context) error {
	logger := log.Log.WithName("cleanupSriovResourcePoliciesAndDeviceAttributes")
	logger.V(1).Info("Cleaning up SriovResourcePolicy and DeviceAttributes CRs")
	listOpts := []client.ListOption{
		client.InNamespace(vars.Namespace),
		client.MatchingLabels{deviceClassGeneratedByLabel: deviceClassOperatorLabelVal},
	}
	policyList := &sriovdrav1alpha1.SriovResourcePolicyList{}
	if err := r.List(ctx, policyList, listOpts...); err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("SriovResourcePolicy CRD not available, nothing to clean up")
		} else {
			return err
		}
	} else {
		for i := range policyList.Items {
			if err := r.Delete(ctx, &policyList.Items[i]); err != nil && !errors.IsNotFound(err) {
				return err
			}
		}
	}
	attrList := &sriovdrav1alpha1.DeviceAttributesList{}
	if err := r.List(ctx, attrList, listOpts...); err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("DeviceAttributes CRD not available, nothing to clean up")
		} else {
			return err
		}
	} else {
		for i := range attrList.Items {
			if err := r.Delete(ctx, &attrList.Items[i]); err != nil && !errors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}
