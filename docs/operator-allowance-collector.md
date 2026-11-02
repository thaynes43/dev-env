# Operator-owned allowance collection

This default-off source path gives the existing elected operator leader a
schedule for finite supported Codex, Claude and Cursor reads. It is separate from
the coding task's lifetime. Source, signed publication, installation and actual
recurring API/agent/UI acceptance are distinct states. See
[DESIGN-053](../.agents/sagas/distributed-dev-env/designs/053-operator-owned-allowance-collection.md).

The operator dispatches fixed model-free readers in one independently qualified
existing remote session. It creates no AI worker, session, provider login,
refresh-token owner or credential copy. It sends no input to the user's existing
Remote Control process. Normal vendor authentication/cache behavior must be
qualified before recurring use; a successful manual read alone is insufficient.

## Configuration

All of these operator flags are required together:

```text
--enable-aggregate-allowance-reports
--aggregate-allowance-config-file=/etc/dev-env-operator/allowances/selection.json
--enable-operator-allowance-collector
--allowance-collector-config-file=/etc/dev-env-operator/allowances/collector.json
```

Leader election must remain enabled. Inline aggregate configuration is not
accepted for operator collection. Both files must be bounded regular private
files; normal read-only projected-volume symlinks and group-readable mode 0440
are supported. Startup reads these files once. Replacing a file does not prove
adoption. Configuration delivery and reload belong to the reviewed deployment.

The aggregate selection retains the existing fixed slot, publisher host UID and
ServiceAccount, independently trusted three-provider contexts/registry, exact
native reader UIDs and portal identity. Actual account hashes remain private
personal metadata. They must not appear in public manifests, arguments or logs.
The portal independently loads matching private trust; report payloads never
establish their own trust.

The separate collector JSON has exactly these fields:

| Field | Requirement |
| --- | --- |
| `version` | `1` |
| `host` | Exact existing `name`, `sessionUID`, `generation`, `podUID`, `serviceAccount`, digest-pinned `image` |
| `slotUID` | Actual UID of the precreated fixed ConfigMap |
| `validUntil` | Explicit RFC3339 deadline, no more than seven days ahead at startup |
| `cadenceSeconds` | 300–1800 |
| `backoffSeconds` | At least cadence, at most 3600 |

The host must be a running/idle, private, non-restored Claude remote session with
no task limits, the selected workbench identity and a nonfuture heartbeat no more
than two minutes old. Its exact agent image and existing read-only
`dev-env-claude-live` / `dev-env-codex-live` access projections must match.
The operator rechecks live session/pod ownership before and after dispatch.
A replacement host pod, host image, generation, account or selected trust is not
silently adopted. The private state fingerprint binds these identities and
scheduling/trust settings. Changing them after collection is unsupported by this
version; preserve the journal and receipt rather than deleting them to bypass a
fence. The separately enforced `validUntil` is excluded from that fingerprint.
An explicitly approved window change can reload the same identity without
resetting sequence or acquiring an unresolved intent again. It does not extend
credentials, grants or policies, and source support does not authorize renewal.
Expiry stops collection without host/slot IO, including after an operator
restart. A well-formed expired collector configuration leaves the core API and
historical GET service able to start; it does not renew the collection window.

## Readers and process ownership

The operator image carries its source-matched `agent-run` helper. Fixed Python
bootstrap code verifies the streamed binary's size/digest, seals an anonymous
memory file and executes only `internal-allowance-capture`. No persistent binary
or provider is installed in the host. Raw provider streams and stderr never
enter the report or operator logs.

The helper accepts only the qualified versions: Codex 0.160.1, Claude Code
2.1.296 and Cursor 2026.10.22-27dcf5f. Other versions refuse. Codex uses its
official account/quota RPC with non-forced matching account barriers. Claude
uses normal `auth status` before/after the isolated local `/usage` builtin;
the result must explicitly report success with zero turns, tokens and cost.
The static Claude OAuth environment token is excluded from that child, matching
the existing Remote Control launch's keeper-owned login path.

Cursor uses normal `about --format json` before/after its own fresh empty
workspace and PTY. Only the qualified current visible terminal states permit
workspace trust, typing `/usage`, or a single Enter after its exact autocomplete
entry. Unknown authentication/permission dialogs refuse. A transient Loading
screen is optional and proves no provider refresh. Each command inherits the
intent deadline; all owned process groups are terminated and the direct child
reaped before a completion receipt is accepted.

The Cursor percentage remains rounded display evidence. Its billing month/day
label does not become a quota-reset instant or countdown. Local successful
collection recency is separate from the unknown provider measurement clock;
strict current-capacity fields remain unknown. Failed reads never become zero.

## Helper upgrades and window changes

The durable operator intent records the helper digest that reserved its cycle.
Every request separately names the executing helper digest and the original
intent helper digest. Capture requires both to match. After an ordinary reviewed
operator image upgrade, the new helper can perform status-only reconciliation
for the exact old nonce and return the old receipt unchanged, including its old
helper digest and original times. A status miss writes a tombstone for that old
intent, so late delivery of the original helper cannot acquire it.

Only a clean `complete` or `never_started` receipt permits a higher monotonic
cycle with the new helper. An uncertain receipt remains blocked across both
helper upgrades and approved window changes. Unknown protocol versions, changed
identity/trust, corrupt files or interrupted persistence remain refusals; this is
not a general state migration or reset path. After a reviewed window change or
compatible helper upgrade, verify the same slot UID, sequence and original
receipts before accepting the next cycle. Never remove receipt or journal files
as an upgrade or renewal procedure.

## Restart, publication and rollback

The precreated `dev-env-system/dev-env-provider-allowances` ConfigMap contains
`report.json` and bounded `collector.json` state. The operator requires only
named-object `get`/`update` in addition to its existing host exec permission.
It does not create/delete the object or need application Secret API rights.
Configuration is not permission to add a Role or start recurring reads.

Before dispatch, a resource-version CAS reserves a unique nonce and monotonic
cycle. The selected host holds an exclusive private runtime lock and fsyncs an
uncertain marker before any vendor command. It commits its sanitized completion
receipt before stdout. A replacement leader reconciles that exact nonce without
recollecting. A status miss first commits a never-started tombstone, preventing a
delayed old transport from executing later. Busy waits; uncertain cleanup or
corrupt/foreign state blocks acquisition. Time alone cannot clear that state.

The existing report store validates provenance, original clocks and watermarks.
Its single CAS writes the report and next journal together; ambiguous writes
are read back, not recollected. Retention is the last two original samples per
provider. Failed checks retain last-good observations and their original times.
External aggregate PUT is refused while operator producer mode is selected;
existing native and portal GET checks remain in effect.

Rollback disables the collector and optional aggregate/portal configuration
before removing their private mounts. Preserve the ConfigMap and host receipt,
latest UI version, user sessions, keeper ownership and original access expiries.
Do not clear an uncertain marker, reset a sequence, force auth refresh or replay
a completed native task. Actual repeated three-provider acquisitions, restart
recovery, native GET and UI refresh/stale/error evidence remain runtime gates.
