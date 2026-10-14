# Local coordination archive lookup — 2026-10-14

Scope: the read-only DESIGN-024 R2 slice after source #286, merge
`ae82d9531463bd80ed947cea9b3577ef2d15e029`. It reuses accepted immutable
storage, metadata validation, the shared lock and the real filesystem check.

`agentd ctl coord-archive lookup` accepts one JSON array of 1 to 1024 identities:

```json
[{"resource":"taskclaims","project":"sample","name":"task-claim.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","uid":"11111111-1111-1111-1111-111111111111"}]
```

It returns matching identity fields, holds, an array of resourceVersion/sha256
selectors, and graceStart. Missing identities return holds=false, revisions=[]
and graceStart=null. Results preserve input order and duplicates. Revision
selectors sort lexically for deterministic output; this is not time ordering.
Stdin uses the existing 1 MiB + 64 KiB bound; complete output metadata is capped
at 4 MiB. Overflow is an invalid error, with no partial output.

Under one shared lock, lookup walks only requested UIDs in 128-name batches.
It checks canonical manifests bound to their exact path, with bounded no-link
reads. It neither reads nor verifies object.json; a damaged object can still
have discoverable metadata. Exact get remains required to verify those bytes.
It returns null grace only for an absent or empty event journal. A nonempty or
unreadable journal refuses the entire batch until event support is implemented.
No journal state, mark, grace period or release eligibility is invented.

The main-dispatcher fixture creates synthetic temporary files on already
existing filesystems with the real device check enabled and no subprocesses.
It exercises stdin put -> stdin lookup -> exact get, identical bytes/digest,
and one corrupt get returning no object. No mount, live archive, credential,
API, client, release, deployment, activation or cutover is involved.

```sh
GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
  flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 \
  /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go \
  test -p 1 -count=1 -v -run '^TestCoordArchiveCLILookupSelectorsRoundTrip$' ./cmd/agentd
```

The command fixture is source behavior, not installed production execution.
Marked lookup, event writing/grace transitions, general get selection, list,
old incoming cleanup and shelf startup wiring remain R2 work. Full R2,
installed consumers, H7/H8, pilot and v1 retirement acceptance remain separate.

The focused source suite passed all 34 top-level coordination methods and their
finite subcases under the same lock and CPU bounds, with Go 1.27.2 and dependency
downloads disabled: cmd 0.115s, agentd 0.844s, protocol 0.069s. No command test
skipped. The new main-dispatcher selector round-trip produced identical bytes
and SHA256 `89faa1c8153acd2d127a29546b9ed90e2cd3a65333ecb7466b92d739b1dd6e56`.
Corrupt exact get returned `corrupt` and no object; lookup still returned metadata.
The suite also covers all four kinds, absent identities, repeated input order,
the maximum 1024-item batch, validation before filesystem access, revision reads
past the 128-name batch, hyphenated resource versions, exact response budget and
one-byte overflow, nonempty/linked journals, linked/missing/malformed manifests,
linked revision directories and substituted identities without partial results.

The morning refresh of the earlier #286 demonstration also actually ran once
without skips, with the same digest and corruption refusal. Both demonstrations
remain synthetic source fixtures; installed production acceptance is held.
