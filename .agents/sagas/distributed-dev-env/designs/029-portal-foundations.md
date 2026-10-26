# DESIGN-029: Portal foundations: source inventory, Attention journey and security evidence

**Status: source-only first milestone for [Story 178](https://work.haynesnetwork.com/work_packages/178).**
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
