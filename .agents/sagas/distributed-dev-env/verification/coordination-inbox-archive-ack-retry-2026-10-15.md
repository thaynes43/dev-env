# C7-R4 inbox acknowledgement retry source record

**Label:** R4. **Story:** #325, `C7-R4-INBOX-ACK-RETRY-1015`.
**Base:** `862f7a4930f0d2d0274ffc5ac96750a8f311bac6`.
This is source and focused-test evidence only. It is not an installed, CI,
reviewed, merged, or usable R4 acceptance. Chapter7 root accepts separately.

## Contract and source evidence

`POST /v1/inbox/{message}/ack` retains its existing session-only and bounded
empty-object checks. Its live message path still calls `Inbox.Ack` and retains
normal create/confirmation behaviour. A live `NotFound`, with archive reads
enabled, now reaches `authorizedInbox(..., true)`. For a verified archived
message, `archivedInboxAckRetry` reuses `archivedInboxReadAck`, performs a
post-I/O `inboxReadFence` against the original recipient, and returns `200
Confirmed` with the verified immutable acknowledgement, `archived: true`, and
the acknowledgement UID's first UTC archive receipt. It has no writer. The
archived branch cannot call `Inbox.Ack` or manufacture an acknowledgement.

The strict empty acknowledgement check also rejects non-empty JSON objects
before live/archive lookup. This was found by the retry negative test, not
weakened. The accepted archive GET helper, store, protocol, and API shapes were
not edited.

Final source SHA-256 values:

```
bcb462905009dd593a7abe71bb27974816802d0a847b8dff4925e2d068b31b87  internal/apiserver/inbox.go
ca657fd2007f66b395c86bca654605bcd4a46be888197bd6ea16ab4dee7e39fb  internal/apiserver/coordination_inbox_archive_ack_retry.go
3471e8109d0d6dc52761c4ab3ccb53fce8e99148262172620dd0dd2929eacce5  internal/apiserver/coordination_inbox_archive_ack_retry_test.go
128b80288eaac8df747764063daaee6049e1c5b08f8257c656cfd76b12c200f3  internal/apiserver/coordination_inbox_archive_get_test.go
b732b2e31c7fd8dfcc20f9c1f234d48e462aeadb22d4058f4717e4f424f3ca0e  internal/apiserver/coordination_inbox_archive_ack_get_test.go
```

The accepted-base source hashes captured before wiring were
`cef73a337dade173a31e2e7283ce6d866acfc942c25bc8bc6bffe73f0d139d79`
for `inbox.go` and
`8b776a1507379b99ca76814d2d593aedd09c47444a824fcba95adc954bcfc516`
for `coordination_inbox_archive_ack_get.go`.

## Focused evidence

The required accepted-base RED used the constrained cached Go command below
with `TestInboxArchiveAckRetryRedAcceptedBase`. It created a live acknowledgement
first, then deleted the live message and acknowledgement while providing valid
archived message and acknowledgement envelopes. It failed as required with HTTP
404 and zero archive calls:

```
accepted-base archived POST retry = 404 messageCalls=0 ackCalls=0
body={"error":{"code":"not_found","message":"inbox record not found"}}
```

The platform transcript retains that direct JSON output. Its raw output was not
written to a durable file at that instant, so no raw-file SHA-256 is claimed.
This is a capture deviation for root to review; the expected RED itself is
preserved above and was not rerun against changed source.

The final GREEN command was:

```
flock -w 60 /tmp/dev-env-go-tests.lock env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off nice -n 19 /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go test -json ./internal/apiserver -run '^(TestInboxArchiveGet.*|TestInboxArchiveAckGet.*|TestInboxArchiveAckRetry.*)$' -count=1
```

It exited 0. Direct JSON is retained outside the repository at
`/home/dev/work/orders/dev-env-chapter7-inbox-ack-retry-1015/green-go-test.json`;
its SHA-256 is
`211224251dc63a1a6a9c95bb29bfdac0ca91de0af53b2f34a65b52755a8b57ce`.
The run contains 17 top-level test methods and 83 subtest nodes, reported
separately from one package pass event (`ok ... internal/apiserver 6.089s`).

`TestInboxArchiveAckRetryConfirmedNoWrites` installs a poisoned inbox Create
interceptor after fixture setup. It proves the live `201` control, archived
`200 Confirmed`, complete stored acknowledgement projection, first archive
receipt, and that an old stored actor differs from the resumed valid caller.
`TestInboxArchiveAckRetryRefusalsNeverWrite` applies the same poison to
default-off, archived ack not-found, archive failure, and extra JSON intent
refusals; it verifies archive-call counts and released slots. Existing focused
archive message/ack suites cover reused helper canonical/digest/binding,
typed-error, nil reader/slots, busy, cancellation, scope, replacement and
actor-fence cases.

## Failures and remaining gates

Besides the expected RED, one test-only compilation correction changed the
interceptor callback's second parameter from `client.Client` to
`client.WithWatch`. One negative test initially sent `bytes.Buffer` through the
fixture's JSON marshaller, producing `{}` rather than its intended raw body;
it was corrected to send a string. That test then exposed the real strict-empty
handler defect, which the source change fixes. No failure was discarded.

Remaining gates: root's independent review and acceptance, commit inspection,
required CI and advisory-review availability, PR/merge, image release and any
deployment or installed verification. No runtime, authentication, rollout,
database, provider, or external-service action was taken.
