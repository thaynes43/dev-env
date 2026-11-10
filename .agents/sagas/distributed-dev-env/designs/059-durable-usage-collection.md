# DESIGN-059: Durable usage collection

- **Status:** Accepted for the first slice, 2026-11-09 (coordinator
  dev-env-fable-coordinator-1109). Option B is taken: it adds no provider
  request, login or access, so it needs no owner approval and no owner
  question is pending. Section 10 lists what Tom may still choose to add
  (timer reads, a Cursor reader); none of it blocks the slice, and each would
  be a new story with his ruling. Both passive sources were checked with real
  data (section 11). Slice 1 (section 7, steps 1 to 8) is built in
  [PR #1018](https://github.com/thaynes43/dev-env/pull/1018); step 9
  (haynes-ops) is not done, so nothing is deployed. Section "Slice 1 as
  built" states where the build differs (2026-11-10).
- **Tracking:** [Story OP509](https://work.haynesnetwork.com/work_packages/509),
  project haynes-engineering: "Usage reporting that keeps working: weekly
  usage and resets without approval windows or a host session that
  suspends". Epic OP187.
- **Builds on:** [DESIGN-030](030-provider-allowance-contract.md) (the
  allowance contract), [DESIGN-052](052-supported-provider-collectors.md)
  (the passive Claude statusline, the Codex account read),
  [DESIGN-053](053-operator-owned-allowance-collection.md) (the
  operator-owned collector) and DESIGN-001 D-41 (the heartbeat).

## 1. Outcome

The portal Usage page shows, per provider, the percent of the weekly
allowance used and when it resets. Claude and Codex numbers come from the
sessions that already run. They keep coming when a session suspends, the
operator rolls out or a CLI version changes. Nobody renews a window, and no
host session is kept alive for it.

## 2. Why collection stopped (read 2026-11-09)

The slot `dev-env-system/dev-env-provider-allowances` holds one report,
served 2026-11-04T15:32:12Z: Codex 65 % of a 10080-minute window, reset
2026-11-09T11:48:50Z; Claude 34 %, reset 2026-11-09T11:59:59Z. Both resets
have passed. The collector journal is idle at cycle 168. Four things stop it,
and any one is enough:

1. **The window.** The collector config carries `validUntil`, at most seven
   days ahead ([operator-allowance-collector](../../../../docs/operator-allowance-collector.md)).
   The approved deadline was 2026-11-04T15:33:23Z (OP323, comment of
   2026-11-03). Expiry stops collection without any host or slot IO. No
   alert covers it; none is built (coordinator audit, 2026-11-09).
2. **The host.** Readers run only inside one remote session,
   `dev-env-1027-010058`, matched by pod UID, generation and image. It is
   Suspended. A resumed pod has a new UID and is not adopted without a new
   approved handoff (DESIGN-053, "One-time replacement handoff").
3. **The version list.** The Claude reader accepts only `2.1.296` and
   `2.1.312` (`internal/usagecollector/remote_batch_linux.go:172`). Agent
   pods run 2.1.321. Codex is pinned the same way to `codex-cli 0.160.1`
   (line 73), so a Codex bump would stop it too.
4. **The private config.** Window, host and account trust live in one
   sops-encrypted Secret (haynes-ops
   `kubernetes/main/apps/dev-env-system/allowance-private-config`). Only the
   age key holder can change any of it, and no agent holds the key.

## 3. What each stop guarded

| Stop | What the record says it protects | Kind | Verdict |
| --- | --- | --- | --- |
| Approval window | DESIGN-053: "normal client authentication/cache behavior still requires qualification and explicit recurring-use authorization". Epic OP187: the 2026-10-24 approval was "architecture/scope plus native capability tests", not deployment authority. OP323: approval bound to one host and an absolute expiry. Risk: unattended, recurring reads of Tom's personal accounts with unqualified side effects (CLI auth or cache writes, a Cursor terminal being typed into). | Caution. No incident behind it. | Passive: goes, because no read is added. Active: replace with one standing approval, a cadence cap and an off switch. |
| One fenced host and its handoff | DESIGN-053 "Execution and ownership": readers run only where the keeper's access-only projections are; "a new pod is not automatically adopted"; a lock and journal stop an old leader overlapping a new one. Risks: (a) a provider client running where a refresh token lives; (b) Cursor needs a logged-in client in that HOME; (c) a cycle running twice. | (a) Hard rule from the 2026-08-29 incident, but every managed session pod already meets it: the keeper's projections cannot carry a refresh token (`internal/claudeauth`, `internal/codexauth`). (b) Real, for Cursor only. (c) Caution. | Relax: "a pod that holds only keeper projections" meets the rule; one pod UID is not needed. Passive needs no host. |
| Per-version allowlist | `remote_batch_linux.go:168`: "the exact set of client versions whose zero-turn /usage stream shape has been qualified ... no range". The [2.1.312 record](../verification/claude-2-1-312-qualification-2026-11-03.md) read the binary's bytes to check `/usage` still makes no model call. Risk: a new CLI whose `/usage` makes a model call, touches auth or changes shape. | Caution. The reader already refuses a result without zero turns, zero cost and its closed shape. The pin adds a manual step per release, and Claude moved 2.1.296, 2.1.312, 2.1.321 in about ten days. | Drop for passive readers: parse the documented shape, refuse anything else. If the active reader stays, keep its shape checks, record the version, stop refusing on it. |
| Encrypted private config | DESIGN-052: the unsalted provider-email hash "is a private correlation reference" and must stay private. DESIGN-053: "no actual account hash goes in public GitOps". Risk: an email hash in a public repo can be matched to Tom's address. | Real exposure, for the account hashes. The deadline, cadence and host selector in the same file are not secret. | Account hashes stay private. Passive needs no account identifier, so it needs no private config. If the active reader stays, move the non-secret settings to public flags. |

Three rules stay whatever is chosen. One owner per rotating refresh token:
the passive route starts no provider client and touches no login, so it
meets the rule without a fence. No model call made only to poll quota, and
no cookie or token extraction (OP187 constraints). No undocumented provider
endpoint (OP509 acceptance 6). That last rule rules out the keeper's planned
read of the CLI's usage endpoint (DESIGN-001 7.3, spike S-16): it works, but
it is undocumented.

## 4. Passive data

### 4.1 Codex

Evidence, all offline, no provider contact:

- **Pinned app-server schema.** `codex app-server generate-json-schema
  --experimental` from the pinned `codex-cli 0.160.1` (the repo's own offline
  method, `internal/agentd/testdata/codex_thread_launch/README.md`) lists a
  server notification `account/rateLimits/updated`. Its `rateLimits` has
  `primary` and `secondary`, each with `usedPercent` (required),
  `windowDurationMins` and `resetsAt` (Unix seconds). File
  `v2/AccountRateLimitsUpdatedNotification.json`, sha256 `37326ef0...a5b8b`.
- **Upstream source at tag `rust-v0.160.1`** (commit `d27764b8`, the commit
  the collector docs already cite). `TokenCountEvent` carries
  `rate_limits: Option<RateLimitSnapshot>`
  (`codex-rs/protocol/src/protocol.rs:2343`). `RateLimitWindow` has
  `used_percent`, `window_minutes` and `resets_at` (`:2392`). The rollout
  writer persists every `TokenCount` event (`codex-rs/rollout/src/policy.rs:113`).
  The snapshot is parsed from the model response's rate-limit headers
  (`codex-rs/codex-api/src/rate_limits.rs:23`).
- **`codex exec --json` carries none.** Its event types
  (`codex-rs/exec/src/exec_events.rs`) put token `usage` on `turn.completed`
  and have no rate-limit field. agentd's parser
  (`internal/agentd/codex_stream.go`) reads none either.
- **No sample in the repo.** Its rollout fixtures are synthetic and have no
  `rate_limits` (`internal/agentd/conversation_usage.go:466`,
  `testdata/conversation_usage/codex_*.jsonl`).
- **One real sample, 2026-11-09** (section 11): a rollout written by one
  `codex exec` in a session pod carries `rate_limits` on its `token_count`
  record, with a 10080-minute `primary` window.

So the pinned source shows that the field exists and is persisted, and one
real rollout shows it filled in for this account.

The reading: agentd already finds each Codex thread's rollout
(`findCodexRollout`) and reads it (`applyCodexRolloutModels`,
`internal/agentd/codex_rollout.go`). The newest `token_count` record with
`rate_limits` gives, for each window, the used percent, its length and its
reset, plus the record's own timestamp: when the client received those
numbers with a model response. The weekly window is the one whose length is
10080 minutes. The 2026-11-04 read had it as `primary`; the code must not
assume which. Task sessions write a rollout: `codex exec` runs without an
ephemeral flag (`internal/agentd/managed_codex.go:250`), and agentd already
reads that rollout for the served model. Remote host sessions run the same
core, so they should write rollouts too; that is not checked.

### 4.2 Claude

- **Statusline input.** The documented callback input
  ([statusline](https://code.claude.com/docs/en/statusline), read
  2026-11-09, not pinned to a version) has `rate_limits.five_hour` and
  `rate_limits.seven_day`, each with `used_percentage` (0 to 100) and
  `resets_at` (Unix seconds). It appears only for claude.ai subscribers, only
  after the session's first API response, and either window may be absent.
- **The repo models exactly that.** `ClaudeStatuslineRead`
  (`internal/providerallowance/claude_statusline.go`) and the image's
  `/usr/local/bin/claude-statusline-usage` keep two optional windows (seven
  days is 10080 minutes), check the numbers strictly, drop every other field,
  and check no version. Their tests use synthetic input only.
- **Interactive only.** The callback runs in the terminal UI: Claude remote
  and local sessions. A headless `claude -p` task is not assumed to run it
  (DESIGN-052).
- **Observed once.** OP191's installed test (haynes-ops #4408, restored by
  #4409) never returned a result. On 2026-11-09 the coordinator saw the
  callback input in a v2 remote session on 2.1.321 (section 11): it carries
  `rate_limits.seven_day`, and the image's helper accepts it.
- **Install point.** agentd writes `~/.claude/settings.json` one key at a
  time with `assertSettingsKey` (`internal/agentd/claudestate.go:191`),
  called from `renderDefaultModel` and `renderBypassPrompt`
  (`internal/agentd/render.go`). No v2 session sets `statusLine` today.
- **Headless tasks stay Unknown.** The documented stream event
  `rate_limit_event` (Agent SDK reference, `SDKRateLimitEvent`) has
  `status`, an optional `resetsAt` and an optional `utilization`, but does not
  say which window it describes. Spike S-6 recorded key names only. It cannot
  give the weekly percent truthfully.

### 4.3 From a session pod to the store

Use the heartbeat. agentd posts its status every 60 s
(`cmd/agentd/main.go:179`) with the pod's ServiceAccount token. The operator
checks that the token's pod belongs to the session in the path
(`internal/apiserver/heartbeat.go`). The status already carries per-session
usage, and an operator that does not know a field ignores it
(`internal/agentd/protocol/status.go`). One new optional field carries one
allowance reading. Session pods need no new route, caller class or RBAC.
`agent-run usage collect claude-statusline` only writes a local file and
makes no request (DESIGN-052), so it cannot be the transport by itself.

### 4.4 What it can promise

| | Codex | Claude |
| --- | --- | --- |
| Updates when | any Codex task or host session makes a model call | an interactive Claude session gets a reply |
| Delay | one heartbeat plus at most one store write per provider per 5 min: about 6 min | the same |
| Nothing running | the last reading ages, with its time shown | the same, and also while only headless Claude tasks run (*Amended 2026-11-10:* or only sessions that do not follow the keeper's projection) |
| Use outside the cluster | shows at the next model call in the cluster | claude.ai use shows at the next interactive reply |
| Reading time | the rollout record's time: when the client received the numbers | the callback time, advanced only when a value changed |
| Stays Unknown | the provider's own measurement time; a window it does not report | the same, plus periods with only headless tasks |

The Claude callback also runs on events that bring no new data (a
permission-mode change, a timer). agentd therefore keeps the previous
reading time when no value changed. That can make a reading look older
than it is. (*Amended 2026-11-10:* it can also look newer, by at most 5
seconds: the helper's file holds only the last callback, and agentd reads
it every 5 seconds.) If that misses the 30-minute promise in practice,
forward the documented `cost.total_api_duration_ms` counter as the signal of
a new reply.

**Trust.** A session pod reports numbers about the one shared login. The
agent in the pod runs as the same user as agentd and can read the pod's
token, so it could post a false reading for its own session. That matters
little: the number is shown, not acted on. (DESIGN-001 7.3's quota priority
would act on it. It is not built, and it would need another check.) Bounds:
the provider must equal the session's agent; percent 0 to 100; reset after
the reading time and within one window length (*Amended 2026-11-10:* plus
one hour); reading time not in the future and not before the pod started; at
most one reading per session per minute (*Amended 2026-11-10:* per 55
seconds); the store keeps the reporting session's name; the next honest
reading replaces a false one. Binding: every managed pod holds only the
keeper's projection of the one login per provider, so a reading is about
that login by construction. agentd sends a reading only while the pod's
credential is still the keeper's current projection; it already makes that
check for the owned Codex capture (`ownedAllowanceStillCurrent`,
`internal/agentd/codex_owned_allowance.go:60`). No account identifier is
compared, so the page says "Account not verified".

## 5. Options

**A. Keep the active collector and remove its stops.** Replace windows with
one standing approval and a cadence cap. Give it a host that never suspends,
or let it adopt any pod that holds only keeper projections. Replace the
version list with shape checks. Move deadline, cadence and host selector to
public flags; account hashes stay private.
*Changes:* the collector config parser, host selection, the handoff path,
one sops edit by the key holder. *Risks:* reads Tom's accounts every 5 to
30 minutes indefinitely; keeps the largest and most fragile code (remote
exec, journal, fences, Cursor terminal automation), the code that broke.
*Owner approves:* the standing recurring reads; the sops edit. *Gains:* the
only option that also sees usage while nothing in the cluster runs.

**B. Passive only.** agentd takes Codex numbers from rollouts and Claude
numbers from the statusline, sends them on the heartbeat, and the operator
stores the newest per provider. The active collector stays as it is,
expired and doing nothing.
*Changes:* agentd, one heartbeat field, a small operator writer, the read
route, one shared display rule, the portal pin. *Risks:* no new reading
while nothing runs, or while only headless Claude tasks run; a worker can
post a false reading, as bounded above; a Codex remote host may write no
rollout (not checked). *Owner approves:* nothing. The idle gap is a limit of
the page, told to the owner, not an access he grants. No provider request,
window, version list or private config is added.

**C. Mix.** B for Claude and Codex, plus an active read for what B cannot
see: Cursor, and Claude when only headless tasks run. This has B's parts plus
a standing approval and an active reader that runs in an ordinary session
pod rather than a fixed host.

## 6. Recommendation

**B.** It has the fewest moving parts and still gives a truthful number. It
adds no provider request and no login, so it needs no window, no host, no
version list and no encrypted file. The numbers are the client's own, about
the one login, with their real receipt time. Its gap (idle periods, and
Claude headless-only periods) is shown on the page, not hidden. If Tom
wants timer-based reads for that gap or a reader for Cursor (section 10), add
them later as a C-shaped second story. Nothing in B has to be undone for that.

## 7. First slice

The slice delivers B for Claude and Codex.

1. **Wire type** (`internal/agentd/protocol/status.go`, new
   `allowance.go`): optional `Status.Allowance`: provider, source
   (`codex-rollout` or `claude-statusline`), reading time, and up to two
   windows of {minutes, used percent, reset}. Bounds as in 4.4. Small: about
   60 lines and 80 of tests.
2. **Codex reader** (`internal/agentd/codex_rollout.go`): read the newest
   `token_count` with `rate_limits` from a bounded tail of the thread's
   rollout at each heartbeat. Check the shape, not the CLI version. Use
   synthetic fixtures built from the pinned source's field names.
   *Amended 2026-11-10:* every window in the record is sent, each labelled
   by its length. Medium: about 120 and 150.
3. **Claude statusline** (`internal/agentd/render.go`, `claudestate.go`): a
   render step for Claude remote and local sessions only. It sets
   `statusLine` to `claude-statusline-usage --output
   $XDG_RUNTIME_DIR/dev-env-usage/claude-statusline.json` and makes that
   directory with mode 0700. If a different `statusLine` is already set, it
   wraps that command with `--display` so the display is kept. The step is
   idempotent: agentd renders at every boot and resume, so it recognizes its
   own command by the helper's exact path and leaves it as it is, never
   wrapping it a second time; a test renders twice. Then agentd
   reads the file at each heartbeat (bounded, owner and mode checked) and
   keeps the reading time when no value changed. *Amended 2026-11-10:* only
   in pods that follow the keeper's Claude projection, and agentd also reads
   the file every 5 seconds. Medium: about 150 and 150.
4. **Send rule** (`internal/agentd` heartbeat assembly): attach a reading
   only while the pod's credential is the keeper's current projection; after
   a refusal, a note in the boot steps names the reason, such as
   `shape_unrecognized`. *Amended 2026-11-10:* a pod that does not follow
   the projection takes no reading and gets no note. Small: about 50 and 60.
5. **Operator writer** (`internal/apiserver/heartbeat.go`, new
   `session_allowances.go`): validate the reading (provider equals the
   session's agent, the bounds in 4.4). Write it into a new ConfigMap
   `dev-env-system/dev-env-session-allowances` with a resource-version
   check: the last two readings per provider, each with the reporting
   session's name; at most one write per provider per 5 minutes, sooner
   when the reset changed. Behind a new flag `--enable-session-allowances`,
   default off. A separate object, because the active collector's fences
   compare the slot's resource version
   (`internal/allowancecollector/handoff.go:202`, `runner.go:286`); a second
   writer there would break any future handoff. Medium: about 200 and 250.
6. **Read route** (`internal/apiserver/provider_allowance_reports.go`):
   `GET /v1/provider-allowances` serves, per provider, the newer of the slot
   item and the session reading. The session reading goes out as a
   `passive_event` item, source version `session-v1`, with its source
   status. It is served whatever the slot's state: today `readStored`
   returns early when the slot is suppressed, missing or unconfigured
   (`provider_allowance_reports.go:193`), and its trust comes from the
   private config; the session reading must not depend on either, or B would
   still hang on the expired slot. *Amended 2026-11-10:* with no private
   slot selection at all, the route is mounted GET-only for the portal
   reader and serves the readings alone. Small to medium: about 100 and 120.
7. **One display rule** (`internal/providerallowance`, used by
   `ProjectCachedRead`): `passive_event` items with source version
   `session-v1` are judged by one built-in rule. It checks shape, sets
   binding Unknown and never marks a value current. The rule applies in the
   operator's own `ProjectCachedRead` call as well as the portal's. Every
   other item keeps the private trust contexts. Without this rule the portal rejects any item
   that matches no private context (`cachedMarkItem`), and so hides it. The
   portal then shows the reading as "Last reported", "Passive event",
   "Account not verified" (`web/portal/session/product_allowance.go`).
   *Amended 2026-11-10:* the rule shows only the `session-v1` items the
   operator merges itself. The operator labels any other one, in the slot or
   a Source, rejected before the merge, and the rule never lifts a rejected
   label, so the portal keeps it rejected. A PUT to the slot that carries a
   session reading's item or status is refused. Small: about 60 and 100.
8. **Docs:** this design's status, a short operator page, the Layout block
   if a package is added. Small.
9. **haynes-ops**, all public, no sops edit: precreate the new ConfigMap
   (metadata only, like the slot) and give the operator Role `get` and
   `update` on that name; pin the operator and add the flag; pin the portal;
   pin the agent image in `templates/app/templates.yaml`. Three small PRs.

**Order.** The portal goes first: until it has the rule, it would hide
these rows. Then the operator with the flag on, then the agent image.
Older operators ignore the new heartbeat field, so agent images can roll
at any time.

**Left as it is:** the active collector (expired, no IO), its flags, its
private config and its version list. Nothing is deleted. A cleanup story can
remove the collector flags, the handoff flag and the supplemental reader
once a week of readings shows the passive route is enough.

**Proof.** `kubectl -n dev-env-system get configmap
dev-env-session-allowances -o yaml` shows, per provider, a reading at most
about 6 minutes behind a running session's last reply, and the name of the
session that reported it. The portal Usage tiles show the same percent and
reset. Then, with no person acting, the readings keep arriving after the
reporting session suspends (the next reading names another session), after
an ordinary operator rollout, and after an agent image with a new Claude or
Codex version. Acceptance 3 of OP509 asks for seven days of this.

**Size:** about 750 lines of Go and 900 of tests, in two dev-env PRs (the
agentd side; the operator and portal side) and one release, plus three small
haynes-ops PRs. One worker session for the source, or two stories split along
that line.

A Codex task's rollout carries `rate_limits` (section 11). If Codex remote
host sessions turn out to write none, hosts give no reading and tasks still
do. An active path (the existing `account/rateLimits/read` in an ordinary
Codex session pod: access-only login, non-forced, no model call) would need
the owner's standing approval. This design does not build it.

## 8. Staleness on the page

A tile shows a plain percent only while its reading is under 30 minutes old
and its reset time is still ahead. Past 30 minutes it shows the percent
greyed with "as of <time>". Once the reset time has passed, it shows no
percent, only "Reset <time>; no reading since", because the new week's use
is unknown, not zero. The portal change is a later story. *Amended
2026-11-10:* not built in slice 1. Until that story, a last-reported line
keeps the percent and says the reported reset time has passed. *Amended
2026-11-10, OP523:* the portal rule is built. A reading older than 30
minutes is greyed and says its age in plain words, for example "reported 3
hours ago". A reading of exactly 30 minutes still shows a plain percent. A
reading a few seconds past that says "reported 31 minutes ago", so the grey
line does not use the same words. Once the reported reset has passed, the
percent is not shown. The line says "This window is over: the reported reset
has passed, so its percent is not shown." and "Last reading was <time>."
With no reading, Claude and Codex say "Nothing has reported yet. A reading
appears when a new Codex task, or a remote Claude session, runs on a current
image and the operator is collecting those readings." The operator serves
that empty tile and an unconfigured source as the same value, so the sentence
includes the collecting condition. Cursor and Grok stay not connected.
*Amended 2026-11-10, OP534:* a session-v1 reading's glance leads with "<N>%
of the week used", then "resets <weekday> <day> <month> <time> UTC", then
"reported <age> ago". One line says it was reported by a running session and
not checked with the provider. The trust lines stay in the tile's details.
The grey, past-reset and no-reading rules above are unchanged. A window other
than 7 days is a smaller line in the same form. If that shorter window's
reset has passed while the week is still current, the smaller line names the
window before the past-reset sentence, and each detail line starts with its
window name.

## 9. Cursor and Grok

**Cursor.** Cursor tasks now run in v2 (DESIGN-058). The only reader in the
repo opens the CLI's Usage screen in a pseudo-terminal and types `/usage`.
It has never produced a percent: one label glyph on that screen is not
qualified (OP192), and the CLI's plan metadata carries no quota or reset
(DESIGN-052). Even when it works, its percent is a rounded display value
and its date is the billing month, not a weekly reset. The team Admin API
needs a team key, and dashboard calls are private (OP193, gated). No passive
source is known. If Tom wants it (Q-02), run that reader inside a running
Cursor task pod at most every 30 minutes, after the glyph is qualified, under
a standing approval. Otherwise the tile says "Not connected: Cursor offers no
supported way for a program to read it." *Amended 2026-11-10, OP532 portal:*
the Usage tile says "Cursor's plan allowance can only be seen on Cursor's own
page." and links to https://cursor.com/dashboard. It does not count tasks or
minutes. The portal's session list has the agent, the mode and the requested
model, and no wall-clock duration, so a weekly total would need a new field
on the session the portal already reads. Grok stays not connected.

**Grok.** No documented programmatic source exists
([R-07](../research/R-07-op192-grok-usage-source-plan.md)). Cursor's help
says Grok in Cursor draws on Cursor's pool. Whether that pool is the X
Subscription's weekly pool depends on a plan type nobody has confirmed. The
only machine-readable route found (R-07 G2, the Grok Build CLI's
`x.ai/billing` method) is undocumented and needs a new login that keeps its
own refresh token. Both conflict with OP509's rules. Grok stays "Not
connected"; the portal already shows that row (#940).

## 10. Choices left to the owner

None of these blocks the slice, and none is pending: option B is the answer
that adds nothing, so the coordinator took it and told Tom in the session.
They are written for a phone, for the day he wants more than B gives. A yes
to any of them is a new story, and his answer is recorded there as a dated
ruling.

**Q-01. Read usage only from running sessions?** "Your weekly usage numbers
would come from the AI sessions you already run, with no extra checks
against your accounts. When nothing is running, the page shows the last
number and when it was read; after the weekly reset it shows no number
until a session runs again. Claude's number comes only from your live
sessions (the ones you open on your phone), not from background jobs. Is
that good, or should the system also check your Claude and Codex usage on a
timer, about every 30 minutes, which you would approve once?" Options: "Only
from running sessions (Recommended)"; "Also check on a timer". Cost of the
timer: automatic checks against both accounts indefinitely, and more code
to keep working through every CLI update, which is the part that broke.
*Recommended:* only from running sessions; revisit after a week of real
readings.

**Q-02. Cursor?** "Cursor has no official way for a program to read its
usage. The system could open Cursor's usage screen in the background and
read the number, about every 30 minutes, while a Cursor job runs. It may
break whenever Cursor changes that screen, and it shows a rounded number
and a monthly billing date, not a weekly reset. Do that, or leave Cursor as
'not connected' for now?" *Recommended:* leave it not connected for now.

**Q-03. Grok?** "Grok has no official way for a program to read its usage
either. The only way found uses an unofficial method and needs you to sign
in to a new Grok tool on the cluster. Keep Grok as 'not connected'?"
*Recommended:* keep it not connected.

## 11. What was checked, and what was not

Checked 2026-11-09, read-only: the slot's report and journal (kubectl); the
host session's phase (Suspended); the operator flags and the private
config's key names (haynes-ops `c3dc8654`, values not read); the operator
Role (`get`, `update` on the slot only); the Claude and Codex versions in
this pod; the version gates, settings render, heartbeat route and display
rules in source at `2b2ec455`; the pinned Codex protocol schema, generated
offline with an empty `CODEX_HOME`; the upstream Codex source at
`rust-v0.160.1`; the public statusline and Agent SDK reference pages; the
tracker records of OP187, OP190, OP191, OP192, OP193, OP323 and OP509.

Not checked by the designer: that Codex rollouts for this account carry
`rate_limits`; that the statusline runs, and carries `rate_limits`, in a v2
remote session on 2.1.321; that Codex remote host sessions write rollouts;
the live portal page (behind sign-in). The designer made no provider request,
login, model call, rollout or transcript read.

**Checked by the coordinator, 2026-11-09T23:42Z, in its own session pod**
(a Claude remote session on the workbench profile, image 2.95.0, Claude Code
2.1.321, `codex-cli 0.160.1`). Both passive sources carry the data. The
values are on OP509, not here.

- **Codex.** One `codex exec` with a one-word prompt, on the pod's access-only
  login, wrote a rollout with one `token_count` record. Its `rate_limits` has
  `primary` with `used_percent`, `window_minutes` 10080 and `resets_at` (Unix
  seconds), `secondary` null, and a `plan_type`. So the weekly window was
  `primary` again, and the slice's first risk is retired for task sessions.
- **Claude.** A `statusLine` command added to the session's
  `~/.claude/settings.json` ran at the next reply without a restart: Claude
  Code picked the setting up live. The callback input named version 2.1.321
  and carried `rate_limits.seven_day` with `used_percentage` and `resets_at`;
  `five_hour` was absent at that moment. The image's
  `/usr/local/bin/claude-statusline-usage --output <file>` accepted that real
  input (exit 0) and wrote a `providerallowance.read.v1` document.
- Both probes were removed afterwards and the settings file restored.
- **The one model call.** Section 3 keeps the rule "no model call made only
  to poll quota". That rule is about a collector that polls. This was one
  qualification call, made once by the coordinator under OP509 to check the
  design's largest unverified claim before about 1,650 lines were built on
  it. It is not repeated, and nothing in the design makes such a call.

Still not checked: that Codex remote host sessions write rollouts; the live
portal page.

## Slice 1 as built, 2026-11-10 (PR #1018)

The coordinator accepted seven differences from sections 4.4, 7 and 8 on
2026-11-10. Each is the rule now in force.

1. **Reset bound.** A reset lies after the reading time and within one
   window length plus one hour. A provider may round a reset up to the hour.
2. **Reading time.** agentd keeps the earlier reading time when no value
   changed. It reads the helper's file every 5 seconds and at each
   heartbeat. A callback with no new numbers can still make a reading look
   newer, by at most those 5 seconds. A full fix is a later story.
3. **Per-session limit.** The operator takes at most one reading per
   session per 55 seconds, so heartbeat jitter drops no honest reading.
   Each operator replica counts on its own.
4. **No projection, no reading.** A pod that does not follow the keeper's
   projection for its provider takes no reading and gets no refusal note.
   For Claude, the render step's skip note says why. A reading found and not
   sent shows in `status.agent.problems` as `allowance-reading: warn:
   reading not sent: <reason>`.
5. **No private selection.** "Unconfigured" includes an operator with no
   private slot selection at all. The route is then mounted GET-only for the
   portal reader and serves the session readings alone, with no private
   context.
6. **Both Codex windows.** Every Codex window in the record is sent, each
   labelled by its length. The weekly window is the one of 10080 minutes.
7. **Staleness.** Section 8 was not built in slice 1. The built-in rule marks
   a measure expired once its reset passes, as data only. The portal then
   kept the percent and said the reported reset time had passed. OP523 builds
   the portal rule; see the amendment in section 8.

Known limits:

- **Claude needs a running session.** Claude readings come only from remote
  or local Claude sessions whose pod follows the keeper's projection. With
  no such session running, the Claude numbers age. They are shown as last
  reported, never as current.
- **Section 8 is built (OP523).** A reading older than 30 minutes is greyed
  with its age in plain words. After the reported reset, the percent is not
  shown. With no reading, Claude and Codex say nothing has reported yet.
