# Collector incarnation handoff: source qualification

Status: incomplete source preparation. Public apply is deliberately closed.
This record grants no runtime authority and is not evidence of an actual capture,
deployment, approval, or acceptance.

DESIGN-049 defines capture headers as caller-owned provenance, and says sequence
is stable per producer and incarnation. The aggregate contract permits a
caller-selected new incarnation; neither document equates that value with a Pod
UID. DESIGN-053 separately binds the collector fingerprint to its selected host
and trust. Accordingly this draft treats a retained producer incarnation as the
same caller-selected logical stream only when the entire selected trust and
account/workspace scope are unchanged and sequence continues; it does not infer
that a physical Pod is the producer incarnation.

The collector's helper has a separate per-Pod runtime ledger under
`XDG_RUNTIME_DIR`; its receipt is bound to Pod UID and fingerprint. The durable
ConfigMap journal is not a copy of that receipt. A successful existing runner
transition to idle follows a validated cleanup-proven terminal receipt, but the
journal itself does not retain that receipt. Because the old Pod was replaced,
this source-only session cannot independently re-read that old runtime ledger.
No evidence of the old ledger or old Pod termination has been inspected here.
An independently qualified terminal old-ledger witness and a way to validate it
are unresolved. No timeout, empty new runtime directory, hash, or approval file
is treated as proof. Public apply therefore refuses, and the CLI refuses before opening a kubeconfig.
No PR is offered as a complete implementation.

The offline preparation draft parses both configurations with the real
collector parser, fences the original journal and report, and computes the
replacement fingerprint without changing other journal bytes. Its internal
apply prototype is reachable only from synthetic tests while public apply is
closed. The test runs the existing Runner with fake objects across an idle
cycle-56 handoff, including a simulated lost update acknowledgement, restart without replay,
one natural subsequent cycle, and refusal of the old plan afterward. It also exercises selected policy, UID, expiry,
backup, and version refusals. Tests are synthetic and do not establish provider
quota, old-ledger state, cluster permissions, or runtime acceptance. CLI and
recovery coverage remains incomplete.
