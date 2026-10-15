# Coordination archive shelf client — source checkpoint

This bounded DESIGN-024 R3 slice adds `internal/shelf.CoordinationArchive` for
put, mark, lookup, get and list over the existing shelf Executor. It validates
requests before exec and uses canonical bounded stdin, one-minute maximum exec
deadlines (earlier caller deadlines win), 8 MiB stdout and 4 KiB stderr caps.
Cancellation, overflow and malformed/incomplete answers return zero results.
Known complete exit-1 refusals remain typed; exit 2 with the real older ctl usage
diagnostic becomes `no_archive`. Transport errors do not become archive refusals.

Get verifies every supplied selector and the full object digest, then requires
and returns the identical canonical bytes; selected corruption never falls back.
List validates the projected object's identity, project/recipient/message binding,
redaction, item errors and cursor binding without hashing that projection against
the full object digest. A corrupt list item exposes neither manifest nor object.
Put preserves immutable first-publisher provenance across retries, including
existing grace on an unmarked retry. Lookup's `holds` means revisions exist;
it does not imply non-null grace. Mark present/reset clears grace.

The client remains caller-gated, like HistoryReader: later runnable/API callers
must take the existing shared ExecSlots pool. No new semaphore, runnable/API
integration, two-second API wait, archive mount, release or runtime activation is
claimed. Rescue and prune behavior is unchanged.

The final owner correction ran this finite fake-client suite on October 15 UTC:

```text
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 \
GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
/home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go \
test -p 1 -count=1 -v ./internal/shelf
```

Result: 14 test methods, 21 passing nodes, zero skips, 0.173 seconds. Seven
coordination-client methods cover five-verb argv and canonical stdin, marked and
plain retries, valid null grace/missing records, invalid batches and prelaunch
selectors, exact Get bytes/pins, malformed/incomplete/noncanonical answers,
list projection/recipient/cursor refusal, six typed refusal codes versus transport
failures, both output caps and context/deadline bounds. Existing history, shelf,
rescue/prune and slots regressions also passed. Two owner compile failures from
a test-helper naming mistake were retained before the successful run; earlier
worker failures and reviews are preserved in the private checkpoint.

Independent review and required CI are final source acceptance gates. Actual
shelf-limit memory measurement, release/installed image, archive activation,
consumer integration and full R2/R3/MVP acceptance remain unproved. The existing
recorded-time/clock-rollback selector limitation is unchanged. No held live,
access, deployment, deletion, v1 or cutover operation occurred.
