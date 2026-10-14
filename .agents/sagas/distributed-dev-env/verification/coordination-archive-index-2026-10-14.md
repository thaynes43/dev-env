# Coordination archive index publication and repair — 2026-10-14

Scope: DESIGN-024 section 4, the next bounded R2 source unit for #175 after
[#283](https://github.com/thaynes43/dev-env/pull/283), merge
`cf91ef4e94f8de5da7e78c42a335a228209e5b7f`.

Under the existing exclusive shelf lock, a put publishes the UID's all-record
index and, for InboxMessage only, the recipient index. The immutable JSON carries
resource, namespace, project, name, UID, optional recipient UID and first archive
time. It carries no object text or mutable latest-revision pointer.

The first time comes from this UID's stored revision manifests, read in batches
of 128; the selected revision's full canonical object is verified. A later
revision is clamped to that time when the clock moves backward. The index name
therefore stays fixed across later revisions and different served forms.

Each missing index is written privately in its own incoming directory, fsynced,
and published with an exclusive hard link. Existing entries are verified before
any missing index is published; identical entries are re-synced. A staged index
inode has at most two links until the later age-bounded incoming cleanup. Damaged
entries, substituted identities, symbolic links, extra hard links and linked
directories are refused. No existing index is overwritten and no stored record
is removed.

Validation used the exact cached Go 1.27.2 toolchain:

```sh
GOMAXPROCS=2 GOFLAGS=-p=1 flock -w 60 /tmp/dev-env-go-tests.lock \
  nice -n 19 go test -p 1 -count=1 -run '^TestCoordArchive' \
  ./internal/agentd ./internal/agentd/protocol
```

All 21 methods and their finite subtests passed: agentd 0.457s, protocol 0.063s.
Seven new methods cover all four kinds, first-time retention across a month and
clock rollback, actual revision rename and both index-link publications followed
by injected fsync failure, missing-entry repair, corruption/link refusals,
recipient substitution, reclaimed UID separation and the 129-revision directory
batch boundary. The existing durability fixture now compares the immutable
revision separately from the newly repaired indices. All data is synthetic and
uses local temporary directories; no model or cluster client is launched.

Focused native and available advisory reviews found no blocking source issues.
Native review noted that directory creation order does not prove batch placement;
the corrected fixture chooses the oldest revision from the actual entry beyond
the first enumeration batch. Runtime source bytes are unchanged by that fix.

This completes index publication and repair in source. R2 still needs the
append-only absence-event journal, bounded lookup/list, latest-revision selection,
command and shelf startup wiring, and old incoming cleanup. R3–R7 and installed
acceptance remain separate. No permission, live test, deployment, archive/deletion
activation, v1 behavior, release or cutover change is included.
