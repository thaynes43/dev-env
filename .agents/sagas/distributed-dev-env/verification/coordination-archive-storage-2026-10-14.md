# Coordination archive immutable storage — 2026-10-14

Scope: DESIGN-024 section 4, the second bounded R2 source slice for #175.
The ingestion foundation merged in [#281](https://github.com/thaynes43/dev-env/pull/281),
commit `4e67b87f03db48fb60948937c5273f6f03b57e92`.

`CoordArchive.Put` validates full canonical object identity, size and SHA256
before writing. Under an exclusive `.shelf.lock`, it writes private object and
manifest files into `.incoming`, fsyncs both files and their directory, then
renames the complete revision and syncs its parents. It reuses the existing
history archive's mount, lock, staging, publication and bounded no-link read
helpers. Existing directory links are refused. Administrator access remains
trusted; no new isolation boundary is claimed.

The path includes resource, project, record name, UID, resourceVersion and the
first 16 digest characters. The complete digest remains in the stored manifest.
An identical put returns the original manifest, including its publication time
and publisher, after re-syncing the verified files and publication directories.
That repairs a previous rename whose parent fsync failed. A conflicting or
damaged entry is never overwritten.
`CoordArchive.Get` reads an exact revision under the shared lock, verifies the
stored manifest, full identity, canonical bytes, size and digest, and returns no
manifest or object on failure. It reads neither staging debris nor other revisions.

The first focused run corrected a fixture that confused a valid missing-revision
query with an invalid ingestion byte count. Native independent review then found
a durability gap in the existing-revision branch. The fix retries publication
syncs before returning success. A per-archive sync hook in the existing publisher
allows finite fault injection without changing default history behavior. The
final focused suite includes the affected history archive regression cases:

```sh
GOMAXPROCS=2 GOFLAGS=-p=1 flock -w 60 /tmp/dev-env-go-tests.lock \
  nice -n 19 go test -p 1 -count=1 -run '^(TestCoordArchive|TestHistoryArchive)' \
  ./internal/agentd ./internal/agentd/protocol
```

The exact cached Go 1.27.2 toolchain was used. All 14 coordination test methods,
their finite subtests and the affected history archive regressions passed:
agentd 4.251s, protocol 0.054s. The eight new filesystem methods
cover all four record kinds, original-manifest idempotence, changed UID and
resourceVersion forms, two concurrent identical puts, invalid input without
writes, object/manifest damage, missing files, symlinks and hard links, a mismatched
complete digest with the same directory prefix, invisible interrupted staging,
missing mount, linked directory refusal, and a real rename followed by an injected
parent-fsync failure. The identical retry repairs every publication directory and
preserves the original bytes; continued sync failure still refuses success.
All fixtures use temporary local
directories; no model or Kubernetes client is invoked.

This primitive is not the complete R2 command. Indices and crash repair, absence
events, bounded lookup/list, latest-revision resolution, CLI and shelf startup
wiring remain. R3–R7 and real consumers remain separate. No deployment, API,
permission, archive/deletion activation, v1 behavior or retirement gate changed.
