# Coordination retention R4: inbox archive metadata source verification

R4 is the API-read delivery unit in DESIGN-024 section6 and backlog11.
C7-R4 is the bounded story prefix used by Chapter7, the current Codex source
coordinator; neither is a runtime feature or an installation claim. P2 below
means a medium-severity native code-review finding. This record covers #313
and [PR314](https://github.com/thaynes43/dev-env/pull/314).

This is a source-only, default-off candidate for #313. It does not claim R4,
release, installation, archive activation, runtime acceptance, or cutover.

`GET /v1/inbox?archived=only` selects the typed shelf's `inboxmessages` list
once per page. The ordinary inbox list remains live-only and does not take a
shelf slot. Archived rows validate canonical text-stripped metadata against the
manifest's exact resource, identity, project, recipient UID and deterministic
message name. The API projects immutable attribution only; it does not join a
sender or recipient Session. Recipient calls use their recipient index, while
configured coordinators use the project's all-record index.

The route retains existing inbox scope/fence checks, rechecks the original
caller after shelf I/O, binds opaque continuation to route/project/recipient/
limit, skips only a valid corrupt marker, and returns unavailable for malformed,
busy, cancelled, or unavailable shelf outcomes. `archived` and `archivedAt`
are additive wire fields. `inbox.ValidArchiveMetadata` accepts only the
text-stripped projection; `ValidMessage` still requires bounded nonempty live
text.

## Historical worker pickup and candidate evidence (2026-10-15)

At pickup, the branch was clean at
`3d0a18e85a39540c9c689abb253a4974cb6be39f`.

The first serialized, offline Go selection used:

```text
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test ./internal/apiserver ./internal/inbox
```

It exposed an environment prerequisite (`KUBEBUILDER_ASSETS` absent) in the
envtest methods and a local helper-name collision, which was fixed by
renaming the internal archive helper. The subsequent focused selection passed:

```text
ok github.com/thaynes43/dev-env/internal/apiserver 0.361s
ok github.com/thaynes43/dev-env/internal/inbox 0.012s
```

At the original worker checkpoint (2026-10-15T18:32:09Z), its candidate had
not been independently reviewed or pushed, and no PR existed. That statement
records the historical checkpoint. Subsequent coordinator work opened PR314
and obtained native closure; current delivery still requires that PR's exact
head checks and findings disposition. Release, installation and runtime
acceptance remain separate. The Codex coordinator owns source acceptance.

## Chapter7 acceptance correction

The initial candidate's two new test methods and summarized command outputs
were insufficient for source acceptance. The worker remains stopped. Chapter7
added direct UTC JSON captures for default-off behavior, live-list isolation,
1/default100/max100/101 limits, strict singleton queries, coordinator and
recipient scopes, deleting/replaced original recipients, post-I/O caller fences,
full wire-normalized immutable attribution after sender removal, malformed
metadata/manifests, corrupt-marker continuation, cursor query binding and
strictly older progress, slot exhaustion/cancellation, and count/4MiB bounds.
Each row in the oversized-page fixture is individually valid. The synthetic
typed shelf client fixture verifies inbox resource/recipient command routing and
HTTP projection through the existing apiv1 decoder; it is not a real full
archive-to-HTTP or installed run.

Independent native review of initial candidate d98cfc2 identified P2 malformed
query parsing and missing acceptance coverage. URL.Query silently drops invalid
pairs; the archived branch now rejects RawQuery ParseQuery errors before shelf
access. Invalid percent escapes and semicolon parameters have focused refusal
cases. The live branch's parsing remains unchanged.

The final constrained direct selection passes **19 methods / 55 nodes** across
apiserver and inbox. An earlier 18-method/54-node selection predates the typed
fixture and is separate evidence, not an additional combined run. Raw JSON, exact
command/environment, source hashes, and native findings/closure are saved under
`/home/dev/work/orders/dev-env-chapter7-inbox-list-1015/`. Owner-reported stdout
summaries remain historical and are not relabelled as direct raw evidence.

The initial worker's helper-name compile failure and absent envtest assets are
retained separately; no download or broad envtest retry was used. Its temporary
unreserved store_test.go edit was moved to the reserved new validator test file;
store_test.go equals the accepted base. The initially missing manifest recipient
UID binding was fixed with a regression before handoff. Historical Chapter7
counters, including #311's two hosted CI failures and previous deadline overrun,
and #301's denied-rerun/R3 hold remain unchanged.

Ordinary exact-head required CI and advisory findings plus native closure must
be inspected before guarded source merge. No release/installation/activation,
provider experiment, access/security change, archive deletion, v1 change or
cutover is authorized by this source slice.

Native closure tightened the evidence: expected attribution is now built field
by field from the original immutable spec instead of calling inboxMetadata.
Oversized pages pad full objects before computing their manifests and stripping
text; every fixture first passes VerifyCoordArchiveObject. The archive validator
explicitly refuses nonempty text. These are source-test corrections, with a
justified finite final selection and no deadline or ownership extension.

## Reproducible source evidence

The final direct UTC selection ran on 2026-10-15 before commit10c21a8 and
passed19methods/55nodes. The native reviewer independently checked the raw
JSON counts/hash and all seven committed file hashes at that commit, closing
the two medium findings at18:43:00Z. The later correction to this verification
record changes documentation only; it does not relabel test or native evidence
as having run against later documentation bytes.

Use Go1.27.2 and the existing cached dependencies; the exact focused selection
is reproducible from this checkout with downloads disabled:

```sh
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off TZ=UTC go test -p=1 -json ./internal/apiserver ./internal/inbox -run '^(TestInbox|TestValidArchiveMetadata|TestInboxIntentNamesBoundsAndOriginalAttribution)' -count=1 -timeout=60s
```

The raw operator capture is supplemental PVC evidence, not a publicly
fetchable repository artifact. Its SHA-256 is `3d29161aeed4992cf5579c44d06ca44d4c97681db27f2c01b0668d860db54ce8`.
The portable evidence is this command, the committed regression tests, the
source hashes below, and the ordinary public PR checks/review. No installed
or real complete archive-to-HTTP result can be inferred from these fixtures.

| Tested Go source | SHA-256 |
| --- | --- |
| `internal/apiserver/inbox.go` | `531317219d4e71760e63b6b80ade4e11b26094e6e2707b41aa06006cdaaeae22` |
| `internal/apiserver/apiv1/inbox.go` | `c5b0891f2a2d26597521385ac1bb0447d41093ded855422c31388b992fe4cdca` |
| `internal/apiserver/coordination_inbox_archive_list.go` | `f13117f10ff7e26c18ccb13b132b5ae7aaf4623dff5d618b6a66a98865b8846a` |
| `internal/apiserver/coordination_inbox_archive_list_test.go` | `4fd4e9d0a152b063fd523339d83a31a792e1edcdf8d8b6d635c6594d4274f6cb` |
| `internal/inbox/store.go` | `875d5127c96fa7a6123c10601f112201bef9ea5628d81b9608a7cddf08ce9687` |
| `internal/inbox/archive_metadata_test.go` | `d617bbe8b82407553b6ba1eb73632ac7165072d7a5b87948a2165a19343c357f` |
