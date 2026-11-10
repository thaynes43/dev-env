# R-08 Cursor usage sources (2026-11-10)

OpenProject #532, under epic #187. Tom, 2026-11-10: community tools may already
read Cursor plan usage, and he pointed at
[ofershap/cursor-usage](https://github.com/ofershap/cursor-usage). This note
records what those tools call, what Cursor's own docs say, and what a read-only
check from this session pod actually returned. It changes no collector and
ships no reader. The build stays a later session.

Checked 2026-11-10 (UTC) from session `dev-env-1110-044912`. Source was read
and not run. Live checks were eight HTTPS reads, listed below. No login, no
browser, no cookie, no write, and no second try after a 4xx.

## Bottom line

1. The tools split in two. The Enterprise Admin and Analytics APIs take a team
   API key and return team spend, request counts, tokens and a billing-cycle
   start. They do not return an individual plan's included percent. The
   individual percent lives on undocumented `cursor.com` routes that want the
   browser session cookie `WorkosCursorSessionToken`, or, for one older route,
   the desktop app's access token.
2. This pod's `CURSOR_API_KEY` is a user API key. `GET /v1/me` returned 200 and
   the identity field names only. Every Admin and Analytics read returned 403
   `permission_denied`. The two undocumented usage routes, called with that
   same key as a Bearer token, returned 401. No plan number came back.
3. The smallest reader that fits the rules is no plan reader. The usage page
   should keep counting what dev-env itself sends, and say in plain words that
   Cursor's plan allowance cannot be read with the key a session pod holds.
   Adopting the cookie route would be a new choice for Tom. It was not tried.

## 1. Tools and the routes they call

Cloned with `git clone --depth 1` into `~/work/research-532` on 2026-11-10,
except UsageButtons, whose GitHub URL returned 404 to both `git clone` and the
GitHub API. Its source was read from the public Go module proxy at `v0.8.0`
(git `6a5a42a496340186668067679c0617fd7d817921`, 2026-05-03). The cursor
provider file is identical in the later module version
`v0.8.1-0.20260503194045-dddd60c7e7d3`. Nothing was installed or executed.

| Tool | Pin | Credential it sends | Individual plan? | Numbers it keeps |
| --- | --- | --- | --- | --- |
| [ofershap/cursor-usage](https://github.com/ofershap/cursor-usage) `65175f0` (2026-02-20) | MCP over the Enterprise APIs | Team Admin API key, HTTP Basic, user is the key and the password is empty. Analytics may want a second key. Env `CURSOR_API_KEY` in that project means the Admin key, not the CLI key | No. README: Enterprise team | Spend cents, included-or-total spend, premium-request counts, cycle start, daily request and line counts, per-event tokens and cents, analytics counts. Also a write: set a user's spend limit |
| [ofershap/cursor-usage-tracker](https://github.com/ofershap/cursor-usage-tracker) `d854ef4` (2026-11-03) | Same APIs, plus a dashboard | Same Basic team key. Env `CURSOR_ADMIN_API_KEY` | No. README: Enterprise plan and an Admin API key | Same family. Normalizes `overallSpendCents` as the total and `spendCents` as on-demand when both are present |
| [udah1/cursor-usage-mcp](https://github.com/udah1/cursor-usage-mcp) `1e5534d` (2026-09-02) | Dashboard routes, no browser at read time | Cookie `WorkosCursorSessionToken=<user sub>::<access token>`. Default source is the desktop `state.vscdb` key `cursorAuth/accessToken`. Fallback is a Playwright login that stores the browser cookie | Yes. Individual calls send `teamId: -1` | Legacy request count and limit, or the token-priced pool percents, on-demand dollars, cycle start and end. Optional per-model tokens and cents |
| [YossiSaadi/cursor-usage-vscode-extension](https://github.com/YossiSaadi/cursor-usage-vscode-extension) `bf6818c` (2025-08-03) | Status bar | The same cookie, pasted from the browser. Polls every 30 minutes by default | Yes, for the legacy request bucket. Team spend is a second path | Per-model `numRequests`, `numTokens`, `maxRequestUsage`, `maxTokenUsage`, `startOfMonth`. Team path adds fast-premium request counts and spend cents |
| [Sammy970/cursor-usage-extension](https://github.com/Sammy970/cursor-usage-extension) `189de19` (2026-04-01) | Status bar | Bearer token read from `state.vscdb` `cursorAuth/accessToken`. Not the CLI API key | Yes, for that desktop login. Legacy bucket only | `gpt-4.numRequests`, `gpt-4.maxRequestUsage`, `startOfMonth`. If the limit is missing it substitutes 500 |
| [anthonybaldwin/UsageButtons](https://github.com/anthonybaldwin/UsageButtons) `v0.8.0` | Stream Deck button, through a Chrome helper | The signed-in browser's cookie jar. The plugin never sees the cookie. Chrome fetches with credentials | Yes | `totalPercentUsed`, `autoPercentUsed`, `apiPercentUsed`, on-demand cents and limit, cycle end. Legacy `gpt-4` request count and limit when that bucket has a limit |

### Hosts and paths

All of these are reads unless marked. Hosts are exactly as the source writes them.

**ofershap/cursor-usage** and **cursor-usage-tracker**, `https://api.cursor.com`, Basic auth:

| Method and path | What the source expects back |
| --- | --- |
| `GET /teams/members` | Member id, name, email, role, removed flag |
| `POST /teams/daily-usage-data` | Per user per day: request counts (`subscriptionIncludedReqs`, `usageBasedReqs`, `apiKeyReqs`, composer, chat, agent), line counts, most-used model. Docs: hourly aggregate, at most one poll an hour, dates required, 30 days max |
| `POST /teams/spend` | Per member `spendCents` (on-demand), `overallSpendCents` (on-demand plus included), `fastPremiumRequests`, dollar limits, and `subscriptionCycleStart` (epoch ms). No cycle end, no included percent |
| `POST /teams/filtered-usage-events` | Per event: model, request units, token counts, `chargedCents`, optional Cursor token fee. Docs: hourly aggregate |
| `GET /teams/groups` | Group spend and `billingCycle.cycleStart` / `cycleEnd`. The tracker README says POST; the client calls GET, which matches the docs |
| `GET /analytics/team/dau`, `/models`, `/agent-edits`, `/tabs`, `/mcp`, `/top-file-extensions`, `/client-versions`, `/commands`, `/plans` | Counts of people, messages, edits, tabs, commands. Not an allowance |
| Tracker only: `GET /analytics/ai-code/commits`, `GET /analytics/by-user/mcp`, `GET /analytics/by-user/commands` | Commit and per-user adoption counts |
| `POST /teams/user-spend-limit` | Write. Sets `hardLimitDollars`. Not a read, and not called |

**udah1/cursor-usage-mcp**, cookie, `https://cursor.com`:

| Method and path | What the source expects back |
| --- | --- |
| `GET /api/usage?user=<sub>` | Legacy model map. The included count is `gpt-4.numRequests` and the limit is `gpt-4.maxRequestUsage`. A usage-priced plan returns `numRequests: 0` and `maxRequestUsage: null` |
| `GET /api/usage-summary` | `membershipType`, `isUnlimited`, `limitType`, `billingCycleStart`, `billingCycleEnd`. `individualUsage.plan`: `used` and `limit` in cents, `autoPercentUsed`, `apiPercentUsed`, `totalPercentUsed`. `individualUsage.onDemand`: enabled, used, limit, remaining, in cents |
| `POST /api/dashboard/get-aggregated-usage-events` | Per model: cents, request units, input, output, cache read, cache write. Body `teamId` is `-1` for an individual account |
| `POST /api/dashboard/teams` | Team only. `requestQuotaPerSeat`. The tool then sets the request limit to `500 * requestQuotaPerSeat` |
| `POST /api/dashboard/get-hard-limit` | Team only. `hardLimitPerUser` in dollars |
| `GET https://cursor.com/dashboard` | Browser login fallback only. Not used on the read path |

The tool's own warning, kept because it changes the meaning of the cents
fields: do not divide `plan.used` by `plan.limit`. `used` is combined spend
and `limit` is only the third-party pool. The percents are the numbers Cursor
computed.

**YossiSaadi/cursor-usage-vscode-extension**, cookie, base
`https://cursor.com/api`:

| Method and path | What the source expects back |
| --- | --- |
| `GET auth/me` | Email, name, `sub` |
| `GET usage?user=<sub>` | `gpt-4`, `gpt-3.5-turbo`, `gpt-4-32k`, each with request count, token count and limits, plus `startOfMonth` |
| `POST dashboard/teams` | Team id and name |
| `POST dashboard/team` | The caller's user id on that team |
| `POST dashboard/get-team-spend` | Per member fast-premium requests and `spendCents` |

**Sammy970/cursor-usage-extension**:

| Method and path | Credential | What the source expects back |
| --- | --- | --- |
| `GET https://api2.cursor.sh/auth/usage` | `Authorization: Bearer` the desktop access token | `gpt-4.numRequests`, `gpt-4.maxRequestUsage`, `startOfMonth` |

The README says `cursor.com/api/usage` needs the cookie, which is why this
extension uses `api2.cursor.sh` instead. The substituted limit of 500 is the
extension's, not Cursor's, whenever `maxRequestUsage` is absent. udah1 treats
that absence as "no request quota".

**UsageButtons**, through the Chrome helper, cookie stays in Chrome:

| Method and path | What the source expects back |
| --- | --- |
| `GET https://cursor.com/api/usage-summary` | The percent fields, on-demand cents, `billingCycleEnd`, `membershipType` |
| `GET https://cursor.com/api/auth/me` | `sub`, used only to decide whether to call the legacy route |
| `GET https://cursor.com/api/usage?user=<sub>` | Legacy `gpt-4` request count and limit |

## 2. Official routes

Read on 2026-11-10 from cursor.com.

| Page | What it says about usage |
| --- | --- |
| [API overview](https://cursor.com/docs/api) | Admin API and Analytics API are for Enterprise teams, Basic auth, key as the username. Cloud Agents API takes Basic or Bearer, and a user API key or a service-account key. No individual plan-usage route |
| [Admin API](https://cursor.com/docs/account/teams/admin-api) | The paths in the ofershap table. Spend is on-demand cents plus overall cents and a cycle start. Daily usage is request and line counts, aggregated hourly. Groups add a cycle start and end. No included-pool percent |
| [Analytics API](https://cursor.com/docs/account/teams/analytics-api) | Team and by-user adoption counts. Cache 15 minutes. Not an allowance |
| [Cloud Agents API](https://cursor.com/docs/cloud-agent/api/endpoints) | `GET /v1/me` is API-key info: key name, created time, and for a user key the owner's id, email and name. `GET /v1/agents/{id}/usage` is input, output and cache tokens for that cloud agent, not the plan pool. Legacy v0 is still documented as a migration source. `GET /v0/me` is not on this page |
| [Usage and limits](https://cursor.com/help/models-and-usage/usage-limits) | How to check usage is a page. The X Subscription: grok.com/dashboard, weekly pool, reset date shown there. Legacy plans: the Spending tab, monthly, reset with the billing cycle. No API |

The overview's key format for an Admin key is a team key with scope `admin:*`.
A user API key is a different credential, created for Cloud Agents and the CLI.

## 3. What this pod's key could read

The pod already holds `CURSOR_API_KEY` for the Cursor CLI. It was sent only as
HTTP Basic or Bearer, only to `api.cursor.com`, `api2.cursor.sh` and
`cursor.com`, only on these reads. Response headers were discarded. Bodies
were parsed for field names. Identity values were not kept.

| # | Request | Auth | Status | Field names | Usage numbers |
| --- | --- | --- | --- | --- | --- |
| 1 | `GET https://api.cursor.com/teams/members` | Basic | 403 | `code`, `message`, `details` | None. `code` was `permission_denied` |
| 2 | `POST https://api.cursor.com/teams/spend` with `page` 1 and `pageSize` 1 | Basic | 403 | same | None. Not retried |
| 3 | `POST https://api.cursor.com/teams/daily-usage-data` with a one-day `startDate` and `endDate` | Basic | 403 | same | None. Dates are required by the docs, so the body was that minimum. Not retried |
| 4 | `GET https://api.cursor.com/v0/me` | Basic | 403 | same | None |
| 5 | `GET https://api.cursor.com/v1/me` | Basic | 200 | `apiKeyName`, `userId`, `createdAt`, `userEmail`, `userFirstName`, `userLastName` | None. These are the user-scoped identity fields. The docs omit them for a service-account key. Values not recorded |
| 6 | `GET https://api.cursor.com/analytics/team/dau?startDate=7d&endDate=today` | Basic | 403 | same as the Admin 403 | None |
| 7 | `GET https://api2.cursor.sh/auth/usage` | Bearer | 401 | `code`, `message`, `details` | None. `code` was `unauthenticated` |
| 8 | `GET https://cursor.com/api/usage-summary` | Bearer | 401 | `error`, `description` | None. `error` was `not_authenticated` |

`GET /teams/groups`, `POST /teams/filtered-usage-events` and
`GET /v1/agents/{id}/usage` were not called. The first two are the same Admin
credential that was already denied. The third needs an agent id and is not a
plan allowance. No write route was called.

### Credentials, and whether we hold them

| Route that can return usage | Credential | Held in this pod? |
| --- | --- | --- |
| Admin and Analytics reads | Team API key, scope `admin:*` for Admin, Enterprise team | No. The held key is a user key: `/v1/me` succeeded and the team routes returned `permission_denied`, not `unauthenticated` |
| `GET /v1/me` | That user key | Yes. Identity only |
| `GET /v1/agents/{id}/usage` | Same user key, per the Cloud Agents docs | The key, yes. Not called. It would be one cloud agent's tokens, not the plan percent |
| `GET /api/usage-summary`, `GET /api/usage`, the dashboard POSTs | `WorkosCursorSessionToken` cookie | No. A 401 with the API key as Bearer matches the tools, which do not send the API key to these routes. No cookie was sought |
| `GET https://api2.cursor.sh/auth/usage` | Desktop access token from `state.vscdb`, as Bearer | No. There is no `state.vscdb` under `~/.cursor`, `~/.config/cursor` or `~/.config/Cursor` |

Local files, names only. After this session's CLI had started with the key,
`~/.config/cursor/auth.json` was present, mode `0600`. It was not opened, not
copied and not sent. The desktop file the community tools read,
`Cursor/User/globalStorage/state.vscdb`, was absent. So the CLI does keep a
local auth file, and it does not keep the desktop access-token database those
tools use.

## 4. Which numbers exist, and how fresh they are

Nothing in section 3 was a usage reading, so this account's percent, count,
limit and reset were not observed. From the sources, the numbers are:

| Source | Numbers | How fresh the source says it is | Usable here |
| --- | --- | --- | --- |
| `GET /api/usage-summary` | Included percents for Cursor models, other models, and the blended total. On-demand cents, limit and remaining. Cycle start and end. Membership type | Not documented. The tools treat one call as the dashboard's current figure. Our call was 401, so no cache header was seen | No. Needs the browser cookie |
| `GET /api/usage` and `GET api2.cursor.sh/auth/usage` | Legacy request count and limit, and a month start. Empty limit means a usage-priced plan, not zero usage | Not documented | No. Cookie, or the desktop access token |
| `POST /teams/spend` | On-demand cents, overall cents, premium-request count, dollar cap, cycle start. No included percent, no cycle end | Current billing cycle. No documented lag | No. Team key, and the wrong shape for one person's included pool |
| `POST /teams/daily-usage-data` | Included, on-demand and API-key request counts, by user and day | Aggregated hourly. Docs say poll at most once an hour | No. Team key |
| `POST /teams/filtered-usage-events` | Tokens and charged cents per event | Aggregated hourly | No. Team key |
| `GET /teams/groups` | Group spend, cycle start and end | The cycle you ask for, default the current one | No. Team key. Not called |
| `GET /v1/agents/{id}/usage` | Token counts for one cloud agent | Per run, as recorded for that agent | Not a plan allowance |
| Analytics `GET /analytics/team/*` | Activity counts | 15-minute cache | Not an allowance |

## 5. Recommendation

Do not build a plan reader in the #532 build session.

The smallest thing that fits the rules:

- Count Cursor from records dev-env already has: tasks, wall-clock minutes and
  the named model. That needs no Cursor call.
- On the Usage page, say plainly that Cursor's plan allowance cannot be read
  with the key a session pod holds, and link to Cursor's own usage page. The
  help page puts The X Subscription on grok.com/dashboard and a legacy plan on
  the Spending tab.
- Leave `~/.config/cursor/auth.json` to the CLI. One owner per rotating login.
  Do not parse it, copy it, or rebuild a `WorkosCursorSessionToken` from it.
- Do not scrape with a browser cookie unless Tom chooses that in its own
  decision. The route that actually carries the individual percents is one
  `GET https://cursor.com/api/usage-summary` with that cookie. It is
  undocumented, it is a human's browser session, and this pod does not have it.

A team Admin key would not fix the individual tile even if Tom later created
one. It reports team spend and request counts for an Enterprise team. It does
not report the included-pool percent the dashboard shows one person. The key
in this pod is not that key.

This agrees with DESIGN-059 section 9 and with R-07's Cursor rows C3 and C4:
the CLI output has no usage field, and the Admin API is team spend. R-07's C5
(a copied JWT on a private `api2.cursor.sh` call) stays rejected. The 2026
tools do not change that. They reach the individual percent only by sending a
browser session.
