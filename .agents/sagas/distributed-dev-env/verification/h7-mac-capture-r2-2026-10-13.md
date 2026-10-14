# H7 existing-Mac metadata capture accepted, 2026-10-13

**Root accepts the bounded installed-configuration and pre-mount metadata
window only.** The corrected checker returned `Ready`. Independent saved-
evidence review found no issues. No second capture or extra request was made
to ratify it. D-92's [shared-history requirement](../requirements/2026-10-13-shared-agent-history.md)
is a separate owner ruling; the capture neither grants read access nor tests it.

The window was October 13, 20:18:15.349379–20:18:25.920316 EDT, corresponding
to October 14, 00:18:15.349379–00:18:25.920316 UTC. The existing Mac launched
the reviewed invocation once, using isolated Python 3.9.6. Both helpers closed
and exited. The saved report and source-bound accounting establish:

| Observation | Result |
|---|---|
| Actual HTTP requests | 26 GETs, all 200, across exactly 20 permitted paths |
| Response bytes | 436,406 |
| Complete typed workload lists | Nine, each complete in one page |
| Objects | 13: five Pods, one Deployment, three ReplicaSets, four Jobs |
| Protected objects | Five, matching expected UID, source, parent and template facts |
| Named rereads | Stable identities and resource versions |
| Archive references | Zero under the reviewed scan predicates in this inventory window |
| PV requests, mutations, retry | Zero |

The six configuration objects match the frozen bundle; generated hooks,
policy conditions and readiness match the reviewed configured profile.
The checker source is `1142e2c3bbec0a570ed99950163c40a4d406ab9d` (#267),
distinct from the expected installed policy source `650f3a04` (haynes-ops
#3902). All 15 source-file hashes, the canonical expected bundle and both
used Mac helper binaries match the reviewed artifact, ID `11854095679`,
from run `38475572846`, job `115565291214`.

## Limits and preserved state

These are per-kind snapshots, not a globally atomic inventory. Raw workload
lists were intentionally not retained. The zero-reference and complete-
inventory properties rely on the pinned checker's reviewed predicates plus
the complete-list and transport receipts; the independent audit cannot
recompute the full inventory digest from the five retained protected rows.
Later trusted writes remain possible.

The configured Kyverno 1.19.1 tag and readiness do not attest the immutable
running engine or policy generation (`generationAttested: false`). Actual
admission, `resource.Get`, controller authenticity, CONNECT/restart behavior
and the owner's personal authenticated path remain separate. This is not H8,
F4, either cold measurement, persistence/restore, a working archived read,
mount/flag activation, HOME deletion, cutover or MVP acceptance.

The failed reader remains held. Earlier failed Jobs, two older source-mismatch
failures, cumulative Q-23 budgets and original canaries remain preserved.
Ready does not reset them. The separately accepted Q-37 Retain attempt remains
consumed. V1 and all runtime configuration are unchanged by this acceptance.

## Evidence

Private directory: `/home/dev/work/orders/dev-env-h7-existing-mac-evidence-1013/`.
The capture subdirectory is mode 0700 and its six files are mode 0600.

| File | SHA-256 |
|---|---|
| `capture-r2/mac-audit-r2.json` (32,244 bytes) | `e61e51d9dee7f825e4472dcd1afb208c570caad0efb953e28b19d80487e83d24` |
| `capture-r2/mac-capture-provenance-r2.json` (13,359 bytes) | `eddd5f84a750db75f18e43e9ed1c29e514ed1f8d83a0a0a4e78d9f32158698b0` |
| `capture-r2/bundle-manifest-r2.json` | `1429f06bf1305bba5baf1254cfa509f75a974c019928557f48b93a13cb20d77e` |
| `independent-capture-r2-evidence-audit-r1.json` | `91abdbfd18ca4122fca553c8bb3a079d334cb9e7dbca4589631eb782b5b3c389` |
| `capture-r2-root-acceptance-r1.json` | `f942f993cc98312ce39bb497ed94872e558d9248a780bc016c6ca376e21daa0b` |

The saved reports contain sanitized metadata/accounting, not conversation
content. No credential, raw API body or private personal username is published.
