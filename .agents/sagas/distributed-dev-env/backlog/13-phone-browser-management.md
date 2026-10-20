# 13: phone and browser management

**Status:** plan, written 2026-10-20 in batch NEXT-BATCH-1020 (story NB-07,
Saga85 / Epic107). Nothing in it is accepted. Proof levels are as of release 2.37.0.
**Depends on:** [03](03-remote-control.md) (its delivered parts); NB-01 and NB-02,
in progress in the same batch
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

| Job | Today (2.37.0) | Proof today | After NB-01 and NB-02 (planned) |
|---|---|---|---|
| See what runs: `list`, `show`, `log`, `fleet` | Ask a workbench session | `list` proven in a v2 session (Story110 T22). The others are installed; no v2 run is recorded | No change |
| Talk to a Claude session | Claude app, Remote Control | Proven on the phone | No change |
| Talk to Codex | ChatGPT app, Remote, a paired host | Proven on the phone | No change |
| Start a session | Ask the standby to run `agent-run` | Creating a Codex host proven from a workbench session. A Claude session started from v2 and then used on the phone: not recorded | No change |
| Suspend, resume, reap | Ask a workbench session | Reap proven from a workbench session (a Codex host probe). Suspend and resume proven from the v1 pod; not recorded from v2 | No change |
| Renew the keeper logins | The v1 pod or an admin laptop starts the helper; Tom opens the link on his phone and pastes the code | The ceremony is proven on the phone, run from the v1 pod. From v2: not built | NB-01: `agent-run keeper-login claude\|codex status\|url\|code` |
| Restart the operator, keeper, broker or shelf | The v1 pod or an admin laptop | From v2: not built (refused) | NB-02: a `rollout restart` from a workbench session |
| Approve a grant | Standing policies approve the v1 set at once | Standing grants proven from a workbench session. A human approval route: not built | No change |
| Status and health | `agent-run auth status`; three operator alerts | `auth status` proven from a workbench session. Alerts on the logins or the standby: not built | NB-01's `status` shows the keeper's state |
| Read notes and history | Ask a session: `~/.shared`, `agent-run log`, or resume a suspended session. Durable notes are in GitHub | Log and resume proven. Archived history reads: not built on the installed system | No change |

## What works today

| Mechanism | Proof level | Notes |
|---|---|---|
| Remote Control, one entry per Claude session | Proven on the phone | Connect, steer, answer, suspend and resume as the same entry, history recalled, an operator restart and two automatic image moves. All on the shared keeper login. |
| A workbench session running `agent-run` | Proven from a workbench session | `auth status`, `pair`, `attach`, creating a Codex host and reaping it, and the standing grants. No record shows Tom driving one from his phone yet. |
| ChatGPT app for Codex hosts | Proven on the phone | Pairing, a prompt and answer, suspend and resume without pairing again, history kept. A coordinator relays the pairing code in chat. |
| Standing auto-approved grants | Proven from a workbench session | Internal ingress, Proxmox API, hw-ssh and a short-lived Proxmox token, each approved by policy with no phone step. |
| The operator-kept standby | Installed; it registered by itself | The operator created the standby and it registered Remote Control on the shared login. Tom opening and steering it on the phone is not recorded (morning item 1). |
| The keeper login ceremony | Proven on the phone, run from the v1 pod | The Claude and ChatGPT logins were both renewed this way on 2026-10-19. |
| The 401-window continue | Installed, not exercised | NB-03 tests it tonight. A fix to how it recognises the CLI's error is open (#473). |
| The Codex keeper refresh | Installed, not exercised | It first comes due before 2026-10-29. |
| A Codex host moving to a new image (suspend, then resume) | Installed, not exercised | NB-03 tests it tonight. |
| Archived history reads (`agent-run history`) | Not built on the installed system | `--enable-history-archive-reads` is off, so the routes answer 404. NB-06 checks what turning it on needs. |
| Alerts for the phone path | Not built | v2 has three alerts: rescue failed, operator metrics absent, coordination records near limit. None covers the keeper logins or the standby. |
| A human approval route for other grants | Not built | Q-16 puts approvals in the Claude Code app. R-03 chose no route. |
| The console page and its login-renewal page | Not built | Plan 03 backlog. |

**In a browser.** claude.ai/code lists the same Remote Control entries as the phone
app. That is not separately recorded for v2. ChatGPT Remote works in the mobile
app only, so Codex hosts have no browser path. v2 has no web page or dashboard of
its own.

## What this batch adds (in progress, not accepted)

**NB-01: keeper login from v2.** A new operator route that only workbench callers
may use: `agent-run keeper-login claude|codex status|url|code`. It mirrors
`agent-run login <session>`. The operator execs into the keeper pod's login
container only (the `claude-login` helper, or the Codex login sidecar), never the
keeper's main container. Access change: the operator's service account gets
`pods` get and list and `pods/exec` create in `dev-env-system`. RBAC cannot limit
exec to one container. Only the operator's code fixes the container and refuses
other callers; no admission guard checks the container. Planned live check: `status` for both providers works from a disposable
workbench session, and `url` returns a link that is then abandoned. The full
ceremony with Tom is morning item 3. State at writing: being built; its
haynes-ops RBAC PR is open (#4130).

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
afterwards. The shelf is restarted only when no archive exec runs. State at
writing: the Roles and the guard are merged (haynes-ops #4123), a guard fix is
open (haynes-ops #4129), and no live restart is recorded.

NB-03, NB-05 and NB-06 test rows of the tables above tonight. Their results go in
their own result files. This plan does not claim them.

## Gaps

These remain if NB-01 and NB-02 pass.

1. **A lapsed Claude login cuts the phone off, and nothing warns Tom.** The login
   lapses about 30 days after the last sign-in, next around 2026-11-18.
   `agent-run auth status` shows the access token, not that date. The keeper
   writes the date as an annotation on its published Secret when the provider
   gives one, but no command or alert reads it. v1's auth-watch pages Tom only
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

**Note for Tom.** Once NB-02 works, every workbench session can restart the v2
services. That includes the standby and every session it starts, because a child
gets its parent's profile. The guard keeps that to a restart: no workbench session
can change those services' image, arguments or service account. Once NB-01 works,
workbench sessions can also start a keeper login ceremony; the sign-in still needs
Tom. The v1 pod can do both today, so this is parity, and this plan adds no gate.

One open point for NB-01's review. The operator's new exec right reaches every
container in `dev-env-system`, including the keeper's main container. That
container runs as the keeper's service account, which can read the login journal
that holds the refresh tokens. Only code keeps the operator in the login
container. An admission check that pins the container, like the restart guard
above, would close that. Whether to add one is a decision for NB-01's review and
Tom, not for this plan.

## Smallest next step: story 13.1, login lapse date and reminder

It closes gap 1 and the login half of gap 2. v2 then matches v1's auth-watch for
the shared login.

- **Scope.** The keeper exports two gauges per provider on its existing metrics
  port (8080): login state, and the time the login lapses. `agent-run auth
  status` and `agent-run keeper-login claude status` show the lapse date and exit
  1 when 7 days or fewer remain. The date reaches session pods as a non-secret
  field in the access projection. Its name must not contain `refresh`, because
  the projection parser refuses any such member. haynes-ops: a ServiceMonitor for
  the keeper and a PrometheusRule with two alerts. "Login lapses within 7 days"
  is a warning on the existing daytime Pushover route, as the Plex canary does.
  "Keeper NeedsLogin" is critical. Quickstart section 6 changes in the same PR
  (named writer). Excluded: the journal, the refresh logic, the ceremony, the
  console, alerts for the standby.
- **Acceptance.** Unit tests: the gauges follow the journal with a fake clock;
  `auth status` shows the date, exits 1 inside 7 days, and shows Unknown when the
  provider gave no date (never a guessed date); no token value appears in any
  metric, log or output. Installed: Prometheus holds both series for both
  providers, and the Claude lapse time matches the keeper's annotation; `auth
  status` in a workbench session shows the date. Natural time, not a gate: the
  warning reaches Tom's phone about 2026-11-11.
- **Dependencies.** NB-01 merged first, because both change the keeper status
  path and quickstart section 6. It does not depend on NB-02.
- **Ownership.** One Sonnet writer; an Opus review, because it touches the
  keeper. The coordinator merges, releases and pins. Files: `internal/keeper`,
  `internal/claudeauth`, `internal/agentrun`, `docs/v2-quickstart.md` section 6;
  in haynes-ops the keeper app, a PrometheusRule and the Alertmanager route.
- **Budget.** About 3 hours elapsed. One writer and one reviewer, one at a time.
  Targeted tests only; CI is the full gate.
- **Artifacts and handoff.** One dev-env PR and one haynes-ops PR, a release and
  a pin, and a result file with the installed evidence.
- **Stop.** At acceptance; if the keeper turns out not to know the lapse date
  (then ship the state gauge and the NeedsLogin alert, and record the date as
  Unknown); at a design question; or at the budget.

## Later

In order. Each becomes its own story when picked up.

1. **Phone-path alerts on the operator.** Alerts for a tripped standby breaker,
   no registered standby for 10 minutes, and a session stuck in
   `login-required`. The operator already has metrics and alerts, so this follows
   13.1's pattern.
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

## Acceptance (the plan as a whole)

- From his phone, off home Wi-Fi, with no v1 pod and no laptop, Tom lists
  sessions, starts one, suspends, resumes and reaps one, renews a keeper login
  with the full ceremony, restarts the keeper, checks `auth status` and reads a
  session's log.
- A lapsing or lost keeper login reaches his phone before the phone path stops.
- Each result is recorded in the quickstart with its date and proof level.

## Morning checklist

From the batch handoff, section 7. Items 3 and 4 need NB-01 and NB-02 installed;
skip them if they are not.

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
