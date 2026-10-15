# Coordination retention bulk lookup source checkpoint — 2026-10-15

`internal/coordretention/lookup.go` consumes one evaluated pass with a caller-
provided shelf `ExecSlots` pool and the existing typed `CoordinationArchive.Lookup`
transport. It validates all live summaries and pending ack message dependencies
before its first exec, deduplicates exact identities, groups by resource and
project, and uses stable batches of at most 1024 identities.

Only a validated pending `InboxReadAck` message proof changes its decision:
archive holds become `Released`, and known no-holds become `Kept`. Other
`Unknown` decisions remain unknown. Invalid summaries do not launch a lookup;
malformed pending dependencies, busy slots, client errors, cancellation and
incomplete or mismatched responses return no partial result. Lookup metadata is
retained for later mark/grace work only; this source slice adds no archive write,
release, delete, runnable, runtime action or acceptance claim.

Focused fake coverage proves exact chunking beyond 1024 records, same-resource
cross-project isolation, dependency deduplication against a listed live message,
and composition with actual rule evaluation: proved absence plus holds releases
the pending ack, no holds keeps it, and a message read error remains Unknown.
It also proves one token held during exec on a supplied two-token pool, restored
capacity, no usable output after a later-batch error/partial/wrong-identity/busy/
cancel result, and cancellation for empty/invalid inputs and after client return.
The revised suite has 9 top-level methods / 21 passing nodes, zero skips (0.061s).
Native R3-BULK-1/2 cancellation/fixture findings are addressed; final native
corrective review and hosted CI are recorded with the PR acceptance. It ran with cached Go 1.27.2, `nice -n 19`, `GOMAXPROCS=2`,
`-p 1`, the shared test lock, and downloads disabled:

```text
go test -p 1 -count=1 -v ./internal/coordretention -run '^TestLookup'
PASS
```

This is source and fake-test evidence only. Full R3, installed behavior and all
existing runtime, access, archive activation, deletion, v1 and cutover holds
remain unaccepted.
