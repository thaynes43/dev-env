# dev-env v2: optional external CLI check

**Optional, following Tom's 2026-10-08 correction (Q-17).** Tom's current workflow
uses `agent-run` or asks agents to start sessions. This external-machine check
does not block plan 02; no laptop setup or test is requested from Tom.
The client is built, signed and deployed (#103, #104, haynes-ops #3579), but
an actual external-machine run remains unverified.

Use these instructions if an external CLI caller later needs the path. It needs
`git`, `make`, Go, `kubectl`, and a
working admin kubeconfig for the main cluster. Keep the kubeconfig, tokens and
agent login files on their current machines; share only pass/fail results.

## Check the cluster context

Choose the main cluster's admin context from `kubectl config get-contexts`.
Use its name below in place of `MAIN_CONTEXT`.

```sh
kubectl --context MAIN_CONTEXT -n dev-env-system get svc dev-env-operator
kubectl --context MAIN_CONTEXT -n dev-agents get configmap dev-env-api-ca -o name
kubectl --context MAIN_CONTEXT auth can-i create serviceaccounts/dev-env-human --subresource=token -n dev-env-system
kubectl --context MAIN_CONTEXT auth can-i create pods --subresource=portforward -n dev-env-system
kubectl --context MAIN_CONTEXT auth can-i create pods --subresource=exec -n dev-agents
```

The first two commands must find the Service and ConfigMap. Each permission
check must answer `yes`. If one fails, stop and report which check failed.
Do not mint a token by hand or paste kubeconfig contents into chat.

## Build the laptop binary

Use a task worktree of `thaynes43/dev-env`, rather than editing a canonical
clone. For an existing clone at `~/repos/dev-env`:

```sh
git -C ~/repos/dev-env fetch origin
git -C ~/repos/dev-env worktree add ~/work/dev-env-laptop-acceptance-1008 -b agent/laptop-acceptance-1008 origin/main
cd ~/work/dev-env-laptop-acceptance-1008
make build-agent-run-darwin
./bin/agent-run-darwin-arm64 version
```

That target builds for an Apple Silicon Mac. On another platform, build the
CLI with `make "$PWD/bin/agent-run"` and use `./bin/agent-run` below. If the canonical
clone is elsewhere, use its path. If it is missing, clone
`https://github.com/thaynes43/dev-env.git` into `~/repos/dev-env` first.

## List and attach

Use a terminal on the laptop for the attach check. Run these commands with the
binary just built, in a shell without API URL, token-file or CA-file overrides:

```sh
unset DEV_ENV_API_URL DEV_ENV_API_TOKEN_FILE DEV_ENV_API_CA_FILE
unset AGENTD_API_URL AGENTD_API_TOKEN_FILE AGENTD_API_CA_FILE
./bin/agent-run-darwin-arm64 fleet --context MAIN_CONTEXT
./bin/agent-run-darwin-arm64 list --context MAIN_CONTEXT
./bin/agent-run-darwin-arm64 --context MAIN_CONTEXT --repo dev-env --local --model claude-haiku-4-5 --size S --wait 2m -o name
```

The create prints a session name. Replace `SESSION_NAME` below with it:

```sh
./bin/agent-run-darwin-arm64 show --context MAIN_CONTEXT SESSION_NAME
./bin/agent-run-darwin-arm64 list --context MAIN_CONTEXT --mine
./bin/agent-run-darwin-arm64 attach --context MAIN_CONTEXT SESSION_NAME
```

`show` must report phase `Running` and parent `dev-env-system/dev-env-human`;
`list --mine` must include it. A printed name alone does not prove the pod
started. The attach must show the Claude TUI. Detach with **Ctrl-b, d**;
the session keeps running. Then clean up only this test session:

```sh
./bin/agent-run-darwin-arm64 reap --context MAIN_CONTEXT SESSION_NAME
```

The operator rescues the session before removing its pod and volume. Report
the session name and whether fleet, list, the human parent, attach, detach and
reap passed. Do not send the TUI transcript or any credentials. The coordinator
will check rescue completion and unchanged UIDs/restarts for existing pods.

`agent-run` reads the pinned CA and mints its human token automatically. Each
command opens a port-forward bound to loopback and closes it on exit. TLS checks
`dev-env-operator.dev-env-system.svc.cluster.local`, even though the connection
uses the forwarded local port. If a kubeconfig lives outside the default path,
add `--kubeconfig /absolute/path/to/config` to each command.
