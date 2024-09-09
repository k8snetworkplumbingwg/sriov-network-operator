package daemon

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

const (
	Unknown = "Unknown"
)

func (dn *DaemonReconcile) updateSyncState(ctx context.Context, nodeState *sriovnetworkv1.SriovNetworkNodeState, status, failedMessage string) error {
	funcLog := log.FromContext(ctx).WithName("updateSyncState")
	oldStatus := nodeState.Status.SyncStatus
	copyState := nodeState.DeepCopy()
	// clear status for patch
	copyState.Status = sriovnetworkv1.SriovNetworkNodeStateStatus{}

	funcLog.V(2).Info("update nodeState status",
		"CurrentSyncStatus", nodeState.Status.SyncStatus,
		"CurrentLastSyncError", nodeState.Status.LastSyncError,
		"NewSyncStatus", status,
		"NewFailedMessage", failedMessage)
	nodeState.Status.SyncStatus = status
	nodeState.Status.LastSyncError = failedMessage

	err := dn.client.Status().Patch(ctx, nodeState, client.MergeFrom(copyState))
	if err != nil {
		funcLog.Error(err, "failed to update node state status",
			"SyncStatus", status,
			"LastSyncError", failedMessage)
	}

	dn.recordStatusChangeEvent(ctx, oldStatus, nodeState.Status.SyncStatus, failedMessage)
	return err
}

func (dn *DaemonReconcile) getHostNetworkStatus(nodeState *sriovnetworkv1.SriovNetworkNodeState) error {
	log.Log.WithName("GetHostNetworkStatus").Info("Getting host network status")
	var iface []sriovnetworkv1.InterfaceExt
	var bridges sriovnetworkv1.Bridges
	var err error

	if vars.PlatformType == consts.VirtualOpenStack {
		iface, err = dn.platformHelpers.DiscoverSriovDevicesVirtual()
		if err != nil {
			return err
		}
	} else {
		iface, err = dn.HostHelpers.DiscoverSriovDevices(dn.HostHelpers)
		if err != nil {
			return err
		}
		if vars.ManageSoftwareBridges {
			bridges, err = dn.HostHelpers.DiscoverBridges()
			if err != nil {
				return err
			}
		}
	}

	nodeState.Status.Interfaces = iface
	nodeState.Status.Bridges = bridges

	return nil
}

func (dn *DaemonReconcile) recordStatusChangeEvent(ctx context.Context, oldStatus, newStatus, lastError string) {
	if oldStatus != newStatus {
		if oldStatus == "" {
			oldStatus = Unknown
		}
		if newStatus == "" {
			newStatus = Unknown
		}
		eventMsg := fmt.Sprintf("Status changed from: %s to: %s", oldStatus, newStatus)
		if lastError != "" {
			eventMsg = fmt.Sprintf("%s. Last Error: %s", eventMsg, lastError)
		}
		dn.eventRecorder.SendEvent(ctx, "SyncStatusChanged", eventMsg)
	}
}
