# Coordination archive shelf client — source checkpoint

This bounded R3 source unit adds `internal/shelf.CoordinationArchive`, the
operator-side typed transport for the existing five `agentd ctl coord-archive`
commands. It uses the existing Running/Ready shelf selection and Executor seam;
it has no runnable, API route, storage redesign, subprocess test, mount claim,
or runtime activation.

The client validates requests before exec, uses canonical bounded stdin for put,
mark and lookup, and caps one exec at one minute (or an earlier caller deadline),
8 MiB stdout and 4 KiB stderr. It refuses cancellation, either output overflow,
null/incomplete/trailing answers and malformed response fields before returning a
result. It accepts only complete documented exit-1 `HistoryError` refusals; the
recognized older CLI usage diagnostic becomes `no_archive`. Transport failures
remain transport failures. A get checks every supplied selector, validates the
manifest, verifies its full object, and returns only the exact canonical bytes
whose digest was verified. List validates its bounded projection envelope,
identity, recipient binding, corrupt-item shape and cursor; it deliberately does
not compare a projection with the full manifest digest.

`CoordinationArchive` is caller-gated. It neither creates nor takes an
`ExecSlots` token; later runnable/API callers must take the existing shared pool
before invoking it. This source checkpoint does not establish per-replica
sharing, an API two-second wait, an installed shelf archive mount, release, or
R2/R3 acceptance.

On 2026-10-15 UTC the focused command below passed. It includes existing shelf,
history and slot regressions plus fake-client coverage for exact five-verb
transport, validation-before-launch, canonical get refusal, incomplete/null
responses, corrupt projection refusal, bounded stdout/stderr, cancellation and
older-shelf `no_archive` handling:

```text
env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
nice -n 19 flock -w 60 /tmp/dev-env-go-tests.lock \
/home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go \
test -p 1 -v ./internal/shelf
```

No live cluster command, archive mount, release, deployment, deletion or
retention action occurred. Independent review and required CI remain owner
acceptance gates.
