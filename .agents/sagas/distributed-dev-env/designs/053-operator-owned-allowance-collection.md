# DESIGN-053: Operator-owned model-free allowance collection

Status: source implementation; runtime activation remains held. Continues OP192/187 and
DESIGN-052. Finite actual captures and offline projection are not live acceptance.

The existing operator leader owns the schedule and report persistence. It runs
fixed bounded normal CLI readers in one explicitly selected, already owned,
long-lived remote session. A coding task's UID or timeout is not the service's
lifetime. No new AI coordinator, inference task, provider principal, refresh-token
owner, credential copy, session resume or user terminal is introduced.

The selected remote session must already have the normal Cursor client context
and keeper-managed access-only Claude/Codex projections. The keeper remains the
sole rotating-token owner. Normal client authentication/cache behavior still
requires qualification and explicit recurring-use authorization. No reader can
answer a login, credential, tool, terms or permission prompt.

## Execution and ownership

Default-off private configuration selects exact session name/UID/generation,
Pod UID, ServiceAccount, HOME, source versions, independently qualified provider
accounts, expiry, cadence and backoff. The operator directly rechecks the live
session and pod before and after each finite remote operation. Deleting, terminal,
stale, replaced or mismatched owners refuse. A new pod is not automatically
adopted. User Remote Control processes and conversations receive no input/restart.

The operator image includes its source-matched agent-run helper. A fixed bounded
bootstrap streams that binary to an anonymous memory file in the selected remote
process, verifies size/digest, and executes the model-free collection entrypoint.
It uses an exclusive private lock for the entire acquisition, including cleanup,
so an old leader's bounded remote operation cannot overlap a replacement leader.
There is no persistent quota-reader process or untracked installed provider.
The helper creates and removes only its own fresh empty Cursor workspace and PTY.
Raw terminal/SDK output and credentials never enter logs or stored reports.

An operator restart resumes the next scheduled acquisition from durable state;
it does not resume a provider/native task. An ambiguous outstanding operation is
never replayed based on elapsed time. A status-only check under the same private
lock recovers the exact completed receipt, or atomically fences a never-started
nonce before permitting a later cycle. A durable uncertain marker or unproved
process-group cleanup blocks further acquisition; neither timeout nor a new
nonce clears it. Each reserved
cycle consumes a monotonic sequence even if acquisition fails. A failed publish
is confirmed against the same report revision, never recollected as a retry.

## Persistence and API

The existing named allowance ConfigMap holds the bounded report and separate
collector intent/sequence metadata. Each conditional mutation uses its current
resource version. A corrupt, foreign or contradictory state refuses without
overwriting it. Last-good observations retain original capture/receipt times
through errors and restarts; current collection recency ages normally. No provider
measurement clock is inferred from a scheduler tick, served time or file mtime.

Explicit operator-publisher mode allows only the internal validated producer and
GET readers. The external aggregate PUT remains refused in that mode. Existing
SessionUID-bound PUT behavior retains its original checks in session-publisher
mode. Source configuration does not grant Kubernetes access or activate a route.

The operator already has session-pod exec permission. Its present Roles do not
grant the named report slot's get/update rights. Any added right must name only
that ConfigMap and be included in the exact activation request. Actual account
trust is private metadata, delivered through a qualified private configuration
mount; no actual account hash goes in public GitOps. The portal independently
loads its matching private trust. Existing projection and service identities are
reused; new Secret API rights or provider credential access are not implied.

## Qualification and acceptance

Use synthetic protocol/process fixtures, fake clocks and capped serial tests.
Verify missing/default configuration performs no IO, acquisition or publication;
wrong UID/pod/generation/version/account, unknown dialog, concurrent lock, expired
configuration, stale heartbeat, corrupt state and ambiguous publication all stop
or withhold appropriately. Prove restart preserves sequence and original clocks.

Runtime acceptance requires signed exact artifacts, reviewed configuration and
minimum permissions, repeated actual acquisitions, operator restart/recovery,
native agent GET and UI refresh/stale/error behavior. A successful API response,
PodReady, source merge or one finite read does not satisfy those criteria.
Disable the collector/reader configuration to roll back; preserve the last report
as historical evidence and retain original credential/grant expiries.
