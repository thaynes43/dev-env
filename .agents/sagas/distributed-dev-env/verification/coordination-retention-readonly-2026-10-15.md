# Coordination retention read-only prerequisite — 2026-10-15

DESIGN-024 R3 section 5 steps 2–3 now have source/fake-client support in
`internal/coordretention`. This package is not wired to an operator runnable and
performs no archive exec, mutation, mark, release, delete or activation.

The supplied Reader must be the uncached operator APIReader. Lists use only
explicitly enabled kinds and the operator namespace, with 10-item publication/
message pages and 50-item claim/ack pages. Summaries retain identities needed by
the rules, never object text or raw objects. Deterministic names and existing
spec/record validators bind ingestion. Evaluation checks coherent mandatory
summary identities independently of the exported Valid flag. Results are sorted
by kind, project, name and UID; this ordering does not infer revision chronology.

Eligibility is Released, Kept or Unknown; Released grants no mutation authority.
Session absence requires NotFound or a valid replacement UID from a response
bound to the requested namespace/name. The same UID remains present regardless
of phase or deletion state. Unknown reads and unreconciled namespaces release
nothing. A pass memo stores namespace/name answers and compares each requested
UID; a later Evaluate call gets a fresh memo, and later delete checks must never
reuse it. Cancellation stops pages, GET proofs and memo eligibility.

Message eligibility needs an absent sender and a bound deterministic ack or
absent recipient. Ack eligibility needs live message UID absence plus explicit
archive-holds evidence. Only proved live absence emits RequiredMessage for the
later bulk lookup; missing or Known=false evidence stays Unknown. Known no-hold
keeps the ack. Malformed responses and scope errors never schedule that lookup.

The final composed fake-client suite used cached Go1.27.2, GOMAXPROCS=2, -p1,
nice19, shared flock and dependency downloads off:

```text
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 \
GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
/home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go \
test -p 1 -count=1 -v ./internal/coordretention
```

Twelve methods /76 passing nodes, zero skips, passed in 0.107 seconds. The final
additional stable project/name/UID ordering fixture was run separately with
`-run '^TestSummariesStableProjectNameUIDOrder$'`; it passed without replaying the
unchanged matrix. Valid four-kind fixtures, paging/continuation/subsets, malformed
scope and identities, list failures/cancellation, session UID/memo/phase/error
rules, sender/recipient/ack combinations and pending archive-proof cases are
covered. Initial worker compile/scheme failures and the owner's invalid-project
test-premise failure are preserved in the private checkpoint; no new project or
metadata-label policy was introduced to satisfy a fixture.

Focused independent source review and required CI remain acceptance gates.
PR298/299 tests/reviews are reused unchanged. Bulk archive lookup/shared-slot
integration, runnable scheduling/metrics, guarded release and default-off wiring
remain later R3 work. Actual shelf memory measurement, installed consumers,
archive activation and full R2/R3/pilot/MVP acceptance remain unproved. All
live/access/deployment/archive deletion/v1/cutover holds remain unchanged.
