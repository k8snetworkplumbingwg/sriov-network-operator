#!/usr/bin/env bash
# Test for OCPBUGS-85433 drain behavior (commit 2: excludeTopology in VfGroup).
#
# Verifies that flipping spec.excludeTopology on a SriovNetworkNodePolicy:
#   1. triggers a partial node drain (only SR-IOV consumer pods evicted)
#   2. does NOT evict unrelated pods on the same node
#   3. restarts the device-plugin (config hash annotation, commit 1)
#   4. records the new excludeTopology in the applied PF status file
#   5. settles back to Idle / uncordons with syncStatus Succeeded
#
# Usage: KUBECONFIG=... ./test-ocpbugs-85433-drain.sh
set -u

OC="oc --insecure-skip-tls-verify"
NS=openshift-sriov-network-operator
NODE=wsfd-advnetlab143.anl.eng.bos2.dc.redhat.com
PF=eno12399
PF_PCI=0000:31:00.0
POLICY=test-excludetopology
RES=openshift.io/testexcludetopology
CONSUMER=exclude-test-consumer
BYSTANDER=exclude-test-bystander
TIMEOUT=300

PASS=0; FAIL=0
ok()   { echo "PASS: $1"; PASS=$((PASS+1)); }
bad()  { echo "FAIL: $1"; FAIL=$((FAIL+1)); }

cleanup() {
  $OC delete pod $CONSUMER $BYSTANDER -n default --ignore-not-found >/dev/null 2>&1
  $OC delete sriovnetworknodepolicy $POLICY -n $NS --ignore-not-found >/dev/null 2>&1
  $OC patch node $NODE --type json -p '[{"op":"remove","path":"/spec/unschedulable"}]' >/dev/null 2>&1
}
trap cleanup EXIT

echo "=== cleanup previous run"
cleanup
sleep 5

echo "=== create policy (no excludeTopology)"
$OC apply -f - <<EOF
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovNetworkNodePolicy
metadata:
  name: $POLICY
  namespace: $NS
spec:
  deviceType: netdevice
  nicSelector:
    pfNames:
    - $PF
  nodeSelector:
    kubernetes.io/hostname: $NODE
  numVfs: 4
  resourceName: ${RES#openshift.io/}
EOF

echo "=== wait for nodeState Succeeded"
for i in $(seq 1 60); do
  S=$($OC get sriovnetworknodestate $NODE -n $NS -o jsonpath='{.status.syncStatus}' 2>/dev/null)
  [ "$S" = "Succeeded" ] && break
  sleep 5
done
[ "$S" = "Succeeded" ] && ok "nodeState Succeeded after policy create" || { bad "nodeState not Succeeded (got '$S')"; exit 1; }

echo "=== wait for resource to be allocatable on node"
ALLOC=""
for i in $(seq 1 60); do
  ALLOC=$($OC get node $NODE -o jsonpath="{.status.allocatable.$RES}" 2>/dev/null)
  [ -n "$ALLOC" ] && [ "$ALLOC" != "0" ] && break
  sleep 5
done
[ -n "$ALLOC" ] && [ "$ALLOC" != "0" ] && ok "resource $RES allocatable on node ($ALLOC)" || { bad "resource $RES never became allocatable (got '$ALLOC')"; $OC get pods -n $NS -l app=sriov-device-plugin -o wide; exit 1; }

echo "=== create consumer pod (requests $RES) and bystander pod on $NODE"
$OC run $CONSUMER --image=quay.io/quay/busybox:latest --overrides "
{\"spec\":{\"nodeSelector\":{\"kubernetes.io/hostname\":\"$NODE\"},\"containers\":[{\"name\":\"$CONSUMER\",\"image\":\"quay.io/quay/busybox:latest\",\"command\":[\"sleep\",\"3600\"],\"resources\":{\"requests\":{\"$RES\":\"1\"},\"limits\":{\"$RES\":\"1\"}}}]}}" >/dev/null
$OC run $BYSTANDER --image=quay.io/quay/busybox:latest --overrides "
{\"spec\":{\"nodeSelector\":{\"kubernetes.io/hostname\":\"$NODE\"},\"containers\":[{\"name\":\"$BYSTANDER\",\"image\":\"quay.io/quay/busybox:latest\",\"command\":[\"sleep\",\"3600\"]}]}}" >/dev/null

for i in $(seq 1 60); do
  CS=$($OC get pod $CONSUMER -n default -o jsonpath='{.status.phase}' 2>/dev/null)
  BS=$($OC get pod $BYSTANDER -n default -o jsonpath='{.status.phase}' 2>/dev/null)
  [ "$CS" = "Running" ] && [ "$BS" = "Running" ] && break
  sleep 5
done
[ "$CS" = "Running" ] && ok "consumer pod Running (VF allocated)" || { bad "consumer pod not Running (got '$CS')"; exit 1; }
[ "$BS" = "Running" ] && ok "bystander pod Running" || bad "bystander pod not Running (got '$BS')"

CONSUMER_UID0=$($OC get pod $CONSUMER -n default -o jsonpath='{.metadata.uid}')
BYSTANDER_UID0=$($OC get pod $BYSTANDER -n default -o jsonpath='{.metadata.uid}')
DP_UID0=$($OC get pods -n $NS -l app=sriov-device-plugin -o jsonpath="{.items[?(@.spec.nodeName=='$NODE')].metadata.uid}")
echo "consumer uid: $CONSUMER_UID0"
echo "bystander uid: $BYSTANDER_UID0"
echo "device-plugin uid: $DP_UID0"

echo "=== PF status file BEFORE:"
$OC debug node/$NODE -- chroot /host cat /etc/sriov-operator/pci/$PF_PCI 2>/dev/null | python3 -m json.tool | grep -E "resourceName|excludeTopology|pciAddress" | head -8

echo "=== flip excludeTopology=true"
$OC patch sriovnetworknodepolicy $POLICY -n $NS --type merge -p '{"spec":{"excludeTopology":true}}'

echo "=== watch drain (up to ${TIMEOUT}s)"
SAW_DRAIN_REQ=0; SAW_CORDON=0; CONSUMER_RESTARTED=0; SETTLED=0
for i in $(seq 1 $((TIMEOUT/5))); do
  ANN=$($OC get node $NODE -o jsonpath='{.metadata.annotations.sriovnetwork\.openshift\.io/state}')
  CORDON=$($OC get node $NODE -o jsonpath='{.spec.unschedulable}')
  [ "$ANN" = "Drain_Required" ] || [ "$ANN" = "Draining" ] && SAW_DRAIN_REQ=1
  [ "$CORDON" = "true" ] && SAW_CORDON=1
  CONSUMER_UID=$($OC get pod $CONSUMER -n default -o jsonpath='{.metadata.uid}' 2>/dev/null)
  [ -n "$CONSUMER_UID" ] && [ "$CONSUMER_UID" != "$CONSUMER_UID0" ] && CONSUMER_RESTARTED=1
  if [ "$ANN" = "Idle" ]; then
    S=$($OC get sriovnetworknodestate $NODE -n $NS -o jsonpath='{.status.syncStatus}')
    [ "$S" = "Succeeded" ] && SETTLED=1 && break
  fi
  sleep 5
done

[ "$SAW_DRAIN_REQ" = "1" ] && ok "node drain requested (annotation=$ANN seen)" || bad "node never requested drain (last annotation='$ANN')"
[ "$SAW_CORDON" = "1" ] && ok "node was cordoned" || bad "node was never cordoned"
[ "$CONSUMER_RESTARTED" = "1" ] && ok "consumer pod was evicted and rescheduled" || bad "consumer pod was NOT evicted"

BYSTANDER_UID=$($OC get pod $BYSTANDER -n default -o jsonpath='{.metadata.uid}' 2>/dev/null)
[ "$BYSTANDER_UID" = "$BYSTANDER_UID0" ] && ok "bystander pod untouched (not evicted)" || bad "bystander pod was evicted too!"

DP_UID=$($OC get pods -n $NS -l app=sriov-device-plugin -o jsonpath="{.items[?(@.spec.nodeName=='$NODE')].metadata.uid}")
[ "$DP_UID" != "$DP_UID0" ] && ok "device-plugin pod restarted (config hash)" || bad "device-plugin pod NOT restarted"

[ "$SETTLED" = "1" ] && ok "settled: annotation Idle + syncStatus Succeeded" || bad "did not settle (annotation='$ANN' status='$S')"
CORDON=$($OC get node $NODE -o jsonpath='{.spec.unschedulable}')
[ "$CORDON" != "true" ] && ok "node uncordoned" || bad "node still cordoned"

echo "=== PF status file AFTER:"
AFTER=$($OC debug node/$NODE -- chroot /host cat /etc/sriov-operator/pci/$PF_PCI 2>/dev/null)
echo "$AFTER" | python3 -m json.tool | grep -E "resourceName|excludeTopology|pciAddress" | head -8
echo "$AFTER" | grep -q '"ExcludeTopology":true' && ok "applied PF status records excludeTopology=true" || bad "applied PF status does not record excludeTopology=true"

echo "=== device-plugin config served (new allocations):"
$OC get cm device-plugin-config -n $NS -o jsonpath="{.data.$NODE}" 2>/dev/null | python3 -c "
import json,sys
try:
    cfg=json.load(sys.stdin)
    for r in cfg.get('resourceList',[]):
        if 'excludeTopology' in r.get('selectors',{}):
            print('PASS: device-plugin config contains excludeTopology')
        else:
            print('FAIL: no excludeTopology in config')
except Exception as e:
    print('FAIL: cannot parse config:', e)
"

echo ""
echo "================ RESULT: $PASS passed, $FAIL failed ================"
[ "$FAIL" = "0" ]
