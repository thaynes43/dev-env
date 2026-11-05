# Portal local test instance

Run the dev-env v2 portal on a loopback address, with fixture data and one
fixed test principal, and drive it with headless Chromium or Playwright. It
serves the production routes, handlers, templates, CSS and CSP. Every page
carries a "Local test instance" banner. Design:
[DESIGN-055](../.agents/sagas/distributed-dev-env/designs/055-portal-local-test-instance.md);
story [OP476](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/476).
The write flows (send, launch, history and the Asks answer form) came with
OpenProject
[#479](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/479).

## Start it

From the repository root:

```sh
make portal-localtest LOCALTEST_ARGS="-listen 127.0.0.1:8099"
```

Or build once and run the binary:

```sh
GOTOOLCHAIN=local GOMAXPROCS=2 nice -n 19 \
  go build -tags portallocaltest -o ~/.local/bin/portal-localtest ./web/portal/localtest
nice -n 19 portal-localtest -listen 127.0.0.1:8099
```

The first output line is the URL:

```
Portal local test instance (fixture data, fixed test principal, not production): http://127.0.0.1:8099/portal/projects/dev-env/environments/internal/sessions
```

Flags:

- `-listen`: a loopback IP and port. The default is `127.0.0.1:0`, which
  picks a free port. `[::1]:0` is accepted where the host has IPv6 loopback.
  Names (even `localhost`), wildcard, LAN addresses and Unix sockets are
  refused.
- `-fixtures`: the directory with `sessions.json` and `tasks.json`. The
  default is `scripts/trials/portal_product`.

Stop it with Ctrl-C or SIGTERM. It removes its temporary fixture token
directory on exit.

Use the address it printed. A request whose Host is not that address (or
`localhost` with the same port) gets 403.

Logging out revokes the one fixed session. Every page then redirects to a
login that is unavailable. Restart the instance to sign in again.

## Pages

All under `/portal/projects/dev-env/environments/internal/`:

| Page | Fixture rows |
| --- | --- |
| `sessions` | `fixture-portal-polish`, `fixture-coordinator`, … |
| `tasks` | `Fixture: portal visual refresh`, … |
| `pages` | `Fixture sample alert` |
| `asks` | three asks: open info, open approval, answered approval |
| `claims` | three claims: running, idle, owner not found |

Query strings work as in production, for example `sessions?session=fixture-uid-coordinator`.

## Flows

The production handlers run each flow. The far end is a fixture operator
route that records the call. No agent receives anything and no session
starts.

| Flow | Where | What the fixture does |
| --- | --- | --- |
| Send | the composer under a selected session, for example `sessions?session=fixture-uid-coordinator` | answers Delivered; the receipt shows under `session-messages/<request ID>` |
| Launch | `sessions#launch`, repositories `dev-env` and `fixture-repo` | creates one Pending session per request ID (`fixture-launch-1`, …) that joins the list; the same request again returns the same session |
| History | "Load this session's conversation" for `fixture-coordinator` or `fixture-terminal` | returns a three-turn fixture conversation |
| Asks answer | `asks?ask=ask-0000000000000001` (info) and `asks?ask=ask-0000000000000002` (approval) | records one answer, then the ask is Answered |

State lives only in the running instance. Restart it to start over from the
fixtures.

## Drive it with Playwright

The session image has the Playwright library and Chromium. With the instance
running:

```sh
NODE_PATH=/usr/local/lib/node_modules/@playwright/mcp/node_modules \
  nice -n 19 node web/portal/localtest/smoke.cjs http://127.0.0.1:8099
```

The smoke opens each page at 320, 390 and 1280 px. It checks the status, the
banner, a fixture row, that the page has no horizontal scroll, and that the
console has no errors. It prints `PASS` or `FAIL` per page and exits non-zero
on a failure.

The flow smoke drives send, launch, history and the Asks answer form at the
same widths. For each width it starts a fresh instance from the binary. It
double-clicks each submit, reloads after submitting, and repeats a launch
request:

```sh
GOTOOLCHAIN=local GOMAXPROCS=2 nice -n 19 \
  go build -tags portallocaltest -o ~/.local/bin/portal-localtest ./web/portal/localtest
NODE_PATH=/usr/local/lib/node_modules/@playwright/mcp/node_modules \
  nice -n 19 node web/portal/localtest/smoke-flows.cjs ~/.local/bin/portal-localtest
```

**Chromium sends `Origin: null` on the portal's forms.** Every portal page
sets `Referrer-Policy: no-referrer`. Under that policy Chromium sends
`Origin: null` on a same-origin form post, and the production origin check
refuses it. The local instance shows the same refusal. The flow smoke prints
it as a `FINDING` line per width. It then runs the flows with a harness-only
override that serves each page with `Referrer-Policy: same-origin`, so
Chromium sends the page origin. To post a form by hand in Chromium, apply the
same override; otherwise every send, launch, answer and logout is refused.
DESIGN-055 section 9.4 has the details. The production fix is tracked in
[OpenProject #480](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/480).

The Playwright MCP server works too: navigate to the printed URL and check
for `#portal-localtest-banner`. The page CSP blocks `fetch` from page scripts,
as in production, so open each page with a navigation.

## What it proves and what it does not

It proves the portal's UI and request handling on fixtures: routing, the
production gate, the session engine's cookie and CSRF checks, the group
check's code against a fixture UserInfo, the source clients and decoders,
the send, launch, history and Asks answer handlers with their replay and
idempotency rules, templates, CSS, CSP and responsive layout.

It does not prove:

- live Authentik login or the live group check (UserInfo);
- live group membership: the fixture UserInfo always admits the fixed
  principal;
- the Postgres session and receipt store;
- real delivery, launch, history or answers: those reach fixture routes only;
- the live configuration: Pages, Asks, Claims, send, launch and history are
  always on here, while the live portal enables each by its mounted config;
- anything about time: the clock is frozen at the fixture's `now`, so pages
  never age, go stale or expire;
- real operator, OpenProject or Alertmanager responses;
- TLS, the ingress, or the deployment.

The banner adds a row at the top of every page. Screenshots and layout or
overflow comparisons against the live portal or the static preview differ by
that row.

Authenticated checks against the live portal remain Tom's.

## How it stays out of production

The code compiles only with `-tags portallocaltest`. `make build-portal` and
the portal image never set that tag, and `make build-portal` clears any tag
set through `GOFLAGS`. Tests in the default build check that and check that
the production gate refuses requests shaped like the local instance's. The
instance opens no outbound connection; its only socket is the loopback
listener. Every upstream name is an in-memory fixture server, every other
destination is refused, and the instance refuses to start if any source's
client could dial anything else.

## Tests

```sh
make test-portal-localtest   # the tagged tests; make test runs them too
```
