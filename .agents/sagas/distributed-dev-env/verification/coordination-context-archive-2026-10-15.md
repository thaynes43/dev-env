# PublishedContext archive GET source verification — 2026-10-15 UTC

This bounded DESIGN-024 R4 source slice adds a single PublishedContext GET
fallback. It branches from accepted merge 7763470 and imports no PR301 code.
The Server switch defaults off; operator flag wiring remains a later source
story. No runtime archive access, activation or retention action is performed.

The live store answers first. A masked foreign record, including malformed
foreign metadata, stays 404 with zero shelf calls. Only an actual live absence
can use the existing shared slot pool and typed shelf Get. Slot waiting honors
the parent deadline and is capped at two seconds; missing dependencies answer
503. Canonical bytes, full-object hash, manifest scope, immutable spec and the
name derived from its identity are verified before projection. A valid foreign
archive answers 404; corrupt, ambiguous or unavailable answers 503. Cancellation
returns no archived object and restores the token.

The existing contextMetadata projection preserves immutable provider and
requested model, with observation/usage/cost still Unknown. It adds archived and
archivedAt; live JSON omits both. The creator can be absent and no source Session
join is made. The existing strict Heartbeat client implementation is unchanged;
explicit and managed-output archived metadata both decode with identical text
and content hash. Fixed canary/directory/live routes are not given fallback.

Final focused evidence comprises 14 methods / 41 passing nodes, zero skips:
API and store tests in correction run 2 (0.807s / 0.073s), plus unchanged shelf
and strict-client tests from correction run 1 (0.014s / 0.025s). These are two
bounded selections, not a claimed single combined execution. Both use:

```text
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off <cached-Go1.27.2> test -p 1 -count=1 -v <changed-packages> -run '^TestContextArchive'
```

Fakes cover authorization before shelf, absent creator and coordinator reads,
exact text/hash/immutable attribution, live wire key omission, true/masked
absence, invalid identity/spec/resource/namespace and transport error cases,
full shared-pool ownership/restoration, cancellation before and after token
acquisition or successful Get, and a short parent slot deadline. An in-process
Executor exercises the actual typed shelf client for successful byte round-trip
and corrupt-byte refusal; it launches no subprocess or real cluster client.
No unrelated accepted suite or TTY fixture was replayed locally.

Native R4-CONTEXT-1–5 and advisory attribution findings are addressed; final
review, exact-head required CI and merge state are recorded on the source PR.
Lists, other record kinds, operator wiring, release, installed/full R4/MVP
acceptance and every separate runtime/access/deletion/v1/cutover gate remain.
Latest selectors still mean unique greatest recorded archive time with UID
birth ordering; strict physical arrival chronology after clock rollback is
unestablished. No corrupt selected object falls back to an older revision.
