# Portal #178: bounded implementation pass, 2026-10-23

Canonical work: [Story 178](https://work.haynesnetwork.com/work_packages/178),
under Epic 120 / Saga 85, related to 121. This is the dedicated implementation
owner's checkpoint, not a second backlog or acceptance of the epic.

## Execution receipt

| Field | Recorded value |
| --- | --- |
| Accountable implementation session | `dev-env-1023-141710` |
| Session UID | `f3624b62-8250-42ef-9329-a66bdaa04247` (own operator read and canonical dispatch receipt) |
| Parent / continuation custody | `dev-env-1019-205125`; parent coordinates pickup and required independent Claude review |
| Worktree | `/home/dev/work/dev-env-1023-141710` |
| Branch | `agent/dev-env-1023-141710` |
| Freshly fetched dev-env base / initial head | `380a759d947faf1131653867cdecf4b7ade878f5` |
| Configured model / effort | `gpt-6.1-sol` / `xhigh`, per dispatch metadata |
| Provider-reported served model / remaining capacity | Unknown / Unknown |
| Actual execution start | `2026-10-23T14:18:10Z` (first execution command) |
| Checkpoint deadline | `2026-10-23T14:33:10Z` (+15 minutes) |
| First milestone deadline | `2026-10-23T14:38:10Z` (+20 minutes) |
| First-pass execution ceiling | `2026-10-23T15:48:10Z` (+90 minutes) |
| Local tooling | Node and Python available; Go absent from `PATH`; repo requests `toolchain go1.27.2` |
| Test bound | At most 2 CPUs (`DEV_ENV_CPU_LIMIT=4`); finite synthetic checks under `nice -n 19`, serialized; Go, if available, `GOMAXPROCS=2`, `-p 1` |

OpenProject claim succeeded and was read back with this worktree/branch as
Execution owner. No new worker, advisor, reviewer or alternate implementation
owner was launched. The OpenProject record carries the scope, acceptance,
budget, exclusions and custody. Usage allowance beyond elapsed time is Unknown.

## Boundaries and early findings

- Fetched `origin/main` matches the initial head above. Open-PR metadata shows
  no portal implementation overlap. Own only this new progress record, a new
  portal-specific design/evidence document and isolated synthetic portal fixtures.
  Do not edit shared roadmap, workflow or Mac-navigation files.
- The parent's TaskClaim cluster-list Forbidden remains a verification limit;
  it is not an empty ownership result. No retry, alternate identity or access
  change was attempted.
- Parent ACK delivery through `agent-run msg dev-env-1019-205125` returned
  `409 conflict`: `agentd: deliver: not addressable: the agent has not started
  in this pod`. That exact messaging action stopped without retry or fallback.
  This canonical checkpoint remains the authorized durable handoff.
- Read `AGENTS.md`, `CLAUDE.md`, the current `.agents/HANDOFF.md` entry,
  plan 03, plan 13, DESIGN-001 section 3.8 / Q-16, launch-profile preflight,
  and PR 292's single requirement file. PR 292 is an open draft at
  `6e7c364ce4a23842895697a8bfe3167f264a8efe`, not an accepted design.
  No `.agents/skills` catalog is present.
- Existing API authentication is Kubernetes TokenReview for audience
  `dev-env-operator`, with caller classes. That is not browser OIDC.
  Current broker startup has no console listener. Planned console pages do
  not establish a deployed browser service.
- DESIGN-001 Q-16 removes the approvals page from the console. No portal grant
  approval, shell, remote-host API or credential infrastructure is authorized.
  `full` remains this ordinary source worker's profile, not `workbench`.
- Public haynes-ops source snapshot resolved to
  `2aeda3157a0300f2f31705008df6c9a8ee48a03c`; targeted route, identity and
  template inspection is in progress. No new portal hostname has been proven.
  The parent's existing V1 host observation is not an assigned V2 portal URL.

## Initial Astra receipt (superseded by the full input below)

Consumed canonical #178 journals 851, 853 and 854 as the parent's documented
relay of the already assigned Astra advisor. They specify the Attention inbox
groups, owner/next action/freshness, independent task outcome and worker
liveness, existing workflow truth, server-side command authorization and
timeout-as-unknown, official remote-session links, no embedded terminal,
and logout/MFA evidence requirements. At this initial read, full design was pending.
No consequential architecture, security configuration, stack or deployment
choice has been made.

Initial next action: finish pinned source inventory and exact internal route evidence or
scoped absence; publish the metadata-only journey and threat/test map by the
first milestone. Independent finite fixtures may follow. Leave architecture
choices pending full documented Astra input. No lifecycle, live identity,
network, credential, deployment, public ingress or V1 operations in this pass.

## Delivery state

### First milestone / +15-minute checkpoint, 14:33 UTC

The [source inventory and threat map](../sagas/distributed-dev-env/designs/029-portal-foundations.md)
is written. GitOps negative route evidence is scoped and pinned: the complete
recursive tree has three routing-candidate HelmReleases under
`dev-env-system/**` (broker, keeper, operator), with no portal route/host in
those candidates and no standalone Ingress/IngressRoute/HTTPRoute file there.
The verified `dev-env.haynesops.com` is V1 code-server, not the V2 portal.
No exact new portal hostname is proven; route/identity configuration remains
a separate prerequisite. Blueprint 90 is confidential OIDC; 95 is public PKCE;
neither is portal configuration. Only workbench supplies the hardware key.

[Synthetic Attention fixtures](../../scripts/trials/portal_attention/README.md)
now provide all four parent/Astra groups, owner/next actor/action, independent
worker/task evidence, exact reviewed head, and missing/stale verification
examples. The Python standard-library generator produces an inert HTML file;
this is a fixture tool, not a selected production UI/BFF stack. No live API,
credential, network, workflow reducer or command path is present.

At `2026-10-23T14:32:47Z`, 17 finite offline unit checks passed under
`nice -n 19`, one process, in 0.016s. They cover presentation threat-map
T05–T07/T09/T12, not OIDC/authz/CSRF/deployed security. Preview generation and
`git diff --check` passed. Local Go checks remain unavailable (no compiler on
`PATH`); no toolchain install or alternate execution route was attempted.
Full documented Astra release/security gates remain pending before any
consequential choice. Next: self-review the source diff and publish a small
ready PR with exact base/head and CI status for parent review coordination.

### Full Astra input received after the initial checkpoint read

Read canonical journals 859/860 in full and acknowledged in journal 863:
`PORTAL178-ASTRA-FINAL-20261023-01` and
`PORTAL178-SOURCE-MAP-ADDENDUM-20261023-02`. This replaces “full design pending”
for source direction; configuration/activation holds remain. The readable
design now carries the Go same-origin BFF direction, confidential OIDC/PKCE,
opaque cookie and separate authorization contract, proposed session defaults,
proxy/SSRF/audit requirements and full Gate A / separate Gate B. No production
auth/adapter or stack wiring was added. Public source independently confirms
the addendum's desired Authentik `2026.8.5` and Traefik `42.1.3` chart pins;
effective deployed proxy trust and IdP behavior remain Unknown.

### Validation after the design receipt

The expanded presentation suite passes **18 tests**, including verification
that predates a newer source update. All new local document links resolve.
The preview renders as synthetic HTML in Playwright without contacting a live
API. Automated geometry checks at 320px and 390px reported no horizontal
overflow, all four cards/groups, minimum link height 44px, and visible keyboard
focus. The 320px page had no script/form/frame/image elements and zero external
resource entries. This is synthetic browser evidence, not a real-phone or
deployed-authentication acceptance run.

A dedicated read-only GitHub Actions workflow repeats the finite fixture
suite and produces the inert preview plus exact source-head/hash artifact.
The existing required CI aggregate is unchanged. No runtime/image/deployment
input was modified. Local Go validation remains unavailable.

### Ready source PR and remaining custody

[PR 525](https://github.com/thaynes43/dev-env/pull/525) is open and ready on
the assigned branch. Initial implementation head:
`49eea27d3bd34bd5ee07a1eb7f9da692891fe39d`; base:
`380a759d947faf1131653867cdecf4b7ade878f5`. The fixture workflow passed on
that initial head. A final self-review found that control-only owner text could
sanitize to blank; the next source revision keeps it visibly Unknown and adds
a nineteenth finite regression check. Final PR head and exact-head CI/review
receipts are published in canonical #178 and the PR to avoid a self-referential
commit hash in this file.

The parent coordinates the required exact-final-head native Claude review;
this pass launches none. The dedicated implementation owner retains findings,
fixes, CI and normal authorized source merge/release after the gates pass.
No additional Tom merge approval is needed. Internal operation still requires
Gate A's actual authorized configuration and runtime evidence; Gate B does not
permit public exposure. Missing route/backend identity are implementation and
delivery prerequisites, not reasons to stop independent source preparation.

### Native review corrections, 15:05 UTC

Parent journal 899 relayed the required native exact-head review of `d15d780`
and authorized bounded corrections in the same PR. Display normalization now
removes Unicode Cc/Cf controls, including invisible format-only owners; empty
display values remain Unknown. Tests assert rendered head/badge properties
rather than claiming release detection from fixture text. DESIGN-029 explicitly
separates OIDC principals from operator caller classes and preserves the keeper
as sole provider-login refresher. Links have distinct accessible row names,
the observation time has `datetime`, deep JSON/recursion fails generically,
and fixture CI rejects untracked source-tree changes. The Python-only package
does not need a Go Layout entry; shared `CLAUDE.md` remains unchanged.

The parent supplied an existing compiler in the same pod's isolated toolchain
directory. At 15:01 UTC the owner verified Go 1.27.2 and SHA256
`548d3c32f83fa81152f543eb1526be7884cfe0526eb5e9ce29d3eca14786e7e0`
at `/home/dev/work/portal-toolchains/go1.27.2/bin/go`. Earlier compiler-absence
observations remain dated history. No system install, PATH/profile change or
new access was used. No heavy Go suite is needed for these Python corrections.
The corrected source requires a fresh exact-head CI/native-review receipt;
the earlier native result does not cover the new head.

The corrected finite suite passed **24 tests** in 0.018s, serialized under
`nice -n 19`; renderer generation, local links and diff checks passed. Preview
SHA256: `cd7165afdc83142f843297f366ed0793645d77018b6a9e641bf91973376f6904`.
The exact new target is recorded in #178/PR 525 after push. No additional
source increment begins before that PR's safe disposition.

### Consolidated native-review correction and pass-boundary handoff

Parent journal 930 supplied the superseded-433 review at 15:37 UTC. Its
decoder-test/CI blocker was already resolved on `e46cdf5`, whose required and
fixture CI passed and whose downloaded source-head/hash were verified. The
bounded decoder-or-schema refusal contract remains; no ad hoc JSON parser was
added. Local dated observations above are not claims of final-head coverage.

The final correction makes Unknown/control/worker/link assertions specific to
their row; rejects surrogate display text; treats isolated marks and known
blank fillers as Unknown; validates ASCII timestamps and escapes their output;
and encodes the complete page before opening the output, preventing malformed
Unicode from creating a partial file. Finite tests cover each behavior. Links
retain their distinct contextual names, with order-independent assertions.
The optional alternative link representation is not needed for that contract.
Checksums now use a portable relative filename. The HTML checksum supports
portable reproduction; actual reproduction requires an independent comparison.
The upload action's archive digest is the integrity reference.
DESIGN-029 also cites parent identity addendum 866. Shared CLAUDE/CI remain
unchanged; Python-only Layout reasoning still applies.

The final suite contains **26 finite tests**. Exact final head, test result,
preview hash and pending CI/native review are published in canonical #178 and
PR 525 after push. Earlier native/advisory/CI receipts cover only their named
heads. Source remains a read-only synthetic foundation, not an operational
portal. No source merge/release, internal deployment or runtime acceptance is
claimed without its actual receipt.

The next Go template/BFF increment is specified in canonical contract 914. It
has **not started** because PR 525's safe disposition is its explicit
prerequisite. At the original 15:48:10 UTC first-pass ceiling, preserve this
clean pushed source head and the concrete next step: parent obtains matching
final-head native review and CI, dispositions findings, performs the already
authorized source merge, then retains supported next-pass custody for the same
implementation owner. This checkpoint ends no project ownership, grants no
automatic new execution allowance and promises no unsupported headless resume.
No worker, alternate owner, grant, credential action, route, listener, connector,
OIDC activation or production operation was started by this pass.

PR, required independent review, merge, release, installation and usable
acceptance remain pending. At the first-pass
boundary preserve exact head/base, checks, findings and next safe step here and
in #178. The parent retains supported continuation custody; executor exit is
not project completion and authorizes no automatic headless resume.

## M2 successor source checkpoint, 2026-10-23 16:20 UTC

The prior pass above is historical. PR525 merged at 16:02:58 UTC as
`597e2cb2fb56a7ce81e055d5f0ed601de126c6ce`, from reviewed head
`3d9bb051717de39b3b80b27ad7338562c2cddc0a`. This worktree freshly fetched
that accepted main and verified the portal files match the reviewed source.
This is squash integration, not a claim that the old branch is an ancestor.
Final PR525 native review passed with two wording minors, corrected here;
that review does not cover M2.

Execution owner is now `dev-env-1023-160816`, branch
`agent/dev-env-1023-160816`, following the explicit relinquishment and verified
Suspended predecessor with source/home retained. #178 claim/readback and ACK
are canonical. Prior Ready/Running-without-Codex and known-undelivered msg409
were recorded as handoff evidence on #177; no transport debugging, other
session inspection or lifecycle action was performed by this successor.

The [read boundary](../../docs/portal-read-boundary.md) and `web/portal/` now
provide a compiling stdlib handler/template, default unavailable dependencies,
pre/post-read explicit authorization, summary-only source binding, four mobile
Attention groups and a no-listener preview reusing PR525 fixtures. Nil live
dependencies cannot become fixture data. Unknown, unavailable, stale and fresh
verified-empty reads remain distinct. Canonical #177 workflow integration is
explicitly unavailable; there is no new reducer.

The compiler was initially absent; the existing provisioning subsequently
supplied `/home/dev/work/portal-toolchains/go1.27.2`. Version Go1.27.2 and
compiler SHA256 `548d3c32f83fa81152f543eb1526be7884cfe0526eb5e9ce29d3eca14786e7e0`
were verified before use. At 16:20 UTC, focused serialized `go test -p 1
./web/portal/...` passed (portal 0.006s, preview 0.006s), under
`GOTOOLCHAIN=local GOMAXPROCS=2 nice -n 19`. Eleven top-level tests include
finite two-user/two-project/environment, guessed-ID, withdrawal, unavailable,
missing-dependency, hostile-text, timestamp and source-binding cases. Preview
tests independently compare two full renders. No external provider/tracker/
cluster or listener is required.

Playwright inspection of the inert preview at 320px and 390px found four
groups/cards, no horizontal overflow, minimum 44px links, visible keyboard
focus, zero external resources and zero script/form/frame/image elements.
An ignored meta `frame-ancestors` directive was removed from the meta policy;
the HTTP header retains it. This is offline browser evidence, not phone or
deployed-security acceptance. Generated local HTML/screenshot stay in ignored
`bin/`, outside source control.

Next action: publish this focused PR, obtain the root-coordinated independent
native review at its exact final head, resolve findings and required CI, then
perform the authorized source merge. One implementation owner remains; no
arbitrary new deadline or turn limit was set. Source/tests do not establish
release, installation, Gate A, human runtime binding, ingress or usable portal
acceptance. Outcome custody continues through those separately scoped gates.

### M2 native-review correction

Canonical #178 journal971 relays the full independent Claude review of
`75e6379748c16384202daade8b35b722642182ef`. That head's required CI passed,
but native review correctly required a listing-freshness fix. Nonempty
snapshots had ignored their own verification, so absent/stale listing evidence
could display zero group counts despite an individually fresh item. The
correction checks listing evidence for every verified snapshot, warns visibly
and labels group counts Unknown when listing evidence is not current. The
synthetic preview now explicitly witnesses its listing at the fixture's fixed
observation time while preserving each row's separate verification.

Exact row freshness assertions replace the vacuous whole-page Unknown check.
Additional finite cases cover a fresh item in missing/stale/future/predates-update
listings and the exact freshness boundary; cancellation without metadata escape;
valid 24 versus 25 rows; 512 versus 513 UTF-8 bytes; refusal/frame headers;
trusted identity unchanged by spoofed headers; rendered CSS versus CSP hash;
visible link text in its accessible name; and archived-claim refusal.
Docs clarify field selection/redaction, portable Go1.27.2 invocation and the
future Gate A requirement for dependency-panic containment/log redaction.
No production server or panic logging behavior is claimed by this source pass.

The corrected focused suite passes fifteen top-level tests, serialized under
the same verified toolchain and CPU settings. Exact corrected head, preview
checksum and fresh CI/native-review targets are published on #178 and PR526;
the old head's receipts are not coverage of this correction. M3 source and all
runtime/ingress/identity activation remain outside PR526.

## M2 accepted source and M3 continuation

PR526 merged at 2026-10-23 16:54:41 UTC as
`cb28f252b1504c69d9b2d369a7551c6cfbb12176`, from reviewed head
`7b4f6858b8e81f42a187c45691532785d61b5d5a`. Required CI39988176697 passed;
native reviewer `dev-env-1023-164635` reviewed all eleven files and the full
correction and returned PASS-with-minors. Canonical #178 journal989 preserves
the full report. The root performed the guarded source merge; this owner
verified the GitHub receipt and did not race it. Source merge establishes no
release, installation, runtime session or Gate A/public acceptance.

Canonical journals985/993 direct the same implementation owner to continue M3
without another worker, reset, guessed deadline or premature provider switch.
The assigned branch merged freshly fetched accepted main via local
`371a6cc935931c8009463c422ca0f04a826c37c6`, preserving additive WIP without
discard, force or another worktree. The [M3 source contract](../../docs/portal-session-boundary.md)
records the maintained verifier selection and finite negative-test map.

M3 adds `web/portal/session`: a coreos/go-oidc/v3 v3.21.0 verifier wrapper with
explicit nonce/authorized-party/time binding; confidential code/S256 adapters
with fixed issuer/client/callback/scopes; single-use pending-login and shared
session-store interfaces; opaque secure host-only cookies; local logout,
revocation and expiry without provider calls or polling activity extension;
Origin/CSRF protection; and buffered panic/cancellation/response containment.
There is no production store fallback or deployment/listener entry point.
Unconfigured assembly remains unavailable. Metadata authorization stays
independent and all live metadata adapters remain unavailable.

Exact token/JWKS destinations, methods, body budgets and redirects are bounded
in the client. Constructors initiate no network request. Static and remote
maintained key-set verification is exercised with generated synthetic keys and
inert transports only. No real provider, tracker, cluster or credential is used
by these source tests. Secrets, raw provider tokens, panic values and agent
content are not logged or returned. The test-only stores make no claim about
deployed multi-replica persistence or revocation.

The two optional M2 review minors are carried here: Unknown listing wording
no longer asserts definite staleness, future evidence is explicitly untrusted,
and the preview test checks exact numeric group counts with no listing warning.
Current focused evidence is thirty-one top-level tests across the portal,
preview and session packages, passing serialized with the verified Go1.27.2
toolchain, GOTOOLCHAIN=local/GOMAXPROCS=2/nice19/-p1; session tests 0.013s at
the latest source check. Targeted `go vet -p 1 ./web/portal/...` also passes.
Module tidy retains oauth2 v0.37.0, adds the pinned OIDC/go-jose modules and
reclassifies the existing client_model dependency as direct without upgrading it.

Next action is the M3 exact-head source PR, root-coordinated independent native
review, findings, required CI and authorized source merge. Exact head/test/preview
and review receipts stay in #178 and the PR. No handoff to another provider or
implementation owner has occurred. Only after this natural reviewed boundary
may the coordinator explicitly assign a single subsequent presentation owner.
Approved OIDC setup905 remains separate infrastructure work; no 1Password ask,
runtime binding, RBAC, secret grant, provider call, ingress or public exposure
was performed. Story178 remains open for its actual usable/internal acceptance.

### M3 native clarification and test isolation

The full native review in #178 journal1007 covers initial M3 head `4cfa2bb2`
and returned PASS-with-minors, with no security/correctness blocker. The reviewer
independently ran 31 top-level tests and 138 subtests, vet, formatting and module
verification. Its initial-head CI39995631874 also passed. Neither receipt is
coverage of the following correction.

The same owner clarified that the effective M3 lifetime is fixed `IdleTTL`
from creation; `AbsoluteTTL` currently controls cookie expiry, with a redundant
later server check until a reviewed activity path exists. The prior PR reply's
"independent upper fence" wording was incorrect for current behavior and is
superseded by the explicit contract. The overlapping expiry test is now named
`after-both-deadlines`, with its coverage limit stated.

Algorithm rejection now uses a trusted ES384 key, the fake exchange checks the
exact code, and logout tests reach a failing RevokeSession after a successful
lookup. Issuer tests isolate literal-IP/localhost rejection with matching
endpoint hosts; the existing guard also handles hostname case/absolute-name
and IPv6-zone forms. Finite callback tests cover malformed/missing/duplicate/
extra fields, oversized codes and missing/duplicate login cookies. Back-to-back
tabs prove a wrong-state attempt cannot consume the browser's current login.
The test-store contract says this explicitly. Preview failure wording is fixed.
No activity endpoint, production policy, callback allowlist expansion, provider,
store adapter, identity binding or ingress change is included.

Exact corrected-head tests/preview/native/CI and PR receipts are recorded in
#178/PR527. Same-owner fix custody remains; no provider handoff is performed
until the actual reviewed source boundary is accepted and explicitly released.
