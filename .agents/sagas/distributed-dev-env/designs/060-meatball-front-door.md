# DESIGN-060: Meatball front door

- **Status:** Accepted 2026-11-10 by the coordinator (story OP517), with Tom's
  rulings of the same day recorded (section 11, and decisions 97 and 98 in the
  saga's decision log): the risky-access list, that a request stays open until
  he answers, and what Meatball may start. Stories 1, 3, 4 and 5 can start
  from it. Story 2 changes access and the Ask contract, so it starts with its
  own ADR and an amendment to DESIGN-034 (section 8), and story 6 waits on
  story 2. Design only: nothing is built, switched on or changed by this
  document.
- **Tracking:** [Story OP517](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/517),
  under [Epic OP516](https://work.haynesnetwork.com/work_packages/516).
- **Origin:** Tom, 2026-11-10, typed in coordinator session
  dev-env-fable-coordinator-1109: "One thing we need Fable super users to help
  build is an easier way for Meatball to use dev-env. The vision was inspired by
  the OpenAI Dot release but it wasn't nearly as capable as I anticipated and was
  often crippled by its own invisible approval session. I need a way it can
  easily manage the backlog and then kick off coordinator coding sessions where a
  strong Claude or Codex agent drives cheaper models without needing any help
  from Meatball to get unblocked. But also we don't need every agent to be a
  superuser, there needs to be a way I can handle approvals for risky access"
- **Builds on:** [DESIGN-001](001-dev-env-v2.md) 3.7 (summoned sessions,
  `CallerPolicy`, D-36, plan 10), [DESIGN-034](034-agent-asks-and-approvals-board.md)
  and ADR-010 (the Asks board), ADR-015 to ADR-017 (delegate grant decisions),
  [DESIGN-057](057-generic-thread-launch.md), OP505 (a narrow worker profile).

## 1. Outcome

Meatball, an agent outside the session pods with no superuser reach, uses one
small set of actions: it manages the backlog, starts a coordinator session on a
story with a model and a budget, reads the result, and stops a session. The
coordinator is an ordinary guarded session. It drives cheaper workers to merged
PRs and never waits on Meatball. Routine access needs nobody. Risky access is one
plain request on Tom's phone, and everything waiting on him is in one list.

Sections 2 to 5 are a read-only assessment, 2026-11-10. Evidence is a file and
line, a tracker item and journal (`#233 j3827`), a PR, or a read I ran
(`kubectl get`, `git show origin/main:`, `agent-run mcp` tools/list). haynes-ops
was read at `b789c1b5`.

## 2. How Meatball reaches dev-env today

**No Meatball identity exists in the platform.** No ServiceAccount, caller class,
`CallerPolicy` or manifest names it. The operator's trusted clients are the
workbench and the v1 pod only (haynes-ops `operator/app/helmrelease.yaml:264`,
`--client-service-accounts=dev-agents/dev-env-workbench,dev/dev-env`, the same
live). `CallerPolicy` is designed and not built: any other caller gets a 403 that
says so (`internal/apiserver/caller.go:154`).

Meatball works through identities it borrows:

| Route | Evidence |
|---|---|
| From Tom's Mac, a Codex task ("Mac relay") runs `kubectl exec` with a machine kubeconfig into an existing session pod, and runs `agent-run` or `opwork` there | #98 and #106 (2026-10-22, into v2 session dev-env-1019-205125); #321 j4025: "actual launch transport kubectl exec of existing Root dev-env-1019-205125 CLI" |
| Session dev-env-1027-010058 (remote, profile `workbench`, now Suspended) as the "authenticated transport" | #262 j3691 sets the header "Origin: meatball, Tom's coordinator; authenticated transport: session/dev-env-1027-010058"; `kubectl get agentsession` shows its profile |
| The API sees the borrowed pod, so Meatball's messages carry that session's label | #221 j3663 (the 010058 conversation denies sending them), j3680 (Tom: "That's just Meatball idk why it says that session name probably cause meatball isn't Claude") |
| On 2026-11-09 Meatball's outage responder ran on a Codex host created through the v1 pod | `haynes-ops-1109-115326` has parent `dev/dev-env` (live read); #481 observation 3: the responder found no verified return route to Meatball |
| Tracker writes | One OpenProject API user for every agent; Meatball's carry "Origin: meatball" text: 186 journals (OP507 evidence, section 7b) |
| Grant approvals for Tom | Four grants on 2026-10-27 record `approvedBy: chat/Tom`, written "via" a local agent on Tom's Mac (#217 j1926, #219 j1959). No code path writes that value (repo grep), so it was written from outside the platform |

**What it may create** is whatever the borrowed identity may. Through the
workbench or the v1 pod it is a trusted client, which may name any profile,
`workbench` included: the profile rules bind only session callers
(`internal/apiserver/validate.go:300-318`). Session parents read live: 68 by the
v1 pod, 28 by Tom's human ServiceAccount (minted with an admin kubeconfig,
`caller.go:24-26`), 8 by this coordinator, 395 by other sessions. Which of the 96
were Meatball's is not recorded. Nothing limits Meatball to its own sessions.

**Not recorded:** the identity behind the Mac's kubeconfig (no matching
ServiceAccount or binding in the cluster; #113 calls it a static-token route).
A `kubectl auth whoami` run on the Mac by Tom or Meatball would settle it.

## 3. Where work stalled on an approval or refusal nobody could see or answer

Counted from the tracker, items 38 to 509, 2026-10-15 to 2026-11-09, by text
match and then checked in the journal. Counts are approximate; the method is in
the OP517 PR.

| Kind | Count | Clearest examples |
|---|---|---|
| A. Grant waited for Tom and timed out | 6 grants (live records: `deniedBy: timeout` on 10-23, 10-27, 10-30 twice, 10-31 twice) | #214 j1920 and #217 j1922, 10-27: UPS egress Pending 30 minutes; the broker "wires LogNotifier", so Tom was never told. #233 j3827, 10-31 04:09: GPU grant "no answer in 30 minutes" while Tom slept; again at 13:54 (j4147). #221 j3364, 10-30: two node grants timed out (12:51 and 13:31) and a node stayed cordoned |
| B. Hold nobody was assigned to clear | 32 items in a hold status on 11-09; at least 8 with no live owner | #498 j6119, 11-09: #329 and #335 meet every criterion but sit in Needs approval since 10-31; "opwork refuses without explicit authorization". #199 j6085: Needs approval 16 days, "Waiting on the owner (Tom): nothing recorded". #264 j6105: the Asks activation draft still pins 10-29 images |
| C. Refused by admission or the API | about 9 admission refusals in 8 items; 409 "not addressable" in 11 items | #481 j5919, 11-09: the outage runbook's `kubectl create job --from=cronjob/recyclarr` refused by `dev-env-agent-guard`. #233 j4493, 11-01: the lead's one delegate approval refused with 422 by a policy the bundle lacked. #233 j4244: a headless worker "cannot receive agent-run msg (409)" |
| D. Relayed authority not accepted | 10 items, 10-26 to 11-01 | #221 j3327: "a peer relay is not verified authority". #319 j4149: a restart run on a relay whose "authentication was not accepted", then retracted. #481 j5918, 11-09, Tom: "My hold was to wait for you why did you keep it on hold after?" |
| E. Question never reached the phone | about 8 cases in 7 items | #273 j3088: "phone delivery unverified". #261 j2710: a headless brief says questions "will be recorded, not delivered". #370 j6116: Needs answer for six days |

The pattern behind A, B and E: a request that needs Tom has no route to him
unless the asking session is a Claude TUI with AskUserQuestion. Tom said how he
wants it (#233 j4134, 10-31, relayed by Meatball): "I do not approve on the mac I
or an agent can approve with a message to the agent requesting the grant."

## 4. What an ordinary coordinator session can do today

"Ordinary" means profile `full` or `dev`: the pod runs as `dev-env-agent` under
`dev-env-agent-guard` and `dev-env-identity-guard`. "Workbench" runs as
`dev-env-workbench`, outside those guards.

| Action | Ordinary | Workbench | Evidence |
|---|---|---|---|
| Start Claude task workers | yes, any full model id | yes | `validate.go:127-128` |
| Start Codex task workers | yes, with `--project` | yes | `--enable-managed-codex-tasks` live; `validate.go:129-156` |
| Start Cursor task workers | yes, with `--project`; refused for a budget-bound caller | yes | `validate.go:157-180`; templates `cursor.apiKey` set |
| Worker profile | the parent's own; cannot widen | the default (`full`) or any it names | `validate.go:310-316`; OP502 |
| Size | S, M or L | same | no size rule for sessions |
| Depth and children | at most 2 levels below a root (`caller.go:46`); 4 unfinished children per agent kind (`caller.go:50`, `sessions.go:575`) | same | |
| Read results | `agent-run show`, `log`; the retained outcome | same | OP496 j6199 |
| Stop a session | yes, and any session, not only its own children | same | `sessions.go:463`, `lifecycle.go:47-52` |
| Review, merge, release PRs; haynes-ops pin PRs | yes: `full` and `dev` mount the bot token | yes | templates.yaml `full` and `dev` mounts |
| `flux reconcile`, `rollout restart` | yes, outside dev-env-system, dev-agents and dev-tools | yes, there too | `agent-guard.yaml:3-16`; `identity-guard.yaml:7-9`; dev-env's own Flux objects live in dev-env-system (`operator/ks.yaml:14`) |
| Jobs | only as the namespace's `default` ServiceAccount | any | `agent-guard.yaml:6-9` |
| Request grants | yes (sessions only, `grants.go:46`) | yes | |
| Pod exec routes: `attach`, `detach`, `login`, `pair` | refused | yes | `internal/agentrun/attach.go:103-104` |
| `thread start --agent codex` | through the API, only on a host of its own profile or one it started | by exec, any host | `thread.go:141`, `threads.go:128-144` |
| Start a Codex phone host | refused | yes | `validate.go:141-142` |
| `keeper-login` | refused | yes | `server.go:31-32` |
| Start a workbench session | refused (child pinned to parent's profile) | yes | `validate.go:310-316`; the Fable coordinator itself was declared in git (haynes-ops `coordinator-session/app/agentsession.yaml`) |
| Hold the hardware SSH key | no (only as a grant) | mounted by profile | templates.yaml `workbench` envFrom |
| Reach Proxmox | `full` holds the read token but no policy gives it the PVE egress | the PVE egress and write credential by policy | templates.yaml `full` envFrom; GrantPolicies `lan-pve-api-*`, `credential-proxmox` (profiles: workbench) |

**Gaps that matter for an ordinary story:** none of the exec routes. Task
dispatch needs no exec: on 2026-11-09 the ordinary remote session
dev-env-1109-202321 (default profile) started four task workers, which inherited
`full` (live read; OP494 comment 6046: one returned its result in 97 seconds),
and OP496 j6199 shows Claude, Codex and Cursor workers reaching three merged PRs
with the same commands. What does block: runbook Jobs that need a named ServiceAccount
(#494 row 5, #481 j5919), and dev-env platform stories that must reconcile or
restart in dev-env-system (those stay workbench work, section 9). One more risk:
an ordinary `full` coordinator's workers inherit `full`, which carries the Omni
Admin key, the GCP key and the Cloudflare tokens (templates.yaml `full`; OP505).

A third class exists, the restricted "coordinator host" (`caller.go:147-153`,
`validate.go:89-90`: `dev` profile, size S or M, task mode, a project). It is
bound to one pod and a host annotation (`coordinator.go:68-79`) and is live only
for a fixture (`--coordinator-hosts` names `dev-env-owned-native-fixture`).
Meatball is not a pod, so it cannot use it.

A fourth exists in code only, the named coordinator identity (DESIGN-042 6.4;
`caller.go:70-76` and `:141-145`; `internal/templates/templates.go:120-137`). A
profile may name the ServiceAccount `dev-env-coordinator-<name>`, and the API
then takes that pod's token as a session caller. The operator has it on
(`cmd/dev-env-operator/main.go:391`), but nothing uses it: no such account
exists in dev-agents (live read: 0), no profile names one, and it has no role
binding. `dev-env-agent-guard` matches `dev-env-agent` by username only, so such
an account is under `dev-env-identity-guard` alone. The coordinators Meatball
starts do not use it in the first slice: they run as `dev-env-agent` on `dev`,
under both guards, with the rights section 7 lists. Its use comes with the
middle column of section 8, when a coordinator has to hold a right its workers
do not: workers inherit the parent's profile, so that also needs OP505's worker
profile first.

## 5. The three approval mechanisms as they are

| | Access grants | Asks board | Agent-decided grants |
|---|---|---|---|
| For | extra access for one session: egress, a kube role, a credential, break-glass, maintenance | a question, or an approval of one exact action, for a human | a coordinator approves its own workers' grants |
| How a request reaches Tom | a standing policy decides in about a second; otherwise **nothing reaches him**: the broker's notifier only logs (`cmd/dev-env-operator/broker.go:166`, `internal/broker/seams.go:31`), `--grant-approval-url` is not set, and the broker's `Decide` has no human caller (only delegate and maintenance code calls it) | a Pushover push, "An agent needs your answer", linking the portal Asks page (DESIGN-034 9) | it does not, by design |
| What he sees | nothing in dev-env; past approvals came by chat and were written from his Mac (section 2) | the question, options, the recommended one, links, expiry | nothing |
| No answer | Denied at 30 minutes (`internal/broker/broker.go:69`) | the Ask expires (default 24 h; an approval at most 24 h); the agent keeps working | the grant times out at 30 minutes |
| Live | on: 10 GrantPolicies, 66 grant records, the last on 2026-11-02; 4 approved `chat/Tom`, 1 by Tom's ServiceAccount, 6 timed out | off: no `--enable-asks` (operator), no `--enable-ask-notifications` (keeper), no Ask CRD (live CRD list) | flags on in both services (`--grant-delegates=dev-agents/dev-env-agent`, haynes-ops operator `helmrelease.yaml:441-444`); the GrantPolicy CRD copy lacks `delegated` (#503), so no delegated policy can exist; the legacy delegate is bound to session dev-env-1029-132039, now Suspended with no pod |
| Words an agent reads | `request_access`: 120 words, 14 parameters; `agent-run help grant`: 936 words | `ask`: 274 words, 9 parameters | `decide_grant`: 75 words, 9 parameters |
| Note | "attested" policies (hardware SSH key, SSH egress, node role, for `full` and haynes-ops) accept the agent's own claim that Tom approved | an approved Ask authorizes no machine action in v1 (DESIGN-034 7) | one break-glass grant was approved by a delegate, 2026-11-01 |

The session MCP server's nine tools carry 717 words of descriptions and
parameter notes (`agent-run mcp`, tools/list, counted). `agent-run help` alone is
1,002 words. That is our own invisible approval session: a request that needs
Tom goes to a log line, and the agent has to read several hundred words to learn
that.

## 6. The front door, on one page

| Action | Front-door tool | Same in `agent-run` | Today |
|---|---|---|---|
| Find stories | `backlog_find(text, status, epic)` | `opwork search` | exists |
| Read one | `backlog_show(id)` | `opwork show` | exists |
| Create a story | `backlog_create(epic, title, scope, acceptance)` | `opwork create` | exists, search first |
| Groom: edit scope, acceptance, budget | `backlog_groom(id, fields)` | `opwork groom` | **missing**: "No command here changes a description" (`opwork.py:36`) |
| Order: what goes next | `backlog_order(id, priority)` | `opwork order` | **missing** |
| Close with evidence | `backlog_close(id, evidence)` | `opwork complete` | CLI only; holds refuse without `--force` (`opwork.py:523`) |
| Start a coordinator | `coordinator_start(item, model, hours, workers)` | `agent-run coordinator start` | **missing**: today a hand-written brief from inside a pod |
| Read state and result | `session_status(name or item)` | `agent-run show` | API exists; Meatball cannot reach it |
| Stop one | `session_stop(name)` | `agent-run reap` | API exists |
| What waits on Tom | `waiting_on_tom()` | `agent-run waiting` | **missing** |

`coordinator_start` names four things: the tracker item (a story or an epic),
the driving model (a full id from an allowed list), the hours, and the most
workers at once. **The model is Tom's choice every time** (his ruling of
2026-11-10, section 7): he tells Meatball which model coordinates and Meatball
passes it through. So the model is required, it has no default, and nothing
here helps Meatball pick one. **There is no effort parameter:** every model
runs at its preset level (section 7). The platform fills in the rest: the repository from the item's
`repos` field, profile, size, task mode, the wall-time limit, the parent
(`meatball`), an idempotency key from the item id so a repeat returns the same
session (D-36 V-03), a claim on the item, and the standard brief (section 7). It
returns the session name, its state, the item link and the portal link.
`session_status` later adds the retained outcome: PR URLs, head SHAs and a
summary.

**Both an MCP server and `agent-run` subcommands, one route.** Meatball is an
MCP client outside the pods; sessions and Tom use `agent-run`. Both call one new
operator route. The MCP server is a small Deployment in dev-env-system that runs
as its own ServiceAccount, `dev-env-meatball`, serves streamable HTTP at `/mcp`
on the internal ingress, and accepts one bearer token kept in 1Password, as the
cigar-journal server does. Meatball then needs no kubeconfig, no exec and no
borrowed pod. `agent-run` from the Mac is not the answer: its outside-pod route
mints tokens with an admin kubeconfig, which is the superuser path this removes.

**Meatball's identity is the first `CallerPolicy`** (DESIGN-001 3.7, D-36),
built only as far as this needs. The policy says what Meatball may start (task
sessions on the coordinator profile, the listed driver models, at most N at once,
at most H hours), that it reads and stops only sessions it started, and that it
never gets exec, grants or the workbench. It is not a client: a client may name
the workbench profile. Tool descriptions stay under 30 words each.

## 7. What a coordinator session is

- **Profile:** `dev` in the first slice, the profile the restricted coordinator
  class already uses. It holds the GitHub bot token, web egress, the Codex
  access projection and the MCP tokens; not the GCP, Omni or Cloudflare keys and
  not the hardware SSH key (templates.yaml `dev`). It runs as `dev-env-agent`
  under both admission guards. Workers inherit it. OP505's narrower worker
  profile replaces it for workers when it lands.
- **Mode:** a task: it runs once within its hours, and its outcome is kept after
  the pod is reclaimed.
- **Drivers:** `claude-opus-5-5`, `gpt-6-astra` or `claude-fable-5-2`. Tom,
  2026-11-10, asked what Meatball may start without asking: "Also allow
  Fable", with the proposed limits: each coordinator up to 4 hours and 4
  workers at a time, at most 2 running at once. That list is the bound on what
  may be named, not a menu for Meatball. Tom, later the same day, typed in the
  coordinator's session: "I always will tell Meatball what model I want
  coordinating and we have preset effort levels". So Meatball never chooses
  the model. The API can enforce only part of that: a start with no model is
  refused, and so is a model outside the list. That the model is the one Tom
  named is Meatball's rule, written in its tool description; the API cannot
  check it. The brief carries no advice on choosing a model.
- **Effort is preset, never a parameter** (the same ruling). A Claude session
  runs at `xhigh` when its model takes it, else the highest level below
  (`internal/agentrun/model.go`, `effortPreference`). Codex runs `gpt-6-astra`
  at `max` and its `gpt-6.1-sol` subagents at `xhigh` (the model policy in the
  common rules). The front door, `agent-run coordinator start` and the brief
  expose no effort setting.
- **Workers:** `claude-sonnet-5-5` for routine work, `claude-opus-5-5` for hard
  or user-visible work, `gpt-6.1-sol` for Codex, and Cursor's Grok model
  (OP496 j6199 used `grok-4.7-high`).
- **Limits:** the coordinator sits at depth 0, its workers at 1, theirs at 2
  (`caller.go:43-46`). At most 4 unfinished workers per agent kind
  (`caller.go:50`); the budget's worker count is a lower cap the API enforces.
  Hours are the task's wall-time limit. Money is reported, not enforced: Codex
  cost reads Unknown today (OP496 j6199).
- **Reports:** to the tracker item (claim, progress, close with evidence) and the
  retained outcome. Meatball reads both; nobody relays.
- **When blocked it never waits on Meatball.** Routine: it acts. A worker needs
  coordinator-approvable access: it decides. Risky: one request to Tom (section
  8), then it carries on with other work. A refusal by admission or the API: it
  records the exact message on the item, does not route around it, finishes the
  rest and ends "blocked: <refusal>". Budget spent: it records where it stopped
  and ends.

### 7.1 First CallerPolicy contract (OP526, 2026-11-10)

This implements the Meatball part of D-36, not the other summoning lanes in
DESIGN-001. One namespaced `CallerPolicy`, `dev-env-system/meatball`, admits
one ServiceAccount. Installing it and that account belongs to haynes-ops; this
source change installs neither. The complete spec is:

```yaml
apiVersion: dev-env.haynesops.com/v1alpha1
kind: CallerPolicy
metadata:
  name: meatball
  namespace: dev-env-system
spec:
  serviceAccount: dev-env-system/dev-env-meatball
  mode: task
  profile: dev
  models: [claude-opus-5-5, gpt-6-astra, claude-fable-5-2]
  maxConcurrent: 2
  maxTimeout: 4h
  maxWorkers: 4
```

All seven fields are required, with no defaults. The schema fixes the account,
mode and profile to those values. `models` is a nonempty set drawn only from
the three shown full IDs. `maxConcurrent` is 1–2, `maxWorkers` is 1–4, and
`maxTimeout` is a positive Go duration no greater than 4h. Thus a policy may
tighten these bounds, never widen them. It has no effort field or configurable
rights. Other policy names or namespaces do not enroll a caller.

The new API caller class is `policy`, resolved after every existing class,
using an uncached policy read. An absent policy leaves the existing unknown
ServiceAccount 403 intact. A policy read failure refuses access. Existing
human, client, session, workbench, coordinator-host and named-coordinator
classification and rights stay as they are.

For OP527, the existing `POST /v1/sessions` is the enforcement seam. It requires
an explicit allowed `model`, its matching `agent` (`claude` or `codex`),
`mode: task`, `profile: dev`, `limits.timeout` and `limits.maxWorkers`. It
refuses every supplied `effort` key, even empty or null. The API writes `xhigh`
for either Claude model and `max` for `gpt-6-astra`. Existing Codex feature and
accepted-project gates still apply. Restore, canary, pilot and instruction
publisher routes grant no alternate entrance. OP527 supplies the tracker
brief and idempotency key; this story adds no coordinator route or CLI.

The API writes the authenticated account as `spec.parent`, at depth 0. The
immutable `spec.limits.maxWorkers` caps its unfinished **direct workers across
all agent kinds**, in addition to the unchanged per-agent child and depth
rules. Workers remain ordinary session callers at depth 1, and their children
at depth 2. Existing sessions without this field retain their existing rules.
Initially only policy callers may set this new request field; section 7.2 adds
human and trusted client callers. Neither model nor effort
defaults from a CLI or a template can substitute for the required model.

Root concurrency counts all unfinished sessions with that account as parent,
across models and repositories. It uses the existing unfinished-session rule:
pending, running, idle and draining count; reaping, suspended, archived,
failed or exited sessions do not. A resume of a newly policy-bound root or
direct worker must find a free slot again: its live count excludes the session
itself. A resumed session then counts by the same ordinary unfinished rule; no
reservation outlives it. Ordinary unbounded sessions retain their existing
lifecycle behavior. Worker records retain the bounded parent's UID so deletion
or name reuse cannot remove their fence. Idempotent repeats return their
existing session before checking capacity. If the policy changed since the
caller was resolved, admission refuses and the caller retries with the new
bounds.

Capacity is a live count, read from the API server under the replica's own
create lock just before the create or the resume patch. Two operator replicas
that admit in the same instant can exceed a cap by one. That is accepted, as it
already is for the existing child rule (`maxRunningChildrenPerAgent`), because
a lock that needs a person to clear it after a crash would stop Meatball and its
coordinators, which this epic exists to prevent (coordinator's review ruling,
2026-11-10). Admission writes no lock object, so a crash or a lost response
leaves nothing to recover. For scheduled dispatch a capacity refusal remains a
refusal.

Bounded creates request strict Kubernetes field validation so an old
AgentSession CRD refuses `maxWorkers` instead of pruning the worker cap.
Installation needs both generated CRDs before the operator rollout, all serving
API replicas on the new version, and operator read access to the policy. It
needs no new operator rights in the session namespace. Meatball gets no
Kubernetes resource rights from this contract.

Only `GET/POST /v1/sessions` and `GET/DELETE /v1/sessions/{name}` accept the new
class. Lists always filter to its own direct sessions, regardless of query
filters. Detail and reap use live parent checks; missing and foreign names get
the same refusal. Detail returns the session and retained outcome, without
descendant or project-wide reads. Retained outcomes use the existing
`X-Dev-Env-Task-Outcome: v1` negotiation. Policy callers receive
`limits.maxWorkers`; existing callers receive it only with
`Dev-Env-Caller-Policy: limits-v1`, preserving old strict response decoders.
Reap preserves the existing rescue finalizer
and UID precondition. No log route (it execs), message, thread, resume, suspend,
grant, keeper-login, fleet or other client route is exposed. OP527 must reuse
these fences when adding its start/status/stop facade.

### 7.2 Coordinator CLI contract (OP527, 2026-11-10)

This refines sections 6 and 7 using the OP526 implementation. The seam is the
existing `POST /v1/sessions`, not a new route. Neither the launcher nor the
operator reads OpenProject or needs its credential. The caller supplies a
positive item ID and a repository name or declared project. The embedded brief
tells the coordinator to read `opwork show ID --history` and claim that item
when it runs. The project catalog resolves the repository for `--project`;
Codex still requires a declared project and the managed-task feature gate.

```
agent-run coordinator start --item ID (--repo R | --project P) --model FULL_ID --hours H --workers N [--run LABEL] [-o json]
agent-run coordinator status NAME [-o json]
agent-run coordinator status --item ID [--run LABEL] [-o json]
agent-run coordinator stop NAME [-o json]
agent-run coordinator brief [--item ID --hours H --workers N]
```

Start requires Tom's explicit full model ID: `claude-opus-5-5`,
`claude-fable-5-2` or `gpt-6-astra`. There is no default or effort flag. Hours
and workers are required whole numbers from 1 to 4. Validation happens before
connecting. Start sends the chosen model, matching agent, `mode: task`,
`profile: dev`, `size: M`, the brief, `limits.timeout: Hh`,
`limits.maxWorkers: N` and the derived `idempotencyKey`. No `effort` key is
sent. For a bounded human/client create with omitted effort, the API applies
the same known model presets as a policy create; unrelated creates keep their
existing defaults. The authenticated caller still supplies the parent.

The key is `coordinator-ITEM`, or `coordinator-ITEM-LABEL` for an explicit
`--run`. LABEL uses lowercase letters, digits and hyphens, starting and ending
with a letter or digit; the complete key is at most 63 characters. Repeat the
same label to retry that run. Choose a new label, for example `--run retry-2`,
only to deliberately start another run of the same item. Keys are scoped to
the authenticated caller, not global across identities.

Start first reads `GET /v1/sessions?mine=true` for that exact key. If one
retained session exists, it returns its detail without starting again, even
after exit and even if start options changed. Multiple matches refuse with
their names; inspect by name. Otherwise start posts the request once, without
automatic retries or redirects. A lost response or 5xx returns exit 6
(unconfirmed); inspect `status --item ID [--run LABEL]`, then repeat the same
start to recover through the retained lookup. Never change the run label to
recover an uncertain request. An automatic POST retry could duplicate a task
that completed before the retry, since the API dedupe covers unfinished tasks.

Within one operator replica the existing API serializes identical creates and
returns the unfinished session; a different request with the same active key
conflicts. The create lock is replica-local: two replicas can both miss the
live key lookup and create duplicate active runs. Human/client lists also use
the Kubernetes cache, so a retained run can be absent from the initial lookup
during cache lag. The CLI therefore cannot guarantee exactly one run across
replicas, if another run starts and ends between the read and POST, during
cache lag after exit, or after its Session record is deleted. Section 7.2
provides ordinary retry protection within those existing API limits; stronger
idempotency needs a separately scoped API change. This preserves the existing
contract and avoids adding a lock or a schema field in this story. Retained
outcomes remain available while the Session record exists, after pod reclaim.

`status --item` uses the same caller-scoped exact key lookup, then the ordinary
detail route. `status NAME` reads that name under the caller's existing ACL.
Start and status print name, phase, item, task verdict when present, and the
retained outcome's summary, PR URLs and head SHAs. Their JSON is one object:
`{"item":527,"session":{...}}`, where `session` is the existing API Session
document and `item` is omitted for a name without a coordinator key. Stop is
the existing DELETE/reap of one name, with the same rescue, retention and
authorization rules. It prints the existing reap message or one Session JSON
document. These commands negotiate both `Dev-Env-Caller-Policy: limits-v1`
and `X-Dev-Env-Task-Outcome: v1`.

Policy callers, Tom's human caller and trusted client callers (including the
workbench) may set `limits.maxWorkers`. For humans and clients its bound is
1 to 4; a policy may tighten that. Ordinary session callers, including a
session on the workbench profile, and restricted coordinator-host callers
still cannot set it. A bounded create uses strict Kubernetes field validation
for every admitted caller so an old CRD cannot silently remove the cap.
Workers use the existing aggregate direct-worker count, depth checks and
resume fences; no CallerPolicy schema or bounds change.

The one-page plain-text brief is embedded in agent-run and available offline
with `coordinator brief`; optional item and budget flags render a concrete
copy. It specifies worker choices, tracker reporting, the retained outcome
file and the section 8 refusal/budget behavior. Passing existing access to a
worker means only an already authorized supported route, never sharing raw
credentials or activating the held agent-decided grants feature. It never
waits on Meatball or asks Meatball to unblock it. OP528 should reuse this
request, key and brief contract plus the existing list/detail/reap routes;
the front-door server needs no tracker credential to launch a coordinator.

### 7.3 Annotations, instructions and the standing authority (OP546, 2026-11-10)

Tom, 2026-11-10, typed in coordinator session dev-env-fable-coordinator-1109:
"Meatball is asking me basic things like if it can connect to the MCP with the
token. It should not be asking permission for basic things and this constant
nagging makes it almost useless."

Research the same day found three causes on our side. The tools carried no MCP
annotations. In the schema for protocol 2025-11-25, and the same schema for
2025-03-26, an omitted `readOnlyHint` means false, an omitted `destructiveHint`
means true, and an omitted `openWorldHint` means true, so a client treats an
unannotated tool as not read-only, destructive and open-world and confirms
every call. The initialize result had no `instructions`. Meatball had nowhere
to read a standing authority, so it asked when its authority was unclear.

`tools/list` returns eleven tools. Each tool's `annotations` object has
`readOnlyHint`, `destructiveHint`, `idempotentHint` and `openWorldHint`. Those
are the fields of `ToolAnnotations`, nested on `Tool` as `annotations`, in both
dated schemas. The names and the nesting are the same. This server follows that
shared object and sets every field. The values come from what the code does:

| Tool | read-only | destructive | idempotent | open-world |
|---|---|---|---|---|
| `backlog_find`, `backlog_show`, `session_status`, `waiting_on_tom`, `standing_authority` | true | false | true | false |
| `backlog_create` | false | false | true | false |
| `backlog_groom` | false | false | false | false |
| `backlog_order` | false | false | true | false |
| `backlog_close` | false | false | false | false |
| `coordinator_start` | false | false | true | true |
| `session_stop` | false | true | true | false |

The reads only look. The tracker writes keep history and delete nothing, so
they are not destructive. `backlog_create` derives a source key from the epic
and the title, `meatball-<epic>-` plus the first eight bytes of SHA-256 of the
epic, a newline and the title. `opwork create` finds that item and writes
nothing when the subject and the supplied text still match, so a repeat leaves
the same state. `opwork groom` patches and adds a journal note on every call,
including a repeat of the same text, so it is not idempotent. `opwork order`
writes nothing when the priority already matches. `opwork complete` writes
nothing once the item is Accepted. When Accepted is not an allowed transition,
each call posts another evidence comment and says not to re-run, so
`backlog_close` is not idempotent. A client must not retry it on its own.

`coordinator_start` calls `ExecuteCoordinatorStart`. That looks up the caller's
retained session for `coordinator-<item>` and returns it without posting
another create, so a repeat for the same item returns the same session while
that record exists. This server does not pass a run label. After the record is
deleted, a repeat starts a new session. Section 7.2's limit still holds: this
is not a cross-replica lock. The started agent uses profile `dev`, which has
web egress, so the tool is open-world. The call itself deletes nothing.

`session_stop` reaps the session. Task clean-up then removes a stopped task
and keeps only its rescued unpushed work, so the hint is destructive. A second
delete while the session is already being reaped changes nothing, and a stop
after the record is gone does not recreate it.

`initialize` includes `instructions`, the `InitializeResult` field of both
schemas, beside `protocolVersion`, `capabilities` and `serverInfo`. The text
is 58 words, under the 120 word limit: "This server is Meatball's front door
to the haynes-engineering tracker and to coordinator sessions. The server and
the operator enforce the caller's limits, so call a tool and report the result
rather than asking the owner first. A refusal is final. Record it and do not
work around it. standing_authority returns the owner's standing instruction
for this caller."

`standing_authority` takes no arguments. Its description is at most 30 words,
like the others. The eleven descriptions together were 142 words at OP546;
the close rule in section 7.4 brings the total to 150. The tool
re-reads the file named by `--authority-file` (`Config.AuthorityFile`) on
every call. The path comes only from that flag. The file must be a regular
file of at most 4096 bytes and valid UTF-8. A larger file is a plain tool
error and is never truncated. A control character other than newline and tab
is a plain tool error. With no flag, or a missing or empty file, the tool
returns exactly: "No standing authority is installed for this caller. Ask the
owner once, and record the answer on the work item." It returns nothing else
from the filesystem.

The authority text is installed by haynes-ops as a ConfigMap and is Tom's
text, changed only by a PR he approves. This change installs none. It grants
no new access and does not change any existing tool's behaviour or limits.
The behaviour page is [docs/frontdoor.md](../../../../docs/frontdoor.md).

### 7.4 Close an item left by the caller's finished coordinator (OP632, 2026-11-10)

Process decision by coordinator `dev-env-fable-coordinator-1109`, recorded in
the OP632 work order. This is not a ruling by Tom. In OP529's acceptance run,
the coordinator started through Meatball's front door left its item In review
and exited. `backlog_close` then met opwork's ownership guard. That guard is
correct for unrelated or live owners. This exception covers only the caller's
own finished coordinator for the exact item.

`backlog_close` first runs the existing `opwork complete` without force. A
successful call returns normally. A refusal can lead to takeover only when
all of the following are proven from reads made during this call:

1. The refusal is specifically opwork's ownership guard, before any write.
   Exit 3 alone cannot prove it: holds, conflicts and other refusals share
   that code. Add `complete --guard-json`, an opt-in representation of the
   existing guard failure. Its versioned JSON object carries the exact
   original message, reason (`ownership` or `hold`), item id, lockVersion,
   execution owner, status and `hold` boolean. The boolean uses opwork's
   existing `HOLDS`: Needs answer, Needs approval and Paused. The hold guard
   still runs first. Ordinary CLI output, exit codes and session guards do
   not change. No text matching grants a takeover.
2. The owner has opwork's exact session form:
   `<agent> | worktree <session> | branch agent/<session>`. Both session
   names must agree and be valid session names. A bare caller name, a path,
   an absent field or an unrecognized representation does not qualify.
3. `agentrun.ExecuteCoordinatorStatus` with `Item` finds exactly one retained
   `coordinator-<item>` under this caller. Its fresh GET must still carry that
   key and its name must exactly equal the owner's session. The operator's
   caller-scoped list and GET enforce provenance. No lookup by owner name,
   history search, alternate identity or cached result is a substitute.
4. The session is in task mode and has a finished task result whose verdict
   is Succeeded or Failed. Starting, running, missing, Unknown and TimedOut
   do not qualify. A failed pod phase alone proves nothing about its task.
5. That same session GET has phase `Suspended` and exactly one condition of
   type `TerminalTaskPodReclaim`, with reason `Reclaimed`. Task exit alone
   proves neither that its pod is gone nor that its workers stopped. Any
   other phase or reason, a missing condition or duplicate conditions
   refuses takeover. In particular, `Grace` and `ChildrenActive` do not
   qualify. Before reclaim settles, an explicit resume can restart `Grace`;
   a settled `Reclaimed` condition is not reset by resume.
6. The refused item's snapshot has a known status and explicitly says it is
   not a hold. Review work held in Needs answer, Needs approval or Paused
   stays held. In review itself is not an explicit hold in opwork.

The caller can close its finished coordinator's item only after the operator
has reclaimed that session's pod: about ten minutes after the task ends,
and only once every worker it started has stopped. Before then,
`backlog_close` returns the original refusal and one sentence naming what
was not proven; the caller tries again later. If terminal task reclaim is
disabled, the condition never appears and takeover never happens.

Accepted residual, per the coordinator's 2026-11-10 review rulings on PR
#1054: the condition records the moment of reclaim. Someone else may later
resume one of the coordinator's workers by hand, and that worker can write
while the item is Accepted. Checking descendants live at close time would
need a new field on the operator's session view, a contract change for its
own story if needed.

After these checks, run `claim --force --expect-lock=<refused lock>` with a
note naming the caller's own finished coordinator and why it is taking over.
The tracker atomically rejects any changed item, including a new hold, with
the existing lockVersion check. No retry adopts a newer lock. The claim's
note follows its successful PATCH, so a conflict writes no note. Then run
`complete` with the caller's original evidence and without force. Both
commands use the existing `meatball` execution identity and origin.

The original refusal message and exit diagnostic are retained, followed by
one sentence naming the missing condition, when proof fails or the takeover
conflicts. These paths make no tracker mutation. Missing, malformed,
truncated or unsupported guard data fails closed. An older opwork that does
not support the option also fails closed; there is no fallback to parsing
text. Operator errors or missing/ambiguous sessions write nothing.

After a successful or possibly applied claim, do not undo it. If completion
does not reach Accepted, return its result and a fresh `show` of the item's
actual state. If that read fails, say the current state is unconfirmed and
return the read error; never claim acceptance or retry the mutation. A
failed or uncertain claim also gets a read back and stops without complete.
Claim and complete are two tracker operations, not one transaction.

`backlog_groom` gets no such exception. Editing scope or acceptance is a new
work decision, not closing the caller's completed coordinator work. It keeps
its current opwork behavior. No other tool receives force, and no general
takeover tool is added. The tool annotations remain unchanged: close writes
kept tracker data, and a repeated completion can add evidence.

Source evidence is one PR containing this design, decision row, implementation
and docs. Offline fakes must show caller-attributed claim then complete for
Succeeded and Failed, and no mutation for running/starting/unknown tasks,
another caller's owner, a mismatched owner, no retained session, operator
failure, malformed owner, every canonical hold, a stale lock and every other
opwork refusal. Additional tests cover malformed signals and failure after
takeover, reclaim grace, active children, absent or duplicate reclaim
conditions, a Running phase with Reclaimed, and Suspended with Grace after
a resume. Signals with reason hold but hold false, a nil agent status and
an empty signal message must also refuse without writes. The coordinator
owns the second-family review, merge, image pin,
installation and live acceptance. Item 492 is untouched by this story's
worker and remains the coordinator's live test.

## 8. One approval path

### The list (ruled by Tom, 2026-11-10)

Tom approved this list by phone prompt in the coordinator's session on
2026-11-10: "Yes, use this list (Recommended)". Before it was shown to him the
coordinator moved the app-namespace workload role from the middle column to
the right, because it can reach Secrets. He may change any line later.

| Routine: needs nobody | Coordinator may approve for its own workers | Risky: Tom's phone |
|---|---|---|
| Start, read, message and stop its own workers within its budget | Pass a worker access the coordinator already holds, for no longer than the coordinator's remaining hours | The shared hardware SSH key, and SSH to any host |
| Read the cluster, logs and metrics (never Secret values) | Read-only egress to a named LAN metrics endpoint, up to 1 hour | Proxmox: the write credential and the PVE API |
| Branch, PR, review; merge when required checks pass; release PRs; haynes-ops pin PRs | | Reading Secret values, and the workload role in an app namespace, which can reach that namespace's Secrets by changing an image |
| `flux reconcile`, suspend, resume; `rollout restart`; CronJob suspend; ExternalSecret force-sync; outside the dev-env namespaces | | Break-glass |
| Jobs that run as the namespace's `default` ServiceAccount | | Node role: cordon, drain, uncordon, taint |
| Tracker writes on its own story and its follow-ups | | Maintenance grants (GPU caps, SYS_ADMIN) |
| Web egress; LAN egress to the internal ingress (policy `lan-internal-ingress`, today `full` and workbench only, so `dev` needs it added) | | Deleting volumes, databases or NAS files, `cnpg destroy` included (today the baseline allows PVC delete in `database`: listing it here is a change) |
| | | Anything in dev-env-system, dev-agents or dev-tools (section 9) |
| | | DNS, firewall, VLAN, DHCP; any UniFi action that takes a device or network down |
| | | Spending money: paid API keys, purchases, new cloud resources |
| | | A new superuser session; any login to one of Tom's accounts |

The middle column is the held agent-decided grants feature. Turning it on needs:
the GrantPolicy CRD copy (#503); one delegated GrantPolicy in haynes-ops listing
exactly this column; the holds on #329 and #335 cleared; the dead legacy-delegate
binding removed; and a decide tool in one sentence. The delegate allowlist
already names `dev-env-agent` on both services, so the policy is the real
control. It is not in the first slice; Tom ranked it below usage reporting
(#503 j6143).

### What Tom sees

The push stays as DESIGN-034 9 has it: "An agent needs your answer", no request
text (Pushover is outside the lab). It opens the board, which shows two example
requests like these:

> **The coordinator for the UPS shutdown test story asks:** let its worker use the
> shared SSH key that opens the servers, for 1 hour, to read the UPS settings on
> the NAS. **Why:** step 3 of the story compares them with Home Assistant. **If
> you say no:** the worker skips step 3 and the story ends with "step 3 not done:
> SSH declined". **[Approve for 1 hour] [Decline]**

> **The coordinator for the download cleanup story asks:** delete 103 leftover
> download folders on the NAS, 148 GB, once. The list is in the story. **Why:**
> the story's cleanup step. **If you say no:** nothing is deleted and the story
> ends with the cleanup step open. **[Approve once] [Decline]**

Every request has these four parts: what, why, how long, what happens on no. The
platform writes the card from the grant or action fields; the agent writes only
the why.

### Through which mechanism: the Asks board

Pick the Asks board, and make grants that need Tom travel through it. Reasons:
the grant approval page was never built (no URL, no human caller of `Decide`, a
log-only notifier); the board is built and tested, with Pushover, the portal,
Authentik sign-in and expiry; and it also covers approvals that are not grants
(a destructive runbook step, spending). DESIGN-034 7 already expects one typed
adapter per machine action, "grant UID/role/TTL" among them. The merge: when no
policy decides a grant, the operator opens an Ask for it; Tom's answer on the
portal calls the broker's existing `Decide` with his portal identity (the
AccessGrant type already documents `authentik/<username>` for this).
Amended 2026-11-10 (ADR-021): the portal stores the answer on the existing
answer route and gains no credential. The broker, in process, calls
`Decide`. `<username>` is `preferred_username` from the UserInfo body the
portal already fetches, not the OIDC subject.

**The adapter's contract (story 2 writes it down before any code).**
DESIGN-034 7 says an approved Ask is a record and carries no machine
permission, and that each adapter that consumes one needs its own typed
validator, a one-use reservation and its own story. ADR-010 is still Proposed.
This is a change to access and to the Ask contract, so story 2 opens with a new
ADR (ADR-021; ADR-020 and DESIGN-061 part 1 have since merged, #1021 and
#1022) and an amendment to
DESIGN-034,
and merges no code before both are accepted. Amended 2026-11-10
(ADR-021): [ADR-021](../adrs/021-grants-through-the-asks-board.md) records
this contract and is Proposed for coordinator review. The build plan, by
file and test, is in that ADR. No code merges before the coordinator
accepts it. Where the ADR is more specific than the bullets below, the
ADR stands: a changed UID or digest is a pre-check, refuses the grant, and
does not call `Decide`; any other `ErrRefused` also ends it refused; only
a conflict or a read error retries. This design's choice, for that
ADR to record or overturn:

- **A third Ask kind, `grant`,** opened only by the operator, never by a
  session. Its scope is typed, not free text: the AccessGrant's UID, its scope
  digest (`grantattest.ScopeDigest`, as a delegate decision binds), the grant's
  type and target, and the TTL asked. The card's what and how-long come from
  these fields.
- **One use.** The answer is the Ask's existing set-once write. The adapter then
  calls `Decide` for that grant UID and digest as `authentik/<username>`, with a
  TTL no longer than the grant asked. `Decide` refuses a grant that is no longer
  pending (`ErrNotPending`, `internal/broker/seams.go:90`), so a repeat cannot
  grant twice. If `Decide` fails after the answer is stored, the operator
  retries it for that Ask until the grant leaves Pending. A grant whose UID or
  digest no longer matches ends refused, never as a new grant.
- **Two clocks, kept apart.** How long the question stays open: until Tom
  answers (his ruling, below). How long the access lasts: the grant's own TTL,
  which the broker enforces and ends. The 24-hour cap (`AskScopeMaxTTL`) guards
  a standing approval that nothing can revoke. A `grant` Ask leaves no standing
  approval, because the answer is spent at once. So the cap and the answer
  rules stay as they are for `approval` Asks, and a `grant` Ask has no
  `scope.expiresAt` and no `expiresAt`.
- **The grant's own timer.** A grant that waits on Tom is no longer denied at
  30 minutes (`PendingTimeout`, `internal/broker/broker.go:69`). It ends when he
  answers, when the asking session withdraws it, or when that session ends.
- **A bound.** Nothing expires these by itself, so the ADR sets how many may be
  open for one session and in all.

### While it waits, on timeout, and in one place

- **Waiting:** the session records "waiting on Tom: <Ask link>" on its item and
  carries on with other work. It checks the answer at its next step, not in a
  sleep loop.
- **No answer is not a no (Tom, 2026-11-10).** Asked how long a request should
  wait, he answered: "I should always respond if I didn't I missed it in a way
  I cannot get back to it. The UI should be my failsafe at missed requests but
  it's super rare I'd say no to be honest, this is a home lab". So a risky
  request does not expire into a refusal. It stays open on the board until he
  answers it. Today a grant times out at 30 minutes; that goes for a grant
  that waits on him. The board's 24-hour ceiling stays for approval Asks and
  does not apply to these (the adapter's contract, above).
- **If the asking session ends first,** its grant ends with it: an AccessGrant
  is bound to the session that asked, so there is nothing left to grant to and
  `Decide` cannot run. The request stays on the board, marked "the session that
  asked has ended", with what was asked, by which session and for which story,
  so he can still get back to it. In the first slice that card has a link to
  the story and a Dismiss button, and no Approve button: a late yes would
  grant nothing, and no component starts work from it. Starting the step again
  is an ordinary coordinator start, by Meatball or Tom, on that story.
- **One place, and it is the failsafe:** the portal's Asks page lists every
  open request, grants included, oldest first, including any he missed. `waiting_on_tom()` returns the same list to Meatball. A tracker item
  in Needs approval or Needs answer carries its Ask link, or the daily sweep
  (#498) clears it.

## 9. What stays superuser

A workbench session is for building and repairing dev-env itself: changes and
restarts in dev-env-system, dev-agents and dev-tools; `flux reconcile` of the
dev-env apps; the exec routes (`attach`, `login`, `pair`); `keeper-login`; a
Codex phone host; work that holds the hardware SSH key by profile. It is created
only on Tom's word, quoted on the tracker item: by a Flux-declared AgentSession,
as for this coordinator, or by a workbench create that names `--profile
workbench`. Neither Meatball nor a coordinator can create one, and the front
door has no such action.

## 10. The first slice

| # | Story | Changes | Size | Depends on |
|---|---|---|---|---|
| 1 | Asks board on | Refresh #264's held haynes-ops draft: Ask CRD, RBAC, operator `--enable-asks`, keeper `--enable-ask-notifications` with the portal origin | M | none: quiet hours were ruled on 2026-11-10 (DESIGN-034 Q-255-02: pushes from 07:00 to midnight Eastern). The story is the existing item OP264 |
| 2 | Grants that need Tom go through the board | First ADR-021 and the DESIGN-034 amendment (section 8, the adapter's contract). Then: the operator opens a `grant` Ask for a grant no policy decided; the portal answer calls `Decide`; no timeout while it waits on Tom; the "session has ended" card; `request_access` and `grant` help cut to 40 words | M | 1; ADR-021 and the DESIGN-034 amendment accepted |
| 3 | Meatball's caller | `CallerPolicy` for one caller: CRD, API class, the ruled limits (a driver from `claude-opus-5-5`, `gpt-6-astra` or `claude-fable-5-2`, required and with no default; 4 hours, 4 workers, 2 at once; no effort setting), own-sessions-only reads and stops | M | none |
| 4 | Coordinator start | One operator route that fills in a coordinator task from a tracker item; the standard brief; `agent-run coordinator start, status, stop`; worker-count cap | M | 3 |
| 5 | Front-door MCP server | Deployment, ServiceAccount, internal ingress, token from 1Password; the eleven tools; `opwork groom` and `order` | M | 3, 4 |
| 6 | Acceptance run | Meatball (or a stand-in with the same token) starts a coordinator on one ready story; cheaper workers reach a merged PR; one risky request reaches Tom's phone and he decides | S | 1, 2, 5 |

Not in the slice: agent-decided grants (#503 and a delegated policy), OP505's
worker profile, named-ServiceAccount Jobs for runbooks (#494 row 5).

**Delete:**

- `--grant-approval-url`, the grant view's approval link, and every "Tom reads
  it on the approval page" text: no such page exists.
- `LogNotifier` as the broker's production notifier.
- `approvedBy: chat/Tom` writes from outside the platform.
- `--grant-legacy-delegates` on both services: it binds a session that has no
  pod.
- Meatball's habit of exec into session pods and the v1 pod: retire it once
  story 5 works.
- The attested path for anything in the risky column: an agent's claim that Tom
  approved stops standing in for Tom.

## 11. Questions for Tom

*Ruled 2026-11-10, all three, by phone prompt in the coordinator's session:
Q-060-01 "Yes, use this list (Recommended)"; Q-060-02 answered in his own
words (section 8, "No answer is not a no"); Q-060-03 "Also allow Fable". He
added, unasked, at 2026-11-10T02:31:08Z by the coordinator's shell clock: "I always will
tell Meatball what model I want coordinating and we have preset effort levels"
(sections 6 and 7). The
questions as drafted follow, for the record. The prompts he saw were put in
plainer words; their text is in the OP517 comments of 2026-11-10.*

**Q-060-01. The risky list.** Agents will ask you on your phone before they do
anything in the right-hand column of section 8: the SSH key, Proxmox, Secrets,
break-glass, node changes, GPU maintenance, deleting data, dev-env's own
services, network changes, spending money, a new superuser. Everything else they
do on their own, or a coordinator approves it for its workers. Change what you
want. *Recommended:* accept it as written. One line is new: agents may delete a
database volume for a rebuild today without asking; the list moves that to your
phone.

**Q-060-02. How long a risky request waits.** When an agent asks you for risky
access and you do not answer, how long should it wait before taking it as no? It
keeps working on other things meanwhile. Today it is 30 minutes, and 6 of the
66 grant records ended that way (section 5). Options: 30 minutes; 8 hours; 24
hours (the most the board allows). *Recommended:* 8 hours.

**Q-060-03. What Meatball may start without asking.** Proposed: coordinators on
Opus 5.5 or GPT-6 Astra, not Fable; each up to 4 hours and 4 workers at a time;
at most 2 running at once. Options: as proposed; also allow Fable; smaller (1 at
a time, 2 hours). *Recommended:* as proposed.

## 12. What I could not determine

- The identity behind the Mac's kubeconfig, and whether Tom or Meatball made the
  96 sessions parented by the v1 pod and Tom's ServiceAccount.
- How `approvedBy: chat/Tom` was written. The journals say a local agent on the
  Mac did it; no dev-env code writes that value.
- The OP494 audit counts 6 owner approvals; the live records show 5 (4
  `chat/Tom`, 1 by Tom's ServiceAccount). OP494 comment 6046 counts 5 timeouts
  in 42 requests; the live records show 6 in 66. This design uses the live
  read.
- Whether Meatball can reach the internal ingress from where it runs.
- Whether the bot token may start `publish-agent.yml` by hand, the fallback a
  release can need.
- Whether a Pushover push is seen: acceptance by Pushover is not delivery
  (DESIGN-034 9).
- The stall counts are text matches checked one by one, not a complete audit;
  items outside project haynes-engineering were not read.
