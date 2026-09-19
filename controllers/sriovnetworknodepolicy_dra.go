/*
Copyright 2026.

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

	"github.com/go-logr/logr"
	sriovdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/api/sriovdra/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	constants "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/dra"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

// collectDRAPolicyResourceNames returns every non-empty resourceName from policies, excluding the default policy.
func collectDRAPolicyResourceNames(pl *sriovnetworkv1.SriovNetworkNodePolicyList) map[string]struct{} {
	names := make(map[string]struct{})
	for i := range pl.Items {
		p := &pl.Items[i]
		if p.Name == constants.DefaultPolicyName {
			continue
		}
		if p.Spec.ResourceName != "" {
			names[p.Spec.ResourceName] = struct{}{}
		}
	}
	return names
}

type draPolicyResource struct {
	resourceName    string
	deviceClassName string
}

// filterDRAResourceNamesByDeviceClass returns policy resource names whose normalized device class names are unique.
// Distinct policy resourceNames that normalize to the same device class name cannot coexist in DRA mode.
// validateDRAResourceNameCollision in the admission webhook rejects such policies at apply time; collisions
// skipped here are logged as a reconciliation fallback (e.g. webhook disabled or objects changed out of band).
func filterDRAResourceNamesByDeviceClass(logger logr.Logger, resourceNames map[string]struct{}) []draPolicyResource {
	keys := make([]string, 0, len(resourceNames))
	for name := range resourceNames {
		keys = append(keys, name)
	}
	sort.Strings(keys)

	seen := make(map[string]struct{})
	filtered := make([]draPolicyResource, 0, len(keys))
	for _, resourceName := range keys {
		deviceClassName := dra.ResourceNameToDeviceClassName(resourceName)
		if _, dup := seen[deviceClassName]; dup {
			logger.Error(nil, "Skipping policy resource name with colliding normalized device class",
				"deviceClassName", deviceClassName, "resourceName", resourceName)
			continue
		}
		seen[deviceClassName] = struct{}{}
		filtered = append(filtered, draPolicyResource{
			resourceName:    resourceName,
			deviceClassName: deviceClassName,
		})
	}
	return filtered
}

// retainedDRAPolicyResources returns resource names kept after normalized device-class deduplication.
// Use the same result for DeviceAttributes, DeviceClass, and SriovResourcePolicy configs.
func retainedDRAPolicyResources(logger logr.Logger, pl *sriovnetworkv1.SriovNetworkNodePolicyList) []draPolicyResource {
	return filterDRAResourceNamesByDeviceClass(logger, collectDRAPolicyResourceNames(pl))
}

// draResourceNameSet returns a set of resource names from a list of draPolicyResource.
func draResourceNameSet(resources []draPolicyResource) map[string]struct{} {
	set := make(map[string]struct{}, len(resources))
	for _, res := range resources {
		set[res.resourceName] = struct{}{}
	}
	return set
}

// draDeviceClassPoolSet returns a set of device class names from a list of draPolicyResource.
func draDeviceClassPoolSet(resources []draPolicyResource) map[string]struct{} {
	set := make(map[string]struct{}, len(resources))
	for _, res := range resources {
		set[res.deviceClassName] = struct{}{}
	}
	return set
}

// ensureControllerOwner sets SriovOperatorConfig as controller owner when it is not already.
// Returns true when OwnerReferences were mutated and an Update is required.
func ensureControllerOwner(owner, obj metav1.Object, scheme *runtime.Scheme) (bool, error) {
	if metav1.IsControlledBy(obj, owner) {
		return false, nil
	}
	if err := controllerutil.SetControllerReference(owner, obj, scheme); err != nil {
		return false, err
	}
	return true, nil
}

// applyDesiredLabels merges operator labels onto obj and reports whether any value changed.
func applyDesiredLabels(obj metav1.Object, desired map[string]string) bool {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	changed := false
	for k, v := range desired {
		if labels[k] != v {
			changed = true
		}
		labels[k] = v
	}
	obj.SetLabels(labels)
	return changed
}

// reconcileDeviceAttributes brings an existing DeviceAttributes CR in line with desired state:
// owner reference, operator labels (including after managed-label drift), and spec.
func reconcileDeviceAttributes(ctx context.Context, r *SriovNetworkNodePolicyReconciler,
	dc *sriovnetworkv1.SriovOperatorConfig, logger logr.Logger,
	desired, existing *sriovdrav1alpha1.DeviceAttributes) error {
	changed, err := ensureControllerOwner(dc, existing, r.Scheme)
	if err != nil {
		return err
	}
	if applyDesiredLabels(existing, desired.Labels) {
		changed = true
	}
	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		changed = true
	}
	if !changed {
		return nil
	}
	logger.V(1).Info("Updating DeviceAttributes", "name", existing.Name)
	return r.Update(ctx, existing)
}

// reconcileSriovResourcePolicy brings an existing SriovResourcePolicy CR in line with desired state:
// owner reference, operator labels (including after managed-label drift), and spec.
func reconcileSriovResourcePolicy(ctx context.Context, r *SriovNetworkNodePolicyReconciler,
	dc *sriovnetworkv1.SriovOperatorConfig, logger logr.Logger,
	desired, existing *sriovdrav1alpha1.SriovResourcePolicy) error {
	changed, err := ensureControllerOwner(dc, existing, r.Scheme)
	if err != nil {
		return err
	}
	if applyDesiredLabels(existing, desired.Labels) {
		changed = true
	}
	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		changed = true
	}
	if !changed {
		return nil
	}
	logger.V(1).Info("Updating SriovResourcePolicy", "name", existing.Name)
	return r.Update(ctx, existing)
}

// deviceClassAPIUnavailable reports whether the cluster client cannot use DeviceClass (API absent or type not in scheme).
func deviceClassAPIUnavailable(err error) bool {
	if err == nil {
		return false
	}
	return apimeta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err)
}

// extendedDeviceClassHasOwnershipMarker reports whether dc was previously managed by this operator.
// The resource-name label survives managed-label drift when generated-by is removed.
func extendedDeviceClassHasOwnershipMarker(dc *resourceapi.DeviceClass, resourceName string) bool {
	if dc == nil || dc.Labels == nil {
		return false
	}
	return dc.Labels[dra.DeviceClassResourceNameLabel] == resourceName
}

// reconcileExtendedDeviceClass brings an existing extended-resource DeviceClass in line with desired
// operator labels (including after managed-label drift) and spec.
func reconcileExtendedDeviceClass(ctx context.Context, r client.Client,
	desired, existing *resourceapi.DeviceClass) error {
	changed := applyDesiredLabels(existing, desired.GetLabels())
	if !equality.Semantic.DeepEqual(desired.Spec, existing.Spec) {
		existing.Spec = desired.Spec
		changed = true
	}
	if !changed {
		return nil
	}
	return r.Update(ctx, existing)
}

// syncDeviceAttributes creates/updates/deletes DeviceAttributes CRs for each unique resourceName from policies (DRA mode).
// Policies reference these via DeviceAttributesSelector; the driver merges attributes onto selected devices.
func (r *SriovNetworkNodePolicyReconciler) syncDeviceAttributes(ctx context.Context,
	dc *sriovnetworkv1.SriovOperatorConfig,
	pl *sriovnetworkv1.SriovNetworkNodePolicyList) error {
	logger := log.Log.WithName("syncDeviceAttributes")
	logger.V(1).Info("Start to sync DeviceAttributes CRs")

	retainedPolicyResources := retainedDRAPolicyResources(logger, pl)

	attrList := &sriovdrav1alpha1.DeviceAttributesList{}
	if err := r.List(ctx, attrList, client.InNamespace(vars.Namespace),
		client.MatchingLabels(dra.OperatorGeneratedByLabels())); err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("DeviceAttributes CRD not available, skipping sync")
			return nil
		}
		logger.Error(err, "Failed to list DeviceAttributes CRs")
		return err
	}

	existingAttrsByName := make(map[string]*sriovdrav1alpha1.DeviceAttributes, len(attrList.Items))
	for i := range attrList.Items {
		existingAttrsByName[attrList.Items[i].Name] = &attrList.Items[i]
	}

	for _, res := range retainedPolicyResources {
		name := res.deviceClassName + "-attrs"
		desired := buildDeviceAttributesCR(name, res.resourceName)
		if err := controllerutil.SetControllerReference(dc, desired, r.Scheme); err != nil {
			logger.Error(err, "Failed to set controller reference on DeviceAttributes", "name", name)
			return err
		}
		if existing, ok := existingAttrsByName[name]; ok {
			if err := reconcileDeviceAttributes(ctx, r, dc, logger, desired, existing); err != nil {
				logger.Error(err, "Failed to reconcile DeviceAttributes", "name", name)
				return err
			}
			continue
		}
		logger.V(1).Info("Creating DeviceAttributes", "name", name)
		if err := r.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				logger.V(1).Info("Adopting existing DeviceAttributes after managed-label drift", "name", name)
				existing := &sriovdrav1alpha1.DeviceAttributes{}
				if err := r.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: name}, existing); err != nil {
					logger.Error(err, "Failed to get existing DeviceAttributes for adoption", "name", name)
					return err
				}
				if err := reconcileDeviceAttributes(ctx, r, dc, logger, desired, existing); err != nil {
					logger.Error(err, "Failed to adopt DeviceAttributes", "name", name)
					return err
				}
				continue
			}
			logger.Error(err, "Failed to create DeviceAttributes", "name", name)
			return err
		}
	}

	desiredPools := draDeviceClassPoolSet(retainedPolicyResources)

	for i := range attrList.Items {
		item := &attrList.Items[i]
		pool := item.Labels[dra.ResourcePoolLabel]
		if _, found := desiredPools[pool]; !found {
			logger.V(1).Info("Deleting obsolete DeviceAttributes", "name", item.Name)
			if err := r.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to delete DeviceAttributes", "name", item.Name)
				return err
			}
		}
	}
	return nil
}

func buildDeviceAttributesCR(name, resourceName string) *sriovdrav1alpha1.DeviceAttributes {
	deviceClassName := dra.ResourceNameToDeviceClassName(resourceName)
	// Use same extended resource name (prefix/resourceName) as device plugin for consistency
	extendedName := buildExtendedResourceName(resourceName)
	labels := dra.OperatorGeneratedByLabels()
	labels[dra.ResourcePoolLabel] = deviceClassName
	return &sriovdrav1alpha1.DeviceAttributes{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: vars.Namespace,
			Labels:    labels,
		},
		Spec: sriovdrav1alpha1.DeviceAttributesSpec{
			Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				resourceapi.QualifiedName(dra.ResourceNameAttributeKey): {StringValue: &extendedName},
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

	retainedPolicyResources := retainedDRAPolicyResources(logger, pl)
	retainedResourceNames := draResourceNameSet(retainedPolicyResources)
	desiredPolicies := make(map[string]*sriovdrav1alpha1.SriovResourcePolicy)
	for _, node := range nl.Items {
		policy, err := r.renderSriovResourcePolicyForNode(ctx, pl, &node, retainedResourceNames)
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
		client.MatchingLabels(dra.OperatorGeneratedByLabels())); err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.V(1).Info("SriovResourcePolicy CRD not available, skipping sync")
			return nil
		}
		logger.Error(err, "Failed to list SriovResourcePolicy CRs")
		return err
	}

	existingPoliciesByName := make(map[string]*sriovdrav1alpha1.SriovResourcePolicy, len(policyList.Items))
	for i := range policyList.Items {
		existingPoliciesByName[policyList.Items[i].Name] = &policyList.Items[i]
	}

	for nodeName, desired := range desiredPolicies {
		if existing, ok := existingPoliciesByName[desired.Name]; ok {
			if err := reconcileSriovResourcePolicy(ctx, r, dc, logger, desired, existing); err != nil {
				logger.Error(err, "Failed to reconcile SriovResourcePolicy", "name", desired.Name, "node", nodeName)
				return err
			}
			continue
		}
		logger.V(1).Info("Creating SriovResourcePolicy", "name", desired.Name, "node", nodeName)
		if err := controllerutil.SetControllerReference(dc, desired, r.Scheme); err != nil {
			logger.Error(err, "Failed to set controller reference", "name", desired.Name)
			return err
		}
		if err := r.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				logger.V(1).Info("Adopting existing SriovResourcePolicy after managed-label drift", "name", desired.Name, "node", nodeName)
				existing := &sriovdrav1alpha1.SriovResourcePolicy{}
				if err := r.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, existing); err != nil {
					logger.Error(err, "Failed to get existing SriovResourcePolicy for adoption", "name", desired.Name)
					return err
				}
				if err := reconcileSriovResourcePolicy(ctx, r, dc, logger, desired, existing); err != nil {
					logger.Error(err, "Failed to adopt SriovResourcePolicy", "name", desired.Name)
					return err
				}
				continue
			}
			logger.Error(err, "Failed to create SriovResourcePolicy", "name", desired.Name)
			return err
		}
	}

	for i := range policyList.Items {
		existing := &policyList.Items[i]
		nodeName := existing.Labels[dra.SriovResourcePolicyNodeLabel]
		if _, exists := desiredPolicies[nodeName]; !exists {
			logger.V(1).Info("Deleting obsolete SriovResourcePolicy", "name", existing.Name, "node", nodeName)
			if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to delete SriovResourcePolicy", "name", existing.Name)
				return err
			}
		}
	}
	return nil
}

// nodeHostname returns the kubernetes.io/hostname label value for node selectors.
// node.Name is not guaranteed to match that label (e.g. FQDN vs short name).
func nodeHostname(node *corev1.Node) (string, error) {
	hostname := node.Labels[corev1.LabelHostname]
	if hostname == "" {
		return "", fmt.Errorf("node %q is missing required label %q", node.Name, corev1.LabelHostname)
	}
	return hostname, nil
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
	node *corev1.Node,
	retainedResourceNames map[string]struct{}) (*sriovdrav1alpha1.SriovResourcePolicy, error) {
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

	hostname, err := nodeHostname(node)
	if err != nil {
		logger.V(1).Info("Skipping node without kubernetes.io/hostname for SriovResourcePolicy", "node", node.Name)
		return nil, nil
	}

	policyLabels := dra.OperatorGeneratedByLabels()
	policyLabels[dra.SriovResourcePolicyNodeLabel] = node.Name
	policy := &sriovdrav1alpha1.SriovResourcePolicy{
		ObjectMeta: metav1.ObjectMeta{
			// Same metadata.name as SriovNetworkNodeState (syncAllSriovNetworkNodeStates: ns.Name = node.Name).
			Name:      node.Name,
			Namespace: vars.Namespace,
			Labels:    policyLabels,
		},
		Spec: sriovdrav1alpha1.SriovResourcePolicySpec{
			NodeSelector: sriovResourcePolicyNodeSelectorForHostname(hostname),
			Configs:      []sriovdrav1alpha1.Config{},
		},
	}

	nodeState := &sriovnetworkv1.SriovNetworkNodeState{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: vars.Namespace, Name: node.Name}, nodeState); err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("SriovNetworkNodeState not yet created, skipping node", "node", node.Name)
			return nil, nil
		}
		logger.Error(err, "Failed to get SriovNetworkNodeState", "node", node.Name)
		return nil, err
	}
	for _, p := range applicablePolicies {
		if p.Spec.ResourceName != "" {
			if _, ok := retainedResourceNames[p.Spec.ResourceName]; !ok {
				logger.V(1).Info("Skipping policy config for filtered DRA resource name",
					"policy", p.Name, "resourceName", p.Spec.ResourceName)
				continue
			}
		}
		config, skip, err := buildPolicyConfig(p, nodeState)
		if skip {
			logger.V(1).Info("Skipping policy config for DRA", "policy", p.Name, "reason", err)
			continue
		}
		if err != nil {
			logger.Error(err, "Failed to build policy config", "policy", p.Name)
			return nil, err
		}
		policy.Spec.Configs = append(policy.Spec.Configs, *config)
	}

	if len(policy.Spec.Configs) == 0 {
		logger.V(1).Info("No policy configs for node, skipping policy creation", "node", node.Name)
		return nil, nil
	}
	return policy, nil
}

// buildPolicyConfig converts a SriovNetworkNodePolicy to a SriovResourcePolicy Config
// (DeviceAttributesSelector + ResourceFilters; resource name is in DeviceAttributes).
// When skip is true, err describes why the policy was omitted and the caller should not treat it as a failure.
func buildPolicyConfig(p *sriovnetworkv1.SriovNetworkNodePolicy,
	nodeState *sriovnetworkv1.SriovNetworkNodeState) (*sriovdrav1alpha1.Config, bool, error) {
	if p == nil {
		return nil, false, fmt.Errorf("policy is required")
	}

	// TODO: Add NetFilter support.
	// We skip the entire config because we cannot express the NetFilter selector for DRA Driver SR-IOV.
	if p.Spec.NicSelector.NetFilter != "" {
		// SriovResourcePolicy ResourceFilter has no netFilter field yet; omitting ResourceFilters
		// would match all devices on the node (driver wildcard semantics).
		return nil, true, fmt.Errorf("netFilter nic selector is not supported for DRA")
	}

	pool := dra.ResourceNameToDeviceClassName(p.Spec.ResourceName)
	config := &sriovdrav1alpha1.Config{
		DeviceAttributesSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{dra.ResourcePoolLabel: pool},
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

	// Map link type. vfio-pci device link type is not detectable.
	// Emit DRA ResourceFilter aliases ("eth"/"ib"); the driver normalizes to
	// "ethernet"/"infiniband" when matching device attributes.
	if p.Spec.DeviceType != constants.DeviceTypeVfioPci && p.Spec.LinkType != "" {
		resourceFilter.LinkType = strings.ToLower(constants.LinkTypeETH)
		if strings.EqualFold(p.Spec.LinkType, constants.LinkTypeIB) {
			resourceFilter.LinkType = strings.ToLower(constants.LinkTypeIB)
		}
	}

	// Do not append a zero-valued ResourceFilter: in dra-driver-sriov, deviceMatchesFilter
	// treats every unset field as a wildcard, so {} matches all devices on the node.
	// NetFilter-only policies are skipped earlier; when nothing else mapped (e.g. GetVfDeviceID
	// returned empty), omit ResourceFilters rather than emitting a match-all filter entry.
	if !equality.Semantic.DeepEqual(resourceFilter, sriovdrav1alpha1.ResourceFilter{}) {
		config.ResourceFilters = append(config.ResourceFilters, resourceFilter)
	}

	return config, false, nil
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
	attrNS, attrName, _ := strings.Cut(dra.ResourceNameAttributeKey, "/")
	return fmt.Sprintf(`device.driver == "%s" && device.attributes["%s"].%s == "%s"`,
		constants.DRADriverBaseDeviceClassName, attrNS, attrName, escaped)
}

// syncExtendedResourceDeviceClasses creates/updates/deletes DeviceClass resources with extendedResourceName.
func (r *SriovNetworkNodePolicyReconciler) syncExtendedResourceDeviceClasses(ctx context.Context,
	dc *sriovnetworkv1.SriovOperatorConfig,
	pl *sriovnetworkv1.SriovNetworkNodePolicyList) error {
	logger := log.Log.WithName("syncExtendedResourceDeviceClasses")
	logger.V(1).Info("Start to sync extended resource DeviceClasses")
	retainedPolicyResources := retainedDRAPolicyResources(logger, pl)
	retainedResourceNames := draResourceNameSet(retainedPolicyResources)
	list := &resourceapi.DeviceClassList{}
	if err := r.List(ctx, list, client.MatchingLabels(dra.OperatorGeneratedByLabels())); err != nil {
		if deviceClassAPIUnavailable(err) {
			logger.V(1).Info("DeviceClass API not available, skipping extended resource DeviceClass sync")
			return nil
		}
		return err
	}

	existingDCByName := make(map[string]*resourceapi.DeviceClass, len(list.Items))
	for i := range list.Items {
		existingDCByName[list.Items[i].Name] = &list.Items[i]
	}

	for _, res := range retainedPolicyResources {
		desired := buildExtendedResourceDeviceClass(res.deviceClassName, res.resourceName, buildExtendedResourceName(res.resourceName), buildDeviceClassCEL(res.resourceName))
		// Do not set controller reference: DeviceClass is cluster-scoped and dc (SriovOperatorConfig) is namespaced.
		// Operator-created DeviceClasses are identified by label and cleaned up in cleanupExtendedResourceDeviceClasses when DRA is disabled.
		if existing, ok := existingDCByName[res.deviceClassName]; ok {
			if err := reconcileExtendedDeviceClass(ctx, r.Client, desired, existing); err != nil {
				logger.Error(err, "Failed to reconcile DeviceClass", "name", res.deviceClassName)
				return err
			}
			continue
		}
		if err := r.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				logger.V(1).Info("Adopting existing DeviceClass after managed-label drift", "name", res.deviceClassName)
				existing := &resourceapi.DeviceClass{}
				if err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
					logger.Error(err, "Failed to get existing DeviceClass for adoption", "name", res.deviceClassName)
					return err
				}
				if !extendedDeviceClassHasOwnershipMarker(existing, res.resourceName) {
					gr := schema.GroupResource{Group: resourceapi.SchemeGroupVersion.Group, Resource: "deviceclasses"}
					return apierrors.NewConflict(gr, existing.Name,
						fmt.Errorf("DeviceClass %q exists but is not operator-managed", existing.Name))
				}
				if err := reconcileExtendedDeviceClass(ctx, r.Client, desired, existing); err != nil {
					logger.Error(err, "Failed to adopt DeviceClass", "name", res.deviceClassName)
					return err
				}
				continue
			}
			return err
		}
	}
	for i := range list.Items {
		item := &list.Items[i]
		// Skip the base DeviceClass from bindata (generated-by only, no resource-name label).
		// That object is owned by syncDRADriverObjs / cleanupDRADriverObjs.
		resourceName, ok := item.Labels[dra.DeviceClassResourceNameLabel]
		if !ok || resourceName == "" {
			continue
		}
		if _, desired := retainedResourceNames[resourceName]; !desired {
			if err := r.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func buildExtendedResourceDeviceClass(deviceClassName, resourceName, extendedResourceName, celExpression string) *resourceapi.DeviceClass {
	labels := dra.OperatorGeneratedByLabels()
	labels[dra.DeviceClassResourceNameLabel] = resourceName
	return &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   deviceClassName,
			Labels: labels,
		},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: &extendedResourceName,
			Selectors: []resourceapi.DeviceSelector{
				{CEL: &resourceapi.CELDeviceSelector{Expression: celExpression}},
			},
		},
	}
}

func (r *SriovNetworkNodePolicyReconciler) cleanupExtendedResourceDeviceClasses(ctx context.Context) error {
	logger := log.Log.WithName("cleanupExtendedResourceDeviceClasses")
	logger.V(1).Info("Cleaning up extended resource DeviceClasses")
	list := &resourceapi.DeviceClassList{}
	if err := r.List(ctx, list, client.MatchingLabels(dra.OperatorGeneratedByLabels())); err != nil {
		if deviceClassAPIUnavailable(err) {
			logger.V(1).Info("DeviceClass API not available, nothing to clean up")
			return nil
		}
		return err
	}
	for i := range list.Items {
		item := &list.Items[i]
		// Leave the base DeviceClass to cleanupDRADriverObjs (no resource-name label).
		if resourceName := item.Labels[dra.DeviceClassResourceNameLabel]; resourceName == "" {
			continue
		}
		if err := r.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
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
		client.MatchingLabels(dra.OperatorGeneratedByLabels()),
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
			if err := r.Delete(ctx, &policyList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
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
			if err := r.Delete(ctx, &attrList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}
