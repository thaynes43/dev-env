# DESIGN-060: Meatball front door

- **Status:** Proposed, 2026-11-10, with Tom's three rulings of the same day
  recorded (section 11): the risky-access list, that a request stays open until
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

**The adapter's contract (story 2 writes it down before any code).**
DESIGN-034 7 says an approved Ask is a record and carries no machine
permission, and that each adapter that consumes one needs its own typed
validator, a one-use reservation and its own story. ADR-010 is still Proposed.
This is a change to access and to the Ask contract, so story 2 opens with a new
ADR (ADR-021: ADR-020 and DESIGN-061 are the task-session clean-up, in work
under [OP518](https://work.haynesnetwork.com/work_packages/518) and not on main
yet; if they land later, this takes the next free number) and an amendment to
DESIGN-034,
and merges no code before both are accepted. This design's choice, for that
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
| 5 | Front-door MCP server | Deployment, ServiceAccount, internal ingress, token from 1Password; the ten tools; `opwork groom` and `order` | M | 3, 4 |
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
