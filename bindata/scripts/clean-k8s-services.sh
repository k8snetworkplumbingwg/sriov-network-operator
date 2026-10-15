#!/bin/bash

if [ "$CLUSTER_TYPE" == "openshift" ]; then
  echo "openshift cluster"
  exit
fi

chroot_path="/host"


ovs_service=$chroot_path/usr/lib/systemd/system/ovs-vswitchd.service
ovs_dropin_dir=$chroot_path/usr/lib/systemd/system/ovs-vswitchd.service.d
ovs_dropin=$ovs_dropin_dir/10-hw-offload.conf

needs_reload=""

if [ -f $ovs_dropin ]; then
  rm -f $ovs_dropin
  rmdir $ovs_dropin_dir >/dev/null 2>&1 || true
  needs_reload="yes"
fi

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
