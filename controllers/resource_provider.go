package controllers

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

var errResourceProviderSwitchBlocked = errors.New("resource provider switch blocked")

// acceptResourceProvider persists the effective mode before consumers act on it.
// Read policies without the informer cache and use optimistic concurrency on the
// config so a concurrent spec update cannot accept the wrong requested mode.
func (r *SriovOperatorConfigReconciler) acceptResourceProvider(ctx context.Context, config *sriovnetworkv1.SriovOperatorConfig) error {
	reader := r.UncachedAPIReader
	if reader == nil {
		reader = r.Client
	}
	desired := config.RequestedResourceProvider()
	current := config.Status.ResourceProvider
	if current == desired {
		return nil
	}
	if current.DevicePlugin && current.DRA {
		return fmt.Errorf("simultaneous device-plugin and DRA providers are not supported: %+v", current)
	}
	policies := &sriovnetworkv1.SriovNetworkNodePolicyList{}
	if err := reader.List(ctx, policies, client.InNamespace(vars.Namespace)); err != nil {
		return fmt.Errorf("failed to list policies before accepting resource provider: %w", err)
	}
	if current == (sriovnetworkv1.ResourceProvider{}) {
		// On upgrade, existing DaemonSets identify the mode even when the user has
		// changed the desired gate while the operator was stopped.
		hasDevicePlugin, err := daemonSetExists(ctx, reader, devicePluginDaemonSetName)
		if err != nil {
			return fmt.Errorf("failed to inspect resource provider DaemonSet %s: %w", devicePluginDaemonSetName, err)
		}
		hasDRA, err := daemonSetExists(ctx, reader, consts.DRADriverDaemonSetName)
		if err != nil {
			return fmt.Errorf("failed to inspect resource provider DaemonSet %s: %w", consts.DRADriverDaemonSetName, err)
		}
		switch {
		case hasDevicePlugin && hasDRA:
			if len(policies.Items) > 0 {
				return fmt.Errorf("cannot initialize resource provider while both provider DaemonSets and node policies exist; remove policies before switching providers")
			}
			current = desired
		case hasDRA:
			current = sriovnetworkv1.DRAResourceProvider()
		default:
			current = sriovnetworkv1.DevicePluginResourceProvider()
		}
	}
	accepted := current
	if len(policies.Items) == 0 {
		accepted = desired
	}
	if config.Status.ResourceProvider != accepted {
		before := config.DeepCopy()
		config.Status.ResourceProvider = accepted
		if err := r.Status().Patch(ctx, config, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return fmt.Errorf("failed to persist accepted resource provider: %w", err)
		}
	}
	if accepted != desired {
		return fmt.Errorf("%w: cannot change resource provider from %+v to %+v while SriovNetworkNodePolicy %s/%s exists; remove all policies before switching providers",
			errResourceProviderSwitchBlocked, accepted, desired, policies.Items[0].Namespace, policies.Items[0].Name)
	}
	return nil
}

// syncResourceProvider installs the selected provider and keeps the previous
// provider until its replacement is ready. The generated device-plugin ConfigMap
// remains available; the node-policy controller refreshes it in device-plugin mode.
func (r *SriovOperatorConfigReconciler) syncResourceProvider(ctx context.Context, config *sriovnetworkv1.SriovOperatorConfig) (bool, error) {
	replacement, previous := devicePluginDaemonSetName, consts.DRADriverDaemonSetName
	install := func() error {
		return syncPluginDaemonObjs(ctx, r.Client, r.Scheme, config, r.FeatureGate, r.renderManifests, r.applyManifest)
	}
	remove := func() error { return cleanupDRADriverObjs(ctx, r.Client, config, r.renderManifests) }
	if config.Status.ResourceProvider.DRA {
		replacement, previous = previous, replacement
		install = func() error {
			return syncDRADriverObjs(ctx, r.Client, r.Scheme, config, r.renderManifests, r.applyManifest)
		}
		remove = func() error { return cleanupDevicePluginObjs(ctx, r.Client, config, r.FeatureGate, r.renderManifests) }
	}
	if err := install(); err != nil {
		return false, fmt.Errorf("failed to sync %s: %w", replacement, err)
	}
	// A stale cache miss must not bypass the replacement rollout check.
	reader := r.UncachedAPIReader
	if reader == nil {
		reader = r.Client
	}
	exists, err := daemonSetExists(ctx, reader, previous)
	if err != nil {
		return false, fmt.Errorf("failed to check %s: %w", previous, err)
	}
	if exists {
		ready, err := r.providerDaemonSetRolledOut(ctx, reader, replacement)
		if err != nil {
			return false, err
		}
		if !ready {
			log.FromContext(ctx).Info("Waiting for replacement resource provider rollout", "daemonSet", replacement)
			return false, nil
		}
	}
	// Always clean up residual RBAC, even after the previous DaemonSet is gone.
	if err := remove(); err != nil {
		return false, fmt.Errorf("failed to clean up %s: %w", previous, err)
	}
	return true, nil
}
