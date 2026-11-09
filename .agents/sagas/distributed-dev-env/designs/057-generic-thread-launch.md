# DESIGN-057: Generic thread launch on an existing v2 host

- **Status:** Proposed, 2026-11-09. Codex adapter in source; live acceptance pending.
- **Tracking:** [Story OP489](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/489),
  under [Epic #91](https://work.haynesnetwork.com/work_packages/91). It supersedes
  [OP272](https://work.haynesnetwork.com/work_packages/272)'s runtime activation
  (journal 5990) and meets [OP481](https://work.haynesnetwork.com/work_packages/481)
  acceptance item 2 for this route.
- **Origin:** Tom, 2026-11-09: "Yes build it but meatball has been starting these
  so check to see what's built already don't duplicate what they have but you need
  that ability too. You need to be able to start Grok too and other providers as we
  bring them online so this needs to be generic tooling agents can use in v2".
- **Builds on:** [DESIGN-038](038-remote-control-launch.md) (the launch phase
  model and `rc start`), `scripts/codex-clean-session.py`
  ([docs](../../../../docs/codex-clean-session.md)), the `agent-run pair` exec
  route, and agentd's peer-verified app-server observer (`codex_host_rpc.go`).

## 1. Goal and non-goals

An authorized caller (Tom, the v1 pod or a workbench session) runs one command
and gets a fresh interactive thread that Tom can see in his provider's phone app:

```bash
agent-run thread start --agent codex --host <codex-host-session> \
  --model gpt-6-astra --effort xhigh --handoff task.md [--repo dev-env] [--wait 5m]
agent-run thread start --agent claude --project dev-env \
  --model claude-opus-5-5 --effort xhigh --handoff task.md
```

It prints one `thread-launch.v1` JSON result whose phases say how far the
launch got. It reports success only when the first owner-visible response
exists, never on queue acceptance, a live pod or a running client alone.

Non-goals: a new grant, RBAC rule, network policy or caller class; pairing or
login; creating a host; enabling `--enable-codex-rc-thread` (it stays off: one
thread per host, ever, OP272 journal 5983); `codex exec` or `codex queue`
(their `exec` source is hidden from the phone's default list, OP482);
a caller-picked sandbox or approval flag (section 6a); resuming or forking a
thread; printing the handoff or the response text.

## 2. Inventory: what exists and what this reuses

Searched on 2026-11-09: dev-env `scripts/`, `scripts/trials/*`,
`internal/codexrc`, `internal/agentrun` (`rc*`, `pair*`), `internal/agentd`
(`codex_rc_*`, `codex_remote*`, `codex_host*`, `cursor_*`); OpenProject
"launch", "thread", "relay", "clean session", "Meatball", "Mac Sol" and
owner `01a17599` (only #237, a check-in tool); haynes-ops `.agents/` (no
thread-launch runbook).

| Piece | What it does | Status | Here |
| --- | --- | --- | --- |
| `scripts/codex-clean-session.py` (#731) | Validates inputs, reserves a one-use name, runs `codex --remote unix://<socket> -C <repo> -m <model> -c model_reasoning_effort=<e> --no-alt-screen -- <task>` in a detached tmux session, checks the pane is alive. Never resumes, pairs or retries. | Merged, proven 2026-10-30; was not in the agent image | **Reused as the Codex create step**, with one change: it now always passes `--dangerously-bypass-approvals-and-sandbox` (section 6a). The image now ships it at `/usr/local/lib/dev-env-codex/`. |
| `agent-run pair` / `sessionExecBase` | Execs `agentd ctl …` in a session pod over SPDY; refuses inside an ordinary session pod (D-19); workbench and v1 pass by RBAC. | Live | **Reused as the only route and caller check.** |
| `internal/agentd/codex_host_rpc.go` | Peer-PID-verified WebSocket client to the daemon's control socket; `initialize`, `remoteControl/status/read`. | Live | **Reused** for host readiness and the new read-only `thread/list`, `thread/turns/list`, `thread/items/list` observer. |
| `internal/agentd/codex_remote.go` | The host's daemon supervisor, `ctl codex-pair`, the pair-issued marker. | Live | **Reused** for the daemon PID and pairing knowledge. |
| `agent-run rc start` / `rc status` (DESIGN-038 2a, #688) | Claude: creates a new remote session, follows pod, agent, RC registration and link, sends the handoff once. Codex refused as not implemented. | Live | **Reused unchanged as the Claude adapter**; its result is translated. |
| `rc fresh` / `--enable-codex-rc-thread` (#696) | One `thread/start` per host, ever, with a deployment-pinned target. | Off | Not used. Stays off. |
| `internal/codexrc` (#691) | Pure Mac project discovery and pairing-evidence mapping. | Source only | Not needed: a v2 host's thread cwd is a path on that host, not a Mac project. |
| `scripts/cursor-acp-session.py`, `cursor-interactive-session.py`, `internal/agentd/cursor_*` | Cursor ACP client and tmux helper; agentd scaffolds deliberately unreachable. Cursor is not an AgentSession provider. | Source only, unqualified | Not called. The Cursor/Grok adapter refuses until qualified (section 6). |
| `scripts/trials/pilot_rendezvous` (DESIGN-027) | One-shot headless dispatch trial harness. | Trial | Not a thread launch; not used. |
| Meatball's launches (OP481) | Phone, the clean-session wrapper inside a host, and `codex exec` (hidden source). | Manual | This command is the supported, repeatable version of the first two. |

## 3. Command and route

`agent-run thread start` takes `--agent` (provider), `--model` (full id),
`--effort`, `--handoff FILE` (one rule for every provider: UTF-8, 1 to 4096
bytes, not blank, no control character but newline and tab), `--wait`
(default 5m, at most 10m) and the provider's target:

- **Codex** takes `--host NAME`, an existing `codex --interactive` session, and
  optional `--repo` (default: the host's repo) and `--name` (default
  `thread-<UTC mmdd-HHMMSS>`). agent-run checks the host is Running, agent
  `codex`, mode `remote`, then execs `agentd ctl thread-start` there through
  `sessionExecBase`, the route `agent-run pair` uses. The handoff travels on the
  exec's stdin, never in an argument. agentd prints the result; agent-run
  checks it is one valid `thread-launch.v1` document whose exit code matches
  its outcome. A host whose agentd predates `thread-start` prints nothing valid
  and agent-run says so (`host_unsupported`: move the host to a newer image). Inside the host the
  wrapper passes the handoff to the TUI as its initial prompt argument, as it
  always has; it is never in agent-run's or agentd's argv, log or output.
- **Claude** takes `--project` and optional `--repo`; `--host` is refused.
  Claude's supervisor owns one conversation per session (DESIGN-038), so a fresh
  Claude conversation is a fresh remote session: the existing `rc start` flow,
  which measured about two minutes on 2026-11-09 (#1002).
- **Cursor and Grok** are refused before any request (section 6).

## 4. Adapter interface

`internal/threadlaunch` is a pure package (standard library and `apiv1` only, so
agent-run stays Kubernetes-free). It holds the provider registry, a phase
recorder and one driver:

```go
type Adapter interface {
    Preflight(ctx, *Recorder) error                 // host_ready, paired; nothing created
    Create(ctx, *Recorder) error                    // client_started; at most once, never retried
    Visible(ctx, *Recorder) (done bool, err error)  // thread_created, source_visible; polled
    FirstResponse(ctx, *Recorder) (done bool, err error) // first_response; polled
}
func Run(ctx, Adapter, Options) apiv1.ThreadLaunchResult
```

An adapter returns a `*Stop` (outcome, fixed code, phase) to end the run. The
driver polls `Visible`, then `FirstResponse`, every 2 seconds until done or the
wait ends. `ErrNotObservable` from `FirstResponse` ends the run as `visible`
with that phase `unknown`. Every route is one adapter: the Codex adapter runs
in the host (agentd), the Claude adapter in agent-run, and a new provider adds
one adapter and one registry row.

## 5. Codex adapter (agentd `ctl thread-start`)

1. **Preflight.** The session must be a Codex remote host. The keeper's access
   projection must be current, the daemon live with a PID, and
   `remoteControl/status/read` (peer-verified) `connected`: `host_ready`.
   Pairing: if this host never issued a pairing code, no phone can have paired,
   so `paired` is `required` and the run stops `action_required` (remedy:
   `agent-run pair <host>`, Tom types the code) before anything is created.
   Otherwise `paired` is `unknown` with reason `pair_code_issued`: Codex has no
   read for the phone's side of the pairing. The cwd is `~/repos/<repo>` on the
   host and must be a directory without symlinks. The handoff should ask the
   thread to make its own worktree, as on the phone.
2. **Snapshot.** `thread/list` for that cwd, all source kinds, newest 20.
3. **Create.** Run the packaged wrapper once, handoff on stdin, with the host's
   control socket, `CODEX_BIN` and a scrubbed environment (the managed launch's
   unset list). The wrapper always passes
   `--dangerously-bypass-approvals-and-sandbox` (section 6a). Its states map to `client_started`: `client-running-unverified`
   is `ok`; `invalid-input`, `name-consumed`, `prerequisite-unavailable` and
   `unsupported-client` fail before a launch; anything else is `unconfirmed`
   and never retried.
4. **Visible.** A thread not in the snapshot, with the same cwd and
   `createdAt` at or after the launch, is `thread_created`. More than one
   candidate is narrowed by the handoff matching its `preview`; still more is
   `unconfirmed` (`thread_ambiguous`). A thread whose non-empty preview does
   not match the handoff, or a sub-agent's thread, is never adopted. The same id must then be in the default
   `thread/list` (no `sourceKinds`: the interactive sources the phone lists):
   `source_visible` `ok`, reason the source (for example `vscode`). Present only
   in the all-sources list is `hidden` with code `hidden_source`: a failure,
   so an exec-sourced thread can never pass.
5. **First response.** `thread/turns/list` (first turn, ascending, items not
   loaded) gives `handoff` `ok` once a turn exists. `thread/items/list` (that
   turn, newest 8) with an `agentMessage` of non-empty text is
   `first_response` `ok` and the outcome `responded`. A `failed` or
   `interrupted` turn is `failed` (`turn_failed`). A completed turn without an
   observed message stays `unknown`.

The observer uses a fresh connection per poll, makes only read calls, and allows
1 MiB frames. If no thread appears, the message says the client may be waiting
at a folder-trust or other prompt in the named tmux session: an authorized
person decides, and nobody sends keys while a native question is pending.

## 6. Claude and Cursor/Grok adapters

**Claude.** `Create` runs `rc start` with its JSON redirected to the adapter.
Mapping: `host_ready` from `pod_ready` and `agent_started`; `paired`
`not_applicable`; `client_started` and `thread_created` from `agent_started`
(conversation id); `source_visible` from RC registration with a validated link
(the link is copied); `handoff` from rc's `handoff`; `first_response` `unknown`
(`provider_observation_unavailable`). A non-success rc outcome keeps its code.

**Cursor/Grok.** Refused with outcome `blocked`, code `provider_not_qualified`:
Cursor is not a v2 AgentSession provider and has no qualified phone-visible
thread route. Grok runs through `cursor-agent`, so `--agent grok` is the same
adapter. Enabling it later means one adapter plus its own qualification story.

## 6a. Permission mode is part of the contract

Tom, 2026-11-09, relayed by Meatball: "Your not starting sessions as always
allow which was how v1 worked !!!!!! Get Opus on fixing that so it's a quick API
that does the right thing instead of being open to your mistakes". Every
qualified provider launches always-allow, as v1 did, and the caller never picks
a flag. Each result carries `permission` from preflight on.

| Provider and path | Effective mode | Where it is set | Pinned by |
| --- | --- | --- | --- |
| Claude: task, local, remote (`--interactive`, the standby), resume, rescue restore, explicit RC continuation | always-allow | `claudeArgs` appends `--dangerously-skip-permissions` to every Claude agent argv; agentd seeds `skipDangerousModePermissionPrompt` | `TestClaudeLaunchPathsAreAlwaysAllow`, `TestEveryClaudeAgentArgvUsesClaudeArgs`, launch tests |
| Claude native subagents | always-allow | inherit the parent's bypass mode; the GitOps `agent-*-worker.md` set no `permissionMode` | Claude Code docs (permission modes) |
| Codex managed task and its resume | always-allow | `--dangerously-bypass-approvals-and-sandbox` in `buildCodexLaunch` | `TestManagedCodexLaunchIsAlwaysAllow` |
| Codex thread on a host (this command) | always-allow | the wrapper always passes `--dangerously-bypass-approvals-and-sandbox`; the 0.160.1 TUI accepts it with `--remote` (only resume and fork refuse overrides) | `scripts/test_codex_clean_session.py`, `TestCodexThreadResponded` |
| Codex phone-started threads (the daemon) | always-allow | `config.toml` `approval_policy = "never"`, `sandbox_mode = "danger-full-access"`; requirements `default_permissions = ":danger-full-access"` (#487) | image smoke test (OP482) |
| Child sessions created through agent-run | always-allow | each child is its own session on the paths above | as above |
| Cursor/Grok | not qualified | the ACP helper answers permission requests `reject_once` only; the tmux helper never passes `--force`/`--yolo` | Cursor helper tests |

**Known prompt that bypass mode does not cover.** Claude Code's critical-path
check (the "dangerous rm not ruled out" prompt) fires when the shell may read a
command differently than it was parsed, such as `rm` inside `sh -c`.
`--dangerously-skip-permissions`, allow rules and hooks cannot approve it; it
auto-denies after two minutes in a TUI and is denied under `-p` (Claude Code
docs, permission modes, "Critical paths"). Results report it as
`notBypassed: ["claude_critical_path_check"]`. Agents avoid it by not running
`rm` inside `sh -c`. Claude Code's only switch for it is the launch variable
`CLAUDE_CODE_EVEN_MORE_DANGEROUSLY_SKIP_PERMISSIONS`. Tom ruled on 2026-11-09:
"Keep the check". No launch sets that variable and no option for it exists.

**Cursor.** An always-allow Cursor launch would pass cursor-agent's
`--force`/`--yolo` or answer ACP `session/request_permission` with the
`allow_always` option. Neither is implemented; it belongs to Cursor's own
qualification story and needs Tom's approval.

## 7. Authorization

No new authority. The Codex route is `sessionExecBase`: refused in an ordinary
session pod (D-19), allowed where RBAC already lets the caller create
`pods/exec` in `dev-agents` (the v1 pod's ServiceAccount, a workbench session,
Tom's kubeconfig). The Claude route is the existing create API and its caller
checks. agentd re-checks inside the host that it is a Codex remote session.

## 8. Result and outcomes

`thread-launch.v1` (`apiv1.ThreadLaunchResult`): `link` (Claude RC URL, else
null), `version`, `outcome`, `provider`, `host`, `sessionName`, `name`,
`threadId`, `source`, `cwd`, `requestedModel`, `permission` (`mode`
`always-allow` or `not-qualified`, `flag`, `source`, `notBypassed`), `phases` (DESIGN-038's
`RCLaunchPhase` record), `totalMs`, `deadlineMs`, `error {code, message, phase}`.
No handoff, response, pairing code or native message text. At most 32 KiB.

| Phase | `ok` means |
| --- | --- |
| `host_ready` | Codex: access current, daemon live, remote `connected`. Claude: pod ready and agent started. |
| `paired` | Never `ok` for Codex (`unknown`/`required`); `not_applicable` for Claude. |
| `client_started` | The provider client process runs. |
| `thread_created` | A new thread or conversation id is bound to this launch. |
| `source_visible` | That thread is in the default (phone) list, or Claude RC registered with a link. |
| `handoff` | The task reached the thread (first turn exists, or Claude transport submitted). |
| `first_response` | The first owner-visible agent message exists. |

| Outcome | Exit | Meaning |
| --- | --- | --- |
| `responded` | 0 | All phases above `ok` (except `paired`). |
| `visible` | 7 | Created and listed; the response is pending or not observable. Check later; never resend. |
| `action_required` | 7 | Pairing or login needs Tom. Nothing was created. |
| `timed_out` | 7 | The wait ended first; phases show where. Never resend. |
| `hidden` | 1 | Created with a source the phone hides. |
| `unconfirmed` | 6 | The launch may have happened. Inspect; never retry automatically. |
| `failed` | 1 | Refused or failed; nothing was created unless phases say otherwise. |
| `blocked` | 1 | The provider is not qualified. |

## 9. Tests and acceptance

Source tests use fakes only: the driver with a fake adapter and clock; the
Codex adapter with a fake daemon, wrapper runner and observer, plus the
observer against an in-process WebSocket server on a Unix socket; agent-run with
a fake API and a fake `kubectl exec`; the pinned 0.160.1 list/turns/items
schemas; a regression test that an `exec`-sourced thread ends `hidden`, never
`responded`, and that the launch argv is the `--remote` TUI, never `exec`.

Live acceptance is the coordinator's: a fresh Codex host on the image that
ships this, Tom types one pairing code, then `thread start` from the v1 pod or
a workbench session must print `responded` and Tom sees the thread on his
phone. No host is created and no pairing is run by the build story.
