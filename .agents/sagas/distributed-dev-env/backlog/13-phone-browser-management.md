# 13: phone and browser management

**Status:** plan, written 2026-10-20 in batch NEXT-BATCH-1020 (story NB-07,
Saga85 / Epic107). Nothing in it is accepted. Proof levels are as of release 2.37.0,
except where a line says otherwise. **Update, 2026-10-20 (release 2.38.3
installed):** NB-01 (keeper login from v2) and NB-02 (restarts from v2) are installed and
proven from a workbench session. Tom has not done either from the phone yet.
**Depends on:** [03](03-remote-control.md) (its delivered parts); NB-01 and NB-02,
installed 2026-10-20
**Parallel with:** [07](07-access-broker.md) (a human approval route, only if one
is needed)

## Goal

Tom runs v2 from his phone or a browser, with no v1 pod and no laptop. He can see
what runs, start and stop sessions, renew the shared logins, restart the v2
services, check health and read notes and history.

The way to do this today is to ask an agent in a workbench session. The standby is
one. Decision 45 (2026-10-08) accepts that: use `agent-run` or ask agents, and a
web page is one more possible client. This plan adds no console. The console page
stays in plan 03.

## Proof levels

| Level | Meaning |
|---|---|
| Proven on the phone | Tom did it on his phone, end to end. |
| Proven from a workbench session | A workbench session did it live. A coordinator ran it, not Tom's phone. |
| Installed, not exercised | In the installed release, never run live. |
| Not built | No code, or code that is off on the installed system. |
| In progress | Being built in this batch. Not released, installed or accepted. |

## What Tom must manage

| Job | Before the batch (2.37.0) | Proof before the batch | After NB-01 and NB-02 (2026-10-20) |
|---|---|---|---|
| See what runs: `list`, `show`, `log`, `fleet` | Ask a workbench session | `list` proven in a v2 session (Story110 T22). The others are installed; no v2 run is recorded | No change |
| Talk to a Claude session | Claude app, Remote Control | Proven on the phone | No change |
| Talk to Codex | ChatGPT app, Remote, a paired host | Proven on the phone | No change |
| Start a session | Ask the standby to run `agent-run` | Creating a Codex host proven from a workbench session. A Claude session started from v2 and then used on the phone: not recorded | No change |
| Suspend, resume, reap | Ask a workbench session | Reap proven from a workbench session (a Codex host probe). Suspend and resume proven from the v1 pod; not recorded from v2 | No change |
| Renew the keeper logins | The v1 pod or an admin laptop starts the helper; Tom opens the link on his phone and pastes the code | The ceremony is proven on the phone, run from the v1 pod. From v2: not built | NB-01: `agent-run keeper-login claude\|codex status\|url\|code\|cancel`. Proven from a workbench session: `status`, `url` and `cancel` for both (Claude `cancel` since 2.38.1). Not yet on the phone; no full ceremony with Tom's code from v2 |
| Restart the operator, keeper, broker or shelf | The v1 pod or an admin laptop | From v2: not built (refused) | NB-02: a `rollout restart` from a workbench session. Proven from a workbench session for the keeper, shelf and operator (broker by dry run). Not yet on the phone |
| Approve a grant | Standing policies approve the v1 set at once | Standing grants proven from a workbench session. A human approval route: not built | No change |
| Status and health | `agent-run auth status`; three operator alerts | `auth status` proven from a workbench session. Alerts on the logins or the standby: not built | NB-01's `status` shows the keeper's state and the login lapse date (proven from a workbench session) |
| Read notes and history | Ask a session: `~/.shared`, `agent-run log`, or resume a suspended session. Durable notes are in GitHub | Log and resume proven. Archived history reads: not built on the installed system | No change |

## What works today

| Mechanism | Proof level | Notes |
|---|---|---|
| Remote Control, one entry per Claude session | Proven on the phone | Connect, steer, answer, suspend and resume as the same entry, history recalled, an operator restart and two automatic image moves. All on the shared keeper login. |
| A workbench session running `agent-run` | Proven from a workbench session | `auth status`, `pair`, `attach`, creating a Codex host and reaping it, and the standing grants. No record shows Tom driving one from his phone yet. |
| ChatGPT app for Codex hosts | Proven on the phone | Pairing, a prompt and answer, suspend and resume without pairing again, history kept. A coordinator relays the pairing code in chat. |
| Standing auto-approved grants | Proven from a workbench session | Internal ingress, Proxmox API and hw-ssh work, each approved by policy with no phone step. Proxmox operator token (2026-10-20, 2.38.1): proven from a workbench session for reads. The token reached the session and `pve` ran at `tier=operator`; release removed it. Before 2.38.1 no token was ever installed (NB-09, then a mint-parsing bug; #474, #481). No write tried. |
| The operator-kept standby | Installed; it registered by itself | The operator created the standby and it registered Remote Control on the shared login. Tom opening and steering it on the phone is not recorded (morning item 1). |
| The keeper login ceremony | Proven on the phone, run from the v1 pod | The Claude and ChatGPT logins were both renewed this way on 2026-10-19. |
| The 401-window continue | Proven from a session (2.38.0, new generation simulated) | NB-03: the pod's token comes back from the keeper in about 4 s and the next turn works. The automatic continue missed the CLI's real 401 error until #473 (2.38.0); then it was sent and the failed prompt completed. A real keeper refresh setting it off is not yet seen. |
| The Codex keeper refresh | Installed, not exercised | It first comes due before 2026-10-29. |
| A Codex host moving to a new image (suspend, then resume) | Done by a coordinator, not yet on the phone | NB-03 moved PEAR from 2.35.0 to 2.37.0: same enrolment, same thread history, daemon reconnected. Tom's check in the ChatGPT app is morning item 2. |
| Archived history reads (`agent-run history`) | Not built on the installed system | `--enable-history-archive-reads` is off, so the routes answer 404. NB-06 checks what turning it on needs. |
| Alerts for the phone path | Not built | v2 has three alerts: rescue failed, operator metrics absent, coordination records near limit. None covers the keeper logins or the standby. |
| A human approval route for other grants | Not built | Q-16 puts approvals in the Claude Code app. R-03 chose no route. |
| The console page and its login-renewal page | Not built | Plan 03 backlog. |

**In a browser.** claude.ai/code lists the same Remote Control entries as the phone
app. That is not separately recorded for v2. ChatGPT Remote works in the mobile
app only, so Codex hosts have no browser path. v2 has no web page or dashboard of
its own.

## What this batch added (installed; proven from a workbench session, not on the phone)

**NB-01: keeper login from v2.** A new operator route that only workbench callers
may use: `agent-run keeper-login claude|codex status|url|code|cancel`. It mirrors
`agent-run login <session>`. The operator execs into the keeper pod's login
container only (the `claude-login` helper, or the Codex login sidecar), never the
keeper's main container. Access change: the operator's service account gets
`pods` get and list and `pods/exec` create in `dev-env-system`. RBAC cannot limit
exec to one container, so an admission policy, `dev-env-operator-exec-guard`
(haynes-ops #4130, extended by #4132), pins the keeper pod, its two login
containers and the fixed helper commands. State: installed (dev-env #475 in
2.38.0; the Claude `cancel` fix #483 in 2.38.1). Proven from a workbench session
on 2026-10-20: `status`, `url` and `cancel` for both providers, the next `url`
not Busy after a `cancel`, a v1-pod caller refused, and the keeper's logins
unchanged after each abandoned try. The full ceremony with Tom's code from the
phone is morning item 3.

**NB-02: restarts from v2.** Two Roles named `dev-env-workbench-admin`, bound to
`dev-agents/dev-env-workbench`:

- in `dev-env-system`: `deployments` and `statefulsets` get, list and watch;
  `patch` only on the Deployments `dev-env-operator`, `dev-env-keeper` and
  `dev-env-broker`; `pods` get and list; `pods/log` get;
- in `dev-agents`: `deployments` get, list and watch; `patch` only on the
  Deployment `dev-env-shelf`; `pods` list.

No secrets, no exec, no delete. RBAC `patch` alone could rewrite a pod template:
the image, the arguments or the service account. So a guard,
`dev-env-workbench-admin-guard` (a ValidatingAdmissionPolicy), lets the workbench
change only the template's `kubectl.kubernetes.io/restartedAt` annotation, which
is what `kubectl rollout restart` writes. Planned live check: `rollout restart` of
the keeper and the operator from a workbench session, inside a
`declare-activity`, with every session still Running and the keeper Ready
afterwards. The shelf is restarted only when no archive exec runs. State: merged
(haynes-ops #4123, guard fix #4129). On 2026-10-20 a workbench session restarted
the keeper, the shelf and the operator, and every session pod stayed unchanged.
Tom has not done it from the phone yet (morning item 4).

NB-03, NB-05 and NB-06 test rows of the tables above tonight. Their results go in
their own result files. This plan does not claim them.

## Gaps

These remain now that NB-01 and NB-02 are installed.

1. **A lapsed Claude login cuts the phone off, and nothing warns Tom.** The login
   lapses about 30 days after the last sign-in. The keeper reports
   2026-11-16 17:51 UTC (`agent-run keeper-login claude status`, 2026-10-20).
   `agent-run auth status` shows the access token, not that date; only
   `agent-run keeper-login claude status` in a workbench session shows it, and no
   alert reads it. v1's auth-watch pages Tom only
   for v1's own login. If the login lapses, every Claude phone entry stops. The
   Codex host runs in the default profile, so it cannot run `keeper-login`, which
   is for workbench callers only. Recovery then needs the v1 pod or a laptop.
2. **No alert covers the phone path.** Nothing pages on keeper NeedsLogin or a
   failed refresh. Nothing pages when the standby's breaker trips (it writes one
   `loop suspected` log line) or when no standby is registered.
3. **Every job is "ask an agent".** That works under decision 45, but it needs a
   live workbench session on a working Claude login, and every answer uses plan
   quota. No page lists sessions with their links and state.
4. **No human approval for a grant outside the standing policies.** Today such a
   grant needs a GrantPolicy change in git. Q-16 limits human approval to powers
   beyond v1, so daily work does not need it yet.
5. **Archived history cannot be read on the installed system.** A suspended
   session can be resumed and read. A reaped session's conversation is gone; its
   Git work stays in a rescue.
6. **Off home Wi-Fi is untested.** The phone path goes through the Anthropic and
   OpenAI relays. NB-04 lists any step that needs the home network.

**Note for Tom.** With NB-02 installed, every workbench session can restart the v2
services. That includes the standby and every session it starts, because a child
gets its parent's profile. The guard keeps that to a restart: no workbench session
can change those services' image, arguments or service account. With NB-01
installed, workbench sessions can also start a keeper login ceremony; the sign-in still needs
Tom. The v1 pod can do both today, so this is parity, and this plan adds no gate.

The open point this plan raised for NB-01's review is closed. The operator's
exec right in `dev-env-system` could have reached the keeper's main container,
whose service account reads the login journal that holds the refresh tokens.
NB-01's review added `dev-env-operator-exec-guard` (haynes-ops #4130), which
admits only the keeper pod's login helpers with fixed commands.

## Smallest next step: story 13.1, login lapse date and reminder

It closes gap 1 and the login half of gap 2. v2 then matches v1's auth-watch for
the shared login.

**Ruling (Tom, 2026-10-20, from his phone):** dev-env notices go out through a
dedicated "dev-env" Pushover application, sent directly by the keeper and
operator, not through Alertmanager. Alertmanager is generic and noisy; the
dedicated app gives dev-env its own name and icon, clear titles, a tap-through
link to the standby, deduplication and a priority chosen per event.

- **Scope.** The keeper exports two gauges per provider on its existing metrics
  port (8080): login state, and the time the login lapses. `agent-run auth
  status` and `agent-run keeper-login claude status` show the lapse date and exit
  1 when 7 days or fewer remain. The date reaches session pods as a non-secret
  field in the access projection. Its name must not contain `refresh`, because
  the projection parser refuses any such member. The keeper sends two notices
  to the dev-env Pushover app: "Claude login lapses in N days — renew from the
  standby" at normal priority, once a day, daytime only (09:00 to 21:00 local),
  from 7 days before the lapse; and "Keeper needs login" at priority 1 (it
  breaks through quiet hours) when a provider reaches NeedsLogin, once per
  incident, with one "resolved" notice after renewal. Each carries a link to the
  standby session. Sends are deduplicated in the keeper's journal-backed state,
  so a keeper restart does not repeat them. The gauges stay for dashboards; no
  Alertmanager route is added. haynes-ops: an ExternalSecret for the app token
  in `dev-env-system`, mounted only into the keeper. Quickstart section 6
  changes in the same PR (named writer). Excluded: the journal, the refresh
  logic, the ceremony, the console, notices for the standby.
- **Acceptance.** Unit tests: the gauges follow the journal with a fake clock;
  `auth status` shows the date, exits 1 inside 7 days, and shows Unknown when the
  provider gave no date (never a guessed date); no token value appears in any
  metric, log or output; the notifier sends at most one warning per day and one
  critical per incident, respects the daytime window, and survives a restart
  without repeating (fake clock, fake Pushover server). Installed: Prometheus
  holds both series for both providers, and the Claude lapse time matches the
  keeper's annotation; `auth status` in a workbench session shows the date; one
  test notice arrives on Tom's phone under the dev-env app. Natural time, not a gate: the
  warning reaches Tom's phone about 2026-11-08 (the installed trigger is
  floor(days left) of 7 or fewer, ahead of the 2026-11-16 17:51 UTC lapse).
- **Dependencies.** NB-01 merged first, because both change the keeper status
  path and quickstart section 6 (met: NB-01 is installed). It does not depend on
  NB-02. **Tom's step, done 2026-10-20:** Tom created the "dev-env" Pushover
  application and added its token to the existing 1Password item `dev-env` as
  the field `DEV_ENV_PUSHOVER_TOKEN` (no new item). Only Tom edits 1Password;
  agents reference the field from git. The user key is reused from
  `upgrade-gate`/`PUSHOVER_USER_KEY`. Every consumer of `dev-env` reads named
  fields (`remoteRef.property`), so the new field reaches only the keeper's
  ExternalSecret.
- **Ownership.** One Sonnet writer; an Opus review, because it touches the
  keeper. The coordinator merges, releases and pins. Files: `internal/keeper`,
  `internal/claudeauth`, `internal/agentrun`, `docs/v2-quickstart.md` section 6;
  in haynes-ops the keeper app and an ExternalSecret for the dev-env Pushover
  token.
- **Budget.** About 3 hours elapsed. One writer and one reviewer, one at a time.
  Targeted tests only; CI is the full gate.
- **Artifacts and handoff.** One dev-env PR and one haynes-ops PR, a release and
  a pin, and a result file with the installed evidence.
- **Stop.** At acceptance; if the keeper turns out not to know the lapse date
  (then ship the state gauge and the NeedsLogin alert, and record the date as
  Unknown); at a design question; or at the budget.

**Story 13.1 decisions and status (2026-10-20).** State: installed and
accepted on 2026-10-20 (evidence at the end of this section). Source is dev-env
PR #495, released as 2.39.0 (#496). The haynes-ops pieces are PR #4138
(ExternalSecret, notice ConfigMap and its Role, a PodMonitor and the metrics
ingress rule) and PR #4140 (the 2.39.0 pin plus the keeper mount of the Pushover
Secret).

- **The lapse date is its own projection key, not a member of `access.json`.**
  The Secret `dev-env-claude-live` gets a second key, `login.json`
  (`{"version":1,"loginLapsesAt":"<RFC 3339>"}`, no `refresh` in any name).
  Reason: agentd decodes `access.json` strictly and refuses any member it does
  not know, so a new member would stop every running session that still has an
  older agentd from following the keeper's login. Those sessions must not be
  disturbed. An older agentd reads only `access.json` and never sees the new
  key (the whole Secret is mounted, so `agent-run` finds `login.json` beside it).
  The keeper writes it with each published generation, and rewrites it for the
  current generation after a restart. An unknown date removes the key.
- **Codex has no known lapse date.** Its journal holds no refresh-token expiry,
  so Codex exports no lapse series, has no projection field and shows `Unknown`.
  Its state gauge and NeedsLogin notices work.
- **Gauges** on the keeper's metrics port (8080): `dev_env_keeper_login_state
  {provider,state}` with states `Ready`, `NeedsLogin`, `LoginInProgress`,
  `Unknown`, and `dev_env_keeper_login_lapse_timestamp_seconds{provider}`
  (Claude only, absent while no date is known). The workers hand their journal
  reading to one in-memory hub; the journal format is unchanged.
- **Link.** The keeper's existing RBAC reads AgentSessions by name only (`get`,
  no `list`), and the standby's name changes. So there is no stable
  per-session link to find. Every notice links `https://claude.ai/code` (the
  session list where the standby appears on the phone), titled "Open the dev-env
  standby", and the message names `agent-run keeper-login claude url` and
  quickstart section 6. No RBAC was added for a link.
- **Deduplication** is a ConfigMap, `dev-env-keeper-notices` in `dev-env-system`
  (key `state.json`: the day and lapse of the last warning, the incident id and
  whether it was announced; no secret). The keeper's existing Roles write no
  ConfigMap, so haynes-ops pre-creates it and gives the keeper `get`, `update`
  and `patch` on that name only: no `create`, no `list`.
- **Day counting.** Days left are the time left divided by 24 hours, rounded
  down, in the phone notice and both CLI commands. The warning and exit 1 start
  when that is 7 or fewer (so about 8 days before the lapse, in real time), and
  the wording is "in 7 days", "in 6 days" ... "in 1 day", "in under a day".
  The daytime window is the New York wall clock, also on DST days.
- **Failure behaviour.** A missing, forbidden or corrupt dedup ConfigMap does not
  silence notices: the keeper keeps its record in memory, logs the reason once and
  retries the store every pass (a restart meanwhile, or a crash between a send and
  its save, may repeat one notice: at-least-once). A Pushover 4xx other than 429
  stops that notice kind until a restart or a state change; 429 and 5xx retry
  every 5 minutes.
- **A ceremony is not an incident.** A Codex login ceremony pauses refresh by
  marking the journal NeedsLogin with a reservation. The keeper reports that as
  `LoginInProgress`, which neither opens nor ends an incident.
- **One-shot test trigger:** `dev-env-keeper notify-test` (quickstart section 6).
- **Installed and accepted (2026-10-20).**
  - Delivery: dev-env #495 and release 2.39.0 (#496, tag commit `0c22cc0`);
    haynes-ops #4138 (ExternalSecret `dev-env-keeper-pushover` Ready, PodMonitor,
    notices ConfigMap and Role) and #4140 (2.39.0 pin and the keeper mount).
    Keeper and operator run `sha-0c22cc0`; sessions and the task worker run
    `dev-env:2.39.0`.
  - Keeper log: "notifier enabled", no "waiting" line.
  - Prometheus: `dev_env_keeper_login_state` is Ready for claude and codex.
    `dev_env_keeper_login_lapse_timestamp_seconds{provider="claude"}` is
    1794851496, which is 2026-11-16T17:51:36Z and matches the keeper's
    annotation. Codex has no lapse series (Unknown, by design).
  - A fresh workbench session on 2.39.0 (`claude-haiku-5-5`, reaped afterwards) ran the
    image's own `agent-run`. All three commands exited 0 (more than 7 days
    left). `auth status`: claude Ready, login lapses 2026-11-16T17:51:36Z
    (27 days left); codex Ready, login lapses Unknown. `keeper-login claude
    status`: Ready, login expires 2026-11-16T17:51:36Z (in 27 days).
    `keeper-login codex status`: Ready, login expires Unknown (the provider gave
    no date).
  - Test notice: `kubectl exec ... dev-env-keeper notify-test` printed `sent`.
    Tom confirmed on his phone that it arrived under the dev-env Pushover app
    (2026-10-20, about 17:45 UTC).
  - Natural time, not a gate: the first warning is expected about 2026-11-08
    (as soon as floor(days left) is 7 or fewer, inside 09:00 to 21:00 New York
    time); the lapse is 2026-11-16 17:51 UTC. Check the keeper log and Tom's
    phone then.
- **Follow-ups (not blocking).**
  1. Done (Opus review, minor 4). An invalid notice flag does not stop the
     keeper at start. A bad value disables notices and logs `notices disabled`;
     the credential keeper keeps running. That is `--notice-timezone`, either
     window flag, and `--pushover-url`, whenever `NoticeOptions.Validate`
     refuses them. Valid notice flags, including the defaults, stay valid.
     Login gauges are unchanged.
  2. Done (haynes-ops #4160, b2fde821). The v2 session CLAUDE.md
     (`dev-env-config-claude`) tells the standby to renew the keeper's Claude
     login with `agent-run keeper-login claude url` and `agent-run keeper-login
     claude code`. The login lapses 2026-11-16 17:51Z; Pushover warns from
     about 2026-11-08.
  3. Closed (Tom, 2026-10-22, Mac Cursor session). The notice does not need a
     link to the standby. He can see the standby in Claude Code. The keeper
     keeps AgentSession reads by name only (`get`, no `list`), and notice URLs
     keep the general link `https://claude.ai/code` with no fixed standby
     name.

## Later

In order. Each becomes its own story when picked up.

1. **Phone-path alerts on the operator.** Alerts for a tripped standby breaker,
   no registered standby for 10 minutes, and a session stuck in
   `login-required`. Sent through the same dev-env Pushover app as 13.1, with
   the same deduplication.
2. **History reads.** After NB-06, decide whether to turn on
   `--enable-history-archive-reads` and what H4's archive needs. Then reading a
   reaped session's history from the phone is one `agent-run history` call
   through a workbench session.
3. **Approval for grants beyond v1.** Only when a real need appears (Q-16), and
   along R-03's option (a).
4. **The console page** (plan 03, Q-11): sessions with links and state, an archive
   button and the login-renewal page. Optional under decision 45. Build it only if
   asking an agent proves too slow or costly. A page on the internal ingress would
   work at home only, so it needs an outside route behind Authentik.

## Standby observation metrics (source only, 2026-10-22)

OpenProject #111 source slice. Later item 1 still owns the phone-path alerts.
These metrics are the read-only observations a later alert can consume. This
section records that source contract. Alert delivery, Pushover, a Grafana
panel, a GitOps pin, production activation, and phone-path acceptance stay
outside this slice.

The operator registers one collector on every replica, on the existing metrics
registry, using the API reader at scrape time. `--enable-standby-session` is
the switch. The collector calls get on ConfigMap `dev-env-standby` and lists
labeled AgentSessions. It does that only while the flag is on. It leaves
Ensure, session writes, and the breaker update on the existing leader runnable.

| Metric | Meaning |
| --- | --- |
| `dev_env_standby_enabled` | 0 or 1 from the configured flag. Always emitted. |
| `dev_env_standby_snapshot_valid` | 1 when this scrape's required reads and decoding succeeded. 0 after a read, permission, or decode failure. |
| `dev_env_standby_session_count{state}` | Count of labeled records in one fixed state. A valid scrape emits every state, including zero. |
| `dev_env_standby_latest_heartbeat_timestamp_seconds{state}` | Latest valid recorded heartbeat in that state, Unix seconds from the session. Omitted when that state has no valid heartbeat. The value is the stored time, and it stays at that time as the collector clock advances. |
| `dev_env_standby_creation_budget_exhausted` | 1 when the stored recent creation count has reached the existing three creations in 30 minutes, otherwise 0. |

The only series label is `state`, on the count and the heartbeat. Its values
are `registered`, `starting`, `login_required`, `offline`, `booting`,
`moving`, `suspended`, `terminal`, and `unknown`.

A labeled record whose agent is not `claude` or whose mode is not `remote` is
`unknown`. Otherwise `Terminal` is `terminal`. Suspended operating mode or
phase is `suspended`. Pending or Draining phase is `moving`. A missing agent,
a phase other than Running or Idle, or boot metadata other than `booting` or
`ready` is `unknown`. Boot `booting` is `booting`. On a Running or Idle record
with boot `ready`: agent or remote `login-required` is `login_required`; agent
`exited`, `failed`, or `interrupted`, or remote `offline`, is `offline`;
remote `registered` with agent `busy`, `idle`, or `waiting` is `registered`;
remote `starting` is `starting`. Every other ready record is `unknown`. A
stored session id or URL leaves the category unchanged. Booting, moving, and
suspended records stay in those categories when an older remote state says
`registered`. Each labeled record counts on its own.

A heartbeat is valid when it is present, its Unix time is positive, and it is
not after the collector clock. Missing and future timestamps omit that state's
heartbeat series. The collector keeps the breaker's existing window, limit,
and future-timestamp parsing. NotFound on `dev-env-standby` is empty history.
Each enabled read uses a 10 second context.

An invalid snapshot emits `enabled` and `snapshot_valid` 0 and omits the
count, heartbeat, and budget series, so other operator metrics remain. A
disabled collector emits `enabled` 0, `snapshot_valid` 1, zero counts, and
budget 0, omits heartbeats, and makes no API read. A consumer needs the
series to be present with `enabled` 1 and `snapshot_valid` 1. An absent scrape
is not a healthy standby.

## Acceptance (the plan as a whole)

- From his phone, off home Wi-Fi, with no v1 pod and no laptop, Tom lists
  sessions, starts one, suspends, resumes and reaps one, renews a keeper login
  with the full ceremony, restarts the keeper, checks `auth status` and reads a
  session's log.
- A lapsing or lost keeper login reaches his phone before the phone path stops.
- Each result is recorded in the quickstart with its date and proof level.

## Morning checklist

From the batch handoff, section 7. Items 3 and 4 need NB-01 and NB-02, which are
installed (2026-10-20).

1. **Phone off home Wi-Fi (cellular).** Open the standby workbench session on
   claude.ai, steer it and get a reply. This records the standby on the phone and
   the first off-home result (gap 6).
2. **PEAR in the ChatGPT app**, after NB-03's image move. Same computer, a new
   turn works, history recalled. This records the Codex host upgrade path.
3. **Keeper login from the phone.** In the workbench session run `agent-run
   keeper-login claude status`. Optionally run the full Claude ceremony from v2:
   the link arrives in chat and you paste the code back. This proves renewal
   without v1.
4. **Restart from the phone.** From the workbench session, restart the keeper
   with `declare-activity` first, and check that sessions are unaffected.
5. **Read** NB-04's parity table and this plan. Decide what comes next: 13.1 or
   something else.
6. **Messaging.** Send a message between two sessions with `agent-run msg` and
   read it.
