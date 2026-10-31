# DESIGN-042: Homelab operator access activation bundle

- **Status:** Proposed, 2026-10-31. Design only. Nothing is granted, activated or
  changed by this document: no policy, guard, role, flag or cluster object.
- **Tracking:** OpenProject Epic [#112](https://work.haynesnetwork.com/work_packages/112),
  Saga #85. Related: #214 (approved-access bundle), #219 (phone approvals), #233
  (OP233 maintenance), #304 (delegated review, deferred), #319 (MAM indexer), #113.
  The work order named no story, so questions use `Q-042-NN` until the lead files
  one. Lead `dev-env-1029-132039` reviews the ready PR. Do not merge it in this story.
- **Inputs:** Tom's four corrections of 2026-10-31, relayed by his coordinator
  (section 1). Tom's #214 words of 2026-10-26.
- **Source examined:** dev-env `34624a4dd7ecc2a19edb2d76db96c22fd71e2b33`; haynes-ops
  `09614e825a5ab102cca0a9d18bd6259b91c6e0ec`. Live objects were read with the session's
  read-only identity on 2026-10-31 between 13:50 and 13:58Z.
- **Amends, on acceptance:** ADR-012's rule that only the human API identity decides a
  maintenance grant. A new ADR records the change. No Accepted ADR is edited.

`DES/` below means haynes-ops `kubernetes/main/apps/dev-env-system/`. `D001` means
[DESIGN-001](001-dev-env-v2.md).

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

Earlier, on #214 (2026-10-26): "I want a streamlined way agents can ask either their
coordinator ... or a user ... for access to anything they need in cluster AND online
and the coordinator or user can gauge the risk and approve or deny - user must be able
to do this from mobile".

These correct implementation drift. They do not remove safety. The outcome is one
activation bundle that Tom reviews once. It restores v1's operator reach through v2's
existing pieces: scoped task grants, per-grant identities, TTL, release, audit, and
D-88 attestation, extended so Tom or a coordinator approves by message. Two cases are
the acceptance tests, not one-off exceptions:

- **GPU:** a maintenance grant with SYS_ADMIN on Card C of talosw01.
- **MAM:** apply the saved Prowlarr cookie to indexer 17.

No new framework, per-command gate or approval service is proposed.

## 2. Inventory: what exists today

### 2.1 Approval paths by grant type

| Grant type | Standing policy | Attested policy (D-88) | Human decision | Live policies (13:50Z) |
|---|---|---|---|---|
| kube | yes | yes | no route: only `internal/broker/maintenance_decision.go:32` calls `Decide`; the phone page is #219, in progress | `kube-nodes-attested` |
| egress | yes | yes | no route (same) | `lan-ceph-mgr-metrics`, `lan-internal-ingress`, `lan-pve-api-lb`, `lan-pve-api-nodes`, `lan-ssh-tier`, `egress-haynesintelligence-ssh-attested`, `egress-pikvm-ssh-attested` |
| credential | yes | yes | refused (`internal/broker/seams.go:151`) | `credential-proxmox`, `credential-hw-ssh-attested` |
| maintenance | refused (`api/v1alpha1/accessgrant_types.go:463`; `internal/broker/match.go:111`) | refused (same) | only `dev-env-system/dev-env-human` (`internal/apiserver/maintenance.go:36`; `seams.go:143`; broker flag `--maintenance-human-service-account`) | none |
| breakglass | refused (`match.go:79`; CRD `:463`) | refused | Tom only, no route; the role is not in git | none |

Facts behind the table:

- `match.go:103-111` matches only kube, egress and credential. The default case
  returns false, so maintenance and breakglass never match a policy.
- Roles `dev-env-grant-breakglass` and `dev-env-grant-secrets-read` never match
  (`match.go:120`; CRD `accessgrant_types.go:573`). Profile `ops` never matches
  (`match.go:83`).
- `dev-env-human` is the token Tom mints on his laptop with his admin kubeconfig
  (`D001:340-342`). A maintenance decision therefore needs the Mac. That is the
  route Tom rejects in quote 3.
- A grant that no policy matches waits 30 minutes and is denied by `timeout`
  (`broker.go:65`). Nobody is told.

### 2.2 Human-only API gates (verified)

| Route | Gate | Cite |
|---|---|---|
| `POST /v1/grants/{name}/maintenance-decision` | `kindHuman` only | `internal/apiserver/maintenance.go:36` |
| task budget `extend` | `kindHuman` (a coordinator reaches the handler and is refused) | `taskbudgets.go:207`; `server.go:258` |
| task budget `continue` | `kindHuman` | `taskbudgets.go:347` |
| native control | `kindHuman` | `native_control.go:41` |

Caller classes are fixed by the TokenReview username, never by the body
(`caller.go:20-40`, `auth.go:87`). `kindCoordinator` is a configured Codex host
identity, not an approver (`coordinator.go:67-76`). No delegate or coordinator
approver exists. This design changes only the maintenance row. The task budget and
native control gates are out of scope.

### 2.3 Attestation as built (D-88)

- Only the grant's own requesting session may attest (`grant_attestation.go:24`). It is
  a requester self-statement. The broker message says "agent attestation, not verified
  human identity" (`internal/grantattest/attest.go:49`, used by `Message` at `:235`).
- The statement binds the grant UID, the scope digest, the TTL, and the attestor's
  session, session UID and pod UID from the token (`attest.go:81-90`). One statement per
  grant: an identical retry returns 200 and a different one 409. It must arrive in the
  30-minute pending window.
- `ScopeDigest` (`attest.go:162`) covers type, credential, role, namespaces, egress
  destinations and ports, TTL, session and session UID. It does **not** cover the
  maintenance envelope or the reason.
- The approver is `user` or `coordinator:<ref>`, and the ref is syntax-checked only
  (`attest.go:52-54, 93-102`). No requester/approver separation exists. Only an explicit
  attested policy turns the statement into an approval.
- The broker re-verifies the statement against the live session and its running pod
  (`internal/broker/attestation.go:48`). It writes `approvedBy policy/<name>`
  (`broker.go:222`). Credential execution re-checks it (`credentials.go:159`).
- Known gap (`D001:4163`): kube and egress execution never re-read `approvedBy`.
- Flags are live: operator `--enable-grant-attestation=true`
  (`DES/operator/app/helmrelease.yaml:312`), broker `--enable-attested-grants=true`
  (`DES/broker/app/helmrelease.yaml:76`).

### 2.4 Grant identities, catalog and guards

| Piece | What it does | Cite |
|---|---|---|
| Kube grant identity | The broker creates SA `dev-agents/grant-<id>` (no automount), binds catalog roles by RoleBinding per namespace or one ClusterRoleBinding, mints a token for the time left, installs it into the pod's grants tmpfs by exec, and deletes the SA on release or expiry. | `internal/broker/kube.go:40, 142-175`; `installer.go:54`; `broker.go:482-496` |
| `dev-env-grant-workloads` | Namespaced. Apps workloads, Jobs, CronJobs, pods, services, configmaps: full CRUD. Logs and events: read. No Secrets, no exec. | `DES/rbac/app/grant-catalog.yaml:24-51` |
| `dev-env-grant-storage` | Namespaced. PVC and VolumeSnapshot create and delete, StatefulSet delete. | `grant-catalog.yaml:56-74` |
| `dev-env-grant-secrets-read` | Namespaced. Secrets get and list. Never policy-approved. | `grant-catalog.yaml:79-88` |
| `dev-env-grant-nodes` | Cluster-wide. Node patch, pod eviction. | `grant-catalog.yaml:93-114` |
| `dev-env-grant-breakglass` | Named in the broker guard and CRD. **Not in git and not live.** | `grant-catalog.yaml:15-16`; live `kubectl get clusterrole` returned NotFound |
| `dev-env-agent-guard` | Matches only `dev-agents/dev-env-agent`. Jobs run as `default` and mount no Secret except `*-volsync-aws-secret`. Workloads: restart only. CronJob: suspend only. Flux: reconcile and suspend. ExternalSecret: force-sync. | `DES/rbac/app/agent-guard.yaml:59, 67-68, 175-182` |
| `dev-env-identity-guard` | Matches every `dev-agents` SA except the workbench, so every `grant-*` identity. No writes in dev-env namespaces; grants write nothing in flux-system, kyverno, external-secrets, kube-system; pods run as `default` and meet the baseline; **no new Secret reference** except the volsync suffix. | `DES/rbac/app/identity-guard.yaml:50-54, 176, 254` |
| `dev-env-broker-guard` | The broker touches only `grant-*` objects in `dev-agents`, and binds only catalog roles to the grant's own SA. | `DES/rbac/app/broker-guard.yaml:78-119` |
| `dev-env-maintenance-annotations` | Only the operator sets the maintenance decision annotation, once. | `DES/rbac/app/maintenance-annotations.yaml` |
| Maintenance executor | The broker creates a bare Pod pinned to the node and node UID, SA `dev-env-maintenance` with no token, caps drop ALL plus the named ones, not privileged, a deny-all NetworkPolicy, and RuntimeClass `nvidia` with `NVIDIA_VISIBLE_DEVICES=<GPU UUID>` for `nvidia-utility`. Control-plane nodes are refused. | `internal/broker/maintenance_executor.go:27-96`; `maintenance_scope.go:142-151` |
| Egress grants | A CNP `grant-<id>` in `dev-agents` for the session's pods: FQDNs, CIDRs, or in-cluster endpoints by namespace and pod labels, with up to 16 ports. No Service names. | `internal/egress/policy.go:37-93` |

The baseline `dev-env-agent` ClusterRole already allows cluster-wide `pods/exec`,
`services/proxy`, `pods/proxy`, Job create and delete, pod delete, and restart and
reconcile patches (`DES/rbac/app/agent.yaml:78-118`). It has no Secret read.

### 2.5 Live evidence of the drift

| Object (read 13:50 to 13:56Z) | What happened |
|---|---|
| `grant-1031-033915-fc19` (maintenance, Card C, SYS_ADMIN, 10m) | Denied, `deniedBy: timeout`, "no answer in 30 minutes" |
| `grant-1031-132422-84ca` (same scope) | Pending at 13:51Z; Denied by `timeout` when read at 13:55:48Z |
| `grant-1031-033808-7327` (same, without SYS_ADMIN) | Released before an answer |
| `grant-1030-122127-d7d9`, `grant-1030-130136-9897` (kube `dev-env-grant-nodes`, workbench, haynes-ops) | Denied by `timeout`. `kube-nodes-attested` names profile `full` (and repo `haynes-ops`) only, so a workbench session matched nothing. This is the whack-a-mole Tom describes. |

## 3. v1 baseline against v2 today

v1's own role `dev-env-operator` (haynes-ops `kubernetes/main/apps/dev/dev-env/app/rbac.yaml:16-136`)
is the template of v2's `dev-env-agent`. The differences are in admission, mounted
credentials, network and approvals. D-70 also counts v1's effective reach through
Headlamp's `cluster-admin` SA (`D001:3947-3960`).

| # | Capability | v1 | v2 today | Gap |
|---|---|---|---|---|
| — | Kubernetes reads without Secrets, exec, proxy, pod delete, restart, reconcile, CronJob suspend, force-sync | yes | yes, guarded (D-19) | none |
| G1 | Job that mounts an app Secret or runs as a named SA | yes. No admission policy matches `dev/dev-env` except `dev-env-v1-token-guard`, which covers tokens only (live VAP read). | refused: default SA and `*-volsync-aws-secret` only, for the agent and for every grant identity | **gap**. R-04 already lists it as P-05 (recyclarr clone). It blocks MAM. |
| G2 | Runtime workload spec changes | patch, unguarded | restart only | deliberate (D-19, GitOps). Flux reverts drift. No change. |
| G3 | Privileged or capability pods on a node (GPU) | effective only, via Headlamp `cluster-admin` (D-70) | maintenance grant, decided only by `dev-env-human` on the Mac | **gap in approval**, not in mechanism |
| G4 | Hardware: SSH to 8 hosts, PVE API, Proxmox operator token, Ceph metrics | key and operator token mounted; CNP allows all eight SSH targets, pvedash 443, nodes 8006, Ceph 9283 (`networkpolicy.yaml:441-487`) | workbench: mounted key and standing LAN policies. full: attested hw-ssh lease and SSH to two hosts, haynes-ops only. Proxmox operator credential: workbench only. | **gap for full sessions and other repos** |
| G5 | Omni | Admin key mounted | Admin key in `full` and `workbench` (`DES/templates/app/templates.yaml:124-127, 183-186`) | none |
| G6 | In-cluster app APIs from the session | fixed list plus internal ingress 8443; not `downloads:9696` directly | platform tier plus `lan-internal-ingress` (full, workbench) | small: no attested policy for app endpoints |
| G7 | Approval of anything outside a standing policy | not needed: standing capability | attested policies only for `full` + `haynes-ops`; maintenance on the Mac; everything else times out | **the main gap** |

The v1 MAM route is **Unknown**. No runbook describes it (haynes-ops grep for
`myanonamouse`, `MAM_ID` and `prowlarr cookie` found none). `MAM_ID_PROWLARR` is mounted
in no pod (only `MAM_ID_SEEDBOX` is, in the qbittorrent sidecar,
`kubernetes/main/apps/downloads/qbittorrent/app/helmrelease.yaml:165-169`). The likely v1
route was G1: an unguarded Job in `downloads` that mounts the Secret. That route is
exactly what v2's guards removed.

## 4. Prior records: drift or deliberate

| Record | Restriction | Verdict | Action here |
|---|---|---|---|
| Q-16 ruling, `D001:5338` | "Standing grants cover parity"; "Human approval gates only capabilities beyond today's" | Governs | Implemented by this bundle |
| D-70, `D001:3947` | "Do not add a new approval gate to an existing capability" | Governs | Same |
| `D001:348` | "only Tom (on the console) can approve a grant" | Drift. Q-16 and D-88 came later. | Superseded by D-88; extended here |
| D-26, D-27 | Tom approves on a page | Already superseded (Q-16, D-70) | None |
| D-54, D-61: schema and match refuse breakglass and secrets-read | Beyond v1's OPERATOR tier | Deliberate | Keep (H1, H2) |
| CRD rule `:463` refuses maintenance policies | Human decision for GPU work | Drift against D-70 and quote 1 | S1 (OP233-ATTESTED-MAINT) |
| ADR-012 (accepted for design and source only) | Human API identity decides, "independent of unverified agent coordinator:string attestation" | Deliberate when written; corrected by quote 3 | New ADR amends it on acceptance (Q-042-04) |
| D-88 policies scoped to `full` + `haynes-ops`, one host each | Each scope approved one at a time | Drift: whack-a-mole | Widen by class (Q-042-01, Q-042-06) |
| D-19 guards for the baseline identity | Runtime writes go through git; default SA; no Secret refs | Deliberate | Keep for `dev-env-agent` |
| Volsync-only Secret allowlist for grant identities | Blocks R-04 P-05 and MAM | Drift: v1 had no Job guard | P2 |
| Workbench-only LAN policies and Proxmox credential (Q-110-08) | Hardware only in the workbench | Deliberate then; now drift, since full sessions do hardware work | Q-042-06 |
| [DESIGN-034](034-agent-asks-and-approvals-board.md) | Only a portal human answers an Ask | Deliberate for Asks | No change. Grants never wait on the board. |
| [DESIGN-039](039-access-policy-change-guard.md) Q-269-03 | Who approves authority changes | Deliberate, pending | H4 defers to it |

## 5. Capability classes

Five classes cover a home lab. Each is an existing grant type with a policy; only C2
needs a new role and one guard expression.

| Class | Grant | Runtime identity | The request names | Credentials |
|---|---|---|---|---|
| **C1 App operator** | kube, `dev-env-grant-workloads` (exists), RoleBinding per namespace | `dev-agents/grant-<id>`, token in the pod's grants tmpfs, selected with `agent-run grant use` | role, namespaces (up to 16), TTL, reason | none new |
| **C2 Secret-use Job** | kube, new `dev-env-grant-secret-use` | same per-grant SA; the Job runs as the namespace's `default` SA | role, namespaces, TTL; the reason names the Secrets and the Job's purpose | uses existing app Secrets in place |
| **C3 Cluster service API** | egress, `endpoints` (namespace + pod labels) and ports (exists) | the session's pods, through CNP `grant-<id>` | namespace, labels, ports, TTL | none; app keys come through C2 or the app's own auth |
| **C4 LAN and host** | egress (FQDN or CIDR, ports 22, 443, 8006, 9283) and credential (`hw-ssh`, `proxmox`) (exist) | session pods; keeper installs the credential in the pod tmpfs | hosts, ports, credential name, TTL | existing hw-ssh key and keeper-minted Proxmox token |
| **C5 Node maintenance** | maintenance (exists) | broker-created executor Pod on the named node, SA `dev-env-maintenance` with no token, deny-all network; commands through the operator exec route | node and node UID, GPU UUID, runtime profile, capabilities, image digest, CPU, memory, operation, restore, TTL | none |

| Class | Max TTL, proposed | Revocation and cleanup | Audit | GitOps or runtime | Status |
|---|---|---|---|---|---|
| C1 | 3h coordinator band, 8h user band | release or expiry deletes the SA, so every token dies; RoleBindings go with it | AccessGrant kept 90 days (`broker.go:68`), `approvedBy`, attestation annotation, objects changed by `grant-<id>`; API audit log Unknown | runtime for live fixes, scaling and one-off Jobs; persistent change in git (Flux reverts drift) | role exists; policy new |
| C2 | 1h | as C1. Release does not stop a running Job: revoke deletes only the SA and RoleBindings (`kube.go:162-176`). So the guard requires `activeDeadlineSeconds` and `ttlSecondsAfterFinished` of 3600 at most on Jobs a grant creates. | as C1, plus the Job spec shows each Secret reference and the command | runtime one-shots; recurring work becomes a CronJob in git | new, small |
| C3 | 3h | broker deletes the CNP; the expiry backstop also does | grant, Hubble flows | runtime | mechanism exists; policy new |
| C4 | egress 3h, SSH 1h; credentials 1h | CNP deleted; credential removed from tmpfs. Removal is not revocation of a copied static key (D-89). | grant, CredentialJob receipt, keeper journal | runtime | exists; policies widened |
| C5 | 1h (30m default) | `stopMaintenance` proves the Pod stopped on the node UID, then deletes Pod and NetworkPolicy | grant, attestation, exec log lines, restore evidence | runtime | exists; attested path in flight (S1) |

**C2 in detail.** The new ClusterRole holds `secrets` with the single verb `use`, plus
Job create, get, list, watch and delete, pod read and `pods/log` get. It holds no `get`
or `list` on Secrets. The identity guard's rule 4 (`identity-guard.yaml:254`) gains
one namespace-level test, outside the `addedSecrets` filter: a grant identity passes when
`authorizer.group('').resource('secrets').namespace(request.namespace).check('use').allowed()`.
One check per request keeps the expression inside the CEL cost budget, which prices each
authorization check at 350000 of 1000000. A per-Secret check inside the filter would
refuse any Job with three or more Secret references. The role has no resourceNames, so
a name adds nothing. A pod can reference only Secrets in its own namespace, and the Job
must still run as `default`. The broker binds the role only in the namespaces the grant
names, only while it lives. `dev-env-agent` never holds `use`, so its volsync-only rule
is unchanged. A server dry-run on the 1.35 API server confirms the custom verb and the
check before activation.

**C2 is not read-back-proof.** The agent writes the Job. A Job can print a value, and
the baseline identity can exec into the Job's pod and read its logs. That equals v1's
exposure. The rule is: print status only. The audit keeps the Job spec. This is why
cluster-wide Secret read stays Tom-only (H2) and the delegable form is per namespace,
time-bound and attested.

## 6. Approval, delegation and escalation

### 6.1 Approval by message

D-88 already does this for kube, egress and credential. The bundle extends it to C2 and
C5 and widens who is eligible.

1. The requester creates the grant without waiting (`agent-run grant request --no-wait`,
   MCP `request_access`, or `agent-run grant maintenance-request`). It reads the name,
   UID, digest and TTL with `agent-run grant show`.
2. It sends one message to the approver with those exact values, the reason and the
   risk. A Remote Control session asks Tom with AskUserQuestion, which reaches his
   phone. A headless task messages its coordinator (`agent-run msg` or the durable
   inbox), which answers or asks Tom.
3. The approver replies approve or deny and names the grant. Silence is deny.
4. The requester runs `agent-run grant attest <grant> --approved-by user|coordinator:<session> --ref <message id>`.
5. The broker approves under the matching attested policy, or the grant times out.

No grant is approved under an attested policy without a statement that cites the
approver's message ref. Standing policies such as `lan-*` and `credential-proxmox` still
approve without one, as today. An attestation is the requester's claim. It is never verified human identity, and
no record, log or UI may call it that.

### 6.2 What binds, and what is new

| Property | Today | Bundle |
|---|---|---|
| Request binding | grant UID, scope digest, TTL, session, session UID, pod UID | unchanged |
| Maintenance binding | no attested path; human decision binds UID and `MaintenanceDigest` | S1 must bind `MaintenanceDigest`, because `ScopeDigest` omits the envelope |
| Replay | one statement per grant; 30-minute window; attestor's pod must be running | optional S5: refuse a second attestation citing the same ref for a different digest |
| Expiry and cleanup | TTL, release, SA or CNP deletion, executor stop proof | unchanged |
| Actor in audit | attestor from the token; `approvedBy policy/<name>`; approver and ref in the annotation | unchanged; user band recorded by policy name (S3) |

### 6.3 Two bands per class

- `spec.attested` (exists): the approver may be `user` or `coordinator:<ref>`. This is
  the coordinator band: narrow scope, short TTL.
- `spec.userAttested` (new, S3): the statement's approver must be `user`. This is the
  user band: wider namespaces, longer TTL. It is still an attestation. The approver
  field is written by the requester and checked for syntax only (`attest.go:93-102`), so
  the policy name records whom the requester says it asked. It is not enforcement.
- A policy that sets both `attested` and `userAttested` is refused.

The requester rule at `accessgrant_types.go:467` must accept `userAttested` as the
requester block, with `profiles`, `repos`, `agents` and `attested` empty. An older
broker then reads a policy that names no requester and matches nothing, the same
fail-closed rollback D-88 uses. An older CRD prunes the field and its rule refuses the
policy, so a Flux apply fails loudly. Remove user-band policies before reverting the
CRD.

### 6.4 Escalation

A request above every band matches no policy. Today it times out silently. S4 changes
the CLI and MCP text: when no readable GrantPolicy covers the scope, the tool says so
before the agent waits. The agent then asks its coordinator, and Tom's phone if the
coordinator cannot approve. If Tom wants that scope from now on, the agent opens a
haynes-ops policy PR for him (H4). There is no one-off route above the user band; that
space is H1 to H5 or a policy change.

### 6.5 Delegated approvers (optional, not in the bundle)

The lead's gate audit proposed a configured list of coordinator identities that decide
themselves, recorded as `delegate/<ref>`, so audit never reads a delegate as human. It
gives real requester/approver separation. It needs a new decision route, a new caller
class or list, and broker `Decide` changes. It is larger than the rest of the bundle
and neither acceptance case needs it. It belongs to #304 (Q-042-05).

## 7. Boundaries that stay with Tom

Everything outside this list is delegable.

| # | Boundary | Why | Enforced by |
|---|---|---|---|
| H1 | Break-glass | Removes every other boundary at once | `match.go:79`, `seams.go:139`, CRD `:463`; the role does not exist |
| H2 | Secret read through the API (`dev-env-grant-secrets-read`, any namespace) | Lists and returns every value at once, with no record of which were used; copies cannot be recalled | `match.go:120`, CRD `:573`. C2 is the delegable form: named Secrets mounted into a Job whose spec is the record. |
| H3 | Claude Max and Codex login ceremony | One owner per rotating refresh token; a second refresher revoked the family on 2026-08-29 | keeper-only design |
| H4 | Changes to the bounds: GrantPolicies, catalog roles, guards, broker and operator RBAC and flags | An agent that widens its own bounds makes them meaningless | Tom's review. This is a convention today: agents can self-merge haynes-ops (Q-16 residual risk). DESIGN-039 proposes enforcement; Q-269-03 is pending. |
| H5 | Destructive storage or data operations: `dev-env-grant-storage`, database, Ceph pool, volume or backup deletion, disk wipes, firmware writes | Not undone by a rollback | Enforced: no policy names `dev-env-grant-storage` or the storage and data namespaces (9.2). Convention: a hardware operation states that it is non-destructive, or waits for Tom. The baseline's narrow `database` PVC delete for CNPG re-clone stays. |

Refused by code for everyone, Tom included: maintenance on control-plane nodes
(`maintenance_scope.go:142-151`), grants in the dev-env namespaces, and profile `ops`.

## 8. Issuance rules

- **Who requests:** a session, for its own pod (`grants.go:45`). Eligible profiles per
  Q-042-01. Children get no wider profile than their parent. `ops` never matches.
- **What a request must name:** the class's scope fields in section 5, an exact TTL, and
  a reason that says what will change and how to restore it. C2 names the Secrets in
  the reason. C5 names the operation and restore fields.
- **Never inferred:** approval from silence, a relay, a peer's claim, an approval of a
  different digest, or an old approval for a new TTL; a namespace from an app name; a
  Secret name; a GPU from a node; a coordinator's identity from message text. A renewal
  is a new grant and a new approval.

| Class | Coordinator band | User band | Concurrency per session (rule; the broker does not enforce one today) |
|---|---|---|---|
| C1 | 3h, app namespaces | 8h, plus infrastructure namespaces | 2 active |
| C2 | 1h, app namespaces | 1h, plus `network` | 1 per namespace |
| C3 | 3h, app namespaces | none in this bundle | 2 active |
| C4 egress | 3h (SSH 1h) | none in this bundle | — |
| C4 credential | 1h `hw-ssh`, 1h `proxmox` | none in this bundle | 1 per credential |
| C5 | 1h, `nvidia-utility`, `SYS_ADMIN` only, GPU workers | none in this bundle | 1 per GPU |

"None in this bundle" means a wider request escalates (6.4). A user-band policy is
added by a later policy PR only when a real request needs it.

## 9. The activation bundle: exact deltas

### 9.1 dev-env source

| ID | Change | Status | Default |
|---|---|---|---|
| S1 | OP233-ATTESTED-MAINT. An attested maintenance GrantPolicy. CRD rule `:463` still refuses breakglass. The match binds `MaintenanceDigest`. The policy scope covers runtime profile, capabilities, nodes, image digests, CPU, memory and max TTL. | **In flight**, separate worker. Branch `agent/op233-attested-maint` was not on origin at 14:05Z. Field and flag names belong to it. | default-off flag |
| S2 | Add `dev-env-grant-secret-use` to both kube role enums (`api/v1alpha1/accessgrant_types.go:254, 572`) and the broker catalog (`internal/broker/checks.go:28`), plus the envtest copies in `internal/broker/testdata/haynes-ops/` (`broker-guard.yaml`, `broker.yaml`, `grant-catalog.yaml`); namespaced only; run `make generate`. | New, small | inert until haynes-ops binds it |
| S3 | `GrantPolicy.spec.userAttested`: same shape as `attested`; approver must be `user`; the requester rule at `accessgrant_types.go:467` accepts it. | New, small | inert until a policy sets it; fail-closed on rollback |
| S4 | CLI and MCP say when no policy covers a request and point to message escalation; attest help requires the approver's message ref. | New, small | text and a read-only match |
| S5 | Refuse a second attestation that cites one ref for a different digest. | New, small, optional | — |
| S6 | Delegate decisions | Not in the bundle (#304) | — |

S1 to S4 ship in one release. Its CRD copy reaches haynes-ops before the operator and
broker pins, as CLAUDE.md requires.

### 9.2 haynes-ops

| ID | Change | Where | Rollback |
|---|---|---|---|
| R1 | CRD copy from the release | `DES/crds/app/` | remove the user-band and maintenance policies first, then revert the copy |
| R2 | Operator and broker pins to the release | `DES/operator/app/helmrelease.yaml`, `DES/broker/app/helmrelease.yaml` | pin back |
| P1 | ClusterRole `dev-env-grant-secret-use` with the grant-role label; broker `bind` resourceNames gain it (`DES/rbac/app/broker.yaml:25-33`); broker-guard RoleBinding catalog gains it (`broker-guard.yaml:52-56`) | `DES/rbac/app/grant-catalog.yaml` | revert; active grants end at release or expiry |
| P2 | Identity guard rule 4: a grant identity that may `use` Secrets in the request namespace may reference them (one check per request); Jobs a grant creates carry `activeDeadlineSeconds` and `ttlSecondsAfterFinished` of 3600 at most | `DES/rbac/app/identity-guard.yaml:176, 254` | revert; new Secret references are refused again |
| P3 | GrantPolicies (table below) | `DES/grant-policies/app/policies.yaml` | delete a policy; matching stops and active grants run out |
| P4 | Broker flag for attested maintenance (name from S1) | `DES/broker/app/helmrelease.yaml` | set it false |

| P3 policy | Type | Band | Scope | maxTTL |
|---|---|---|---|---|
| `kube-workloads-attested` | kube | attested | `dev-env-grant-workloads`; app namespaces (Q-042-03) | 3h |
| `kube-workloads-user-attested` | kube | userAttested | same role; plus the infrastructure namespaces | 8h |
| `kube-secret-use-attested` | kube | attested | `dev-env-grant-secret-use`; app namespaces | 1h |
| `kube-secret-use-user-attested` | kube | userAttested | same role; plus `network` only (Authentik wiring) | 1h |
| `egress-cluster-services-attested` | egress | attested | endpoints by app namespace; the app Service ports, up to 16 (list Unknown until the PR enumerates them). Whether an entry with empty `matchLabels` admits any pod is Unknown and must be tested. | 3h |
| `kube-nodes-attested` (widen) | kube | attested | requesters per Q-042-01 | 3h |
| `lan-ssh-attested` | egress | attested | the eight `lan-ssh-tier` FQDNs and the PiKVM CIDR of `egress-pikvm-ssh-attested`, port 22 | 1h |
| `lan-pve-api-attested` | egress | attested | `pvedash.haynesnetwork` and the five node FQDNs, ports 443 and 8006 | 3h |
| `credential-hw-ssh-attested` (widen) | credential | attested | requesters per Q-042-06 | 1h |
| `credential-proxmox-attested` | credential | attested | `proxmox` | 1h |
| `maintenance-gpu-utility-attested` | maintenance | attested | `nvidia-utility`; `SYS_ADMIN`; GPU worker nodes (list Unknown; from node labels); digest-pinned images; CPU 500m and memory 512Mi at most | 1h |

The 33 live namespaces, proposed split:

- **App (coordinator band):** `ai`, `downloads`, `frontend`, `home-automation`, `media`,
  `observability`, `office`, `photos`.
- **Infrastructure (user band, C1 only; C2 adds `network` alone):** `arc-runners`,
  `arc-systems`, `cert-manager`, `k8tz`, `network`, `upgrade-agent`. C2 stays out of
  `cert-manager` (the DNS token) and `upgrade-agent` (the shepherd and responder App
  keys), because a Job can print what it mounts.
- **Storage and data (H5; no policy in this bundle):** `ceph-csi`, `database`,
  `openebs-system`, `rook-ceph`, `storage-util`, `volsync-system`.
- **Never:** `dev-env-system`, `dev-agents`, `dev-tools`, `dev-env-budgets`, `dev` (the
  v1 pod), `default`, `flux-system`, `kyverno`, `external-secrets`, `kube-system`,
  `kube-public`, `kube-node-lease`, `cilium-secrets`.

### 9.3 Activation order

1. Merge S1 to S4 and release. Default-off and inert. Routine, by the lead.
2. haynes-ops R1, then R2. Routine GitOps.
3. One haynes-ops PR with P1, P2, P3 and P4, opened as a held draft for Tom. **This is
   the single activation review.** Inside it, P1 lands before the secret-use policies
   and P2 before acceptance A2.
4. Run A1 and A2 (section 10) and record the evidence on the tracker.
5. PR #4501 is the lead's call: close it after A2 passes, or fix and keep it as a
   fallback. It reads key `PROWLARR_API_KEY`, but `prowlarr-secret` holds only
   `PROWLARR__AUTH__APIKEY` (`kubernetes/main/apps/downloads/prowlarr/app/externalsecret.yaml:14-18`;
   live ExternalSecret template read).

Rollback goes in reverse. Each row above says how.

## 10. Acceptance

### A1: GPU maintenance on Card C

Preconditions: S1 released and pinned; P3 `maintenance-gpu-utility-attested` and P4
applied.

1. A full haynes-ops session (today `haynes-ops-1029-223503`) runs
   `agent-run grant maintenance-request --task-json <task> --reason <r> --ttl 10m`.
   The task: node `talosw01`, node UID `b7ef4b0a-524b-411b-9b47-79e3c37e7269`, resource
   `GPU-d8a856f1-f955-f683-bc24-654561496774`, capabilities `[SYS_ADMIN]`, runtime
   profile `nvidia-utility`, image `ghcr.io/thaynes43/comfyui:0.39.0-2@sha256:991867f5…`,
   100m CPU, 128Mi, the read-only operation and restore text of `grant-1031-132422-84ca`.
2. It sends Tom (or its coordinator) the grant name, UID, digest, TTL, operation and
   restore. The approver replies approve.
3. Within 30 minutes: `agent-run grant attest <grant> --approved-by user --ref <message id>`.
4. Expect `Approved`, `approvedBy policy/maintenance-gpu-utility-attested`, and the
   "not verified human identity" message. Expect an executor Pod on talosw01 with
   RuntimeClass `nvidia`, `NVIDIA_VISIBLE_DEVICES` set to Card C's UUID, caps `SYS_ADMIN`
   only, and a deny-all NetworkPolicy.
5. Through `agent-run grant maintenance-exec`, run read-only `nvidia-smi` queries: only
   Card C visible, power limit still 350 W.
6. Release. Expect the stop proof, the Pod and NetworkPolicy gone, and the grant
   `Released`.

Negative checks: `SYS_RAWIO`, another GPU UUID, a control-plane node, a TTL over 1h, or
a wrong digest each fails to approve.

Evidence: the grant's status and annotations, the broker log lines, the executor Pod
spec, the query output, the cleanup proof, and power readings before and after.

### A2: Apply the saved MAM cookie in Prowlarr

Preconditions: S2 released; P1, P2 and `kube-secret-use-attested` with `downloads`
applied.

1. The agent requests kube `dev-env-grant-secret-use` in `[downloads]`, TTL 30m. The
   reason names `myanonamouse-secret` and `prowlarr-secret` and the indexer change.
2. Message approval, attestation, `Approved` under `policy/kube-secret-use-attested`,
   token installed. `agent-run grant use <grant>`.
3. With the grant context, create a Job in `downloads`: default SA, a digest-pinned
   image, env `MAM_ID` from `myanonamouse-secret` key `MAM_ID_PROWLARR`, env `API_KEY`
   from `prowlarr-secret` key `PROWLARR__AUTH__APIKEY`. It calls
   `http://prowlarr.downloads.svc.cluster.local:9696`: GET `/api/v1/indexer/17`, checks
   the implementation is MyAnonamouse, sets only field `mamId`, then PUT. It may run the
   indexer test. It prints status codes and booleans only.
4. Expect admission to pass. Negative checks: the same Job from `dev-env-agent` is
   refused; the same Job from a C1 grant identity (no `use`) is refused; a Job naming a
   Secret in an ungranted namespace is refused.
5. Release. Expect the SA gone.

Traffic stays inside `downloads`, which has no NetworkPolicy, so no egress grant is
needed. The apply step does not change the Secret, so qbittorrent does not restart.
Saving a new cookie into 1Password does change the Secret, and Reloader then restarts
the seedbox pod. That is the shared Secret's consequence, outside this step.

Evidence: the grant, the Job spec and status, its status-only log, Prowlarr's indexer
17 test result, and qbittorrent's restart count before and after.

## 11. Pending decisions for Tom

| ID | Question | Recommended answer |
|---|---|---|
| Q-042-01 | Which sessions may use the attested classes? | Every `full` and `workbench` session in any repo, and their children. `dev` stays on the baseline; `ops` never matches (D-18). |
| Q-042-02 | Who may approve by message for each class? | Tom or the requester's coordinator for C1 to C5 in the coordinator band. Only Tom for the user band; the requester's statement says so, and nothing verifies it. |
| Q-042-03 | Which namespaces belong to each band for C1 and C2? | The lists in 9.2. Named risk: C2 in `network` lets a Job mount Authentik's Secrets. |
| Q-042-04 | Amend ADR-012 so an attested policy may approve the bounded GPU maintenance class? | Yes, by a new ADR. Other maintenance scopes get their own attested policy by policy PR (6.4). The `dev-env-human` route stays only as a fallback. |
| Q-042-05 | Build delegate decisions (`delegate/<ref>`) now? | No. Defer to #304. Attestation by message covers both acceptance cases. |
| Q-042-06 | Extend hardware access (all eight SSH targets, PVE API, hw-ssh key and Proxmox operator token) to every eligible session on attestation? | Yes, with 1h credential leases. Destructive hardware operations stay H5. |

## 12. Non-goals

- No new approval service, page, per-command gate or Asks dependency.
- No change to the task budget or native control human gates.
- No change to break-glass, Secret read, the login ceremony, or guard self-protection.
- No enforcement of H4; that is DESIGN-039's.
- No live change, grant, policy or flag from this document.
