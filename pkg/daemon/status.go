package daemon

import (
	"context"
	"fmt"
	"reflect"

	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	sriovnetworkv1 "github.com/k8snetworkplumbingwg/sriov-network-operator/api/v1"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

const (
	Unknown = "Unknown"
)

func (dn *DaemonReconcile) updateSyncState(ctx context.Context, desiredNodeState *sriovnetworkv1.SriovNetworkNodeState, status, failedMessage string) error {
	funcLog := log.Log.WithName("updateSyncState")
	currentNodeState := &sriovnetworkv1.SriovNetworkNodeState{}
	desiredNodeState.Status.SyncStatus = status
	desiredNodeState.Status.LastSyncError = failedMessage

	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := dn.client.Get(ctx, client.ObjectKey{desiredNodeState.Namespace, desiredNodeState.Name}, currentNodeState); err != nil {
			funcLog.Error(err, "failed to get latest node state",
				"SyncStatus", status,
				"LastSyncError", failedMessage)
			return err
		}

		funcLog.V(2).Info("update nodeState status",
			"CurrentSyncStatus", currentNodeState.Status.SyncStatus,
			"CurrentLastSyncError", currentNodeState.Status.LastSyncError,
			"NewSyncStatus", status,
			"NewFailedMessage", failedMessage)

		err := dn.client.Status().Patch(ctx, desiredNodeState, client.MergeFrom(currentNodeState))
		if err != nil {
			funcLog.Error(err, "failed to update node state status",
				"SyncStatus", status,
				"LastSyncError", failedMessage)
			return err
		}
		return nil
	})

	if retryErr != nil {
		funcLog.Error(retryErr, "failed to update node state status")
		return retryErr
	}

	dn.recordStatusChangeEvent(ctx, currentNodeState.Status.SyncStatus, status, failedMessage)
	return nil
}

func (dn *DaemonReconcile) shouldUpdateStatus(current, desiredNodeState *sriovnetworkv1.SriovNetworkNodeState) (bool, error) {
	// check number of interfaces are equal
	if len(current.Status.Interfaces) != len(desiredNodeState.Status.Interfaces) {
		return true, nil
	}

	// check for bridges
	if !reflect.DeepEqual(current.Status.Bridges, desiredNodeState.Status.Bridges) {
		return true, nil
	}

	// check for system
	if !reflect.DeepEqual(current.Status.System, desiredNodeState.Status.System) {
		return true, nil
	}

	// check for interfaces
	// we can't use deep equal here because if we have a vf inside a pod is name will not be available for example
	// we use the index for both lists
	c := current.Status.DeepCopy().Interfaces
	d := desiredNodeState.Status.DeepCopy().Interfaces
	for idx := range d {
		// check if it's a new device
		if d[idx].PciAddress != c[idx].PciAddress {
			return true, nil
		}
		// remove all the vfs
		d[idx].VFs = nil
		c[idx].VFs = nil

		if !reflect.DeepEqual(d[idx], c[idx]) {
			return true, nil
		}
	}

	return false, nil
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
	nodeState.Status.System.RdmaMode, err = dn.HostHelpers.DiscoverRDMASubsystem()
	return err
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
