# Portal session boundary: M3 source contract

**M5B amendment:** [the runtime contract](portal-session-runtime.md) supersedes
M3's access-token discard/UserInfo prohibition and cookie-only absolute expiry
description for the reviewed M5B source. The exact session now retains an
encrypted short-lived access token for fresh UserInfo checks; both server and
cookie lifetimes are capped by its verified expiry. The original M3 account
below remains historical. Runtime wiring remains unavailable pending its gates.

Status: source implementation under Story178, following canonical
journals970/985/988/993. PR526 is merged at its reviewed target; this work is
excluded from that PR and needs its own exact-head review and CI. The same
implementation owner retains M2 findings and review/CI/merge pickup. No runtime
OIDC client, identity binding, secret, listener or ingress is created here.

## Dependency and placement

Use the existing Go module and `web/portal` read boundary. Candidate verifier:
[`github.com/coreos/go-oidc/v3` v3.21.0](https://github.com/coreos/go-oidc/releases/tag/v3.21.0),
verified through upstream release metadata and `go list -m` on 2026-10-23.
Its [module](https://github.com/coreos/go-oidc/blob/v3.21.0/go.mod) requires
Go1.25.0, go-jose/v4 v4.1.4 and oauth2 v0.36.0. The repository's Go1.27.2 and
existing oauth2 v0.37.0 satisfy those floors without a downgrade. Verify the
module checksum and exact source before adding the dependency. This is an
implementation choice within DESIGN-029, not a replacement JWT verifier.
The selected go-jose v4.1.4 includes the upstream fix for
[GHSA-78h2-9frx-2jm8](https://github.com/go-jose/go-jose/security/advisories/GHSA-78h2-9frx-2jm8).
This targeted primary-source check is not a complete vulnerability scan or
runtime security acceptance.

The upstream [verifier interface](https://github.com/coreos/go-oidc/blob/v3.21.0/oidc/verify.go)
supports an explicit issuer, client ID, algorithm allowlist, injected clock and
static or remote key set. Keep all issuer, audience, signature and expiry
checks enabled. Tests may use upstream `StaticKeySet` with generated synthetic
keys; token production in a fixture is not a new token-verification algorithm.
The application must additionally bind nonce, single-use login state, PKCE
transaction, subject presence and exact callback. Never call userinfo or
distributed-claim endpoints to establish portal authorization.
The wrapper must also enforce authorized-party binding for multiple audiences,
strict expiry at the configured clock and explicit future `iat`/`nbf` policy;
the library does not enforce all of those caller-specific bindings itself.
`NewRemoteTokenVerifier` accepts only the exact configured issuer JWKS endpoint
through `NewIssuerHTTPClient`; it constructs the maintained remote key set
without initiating discovery or key fetching. Fixture verification exercises
the real key-set path using a fake transport and generated public keys.

## Small typed boundaries

- Authentication config: exact HTTPS issuer, client ID, exact registered
  callback, fixed authorization/token/JWKS endpoints, `openid/profile`, explicit
  signing algorithms and positive session/transaction durations. The approved
  client/callback remain journal905; proposed session durations in DESIGN-029
  are not selected runtime policy. No user-controlled destination or return URL.
  The engine checks that code-client issuer/client/callback bindings match the
  verifier and validates the complete fixed authorization request before setting
  the browser transaction cookie or returning a redirect.
- OAuth exchange: one typed code exchange with S256 verifier through existing
  oauth2. A bounded configured transport must reject redirects, unapproved
  destinations and oversized responses. Discovery must not silently introduce
  another host. Constructors and tests perform no real network call.
- Pending login storage: server-side state, nonce and PKCE verifier bound to a
  cryptographically random browser transaction handle; atomic, expiring,
  single-use consumption prevents replay, including across replicas. No default
  in-memory production substitute; an unconfigured store is unavailable.
- Portal session storage: opaque random handle, issuer+immutable subject,
  creation/idle/absolute expiry and local revocation. Shared-store availability
  and current revocation are checked on every authentication. Provider tokens
  stay server-side and are not session-cookie payloads. The keeper retains all
  Claude/Codex refresh ownership. No provider refresh token is requested here.
- Source assembly: compose authentication with the existing independent current
  authorizer and read handler. All live metadata adapters remain unavailable.
  Add buffered panic containment with generic responses and no panic value,
  request headers, cookie, query or body in logs. Construct the handler/server
  for tests; never call a listen/start function in this source milestone.

Cookies are opaque, `__Host-`, Secure, HttpOnly, host-only, Path `/`, SameSite
Lax. Logout requires POST, exact configured Origin, authenticated current
session and a server-bound CSRF value. Local logout/revoke/expiry works without
calling the IdP; IdP SSO logout semantics remain separate and unverified. Reads
and polling do not update activity or extend idle expiry. Login or group claims
never become operator human/workbench identity or grant metadata authority.

## M3 lifetime semantics

There is no activity/touch operation in M3. The effective server-side session
lifetime is the configured `IdleTTL` measured from creation: it is a fixed,
non-sliding cap. Since construction requires `IdleTTL <= AbsoluteTTL`, the
later absolute-time check cannot independently decide an authentication result.
`AbsoluteTTL` currently determines the browser cookie's Max-Age/Expires; a
browser may retain that opaque handle after server authentication has expired.
Cookie presence never proves authentication or authority.

A reviewed activity path and corresponding tests are prerequisites for genuine
sliding idle behavior and an independently effective absolute lifetime. No such
endpoint or runtime policy is added here. The test named
`after-both-deadlines` proves rejection after both deadlines, and deliberately
does not claim to isolate the redundant absolute check.

## Finite negative test map

Use fake clock, fake exchange, atomic fake stores, upstream static signing
keys and `httptest.ResponseRecorder`; no provider/tracker/cluster/listener.
Test wrong issuer/audience/algorithm/signature, absent subject, nonce mismatch,
expired/future token, state mismatch/replay/transaction expiry, PKCE challenge
and verifier binding, exact callback/client/scopes, cookie flags and fixation,
local revoke/logout/idle/absolute expiry and no polling extension, CSRF and
Origin/method rejection, permission withdrawal through M2, unavailable stores
and dependencies, generic panic containment and secret-canary log exclusion.
Transport fixtures must reject cross-origin/metadata/loopback destinations,
redirects and excessive bodies without making a request to a live service.

Gate A still requires actual reviewed storage/configuration, deployed IdP and
proxy behavior, current human/resource binding, ingress isolation and the full
security test map. This document grants none of those operations. Separate
public ASVS acceptance remains unmet. Do not ask Tom to populate 1Password
until the reviewed provider exists; no blueprint is applied from this pass.

## Accepted M2 base and additive WIP

PR526 merged as `cb28f252b1504c69d9b2d369a7551c6cfbb12176` at
2026-10-23 16:54:41 UTC after exact-head CI and native PASS-with-minors. The
assigned branch merged that freshly fetched main without discarding local WIP
or forcing a branch. M3 also carries journal989's optional wording/future-time
and exact preview-count coverage corrections. The current reader still denies
the entire collection if any item is unauthorized; choosing a usable authorized
selection adapter remains a separate reviewed integration, never an inferred
role or broadened permission.

## Remaining Gate A evidence

The source milestone does not establish production-store bounds, rate limits,
atomicity across replicas, clock behavior or recovery. Actual IdP callback
parameters and refresh-token behavior must be verified: extra `iss` or
`session_state` currently fails closed, and unexpected refresh tokens are
rejected. Do not widen callback acceptance from assumptions. TLS termination
and backend bypass, DNS/egress restrictions, re-login/old-handle revocation and
a useful post-login landing route also remain deployment/integration evidence.
No live provider, client secret, binding or route follows from this source.
