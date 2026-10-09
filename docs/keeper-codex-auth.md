# Keeper-owned Codex authentication

**Status: implementation in progress; disabled until source, deployment and
real-client acceptance pass.** This is the authentication path for the first
v2 owner test with two distinct Codex computer links and shared project files.
It preserves each host's private enrollment and makes the keeper the sole owner
of the rotating refresh token.

```mermaid
flowchart LR
    Owner[Tom signs in once] --> Helper[Bounded keeper login helper]
    Helper -->|fresh login through private staging| Keeper[Keeper adopts and refreshes]
    Keeper --> Private[(Private durable auth journal)]
    Keeper -->|access tokens and generation only| Live[(Access-only projection)]
    Live --> A[Host A private auth.json]
    Live --> B[Host B private auth.json]
    A --> LinkA[Codex computer link A]
    B --> LinkB[Codex computer link B]
```

## First sign-in

The initial test uses the pinned Codex 0.160.1 native device-login ceremony in a
bounded helper inside the keeper trust boundary. Each attempt has a fresh
private `CODEX_HOME` and a fifteen-minute deadline. The helper shares only a
dedicated temporary staging area with the keeper; it receives no CA, GitHub App
credential, remote-host enrollment or live auth home. It runs no agent or daemon.

Tom receives the sign-in link/code through an authenticated control response and
the native actionable question tool. Challenge and token material never enter
pod logs, Kubernetes events/status, public issues, git or a handoff. If device
login is unavailable, verify that failure before asking Tom for the required
account setting or supported alternate ceremony.

The keeper accepts only the completed attempt bound to the current keeper and
attempt identity, validates it, persists it privately and removes staging. It
never imports v1's auth or copies a live remote host's refresh token. Fresh host
pairing is a later, separate action: each private enrollment gets its own
computer link. This initial CLI ceremony does not replace the recorded target
of authenticated console renewal.

Starting a replacement login pauses refresh while preserving the prior private
credential. Cancelling may restore a previously confirmed usable login through
a fenced, confirmed save; it cannot revive an ambiguous refresh. A different
account is refused before adoption for this first test.

The reservation lasts fifteen minutes and is busy for every leader while live.
After expiry, a fenced leader may restore the retained usable credential only
when the durable record explicitly permits it. A new login first resolves an
expired reservation. If a restore save is not acknowledged, halt the worker and
attempt a fenced `NeedsLogin` tombstone; never publish or resume from an
unconfirmed save. Storage failure can prevent that tombstone from persisting,
so the record remains a visible unresolved condition rather than proof that
token material was deleted.

## Rotation and delivery

The keeper privately stores one bounded versioned record in the named
`dev-env-keeper-codex-auth` Secret. It contains the login, ownership generation,
expiry, attempt identity and durable state. Only the keeper may read or patch
that named record; session/host pods receive no permission or mount for it.

Refresh uses the exact pinned upstream native OAuth contract. Before dispatch,
persist a `RefreshIntent` and check uncached own-Lease identity with enough
remaining lifetime for the complete HTTP deadline, durable save and safety
margin. Cancel on leadership loss. There are no redirected requests or automatic
retries after possible dispatch. A successful response must preserve account
identity and supply valid replacement material; CAS-persist the replacement
before publishing access tokens. If publication fails, retry publication only.

Replacement material is durably confirmed before a separate save makes it
`Ready`. An unfinished intent cannot dispatch again. Only the same attempt that
can prove it never dispatched may restore the known-unused token when its
pre-dispatch time budget expires. The public projection also uses a bounded
conditional write: an old publisher cannot replace a newer generation.

A transport, response or persistence ambiguity remains durable and requires a
fresh login. A new leader cannot replay a possibly consumed refresh token.
This worker does not use the generic retrying credential loop. Disabled mode
performs no login, journal read, refresh or publication.

The access-only `dev-env-codex-live` projection contains `id_token`,
`access_token`, `account_id`, `exp`, generation and refresh metadata. It contains
no refresh token. agentd validates one complete projection generation and
atomically writes a mode-0600 private `auth.json`, with ChatGPT auth mode and an
empty refresh token, before launch. It follows newer generations while the
daemon runs; token updates do not restart it or replace enrollment state.

Schedule refresh from the observed expiry with a conservative lead time beyond
projection/reload latency and the client's five-minute refresh window. The
ten-day lifetime observed in S-3 is evidence from that login, not a guaranteed
lifetime for future logins. Expired/stale/unavailable authentication blocks new
launches and is reported as a concrete renewal need.

For immediate acceptance, an authenticated `refresh-once` control action uses
the same serialized, fenced and durable refresh path, bypassing only its due-time
check. The caller supplies the observed positive generation. A changed generation,
live login reservation, unresolved intent or unavailable leadership budget
refuses the action. Repeating that generation cannot dispatch another refresh
after it advances. One action sends at most one POST; lost responses do not
authorize replay. This verifies propagation to running clients without changing
token expiry or waiting for the normal early-refresh schedule.

Codex readiness is reported separately from the keeper's GitHub service health.
The first interactive login must not make the keeper's deployment wait forever
to become ready. Missing Codex auth still blocks new Codex launches.

## What must pass before Tom tests it

- Source tests cover bounded parsing/redaction, fresh attempt adoption, journal
  CAS, durable intent/recovery, Lease budget/loss, replacement-before-publication
  ordering and ambiguous-response refusal.
- Synthetic agentd tests prove access-only generation validation, atomic private
  replacement and update without touching provider state or restarting a daemon.
- Signed images and exact named-secret RBAC are deployed through reviewed GitOps;
  keeper helper mounts and CPU limits are verified at admission.
- Tom completes the fresh keeper-owned sign-in. Actual pinned Codex launches and
  two separately paired hosts work on access-only files. One refresh reaches both
  running hosts without another refresher or daemon restart.
- Host replacement preserves its own enrollment; the other host remains usable.
  Auth history, raw tokens and private account identifiers remain private.

The earlier S-3 unpaired app-server result supports this implementation, but does
not substitute for two paired-host, refresh and replacement acceptance. Shared
storage, project/rule loading, writer ownership and managed task resume remain
independent gates in [plan 11](../.agents/sagas/distributed-dev-env/backlog/11-project-workspaces.md).

The pinned implementation references are
[device login](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/login/src/device_code_auth.rs),
[OAuth transport](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/login/src/oauth/client.rs)
and [auth manager](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/login/src/auth/manager.rs).
