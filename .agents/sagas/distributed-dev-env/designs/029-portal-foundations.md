# DESIGN-029: Portal foundations: source inventory, Attention journey and security evidence

**Historical foundation status: source-only first milestone for [Story 178](https://work.haynesnetwork.com/work_packages/178).**
This is a bounded foundation under Epic 120 / Saga 85, related to 121. It is
not an accepted portal architecture, an approval surface, a new orchestrator,
or delivery of the wider console epic. Execution and custody are recorded in
the [progress receipt](../../../context/2026-10-23-portal-progress.md).

## Controlling inputs and pending decisions

Read at freshly fetched dev-env commit
`380a759d947faf1131653867cdecf4b7ade878f5`:

- [DESIGN-001 section 3.8](001-dev-env-v2.md#38-the-console) and Q-16: the
  console is planned; its grant-approval page was removed. Approvals remain
  in the Claude Code app under the existing workflow. No new portal approval
  authority follows from displaying an action or an agent's output.
- [Plan 03](../backlog/03-remote-control.md): existing Remote Control,
  keeper-owned login and workbench mechanisms; console remains open.
  Historical installation tables are dated evidence, not a new runtime check.
- [Plan 13](../backlog/13-phone-browser-management.md) and the current
  [HANDOFF](../../../HANDOFF.md): accepted phone checks stay accepted. Do not
  repeat them for portal development. This pass's internal-only scope
  does not implement plan 13's possible later outside-access route.
- [Launch-profile preflight](../../../../docs/launch-profile-preflight.md):
  profile selection, credential availability, network reachability and action
  authority are separate. Installed `hw-ssh` is not proof of any of them.
- [PR 292's single requirement file](https://github.com/thaynes43/dev-env/blob/6e7c364ce4a23842895697a8bfe3167f264a8efe/.agents/sagas/distributed-dev-env/requirements/2026-10-14-multi-client-viewer.md):
  open **draft / ungroomed**, not accepted design or implementation authority.

Canonical #178 journals **851, 853 and 854** carry the parent's relay of the
existing Astra advisor's input. It precedes this document: authenticated
control-plane view of existing truth; Attention groups below; server OIDC/BFF;
current server authorization, typed commands, source preconditions and
idempotency; official Remote Control links; no embedded terminal/browser.
The full design was received in journal **859**, marker
**`PORTAL178-ASTRA-FINAL-20261023-01`**, followed by the source-map addendum in
journal **860**, **`PORTAL178-SOURCE-MAP-ADDENDUM-20261023-02`**. The owner read
and acknowledged both in #178 before making consequential choices. The full
input supersedes the earlier pending-design status for source direction only;
operational configuration and activation holds remain. No new advisor, worker
or reviewer was started. Concrete backend identity/authority mapping, reviewed
session storage and deployment configuration remain unresolved. No production
adapter is wired.

### Documented Astra direction and implementation constraints

Use a maintained frontend with a **same-origin Go BFF**, fitting the existing
Go control plane; do not add a TypeScript backend. Keep task, session and
authority sources canonical. Portal persistence may hold portal sessions,
audit, preferences and command-idempotency receipts; it must not become a
scheduler or duplicate #177's workflow model. The static Python fixture below
selects neither the production frontend framework nor backend placement.

OIDC is confidential, server-side authorization code with PKCE S256 and exact
registered HTTPS callbacks. Key identity by issuer plus subject. Validate
issuer, audience, algorithm, expiry, state and nonce. Tokens stay on the server,
never in browser storage. Use an opaque Secure, HttpOnly, host-only `__Host-`
cookie with SameSite Lax and separate CSRF/Origin checks. Local logout/revoke
must work independently of IdP availability, across replicas and streams.
The proposed source-contract defaults are **30-minute idle, 12-hour absolute
session and 5-minute privileged elevation**; they require threat-model review
and are not selected production policy or an SLO. Background polling is not
user activity. Verify actual deployed MFA/logout semantics before claiming them.

An OIDC issuer-plus-subject principal is not an operator human or workbench
caller. Authentication and authorization remain separate. Existing
[`auth.go`](https://github.com/thaynes43/dev-env/blob/380a759d947faf1131653867cdecf4b7ade878f5/internal/apiserver/auth.go#L61)
and [`caller.go`](https://github.com/thaynes43/dev-env/blob/380a759d947faf1131653867cdecf4b7ade878f5/internal/apiserver/caller.go#L99)
require audience-bound ServiceAccount authentication and explicit caller
classification. The BFF must enforce each user's project/resource scope;
portal login does not confer either caller identity. The keeper remains
the sole refresher of Claude/Codex provider logins. The portal never holds,
refreshes or proxies those provider refresh tokens; see the existing
`internal/keeper/claude_worker.go` and `internal/keeper/codex_worker.go`
ownership boundary. A portal OIDC session is a separate identity concern.
Canonical #178 journal 866 is the parent's explicit source-boundary addendum;
the Go citations above are its pinned implementation evidence.

Enforce deny-by-default resource/project/environment scope on every read,
download, stream and command. Existing valid standing authority remains valid;
ask only for material scope deltas. Known denials and action-specific holds
remain. Commands must be narrow typed adapters, checking current entitlement,
target, exact SHA, constraints, source-state precondition and idempotency bound
to the material request. Timeout/interruption means Unknown and reconciliation,
never blind replay or optimistic success. Stale/offline pages cannot queue
privileged actions. No general shell, proxy or filesystem browser endpoint.

Render agent/repository/log output as escaped plaintext by default. Optional
Markdown needs sanitization. Do not serve active HTML/SVG artifacts from the
authenticated origin, raw HTML or remote images. Use structured safe responses
and pre-response redaction; never collect/log tokens, cookies, pairing codes or
full untrusted prompts/responses. CSP adds defense, not an escaping substitute.
Connector destinations must be trusted configuration, with redirect/egress
bounds and SSRF protection for metadata endpoints and loopback. Trust forwarded
headers only from the actual Traefik path; verify backend bypass and spoofing.
Bound request/log volume, streams, rates and reconnects.

Audit actor, versioned scope reference, resource, action, material-parameter
fingerprint, exact SHA, request ID, precondition and denied/failed/unknown/success
outcome. Audit storage/access/retention must preserve attribution and exclude
secret material. This document specifies tests; it does not add that storage.

The mobile experience is Attention / Tasks / Sessions, with official session
links and capabilities, no pairing secrets. Task outcome, liveness, review,
authority and release state stay distinct. Review return proceeds through
acknowledgment and finding disposition to exact-SHA recheck; CI and release
authority are independent. Support accessible touch/keyboard flows and
reconnect without duplicate actions; defer embedded terminals and browsers.

## Pinned source inventory

All dev-env paths below are at the base above. They describe code, not observed
deployment. The repository is a Go module (`toolchain go1.27.2`), with standard
`net/http` API handlers. It has no existing portal package, frontend manifest,
browser session store or OIDC callback among the inspected entry points.

| Source | Existing contract | Portal implication |
| --- | --- | --- |
| `internal/apiserver/auth.go`, `caller.go` | Audience-bound Kubernetes TokenReview; human, trusted-client, session and separately configured coordinator classes; live pod/session UID checks | Authentik authentication alone is not this API's authorization. Never send a ServiceAccount or agent token to a browser or label a portal backend as workbench to gain access. |
| `internal/apiserver/server.go` | `/v1` JSON routes; sessions, fleet, directory, claims, inbox, contexts, history, grants and keeper-login; fallback 404 | Reuse exact reviewed read adapters. API availability and each route's scope are distinct; do not expose a generic reverse proxy. |
| `internal/apiserver/sessions.go`, `apiv1/types.go` | List omits prompt; detail includes prompt. Session phase, agent state, task outcome, usage and remote state are distinct fields. | Explicitly allowlist browser metadata. A raw detail proxy leaks more than inventory needs. Configured model and provider-reported served model remain separate; absent usage/capacity is Unknown. |
| `internal/apiserver/sessiondirectory.go`, `apiv1/sessiondirectory.go` | Explicit metadata-discovery project scope; live identity proof; query-bound pagination, at most 20 entries/page | Discovery is useful input, not payload/history authority. Preserve source timestamp, resource version, source and placement uncertainty. |
| `internal/apiserver/taskclaims.go`, `inbox.go`, `publishedcontexts.go` | Existing ownership, ACK and context routes with separate enablement, identity and payload scopes | Read metadata only initially. An inbox read/ACK is not acceptance. Do not copy private transcripts or add workflow transitions. |
| `internal/apiserver/keeper_login.go` | Even status is gated to workbench; fixed provider commands; sign-in material is ephemeral and never logged | Login renewal is a later narrow operation with existing ceremony/authority. No token/file reads, login URLs or codes in this foundation. |
| `internal/agentrun/mcp.go` | Three access tools use existing `/v1/grants`; they neither approve nor install grants | Reuse tool semantics later; no browser toolbox execution or generic shell adapter. |
| `cmd/dev-env-operator/broker.go` | Broker reconciliation, grants, metrics and health listeners; no console listener in startup | DESIGN-001's planned broker UI is not an installed web app. Do not infer service placement from old prose. |
| `internal/templates`, `internal/apiserver/validate.go`, `docs/launch-profile-preflight.md` | Templates resolve profile; children retain parent ceiling; workbench-origin Codex hosts deliberately downscope | Display declared/resolved capability separately from authority. No portal profile selector or auto-escalation in this pass. |
| `images/agent/tools/hw-ssh.sh`, templates below | Existing CLI can run powerful host commands; the current workbench template alone supplies its key | Inventory only. No shell, SSH, remote-host API, key access or credential duplication. Old script comments do not override current profile rules. |

The inspected API has transport timeouts; this is not evidence of portal
per-user/resource rate limiting. Browser OIDC sessions, CSRF and narrow portal
authorization are missing prerequisites, not protections furnished by TokenReview.

## Internal route and identity evidence

Public GitOps snapshot:
[`haynes-ops@2aeda3157a0300f2f31705008df6c9a8ee48a03c`](https://github.com/thaynes43/haynes-ops/tree/2aeda3157a0300f2f31705008df6c9a8ee48a03c).
The recursive tree response was complete (`truncated=false`). Selected paths
below are relative to `kubernetes/main/apps/` at that snapshot.

| Path | Git blob | Result |
| --- | --- | --- |
| `dev-env-system/broker/app/helmrelease.yaml` | `b8830d991c5856e24f03089c74bdf1e11618ce8f` | No route/ingress/host declaration found. |
| `dev-env-system/operator/app/helmrelease.yaml` | `7b2e31b7ce6cfab0e84cdfad58c65b2adeede6e6` | Internal API service, port 8443; no portal route. |
| `dev-env-system/keeper/app/helmrelease.yaml` | `070a274e1e39220907fe1c98a93678241a6331ae` | No route/ingress/host declaration found. |
| `dev/dev-env/app/ingressroute.yaml` | `c096f970a5499c38969911505f7eeacbc14546f8` | Existing **V1** `Host(dev-env.haynesops.com)`; `traefik-internal`, target `internal.haynesops`, `websecure`, `certificate-haynesops`, middleware `network/ak-outpost-dev-env-outpost`, service `dev-env:8443`. |
| `network/authentik/app/ingressroute.yaml` | `d76facbcfa1ff14c7ee6d420c483b14c9c92f71d` | Declares `authentik.haynesnetwork.com`; no `authentik.haynesops.com` in this route. |
| `network/authentik/app/blueprints/50-dev-env.yaml` | `dbb1894941d275cd3dcf137ee792710abe5347e3` | V1 forward-auth application/outpost. Preserve it unchanged. |
| `network/authentik/app/blueprints/90-cigar-journal-oidc.yaml` | `3c0f98149b5149c39221cbb8bf322960bcf43331` | Confidential client example; explicit code grant, strict callback, `user_uuid` subject, `per_provider` issuer. Not portal config. |
| `network/authentik/app/blueprints/95-haynes-quest-oidc.yaml` | `e1e2cb6e6fe45bf27a0adbea6f69089c1a979f5a` | Public PKCE client example; same subject/issuer and strict callback patterns. **Not** a confidential BFF template. |
| `dev-env-system/templates/app/templates.yaml` | `2021c1b3ccc1bf761f478bcc382a399d3bcac0e6` | Default `full`; workbench uses `dev-env-workbench`; only workbench includes the hardware-key source. Metadata inspected; no credential material read. |

Source-map addendum 860's desired versions were independently checked at the
same pinned public snapshot: Authentik chart `2026.8.5`
(`network/authentik/app/helmrelease.yaml`, blob
`e5d012d13276ffbe1f5029c5c279787af09b5c09`) and internal Traefik OCI chart
`42.1.3` (`network/traefik/traefik-internal/app/ocirepository.yaml`, blob
`ac8db0633cbfbd59b7c0bff4933b1682d3f88baa`). These are desired GitOps
versions, not observed running versions. Internal Traefik's HelmRelease
(blob `6ce355356b62d42d1b465aaf12ae3df601b71c0b`) sets both provider ingress
classes to `traefik-internal`, with `allowExternalNameServices=true` and CRD
`allowCrossNamespace=true`. It has no explicit `forwardedHeaders` /
`trustedIPs` fields. Omission proves neither secure nor insecure effective
settings. Chart defaults, rendered configuration and actual deployed
trust/spoofing/bypass behavior remain Unknown until authorized checks.

Route search was bounded to `dev-env-system/**` files named HelmRelease,
Ingress/IngressRoute or HTTPRoute: three HelmReleases, no standalone route
file, and no `Host(...)`, `hostnames`, ingress/route block or ingress kind in
those candidates. This is scoped negative source evidence, not an exhaustive
cluster/DNS scan. **No exact V2 portal hostname is declared in these inspected
sources.** Internal convention is not a URL assignment. A separate,
non-colliding host and reviewed authorized GitOps route are prerequisites;
inventing a name or reusing V1 would not satisfy them. No DNS, ingress,
Authentik, network, RBAC, deployment or credential configuration was changed.

## Metadata-only mobile journey

Historical foundation journey; this section does not describe the later scoped send/history capabilities. See [Current combined source contract](#current-combined-source-contract-2026-10-27).

The first screen answers: what needs attention, who owns it, what happens
next, and how recently was that verified? The existing workflow owner
([#177](https://work.haynesnetwork.com/work_packages/177))
supplies canonical workflow truth. This portal displays it; it does not
reimplement that owner's reducer or infer task completion from worker exit.

Overlap-only open-PR metadata at pickup identifies
[PR 524](https://github.com/thaynes43/dev-env/pull/524) as the active workflow
contract work. Its implementation was not inspected or modified. Runtime
integration depends on its owner's accepted contract and exact reviewed head;
these fixtures deliberately consume no workflow state or transitions.

| Attention group (Astra journal 851) | Required visible evidence | Next step displayed |
| --- | --- | --- |
| Needs you | Existing decision/hold reference and exact scope; accountable owner; next actor; age / last verification | Open canonical decision in its existing surface. No grant or approval button. |
| Needs worker | Canonical work reference, current accountable owner and next actor | Show the already recorded dispatch/pickup action; do not launch or silently assign a worker. |
| Review returned | Reviewed commit and receipt, acknowledgment/fix/recheck state as supplied by canonical truth | Owner acknowledges and fixes or answers findings; CI, exact-head recheck and release authority remain separate. |
| Handoff missing | Worker liveness and missing result/acceptance evidence shown independently | Accountable owner collects the result; “exited” alone cannot become completed. |

Every card keeps canonical source, accountable owner, next actor/action,
source update time, last verified time and freshness together at phone width.
Missing owner/action/time is visibly Unknown. Future timestamps are invalid
evidence; stale data stays labeled stale. A failed/denied read is unavailable,
never “nothing needs attention.” A verified empty result is a separate state.
An observation's age does not become a task deadline or approval expiry.

Read detail without losing the list position. Keep task outcome separate from
worker phase. Show source/tested/released/installed/accepted proof separately;
no green completion badge based on exit, acknowledgment, review receipt or CI
alone. External links must come from an exact allowlist and carry no login
material. Use official remote-session surfaces later; no terminal, iframe,
embedded browser, automatic remote preview or transcript import here.

The independent foundation may contain finite synthetic metadata and a static
mobile preview. Its renderer consumes only those fixtures, does no network
fetch or command dispatch, and selects no production frontend/auth stack.
Synthetic display checks cannot establish authorization or runtime security.

## Threat model and finite evidence map

Protected assets: canonical task/owner and review truth, private source/context,
portal sessions and authorization, agent/keeper credentials, control-plane
availability, and audit integrity. Treat browser input, agent prose, logs,
remote URLs, stale events and imported metadata as untrusted. The intended
browser → portal/BFF → existing API and portal → Authentik boundaries require
review before implementation. Agent output cannot define roles or authority.

| ID | Threat / required behavior | Finite synthetic test and proof still needed |
| --- | --- | --- |
| T01 | Forged identity, issuer/audience/nonce mismatch, callback substitution or code replay | Future OIDC callback tests: wrong issuer/audience/state/nonce, missing PKCE, expired/reused code and non-exact callback rejected. No OIDC handler exists in this pass. |
| T02 | Authenticated user crosses project/object/role scope; browser forges owner/admin; backend becomes confused deputy | Future server adapter tests: viewer read allowlist; denied object/project; revoked/changed role rechecked on each request; never promote to workbench. Fixture Unknown/denied presentation only. |
| T03 | Session theft/fixation, logout or IdP revocation leaves portal usable | Future server tests: opaque Secure/HttpOnly session cookie, deliberate SameSite policy, rotate at login, idle/absolute expiry, local revoke across replicas, replay denied; deployed IdP logout separately verified. |
| T04 | Cross-site command, login or logout request; permissive CORS | Future cross-origin/state/CSRF fixture matrix. GET stays read-only; CSRF token plus Origin policy for state changes; no wildcard credentialed CORS. Session cookie policy alone is not full CSRF protection. |
| T05 | Agent HTML/script/ANSI/URL injection, remote image exfiltration | Current fixture preview: escaped text only, bounded strings/rows, no raw HTML/Markdown/ANSI execution or automatic external subresources; allowlisted links. Future production browser/CSP tests still required. |
| T06 | Raw detail/prompt/transcript/credential leakage through payload, logs, cache or errors | Fixture schema excludes all extra fields; secret-shaped synthetic extra fields rejected. Future adapter negative tests and captured network/log/cache evidence required; redaction alone is not safe access control. |
| T07 | Stale/conflicting evidence implies task completion, empty inbox or review pass | Fixture cases: exited worker with missing handoff; separate task outcome; missing/future/stale timestamps; denied/unavailable vs verified empty. Review/CI/head provenance remains canonical input. |
| T08 | Forged command, shell/SSRF target, stale UID/head, duplicate/replayed request or timeout | Future narrowly typed adapter tests: current server entitlement and scope reference, UID/source precondition, idempotency key bound to actor/resource/arguments; changed state rejects; timeout reports Unknown and reconciles without blind replay. No command endpoint here. |
| T09 | Unbounded polling/output/pagination/login attempts consume control plane | Fixture bounds: maximum rows, field length and input bytes; no polling. Future per-principal/IP/resource limits, bounded pages/timeouts, SSE connection/reconnect caps, 429/backoff tests and audit evidence. No load test. |
| T10 | Forged/sensitive audit record, mutable owner/review source | Future append/retention and attribution tests: actor, action, resource UID, head/version, reason, decision, correlation and outcome; no tokens/codes/bodies. Store/channel and access policy await Astra review. |
| T11 | Logout or MFA label overstates actual Authentik behavior | Future tests verify deployed RP/local logout and administrative revocation independently. Fresh MFA must be backed by verified flow/claims and age policy; `max_age=0` / `acr_values` alone is insufficient. No live login/logout test now. |
| T12 | Prototype accidentally becomes an exposed control surface | Fixture preview has no live API, auth configuration, commands or deployment manifest. Production wiring requires the internal gate below; public publication has a separate gate. |
| T13 | Forged forwarding/identity headers or direct backend access bypasses the real proxy boundary | Future tests: external spoofed headers ignored/refused; only actual Traefik trusted; backend cannot bypass authn/authz; effective chart/rendered/deployed settings verified. Source omission is not a pass. |
| T14 | Connector URL/redirect becomes SSRF to loopback, metadata or another private service | Future adapters accept configured destinations only; reject user-supplied hosts, redirects and URL ambiguity. Bound egress and response size. No connector/network call in current fixtures. |

The [Authentik logout documentation](https://docs.goauthentik.io/add-secure-apps/providers/oauth2/frontchannel_and_backchannel_logout/)
currently labels front/back-channel logout Preview and requires explicit RP
support. Documentation is not proof of the deployed version/configuration.
Portal-local revocation must work independently. The
[OIDC provider documentation](https://docs.goauthentik.io/add-secure-apps/providers/oauth2/)
is the implementation reference; neither V1 forward-auth nor an existing app's
public client establishes portal policy. Browser session test criteria also
follow the [OWASP session guidance](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html).

## Delivery gates and next safe step

| Stage | Required evidence | Current state |
| --- | --- | --- |
| Foundation source | Pinned inventory, scoped route evidence, documented Astra input/pending choices, mobile metadata examples, finite threat fixtures, exact head/base | This pass; source/fixtures only. |
| Internal MVP source/release | Full Astra input acknowledged; concrete identity/authorization/session implementation reviewed; typed read adapters and negative tests; final-head independent native Claude review coordinated by parent; every finding resolved; required `CI - Success`; normal authorized agent-opened PR merge/release | Design input received. Implementation/review pending. No redundant Tom merge permission required. |
| **Gate A: authorized internal operation** | Pinned deployed identity/ingress versions and source map; exact separate hostname; reviewed OIDC client/identity and existing configuration authority; authorized GitOps route/TLS/network/service changes; source/running digest match; invalid OIDC rejection; two-user/two-project guessed-ID/download/stream isolation; proxy-header spoofing/backend bypass; CSRF/XSS/hostile approval text/secret canaries and bounded logs; double-click/concurrent-tab/timeout-after-write/stale-SHA idempotency; revoke/logout/expiry/permission withdrawal closes access and streams; deployed MFA semantics; no-public-route proof; mobile review reaches its owner, exited worker retains successor or blocked custody, unchanged authorized scope does not re-ask, reconnect does not duplicate action | Held separately. No deployment, credential/access change, grant or runtime acceptance is authorized by this source packet. Preserve V1 and running sessions. |
| **Gate B: separate public hardening/release** | Explicit public-exposure authorization; applicable OWASP ASVS 5 Level 2 evidence; independent authz/action/hostile-output review; no unresolved critical/high findings; separate client/origins/TLS/ingress limits; measured identity-revocation bound (60 seconds is a suggestion to verify, not an achieved SLO); external negative tests/lost-device revocation/rotation/restore/rollback/ingress kill switch; audit/incident evidence; dependency/security checks; exact tested artifact match and final review/CI | Separate future gate; **do not publish publicly**. Internal access is not a waiver of authentication or authorization. |

Next safe step is source-only fixtures and a small ready PR, then parent-owned
coordination of the required exact-head independent review. Do not dispatch
that review from this initial pass. Keep implementation and identity/route gaps
explicit, retain the dedicated implementation owner for fixes and CI, and
preserve supported parent pickup at the pass boundary. A completed executor
or source milestone is not completed portal delivery.

### M2 read-boundary source, 2026-10-23

After PR525 merged as `597e2cb2fb56a7ce81e055d5f0ed601de126c6ce`, the sole
authorized implementation successor continues in `web/portal/`. The
[M2 read contract](../../../../docs/portal-read-boundary.md) specifies the
Go standard-library handler/template, typed summary dependencies, explicit
resource/project/environment scope and post-read revocation checks. Fixture
and pure projection tests establish source behavior only. The #177 adapter
stays unavailable until its accepted interface is separately integrated.
No listener, OIDC middleware, operator-human mapping, live connector or
runtime authorization is installed by this increment. Gate A and Gate B
remain separate and unmet.

### M3 session source, 2026-10-23

Canonical #178 journals985/993 continue the same implementation owner after
PR526 merged. The [session-boundary contract](../../../../docs/portal-session-boundary.md)
selects maintained coreos/go-oidc/v3 with the existing oauth2 module, typed
single-use transaction/shared-session storage interfaces, opaque cookies,
current local revocation, Origin/CSRF logout and fail-closed handler composition.
Offline signing keys, clocks, stores and transports provide finite source tests.
There is no production storage fallback, running listener, provider call,
runtime human mapping, metadata adapter or ingress activation. The approved
confidential-client setup remains separate infrastructure work. These source
tests leave Gate A and the public ASVS gate unmet.

### M4 mobile presentation, 2026-10-23

Attention rendering keeps the accepted read and session contracts. The phone
and desktop page shows canonical source, owner, next actor, next action, age,
source update, last verification and freshness without a disclosure. Task
outcome, worker liveness, review, reviewed head, authority and release remain
separate labeled facts. Facts that a group must show stay outside the disclosure. Verified empty snapshots
show the four groups at zero. Unknown and stale listings do not present a
current zero count. A returned review keeps review state and reviewed head visible. A missing
handoff keeps worker liveness and task outcome visible. A single-task page
does not present the other groups as verified empty. Offline browser checks
are one checker run at a time, bound to a source-head manifest, and remain
synthetic presentation evidence, not device, authentication or Gate A acceptance.

### M6 same-connection recovery source, 2026-10-23

After accepted PR 529 merge `d4d21ef9`, the sole fresh source successor implements
transaction-bound exact incarnation admission and the [M6 recovery contract](../../../reports/portal178-m6/README.md).
Fixed external ConfigMap GETs carry no per-session Kubernetes state or mutation
power. Measured identity must belong to the actual SQL transaction, with exact
UID/RV snapshot checks around commit. Runtime remains 503. Disposable asynchronous
failover/RBAC/latency evidence, final-head native review and required CI must be
recorded separately; live admission RBAC and DB/auth/isolation/Gate A and shared
Authentik logging remain external gates. UI/infra/Future121 ownership is unchanged.

### M6 production assembly, 2026-10-24

The internal portal executable is `dev-env-portal`. It loads the mounted contract, wires the existing session engine, current authorizer, PostgreSQL store, and same-connection fence, and listens only on TLS port 8443 for `dev-env-v2.haynesops.com`. Unconfigured `NewRuntimeAssembly` remains unavailable. Production attention stays on `UnavailableReader`. Sessions and Tasks are separate optional reads. `config/operator-api-url` is `https://dev-env-operator.dev-env-system.svc.cluster.local:8443`. `config/openproject-base-url` is `https://openproject-web.frontend.svc.cluster.local:8443`. Tasks requests `/api/v3/projects/3/work_packages?offset=<page>&pageSize=50&filters=%5B%5D` and treats offset as a page number. The portal ServiceAccount is refused unless `--enable-portal-reader` is on, and it cannot also be a human, client, session, or coordinator. Session pages use a bounded namespace list and the snapshot continuation token. Freshness comes from `status.agent.lastHeartbeat`. `secrets/OPERATOR_TOKEN` and `secrets/OPENPROJECT_TOKEN` are independent: absent means that view is not connected, and a present malformed file fails startup. The operator token is re-read per request, must expire in the future, and its only audience is `dev-env-operator`. The operator flag `--enable-portal-reader` defaults off. When on, only `system:serviceaccount:dev-agents:dev-env-v2-portal` may `GET /v1/sessions`, and only for namespace `dev-agents` and repository `dev-env`, with the narrow summary page. No new Kubernetes RBAC and no TaskClaim path. Tasks reads project 3's work-package collection and does not follow another origin. Schema 2 bindings remain a deferred attention parser, not the Tasks inventory. The #177 group contract remains the offline checker in `scripts/trials/workflow_handoff`. Canonical 1Password item is existing `HaynesKube/dev-env`; proposed items `dev-env-v2-portal`, `dev-env-v2-portal-runtime`, and `dev-env-v2-portal-migration` are superseded and are not selectors. OIDC client id value `dev-env-v2-portal-internal`, the provider, and the callback are unchanged. Runtime field population on that same item stays held. Consumers may stage inert individual `remoteRef.property` entries for `DB_USERNAME`, `DB_PASSWORD`, `SESSION_AEAD_KEY`, and `SESSION_GENERATION`; that definition is not population, installation, SecretSynced, or an access grant. Startup still fails closed until those files are mounted. (Superseded 2026-10-26 by "Group-based access" below.) The subject pin stays the existing issuer-plus-subject input in `config/subject`, not a new item property. This source does not install, populate, or read the item.

### Group-based access, 2026-10-26

Ruling (Tom, 2026-10-26, #178 journal 1786): "No we should manage users by group not by ID." This supersedes the M6 sentence that keeps a subject pin in `config/subject`.

- The portal no longer reads `config/subject`, and no `PORTAL_SUBJECT` field exists.
- Any issuer-verified subject may hold a session. Every authorization still checks a fresh UserInfo response for that same session's subject, with `portal_is_active` and `portal_is_admin` both `true`.
- `portal_is_admin` is the provider-local mapping's direct-membership check of the pinned existing Admins group. Adding or removing a member of that group in Authentik grants or withdraws access at the next request.
- Session binding, revocation, CSRF and the resource allowlist are unchanged.
- In haynes-ops, the Authentik application's user-specific binding goes away in a later reviewed step. The Admins group binding stays.

### Group-based action scopes, 2026-10-29

Tom's 2026-10-26 ruling, "No we should manage users by group not by ID,"
also governs send, history and launch. On 2026-10-29 he selected "Same group
as portal login" for send and history; launch follows the same rule. This
supersedes the subject-bound action configuration in the dated October 27
source contracts below (OpenProject #248, related #244 and #239, Epic #120).

The private `config/session-actions.json` names only resources:

```json
{
  "send": {"targets": [{"name": "agent-example", "uid": "uid-example"}]},
  "history": {"targets": [{"name": "agent-example", "uid": "uid-example"}]},
  "launch": {"repos": ["dev-env"]}
}
```

Each action is optional and stays off when absent. Only these exact keys
are accepted. Target name/UID validation, duplicate rejection, the limit of
64 targets per action, the launch repository limits and the file size bound
remain in force. A file carrying `issuer` or `subject`, including the old
per-person format, is refused as a whole at load: no action scope is returned
and startup fails closed. There is no compatibility fallback.

Each request uses its own authenticated portal-session principal. The issuer
remains pinned by the existing session verifier and current authorizer; it
is not supplied by the action file. Live UserInfo must identify that same
subject and have `portal_is_active` and `portal_is_admin` both true. Two
different admitted principals may use the same configured resources. Send
and launch retain their initial authorization and fresh check immediately
before dispatch; history retains its checks before fetch and before releasing
the response. Request-private session references, token digests, expiry and
revocation checks, CSRF, attribution, receipt privacy and replay protections
are unchanged. Configured send and launch forms are available to any admitted
principal; an unconfigured action shows no form.

The operator's `--portal-send-target` and `--portal-history-target` flags
remain the independent per-session name/UID allowlists. The portal's resource
configuration grants no operator target outside those flags. Operator launch
policy and repository checks also remain unchanged. Source tests and a PR do
not establish deployment or authenticated runtime acceptance.

### Recovery witness dropped, 2026-10-26

Ruling (Tom, 2026-10-26, #178): drop the admission witness and recovery fence.
PostgreSQL restarts and failovers must need no human action. Later the same day:
"do the fastest path to a working demo, don't get hung up on edge cases of fancy
security stuff yet."

- The portal no longer reads `config/admission-api-url`, `config/admission-uid`,
  `config/admission-token-file`, `config/witness-gets-per-second`,
  `config/witness-get-burst`, `ca/admission-ca.crt` or the projected admission
  token. It makes no Kubernetes request and does not measure the backend
  incarnation. Leftover mounted files are ignored.
- Accepted risk: after a restore from backup, or a failover that loses an
  acknowledged revoke, a revoked session can authenticate again until it
  expires (at most the 12h absolute TTL).
- Mitigation: delete the session rows, or rotate `SESSION_GENERATION`. Rotation
  still invalidates every existing session and pending login.
- This supersedes the M6 recovery contract and its manual close, restore, then
  CAS-open flow (the "M6 same-connection recovery source" section above and
  `.agents/reports/portal178-m6/`). Session handling, CSRF, OIDC, group
  authorization, TLS pins and the PostgreSQL store are otherwise unchanged.

### Entry and status colors, 2026-10-26

Tom signed in on the first live deploy. Authentik's app tile opened the bare
origin, which had no route and showed the read boundary's refusal text. He also
said the green status icons clash with the theme.

- `GET /` with no query redirects (303) to Sessions.
- Sessions or Tasks without a current session redirect (303) to `/auth/login`.
  A signed-in user whom current authority refuses still gets 403, so a
  refusal never loops back to sign-in.
- The `ok` tone (the Running badge and the live Read-only dot) uses the accent
  lavender instead of green, in both color schemes. Idle and Starting turn
  muted grey so that Running stays the only accent badge. Waiting (amber),
  Busy (blue) and Failed (red) are unchanged.

### Usable demo pass, 2026-10-27

Tom: "Is the site usable? It looked stubbed out when I logged in." Rendering
the live session inventory and the live OpenProject collection through the
portal's own decoders showed why.

- **Tasks could not load.** OpenProject 17's `nextByOffset` link points at
  `/api/v3/workspaces/3/work_packages` and carries extra keys
  (`includeSubprojects`, `showHierarchies`, `showSums`, `sortBy`). The reader
  refused the whole collection because of it. The reader already builds every
  page URL itself, so it no longer checks or follows the collection's paging
  links; the page arithmetic (`total`, `count`, `pageSize`, `offset`) is
  unchanged. A 96-package live collection now decodes in two pages.
- **Tasks show what each item is.** The list shows the type (Saga, Epic,
  Story), the parent, and the Execution owner's worktree name. The detail
  adds a plain-text excerpt of the description, at most 600 characters, with
  link targets and Markdown marks removed. Empty owners are left out instead of
  shown as Unknown. Draft and Ungroomed group as Planned, Paused as Needs
  attention. Ruling reversed under Tom's 2026-10-26 fastest-path direction:
  the description was excluded before; a bounded excerpt is now shown to the
  Admins group. The full text stays in OpenProject behind the row's link.
- **Stubs removed.** The Automations and Watchdog "Planned" entries, the
  Planned page and the all-Unknown provider allowance bar are gone. A remote
  or interactive session has no Usage panel instead of a panel of Unknowns.
  The Usage view's tiles count measured, in-progress and needs-attention
  sessions; it still adds no token or cost values.
- **No stale banner.** Suspended sessions never heartbeat, so the page was
  always "stale". The freshness notice is now a status-bar line; each
  session's detail still shows its own freshness.
- **No CA re-pin.** CNPG renews its CA every 90 days with the same key and
  subject, and Go refuses an expired root. The portal now adds each pinned
  PostgreSQL CA to its root pool with the validity window opened, so a
  renewed server certificate keeps verifying. Chain, hostname, key usage and
  the server certificate's dates are still checked. This removes the
  2026-11-05 re-pin deadline for `postgres-ca.crt`.

Next build step, not started: the operator's portal view lists only
`repo: dev-env` sessions and carries no task description or outcome, so a
session row cannot say what it is working on. (Every repository and the task
outcome landed the next day; see below. A task description is still not shown.)

### Projects, filters, task results and usage monitoring, 2026-10-27

Tom on the live portal: Tasks and Usage appeared twice (top bar and rail),
clicking the dev-env project or Needs attention did nothing, and "I thought we
had basic usage metrics already wired up". At 03:40Z, relayed by the
coordinator: build out usage monitoring and show which sessions were
interactive and which were headless or coordinator-driven, also in the usage
stats. No new credential, adapter or policy.

- **Every repository.** The operator's portal view lists every session in
  `dev-agents` with its own `repo`; the portal accepts any repository name
  (`^[A-Za-z0-9_.-]{1,100}$`, not `.` or `..`). Projects in the rail are the
  repositories, with counts. The Sessions read follows up to 10 pages (500
  sessions). A page lists at most the 150 most urgent and recent; group counts
  read "listed of total", and the rail, stats and breakdowns cover everything
  read. The portal also renders every product page within 7/8 of the 1 MiB
  response bound, keeping fewer rows (least urgent first) when the content is
  unusually large; the breakdowns keep 19 groups and merge the rest into one
  Other row.
- **Filters.** The rail and, on a phone, a chip row filter by query: `?show=attention`
  on Sessions or Tasks, `?project=<repo>` on Sessions. Exactly one canonical key
  is accepted; anything else is refused. Rail counts cover the whole read; the
  list and the Usage view cover the filtered sessions. The rail no longer
  repeats Tasks or Usage, which stay in the top bar.
- **Task results.** The activity view adds `provider` (claude, codex,
  opencode), `launchedBy` (`spec.parent`) and, for a task, `taskResult`
  (Succeeded, Failed, TimedOut, Unknown). A Failed phase only says the pod
  ended: on 2026-10-27 all 23 Failed-phase sessions had succeeded tasks while 10
  failed tasks sat unflagged. The badge and group now follow the task result.
  A failure is Needs attention for 24 hours, then Stopped; the zero count is
  not shown. A task's failure is dated by its last activity. A pod that ended
  (Failed phase, or agentd failed or interrupted) is dated by the later of its
  last activity and its last heartbeat: suspended sessions keep heartbeating, so
  a heartbeat never dates a task's end. A failure with neither time is not
  recent. The operator omits a `spec.parent` outside
  `apiv1.PortalLauncherPattern`, so one odd value cannot fail the read.
- **How sessions run.** Interactive is `remote` or `local` (a person drives
  it); Headless is `task` (often launched by a coordinator, shown as "launched
  by"). Each session and the Usage view show it.
- **Usage monitoring.** Usage adds an Account allowance block (Claude and Codex
  "Not connected": no allowance collector is deployed, and plan limits are never
  inferred from tokens) and three breakdowns: by how sessions ran, by provider
  and requested model, and by project. Each sums reported values only. Cache
  reads stay a separate column and are never added to input. A cost that is not
  a reported USD estimate counts as unavailable, and a group with nothing
  measured says "Not measured", not zero. This reverses the earlier "no totals"
  rule under Tom's direction; sums are per group and labelled.
- Compatibility: an operator before this change sends none of the new keys and
  only `dev-env`; the portal accepts that. A portal before this change refuses
  the new keys, so the portal image rolls out before the operator image.

### Message box, first increment, 2026-10-27

Historical frontend-only increment. For the current combined source, see [Current combined source contract](#current-combined-source-contract-2026-10-27).

Tom asked (2026-10-27, relayed by the coordinator) for the message box / agent
IDE modelled on open-source agent windows. This increment adds it to the
existing Agents detail, server-rendered like the rest of the portal: no script,
no form, and the same Content-Security-Policy (`form-action 'none'`).

- **What it shows.** Each session's detail ends with a Messages panel: a
  history line, a composer (draft box, provider and requested model, a route
  badge, Send) and two short lines under it: the send gate and the route.
- **Send is unavailable, and says why.** The portal's operator identity
  `dev-agents/dev-env-v2-portal` may only `GET /v1/sessions`
  (`internal/apiserver/server.go`). `POST /v1/sessions/{name}/messages` sends
  as its operator caller, and no caller class stands for a portal user
  (issuer plus subject); portal login confers no operator identity (above). The
  durable inbox serves session and coordinator callers with an accepted project
  snapshot only. So Send is a disabled button outside any form: nothing is
  sent, queued or shown as sent.
- **History is unavailable.** The portal reads no inbox and no session
  messages, and the session summary carries no message fields.
- **Route.** From summary fields only, it states what agentd's delivery
  (D-16, D-65) would do with a message from the existing `agent-run msg` route:
  a headless task reads none; an agent that is not running, or a session not in
  phase Running, takes none; a starting agent takes none yet; a running
  interactive (`remote` or `local`) session that is busy, waiting or idle may
  take one, and the panel shows the command. agentd decides delivery when a
  message is sent, so even that state promises nothing. Anything else is
  Unknown.
- **Drafts.** Only a session the route may reach gets an enabled draft box.
  Each session's box is its own element, so a draft survives switching sessions
  on the page and is lost on reload. A draft is never sent.

**Backend prerequisite for a live send, not built here.** A narrow typed
portal command: a sender attributed to the portal user, per-project and
per-session entitlement checked at send time, an idempotency key bound to the
request, Unknown (never success) on timeout, an audit record, and the CSRF and
Origin checks above; plus a read of the selected session's message history.
This is new authority: it needs its own design and Tom's approval, and belongs
to the backend lane, not this frontend increment.

### Composer seam and receipts, 2026-10-27

Historical frontend-only increment. For the current combined source, see [Current combined source contract](#current-combined-source-contract-2026-10-27).

Tom's direction (11:45 UTC, relayed by the coordinator) put a working composer
first. The backend lane proposed a v1 send contract: a same-origin
`POST /portal/projects/dev-env/environments/internal/session-messages` with
form fields `csrf`, `requestID`, `session`, `sessionUID` and `text`, and a
receipt at `GET …/session-messages/{requestID}` whose `transportStatus` is
Delivered, Refused, Pending or Unknown with a static reason. The route,
receipt storage, operator call and production wiring belong to the backend
lane. This change is the frontend half; it enables no live authority.

- **Seam.** `AssemblyConfig.Send` takes a `SendForms`
  (`web/portal/session/product_send.go`) whose `NewRequestID` returns one fresh
  ID per rendered draft box, in the route's format. Nil keeps every Send
  disabled, as before. The CSRF value is the caller's existing portal session
  value, read again when the page is built.
- **Forms.** With a send route, each session the route may reach (running
  interactive, agent busy, waiting or idle) gets a real `<form method="post">`
  to the fixed action with the hidden fields above, a required `text` box and
  an enabled Send. Others keep the disabled gate. An ID failure or a repeated
  ID leaves the whole page without forms. The renderer drops any form whose
  action, CSRF shape, request ID shape or target is wrong, and says sending is
  unavailable.
- **Policy.** A Sessions page that holds a form gets `form-action 'self'`, in
  the header and the page's meta policy; every other page keeps
  `form-action 'none'`. No script is added. Its access labels read "Read and
  message" instead of "Read-only".
- **Receipts.** `portal.RenderReceipt` renders one receipt with the product
  stylesheet and policy (no form). Delivered reads as agentd's transport
  acceptance for the agent's terminal, never a read, reply or finished task.
  Pending says not to send again; Unknown says to check the session first; any
  status outside the four renders Unknown. "Check again" links to the receipt
  itself only for a well-formed request ID.
- **Preview.** `--product` also writes `sessions-send.html` and one receipt
  per status, all labelled fixture data. Their forms post nowhere real.

### Selected-session history, 2026-10-27

Historical frontend-only increment. For the current combined source, see [Current combined source contract](#current-combined-source-contract-2026-10-27).

Tom (12:43 UTC, relayed): "How would someone know what to send these agents if
they can't see their message history?" A composer with history unavailable is
not the finished IDE. This change adds the frontend half of a selected-session
conversation view; no supported route serves the history yet.

- **Where history is today.** The portal identity may only list summaries.
  `/v1/sessions/{name}/log` tails agentd's log, which for an interactive
  session holds only lifecycle lines. `/v1/history` serves archived private
  Sessions after HOME expiry, as raw provider files. The inbox holds
  coordination messages. Portal receipts (backend PR #572) keep a digest of the
  text, never the text. A live session's conversation exists only as its
  provider transcript in its own pod.
- **Proposed backend contract** (backend lane; Story 178 journal 1894).
  `GET /v1/portal/sessions/{sessionUID}/conversation?limit=N&before=CURSOR`,
  default-off, portal adapter caller only, with the existing userinfo and
  group checks. It is fenced to the Session UID and its current pod UID, and
  served in-pod by agentd from the conversation agentd recorded for that
  Session: no other file, no path from the request. It returns turns (`seq`,
  `at`, role `user`, `assistant` or `delivered-message`, `from`, capped
  plaintext, `truncated`) and coverage (`complete`, `partial`, `truncated` or
  `unavailable`, a reason code, the conversation ID and sequence bounds). Tool
  use is names and counts only, with no tool output, no thinking, and secrets
  redacted.
- **Selection.** `SessionsPath?session=<uid>` selects one session; it does not
  narrow the list. The server opens the selection: its row is the default
  detail and the list's highlight on a wide screen, and a phone opens that
  detail instead of the list until the reader follows a link in the page. With
  a source connected, each session's Messages panel links to its own selected
  view (`?session=<uid>`, no `#sN`: row IDs are positions, so a fragment could
  open another session once the list moves), and only the selected session's
  conversation is read, with a 5-second bound. Without a source, the selected
  session states the gap and no links render. Past the 150-row listing cap the
  selected session takes the last listed row's place, so it is listed and read;
  totals, breakdowns and an incomplete-read warning still cover the whole read.
- **Seam.** `SessionSource.UseConversations(ConversationSource)`;
  `ReadConversation(ctx, name, uid)` returns `Conversation` (state, reason
  code, turns). A read error, an `unavailable` state or an unknown state shows
  no turns. Roles outside the three render as events.
- **Rendering.** Turns are escaped plaintext with line breaks kept, labelled
  User, Agent or "Message from …", with time and a "cut to fit" mark. At most
  60 turns (the newest), 4 KiB per turn and 96 KiB of turn text in total are
  rendered; anything cut marks the conversation Truncated. When the page is
  trimmed to fit the response bound, the selected session is never dropped,
  and the response header policy is taken from the page actually rendered.
- **Receipts.** The backend's reason is a fixed code (`delivered`, `refused`,
  `unknown`). `ProductReceipt.ReasonCode` carries it, and the page words it;
  an unknown code reads "Reason not recorded". "Back to the session" returns
  to the selected view (Native review P2 corrections, 2026-10-27).
- **DTO agreement with the backend lane.** Request IDs are 43 base64url
  characters, within the accepted 16 to 128. One adapter
  (`web/portal/session/message_contract.go`, the backend's file) maps
  `TransportDelivered` to Delivered and passes Refused, Pending and Unknown
  through; anything else is Unknown. `SessionName` maps to `Session`. Pending
  and Unknown never authorize a blind resend.

### Current combined source contract, 2026-10-27

The combined source now contains the typed send and receipt backend, the
selected-session conversation projection, BFF adapters and default-off
production configuration. Earlier statements such as “not built here” and
“proposed backend contract” describe their dated frontend increments; they
do not describe the current combined source. Source implementation is not
installation or live acceptance.

Send and private history remain independently disabled unless explicitly
configured. The existing private `config/session-actions.json` binds each
action to an exact issuer, subject and Session name/UID target set. Separate
operator `--portal-send-target` and `--portal-history-target` flags bind
their corresponding targets; `--enable-portal-reader` alone grants neither
action. Current portal authorization and the independent action scope both
remain required. The publication repair must leave an unscoped
`NewCommandForms` result inert: no command reservation, dispatch or receipt
access. This records the accepted source invariant, not a new permission.

Send uses the fixed same-origin form and receipt routes described above.
Immutable reservation and result records retain the existing database
privileges. Delivered means transport acceptance, not an agent read, reply
or completed task; Pending and Unknown do not permit blind resubmission.

History uses the typed projection bound to the registered conversation,
immutable Session owner and current Pod. Unsupported or unbound targets
remain Unavailable; there is no raw-log, archive, inbox or arbitrary-file
fallback. The cursor binds the whole snapshot and its owner: transcript
appends invalidate it. The current BFF requests only the newest page and
does not send a before cursor. Stable pagination while a conversation grows
is not implemented, and this clarification does not change that contract.

See the [default-off production configuration](../../../../docs/portal-session-runtime.md#default-off-portal-action-source-configuration)
for the implemented configuration and compatibility requirements.
Publication, compatible installation, exact private principal/target
configuration, live action authorization and authenticated acceptance are
separate steps. This documentation change enables none of them.

History binds only at a first launch on agentd 2.47 or later. The operator's
default-off `--pilot-agent-image` (a digest-pinned 2.x tag) and
`--pilot-agent-session` (`<creating session name>/<idempotency key>`) flags run
the sessions matching that parent and key on a newer agent image, with no
templates edit and no restart of any other session. The match is not limited
to one session: using a fresh key for one session and removing the flags
afterwards is an operating rule, not an enforced limit. See [pilot agent image
for one session](../../../../docs/portal-session-runtime.md#pilot-agent-image-for-one-session-default-off).

### Open in Claude (Remote Control link), 2026-10-27

Tom (19:19:40 UTC, relayed): "I should be able to do agent-run type things from the front end." The first step needs no new action scope.

- **The operator** adds each session's Remote Control entry to the portal reader's summary. It sends the state word, plus the link only when the link is exactly `https://claude.ai/code/session_<id>`. The same value is already in AgentSession status, which cluster readers can see.
- **The portal** re-checks both values, which reach only an admitted portal user. A registered agent's header gets an **Open in Claude** link. Its context names the honest state, and says when a headless task or a local terminal has no Remote Control.
- **Assumption, not verified here:** opening the link needs the owner's claude.ai sign-in. It is not a bearer link.
- **The renderer** drops any header link that is not `https://`.
- **Deploy order:** portal first, then operator, because the portal refuses unknown activity keys.
- **Out of scope:** launch, suspend, resume, reap and log from the portal. They need a delegated portal caller on the operator's coordinator routes, which is a new action scope that needs its own confirmation.

### Default-off portal launch route, 2026-10-27

The operator now has a default-off `POST /v1/portal/sessions` for the portal
reader, registered only when `--portal-launch-repo` is set. It maps one request to
a fixed Claude `CreateSessionRequest` (task, local or remote; `S` or `M`; a task
prompt only; a full model id) and runs it through the existing create handler.
The parent is `portal/<actorRef>`, and the request ID becomes a per-parent
idempotency key. Two more flags bound it: `--portal-launch-profile` is required
and names the one profile every launch runs on (the default `full` is not used),
and `--portal-launch-max-unfinished` (1 to 8, default 2) caps unfinished portal
launches, answering 429 at the cap. See [default-off portal launch
route](../../../../docs/portal-session-runtime.md#default-off-portal-launch-route).
This is the operator side only. The portal form and receipt, the private `launch`
scope, the profile choice and every live flag are separate steps that need Tom's
confirmation.

### Default-off portal launch form, 2026-10-27

The portal's half of the launch route is in source. `session-actions.json` may
carry a `launch` entry (`issuer`, `subject`, `repos`), parsed like `send` and
`history`. With it, the Sessions page shows a New agent form to that one
principal, and `POST .../session-launches` runs the same session, Origin, CSRF,
authorization and principal checks as Send before it asks the operator's launch
route to start a constrained Claude session. Success redirects to the selected
agent, which is the receipt. Limit, refusal, invalid and unknown outcomes get
fixed-wording result pages; Unknown never retries. See [default-off portal
launch form](../../../../docs/portal-session-runtime.md#default-off-portal-launch-form-bff).
The form's markup and copy are a first pass for the interface review. Nothing is
activated: the operator flags, this private scope and Tom's confirmation are
still required.

### Interactive conversation usage, Unit A, 2026-10-27

Interactive (local and remote) sessions have no usage today, because the
`InitialManagedInvocation` observation covers a headless task's first invocation
only. Unit A adds an inert measurement in agentd, with no CRD, heartbeat,
operator or portal wiring. Nothing publishes it.

- **Type.** `protocol.ConversationUsage` is separate from `UsageObservation`: coverage
  `RegisteredConversation`, counter semantics `ConversationCumulative`, a result of
  Final, Partial or Unknown with a reason code, per-served-model rows whose four
  counters are Reported or Unknown, the turns counted and the records skipped. It has
  no cost, no requested model and no text. Unknown is never zero. Final means every
  record was read and counted as of the observation time; the conversation may grow.
- **Fence.** `MeasureConversationUsage` reads through the same registration and
  current-pod fence as the portal's history (now one helper, `readRegisteredPortalTranscript`)
  and the same fixed, no-link file lookup and 1 MiB read bound. A launch from before
  registration, another pod or Session, a moved worktree or a missing file is Unknown with a
  reason, with no fallback to another file. A record over the read bound is Unknown
  (`source_limit`); Unit B should decide whether usage needs a larger bound than history.
- **Interactive only.** A task session is refused by mode before any read. Its figure stays
  the initial invocation's and is never added to this one.
- **Claude.** Only compact `"type":"assistant"` lines are decoded, into ids, model and the
  four usage counters. Claude Code writes one record per content block of a streamed
  message, each repeating the message id and usage, and an early record can hold a smaller
  `output_tokens`. A message counts once, each counter at its maximum across its records,
  then messages are summed per served model. Cache read and creation stay their own
  counters. API-error and synthetic records are ignored. Anything unreadable, oversize,
  from another Claude Code version or conversation, or without usable counters is skipped
  and makes the result Partial.
- **Codex.** The latest valid `token_count` snapshot's `total_token_usage` is the figure;
  repeated snapshots are never summed and `last_token_usage` is never read. A counter that
  goes backwards is a reset: nothing is added across it, the latest run's totals are shown
  and the result is Partial (`counter_reset`). One served model (from `turn_context`) takes
  the thread's totals; several have no honest split and are listed with Unknown counters.
  The repository holds no sample of a real Codex rollout, so this format is read as the
  pinned CLI is understood to write it and is tested on synthetic fixtures only; a real
  rollout must be checked before anything is published.

Unit B adds the additive `status.conversationUsage` field (the CRD first, through
dev-env-crds, because `status.usageObservation` constrains its coverage and semantics to
the initial invocation's), the agentd heartbeat field, the operator write, the portal
reader's activity field and the Usage panel.

### Interactive conversation usage, Unit B milestone 1 (backend), 2026-10-28

Unit A's measurement now runs and is stored. The portal does not show it yet.

- **Compatibility, checked in code.**
  - *agentd and the operator.* The operator decodes the heartbeat body without
    refusing unknown fields (`decodeJSON(..., strict=false)` in `heartbeat.go`), so a
    newer agentd's `conversationUsage` is ignored by an older operator, and an older
    agentd's heartbeat has no field, so a newer operator leaves the stored status as it
    is. Neither direction breaks. A type error inside a known field would reject a
    whole heartbeat, so `ConversationUsage` decodes a bad object to a value that is not
    Valid and the operator ignores it, as it does for `usageObservation`.
  - *Operator and portal.* The portal's activity decoder is strict (`exactKeys` in
    `product_activity.go`), so a new activity key needs the portal first. The operator's
    reader sends nothing new in this milestone, so the order matters only for
    milestone 2.
  - *CRD.* `status.conversationUsage` is additive and optional. A stale CRD prunes an
    unknown status field silently (haynes-ops #3502), so the CRD goes first.
- **Rollout order.** CRD (dev-env-crds), then the operator, then the portal (milestone
  2). Measurement needs agentd from this release, so only a new session on a newer agent
  image reports it (the `--pilot-agent-image` mechanism); old pods never gain it, and
  report nothing, which reads as absent. No template edit and no session restart.
- **Status field.** `status.conversationUsage` mirrors `protocol.ConversationUsage`:
  fixed coverage and semantics words, Final, Partial or Unknown, a reason, up to 8 served
  models with Reported or Unknown counters, turns and skipped. `status.usageObservation`
  and its enums are unchanged, and the two figures are never added.
- **agentd.** An interactive (local or remote) session measures its registered
  conversation in the background every five minutes, and the newest figure rides each
  heartbeat; Unknown is never sent. The scan streams the provider's record line by line
  up to 64 MiB, as of its size when opened, keeping only numbers, so a long transcript no
  longer reads as Unknown. A half-written last line waits for the next scan. The history
  path keeps its 1 MiB bound.
- **Codex guard.** When a snapshot carries a per-turn figure (`last_token_usage`), each
  rise in the cumulative total must equal it, or the result is Partial
  (`delta_mismatch`). After a counter reset the figure and Turns are the latest run's.
  Codex input already includes the cached tokens, so input and cache read overlap and a
  consumer must not add them.
- **Operator.** The heartbeat handler validates the figure, then writes it for the
  reporting session only: interactive, the session's own provider, not Unknown, and not
  older than the one stored. An absent field leaves the stored value; the stored figure
  keeps its observation time, so a reader can see its age.
- **A stored figure can go stale, on purpose.** If a later measurement is Unknown (the
  record moved, the pod was replaced by an older agent), agentd stops sending and the
  operator keeps the last figure rather than clear it. Its `observedAt` is the age, and
  the milestone 2 portal view marks an old figure as stale. Clearing would turn a
  once-measured session back into "no figure" with no sign it was ever measured.
- **One oversize Claude record makes the result Partial.** A single assistant record
  over 64 KiB cannot be read and is counted as skipped, so the figure is a marked lower
  bound. That is the conservative choice; it can be relaxed once real records are known.
- **What is still an assumption.** That Claude Code repeats usage per content block and
  its output counter only grows (seen on 2.1.296), and the Codex `token_count` shape, are
  encoded in synthetic fixtures. No published schema for either has been checked in.

### Interactive conversation usage, Unit B milestone 2 (reader and view model), 2026-10-28

The portal reader can now carry each interactive session's measured conversation
usage, and the portal maps it to a view model and renders it. The template, layout
and copy are below, under "Interface".

- **Rollout choice: a default-off operator flag.** The portal's activity decoder is
  strict (`exactKeys` in `product_activity.go`; `TestActivityDecoderIsStrictAboutKeysItDoesNotKnow`
  pins it), so a portal from before this change reads a view with an unknown
  `conversationUsage` key as unavailable, for the whole Sessions page. The operator
  therefore sends the key only with `--portal-conversation-usage` (requires
  `--enable-portal-reader`), and the flag is turned on after the new portal is rolled.
  The new portal treats the key as optional, so it reads a view from an operator that
  does not send it, and one that does. The order is: CRD (already), operator
  (flag off), portal, then the flag.
- **Operator allowlist.** The key carries `resultState` (Final or Partial), `reasonCode`
  (Partial only), per-served-model counters (Reported or Unknown), `turns`, `skipped`
  and `observedAt`, and nothing else: no coverage or semantics words, no provider (the
  activity already has it), no path, id, record text, requested model or cost. It is
  sent only for an interactive session whose stored figure is valid for the session's own
  provider and is not Unknown; a task session, an invalid figure and an Unknown one send
  no key. A time ahead of the operator's clock is sent as now.
- **Portal decoder.** Exactly those keys, re-checked with the same `Valid()` the agentd
  measurement and the operator's heartbeat use, for an interactive session of Claude or
  Codex. Anything else makes the read unavailable, like the other optional activity keys.
  A time a little ahead of the portal's clock is clamped to now.
- **View model.** Measured conversation usage is its own panel on the session ("Conversation
  usage": result with the reason, when it was measured, freshness, provider calls counted,
  records skipped, and each served model's counters on a line) and its own table in the
  Usage view, grouped by provider and served model. Providers never share a row, so Codex's
  input (which includes cached tokens) is never added to Claude's, and nothing is added to
  a task's initial invocation. An Unknown or absent counter is never shown as zero; a
  Reported zero is.
- **Stale.** A figure older than 15 minutes (three of agentd's five-minute measurements)
  is marked stale, by the portal's clock at read time. The figure is not hidden.
- **Still not shown:** a cost, a charge, an allowance or a quota. The Usage view's task-only
  wording (scope, coverage and the interactive "not measured" notes) changes only when a
  figure is present.
- **Interface, 2026-10-28.** The panel and table use the existing components (badge,
  key-value list, table, note) and tokens; there is still no script and no inline style.
  The generic panel gained two optional parts: state lines (a badge and one sentence)
  and a table, in the order states, tiles, table, fields, note.
  - *Panel.* Two state lines: Final ("Every complete usage record was counted, as of
    the measured time.") or Partial (amber; the reason in one plain sentence, then "The
    counts shown are a lower bound." when any counter is Reported; a model split, whose
    counters are all Unknown, has no bound to state), and Current ("Measured within the last 15 minutes.") or Stale
    (amber; "Older than 15 minutes, so it may be behind the conversation."). A stale
    figure is still shown. Then one table row per served model (Input, Output, Cache
    read, Cache create; Unknown dimmed, a Reported zero as 0), when it was measured,
    provider calls counted, records skipped, and a note: provider-reported token counts
    for the registered conversation, cumulative to the measured time, not a cost or a
    charge. For Codex the note says its input already includes cache reads. No price is
    shown. The list item reads "Conversation: In … · Out …" (or "Conversation: N
    models"), then "partial" and "stale" when they apply.
  - *Usage view.* With a figure present, the conversation table comes first, before the
    initial-invocation tables, whose titles then name their scope ("Initial invocations
    by …", and "Initial invocations by session"). Its rows add Partial and Stale counts,
    and a row's line says "Lower bound" or "May be behind". The scope chip reads
    "Initial invocations and conversations", the Interactive stat counts the sessions
    with conversation usage, and the coverage panel gains a Conversations line and says
    that conversations carry no price. The account allowance stays "Not connected".
    Without a figure, the page reads as before.
  - *Fixture.* The committed Sessions fixture has a Final and current Claude session
    with two served models, a Partial Claude session, and a stale Codex session.
  - *Reasons.* Every reason code `Valid()` accepts on a Partial has its own sentence,
    and a test reads the protocol's Reason constants so a new code cannot go unworded.
    agentd sends only five as Partial (skipped records, missing counters, a model
    split, a counter reset, a delta mismatch); the other nine it sends as Unknown,
    which the operator never forwards, and each is worded for a figure missing a
    part. A code with no wording still shows as itself.
  - *Final is not every line.* Final counts every complete usage record. Lines that
    are not usage records, API-error and synthetic records, a half-written last line
    and lines appended after the scan opened the file are not counted.
  - *Assumption, not checked against a live transcript:* the registered conversation's
    transcript may not include a Claude subagent's transcript when Claude Code keeps it
    in a separate file, so a Final figure may leave out subagent usage.
  - *Cache reads.* Claude reports cache reads apart from input; Codex input already
    includes them. The coverage panel and the task Usage panel's note now say so per
    provider, replacing "a subset that can overlap input".
  - *No version number.* The copy says "an older agent". Measurement reaches a session
    only on an agent image from the first release after 2.50.0.
