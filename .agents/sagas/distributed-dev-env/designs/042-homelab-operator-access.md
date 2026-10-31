# DESIGN-042: Homelab operator access activation bundle

- **Status:** Proposed, 2026-10-31. Revised four times that day after reviews by the
  lead, the coordinator, the advisory reviewer and an independent Codex reviewer.
  Design only. Nothing is granted, activated or changed by this document: no policy,
  guard, role, flag, template or cluster object.
- **Tracking:** OpenProject OP233 (#233). Epic
  [#112](https://work.haynesnetwork.com/work_packages/112), Saga #85. Related: #214,
  #219, #304 (optional agent auto-review, a separate layer), #319 (MAM), #113. Lead
  `dev-env-1029-132039` reviews PR #782. Do not merge it in this story.
- **Inputs:**
  - Tom's four corrections of 2026-10-31 (section 1) and his #214 words of 2026-10-26;
  - lead and coordinator reviews, including the S5 decisions on PR #785;
  - the Codex review at `~/.shared/handoffs/op233-codex-review/findings.md`
    (CODEX-REVIEW-DESIGN-042, verdict REQUEST CHANGES).
- **Source examined:**
  - dev-env `34624a4dd7ecc2a19edb2d76db96c22fd71e2b33` and main
    `2984528d78e54f3dfd77cf12755f2f389a075dff` (#781 merged); PR #785 head;
  - haynes-ops `09614e825a5ab102cca0a9d18bd6259b91c6e0ec`, plus #4501 and #4502;
  - live objects, read on 2026-10-31 between 13:50 and 14:23Z with the session's
    read-only identity.
- **ADR bookkeeping (decided):** ADR-013 (#781) amends ADR-012 for attested
  maintenance. When Tom approves this bundle, ADR-014 records:
  - the two approval modes;
  - the reserved-decision cases;
  - the namespace-operator class.

  No Accepted ADR is edited.

`DES/` below means haynes-ops `kubernetes/main/apps/dev-env-system/`. `D001` means
[DESIGN-001](001-dev-env-v2.md). "Authenticated actor" means a TokenReview-verified
identity recording its own decision. "Agent assertion" means an agent's unverified
statement that someone else approved.

## 0. Recommended activation scope

Tom approves **one bundle, once** (Q-042-01). Until its flags, roles and policies land,
it changes nothing at runtime. Every item is either default-off source or GitOps data
with a rollback.

**The risks Tom accepts by approving it:**

1. **Eligibility.** Every `full` and `workbench` session, in any repo, may get the
   class grants of section 5 once an approval is recorded. Today only `full` sessions
   in `haynes-ops` can, and only for single hosts.
2. **Mode (a)** is the requester's own attestation. It is an agent assertion: a claim,
   not authentication of the approver. A session that lies gets no more than the
   matching policy allows, for its TTL.
3. **Mode (b) and reserved decisions.** Listed delegates may decide other agents'
   grants through the authenticated API. A delegate is an authenticated actor. When a
   delegate relays Tom's answer, Tom's consent is still an agent assertion. With S7, a
   delegate who cites Tom's message may also execute these reserved cases (RD1, bounds
   changes, already runs through the existing delivery workflow):
   - break-glass;
   - Secret read;
   - one bound storage delete;
   - recording a hardware operation.

   A delegate cannot decide its own or an ancestor's request. Only Tom's human or
   client caller may launch a delegate session. The Claude lead becomes a delegate
   only after P6, which waits on the template hold.
4. **Namespace operator.** A grant in named app namespaces may exec into pods, restart
   workloads, and run Jobs that mount the Secrets that grant names.
   - **Wider than it looks:** a delegate may approve any Secret name in those
     namespaces that is not on the policy's deny list.
   - **Exposure:** exec reaches every Secret already mounted in that namespace's pods,
     and whatever those pods' SAs can read. So the class effectively grants read of
     those Secrets. "No Secret get or list" does not mean "cannot read Secrets".
5. **Hardware.** Eligible sessions may lease two credentials for 1h:
   - the shared hw-ssh key, which is root on PiKVM and HaynesTower and sudo-allowlisted
     on the PVE nodes;
   - a Proxmox operator token, which is root-equivalent.

   Release removes the files. It does not recall a copy or stop a running process.
   Destructive hardware operations remain a convention, not a broker-enforced
   boundary.
6. **GPU.** A message may approve SYS_ADMIN maintenance on Card C, within #781's
   exact-envelope policy.
   - Under mode (a), only a session that a listed creator launched directly may
     self-approve (S6). Child sessions never do.
   - #781 as merged admits any top-level session: headless tasks from the v1-pod
     client, the workbench client or the coordinator-host fixture.

**Persistent deltas** (grouped into tracks below):

| # | Repo | Change | Status | Rollback |
|---|---|---|---|---|
| S1 | dev-env | Attested maintenance (ADR-013), flag `--enable-attested-maintenance` | **merged** as `2984528d` (#781); default off | flag false |
| S2 | dev-env | Delegate decisions (6.3): `--grant-delegates`; `POST /v1/grants/{name}/decision`; GrantPolicy `spec.delegated`; `delegate/<ns>/<sa>`; self, ancestor and no-ref decisions refused; covers maintenance extensions | new; default off | empty the list |
| S3 | dev-env | `agent-run grant decide` and an MCP tool; `secrets`, `targets` and `allNamespaces` on requests; a "no policy covers this" notice | new, small | revert |
| S4 | dev-env | Named coordinator identity (6.4): templates accept a `dev-env-coordinator-<name>` SA; the pod stays a session caller; only human or client callers launch it; one Running session per SA | new, small | revert |
| S5 | dev-env | Named Secrets (5.1): `KubeGrant.secrets` in `ScopeDigest`; role `dev-env-grant-namespace-operator`; policy `kube.secretDenyList`; the broker writes a per-grant Role holding `use` on exactly the named Secrets | **PR #785, open.** Its param-only guard must change to the fail-closed check in 5.1. | revert |
| S6 | dev-env | Attested maintenance creators (6.2): broker flag `--attested-maintenance-creators`, default `dev-env-system/dev-env-human`; depth 1 | new, small; **required before P2 sets `--enable-attested-maintenance`** | reset to the default |
| S7 | dev-env | Reserved-decision cases (7.1) in the S2 route: RD2, RD3 (named and cluster-wide), RD4 storage, and RD4 hardware as audit intent only. Each binds grant UID, exact scope or target, TTL and a mandatory Tom-message ref. Flag `--reserved-decision-classes`, default empty. | new | empty the flag |
| R1 | haynes-ops | CRD copy from each release that carries S2 to S7 | routine | remove the new-field policies first, then revert |
| R2 | haynes-ops | Operator and broker pins | routine | pin back |
| P1 | haynes-ops | Ten GrantPolicies (9.1) | **activation** | delete a policy |
| P2 | haynes-ops | Broker `--enable-attested-maintenance=true` (after S6); `--grant-delegates=<list>` on operator and broker; `--reserved-decision-classes=rd2,rd3,rd4` | **activation** | false; empty |
| P3 | haynes-ops | `dev-env-maintenance-annotations` also allows the operator's set-once decision annotation on kube and egress grants | **activation** | revert |
| P4 | haynes-ops | ClusterRole `dev-env-grant-namespace-operator`. The broker may create per-grant Roles `grant-<id>` that hold only `use` on named Secrets; the broker guard pins their shape. Bind and catalog entries. | **activation** | revert; per-grant Roles go at release |
| P5 | haynes-ops | Identity guard: per-name `use` checks (fail closed). VAP `dev-env-grant-scope`: target UIDs for storage deletes. | **activation** | revert; references and deletes refused again |
| P6 | haynes-ops | SA `dev-agents/dev-env-coordinator-lead` with exactly `dev-env-agent`'s bindings; agent-guard match extended to it; a `coordinator` profile in `dev-env-templates` | **blocked by the global template hold** | revert |
| P7 | haynes-ops | ClusterRole `dev-env-grant-breakglass` (7.1, RD2) | **activation** | delete it; RD2 is refused again |

**Independent tracks inside the one approval.** Tom's single approval covers every
track. Each track lands on its own prerequisites, and no track waits on an unrelated
one. Every track ships as a held haynes-ops PR for Tom's review, after its own R1 and
R2.

| Track | What it delivers | Deltas | Prerequisites | When it activates |
|---|---|---|---|---|
| **T0** Policies on live machinery | mode (a) class grants for C1 (app namespaces), C3 and C4; the widened `kube-nodes-attested` | the attested-only P1 policies: `kube-workloads`, `egress-cluster-services`, `kube-nodes-attested`, `lan-ssh`, `lan-pve-api`, `credential-hw-ssh-attested`, `credential-proxmox-attested` | none: D-88 is live | **at once**, with its PR |
| **T1** A real Claude coordinator | the lead runs under its own SA | S4, P6 | S4 source; Q-042-02 | when S4 is pinned and the exception is granted |
| **T2** Delegated message approval | mode (b) for kube, egress and maintenance | S2, S3; P2 `--grant-delegates`; P3; `delegated` blocks; `kube-workloads-infra` | S2 and S3 source | when S2 and S3 are pinned. It serves coordinator hosts at once, and the lead after T1. |
| **T3** Pod exec and named Secrets | C2 | S5 (PR #785, with the fail-closed change), P4, P5, `kube-namespace-operator` | S5 source; T2, because the policy is delegated-only | when S5 is pinned and T2 is live |
| **T4** GPU maintenance | C5 under mode (a) | S1 (merged), S6; `maintenance-card-c`; P2 `--enable-attested-maintenance` | S6 source | when S6 is pinned. Mode (b) for GPU follows T2. |
| **T5** Reserved cases | RD2, RD3, RD4 storage, and RD4 hardware as audit only | S7, P7, P2 `--reserved-decision-classes` | S7 source; T2 | last; it blocks none of T0 to T4 |

RD1 (bounds changes) is in no track. It already runs through the existing delivery
workflow (7.2). This bundle's own PRs use that workflow, approved by Tom's answer to
Q-042-01.

**Already live** (args read at 13:50Z):

- broker: `--enable-attested-grants=true`, `--enable-maintenance-grants=true`;
- operator: `--enable-grant-attestation=true`, `--enable-maintenance-api=true`.

**Existing human-only gate.** The maintenance decision route (`kindHuman`, recorded as
`human/<sa>`) stays as a fallback. S1 and S2 remove the dependence on it once they are
activated.

**Not in scope:** refusing a ref reused across digests; OP304 auto-review.

**The decision left for Tom:**

- **Q-042-01:** approve this bundle, with the six risks and the tracks above.
- **Q-042-02:** grant a template-hold exception for the `coordinator` profile only,
  with no restart of active sessions.

Nothing else is asked.

## 1. Outcome and Tom's correction

Tom, 2026-10-31:

1. "Nothing we do should be human only what the actual fuck do you think we are
   building an AGENTIC OS for?"
2. "We can't be playing wack a mole I explained already that agents will need access
   to everything it's a home lab!"
3. "I do not approve on the mac I or an agent can approve with a message to the agent
   requesting the grant."
4. "Agents typically do the prowlarr side for me. This was a standard procedure in
   dev-env v1 so it should be no trouble since you made v2 parity".

On #214 (2026-10-26): "I want a streamlined way agents can ask either their
coordinator ... or a user ... for access to anything they need in cluster AND online
and the coordinator or user can gauge the risk and approve or deny - user must be able
to do this from mobile".

These correct implementation drift. They do not remove safety. Decision authority over
reserved classes stays with Tom. No internal class may remain executable only through a
human actor once Tom has approved by message. The bundle reuses scoped grants,
per-grant identities, TTL, release, audit and D-88.

It adds:

- one authenticated decision route, with reserved cases;
- one reusable namespace role;
- named Secrets and targets.

The acceptance tests are GPU (A1), MAM through a grant (A2), and the reserved-class
matrix (A3).

## 2. Inventory: what exists today

### 2.1 Approval paths by grant type

| Grant type | Standing policy | Attested policy (D-88) | Other decision | Live policies (13:50Z) |
|---|---|---|---|---|
| kube | yes | yes | none. Only `internal/broker/maintenance_decision.go:32` calls `Decide`. | `kube-nodes-attested` |
| egress | yes | yes | none | `lan-ceph-mgr-metrics`, `lan-internal-ingress`, `lan-pve-api-lb`, `lan-pve-api-nodes`, `lan-ssh-tier`, `egress-haynesintelligence-ssh-attested`, `egress-pikvm-ssh-attested` |
| credential | yes | yes | refused (`seams.go:151`) | `credential-proxmox`, `credential-hw-ssh-attested` |
| maintenance | refused | on main `2984528d`, attested only and default off; extensions still need the human route (`maintenance_attestation.go:59-60` on main) | only `dev-env-system/dev-env-human` (`internal/apiserver/maintenance.go:36`; `seams.go:143`) | none |
| breakglass | refused (`match.go:79`; CRD `:463`) | refused | none; the role is not in git | none |

- Roles `dev-env-grant-breakglass` and `dev-env-grant-secrets-read` never match
  (`match.go:120`; CRD `accessgrant_types.go:573`). Profile `ops` never matches
  (`match.go:83`).
- `KubeGrant` holds only `role` and 1 to 16 `namespaces` (`accessgrant_types.go:248-266`).
  The Secret-read role is namespaced and bound by RoleBindings (`kube.go:55-79`), so no
  grant can read Secrets cluster-wide.
- `Broker.Decide` is an internal seam, not a public approval service. For
  non-maintenance grants it refuses empty and `policy/` approvers, but it does not
  require a `human/` prefix (`seams.go:127-167`).
- `dev-env-human` is the token Tom mints on his laptop (`D001:340-342`). A grant that
  no policy matches waits 30 minutes, then is denied by `timeout` (`broker.go:65`).

### 2.2 Human-only gates and caller classes

| Route | Gate | Cite |
|---|---|---|
| `POST /v1/grants/{name}/maintenance-decision` | `kindHuman` only: the one concrete human-only route in scope | `maintenance.go:36` |
| task budget `extend`, `continue`; native control | `kindHuman` (out of scope) | `taskbudgets.go:207, 347`; `native_control.go:41` |

- **How callers are classified.** `resolveCaller` (`caller.go:121-139`) checks, in
  order: Tom's human SA, workbench session pods, clients, the shared session SA
  `dev-agents/dev-env-agent`, then coordinator hosts.
- **Coordinator hosts.** A coordinator host needs its own SA, a pod with that exact
  name, a live UID and the `coordinator-host` annotation (`coordinator.go:64-76`).
  Live, there is one: `dev-agents/dev-env-owned-native-fixture`, a Codex fixture.
- **No Claude coordinator exists.** Every Claude session runs as the shared SA, so none
  can call as a coordinator. A coordinator-classified pod is also never a session
  caller.
- **No coordinator decides grants today.**

### 2.3 Attestation as built (D-88)

- Only the requesting session may attest (`grant_attestation.go:24`). It binds:
  - the grant UID, scope digest and TTL;
  - the attestor's session, session UID and pod UID, taken from the token
    (`attest.go:81-90`).
- Each grant takes one statement, within 30 minutes. The ref is checked for syntax
  only (`attest.go:52-54, 93-102`).
- `ScopeDigest` (`attest.go:162`) covers type, credential, role, namespaces, egress
  targets, TTL and session. #781 adds the whole maintenance envelope.
- The broker re-verifies the statement against the live session and pod
  (`attestation.go:48`). It writes `approvedBy policy/<name>` (`broker.go:222`) and an
  Event (`broker.go:533-536`). An approved grant is phase **Active** (`broker.go:253-259`).
- Known gap (`D001:4163`): kube and egress execution never re-read `approvedBy`.

### 2.4 Grant identities, catalog and guards

| Piece | What it does | Cite |
|---|---|---|
| Kube grant identity | SA `dev-agents/grant-<id>` with no automount; catalog roles bound by RoleBinding or ClusterRoleBinding; token minted and installed in the pod's tmpfs. Revoke deletes the bindings, then the SA. | `kube.go:34-90, 137-175`; `installer.go:52-64`; `broker.go:266-287, 482-496` |
| `dev-env-grant-workloads` | Namespaced. CRUD on workloads, Jobs, pods, services and configmaps. No Secrets, no exec. | `DES/rbac/app/grant-catalog.yaml:24-51` |
| `dev-env-grant-storage` | Namespaced. Create and delete PVCs and VolumeSnapshots; delete StatefulSets. | `grant-catalog.yaml:56-74` |
| `dev-env-grant-secrets-read` | Namespaced. Secret get and list. | `grant-catalog.yaml:79-88` |
| `dev-env-grant-nodes` | Cluster-wide. Node patch and pod eviction. | `grant-catalog.yaml:93-114` |
| `dev-env-grant-breakglass` | Named but **not in git and not live** | live NotFound |
| `dev-env-agent-guard` | Matches only `dev-agents/dev-env-agent`. Jobs run as `default` and mount only `*-volsync-aws-secret`. Workloads: restart only. | `agent-guard.yaml:57-60, 67-68, 175-182` |
| `dev-env-identity-guard` | Matches every `dev-agents` SA except the workbench. No writes in the dev-env namespaces; protected objects (RBAC, webhooks, CRDs, dev-env objects) locked; pods run as `default`; **no new Secret reference**. | `identity-guard.yaml:50-54, 176, 254, 267-296` |
| `dev-env-broker-guard` | The broker touches only `grant-*` objects and binds only catalog roles. | `broker-guard.yaml:52-56, 78-119` |
| `dev-env-maintenance-annotations` | Only the operator sets the decision annotation, once, and only on maintenance grants. | `maintenance-annotations.yaml:16-35` |
| Maintenance executor | A bare Pod pinned to the node UID, with no token, only the named capabilities, and deny-all network. Control-plane nodes are refused. | `maintenance_executor.go:27-96`; `maintenance_scope.go:142-151` |
| Egress grants | CNP `grant-<id>`: FQDNs, CIDRs or in-cluster endpoints, up to 16 ports | `internal/egress/policy.go:37-93` |
| Templates | A profile may name an SA, but only `dev-env-workbench` is accepted | `templates.go:270-273, 491-492` |

The baseline `dev-env-agent` role has these verbs:

- cluster-wide `pods/exec`, `services/proxy` and `pods/proxy`;
- Job create and delete;
- pod delete;
- restart and reconcile patches.

It has no Secret read (`agent.yaml:78-118`). Exec is refused in the dev-env and
controller namespaces, and into pods whose SAs are on the Kyverno exec guard's
privileged list. Prowlarr is on neither list, so the baseline may already exec into
it. This design does not use that route.

### 2.5 Live evidence of the drift

| Object (13:50 to 13:56Z) | What happened |
|---|---|
| `grant-1031-033915-fc19`, `grant-1031-132422-84ca` (maintenance, Card C, SYS_ADMIN, 10m) | Both Denied by `timeout`: "no answer in 30 minutes" |
| `grant-1031-033808-7327` (same, without SYS_ADMIN) | Released before an answer |
| `grant-1030-122127-d7d9`, `grant-1030-130136-9897` (kube nodes, workbench) | Denied by `timeout`. `kube-nodes-attested` names only `full` and `haynes-ops`. That is the whack-a-mole. |

## 3. v1 baseline against v2 today

v1's role `dev-env-operator` (haynes-ops `kubernetes/main/apps/dev/dev-env/app/rbac.yaml:16-136`)
is the template for `dev-env-agent`. D-70 also counts Headlamp's `cluster-admin`
(`D001:3947-3960`).

| # | Capability | v1 | v2 today | Gap and fix |
|---|---|---|---|---|
| — | Reads without Secrets; exec into app pods; proxy; pod delete; restart; reconcile; suspend; force-sync | yes | yes, guarded (D-19) | none |
| G1 | A Job that mounts an app Secret | yes: no admission policy matched `dev/dev-env` except the token guard (live read) | refused | **gap**: C2 (S5, P4, P5); R-04 P-05 |
| G2 | Runtime workload spec changes | patch | restart only | deliberate (D-19) |
| G3 | Capability pods on a node (GPU) | via Headlamp `cluster-admin` | human route only | **approval gap**: S1, S2, S6 |
| G4 | Hardware: 8 SSH hosts, PVE API, Proxmox operator token | mounted; CNP open (`networkpolicy.yaml:441-487`) | workbench: standing policies. full: two attested hosts, haynes-ops only. | **gap**: P1 |
| G5 | Omni | Admin key | Admin key in `full` and `workbench` | none |
| G6 | In-cluster app APIs | a fixed list plus internal ingress | platform tier plus `lan-internal-ingress` | small: P1 |
| G7 | Approval outside a standing policy | not needed | narrow attested policies; human route for maintenance; everything else times out | **main gap**: S2, P1, P6 |
| G8 | Break-glass, Secret API read, destructive storage | via Headlamp `cluster-admin` | **no executable path for anyone** | **gap**: S7 (section 7). Bounds changes are not a gap: they already run through the delivery workflow. |

The v1 MAM route is **Unknown**: no runbook describes it. `MAM_ID_PROWLARR` is
mounted in no pod (`kubernetes/main/apps/downloads/qbittorrent/app/helmrelease.yaml:165-169`),
so exec alone cannot reach it. The likely v1 route was G1.

## 4. Prior records: drift or deliberate

| Record | Restriction | Verdict | Action |
|---|---|---|---|
| Q-16 ruling, `D001:5338` | "Standing grants cover parity"; "Human approval gates only capabilities beyond today's" | Governs | This bundle |
| D-70, `D001:3947` | "Do not add a new approval gate to an existing capability" | Governs | Same |
| `D001:348` | "only Tom (on the console) can approve a grant" | Drift | Superseded; modes (a) and (b) |
| D-26, D-27 | Tom approves on a page | Superseded (Q-16, D-70) | S7 gives D-27's restricted break-glass a message route |
| D-54, D-61: breakglass and secrets-read refused by schema and match | Beyond OPERATOR | Deliberate for policies | Keep. S7 adds per-decision cases, not policies. |
| CRD `:463` refuses maintenance policies | Human-only GPU | Drift | S1 (merged) |
| ADR-012 | Human identity decides maintenance | Corrected by quote 3 | ADR-013, then ADR-014 |
| D-88 policies scoped to one repo and one host | One scope at a time | Drift: whack-a-mole | P1 |
| D-19 baseline guards | Runtime writes go through git | Deliberate | Keep |
| Volsync-only Secret allowlist | Blocks R-04 P-05 and MAM | Drift | C2 |
| Templates accept only the workbench SA | One non-default identity | Blocks the lead's mode (b) | S4, P6 |
| Workbench-only LAN policies (Q-110-08) | Hardware only in workbench | Drift now | P1 |
| [DESIGN-034](034-agent-asks-and-approvals-board.md) | Only a portal human answers an Ask | Deliberate for Asks | Grants never wait on the board |
| [DESIGN-039](039-access-policy-change-guard.md) | Consent for authority changes; D-88 and relays are not machine-verifiable consent (G039:212-230); Q-269-03 pending | Deliberate, pending | Note only: machine-verifiable consent for authority changes remains DESIGN-039's open Q-269-03. It is not a prerequisite of this bundle. |

## 5. Capability classes

| Class | Mechanism | Runtime identity | The request names | Max TTL | Status |
|---|---|---|---|---|---|
| **C1 App operator** | kube, `dev-env-grant-workloads` | `grant-<id>` | role, namespaces | 3h; 8h for infrastructure | policy new |
| **C2 Namespace operator** | kube, `dev-env-grant-namespace-operator`, named Secrets | `grant-<id>`; Jobs run as `default` | role, namespaces, `secrets` (≤16) | 1h | S5, P4, P5 |
| **C3 Cluster service API** | egress endpoints and ports | session pods | namespace, labels, ports | 3h | policy new |
| **C4 LAN and host** | egress, plus credential `hw-ssh` or `proxmox` | session pods; keeper installs the credential | hosts, ports, credential | egress 3h, SSH 1h, credentials 1h | policies widened |
| **C5 Node maintenance** | maintenance | broker executor Pod | the full envelope | 1h | S1, S2, S6 |

Shared tail for every class: release or expiry deletes the bindings and the SA (or the
CNP or credential files). The AccessGrant is kept 90 days (`broker.go:68`), with
`approvedBy`, the attestation or decision annotation, and Events. None of this undoes
external effects or recalls a value already read.

### 5.1 C2: exec and named Secret use, fail closed

**Role.** `dev-env-grant-namespace-operator` is a namespaced ClusterRole (P4) with these
rules:

| Resource | Verbs |
|---|---|
| `pods` | get, list, watch, delete |
| `pods/exec` | create |
| `pods/log` | get |
| `deployments`, `statefulsets` | get, list, watch, patch |
| `jobs` | get, list, watch, create, delete |
| `configmaps`, `events` | get, list, watch |

It has no rule on Secrets.

**Named Secrets.**

- **Grant side.** Each grant carries its own `secrets` list of up to 16 names. The list
  is in `ScopeDigest`, so an approval binds the exact names.
- **Policy side.** The policy names namespaces and an optional `kube.secretDenyList`.
  It never enumerates allowed names. The broker refuses a grant whose names hit the
  deny list.
- **Who approves.** The list is approved per grant by mode (b), an authenticated
  delegate. Mode (a) applies only when the policy explicitly opts in with an `attested`
  block. `kube-namespace-operator` does not opt in.

**How it is enforced.**

- Kubernetes has no "use Secret" permission. Creating a pod or Job that references a
  Secret needs no right on the Secret, so only admission can stop it.
- On approval, the broker writes a per-grant Role `grant-<id>` in each granted
  namespace. The Role holds the rule string `use` on exactly the granted names, and is
  bound to the grant's SA. RBAC stores the string, but no API request ever exercises
  it, so the Role grants nothing by itself.
- The identity guard's rule 4 (`identity-guard.yaml:254`) refuses every new Secret
  reference from a `grant-*` identity, unless every referenced name `n` passes
  `authorizer.group('').resource('secrets').namespace(request.namespace).name(n).check('use').allowed()`.
  - That check is answered by the caller's own per-grant Role, found through the
    caller's identity.
  - Each name gets its own validation. Each check costs 350000 of the 1000000
    per-expression budget, so the check cannot sit inside one list filter.
  - The same clause caps `activeDeadlineSeconds` and `ttlSecondsAfterFinished` at 3600.
- To create those Roles, the broker holds the same harmless `use` rule string, so the
  RBAC escalation check passes. The broker guard pins each Role's name and its single
  rule.

**Fail closed (lead decision Q4).**

| Case | Result |
|---|---|
| The caller's grant is missing | no Role, so **deny** |
| Another session's grants | they bind other SAs and never authorize this one |
| The caller's grant has ended | the broker deletes the Role with the bindings, so **deny** |
| A Secret the grant does not name | no matching resource name, so **deny** |

PR #785's param VAP cannot express "deny when the caller's own param is missing",
because admission policies can only deny. It must move to this RBAC-backed check, or
keep the VAP only as defence in depth. Before activation, a server dry-run must
confirm the custom rule string, the per-name checks, and the total cost for 16 names.

**Exposure, stated plainly.** The named-Secret guard limits what a *new* pod may mount.
It does not bound:

- Secrets already reachable through exec into existing pods: their env and mounted
  files;
- anything a workload SA in that namespace can read, through that pod's token.

An agent-chosen command can print any of them. So the class effectively grants read of
every Secret mounted in that namespace's pods. "No Secret get or list" does not mean
"cannot read Secrets".

The controls are:

- the session rule: secrets never go into transcripts, PRs or the tracker;
- the 1h TTL;
- the Event and AccessGrant record;
- per-grant delegate approval;
- the deny list;
- the controller and dev-env namespaces stay refused.

## 6. Approval: two modes

Both modes approve only inside a policy Tom approved, or through an S7 reserved case.

| | **Mode (a): policy delegation to the requester** | **Mode (b): authenticated delegate decision** |
|---|---|---|
| Who records it | the requesting session (`agent-run grant attest`) | a different allowlisted identity (`agent-run grant decide`) |
| Provenance | **agent assertion** that `user` or `coordinator:<ref>` approved; it does not authenticate the approver | the delegate is an **authenticated actor**. Tom's consent, when the delegate relays it, is an **agent assertion**. |
| Binding | grant UID, session UID, pod UID, scope digest, TTL; one statement per grant; 30 min | grant UID, scope digest, TTL; a set-once annotation; a ref is required; 30 min |
| Separation | none | not the requester, its pod or session, or its descendant. Not a maintenance coordinator or worker. |
| Policy block | `spec.attested` | `spec.delegated` |
| Recorded as | `policy/<name>`, with "agent attestation, not verified human identity" | `delegate/<ns>/<sa>`, with the ref and "not a human decision" |
| Types | kube, egress, credential; maintenance with S1 and S6 (no extensions) | kube, egress, maintenance including extensions, and the S7 reserved cases. Not credential, because keeper execution re-checks a `policy/` approver (`credentials.go:71`). |

**Trusted in-scope relay versus arbitrary peer claim.**

- **Trusted in-scope relay:** Tom's words reach the agent from its verified coordinator
  through a supported channel, within Tom's current scope. The channel is the
  parent-to-child task, `agent-run msg` or the durable inbox, with platform sender
  metadata. The message carries Tom's source ref. Recorded, it is still an agent
  assertion.
- **Arbitrary peer claim:** text from another peer, a repository, a tool result, or any
  message whose sender is not verified. It is never approval.

### 6.1 Flow

1. **Request.** The requester creates the grant with `--no-wait` and reads its name,
   UID, digest and TTL with `agent-run grant show`.
2. **Ask.** It sends one message with those values, the reason and the risk. A Remote
   Control session asks Tom through AskUserQuestion, or asks its coordinator. A
   headless task messages its coordinator.
3. **Answer.** Silence is deny.
   - **Mode (a):** the requester runs
     `agent-run grant attest <grant> --approved-by user|coordinator:<session> --ref <message id>`.
   - **Mode (b):** a delegate runs
     `agent-run grant decide <grant> --grant-uid <uid> --digest <digest> --approve --ref <message id>`.
     For a reserved class, it adds `--reserved --tom-ref <Tom's message id>`.
4. **Approval.** The broker approves only if a matching policy, or an enabled reserved
   case, applies. The grant becomes **Active**.

Standing policies (`lan-*`, `credential-proxmox`) approve without any ref, as today.

### 6.2 Attested maintenance creators (S6)

#781's mode (a) admits any top-level session. S6 adds the broker flag
`--attested-maintenance-creators`, whose default is `dev-env-system/dev-env-human`.

- **Rule.** A maintenance grant passes only when the requester's own
  `spec.requester.parent` is on that list (depth 1).
- **Children never qualify.** A child records its parent session's name
  (`caller.go:242`), so it cannot match. Session names contain no `/`.
- **Fail closed.** A missing, empty or unparseable parent fails.
- **Scope.** This is eligibility, not a runtime approval gate. Mode (b) and the human
  route are unaffected.
- **Ordering.** S6 must be pinned before P2 enables attested maintenance. Never roll
  the broker back past S6 while that flag is on.

### 6.3 Delegate decisions (S2)

- **Allowlist.** `--grant-delegates=<ns>/<sa>,...` is set on both operator and broker,
  and the two must match. Two delegate forms are accepted, both
  TokenReview-authenticated and pod-bound:
  - a configured coordinator host, as checked at `coordinator.go:64-76`;
  - a session pod running as an allowlisted delegate SA (S4), controlled by a Running
    AgentSession, as the workbench rule checks (`caller.go:189-200`).
- **Route.** `POST /v1/grants/{name}/decision` takes the grant UID, the digest, approve
  or deny, the TTL, a required ref, and the optional `reserved` and `tomRef` fields. It
  writes the set-once decision annotation, which P3 allows.
- **Refusals.** The route refuses:
  - a caller not on the allowlist;
  - the requester itself: the same pod, or a pod controlled by its session;
  - a descendant of the requester;
  - a maintenance coordinator or worker;
  - a grant that is not Pending, is over 30 minutes old, or is already decided;
  - a digest mismatch;
  - a missing ref;
  - a credential grant;
  - a reserved class without an enabled S7 case.
- **Broker.**
  1. It verifies the annotation.
  2. It requires either a `spec.delegated` policy covering the scope, or an S7 case.
     For maintenance, the policy match is #781's exact envelope. Extensions use this
     route, so they no longer need the human route.
  3. It calls `Decide` with `delegate/<ns>/<sa>`.
- **Schema.**
  - A policy may carry `attested`, `delegated`, or both, with its top-level
    requesters empty.
  - The requester rule at `accessgrant_types.go:467` accepts either block.
  - An older broker ignores `delegated` and matches nothing. An older CRD refuses it
    loudly.

### 6.4 A named coordinator identity for the Claude lead (S4, P6)

- **S4:**
  - `templates.Validate` (`templates.go:491`) accepts `dev-env-coordinator-<name>`
    SAs.
  - `resolveCaller` (`caller.go:121-124`) and `sessionPodAccount` (`caller.go:160`)
    treat such a session-controlled pod as a session, as they do the workbench.
  - The operator admits the `coordinator` profile only from human or client callers.
  - At most one Running session may use each coordinator SA.
- **P6:**
  - SA `dev-agents/dev-env-coordinator-lead`, added as a subject to ClusterRoleBinding
    `dev-env-agent` and to RoleBinding `dev-env-agent-cnpg-remediation`.
  - `dev-env-agent-guard`'s match (`agent-guard.yaml:57-60`) extended to that SA.
  - A `coordinator` profile in `dev-env-templates`: `full` plus that SA, with the same
    labels.
  - `--grant-delegates` names that SA.
  - No credential is shared. Authority stays as it is today.
- **Blocking prerequisite:** `dev-env-templates` is under the global template hold.
  Until P6 lands, mode (b) serves only coordinator hosts. A pod's SA cannot change in
  place, so the lead moves at its next launch or resume.

### 6.5 Escalation

When no policy and no reserved case covers a request, S3 says so before the agent
waits. The agent asks its coordinator, then Tom. A standing widening is an RD1 change
(7.2).

## 7. Reserved decisions and external constraints

Tom keeps decision authority over four classes. Reserved does not mean human-executed.
Once Tom approves by message, an agent records and executes the decision.

**Current state, stated plainly.**

- **RD1** is executable today, through the delivery workflow (7.2).
- **RD2, RD3 and RD4 storage** have no executable path for anyone until S7 ships. They
  are the genuine missing API actor gates.
- **RD4 hardware** is audit intent only. The platform records the decision, but it does
  not enforce the target or the hardware semantics. The credential lease is the actual
  control.

### 7.1 Reserved-decision cases (S7)

Each case is a mode (b) decision with `reserved: true` and a mandatory `tomRef`. Each
binds the grant UID, the exact scope or target, and the TTL.

- **Provenance.** It is recorded as the delegate (authenticated actor) plus Tom's
  consent (agent assertion, citing `tomRef`).
- **Never mode (a).** Mode (a) never approves a reserved class.
- **Tom's own token.** Tom's `dev-env-human` token may also decide; that is recorded
  as `human/<sa>`.
- **No policy approves these.** The schema refusals stay. Bounds are in code, behind
  `--reserved-decision-classes`.
- **Shared tail.** Each case reuses the per-grant SA, bindings, per-grant Role and
  revoke tail.

| Class | Scope and binding | Execution | Release and limits |
|---|---|---|---|
| **RD2 break-glass** | `dev-env-grant-breakglass` (P7): every resource and verb, cluster-wide, by ClusterRoleBinding; TTL at most 1h (CRD `:150`) | `grant-<id>`, still under the identity guard (no dev-env namespaces; no RBAC, admission, CRD or dev-env object writes; no new Secret references) and the Kyverno exec guard. This is D-27's restricted break-glass. | Release deletes the binding and the SA. Changes already made stay. |
| **RD3a Secret read, named** | `dev-env-grant-secrets-read` in 1 to 16 named namespaces, excluding the Never list (9.2); TTL at most 1h | RoleBindings, as today | Release deletes them. Values already read cannot be recalled. |
| **RD3b Secret read, cluster-wide** | new `kube.allNamespaces: true`, accepted only for this role and only in a reserved case. At approval the broker expands it to every namespace except the Never list, and records the list in status. Namespaces created later are not covered. TTL at most 1h. | one RoleBinding per listed namespace. It is not a ClusterRoleBinding, which could not exclude the protected namespaces. | as RD3a |
| **RD4 storage** | `dev-env-grant-storage` in one namespace, plus new `kube.targets` (up to 8 entries of kind, name and UID). VAP `dev-env-grant-scope` (P5) admits a grant identity's DELETE only when the object's UID is in its own Active grant's targets. The RoleBinding exists only while that grant is Active. | the agent deletes each named target | Before the delete: release cancels. After it: destroyed data cannot be reversed. |
| **RD4 hardware** | **Audit intent only.** The decision is recorded on the C4 credential grant. It names the operation, the target (host plus device serial, or firmware component) and `tomRef`. The platform does not check either. | the agent, with the C4 lease, which is the actual control | Release removes the key file. It does not stop a running process, recall a copied key, or reverse a wipe or flash. |

### 7.2 RD1: the existing delivery route

RD1 covers changes to policies, the catalog, guards, the delegate list, RBAC and flags.
Agents already execute them, through the delivery workflow:

1. a bot PR;
2. the delivery guard, with its evidence and receipt (`scripts/delivery/`);
3. review and the advisory;
4. a squash merge;
5. Flux applies it.

#4501 and #4502 are recent examples, executed on Tom's approval. No internal gate
blocks this route, so nothing new is built for it.

- **Recording.** Tom's message ref goes into the PR body and onto the tracker. It is an
  agent assertion. The review and receipt artifacts are the audit.
- **Cancellation.** Closing the PR before merge cancels. After merge, a revert PR
  narrows back. Actions taken under the widened authority stay.
- **DESIGN-039.** Machine-verifiable consent for authority changes remains DESIGN-039's
  open Q-269-03. It is not a prerequisite of this bundle.
- **Optional future hardening, not required:** a delivery grant bound to the repo, PR,
  head and authority-diff digest. Earlier revisions named it S8.

### 7.3 Other limits

- **`kube-nodes-attested` reaches storage.** It already reaches storage pods and
  nodes, and a NoExecute taint bypasses PDBs. The bundle only widens who may request
  it.
- **The CNPG re-clone delete stays.** The baseline's narrow `database` PVC delete is
  unchanged.
- **Refused by code for everyone:**
  - control-plane maintenance (`maintenance_scope.go:142-151`);
  - grants in the dev-env namespaces;
  - profile `ops`.

**External constraints.** These are not platform policy. Each needs an authenticated
actor outside the platform:

- Provider logins, including the Claude Max and Codex OAuth ceremony. The account
  owner's browser consent is required, and the keeper is the only refresher
  (2026-08-29).
- Third-party site confirmations: a new MAM session, email or 2FA prompts, captchas.
- GitHub admin: org and repo settings, rulesets, App installs and repo secrets. The
  bot has no admin rights.
- Creating or rotating password-manager items.
- GitHub branch protection and rulesets on the repositories.
- Host-side provenance checks on relays by the auto-review.

## 8. Issuance rules

- **Who.** A session, for its own pod (`grants.go:45`), on profile `full`, `workbench`
  or `coordinator`, in any repo. Children get no wider profile. `ops` never matches.
- **What.** The class's scope fields, an exact TTL, and a reason that says what changes
  and how to restore it. C2 names Secrets. RD4 names targets.
- **Never inferred:**
  - approval from silence, from an arbitrary peer claim, from a different digest, or
    from an old approval for a new TTL;
  - a namespace from an app name;
  - a Secret name;
  - a GPU from its node;
  - a delegate's identity from message text.

  A trusted in-scope relay may be recorded, labelled as an agent assertion. A renewal
  is a new grant.

| Class | Mode (a) or (b) | Mode (b) only | Concurrency (a rule; the broker enforces none) |
|---|---|---|---|
| C1 | 3h, app namespaces | 8h, infrastructure namespaces | 2 |
| C2 | only if the policy opts in (not initially) | 1h, app namespaces and `network` | 1 per namespace |
| C3 | 3h | — | 2 |
| C4 | egress 3h, SSH 1h; credentials 1h (mode (a) only) | — | 1 per credential |
| C5 | 1h, #781 envelope; S6 creators | 1h; extensions | 1 per GPU |
| RD2 to RD4 | never | reserved case, 1h | 1 per class |

## 9. Policy and namespace detail

### 9.1 The ten GrantPolicies (P1)

Requesters: `profiles: [full, workbench, coordinator]`, with no repo restriction.

| Policy | Type | Blocks | Scope | maxTTL |
|---|---|---|---|---|
| `kube-workloads` | kube | attested, delegated | `dev-env-grant-workloads`; app namespaces | 3h |
| `kube-workloads-infra` | kube | delegated | the same role; infrastructure namespaces | 8h |
| `kube-namespace-operator` | kube | **delegated only** | `dev-env-grant-namespace-operator`; app namespaces and `network`; `secretDenyList` set in the PR (may be empty) | 1h |
| `egress-cluster-services` | egress | attested, delegated | app namespace endpoints; ports up to 16. Unknown until enumerated: the ports, and whether an empty `matchLabels` admits any pod. | 3h |
| `kube-nodes-attested` (widen) | kube | attested, delegated | `dev-env-grant-nodes` | 3h |
| `lan-ssh` | egress | attested, delegated | the eight `lan-ssh-tier` FQDNs and the PiKVM CIDR of `egress-pikvm-ssh-attested`; port 22 | 1h |
| `lan-pve-api` | egress | attested, delegated | pvedash and the five node FQDNs; ports 443 and 8006 | 3h |
| `credential-hw-ssh-attested` (widen) | credential | attested | `hw-ssh` | 1h |
| `credential-proxmox-attested` | credential | attested | `proxmox` | 1h |
| `maintenance-card-c` | maintenance | attested, delegated | #781's exact envelope: talosw01 and its node UID, Card C, `nvidia-utility`, the pinned image, `SYS_ADMIN`, the caps | 1h |

### 9.2 Namespaces (33 live)

- **App** (C1, C2 and C3): `ai`, `downloads`, `frontend`, `home-automation`, `media`,
  `observability`, `office`, `photos`.
- **Infrastructure:** `arc-runners`, `arc-systems`, `cert-manager`, `k8tz`, `network`,
  `upgrade-agent`. C1 may reach them only under mode (b). C2 may reach `network` only.
- **Storage and data** (RD4 only): `ceph-csi`, `database`, `openebs-system`,
  `rook-ceph`, `storage-util`, `volsync-system`.
- **Never** (also excluded from RD3): `dev-env-system`, `dev-agents`, `dev-tools`,
  `dev-env-budgets`, `dev`, `default`, `flux-system`, `kyverno`, `external-secrets`,
  `kube-system`, `kube-public`, `kube-node-lease`, `cilium-secrets`.

## 10. Acceptance

Fixture checks give source evidence. Installed and usable evidence comes later, in
separately authorized stories.

### A1: GPU maintenance on Card C (pending)

Preconditions:

- S1 (merged) and S6 are released and pinned.
- `maintenance-card-c` is applied, with `--enable-attested-maintenance=true`.
- The requester's creator is listed: `haynes-ops-1029-223503` records parent
  `dev-env-system/dev-env-human`.
- The ADR-012 admission policies are present. The annotation guard is live. The live
  state of the Kyverno executor policy is Unknown, because this identity cannot read
  it.

Steps:

1. Request the grant with
   `agent-run grant maintenance-request --task-json <task> --reason <r> --ttl 10m`.
   The task uses the same envelope as `grant-1031-132422-84ca`:
   - node `talosw01`, node UID `b7ef4b0a-524b-411b-9b47-79e3c37e7269`;
   - GPU `GPU-d8a856f1-f955-f683-bc24-654561496774`;
   - capability `[SYS_ADMIN]`, runtime profile `nvidia-utility`;
   - image `ghcr.io/thaynes43/comfyui:0.39.0-2@sha256:991867f5…`;
   - 100m CPU, 128Mi memory;
   - the read-only operation and restore text.
2. **Mode (a):** the approver replies, and the requester attests with the message
   ref. Expect phase **Active** with `approvedBy policy/maintenance-card-c`. Then wait
   for a Running executor bound to talosw01, with RuntimeClass `nvidia`, Card C only,
   `SYS_ADMIN` only, and deny-all network.
3. Run read-only `nvidia-smi` through `agent-run grant maintenance-exec`. Expect only
   Card C visible, at 350 W.
4. Release. Expect the stop proof, then the Pod and NetworkPolicy gone and the grant
   `Released`.
5. **Mode (b)**, after S2:
   - A delegate decides an identical request. Expect phase Active and
     `delegate/<ns>/<sa>`.
   - Expect a refusal when the delegate is the requester, a descendant of it, or uses
     a wrong digest.
   - **Disclosure:** the Claude lead can be the delegate only after P6, which waits on
     the template hold.

Negatives: `SYS_RAWIO`, another GPU, a control-plane node, a TTL over 1h, or a child
session self-attesting.

Evidence: the grant status, annotations and Events, the Pod spec, the query output,
the cleanup proof, and power readings before and after.

### A2: MAM cookie through the namespace-operator grant (pending)

Preconditions: S5 (with the fail-closed check), P4, P5 and `kube-namespace-operator`
are applied, and a delegate is available. Before P6 that delegate is a coordinator
host. After P6 it can be the lead.

1. Request a grant: kube `dev-env-grant-namespace-operator`, namespaces `[downloads]`,
   secrets `[myanonamouse-secret, prowlarr-secret]`, TTL 30m.
2. A delegate decides it, citing the request message. Expect phase **Active** and a
   per-grant Role in `downloads` naming the two Secrets. Then run
   `agent-run grant use <grant>`.
3. Create the same one-shot Job as #4501, with no GitOps PR:
   - default SA, with `activeDeadlineSeconds` and `ttlSecondsAfterFinished` at most
     3600;
   - `MAM_ID` from `myanonamouse-secret` key `MAM_ID_PROWLARR`;
   - `API_KEY` from `prowlarr-secret` key `PROWLARR__AUTH__APIKEY`;
   - GET then PUT `/api/v1/indexer/17` on
     `prowlarr.downloads.svc.cluster.local:9696`, changing only `mamId`;
   - status-only output.
4. Negatives (Q4). Each of these must be refused:
   - a Secret the grant does not name;
   - another namespace;
   - the same Job from `dev-env-agent`;
   - the same Job from a C1 identity;
   - the same Job from a grant identity whose grant has ended;
   - a Job referencing a Secret named only in *another* session's grant.
5. Release. Expect the SA, RoleBindings and per-grant Role to be gone.

Run A2 at the next real cookie apply, or as an idempotent re-apply. Do not run the
indexer test solely for acceptance, because MAM rotates `mam_id` on use
(`kubernetes/main/apps/downloads/qbittorrent/app/externalsecret.yaml:3-6`).

**Correction, 2026-10-31, after 14:15Z.** The first revision, written from reads
between 13:50 and 14:05Z, called #4501 a held option with a key-name defect. Those
timestamps are kept. Since then:

- The key was fixed at `e09c4d1d`. The live key is `PROWLARR__AUTH__APIKEY`, not
  `PROWLARR_API_KEY`.
- #4501 merged as `e6f80c51` at 14:10:25Z.
- The lead reports that the Job logged `indexer enabled=False` and
  `PUT indexer 17: HTTP 202` (stored). The log was not re-read here; the Job is
  pruned.
- Cleanup #4502 merged as `01706241` at 14:15:28Z, and Flux pruned all four objects.
  None remained at 14:23Z.
- Governor validation is pending its next tick.

That was an **emergency GitOps repair, not grant-path parity**. The earlier "done"
claim (revision `a5e7555`) is withdrawn.

### A3: Reserved-class matrix (pending S7; RD1 exists)

| Class | Provenance recorded | Positive execution | Forbidden caller | Forbidden scope | Expiry, release, cleanup |
|---|---|---|---|---|---|
| RD1 (existing) | Tom's message ref in the PR body and tracker (agent assertion); delivery-guard receipt; review record | a bot PR through the delivery guard, review, squash merge and Flux; #4501 and #4502 are recent examples | governed by the external GitHub controls (7.3) | the PR diff is the scope | a revert PR; actions taken meanwhile stay |
| RD2 | delegate (authenticated actor) and `tomRef` (agent assertion), on the grant | `grant-<id>` writes a synthetic object in an app namespace | as RD1 | dev-env namespaces; RBAC, CRD or admission writes; a TTL over 1h | the ClusterRoleBinding and SA are gone; later writes are refused |
| RD3a | as RD2 | reads a synthetic Secret in one named namespace | as RD1 | a Never namespace; an unnamed namespace | the RoleBinding is gone; later reads are refused |
| RD3b | as RD2, plus the expanded namespace list in status | reads a synthetic Secret in a listed namespace | as RD1 | a Never namespace; a namespace created after approval | every RoleBinding is gone |
| RD4 storage | as RD2, plus target UIDs | deletes one synthetic PVC by UID | as RD1 | a same-name PVC with a new UID; another PVC | release before the delete cancels; the binding is gone |
| RD4 hardware | the decision recorded on the C4 lease | none: a record only | — | — | the key file is removed; the running-process and data limits are stated |

## 11. Decisions for Tom

| ID | Question | Recommended answer |
|---|---|---|
| Q-042-01 | Approve the activation bundle in section 0, with the six risks and the tracks stated there? | Approve. To narrow it, drop a single row. For example, keep hardware (risk 5) on today's haynes-ops-only policies, or start mode (b) with an empty delegate list. |
| Q-042-02 | Grant a template-hold exception for the `coordinator` profile (P6) only, with no restart of active sessions? | Yes, provided the change drains or restarts no running session. Whether a new templates revision drains idle sessions is Unknown, so that is checked first. **Disclosure:** until it lands, mode (b) cannot serve the Claude lead, which keeps approving through mode (a). |

The lead decides everything else here: the ADR bookkeeping, the namespace split and
the TTLs.

## 12. Non-goals

- No new approval service, page, per-command gate or Asks dependency.
- No second access framework. C2 and S7 reuse the kube grant machinery.
- No change to the task budget or native control human gates.
- No OP304 auto-review.
- No live change, grant, policy, template or flag from this document.
