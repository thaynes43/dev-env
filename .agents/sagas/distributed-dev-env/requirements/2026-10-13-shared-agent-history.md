# Shared history for Tom's trusted dev-env agents

**D-92, owner clarification recorded October 13, 2026 (EDT).** The Mac
coordinator relayed this direction into the existing Chapter 5 session. The
original owner turn was not directly observed here; preserve that provenance.
This is an owner requirement, separate from the metadata capture's outcome.
It opens no new architecture or review project and consumes no question ID.

## Required behavior

All of Tom's trusted dev-env agents may read shared agent history. They do not
need denied-read isolation from one another. Per-agent or cross-agent read
denials within that trusted set are not a parity gate or an activation goal.
Positive retrieval by trusted peers is the acceptance target.
Project labels remain useful provenance and search filters; they do not require
a read boundary between Tom's trusted agents in the shared history pool.

Reuse the existing authenticated, admitted agent and configured coordinator
identities. Do not silently add external identities, namespace wildcards,
anonymous/public access or cluster RBAC. Keep credential exclusion and secret
handling intact. Shared read access does not grant publication replacement,
archive mutation, HOME deletion, owner overrides or unrestricted volume access.
Integrity, authorized write/delete paths and their existing safeguards remain.
V1 remains unchanged under Q-35. No consumed canary or attempt is reused.

## Bounded delivery plan

1. Record the independent result of the existing GET-only capture as its own
   bounded metadata/configuration checkpoint. It cannot decide read-sharing
   policy or substitute for a working history read.
2. Preserve one approved history sample through the existing export/archive
   path, then retrieve the same preserved bytes through the authenticated
   history API from two trusted agent callers using existing authenticated
   access. Keep the sample private and publish only digest/accounting evidence.
   Use the shared history pool and compare its recorded digest; do not build a per-agent
   denial matrix. Keep the existing bounded persistence/restore and secret-
   exclusion checks, and the reviewed resource limits needed to run them safely.
3. Record the installed read and recovery result. Keep deletion off until its
   existing archive-verify/read gates hold. Refuse public/anonymous and external
   access; preserve write/delete integrity without adding another isolation
   project between Tom's agents.

H8 still needs an approved non-canary source; original canaries remain retained.
This clarification does not bypass F4's credential exclusions, invent missing
writer evidence, grant a retry or enable a feature flag by itself.

## Identified security-sensitive implementation change

Before the D-92 source correction, `internal/apiserver/history.go:historyScope`
called `contextCaller` and separately required a Session's saved project to equal the requested
project, within `ContextReadProjects`. That was narrower than shared history
across Tom's trusted agents. The identified implementation changes only
**history read authorization** for already authenticated trusted callers.
Projectless histories are included. The existing exact configured API clients,
live Sessions and configured Coordinators gain shared history reads; existing
authentication/classification and live Session checks remain. Configured clients
retain their existing trust without a new Pod/Session fence. Empty list project
selects the existing projectless bucket, not an all-project merged index.

This history-only change was identified to the owner before implementation.
Preserve `contextCaller`'s authentication and live identity checks and the separate
publication/write/delete checks. Leave `contextReadScope` unchanged. Do not
remove a shared helper's checks in a way that broadens write authority.
No cluster RBAC change is proposed here;
identify any actual new identity, API privilege or trust membership separately
before executing it. The source change enables no running flag by itself;
history reads remain off pending installed archive acceptance.

DESIGN-013 retains its separate published-context scope; DESIGN-025 section 8
records the history-only correction. D-92 supersedes any requirement to prove
denied reads between Tom's trusted agents. Anonymous/external refusal remains required.

Relay evidence is retained privately as
`dev-env-h7-archive-guard-1013/d92-shared-history-owner-clarification-relay-r1.json`.
Its SHA-256 is
`10409282db165fd5c294ee63ecc1623daf628356f680eca660f10ba2bdea6e01`.
The capture report and its acceptance remain separate evidence. The
[2.28.0 delivery record](../verification/d92-shared-history-delivery-2026-10-13.md)
records the source correction, signed publication and verified operator/template
pins; runtime reads remain off and the sample/read acceptance remains pending.
