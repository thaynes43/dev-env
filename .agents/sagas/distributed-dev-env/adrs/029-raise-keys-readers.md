# ADR-029: The raise signing keys can be read by the cluster's Secret readers; admission and the ledger are the fence

- **Status:** Accepted 2026-11-13 by the coordinator session
  `dev-env-fable-coordinator-1109`, on a finding of the cross-family review
  of the haynes-ops install rules (DESIGN-066 section 15.4). Nothing it
  decides is built or installed: no key exists and no flag is set.
- **Date:** 2026-11-13
- **Decider:** The coordinator session `dev-env-fable-coordinator-1109`,
  under OpenProject #830. This is the coordinator's ruling, not Tom's. It
  changes nothing in what Tom approved (story 830 comment 8085).
- **Supersedes:** in [ADR-028](028-raise-a-running-session.md), only the
  words of decision 5 that the record is signed "with a private key only the
  operator reads", and the part of decision 6 that leaves "who else can read
  a private key" to an install check without saying what the check accepts.
  ADR-028 stays Accepted and is not edited. Everything else in it stands.
- **Amends:** [DESIGN-066](../designs/066-raise-a-running-session.md)
  Ruling 2, safety rule 14, sections 6.6, 7.5, 11 (install rules 2 to 6) and
  12 (known limit 7). The review and its rulings are in section 15.4.
- **Tracking:** OpenProject [#830](https://work.haynesnetwork.com/work_packages/830).

## Context

ADR-028 signs every raise record with an Ed25519 key that the operator keeps
in a Secret, and every notice outcome with a key that the keeper keeps in a
Secret. It said the operator's key is one "only the operator reads", and it
listed as not determined which accounts can read Secrets in `dev-env-system`.

The review of the install rules (Grok 4.7, haynes-ops PR 4888,
`~/.shared/handoffs/op830/install-review-grok.md`, finding 3) resolved the
cluster's RoleBindings and ClusterRoleBindings without reading any Secret:

- Beside each key's owner and the cluster's admin subjects, about 36
  controller ServiceAccounts hold get, list or watch on Secrets across the
  whole cluster: Flux's controllers, cert-manager, external-secrets, the
  storage and observability operators and others.
- Some of them can also update Secrets.
- Admission does not run on a read. No ValidatingAdmissionPolicy can fence
  one.

So the keys are not owner-only to read, and the design cannot make them so
without changing the rights of controllers it does not own.

## Decision

1. **The design does not claim the keys are owner-only to read.** The
   cluster's existing cluster-wide Secret readers are accepted as able to
   read both raise keys. They can already read every other Secret in
   `dev-env-system`.
2. **A key alone raises nothing.** A record counts only when it is the
   annotation on the session and matches the operator ledger, and a notice
   outcome only when it is in the keeper's ledger. Admission lets only the
   operator write the record and its ledger, and only the keeper write its
   ledger (DESIGN-066 section 11, rules 1, 3 and 4). Those rules are the
   fence. They are installed and checked before any key is written and
   before any flag is set.
3. **The signature is the second line.** It keeps a forged or edited record
   from counting while one admission rule is missing. With the record rule
   and the ledger rule both missing, an account that can read the operator's
   key and write both objects could forge a record that counts. That is a
   stated limit (DESIGN-066 known limit 7).
4. **Writes of the key Secrets are owner-only by admission.** Install rules 3
   and 4 also refuse anyone but the owner an update or delete of its key
   Secret, so nobody else can plant or replace a key before or after the
   owner's first start.
5. **The install check names every reader in three classes:** the owner; the
   cluster's admin subjects; accounts with the right across the whole
   cluster. A subject in none of them is removed, or accepted by name with a
   reason, before a key is written. The record is on story 830.

## Alternatives rejected

| Alternative | Why not |
| --- | --- |
| Narrow Secret read in `dev-env-system` to the owners | It means changing the cluster-wide rights of about 36 controllers this project does not own. |
| Keep no private key at rest: each process signs with a key held only in memory and publishes its public key | Every restart would need the old public keys kept so that live records still verify, across two operator replicas. Trust would rest on who may write the public-key ConfigMap, which is the same admission fence as today. More parts, no stronger fence. |
| Drop the signature and rely on admission and the ledger alone | The signature still stops a forged record while a rule is missing, against every account that cannot read Secrets. |

## Consequences

| # | Consequence |
| --- | --- |
| C-80 | Good: the design states what the keys protect and what they do not, and the install check can pass on the cluster as it is. |
| C-81 | Good: no account but the owner can plant or replace a key. |
| C-82 | Bad: an account with cluster-wide Secret read can read both keys. With the admission rules in place it can sign but not raise. |
| C-83 | Bad: removing the record rule and the ledger rule together, which takes a cluster admin, leaves only the cluster's own access control between a Secret reader and a forged raise. |

## What I could not determine

- Whether any of the accepted readers ever calls get on these two names. The
  review checked the right, not the use.
- The live objects of the newer Kyverno policy kinds
  (`policies.kyverno.io`). Listing them was refused for the reviewer and for
  the coordinator. In git none matches Secrets or ConfigMaps.
