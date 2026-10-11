# v2.20.0 installation and remaining acceptance — 2026-10-11

Source #199, #200 and #202 and release #201 are merged. Signed v2.20.0 contains
prospective SessionDirectory enrollment, initial-invocation usage provenance,
and the original private executor Pod retention correction. Required CI and
native independent reviews passed. Claude reviews are optional and unavailable;
failed advisory runs produced no verdict, and their error receipts were read.

[haynes-ops #3823](https://github.com/thaynes43/haynes-ops/pull/3823) merged as
`29cd88115722938c15bbaa3baa94964615746090`. The operator runs signed source
`a44b5b1e0b92b4c1a146254e03202617a58b7a01` at digest
`d0463c21efa3a46f5f6ee94623b587aef9f0d9bc6d6632f567d018172dc43447`.
Future templates use signed agent v2.20.0 at digest
`823e015567fdb34951c4e6ce7e39891e3cc8a1e2d1d219bee127fe537cb31303`,
with template revision `2.20.0-ec26880d2a`. The combined AgentSession schema is
`32b2c3e3f40258694e3693a3f5ba3631b71fa729b1708a3cfb6f9e4828155703`;
SessionDirectory schema is
`7afdd4259b6909c302b77e54671a9d07b1580a98351c1b047977d41ea1c12724`.

## Observation correction

The frozen installation observer issued 22 ordinary reads once. Its first 21
reads succeeded. The final direct Kubernetes SessionDirectory list was Forbidden:
this session's ServiceAccount lacks that operator-only authority. Its empty List
stdout is not a successful list and cannot establish zero directory records.
The original refusal, all 44 stdout/stderr artifacts and logical campaign counter
one remain retained. No second list, identity switch or elevated grant is allowed.

Native review also found four exact API defaults on the new admission policy:
matchPolicy Equivalent, empty namespaceSelector and objectSelector, and resource
scope `*`. Each is absent from source and present in the saved response. A new,
separately reviewed offline adapter may bind those four values and compare the
entire remaining spec. Existing policies, bindings, CEL expressions, messages,
reasons and unrelated fields remain exact. This correction does not authorize
normalizing unknown differences or changing the frozen observer.

The separate offline acceptance reads only the saved evidence. It must retain
all original installation and preservation comparisons from the 21 reads and report Directory
cardinality Unknown. It cannot turn this historical observation into a fresh
API authorization decision. Original observer review:
`2135165639de6413c06e16f7931c00db0ec24f2497e155a0edd5da69489e0de0`.
Frozen adapter `fc0f647838d3124725ef6abfde3eca15bf8f89b13e967bc3b7c411ec4fad7570`
and contract `e9c05c3165404b4e344ba519b30051e397ec9d9a0bd04278533a0ab1231bf957`
passed native preparation review
`3fb9d77dc946d515c64252cd88e49e7fda804f8b858f238aebbdfb048bc7f8a7`.
One offline execution passed all 26 checks, with zero new runtime reads; result
`0650bd8db1e868813fe58bdb14c4176bc5258199110c888cc51cfde89becdab7`.
Independent saved-result review passed without findings:
`82416baf500933abcc8276fb1da4514d0966ee8750d9be7824bc133ed12fa62b`.
Acceptance is installation/preservation only, with Directory cardinality Unknown.
The scoped upgrade activity ended after verification.

The observed window was 22:00:58–22:01:03 UTC. Both exact new operator replicas
were Ready; all 11 non-operator Pod UIDs/restarts/specs, four retained Jobs, the
failed native ledger, nine prior schemas, 62 prior authority records, 12 prior
guard objects, 15 network policies and catalog/common ConfigMap identities were
preserved. Only the reviewed Directory schema/grant/guard and signed operator/
future-template delta appeared. All affected Flux revisions matched the merge.

## Next fixed rule acceptance

Global private-project admission remains false. Common rules, project canaries
and directory discovery remain disabled. Existing retention, claims, published
contexts and durable inbox flags remain enabled. Installation creates no Session
or provider attempt and does not restart existing session pods.

A separate bounded GitOps activation must install exactly the two trusted rule
records and project-scoped metadata discovery. Before EACH first fixed canary
creation, perform a fresh authorized project-scoped operator/API directory read.
Record its authorization, completeness, exact original identities and result;
foreign, ambiguous or incomplete evidence refuses creation. Do not bypass the
operator-only Kubernetes boundary or recreate/rearm an existing record.

Permanent IDs are `rule-canary-claude-1011` and `rule-canary-codex-1011`, with one
initial attempt per provider. Claude requests `claude-opus-5-5` / `xhigh`; the
independent Codex driver requests `gpt-6-astra` / `max`. Actual served models remain
Unknown without evidence. Each executor has its own HOME and bounded Pod, and
uses the subscription-backed provider CLI without enrollment copies or paid API
substitution. Native output must demonstrate both loaded common and repository
markers; the prompt contains neither marker. Each new task still performs a
fresh selected-repository fetch and immutable pin. Resume preserves original WIP.

Actual both-provider rules, genuine private repository authorization, prospective
directory enrollment, retained output/usage, claims/messages/stopped context,
whole-task private budgets, native recovery and the concurrent owner pilot remain
unaccepted. Operator rollout with zero managed Sessions proves prerequisite
installation only. The pilot must show actual placement and loaded rules, peers
usable after terminating one disposable session, and upgrade preservation with
live sessions. No memory stress is permitted.

## Owner questions and stopped checks

Q-24 remains pending: separate native pairing/hosts versus a coordinator with
isolated workers. The finite Codex 0.160.1 audit did not establish complete
concurrent native chat routing through one paired computer. Sequential replacement
and a retained computer name do not prove that contract. No fleet UX is selected.
Local-model contracts remain included; inference waits for GPU readiness and
household reservations.

Q-25 needs no owner decision. The failed local schema-host DNS route is stopped
after three attempts. The owner's reply was “I don't understand the question.”
Continue the already-authorized required hosted CI and existing real-API schema
evidence. Do not infer permission for a DNS investigation or retry that route.
