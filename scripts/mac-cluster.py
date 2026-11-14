#!/usr/bin/python3 -I
"""Run an approved built-in command using the existing Mac cluster identity only."""

import json
import os
from pathlib import Path
import stat
import subprocess
import sys

CONFIG = Path("/Users/thaynes/.kube/haynes-ops-machine.yaml")
CONTEXT = "haynes-haynes-ops-machine"
ENTRY = "haynes-haynes-ops-haynes-m5max-local-agent"
SERVER = "https://haynes.kubernetes.na-west-1.omni.siderolabs.io"
IDENTITY = "haynes-m5max-local-agent"
KUBECTL = "/usr/local/bin/kubectl"
FLUX = "/opt/homebrew/bin/flux"
MAX_CONFIG = 1024 * 1024
PROBE_TIMEOUT = 15

# No config, plugin, unknown commands, or plugin-capable `create` subcommands.
# The set is routing scope, not approval to perform any of these operations.
COMMANDS = {
    "kubectl": frozenset(("get", "describe", "logs", "events", "top", "wait",
                          "exec", "port-forward", "rollout", "apply",
                          "patch", "delete", "scale", "annotate",
                          "label", "auth", "version", "api-resources",
                          "api-versions", "explain", "cluster-info")),
    "flux": frozenset(("get", "logs", "events", "check", "stats", "trace",
                       "tree", "reconcile", "suspend", "resume", "export",
                       "version")),
}
FORBIDDEN = frozenset((
    "--kubeconfig", "--context", "--cluster", "--user", "--server",
    "--token", "--username", "--password", "--client-certificate",
    "--client-key", "--certificate-authority", "--insecure-skip-tls-verify",
    "--tls-server-name", "--as", "--as-group", "--as-uid", "--as-user-extra",
    "--kuberc", "--v", "--vmodule", "--profile", "--profile-output",
))
SHORT_BOOL = frozenset(("-A", "-h", "-w", "-i", "-t", "-it", "-ti", "-R", "-q"))
SHORT_VALUE = frozenset(("-n", "-o", "-l", "-f", "-k", "-c", "-p"))


class Refusal(Exception):
    """A fixed, non-sensitive diagnostic, never client output or config data."""


def request(argv):
    if argv in (["check"], ["check", "--live"]):
        return "check", [], argv == ["check", "--live"]
    if len(argv) < 2 or argv[0] not in COMMANDS or argv[1] not in COMMANDS[argv[0]]:
        raise Refusal("Use check [--live] or kubectl|flux <supported command>. See docs/mac-cluster-launcher.md.")
    tool, args = argv[0], argv[1:]
    if tool == "kubectl" and args[0] == "auth" and (len(args) < 2 or args[1] not in ("can-i", "whoami")):
        raise Refusal("Only auth can-i and auth whoami are supported.")
    for index, arg in enumerate(args[1:], start=1):
        if arg == "--":
            # pflag can consume `--` as the preceding option's VALUE instead
            # of a separator (`-n -- --context=other`). Without reproducing
            # every subcommand's flag arity, refuse that ambiguous spelling.
            previous = args[index - 1]
            if previous.startswith("-") and "=" not in previous:
                raise Refusal("Ambiguous -- separator after an option. Put its value explicitly before --.")
            break  # A genuine separator: positional or remote exec arguments.
        if arg.startswith("--"):
            # kubectl normalizes underscores to hyphens before parsing flags.
            if arg.split("=", 1)[0].replace("_", "-") in FORBIDDEN:
                raise Refusal("Connection, identity, credential, TLS, kuberc and debug overrides are refused.")
        elif arg.startswith("-") and arg != "-":
            # Never admit -s/-v or arbitrary clusters such as -AsURL/-iv9.
            # Arity differs by subcommand: -f/-p take values for some but
            # mean booleans for logs, where -fsURL would smuggle a -s flag.
            # Accept exact tokens only; values must be separate argv entries.
            if arg not in SHORT_BOOL and arg not in SHORT_VALUE:
                raise Refusal("Unsupported short option; use a supported long option. Server and debug overrides are refused.")
    return tool, args, True


def environment(source):
    # Inherited Go HTTP/2 debugging can print Authorization headers. Use an
    # allowlist, not an ever-growing debug/helper/proxy/trust variable denylist.
    env = {name: source[name] for name in (
        "LANG", "LC_ALL", "LC_CTYPE", "TERM", "COLORTERM", "COLUMNS", "LINES"
    ) if name in source}
    env.update({"HOME": "/Users/thaynes", "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
                "KUBECONFIG": str(CONFIG), "KUBECTL_KUBERC": "false"})
    return env


def fingerprint():
    try:
        info = CONFIG.lstat()
    except OSError:
        raise Refusal("The fixed Mac kubeconfig is unavailable. Stop; do not use another config or log in.") from None
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid()
            or stat.S_IMODE(info.st_mode) & 0o077 or not 0 < info.st_size <= MAX_CONFIG):
        raise Refusal("The fixed kubeconfig must be a bounded, owner-only regular file. Stop; do not change credentials or permissions here.")
    return (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def unchanged(before):
    if fingerprint() != before:
        raise Refusal("The fixed kubeconfig changed during preflight. No command was launched; inspect before trying again.")


def probe(argv, env, stage):
    # Stage is a fixed caller label, never client output. A live failure can be
    # executor/network access or authentication; it does not prove bad config.
    failure = stage + " failed. Check executor/client access" + (
        " and network/identity" if stage == "Live identity verification" else " and the fixed config") + ". No command dispatched; no login, fallback or automatic retry."
    try:
        result = subprocess.run(argv, env=env, stdin=subprocess.DEVNULL,
                                capture_output=True, timeout=PROBE_TIMEOUT, check=False)
        if result.returncode or len(result.stdout) > MAX_CONFIG * 4:
            raise Refusal(failure)
        return json.loads(result.stdout)
    except (OSError, subprocess.TimeoutExpired, ValueError, UnicodeError):
        # Client output, exception strings and argv may contain credentials.
        raise Refusal(failure) from None


def only_entry(config, key, name, body):
    rows = config.get(key)
    if (not isinstance(rows, list) or len(rows) != 1 or not isinstance(rows[0], dict)
            or rows[0].get("name") != name or not isinstance(rows[0].get(body), dict)):
        raise Refusal("The fixed kubeconfig has an unexpected context, cluster or user. No command launched.")
    return rows[0][body]


def validate(config):
    if not isinstance(config, dict) or config.get("current-context") != CONTEXT:
        raise Refusal("The fixed kubeconfig's current context is not the expected Mac context. No command launched.")
    context = only_entry(config, "contexts", CONTEXT, "context")
    cluster = only_entry(config, "clusters", ENTRY, "cluster")
    user = only_entry(config, "users", ENTRY, "user")
    if (context.get("cluster") != ENTRY or context.get("user") != ENTRY
            or set(context) - {"cluster", "user", "namespace"}):
        raise Refusal("The fixed context does not select the expected cluster and user. No command launched.")
    if cluster != {"server": SERVER}:
        raise Refusal("The fixed cluster server or TLS/proxy configuration is unexpected. No command launched.")
    token = user.get("token")
    if (set(user) != {"token"} or not isinstance(token, str) or not 0 < len(token) <= 16384
            or any(character.isspace() for character in token)):
        raise Refusal("Only the existing inline token is supported. Exec, OIDC, token files, certificates and impersonation are refused.")


def selectors():
    return ["--kubeconfig=" + str(CONFIG), "--context=" + CONTEXT,
            "--cluster=" + ENTRY, "--user=" + ENTRY, "--server=" + SERVER]


def launch(argv):
    tool, args, verify = request(argv)
    env = environment(os.environ)
    before = fingerprint()
    # config view is a local parser, not an API/auth call. Never --raw, --flatten
    # or print its result. The token is redacted; whoami verifies the principal.
    validate(probe([KUBECTL, "--kubeconfig=" + str(CONFIG),
                    "config", "view", "-o", "json"], env, "Static config inspection"))
    unchanged(before)
    if verify:
        result = probe([KUBECTL, *selectors(), "auth", "whoami",
                        "--request-timeout=10s", "-o", "json"], env, "Live identity verification")
        if (not isinstance(result, dict) or not isinstance(result.get("status"), dict)
                or not isinstance(result["status"].get("userInfo"), dict)
                or result["status"]["userInfo"].get("username") != IDENTITY):
            raise Refusal("Live identity does not match the expected Mac service account. No command launched; do not log in or switch credentials.")
        unchanged(before)
    if tool == "check":
        print("mac-cluster: " + ("live identity verified: " + IDENTITY if verify
                                 else "static selection verified; live identity not checked"))
        return 0
    unchanged(before)
    executable = KUBECTL if tool == "kubectl" else FLUX
    try:
        # No shell, no retry, no captured command output. Exec/log streams remain
        # interactive, and the child exits with its original status.
        return subprocess.call([executable, *selectors(), *args], env=env)
    except OSError:
        raise Refusal("The fixed client could not launch. Stop; do not select another executable or login route.") from None


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    if argv == ["--help"]:
        print(__doc__ + "\nUsage: mac-cluster.py check [--live]\n       mac-cluster.py kubectl|flux <supported command> [args]\nSee docs/mac-cluster-launcher.md. This does not intercept bare client commands.")
        return 0
    try:
        return launch(argv)
    except Refusal as error:
        print("mac-cluster: refused: " + str(error), file=sys.stderr)
        return 2
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    sys.exit(main())
