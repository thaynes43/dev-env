# OP192/187 recovery and held recurring activation — 2026-11-02

Source PR [865](https://github.com/thaynes43/dev-env/pull/865) merged as
`ce6edc599fef4fc50a145760247561eadb3bbb00`.
[Publish run 41536642759](https://github.com/thaynes43/dev-env/actions/runs/41536642759)
signed and verified the source-matched operator/helper and portal artifacts.
The codex-login artifact is not selected for adoption.

[haynes-ops#4596](https://github.com/thaynes43/haynes-ops/pull/4596) installed the
additive AgentSession schema and signed agent 2.88. Its source merge and actual
Flux readback are separate evidence; existing user sessions retain their pods.
[haynes-ops#4597](https://github.com/thaynes43/haynes-ops/pull/4597) proposes the
ordinary operator/portal image adoption with collection default off. Its exact
current-head CI, independent review, advisory disposition and installed readback
must be recorded before asserting installation.

The requested recurring outcome is **INACTIVE and unaccepted**. Finite authenticated
provider captures, fixtures, source tests, signed publication and Pod readiness do
not prove recurring collection, agent consumption or UI refresh/staleness.

The one current activation proposal is private packet v2, retained with the
canonical OpenProject OP192/187 handoff. It supersedes v1's historical ESO/vault
proposal. It reuses the public SOPS age recipient and the existing
`flux-system/sops-age` controller reference. A config-only Flux Kustomization in
`flux-system` owns two encrypted, explicitly namespaced metadata-only Secrets in
`dev-env-system` and `dev-agents`. Registering it through the flux-system parent
avoids the dev-env-system parent's namespace transformer. Neither the private key
nor provider credentials are read/copied; applications receive no Secret API rights.

All activation remains held: precreate the fixed report slot through reviewed
GitOps, bind only its actual Kubernetes-assigned UID, encrypt the exact bound
metadata, reconcile config-only delivery, then adopt the reviewed mounts, flags
and minimum rule. The only proposed new persistent rule gives the existing
`dev-env-system/dev-env-operator` ServiceAccount `get/update` on the core ConfigMap
named `dev-env-provider-allowances`. It adds no create/list/watch/patch/delete or
Secret rights. Mounts use normal read-only directories, 0440 and existing fsGroup;
operator/portal explicitly depend on the config Kustomization across namespaces.

Private account/source trust, host binding, original observation clocks and actual
slot UID stay out of public plaintext. No workspace, quota period, reset precision
or per-model Grok meter is inferred. A billing-date advisory is not a quota reset.
The packet's proposed recurrence window ends with the original authorized work
cutoff, 2026-11-03 15:33:23 UTC, and extends no credentials or access.

After precise activation approval, acceptance still requires multiple actual
three-provider cycles with distinct original timestamps and monotonic durable
state, `agent-run usage --read-current -o json` under an actual allowed reader,
UI refresh/stale/error and last-good presentation, and bounded restart/recovery
without duplicate acquisition. Bare `usage -o json` is only the local preflight.
Preserve user Session/Pod identities, report/intent state and credential ownership.
Do not restart or type into the user's Remote Control conversations.

OP355 separately retains the predecessor's exit143/native_protocol_refused.
That subtype includes both parser refusal and missing thread/terminal events;
terminal status alone does not establish the trigger. Recovery custody is a real
new native acknowledgment, never a fabricated outgoing acceptance or a resumed
completed private task. Source hardening must use a bounded follow-up under the
existing story and cannot expand this usage delivery into a new architecture.
