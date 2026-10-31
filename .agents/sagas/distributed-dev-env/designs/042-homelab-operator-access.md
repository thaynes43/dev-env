# DESIGN-042: Homelab operator access activation bundle

- **Status:** Proposed, 2026-10-31; revised the same day after the lead's and
  coordinator's review. Design only. Nothing is granted, activated or changed by this
  document: no policy, guard, role, flag or cluster object.
- **Tracking:** OpenProject Epic [#112](https://work.haynesnetwork.com/work_packages/112),
  Saga #85. Related: #214 (approved-access bundle), #219 (phone approvals), #233
  (OP233 maintenance, dev-env PR #781), #304 (optional agent auto-review, a separate
  layer), #319 (MAM indexer), #113. The work order named no story, so the question
  uses `Q-042-01` until the lead files one. Lead `dev-env-1029-132039` reviews PR #782.
  Do not merge it in this story.
- **Inputs:** Tom's four corrections of 2026-10-31, relayed by his coordinator
  (section 1). Tom's #214 words of 2026-10-26. The lead's and coordinator's review
  points of 2026-10-31.
- **Source examined:** dev-env `34624a4dd7ecc2a19edb2d76db96c22fd71e2b33` and PR #781
  head `57f4d3c6acf132c2937ac8ae4d4be3bc83fd650d`; haynes-ops
  `09614e825a5ab102cca0a9d18bd6259b91c6e0ec`, plus PRs #4501 and #4502. Live objects
  were read with the session's read-only identity on 2026-10-31 between 13:50 and
  14:23Z.
- **ADR bookkeeping (decided):** ADR-013 (PR #781) already amends ADR-012 for
  attested maintenance. When Tom approves this bundle, a new ADR-014 records the two
  approval modes and the reserved decisions. No Accepted ADR is edited.

`DES/` below means haynes-ops `kubernetes/main/apps/dev-env-system/`. `D001` means
[DESIGN-001](001-dev-env-v2.md).

## 0. Recommended activation scope

Tom approves **one bundle, once** (Q-042-01). Until its flags and policies land, it
changes nothing at runtime. Every item is default-off source or GitOps data with a
one-step rollback.

**The risks Tom accepts by approving it:**

1. Every `full` and `workbench` session, in any repo, may get the class grants of
   section 5 once an approval is recorded. Today only `full` sessions in `haynes-ops`
   can, and only for single hosts.
2. **Mode (a)** is the requester's own attestation. It is a claim, not authentication
   of the approver. A session that lies gets no more than the matching policy allows,
   for its TTL.
3. **Mode (b)**: listed coordinator identities may decide other agents' grants,
   including GPU maintenance, through the authenticated API. A delegate cannot decide
   its own request.
4. **Hardware:** those sessions may lease the shared hw-ssh key for 1h. It is root on
   PiKVM and HaynesTower, and sudo-allowlisted on the PVE nodes. They may also lease a
   Proxmox operator token for 1h, which is root-equivalent on the cluster. Release
   removes the files. It cannot recall a copy.
5. **GPU:** a message may approve SYS_ADMIN maintenance on Card C, within #781's
   exact-envelope policy.
6. **App Secrets:** a GitOps one-shot Job that mounts existing Secrets of its own
   namespace is a routine deploy. Agents may self-merge it, as #4501 was. The Job's
   author can print what it mounts.

The reserved decisions (section 7) stay with Tom.

**Persistent deltas, in activation order:**

| # | Repo | Change | Status | Rollback |
|---|---|---|---|---|
| S1 | dev-env | Attested maintenance (ADR-013): an attested GrantPolicy with an exact maintenance envelope; flag `--enable-attested-maintenance` | PR #781, open; default off | flag false |
| S2 | dev-env | Delegate decisions (6.2): operator and broker flag `--grant-delegates`; `POST /v1/grants/{name}/decision`; GrantPolicy `spec.delegated`; `Decide` accepts `delegate/<ns>/<sa>`; self-decision refused | New, small to medium; default off (empty list) | empty the list |
| S3 | dev-env | `agent-run grant decide` and an MCP tool for delegates; CLI and MCP say when no policy covers a request | New, small | revert |
| R1 | haynes-ops | CRD copy from the release that carries S1 and S2 | routine | remove delegated-only and maintenance policies first, then revert |
| R2 | haynes-ops | Operator and broker pins to that release | routine | pin back |
| P1 | haynes-ops | Nine GrantPolicies (9.1) | **activation** | delete a policy; matching stops and active grants run out |
| P2 | haynes-ops | Broker `--enable-attested-maintenance=true`; operator and broker `--grant-delegates=<list>` | **activation** | false; empty list |
| P3 | haynes-ops | `dev-env-maintenance-annotations`: the operator's set-once decision annotation is also allowed on kube and egress grants, not maintenance only | **activation** | revert; delegates then decide maintenance only |

Order:

1. Merge S1, S2 and S3 and release.
2. Land R1, then R2.
3. Open one held haynes-ops PR with P1, P2 and P3. **That PR is the single approval.**
4. Run A1 (section 10).

S1 and S2 are inert until step 3.

**Already live, no change needed** (deployment args read at 13:50Z):

- broker: `--enable-attested-grants=true` and `--enable-maintenance-grants=true`;
- operator: `--enable-grant-attestation=true` and `--enable-maintenance-api=true`.

The only maintenance switch left is #781's `--enable-attested-maintenance`.

**Already done:** MAM (A2), as a GitOps one-shot, 2026-10-31.

**Not in scope** (optional later): a runtime secret-use grant (5.2), refusing an
approval ref reused across digests, and OP304's agent auto-review.

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
v1's operator reach through v2's existing pieces: scoped task grants, per-grant
identities, TTL, release, audit and D-88 attestation. It adds one authenticated
delegate decision route. Two cases are its acceptance tests, not one-off exceptions:
GPU maintenance with SYS_ADMIN on Card C of talosw01 (A1), and applying the saved
Prowlarr cookie to indexer 17 (A2, done).

## 2. Inventory: what exists today

### 2.1 Approval paths by grant type (dev-env `34624a4`)

| Grant type | Standing policy | Attested policy (D-88) | Other decision | Live policies (13:50Z) |
|---|---|---|---|---|
| kube | yes | yes | none: only `internal/broker/maintenance_decision.go:32` calls `Decide`; the phone page is #219, in progress | `kube-nodes-attested` |
| egress | yes | yes | none (same) | `lan-ceph-mgr-metrics`, `lan-internal-ingress`, `lan-pve-api-lb`, `lan-pve-api-nodes`, `lan-ssh-tier`, `egress-haynesintelligence-ssh-attested`, `egress-pikvm-ssh-attested` |
| credential | yes | yes | refused (`internal/broker/seams.go:151`) | `credential-proxmox`, `credential-hw-ssh-attested` |
| maintenance | refused (`api/v1alpha1/accessgrant_types.go:463`; `internal/broker/match.go:111`) | refused at `34624a4`; PR #781 adds it, default off | only `dev-env-system/dev-env-human` (`internal/apiserver/maintenance.go:36`; `seams.go:143`) | none |
| breakglass | refused (`match.go:79`; CRD `:463`) | refused | no route; the role is not in git | none |

- Roles `dev-env-grant-breakglass` and `dev-env-grant-secrets-read` never match
  (`match.go:120`; CRD `accessgrant_types.go:573`). Profile `ops` never matches
  (`match.go:83`).
- `dev-env-human` is the token Tom mints on his laptop with his admin kubeconfig
  (`D001:340-342`). A maintenance decision therefore needs the Mac. That is the route
  Tom rejects in quote 3.
- A grant that no policy matches waits 30 minutes and is denied by `timeout`
  (`broker.go:65`). Nobody is told.

### 2.2 Human-only API gates (verified)

| Route | Gate | Cite |
|---|---|---|
| `POST /v1/grants/{name}/maintenance-decision` | `kindHuman` only | `internal/apiserver/maintenance.go:36` |
| task budget `extend` | `kindHuman` (a coordinator reaches the handler and is refused) | `taskbudgets.go:207`; `server.go:258` |
| task budget `continue` | `kindHuman` | `taskbudgets.go:347` |
| native control | `kindHuman` | `native_control.go:41` |

Caller classes come from the TokenReview username, never from the body
(`caller.go:20-40`, `auth.go:87`). `kindCoordinator` is a configured coordinator host:
its SA is listed in `--coordinator-hosts`, its token's pod name matches, and the live pod
carries the host annotation (`coordinator.go:67-76`). Live, one host is configured:
`dev-agents/dev-env-owned-native-fixture` (`DES/operator/app/helmrelease.yaml:294`). No
coordinator may decide a grant today. The human maintenance route stays as a fallback.
The task budget and native control gates are out of scope.

### 2.3 Attestation as built (D-88)

- Only the grant's own requesting session may attest (`grant_attestation.go:24`). It is
  a requester self-statement. The broker message says "agent attestation, not verified
  human identity" (`internal/grantattest/attest.go:49`, used by `Message` at `:235`).
- The statement binds the grant UID, the scope digest, the TTL, and the attestor's
  session, session UID and pod UID from the token (`attest.go:81-90`). One statement per
  grant: an identical retry returns 200, a different one 409. It must arrive within the
  30-minute pending window.
- `ScopeDigest` (`attest.go:162`) covers type, credential, role, namespaces, egress
  destinations and ports, TTL, session and session UID. At `34624a4` it omits the
  maintenance envelope. PR #781 extends it to the whole envelope.
- The approver is `user` or `coordinator:<ref>`. The ref is syntax-checked only
  (`attest.go:52-54, 93-102`). No requester/approver separation exists.
- The broker re-verifies the statement against the live session and its running pod
  (`internal/broker/attestation.go:48`). It writes `approvedBy policy/<name>`
  (`broker.go:222`). Credential execution re-checks it (`credentials.go:159`).
- Known gap (`D001:4163`): kube and egress execution never re-read `approvedBy`.

### 2.4 Grant identities, catalog and guards

| Piece | What it does | Cite |
|---|---|---|
| Kube grant identity | The broker creates SA `dev-agents/grant-<id>` with no automount. It binds catalog roles, by RoleBinding per namespace or one ClusterRoleBinding. It mints a token for the time left and installs it in the pod's grants tmpfs by exec. It deletes the SA on release or expiry. | `internal/broker/kube.go:40, 142-176`; `installer.go:54`; `broker.go:482-496` |
| `dev-env-grant-workloads` | Namespaced. Full CRUD on Apps workloads, Jobs, CronJobs, pods, services and configmaps. Read on logs and events. No Secrets, no exec. | `DES/rbac/app/grant-catalog.yaml:24-51` |
| `dev-env-grant-storage` | Namespaced. Create and delete PVCs and VolumeSnapshots; delete StatefulSets. | `grant-catalog.yaml:56-74` |
| `dev-env-grant-secrets-read` | Namespaced. Get and list Secrets. Never policy-approved. | `grant-catalog.yaml:79-88` |
| `dev-env-grant-nodes` | Cluster-wide. Patch nodes, evict pods. | `grant-catalog.yaml:93-114` |
| `dev-env-grant-breakglass` | Named in the broker guard and the CRD. **Not in git and not live.** | `grant-catalog.yaml:15-16`; live NotFound |
| `dev-env-agent-guard` | Matches only `dev-agents/dev-env-agent`. Jobs run as `default` and mount no Secret except `*-volsync-aws-secret`. Workloads: restart only. CronJobs: suspend only. Flux: reconcile and suspend only. ExternalSecrets: force-sync only. | `DES/rbac/app/agent-guard.yaml:59, 67-68, 175-182` |
| `dev-env-identity-guard` | Matches every `dev-agents` SA except the workbench, so every `grant-*` identity. No writes in the dev-env namespaces. Grants write nothing in flux-system, kyverno, external-secrets or kube-system. Pods run as `default` and meet the baseline. **No new Secret reference** except the volsync suffix. | `DES/rbac/app/identity-guard.yaml:50-54, 176, 254` |
| `dev-env-broker-guard` | The broker touches only `grant-*` objects in `dev-agents`. It binds only catalog roles, each to the grant's own SA. | `DES/rbac/app/broker-guard.yaml:52-56, 78-119` |
| `dev-env-maintenance-annotations` | Only the operator sets the decision annotation, once, and only on a maintenance grant. | `DES/rbac/app/maintenance-annotations.yaml:16-35` |
| Maintenance executor | A bare Pod pinned to the node and node UID. It runs as SA `dev-env-maintenance` with no token, drops ALL capabilities and adds only the named ones, is not privileged, and gets a deny-all NetworkPolicy. For `nvidia-utility` it uses RuntimeClass `nvidia` with `NVIDIA_VISIBLE_DEVICES=<GPU UUID>`. Control-plane nodes are refused. | `internal/broker/maintenance_executor.go:27-96`; `maintenance_scope.go:142-151` |
| Egress grants | A CNP `grant-<id>` in `dev-agents` for the session's pods. Destinations are FQDNs, CIDRs, or in-cluster endpoints by namespace and pod labels, with up to 16 ports. No Service names. | `internal/egress/policy.go:37-93` |

The baseline `dev-env-agent` ClusterRole already allows cluster-wide `pods/exec`,
`services/proxy` and `pods/proxy`, Job create and delete, pod delete, and the restart
and reconcile patches (`DES/rbac/app/agent.yaml:78-118`). It has no Secret read.

### 2.5 Live evidence of the drift

| Object (read 13:50 to 13:56Z) | What happened |
|---|---|
| `grant-1031-033915-fc19` (maintenance, Card C, SYS_ADMIN, 10m) | Denied, `deniedBy: timeout`, "no answer in 30 minutes" |
| `grant-1031-132422-84ca` (same scope) | Pending at 13:51Z; Denied by `timeout` when read at 13:55:48Z |
| `grant-1031-033808-7327` (same, without SYS_ADMIN) | Released before an answer |
| `grant-1030-122127-d7d9`, `grant-1030-130136-9897` (kube `dev-env-grant-nodes`, workbench, haynes-ops) | Denied by `timeout`. `kube-nodes-attested` names only profile `full` and repo `haynes-ops`, so a workbench session matched nothing. This is the whack-a-mole Tom describes. |

## 3. v1 baseline against v2 today

v1's role `dev-env-operator` (haynes-ops `kubernetes/main/apps/dev/dev-env/app/rbac.yaml:16-136`)
is the template of v2's `dev-env-agent`. The differences are in admission, mounted
credentials, network and approvals. D-70 also counts v1's effective reach through
Headlamp's `cluster-admin` SA (`D001:3947-3960`).

| # | Capability | v1 | v2 today | Gap |
|---|---|---|---|---|
| — | Kubernetes reads without Secrets; exec, proxy, pod delete, restart, reconcile, CronJob suspend, force-sync | yes | yes, guarded (D-19) | none |
| G1 | A Job that mounts an app Secret or runs as a named SA | yes. No admission policy matches `dev/dev-env` except `dev-env-v1-token-guard`, which covers tokens only (live read). | refused at runtime; possible as a GitOps one-shot | closed for MAM by the GitOps one-shot (A2). The runtime form is optional (5.2). R-04 lists it as P-05. |
| G2 | Runtime workload spec changes | patch, unguarded | restart only | deliberate (D-19, GitOps). Flux reverts drift. |
| G3 | Capability pods on a node (GPU) | only through Headlamp `cluster-admin` (D-70) | maintenance grant, decided only on the Mac | **approval gap**: S1, S2 |
| G4 | Hardware: SSH to 8 hosts, PVE API, Proxmox operator token, Ceph metrics | key and operator token mounted; CNP allows all eight SSH targets, pvedash 443, nodes 8006, Ceph 9283 (`networkpolicy.yaml:441-487`) | workbench: mounted key and standing LAN policies. full: attested hw-ssh lease and SSH to two hosts, haynes-ops only. Proxmox credential: workbench only. | **gap** for full sessions and other repos: P1 |
| G5 | Omni | Admin key mounted | Admin key in `full` and `workbench` (`DES/templates/app/templates.yaml:124-127, 183-186`) | none |
| G6 | In-cluster app APIs from the session | a fixed list, plus internal ingress 8443 | platform tier plus `lan-internal-ingress` | small: P1 |
| G7 | Approval outside a standing policy | not needed | attested policies only for `full` + `haynes-ops`; maintenance only on the Mac; everything else times out | **the main gap**: S2, P1 |

## 4. Prior records: drift or deliberate

| Record | Restriction | Verdict | Action |
|---|---|---|---|
| Q-16 ruling, `D001:5338` | "Standing grants cover parity"; "Human approval gates only capabilities beyond today's" | Governs | This bundle implements it |
| D-70, `D001:3947` | "Do not add a new approval gate to an existing capability" | Governs | Same |
| `D001:348` | "only Tom (on the console) can approve a grant" | Drift; Q-16 and D-88 came later | Superseded by D-88; mode (b) added |
| D-26, D-27 | Tom approves on a page | Already superseded (Q-16, D-70) | None |
| D-54, D-61: schema and match refuse breakglass and secrets-read | Beyond v1's OPERATOR tier | Deliberate | Reserved decisions RD2, RD3 |
| CRD `:463` refuses maintenance policies | Human-only GPU work | Drift (D-70, quote 1) | S1 (#781) |
| ADR-012 | Only the human API identity decides maintenance | Deliberate when written; corrected by quote 3 | Amended by ADR-013 (#781); ADR-014 adds mode (b) |
| D-88 policies scoped to `full` + `haynes-ops`, one host each | Approved one scope at a time | Drift: whack-a-mole | Widen by class (P1) |
| D-19 guards on the baseline identity | Runtime writes go through git | Deliberate | Keep |
| Workbench-only LAN and Proxmox policies (Q-110-08) | Hardware only in the workbench | Deliberate then; drift now | P1 |
| [DESIGN-034](034-agent-asks-and-approvals-board.md) | Only a portal human answers an Ask | Deliberate for Asks | No change; grants never wait on the board |
| [DESIGN-039](039-access-policy-change-guard.md) Q-269-03 | Who approves authority changes | Deliberate, pending | RD1 defers enforcement to it |

## 5. Capability classes

Five classes cover a home lab. Each uses an existing mechanism.

| Class | Mechanism | Runtime identity | The request names | Credentials |
|---|---|---|---|---|
| **C1 App operator** | kube grant, `dev-env-grant-workloads`, RoleBinding per namespace | `dev-agents/grant-<id>`, token in the grants tmpfs, selected with `agent-run grant use` | role, namespaces (up to 16), TTL, reason | none new |
| **C2 Secret-use Job (GitOps one-shot)** | a haynes-ops PR adds a Job, its own SA (no token, no RBAC) and a CNP in the target namespace; Flux applies it; a cleanup PR prunes it | the Job's own SA in the target namespace | in git: namespace, Secrets and keys, image digest, egress | existing app Secrets, used in place |
| **C3 Cluster service API** | egress grant, `endpoints` (namespace + pod labels) and ports | the session's pods, through CNP `grant-<id>` | namespace, labels, ports, TTL | none; app keys come through C2 or the app's own auth |
| **C4 LAN and host** | egress grant (FQDN or CIDR; ports 22, 443, 8006, 9283) and credential grant (`hw-ssh`, `proxmox`) | session pods; the keeper installs the credential in the pod tmpfs | hosts, ports, credential, TTL | existing hw-ssh key; keeper-minted Proxmox token |
| **C5 Node maintenance** | maintenance grant | broker-created executor Pod on the named node; commands go through the operator exec route | node and node UID, GPU UUID, runtime profile, capabilities, image digest, CPU, memory, operation, restore, TTL | none |

| Class | Max TTL | Revocation and cleanup | Audit | Status |
|---|---|---|---|---|
| C1 | 3h, or 8h in the infrastructure namespaces | release or expiry deletes the SA, so every token dies; RoleBindings go with it | AccessGrant kept 90 days (`broker.go:68`), `approvedBy`, attestation or decision annotation, objects changed by `grant-<id>` | role exists; policies new |
| C2 | the Job's `activeDeadlineSeconds`, then the cleanup PR | the cleanup PR removes the Job, SA, CNP and script; Flux prunes them | git history, Flux events, the Job's status-only log | **exists; proven by A2** |
| C3 | 3h | the broker deletes the CNP; the expiry backstop also does | grant, Hubble flows | mechanism exists; policy new |
| C4 | egress 3h, SSH 1h; credentials 1h | CNP deleted; credential removed from tmpfs. Removal does not revoke a copied static key (D-89). | grant, CredentialJob receipt, keeper journal | exists; policies widened |
| C5 | 1h (30m default) | `stopMaintenance` proves the Pod stopped on the node UID, then deletes Pod and NetworkPolicy | grant, attestation or decision, exec log lines, restore evidence | S1 (#781) |

### 5.1 C2 rules

- The Job mounts only existing Secrets of its own namespace. It runs as its own SA, with
  no token and no RBAC. A CNP limits its egress to the one app and DNS.
- It prints status codes and booleans only. The author can print a value, so this is
  a rule, not a control.
- It is a routine deploy that an agent may self-merge, followed by its cleanup PR. A
  Job that would change the bounds (RBAC, guards, policies) is RD1 instead.

### 5.2 Optional runtime form (not in scope)

If GitOps round trips prove too slow, add a namespaced catalog role
`dev-env-grant-secret-use`. It would hold the custom verb `use` on Secrets (no get or
list), plus Job create and delete. One identity-guard test would let a grant identity
reference Secrets when
`authorizer.group('').resource('secrets').namespace(request.namespace).check('use').allowed()`.
That is one authorization check per request, which stays inside the CEL cost budget,
since each check costs 350000 of 1000000. A per-Secret check would refuse any Job with
three or more Secret references. Release does not stop a running Job, so the guard
would cap `activeDeadlineSeconds` and `ttlSecondsAfterFinished` at 3600 for such
identities. This form is not needed for parity.

## 6. Approval: two modes

Both modes approve only inside a GrantPolicy that Tom approved in git. They differ in
who records the decision.

| | **Mode (a): policy delegation to the requester** | **Mode (b): authenticated delegate decision** |
|---|---|---|
| Who records it | the requesting session itself (`agent-run grant attest`) | a different, listed coordinator identity (`agent-run grant decide`) |
| What it proves | the requester's own claim that `user` or `coordinator:<ref>` approved, citing a message ref. It does **not** authenticate the claimed approver. | that an authenticated coordinator SA in `--grant-delegates` decided this exact request |
| Binding | grant UID, session UID, pod UID, scope digest, TTL; one statement per grant; 30-minute window | grant UID, scope digest, TTL no longer than requested; set-once decision annotation; 30-minute window |
| Separation | none: requester and attestor are the same session | the delegate may not be the requester, the requester's pod or session, or a maintenance coordinator or worker of the grant |
| Policy block | `spec.attested` (exists) | `spec.delegated` (new, S2) |
| Recorded as | `approvedBy policy/<name>`, with "agent attestation, not verified human identity" | `approvedBy delegate/<ns>/<sa>`, with the host ID, the ref, and "not a human decision" |
| Grant types | kube, egress, credential; maintenance with S1 | kube, egress, maintenance. Credential is not built: keeper execution re-checks a `policy/` approver (`credentials.go:71`). |

Neither mode is verified human authority. An attested `user` claim is never described
as one.

### 6.1 Flow

1. The requester creates the grant without waiting (`agent-run grant request --no-wait`,
   MCP `request_access`, or `agent-run grant maintenance-request`). It reads the name,
   UID, digest and TTL with `agent-run grant show`.
2. It sends one message with those values, the reason and the risk:
   - A Remote Control session asks Tom through AskUserQuestion on his phone, or asks
     its coordinator.
   - A headless task messages its coordinator, with `agent-run msg` or the durable
     inbox.
3. The approver answers. Silence is deny.
   - **Mode (a):** the approver replies to the requester. The requester runs
     `agent-run grant attest <grant> --approved-by user|coordinator:<session> --ref <message id>`.
   - **Mode (b):** a delegate runs `agent-run grant decide <grant> --grant-uid <uid> --digest <digest> --approve [--ref <message id>]`.
     When the delegate relays Tom's answer, the ref names Tom's message.
4. The broker approves only if a matching policy carries the right block, the mode's
   checks pass, and the grant is still Pending. Otherwise the grant times out.

A grant under an attested or delegated policy is never approved without a recorded
statement or decision that cites its ref. Standing policies such as `lan-*` and
`credential-proxmox` approve without one, as today.

### 6.2 Delegate decisions (S2)

- **Allowlist:** `--grant-delegates=<ns>/<sa>,...` on the operator and the broker. The
  two lists must match, as the human SA pair already does. Each entry must also be a
  configured coordinator host, so it authenticates as `kindCoordinator`, bound to its
  pod.
- **Route:** `POST /v1/grants/{name}/decision` takes grant UID, digest, approve or deny,
  TTL and ref. The digest is `ScopeDigest`, which covers the maintenance envelope after
  #781. The route writes the existing set-once decision annotation (P3 allows it on
  kube and egress).
- **Refused:**
  - a caller not on the list;
  - the requester itself: the same pod UID, or a pod controlled by the requester's
    session;
  - a maintenance coordinator or worker named in the envelope;
  - a grant that is not Pending, is past 30 minutes, or was already decided;
  - a digest mismatch;
  - credential or breakglass grants, and the roles `dev-env-grant-secrets-read`,
    `dev-env-grant-breakglass` and `dev-env-grant-storage`.
- **Broker:**
  1. Checks the annotation's binding.
  2. Checks that a policy with `spec.delegated` covers the scope and the requester. For
     maintenance this is #781's exact-envelope match.
  3. Calls `Decide` with `delegate/<ns>/<sa>`.
  4. Writes no `human/` string.
- **Schema:** a policy may carry `attested`, `delegated`, or both. Its top-level
  `profiles`, `repos` and `agents` stay empty. The requester rule at
  `accessgrant_types.go:467` accepts either block. An older broker ignores `delegated`,
  so a delegated-only policy matches nothing. An older CRD prunes the field, and the
  rule then refuses the policy, so a Flux apply fails loudly. Remove delegated-only
  policies before reverting the CRD.
- **Known limit:** only configured coordinator hosts authenticate as coordinators. Live,
  that is one Codex fixture identity. A Claude Remote Control lead runs as a session
  under the shared session SA, so it cannot be a delegate until the operator can mark a
  named session as a coordinator host. That mechanism is **Unknown** and is a
  follow-up. Until then, a Claude lead approves through mode (a).

### 6.3 Escalation

A request that no policy covers matches nothing. Today it times out silently. S3 makes
the CLI and MCP say so before the agent waits. The agent then asks its coordinator, or
Tom's phone if the coordinator cannot approve. If Tom wants that scope from now on, an
agent opens a policy PR, which is reserved decision RD1. No one-off route exists outside
the policies.

## 7. Reserved decisions and external constraints

A reserved decision is decision authority that stays with Tom. It never means a human
must execute the task or be the runtime actor. Tom decides through any channel: a
message to an agent, a phone answer, or a PR approval. Agents then record the decision
and carry out the approved task.

| # | Reserved decision | Why | Enforced by |
|---|---|---|---|
| RD1 | Changing the bounds: GrantPolicies, catalog roles, guards, the delegate allowlist, broker and operator RBAC and flags | An agent that widens its own bounds makes them meaningless | Convention today: agents can self-merge haynes-ops (Q-16 residual risk). DESIGN-039 proposes enforcement; Q-269-03 is pending. |
| RD2 | Break-glass | Removes every other boundary at once | `match.go:79`, `seams.go:139`, CRD `:463`; the role does not exist |
| RD3 | Secret read through the API (`dev-env-grant-secrets-read`, any namespace) | Lists and returns every value at once, with no record of which were used | `match.go:120`, CRD `:573`. C2 is the delegable form. |
| RD4 | Destructive storage or data operations: `dev-env-grant-storage`, deleting a database, Ceph pool, volume or backup, disk wipes, firmware writes | A rollback does not undo them | No policy names `dev-env-grant-storage` or the storage and data namespaces (9.2). Hardware operations follow a convention: state that the operation is non-destructive, or wait for Tom. |

Residual risk under RD4: `kube-nodes-attested` (cluster-wide node patch and eviction)
already reaches storage pods and nodes, and a NoExecute taint bypasses PDBs. The bundle
only widens who may request it. The baseline's narrow `database` PVC delete for a CNPG
re-clone stays.

**External constraints.** These are not platform policy, and no grant can lift them:

- Provider logins, including the Claude Max and Codex OAuth ceremony. The provider wants
  the account owner's browser consent. One refresher per token family; a second
  refresher revoked the family on 2026-08-29. The keeper owns refresh.
- Third-party site confirmations, such as creating a new MAM session on its site, email
  or 2FA prompts, and captchas.
- GitHub admin settings: org and repo admin, rulesets, App installation, repo secrets.
  The bot has no admin rights.
- Creating or rotating password-manager items. Agents read them only through
  ExternalSecrets.

Refused by code for everyone: maintenance on control-plane nodes
(`maintenance_scope.go:142-151`), grants in the dev-env namespaces, and profile `ops`.

## 8. Issuance rules

- **Who requests:** a session, for its own pod (`grants.go:45`). It must be a `full` or
  `workbench` session, in any repo. Children get no wider profile than their parent.
  `ops` never matches.
- **What a request names:** the class's scope fields (section 5), an exact TTL, and a
  reason that says what changes and how to restore it.
- **Never inferred:**
  - an approval from silence, a relay, a peer's claim, an approval of a different
    digest, or an old approval for a new TTL;
  - a namespace from an app name;
  - a Secret name;
  - a GPU from a node;
  - a delegate's identity from message text.

  A renewal is a new grant and needs a new approval.

| Class | Mode (a) or (b) | Mode (b) only | Concurrency per session (a rule; the broker enforces none) |
|---|---|---|---|
| C1 | 3h, app namespaces | 8h, infrastructure namespaces | 2 active |
| C2 | GitOps; no grant | — | one Job per PR, then its cleanup PR |
| C3 | 3h, app namespaces | — | 2 active |
| C4 egress | 3h, SSH 1h | — | — |
| C4 credential | 1h, mode (a) only | — | 1 per credential |
| C5 | 1h, #781 exact envelope | — | 1 per GPU |

## 9. Policy and namespace detail

### 9.1 The nine GrantPolicies (P1)

Requesters are `profiles: [full, workbench]` with no repo restriction. They go under
`attested`, `delegated`, or both, as listed.

| Policy | Type | Blocks | Scope | maxTTL |
|---|---|---|---|---|
| `kube-workloads` | kube | attested, delegated | `dev-env-grant-workloads`; app namespaces | 3h |
| `kube-workloads-infra` | kube | delegated | same role; infrastructure namespaces | 8h |
| `egress-cluster-services` | egress | attested, delegated | endpoints by app namespace; the app Service ports, up to 16. The list is Unknown until the PR enumerates it. Whether an empty `matchLabels` admits any pod is Unknown and is tested first. | 3h |
| `kube-nodes-attested` (widen) | kube | attested, delegated | `dev-env-grant-nodes` | 3h |
| `lan-ssh` | egress | attested, delegated | the eight `lan-ssh-tier` FQDNs and the PiKVM CIDR of `egress-pikvm-ssh-attested`, port 22 | 1h |
| `lan-pve-api` | egress | attested, delegated | `pvedash.haynesnetwork` and the five node FQDNs, ports 443 and 8006 | 3h |
| `credential-hw-ssh-attested` (widen) | credential | attested | `hw-ssh` | 1h |
| `credential-proxmox-attested` | credential | attested | `proxmox` | 1h |
| `maintenance-card-c` | maintenance | attested, delegated | #781 exact envelope: talosw01 and its node UID, Card C's GPU UUID, `nvidia-utility`, the pinned comfyui image digest, capabilities within `[SYS_ADMIN]`, CPU and memory caps | 1h |

`maintenance-card-c` is one GPU and one image, because #781 matches envelopes exactly.
A second GPU needs its own policy (RD1). That keeps maintenance precise, at the cost of
one policy per target.

### 9.2 Namespaces (33 live)

- **App** (C1 and C3): `ai`, `downloads`, `frontend`, `home-automation`, `media`,
  `observability`, `office`, `photos`.
- **Infrastructure** (C1, mode (b) only): `arc-runners`, `arc-systems`, `cert-manager`,
  `k8tz`, `network`, `upgrade-agent`.
- **Storage and data** (RD4, no policy): `ceph-csi`, `database`, `openebs-system`,
  `rook-ceph`, `storage-util`, `volsync-system`.
- **Never:** `dev-env-system`, `dev-agents`, `dev-tools`, `dev-env-budgets`, `dev` (the
  v1 pod), `default`, `flux-system`, `kyverno`, `external-secrets`, `kube-system`,
  `kube-public`, `kube-node-lease`, `cilium-secrets`.

## 10. Acceptance

### A1: GPU maintenance on Card C (pending)

Preconditions:

- S1 released and pinned.
- `maintenance-card-c` and `--enable-attested-maintenance=true` applied.
- The ADR-012 admission policies present. The annotation guard is live. The Kyverno
  executor policy's live state is Unknown, because this identity cannot read it.

Steps:

1. A full session (today `haynes-ops-1029-223503`) runs
   `agent-run grant maintenance-request --task-json <task> --reason <r> --ttl 10m`.
   The task carries:
   - node `talosw01`, node UID `b7ef4b0a-524b-411b-9b47-79e3c37e7269`;
   - resource `GPU-d8a856f1-f955-f683-bc24-654561496774`;
   - capabilities `[SYS_ADMIN]`, profile `nvidia-utility`;
   - image `ghcr.io/thaynes43/comfyui:0.39.0-2@sha256:991867f5…`;
   - 100m CPU and 128Mi memory;
   - the read-only operation and restore text of `grant-1031-132422-84ca`.
2. It sends Tom or its coordinator the grant name, UID, digest, TTL, operation and
   restore.
3. **Mode (a):** the approver replies approve, and the requester attests with
   `--approved-by user` (or `coordinator:<session>`) and the message ref. Expect
   `Approved` and `approvedBy policy/maintenance-card-c`.
4. Expect an executor Pod on talosw01 with:
   - RuntimeClass `nvidia`;
   - `NVIDIA_VISIBLE_DEVICES` set to Card C;
   - `SYS_ADMIN` as the only added capability;
   - a deny-all NetworkPolicy.
5. Through `agent-run grant maintenance-exec`, run read-only `nvidia-smi` queries.
   Expect only Card C visible, with the power limit still at 350 W.
6. Release. Expect the stop proof, the Pod and NetworkPolicy gone, and the grant
   `Released`.
7. **Mode (b)**, after S2:
   - A listed delegate decides a fresh identical request. Expect
     `approvedBy delegate/<ns>/<sa>`.
   - The delegate deciding its own request is refused.
   - A decision with a wrong digest is refused.

Negative checks: `SYS_RAWIO`, another GPU UUID, a control-plane node, or a TTL over 1h
each fails to approve.

Evidence:

- the grant's status and annotations;
- the broker log lines;
- the executor Pod spec;
- the query output;
- the cleanup proof;
- power readings before and after.

### A2: Apply the saved MAM cookie (done, GitOps one-shot)

**Correction, 2026-10-31, after 14:15Z.** The first revision of this design was written
from reads between 13:50 and 14:05Z. It called #4501 a held option with a key-name
defect. That was true then, and these timestamps are kept. Since then:

- The key was fixed on the PR branch at `e09c4d1d`. The Job had read
  `PROWLARR_API_KEY`; the live Secret holds `PROWLARR__AUTH__APIKEY`.
- #4501 merged as `e6f80c51` (14:10:25Z).
- The Job completed. Its log said `indexer enabled=False` and
  `PUT indexer 17: HTTP 202`, so the value is stored. These lines are as the lead
  reported them. The Job is pruned, and this design did not re-read the log.
- Cleanup #4502 merged as `01706241` (14:15:28Z). Flux pruned all four objects. At
  14:23Z, `downloads` holds no one-shot Job, SA, CNP or script ConfigMap.
- Governor validation waits for its next natural tick.

So MAM is no longer an option in flight. It is the first completed acceptance case of
C2 (GitOps one-shot), and it needed no platform change. The apply step did not change
the Secret, so qbittorrent did not restart. Saving a new cookie into the password
manager does change the Secret, and Reloader then restarts the seedbox pod. That is the
shared Secret's consequence.

Evidence: haynes-ops #4501 and #4502, their merge commits, the lead's record of the Job
log, and the live absence of the four objects.

## 11. Decision for Tom

| ID | Question | Recommended answer |
|---|---|---|
| Q-042-01 | Approve the activation bundle in section 0, with the six risks stated there? | Approve. To narrow it, drop a single row: for example, keep hardware (risk 4) on today's haynes-ops-only policies, or start mode (b) with an empty delegate list. |

Every other choice in this design is made by the lead and recorded here. That covers
ADR bookkeeping, the namespace split, TTLs and the #4501 follow-through.

## 12. Non-goals

- No new approval service, page, per-command gate or Asks dependency.
- No change to the task budget or native control human gates.
- No OP304 auto-review. It is an optional layer on top of either mode.
- No enforcement of RD1; that belongs to DESIGN-039.
- No live change, grant, policy or flag from this document.
