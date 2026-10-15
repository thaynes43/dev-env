# Archived inbox acknowledgement GET — 2026-10-15

Story C7-R4-INBOX-ACK-GET-1015 (Chapter 7 source story #319), under epic #175
and backlog 11, retention read fallback (R4), implements the GET acknowledgement portion of
[DESIGN-024 section 6](../designs/024-coordination-record-retention.md#6-reads-fall-back-to-the-archive).
It depends on accepted PR316/main
`51f8e3912ecef646980e429557a2eb7ce875227d`, not held #301/R3 or an unaccepted
branch. The original five-path claim has one explicit supplemental test path,
`coordination_inbox_archive_get_test.go`, needed to replace the previous slice’s
temporary GET-ack prohibition. Original claim seals and the supplemental record
remain preserved.

## Behavior

With archive reads enabled, GET `/v1/inbox/{message}/ack` first authorizes its
live or verified archived message, then prefers a valid live acknowledgement.
Only an actual live acknowledgement NotFound asks the existing typed archive
Get for its deterministic name, without UID/revision/hash pins. The accepted
typed default selection provides that UID’s first archive time. The full
canonical object, digest and manifest are checked, then project, message name
and UID, full recipient namespace/name/UID and stored actor attribution are
validated against the authorized message. The response uses the existing
projection with additive `archived` and `archivedAt` fields only for an archived
acknowledgement. An archived message with a live ack returns a live ack view.

Original recipient Session/controlled live Pod or coordinator read scope is
checked before and after archive I/O. A deleting original Session remains
permitted under the existing inbox contract; an absent/replaced recipient or
changed executor is refused. No sender Session join or current-actor rewrite
is introduced. Default-off behavior, live errors, foreign live records and
POST/publish writes do not acquire a new archive path. POST ack retry is a
separate remaining slice, not implemented here. Typed NotFound maps to 404;
other archive failures, bad objects, no shelf/slot and cancellation map to 503.
All acquired slots are released.

## Evidence and limitations

The first RED attempt failed in the fixture because its ack manifest omitted
`messageUID`; it is preserved as fixture failure1. After correcting the fixture,
`red2-go-test.out` captured the actual accepted production source returning 404
with zero archive calls despite a protocol-verified archive ack. The test used
raw HTTP JSON and compiled before the new response fields existed. This is
accepted-base evidence, not a disabled-flag mutation. Its original source
hashes and direct plaintext output remain saved.

The worker’s initial two-method capture was incomplete acceptance evidence.
Its bounded test closure subsequently passed **4 methods / 19 nodes** in direct
JSON while production remained frozen at `ed7524ed926d151f2dcb3167deaeb140280a9d64`.
Native review found no production defect at that frozen source, but identified
the old message test’s superseded GET-ack 404/noarchive assertion. The coordinator
removed that obsolete GET block and renamed the function, retaining its
POST-ack 404/noarchive and publish 201/noarchive assertions unchanged. New tests
positively cover archived-message/live-ack and archived-message/archived-ack.

Coordinator closure adds independent full-field actor/recipient/message oracles,
coordinator and recipient pre/post fences, absent sender/deleting original
Session positives, foreign-live refusal, and semantic mutations whose complete
canonical digest envelopes still pass protocol verification. These cover full
recipient identity, message name/UID, project, namespace, actor validity, creation
and deletion state, plus a non-UTC receipt. Earlier tests cover corrupt envelopes,
typed errors, missing/busy slots, cancellation and additive HTTP JSON decoding.
No existing separate typed acknowledgement consumer was found; the HTTP response
is decoded into the existing additive API type. These are finite offline fixtures,
not an installed shelf or complete real archive-to-HTTP integration proof.

The first combined coordinator run exposed a prior message-test oracle defect:
equal `ReportedAt` instants carried different Local/UTC representations. The
coordinator normalizes copies of that timestamp to UTC before the unchanged
full attribution/text comparison. Nil presence and every other provenance field
remain asserted; no production code or ownership assertion was weakened. That
failed capture remains preserved as root fixture failure1.

After those real test changes, the final combined selection passed
**15 methods / 94 nodes** in one direct JSON capture. This is one run; earlier
worker captures are separate evidence and are not added to its count.

```sh
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -p=1 -json ./internal/apiserver -run '^(TestInboxArchiveAckGet|TestInboxArchiveGet)' -count=1 -timeout=90s
```

Use cached Go 1.27.2 and existing dependencies, with downloads off. The direct
command used the cached executable’s full path; no broad agentd, held TTY,
envtest, load loop or image build ran locally. Direct raw JSON SHA-256:
`bbdda7892f8ffe0d5adc60908b7c381d06cd3813edf3a9608cf646806a45736c`.

| Tested source | SHA-256 |
| --- | --- |
| `internal/apiserver/inbox.go` | `cef73a337dade173a31e2e7283ce6d866acfc942c25bc8bc6bffe73f0d139d79` |
| `internal/apiserver/apiv1/inbox.go` | `8960f8c464ee8043639a44cd798dd308dbef8b30dda6ab0e616edc8951b83c9a` |
| `internal/apiserver/coordination_inbox_archive_ack_get.go` | `8b776a1507379b99ca76814d2d593aedd09c47444a824fcba95adc954bcfc516` |
| `internal/apiserver/coordination_inbox_archive_ack_get_test.go` | `7a70de59ea6bd5259031d265132e065f50a7615ac30170afc245b3720cd10dd6` |
| `internal/apiserver/coordination_inbox_archive_get_test.go` | `1bc8fd52f2629b7cab74cdfa9941a0f174d7257ca61cfaa58beb955711d3c5f9` |

These hashes attest the tested Go bytes. The later verification edits change
only this record, so they require review and ordinary exact-head CI without
repeating the local behavior tests. The original raw captures and failures are
retained separately; the later correction does not erase them.

Supplemental PVC evidence lives under
`/home/dev/work/orders/dev-env-chapter7-inbox-ack-get-1015/`. It includes the
exact command, raw captures, source hashes, finite deadlines and owner handoffs.
These files are not public artifacts. The committed tests, contract, portable
command and public exact-head PR checks provide reviewable source evidence.

The original model mismatch self-report, supported configuration correction,
timestamps-only login check deviation and administrative interruptions remain
recorded. Supported `/status` confirmed the implementation worker’s configured
model `gpt-5.6-terra` and reasoning effort `high`; it did not independently prove
the model that served a request. No login, renewal, GUI, usage-reset check or
unrelated Claude action followed.

The first advisory review found spacing errors and dense evidence paragraphs
in this record. Those are corrected in a real documentation commit, with the
original finding preserved. Independent native closure, required CI and available
advisory findings must be inspected for the final exact head before source
acceptance. No successful check on an earlier head substitutes for that evidence.

This source candidate does not claim a merge, installed or usable acceptance,
or completion of R4. Retention activation, runtime or provider access, security,
credentials, archive deletion, deployment, v1 changes and cutover remain held.
All failure, deviation, deadline and stall records remain preserved. The original
failed attempt 1 and permission-denied rerun on #301, and dependent R3 work,
remain held; no alternate retry or trigger is authorized.
