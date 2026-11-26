# Mac cluster launcher

Use `scripts/mac-cluster.py` for authorized kubectl and Flux work on the existing
Mac. It refuses a wrong configuration before authentication, then verifies the
live service-account username before **every** command. Work record:
[OP789](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/789).

## Use

From this checkout:

```sh
# Local inspection only: no network, login or credential plugin.
/usr/bin/python3 -I -B scripts/mac-cluster.py check

# One bounded live identity check, no resource changes.
/usr/bin/python3 -I -B scripts/mac-cluster.py check --live

# Both commands first perform the same mandatory live identity check.
/usr/bin/python3 -I -B scripts/mac-cluster.py kubectl get namespace dev
/usr/bin/python3 -I -B scripts/mac-cluster.py flux get kustomizations -A
```

From another repository or task, use the **absolute path to this reviewed
checkout's script**. Do not rely on an alias, a shell function, a changed PATH,
`direnv`, or the current working directory to select it. The executable shebang
also uses the installed `/usr/bin/python3` in isolated mode.

This uses only the existing file `/Users/thaynes/.kube/haynes-ops-machine.yaml`,
context `haynes-haynes-ops-machine`, cluster/user entry
`haynes-haynes-ops-haynes-m5max-local-agent`, and server
`https://haynes.kubernetes.na-west-1.omni.siderolabs.io`. The server must report
username `haynes-m5max-local-agent`. The installed executables are
`/usr/local/bin/kubectl` and `/opt/homebrew/bin/flux`.

The config must be a nonempty regular file owned by the invoking user with no
group/other permissions, at most 1 MiB. It must contain exactly the expected
context, cluster and inline-token user. No exec/OIDC provider, token file,
client certificate, impersonation, alternative CA, TLS override or proxy is
accepted. The launcher inspects only redacted local config metadata. It never
asks kubectl for raw credentials and never prints config/probe output.

Child processes receive a minimal environment: fixed `HOME=/Users/thaynes`,
`PATH=/usr/bin:/bin:/usr/sbin:/sbin`, `KUBECONFIG`, and `KUBECTL_KUBERC=false`.
Only `LANG`, `LC_ALL`, `LC_CTYPE`, `TERM`, `COLORTERM`, `COLUMNS` and `LINES`
can be inherited for locale and terminal behavior. All other variables are
absent, including Go HTTP/2 debugging (which can log authorization headers),
helper/loader settings, proxies, alternative TLS trust and Kubernetes endpoints.
All five selectors are also explicit command arguments. Parent environment and
all configuration files remain unchanged.

## Supported commands

- kubectl: `get`, `describe`, `logs`, `events`, `top`, `wait`, `exec`,
  `port-forward`, `rollout`, `apply`, `patch`, `delete`, `scale`, `annotate`,
  `label`, `auth can-i`, `auth whoami`, `version`, `api-resources`, `api-versions`,
  `explain`, `cluster-info`.
- Flux: `get`, `logs`, `events`, `check`, `stats`, `trace`, `tree`, `reconcile`,
  `suspend`, `resume`, `export`, `version`.

These are recognized built-ins, **not permission to perform them**. Existing task
approval, RBAC and safety rules still apply, including for reads of sensitive
resources, runtime writes, port-forwards and remote exec. `config`, plugin/login
commands, `create`, Flux bootstrap/install, and unknown verbs are not supported.
`diff` is excluded because its external diff helper is outside this launcher's
fixed-client contract. `cp` is also outside the initial supported command set.
Extend this list only with reviewed routing tests. The launcher never runs a
shell to dispatch a command.

Connection and identity overrides are refused even when their values match:
`--kubeconfig`, `--context`, `--cluster`, `--user`, `--server`, `-s`, token/password/
certificate/TLS options, impersonation flags, and `--kuberc`. Debug verbosity and
profiling flags are refused. Underscore variants and arbitrary short-option
clusters are refused too. Use separate short options or long options; common
short flags (`-n`, `-o`, `-l`, `-f`, `-k`, `-c`, `-p`) and `-it` work. Short
flags must be exact tokens: use `-n dev`, not `-ndev`. Some flags have different
arity by command (`logs -f` is boolean), so attached values or other clusters
could conceal an override and are refused.
A `--` directly after an option without `=` is refused because the client could
consume it as that option's value. Put values explicitly before the separator,
for example `exec -n dev pod -- command`. Arguments after a genuine `--` are
positional or belong to a remote exec command, so they are preserved unchanged. Remote commands are outside this local selection guard.

## Failure and enforcement boundary

A missing file/client, mismatch, invalid config, or failed/timed-out identity
check exits 2 without dispatch, login, refresh, fallback, or retry. Inspect the
specific problem. Errors identify static config inspection versus live identity
verification; a live failure can be executor/network access or authentication,
not necessarily the config. Read-only execution sandboxes may block network or
client cache access. The launcher does not request or change executor permissions.
Do not use the human kubeconfig, refresh Omni login, install
credentials or silently bypass the launcher. Client commands preserve their own
streams and exit code. A request failing after dispatch is not retried either.

This script is not installed globally and **does not intercept bare kubectl or
Flux**. Repo instructions and task launchers must invoke it explicitly. In-pod
clients retain their own existing configuration; this Mac file is not copied into
pods. No RBAC, security policy, access grant, shell setting, credential or personal
instruction is changed.

The launcher trusts installed binaries and the owner-controlled file. It detects
ordinary file changes during preflight, but does not close a malicious in-place
edit race after the last check or bind a refreshed server-side identity forever.
There is no credential snapshot. A separate structured command-tool integration
that exposes only this entry point would make accidental bypass harder. Enforcing
that against arbitrary shell/direct-client use needs separately reviewed tool or
sandbox policy. Do not claim this PR provides that enforcement.

## Tests and rollout

```sh
nice -n 19 /usr/bin/python3 -I -B -m unittest discover -s scripts -p 'test_mac_cluster.py' -v
```

The serial tests use synthetic credentials and mocked client dispatch. When
kubectl is installed, a local-only test also parses synthetic exec/OIDC configs
and proves that no plugin is run. The required CI Test job runs this suite.
No test needs cluster access. A separate manual `check --live` is the bounded
real-identity smoke. See [the design](../.agents/sagas/distributed-dev-env/designs/mac-cluster-launcher.md).

After review and required CI, use the merged checkout's absolute script path in
Mac cluster task handoffs. Follow-up integration should make that path a required
structured launch input and refuse a missing launcher. Do not install global
wrappers or rewrite shell/PATH/kubeconfig settings as part of this change.
