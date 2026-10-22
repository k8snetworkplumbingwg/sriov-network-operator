#!/bin/bash

if [ "$CLUSTER_TYPE" == "openshift" ]; then
  echo "openshift cluster"
  exit
fi

chroot_path="/host"


ovs_service=$chroot_path/usr/lib/systemd/system/ovs-vswitchd.service

needs_reload=""

# Preserve the OVS drop-in across pod termination, including temporary node
# exclusion and rolling updates. The k8s plugin removes it during reconciliation
# when OVS configuration or switchdev is no longer requested.

# Older versions injected hw-offload directly into the service file instead of a drop-in
if [ -f $ovs_service ]; then
  if grep -q hw-offload $ovs_service; then
    sed -i.bak '/hw-offload/d' $ovs_service
    needs_reload="yes"
  fi
fi

if [ -n "$needs_reload" ]; then
  chroot $chroot_path /bin/bash -c systemctl daemon-reload >/dev/null 2>&1 || true
fi
