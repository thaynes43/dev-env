# H7 metadata audit failed; existing-access route and hold

The compatible guard's [source/hook checkpoint](h7-compatible-guard-source-and-hook-2026-10-13.md)
remains valid. [haynes-ops #3904](https://github.com/thaynes43/haynes-ops/pull/3904)
added the separate metadata reader, reviewed at `f334d64a` and merged as
`85d1dce3` at 21:06:04 UTC. All required checks and advisory passed. Independent
review found and fixed two issues: unverified selector values reaching output,
and contradictory pagination counts being accepted. The final 11 fixture methods
and pinned-image interpreter check passed; the
[independent fix review](https://github.com/thaynes43/haynes-ops/pull/3904#issuecomment-6155192909)
found no remaining source issue. This did not prove actual execution.

## Actual failed attempt

Flux applied the reader foundation at `85d1dce3`; its Kustomization is
Ready/Healthy. The dedicated `dev-env-system/dev-env-h7-metadata-reader`
ServiceAccount, Role/RoleBinding in `dev-agents` and named ClusterRole/Binding
are installed. The Role permits get/list of nine workload kinds; the cluster
grant permits get of six named policy, binding, webhook and namespace objects.
They have no automatic expiry or withdrawal plan. Existing v1 grants were not
edited; the reader is not a trusted archive actor.

The dependent audit automatically created
`dev-env-system/dev-env-h7-metadata-audit-1013a` at 21:06:28 UTC. No explicit
audit reconcile was issued by the coordinator. Its sole collector exited 1 at
21:06:31; the Job became Failed/BackoffLimitExceeded at 21:06:34, with no restart.
The audit Kustomization is unhealthy. Preserve this failed attempt, in addition
to the two older failed Jobs; no retry, replacement or cleanup follows.

The reviewed collector/configuration hashes matched. Its sanitized output was
`Failed / InstalledPolicySourceMismatch` after one GET and 32,392 response
bytes. The named archive policy reported ready but its normalized spec did not
equal committed source. No hooks, lists or protected objects were collected.
**No complete pre-mount baseline or installed acceptance.** The differing fields
were not observed by this attempt; a default must be verified from primary source
before changing normalization.

The actual Pod also received a `k8tz` init container, timezone environment and
mount additions absent from reviewed source. The injected init requests 50m CPU
and has no CPU/memory limits. Thus the reviewed execution confinement was not
established. No repaired Pod will be launched under the hold.

Carry this finding into later credential-free measurement Job contracts: verify
the mutation path and actual init/container/resource inventory, with a reviewed
override or limits where required. Submitted Pod source alone does not establish
that every executing container is bounded. Do not launch a sibling measurement
on the failed confinement premise.

## Q-36 / D-90: existing Mac evidence and deployment hold

At 21:05 UTC, oversight asked Tom whether it could tell this coordinator to hold
the new reader and use existing Mac access for evidence. Its explanation named
the pre-use workload/archive-mount baseline and named guard readiness/webhook
configuration, excluded conversations and said configuration does not prove real
guard behavior. The relay quotes his answer: **"Go for it"**.

The coordinator read the oversight relay's parent instruction and transcript
excerpt. The original parent returned no turns; direct owner-turn and native
prompt delivery were not observed. This constrains the evidence route: hold
further reader deployment/access and use bounded existing authorized Mac reads.
It does not approve the prior grant or any undo. The relay expressly required
reporting already-deployed state without undoing it.

The hold arrived after the merge, foundation apply and failed automatic Job.
No additional reader access, retry or undo is proceeding. The existing Mac route
independently reached the same first-policy source mismatch without an access
denial. Independent review supports its **historical workload baseline**:
nine complete kind lists, 13 objects, zero references to either archive claim,
five exact protected UID/source templates and 11 stable named rereads during
21:15:48–21:16:00 UTC. Reconstructing the verified template enrichment reproduces
the baseline facts digest. Both hook components passed unchanged validation.
The report retains `NotFullAuditAcceptance`.

The transport did not establish the strict request contract: it buffered entire
subprocess responses before byte checks, counted subprocesses rather than actual
requests with client retries disabled, and read version outside its counters.
The saved response sizes were small; that does not prove enforced limits.
Preserve the useful historical observation without promoting transport or full
audit acceptance.

Pinned Kyverno 1.19.1 primary source establishes two required readiness conditions,
`WebhookConfigured` and `RBACPermissionsGranted`; it does not produce the checker's
required `PolicyCached`. Both observed condition sets were True. The saved
message hashes also exactly match its benign generation-disabled message; an
empty-message requirement wrongly rejects that status. Keep
`generationAttested: false` because observedGeneration remains unattested.
See the [status producer](https://github.com/kyverno/kyverno/blob/v1.19.1/pkg/controllers/policystatus/controller.go),
[readiness calculation](https://github.com/kyverno/kyverno/blob/v1.19.1/pkg/controllers/policystatus/vpol.go)
and [message producer](https://github.com/kyverno/kyverno/blob/v1.19.1/pkg/controllers/admissionpolicygenerator/generate-vap.go).

Missing-only item TypeMeta derivation is sound when the typed endpoint and outer
List type are verified and conflicting item types are rejected. Incorporate it
into the source correction without relaxing UID, source or lineage checks.
The 43 equal diagnostic expression token comparisons remain diagnostic: that
regex is not a full CEL lexer. A reviewed correction needs a pinned full lexer
and successful parsing of both expressions, preserving literal/token bytes and
ignoring only whitespace. Retain exact comparisons elsewhere; no schema-default
discrepancy was established.

The exact contract is in local PVC order
`dev-env-h7-archive-guard-1013/existing-mac-evidence-handoff-r1.md`. It specifies
the reviewed collector and expected bundle, nine complete workload-kind lists,
six named cluster GETs, exact UID/source lineage, public selector verification,
stable rereads, response/time/page/call limits and sanitization. Reusing the pure
audit needs a reviewed Mac transport adapter; unmodified main expects in-cluster
credential paths. No credential export or administrator-token borrowing follows.

## Corrected source ready; fresh capture pending

[dev-env #267](https://github.com/thaynes43/dev-env/pull/267) merged at
22:14:26 UTC as `dad78a3958867a4f7260624dc7932d402927a67a`. Its exact reviewed
head is `1142e2c3bbec0a570ed99950163c40a4d406ab9d`. The isolated checker uses
the pinned full CEL lexer/parser, missing-only typed-list derivation and the
supported Kyverno 1.19.1 readiness profile. Its bounded private adapter uses
the unchanged Mac GET transport. The review's Python 3.9 timestamp blocker is
fixed with exact integer nanoseconds and strict calendar validation; redacted
transport refusals retain their code only for the exact pre-dispatch shape.

Required CI, advisory and independent native review passed. Hosted Go suites
passed; Python 3.9.25 and 3.12.15 each passed all 20 methods, including the real
CEL integrations. Artifact `11854095679` contains Go 1.27.1 Mac binaries and
a manifest: all 15 source hashes and three binary hashes/sizes were verified.
These are hosted interpreter observations, not a new Mac runtime observation.
The configured engine profile is not immutable live-binary attestation.

Next: invoke the reviewed adapter once through the existing Mac route and
preserve a new sanitized report and provenance under exclusive filenames.
The exact invocation and hashes are in `existing-mac-capture-handoff-r2.md`.
No fresh capture has run with this correction. Source approval and built tools
do not close the installed configuration/baseline gate. Do not edit or redeploy
the held cluster reader. Withdrawal of
already-installed reader access needs a separate
concrete owner ruling; no such ruling was observed. No workstation commands are
requested from the owner.

Real webhook delivery, lookups, authentic controller transitions, CONNECT and
restart outcomes remain separate. So do PV Retain, F4, both cold measurements,
restore, later baseline freshness and flags. The archive remains unmounted,
v1 stays unchanged under Q-35, and all consumed canaries/sessions/HOMEs and
credentials remain preserved.

## Local receipts

Under `/home/dev/work/orders/dev-env-h7-archive-guard-1013/`; private output is
not copied into this public repository.

| Receipt | SHA-256 |
|---|---|
| `pr3904-merged-r1.json` | `b21f959607df32a42b43204c099ef4b5e26047572740b65fd3b9dfb65c982035` |
| `independent-reader-review-r2.json` | `70a1dd63668117f2d43972a722b5fce2137b1b9571044fe2140314af4777f815` |
| `installed-reader-verification-r1.json` | `3c57e04d0220d24f54c0fedfc4b30ab7c0465b5d6cc0c5ceca5a75caf16a8049` |
| `q36-relayed-direction-r1.json` | `96ea06588570ba276c09956e0799e539202d53cfbb2e584155da7ca48a4d373c` |
| `existing-mac-evidence-handoff-r1.md` | `0b6083fc3e6bece20d02658b122b7a657913da1e30b644033187e2e582f71ea1` |
| `independent-review-pr267-1142e2c3-pr268-ce55f996-r2.json` | `00530e97188f07fa6be117844c8abf53fd6e85566a536cfef18a3d78dbe08d3e` |
| `mac-checker-artifact-verification-1142e2c3-r1.json` | `4979b4aea6f808e08f2948ace432546646193f5b9517aa00c31a61135ce63567` |
| `existing-mac-capture-handoff-r2.md` | `b5efc37614fd9c4f8aee19dd634070313ebb3cfdcb76151225c900dd44642d60` |

The retained collector output is 2,156 bytes, SHA-256
`511addabf27f5aca747d73f1888ff945de91d281213694e585cee889d1294799`.
Its evidence window is 21:06:31.199730–21:06:31.292082 UTC. Separate kind-list
snapshots cannot become a globally atomic baseline; Kyverno readiness without
observedGeneration remains explicitly generation-unattested.

The delivered Mac manifest is local
`/home/dev/work/orders/dev-env-h7-existing-mac-evidence-1013/bundle-manifest-r1.json`,
SHA-256 `b104b63b4eedb293058026b24e039209e86ae149bc42f890df7877629c304f99`.
Its inventory capture spans 21:15:48–21:16:00 UTC, 26 GETs. The unchanged original
audit failure and earlier diagnostics remain preserved separately.

Independent Mac review receipt:
`/home/dev/work/orders/dev-env-h7-existing-mac-evidence-1013/independent-mac-evidence-review-r1.json`,
SHA-256 `3fced52cf8a3bde54f72d8c86374c1ca7c82d70b5d744a892d1da8ee64909651`.
The baseline facts digest is
`34709d7dab0c17fc9c3cdec5d2811750a20fa476cb0e574aaa57cb09dc054737`.
