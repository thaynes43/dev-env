# Launch profile preflight

This is the launch-correctness guidance for [incident #168](https://work.haynesnetwork.com/work_packages/168): a native coordinator thread was used for an Unraid planning task while it was attached to a `full`-profile host. The image contained `ssh` and `hw-ssh`, but that did not make a hardware key, LAN egress, or a hardware-operation approval available.

The current profiles, their ServiceAccounts, profile labels, mounts, and standing-grant selectors are the authority for what a newly created pod can receive. This document maps the existing launch paths to those facts; it does not add a profile policy, an approval path, or a capability grant.

## Capability map

| Declared work scope | Existing profile selection | What must still be true before use |
| --- | --- | --- |
| Routine repository work and public-web access | Use the caller's resolved profile. For a trusted client, an omitted profile resolves through the templates' default (`full` unless the deployed templates say otherwise). | Normal session and repository authorization rules apply. A program included in the image is not evidence of a credential or network capability. |
| Bounded cluster reads or routine runtime verbs | Use the caller's resolved profile; this scope alone does not select `workbench`. | `full` retains its documented bounded cluster/runtime access. Check the required operation's existing policy instead of treating all cluster work as a profile escalation. |
| Explicit workbench-only control or hardware/host operations | Start from, or dispatch to, an existing authorized `workbench`-profile session when the work record explicitly names that role. | `workbench` uses `dev-env-workbench`, `DEV_ENV_WORKBENCH`, and its workbench-specific mounts and profile labels. The required grant or standing policy, egress, target authorization, and any action-specific approval remain separate checks. |
| Accepted-project task from a configured coordinator host | The existing coordinator API ceiling permits only `dev`, task mode, S/M sizes, and a declared project. | `dev` is the accepted-project executor profile for this narrowly configured route; it is not an automatic substitute for `workbench`. |
| Summoned automation | `ops` is reserved for the restricted `dev-env-ops`/summoned-session route. | `ops` is not a manual escalation profile and is excluded from the ordinary grant-policy match. |
| A remote Codex coordinator host | A workbench-session origin must omit `profile`, which selects the templates' default. A human or trusted client may explicitly select a valid profile. | A workbench-origin host does not inherit workbench capability. Every later native thread or turn has the host's already-selected resolved profile; it cannot choose a Kubernetes profile. Dispatch hardware/host work to an authorized workbench session instead of treating a coordinator host as workbench. |

The `hw-ssh` key is mounted only for `workbench` sessions. General agent SSH certificates remain unfinished, and the keeper's Proxmox token minting is a separate path. Neither the image's binaries nor an existing host's profile replace those controls.

The workbench-origin Codex-remote default-profile exception has a narrower
purpose: it stops that phone-driven host from inheriting the workbench control
identity, including its pod-exec capability. It is not a hardware/SSH
eligibility check. In particular, that downscoped remote host must not be
treated as having the workbench `hw-ssh` mount or its LAN-policy eligibility.

## Entry-point inventory

| Entry point | Where profile is selected | Current protection and limit |
| --- | --- | --- |
| Native Codex thread or turn on an existing host | Nowhere in the native thread/turn operation. The host's pod was already created with its resolved profile. | The dispatcher must select an already-authorized host and inspect its resolved profile and relevant existing capability metadata before starting the task. Native start cannot change that profile. |
| `agent-run` CLI | `--profile` is the only structured profile input, sent in `CreateSessionRequest`. An omitted profile uses the deployed template default for a trusted client, the parent's resolved profile for a session child, and the deployed template default for the workbench-origin Codex-remote exception. | The CLI has no declared capability or role field and must not scan the prompt for words such as `ssh`, `Unraid`, or `hardware`. A child normally inherits its parent's profile, so a session cannot self-upgrade. |
| Managed session API | `CreateSessionRequest.profile`, validated against the deployed templates. | A session child is pinned to its parent profile. The explicit Codex-remote exception above is enforced by the API only for a workbench-session origin: it must omit the profile and then uses the deployed template default. |
| `agent-run mcp` toolbox | It does not select a profile or launch a session. | Its three tools only use existing grant routes; they do not approve or install grants. An external terminal wrapper has the same already-selected host/CLI profile rules and no inferred selector. |
| Phone/browser UI | No v2 web UI currently selects a profile. Phone remote-control opens an existing session or host. | A future UI needs a reviewed structured role/capability contract before it can choose profiles. It must not claim enforcement merely by displaying this map. |

These statements describe the existing contracts in `internal/agentrun/create.go`, `internal/apiserver/apiv1/types.go`, and `internal/apiserver/validate.go`. The Codex-host exception has a focused regression in `internal/apiserver/workbench_test.go`. The deployed workbench behavior, network/Proxmox access, and `hw-ssh` mount are documented in the [v2 quick start](v2-quickstart.md#what-a-workbench-session-can-do).

## Existing enforced cases

The following are server-side checks on the structured `profile`, `agent`, and
`mode` fields. They occur before the API creates an `AgentSession`; `agent-run`
does not substitute its own profile policy.

| Explicit request | Existing result | Remedy |
| --- | --- | --- |
| A session tries to create a child with a profile other than its own | `403`: `a session's child runs on its parent's profile <parent>, not <asked>`. | Use the inherited profile, or have a separately authorized trusted client dispatch the intended profile. A child cannot elevate itself. |
| A workbench session creates a Codex remote host and sends any `profile` | `403`: `a codex host started by a workbench session runs on the default profile; omit the profile`. | Omit `--profile`; that creates the intentionally downscoped host on the deployed template default. Dispatch hardware/host work separately to an authorized workbench session. |
| A client explicitly names a profile missing from the deployed templates | `422` profile validation; the profile lookup says it is not in the templates. | Choose a current deployed profile after inspecting its metadata. Do not add or guess a profile at launch time. |

An eligible trusted client may still explicitly create a workbench session. That
is the structured selection route; its success says only that the session used
the selected template profile. It does not establish the availability of a key,
an egress path, a target account, or an approval.

## Managed Codex task admission

Managed Codex execution is a distinct operator task route. It is not the native
thread/turn path and it is not a fallback when native or external dispatch is
unavailable. A successful local clone, an authenticated CLI, or a reachable
host does not establish managed-task admission.

Before a launcher submits this route, it needs all of the following existing,
server-side prerequisites:

1. Use task mode with an explicit `--project`. The project must be in the
   accepted server catalog and must yield an accepted server-side project
   snapshot. `--repo` is optional only to select a repository declared by that
   project; it does not nominate an arbitrary local or reachable repository.
   Omit `--repo` only when the accepted project has one selectable repository.
2. The operator's managed-Codex capability must already be enabled. It is not a
   CLI flag and a caller must not attempt to enable it, select a fallback
   profile, or retry another route to bypass its absence.
3. Supply a valid Codex model and use a supported task timeout when a limit is
   needed: `--timeout` maps to `limits.timeout`. Omit `--max-turns`; native
   Codex execution has no exact max-turns contract.

| Server refusal | Meaning and safe next step |
| --- | --- |
| `422 agent: managed codex tasks require the enabled feature and an accepted project snapshot` | This refusal arises when `--project` is absent, task mode was not requested, or the feature is off after the server accepted the project snapshot. An unavailable project/snapshot instead fails earlier with the project `422` below. Only when the work record names an already authorized project and task mode should the launcher use those declared arguments; otherwise stop. Do not infer catalog acceptance, enable a feature, alter a profile, or route the task to a native host. |
| `422 project: accepted project catalog is not configured` or `accepted project/repository is unavailable or invalid` | Stop. The requested project/repository is not proven by the server catalog. Obtain an owner-delivered catalog/launch decision; do not treat `--repo`, local checkout state, CLI login, or network reachability as acceptance. |
| `422 limits.maxTurns: native Codex exec has no exact max-turns contract; use limits.timeout` | Remove `--max-turns` and use a positive `--timeout` only if the task's existing scope permits a timeout. This changes no admission or profile decision. |

A Codex remote host has a separate bootstrap contract: it runs the pairing daemon
and therefore does not require a managed-Codex feature or accepted project
snapshot. That fact does not make the host a managed task executor, select or
change its resolved profile, or authorize it as a workaround for a managed-task
`422`.
The configured coordinator route remains separate as well: its accepted-project
tasks retain the `dev` profile ceiling described above, including its own caller
and size restrictions. Session children still retain the parent-profile ceiling.

## Dispatcher preflight

Run this before a native task/thread start or before asking another launcher to create a session:

1. Read the work record's explicit scope or role. Do not infer hardware or host-operations intent from a task name, prompt, repository, binary, or model.
2. For explicitly declared hardware/host-operations scope, select an existing authorized `workbench` session/host. Before task start, use an authorized read-only client to inspect `agent-run show <session> -o json` and confirm its `profile`, then compare it with the deployed template's ServiceAccount, labels, mounts, and profile policies. A missing stored `profile` field is not proof of `workbench` or any capability: resolve it only through the deployed template default for that caller's route. Do not read a key or other credential to infer capability.
3. Resolve an omitted profile by caller before launch: a trusted client gets the deployed template default; a session child gets its parent's resolved profile; and a Codex remote host created by a workbench session gets the deployed template default. If the dispatcher cannot make the authorized host selection and readback, stop rather than infer `workbench` or a capability from an omitted profile.
4. For `agent-run`, make profile selection explicit only where that client is authorized to do so. Preserve the server's parent-profile ceiling and its Codex-remote default-profile rule. Never translate `full` to `workbench` automatically.
5. Separately verify the required existing grant/policy, egress, target authorization, and action-specific approval. Do not inspect or print credential material while doing this preflight.
6. Where the task has no structured scope/role, state the limitation to the requester and obtain a dispatch decision outside the native thread/turn path. Current clients cannot enforce intent from free-form text.

This preflight does not alter a running native host, create a session, request a grant, mint a credential, or make a host operation authorized. It records the boundary that was missing in #168: profile selection happens before task start, and a profile alone never proves a usable key, network path, or approval.
