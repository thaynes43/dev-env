# Coordination archive ingestion — 2026-10-14

This is the first bounded source slice of DESIGN-024 R2, under #175. The shelf
needs the full coordination object before a future retention delete. Decoding
it into current CRD structs would discard newer fields; trusting a manifest
without checking the object could archive a different project, UID or revision.

The protocol layer validates the manifest and reads one bounded JSON object,
keeping numbers as written and all unknown fields. It produces DESIGN-024's
canonical bytes only when kind, namespace, name, UID, resourceVersion, project,
recipient/message identities where applicable, byte count and SHA256 match.
Invalid input yields `invalid` without returning object bytes.

Paths: `internal/agentd/protocol/coordarchive.go` and its focused tests. The
contract is DESIGN-024 section 4, not a new architecture or owner ruling. This
unit is limited to ingestion. Atomic shelf publication, indexes/events,
put/mark/lookup/get/list commands, mounting, measurements, controller retention
and read fallback remain R2–R7 work. There is no filesystem or API authority in
the ingestion helper, and no flag, CLI route, deletion or deployment is added.

The required source check is the protocol package suite, once under the shared
test lock, `nice -n 19`, `GOMAXPROCS=2`, `-p 1` and `-count 1`. Fixtures cover all
four kinds, preserved unknown fields and number encodings, identity/digest
substitution, malformed input, canonical and incoming byte limits and bounded
reader consumption. No cluster/provider tests, CPU burners, stress tools or
wide/looped test runs are authorized. Native independent review and required
source CI remain separate from installed acceptance.

The full protocol suite passed in 0.055 seconds using the cached, repo-pinned
Go 1.27.2 with the bounds above. The first invocation found Go absent from
`PATH`; no tool was installed. The first compiled run caught non-UUID synthetic
fixture identifiers, which were replaced with valid synthetic Kubernetes UIDs.
Substitution fixtures now use different valid UIDs, so they exercise object
binding rather than only format rejection. No live API call was made.

The separate Mac inquiry's 20 saved files were verified locally, including
every response/stream digest and launch/body/argv binding. All six gates passed
in the recorded 04:57:24.740398–04:57:27.031887 UTC window (2.291489 seconds),
with zero stderr/truncation and all cleanup reported proved. Source provenance
SHA256 `c67132403e4d1359fd7092e533e41f7cba33a2a2ed50f20757babd931c1b45d1`.
This accepts the named ServiceAccount and the five exact authorization answers
in that window. It proves neither a permanent personal identity nor admission
delivery and grants no impersonation, dry-run or repeat inquiry.

The next source milestone is atomic immutable shelf put/get over these verified
bytes. #175's real consumers, contention and durable inbox/ACK remain unaccepted;
this source slice does not establish a pilot or full-v1 retirement. The separate
six-query authority attempt was accepted and consumed. Live guard requests,
impersonation, dry-runs, deployment, archive activation and cutover remain held.
