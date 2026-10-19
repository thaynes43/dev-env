# Owned-root operator replacement correction — 2026-10-19

Existing Saga85/Epic107/Story110, same sole owner and original thread/HOME.
The bounded eighth live attempt preserved its Pod but stopped its native process
with `budget authority unavailable` during operator replacement. It is retained,
not retried. That reason combines remote authority and local access sync; the
actual failed phase is unknown. Safe phase-only warnings separate future failures.
No tokens, URLs, projections or native auth contents enter diagnostics.

Real-TLS regressions reproduce two independent source defects: a reused
connection lost before response headers fails observation despite an available
replacement, and the old ten-second API shutdown returns before an existing
sixteen-second native request can finish. Observation alone gains the standard
HTTP transport's replayable-request marker (not transmitted); it still requires
a new complete exact-binding response within its original two-second context.
Admission and mutations do not gain retries. Server drain becomes seventeen
seconds without extending request/task deadlines. Local access sync is unchanged.

Regression coverage retains denial, revocation, identity, malformed/truncated
status, cancellation/deadline, lost-admission and fresh-connection failure refusal.
The real owned gate uses healthy synthetic access to isolate the remote failure.
Serving-until-withdrawal tests require fresh authority and fresh revocation on
both retained and new connections; they do not prove Kubernetes route timing.
Deployment must keep the old listener serving while endpoints withdraw, within
the existing termination grace. A fixed preStop interval alone is not acceptance.

Scope excludes credentials/RBAC/trust/pairing, model/provider calls, new framework,
and held v1/rendezvous/H7/grant actions. Tests run serially, two Go CPUs under nice19;
no load/stress/wide loops. Source/CI/publication/installed/live acceptance remain
separate. Counters, stop receipts, finite budgets and prior attempts are retained.
