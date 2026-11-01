# OP323 production entrypoint, 2026-11-01

Source only. No activation, no Git publication, and no claim that tests
were executed in this worker. Sol runs the frozen artifact tests after
handoff. This record does not establish provider account identity or
completed data ingestion.

## What changed

`cmd/dev-env-operator` can now construct the existing
`apiserver.ProviderSourceReports` and its existing
`ProviderSourceReportStore`. The Server field was already optional.
`run` assigns it only inside the existing API block, from the manager
client as the store writer and the API reader as both live readers.
Construction performs no ConfigMap read, write, list, or watch.

With the enable flag false and the selection empty, the field stays nil
and the route stays unregistered. Enable without a selection, or a selection
while disabled, refuses startup. Maximum age alone does not enable it.

## Configuration contract

Flags, both default off, neither installed in a manifest:

```text
--enable-provider-source-reports
--provider-source-config=<inline JSON, not a file path>
```

A valid enable also requires the API (`--api-bind-address` not `0`) and
`--session-namespace=dev-agents`. It does not change
`--enable-portal-reader`.

Exact JSON:

```json
{"slot":"codex-current-source","publisherRef":"codex-get-usage-limits-observation-publisher","producerIncarnation":"codex-retained-tool-source-1","sourceGeneration":1,"maxAgeSeconds":1800}
```

`maxAgeSeconds` may be any integer from 1 through 1800. The other four
values are exact. Unknown, duplicate, missing, non-integer, non-positive,
and too-old settings, including 1801 and 86400, fail closed. The only
error text is `provider source configuration was refused`.

Fixed, and not JSON fields:

- `GET` and `PUT` `/v1/provider-source-reports/codex-current-source`
- ConfigMap `dev-env-system/dev-env-provider-source-codex-current`
- key `report.json` (`apiserver.ProviderSourceDataKey`)
- publisher `dev-agents/dev-env-1027-010058`, UID `84ddc461-fd07-47ab-a2c1-72910898bac4`, ServiceAccount `dev-env-workbench`
- portal GET ServiceAccount `dev-agents/dev-env-v2-portal`, still gated by `--enable-portal-reader`
- current pod checked per request; no historical pod UID
- closed report at most 16 KiB; `providerUpdatedAt` remains null on this slot

The store still only gets and resourceVersion-CAS-updates an existing
object. This slice adds no ConfigMap, Role, binding, schema, collector,
or provider probe.

## Checks expected later

Package tests, not run here:

- `TestProviderSourceConfig` parses the real flags. Valid 1 and 1800
  second selections build the real handler. Bad documents, max age alone,
  API off, and a non-`dev-agents` session namespace stay nil and do not
  echo the submission.
- `TestProviderSourceEntrypoint` mounts that handler on `Server.Handler`
  over a fake client. A synthetic slot passes the existing auth fence.
  Nil stays off with no slot read. Caller, portal, generation,
  incarnation, and non-null `providerUpdatedAt` fences hold. Retrieval
  and the used/remaining shape stay as stored. A one-second age suppresses
  without rewriting. List, watch, create, delete, and patch stay at zero.
- `TestProviderSourceMissingObject` reads Unknown and refuses the write
  when the object is absent. It does not create the ConfigMap.

No live cluster, provider, or credential is involved. No apiserver test
seam was added: the exported store is assigned to the existing handler
field from the operator package.

## Integrator qualification

Exact worker artifact SHA256 `3da75d8b02ca608d86249a72241f2fc3df739321efbed6d5d43e06332c70310c`
was imported and six producer hashes verified before mechanical gofmt of the new test.
Worker source-only tests were unrun. The terminal receipt at 17:57:30Z
exceeded the requested 17:55:48Z deadline; elapsed-bound compliance is not claimed.
Root local fake-client tests and native exact-head review are recorded separately.
No runtime flags, object, role, binding, collector or reader were activated.
