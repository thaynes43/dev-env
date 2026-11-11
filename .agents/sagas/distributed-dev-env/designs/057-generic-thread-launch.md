# DESIGN-057: Generic thread launch on an existing v2 host

- **Status:** Proposed, 2026-11-09. Codex adapter in source; live acceptance pending.
  Section 10 (session-pod callers through the operator's API, OP495) in source,
  2026-11-09; live acceptance pending.
  Section 11 (`thread status`, the observe-only recheck, OP492) in source,
  2026-11-10; exec route only; live acceptance pending.
  Section 12 (folder trust for a platform repository, OP695) in source,
  2026-11-10; live acceptance pending.
- **Tracking:** [Story OP489](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/489),
  under [Epic #91](https://work.haynesnetwork.com/work_packages/91). It supersedes
  [OP272](https://work.haynesnetwork.com/work_packages/272)'s runtime activation
  (journal 5990) and meets [OP481](https://work.haynesnetwork.com/work_packages/481)
  acceptance item 2 for this route. [Story OP495](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/495)
  adds section 10. [Story OP492](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/492)
  adds section 11. [Story OP695](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/695)
  adds section 12.
- **Origin:** Tom, 2026-11-09: "Yes build it but meatball has been starting these
  so check to see what's built already don't duplicate what they have but you need
  that ability too. You need to be able to start Grok too and other providers as we
  bring them online so this needs to be generic tooling agents can use in v2".
- **Builds on:** [DESIGN-038](038-remote-control-launch.md) (the launch phase
  model and `rc start`), `scripts/codex-clean-session.py`
  ([docs](../../../../docs/codex-clean-session.md)), the `agent-run pair` exec
  route, and agentd's peer-verified app-server observer (`codex_host_rpc.go`).

## 1. Goal and non-goals

An authorized caller (Tom, the v1 pod, a workbench session or, since section 10,
an ordinary v2 session pod) runs one command
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
  and agent-run says so (`host_unsupported`: move the host to a newer image).
  From an ordinary session pod, which has no exec, the operator's API runs the
  same command instead (section 10). Inside the host the
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
at a prompt in the named tmux session. A repository the platform bootstrapped
under `~/repos` is trusted before launch (section 12). Any other prompt is left
for an authorized person, and nobody sends keys while a native question is pending.

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

No new authority. The Codex route is `sessionExecBase` where RBAC already lets
the caller create `pods/exec` in `dev-agents` (the v1 pod's ServiceAccount, a
workbench session, Tom's kubeconfig). An ordinary session pod has no exec
(D-19); since 2026-11-09 it goes through the operator's API, under a narrower
form of the message route's rule with a profile fence (section 10). The Claude route is the existing create API and its
caller checks. agentd re-checks inside the host that it is a Codex remote
session.

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
| `responded` | 0 | All phases above `ok` (except `paired`). A `thread status` result also leaves `client_started` `unknown` (section 11). |
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

## 10. Session-pod callers through the operator's API (2026-11-09, OP495)

**The gap.** Section 3's Codex route execs into the host from the caller. An
ordinary v2 session pod has no `pods/exec` in `dev-agents` (D-19), so an
agent coordinating from one could not start a Codex thread at all: agent-run
refused it with `caller_not_allowed`.

**Ruling.** Tom, 2026-11-09, by phone prompt in session dev-env-1109-202321:
"why are you unable to do so? A coordinator should have this ability ... Your
job as Fable 5.2 is to get dev-env v2 up to speed so an agentic coordinator can
dispatch Cursor/Codex/Claude bots to build the studio platform". The fix gives
no caller exec. The operator's API runs the host's own command for it, as it
already delivers `agent-run msg` and reads `agent-run log` (D-65). The ruling
is the enablement: the route is on by default, with no flag.

**Route.** `POST /v1/sessions/{host}/threads`, body
`apiv1.ThreadStartRequest` (`model`, `effort`, `name`, optional `repo`,
`wait` as a Go duration, `handoff`). The operator checks the body by
agent-run's own rules (section 3), then the host: a Codex `remote` session,
phase Running, not suspended, with its own Running and Ready pod. Anything
else answers a `failed` result (`not_codex_host` or `host_not_running`) before
any exec. It then runs `agentd ctl thread-start` in the host through
`internal/podexec`, with the same arguments agent-run passes and the handoff
on stdin, and reads the answer as agent-run does: one valid result whose exit
code matches its outcome passes through unchanged (phases, permission and
all); an agentd without `thread-start` is `host_unsupported`; a lost exec
stream or anything else is `unconfirmed` (`host_result_unconfirmed`), never
retried. Once the host check has run, the answer is 200 with one
`thread-launch.v1` result. Every other status is an API error sent before any
exec: a bad body (400, 422), a missing session (404), a caller refused (401,
403, including the profile fence below), a latched task budget, or an API
that cannot exec (503).

**Authorization.** It starts from the message route's rule (D-65), where any
authenticated caller may reach any session, and is narrower in two ways. Tom
and the trusted clients keep that rule. The portal reader is refused, as on
every route not listed for it.

- **Coordinator hosts are refused** (`kindCoordinator`). The message route
  admits one for its own children only, by passing `--expected-pod-uid` and
  `--expected-session-uid` to agentd, and `ctl thread-start` takes neither
  flag.
- **A session caller passes a profile fence.** A message is information from
  its sender. A handoff is the first instruction of an always-allow thread,
  which then acts with the host's profile: its credentials and egress. Without
  a fence, a session on a narrower profile could make a host on a wider one
  act for it. So a session is admitted only when its profile equals the
  host's, or when it is a strict ancestor of the host through `spec.parent`
  (it, or a child of it, started the host). Each profile is the pod's
  `dev-env.haynesops.com/profile` label, else `spec.profile`, as the API reads
  a caller's. The controller labels every pod it builds with the resolved
  profile name, the templates' default when the spec names none, so an empty
  profile is unresolved and never equals another, even another empty one. The
  ancestor walk is the grant delegates' (`sessionAncestry`): live reads, the
  name and UID of each parent, at most `maxSessionDepth + 2` steps. A refusal
  is a 403 before any exec that names both profiles; agent-run reports
  `caller_not_allowed` with that reason. Added 2026-11-09 at the
  coordinator's review of #1007.

The target's task budget is checked as for a message. The operator logs the
caller and the handoff's byte length, never its text. No RBAC, admission,
network policy or CRD changes: the operator already execs into `dev-agents`
pods for messages.

**Waiting.** The listener cuts a request at 30 seconds of read and 60 of write
(`Runner`). A launch may wait up to 10 minutes, so this route moves both
deadlines for its own request (`http.ResponseController`) to the wait plus 90
seconds. The exec stops a minute after the wait, as agent-run's kubectl exec
does, and the host bounds the launch itself. agent-run waits the wait plus two
minutes for the answer. An operator restart during a wait ends the request:
agent-run reports `unconfirmed` (`api_result_unconfirmed`) and never resends.
Each launch holds one exec stream for its wait; no extra limit is added.

**Client.** agent-run in an ordinary session pod (the projected token, not the
workbench) sends the one POST. Tom's laptop, the v1 pod and a workbench
session keep the exec route (a workbench session is a session caller, so the
fence would apply to it on this route). agent-run checks the result again and
prints it.
A lost answer or a 5xx is `unconfirmed`; nothing is retried. An operator
without the route answers its catch-all 404 (`no route POST ...`) before
authentication, and agent-run reports `route_unsupported`, naming the route
and an operator released after 2.95.0, with no fallback.

**Excluded.** Pairing stays on the exec route: `agent-run pair` prints a
secret code, and this route does not carry one. Also excluded: the Claude path
(it already uses the API), `thread status` (OP492), the Cursor/Grok adapter
(OP491) and any change to launch permission flags (section 6a).

**Acceptance.** Source tests use the API's fake exec and fake client (the
profile fence among them: same profile, parent and grandparent admitted;
another profile, a sibling, a child and empty profiles refused before exec;
Tom and trusted clients on any profile), a real listener with short timeouts
over HTTP/1.1 and HTTP/2, and agent-run against a fake API. Live proof waits for a paired host: from an ordinary session pod,
`thread start --agent codex --host <host>` must print `responded` and Tom must
see the thread on his phone.

## 11. Observe-only recheck: `thread status` (2026-11-10, OP492)

**The gap.** `thread start` ends `visible` (exit 7, `response_pending`) when
the first response arrives after `--wait`. The result says "check again
later", and nothing checked again: the only command that observed a thread
also launched one.

**Command.**

```bash
agent-run thread status --agent codex --host <codex-host> --thread <threadId> [--repo R]
```

`--thread` is a result's `threadId`. `--repo` is the repo the thread was
started in (default: the host's repo), because the thread lists are read for
one cwd. It takes no handoff, model, effort, name or wait: each is a usage
error. It prints one `thread-launch.v1` line and exits with its outcome's code.

**Observe-only, by construction.** Nothing on this path can launch or send:

- agent-run execs `agentd ctl thread-status --thread ID [--repo R]`, never
  `thread-start`, without `-i`: the exec has no stdin to carry an input.
- `ctl thread-status` reads no stdin and has no launch flag.
- `threadlaunch.Observe` drives an `Observer` (`Locate`, `Visible`,
  `FirstResponse`). The interface has no `Create`, and the Codex status pass
  drops the wrapper, its interpreter and the poll clock from its dependencies
  before it runs.
- The daemon sees only the handshake (`initialize` and its `initialized`
  notification), `remoteControl/status/read` for `host_ready`, and the three
  read calls of section 5 (`thread/list`, `thread/turns/list`,
  `thread/items/list`).

**One pass.** It does not poll, wait or retry.

1. **Locate.** The session must be a Codex remote host and the id a native
   thread id. `host_ready` is section 5's check, unchanged (access current,
   daemon live, remote `connected`), with the same codes. `paired` is reported
   from the host's marker (`unknown` or `required`) and never stops the pass:
   the thread already exists. The cwd is `~/repos/<repo>`, as for a launch.
2. **Visible.** The default `thread/list` for the cwd (newest 20, the phone's
   list) is read first. The id there is `thread_created` and `source_visible`
   `ok` from one read, and sub-agent or exec threads cannot crowd it out of
   the 20. Only when it is absent is the all-sources list read: the id there
   is `hidden` (`hidden_source`), and in neither list the pass ends `failed`
   with `thread_not_found`. An older thread, another repo's thread and a wrong
   id look the same.
3. **First response.** Section 5 step 5, the same code: `responded` when an
   agent message exists; `failed` (`turn_failed`) for a failed or interrupted
   first turn; `visible` with `response_not_observable` for a completed turn
   without an observed message; `visible` with `response_pending` when no turn
   exists yet or it is in progress.

**Result.** The same document, so a caller reads it as it reads a launch.
What differs: `name`, `requestedModel` and `deadlineMs` are empty or zero;
`client_started` stays `unknown` (`not_observed_by_status`: the pass does not
look at the client process); `permission` repeats the launch contract with
source `launch_contract`, because a status pass does not observe it; and
`threadId` and `source` are set only once the thread is found. `unconfirmed`
and `timed_out` never occur: nothing was launched and nothing waited. A failed
read is `failed` with `observation_unavailable`. agent-run relays only an
answer a status pass can give: a lost or invalid one, a launch's outcome
(`unconfirmed`, `timed_out`, `blocked`), or a found thread that is not
`--thread` is `failed` with `status_unavailable`. All are safe to repeat.
`response_not_observable` is the one answer a recheck will not change.

**Any listed thread.** The pass accepts any thread id listed for the cwd, not
only one `thread start` returned. For a thread started another way (the phone,
say) the turn states are still what was observed, but `permission` and the
`handoff` phase describe a `thread start` launch and say nothing about that
thread.

**Route and authorization.** The exec route and checks of `thread start`
(section 3): `sessionExecTarget`, then the host must be a Running Codex
`remote` session. No grant, RBAC rule, API route or CRD is added. The
operator's thread route (section 10) carries a launch only, so an ordinary
session pod, which has no exec (D-19), gets `failed` with
`status_route_unavailable` before any request, and never falls back to the
launch route. A read route for session pods would be a new API contract with
section 10's profile fence; it is not part of OP492 and needs its own story.
A host whose agentd predates `thread-status` answers `status_unsupported`.

**Other providers.** Claude is refused with
`provider_observation_unavailable` (its first response is not observable;
`rc status` reads its session). Cursor and Grok are `blocked`, as for a launch.

**Tests.** Fakes only: the driver with a fake observer; the Codex status pass
with a scripted host (completed, pending, failed and interrupted turns,
hidden, not found, read failures, host refusals) whose launch fakes fail the
test if reached; the same pass against the in-process app-server, asserting
the methods sent are only the handshake and the three reads; `ctl
thread-status` refusals; and agent-run with a fake API and a fake
`kubectl exec`, asserting the exact argv, no `-i`, empty stdin and no POST.

**Live acceptance and one open question.** Live acceptance is the
coordinator's, on a host whose image ships this. It must also settle what no
fake can: how Codex 0.160.1 answers `thread/turns/list` and
`thread/items/list` for a thread whose client has exited or whose daemon
restarted. A launch reads them within its wait; a status pass may run much
later. If they fail there, status reports `observation_unavailable`; that is
truthful but not useful, and it would need a design change, not a retry.

## 12. Folder trust for a platform repository (2026-11-10, OP695)

**Ruling.** Tom, 2026-11-10, by phone prompt in coordinator session
dev-env-fable-coordinator-1109: "You cannot be asking this it needs to happen
automatically. It's going to happen every time make it part of our tooling to
accept and trust".

For a folder the platform itself bootstrapped, that replaces "an authorized
person decides folder trust". The tooling accepts the folder before the client
starts. Any other prompt (authentication, a later question) is unchanged:
nobody sends keys, and an authorized person still decides those.

**Where Codex 0.160.1 stores it.** `codex --help` says configuration is
`$CODEX_HOME/config.toml` (the help text names `~/.codex/config.toml`; on a
host that directory is `Settings.CodexHome`). There is no `codex trust`
subcommand, and `--dangerously-bypass-hook-trust` is hook trust, not this
prompt. `codex app-server generate-json-schema --experimental` does not name
the key: its `Config` object sets `additionalProperties: true` and has no
`projects` field. The 0.160.1 binary does. Its onboarding writer
(`tui/src/onboarding/onboarding_screen.rs`, the trace `tui-project-trust-write`)
persists `projects."<absolute path>".trust_level`. `ProjectConfig` has that one
field, and `TrustLevel` is `trusted` or `untrusted`. The same binary's folder
prompt says the decision is saved. The entry Codex itself writes is:

```toml
[projects."/home/dev/repos/<repo>"]
trust_level = "trusted"
```

A later rewrite of `config.toml` by Codex keeps a key it owns. A sidecar file
would not.

**Who writes it.** agentd, inside `thread start`'s preflight, after the same
host, pairing and cwd checks the launch already makes, and before the wrapper
runs. The wrapper does not: it cannot see which folders the platform
bootstrapped, its contract is to start one client and never approve a folder,
and a status pass must not run it. The write is configuration, never keystrokes
into a client. `thread status` does not write trust.

**What qualifies.** Only the resolved path of one repository the platform
bootstrapped for that session: `~/repos/<repo>`, where `<repo>` is the launch's
repository name (the request's, otherwise the host's). The path is absolute and
clean, no component is a symlink, `EvalSymlinks` returns that same path, and
the directory is a Git repository whose toplevel is that path. Its `.git` is a
real directory there, not a file and not a symlink. Nothing a handoff names.
Nothing outside `~/repos`. Nothing above that repository root, and not a
subdirectory of it.

**When it does not.** A folder that does not qualify stops the launch at once
with `folder_not_qualified` (outcome `failed`, exit 1), before the wrapper and
before the wait. A `config.toml` that cannot be parsed, is not a regular file,
or holds `projects` in a shape that cannot be updated without rewriting other
keys, stops the launch with `codex_config_invalid` and the file is left byte
for byte as it was. A config that already trusts the folder is left untouched.
An update changes only that one entry: a missing folder is appended as its own
table, and an existing table keeps every other line. Other keys and other
folders stay byte for byte.

**Out of this section.** Claude's and Cursor's prompts. Pairing. Any other
Codex setting. A haynes-ops change. Turning a flag on.

The host config composer (`codexHostConfig`) already keeps a trusted project it
did not itself add, on a public host. A private Codex host
(`AGENTD_ENABLE_PRIVATE_CODEX_HOST`) refuses a trusted root outside the catalog
project roots it owns (`~/codex/<project>`). That check runs on a host status
read, not only when the config is rewritten. This launch therefore does not
write on a private host: it stops with `codex_config_invalid` and leaves
`config.toml` unchanged. The private composer is not changed.

The read and the write hold an exclusive flock on `config.toml.lock` in the
Codex home. If that lock is busy, or the file changed after it was read, the
launch stops with `codex_config_invalid` and the file is left as it was. Two
launches cannot drop each other's folder entry, and a Codex rewrite during the
edit is not overwritten.

