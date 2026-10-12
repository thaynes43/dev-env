# Fixed-rule activation evidence — 2026-10-11 UTC

This record separates the final checker correction, configuration rollout,
authorized Directory read and fixed provider probes. A passing source or
preparation review does not accept the next runtime stage.

## Owner ruling and final correction

Q-26 answered **Allow one correction (Recommended)**. The ruling is retained in
`/home/dev/work/orders/dev-env-fixed-rule-activation-1011/owner-question-q26-ruling-r1.json`
(SHA256 `b8a51fcd3e8f7b824cf518439f94cb5dfe508532de5e9dd89403acb2de0ca101`).
The three failed preparations remain retained. One final R4 is authorized;
failure stops this route, with no fifth candidate, alternate method or retry.

The root-ratified metadata contract (`7907b74a`) binds complete source labels and
annotations to the Deployment and its owned ReplicaSet. It permits only the
explicit generated hash, two topology labels, CNI structure and two verifier
annotations. Unknown keys or values fail. Eight node bindings have source and
saved gateway evidence; an unknown node or `talosw04` fails without another GET.
Generated MAC format is bounded; it proves no network or hardware identity.
Verifier annotations containing `skip` prove no signature enforcement. Signed
publication evidence remains separate.

R4 helper `c874b636` and plan `d76bdf78` are frozen under
`/home/dev/work/orders/dev-env-fixed-rule-activation-1011/observation-preparation-r4/`.
The delta preserves every existing nonmetadata predicate and adds one mandatory
metadata check. Native preparation review `bacc02c8` passed the exact candidate.
It authorizes no runtime action by itself.

## Fresh baseline

Root authority `0a011b64` permitted one baseline execution. It completed with
exit 0, 21 successful GETs and 28 reported checks. Baseline `9e70e779` and
checkpoint `fe3a8918` retain all 42 response hashes and the durable attempt.
Independent saved-response review `adc840f9` accepted the baseline. It checked
13 Pods (two operator and 11 peers), four Jobs, ten schemas, 64 authority
objects, 14 guards, seven fresh policy statuses, 15 networks and four ConfigMaps.
The complete raw-to-snapshot correspondence passed; neither this report nor its
review accepts the post-roll metadata predicate.

Ops #3824 at `6c8e0281` passed native source review and required Diff/Flux CI,
then squash-merged as `00bd6403bc8cb0e2dfb88b7d207482c614068185` at 23:51:14 UTC.
Its change has only six additional operator arguments. The operator remains
pinned to signed `sha-a44b5b1`/`d0463c21`; task image and template remain v2.20.
Global private-project admission remains disabled. No provider starts without
an explicit fixed-ID request.

Deployment and preservation are accepted. Normal Flux settlement is recorded in
`normal-flux-settle-r4.json` (`42d379c0`): the source interval is 30 minutes, so
the single observer cannot run before 2026-10-12 00:29:14 UTC. No polling,
reconcile mutation or extra GET is authorized during that wait. Activity
`act-235107-2570013` covered the operator rollout and ended after verification.
Root authority `a30051b5` permitted the single observer after settlement. It
completed at 2026-10-12 00:31:30 UTC with exit 0, 21 GETs and all 29 predicates
passed. Observation `c6946024` and checkpoint `ba357421` retain all 42 raw hashes.
Independent saved review `d394b19e` passed the complete raw response bindings,
both Ready replacement Pods and their controller/metadata chains, all 11 peers,
four Jobs/ledgers, ten CRDs, 64 authority objects, 14 guards, seven fresh policy
statuses, 15 networks, four ConfigMaps and the exact installed Flux revision
`00bd6403`. Root acceptance is `b74d9a29`. No current dev-env reloader resource
changed. This proves configuration preservation only; it accepts no provider,
Directory payload, rule loading, model cost, history or native recovery.

## Client gates

The merged-source local client and signed v2.21 artifact are separately accepted
in [the client record](clients-2026-10-11.md). Neither new route has been invoked.
The Directory wrapper's inert preparation (`91233c74`, contract `b34e7553`)
passed independent wrapper review `c0daa6cc`. Its executable installation,
caller and plan bindings remain unset; it provides no invocation permission.

The first authorized project page must be complete. A nonempty or ambiguous
page holds creation; a complete empty page is only a read observation. Every
first fixed create requires a fresh page, separately accepted original-session
provenance, native review and one explicit root authorization. Preserve the
permanent IDs and consumed or uncertain attempts; never infer permission to
rearm, replace or retry.

The retained capture audit (`8093f2b6`) identified a source access blocker:
the initiating ordinary client is rejected by the generic context reader's
Session/coordinator identity check. A project payload grant does not override
it. Task success also precedes capture completion. The fixed probes use direct
Pods; there is no Job output route. The live log endpoint requires a Ready source
Pod and provides no retained publication receipt.

DESIGN-022 is the next bounded source slice: one named stored result read bound
to the trusted record, initiating client and exact original Session UID. It must
preserve general ACLs, require immutable original/publication authority and
work without source Pod liveness. It adds no creation, exec, HOME/PVC read,
identity switch or Kubernetes grant. Implementation, signed deployment and
result retrieval readiness must pass separately before either permanent POST.
Its first focused API run exposed another existing source blocker: initial
capture-enabled fixed creation validates before the real Session UID exists.
The failed receipt `17ddd14e` preserves the pre-create refusal. The source slice
also extends the existing admission-only DeepCopy UID preview to this fixed
capture path; the real original remains unchanged and runtime still requires
the assigned UID. Earlier capture tests seeded a retained Session and did not
accept first creation. No permanent runtime ID was consumed by these fake tests.
Its fresh branch is `agent/dev-env-canary-result-1012`; the contract was published
on #173/#174/#91 before implementation.

Actual both-provider rules, private GitHub authorization, full provider history,
claims and inbox consumer behavior, budget and recovery integration, concurrent
isolation and the owner pilot remain unaccepted. Q-24 is pending; no fleet UX is
chosen. Q-25's failed DNS route stays stopped. Claude review is optional while
down. Parent issues remain open.
