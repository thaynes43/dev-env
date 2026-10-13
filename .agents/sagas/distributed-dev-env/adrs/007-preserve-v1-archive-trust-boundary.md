# ADR-007: Preserve v1 during archive migration

- **Status:** Accepted 2026-10-13 as the narrow record of the Q-35 owner answer
  relayed through the oversight conversation; D-89. The coordinator observed
  the relay, not the original owner turn or native phone-prompt delivery.
- **Date:** 2026-10-13
- **Decider:** Tom, as quoted in the relay; no independent authority is granted
  by a peer message.
- **Amends:** [ADR-006](006-private-home-expiry-corrections.md) section 2's
  proposed restriction of v1 archive access and C-42's v1 denial/privacy
  consequence. The other corrections stand.
- **Retains:** Q-31's archive-before-delete gates, Q-33's permanent personal
  Kubernetes account, and separate approval for v1 decommissioning.
- **Design:** [DESIGN-025 section 20](../designs/025-private-home-expiry.md#20-h7-archive-trust-boundary-q-35)
- **Tracking:** [#261](https://github.com/thaynes43/dev-env/issues/261),
  [#91](https://github.com/thaynes43/dev-env/issues/91)

## Context

ADR-006 proposed denying shelf access to every ServiceAccount except the v2
operator, including v1's existing administrator identities. The H7 read-only
audit also found that v1 can create a Job using the operator ServiceAccount or
exec into an operator Pod with projected credentials. A direct shelf guard
therefore would reduce v1 access without establishing technical isolation from
v1. No borrowing probe or token read was made.

The relayed question was: "During migration, can v1 retain trusted admin
access, or must the archives be technically isolated from v1 before use?"
The quoted owner answer was:

> v1 must remain the same until it’s decommissioned after the MVP has been accepted

The native question submission was accepted, but delivery was never verified.
The coordinator read the separate oversight relay containing this question and
answer; reading the original oversight thread returned no turns. Record this
as a relayed owner answer, not a directly observed native answer. The local
receipt `q35-relayed-answer-r1.json` has SHA-256
`b8b96a86eb81628733dd4e86068f029b9b99fd7eeed38842f5e01988d7b32903`.

## Decision

Keep v1's behavior, configuration and current access unchanged during
migration. Accepted MVP precedes decommissioning; the existing separate owner
approval for decommissioning still applies. This ruling constrains compatible
v2 work already authorized. It grants no new rights and authorizes no automatic
decommissioning, archive activation or destructive action.

H7's admission guard protects the archives against v2 agents and untrusted
ServiceAccounts, including the v2 broker and keeper. It preserves v1's existing
identities (`dev/dev-env` and `upgrade-agent/dev-env-ops`) and legitimate
controller-created descendants of their authorized workloads. Human users,
the actual Flux shelf reconciler and the v2 operator retain their existing
roles. A controller exception must establish the authorized parent chain;
an arbitrary owner reference, label or selected operator ServiceAccount is
not proof that a v2 caller is trusted. No new RBAC grant is included.

Both archives remain reachable through v1's current administrator powers and
existing cluster, node, storage and hypervisor administrator routes. The guard
must not claim privacy or technical isolation from those trusted actors. A
scoped workflow scan showing no shelf use does not authorize removing an
unused v1 permission. The test matrix proves compatible v1 paths remain
available, alongside refusal of untrusted v2 paths; ADR-006's proposed v1 shelf
denial and DESIGN-024 R7's corresponding denial expectation are superseded.

Tom's personal Kubernetes account remains permanent under Q-33. Headlamp retires
after full v2 adoption. Preserving its existing administrator backend does not
make that shared ServiceAccount an owner-override identity. Current personal
authentication and the exact-user override policy still require their own
evidence; the email stays outside public manifests.

## Consequences

- **C-44:** v1 is a trusted administrator during migration. History is not
  protected from that existing access; neither direct denial nor credential
  borrowing restrictions may change v1 under this ruling.
- **C-45:** implement and review the compatible guard before mounting the
  archive. Test actual admission expressions with explicit old/new and CONNECT
  inputs; ordinary Kyverno CLI fixtures do not establish those cases. Installed
  webhook acceptance remains separate.
- **C-46:** the unused history PVC, owner-set PV `Retain`, F4 classifications,
  both cold resource measurements, default-off flags, non-canary H8 source and
  installed archived-read acceptance remain independent gates. Nothing here
  enables a flag or permits deletion, retry, cleanup or privilege expansion.
