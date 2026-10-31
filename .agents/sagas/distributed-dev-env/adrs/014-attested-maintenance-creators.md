# ADR-014: Restrict attested maintenance to sessions launched by listed creators

- **Status:** Accepted 2026-10-31 for source preparation only; runtime activation held.
- **Amends:** [ADR-013](013-attested-maintenance-approval.md), top-level requester eligibility.
- **Tracking:** OpenProject #330, origin #233; DESIGN-042 S6.
- **Authority:** Tom's DESIGN-042 source direction through the coordinator's released-work handoff. This records eligibility, not a requirement for a human runtime approval actor.

A requester may attest a maintenance grant only when its live AgentSession has an explicit depth label `0` and `spec.parent` exactly matches a ServiceAccount in `--attested-maintenance-creators`. The parent is the creator identity recorded at launch. It is not the identity executing the maintenance task or recording the approval message.

When unset, the list defaults to `--maintenance-human-service-account` if that is a valid namespace/name. An explicitly empty list admits no creator. The parser accepts at most 16 distinct namespace/name references and sorts them. Missing parents, child-session parents, missing or noncanonical depth labels and unlisted creators fail closed.

The same rule runs before approval, before creating the executor and while its lease runs. Removing the creator ends the approval: a running executor enters StopPending, its deadline is shortened, and cleanup waits for proof it ended. The grant never reports successful cleanup merely because deletion or a stop request was sent.

All other ADR-013 requirements remain: a bound requester attestation, exact policy envelope, identity checks, no overlapping maintenance lease and the existing expiry/release tail. The authenticated human fallback and independent delegate route remain separate. A child cannot self-attest maintenance, even when its parent was launched by an allowed creator.

The concrete `haynes-ops-1029-223503` launch shape is depth **0**, parent `dev-env-system/dev-env-human`. DESIGN-042's earlier depth-1 wording is an indexing error; children of sessions have depth 1 or more.

Source tests exercise that launch shape, configurable creators, unlisted v1/workbench/coordinator creators, child and missing parents, empty lists, bad depth labels and revocation before and during execution. These are offline source checks. No flag, policy, template, grant or credential is activated by this ADR. S6 must be installed before enabling attested maintenance; do not roll back past it while that flag is enabled.
