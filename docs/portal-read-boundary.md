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
#177. Until that accepted interface is available, production attention uses `UnavailableReader`. The production Sessions and Tasks collections are independent and do not join a session to a work package. Sessions reads the operator portal-reader page `GET /v1/sessions`; the operator, not the query string, keeps namespace `dev-agents` and repository `dev-env`. Tasks reads only `GET https://openproject-web.frontend.svc.cluster.local:8443/api/v3/projects/3/work_packages` and keeps a row only when its project link is `/api/v3/projects/3`. A missing token says that view is not connected. A failed, redirected, or untrusted read says the view is unavailable and does not present an empty inventory. `secrets/OPERATOR_TOKEN` is re-read on each request and must be a current JWT whose only audience is `dev-env-operator`. `secrets/OPENPROJECT_TOKEN` is a separate bearer. Repositories is `customField3` free text; blank text stays Unknown. The deferred attention reader can still parse schema 2 bindings in tests. Those bindings are not a production startup requirement and are not the Tasks inventory. The #177 group reducer remains the offline checker in `scripts/trials/workflow_handoff`. OpenProject collection responses must be `application/hal+json`. Operator responses stay `application/json` and reject unknown fields. The preview command stays on its fixtures.
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

Open that local file to inspect the mobile page. The command reuses
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
