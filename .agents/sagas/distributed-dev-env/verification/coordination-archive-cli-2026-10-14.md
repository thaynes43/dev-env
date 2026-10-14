# Local coordination archive CLI demonstration — 2026-10-14

Scope: the smallest end-to-end DESIGN-024 R2 command slice after
[#285](https://github.com/thaynes43/dev-env/pull/285), merge
`9f5e71fb17ddb2d6ee6f1d91a194b45aebc70589`. This uses the accepted ingestion,
immutable revision storage, index publication and durability repair.

`agentd ctl coord-archive put` reads one bounded JSON request containing the
manifest without archivedAt and the full object. Invalid UTF-8, unknown envelope fields,
trailing JSON, invalid identity/digest and oversized requests are refused.
The backend checks for a nonempty journal while holding the exclusive shelf
lock. It publishes an unmarked revision and prints its stored manifest plus
`graceStart: null` only when no journal state needs interpretation.
It refuses existing nonempty journals. No absence-event implementation is
required for this unmarked path; marking remains unsupported.

`agentd ctl coord-archive get --resource R --name N --uid U
--resource-version V --sha256 H` resolves one exact revision across projects
under the shared lock. Names under multiple projects are ambiguous. It verifies
the stored manifest and full canonical object before returning either.
Invalid query components, missing revisions and corruption fail closed.

The runnable local demonstration uses the existing main command dispatcher
and real storage. A private test constructor chooses temporary storage on the
already-present /dev/shm filesystem; HOME is another temporary directory.
The real filesystem-device check remains enabled. No filesystem is mounted,
no live archive is used, and there is no public root or mount-bypass option.
The command runner refuses any subprocess call.

```sh
GOMAXPROCS=2 GOFLAGS=-p=1 flock -w 60 /tmp/dev-env-go-tests.lock \
  nice -n 19 go test -p 1 -count=1 -v \
  -run '^TestCoordArchiveCLIRoundTripAndCorruption$' ./cmd/agentd
```

Observed command evidence from the synthetic fixture:

```text
agentd ctl coord-archive put
  exit=0; graceStart=null
  sha256=89faa1c8153acd2d127a29546b9ed90e2cd3a65333ecb7466b92d739b1dd6e56
agentd ctl coord-archive get --resource taskclaims
  --name task-claim.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  --uid 11111111-1111-1111-1111-111111111111 --resource-version 42
  --sha256 89faa1c8153acd2d127a29546b9ed90e2cd3a65333ecb7466b92d739b1dd6e56
  exit=0; identical canonical bytes and digest
same exact get after synthetic object corruption
  exit=1; error=corrupt; no object returned
```

The focused suite used Go 1.27.2 under the same CPU/shared-lock bounds:
`go test -p 1 -count=1 -run '^TestCoordArchive' ./cmd/agentd
./internal/agentd ./internal/agentd/protocol`. All 27 methods/subtests passed:
cmd 0.020s, agentd 0.654s, protocol 0.048s. It also covers malformed/oversized
requests, unsupported options, unmounted ordinary HOME, unsafe exact identities,
ambiguous projects, missing manifests and unknown grace state without writes.
The local command methods skip when a separate existing temporary filesystem
is unavailable; they actually ran and passed in the dev-env source workspace.

Focused native review found that typed JSON decoding could replace an invalid
publisher byte before manifest validation. The command now rejects invalid
envelope UTF-8 before decoding. A valid request with one malformed publisher byte
returns invalid and leaves the archive fixture empty. The focused suite above
passed after that correction.

This is partial command support. General latest-revision selection, marking,
events, lookup/list, cleanup and full shelf startup wiring remain R2 work.
No live CLI round-trip, deployment, release, permission, credentials, archive
mount/activation, v1 change or cutover is authorized or claimed.
