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

PR, required independent review, merge, release, installation and usable
acceptance remain pending. At the first-pass
boundary preserve exact head/base, checks, findings and next safe step here and
in #178. The parent retains supported continuation custody; executor exit is
not project completion and authorizes no automatic headless resume.
