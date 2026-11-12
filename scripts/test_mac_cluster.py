"""Finite Mac launcher regressions; synthetic credentials only, no cluster writes."""

import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("mac_cluster", Path(__file__).with_name("mac-cluster.py"))
launcher = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(launcher)
SENTINEL = "synthetic-private-fixture-never-log"


def config():
    return {"apiVersion": "v1", "kind": "Config", "current-context": launcher.CONTEXT,
            "contexts": [{"name": launcher.CONTEXT, "context": {"cluster": launcher.ENTRY, "user": launcher.ENTRY}}],
            "clusters": [{"name": launcher.ENTRY, "cluster": {"server": launcher.SERVER}}],
            "users": [{"name": launcher.ENTRY, "user": {"token": SENTINEL}}]}


def identity(username=launcher.IDENTITY):
    return {"status": {"userInfo": {"username": username}}}


class LauncherTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "machine.json"
        self.path.write_text(json.dumps(config()))
        self.path.chmod(0o600)
        patcher = mock.patch.object(launcher, "CONFIG", self.path)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.out, self.err = io.StringIO(), io.StringIO()

    def invoke(self, args, value=None, who=None, code=0):
        response = config() if value is None else value
        live = identity() if who is None else who
        replies = [subprocess.CompletedProcess([], 0, json.dumps(response).encode(), b""),
                   subprocess.CompletedProcess([], 0, json.dumps(live).encode(), b"")]
        with (mock.patch.object(launcher.subprocess, "run", side_effect=replies) as probe,
              mock.patch.object(launcher.subprocess, "call", return_value=code) as dispatch,
              contextlib.redirect_stdout(self.out), contextlib.redirect_stderr(self.err)):
            result = launcher.main(args)
        self.assertNotIn(SENTINEL, self.out.getvalue() + self.err.getvalue())
        return result, probe, dispatch

    def test_static_check_is_offline(self):
        code, probe, dispatch = self.invoke(["check"])
        self.assertEqual(code, 0)
        self.assertEqual(probe.call_count, 1)
        self.assertEqual(probe.call_args.args[0], [launcher.KUBECTL, "--kubeconfig=" + str(self.path),
                                                 "config", "view", "-o", "json"])
        dispatch.assert_not_called()
        self.assertIn("live identity not checked", self.out.getvalue())

    def test_live_check_is_bounded_and_does_not_dispatch(self):
        code, probe, dispatch = self.invoke(["check", "--live"])
        self.assertEqual(code, 0)
        self.assertEqual(probe.call_count, 2)
        self.assertEqual(probe.call_args.args[0], [launcher.KUBECTL, *launcher.selectors(),
                                                 "auth", "whoami", "--request-timeout=10s", "-o", "json"])
        self.assertEqual(probe.call_args.kwargs["timeout"], 15)
        self.assertEqual(probe.call_args.kwargs["stdin"], subprocess.DEVNULL)
        dispatch.assert_not_called()

    def test_dispatch_pins_both_clients_and_always_checks_live_identity(self):
        for tool, path, args in (("kubectl", launcher.KUBECTL, ["get", "pods", "-n", "dev"]),
                                 ("flux", launcher.FLUX, ["get", "kustomizations", "-A"])):
            with self.subTest(tool=tool):
                code, probe, dispatch = self.invoke([tool, *args], code=17)
                self.assertEqual(code, 17)
                self.assertEqual(probe.call_count, 2)
                self.assertEqual(dispatch.call_args.args[0], [path, *launcher.selectors(), *args])
                self.assertNotIn("shell", dispatch.call_args.kwargs)

    def test_ambient_config_and_auth_environment_cannot_change_selection(self):
        ambient = {"KUBECONFIG": "/wrong/human.yaml:/another", "KUBERC": "/wrong/preferences",
                   "KUBECTL_KUBERC": "true", "HTTPS_PROXY": "https://wrong.invalid",
                   "http_proxy": "https://wrong.invalid", "ALL_PROXY": "https://wrong.invalid",
                   "SSL_CERT_FILE": "/wrong/ca", "SSL_CERT_DIR": "/wrong/certs",
                   "KUBERNETES_MASTER": "https://wrong.invalid", "KUBERNETES_SERVICE_HOST": "wrong.invalid",
                   "KUBERNETES_SERVICE_PORT": "1234", "KEEP_ME": "yes"}
        with mock.patch.dict(os.environ, ambient, clear=True):
            code, probe, dispatch = self.invoke(["kubectl", "get", "pods"])
            self.assertEqual(dict(os.environ), ambient)
        self.assertEqual(code, 0)
        for call in [*probe.call_args_list, dispatch.call_args]:
            self.assertEqual(call.kwargs["env"], {"KUBECONFIG": str(self.path),
                                                 "KUBECTL_KUBERC": "false", "KEEP_ME": "yes"})

    def test_missing_config_never_invokes_a_client(self):
        self.path.unlink()
        code, probe, dispatch = self.invoke(["kubectl", "get", "pods"])
        self.assertEqual(code, 2)
        probe.assert_not_called()
        dispatch.assert_not_called()

    def test_unreadable_config_parse_failure_never_falls_back(self):
        with (mock.patch.object(launcher.subprocess, "run", side_effect=PermissionError(SENTINEL)) as probe,
              mock.patch.object(launcher.subprocess, "call") as dispatch,
              contextlib.redirect_stderr(self.err)):
            self.assertEqual(launcher.main(["kubectl", "get", "pods"]), 2)
        self.assertEqual(probe.call_count, 1)
        dispatch.assert_not_called()
        self.assertNotIn(SENTINEL, self.err.getvalue())

    def test_symlink_empty_oversized_and_shared_config_refused(self):
        original = self.path.read_bytes()
        for case in ("symlink", "empty", "oversized", "shared"):
            with self.subTest(case=case):
                if self.path.is_symlink() or self.path.exists():
                    self.path.unlink()
                self.path.write_bytes(original)
                self.path.chmod(0o600)
                if case == "symlink":
                    target = self.path.with_suffix(".other")
                    self.path.rename(target)
                    self.path.symlink_to(target)
                elif case == "empty":
                    self.path.write_bytes(b"")
                elif case == "oversized":
                    self.path.write_bytes(b"x" * (launcher.MAX_CONFIG + 1))
                else:
                    self.path.chmod(0o644)
                code, probe, dispatch = self.invoke(["check"])
                self.assertEqual(code, 2)
                probe.assert_not_called()
                dispatch.assert_not_called()

    def test_wrong_context_cluster_user_or_server_refused_before_auth(self):
        mutations = [
            lambda c: c.update({"current-context": "human"}),
            lambda c: c.pop("contexts"),
            lambda c: c["contexts"][0].update(name="human"),
            lambda c: c["contexts"][0]["context"].update(cluster="human"),
            lambda c: c["contexts"][0]["context"].update(user="human"),
            lambda c: c["contexts"].append(copy.deepcopy(c["contexts"][0])),
            lambda c: c["clusters"][0].update(name="other"),
            lambda c: c["users"][0].update(name="other"),
            lambda c: c["clusters"][0]["cluster"].update(server="https://other.invalid"),
            lambda c: c["clusters"][0]["cluster"].update(server="http://other.invalid"),
            lambda c: c["clusters"][0]["cluster"].update({"proxy-url": "https://other.invalid"}),
            lambda c: c["clusters"][0]["cluster"].update({"insecure-skip-tls-verify": True}),
            lambda c: c["clusters"][0]["cluster"].update({"tls-server-name": "other.invalid"}),
            lambda c: c["clusters"][0]["cluster"].update({"certificate-authority": "/other"}),
            lambda c: c["contexts"][0]["context"].update(extensions=[{}]),
        ]
        for index, mutate in enumerate(mutations):
            with self.subTest(index=index):
                value = config()
                mutate(value)
                code, probe, dispatch = self.invoke(["kubectl", "get", "pods"], value=value)
                self.assertEqual(code, 2)
                self.assertEqual(probe.call_count, 1)
                dispatch.assert_not_called()

    def test_exec_oidc_and_other_credentials_refused_before_auth(self):
        alternatives = [
            {"exec": {"command": "/never-run", "args": [SENTINEL]}},
            {"auth-provider": {"name": "oidc", "config": {"id-token": SENTINEL}}},
            {"tokenFile": "/never-read"}, {"client-key": "/never-read"},
            {"client-key-data": SENTINEL}, {"client-certificate-data": SENTINEL},
            {"username": "human", "password": SENTINEL}, {"as": "human"},
            {"as-groups": ["system:masters"]}, {"as-uid": "other"},
            {"as-user-extra": {"key": ["value"]}}, {"token": ""},
            {"token": "a\nb"}, {"token": "x" * 16385}, {"token": 5},
        ]
        for extra in alternatives:
            for keep_token in (False, True):
                with self.subTest(keys=list(extra), keep_token=keep_token):
                    value = config()
                    value["users"][0]["user"] = {**({"token": SENTINEL} if keep_token else {}), **extra}
                    code, probe, dispatch = self.invoke(["flux", "get", "sources", "all"], value=value)
                    self.assertEqual(code, 2)
                    self.assertEqual(probe.call_count, 1)
                    dispatch.assert_not_called()

    def test_every_long_override_variant_refused_before_client(self):
        for tool in ("kubectl", "flux"):
            for flag in launcher.FORBIDDEN:
                for option in (flag, flag + "=" + SENTINEL, "--" + flag[2:].replace("-", "_") + "=" + SENTINEL):
                    with self.subTest(tool=tool, option=option.split("=")[0]):
                        code, probe, dispatch = self.invoke([tool, "get", "pods", option, SENTINEL])
                        self.assertEqual(code, 2)
                        probe.assert_not_called()
                        dispatch.assert_not_called()

    def test_separator_consumed_as_flag_value_cannot_hide_overrides(self):
        for tool in ("kubectl", "flux"):
            for flag in ("--namespace", "-n", "--output", "-o", "--field-selector"):
                with self.subTest(tool=tool, flag=flag):
                    code, probe, dispatch = self.invoke([tool, "get", "pods", flag,
                                                        "--", "--context=human", "--kubeconfig=human"])
                    self.assertEqual(code, 2)
                    probe.assert_not_called()
                    dispatch.assert_not_called()
        for args in (["kubectl", "exec", "pod", "--namespace=dev", "--", "program", "--context=remote"],
                     ["kubectl", "exec", "-n", "dev", "pod", "--", "program", "-sremote"]):
            self.assertEqual(self.invoke(args)[0], 0)

    def test_short_overrides_and_clusters_refused(self):
        for flag in ("-s", "-shttps://wrong.invalid", "-s=bad", "-Asbad", "-v", "-v9", "-iv9", "-Atsbad"):
            with self.subTest(flag=flag):
                code, probe, dispatch = self.invoke(["kubectl", "get", "pods", flag])
                self.assertEqual(code, 2)
                probe.assert_not_called()
                dispatch.assert_not_called()

    def test_approved_short_values_and_remote_exec_boundary(self):
        for args in (["get", "pods", "-ndev", "-ojson", "-lapp=example"],
                     ["exec", "-it", "pod", "-n", "dev", "--", "sh", "-c", "echo --context=remote"],
                     ["exec", "pod", "--", "program", "--context=remote", "-sremote"]):
            with self.subTest(args=args):
                code, _, dispatch = self.invoke(["kubectl", *args])
                self.assertEqual(code, 0)
                self.assertEqual(dispatch.call_args.args[0][-len(args):], args)

    def test_unknown_plugin_config_and_auth_commands_refused(self):
        for args in (["kubectl", "oidc-login"], ["kubectl", "plugin", "run"],
                     ["kubectl", "create", "plugin"], ["kubectl", "config", "view", "--raw"],
                     ["kubectl", "auth", "reconcile"], ["flux", "plugin", "run"],
                     ["flux", "bootstrap"], ["/wrong/kubectl", "get", "pods"],
                     ["kubectl", "--context=wrong", "get", "pods"], ["check", "--kubeconfig=x"]):
            with self.subTest(args=args):
                code, probe, dispatch = self.invoke(args)
                self.assertEqual(code, 2)
                probe.assert_not_called()
                dispatch.assert_not_called()

    def test_bad_config_shapes_refused(self):
        for value in ([], "text", 42, {}, {"current-context": launcher.CONTEXT},
                      {**config(), "users": [None]}, {**config(), "clusters": {}}):
            with self.subTest(value=type(value)):
                code, probe, dispatch = self.invoke(["check"], value=value)
                self.assertEqual(code, 2)
                self.assertEqual(probe.call_count, 1)
                dispatch.assert_not_called()

    def test_live_mismatch_and_malformed_results_refuse_dispatch(self):
        for live in (identity("human"), {}, [], {"status": None}, {"status": {"userInfo": []}}):
            for tool in ("kubectl", "flux"):
                with self.subTest(live=live, tool=tool):
                    code, probe, dispatch = self.invoke([tool, "get", "pods"], who=live)
                    self.assertEqual(code, 2)
                    self.assertEqual(probe.call_count, 2)
                    dispatch.assert_not_called()

    def test_probe_failure_timeout_and_invalid_json_are_not_printed_or_retried(self):
        failures = [subprocess.CompletedProcess([], 1, SENTINEL.encode(), SENTINEL.encode()),
                    subprocess.CompletedProcess([], 0, SENTINEL.encode(), b""),
                    subprocess.CompletedProcess([], 0, b"\xff", b""),
                    subprocess.TimeoutExpired([SENTINEL], 15, output=SENTINEL),
                    OSError(SENTINEL)]
        for failed_phase in ("parse", "live"):
            for failure in failures:
                with self.subTest(phase=failed_phase, failure=type(failure)):
                    replies = ([subprocess.CompletedProcess([], 0, json.dumps(config()).encode(), b"")]
                               if failed_phase == "live" else []) + [failure]
                    with (mock.patch.object(launcher.subprocess, "run", side_effect=replies) as probe,
                          mock.patch.object(launcher.subprocess, "call") as dispatch,
                          contextlib.redirect_stderr(self.err)):
                        self.assertEqual(launcher.main(["kubectl", "get", "pods"]), 2)
                    self.assertEqual(probe.call_count, len(replies))
                    dispatch.assert_not_called()
                    self.assertNotIn(SENTINEL, self.err.getvalue())

    def test_config_change_after_parse_or_live_check_refuses_dispatch(self):
        for changed_phase in (1, 2):
            with self.subTest(phase=changed_phase):
                count = 0

                def probe(*args):
                    nonlocal count
                    count += 1
                    if count == changed_phase:
                        self.path.write_text(self.path.read_text() + " ")
                    return config() if count == 1 else identity()

                with (mock.patch.object(launcher, "probe", side_effect=probe),
                      mock.patch.object(launcher.subprocess, "call") as dispatch,
                      contextlib.redirect_stderr(self.err)):
                    self.assertEqual(launcher.main(["kubectl", "get", "pods"]), 2)
                self.assertEqual(count, changed_phase)
                dispatch.assert_not_called()

    def test_missing_fixed_client_never_uses_path_fallback(self):
        with (mock.patch.object(launcher, "probe", side_effect=[config(), identity()]),
              mock.patch.object(launcher.subprocess, "call", side_effect=FileNotFoundError(SENTINEL)) as dispatch,
              contextlib.redirect_stderr(self.err)):
            self.assertEqual(launcher.main(["flux", "get", "sources", "all"]), 2)
        self.assertEqual(dispatch.call_count, 1)
        self.assertEqual(dispatch.call_args.args[0][0], launcher.FLUX)
        self.assertNotIn(SENTINEL, self.err.getvalue())


# Real kubectl only parses local synthetic configs. These tests never call whoami
# or any network operation. An exec fixture would create a sentinel if invoked.
class InstalledParserTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which("kubectl"), "installed kubectl unavailable")
    def test_real_local_parser_never_executes_oidc_or_exec(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "machine.json"
            sentinel = Path(directory) / "plugin-ran"
            good = config()
            bad_exec = config()
            bad_exec["users"][0]["user"]["exec"] = {
                "apiVersion": "client.authentication.k8s.io/v1beta1", "command": "/bin/sh",
                "args": ["-c", 'touch "' + str(sentinel) + '"'], "interactiveMode": "Never"}
            bad_oidc = config()
            bad_oidc["users"][0]["user"]["auth-provider"] = {"name": "oidc", "config": {"id-token": SENTINEL}}
            for value, expected in ((good, 0), (bad_exec, 2), (bad_oidc, 2)):
                path.write_text(json.dumps(value))
                path.chmod(0o600)
                out, err = io.StringIO(), io.StringIO()
                with (mock.patch.object(launcher, "CONFIG", path),
                      mock.patch.object(launcher, "KUBECTL", shutil.which("kubectl")),
                      mock.patch.object(launcher.subprocess, "call") as dispatch,
                      contextlib.redirect_stdout(out), contextlib.redirect_stderr(err)):
                    self.assertEqual(launcher.main(["check"]), expected)
                dispatch.assert_not_called()
                self.assertFalse(sentinel.exists())
                self.assertNotIn(SENTINEL, out.getvalue() + err.getvalue())


if __name__ == "__main__":
    unittest.main()
