# DESIGN-016: retained initial managed output

Contract published 2026-10-11. Parent #174; ADR-003 controls privacy and
coordination. This is one bounded source PR. DESIGN-013 explicit publication and
its separate payload ACL are reused. Full native history and the durable session
directory remain separate acceptance.

The finite source audit proves two existing parser seams: Claude assistant text
and Codex confirmed-thread agent messages. Claude currently feeds both stdout
and stderr into its event parser, so stderr can resemble a provider event.
Codex separates stderr already. TUI and resumed native history bypass these
parsers. Existing raw events and task results stay private; the log API requires
a live source Pod, while PublishedContext payload reads do not.

## Source boundary

Separate stdout and stderr for every managed provider through the existing
runner. Keep stderr diagnostic and outside assistant-event capture. Reuse the
existing parsers; do not import raw events, prompts, tools, configuration,
provider databases or HOME paths into the operator. Preserve native protocol
refusals and subscription CLI operation. No provider execution occurs in source
validation.

Add a default-off initial managed task capture path, enabled only by an
immutable trusted managed-task binding. Caller input cannot enable it. The first
accepted route is an owned private retained project canary with an original
Session UID, current owned Pod UID and the accepted project inputs. Local-model
capture remains disabled until its parser, exact identity and GPU/household
reservation gates are implemented; no inference or paid API substitute follows.
Native/TUI/resumed output is excluded rather than reconstructed.

Before launching the provider, create a bounded durable private invocation
receipt binding the original Session UID, Pod UID, requested provider and
original launch identity. The operator supplies original Session UID and capture
binding through the typed document or its fixed owned Pod environment; env
values alone are not server authority. An absent or ambiguous invocation receipt
cannot be repaired by relaunching or reusing another attempt. Use the existing
ownership, regular-file, atomic write, fsync and uncertain-intent helpers.

Collect only accepted assistant text from native stdout into at most four UTF-8
chunks, each at most 64 KiB and total at most 256 KiB. Preserve exact text within
those bounds and split at valid UTF-8 boundaries. On overflow, protocol refusal,
missing conversation identity or parsing gaps, preserve a truthful partial or
Unknown local receipt; do not replay the model or label the capture complete.
Private chunk files are owned regular files under a fixed .agentd directory,
with mode 0600 and no caller-controlled paths. Preserve them on uncertain ACKs.

After the initial managed provider exits, while the original Pod remains owned
and nondeleting, publish the bounded chunks through the existing protected
context API. Each fixed key derives from original Pod/invocation identity and
ordinal; retries cannot choose a new key, overwrite content or recapture from
changed inputs. Durable original intent precedes HTTP. Use the existing bounded
HTTPS/projected-token/no-redirect client with no automatic retry. Failure to
confirm publication preserves private intent and uncertainty; it never starts a
new provider task.

## Truthful shared provenance

Extend the strict publication DTO and immutable schema with a small managed
output assertion, including ordinal and publisher-declared conversation ID.
The server derives original Session/Pod/actor, invocation identity, project,
catalog/rules, requested provider/model and timestamp, checks the immutable
trusted capture binding, and rejects unsupported modes/providers or foreign
identity. Accept only ordinals 0–3 and the corresponding fixed key. Coordinator
publication on another source's behalf cannot assert native output.

Use distinct Origin `ManagedOutputAssertion` and Completeness `PartialManagedOutput`
or `Unknown`, preserving existing ExplicitPublished/ExplicitExcerpt semantics.
The conversation ID is a publisher assertion validated by the local parser; the
server does not attest a provider transcript, actual served model, usage, cost or
stop state. These remain Unknown. *Amended 2026-10-12 ([DESIGN-018](018-initial-invocation-usage-provenance.md),
#176): the stored record still says Unknown, but a reader that asks with the
attribution header sees the source Session's retained provider-reported
observation of the same invocation joined at read time. The join is live: once the
source Session is reaped or replaced, the reader sees Unknown again.* Lists still return metadata only; text reads
require the existing independent explicit project payload grant. No HOME/PVC
mount, source Pod exec, control grant or new reader privilege is introduced.

## Affected paths and acceptance

Affect agentd's runner/parsers/settings/protocol/private receipts, the bounded
context publisher, trusted canary binding/controller transport, publication
DTO/store/schema and generated files, plus focused tests and this saga/handoff.
Keep legacy capture disabled and explicit publication behavior unchanged. No
new service, repository lock, lifecycle cleanup, storage migration, enrollment
copy or credential owner is added.

Focused tests must prove fake stderr assistant events cannot be captured, only
confirmed Codex thread agent messages are accepted, UTF-8/byte/chunk limits are
exact, invocation and fixed keys survive uncertain ACKs without reconstruction,
foreign/missing/partial private files are preserved and refused, trusted mode
and original UID fences hold, old explicit context schema/reads remain valid,
and stopped-source authorized reads require no Pod exec. Reuse existing real
cached schema checks; no wide, looped, stress or local image tests. Low
parallelism and resource limits apply to every fixture.

Native independent review and required CI gate squash merge. Claude review is
optional while down; read its outcome/findings without retries or auth repair.
A release and exact GitOps schema/operator/agent promotion follow as needed.
Only the operator may roll during promotion; all retained fixtures, schemas,
claims authority, catalog/templates, native ledger and other Pods must be proved
preserved. The held-draft exception still applies to any v1 pod restart change.

Actual capture and authorized stopped-source reads require a separate bounded
installed pilot, with the provider output and original identities observed.
Source tests and file presence do not accept native history, session directory,
resume/recovery or the multi-provider MVP. Keep #174/#91 open.

Malformed raw provider protocol UTF-8 refuses before JSON parsing can substitute
characters. Mark capture Unknown and preserve any already accepted private
prefix; a complete=true event cannot override this uncertainty. CAPTURE-R1
regression and Common195 integration retain the original failed/uncertain packets.

## A gap freezes the capture — 2026-10-12 (history-02, #174)

Review finding history-02 showed that the Claude path kept accepting assistant
text after a gap. `claude()` and `text()` marked the capture Unknown on a
malformed or non-UTF-8 line, a foreign or missing init, a nil message or a
chunk overflow, then appended the next valid assistant text straight onto the
accepted prefix. The published chunk was text that never existed contiguously.

Rule: the first gap ends capture. Once the capture is Unknown, `claude()` and
`text()` accept nothing more. The retained chunks are exactly the prefix
accepted before the gap, labelled Unknown. The overflow case shares `text()`,
so it holds for Codex too; Codex's parser already stopped on a malformed line.
No retry, replay or relabelling follows. Focused tests in
`internal/agentd/managed_output_test.go` cover the Claude valid/invalid/valid
sequence and the overflow-then-ASCII sequence.
