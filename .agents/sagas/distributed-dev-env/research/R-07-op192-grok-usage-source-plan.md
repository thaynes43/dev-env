# R-07 OP192 Grok usage source plan (2026-11-05)

OpenProject OP192 ("Deliver mandatory V1 Cursor and Grok shared weekly allowance
probe"), Story under Epic OP187. This is a source plan for review. It changes no
collector acquisition, calls no provider, and uses no login, token, cookie or
credential. Public documentation was read on 2026-11-05 (UTC). Repository facts
are from `main` at `5bf24fec`.

- Source-plan custody: `dev-env-1104-010332` (coordinator), worker: its native
  Opus subagent. Requirements owner: meatball. Decision owner for any private
  route or new login: Tom, under OP193.
- Earlier inputs, read as data: the 2026-11-04 coordinator research handoffs
  (`GROK-SOURCE-RESEARCH.json`, `OPENUSAGE-CODEXBAR-RESEARCH.json`) and the OP323
  owner note. Third-party projects are corroboration only.
- Page quotes below came through a summarizing fetch tool. Re-read the page
  before quoting it in an ADR.

## Bottom line

1. No documented, programmatic, individual-account source for a Grok allowance
   exists today. xAI's official usage view is a page,
   [grok.com/dashboard](https://grok.com/dashboard) Usage
   ([xAI Plans and usage](https://docs.x.ai/x-subscription/usage-limits)).
2. Cursor's own help says Grok 4.7 in Cursor "draws from the usage pool for
   Cursor models included with your plan"
   ([Grok 4.7](https://cursor.com/help/models-and-usage/grok-4-7)). On The X
   Subscription that pool is one weekly pool shared with Grok Bot and the Grok
   apps. Legacy Cursor plans have monthly pools, which the docs do not describe
   as shared ([usage limits](https://prod.cursor.com/help/models-and-usage/usage-limits)).
   We do not know which plan Tom's Cursor account is on. So **Cursor quota is not
   proven to be Grok quota.** The relationship stays typed Unknown.
3. The closest first-party machine-readable source is xAI's own open-source
   Grok Build CLI. Its ACP extension method `x.ai/billing`, served by the
   documented `grok agent stdio`, returns `creditUsagePercent`, `currentPeriod`
   and `isUnifiedBillingUser` for "Grok Build coding credits". That percent is
   The X Subscription pool only when `isUnifiedBillingUser` is true, and it is
   never assumed to be Tom's Grok 4.7-in-Cursor entitlement. The CLI keeps its
   own login. The method is not in docs.x.ai. Using it needs a CLI install and a
   new `grok login`: an OP193 decision (D1), considered only after D0.
4. Next bounded step, needing only approval of this plan (D5) and no new
   access: add the separate typed **Grok row** (Not connected) to the read API,
   `agent-run usage` and the portal (S1). It ships in order: first an operator
   that accepts the Grok row, then the producers that emit it (section 4).
5. OP192's acceptance ("same-account weekly percent/reset matches approved
   official display/source; dedupe Cursor and Grok") stays blocked until D0 to
   D3 are answered and an approved source works.

## 1. Provider rows

Four rows. Grok is a row of its own, never folded into Cursor.

| Row | Provider id | What it measures | Source today | Status today | Relationship |
| --- | --- | --- | --- | --- | --- |
| Claude | `anthropic` | Claude subscription session and weekly windows | Claude SDK usage report (`claude-sdk-usage-report-2026.11.02`); passive statusline (OP191) | In the collector config; acceptance open (OP191) | Independent |
| Codex | `openai` | Codex rate-limit windows | Codex app-server account rate limits (`codex-app-server-sanitized-2026.11.02`, OP190) | In the collector config; acceptance open | Independent |
| Cursor | `cursor` | The plan's Included percent. Grok used in Cursor counts here, in the Cursor-models pool (Cursor docs) | Cursor CLI builtin `/usage` display, renderer `2026.10.22-27dcf5f`, sanitized as `cursor-cli-usage-sanitized-2026.11.02` | In the collector config; recurring acceptance open | Shared with Grok: **Unknown** |
| Grok | `grok` (proposed) | The Grok-side allowance xAI reports for one grok.com account: Grok Build coding credits. It is The X Subscription pool only when `isUnifiedBillingUser` is true, and never assumed to be Tom's Grok 4.7-in-Cursor entitlement | None | **Absent.** No status row, no item, no portal row | Shared with Cursor: **Unknown** |

`xai` stays a different id: direct xAI API spend (management or inference
keys). It is never quota and never merged with `grok` or `cursor`. The Cursor
display adapter already emits `xai` only as `unsupported`, binding unknown, with
no measures (`internal/providerallowance/cursor_display.go` `finishXAI`,
`xaiItem`).

What the code does today, and why the Grok row is missing:

- `Collection.build` adds unconfigured statuses for `cursor`, `anthropic` and
  `openai` only (`internal/providerallowance/collection.go:559`). The agent-run
  local preflight does the same (`internal/agentrun/usage.go:79`). The portal maps
  three providers (`web/portal/session/product_allowance.go:23`). A Grok value, if
  one were served, would show nowhere; Grok absence is silent.
- The API's aggregate store rejects any document whose status set is not exactly
  those three providers (`internal/apiserver/provider_allowance_report_store.go`:
  `aggregateProviders` at :478, `indexAllowanceStatuses` at :497). `ParseAggregateTrust` accepts the same three
  (`internal/providerallowance/aggregate_trust.go` `knownProvider`).
- The collector config accepts exactly three providers and **zero**
  `EntitlementRule`s (`internal/allowancecollector/config.go:50`, `:78`, `:85`).
  Dedupe needs a caller-trusted, verified `EntitlementRule` with account
  bindings (`SharedPools`, `cursorGrokShared` at `collection.go:868`). A payload
  `entitlementRef` never creates a pool. So production cannot dedupe Cursor and
  Grok today, which is correct until proof exists.
- The Cursor display parser reads the `Included`, `Auto` and `API` rows and
  projects only `Included` (`internal/usagecollector/cursor_protocol.go:136`).
  Which row carries Grok 4.7 consumption is Unknown. Its reset label is the
  billing-cycle month/day, not an allowance renewal, and the period is
  `not_exposed` (`cursor_cli_capture.go`).

## 2. Source options

Public or first-party sources only. "Permitted now" means allowed under OP192's
constraints with no new login, credential, install, private route or paid
service.

### Grok row candidates

| # | Source | Official status | Account binding | Window and reset | Units | Protocol | Auth class | Permitted now? |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| G1 | grok.com/dashboard Usage | Official, documented as a display | One paid seat; on team plans "Seats do not share weekly pools." | Weekly. Shows "the next reset date". Rolling or fixed and timezone: Unknown | Not documented (Unknown) | Web page | Interactive grok.com/X web session | **Parity reference only.** A human reads it. No agent login, browser automation or scraping |
| G2 | Grok Build CLI ACP method `x.ai/billing` via `grok agent stdio` | `grok agent stdio` is documented. The extension method is in xAI's Apache-2.0 source only | The CLI's grok.com login. The payload carries no account identity; a link to the Cursor account is Unknown | `currentPeriod.type` (`USAGE_PERIOD_TYPE_WEEKLY` or monthly), `start`, `end` as RFC 3339 instants. Deprecated `billingPeriodStart/End` | `creditUsagePercent`, float 0.0 to 100.0 (fractional). Money fields in USD cents: `prepaidBalance`, `onDemandCap`, `onDemandUsed` | JSON-RPC over stdio to a local CLI; the CLI calls its proxy `/billing?format=credits` | `grok login` user OAuth, held and refreshed only by the CLI. An API key cannot read it: the handler requires grok.com auth | **No. Needs D1:** CLI install, one new login, and an undocumented method |
| G3 | Grok Build TUI `/usage` screen | Not in the documented CLI reference. The source says "Every prompt, `/usage`, and poll path hits `x.ai/billing`" | As G2 | As G2, display precision | Unknown | Terminal display | As G2 | **No.** Same gate as G2 and strictly worse (screen parsing) |
| G4 | `cli-chat-proxy.grok.com /v1/billing?format=credits` called directly | Internal proxy, undocumented | The copied token's account | As G2 | As G2 | HTTPS with client-impersonating headers | A copied user token; a second refresher can revoke the family | **Rejected** (hard rule: one owner per refresh token). Not a D1 option |
| G5 | xAI Management API billing: `POST /v1/billing/teams/{team_id}/usage`, `GET .../prepaid/balance`, `.../postpaid/invoice/preview`, `.../postpaid/spending-limits` | Official, documented | xAI API team (`team_id`) | Caller time range; monthly postpaid. No weekly pool | USD spend, tokens, other units | HTTPS REST, `management-api.x.ai` | Management key (Bearer) from the xAI Console | **No.** Not a Grok allowance. Only an `xai` spend row, if ever wanted, after a key decision |
| G6 | Inference response `usage` and `cost_in_usd_ticks` | Official, documented | The API key's team | Per request | Tokens, USD ticks | HTTPS | Inference API key | **No.** Only calls made with our own key; not Tom's Grok in Cursor |

### Cursor row candidates (where Grok-through-Cursor lives)

| # | Source | Official status | Account binding | Window and reset | Units | Protocol | Auth class | Permitted now? |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| C1 | Cursor CLI builtin `/usage` ("Show plan and on-demand usage") | Rendered by the qualified CLI, but absent from the public slash-command list (36 commands, no `/usage`) | Hashed `cursor-email-sha256` ref | Period not exposed; billing-cycle month/day label, display only | Rounded integer percent; `Included`, `Auto`, `API` rows | Terminal display of the user's own CLI | The CLI's existing login on the approved host | **Yes, existing source.** Weekly vs monthly plan and the unified flag are not shown |
| C2 | Cursor Settings > Usage, web dashboard (legacy Spending tab); for The X Subscription, grok.com/dashboard | Official display | The Cursor account | Legacy: monthly with the billing cycle. X Subscription: weekly | Not documented as percent | UI | Interactive session | **Parity reference only**, human read |
| C3 | `agent status`, `agent about --format json`, `/about` | Documented: authentication and account info | Cursor account | None documented | None documented | Local CLI | Existing login | Yes, but DESIGN-030 found no quota fields |
| C4 | Cursor Admin API: `/teams/spend`, `/teams/daily-usage-data`, `/teams/filtered-usage-events` | Official, documented | A team | Billing-cycle spend; no weekly percent or reset | Cents, events | HTTPS REST | Team API key (Basic) | **No.** Team only; no individual pool |
| C5 | Cursor `DashboardService` (`GetSandUsageStatus`, `GetCurrentPeriodUsage`) on `api2.cursor.sh` | Private, undocumented | The copied JWT's account | `usagePercent`, `nextResetTimestampUtc`, `isUnified` per third-party code | Percent | Connect JSON | Copied Cursor JWT | **Rejected under OP192**: a copied Cursor JWT on a private route (one owner per refresh token; DESIGN-030 "no private call follows"). The OP193 hold also applies. "Sand" is not proven equal to Tom's Grok 4.7 entitlement |

No source proves that a Cursor account and a grok.com account are one seat.
xAI's [pricing](https://docs.x.ai/x-subscription/pricing) says "Paid individual
plans include X Premium benefits when you connect your X account", but no
documented field exposes that link.

### Evidence (read 2026-11-05 unless noted)

- xAI [Plans and usage](https://docs.x.ai/x-subscription/usage-limits): "Each paid
  seat on The X Subscription receives a weekly pool for eligible first-party usage
  only"; "shared across eligible Grok and Cursor surfaces for that seat"; "Seats
  do not share weekly pools"; no API or CLI named.
- xAI [Pricing](https://docs.x.ai/x-subscription/pricing): "The shared included
  usage pool covers Grok Bot usage, and Grok models across Grok and Cursor";
  "Existing Cursor, SuperGrok, and X Premium plans continue unless you change or
  cancel them." Pool units are multiples of "Base", undefined.
- xAI [Extra usage](https://docs.x.ai/x-subscription/extra-usage): prepaid
  Additional Usage credit is separate from the weekly pool; review at
  grok.com/dashboard.
- xAI [Cursor Token Rate](https://docs.x.ai/x-subscription/cursor-token-rate):
  first-party models (Grok, Composer) are exempt; individual plans do not pay it.
- xAI [Billing reference](https://docs.x.ai/developers/rest-api-reference/management/billing):
  usage is "historical usage of the API"; no X Subscription endpoint.
- xAI [Grok CLI reference](https://docs.x.ai/build/cli/reference): `grok login
  [--device-auth]`, `grok agent stdio`; no usage, status or credits command.
- xAI source, public: [xai-org/grok-build](https://github.com/xai-org/grok-build),
  Apache-2.0, commit `2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8` (2026-09-29),
  `crates/codegen/xai-grok-shell/src/extensions/billing.rs`: method `x.ai/billing`,
  fields above, `isUnifiedBillingUser` "a shared weekly/monthly pool", proto3
  omits zero scalars, upstream error text passed into the ACP error.
- Cursor [usage limits](https://prod.cursor.com/help/models-and-usage/usage-limits):
  legacy plans have monthly pools; X Subscription has one weekly pool "shared
  across Cursor, Grok Bot, and the Grok apps"; Grok 4.7, 4.6 and 4.5 are Cursor
  models; usage at grok.com/dashboard or the legacy Spending tab.
- Cursor [Grok 4.7](https://cursor.com/help/models-and-usage/grok-4-7): Cursor
  models pool; effort levels low, medium, high and xhigh; 256k standard context,
  long-context rates above it. Tom's label "Grok 4.7 256K Extra High" matches
  xhigh at the 256k standard context by name only (not verified).
- Cursor [CLI parameters](https://cursor.com/docs/cli/reference/parameters) and
  [slash commands](https://cursor.com/docs/cli/reference/slash-commands): no
  documented usage command.
- Cursor [Admin API](https://cursor.com/docs/account/teams/admin-api): team key,
  spend and events, no weekly percent.
- Cursor forum, [Usage API / CLI Command?](https://forum.cursor.com/t/usage-api-cli-command/160967)
  (2026-05-19, no staff badge visible): "There's no public usage API or CLI
  command for individual plan usage right now." A later thread on Grok Bot usage
  returned 404 to the fetcher: Unknown.

## 3. Typed states and disclosure

Each state uses existing `providerallowance.read.v1` fields. No schema change is
needed (proved by the fixture in section 4).

| State | When | `read.v1` | `agent-run usage -o json` | Portal |
| --- | --- | --- | --- | --- |
| Not connected | No Grok source configured | Status `grok`, `producerRef` `collection-unconfigured`, `availability` `unconfigured`, attempt `unknown`/`source_unconfigured`, freshness `unknown`/`none`, validation `unverified`, binding `unknown`, `lastGood` null. No item | The same row. The local preflight adds `local_unconfigured_grok_no_provider_request` | Row "Grok", "Not connected", note "No Grok account view is connected. Nothing here is estimated." |
| Unknown | A source exists but nothing is trusted as current: auth needed, unavailable, unsupported, unknown binding, absent percent, team seat without a personal pool | Status availability as reported; item value `unknown` or current `unknown` with a fixed reason | Same document | "Unknown" with fixed reason copy; last reported values only as non-actionable lines |
| Stale | Last good older than the selected maximum age | Freshness `stale` | Same | "Stale", last trusted reading, no countdown |
| Expired | `currentPeriod.end` passed with no new reading | Current `expired`, value unknown; the observation keeps its historical decimal | Same | Historical line only; no bar |
| Measured | Verified binding, validated fields, fresh, exact `end` instant, and a present `creditUsagePercent` | Current `known` | Same | "Measured" |
| Shared with Cursor (deduped) | Only with a caller-trusted, verified `EntitlementRule` binding the Cursor and Grok account generations, under D2's proof standard | Grok current `not_applicable`, reason `shared pool alias`; Cursor carries the value | Same | Grok row says it is the same pool as Cursor and draws no second bar. Percentages are never added |

Relationship disclosure: until D2 is met, both the Cursor and Grok rows say
"Grok used in Cursor counts toward Cursor's plan usage. Whether Cursor and Grok
share one pool is Unknown." A proven "separate pools" state would need a new
trusted registry fact; it is out of scope and shows as Unknown.

Never shown or stored: raw provider payloads; upstream error text (G2 passes it
into its ACP error, so map it to fixed codes); grok.com user id, email, X handle,
team names, payment or invoice data; billing history arrays; subscription tier
free text unless allowlisted as a display field; any token or cookie. Account
binding uses only a hashed ref, following `cursor-email-sha256`. A Grok ref format
is not defined yet (D1, binding).

## 4. Contracts and synthetic qualification

### Added in this change (test-only)

`internal/providerallowance/op192_grok_row_test.go`,
`internal/providerallowance/testdata/op192/grok-row-not-connected.read.json` and
`internal/allowancecollector/op192_entitlement_test.go`. Synthetic inputs only;
no non-test source changes.

- `TestOP192GrokNotConnectedRowIsWireCompatible`: the four-row Not connected
  document passes strict decode and the reviewed read schema, carries no item,
  round-trips unchanged, and leaves the three portal rows Not connected. A
  variant gives Grok a `conflict` status, which a folded Cursor row would show
  as "Conflicting allowance reports"; Cursor stays Not connected.
- `TestOP192GrokNeverFoldsIntoCursorWithoutTrustedPool`: with the registry shape
  production allows (no `EntitlementRule`), Cursor `11` and Grok `12.5` stay
  separate, even when both payloads claim one `entitlementRef`. The cached portal
  reader shows Cursor `11% used` and no row shows `12.5` or `23.5`. A control with
  a verified pool does demote Grok, so the assertion is not vacuous.
- `TestOP192GrokRowTypedStates`: fractional percent kept exactly; past reset
  becomes `expired` with the historical value kept; an unknown reset is not
  invented; an absent percent stays Unknown, never `0`; paid spend is a separate
  money measure with no derivation; an account switch drops the old account's
  values.
- `TestOP192CollectorConfigRefusesAnySharedPoolRule` (allowancecollector):
  `ParseConfig` refuses an unverified Cursor/Grok rule, a verified Cursor/Grok
  rule and a verified Cursor/xAI rule. A control shows the same config and trust
  parse without the rule.

Results: all pass on the first run, because they characterize current
behavior. Sensitivity checks, each applied temporarily and then restored with
its hash re-checked:

- in the test, a verified pool in the production-shaped registry, absent
  percent replaced by `"0"`, and a future reset each failed the expected subtest;
- in `web/portal/session/product_allowance.go`, folding `grok` statuses into the
  Cursor row failed the `grok-conflict` variant (Cursor became "Conflicting
  allowance reports"); the identical-status document alone did not catch it;
- in `internal/allowancecollector/config.go`, removing the zero-`EntitlementRule`
  check failed all three refusal subtests.

The full `internal/providerallowance` and `internal/allowancecollector`
packages, `go vet` and `gofmt` pass.

### S1: typed Grok row (next bounded step, source only, approved by D5, no new access)

1. Make the API aggregate store (`indexAllowanceStatuses`, `preserveAllowance`)
   accept `grok` as an optional gap status with no item, and accept a
   three-row document replacing a four-row one. `ParseAggregateTrust` changes
   only in S2, when a Grok source is trusted.
2. Add `grok` to `Collection.build`'s unconfigured statuses, to the agent-run
   local preflight, and to `UnconfiguredAggregateDocument`
   (`provider_allowance_report_store.go:664`), so an empty slot also shows Grok
   as Not connected.
3. Add the portal row `{grok, Grok}` with the Not connected note and the
   relationship line on both Cursor and Grok rows, and add Grok to the portal
   preview states (`web/portal/preview/product_states.go:334`).
4. Update tests that count three statuses, the usage consolidation gap text and
   the docs. The collector config keeps three acquired providers.
5. Contract tests: API (aggregate TLS read), `agent-run usage -o json`
   (`--from-file` and `--read-current`), portal render and preview, each with
   this fixture.

**Two producers, one store.** `Collection.build` output reaches the store from
two binaries: the operator's own collector (`allowancecollector` journal, then
`AssembleCaptureBundle`, then `Collection.build`), and `agent-run` in the
separately pinned agent image (`usage capture` through `AssembleCaptureBundle`,
and `usage --publish-file`, which publishes the document it is given). The
store checks both the incoming and the stored document: `Put` calls
`preserveAllowance(prev, next)`, which calls `indexAllowanceStatuses(prev)`. An
operator that does not know `grok` therefore rejects every `Put` once a
four-row document is stored, not only four-row inputs. `Read` serves the stored
bytes after a canonical check only, so older portals and agents still read the
slot and ignore the Grok row.

**Release ordering.**

1. R1: an operator pin with step 1. It may also carry the served empty-slot
   document from step 2, which is never stored. Its own collector still emits
   three rows. Verify it is installed and serving.
2. R2, only after R1 is installed: everything that emits four rows, meaning the
   `Collection.build` change in the operator collector and an agent image whose
   `agent-run` emits four. The agent image pin must not move ahead of R1.
3. The portal change (step 3) only reads, so it can ship with either release.

**Rollback constraint.** Never roll the operator back below R1 while the slot
holds a four-row document. To roll back, first return every four-row producer
to three rows (operator R2 to R1, agent image to its prior pin), let a
three-row document replace the stored one (R1 accepts that), confirm it with a
read of the slot, and only then move the operator below R1. If a pre-R1
operator meets a stored four-row document, publication fails closed and the
stored values age to stale until an R1-or-later operator is restored.

### S2: Grok adapter (only after D1 approves G2)

A sanitized capture `grok.acp.billing.sanitized.v1` with closed fields:
`capturedAt`, `creditUsagePercent` (decimal string or null), `periodType`
(`weekly`, `monthly` or `unknown`), `periodStart`, `periodEnd`,
`isUnifiedBillingUser` (true, false or null), optional cents fields per D4, and
a hashed account ref per D1's binding item. It projects to `providerallowance.v1` with source
kind `native_rpc`, a dated source version and capability
`grok-acp:x.ai-billing`. Synthetic fixtures:

| Fixture | Expected |
| --- | --- |
| Weekly unified, `37.5`, period start/end | Known percent; reset instant = `end`; `durationMinutes` from start/end only |
| Monthly legacy shape (deprecated `monthlyLimit`/`used` only) | Percent Unknown; no derived percent until reviewed |
| Trial or promotional tier label | Tier is display text; no reset inferred from it |
| `creditUsagePercent` absent, including a fresh period at 0% | Unknown, not `0` and not Measured (proto3 omits zero) |
| `end` in the past | Expired; historical value kept |
| Period missing | Reset Unknown |
| `0.001`, `99.999`; `100.5`; non-numeric | Kept exactly; refused; refused with a fixed code |
| Account ref changes | Generation bump; old values dropped |
| `prepaidBalance`, `onDemandUsed` | Separate `api_spend` money measures; never summed with the percent |
| Team seat with no personal pool | `unsupported` with a fixed code |
| Upstream error with secret-like text | Fixed code; no text kept |
| `isUnifiedBillingUser` true and a weekly Cursor plan | Still no dedupe without a trusted `EntitlementRule` |

### S3: live acceptance (after D1 and D3, separately approved)

Two fresh Grok observations with distinct original times on the approved host,
read through the API, agent and portal, and Tom's same-time reading of
grok.com/dashboard (and Cursor's usage view) for parity. This is a gate to state
in the PR, not a claim.

proto3 omits zero values, so right after a reset (0% used) `creditUsagePercent`
is absent and the row stays Unknown, not Measured. S3 therefore needs
observations taken after some use in the period, and it cannot confirm a 0%
reading. An Unknown fresh period is expected, not a source failure.

## 5. Open decisions

Ask one at a time, D0 first. Record each answer as a dated ruling in
DESIGN-030's OP192 section, or in an ADR when it changes an accepted decision,
and point R-07 at it. R-07 itself stays the research record.

- **D0 (meatball, through the parent): Tom's Cursor plan type, then what the
  Grok row is.** OP192 and OP187 assume one verified shared Cursor/Grok
  entitlement. Cursor's docs say Grok 4.7 in Cursor counts in the Cursor-models
  pool, which is shared with the Grok apps on The X Subscription; legacy
  monthly pools are not described as shared. First ask whether Tom's Cursor
  account is on a legacy Cursor plan or The X Subscription. Then choose: (a) the
  Grok row is the grok.com allowance, a separate row; (b) only
  Grok-through-Cursor matters, so the Cursor row plus the disclosure line
  covers it and no Grok source is needed. Unblocks: whether D1 is needed at all,
  and the OP192 acceptance wording.
  *Recommendation:* get the plan type before any Grok Build login is
  considered. Choose (b) unless Tom says he also uses Grok apps or Grok Build on
  a subscription he wants tracked.
- **D1 (Tom, OP193): approve route G2?** Considered only after D0 chooses (a).
  G4 and C5 are rejected and are not options. Exact route:
  - Host: the collector host, a `workbench`-profile session (ServiceAccount
    `dev-env-workbench`, which `internal/allowancecollector/config.go`
    requires). Its egress is the web tier: haynes-ops
    `kubernetes/main/apps/dev-env-system/network/app/session-web.yaml` (read at
    `efe03617`) allows TCP 80/443 to any public address for `dev-agents`
    session pods other than `ops`, maintenance, canary and fixture pods. The
    hosts below are therefore reachable with no network change. Reachability is
    not authorization; a narrower allowlist would be a haynes-ops policy change.
  - Destinations: `https://cli-chat-proxy.grok.com/v1/billing?format=credits`
    (the CLI's proxy, `PRODUCTION_ENDPOINTS` in `xai-grok-env`), and the OIDC
    issuer `https://auth.x.ai` (discovery and token refresh). Tom completes the
    device-code page on his own device, not from the pod. Whether `grok agent
    stdio` with no prompt contacts other hosts by default (models list, the
    `code.grok.com` relay, the `grok.com` gateway, `assets.grok.com`) is Unknown
    and must be checked at the pinned release.
  - Install: a pinned `grok` CLI release from xai-org/grok-build in the
    collector host image.
  - Login: Tom runs one `grok login --device-auth` on that host. The device-code
    URL and code are secrets: never in a log, PR, tracker item, message or
    `~/.shared`.
  - Token: the CLI stores it in `~/.grok/auth.json` (owner-only `0600`;
    `GROK_HOME` and `GROK_AUTH_PATH` move it) with a refresh token
    (`offline_access`), and refreshes it itself. It stays on the host's private
    HOME volume, never in `~/.shared`, a worktree, a rescue bundle or a history
    export. The CLI is its only reader and refresher; the collector never reads
    the file.
  - CLI side effects: `billing.rs:261-266` writes each fetched credits snapshot
    to the unified log (`~/.grok/logs/unified.jsonl`, `billing.rs:148`), so
    account billing data lands on the host's disk; feedback, trace upload,
    managed config and telemetry default to the proxy
    (`xai-grok-config/src/endpoints.rs:3`, `:146`); upstream error text enters
    the ACP error. Before approval, verify at the pinned release which of these
    can be turned off (for example `OTEL_TRACES_EXPORTER=none`,
    `telemetry.otel_enabled`), and keep `~/.grok/logs` out of every export.
  - Binding: Tom's assertion in the private collector config, because the
    billing payload carries no account identity (the hashed ref format is
    defined with S2).
  - Data: the collector calls `x.ai/billing` over a local `grok agent stdio` and
    keeps only the S2 fields.
  Unblocks S2. *Recommendation:* defer. If D0 chooses (a), approve only with
  every condition above verified at the pinned release; otherwise reject, and
  the Grok row stays Not connected.
- **D2 (meatball, then Tom): proof standard for "shared with Cursor".** Proposal:
  never dedupe until a written standard exists. At minimum it needs Tom's
  statement that both accounts are one X Subscription seat; a human reading of
  Cursor's usage view (C2) that shows the weekly X Subscription plan, because
  the Cursor CLI display (C1) exposes no period; and a Grok reading with
  `isUnifiedBillingUser: true` whose period end matches, all taken together.
  Enabling it also needs a reviewed change to the collector config, which now
  refuses every `EntitlementRule` (pinned by
  `TestOP192CollectorConfigRefusesAnySharedPoolRule`). Unblocks the dedupe state.
  *Recommendation:* adopt "never dedupe" for V1 and record the standard above as
  the only path to change it.
- **D3 (Tom): parity reading.** Will Tom read grok.com/dashboard and Cursor's
  usage view at the time of a captured observation? Unblocks S3 and OP192
  acceptance. *Recommendation:* yes, at S3 only, one reading per observation;
  the agent records only the percent and reset Tom states.
- **D4 (meatball): money on the Grok row.** Show prepaid credit and on-demand
  spend as separate money measures, or percent only? Unblocks S2's field list.
  *Recommendation:* percent only for V1, which keeps payment data out; add money
  later as its own measures if wanted.
- **D5 (meatball): approve S1 now.** It needs no new access and is independent
  of D0 and D1. Unblocks visible Not connected and Unknown disclosure in API,
  agent and portal. *Recommendation:* approve, with the R1-then-R2 release
  ordering and the rollback constraint in section 4.
