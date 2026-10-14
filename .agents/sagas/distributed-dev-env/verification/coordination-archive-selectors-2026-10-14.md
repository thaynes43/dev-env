# Coordination archive get selectors — 2026-10-14

Scope: the independent safe DESIGN-024 R2 general-get selector portion after
#288, merge `a2d2f2ca384d026a0c9cb6a3580688624b93a6cb`. No retention or clock
policy is redesigned. Source behavior is distinct from installed acceptance.

The source command accepts:

```text
agentd ctl coord-archive get --resource R --name N [--uid U]
  [--resource-version V --sha256 H]
```

Revision and complete SHA256 must be supplied together. Without UID, the reader
selects the uniquely greatest recorded UID first-publication time: the minimum
canonical manifest time must agree with the UID's immutable all-record index.
A later revision of an older UID does not reorder UID births. Missing/damaged
birth metadata, including loss of the first manifest without a matching index,
refuses instead of silently reranking the UID. With revision omitted, the reader
selects that UID's uniquely greatest recorded revision archivedAt. A revision/hash
pair without UID applies to the chosen UID only; there is no older-UID fallback.

One shared lock spans discovery, selection and verification. The reader finds
the unique project for the name before any object read, refusing cross-project
ambiguity even with full pins. UID/revision metadata is read in 128-name batches
with constant candidate memory, bounded no-link canonical manifest reads and
immutable identity checks. Defaults refuse invalid, incomplete or linked metadata.
Only the selected object is read, with full canonical identity/size/SHA256
verification before output. A damaged chosen object never falls back to an older
one. Fully pinned reads do not depend on other UID/revision time ordering.
The existing object, metadata and output bounds remain unchanged.

**Unestablished chronology:** the accepted archive has recorded timestamps and
UID birth indices, but no independent publication sequence. Equal greatest times
refuse as ambiguous, with no object; explicit UID/revision/hash pins remain usable.
ResourceVersion, UID/digest lexical order, directory enumeration and filesystem
mtime never break ties. Strict physical arrival order after clock rollback cannot
be proved from this metadata. This unit implements recorded-time selection and
does not claim that unresolved chronology choice or full selector/installed
acceptance is settled. No new clock, journal or sequencing policy was introduced.

The synthetic main-dispatcher scenario uses existing filesystems and the real
device check, temporary data and no client subprocesses. Its UID/version strings
are deliberately opposite to time order. A newer capture of an older UID occurs
before the newer UID's next revision, proving birth ordering is not recapture
ordering. The selected bytes and full digest match:

```text
omitted UID: immutable first-publication ordering
omitted revision: recorded archivedAt ordering; opaque version order ignored
selected object: identical bytes
sha256=9976e476808188ec908d035370fef349fc6c2b3a9cfa1a6e4a7998e0bc98593a
corrupt selected latest: exit1 corrupt; no object and no fallback
```

Run only this new scenario in the configured source checkout:

```sh
GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
  flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 \
  /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go \
  test -p 1 -count=1 -v -run '^TestCoordArchiveCLIOptionalSelectorsAndCorruption$' ./cmd/agentd
```

The final focused coordination suite passed all 49 top-level methods and 129
passing nodes including finite subcases, with no skips or downloads: cmd 0.104s,
agentd 1.701s, protocol 0.053s on cached Go 1.27.2 under the same lock/CPU limits.
Changed get/CLI wiring warranted the existing focused regressions. There was no
separate repeat of a settled plain demonstration.

The suite covers all four kinds, opposite lexical UID/version order, old UID
recapture, paired pins and no older-UID search, equal UID/revision time refusal,
full pins through ties, corrupt selected object without fallback, immutable
identity substitution, missing/damaged birth index, lost first manifest, unsafe
paths/links, cross-project ambiguity and actual second-batch corruption refusal
for both UID and revision scans. The first run caught a test-helper type mismatch;
its compile-failure log is preserved, followed by a passing corrected suite.
Focused review noted the original revision batch fixture could pass without using
the second batch. It now corrupts a non-winning manifest at its observed position
beyond entry 128 and requires corruption refusal. The final suite above passed
after that fixture and the birth-index consistency guard. Budgets, prior failures
and the three-failure/60-minute stall history are preserved, not reset.

Next source: paged list using the accepted immutable index, then old incoming
cleanup and shelf startup wiring. Any claim of strict arrival chronology needs
an authoritative interpretation; this unit does not invent one. Installed
consumers, H7/H8, full parity and pilot acceptance remain separate. Impersonated
live dry-runs await Tom; consumed inquiries are not repeated. No new access or
credentials, release/deployment, archive mount/activation/deletion, v1 changes
or cutover is authorized or executed.
