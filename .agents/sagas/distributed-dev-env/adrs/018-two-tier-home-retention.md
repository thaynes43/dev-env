# ADR-018: Evidence-based two-tier HOME retention

- Status: owner policy accepted; source implementation and runtime acceptance separate.
- Decision: Tom, 2026-11-01 21:11:37 UTC, direct source work order.
- Tracking: OpenProject [350](https://work.haynesnetwork.com/work_packages/350),
  linked to [102](https://work.haynesnetwork.com/work_packages/102) under Epic 99;
  historical delivery issue [174](https://github.com/thaynes43/dev-env/issues/174).
- Supersedes ADR-005/006 and DESIGN-025 only for HOME retention duration,
  completion clock, and universal archive-before-expiry prerequisites. Accepted
  ADRs remain historical. ADR-004 coordination retention, ADR-007 trust boundaries,
  ADR-008 marking behavior, finalizers, canary/pilot holds and unrelated guards
  are unchanged.
- Also supersedes ADR-003's universal history-archive-before-HOME-deletion
  wording only: required history/recovery and handoff/result material still
  needs explicit preservation, with unknown requirements a named exception.
- Design: [DESIGN-044](../designs/044-two-tier-home-retention.md).

Completed work with machine-verifiable publication and preservation evidence may
be proposed for immediate cleanup. Success exit, PR existence and a Git rescue
boolean alone do not prove preservation. Recovery or potentially needed stopped
work has a maximum seven-day retention window with a pre-expiry warning. At
168 hours unresolved or unknown work enters a visible owner decision batch;
expiry never means permission to delete and does not create indefinite hidden
retention. Active sessions remain protected. Missing clocks are explicit owner
decision exceptions, never replaced with PVC creation time or a new scan time.

The primary clock is the provider task's recorded `finishedAt`, bound to the
exact Session, executor attempt and PodUID. Actual terminated recovery without
task completion uses its recorded executor terminal `observedAt`, retaining that
fallback provenance. Required handoffs/results and explicitly required history
or recovery data need preservation evidence. A provider-history archive is not
a universal prerequisite, and credentials/caches need not be retained forever.
Unknown data requirements are named exceptions.

Existing cleanup requires specific owner approval of a UID-bound destructive
batch. No unattended irreversible purge is authorized. This slice implements a
pure policy/proof verifier and finite offline dry-run only. It adds no collector,
runtime flag, message delivery, mounts, deletion path, finalizer exception,
activation or deployment. A later collector and any destructive action require
their own bounded integration and authority; this ADR grants neither.
