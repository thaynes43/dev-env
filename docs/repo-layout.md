# Repository layout

`make check-layout` reads the first code block under the `## Layout` heading in this
file. Every Go package directory under `api/`, `cmd/`, `internal/` and `scripts/`
needs a line here that starts with its path, a slash and a space. Add the line in the
PR that adds the package. This file is a reference: search it for a path.

Directories still to come: later controllers (ToolSession, LLMLease) beside
`internal/controller/`.

## Layout

```
.agents/HANDOFF.md       dated delivery log and ruling index; large, so search it rather than read it
docs/repo-layout.md      this layout; make check-layout reads it
docs/agent-rules-rationale.md  reasons and history cut from CLAUDE.md (OP499)
docs/frontdoor.md        Meatball's front door: the fifteen MCP tools, the token files, and what the cluster must already have (DESIGN-060)
.agents/sagas/<saga>/    saga README (vision, decision log), KICKOFF, adrs/, designs/, backlog/, research/
.agents/skills/          repo-native agent skills, one SKILL.md each: supervised-dispatch, worker-qa-loop, timer-checkin, tmux-cli-handoff
.github/workflows/       ci.yml (lint, test, build, check-generated, image builds; aggregate `CI - Success`), publish.yml (operator image from main, signed), publish-agent.yml (agent image from a v2.x.y tag, signed), publish-git-wait.yml (the git-wait trial fixture's
                         git-wait-sha-<sha> tag from main, signed), release-please.yml, Claude review + @claude
.github/renovate.json5   Renovate config (gomod, Actions SHA pins, Dockerfile digests, ARG and Makefile pins)
tools/h7-metadata-checker/  isolated Python metadata checker and bounded Mac adapter; no operator dependency or deployment
tools/h7-metadata-checker/celhelper/  separate Go module: pinned CEL lexer/parser, token comparison only
tools/h7-metadata-checker/transport/  separate Go module: existing-Mac GET-only transport, fixed authority and request budgets
.github/workflows/h7-native-continuation-diagnostic.yml  bounded hosted diagnostic for the exact synthetic native continuation fixture
tools/h7-retain-action/  isolated one-time history-PV retention action; owner decision required before execution
.github/workflows/h7-retain-action.yml  finite offline Retain-action fixtures and Mac artifact build
release-please-config.json, .release-please-manifest.json   one repo version, `go` type, first release 2.0.0
api/v1alpha1/            CRD types, group dev-env.haynesops.com (AgentSession; AccessGrant and GrantPolicy, D-54; Activity, D-66;
                         CredentialJob); depends on apimachinery only. Its envtest suite proves the schema's rules (D-39) on a real API server
api/coordination/v1alpha1/  CRD types, the separate protected group coordination.dev-env.haynesops.com (TaskClaim, InboxMessage,
                         InboxReadAck, PublishedContext, SessionDirectory); its CRDs go to config/crd/ and haynes-ops like the others
cmd/dev-env-operator/    the operator, and the access broker as its second mode, `dev-env-operator broker` (broker.go, D-61)
cmd/dev-env-keeper/      the keeper, its own binary in the operator image (D-38); main only, the work is internal/keeper
cmd/dev-env-portal/      the internal read-only portal process; main only, the assembly is web/portal/session
cmd/dev-env-frontdoor/   Meatball's front-door MCP server (DESIGN-060); streamable HTTP at /mcp; main only, the tools are internal/frontdoor
web/portal/localtest/    the portal local test instance (DESIGN-055): real handlers on loopback, fixture upstreams, one fixed
                         test principal; built only with -tags portallocaltest (`make portal-localtest`), in no image
cmd/agentd/              the in-pod supervisor: `run`, `home-verify` (the original-HOME init check, OP252), `hold` (the rescue pod, D-55), `shelf [--history-archive]` (the shelf pod,
                         D-67, DESIGN-025), `run-agent`
                         (a task, or a TUI, D-58), `claude-login[-status|-idle]` (the keeper pod's Claude login helper, T09 increment 5), `render`, `ctl status|rescue [--stop-agent]|prepare-restart` (D-40 to D-43,
                         D-48, D-58), `ctl deliver|log` (D-65), `ctl codex-pair` (Story110 T17), `ctl thread-start` (DESIGN-057), `ctl login url|code|status` (remote sessions), `ctl rescues|hold-rescue|prune` (D-67),
                         `ctl grant-install|grant-remove|grant-list|grant-use` (D-63),
                         `ctl history-export|history-archive` (DESIGN-025 H4), `ctl coord-archive put [--mark]|mark|lookup|get` (DESIGN-024 R2; journal grace, recorded-time or pinned get)
cmd/dev-env-allowance-handoff/  explicitly invoked one-time collector fingerprint handoff preparation and guarded apply; the elected operator runs the same fenced apply from `--allowance-handoff-file` (default off, in-slot backup)
cmd/agent-run/           the CLI, one static binary (CGO_ENABLED=0, D-06); commands in internal/agentrun, fixed internal allowance helper in internal/usagecollector
cmd/home-retention-dry-run/  standalone bounded offline HOME policy report (DESIGN-044); no collector, runtime integration or deletion
internal/version/        the build identity every binary's `version` prints
internal/testenv/        starts envtest (kube-apiserver + etcd) with config/crd/ installed, for test suites
internal/testsock/       ShortDir: a short /tmp directory for a test's Unix socket (108-byte sun_path; t.TempDir follows GOTMPDIR in a session pod); test support, imported only from _test.go files (OP506)
internal/templates/      parses and checks dev-env-templates, the GitOps data pods are built from; its revision (D-44)
internal/controller/     the AgentSession reconciler: each session's pod and volume (D-44), the reap finalizer and the
                         guarded deletes (D-45): the rescue by exec before a suspend deletes a pod, and the archive of
                         a reaped session's volume after a verified rescue (D-51), in a hold pod when the volume has no pod (D-55); a private
                         Session's HOME expiry up to Archived, default off (DESIGN-025 5.4: private_expiry.go, history_relay.go). Its envtest suite proves DESIGN-001
                         5.1 (no owner reference to the operator; no pod or volume write or delete outside the guards;
                         delete and suspend wait for rescue); a fake rescuer stands in for exec. The one Pod write
                         besides create and the guarded deletes is DESIGN-025's stop request (private_stop.go), pinned
                         by TestOnlyStopPrivateExecutorPatchesPods. DESIGN-054's scheduled-work releaser
                         (scheduled_work_release.go, no flag): records an ended or reaped scheduled session's occurrence
                         in its ScheduledWork (uncached reads, no watch) and lifts only the scheduled-work finalizer
internal/apiserver/      the operator's /v1 API (D-46): HTTPS runnable, TokenReview auth, caller classes, sessions,
                         heartbeat, suspend and resume (D-60), fleet, grants (D-56), activities (D-66), rescues (D-67) and the Codex thread route (DESIGN-057 10, threads.go) handlers;
                         also decisions, project canaries and their results, task claims, the inbox, published contexts, the
                         session directory, task budgets and archived history (DESIGN-025 8); CreateScheduledSession, the scheduled-work executor's fixed-name
                         creation entry beside CreateStandby, with createSessionMode's checks (DESIGN-054 S3b); the /v1/scheduled-work routes (S5b), behind `--enable-scheduled-work-api` (default off, 404 while off, independent of `--enable-scheduled-work`). Unit tests use the fake client; its envtest suite mints real
                         tokens and serves through a manager wired as the operator's
internal/podexec/        the one pods/exec client (WebSocket, SPDY fallback): the rescue, and the API's log, message (D-65) and thread (DESIGN-057 10) routes
internal/apiserver/apiv1/  the API's wire types, error codes and Claude effort table, standard library and agentd's standard-library protocol only, for agent-run
internal/activity/       the reaper of expired declare-activity declarations (D-66), a leader-only runnable in the operator
internal/reminders/      the reminder engine (DESIGN-032, #206): schedule arithmetic with explicit DST rules, target allowlist, a CalendarAdapter interface with a fake, and the leader-only Scheduler over the Reminder CRD; the keeper hosts it behind `--enable-reminders` (default off), the operator serves `/v1/reminders` behind `--enable-reminders-api` (default off)
internal/maintenancecontract/ bounded offline upgrade-shepherd envelope validator and correlation evaluator; a candidate is not operational deferral
internal/scheduledwork/  DESIGN-054's pure scheduled-session rules: the session-end rule, the terminal record an ended or reaped session gives its occurrence, and its write into a ScheduledWork's status; the fixed-name entry's Dispatch, collision and refusal types and the spec hash; no I/O
internal/scheduledwork/executor/  DESIGN-054's leader-only scheduled-work executor (S3b), behind `--enable-scheduled-work` (default off): claims each due occurrence by compare-and-set on the uncached ScheduledWork, checks the caps and revalidates, and creates through apiserver.Server.CreateScheduledSession under the derived name; no ScheduledWork informer; Sources (sources.go, S3b2) is its Authorizer and TemplateResolver: the dev-env-scheduled-work ConfigMap and the owner session and pod, read uncached, decided by the authority package
internal/standby/       the leader-only standby runnable (Story110 T25, plan 03): keeps one `remote` Claude AgentSession ready, found by its label, behind `--enable-standby-session` (default off); a ConfigMap-backed circuit breaker allows three creations in 30 minutes
internal/scheduledwork/authority/  DESIGN-054 S3b2's pure authority and template rules: SpecHash (every spec field but editor and cancelled), Decide (the standing basis or wait, and the human approval) and Choose (the narrower admitted one), TemplateDigest, and the strict fail-closed dev-env-scheduled-work templates parser and Resolve; no I/O, no executor import
internal/scheduledwork/pages/  DESIGN-054 S4a's page Reminders: Key (the deterministic name per item UID, occurrence, event and attempt), Build (the one-shot Reminder, due now, to the item's pushover notify refs only, owned by client/dev-env-system/scheduled-work), PageState (a Reminder's delivery status as sent, failed, ambiguous or skipped) and the best-effort Pager (create if absent; skipped on an unserved kind or a refused create; after AlreadyExists or an ambiguous create error, one verifying read under its own short deadline records only the page's own Reminder, else skipped; never update or delete); its callers are the executor and the releaser (S4b)
internal/shelf/          the operator's side of the shelf pod (D-67): finds it, lists and prunes rescues there by exec; the pruner is
                         a leader-only runnable; ExecSlots, the per-replica limit on archive execs (DESIGN-024 5, DESIGN-025 6.4);
                         HistoryReader, the history archive's reads for the API (DESIGN-025 8)
internal/agentrun/       agent-run's commands (D-50, D-58, D-60, D-65, D-66, D-67): -p, --local, list, show, log, msg, reap, suspend, resume, rescue list|restore, history list|show|read (DESIGN-025), declare-activity, attach, detach, login, fleet, rc start|status (DESIGN-038, client observations), thread start|status (DESIGN-057), maintenance check, pages list|show (offline, DESIGN-031), schedule create|list|show|edit|cancel|approve|retry (DESIGN-054); finds the API and a token in a session
internal/frontdoor/      Meatball's MCP server (DESIGN-060): one bearer, fifteen tools, opwork as a fixed-argument subprocess, coordinator start/status/stop and parent notes through agent-run's operator client
                         pod, in another pod (a minted token) or from flags; imports the standard library, apiv1 and
                         agentd's protocol, and the standard-library helpers maintenancecontract, pages, version, and tokenfile (`make build` checks the binary does not link Kubernetes); tests run against an httptest TLS server
internal/keeper/         the keeper (D-52), including the default-off Ask notifier (DESIGN-034) and summoned-session pager (DESIGN-064 6.8): mints the haynes-dev-bot token
                         into dev-agents/dev-env-gh-token every 40 minutes, and with --enable-ops-gh-token (default off,
                         DESIGN-064 6.2) the haynes-ops-bot token into dev-env-ops-gh-token. Both sit behind one Lease.
                         Only those non-rotating installation tokens are Keeper Jobs; the rotating Codex login and the
                         Proxmox credential grants are separate journaled, Lease-fenced leader runnables. Unit tests use a fake
                         GitHub (httptest) and a fake clock; its envtest suite runs it as its ServiceAccount with exactly
                         the Roles haynes-ops gives it
internal/broker/         the access broker (D-61): decides AccessGrants (GrantPolicy match, Decide for the approval page,
                         a Notifier), makes kube, break-glass and egress grants (a ServiceAccount and RoleBindings or a
                         ClusterRoleBinding per grant) and revokes them; ExecInstaller installs kube tokens on stdin into the pod's grants tmpfs (D-63), guarded by pod UID; older pods fail/revoke after three attempts. Its envtest
                         suite runs it as dev-env-broker under haynes-ops' RBAC and broker guard, copied into testdata/
internal/homeguard/testutil/  non-secret offline Root fixture and rendered Pod golden linking OP286 local stages
internal/homeguard/     exact original HOME binding validation, metadata only; default-off OP252 lifecycle/start guard
internal/homeretention/ pure two-tier HOME policy and signed preservation proof verifier (DESIGN-044); no I/O or deletion authority
internal/grantattest/    pure reserved decision bounds/snapshot/storage/audit helpers (DESIGN-042 S7, no authority) and the agent attestation of a grant (DESIGN-001 D-88): one annotation, its bounds and the scope digest, shared by the operator's API and the broker; pure. A claim, not verified human identity
internal/egress/         the scoped CiliumNetworkPolicy builder and ownership checks (D-64), shared by broker and operator
internal/grantexpiry/    the operator's separate egress expiry backstop (D-64), watches grants, reads CNPs by name only,
                         deletes by verified ownership and UID, and preserves the broker's grant audit record
internal/agentd/         agentd: config rendering (the dev-init.sh port), clone and worktree, the task runner, heartbeat,
                         status, rescue and its bundle on the shared volume (D-48), the private history export and the
                         shelf's history archive (DESIGN-025 7.1; historyexport_layout.go classifies every provider and
                         agentd name, and its scans keep the table complete); tests fake claude and tmux and run
                         git against a bare repo in t.TempDir()
internal/agentd/protocol/  what agentd and the operator exchange: the session document, the status, the heartbeat route,
                         the rescue report, and the rescue layout and manifest on the shared volume; the history
                         archive's wire types and the canonical JSON both sides hash (canonical.go, DESIGN-025 5.4)
internal/agentd/hostexecutor/  owns one foreground native computer for one immutable budget binding (no budget authority, no pairing)
internal/codexhostpool/  DESIGN-063's pure Codex host choice (OP698): host facts, a three-valued login fact (current, lapsed, unknown) and the caller in, the section 5 result out; standard library only, no I/O, the clock is an argument
internal/codexrc/        pure pinned Codex project/host discovery and rc-launch error mapping; no I/O, launch or pairing authority
internal/threadlaunch/   DESIGN-057's provider-neutral thread launch: registry (codex host, claude session, cursor/grok unqualified), Adapter interface, phase recorder and driver, and the read-only Observer pass behind thread status; pure, no I/O
internal/codexauth/      the access-only Codex document the keeper publishes and agentd reads; it cannot carry a refresh token
internal/claudeauth/     the access-only Claude projection document (dev-env-claude-live) and the merge of its access fields into a pod's .credentials.json; it cannot carry a refresh token; pure, no I/O (Story110 T09 increment 1)
internal/claudelogin/    the keeper-owned Claude login ceremony (Story110 T09 increment 5): the control socket's wire types and client, and the helper that runs the pinned `claude auth login` in a memory-backed staging HOME, prints only the sign-in link and reads the one-time code on stdin; reads no credential
internal/tokenfile/      bounded reads of rotating projected bearer-token files
internal/eventnote/      bounds an Event note to the API's 1024-byte limit on a UTF-8 rune boundary; pure, shared by the controller and the broker
internal/projectcatalog/ parses and validates the GitOps project catalog and its per-task snapshot; no filesystem or network authority
internal/projectsync/    agentd's bounded, model-free GitOps project management (`agentd project-sync|project-refresh`): actor, API, receipt
internal/pages/          bounded keeper Pushover page records, Alertmanager snapshots, Gatus current checks and shared existing-order/canonical-claim response projection (DESIGN-031); read-only source interfaces, no production adapters; standard library only
internal/providerallowance/  typed providerallowance.v1 observation and providerallowance.read.v1 cached-read contract: strict decode, caller-supplied trust, and derivation checks; no collector, adapter, storage, or network
internal/usagecollector/  bounded supported provider readers and private callback capture; fixed finite operator helper with durable cleanup receipts; no settings installation, automatic recurrence or publication; normal client auth/cache effects require qualification
internal/allowancecollector/  default-off elected-operator schedule: exact existing host fences, durable reserved cycles, status-only recovery and validated atomic aggregate publication (DESIGN-053); no session creation, grant or credential copy
internal/usageconsolidation/  bounded pure report projector over caller-authorized allowance reads and initial-invocation observations; no aggregation, collector, provider, clock, authorization, or I/O
internal/usagetotals/    leader-only counters of task tokens, cost, sessions and wall-clock seconds that outlive Session clean-up (DESIGN-059, OP524); one annotation on the AgentSession, no CRD change
internal/projectcanary/  validates the two finite operator-owned project rule canaries and binds a session to one
internal/projectcanary/testutil/  synthetic canary fixtures for tests
internal/commonrules/    retains the common instruction inputs (common text, Codex header) bound to an enabled managed task
internal/commonrules/testutil/  synthetic common-source fixtures for tests
internal/taskclaim/      the TaskClaim store: immutable explicit ownership, Create only, uncached reads; also the bounds
                         that all three coordination stores apply and their record-count metrics (DESIGN-010)
internal/inbox/          the inbox store: immutable API messages and explicit read acknowledgements, Create only, uncached reads
internal/asks/           the Ask store: create, list, withdraw, a set-once answer and the operator's leader-only CAS expiry sweeper behind --enable-asks; no inbox delivery in v1 (OpenProject Epic #254)
internal/contextpublication/  the PublishedContext store: immutable published excerpts, Create only, uncached reads
internal/delegatedresult/  pure delegated-result codec: strict bounded request and receipt decoding, frozen attempt identity and digest; Admission and Store have only the refusing Disabled implementation; no route, storage, provider or caller
internal/sessiondirectory/  the SessionDirectory store: bounded original session metadata and CAS witnesses
internal/sessionraise/  DESIGN-066 slice (a): the raise record, its signature, the operator ledger, the notice text and the two key pairs. A record counts only in Counts. Off by default and not installed
internal/sessiontitle/  DESIGN-065: validates a session title, names the model family, composes the display title, and classifies a recorded Claude Code capture-pane screen
internal/summonledger/  DESIGN-064 5.9: the create ledger's entry and charge types, codec and time rules (window, attempt hold, prune), shared by the API's write paths and the operator leader's admission
internal/summonoutcome/  DESIGN-064 6.6 and 6.8: the shared optimistic outcome/closure/pending-filing write, the one ordered guaranteed-close reason table, and the terminal page table
internal/summonquota/    DESIGN-064 6.9 (OP783): the leader's quota gate for summoned sessions: Decide (priority, reading, flag lines, clock) and the Gate the operator sets as Reconciler.SummonQuota; fails open
internal/taskbudget/     the task budget ledger: retained, deterministic task admission in ConfigMaps behind CAS (KubeStore)
internal/coordretention/ read-only paged coordination-record summaries and fail-closed release eligibility evaluation (DESIGN-024 R3 prerequisite)
config/crd/              CRDs generated by controller-gen; haynes-ops gets a copy by PR with every change, before the
                         operator pin that needs it (a stale copy prunes status fields silently; haynes-ops #3502)
images/operator/         Dockerfile: ghcr.io/thaynes43/dev-env-operator (operator + keeper + finite agent-run allowance helper); build args COMMIT, VERSION
images/portal/           Dockerfile: ghcr.io/thaynes43/dev-env-portal, the internal read-only portal; build args COMMIT, VERSION; publish.yml signs sha-<short>
images/frontdoor/        Dockerfile: ghcr.io/thaynes43/dev-env-frontdoor, Meatball's MCP server plus opwork; build args COMMIT, VERSION; publish.yml signs sha-<short>
images/agent/            Dockerfile: ghcr.io/thaynes43/dev-env, tag line 2.x (D-53); smoke-test.sh; tools/ (pve, hw-ssh, v1 copies). CI only, never in the pod
images/codex-login/      Dockerfile: ghcr.io/thaynes43/dev-env-codex-login, the keeper's login-only helper (D-78); published by publish.yml
images/git-wait/         the git-wait trial fixture: a patched Git build recipe; publish-git-wait.yml, fixture tags only
scripts/codex-clean-session.py  native client-only clean launcher and offline tests; [usage](docs/codex-clean-session.md), no operator or daemon changes
scripts/trials/          bounded synthetic trial scripts and their checks (git-wait, workspace CephFS); run only in reviewed fixture Jobs
scripts/trials/native_lifecycle_profile/  renders the isolated native lifecycle profile; provisions nothing
scripts/trials/pilot_rendezvous/  default-off bounded existing-CLI pilot gate/one-dispatch harness; offline injected checks
scripts/trials/workflow_handoff/  offline coordinator handoff checker; validates one bounded record and prints the next manual action; --watchdog is finite output-only PR-bound owner-wake and escalation planning; does not dispatch, grant, or write tracker state
scripts/trials/stopped_handoff/  offline synthetic stopped-handoff fixture driver; validates one record and local git fixtures only; does not dispatch, stop a live executor, or change a TaskClaim
scripts/delivery/        source-only wrapper for the pinned haynes-ops delivery guard; verifies that checkout, then invokes it once; no reconcile, release, or deployment
images/THIRD_PARTY.md    what each published image bundles and under which license; update it with the Dockerfiles.
                         It and LICENSE are copied into the published images, whose license directories CI checks (#55)
Makefile                 generate, check-generated, check-layout, lint, test, envtest, build, build-portal, build-frontdoor, build-agent-run-darwin, tools
.golangci.yml            golangci-lint v2 config
LICENSE                  MIT, copyright Tom Haynes
```
