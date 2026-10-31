# DESIGN-042 section 6.6 source continuation (OP337)

Source preparation only. All new flags default empty; no live configuration or
activation is authorized. This increment reuses S2's authenticated decision route,
protected annotation, broker and cleanup. S5/S3 Secret integration and S7 cases
remain under their separate owners.

`--grant-policy-delegates` is a bounded subset of `--grant-delegates`. A designated
actor may replace tree membership with a live delegated policy that covers the
exact requester and grant scope. Self, requester-descendant and maintenance
participant refusals remain. Verified bounded ancestry is retained by digest;
missing, cyclic, excessive or replaced ancestry refuses authority.

The decision and broker status retain the selected policy name, UID and effective
delegated specification digest, requester session and Pod UIDs, and ancestry
bindings. API prewrite, broker apply and Active retention share the checker. A
changed policy, identity, ancestry or list ends the lease through normal cleanup.
Transient reads neither authorize access nor clear a valid Pending annotation.

Legacy eligibility preserves the existing session identity and profile and binds
an explicit session/Pod/ServiceAccount tuple on both services. ADR-017 records
the proposed form3-only same-baseline-account exception and its trust-root
residual. The coordinator authorized source preparation of this amendment;
runtime approval remains held. Policy reach alone confers no actor eligibility.

No source fixture is installed or usable acceptance. The final proposal must state
literal new configuration, risks, independent review and CI evidence. Runtime
RBAC, admission, flags, policies, templates, grants and credentials remain held.
