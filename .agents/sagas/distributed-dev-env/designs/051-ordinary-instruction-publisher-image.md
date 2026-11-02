# DESIGN-051: Ordinary instruction publisher image qualification

Status: source preparation, OP354, 2026-11-02. No runtime enrollment, provider
launch, image adoption or permission is activated by this source change.

The ordinary receiver image path (DESIGN-048) upgrades explicitly enrolled new
receivers without changing the global worker image. Its fresh parent can still
receive an older global image whose control client cannot create an enrolled
receiver or issue instructions. A fresh project-bound parent created through
the existing legitimate ordinary client route needs the same qualified client.
An existing legacy parent without accepted project context cannot be retrofitted.

Add independent `coordinatorInstructionPublisher` request/spec opt-in and
`--coordinator-instruction-publisher`. It is immutable and default false. Only a
trusted ordinary client creating a fresh private catalog task at depth zero or
one, with an accepted project snapshot and dev profile, may select it. It refuses
shared workspaces, canaries, pilots, budget bindings, restored/summoned tasks,
tools, local/remote modes, named identities and receiver enrollment. The operator
uses the same default-off released digest-qualified instruction agent image for
this prospective Pod. Existing Pods and ordinary unselected tasks are unchanged.
No arbitrary caller-selected image or parent/key selector is introduced.
The publisher image must be version 2.87 or newer, whose control client includes
the explicit receiver-create flag absent from 2.86. Format/version validation is
not signature or artifact qualification; exact signed bytes remain a delivery gate.

This field delivers the existing control client only. It does not enroll a native
receiver, set the receiver environment, fabricate a parent UID, create an attempt
or consumption receipt, relax messaging authorization, change depth, alter a
task claim, or grant repository/access authority. The new parent still needs its
actual accepted project/task claim. Its enrolled child is separately admitted
and bound to the actual live parent UID through the existing receiver path.

An identical accepted create remains recoverable by its original idempotency
key after image configuration rollback. New publisher enrollment without a
qualified image fails closed. Prospective Pod construction rechecks ordinary
scope, the accepted selected repository and ownership and refuses pilot overlap.

Validation must cover a trusted client creating a fresh qualified parent, an
ordinary enrolled child bound to that parent's actual UID, unchanged normal task
images and mounts/accounts, receiver environment absent on publisher, wrong caller
and scope refusal, depth enforcement, changed-intent conflict and accepted-create
retry after rollback. Generated CRD tests must reject retroactive opt-in while
allowing an existing object to acquire the false default. Run capped serial suites
and independent exact-source review before required CI and signed publication.

Actual acceptance still needs the legitimate client's own invocation, fresh
native parent and child, correlated model-issued instruction/checkpoint/consume
and durable receipts. A prospective Pod spec or ready Pod is not that proof.
Existing global worker, pilot, credential, grant and user-session holds remain.
