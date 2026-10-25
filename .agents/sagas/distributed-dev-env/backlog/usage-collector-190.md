# Story190 C1: small read-report projector

**Status:** source-only projector, 2026-10-25. This is not a collector,
provider adapter, command, API, portal, scheduler, registry, schema platform, or
deployment. It does not accept Epic187, Story188, native188, Cursor, Grok, or
mandatory full-V1 coverage.

## Included

- a bounded pure Go report over existing validated allowance read snapshots and
  initial-invocation usage observations
- exact caller-provided session UID labels only
- deterministic duplicate/conflict suppression and JSON-safe copied DTOs
- source documentation, focused tests, and the saga/layout pointers

## Excluded

Storage, collection, provider calls, raw sessions or private logs, auth, account
association, aggregation, pricing, availability decisions, HTTP/CLI/RPC wiring,
portal presentation, scheduling, new registry/JSON Schema, and changes to
UsageVersion2 or Story109.

## Acceptance

Focused package tests prove snapshot preservation, Unknown/zero/reset handling,
requested versus served-model provenance, idempotent replay, conflict and
permutation handling, exact/missing/conflicting labels, scope separation, limits,
non-aliasing, JSON roundtrip, and no I/O or clock surface. Run focused
`providerallowance` and `protocol` tests, `gofmt`, layout, and diff checks under
the normal serialized CPU limits. A ready PR and independent exact-head review
remain delivery gates; merge/release/deployment are excluded.

## C2: private on-demand exporter

C2 adds only `agent-run usage-export`: a caller-supplied finite manifest of
exact Session-name and expected-UID bindings, plus optional caller-provided
`providerallowance.read.v1` snapshots. It reads each selected Session once with
the existing provider attribution and direct-v1 capture request headers, no
redirect or retry. It rejects malformed or duplicate selections before a
connection, validates the returned name/UID and direct source UID, normalizes
the existing capture view, and hands validated DTOs to this C1 projector.

The command performs no list, descendant scan, history, log, console, provider,
credential, storage, scheduler, portal, or API change. A refusal, mismatch, or
malformed response stops later selected reads and records only safe coverage
metadata; it never serializes the raw Session or backend error body. Its new
exclusive output directory is mode 0700; `report.json` and `summary.txt` are
mode 0600, created without overwrite or symlink traversal. They are private
bounded evidence, not public artifacts, live monitoring, or full Story190/V1
acceptance. See the saga decision log for the Story190 boundaries.
