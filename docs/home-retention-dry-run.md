# HOME retention dry-run

This standalone source tool verifies recorded preservation evidence and reports
two-tier HOME retention decisions. It does not collect live evidence or perform
cleanup. [ADR-018](../.agents/sagas/distributed-dev-env/adrs/018-two-tier-home-retention.md)
records the owner policy; [DESIGN-044](../.agents/sagas/distributed-dev-env/designs/044-two-tier-home-retention.md)
defines its evidence and remaining collector integration. No operator, agentd,
release pin or runtime flag calls this package.

With a local Go toolchain matching `go.mod`, build the standalone command:

```sh
GOMAXPROCS=2 nice -n 19 go build -p 1 -o bin/home-retention-dry-run ./cmd/home-retention-dry-run
bin/home-retention-dry-run --now 2026-11-01T21:11:37Z \
  --input /private/offline-snapshot.json --trust /private/trusted-public-keys.json
```

Use synthetic data when testing. Real snapshots and reports can contain private
repository/storage identities and must stay outside public git. `--input -`
reads stdin. Input is at most 4 MiB and 1,000 HOME records; trust configuration is
separate, at most 64 KiB and 64 keys. Omit `--trust` to report deadlines and
exceptions without allowing immediate candidates. No keys are trusted by default.
No production attestation keys or collector are supplied by this change.

The same command consumes existing sanitized AgentSession/PVC/Pod audit metadata:

```sh
bin/home-retention-dry-run --now 2026-11-01T21:09:03Z \
  --format legacy-snapshot --input /private/snapshot.json \
  --legacy-dispositions /private/fixture-uids.json
```

This bounded adapter preserves existing task/terminal clocks, exact recorded
identity associations, raw snapshot SHA-256, capture time and source field paths.
It never uses PVC birth as completion or promotes old success, `CleanAndPushed`
or PR metadata into preservation proof. Missing/conflicting bindings remain
exceptions; known timestamps remain in audit provenance even when they cannot
establish a bound clock. Active provider mounts protect work; rescue holds retain
separate protection. The optional `--legacy-dispositions` file maps exact PVCUIDs
to named fixture dispositions. It cannot remove existing protection or supply
preservation authority; real UID lists stay private. Extra/duplicate UIDs and
invalid disposition values refuse. Legacy mode rejects `--trust` and cannot
certify live immediate candidates.

`--now` is mandatory, so a replay uses the same clock. Seven days is exactly
168 hours, including equality. `--warning-lead` defaults to `24h` and must be
positive and at most `168h`. `--observation-max-age` defaults to `5m` and must be
positive and at most `1h`. These flags change report calculation only. There is
no notification delivery, scheduler, destructive flag or delete mode.

The observation age bound also applies to proof capture and verification of
publication/durable-copy receipts. An unchanged HOME cannot keep a stale remote
receipt valid indefinitely; a collector may need to query the remote again.

For example, a minimal old record with no preservation evidence is represented
explicitly, rather than inferred clean from its age or a success exit:

```json
{"version":"home-retention.v1","records":[{"uid":"synthetic-pvc-001","state":"unknown"}]}
```

The report always carries `dryRun: true` and `deleteEnabled: false`.
`immediateCandidates` contains only verified preserved entries. All other
records remain visible in `stoppedWorkQueue`, including protected active/held
or separate-disposition records. Each list is ordered by immutable PVCUID
(`uid`), with sorted reason codes and exact Session/PVC/Pod/executor binding.
Named `canary`, `pilot`, `fixture` and `rescue_hold` dispositions are protected;
unknown disposition values block candidacy and follow the warning/decision clock.
State, hold and disposition must match a fresh authenticated observation before
they can support candidacy. Stopped holds and special fixtures remain protected
but carry `dispositionDecisionRequired: true` at expiry or with an unknown clock.
Active work stays protected without expiry actions. Clearing a raw hold or
fixture marker cannot reuse its old observation.
Statuses distinguish retained, warning, decision required and protected work.
Missing/future clocks have no fabricated deadline and need an owner decision;
expired unresolved work stays in the decision queue. A candidate is never a
UID-bound destructive approval.

The primary clock is the recorded provider task `finishedAt` for the same
Session/PVC/Pod/executor attempt. Only when no task-completion record exists may
a recorded actual terminal observation supply `observedAt`. The report states
the clock source. An invalid task timestamp cannot silently fall back. HOME
revision changes invalidate preservation evidence but do not reset that attempt's
completion clock. A resumed executor or replacement Pod needs its own actual
binding and completion evidence; active mounts always prevent candidacy.

The attestation contract is defined in
[`types.go`](../internal/homeretention/types.go), with signing and verification in
[`verify.go`](../internal/homeretention/verify.go). Signing uses domain-separated
bytes from `SigningBytes` and Ed25519 for optional offline verification. The payload is the typed Go `encoding/json`
representation, preserving array order; it is not arbitrary raw JSON or a generic
cross-language canonicalization scheme. Trust purpose separates collector,
publication and durable-copy keys. Publication keys are scoped to an exact
repository and remote; durable-copy keys to an exact destination. The snapshot
cannot add trusted keys. Signatures authenticate an authorized collector's
assertions; they do not independently query a Git host, verify a push or inspect
HOME contents.

The reusable library also accepts an `EvidenceVerifier` supplied by an already
authenticated caller. It can verify exact existing agentd/operator transport
provenance and remote receipt scopes without new signing infrastructure. It
must authenticate the complete domain/payload and receipt authority, never a
snapshot's own flag. The offline CLI cannot enable this boundary from a JSON
field or command-line assertion. Production collector integration remains
separate; new signing-key provision is not a policy gate.

An immediate candidate needs complete clean repository/worktree/ref/stash
inventory, authenticated exact-remote publication receipts, separately preserved
required artifacts, and a current authenticated observation matching proof ID, executor,
writer generation and HOME revision. Success/PR existence, old `CleanAndPushed`
booleans, stale remote-tracking refs and missing arrays cannot substitute for
these attestations. Every required handoff, result, history or recovery category
must be explicitly resolved; unknown requirements remain named exceptions.
No provider-history archive is required when that requirement is explicitly none.
The authenticated requirements include an authoritative `source` and immutable
`revision`. The future collector must resolve these references to existing owner
decisions; naming one does not create authority. Initialized submodules also
need their own repository inventory and receipts.

The legacy adapter produces audit records, not automatic migration or backfill
of proof. Versioned attestation input rejects unknown versions/fields, duplicate
records or JSON members, excessive nesting, trailing documents and input bounds.
Valid old records with missing proof remain reportable exceptions. The legacy
metadata format ignores unused provider fields within strict byte/count/depth/
duplicate bounds; ignored fields never establish preservation or authority.

This tool intentionally cannot certify that a later destructive action is safe.
Late writes, resumed work, new Pod/PVC UIDs, stale observations and superseded
proofs invalidate candidacy. Any separately approved executor would need fresh
uncached active-mount/identity reads and write fencing immediately before a
UID/resourceVersion-conditional action. Neither that executor nor its activation
is implemented here.
