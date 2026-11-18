# ADR-032: Admit ordinary Codex hosts by observed headroom and bounded intent

Date: 2026-11-18. Status: Proposed. Tracking: OP922.

DESIGN-069 records the investigated create/select paths and the limits of today's
status. Keep reuse first. Add a passive container memory sample so a full host
can yield another host, and a human/trusted-client-only fresh request for an old
host that cannot report it. Never interpret a launch lease as thread capacity.

A profile may hold at most two ordinary retained hosts through this route;
pending reservations count and an uncertain write retains one slot. CAS appends
reservations; their snapshot precedes the live list, so concurrent commit/release
cannot disappear from admission. Definite API refusals release their own slot.
Preserve the fleet running cap, all protected/workbench exclusions and launch leases. Enforce
the same decision at API creation as in CLI preflight. Rescue restore skips
reuse selection and keeps the fixed profile/fleet bounds. Record fresh intent on
the new session; add no unconditional keys to legacy response documents.

The reserve is less than 1 GiB or less than 20% remaining, from a fresh
current-pod cgroup v2 working-set sample (`memory.current` minus
`inactive_file` in `memory.stat`, floored at zero). Unknown capacity keeps existing reuse behavior.
It is not native thread admission and cannot prevent memory growth or direct
phone launches. New agentd is needed for automatic capacity; explicit fresh
intent lets a trusted client request room without restarting an old host.

An absent uncertain create is not expired or manually cleared: a safe reset
needs independent fencing and a later recovery contract. ConfigMap RBAC in the
configured policy namespace and the haynes-ops CRD copy are prerequisites.
GitOps ships the ledger empty with no `pending` key and IfNotPresent/prune-disabled
annotations. The operator never creates it and needs only named get/update.
A missing, forbidden or malformed ledger is typed `capacity_ledger_unavailable`
for counted create/resume, including rescue restore; reuse/report continue.
Rescue-blocked and unresolved-account ordinary hosts count toward retention.
An unknown outcome is named `create_unconfirmed` with its session and attempt.

Coordinator rulings dated 2026-11-18: R1–R4 in OP922 comment 9217 and G1–G9
fix-round dispatch in comment 9228, detailed in DESIGN-069. These are story-owner
rulings by dev-env-fable-coordinator-1109, not Tom. Q-069-01 and Q-069-02 are
answered by R1 and R2; the fenced-reset follow-up is OP925. Status stays Proposed.

No lifecycle action, deployment, pairing or change to the named workbench
coordinator is authorized by this decision. Claude Opus reads the PR before
merge; source passing tests is not installed or owner-accepted behavior.
