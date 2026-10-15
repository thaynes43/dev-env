# C7-R4 inbox archived-metadata list — source checkpoint

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

## Local evidence

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

The candidate has not been independently reviewed, pushed, PR'd, released or
merged. Chapter7 remains the sole coordinator and acceptance authority.

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
