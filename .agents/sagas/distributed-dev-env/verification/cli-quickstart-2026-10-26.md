# CLI quick-start verification — 2026-10-26

Scope: source/documentation-only verification for NIGHT92-CLI-QUICKSTART-20261026-01.
It neither launched a session nor contacted an API, Kubernetes, a provider, or
an external release service.

## Baseline and observations

- Source baseline inspected: `4139a7271b4eafd0b6db391b1a0cc73328524321`.
- This worker's installed client: `agent-run v2.45.0` at `/usr/local/bin/agent-run`,
  commit `86cef5bb619bbde29610e3436299a3a4362b7a88`, Go 1.27.2,
  linux/amd64.
- The coordinator's separate preflight reported v2.44.0 at
  `4c4dd5b5091bff0ad2b726018bb520d239b9db8f`. These are local observations,
  not a claim that either version is installed or deployed everywhere.
- No release asset or general installation route was demonstrated. The quick
  starts now require selecting an already supplied v2 executable and recording
  its `version`; they do not infer one from current source or an old filename.

## Evidence inspected

Safe local commands run, with no mutable subcommand execution:

```text
agent-run version
agent-run --help
agent-run help run
agent-run help list
agent-run help show
agent-run help log
agent-run help fleet
agent-run help attach
agent-run help detach
```

The v2.45.0 help confirms the documented launch forms; `list`, `show`, `log`,
and `fleet`; and owner-side `attach`/`detach`. `help run` confirms a full model
ID, model-appropriate effort, `--project` only for a new task, and separate
`--profile` semantics.

Source anchors inspected at the baseline:

- `internal/agentrun/help.go`: top-level discovery/CA help and launch help.
- `internal/agentrun/creds.go`: projected-session identity, in-cluster
  ServiceAccount minting, explicit CA precedence, and existing-kubeconfig
  laptop route.
- `internal/agentrun/create.go`: task/local/interactive exclusion, project-task
  restriction, provider/model handling, and profile request field.
- `internal/agentrun/attach.go`: attach/detach caller restriction and
  Kubernetes-rights requirement.
- `internal/apiserver/validate.go`: child profile inheritance and the explicit
  workbench-created Codex remote-host exception (omit the profile; the server
  selects the default profile).

## Correction and limits

The demonstrated drift was the hard-coded historical
`agent-run-v2.35.0` filename and the workflow guide's instruction to build
current source as a v2 client. Both could select a client different from the
installed/released one. The updated docs distinguish ordinary `--repo` work
from accepted `--project` managed tasks, preserve the v1 launcher, and state
that neither `--repo` nor native thread start selects a profile or an accepted
project snapshot.

No live catalog admission, provider availability, model service, profile
authorization, API reachability, CA file availability, or installed deployment
state was tested. Actual served model, usage, and cost are Unknown. The only
configured worker model supplied in this work order was `gpt-5.6-terra` at
`medium`; this verification did not probe its availability or send it to a
service.
