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
and publisher. A conflicting or damaged entry is never overwritten.
`CoordArchive.Get` reads an exact revision under the shared lock, verifies the
stored manifest, full identity, canonical bytes, size and digest, and returns no
manifest or object on failure. It reads neither staging debris nor other revisions.

The focused suite ran once after correcting a fixture that confused a valid
missing-revision query with an invalid ingestion byte count:

```sh
GOMAXPROCS=2 GOFLAGS=-p=1 flock -w 60 /tmp/dev-env-go-tests.lock \
  nice -n 19 go test -p 1 -count=1 -run '^TestCoordArchive' \
  ./internal/agentd ./internal/agentd/protocol
```

The exact cached Go 1.27.2 toolchain was used. All 13 test methods and their finite
subtests passed: agentd 0.113s, protocol 0.053s. The seven new filesystem methods
cover all four record kinds, original-manifest idempotence, changed UID and
resourceVersion forms, two concurrent identical puts, invalid input without
writes, object/manifest damage, missing files, symlinks and hard links, a mismatched
complete digest with the same directory prefix, invisible interrupted staging,
missing mount and linked directory refusal. All fixtures use temporary local
directories; no model or Kubernetes client is invoked.

This primitive is not the complete R2 command. Indices and crash repair, absence
events, bounded lookup/list, latest-revision resolution, CLI and shelf startup
wiring remain. R3–R7 and real consumers remain separate. No deployment, API,
permission, archive/deletion activation, v1 behavior or retirement gate changed.
