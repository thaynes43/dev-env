# DESIGN-041: Upgrade attempt claims

- **Status:** Proposed, 2026-10-31. Design only; nothing is activated.
- **Tracking:** OpenProject [#325](https://work.haynesnetwork.com/work_packages/325),
  Epic #173, Saga #85. Work order `OVERNIGHT-UPGRADECLAIM-325-DESIGN`;
  lead `dev-env-1029-132039` reviews the ready PR. Do not merge it in this story.
- **Inputs:** #325 journals 4060, 4065, 4066, 4075 and 4076. Journal 4075
  corrects the gap report's invalid key and proposed Job owner. The lead
  revised its key in the review of this PR: the repository leaves the task
  key so the store's rename guard applies (section 2). Journal 4076 records
  source merge, not runtime acceptance.
- **Source examined:** dev-env `6c7f344ada642a9d92b61075c35c393ff8a4aac1`;
  haynes-ops `073f0778b02806b313c261847429fe440a5ae2b4` (PR #4498).

## 1. Outcome and existing boundary

A v2 Session that performs an upgrade attempt owns one canonical TaskClaim.
The PR carries an immutable reference to that claim. A consumer joins the
reference to an authorized canonical read. Claim ownership never implies
approval, successful vetting, merge authority or a successful deployment.

The existing pieces are [DESIGN-010](010-atomic-task-claims.md),
[TaskClaim types](../../../../api/coordination/v1alpha1/taskclaim_types.go) and
[the API](../../../../internal/apiserver/taskclaims.go). The API derives the
Session owner and actor from authenticated identity. Atomic Create chooses
one owner. A stopped or deleting owner keeps its claim. This design adds no
release, takeover or retention exception.
Host registration and child ownership checks are in
[coordinator.go](../../../../internal/apiserver/coordinator.go).

The [#4498 provenance record](https://github.com/thaynes43/haynes-ops/blob/073f0778b02806b313c261847429fe440a5ae2b4/.github/workflows/pr-disposition.yml)
already logs the GitHub repository, PR number, current head and matching vet
comment reference. Its `attempt` holds `headSha` and `vet`; `vet` holds
`commentId`, `sha` and `verdict`, or is null. It logs `claim: 'Unknown'` with
`claimBasis: 'no-supported-upgrade-claim-actor'`. Shepherd-authored PRs get a
log-only record. These records are read-side provenance, not claim integration.
The next natural schedule supplies runtime observation; this story triggers none.

## 2. Identity: the lead's settled decision

Use this exact tuple:

```text
project:    haynes-ops
repository: <catalog repository name>
taskKey:    task:upgrade.pr<N>.<head40>
```

`repository` is the selected catalog name, such as `haynes-ops`, rather than
`thaynes43/haynes-ops`. Resolve the GitHub name through that catalog identity
when joining the PR record. `N` is the canonical positive decimal PR number,
without leading zeros. `head40` is the full lowercase 40-hex PR head SHA.
The repository is not repeated in the task key. The identity already carries
it, and the store's rename guard (`sameWork`, `internal/taskclaim/store.go`)
matches the same project, GitHub repository and task key under another
repository key. A repository-specific task key would change on a catalog
rename and let a second Session claim the same PR and head.

For example, haynes-ops PR #4475 at its recorded head has this key:

```text
task:upgrade.pr4475.4a4ff22e9d83689c6de1d0318bbcbb0b68c1b6cc
```

The CRD accepts task keys matching
`^task:[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?$` and limits the full key to
133 characters. Dots, `pr`, decimal digits and the SHA fit the task-key
character set. The body starts with `u` and ends with a hex digit. The key
length is `56 + digits(N)`; the example is 60 characters, and up to 77 PR
digits fit. Validate the full length before claiming. Reject an overlong
key; never truncate the SHA or hash a different task key.

A head change makes a new key and a new claim. Old claims, vet verdicts and
other evidence remain bound to the old head and are never reused. A Job UID,
timestamp or work-order key does not create a second claim for the same
repository, PR and head.

Stopping does not end ownership; there is no release or takeover
(`taskclaim_types.go`). A retry at the same head by the same Session gets
Confirmed. Any other Session gets Conflict and must not act, even after the
owner stops. Only coordination retention, off in the live operator, can later
archive and delete a claim whose owner has been absent at least 30 days; the
same identity can then be claimed by a new owner (DESIGN-024). Otherwise
proceeding needs a new head or a path outside this design.

## 3. The Session must actually perform the attempt

Only a v2 Session that performs the vet, merge decision or permitted merge
owns the claim. Its accepted project snapshot must select project
`haynes-ops` and the exact repository. It acts under existing haynes-dev-bot
rights and the unchanged merge policy. Manual carve-outs and explicit holds
still apply.

No Session may lend its identity to a Shepherd run it did not perform.
A Job pod is never a Session. Natural Shepherd CronJob runs therefore have
**no owner under this design**. A free-text `predictedOwner`, a successful
Job or a `shepherd-vet` marker cannot supply one.

The schema permits actor kinds `session` and `coordinator`, but every owner
is still a `SessionIdentity`. A registered coordinator may create a claim
only for its own verified child Session. The in-session producer here,
option (a), uses its own Session identity.
Options (b) and (c) remain Tom's decision in section 6.

## 4. Producer contract

The Session reads the PR's full current head and constructs section 2's identity.
Before performing the attempt, it calls `agentd task-claim-create` with only
that identity. The existing client takes bounded JSON on stdin. The server
derives owner and provenance; the caller does not provide them.

On `Created` (HTTP 201) or `Confirmed` (HTTP 200), verify the returned
identity and own Session namespace, name and UID. Retain the returned claim
name and UID as the producer receipt. Then write one PR comment containing
this exact marker:

```text
<!-- upgrade-claim name=<claim> uid=<uid> sha=<head40> -->
```

The comment also records the owner namespace, name and UID from the receipt,
so a consumer can compare owner attribution. Comment authorship and prose
alone never establish ownership. The canonical read must confirm them.
haynes-ops is public, so anyone can post a marker. Consumers consider only
markers authored by the producing account, as #4498 filters vet comments by
`VET_AUTHOR` (`pr-disposition.yml`). Authorship is a candidate filter, never
proof. A marker from another author is ignored, not a conflict.
The marker and receipt are immutable. Do not edit an older marker to a new
head or substitute another claim UID. Same-owner confirmation reuses the
existing marker instead of posting duplicates.

On Conflict (HTTP 409), or when ownership is Unknown, write no marker and do
not act. The API's outcome is only Created, Confirmed or Conflict; error
replies carry no outcome. Unknown means a
503 "task ownership is unconfirmed" (`taskclaims.go`), a reply the client
rejects as unconfirmed, or a lost transport. A 429 LimitExceeded and other
refusals also stop the attempt. A lost acknowledgement is Unknown even if
creation may have succeeded. It gives no permission to proceed or choose a
new key.

Recheck the PR head before writing the marker and before any action. If it
changed, stop the old attempt. Claim the new identity before proceeding.
If marker publication is unconfirmed, stop pending confirmation of that
same marker; do not duplicate it or perform the attempt on uncertain evidence.

The claim's `provenance.reportedHead` is a Session heartbeat observation.
It is not proof of the PR head. The exact task key and PR marker bind this
attempt to the PR head.

## 5. Consumer join

Extend #4498's existing record with claim observation and canonical owner
metadata. Keep the vet reference and verdict separate from the claim.
Do not parse `predictedOwner`, labels, a work-order status or Job output
into ownership.

A consumer uses only an existing authorized TaskClaim read. Today no GitHub
Actions classifier can read TaskClaims. #4498 runs on a GitHub-hosted runner
with no in-cluster identity, and `claimReadScope` admits only a live Session
whose saved snapshot selects the project, or a registered coordinator host.
So #4498's `claim` stays Unknown, even with a current-head marker, until a
read path is designed and approved separately. Q-325-01 does not create one.
The in-session read
`agentd task-claim-get --project haynes-ops --claim <claim> --uid <uid>`
works only from a live haynes-ops project Session or a registered
coordinator; a dev-env Session is refused. This design gives the classifier
no new identity, token or TaskClaim access.

| Observation | Claim state |
| --- | --- |
| Marker SHA equals the PR's current head. A canonical read returns the same claim name and UID, the full section 2 identity, and the same Session owner namespace, name and UID as the producer receipt. | **Available**; copy canonical identity, reference and owner. |
| Marker belongs to an older head. | **Superseded** for that historical attempt. Never attach its owner or evidence to the current head. |
| Marker is missing, malformed or ambiguous; read is denied, unavailable or unconfirmed; or any identity, UID or owner comparison fails. | **Unknown** for the current attempt. Never infer Unclaimed. |

Recheck the PR head after the join. A head race invalidates the current
observation. Conflicting current-head markers are Unknown; do not choose
one by comment order. An older-head marker may remain as Superseded while
the current attempt is Unknown or has its own Available claim. A read that
returns `archived: true` is not Available; record it as Unknown.

Available means the ownership join succeeded. Owner phase and stop state
keep their existing uncertainty. It does not mean an owner is still running,
a HOLD was lifted, the vet approved this head, CI passed or merging is allowed.
No replacement Session inherits ownership through name reuse.

## 6. Exact pending decisions for Tom

Both entries are pending. Neither is staged or authorized by this document.
The #325 lead delivers them through the owner-question route. This design-only
worker records them for that handoff and does not claim delivery or an answer.

**Q-325-01 — Enable haynes-ops claim scope?** Change the operator argument
from `--task-claims-read-projects=dev-env` to
`--task-claims-read-projects=dev-env,haynes-ops` in haynes-ops
[`kubernetes/main/apps/dev-env-system/operator/app/helmrelease.yaml`](https://github.com/thaynes43/haynes-ops/blob/073f0778b02806b313c261847429fe440a5ae2b4/kubernetes/main/apps/dev-env-system/operator/app/helmrelease.yaml).
Tom may approve that exact activation or leave it disabled.

This widens claim writes as well as reads to eligible haynes-ops Sessions.
`createTaskClaim` calls `claimReadScope`, which checks this allowlist before
creation. The same code requires the owner's saved project/repository snapshot,
current catalog authority and live Session/Pod identity. Configured coordinators
also gain metadata scope and may create only for their verified child owners.
This is project-wide claim scope, not a grant limited to upgrade keys.

At the examined haynes-ops revision, `--enable-task-claims=true` is already
set. Together with the existing API and `agentd` client, the sources show that
this one allowlist change is the only additional claim-authority activation
needed for option (a). No new principal, CRD, RBAC or coordinator registration
is needed. A later writer implementation and runtime acceptance remain separate.

The claim still needs a Session launched as a haynes-ops project task
(`--project haynes-ops`) with no shared workspace; only that route writes the
project snapshot that `claimSnapshot` requires. Some existing haynes-ops
Sessions have no snapshot and cannot claim. It also needs room under the
TaskClaim limits: 512 per owner Session and 4096 per project
(`internal/taskclaim/store.go`), a 429 when exceeded. Each head uses one
record, and with coordination retention off, nothing frees that room.

Rollback is a reviewed GitOps revert to
`--task-claims-read-projects=dev-env` in the same HelmRelease, preserving any
other independently approved scopes if the baseline has changed. Removing
haynes-ops refuses its claim API reads and creates. It does not delete existing
claims, release their owners or erase PR markers. Consumers without a confirmed
read return Unknown. Do not roll back the schema or operator image for this.

**Q-325-02 — How, if at all, may natural Shepherd runs participate?** Leave
both alternatives undecided:

| Option | Exact authority effect |
| --- | --- |
| (b) Add a new automation actor class. | Requires a separately designed schema and API authority change. A Job could act as itself only under a new authenticated automation contract. If the Job is also to own the attempt, the Session-only owner contract must change too. A new actor enum alone cannot make a Job a Session owner. No such write or ownership right exists today. |
| (c) Register Shepherd's ServiceAccount as a coordinator host. | Widens the operator's `--coordinator-hosts` trust configuration. Existing host checks require a dedicated ServiceAccount, declared Pod name and host ID, matching live Pod UID and host annotation. Listing a ServiceAccount alone does not register arbitrary CronJob pods. The registered actor could create claims for its own verified child Sessions, never own claims itself or select a foreign Session. A real child must perform the attempt to meet this design's owner rule. This does not confer GitHub merge rights. Registration admits the whole coordinator class, not a claim-only right. The host may also create child task Sessions and read, message, suspend and resume them, use task budgets, and read in-scope directory, context, inbox and history metadata (coordinator routes in `internal/apiserver/server.go`). Today's binding needs a stable Pod name (`coordinator.go`), which CronJob Job pods never have. So (c) needs a stable-named host Pod or a separately designed host-binding change, not only a `--coordinator-hosts` entry. |

Tom may choose either future design or keep natural runs without canonical
ownership. This document chooses neither. Option (c) must account for changing
CronJob Pod names and the existing host-binding rules; it cannot silently relax
them or invent a phantom child owner. Q-325-01 does not approve (b) or (c).

## 7. Non-goals and acceptance boundary

No CRD change, RBAC change, Shepherd script change, merge-policy change or
writer implementation is included. There is no claim creation, activation,
cluster write, workflow trigger or upgrade attempt in this story.

Acceptance for this work is one ready docs-only PR containing this Proposed
design and one saga README entry. The lead reviews authority claims against
the named sources. No tests run. `make check-layout` checks Go package entries
in CLAUDE.md; these two documentation changes do not touch its inputs.

This is proposed source documentation. It is not built code, a deployed
integration or usable/accepted upgrade ownership. Producer, consumer and
activation work need later bounded authorization and their own evidence.
