# ADR-017: Proposed exact legacy delegate identity and designated policy reach

- Status: Proposed source design; activation held
- Date: 2026-10-31
- Canonical source story: OP337, Epic112/Saga85; integration OP336
- Source clarification: coordinator transport010058, `COORDINATOR-REPLY-2.md`
  in the private reach/legacy handoff. No runtime risk acceptance is claimed.
- Extends ADR-015; does not edit its Accepted record or ADR-016.

The running legacy Claude lead has full profile, depth1 and the shared baseline
ServiceAccount. It is not S4 named-coordinator admitted. The separate requester
has full profile, depth0 and the human ServiceAccount as its immediate creator.
A new coordinator fixture, reparenting or restart cannot satisfy those outcomes.

Add default-empty `--grant-policy-delegates`, a bounded subset of
`--grant-delegates`, on API and broker. Empty preserves current tree-only
behavior. Designated decisions replace tree membership only with exact delegated
policy coverage. Bind policy name/UID/effective digest, requester session/Pod
UIDs, actor session/Pod UIDs, designation and complete bounded ancestry. Retain
all scope, TTL, live actor and separation checks. Recheck before recording,
applying and retaining Active access. Read failures retry without granting or
clearing a valid Pending record. Policy/list/identity loss invokes normal cleanup.

Add default-empty `--grant-legacy-delegates`, at most four exact tuples:
`namespace/sa:saUID:session:sessionUID:pod:podUID`. Only the configured baseline
account in the session namespace is accepted; it must also appear on both
lists. Every tuple binds a full-profile depth1 Claude session and its same-name
Running controlled Pod. TokenReview supplies the authenticated account UID and
Pod UID; both services also read the live ServiceAccount UID. Other holders of
that shared account receive no delegate eligibility, including other actor forms.
Caller classification, session profile and runtime identity are unchanged.

**Explicit proposed amendment:** for form3 only, exact session/Pod/SA separation
replaces S2's broad same-account requester refusal. Otherwise the required two
existing sessions, which share the baseline SA, can never use delegated approval.
Self, same Pod, requester-descendant and maintenance-participant refusals remain.
The exception is never applied to host/named-session forms. Removing/replacing
the tuple or either list revokes the grant. This is a source proposal, not an
assertion that the old same-account invariant stayed unchanged.

A root able to exec into the actor Pod or mint its bound token can act as that
actor. Pod-bound authentication does not establish human identity, verify a
message author or constrain such roots. The new broker/API ServiceAccount UID
reads require separate reviewed runtime RBAC; no permission is applied here.

S7 may reuse the explicit typed reserved-authority seam after validating an
enabled exact case/revision/digest on every pass. The ordinary policy checker
refuses reserved authority. No policy failure falls through to a reserved case.
This source increment implements no reserved case or new API route.
