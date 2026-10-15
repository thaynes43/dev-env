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
