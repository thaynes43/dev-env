# ADR-030: The operator names the Codex host allowed on the workbench account

- **Status:** Proposed. Nothing is built, installed or accepted.
- **Date:** 2026-11-14.
- **Decider:** Proposed by the OP858 design worker for coordinator
  `dev-env-fable-coordinator-1109`. The owner requirements and coordinator rulings
  are distinguished in DESIGN-067 section 1. This worker accepts no access change.
- **Design:** [DESIGN-067](../designs/067-named-codex-workbench-coordinator.md).
- **Tracking:** [OP858](https://work.haynesnetwork.com/work_packages/858), epic #91.
- **Supersedes:** nothing. T30, the named delegate identity rules, ADR-024's
  parent-owned wait and ADR-028's raise contract remain intact.
- **Evidence:** dev-env main `768935da630b2675e55860ece925f0e3b390c880`; haynes-ops
  main `dec3c06241bd6bd74b4d5f9017bd8d804d5596f9`. Source links in DESIGN-067 pin
  those commits. Installed state and provider behavior need their own proof.

## Context

OP858 records Tom's request for a Codex successor coordinator with the existing
workbench authority. It attributes `gpt-6.1-sol`, effort `medium` and title
`Dev-env coordinator 2` to him. It holds installation until he confirms the exact
effects. The coordinator ruled that only an operator-named Codex host may run on
a workbench-account profile, through both API and manifest paths. That ruling is
the coordinator's, not another quotation from Tom.

Today the API lets Tom and trusted clients create a Codex remote host on a valid
profile. A workbench session may create one only with its profile omitted; an
ordinary session may not. The pod builder separately gives a profile's workbench
account to any qualifying stored session, including a raw-manifest Codex remote
host. It does not enforce the API caller rule there. A default or alias profile
can name the same account.
([validate.go:152–170,315–369](https://github.com/thaynes43/dev-env/blob/768935da630b2675e55860ece925f0e3b390c880/internal/apiserver/validate.go#L152-L369),
[pod.go:320–394](https://github.com/thaynes43/dev-env/blob/768935da630b2675e55860ece925f0e3b390c880/internal/controller/pod.go#L320-L394),
[templates.go:600–624](https://github.com/thaynes43/dev-env/blob/768935da630b2675e55860ece925f0e3b390c880/internal/templates/templates.go#L600-L624))

The Fable coordinator is declared once in git at depth 0 and orphaned on app
removal. That is the creation precedent, not authority to bypass a Codex guard.
([haynes-ops coordinator manifest:11–36](https://github.com/thaynes43/haynes-ops/blob/dec3c06241bd6bd74b4d5f9017bd8d804d5596f9/kubernetes/main/apps/dev-env-system/coordinator-session/app/agentsession.yaml#L11-L36),
[Kustomization:3–21](https://github.com/thaynes43/haynes-ops/blob/dec3c06241bd6bd74b4d5f9017bd8d804d5596f9/kubernetes/main/apps/dev-env-system/coordinator-session/ks.yaml#L3-L21))

Native threads act with their host's profile. The existing wrapper starts them
with approvals and sandbox bypassed. The existing workbench account can exec into
privileged pods and reach their mounted credentials. Calling it a coordinator
does not narrow that authority.
([threads.go:118–127](https://github.com/thaynes43/dev-env/blob/768935da630b2675e55860ece925f0e3b390c880/internal/apiserver/threads.go#L118-L127),
[wrapper:68–79](https://github.com/thaynes43/dev-env/blob/768935da630b2675e55860ece925f0e3b390c880/scripts/codex-clean-session.py#L68-L79),
[workbench-operator.yaml:16–37,45–86](https://github.com/thaynes43/haynes-ops/blob/dec3c06241bd6bd74b4d5f9017bd8d804d5596f9/kubernetes/main/apps/dev-env-system/rbac/app/workbench-operator.yaml#L16-L86))

## Proposed decision

1. **One operator-named host.** Add `--codex-workbench-hosts`, empty by default,
   with zero or one exact session name in the session namespace. Reject ambiguous
   or multi-host configuration. The account guard is always active in the new
   build; no independent bypass flag disables it.
2. **Account-based enforcement in both places.** Resolve defaults and aliases.
   Every Codex remote host whose profile uses `dev-env-workbench` must meet the
   shared name, namespace and creation-shape predicate. Reserve that name against
   an ordinary-profile or different-provider shape too. An unreadable template
   refuses. Raw manifests and trusted clients cannot make another admitted host.
3. **Git-only creation at depth 0.** Keep API explicit names unavailable and refuse
   workbench-account Codex host creation through that API, including by a trusted
   client. Declare the allowed host through Flux only after Tom's confirmation,
   using the Fable app's create-once, no-prune and orphan conventions. Require the
   Flux parent, exact depth `"0"` and an ordinary remote-host shape. These are shape
   checks, not cryptographic proof of Flux or owner identity.
4. **No pod on refusal.** The controller never builds an unlisted privileged
   agent pod or silently downgrades it. It exposes a recoverable Pending condition
   with a fixed reason, keeps any HOME and permits an ordinary-account rescue hold
   pod. Apply the guard to every agent-pod construction and resume path. Preserve
   running sessions; stop-before-install/removal is a required procedure, and the
   setting is explicitly not a live revocation mechanism.
5. **Keep T30.** A workbench-created ordinary Codex host still must omit its
   profile. If the resolved default uses the workbench account, refuse it. This
   does not introduce a workbench-session named-host create exception.
6. **Privileged threads have privileged entry points.** On the reserved host,
   require the shared eligibility check and human, trusted-client or workbench
   caller before generic same-profile/ancestry allowances. Add UID fences to
   operator-mediated thread exec. Ordinary sessions cannot start the thread.
   Keep existing exec RBAC; the phone remains the owner's paired-computer route.
   The coordinator launch explicitly selects the reserved host, model
   `gpt-6.1-sol` and effort `medium`. The one-use RC route is not activated here.
7. **Never cross the ordinary pool.** Derive protection from the allowed name and
   resolved workbench account in selection, lease rechecks and later lifecycle
   automation. Keep a GitOps hands-off entry as a second protection. An explicit
   coordinator launch refuses pool hosts and never falls back; an ordinary
   platform thread launch refuses the reserved host.
8. **Prove phone behavior.** The pairing code is delivered privately by an
   authorized exec custodian and entered by Tom. A code-issued marker is not proof
   of pairing. Prove an exact new thread's response and visibility on his phone.
   Set the platform host title at create; native computer/thread title control
   remains Unknown until measured. Do not equate a tmux name with a phone title.
9. **Reuse the existing question routes.** Prove Tom's direct thread reply. Use
   the existing session Ask/MCP and portal board for asynchronous notice and answer
   when agreed, with the coordinator reading the answer on its next supported
   turn. Neither a peer message nor a wake is Tom's answer. Native Codex phone
   prompts remain unverified. No new notification credential or approval channel.
10. **The parent owns its wait.** Preserve ADR-024. Qualify native completion of
    a timer, an addressed child-note wait and a new-file wait into the same idle
    thread. If absent, the smallest missing addition is a supported native harness
    completion hook for its own wait, carrying bounded metadata as tool output.
    No process types input and agentd sends no unsolicited `turn/start`.
    Watch-dependent lanes stay with the outgoing coordinator until all three
    sources are proved. Missing provider support is an explicit blocker, not an
    invitation to patch the installed CLI or substitute a different route.
11. **Custody and ending are explicit.** Keep OP812 custody until lane release and
    checkpoint ACK. Quiesce all remote writers before rescue; generic git rescue
    alone does not prove that. Suspend before removing allowance or downgrading.
    Remove the GitOps app before reap so it cannot recreate an empty computer.
    Preserve required native history separately from git rescue; do not copy
    enrollment or a refresh login to manufacture a replacement.
12. **Separate evidence and approvals.** Future slices have their own tests,
    reviews, pins and install checks. Record source, tested, installed and usable
    acceptance separately. Owner questions in DESIGN-067 section 13 are pending.
    This ADR grants no installation, pairing, lifecycle or credential authority.

## Alternatives considered

| Alternative | Why it loses |
| --- | --- |
| Apply a workbench Codex manifest without a design or guard | Bypasses API policy and leaves the same hole for every other manifest. |
| Exempt all human/client Codex creates | Trusted callers could create unlimited privileged computers. |
| Check only the literal profile name | Misses aliases and a workbench-account default. |
| API-only allow-list | A stored Kubernetes object never visits that API. |
| Controller-only guard with no API refusal | Callers get an apparently accepted create and must discover the refusal later. |
| A per-session annotation saying “coordinator” | A caller-supplied claim is not the operator's setting. |
| Reuse the ordinary host pool | Gives ordinary work a privileged host or sends the coordinator to a host with insufficient authority. |
| Reuse a named delegate account | Those accounts have a different Claude-only contract and agent rights. This asks for the existing workbench authority. |
| Widen depth, mint credentials or add grants | None is needed; thread authority already comes from the existing profile. |
| Type into tmux to wake it | Contradicts ADR-024 and can answer a pending question by accident. |
| Make `schedule` start replacement coordinators | Loses the exact thread and custody; it does not wake its own held wait. |
| Treat a pairing-issued marker as owner acceptance | It cannot confirm the phone paired or displayed this conversation. |
| Drop the flag to revoke access | A live pod keeps its account and token. Explicit suspension must happen first. |

## Consequences

- Good: one named, reviewable GitOps exception supplies the intended workbench
  coordinator without opening a general privileged-host create route.
- Good: unlisted manifests, aliases and trusted-client API creates fail with
  visible reasons, and ordinary host reuse cannot select the coordinator.
- Bad: the phone can drive the whole workbench account. It can reach credentials
  through exec even where direct Secret reads are denied. The allow-list does not
  contain an adversarial superuser, GitOps writer or cluster administrator.
- Bad: one host can contain multiple powerful native threads. Phone starts are
  outside the platform's model/purpose selector and share HOME and quotas.
- Bad: rollback and access removal require a quiet, preserved stop. Neither a
  setting change nor an old controller revokes a running pod.
- Unknown: native phone titles and question prompts, exact idle completion-hook
  support, native thread reopening after resume, deployed preconditions and served
  model evidence. DESIGN-067 sections 9, 11 and 12 state the proof gates.
- Neutral: no CRD or access-grant shape change is required for the host guard.
  A provider hook outside dev-env, if needed, is a separate approval and dependency.
