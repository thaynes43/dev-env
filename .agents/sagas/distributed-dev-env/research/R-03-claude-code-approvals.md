# R-03: approvals inside the Claude Code app

**Status:** docs-only spike, 2026-10-08; Q-18's premise is corrected under D-70,
with no approval route selected. No approval
adapter, approver session, guard or grant policy is enabled by this document.
Installed Claude Code is `2.1.292`. Documentation and CLI flag inspection establish
available mechanisms; they do not establish a working phone approval boundary.

## Constraints

Q-16 requires approvals inside the Claude Code app and full v1 capability parity.
The access design must preserve today's effective operations. Standing grants
cover preapproved direct OPERATOR and accepted credential scopes; Headlamp access
keeps Tom's existing live directive for its task or access scope. It is not blanket
standing admin access. Secret reads, drains, snapshots and broad
workload operations are already reachable through that identity; a direct grant
would change their access mechanism rather than add a new effective power. No
concrete additional Kubernetes capability is established by those examples, so
the earlier Q-18 question about new powers is withdrawn. D-71 names the concrete
guarded replacement workflow; its approval implementation remains unselected. Existing
owner rules still apply, and no blanket direct admin grant is authorized.
Agents retain haynes-ops self-merge, app maintenance and
Authentik blueprint wiring. No Authentik, Traefik or postgres lockdown, git review
gate or CODEOWNERS gate is introduced. The existing GitOps bypass through
cluster-admin Flux remains an accepted residual risk. An approval authority inside
infrastructure agents effectively administer cannot claim a hard boundary against
those administrators merely by separating its workload.

Q-17 corrected session access: Tom uses `agent-run` or asks agents to start
sessions. A management web UI is another possible client. That changes neither
the approval surface nor the priority order. Plan 02's corrected acceptance passed;
the next work is this spike, the parity audit, then keeper SSH minting.

D-71 clarifies the concrete target: replace owner-directed Headlamp access with a
guarded route preserving accepted tasks, then retire Headlamp after parity and
guardrail acceptance. These approval candidates remain relevant to that workflow;
calling its existing powers parity does not make replacement controls optional.
No candidate is selected, and no authority-isolation claim has been proved.

## What the harness exposes

| Mechanism | Evidence and limit |
|---|---|
| `PreToolUse` | Receives tool input and `tool_use_id`; can request a prompt. `AskUserQuestion` answers can be supplied programmatically through `updatedInput`. |
| `PermissionRequest` | Runs before a permission decision, without a `tool_use_id` or a documented human-origin field. A hook can allow or deny. |
| `PostToolUse` | Receives successful tool input/output and `tool_use_id`. Success is not a documented attestation of a human decision. The exact native `AskUserQuestion` answer shape still needs verification. |
| Failure and denial | `PostToolUseFailure` describes failure. `PermissionDenied` covers auto-mode denial. Neither documents a receipt for a manual phone denial. Missing evidence must never approve. |
| Hook timeout | Command, HTTP and MCP pre-tool hook timeouts continue through normal permissions. A force-ask hook alone is insufficient. |

These are documented harness facts, not a proof of approval provenance.
[Hook reference](https://code.claude.com/docs/en/hooks).

Managed permission rules can require the exact approval tool to prompt; ask rules
precede allow rules, so a remembered allow is insufficient. Managed settings can
restrict permission sources, hooks, bypass and auto modes, and sideloading.
They must be immutable to the approver process and ordinary agents.
[Permissions](https://code.claude.com/docs/en/permissions),
[managed settings](https://code.claude.com/docs/en/managed-settings),
[permission modes](https://code.claude.com/docs/en/permission-modes).

Mods can override ask decisions. `allowManagedHooksOnly` restricts user-installed
mods; the approver must load no unaudited organization mod. Use a fixed managed
MCP server set and a minimal built-in tool set. `allowedTools` preapproves tools;
it is not a tool availability allowlist.
[Managed mods](https://code.claude.com/docs/en/plugins/mods/admin#choose-how-much-to-allow),
[managed MCP](https://code.claude.com/docs/en/managed-mcp#exclusive-control-with-managed-mcpjson),
[CLI reference](https://code.claude.com/docs/en/cli-reference).

The MCP `anthropic/requiresUserInteraction` annotation is stronger than ordinary
permission settings, but the documentation warns that Remote Control may require
answering its full dialog in the terminal. It is not an established phone solution.
MCP form elicitation is another candidate; mobile forwarding is unproven, and an
`accept` response alone does not establish human origin.
[MCP interaction and elicitation](https://code.claude.com/docs/en/mcp#require-approval-for-a-specific-tool).

## Candidates and who can forge them

| Candidate | Authority and forgery | Dependency and conclusion |
|---|---|---|
| **(a) Guarded approver Remote Control session** | A separate control workload has immutable managed policy and only purpose-specific approval tools. The tool's server records an executed decision against a broker challenge. This could remove the requesting agent from the ordinary receipt path, but is an inference, not vendor-attested human provenance. Anyone who controls its process, configuration, receipt credential or transport can forge it. Shared-account OAuth isolation is unresolved. | Needs plan 03's keeper login and Remote Control core, isolation of the complete approval authority, and actual phone/adversarial tests. Recommend a staged investigation; do not enable additional capabilities until it passes without reducing parity. |
| **(b) Ask in the requesting session** | The requester controls its local tools/state and can fabricate hook submissions or any decision credential it holds. Programmatic answers are supported. | A soft gate. Needs a Remote Control session to reach the Claude Code app; a headless task has no native phone question channel. Not an enforcement boundary. |
| **(c) Coordinator relay** | An ordinary coordinator can submit a claimed answer through any credential it owns. Its model text or transcript is not proof. Isolation would turn this into (a). | A soft gate with less infrastructure if an existing Claude Code coordinator relays it. A coordinator running in the ChatGPT app is not the Q-16 approval surface. Enabling additional capabilities through this route requires explicit acceptance of forgery risk. |

## The boundary that option (a) still has to prove

The current broker's `Decide` seam checks a pending grant and records a decision.
It is internal, not an exposed unauthenticated approval endpoint. Its future
adapter must authenticate the approver; an arbitrary `By` string is not identity.
The old round-3 pending view, TTL validation and decision/audit logic may carry
over. Pushover, Authentik headers, CSRF and the web approval page do not.
[Broker decision seam](../../../../internal/broker/seams.go).

Bind a receipt to the grant UID, requester session and pod UIDs, canonical scope
and maximum TTL, broker nonce, decision and approved TTL, approver control-session
and tool event, issue time and expiry. Recheck live state and consume the nonce
atomically with the decision. Reject stale requests, replacements, changed inputs,
replays and missing evidence. Keep bounded audit metadata rather than transcripts.
The current requester record includes a session UID, and new requests are fenced
to it (#111). The complete human receipt, nonce and authority checks above remain
future implementation requirements.
[Grant types](../../../../api/v1alpha1/accessgrant_types.go).

An ordinary `AgentSession` is unsuitable for the authority: current session
message, log and lifecycle routes allow other authenticated callers to reach it.
The approver needs a separate control workload outside those routes. Protect the
verifier and broker decision/status authority too; guarding only a hook is not
enough. Existing admission guards cover v2 session identities, with exceptions;
they do not stop v1's broad pod exec. Network policy does not prevent Kubernetes
API exec. **A guard that removes a v1 operation fails Q-16 and must not ship.**
Whether a complete isolated authority can satisfy both tests remains unresolved.
[Session commands](../../../../internal/apiserver/podcmd.go),
[current guard](https://github.com/thaynes43/haynes-ops/blob/8f3e104d7a6551e6ff05c1ddc76dc358779ee1a3/kubernetes/main/apps/dev-env-system/rbac/app/identity-guard.yaml),
[v1 RBAC](https://github.com/thaynes43/haynes-ops/blob/8f3e104d7a6551e6ff05c1ddc76dc358779ee1a3/kubernetes/main/apps/dev/dev-env/app/rbac.yaml).

Plan 03 distributes access tokens from one Claude account, including session
scopes. R-02 documents OAuth-backed bridge JWT minting. Test whether another token
holder can control the approver bridge or fabricate a permission response. This is
an unresolved isolation requirement, not a demonstrated exploit. Local managed
settings alone do not settle it. Do not claim protection from a malicious
self-merging GitOps writer or administrator controlling the authority.
[R-02](R-02-remote-control-identity.md),
[Remote Control](https://code.claude.com/docs/en/remote-control).

Fresh approval time and fresh account authentication are different. The old
break-glass page used five-minute login freshness. This spike does not silently
replace that with a recent tool event. Any replacement requires a separate explicit
ruling before break-glass is enabled.

## Sequence and acceptance

There is a dependency cycle if plan 07 needs Remote Control while plan 03 waits for
plan 07's old approval console. Separate **plan 03 core** from its management UI:

1. Complete effective v1 parity and keeper SSH minting. Before asking about an
   approval implementation, name the specific new capability or workflow it would
   govern. D-70 withdraws Q-18's OPERATOR-only premise; no route is selected and
   the old prompt does not hold the next owner provisioning step.
2. If Tom chooses (a), build the bounded plan 03 prerequisite: a fresh
   keeper-owned Max login, fenced
   refresh, access-token-only distribution, cold-home account seeding and Remote
   Control registration/resume. Never copy v1's refresh token. The keeper currently
   owns only the GitHub token job; this prerequisite is not built yet.
3. Then run (a)'s isolated phone and adversarial spike. Management UI
   work does not have to precede it. Do not enable human grants on a documentation
   result alone.

Acceptance must prove the complete immutable request appears in the Claude Code
phone prompt, approve commits that exact request, and deny/cancel/disconnect never
approve. Cover saved allows, attempted mode or mod changes, hook failures/timeouts,
reconnects, forged hook JSON, raw tool calls, receipt replay and replaced
grant/session/pod UIDs. Test authority reachability from every existing agent
identity and the shared OAuth bridge case, and prove existing v1 operations still
work. If isolation or parity fails, keep the human path disabled and return to Tom
with the concrete failure. No existing agent session is restarted for these tests.

## Proposed ruling

Pursue (a) in stages, with no new human-gated capability enabled until the phone,
receipt, isolation and parity tests pass. This preserves progress on standing
grants and SSH minting while testing the in-app boundary honestly. Alternatives
are an explicitly trusted coordinator soft gate or deferring the human path.
This remains a design candidate, not a selected route. Q-18's earlier prompt is
withdrawn after the effective-baseline correction. Any future proposal must name
the concrete use case and preserve Q-16: no parity loss, second approval app or
silent change to existing owner rules or login freshness.
