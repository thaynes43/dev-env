"""Finite stdlib-only fixtures; no cluster/client construction or CPU load loops."""

import base64
import copy
import hashlib
import json
from pathlib import Path
import sys
import unittest
from unittest import mock

APP = Path(__file__).resolve().parent
sys.path.insert(0, str(APP))
import collector
import mac_adapter as M
from private_pipe import BinaryPipe, CELChecker

# Both imports must share the same Refused class for adapter sanitization.
C = collector
EXPECTED = json.loads((APP / "expected.json").read_text())
SECRET = "PRIVATE_INLINE_ENV_TOKEN_NEVER_OUTPUT"
CEL_BINARY = None
if len(sys.argv) >= 3 and sys.argv[1] == "--cel-helper":
    CEL_BINARY = Path(sys.argv[2]).resolve()
    del sys.argv[1:3]


class ExactFixtureCEL:
    """Pure fixture stub: exact bytes only; never substitutes for the real lexer."""
    def equal(self, actual, expected):
        C.require(isinstance(actual, str) and isinstance(expected, str), "MalformedCELExpression")
        return actual == expected


def audit(reader, expected, result, cel=None):
    return C.audit(reader, expected, result, cel or ExactFixtureCEL(), kyverno_version="1.19.1")


def meta(name, uid="fixture-uid", namespace=None):
    return {"name": name, "uid": uid, "resourceVersion": "900", "generation": 1,
            **({"namespace": namespace} if namespace else {})}


def actual_template(source):
    value = copy.deepcopy(source)
    value["metadata"]["creationTimestamp"] = None
    spec = value["spec"]
    spec.update({"dnsPolicy": "ClusterFirst", "restartPolicy": "Always", "schedulerName": "default-scheduler",
                 "terminationGracePeriodSeconds": 30, "serviceAccount": spec["serviceAccountName"]})
    for container in spec["containers"]:
        container.update({"imagePullPolicy": "IfNotPresent", "terminationMessagePath": "/dev/termination-log",
                          "terminationMessagePolicy": "File"})
    return value


def baseline():
    rows = []
    for key, info in EXPECTED["lineage"].items():
        kind, name = key.split("/")
        obj = {"apiVersion": "v1" if kind == "Pod" else "apps/v1", "kind": kind,
               "metadata": meta(name, info["uid"], C.NAMESPACE)}
        t = actual_template(EXPECTED["shelfTemplates"][info["sourceCommit"]])
        obj["metadata"]["labels"] = {"app.kubernetes.io/name": "dev-env-shelf"}
        if kind == "Deployment":
            obj["metadata"]["labels"].update({"kustomize.toolkit.fluxcd.io/name": "dev-env-shelf",
                                              "kustomize.toolkit.fluxcd.io/namespace": "dev-env-system"})
        else:
            parent_kind, parent_name = info["parent"].split("/")
            obj["metadata"]["ownerReferences"] = [{"apiVersion": "apps/v1", "kind": parent_kind,
                                                    "name": parent_name, "uid": EXPECTED["lineage"][info["parent"]]["uid"],
                                                    "controller": True, "blockOwnerDeletion": True}]
            t["metadata"]["labels"]["pod-template-hash"] = name.split("-")[-1] if kind == "ReplicaSet" else "b5ff85487"
        if kind == "Pod":
            obj["metadata"]["labels"] = t["metadata"]["labels"] | {"topology.kubernetes.io/region": "main", "topology.kubernetes.io/zone": "w"}
            obj["metadata"]["annotations"] = {"k8s.v1.cni.cncf.io/network-status": SECRET}
            obj["spec"] = t["spec"] | {"nodeName": "talosw02", "priority": 0, "preemptionPolicy": "PreemptLowerPriority",
                                       "tolerations": [{"key": k, "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": 300}
                                                       for k in ("node.kubernetes.io/not-ready", "node.kubernetes.io/unreachable")]}
        else:
            obj["spec"] = {"template": t}
        rows.append(obj)
    return rows


def policy_object(source):
    obj = copy.deepcopy(source)
    obj.pop("resource")
    obj["metadata"] = meta(source["metadata"]["name"])
    obj["spec"] = C.normalized_policy(obj["spec"], obj["kind"])
    if obj["kind"] == "ValidatingPolicy":
        obj["status"] = {"conditionStatus": {"ready": True, "message": "", "conditions": [
            {"type": key, "status": "True", "reason": "Succeeded", "message": SECRET, "observedGeneration": 0}
            for key in ("WebhookConfigured", "RBACPermissionsGranted")]}}
        obj["status"]["conditionStatus"]["message"] = C.DISABLED_GENERATION_MESSAGE
    elif obj["kind"] == "ValidatingAdmissionPolicy":
        obj["status"] = {"observedGeneration": 1, "typeChecking": {"expressionWarnings": []}}
    return obj


def hooks():
    rows = []
    for source in EXPECTED["policies"]:
        if source["kind"] != "ValidatingPolicy":
            continue
        name = source["metadata"]["name"]
        selector = copy.deepcopy(source["spec"]["matchConstraints"].get("namespaceSelector", {}))
        selector["matchExpressions"] = [{"key": "kubernetes.io/metadata.name", "operator": "NotIn", "values": [v]}
                                        for v in ("kube-system", "kyverno")]
        rows.append({"name": "vpol.validate.kyverno.svc-fail-finegrained-" + name,
                     "clientConfig": {"service": {"name": "kyverno-svc", "namespace": "kyverno", "port": 443,
                                                  "path": "/vpol/" + name}, "caBundle": SECRET},
                     "failurePolicy": "Fail", "timeoutSeconds": 10, "matchPolicy": "Equivalent",
                     "sideEffects": "NoneOnDryRun", "admissionReviewVersions": ["v1"],
                     "rules": copy.deepcopy(source["spec"]["matchConstraints"]["resourceRules"]),
                     "namespaceSelector": selector, "objectSelector": {},
                     "matchConditions": copy.deepcopy(source["spec"].get("matchConditions", []))})
    return rows


class Fake:
    def __init__(self):
        self.named, self.rows, self.requests, self.bytes = {}, {}, 0, 0
        self.get_calls, self.receipts = 0, []
        for source in EXPECTED["policies"]:
            self.named[C.api_path(source["apiVersion"], source["resource"], name=source["metadata"]["name"])] = policy_object(source)
        self.named[C.api_path("v1", "namespaces", name=C.NAMESPACE)] = {
            "apiVersion": "v1", "kind": "Namespace", "metadata": meta(C.NAMESPACE) | {
                "labels": {"kubernetes.io/metadata.name": C.NAMESPACE}}}
        self.named[C.api_path("admissionregistration.k8s.io/v1", "validatingwebhookconfigurations", name="kyverno-resource-validating-webhook-cfg")] = {
            "apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration",
            "metadata": meta("kyverno-resource-validating-webhook-cfg"), "webhooks": hooks()}
        for obj in baseline():
            self.named[C.api_path(obj["apiVersion"], next(r for v, r, k in C.KINDS if k == obj["kind"]), C.NAMESPACE, obj["metadata"]["name"])] = obj
        for version, resource, kind in C.KINDS:
            self.rows[C.api_path(version, resource, C.NAMESPACE)] = {
                "apiVersion": version, "kind": kind + "List", "metadata": {"resourceVersion": "900"},
                "items": [o for o in baseline() if o["kind"] == kind]}

    def get(self, path, query=None):
        self.requests += 1
        self.get_calls += 1
        if query:
            assert query == {"limit": C.PAGE_SIZE}, "Unexpected pagination or retry in full fixture"
            return copy.deepcopy(self.rows[path])
        return copy.deepcopy(self.named[path])


def report():
    return {"state": "Failed", "policies": [], "hooks": [], "lists": [], "protectedObjects": []}


class CollectorTests(unittest.TestCase):
    def test_frozen_public_source_hashes(self):
        self.assertEqual(hashlib.sha256((APP / "expected.json").read_bytes()).hexdigest(),
                         "e2b03a0718b38ba9e014fb04295bcac65f0a171ee09fe3b87cfc9276e559ec54")
        self.assertEqual(C.digest(EXPECTED), C.EXPECTED_BUNDLE_SHA256)
        self.assertEqual(len(EXPECTED["policies"]), 4)
        self.assertEqual(len(EXPECTED["shelfTemplates"]), 3)

    def test_complete_audit_sanitizes_values_and_rechecks_current_uids(self):
        reader, result = Fake(), report()
        audit(reader, EXPECTED, result)
        self.assertEqual(result["state"], "Ready")
        self.assertEqual(len(result["lists"]), 9)
        self.assertEqual(len(result["protectedObjects"]), 5)
        self.assertTrue(result["namedObjectsStable"])
        self.assertNotIn(SECRET, json.dumps(result))
        self.assertLess(reader.requests, C.MAX_REQUESTS)
        self.assertFalse(result["policies"][0]["readiness"]["generationAttested"])
        for actual, public in zip(result["hooks"], hooks()):
            self.assertEqual(actual["namespaceSelector"], public["namespaceSelector"])
            self.assertEqual(actual["rules"], [json.loads(rule) for rule in C.normalize_rules(public["rules"])])

    def test_policy_and_hook_fail_closed(self):
        changes = {
            "policy-drift": lambda f: f.named[next(iter(f.named))]["spec"].update({"failurePolicy": "Ignore"}),
            "not-ready": lambda f: f.named[next(iter(f.named))]["status"]["conditionStatus"].update({"ready": False}),
            "missing-condition": lambda f: f.named[next(iter(f.named))]["status"]["conditionStatus"]["conditions"].pop(),
            "missing-hook": lambda f: next(v for v in f.named.values() if v["kind"] == "ValidatingWebhookConfiguration")["webhooks"].pop(),
            "exclude-namespace": lambda f: next(v for v in f.named.values() if v["kind"] == "ValidatingWebhookConfiguration")["webhooks"][0]["namespaceSelector"].update({"matchLabels": {"kubernetes.io/metadata.name": "other"}}),
            "extra-condition": lambda f: next(v for v in f.named.values() if v["kind"] == "ValidatingWebhookConfiguration")["webhooks"][0]["matchConditions"].append({"name": "skip", "expression": "false"}),
            "missing-rule": lambda f: next(v for v in f.named.values() if v["kind"] == "ValidatingWebhookConfiguration")["webhooks"][0]["rules"].pop(),
            "stale-native-generation": lambda f: next(v for v in f.named.values() if v["kind"] == "ValidatingAdmissionPolicy")["status"].update({"observedGeneration": 0}),
            "warning": lambda f: next(v for v in f.named.values() if v["kind"] == "ValidatingAdmissionPolicy")["status"]["typeChecking"].update({"expressionWarnings": [{"warning": SECRET}]}),
        }
        for label, change in changes.items():
            with self.subTest(label=label):
                fake, result = Fake(), report()
                change(fake)
                with self.assertRaises(C.Refused):
                    audit(fake, EXPECTED, result)
                self.assertNotIn(SECRET, json.dumps(result))
                self.assertEqual(result["state"], "Failed")

    def test_hook_rejects_private_selector_and_unknown_rule_fields_without_output(self):
        changes = {
            # This NotIn requirement still matches an absent namespace label.
            "matching-private-exclusion": lambda h: h["namespaceSelector"]["matchExpressions"].append(
                {"key": "private.example/selector", "operator": "NotIn", "values": [SECRET]}),
            "unknown-selector-field": lambda h: h["namespaceSelector"].update({"private": SECRET}),
            "private-object-selector": lambda h: h["objectSelector"].update({"matchExpressions": [
                {"key": "private.example/selector", "operator": "NotIn", "values": [SECRET]}]}),
            # Existing rule normalization rejects unknown keys before emission.
            "unknown-rule-field": lambda h: h["rules"][0].update({"private": SECRET}),
        }
        for index in (0, 1):
            for label, change in changes.items():
                with self.subTest(hook=index, label=label):
                    fake = Fake()
                    config = next(v for v in fake.named.values() if v["kind"] == "ValidatingWebhookConfiguration")
                    change(config["webhooks"][index])
                    if label == "matching-private-exclusion":
                        self.assertTrue(C.selector_matches(config["webhooks"][index]["namespaceSelector"],
                                                           {"kubernetes.io/metadata.name": C.NAMESPACE}))
                    value = M.capture_report(fake, EXPECTED, ExactFixtureCEL(), kyverno_version="1.19.1")
                    self.assertEqual(value["state"], "Failed")
                    self.assertEqual(value["failure"]["code"], "MalformedWebhookRule" if label == "unknown-rule-field" else
                                     "WebhookExcludesTarget" if label == "private-object-selector" else "WrongWebhookNamespaceSelector")
                    self.assertNotIn(SECRET, json.dumps(value))

    def test_archive_slots_and_malformed_revision(self):
        for version, resource, kind in C.KINDS:
            for claim in sorted(C.ARCHIVES):
                with self.subTest(kind=kind, claim=claim):
                    t = {"spec": {"containers": [{"name": "test", "env": [{"name": "PRIVATE", "value": SECRET}]}],
                                  "volumes": [{"name": "archive", "persistentVolumeClaim": {"claimName": claim}}]}}
                    o = {"apiVersion": version, "kind": kind, "metadata": meta("unexpected", namespace=C.NAMESPACE)}
                    if kind == "Pod":
                        o["spec"] = t["spec"]
                    elif kind == "ControllerRevision":
                        o["data"] = {"spec": {"template": t}}
                    elif kind == "CronJob":
                        o["spec"] = {"jobTemplate": {"spec": {"template": t}}}
                    else:
                        o["spec"] = {"template": t}
                    with self.assertRaisesRegex(C.Refused, "") as caught:
                        C.verify_baseline(baseline() + [o], EXPECTED)
                    self.assertEqual(caught.exception.code, "UnexpectedArchiveBearingObject")
        for data in (None, "raw-unexpected", {}, {"spec": {"template": None}}):
            with self.subTest(revision=data):
                o = {"apiVersion": "apps/v1", "kind": "ControllerRevision", "metadata": meta("history", namespace=C.NAMESPACE), "data": data}
                with self.assertRaises(C.Refused):
                    C.workload_facts(o)

    def test_owner_and_template_tampering(self):
        for mode in ("uid", "owner-uid", "owner-name", "owner-kind", "unknown-env", "wrong-image", "extra-capability", "extra-volume", "missing-object", "shelf-sa-job", "shelf-template-label-job"):
            with self.subTest(mode=mode):
                rows = baseline()
                rs = next(o for o in rows if o["kind"] == "ReplicaSet")
                if mode == "uid":
                    rs["metadata"]["uid"] = "changed-uid"
                elif mode.startswith("owner-"):
                    rs["metadata"]["ownerReferences"][0][mode[6:]] = "Wrong" if mode == "owner-kind" else "wrong"
                elif mode == "unknown-env":
                    rs["spec"]["template"]["spec"]["containers"][0]["env"].append({"name": "PRIVATE", "value": SECRET})
                elif mode == "wrong-image":
                    rs["spec"]["template"]["spec"]["containers"][0]["image"] = "arbitrary"
                elif mode == "extra-capability":
                    rs["spec"]["template"]["spec"]["containers"][0]["securityContext"]["capabilities"]["add"] = ["SYS_ADMIN"]
                elif mode == "extra-volume":
                    rs["spec"]["template"]["spec"]["volumes"].append({"name": "secret", "secret": {"secretName": "private"}})
                elif mode == "missing-object":
                    rows.remove(rs)
                else:
                    rows.append({"apiVersion": "batch/v1", "kind": "Job", "metadata": meta("chosen-shelf", namespace=C.NAMESPACE),
                                 "spec": {"template": {"metadata": {"labels": {"app.kubernetes.io/name": "dev-env-shelf"}} if mode == "shelf-template-label-job" else {},
                                                       "spec": {"serviceAccountName": "default" if mode == "shelf-template-label-job" else "dev-env-shelf",
                                                                "containers": [{"name": "test"}]}}}})
                with self.assertRaises(C.Refused):
                    C.verify_baseline(rows, EXPECTED)

    def test_complete_pagination_and_refusals(self):
        pod = next(o for o in baseline() if o["kind"] == "Pod")
        def page(rv="900", continuation="", items=None, remaining=None):
            return {"apiVersion": "v1", "kind": "PodList", "metadata": {"resourceVersion": rv, "continue": continuation,
                     **({"remainingItemCount": remaining} if remaining is not None else {})}, "items": [] if items is None else items}
        class Pages:
            def __init__(self, rows):
                self.rows = iter(rows)
                self.calls = 0
            def get(self, path, query):
                self.calls += 1
                return next(self.rows)
        other = pod | {"metadata": meta("other", "other-uid", C.NAMESPACE)}
        third = pod | {"metadata": meta("third", "third-uid", C.NAMESPACE)}
        valid = {
            "all-counts": [page(continuation="opaque", items=[pod], remaining=1), page(items=[other], remaining=0)],
            "no-counts": [page(continuation="opaque", items=[pod]), page(items=[other])],
            "first-omitted": [page(continuation="opaque", items=[pod]), page(items=[other], remaining=0)],
            "terminal-omitted": [page(continuation="opaque", items=[pod], remaining=1), page(items=[other])],
            "middle-and-terminal-omitted": [page(continuation="opaque", items=[pod], remaining=2),
                                            page(continuation="b", items=[other]), page(items=[third])],
            "first-and-terminal-omitted": [page(continuation="opaque", items=[pod]),
                                            page(continuation="b", items=[other], remaining=1), page(items=[third])],
        }
        for name, pages in valid.items():
            with self.subTest(valid=name):
                rows, summary = C.complete_list(Pages(pages), "v1", "pods", "Pod")
                self.assertEqual(len(rows), len(pages))
                self.assertTrue(summary["complete"])
                self.assertNotIn("opaque", json.dumps(summary))
        cases = {
            "changed-rv": [page(continuation="a", items=[pod]), page(rv="901")],
            "repeated-token": [page(continuation="a", items=[pod]), page(continuation="a", items=[pod | {"metadata": meta("other", "other", C.NAMESPACE)}])],
            "duplicate": [page(continuation="a", items=[pod]), page(items=[pod])],
            "remaining-nonzero": [page(remaining=3)],
            "missing-rv": [page(rv=None)],
            "null-items": [{"apiVersion": "v1", "kind": "PodList", "metadata": {"resourceVersion": "900"}, "items": None}],
            "empty-continuation-page": [page(continuation="a")],
            "page-bound": [page(continuation=str(i), items=[pod | {"metadata": meta("pod-"+str(i), "uid-"+str(i), C.NAMESPACE)}]) for i in range(C.MAX_PAGES)],
            "advertised-item-missing": [page(continuation="a", items=[pod], remaining=1), page(remaining=0)],
            "advertised-item-missing-terminal-omitted": [page(continuation="a", items=[pod], remaining=1), page()],
            "inconsistent-positive-count": [page(continuation="a", items=[pod], remaining=2),
                                             page(continuation="b", items=[other], remaining=2)],
            "advertised-total-overrun-omitted": [page(continuation="a", items=[pod], remaining=1), page(items=[other, third])],
            "continuation-after-advertised-total": [page(continuation="a", items=[pod], remaining=1),
                                                    page(continuation="b", items=[other])],
        }
        for name, pages in cases.items():
            with self.subTest(case=name), self.assertRaises(C.Refused):
                C.complete_list(Pages(pages), "v1", "pods", "Pod")

    def test_named_current_object_change_refuses(self):
        fake, result = Fake(), report()
        get = fake.get
        path = C.api_path("apps/v1", "deployments", C.NAMESPACE, "dev-env-shelf")
        def changing(name, query=None):
            obj = get(name, query)
            if name == path:
                obj["metadata"]["resourceVersion"] = "901"
            return obj
        fake.get = changing
        with self.assertRaises(C.Refused) as caught:
            audit(fake, EXPECTED, result)
        self.assertEqual(caught.exception.code, "NamedObjectChangedDuringAudit")
        self.assertEqual(result["state"], "Failed")

    def test_pinned_readiness_accepts_only_produced_conditions_and_message(self):
        source = EXPECTED["policies"][0]
        for message in ("", C.DISABLED_GENERATION_MESSAGE):
            obj = policy_object(source)
            obj["status"]["conditionStatus"]["message"] = message
            state, good = C.readiness(obj, source, kyverno_version="1.19.1")
            self.assertTrue(good)
            self.assertFalse(state["generationAttested"])
            self.assertNotIn(SECRET, json.dumps(state))
        changes = {
            "missing": lambda s: s["conditions"].pop(),
            "duplicate": lambda s: s["conditions"].append(copy.deepcopy(s["conditions"][0])),
            "unproduced": lambda s: s["conditions"].append({"type": "PolicyCached", "status": "True"}),
            "unknown": lambda s: s["conditions"].append({"type": SECRET, "status": "True"}),
            "negative": lambda s: s["conditions"][0].update({"status": "False"}),
            "uncertain": lambda s: s["conditions"][0].update({"status": "Unknown"}),
            "invalid-status": lambda s: s["conditions"][0].update({"status": SECRET}),
            "not-ready": lambda s: s.update({"ready": False}),
            "unknown-message": lambda s: s.update({"message": SECRET}),
            "autogen-message": lambda s: s.update({"message": "skip generating ValidatingAdmissionPolicy: pod controllers autogen is enabled."}),
        }
        for name, change in changes.items():
            with self.subTest(name=name):
                obj = policy_object(source)
                change(obj["status"]["conditionStatus"])
                try:
                    state, good = C.readiness(obj, source, kyverno_version="1.19.1")
                    self.assertFalse(good)
                    self.assertNotIn(SECRET, json.dumps(state))
                except C.Refused as error:
                    self.assertNotIn(SECRET, error.code)
        for version, mode in (("1.20.0", None), ("1.19.1", "generation"), ("1.19.1", "background-only")):
            with self.subTest(version=version, mode=mode), self.assertRaises(C.Refused):
                wrong = copy.deepcopy(source)
                if mode == "generation":
                    wrong["spec"]["generateValidatingAdmissionPolicy"] = True
                elif mode:
                    wrong["spec"]["evaluation"]["admission"] = {"enabled": False}
                C.readiness(policy_object(source), wrong, kyverno_version=version)

    def test_missing_only_typed_list_typemeta_all_nine_endpoints(self):
        for version, resource, kind in C.KINDS:
            with self.subTest(kind=kind):
                row = {"metadata": meta("typed-row", namespace=C.NAMESPACE)}
                page = {"apiVersion": version, "kind": kind + "List", "metadata": {"resourceVersion": "900"}, "items": [row]}
                reader = mock.Mock()
                reader.get.return_value = page
                rows, summary = C.complete_list(reader, version, resource, kind)
                self.assertEqual(rows[0], row | {"apiVersion": version, "kind": kind})
                self.assertNotIn("kind", row)
                self.assertTrue(summary["complete"])
                for field, value in (("apiVersion", "wrong/v1"), ("kind", "Wrong"), ("kind", None)):
                    page["items"] = [row | {field: value}]
                    with self.assertRaises(C.Refused):
                        C.complete_list(reader, version, resource, kind)
                page["items"] = [{"metadata": {"name": "missing-uid", "namespace": C.NAMESPACE, "resourceVersion": "900"}}]
                with self.assertRaises(C.Refused):
                    C.complete_list(reader, version, resource, kind)
                page["kind"] = "OtherList"
                with self.assertRaises(C.Refused):
                    C.complete_list(reader, version, resource, kind)
        with self.assertRaises(C.Refused):
            C.complete_list(mock.Mock(), "v1", "secrets", "Secret")

    def test_only_known_cel_fields_are_compared_and_other_fields_stay_exact(self):
        source = EXPECTED["policies"][0]["spec"]
        changed = copy.deepcopy(source)
        changed["failurePolicy"] = "Ignore"
        self.assertFalse(C.policy_matches_source(changed, source, "ValidatingPolicy", ExactFixtureCEL()))
        changed = copy.deepcopy(source)
        changed["variables"][0]["expression"] += " "
        # Exact stub proves the validator delegates to its checker; it does not
        # itself strip strings, perform regex tokenization or AST equivalence.
        self.assertFalse(C.policy_matches_source(changed, source, "ValidatingPolicy", ExactFixtureCEL()))
        changed = copy.deepcopy(source)
        changed["variables"][0]["name"] += "other"
        self.assertFalse(C.policy_matches_source(changed, source, "ValidatingPolicy", ExactFixtureCEL()))
        with self.assertRaises(C.Refused):
            audit(Fake(), EXPECTED | {"sourceRevision": "tampered"}, report())


class FakePipe:
    def __init__(self, replies):
        self.replies, self.requests, self.closed = iter(replies), [], False
    def exchange(self, request):
        self.requests.append(request)
        return next(self.replies)
    def close(self):
        self.closed = True


class AdapterTests(unittest.TestCase):
    def reply(self, path, body, *, attempt=1, total=None):
        value = json.dumps(body).encode()
        return {"receipt": {"method": "GET", "path": path, "startedAt": "2026-10-13T00:00:00Z",
                "finishedAt": "2026-10-13T00:00:01Z", "httpStatus": 200, "responseBytes": len(value),
                "attempt": attempt, "totalResponseBytes": len(value) if total is None else total},
                "bodyBase64": base64.b64encode(value).decode()}

    def test_transport_reply_accounting_and_private_body(self):
        path = C.api_path("v1", "namespaces", name=C.NAMESPACE)
        value = self.reply(path, {"private": SECRET})
        pipe = FakePipe([value])
        reader = M.TransportReader(pipe, EXPECTED)
        self.assertEqual(reader.get(path), {"private": SECRET})
        self.assertEqual((reader.requests, reader.bytes), (1, value["receipt"]["responseBytes"]))
        self.assertNotIn(SECRET, json.dumps(reader.receipts))
        self.assertEqual(pipe.requests, [{"op": "get", "path": path}])
        value = self.reply(path, {}, attempt=1, total=reader.bytes+2)
        pipe.replies = iter([value])
        with self.assertRaises(C.Refused):
            reader.get(path)
        self.assertTrue(pipe.closed)

    def test_rfc3339nano_all_fraction_lengths_and_calendar(self):
        path = C.api_path("v1", "namespaces", name=C.NAMESPACE)
        origin = M.rfc3339nano_ns("2026-10-13T00:00:00Z")
        for digits in range(10):
            with self.subTest(fraction_digits=digits):
                fraction = "123456789"[:digits]
                suffix = "." + fraction if digits else ""
                started = "2026-10-13T00:00:00" + suffix + "Z"
                finished = "2026-10-13T00:00:01" + suffix + "Z"
                ns = int(fraction.ljust(9, "0")) if digits else 0
                self.assertEqual(M.rfc3339nano_ns(started), origin + ns)
                reply = self.reply(path, {})
                reply["receipt"].update({"startedAt": started, "finishedAt": finished})
                reader = M.TransportReader(FakePipe([reply]), EXPECTED)
                self.assertEqual(reader.get(path), {})
                self.assertEqual(reader.receipts[0]["startedAt"], started)
                self.assertEqual(reader.receipts[0]["finishedAt"], finished)
        self.assertEqual(M.rfc3339nano_ns("1970-01-01T00:00:00.000000001Z"), 1)
        self.assertEqual(M.rfc3339nano_ns("1969-12-31T23:59:59.999999999Z"), -1)
        self.assertEqual(M.rfc3339nano_ns("2000-03-01T00:00:00Z") -
                         M.rfc3339nano_ns("2000-02-29T00:00:00Z"), 86400 * M.NANOSECONDS)
        invalid = (
            "1900-02-29T00:00:00Z", "2026-02-29T00:00:00Z", "2026-04-31T00:00:00Z",
            "0000-01-01T00:00:00Z", "2026-00-01T00:00:00Z", "2026-13-01T00:00:00Z",
            "2026-10-00T00:00:00Z", "2026-10-13T24:00:00Z", "2026-10-13T00:60:00Z",
            "2026-10-13T00:00:60Z", "2026-10-13T00:00:00.Z", "2026-10-13T00:00:00.1234567890Z",
            "2026-10-13T00:00:00+00:00", "2026-10-13T00:00:00-01:00", "2026-10-13T00:00:00z",
            "2026-10-13T00:00:00Zextra", "2026-10-13 00:00:00Z", SECRET, None,
        )
        for value in invalid:
            with self.subTest(value=value):
                reply = self.reply(path, {})
                reply["receipt"]["startedAt"] = value
                pipe = FakePipe([reply])
                reader = M.TransportReader(pipe, EXPECTED)
                with self.assertRaises(C.Refused) as caught:
                    reader.get(path)
                self.assertEqual(caught.exception.code, "MalformedTransportTimestamp")
                self.assertTrue(pipe.closed)
                self.assertEqual(reader.receipts, [])

    def test_timestamp_order_and_parent_envelope_keep_nanosecond_precision(self):
        path = C.api_path("v1", "namespaces", name=C.NAMESPACE)
        cases = (
            ("2026-10-13T00:00:00.123456789Z", "2026-10-13T00:00:00.123456789Z", True),
            ("2026-10-13T00:00:00.123456789Z", "2026-10-13T00:00:00.123456790Z", True),
            ("2026-10-13T00:00:00.123456789Z", "2026-10-13T00:00:00.123456788Z", False),
            ("2026-12-31T23:59:59.999999999Z", "2027-01-01T00:00:00Z", True),
            ("2026-10-13T00:00:00Z", "2026-10-13T00:00:05.000000001Z", True),
            ("2026-10-13T00:00:00Z", "2026-10-13T00:00:05.999999999Z", True),
            ("2026-10-13T00:00:00Z", "2026-10-13T00:00:06Z", True),
            ("2026-10-13T00:00:00Z", "2026-10-13T00:00:06.000000001Z", False),
        )
        for started, finished, valid in cases:
            with self.subTest(started=started, finished=finished):
                reply = self.reply(path, {})
                reply["receipt"].update({"startedAt": started, "finishedAt": finished})
                reader = M.TransportReader(FakePipe([reply]), EXPECTED)
                if valid:
                    self.assertEqual(reader.get(path), {})
                else:
                    with self.assertRaises(C.Refused) as caught:
                        reader.get(path)
                    self.assertEqual(caught.exception.code, "MalformedTransportTimestamp")

    def test_preserve_only_exact_redacted_predispatch_refusal(self):
        path = C.api_path("v1", "namespaces", name=C.NAMESPACE)
        previous = self.reply(path, {})
        initial_bytes = previous["receipt"]["responseBytes"]
        base = {"receipt": {"method": "GET", "path": "", "startedAt": "2026-10-13T00:00:01.000000001Z",
                "finishedAt": "2026-10-13T00:00:01.000000002Z", "responseBytes": 0,
                "attempt": 1, "totalResponseBytes": initial_bytes, "error": "RequestShapeRefused"}}
        pipe = FakePipe([previous, base])
        reader = M.TransportReader(pipe, EXPECTED)
        reader.get(path)
        with self.assertRaises(C.Refused) as caught:
            reader.get(path)
        self.assertEqual(caught.exception.code, "RequestShapeRefused")
        self.assertEqual((reader.requests, reader.bytes), (1, initial_bytes))
        with self.assertRaises(C.Refused):
            reader.get(path)
        self.assertEqual(len(pipe.requests), 2)
        changes = {
            "advanced-attempt": lambda r: r["receipt"].update({"attempt": 2}),
            "bytes": lambda r: r["receipt"].update({"responseBytes": 1, "totalResponseBytes": initial_bytes+1}),
            "status": lambda r: r["receipt"].update({"httpStatus": 403}),
            "zero-status-key": lambda r: r["receipt"].update({"httpStatus": 0}),
            "body": lambda r: r.update({"bodyBase64": "e30="}),
            "method": lambda r: r["receipt"].update({"method": "POST"}),
            "different-error": lambda r: r["receipt"].update({"error": "TransportRefused"}),
            "success": lambda r: r["receipt"].pop("error"),
        }
        for name, change in changes.items():
            with self.subTest(name=name):
                bad = copy.deepcopy(base)
                change(bad)
                pipe = FakePipe([previous, bad])
                reader = M.TransportReader(pipe, EXPECTED)
                reader.get(path)
                with self.assertRaises(C.Refused) as caught:
                    reader.get(path)
                self.assertNotEqual(caught.exception.code, "RequestShapeRefused")
                self.assertTrue(pipe.closed)

    def test_transport_refusals_are_sanitized_and_never_retry(self):
        path = C.api_path("v1", "namespaces", name=C.NAMESPACE)
        changes = {
            "private-error": lambda r: r["receipt"].update({"error": SECRET}),
            "private-path": lambda r: r["receipt"].update({"path": SECRET}),
            "unknown-field": lambda r: r["receipt"].update({"private": SECRET}),
            "counter": lambda r: r["receipt"].update({"attempt": 0}),
            "bytes": lambda r: r["receipt"].update({"responseBytes": 0, "totalResponseBytes": 0}),
            "bad-body": lambda r: r.update({"bodyBase64": SECRET}),
            "http-error-with-body": lambda r: r["receipt"].update({"error": "HTTPStatusRefused", "httpStatus": 403}),
        }
        for name, change in changes.items():
            with self.subTest(name=name):
                reply = self.reply(path, {})
                change(reply)
                pipe, reader = FakePipe([reply]), None
                reader = M.TransportReader(pipe, EXPECTED)
                with self.assertRaises(C.Refused) as caught:
                    reader.get(path)
                self.assertNotIn(SECRET, caught.exception.code)
                self.assertNotIn(SECRET, json.dumps(reader.receipts))
                with self.assertRaises(C.Refused):
                    reader.get(path)
                self.assertEqual(len(pipe.requests), 1)
        pipe = FakePipe([])
        reader = M.TransportReader(pipe, EXPECTED)
        with self.assertRaises(C.Refused):
            reader.get("/api/v1/namespaces/dev-agents/secrets")
        self.assertEqual(pipe.requests, [])

    def test_cel_reply_sanitization_and_version_pin(self):
        for reply in ({"ok": False, "profile": "cel-go0.30.0", "code": SECRET},
                      {"ok": True, "profile": "other", "equal": True},
                      {"ok": True, "profile": "cel-go0.30.0", "equal": True, "raw": SECRET}):
            pipe = FakePipe([{"ok": True, "profile": "cel-go0.30.0"}, reply])
            checker = CELChecker(pipe)
            with self.assertRaises(C.Refused) as caught:
                checker.equal("true", "true")
            self.assertNotIn(SECRET, caught.exception.code)
            self.assertTrue(pipe.closed)

    def test_transport_source_scope_and_unchanged_files(self):
        import re
        reader = M.TransportReader(FakePipe([]), EXPECTED)
        actual = set(re.findall(r'"(/apis?/[^"\n]+)"', (APP / "transport/main.go").read_text()))
        self.assertEqual(actual, reader.paths)
        expected = {"main.go": "f6c54705ecbc7e9d4227dc8910e531646e90ca34ec712800e1050505d8675beb",
                    "main_test.go": "06e9e531a132da3adf277cb72f3d25d3c7e3bc045f59cce71e00f4dc423bf190",
                    "go.mod": "5624e29a239805945a5d021e74ca1a17b59ff5d75febad9d1f61cfcb11da2751",
                    "go.sum": "3f8f6a8ac1048ed9b6cb66a4e2795ff956d52fd447239c67eed83943fa91ac35"}
        for name, digest in expected.items():
            self.assertEqual(hashlib.sha256((APP / "transport" / name).read_bytes()).hexdigest(), digest)


@unittest.skipUnless(CEL_BINARY, "Real CEL binary validation is hosted only")
class RealCELTests(unittest.TestCase):
    def setUp(self):
        self.pipe = BinaryPipe(CEL_BINARY, hashlib.sha256(CEL_BINARY.read_bytes()).hexdigest(), max_input=1024*1024, max_reply=1024)
        self.cel = CELChecker(self.pipe)
    def tearDown(self):
        self.pipe.close()

    def test_all_public_policy_expressions_parse_and_ignore_only_whitespace(self):
        for source in EXPECTED["policies"]:
            if source["kind"] == "ValidatingAdmissionPolicyBinding":
                continue
            actual = copy.deepcopy(source["spec"])
            for group in ("matchConditions", "variables", "validations"):
                for row in actual.get(group, []):
                    for field in ("expression", "messageExpression"):
                        if field in row:
                            row[field] = "\t" + row[field] + "\n"
            self.assertTrue(C.policy_matches_source(actual, source["spec"], source["kind"], self.cel))

    def test_full_offline_audit_with_missing_item_types(self):
        fake, result = Fake(), report()
        for value in fake.rows.values():
            for row in value["items"]:
                row.pop("apiVersion")
                row.pop("kind")
        audit(fake, EXPECTED, result, self.cel)
        self.assertEqual(result["state"], "Ready")
        self.assertNotIn(SECRET, json.dumps(result))


if __name__ == "__main__":
    unittest.main(verbosity=2)
