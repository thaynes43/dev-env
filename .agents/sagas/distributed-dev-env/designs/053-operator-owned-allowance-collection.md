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

The scheduled helper's stderr is a separate best-effort diagnostic channel,
not a receipt, journal or provider-output channel. At most three newline-terminated
JSON frames use schema `dev-env.allowance-diagnostic.v1`, with exact keys `schema`,
`provider`, `stage`, `reason`, `clientVersion`. Providers, stages and reasons are
closed enums enforced by `DecodeAcquisitionDiagnostics`; only a strictly shaped
public version may accompany `version_unqualified`. Raw vendor stderr and error
strings remain discarded. Unknown keys/codes, malformed or incomplete frames,
extra lines or more than 2048 bytes discard the entire diagnostic channel without
logging its bytes. Overflow is consumed and discarded, preserving the existing
machine-output/transport result. The internal command adds no plain-text error
line on failure. A nonzero exit still refuses acquisition; a decoded diagnostic
cannot clear cleanup uncertainty or promote a failed capture.

The operator logs only validated diagnostic fields and the existing cycle number.
There is no additional vendor invocation, retry or durable state. Older operators
discard the side channel; older helpers may emit no frames (or their old generic
error line, which newer operators discard). Stdout request/receipt and durable
journal schemas remain unchanged. This source-compatible diagnostic change grants
no version enrollment, account qualification, login, permission or runtime window.

An operator restart resumes the next scheduled acquisition from durable state;
it does not resume a provider/native task. An ambiguous outstanding operation is
never replayed based on elapsed time. A status-only check under the same private
lock recovers the exact completed receipt, or atomically fences a never-started
nonce before permitting a later cycle. A durable uncertain marker or unproved
process-group cleanup blocks further acquisition; neither timeout nor a new
nonce clears it. Each reserved
cycle consumes a monotonic sequence even if acquisition fails. A failed publish
is confirmed against the same report revision, never recollected as a retry.

## Compatible upgrades and explicit window changes

The stable fingerprint includes host/slot identity, cadence and independently
selected trust; the separately enforced acquisition deadline is excluded. An
explicitly authorized deadline change reloads the same lifecycle without
resetting sequence, last-good clocks or outstanding-work fences. Source support
for this change grants no window renewal or credential/policy extension. Other
identity/trust changes have no automatic migration in this version.

The operator journal records each intent's original helper digest. A request
separates the executing binary digest from that original intent digest; capture
requires equality. A compatible new helper may reconcile an old exact nonce by
status only, preserving the old receipt bytes/digest. Its status-miss tombstone
also names the old intent, blocking late original dispatch. Only a terminal
cleanup-proven receipt permits a later monotonic cycle with the new helper.
Uncertain work, corrupt persistence or an unknown protocol is never cleared by
an image or deadline change. No journal/receipt deletion is a supported upgrade.

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

## One-time replacement handoff (2026-11-03)

*Execution route.* The standalone helper needs a caller that can update the slot. The elected operator already holds that authority, so source also adds a default-off `--allowance-handoff-file` route with an in-slot full backup; see [the verification record](../verification/collector-incarnation-handoff-2026-11-03.md#operator-native-execution-route-source-2026-11-03). It adds no permission and applies nothing by itself.

The human-directed parent coordinator session may explicitly invoke the
maintenance helper to prepare the particular cycle-56 host replacement and
deadline 2026-11-04T15:33:23Z. This is not an automatic host adoption or a
general identity migration. Producer incarnation
remains the same caller-selected logical stream: trust, account and workspace
bindings, captures, sequences and historical times remain unchanged. Only the
host Pod UID, generation, image and the approved deadline may change.

The human-directed parent coordinator session, not the operator binary,
independently authenticates the exact named ConfigMap UID and original bytes, old fingerprint, idle cycle, nonce, report,
captures, known helper and archived old-Pod eviction evidence, and obtains
approval of the exact prepared hash. A supplied snapshot or hash is not proof of
provenance or human approval. A trusted idle journal from the existing Runner
is transitive terminal cleanup evidence because that Runner commits idle only
after a validated cleanup-proven terminal receipt and atomic report/journal
update. No old runtime ledger is fabricated or bypassed.

Preparation uses the existing config and journal parsers and binds the
original and replacement configurations, evidence and proposed exact bytes.
Apply uses the specifically selected uncached Kubernetes client, verifies the
fresh replacement session, Pod, image, service account, owner references,
heartbeat and bound HOME, and fences the named mutable slot and original
resource version. It fsyncs a private full old-config, journal and report
backup before rechecking those guards and changing only the journal fingerprint
with a single CAS. Exact readback resolves an ambiguous acknowledgement and an
identical retry requires the same backup. Contradictory state, changed trust or
identity, non-idle work, expiry or replay refuses. Old and new runners with a
mismatched fingerprint refuse before provider operations. Runtime invocation
requires separately qualified actual evidence, exact approval and review; this
source design and synthetic tests confer none of those.
