# OP354 source checkpoint, 2026-11-02

This branch preserves Grok-authored authenticated-instruction API, agentd and
CLI work. It is not ready to merge, release or install. Canonical work remains
[OP354](https://work.haynesnetwork.com/work_packages/354).

The original source passed focused race tests in agentd, agentrun and
apiserver. Repository-pinned controller-gen v0.22.0 regenerated the artifacts.
An actual Kubernetes 1.35.0 envtest rejected the CRD: per-item `oldSelf`
transition rules occur inside an uncorrelatable atomic instruction list.
The schema requires a keyed, bounded representation that preserves immutable
delivery records, one-time consumption and existing-record retention, with
real schema regression tests. Removing those checks is not a fix.

A later Grok pass added native lifecycle and explicit read/consume plumbing
and CLI retry/correlation regressions. Those additions are preserved here
without a test-pass claim. They did not correct the invalid API schema.
The one-shot native runner still lacks the completed instruction-key discovery
and model-visible checkpoint receiver path. A known-key API read or a daemon
receipt does not establish actual native consumption.

Next: correct and regenerate the schema, qualify the new source/tests, finish
the supported discovery/receiver path without inventing a fake ACK, then obtain
final-head review and CI. Installed authenticated delivery and a nonmutating
native consumption/correlated-reply proof remain required. Existing grants,
credentials, quota ingestion, observer/canary and storage holds are unchanged.
Private worker reports and provider transcripts are excluded from this branch.
