package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	snclientset "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/client/clientset/versioned"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/featuregate"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/helper"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/platforms"
	plugin "github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/plugins"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/systemd"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

type DaemonReconcile struct {
	client client.Client

	sriovClient snclientset.Interface
	// kubeClient allows interaction with Kubernetes, including the node we are running on.
	kubeClient kubernetes.Interface

	HostHelpers helper.HostHelpersInterface

	platformHelpers platforms.Interface

	eventRecorder *EventRecorder

	featureGate featuregate.FeatureGate

	// list of disabled plugins
	disabledPlugins []string

	loadedPlugins         map[string]plugin.VendorPlugin
	lastAppliedGeneration int64
	disableDrain          bool
}

func New(
	client client.Client,
	sriovClient snclientset.Interface,
	kubeClient kubernetes.Interface,
	hostHelpers helper.HostHelpersInterface,
	platformHelper platforms.Interface,
	er *EventRecorder,
	featureGates featuregate.FeatureGate,
	disabledPlugins []string,
) *DaemonReconcile {
	return &DaemonReconcile{
		client:          client,
		sriovClient:     sriovClient,
		kubeClient:      kubeClient,
		HostHelpers:     hostHelpers,
		platformHelpers: platformHelper,

		lastAppliedGeneration: 0,
		eventRecorder:         er,
		featureGate:           featureGates,
		disabledPlugins:       disabledPlugins,
	}
}

func (dn *DaemonReconcile) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLogger := log.FromContext(ctx).WithName("Reconcile")
	// Get the latest NodeState
	sriovResult := &systemd.SriovResult{SyncStatus: consts.SyncStatusSucceeded, LastSyncError: ""}
	desiredNodeState := &sriovnetworkv1.SriovNetworkNodeState{}
	err := dn.client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, desiredNodeState)
	if err != nil {
		if errors.IsNotFound(err) {
			reqLogger.Info("NodeState doesn't exist")
			return ctrl.Result{}, nil
		}
		reqLogger.Error(err, "Failed to fetch node state", "name", vars.NodeName)
		return ctrl.Result{}, err
	}
	originalNodeState := desiredNodeState.DeepCopy()

	latest := desiredNodeState.GetGeneration()
	reqLogger.V(0).Info("new generation", "generation", latest)

	// Update the nodeState Status object with the existing network interfaces
	err = dn.getHostNetworkStatus(desiredNodeState)
	if err != nil {
		reqLogger.Error(err, "failed to get host network status")
		return ctrl.Result{}, err
	}

	// load plugins if it has not loaded
	if len(dn.loadedPlugins) == 0 {
		dn.loadedPlugins, err = loadPlugins(desiredNodeState, dn.HostHelpers, dn.disabledPlugins)
		if err != nil {
			reqLogger.Error(err, "failed to enable vendor plugins")
			return ctrl.Result{}, err
		}
	}

	if dn.lastAppliedGeneration == latest {
		if vars.UsingSystemdMode && dn.lastAppliedGeneration == latest {
			// Check for systemd services and output of the systemd service run
			sriovResult, err = dn.CheckSystemdStatus(ctx, desiredNodeState)
			//TODO: in the case we need to think what to do if we try to apply again or not
			// for now I will leave it like that so we don't enter into a boot loop
			if err != nil {
				reqLogger.Error(err, "failed to check systemd status unexpected error")
				return ctrl.Result{}, nil
			}

			// only if something is not equal we apply of not we continue to check if something change on the node,
			// and we need to trigger a reconfiguration
			if desiredNodeState.Status.SyncStatus != sriovResult.SyncStatus ||
				desiredNodeState.Status.LastSyncError != sriovResult.LastSyncError {
				err = dn.updateSyncState(ctx, desiredNodeState, sriovResult.SyncStatus, sriovResult.LastSyncError)
				if err != nil {
					reqLogger.Error(err, "failed to update sync status")
				}
				return ctrl.Result{}, err
			}
		}

		// Check if there is a change in the host network interfaces that require a reconfiguration by the daemon
		skipReconciliation, err := dn.shouldSkipReconciliation(ctx, desiredNodeState)
		if err != nil {
			return ctrl.Result{}, err
		}

		if skipReconciliation {
			// Check if we need to update the nodeState status
			if dn.shouldUpdateStatus(desiredNodeState, originalNodeState) {
				err = dn.updateSyncState(ctx, desiredNodeState, desiredNodeState.Status.SyncStatus, desiredNodeState.Status.LastSyncError)
				if err != nil {
					reqLogger.Error(err, "failed to update NodeState status")
					return ctrl.Result{}, err
				}
			}
			// if we didn't update the status we requeue the request to check the interfaces again after the expected time
			reqLogger.Info("Current state and desire state are equal together with sync status succeeded nothing to do")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	// if the sync status is not inProgress we set it to inProgress and wait for another reconciliation loop
	if desiredNodeState.Status.SyncStatus != consts.SyncStatusInProgress {
		err = dn.updateSyncState(ctx, desiredNodeState, consts.SyncStatusInProgress, "")
		if err != nil {
			reqLogger.Error(err, "failed to update sync status to inProgress")
			return ctrl.Result{}, err
		}
	}

	reqReboot := false
	reqDrain := false

	// check if any of the plugins required to drain or reboot the node
	for k, p := range dn.loadedPlugins {
		d, r := false, false
		d, r, err = p.OnNodeStateChange(desiredNodeState)
		if err != nil {
			reqLogger.Error(err, "OnNodeStateChange plugin error", "plugin-name", k)
			return ctrl.Result{}, err
		}
		reqLogger.V(0).Info("OnNodeStateChange result",
			"plugin", k,
			"drain-required", d,
			"reboot-required", r)
		reqDrain = reqDrain || d
		reqReboot = reqReboot || r
	}

	// When running using systemd check if the applied configuration is the latest one
	// or there is a new config we need to apply
	// When using systemd configuration we write the file
	if vars.UsingSystemdMode {
		reqLogger.V(0).Info("writing systemd config file to host")
		systemdConfModified, err := systemd.WriteConfFile(desiredNodeState)
		if err != nil {
			reqLogger.Error(err, "failed to write configuration file for systemd mode")
			return ctrl.Result{}, err
		}
		if systemdConfModified {
			// remove existing result file to make sure that we will not use outdated result, e.g. in case if
			// systemd service was not triggered for some reason
			err = systemd.RemoveSriovResult()
			if err != nil {
				reqLogger.Error(err, "failed to remove result file for systemd mode")
				return ctrl.Result{}, err
			}
		}
		reqDrain = reqDrain || systemdConfModified
		// require reboot if drain needed for systemd mode
		reqReboot = reqReboot || systemdConfModified || reqDrain
		reqLogger.V(0).Info("systemd mode WriteConfFile results",
			"drain-required", reqDrain, "reboot-required", reqReboot, "disable-drain", dn.disableDrain)

		err = systemd.WriteSriovSupportedNics()
		if err != nil {
			reqLogger.Error(err, "failed to write supported nic ids file for systemd mode")
			return ctrl.Result{}, err
		}
	}

	reqLogger.V(0).Info("aggregated daemon node state requirement",
		"drain-required", reqDrain, "reboot-required", reqReboot, "disable-drain", dn.disableDrain)

	// handle drain only if the plugins request drain, or we are already in a draining request state
	if reqDrain || (utils.ObjectHasAnnotationKey(desiredNodeState, consts.NodeStateDrainAnnotationCurrent) &&
		!utils.ObjectHasAnnotation(desiredNodeState,
			consts.NodeStateDrainAnnotationCurrent,
			consts.DrainIdle)) {
		drainInProcess, err := dn.handleDrain(ctx, desiredNodeState, reqReboot)
		if err != nil {
			reqLogger.Error(err, "failed to handle drain")
			return ctrl.Result{}, err
		}
		// drain is still in progress we don't need to re-queue the request as the operator will update the annotation
		if drainInProcess {
			return ctrl.Result{}, nil
		}
	}

	// if we don't need to drain, and we are on idle we need to request the device plugin reset
	if !reqDrain && utils.ObjectHasAnnotation(desiredNodeState,
		consts.NodeStateDrainAnnotationCurrent,
		consts.DrainIdle) {
		_, err = dn.annotate(ctx, desiredNodeState, consts.DevicePluginResetRequired)
		if err != nil {
			reqLogger.Error(err, "failed to request device plugin reset")
			return ctrl.Result{}, err
		}
		// we return here and wait for another reconcile loop where the operator will finish
		// the device plugin removal from the node
		return ctrl.Result{}, nil
	}

	// if we finish the drain we should run apply here
	if dn.isDrainCompleted(desiredNodeState) {
		return dn.Apply(ctx, desiredNodeState, reqReboot, sriovResult)
	}

	return ctrl.Result{}, err
}

func (dn *DaemonReconcile) DaemonInitialization() error {
	funcLog := log.Log.WithName("DaemonInitialization")
	var err error

	if !vars.UsingSystemdMode {
		funcLog.V(0).Info("daemon running in daemon mode")
		_, err = dn.HostHelpers.CheckRDMAEnabled()
		if err != nil {
			funcLog.Error(err, "warning, failed to check RDMA state")
		}
		dn.HostHelpers.TryEnableTun()
		dn.HostHelpers.TryEnableVhostNet()
		err = systemd.CleanSriovFilesFromHost(vars.ClusterType == consts.ClusterTypeOpenshift)
		if err != nil {
			funcLog.Error(err, "failed to remove all the systemd sriov files")
		}
	} else {
		funcLog.V(0).Info("Run(): daemon running in systemd mode")
	}

	if err := dn.prepareNMUdevRule(); err != nil {
		funcLog.Error(err, "failed to prepare udev files to disable network manager on requested VFs")
	}
	if err := dn.HostHelpers.PrepareVFRepUdevRule(); err != nil {
		funcLog.Error(err, "failed to prepare udev files to rename VF representors for requested VFs")
	}

	ns := &sriovnetworkv1.SriovNetworkNodeState{}
	// init openstack info
	if vars.PlatformType == consts.VirtualOpenStack {
		ns, err = dn.HostHelpers.GetCheckPointNodeState()
		if err != nil {
			return err
		}

		if ns == nil {
			err = dn.platformHelpers.CreateOpenstackDevicesInfo()
			if err != nil {
				return err
			}
		} else {
			dn.platformHelpers.CreateOpenstackDevicesInfoFromNodeStatus(ns)
		}
	}

	err = dn.getHostNetworkStatus(ns)
	if err != nil {
		funcLog.Error(err, "failed to get host network status on init")
		return err
	}

	// save init state
	err = dn.HostHelpers.WriteCheckpointFile(ns)
	if err != nil {
		funcLog.Error(err, "failed to write checkpoint file on host")
	}
	return nil
}

func (dn *DaemonReconcile) CheckSystemdStatus(ctx context.Context, desiredNodeState *sriovnetworkv1.SriovNetworkNodeState) (*systemd.SriovResult, error) {
	funcLog := log.Log.WithName("CheckSystemdStatus")
	serviceEnabled, err := dn.HostHelpers.IsServiceEnabled(systemd.SriovServicePath)
	if err != nil {
		funcLog.Error(err, "failed to check if sriov-config service exist on host")
		return nil, err
	}
	postNetworkServiceEnabled, err := dn.HostHelpers.IsServiceEnabled(systemd.SriovPostNetworkServicePath)
	if err != nil {
		funcLog.Error(err, "failed to check if sriov-config-post-network service exist on host")
		return nil, err
	}

	// if the service doesn't exist we should continue to let the k8s plugin to create the service files
	// this is only for k8s base environments, for openshift the sriov-operator creates a machine config to will apply
	// the system service and reboot the node the config-daemon doesn't need to do anything.
	sriovResult := &systemd.SriovResult{SyncStatus: consts.SyncStatusFailed,
		LastSyncError: fmt.Sprintf("some sriov systemd services are not available on node: "+
			"sriov-config available:%t, sriov-config-post-network available:%t", serviceEnabled, postNetworkServiceEnabled)}
	if serviceEnabled && postNetworkServiceEnabled {
		sriovResult, err = systemd.ReadSriovResult()
		if err != nil {
			funcLog.Error(err, "failed to load sriov result file from host")
			return nil, err
		}
	}

	if sriovResult.LastSyncError != "" || sriovResult.SyncStatus == consts.SyncStatusFailed {
		funcLog.Info("sync failed systemd service error", "last-sync-error", sriovResult.LastSyncError)
		err = dn.updateSyncState(ctx, desiredNodeState, consts.SyncStatusFailed, sriovResult.LastSyncError)
		if err != nil {
			return nil, err
		}
		dn.lastAppliedGeneration = desiredNodeState.Generation
	}
	return sriovResult, nil
}

func (dn *DaemonReconcile) Apply(ctx context.Context, desiredNodeState *sriovnetworkv1.SriovNetworkNodeState, reqReboot bool, sriovResult *systemd.SriovResult) (ctrl.Result, error) {
	reqLogger := log.FromContext(ctx).WithName("Apply")
	// apply the vendor plugins after we are done with drain if needed
	for k, p := range dn.loadedPlugins {
		// Skip both the general and virtual plugin apply them last
		if k != GenericPluginName && k != VirtualPluginName {
			err := p.Apply()
			if err != nil {
				reqLogger.Error(err, "plugin Apply failed", "plugin-name", k)
				return ctrl.Result{}, err
			}
		}
	}

	// if we don't need to reboot, or we are not doing the configuration in systemd
	// we apply the generic plugin
	if !reqReboot && !vars.UsingSystemdMode {
		// For BareMetal machines apply the generic plugin
		selectedPlugin, ok := dn.loadedPlugins[GenericPluginName]
		if ok {
			// Apply generic plugin last
			err := selectedPlugin.Apply()
			if err != nil {
				reqLogger.Error(err, "generic plugin fail to apply")
				return ctrl.Result{}, err
			}
		}

		// For Virtual machines apply the virtual plugin
		selectedPlugin, ok = dn.loadedPlugins[VirtualPluginName]
		if ok {
			// Apply virtual plugin last
			err := selectedPlugin.Apply()
			if err != nil {
				reqLogger.Error(err, "virtual plugin failed to apply")
				return ctrl.Result{}, err
			}
		}
	}

	if reqReboot {
		reqLogger.Info("reboot node")
		dn.eventRecorder.SendEvent(ctx, "RebootNode", "Reboot node has been initiated")
		return ctrl.Result{}, dn.rebootNode()
	}

	_, err := dn.annotate(ctx, desiredNodeState, consts.DrainIdle)
	if err != nil {
		reqLogger.Error(err, "failed to request annotation update to idle")
		return ctrl.Result{}, err
	}

	reqLogger.Info("sync succeeded")
	syncStatus := consts.SyncStatusSucceeded
	lastSyncError := ""
	if vars.UsingSystemdMode {
		syncStatus = sriovResult.SyncStatus
		lastSyncError = sriovResult.LastSyncError
	}

	// Update the nodeState Status object with the existing network interfaces
	err = dn.getHostNetworkStatus(desiredNodeState)
	if err != nil {
		reqLogger.Error(err, "failed to get host network status")
		return ctrl.Result{}, err
	}

	err = dn.updateSyncState(ctx, desiredNodeState, syncStatus, lastSyncError)
	if err != nil {
		reqLogger.Error(err, "failed to update sync status")
	}
	dn.lastAppliedGeneration = desiredNodeState.Generation
	return ctrl.Result{}, err
}

func (dn *DaemonReconcile) shouldSkipReconciliation(ctx context.Context, latestState *sriovnetworkv1.SriovNetworkNodeState) (bool, error) {
	funcLog := log.Log.WithName("shouldSkipReconciliation")
	var err error

	// Skip when SriovNetworkNodeState object has just been created.
	if latestState.GetGeneration() == 1 && len(latestState.Spec.Interfaces) == 0 {
		err = dn.HostHelpers.ClearPCIAddressFolder()
		if err != nil {
			funcLog.Error(err, "failed to clear the PCI address configuration")
			return false, err
		}

		funcLog.V(0).Info("interface policy spec not yet set by controller for sriovNetworkNodeState",
			"name", latestState.Name)
		if latestState.Status.SyncStatus != consts.SyncStatusSucceeded ||
			latestState.Status.LastSyncError != "" {
			err = dn.updateSyncState(ctx, latestState, consts.SyncStatusSucceeded, "")
			if err != nil {
				return false, err
			}
		}
		return true, nil
	}

	// Verify changes in the status of the SriovNetworkNodeState CR.
	if dn.lastAppliedGeneration == latestState.Generation {
		log.Log.V(0).Info("shouldSkipReconciliation() verifying status change")
		for _, p := range dn.loadedPlugins {
			// Verify changes in the status of the SriovNetworkNodeState CR.
			log.Log.V(0).Info("shouldSkipReconciliation(): verifying status change for plugin", "pluginName", p.Name())
			changed, err := p.CheckStatusChanges(latestState)
			if err != nil {
				return false, err
			}
			if changed {
				log.Log.V(0).Info("shouldSkipReconciliation(): plugin require change", "pluginName", p.Name())
				return false, nil
			}
		}

		log.Log.V(0).Info("shouldSkipReconciliation(): Interface not changed")
		if latestState.Status.LastSyncError != "" ||
			latestState.Status.SyncStatus != consts.SyncStatusSucceeded {
			err = dn.updateSyncState(ctx, latestState, consts.SyncStatusSucceeded, "")
			if err != nil {
				return false, err
			}
		}
		return true, nil
	}

	return false, nil
}

func (dn *DaemonReconcile) shouldUpdateStatus(latestState, originalState *sriovnetworkv1.SriovNetworkNodeState) bool {
	funcLog := log.Log.WithName("shouldUpdateStatus")
	for _, latestInt := range latestState.Status.Interfaces {
		found := false
		for _, originalInt := range originalState.Status.Interfaces {
			if latestInt.PciAddress == originalInt.PciAddress {
				found = true

				// we remove the VFs list as the info change based on if the vf is allocated to a pod or not
				copyLatestInt := latestInt.DeepCopy()
				copyLatestInt.VFs = nil
				copyOriginalInt := originalInt.DeepCopy()
				copyOriginalInt.VFs = nil
				if !reflect.DeepEqual(copyLatestInt, copyOriginalInt) {
					if funcLog.V(2).Enabled() {
						funcLog.V(2).Info("interface status changed", "originalInterface", copyOriginalInt, "latestInterface", copyLatestInt)
					} else {
						funcLog.Info("interface status changed", "pciAddress", latestInt.PciAddress)
					}
					return true
				}
				funcLog.V(2).Info("DEBUG interface not changed", "lastest", latestInt, "original", originalInt)
				break
			}
		}
		if !found {
			funcLog.Info("PF doesn't exist in current nodeState status need to update nodeState on api server",
				"pciAddress",
				latestInt.PciAddress)
			return true
		}
	}
	return false
}

// handleDrain: adds the right annotation to the node and nodeState object
// returns true if we need to finish the reconcile loop and wait for a new object
func (dn *DaemonReconcile) handleDrain(ctx context.Context, desiredNodeState *sriovnetworkv1.SriovNetworkNodeState, reqReboot bool) (bool, error) {
	funcLog := log.Log.WithName("handleDrain")
	// done with the drain we can continue with the configuration
	if utils.ObjectHasAnnotation(desiredNodeState, consts.NodeStateDrainAnnotationCurrent, consts.DrainComplete) {
		funcLog.Info("the node complete the draining")
		return false, nil
	}

	// the operator is still draining the node so we reconcile
	if utils.ObjectHasAnnotation(desiredNodeState, consts.NodeStateDrainAnnotationCurrent, consts.Draining) {
		funcLog.Info("the node is still draining")
		return true, nil
	}

	// drain is disabled we continue with the configuration
	if dn.disableDrain {
		funcLog.Info("drain is disabled in sriovOperatorConfig")
		return false, nil
	}

	// annotate both node and node state with drain or reboot
	annotation := consts.DrainRequired
	if reqReboot {
		annotation = consts.RebootRequired
	}
	return dn.annotate(ctx, desiredNodeState, annotation)
}

func (dn *DaemonReconcile) rebootNode() error {
	funcLog := log.Log.WithName("rebootNode")
	funcLog.Info("trigger node reboot")
	exit, err := dn.HostHelpers.Chroot(consts.Host)
	if err != nil {
		funcLog.Error(err, "chroot command failed")
		return err
	}
	defer exit()
	// creates a new transient systemd unit to reboot the system.
	// We explictily try to stop kubelet.service first, before anything else; this
	// way we ensure the rest of system stays running, because kubelet may need
	// to do "graceful" shutdown by e.g. de-registering with a load balancer.
	// However note we use `;` instead of `&&` so we keep rebooting even
	// if kubelet failed to shutdown - that way the machine will still eventually reboot
	// as systemd will time out the stop invocation.
	cmd := exec.Command("systemd-run", "--unit", "sriov-network-config-daemon-reboot",
		"--description", "sriov-network-config-daemon reboot node", "/bin/sh", "-c", "systemctl stop kubelet.service; reboot")

	if err := cmd.Run(); err != nil {
		funcLog.Error(err, "failed to reboot node")
		return err
	}
	return nil
}

func (dn *DaemonReconcile) prepareNMUdevRule() error {
	// we need to remove the Red Hat Virtio network device from the udev rule configuration
	// if we don't remove it when running the config-daemon on a virtual node it will disconnect the node after a reboot
	// even that the operator should not be installed on virtual environments that are not openstack
	// we should not destroy the cluster if the operator is installed there
	supportedVfIds := []string{}
	for _, vfID := range sriovnetworkv1.GetSupportedVfIds() {
		if vfID == "0x1000" || vfID == "0x1041" {
			continue
		}
		supportedVfIds = append(supportedVfIds, vfID)
	}

	return dn.HostHelpers.PrepareNMUdevRule(supportedVfIds)
}

// isDrainCompleted returns true if the current-state annotation is drain completed
func (dn *DaemonReconcile) isDrainCompleted(desiredNodeState *sriovnetworkv1.SriovNetworkNodeState) bool {
	return utils.ObjectHasAnnotation(desiredNodeState, consts.NodeStateDrainAnnotationCurrent, consts.DrainComplete)
}

func (dn *DaemonReconcile) annotate(
	ctx context.Context,
	desiredNodeState *sriovnetworkv1.SriovNetworkNodeState,
	annotationState string) (bool, error) {
	funcLog := log.Log.WithName("annotate")

	funcLog.Info(fmt.Sprintf("apply '%s' annotation for node", annotationState))
	err := utils.AnnotateNode(ctx, desiredNodeState.Name, consts.NodeDrainAnnotation, annotationState, dn.client)
	if err != nil {
		log.Log.Error(err, "Failed to annotate node")
		return false, err
	}

	funcLog.Info(fmt.Sprintf("apply '%s' annotation for nodeState", annotationState))
	if err := utils.AnnotateObject(context.Background(), desiredNodeState,
		consts.NodeStateDrainAnnotation,
		annotationState, dn.client); err != nil {
		return false, err
	}

	// the node was annotated we need to wait for the operator to finish the drain
	return true, nil
}

// SetupWithManager sets up the controller with the Manager.
func (dn *DaemonReconcile) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sriovnetworkv1.SriovNetworkNodeState{}).
		Complete(dn)
}
