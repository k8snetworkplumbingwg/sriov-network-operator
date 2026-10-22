#!/usr/bin/env python3
"""Stateful mock API and migration/installer regression matrix (no live cluster)."""
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
GROUP = "sriovnetwork.openshift.io/v1"
OWNER = dict(apiVersion=GROUP, kind="SriovOperatorConfig", name="default", uid="config")
OTHER = dict(apiVersion="example.org/v1", kind="Other", name="keep", uid="other")


def mock():
    path = Path(os.environ["MOCK_STATE"])
    state = json.loads(path.read_text())
    args = sys.argv[2:]
    namespace = None
    if args[0] == "-n":
        namespace, args = args[1], args[2:]
    action = args[0]
    if action in ("apply", "install", "upgrade"):
        if action == "apply":
            sys.stdin.read()
        state["events"].append(action)
        path.write_text(json.dumps(state))
        return
    resource = args[1]
    if resource == "crd":
        result = dict(kind="CustomResourceDefinition", metadata=dict(
            name="sriovoperatorconfigs.sriovnetwork.openshift.io")) if state["crd"] else None
    elif resource.startswith("sriovoperatorconfigs."):
        result = state["config"]
    elif resource == "deployment":
        result = dict(kind="Deployment", metadata=dict(name="sriov-network-operator"),
                      spec=dict(replicas=state["replicas"]))
    elif resource == "pods":
        result = dict(kind="PodList", items=state["pods"])
    else:
        result = state["objects"].get(resource)
    assert namespace is None or namespace == state["namespace"], args
    if state.get("denied_read") == resource:
        sys.exit("Forbidden read")
    if action == "patch":
        state["events"].append("patch")
        if state.get("deny_patch") or state.get("fail_after") == state["patched"]:
            path.write_text(json.dumps(state))
            sys.exit("Forbidden patch")
        if state.get("conflict"):
            state["conflict"] = False
            result["metadata"]["resourceVersion"] = "2"
            path.write_text(json.dumps(state))
            sys.exit("Conflict")
        patch = json.loads(args[args.index("-p") + 1])
        for op in patch:
            key = op["path"].split("/")[-1]
            if op["op"] == "test":
                assert result["metadata"][key] == op["value"]
            else:
                assert op["op"] == "remove" and op["path"].startswith("/metadata/ownerReferences/")
                del result["metadata"]["ownerReferences"][int(key)]
        state["patched"] += 1
        path.write_text(json.dumps(state))
        return
    assert action == "get", args
    if result is not None:
        print(json.dumps(result))


def inventory():
    # Independent inventory from declaration metadata, including disabled Prometheus.
    import re
    resources = []
    for name in ("service_account", "role", "role_binding", "prometheus_role", "prometheus_role_binding"):
        for doc in (ROOT / ("deploy/operands_" + name + ".yaml")).read_text().split("---"):
            kind = re.search(r"^kind: (.+)$", doc, re.M)
            meta = re.search(r"^metadata:\n  name: (.+)$", doc, re.M)
            if kind and meta:
                resources.append(kind[1] + "/" + meta[1])
    assert len(resources) == 19
    return resources


def baseline(namespace):
    objects = {}
    for resource in inventory():
        kind, name = resource.split("/")
        objects[resource] = dict(
            apiVersion="v1" if kind == "ServiceAccount" else "rbac.authorization.k8s.io/v1",
            kind=kind, metadata=dict(name=name, namespace=namespace, uid=resource,
                resourceVersion="1", ownerReferences=[OWNER.copy(), OTHER.copy()],
                labels=dict(keep="yes"), annotations=dict(keep="yes"), finalizers=["keep"]),
            spec=dict(keep="yes"))
    return dict(namespace=namespace, crd=True, config=dict(apiVersion=GROUP,
        kind="SriovOperatorConfig", metadata=dict(name="default", namespace=namespace, uid="config")),
        replicas=0, pods=[], objects=objects, events=[], patched=0)


def tests():
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        statefile = tmp / "state"
        executable = tmp / "cluster"
        executable.write_text('#!/bin/sh\nexec python3 "' + str(Path(__file__).resolve()) + '" mock "$@"\n')
        executable.chmod(0o755)
        env = dict(os.environ, MOCK_STATE=str(statefile), OPERATOR_EXEC=str(executable))
        def run(state, ok=True, command=None):
            statefile.write_text(json.dumps(state))
            result = subprocess.run(command or ["bash", "hack/migrate-operand-rbac-ownership.sh",
                state["namespace"]], cwd=ROOT, env=env, capture_output=True, text=True)
            assert (result.returncode == 0) == ok, result.stderr
            return json.loads(statefile.read_text())

        for namespace in ("sriov-network-operator", "custom"):
            original = baseline(namespace)
            migrated = run(original)
            assert migrated["patched"] == 19
            for resource, obj in migrated["objects"].items():
                expected = copy.deepcopy(original["objects"][resource])
                expected["metadata"]["ownerReferences"] = [OTHER]
                assert obj == expected
                # Config removal cannot select these objects by the old owner UID.
                assert not any(o["uid"] == "config" for o in obj["metadata"]["ownerReferences"])
            assert run(migrated)["patched"] == 19
            fresh = baseline(namespace)
            fresh.update(objects={}, config=None, crd=False)
            assert run(fresh)["patched"] == 0
            no_config = baseline(namespace)
            no_config["config"] = None
            assert run(no_config, False)["patched"] == 0
            for key, value in (("uid", "foreign"), ("name", "foreign"),
                               ("kind", "Foreign"), ("apiVersion", "foreign/v1")):
                bad = baseline(namespace)
                list(bad["objects"].values())[-1]["metadata"]["ownerReferences"][0][key] = value
                assert run(bad, False)["patched"] == 0
            for key in ("namespace", "uid", "resourceVersion"):
                bad = baseline(namespace)
                list(bad["objects"].values())[-1]["metadata"][key] = ""
                assert run(bad, False)["patched"] == 0
            for field, value in (("replicas", 1), ("pods", [dict(spec=dict(
                    serviceAccountName="sriov-network-operator"), metadata=dict(deletionTimestamp="now"))])):
                bad = baseline(namespace)
                bad[field] = value
                assert run(bad, False)["patched"] == 0
            conflict = baseline(namespace)
            conflict["conflict"] = True
            assert run(conflict)["patched"] == 19
            for key, value in (("deny_patch", True), ("denied_read", inventory()[-1])):
                bad = baseline(namespace)
                bad[key] = value
                assert run(bad, False)["patched"] == 0
            partial = baseline(namespace)
            partial["fail_after"] = 2
            partial = run(partial, False)
            assert partial["patched"] == 2
            del partial["fail_after"]
            assert run(partial)["patched"] == 19

        # Real installer entry points must abort before apply/Helm on migration error.
        make = tmp / "make"
        make.write_text("#!/bin/sh\nexit 0\n")
        make.chmod(0o755)
        helm = tmp / "helm"
        helm.write_text('#!/bin/sh\nexec "' + str(executable) + '" "$@"\n')
        helm.chmod(0o755)
        env.update(PATH=str(tmp) + ":" + env["PATH"], HELM_MODE="upgrade",
                   HELM_TAKE_OWNERSHIP="true", NAMESPACE="custom")
        bad = baseline("custom")
        bad["replicas"] = 1
        for script, extra in (("hack/deploy-setup.sh", ["custom"]),
                              ("hack/deploy-operator-helm.sh", [])):
            result = run(bad, False, ["bash", script] + extra)
            assert not result["events"]
        for script, extra in (("hack/deploy-setup.sh", ["custom"]),
                              ("hack/deploy-operator-helm.sh", [])):
            result = run(baseline("custom"), True, ["bash", script] + extra)
            assert result["patched"] == 19
            assert result["events"][:19] == ["patch"] * 19
            assert result["events"][19] == ("apply" if extra else "upgrade")
        # argv matrix is covered by deploy_operator_helm_test.sh.
        print("PASS: migration guards, full inventory, preservation, conflicts, retries, and installer aborts")


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "mock":
        mock()
    else:
        tests()
