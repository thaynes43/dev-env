#!/usr/bin/env python3
"""Compile and evaluate the UNAPPLIED executor policy offline with pinned Kyverno."""

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

VERSION = "1.19.2"
COMMIT = "e63308f1e16c05cd009ac4864be6ec25c66b49be"
ARCHIVES = {
    ("Darwin", "arm64"): ("darwin_arm64", "7915c03c613c22363a100adb3da8d06f2e2626d9d21a98b97f921795ffdc536f"),
    ("Linux", "x86_64"): ("linux_x86_64", "357eaf1df8bc24ddd5a73979fadeaae6a032e8710f11af264229756e4956bf4a"),
}
ROOT = Path(__file__).resolve().parents[1]
PACKET = ROOT / ".agents/sagas/distributed-dev-env/verification/bounded-maintenance-2026-10-30"
FIXTURES = ROOT / "scripts/maintenance-policy-fixtures"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, help="reuse a pinned release archive instead of downloading it")
    args = parser.parse_args()
    system, checksum = ARCHIVES[(platform.system(), platform.machine())]
    name = f"kyverno-cli_v{VERSION}_{system}.tar.gz"
    archive = args.archive or ROOT / "bin/tools" / name
    if not archive.exists():
        archive.parent.mkdir(parents=True, exist_ok=True)
        url = f"https://github.com/kyverno/kyverno/releases/download/v{VERSION}/{name}"
        with urllib.request.urlopen(url, timeout=60) as response, archive.open("wb") as target:
            shutil.copyfileobj(response, target)
    if hashlib.sha256(archive.read_bytes()).hexdigest() != checksum:
        raise RuntimeError(f"pinned archive checksum mismatch: {archive}")
    work = Path(tempfile.mkdtemp(prefix="maintenance-policy-offline-"))
    binary = work / "kyverno"
    with tarfile.open(archive) as source:
        member = next(m for m in source.getmembers() if Path(m.name).name == "kyverno" and m.isfile())
        with source.extractfile(member) as stream, binary.open("wb") as target:
            shutil.copyfileobj(stream, target)
    binary.chmod(0o700)
    env = dict(os.environ, KUBECONFIG=os.devnull, GOMAXPROCS="2")
    version = subprocess.check_output([str(binary), "version"], env=env, text=True, timeout=30)
    if f"Version: {VERSION}" not in version or COMMIT not in version:
        raise RuntimeError(f"unexpected Kyverno identity: {version}")
    policy = (PACKET / "maintenance-executor.yaml").read_text()
    pod = json.loads((FIXTURES / "pod.json").read_text())
    context = json.loads((FIXTURES / "context.json").read_text())
    user = json.loads((FIXTURES / "userinfo.json").read_text())

    def run_case(name, resource, lookup, actor, expected, text=policy):
        case = work / name
        case.mkdir()
        for filename, value in [("pod.json", resource), ("context.json", lookup), ("userinfo.json", actor)]:
            (case / filename).write_text(json.dumps(value))
        (case / "policy.yaml").write_text(text)
        command = [str(binary), "apply", str(case / "policy.yaml"), "--resource", str(case / "pod.json"),
                   "--context-file", str(case / "context.json"), "--userinfo", str(case / "userinfo.json"),
                   "--crd-paths", str(ROOT / "config/crd/dev-env.haynesops.com_accessgrants.yaml"),
                   "--set", "request.operation=CREATE", "--detailed-results", "--table", "--remove-color"]
        result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=60)
        output = result.stdout + result.stderr
        (case / "result.txt").write_text(output)
        if expected == "Pass":
            valid = result.returncode == 0 and re.search(r"│\s*Pass\s*│", output) and "Error" not in output and "Fail" not in output
        elif expected == "Fail":
            valid = result.returncode != 0 and re.search(r"│\s*Fail\s*│", output) and "Error" not in output
        else:
            valid = expected in output
        if not valid:
            raise RuntimeError(f"{name}: expected {expected}, exit {result.returncode}\n{output}")
        print(f"PASS {name}: expected {expected}")

    # These two probes establish that the real compiler/request model catches
    # the original defects; a permissive dyn CEL mock did not catch them.
    run_case("original-dynamic-env-rejected", pod, context, user, "failed to compile policy",
             policy.replace("'value':string(variables.task.resource)", "'value':variables.task.resource"))
    run_case("original-omitted-subresource-errors", pod, context, user, "no such key: subResource",
             policy.replace("request.?subResource.orValue('')", "request.subResource"))
    run_case("utility-create", pod, context, user, "Pass")
    generic_pod, generic_context = copy.deepcopy(pod), copy.deepcopy(context)
    generic_pod["spec"].pop("runtimeClassName")
    generic_pod["spec"]["containers"][0].pop("env")
    generic_task = generic_context["spec"]["resources"][0]["spec"]["maintenance"]
    generic_task.pop("runtimeProfile")
    generic_task["resource"] = "synthetic-device"
    run_case("generic-create", generic_pod, generic_context, user, "Pass")
    for name, index, value in [("visible-all", 0, "all"), ("compute", 1, "compute,utility")]:
        bad = copy.deepcopy(pod)
        bad["spec"]["containers"][0]["env"][index]["value"] = value
        run_case(name, bad, context, user, "Fail")
    bad = copy.deepcopy(pod)
    bad["spec"]["runtimeClassName"] = "other"
    run_case("other-runtime", bad, context, user, "Fail")
    bad_user = copy.deepcopy(user)
    bad_user["userInfo"]["username"] = "system:serviceaccount:dev-env-system:dev-env-operator"
    run_case("other-actor", pod, context, bad_user, "Fail")
    for name, change in [("control-plane-label", "label"), ("control-plane-taint", "taint")]:
        bad_context = copy.deepcopy(context)
        node = bad_context["spec"]["resources"][1]
        if change == "label":
            node["metadata"]["labels"] = {"node-role.kubernetes.io/control-plane": ""}
        else:
            node["spec"]["taints"] = [{"key": "node-role.kubernetes.io/control-plane", "effect": "NoSchedule"}]
        run_case(name, pod, bad_context, user, "Fail")
    print(f"Pinned Kyverno {VERSION} offline engine PASS; logs: {work}")
    print("No cluster lookup/webhook, CONNECT exec, runtime/image/GPU or termination qualification.")


if __name__ == "__main__":
    main()
