# Explicit Mac cluster launcher (OP789)

Status: implementation for review. Scope: the existing authorized Mac identity,
not a new credential, grant, login, or security policy. Work record:
[OP789](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/789).

## Problem and decision

Ambient `KUBECONFIG`, the repository's `.envrc`, and kubectl's default config can
select a human OIDC login instead of the existing Mac service account. A reminder
does not make a mistaken invocation fail. Add an explicit, versioned launcher in
`scripts/mac-cluster.py`. It never selects a credential from its arguments or
environment and never invokes login, refresh, or a credential plugin.

The fixed contract is `/Users/thaynes/.kube/haynes-ops-machine.yaml`, context
`haynes-haynes-ops-machine`, cluster and user entry
`haynes-haynes-ops-haynes-m5max-local-agent`, API server
`https://haynes.kubernetes.na-west-1.omni.siderolabs.io`, and live username
`haynes-m5max-local-agent`. These selectors already exist in the documented Mac
route. No credential bytes are stored in source, test fixtures, logs, or new files.

## Implementation

1. Accept only reviewed built-in commands and refuse caller connection, identity,
   credential, TLS, kuberc preference, and verbose-debug overrides. Do this before
   reading the config or invoking either client. The launcher is not an action
   authorization engine: writes and pod exec still require their existing approval.
2. Use fixed absolute installed kubectl/Flux paths. Run Python in isolated mode.
   Replace child `KUBECONFIG` with the fixed path, disable kuberc, and remove ambient
   Kubernetes endpoint, proxy, and alternate TLS trust variables. Do not alter the
   parent environment, files, shell configuration, or installed binaries.
3. Require a bounded, owner-only regular config file. Parse it with kubectl's
   **local-only**, redacted `config view -o json`, capturing output only in memory.
   This operation does not authenticate, run exec plugins, flatten external files,
   or contact the API. Reject parse errors without echoing its output. Require one
   exact context, cluster, user and server; the only user field is a nonempty token.
   Reject auth providers, exec, token files, certificates, impersonation, proxy and
   TLS overrides. The token value is not requested or inspected; mandatory live
   verification proves its principal. Reject the wrong current context even though dispatch also pins
   it explicitly. No fallback to a default or merged config is possible.
4. Static `check` needs no network. Every kubectl/Flux dispatch and `check --live`
   performs one bounded `kubectl auth whoami` and requires the exact username
   before the requested command. Any error or mismatch stops without retry or fallback.
5. Check file metadata for changes during preflight and immediately before
   dispatch. Pass explicit kubeconfig/context/cluster/user/server on every command.
   Forward the command's standard streams and exit status unchanged.

## Limits and rollout

This prevents accidental selection **through this launcher**. It cannot intercept
bare kubectl/Flux, a different absolute executable, another client, remote shell
commands, or direct HTTP. It is not a sandbox, credential broker, or RBAC policy.
It trusts the installed client binaries and the owner-controlled config file;
metadata checks detect ordinary concurrent edits, not an adversarial in-place
race after the final check. No credential snapshot is made to close that race.
Static checks cannot prove the token's server-side identity. Every dispatch therefore
requires live verification; only standalone `check` is offline. Server policy remains
authoritative thereafter.

Start by invoking the reviewed launcher by its absolute repo path in Mac task
handoffs. Follow-up launch integration should provide that executable as the
structured cluster-command entry point and fail closed if it is unavailable.
Any stronger interception or sandbox/tool policy is separate work requiring its
own authorization. This change installs nothing globally, changes no personal
instructions, and does not change in-pod tooling.

## Verification

Finite offline tests cover fixed arguments/environment, missing or mismatched
config, token-only shape, OIDC/exec refusal before any network call, short and long
override variants, kuberc, plugin dispatch, output secrecy, live mismatch/failure,
and config changes. They run serially in required CI. Local-parser tests use
synthetic configs with the installed kubectl when available and never authenticate.
The bounded real smoke is `check --live`, with no resource mutation or token output.
