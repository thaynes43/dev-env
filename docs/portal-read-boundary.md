# Portal read boundary (Story 178, M2)

This source milestone extends [DESIGN-029](../.agents/sagas/distributed-dev-env/designs/029-portal-foundations.md)
and merged PR525. It adds a Go standard-library `html/template` presentation
and injectable `net/http` read handler inside the existing module. It installs
no listener, identity middleware, connector or runtime binding. Gate A and the
separate public ASVS gate remain unmet.

## Dependencies and identity

`web/portal` accepts a trusted authenticator, current authorizer, summary-only
metadata reader and an explicit freshness window. The authenticator must return
an already authenticated immutable issuer-plus-subject principal. Request and
forwarded headers do not establish identity in this package. Authentication is
separate from authorization. OIDC identities are not operator human or workbench
callers. No production OIDC middleware or operator mapping is supplied.

The authorizer checks the exact principal, project, environment and task on
every request. An empty task selects the Attention collection, requiring explicit
collection permission as well as permission for each returned task. Denial
fails closed for the whole response; it does not turn a partial read into an
apparently empty inbox. Authentication and authorization are checked again
after the read and immediately before the buffered response is committed.
Withdrawal during a read discards all metadata. Future wiring must preserve
these current checks, local session revocation and cancellation.

`GET /portal/projects/{project}/environments/{environment}/attention` and
`GET /portal/projects/{project}/environments/{environment}/tasks/{task}` are
the only routes. There are no writes, command endpoints, streams, downloads or
redirects. Unknown/denied resources use the same generic response. Errors never
echo input, backend messages, identities or metadata. Missing dependencies and
the unavailable workflow adapter return 503, never fixture success.

## Summary and truth contract

The reader returns a bounded typed snapshot, with exact requested scope and
task identities. It has no field for prompts, logs, artifacts, pairing data or
credentials. Production adapters must select and redact fields before this
boundary; text fields must never be filled with sensitive payloads. Escaping
is not secret redaction. A separate pure projection refuses archived claims and validates the
existing task-claim/session binding and selects owner and worker metadata;
session exit is executor evidence, never the task outcome or release state.

Attention group, task outcome, next action and review facts are supplied by
canonical workflow metadata. This package does not infer transitions or replace
#177. Until that accepted interface is available, production attention uses `UnavailableReader`. The production Sessions and Tasks collections are independent and do not join a session to a work package. Sessions reads the operator portal-reader page `GET /v1/sessions`. The portal ServiceAccount is reserved before any other caller class and is refused while `--enable-portal-reader` is off. Each page lists at most the requested limit from namespace `dev-agents`, keeps every repository's sessions (each summary carries its own `repo`; until 2026-10-27 only `dev-env`), and continues with the opaque snapshot token alone while comparing the returned resourceVersion to the cursor. Freshness uses the operator-accepted `status.agent.lastHeartbeat`: future evidence is unknown, a heartbeat within 15 minutes is observed, and an older heartbeat is stale; a multi-page read retains its worst page freshness. Tasks reads only `GET https://openproject-web.frontend.svc.cluster.local:8443/api/v3/projects/3/work_packages?offset=<page>&pageSize=50&filters=%5B%5D`. Offset is the page number. `filters=[]` is the only filter, so the read is not the upstream default open-status collection. A returned page link is neither checked nor requested; the next request is rebuilt at that fixed origin (OpenProject 17 points its own link at `/api/v3/workspaces/3`). A page may be up to 1 MiB so a full HAL work package, including its description, fits. Since 2026-10-27 a plain-text excerpt of at most 600 runes is rendered; the full description stays in OpenProject. The portal keeps a row only when its project link is `/api/v3/projects/3`. A missing token says that view is not connected. A failed, redirected, or untrusted read says the view is unavailable and does not present an empty inventory. `secrets/OPERATOR_TOKEN` is re-read on each request and must be a current JWT whose only audience is `dev-env-operator`. `secrets/OPENPROJECT_TOKEN` is a separate bearer. Repositories is `customField3` free text; blank text stays Unknown. The deferred attention reader can still parse schema 2 bindings in tests. Those bindings are not a production startup requirement and are not the Tasks inventory. The #177 group reducer remains the offline checker in `scripts/trials/workflow_handoff`. OpenProject collection responses must be `application/hal+json`. Operator responses stay `application/json` and reject unknown fields. The preview command stays on its fixtures.

The portal asks for `GET /v1/sessions?limit=50&view=activity`. `view=activity`
is opt-in: without it the operator keeps each summary to its original six keys.
A portal image from before 2026-10-27 refuses the activity keys added that day
and any repository but `dev-env`, so the portal image rolls out before the
operator image; a newer portal accepts an older operator's output. A newer
portal against an operator without the view gets a refusal and says Sessions
unavailable. With the view, each summary adds one `activity` object, DESIGN-029's
browser allowlist: `mode` (`task`, `local`, `remote`), `provider` (`claude`,
`codex`, `opencode`), agentd's `agentState` word, `lastActivity` (absent when
unknown or in the future), the configured `requestedModel`, `launchedBy`
(`spec.parent`, omitted when unset or outside `apiv1.PortalLauncherPattern`),
for a task `taskResult` (`Succeeded`, `Failed`, `TimedOut`, `Unknown`; the
status message and reason are never sent), and `usage`. Any other word or an unbounded model id is
`Unknown`. `usage` is the session's own initial managed invocation as its
provider reported it (DESIGN-018), from the same validated observation
`agent-run show` reads. Only task sessions carry one, so an interactive or
remote session's usage is always `Unknown`. Provider-reported served models stay
separate from the requested model. Each token counter keeps `Unknown` apart from
an explicit zero. Claude reports cache read tokens apart from input; Codex input
already includes them. Cache reads are never added to input. Cost is shown only as the existing CLI-equivalent estimate, never as a
charge or a subscription quota. The Usage view sums reported values per group
(by how sessions ran, by provider and requested model, by project); an
`Unknown` counter or cost is never counted as zero, and the account allowance
is shown apart, with its own state (see [Account allowance](#account-allowance)). The portal accepts a row of exactly the six base keys, or those six
and a valid `activity`; a row without `activity` renders every activity field
`Unknown`, never zero. It refuses a null or malformed `activity`, an unknown key,
a null, a word outside its set, an `Unknown` scope that carries a reported value,
or a mode other than `task` whose usage scope is not `Unknown`. The Usage view
below states the same coverage.

Task outcome, worker liveness, review, authority and release are displayed
separately. Canonical links are constructed from validated numeric work-package
IDs at one fixed HTTPS origin; no imported URL becomes a link.

Missing or future timestamps, or verification older than the source update,
mean Unknown. Evidence beyond the explicitly configured window is Stale. Only
a fresh verified empty snapshot says Verified empty; denied/unavailable/unknown
and stale empty snapshots never claim that nothing needs attention. The window
is a fixture parameter, not an operational freshness guarantee.
Nonempty listings also carry their own verification evidence. Missing, stale,
future or verification-before-update listing evidence produces a visible warning
and Unknown group counts, even if an individual row is freshly verified.

Plain text is bounded and escaped with `html/template`. Display controls are
removed; blank/invisible-only text becomes Unknown. No raw HTML, Markdown,
remote resources or scripts are rendered. Responses use no-store, a restrictive
CSP, no-referrer and nosniff. These source checks do not establish deployed
authentication, session security, proxy trust, CSRF, SSRF or ingress isolation.

## Sessions and Tasks presentation

The Sessions and Tasks pages are the approved "Agents" design (Astra v2,
2026-10-26): charcoal, dark first, lilac accents, with a coordinated light
theme chosen by `prefers-color-scheme`. Both are server-rendered with no
script. They keep the two existing routes; nothing new is fetched or routed.

- **Shell.** A top bar (Agents, Tasks, Automations and Watchdog marked
  Planned, Usage), a provider allowance strip whose values are `Unknown`, a rail,
  a grouped list and a workspace. Sessions are grouped Needs attention, In
  progress, Stopped and State Unknown, newest activity first. A Suspended,
  Archived or Failed phase decides the group and the badge; agentd's last
  reported state outlives the pod, so it appears only in Activity, labelled as
  last reported. Tasks are grouped by status. Counts appear only for a
  complete read.
- **Selection.** Each list item links to an in-page anchor (`#s1`, `#t1`). CSS
  `:target` shows that item's detail; with no anchor the first item's detail is
  shown. On a phone the list shows first, the detail replaces it, and a back
  link returns to `#list`. A bottom bar replaces the rail.
- **Usage view (`#usage`).** Measured sessions as "k of N", account
  allowance and billed cost `Unknown`, one row per session with input, output,
  cache read, cache create and the CLI-equivalent estimate, and a coverage
  panel. A session without measured usage shows `Unknown` in every cell. No
  totals are computed.
- **Conversation usage.** An interactive session whose agent reports its
  registered conversation (behind the operator's default-off
  `--portal-conversation-usage`) gets a Conversation usage panel: Final or
  Partial with its reason and "a lower bound", Current or Stale (older than 15
  minutes, still shown), one row per served model, and no price. The Usage view
  then lists a table by provider and served model first, apart from the
  initial-invocation tables, and never adds Claude and Codex together. DESIGN-029
  "Interactive conversation usage, Unit B milestone 2" has the wording.
- **Planned view (`#planned`).** Automations (reminders, scheduled runs and
  media workflows) and Watchdog are listed as Planned. No scheduler, monitor or
  backend exists for them.
- **Fixture label.** Fixture previews say "Fixture preview · sample data".

A rendered Sessions or Tasks page sends `portal.ProductPolicy`: `default-src
'none'`, `style-src` with the SHA-256 of the embedded `views.css` and nothing
else, `base-uri 'none'`, `form-action 'none'`, `frame-ancestors 'none'`. The
session layer's stricter policy, which has no `style-src`, stays on every
refusal. With only the session layer's policy, Chromium blocks the page's
inline stylesheet under `default-src 'none'`; this was checked on the fixture
preview on 2026-10-26. `html/template` drops CSS comments from template
text, so `views.css` carries none; a test hashes the rendered `<style>` against
the policy.

## Account allowance

Sessions shows a compact Account allowance list in the list header. Usage shows the
same model in full, also on a read with no rows. Tasks, Asks and Pages show none.
A failed Sessions read shows no allowance.
No allowance reader is deployed, so production shows Claude and Codex as
`Not connected`, with no percent.

When any provider reports usage windows, the provider lines sit in a "By provider"
disclosure. On a window wider than 700px and at most 760px tall it starts closed beside
the Account allowance link, so a connected strip cannot squeeze the session list
(OP307, 2026-10-31). Elsewhere it stays open. A strip with no windows, such as
today's `Not connected` line, has no disclosure and never folds.

`MapProductAllowance` is the pure projection a later reader will use. It maps a
decoded `providerallowance.read.v1` document ([DESIGN-030](../.agents/sagas/distributed-dev-env/designs/030-provider-allowance-contract.md))
to four states. Only the preview fixtures call it today.

| State | Meaning | Percent |
| --- | --- | --- |
| `Not connected` | no reader, or `unconfigured` availability | never |
| `Unknown` | a reader answered, but the value cannot be trusted; carries a safe reason | never |
| `Stale` | validated, verified, known value with `stale` freshness | as reported |
| `Measured` | validated, verified, known value with `fresh` freshness | as reported |

A percent is printed as the contract spells it, with its reported direction (`% left`
or `% used`). The portal never converts used to remaining, reads no derivation and
takes no token or cost input. A window label comes from `durationMinutes`; a contract
ref is never displayed. Windows that are not percent, or not `included_quota` or
`rate_limit`, are left out. A window with no known value shows `Unknown` beside any
known ones. Any current conflict in a provider's items refuses that provider, whether or
not the measure is displayed. Differing decimals also refuse it: on a measure the portal
never displays, any difference; on a displayed window, when both values fit the percent
grammar or neither does. A window with exactly one value outside that grammar shows
`Unknown` instead.

## Mobile presentation

Essential facts stay visible on every item: canonical source, owner, next actor, next action, source age, source update, last verification and freshness. Task outcome, worker liveness, review, reviewed head, authority and release stay labeled and separate. A returned review keeps its review state and reviewed head visible. A missing handoff keeps worker liveness and task outcome visible. The other separate facts stay in a native `details` disclosure. The disclosure is not an action and queues nothing. A single-task page names that task's group only and does not present the other groups as verified empty.

A verified empty snapshot shows all four groups at zero and says verified empty. A listing whose own evidence is missing, stale, future or older than its source update keeps Unknown group counts, including when a row inside it is fresh. A read whose state is unknown does not render group counts. Denied and unavailable reads stay the same plain refusal. They never become a verified empty page.

An offline browser check drives this preview renderer and the Chromium already present in the session image. It is repeatable synthetic evidence at 320px, 390px, tablet and desktop widths. It is not a physical-device pass, deployed authentication, or Gate A.

The checker defaults are bound to the dev-env session image: Playwright module `/usr/local/lib/node_modules/@playwright/mcp/node_modules/playwright` and Chromium `/opt/dev-env/ms-playwright/chromium-1247/chrome-linux64/chrome`. Override them with `PORTAL_PLAYWRIGHT_MODULE` and `CHROME_PATH`. A missing module or binary stops with that path instead of a stack trace. Run one checker at a time. The checker refuses a scenario directory whose `manifest.json` does not match the file hashes and the checkout source head. Uncommitted changes under `web/portal` or `scripts/trials/portal_attention` are refused, so a dirty tree cannot be labeled as an exact commit. Standard output is the elapsed time and failure count. `report.json` records the Playwright version, Chromium version, source head and manifest digest. The scenario command writes only a directory that does not yet exist. An existing destination and an existing `<dir>.previous` are left unchanged. The commands below call `mktemp -u`, which prints a new path and does not create it, so a second copy of the same command does not reuse or delete the first directory.

```sh
scenarios=$(mktemp -u /tmp/portal-attention-scenarios.XXXXXX)
browser=$(mktemp -u /tmp/portal-attention-browser.XXXXXX)
GOTOOLCHAIN=local GOMAXPROCS=2 nice -n 19 \
  go run -p 1 ./web/portal/preview --scenarios "$scenarios"
nice -n 19 node scripts/trials/portal_attention/browser_check.cjs \
  "$scenarios" "$browser"
```

## Offline evidence and custody

Run focused tests with Go1.27.2 as pinned by `go.mod`, `GOTOOLCHAIN=local`,
`GOMAXPROCS=2`, `nice -n 19`, `go test -p 1 ./web/portal/...`. Tests use injected
dependencies and recorders, with no provider, tracker, cluster or listener.
The preview command under `web/portal/preview` renders synthetic data through
the same handler into an HTML file. It uses a fixed clock and opens no socket.
Its exact invocation and verification receipts are recorded with the PR.

From the repository root:

```sh
GOTOOLCHAIN=local GOMAXPROCS=2 nice -n 19 \
  go run -p 1 \
  ./web/portal/preview --output /tmp/portal-attention.html
```

Open that local file to inspect the mobile page. The Sessions and Tasks pages
render the same way from `scripts/trials/portal_product/sessions.json` and
`tasks.json`, a synthetic operator page and OpenProject collection with a fixed
clock, through the live decoders and page builders:

```sh
GOTOOLCHAIN=local GOMAXPROCS=2 nice -n 19 \
  go run -p 1 ./web/portal/preview --product "$(mktemp -d)"
```

That writes `sessions.html` and `tasks.html`, labelled as fixture previews.
The Sessions fixture includes Final, Partial and stale conversation-usage
figures. The command reuses
`scripts/trials/portal_attention/fixtures.json` and its fixed observation time;
it does not connect to OpenProject, Authentik, a provider or the operator. The
preview's issuer and subject are invented `.example` fixture values. The
fixture's `readState` and fixed `asOf` supply its synthetic listing evidence;
individual row verification remains separate. The
offline authenticator/authorizer live only in this command and are never
defaults for the handler. An independent second render in the preview test
compares the full output, supporting repeatability rather than merely
asserting a checksum.

Before live wiring, Gate A must include dependency-panic containment and server
log redaction. Although buffering prevents a dependency panic from returning
metadata, a default `net/http` server may log the panic value. This source
package installs no server and establishes no production log protection.

This session owns source, tests, findings and review/CI/merge pickup. Source
merge is distinct from release, installation and usable acceptance. Approved
client `dev-env-v2-portal-internal`, exact callback
`https://dev-env-v2.haynesops.com/auth/callback`, `openid/profile` and server-side
secret scope remain recorded in #178 journal905. Authentik/1Password work is
separate; runtime human binding and internal Traefik activation remain held.
