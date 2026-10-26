# PR553 finite CLOSED bootstrap source acceptance

Source acceptance is complete locally; exact commands and outcomes are recorded below.
This is source readiness only. Independent exact-head review, all findings,
required current CI, merge, release and separately gated activation belong to
the coordinator. This writer performs none of those lifecycle steps.

## Scope and provenance

- Original base: `7e56810a4f4db54f03f903612efdc27e28b2b59a`.
- Verified takeover head: `67a128d201d399841c564a1f656cf31799b99334`.
- Existing draft PR: https://github.com/thaynes43/dev-env/pull/553
- Same remote branch: `agent/portal-closed-bootstrap-178-20261026`.
- Private worktree: `/home/dev/work/dev-env-1026-012531`; local branch
  `agent/dev-env-1026-012531`. Only fast-forward descendants are delivered.
- Assignment configuration: `gpt-6.1-sol` / medium; actual serving model,
  costs and actual session UID are Unknown. No model/provider switch, probe,
  nested worker, prior-task resumption, denied connector action or identity fallback.
- Earlier Terra writer outcomes at the takeover head were incomplete source
  checkpoints. Their recorded compile/two-test results remain historical;
  they did not satisfy this acceptance. Earlier timestamps/toolchain paths
  describe their pods, not this pod.

## Final behavior

1. The action defaults OFF. Enabling it requires an externally configured
   `--portal-admission-bootstrap-permit-uid` and
   `--portal-admission-bootstrap-permit-resource-version`. Missing/malformed
   anchors are refused at flag parsing; the handler also fails closed. The
   immutable configuration pins the approved initial permit snapshot, including
   its bound Session UID/generation, Pod UID, request ID, payload digest, expiry
   and revocation. Permit self-assertions cannot approve a replacement UID or
   rearm an updated active permit.
2. Consumption uses resourceVersion CAS and records the approved original RV,
   outcome and any authoritative target UID/RV. An already CLOSED target retires
   creation authority too. Repeats only reconcile a recorded UID with a fresh
   read. Missing/replaced targets are drift. Unrecorded creation outcomes remain
   consumed/uncertain and require manual disposition; payload equality does not
   prove creation provenance. Present after consumption/AlreadyExists records
   the producer's UID without claiming this action created it.
3. An update failure never leads to CREATE in that request. A CAS loser may
   reconcile durable state only. A CREATE requires a successful consumption CAS,
   final fresh permit and caller checks, and occurs at most once per pinned
   incarnation. If the initial CAS did not commit, storage can still show active:
   the client must not replay an uncertain POST, and the activation owner must
   retire/dispose of uncertain configuration rather than reset or rearm it.
   There is no automatic retry. If a successful CREATE's UID receipt could not
   be persisted, later matching payload is insufficient to reconstruct it.
4. Uncached Session and Pod reads check exact namespace, UID, owner name,
   APIVersion, Kind, UID, ServiceAccount, deletion, remote mode, desired Running
   mode and observed Running/Idle lifecycle with a Running Pod. These are
   lifecycle checks, not proof that a native turn is active. TokenReview keeps
   the operator audience and existing caller classes. Shared SA, human, client,
   portal class or TaskClaim is not permission.
5. The target is only ever CREATED or GET. Only the permit is CAS-updated.
   There is no target PATCH, UPDATE, DELETE or generic object selector. Requests
   require one exact JSON key, valid UTF-8, whole-body 1025-byte sentinel reads
   with a 1024-byte maximum and strict EOF. Query selectors are refused.
6. The CLI sends one POST with no redirect or retry. The target CREATE transport also disables client-go Retry-After retries and
   redirects, with a transport-level one-POST test. Raw CLI HTTP responses are
   bounded before JSON decoding; exact 200/Present or 201/Created receipts must
   contain the matching request ID, valid UID and RV, with no duplicate,
   unknown/case-aliased keys or malformed fields. Refusals/uncertainty expose no
   raw server/transport error. A deterministic post-send read-deadline test
   proves timeout handling without sleeps or a POST replay.

## Validation matrix and receipts

- [final-focused-tests.txt](final-focused-tests.txt): final focused API/CLI/flags plus existing
  TokenReviewer/identity checks, including real disposable admission/RBAC.
- [package-regression.txt](package-regression.txt): complete CLI, operator and
  test-helper package suites, one suite at a time under the shared lock.
- [final-concurrency-race.txt](final-concurrency-race.txt): final deterministic two-replica CAS
  contention with the race detector, not a load or repeated stress test.
- [failed-checks.txt](failed-checks.txt): preserved failed checks and corrections.
- [toolchain.txt](toolchain.txt): official archive verification and pinned assets.

The API matrix checks positive UID/RV receipts, both existing and new witnesses
through deletion/replacement, restart and consumed replay; wrong caller classes,
audience, Session/Pod/owner/mode; expiry/revocation at consumption and afterward;
malformed bodies/anchors and forged/recreated permits; true cross-replica CAS;
consume failure before/after server commit; CREATE timeout before/after commit;
receipt failure and lost committed receipt response; concurrent producers and
AlreadyExists; read failure and unknown-provenance refusal. Assertions include
CREATE counts and preserved target UIDs. Fake faults/channels are deterministic.

The real test starts its own localhost API server using pinned official envtest
assets. It verifies exact executor `system:serviceaccount:dev-env-system:dev-env-operator`,
fixed namespace/name and complete `{schema: '1', state: 'closed'}`. Wrong actor,
name, namespace, OPEN, schema, missing/extra data, binaryData and metadata are
rejected by the actual bound admission policy. A deliberate CEL division error
proves failurePolicy Fail. Real RBAC proves named GET and permit GET/UPDATE,
no broad agent rights, denied target UPDATE/DELETE, and that resourceNames does
not authorize CREATE. Adversarial extra CREATE roles exist only in the disposable
fixture so admission must be the rejecting layer. No policy-token-only test or
skip substitutes for this proof.

Final focused run: PASS, 12.753s wall; final CAS race run: PASS, 14.376s
wall (includes compilation). Full CLI/operator/test-helper packages: PASS,
5.006s wall. `make check-layout` and `git diff --check`: PASS. No check was
skipped to establish this acceptance. The focused command selects no test-helper
tests; its separate full-package receipt supplies that coverage.

## Limits and handoff

No new dependency, production permission, active GitOps configuration, live API
request, bootstrap request, permit, grant, deployment, release, merge or portal
opening occurred. CA/DB/usage/portal-opening and other owner holds remain.
Required hosted CI and independent review are pending with the coordinator;
source-only checks prove no installation or activation. Activation requires the
negative matrix, exact-head source CI/review and externally pinned permit, with
separate reviewed permissions/admission and subsequent disable/revocation.

The exact final commit and complete changed-file list are published in the
PR553 READY comment and draft PR body. Stop source work at that handoff; do not
start typed usage or another story here.
