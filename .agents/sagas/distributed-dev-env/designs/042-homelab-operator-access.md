# DESIGN-042: Homelab operator access activation bundle

- **Status:** Proposed, 2026-10-31. Revised twice the same day after the lead's and
  the coordinator's reviews. Design only. Nothing is granted, activated or changed by
  this document: no policy, guard, role, flag, template or cluster object.
- **Tracking:** OpenProject Epic [#112](https://work.haynesnetwork.com/work_packages/112),
  Saga #85. Related items:
  - #214, the approved-access bundle;
  - #219, phone approvals;
  - #233, OP233 maintenance (dev-env PR #781);
  - #304, optional agent auto-review, a separate layer;
  - #319, the MAM indexer;
  - #113.

  The work order named no story, so questions use `Q-042-NN` until the lead files one.
  Lead `dev-env-1029-132039` reviews PR #782. Do not merge it in this story.
- **Inputs:** Tom's four corrections of 2026-10-31, relayed by his coordinator
  (section 1). Tom's #214 words of 2026-10-26. Two rounds of lead and coordinator
  review on 2026-10-31.
- **Source examined:**
  - dev-env `34624a4dd7ecc2a19edb2d76db96c22fd71e2b33`, and PR #781 at head
    `57f4d3c6acf132c2937ac8ae4d4be3bc83fd650d`;
  - haynes-ops `09614e825a5ab102cca0a9d18bd6259b91c6e0ec`, plus PRs #4501 and #4502;
  - live objects, read with the session's read-only identity on 2026-10-31 between
    13:50 and 14:23Z.
- **ADR bookkeeping (decided):** ADR-013 (PR #781) already amends ADR-012 for
  attested maintenance. When Tom approves this bundle, ADR-014 records three things:
  the two approval modes, the reserved decisions, and the namespace-operator class.
  No Accepted ADR is edited.

`DES/` below means haynes-ops `kubernetes/main/apps/dev-env-system/`. `D001` means
[DESIGN-001](001-dev-env-v2.md).

## 0. Recommended activation scope

Tom approves **one bundle, once** (Q-042-01). Until its flags, roles and policies land,
it changes nothing at runtime. Every item is either default-off source or GitOps data,
and each has a one-step rollback.

**The risks Tom accepts by approving it:**

1. **Eligibility.** Every `full` and `workbench` session, in any repo, may get the
   class grants of section 5 once an approval is recorded. Today only `full` sessions
   in `haynes-ops` can, and only for single hosts.
2. **Mode (a)** is the requester's own attestation. It is a claim and does not
   authenticate the approver. A session that lies gets no more than the matching
   policy allows, for its TTL.
3. **Mode (b).** Listed delegate identities may decide other agents' grants, GPU
   maintenance included, through the authenticated API. A delegate cannot decide its
   own request. The Claude lead becomes a delegate only after it runs under its own
   coordinator SA, which has the same authority as today. That step waits on the
   template hold (P6).
4. **Namespace operator.** A grant in named app namespaces may exec into pods, restart
   workloads, and run Jobs that mount the Secrets the grant names. It never gets
   Secret `get` or `list`. An agent-chosen command can still print a Secret it can
   reach, so the grant does not stop the agent reading one.
5. **Hardware.** Eligible sessions may lease two credentials for 1h:
   - the shared hw-ssh key: root on PiKVM and HaynesTower, sudo-allowlisted on the PVE
     nodes;
   - a Proxmox operator token, which is root-equivalent.

   Release removes the files but cannot recall a copy.
6. **GPU.** A message may approve SYS_ADMIN maintenance on Card C, within #781's
   exact-envelope policy.

The reserved decisions (section 7) stay with Tom.

**Persistent deltas, in activation order:**

| # | Repo | Change | Status | Rollback |
|---|---|---|---|---|
| S1 | dev-env | Attested maintenance (ADR-013): an attested GrantPolicy with an exact maintenance envelope; flag `--enable-attested-maintenance` | PR #781, open; default off | flag false |
| S2 | dev-env | Delegate decisions (6.2): flag `--grant-delegates` on operator and broker; `POST /v1/grants/{name}/decision`; GrantPolicy `spec.delegated`; `Decide` accepts `delegate/<ns>/<sa>`; self-decision refused | new; default off (empty list) | empty the list |
| S3 | dev-env | `agent-run grant decide` and an MCP tool; `secrets` on grant requests; CLI and MCP say when no policy covers a request | new, small | revert |
| S4 | dev-env | Named coordinator identity (6.3): templates accept a `dev-env-coordinator-<name>` SA; such a session pod stays a session caller, as the workbench does | new, small | revert; no profile names it |
| S5 | dev-env | Named Secrets (5.1): `KubeGrant.secrets`; role `dev-env-grant-namespace-operator` in the enums and the broker catalog; `ScopeDigest` covers `secrets` | new, small | revert; no policy names the role |
| R1 | haynes-ops | CRD copy from the release that carries S1 to S5 | routine | remove delegated-only, maintenance and namespace-operator policies first, then revert |
| R2 | haynes-ops | Operator and broker pins to that release | routine | pin back |
| P1 | haynes-ops | Ten GrantPolicies (9.1) | **activation** | delete a policy; matching stops and active grants run out |
| P2 | haynes-ops | Broker `--enable-attested-maintenance=true`; operator and broker `--grant-delegates=<list>` | **activation** | false; empty list |
| P3 | haynes-ops | `dev-env-maintenance-annotations` also allows the operator's set-once decision annotation on kube and egress grants | **activation** | revert; delegates then decide maintenance only |
| P4 | haynes-ops | ClusterRole `dev-env-grant-namespace-operator`; broker `bind` and broker-guard catalog entries | **activation** | revert; active grants end at release or expiry |
| P5 | haynes-ops | Identity guard: one clause; new VAP `dev-env-grant-secret-names` (5.1) | **activation** | revert; new Secret references are refused again |
| P6 | haynes-ops | SA `dev-agents/dev-env-coordinator-lead` with exactly `dev-env-agent`'s bindings; agent-guard match extended to it; a `coordinator` profile in `dev-env-templates` | **blocked by the global template hold** | revert; the lead relaunches on `full` |

Order:

1. Merge S1 to S5 and release.
2. Land R1, then R2.
3. Open one held haynes-ops PR with P1 to P5. **That PR is the single approval.**
4. Land P6 when the template hold allows it.
5. Run A1 and A2 (section 10).

All source stays inert until step 3.

**Already live, no change needed** (deployment args read at 13:50Z):

- broker: `--enable-attested-grants=true` and `--enable-maintenance-grants=true`;
- operator: `--enable-grant-attestation=true` and `--enable-maintenance-api=true`.

The only maintenance switch left is #781's `--enable-attested-maintenance`.

**Not in scope:**

- refusing an approval ref that is reused across digests;
- OP304's agent auto-review.

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

These correct implementation drift. They do not remove safety. The bundle restores
v1's operator reach through v2's existing pieces:

- scoped task grants and per-grant identities;
- TTL, release and audit;
- D-88 attestation.

It adds three things: one authenticated delegate decision route, one reusable
namespace role, and one field that names Secrets.

Two cases are its acceptance tests, not one-off exceptions:

- GPU maintenance with SYS_ADMIN on Card C of talosw01 (A1);
- applying the saved Prowlarr cookie to indexer 17 through a grant (A2).

## 2. Inventory: what exists today

### 2.1 Approval paths by grant type (dev-env `34624a4`)

| Grant type | Standing policy | Attested policy (D-88) | Other decision | Live policies (13:50Z) |
|---|---|---|---|---|
| kube | yes | yes | none: only `internal/broker/maintenance_decision.go:32` calls `Decide`. The phone page is #219, in progress. | `kube-nodes-attested` |
| egress | yes | yes | none (same) | `lan-ceph-mgr-metrics`, `lan-internal-ingress`, `lan-pve-api-lb`, `lan-pve-api-nodes`, `lan-ssh-tier`, `egress-haynesintelligence-ssh-attested`, `egress-pikvm-ssh-attested` |
| credential | yes | yes | refused (`internal/broker/seams.go:151`) | `credential-proxmox`, `credential-hw-ssh-attested` |
| maintenance | refused (`api/v1alpha1/accessgrant_types.go:463`; `internal/broker/match.go:111`) | refused at `34624a4`; PR #781 adds it, default off | only `dev-env-system/dev-env-human` (`internal/apiserver/maintenance.go:36`; `seams.go:143`) | none |
| breakglass | refused (`match.go:79`; CRD `:463`) | refused | none; the role is not in git | none |

- Roles `dev-env-grant-breakglass` and `dev-env-grant-secrets-read` never match
  (`match.go:120`; CRD `accessgrant_types.go:573`). Profile `ops` never matches
  (`match.go:83`).
- `KubeGrant` carries only `role` and `namespaces` (`accessgrant_types.go:248-266`).
  No grant can name a Secret today.
- `dev-env-human` is the token Tom mints on his laptop with his admin kubeconfig
  (`D001:340-342`). A maintenance decision therefore needs the Mac. Tom rejects that
  route in quote 3.
- A grant that no policy matches waits 30 minutes and is then denied by `timeout`
  (`broker.go:65`). Nobody is told.

### 2.2 Human-only gates and caller classes (verified)

| Route | Gate | Cite |
|---|---|---|
| `POST /v1/grants/{name}/maintenance-decision` | `kindHuman` only | `internal/apiserver/maintenance.go:36` |
| task budget `extend` | `kindHuman`; a coordinator reaches the handler and is refused | `taskbudgets.go:207`; `server.go:258` |
| task budget `continue` | `kindHuman` | `taskbudgets.go:347` |
| native control | `kindHuman` | `native_control.go:41` |

- Caller classes come from the TokenReview username, never from the body
  (`caller.go:20-40`, `auth.go:87`).
- `resolveCaller` checks in this order (`caller.go:121-139`):
  1. Tom's human SA;
  2. a workbench session pod;
  3. clients;
  4. the shared session SA `dev-agents/dev-env-agent`;
  5. configured coordinator hosts.
- A coordinator host must run as its own SA, from a pod with that exact name, a live
  UID, and the `coordinator-host` annotation (`coordinator.go:64-76`). Live, one host is
  configured: `dev-agents/dev-env-owned-native-fixture`, a Codex fixture
  (`DES/operator/app/helmrelease.yaml:294`).
- Every Claude session, the lead included, runs as the shared session SA, so none can
  call as a coordinator.
- A pod classified as a coordinator is never a session caller. A coordinator-host
  entry would therefore cut a Claude lead off its session routes: heartbeat, grant
  requests, attestation and inbox.
- No coordinator may decide a grant today. The human maintenance route stays as a
  fallback. The task budget and native control gates are out of scope.

### 2.3 Attestation as built (D-88)

- Only the grant's own requesting session may attest (`grant_attestation.go:24`). It is
  a requester self-statement. The broker message says "agent attestation, not verified
  human identity" (`internal/grantattest/attest.go:49`, used by `Message` at `:235`).
- The statement binds:
  - the grant UID;
  - the scope digest;
  - the TTL;
  - the attestor's session, session UID and pod UID, taken from the token
    (`attest.go:81-90`).

  One statement is allowed per grant. An identical retry returns 200; a different one
  returns 409. It must arrive within the 30-minute pending window.
- `ScopeDigest` (`attest.go:162`) covers type, credential, role, namespaces, egress
  destinations and ports, TTL, session and session UID. PR #781 extends it to the whole
  maintenance envelope.
- The approver is `user` or `coordinator:<ref>`. The ref is checked for syntax only
  (`attest.go:52-54, 93-102`). No requester/approver separation exists.
- The broker re-verifies the statement against the live session and its running pod
  (`internal/broker/attestation.go:48`). It writes `approvedBy policy/<name>`
  (`broker.go:222`), and it emits an Event on the grant (`broker.go:533-536`).
- Known gap (`D001:4163`): kube and egress execution never re-read `approvedBy`.

### 2.4 Grant identities, catalog and guards

| Piece | What it does | Cite |
|---|---|---|
| Kube grant identity | The broker creates SA `dev-agents/grant-<id>` (no automount) and binds catalog roles to it, by RoleBinding per namespace or one ClusterRoleBinding. It mints a token for the time left, installs it into the pod's grants tmpfs by exec, and deletes the SA on release or expiry. | `internal/broker/kube.go:40, 142-176`; `installer.go:54`; `broker.go:482-496` |
| `dev-env-grant-workloads` | Namespaced. Full CRUD on Apps workloads, Jobs, CronJobs, pods, services and configmaps; read on logs and events. No Secrets, no exec. | `DES/rbac/app/grant-catalog.yaml:24-51` |
| `dev-env-grant-storage` | Namespaced. Creates and deletes PVCs and VolumeSnapshots; deletes StatefulSets. | `grant-catalog.yaml:56-74` |
| `dev-env-grant-secrets-read` | Namespaced. Secrets get and list. Never policy-approved. | `grant-catalog.yaml:79-88` |
| `dev-env-grant-nodes` | Cluster-wide. Node patch, pod eviction. | `grant-catalog.yaml:93-114` |
| `dev-env-grant-breakglass` | Named in the broker guard and the CRD. **Not in git and not live.** | `grant-catalog.yaml:15-16`; live NotFound |
| `dev-env-agent-guard` | Matches only `dev-agents/dev-env-agent`. Jobs run as `default` and mount no Secret except `*-volsync-aws-secret`. Workloads: restart only. CronJobs: suspend only. Flux objects: reconcile and suspend only. ExternalSecrets: force-sync only. | `DES/rbac/app/agent-guard.yaml:57-60, 67-68, 175-182` |
| `dev-env-identity-guard` | Matches every `dev-agents` SA except the workbench, so every `grant-*` identity. No writes in the dev-env namespaces. Grants write nothing in flux-system, kyverno, external-secrets or kube-system. Pods run as `default` and meet the baseline. **No new Secret reference** except the volsync suffix. | `DES/rbac/app/identity-guard.yaml:50-54, 176, 254` |
| `dev-env-broker-guard` | The broker touches only `grant-*` objects in `dev-agents`, and binds only catalog roles to the grant's own SA. | `DES/rbac/app/broker-guard.yaml:52-56, 78-119` |
| `dev-env-maintenance-annotations` | Only the operator sets the decision annotation, once, and only on a maintenance grant. | `DES/rbac/app/maintenance-annotations.yaml:16-35` |
| Maintenance executor | A bare Pod pinned to the node and its UID. It runs as SA `dev-env-maintenance` with no token, drops ALL capabilities and adds back only the named ones, is not privileged, and gets a deny-all NetworkPolicy. Profile `nvidia-utility` adds RuntimeClass `nvidia` with `NVIDIA_VISIBLE_DEVICES=<GPU UUID>`. Control-plane nodes are refused. | `internal/broker/maintenance_executor.go:27-96`; `maintenance_scope.go:142-151` |
| Egress grants | A CNP `grant-<id>` in `dev-agents` for the session's pods. Targets are FQDNs, CIDRs, or in-cluster endpoints by namespace and pod labels, with up to 16 ports. No Service names. | `internal/egress/policy.go:37-93` |
| Templates | A profile may name a ServiceAccount, but validation accepts only `dev-env-workbench`. | `internal/templates/templates.go:270-273, 491-492` |

The baseline `dev-env-agent` role already allows:

- cluster-wide `pods/exec`, `services/proxy` and `pods/proxy`;
- Job create and delete, and pod delete;
- the restart and reconcile patches.

See `DES/rbac/app/agent.yaml:78-118`. The role has no Secret read.

Exec stays refused in the dev-env and controller namespaces, and into pods whose SA is
on the Kyverno exec guard's privileged list. Prowlarr is on neither list, so the
baseline agent may already exec into it. This design does not use that route.

### 2.5 Live evidence of the drift

| Object (read 13:50 to 13:56Z) | What happened |
|---|---|
| `grant-1031-033915-fc19` (maintenance, Card C, SYS_ADMIN, 10m) | Denied, `deniedBy: timeout`, "no answer in 30 minutes" |
| `grant-1031-132422-84ca` (same scope) | Pending at 13:51Z; Denied by `timeout` when read at 13:55:48Z |
| `grant-1031-033808-7327` (same, without SYS_ADMIN) | Released before an answer |
| `grant-1030-122127-d7d9`, `grant-1030-130136-9897` (kube `dev-env-grant-nodes`, workbench, haynes-ops) | Denied by `timeout`. `kube-nodes-attested` names only profile `full` and repo `haynes-ops`, so a workbench session matched nothing. This is the whack-a-mole Tom describes. |

## 3. v1 baseline against v2 today

v1's role `dev-env-operator` (haynes-ops `kubernetes/main/apps/dev/dev-env/app/rbac.yaml:16-136`)
is the template for v2's `dev-env-agent`. The two differ in admission, mounted
credentials, network and approvals. D-70 also counts v1's effective reach through
Headlamp's `cluster-admin` SA (`D001:3947-3960`).

| # | Capability | v1 | v2 today | Gap and fix |
|---|---|---|---|---|
| — | Kubernetes reads without Secrets; exec into app pods; proxy; pod delete; restart; reconcile; CronJob suspend; force-sync | yes | yes, guarded (D-19) | none |
| G1 | A Job that mounts an app Secret | yes. No admission policy matches `dev/dev-env` except `dev-env-v1-token-guard`, which covers tokens only (live read). | refused at runtime | **gap**. Fixed by C2 (S5, P4, P5). R-04 lists it as P-05. |
| G2 | Runtime workload spec changes | patch, unguarded | restart only | deliberate (D-19, GitOps); Flux reverts drift |
| G3 | Capability pods on a node (GPU) | only through Headlamp `cluster-admin` (D-70) | maintenance grant, decided only on the Mac | **approval gap**: S1, S2 |
| G4 | Hardware: SSH to 8 hosts, PVE API, Proxmox operator token, Ceph metrics | key and operator token mounted; the CNP allows all eight SSH targets, pvedash 443, nodes on 8006 and Ceph on 9283 (`networkpolicy.yaml:441-487`) | workbench: key mounted, standing LAN policies. full: attested hw-ssh lease, SSH to two hosts, haynes-ops only. Proxmox credential: workbench only. | **gap** for full sessions and other repos: P1 |
| G5 | Omni | Admin key mounted | Admin key in `full` and `workbench` (`DES/templates/app/templates.yaml:124-127, 183-186`) | none |
| G6 | In-cluster app APIs from the session | a fixed list, plus internal ingress on 8443 | platform tier plus `lan-internal-ingress` | small: P1 |
| G7 | Approval outside a standing policy | not needed | attested policies cover only `full` + `haynes-ops`; maintenance needs the Mac; everything else times out | **the main gap**: S2, P1, P6 |

The v1 MAM route is **Unknown**. No runbook describes it: a haynes-ops grep for
`myanonamouse`, `MAM_ID` and `prowlarr cookie` found none. `MAM_ID_PROWLARR` is mounted
in no pod. Only `MAM_ID_SEEDBOX` is, in the qbittorrent sidecar
(`kubernetes/main/apps/downloads/qbittorrent/app/helmrelease.yaml:165-169`). So exec
alone cannot reach the cookie. The likely v1 route was G1: an unguarded Job that
mounts the Secret.

## 4. Prior records: drift or deliberate

| Record | Restriction | Verdict | Action |
|---|---|---|---|
| Q-16 ruling, `D001:5338` | "Standing grants cover parity"; "Human approval gates only capabilities beyond today's" | Governs | This bundle implements it |
| D-70, `D001:3947` | "Do not add a new approval gate to an existing capability" | Governs | Same |
| `D001:348` | "only Tom (on the console) can approve a grant" | Drift; Q-16 and D-88 came later | Superseded by D-88; mode (b) added |
| D-26, D-27 | Tom approves on a page | Already superseded by Q-16 and D-70 | None |
| D-54, D-61: schema and match refuse breakglass and secrets-read | Beyond v1's OPERATOR tier | Deliberate | Reserved decisions RD2 and RD3 |
| CRD `:463` refuses maintenance policies | Human-only GPU work | Drift (D-70, quote 1) | S1 (#781) |
| ADR-012 | Only the human API identity decides maintenance | Deliberate when written; corrected by quote 3 | ADR-013 (#781) amends it; ADR-014 adds mode (b) |
| D-88 policies scoped to `full` + `haynes-ops`, one host each | Each scope approved one at a time | Drift: whack-a-mole | Widen by class (P1) |
| D-19 guards on the baseline identity | Runtime writes go through git | Deliberate | Keep |
| Volsync-only Secret allowlist | Blocks R-04 P-05 and MAM | Drift: v1 had no Job guard | C2 (P5) |
| Templates accept only the workbench SA | One non-default identity | Deliberate then; blocks mode (b) for the lead | S4, P6 |
| Workbench-only LAN and Proxmox policies (Q-110-08) | Hardware only in the workbench | Deliberate then; drift now | P1 |
| [DESIGN-034](034-agent-asks-and-approvals-board.md) | Only a portal human answers an Ask | Deliberate for Asks | No change; grants never wait on the board |
| [DESIGN-039](039-access-policy-change-guard.md) Q-269-03 | Who approves authority changes | Deliberate, pending | RD1 defers enforcement to it |

## 5. Capability classes

Five classes cover a home lab. C2 needs one new role and one schema field. The others
use the existing grant types.

| Class | Mechanism | Runtime identity | The request names | Credentials |
|---|---|---|---|---|
| **C1 App operator** | kube grant, `dev-env-grant-workloads` | `dev-agents/grant-<id>`; token in the grants tmpfs; selected with `agent-run grant use` | role, namespaces (up to 16), TTL, reason | none new |
| **C2 Namespace operator** | kube grant, new `dev-env-grant-namespace-operator`, with named Secrets | `dev-agents/grant-<id>`; its Jobs run as the namespace's `default` SA | role, namespaces, `secrets` (up to 16 names), TTL, reason | existing app Secrets, used in place |
| **C3 Cluster service API** | egress grant: `endpoints` (namespace + pod labels) and ports | the session's pods, through CNP `grant-<id>` | namespace, labels, ports, TTL | none |
| **C4 LAN and host** | egress grant (FQDN or CIDR; ports 22, 443, 8006, 9283) and credential grant (`hw-ssh`, `proxmox`) | session pods; the keeper installs credentials into the pod tmpfs | hosts, ports, credential, TTL | existing hw-ssh key; keeper-minted Proxmox token |
| **C5 Node maintenance** | maintenance grant | broker-created executor Pod on the named node; commands through the operator exec route | node and its UID, GPU UUID, runtime profile, capabilities, image digest, CPU, memory, operation, restore, TTL | none |

| Class | Max TTL | Revocation and cleanup | Audit | Status |
|---|---|---|---|---|
| C1 | 3h; 8h in the infrastructure namespaces | Release or expiry deletes the SA, so every token dies. RoleBindings go with it. | AccessGrant kept 90 days (`broker.go:68`); `approvedBy`; the attestation or decision annotation; an Event on the grant | role exists; policies new |
| C2 | 1h | As C1. Release does not stop a running Job, so the guard caps `activeDeadlineSeconds` and `ttlSecondsAfterFinished` at 3600. | As C1, plus the Job spec records each Secret reference | new, small |
| C3 | 3h | The broker deletes the CNP; the expiry backstop also does. | grant, Hubble flows | mechanism exists; policy new |
| C4 | egress 3h, SSH 1h; credentials 1h | CNP deleted; credential removed from tmpfs. Removal does not revoke a copied static key (D-89). | grant, CredentialJob receipt, keeper journal | exists; policies widened |
| C5 | 1h (30m default) | `stopMaintenance` proves the Pod stopped on the node UID, then deletes the Pod and its NetworkPolicy. | grant, attestation or decision, exec log lines, restore evidence | S1 (#781) |

### 5.1 C2: one reusable runtime path for exec and Secret use

This replaces bespoke per-service Jobs written in git. It reuses the kube grant
machinery (D-56 to D-63): a per-grant SA, RoleBindings in the named namespaces, TTL,
release and the Event.

**The role** `dev-env-grant-namespace-operator` (P4) is a namespaced ClusterRole with
the grant-role label. Its rules:

| Resource | Verbs |
|---|---|
| `pods` | get, list, watch, delete |
| `pods/exec` | create |
| `pods/log` | get |
| `deployments`, `statefulsets` | get, list, watch, patch |
| `jobs` | get, list, watch, create, delete |
| `configmaps`, `events` | get, list, watch |
| `secrets` | the custom verb `use` only; no get, list or watch |

The patch verb exists for rollout restart. Other spec patches are also possible for a
grant identity, and Flux reverts them.

**Schema** (S5): `KubeGrant.secrets` holds up to 16 names. They must be DNS-1123
names, and the field is allowed only with this role. `ScopeDigest` covers them, so both
an attestation and a delegate decision bind the exact names. The API also passes them
on requests (S3).

**Guard deltas** (P5). There are two, because a guard cannot read a grant's scope
without taking the grant as a param:

1. **Identity guard rule 4** (`identity-guard.yaml:254`) gains one clause. A
   `grant-*` identity may add Secret references when
   `authorizer.group('').resource('secrets').namespace(request.namespace).check('use').allowed()`.
   - This is one authorization check per request. Each check costs 350000 of the
     1000000 per-expression CEL budget, so per-Secret checks would refuse three or
     more references.
   - The same clause caps `activeDeadlineSeconds` and `ttlSecondsAfterFinished` at
     3600.
   - `dev-env-agent` and C1 identities never hold `use`, so they stay refused.
2. **New VAP `dev-env-grant-secret-names`**. Its `paramKind` is AccessGrant, and its
   binding `paramRef` reads namespace `dev-agents` with selector `{}`.
   - It matches pod-bearing creates and updates by `grant-*` usernames.
   - For each grant param, it requires:
     `params.metadata.name != <caller's grant name> || (request.namespace in params.spec.kube.namespaces && addedSecrets.all(n, n in params.spec.kube.secrets))`.
   - The caller's own grant is always a param, because its SA is named after it.
     Every other param passes trivially.
   - `parameterNotFoundAction: Allow` is safe: with no grants there are no grant
     identities.
   - Evaluation runs over every retained grant, for grant-identity requests only.

Before activation, a server dry-run on the 1.35 API server must confirm four things:
the custom verb, the `authorizer` check, per-param evaluation, and the cost at the
retained grant count.

**Honest risk.** An agent-chosen command in a pod that holds a Secret can print it.
That applies to exec into an app pod, and to a Job the agent writes. So the grant does
not stop the agent reading a Secret it can reach. The controls are:

- the session rule: secrets never go into transcripts, PRs or the tracker;
- the 1h TTL;
- the Event and the AccessGrant record per grant;
- no `dev-env-grant-secrets-read` by policy;
- break-glass unchanged;
- controller and dev-env namespaces still refused;
- the named-Secret list, which bounds what a new pod may mount.

Exec into app pods is already v1-equivalent in v2's baseline (2.4). C2's new reach is
Secret use for Secrets that no pod mounts, such as `MAM_ID_PROWLARR`.

**GitOps one-shots** stay possible as routine deploys, in App namespaces and `network`
only. They are an emergency route, not the grant path.

## 6. Approval: two modes

Both modes approve only inside a GrantPolicy that Tom approved in git. They differ in
who records the decision.

| | **Mode (a): policy delegation to the requester** | **Mode (b): authenticated delegate decision** |
|---|---|---|
| Who records it | the requesting session itself (`agent-run grant attest`) | a different, allowlisted identity (`agent-run grant decide`) |
| What it proves | the requester's own claim that `user` or `coordinator:<ref>` approved, citing a message ref. It does **not** authenticate the claimed approver. | that an authenticated, pod-bound identity in `--grant-delegates` decided this exact request |
| Binding | grant UID, session UID, pod UID, scope digest, TTL; one statement per grant; 30-minute window | grant UID, scope digest, TTL no longer than requested; a set-once decision annotation; 30-minute window |
| Separation | none: the requester and the attestor are the same session | the delegate may not be the requester, the requester's pod or session, or a coordinator or worker named in a maintenance envelope |
| Policy block | `spec.attested` (exists) | `spec.delegated` (new, S2) |
| Recorded as | `approvedBy policy/<name>`, with "agent attestation, not verified human identity" | `approvedBy delegate/<ns>/<sa>`, with the ref and "not a human decision" |
| Grant types | kube, egress, credential; maintenance with S1 | kube, egress, maintenance. Credential is not built, because keeper execution re-checks a `policy/` approver (`credentials.go:71`). |

Neither mode is verified human authority. An attested `user` claim is never described
as one.

### 6.1 Flow

1. The requester creates the grant without waiting. It uses
   `agent-run grant request --no-wait`, MCP `request_access`, or
   `agent-run grant maintenance-request`. It then reads the name, UID, digest and TTL
   with `agent-run grant show`.
2. It sends one message with those values, the reason and the risk:
   - a Remote Control session asks Tom through AskUserQuestion on his phone, or asks
     its coordinator;
   - a headless task messages its coordinator, with `agent-run msg` or the durable
     inbox.
3. The approver answers. Silence means deny.
   - **Mode (a):** the requester runs
     `agent-run grant attest <grant> --approved-by user|coordinator:<session> --ref <message id>`.
   - **Mode (b):** a delegate runs
     `agent-run grant decide <grant> --grant-uid <uid> --digest <digest> --approve [--ref <message id>]`.
     When the delegate relays Tom's answer, the ref names Tom's message.
4. The broker approves only when all of these hold:
   - a matching policy carries the right block;
   - the mode's checks pass;
   - the grant is still Pending.

   Otherwise the grant times out.

A grant under an attested or delegated policy is never approved without a statement or
decision that cites its ref. Standing policies such as `lan-*` and `credential-proxmox`
approve without one, as they do today.

### 6.2 Delegate decisions (S2)

- **Allowlist:** `--grant-delegates=<ns>/<sa>,...`, set on both the operator and the
  broker. The two lists must match, as the human SA pair already does.
- **Two delegate forms.** Both are TokenReview-authenticated and pod-bound:
  1. a configured coordinator host, verified as `coordinator.go:64-76` does;
  2. a session pod running as an allowlisted delegate SA (S4). Its pod must be
     controlled by a Running AgentSession, as the workbench rule checks
     (`caller.go:189-200`).

  The second form exists because classifying the lead as a coordinator would cut it
  off its session routes (2.2).
- **Route:** `POST /v1/grants/{name}/decision` takes:
  - the grant UID and the digest (`ScopeDigest`, which covers the maintenance
    envelope after #781 and the Secret names after S5);
  - approve or deny, and the TTL;
  - the ref.

  It writes the existing set-once decision annotation. P3 allows that annotation on
  kube and egress grants.
- **Refused:**
  - a caller not on the list;
  - the requester itself: the same pod UID, or a pod controlled by the requester's
    session;
  - a coordinator or worker named in a maintenance envelope;
  - a grant that is not Pending, is past 30 minutes, or was already decided;
  - a digest mismatch;
  - credential and breakglass grants;
  - the roles `dev-env-grant-secrets-read`, `dev-env-grant-breakglass` and
    `dev-env-grant-storage`.
- **Broker:**
  1. It checks the annotation's binding.
  2. It checks that a policy with `spec.delegated` covers the scope and the requester.
     For maintenance, that means #781's exact-envelope match.
  3. It calls `Decide` with `delegate/<ns>/<sa>`.
  4. It writes no `human/` string.
- **Schema:** a policy may carry `attested`, `delegated`, or both. Its top-level
  `profiles`, `repos` and `agents` stay empty. The requester rule at
  `accessgrant_types.go:467` accepts either block.
  - An older broker ignores `delegated`, so a delegated-only policy matches nothing.
  - An older CRD prunes the field, and the rule then refuses the policy, so a Flux
    apply fails loudly.
  - So remove delegated-only policies before reverting the CRD.

### 6.3 A named coordinator identity for the Claude lead (S4, P6)

This is the smallest change that lets mode (b) serve the actual lead. It shares no
credentials and keeps current authority as it is.

- **S4 (dev-env):**
  - `templates.Validate` (`templates.go:491`) also accepts SAs named
    `dev-env-coordinator-<name>`.
  - `resolveCaller` treats a session-controlled pod running as such an SA as a
    session (`caller.go:121-124`), as it does for the workbench. `sessionPodAccount`
    (`caller.go:160`) accepts that SA.
- **P6 (haynes-ops):**
  - SA `dev-agents/dev-env-coordinator-lead`, added as a subject to ClusterRoleBinding
    `dev-env-agent` and RoleBinding `dev-env-agent-cnpg-remediation`. Those are exactly
    the baseline bindings.
  - `dev-env-agent-guard`'s match condition (`agent-guard.yaml:57-60`) is extended to
    that username, so its Jobs and patches stay guarded. The identity guard and the
    Kyverno exec guard already cover every `dev-agents` SA.
  - A `coordinator` profile in `dev-env-templates` (`DES/templates/app/`): the `full`
    profile plus `serviceAccount: dev-env-coordinator-lead`, with the same pod labels,
    so the same network tiers apply.
  - `--grant-delegates` names `dev-agents/dev-env-coordinator-lead`.
- **Blocking prerequisite:** `dev-env-templates` is under the global template hold.
  Until P6 lands, mode (b) serves only coordinator hosts, and the lead approves through
  mode (a).
- A pod's SA cannot change in place. The lead moves to the profile at its next launch
  or resume. Running sessions are not touched.

### 6.4 Escalation

A request that no policy covers matches nothing. Today it times out silently. S3 makes
the CLI and MCP say so before the agent waits. The agent then asks its coordinator, or
asks Tom's phone if the coordinator cannot approve. If Tom wants that scope from now
on, an agent opens a policy PR; that is reserved decision RD1. There is no one-off
route outside the policies.

## 7. Reserved decisions and external constraints

A reserved decision is decision authority that stays with Tom. It never means that a
human must execute the task or be the runtime actor. Tom decides through any channel:
a message to an agent, a phone answer, or a PR approval. Agents then record the
decision and carry out the approved task.

| # | Reserved decision | Why | Enforced by |
|---|---|---|---|
| RD1 | Changing the bounds: GrantPolicies, catalog roles, guards, the delegate allowlist, broker and operator RBAC and flags | An agent that widens its own bounds makes them meaningless | Convention today: agents can self-merge haynes-ops (Q-16 residual risk). DESIGN-039 proposes enforcement; Q-269-03 is pending. |
| RD2 | Break-glass | It removes every other boundary at once | `match.go:79`, `seams.go:139`, CRD `:463`; the role does not exist |
| RD3 | Secret read through the API (`dev-env-grant-secrets-read`, any namespace) | It lists and returns every value at once, with no record of which were used | `match.go:120`, CRD `:573`. C2 is the delegable form: named Secrets, used, not listed. |
| RD4 | Destructive storage or data operations: `dev-env-grant-storage`, deleting a database, Ceph pool, volume or backup, disk wipes, firmware writes | A rollback does not undo them | No policy names `dev-env-grant-storage` or the storage and data namespaces (9.2). For hardware it is a convention: the operation states that it is non-destructive, or waits for Tom. |

One residual risk sits under RD4. `kube-nodes-attested` (cluster-wide node patch and
eviction) already reaches storage pods and nodes, and a NoExecute taint bypasses PDBs.
The bundle only widens who may request it. The baseline's narrow `database` PVC delete
for a CNPG re-clone stays.

**External constraints.** These are not platform policy, and no grant can lift them:

- Provider logins, including the Claude Max and Codex OAuth ceremony. The provider
  requires the account owner's browser consent. One refresher is allowed per token
  family: a second one revoked the family on 2026-08-29. The keeper owns refresh.
- Third-party site confirmations, such as creating a new MAM session on its site,
  email or 2FA prompts, and captchas.
- GitHub admin settings: org and repo admin, rulesets, App installation and repo
  secrets. The bot has no admin rights.
- Creating or rotating password-manager items. Agents read them only through
  ExternalSecrets.

Refused by code for everyone:

- maintenance on control-plane nodes (`maintenance_scope.go:142-151`);
- grants in the dev-env namespaces;
- profile `ops`.

## 8. Issuance rules

- **Who requests:** a session, for its own pod (`grants.go:45`). The session must use
  the `full`, `workbench` or `coordinator` profile; any repo qualifies. Children get no
  wider profile than their parent. `ops` never matches.
- **What a request names:** the class's scope fields (section 5), an exact TTL, and a
  reason that says what changes and how to restore it. C2 names each Secret.
- **Never inferred:**
  - approval from silence, from a relay, or from a peer's claim;
  - approval from a decision on a different digest;
  - a new TTL from an old approval;
  - a namespace from an app name;
  - a Secret name;
  - a GPU from its node;
  - a delegate's identity from message text.

  A renewal is a new grant and needs a new approval.

| Class | Mode (a) or (b) | Mode (b) only | Concurrency per session (a rule; the broker enforces none) |
|---|---|---|---|
| C1 | 3h, app namespaces | 8h, infrastructure namespaces | 2 active |
| C2 | 1h, app namespaces and `network` | — | 1 per namespace |
| C3 | 3h, app namespaces | — | 2 active |
| C4 egress | 3h, SSH 1h | — | — |
| C4 credential | 1h, mode (a) only | — | 1 per credential |
| C5 | 1h, #781 exact envelope | — | 1 per GPU |

## 9. Policy and namespace detail

### 9.1 The ten GrantPolicies (P1)

Requesters are `profiles: [full, workbench, coordinator]`, with no repo restriction.
They go under `attested`, `delegated`, or both, as listed.

| Policy | Type | Blocks | Scope | maxTTL |
|---|---|---|---|---|
| `kube-workloads` | kube | attested, delegated | `dev-env-grant-workloads`; app namespaces | 3h |
| `kube-workloads-infra` | kube | delegated | the same role; infrastructure namespaces | 8h |
| `kube-namespace-operator` | kube | attested, delegated | `dev-env-grant-namespace-operator`; app namespaces and `network`. Secret names come per grant. | 1h |
| `egress-cluster-services` | egress | attested, delegated | endpoints by app namespace, and the app Service ports, up to 16. The port list is Unknown until the PR enumerates it. Whether an empty `matchLabels` admits any pod is Unknown and is tested first. | 3h |
| `kube-nodes-attested` (widen) | kube | attested, delegated | `dev-env-grant-nodes` | 3h |
| `lan-ssh` | egress | attested, delegated | the eight `lan-ssh-tier` FQDNs and the PiKVM CIDR of `egress-pikvm-ssh-attested`, port 22 | 1h |
| `lan-pve-api` | egress | attested, delegated | `pvedash.haynesnetwork` and the five node FQDNs, ports 443 and 8006 | 3h |
| `credential-hw-ssh-attested` (widen) | credential | attested | `hw-ssh` | 1h |
| `credential-proxmox-attested` | credential | attested | `proxmox` | 1h |
| `maintenance-card-c` | maintenance | attested, delegated | #781's exact envelope: talosw01 and its node UID, Card C's GPU UUID, `nvidia-utility`, the pinned comfyui image digest, capabilities within `[SYS_ADMIN]`, and CPU and memory caps | 1h |

#781 matches envelopes exactly, so `maintenance-card-c` covers one GPU and one image.
A second GPU needs its own policy (RD1). That keeps maintenance precise, at the cost
of one policy per target.

### 9.2 Namespaces (33 live)

- **App** (C1, C2 and C3): `ai`, `downloads`, `frontend`, `home-automation`, `media`,
  `observability`, `office`, `photos`.
- **Infrastructure** (C1 under mode (b) only; C2 in `network` only): `arc-runners`,
  `arc-systems`, `cert-manager`, `k8tz`, `network`, `upgrade-agent`.
  - C2 stays out of `cert-manager`, which holds the DNS token.
  - C2 stays out of `upgrade-agent`, which holds the bot and LLM keys.
- **Storage and data** (RD4, no policy): `ceph-csi`, `database`, `openebs-system`,
  `rook-ceph`, `storage-util`, `volsync-system`.
- **Never:** `dev-env-system`, `dev-agents`, `dev-tools`, `dev-env-budgets`, `dev` (the
  v1 pod), `default`, `flux-system`, `kyverno`, `external-secrets`, `kube-system`,
  `kube-public`, `kube-node-lease`, `cilium-secrets`.

## 10. Acceptance

### A1: GPU maintenance on Card C (pending)

Preconditions:

- S1 is released and pinned.
- `maintenance-card-c` and `--enable-attested-maintenance=true` are applied.
- The ADR-012 admission policies are present. The annotation guard is live. The
  Kyverno executor policy's live state is Unknown, because this identity cannot read
  it.

Steps:

1. A full session (today `haynes-ops-1029-223503`) runs
   `agent-run grant maintenance-request --task-json <task> --reason <r> --ttl 10m`.
   The task names:
   - node `talosw01`, with node UID `b7ef4b0a-524b-411b-9b47-79e3c37e7269`;
   - resource `GPU-d8a856f1-f955-f683-bc24-654561496774`;
   - capabilities `[SYS_ADMIN]` and profile `nvidia-utility`;
   - image `ghcr.io/thaynes43/comfyui:0.39.0-2@sha256:991867f5…`;
   - 100m CPU and 128Mi memory;
   - the read-only operation and restore text of `grant-1031-132422-84ca`.
2. It sends Tom or its coordinator the grant name, UID, digest, TTL, operation and
   restore.
3. **Mode (a):** the approver replies approve. The requester attests with
   `--approved-by user` (or `coordinator:<session>`) and the message ref. Expect
   `Approved` and `approvedBy policy/maintenance-card-c`.
4. Expect an executor Pod on talosw01 with RuntimeClass `nvidia`, with
   `NVIDIA_VISIBLE_DEVICES` set to Card C, with `SYS_ADMIN` as its only added
   capability, and with a deny-all NetworkPolicy.
5. Run read-only `nvidia-smi` queries through `agent-run grant maintenance-exec`.
   Expect only Card C to be visible, and its power limit still at 350 W.
6. Release. Expect the stop proof, the Pod and its NetworkPolicy gone, and the grant
   `Released`.
7. **Mode (b)**, after S2. A delegate decides a fresh identical request. Expect
   `approvedBy delegate/<ns>/<sa>`. The delegate deciding its own request is refused,
   and so is a decision with a wrong digest.
   - **Disclosure:** the Claude lead can be that delegate only after P6, which is
     blocked by the template hold. Until then, step 7 uses a coordinator host
     identity, or waits.

Negative checks: a request fails to approve if it names `SYS_RAWIO`, another GPU UUID,
or a control-plane node, or asks for a TTL over 1h.

Evidence:

- the grant's status and annotations, and its Events;
- the executor Pod spec;
- the query output;
- the cleanup proof;
- power readings before and after.

### A2: MAM cookie through the namespace-operator grant (pending)

Preconditions: S5 is released, and P4, P5 and `kube-namespace-operator` are applied.

1. The agent requests a grant:
   - kube `dev-env-grant-namespace-operator`;
   - namespaces `[downloads]`;
   - secrets `[myanonamouse-secret, prowlarr-secret]`;
   - TTL 30m.
2. Approval comes by mode (a) or (b). The broker approves it under
   `kube-namespace-operator`, installs the token, and the agent runs
   `agent-run grant use <grant>`.
3. With the grant context, the agent creates the same one-shot Job as #4501, with no
   GitOps PR. The Job:
   - runs as the default SA;
   - sets `activeDeadlineSeconds` and `ttlSecondsAfterFinished` at 3600 or less;
   - takes `MAM_ID` from `myanonamouse-secret` key `MAM_ID_PROWLARR`;
   - takes `API_KEY` from `prowlarr-secret` key `PROWLARR__AUTH__APIKEY`;
   - runs GET then PUT on `/api/v1/indexer/17` at
     `prowlarr.downloads.svc.cluster.local:9696`, changing only `mamId`;
   - prints only status codes and booleans.

   Exec alone cannot do this step. The Prowlarr pod holds the API key, but no pod
   mounts `MAM_ID_PROWLARR`.
4. Negative checks. Each of these must be refused:
   - a Secret the grant does not name;
   - another namespace;
   - the same Job created by `dev-env-agent`;
   - the same Job created by a C1 identity, which has no `use`.
5. Release. Expect the SA to be gone and the Job to clean itself up.

Run A2 at the next real cookie apply, or as an idempotent re-apply of the stored value.
Do not run the indexer test only for acceptance. MAM rotates `mam_id` on use
(`kubernetes/main/apps/downloads/qbittorrent/app/externalsecret.yaml:3-6`).

Evidence:

- the grant and its Events;
- the Job spec and its status;
- the Job's status-only log;
- the refusals;
- qbittorrent's restart count before and after.

**Correction, 2026-10-31, after 14:15Z.** The first revision was written from reads
between 13:50 and 14:05Z. It called #4501 a held option with a key-name defect. That
was true then, and those timestamps are kept. Since then:

- The key was fixed on the PR branch at `e09c4d1d`. The live Secret holds
  `PROWLARR__AUTH__APIKEY`, not `PROWLARR_API_KEY`.
- #4501 merged as `e6f80c51` at 14:10:25Z.
- The lead reports that the Job logged `indexer enabled=False` and
  `PUT indexer 17: HTTP 202`, meaning the value was stored. This design did not
  re-read the log, because the Job is pruned.
- Cleanup #4502 merged as `01706241` at 14:15:28Z, and Flux pruned all four objects. At
  14:23Z, `downloads` holds no one-shot Job, SA, CNP or script ConfigMap.
- Governor validation waits for its next natural tick.

That was an **emergency GitOps repair, not grant-path parity**. It does not satisfy
A2. The previous revision (`a5e7555`) had marked A2 done; that claim is withdrawn.

## 11. Decisions for Tom

| ID | Question | Recommended answer |
|---|---|---|
| Q-042-01 | Approve the activation bundle in section 0, with the six risks stated there? | Approve. To narrow it, drop a single row. For example, keep hardware (risk 5) on today's haynes-ops-only policies, or start mode (b) with an empty delegate list. |
| Q-042-02 | Let the one `coordinator` profile (P6) through the global template hold? | Yes, once the hold's own exit criteria allow it. **Disclosure:** until it lands, mode (b) cannot serve the Claude lead, which keeps approving through mode (a). |

The lead decides everything else here: the ADR bookkeeping, the namespace split and
the TTLs.

## 12. Non-goals

- No new approval service, page, per-command gate or Asks dependency.
- No second access framework: C2 reuses the kube grant machinery.
- No change to the task budget or native control human gates.
- No OP304 auto-review. It is an optional layer on top of either mode.
- No enforcement of RD1; that belongs to DESIGN-039.
- No live change, grant, policy, template or flag from this document.
