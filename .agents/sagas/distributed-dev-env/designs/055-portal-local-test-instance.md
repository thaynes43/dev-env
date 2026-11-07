# DESIGN-055: A local test instance of the portal

- **Status:** Proposed, 2026-11-05. The read-only first implementation is
  merged in PR [#944](https://github.com/thaynes43/dev-env/pull/944)
  (`db04029e`). The write flows (section 9) are in PR
  [#946](https://github.com/thaynes43/dev-env/pull/946).
- **Origin:** Tom asked why agents need the live UI when they can host an
  identical test instance locally, with no auth, and drive it with headless
  Chrome or Playwright tests.
- **Tracking:** [Story OP476](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/476),
  "Agent-local portal UI/API development and testing entry".
- **Builds on:** [DESIGN-029](029-portal-foundations.md) (portal foundations),
  [DESIGN-045](045-portal-claims-view.md) (Claims), the preview harness in
  `web/portal/preview`.
- **How-to:** [docs/portal-local-test.md](../../../../docs/portal-local-test.md).

## 1. Goal and non-goals

An agent starts the portal on a loopback address and drives it like a
browser user. The instance serves the production routes, handlers, decoders,
templates, CSS and CSP. Data comes from fixtures, and one fixed test principal
is always signed in. Agents use it for development and UI acceptance.

Non-goals: live Authentik login, live group authorization, the Postgres store,
send, launch and history actions, and any deployment. The instance gives no
access to anything live.

## 2. What exists

- The live portal runs at `dev-env-v2.haynesops.com` behind Authentik. Agents
  hold no browser credential for it (DESIGN-029), so authenticated checks were
  Tom's.
- `web/portal/preview` writes static HTML from fixtures through the page
  builders. It skips the request path: the gate, the session engine, the
  assembly's routing, the source transports and the decoders' HTTP framing.
- `session.LoadProduction` builds the real handler. It requires the mounted
  production contract: pinned URLs, secrets, CA bundles, a TLS certificate and
  a reachable Postgres. It cannot run in a session pod.

## 3. Decision

Add a test boundary that reuses the production assembly and replaces only
what needs live identity or live services. The boundary is a build tag.

| Part | Production | Local test instance |
| --- | --- | --- |
| Gate | `productionGate` | `productionGate`, behind the local test gate (section 4) |
| Routes and handlers | `NewAssembly` | `NewAssembly`, unchanged |
| Session engine | `Engine` with Postgres store | `Engine` with an in-memory store holding one seeded session |
| Identity provider | Authentik code flow | none: login answers unavailable, no ID token verifies |
| Authorizer | `CurrentAuthorizer` (live UserInfo groups) | `CurrentAuthorizer`, asking a fixture UserInfo route that admits only the seeded session's fixture token (section 9) |
| Sessions, Tasks, Pages, Asks, Claims | pinned HTTPS clients to cluster services | the same clients, dialling in-memory fixture servers |
| Attention reader | `portal.UnavailableReader` | the same |
| Pages, Asks, Claims | each on only when its mounted config enables it | always on |
| Command receipts | `PostgresStore` | the in-memory store, following `PostgresStore`'s receipt rules (section 9) |
| Send, launch, history, Asks answers | opt-in per target, Postgres receipts, the live operator | on, against fixture operator routes that record every call (section 9) |
| Listener | TLS on `:8443` | plain HTTP on a loopback IP |

The tag is `portallocaltest`. Two files carry it:
`web/portal/session/localtest.go` and the command `web/portal/localtest`.
`make build-portal` and `images/portal/Dockerfile` never set it, so the
deployed binary contains none of this code. `make build-portal` passes an
empty `-tags=`, so a tag set through `GOFLAGS` cannot reach the portal build.
A production build with the tag set by mistake would still have no path to
it: `cmd/dev-env-portal` takes no flag and calls only `ServeProduction`.

```mermaid
flowchart LR
  B[Headless Chromium] -->|http://127.0.0.1:port| G[local test gate]
  G -->|TLS-marked request, test host, fixed session cookie| P[productionGate]
  P --> A[NewAssembly: real routes and handlers]
  A --> E[Engine + in-memory store]
  A --> S[Sessions / Tasks / Pages / Asks / Claims sources]
  S -->|pinned clients, dial| N[in-memory fixture net]
  N -->|unknown destination| X[refused]
```

## 4. The test gate

The gate stands in for the TLS listener and the browser login. For each
request it:

1. Refuses a client that is not loopback, a Host that is not the bound
   address (or `localhost` with its port), and an absolute-form URL. The Host
   check stops DNS rebinding from another site.
2. Rewrites the request as the production gate expects: Host
   `portal.localtest.invalid`, a TLS connection-state marker, and the fixed
   session cookie in place of any cookie the client sent.
3. Translates only an Origin equal to the request's own `http://` Host to
   the test origin. A cross-origin form post keeps its Origin and fails the production
   check.
4. Adds a banner as the first element of every HTML body, prefixes the page
   title with "Local test", and adds a first line to every plain-text answer.
   The banner uses the existing `.banner` class, so it needs no CSP change.

Then `productionGate` applies its own checks, including the forwarded-identity
refusal, and the assembly serves the page.

The fixed principal has its own issuer, `https://issuer.localtest.invalid/fixture-only/`,
and subject `local-test-reader`. The production engine refuses a session from
any issuer but Authentik's.

## 5. Fixtures and the network

- Sessions and Tasks use `scripts/trials/portal_product/sessions.json` and
  `tasks.json`, the envelopes the preview harness reads. The instance clock is
  the Sessions fixture's `now`, so pages render the same way each run.
- Pages, Asks and Claims use small synthetic records in `localtest.go`.
- Each upstream name the portal pins (operator, OpenProject, Alertmanager) is
  served by an in-memory HTTP server over `net.Pipe`. The operator and
  OpenProject servers present certificates from a fresh in-memory CA that the
  pinned clients trust. Every other destination, protocol or port is refused.
  Nothing leaves the process. `NewLocalTest` refuses to build the instance if
  any source's client has a proxy or a dialer other than the fixture net.
- The source clients read token files, as in production. The instance writes
  unsigned fixture tokens to a fresh private temporary directory and removes it
  on exit. Only the in-memory fixture servers accept them. They are not
  credentials.

## 6. Isolation proofs

Default build, so CI and the image path run them:

- `TestLocalTestExcludedFromDefaultBuild`: `localtest.go` is ignored without the
  tag, `web/portal/localtest` has no files without it, and no default-build
  file, the Dockerfile, the portal `main.go` or the portal build line in the
  Makefile names the tag.
- `TestPortalCommandRefusesLocalTestSelectors`: the production command refuses
  local-test flags and words before any work.
- `TestProductionRefusesLocalTestShapedRequests`: the production gate and
  engine refuse plain HTTP, loopback and test hosts, each forwarded-identity
  header, and a session minted for the local test issuer.

Tagged (`make test-portal-localtest`, which `make test` runs):

- Every page renders with the banner, its fixture rows and a production CSP
  header; Sessions and Tasks match production's policy exactly. Every
  upstream was reached through the fixture net.
- The gate refuses non-loopback clients, other hosts and ports, absolute-form
  URLs and forwarded identity.
- Login does not start. A cross-origin logout is refused, and a same-origin
  logout revokes the fixed session.
- The fixture network refuses every other destination within a 2s deadline.
- The command refuses non-loopback, wildcard, name and Unix-socket listen
  values.

## 7. Limits

The instance proves UI and handler behaviour on fixtures. It does not prove
live login, live group authorization, the Postgres store, real operator or
OpenProject responses, TLS, or the deployment. Unix sockets are refused:
Chromium cannot open them, and the gate binds the Host to an IP and port.

It is not a copy of the live configuration. Pages, Asks, Claims, send,
launch and history are always on here, while the live portal enables each by
its mounted config, so the live navigation and controls can differ. Send,
launch and Asks answers reach fixture routes only: no agent receives a
message, no session starts, no ask notifies anyone. Logging out revokes the
one fixed session; every page then
redirects to a login that is unavailable until the instance restarts.
The clock is frozen at the fixture's `now`, so pages never age or go stale.
The banner adds a row at the top, so screenshots and overflow comparisons
against the live portal differ by that row.

## 8. Follow-ups

The first increment (PR #944, OP476) was read-only: sessions, tasks, pages,
asks and claims. Send, launch, history and the Asks answer form came with
OpenProject
[#479](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/479)
(section 9).

- The historical Origin finding in section 9.4 was tracked in [OpenProject #480](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/480). Session routes now send `Referrer-Policy: same-origin` (PR #948). Attention remains `no-referrer`.
- An Attention metadata fixture, if Attention is wired in production.
- Selecting other fixture scenarios, for example the preview's product states.

## 9. Addendum: write flows (OP479, 2026-11-05)

### 9.1 What is wired

All four flows use the production handlers and templates. Only the far end
is a fixture.

| Flow | Production handler | Fixture end |
| --- | --- | --- |
| Send | `commandHandler`, every fixture session a target, the real `commandClient` | operator `POST /v1/portal/session-messages`: answers Delivered with a fixture pod UID |
| Launch | `launchHandler`, scope `dev-env` and `fixture-repo`, the real `launchClient` | operator `POST /v1/portal/sessions`: a new request ID makes one Pending fixture session that joins the Sessions list; the same request returns the same receipt; other content under that ID is a conflict |
| History | `conversationSource` for `fixture-coordinator` and `fixture-terminal` | operator `GET /v1/portal/sessions/<uid>/conversation`: a three-turn fixture projection |
| Asks answer | the Asks route's answer path, replay protection in command receipts | operator `POST /v1/portal/asks/<id>/answer`: one valid answer to an open ask, then Answered; conflict when closed, gone when expired |

The fixture operator records every write and every conversation read. Tests
read that record to prove that a flow reached the fixture exactly as often as
expected.

The in-memory store implements the command receipt contract with
`PostgresStore`'s rules: a reservation belongs to a current portal session
and expires with it, a replay with other content is refused, and the terminal
receipt is written once.

The authorizer is the production `CurrentAuthorizer`. It asks a fixture
UserInfo route at `issuer.localtest.invalid`, over the fixture net with the
fixture CA, which admits only the seeded session's fixture access token. The
conversation source needs this concrete authorizer, and the group check's
code now runs on fixture data.

### 9.2 Isolation

The start-up guard (`localTestDialersFixtureOnly`) also requires:

- the send, launch, authorizer and conversation clients to dial only the
  fixture net, with no proxy;
- the conversation source, the send handler and the launch handler to use
  the same authorizer object as the assembly.

Any new network-capable field must be added to that guard. The tests keep
the DNS interception from section 6, so every flow test fails if anything
resolves a name.

### 9.3 Tests

`TestLocalTestSendFlow`, `TestLocalTestLaunchFlow`, `TestLocalTestHistoryFlow`
and `TestLocalTestAsksAnswerFlow` each went RED before their flow was wired
and pass now. Each checks:

- exactly the expected fixture call;
- a double submit and a reload;
- a refused cross-origin or tampered post that never reaches the operator.

`web/portal/localtest/smoke-flows.cjs` drives the same flows in an
unmodified headless Chromium at 320, 390 and 1280 px. It starts a fresh
instance per width. Its default run is the acceptance run. Session routes
set `Referrer-Policy: same-origin`, so a refused POST is a failure rather
than the expected #480 result. The script also covers back/forward on a send
receipt, an abort before the POST is sent, a response dropped only after the
fixture receipt exists, wrong-target refusals, and fixture logout. A browser
run of those added checks is not claimed from this text. History is a GET
flow. A refused POST still reports `NOT ACCEPTED (#480)` and the run exits 1
(section 9.4).

### 9.4 Historical finding: Chromium sent `Origin: null` under `no-referrer`

On 2026-11-05, session pages set `Referrer-Policy: no-referrer` through
`authHeaders`. Under that policy Chromium sent `Origin: null` on a
same-origin form POST. This is specified behaviour: the [Fetch Standard, "append a request `Origin` header"](https://fetch.spec.whatwg.org/#append-a-request-origin-header) sets the
serialized origin to `null` for a request whose mode is not `cors`, whose
method is not GET or HEAD, and whose referrer policy is `no-referrer`. The
production origin checks accept only the configured origin, so they refused
these posts:

- send (`commands.go`);
- launch (`launch.go`);
- Asks answers (`asks.go`);
- logout (`engine.go`).

The local instance reproduced this, because it runs the same handlers.
Verified with Playwright's Chromium 1247 on 2026-11-05. Other engines were
not checked. PR #948 changed `authHeaders` to `same-origin`. Attention's
boundary still sets `no-referrer` and was not part of that change.

The current default smoke accepts a POST only when the unmodified instance
accepts it. A refused post of send, launch or Asks is still
`NOT ACCEPTED (#480)` and the run exits 1. The flag
`--diagnostic-referrer-same-origin` serves each document with
`Referrer-Policy: same-origin` through a harness-only override; see
[Referrer Policy, `same-origin`](https://w3c.github.io/webappsec-referrer-policy/#referrer-policy-same-origin). That run labels every line `DIAGNOSTIC`. A post refused even
under the override is printed `DIAGNOSTIC NOT ACCEPTED (#480)` and fails the
run. It exits 3 only when every diagnostic check holds, and it is never
acceptance evidence. The server policy is already `same-origin`; the flag
does not turn a diagnostic run into acceptance. The historical fix was a
production decision outside OP479, tracked in [OpenProject #480](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/480) ("Portal forms refused in
Chromium: Referrer-Policy no-referrer makes Origin null on same-origin
posts").
