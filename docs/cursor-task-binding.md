# Disabled Cursor task binding

This source-only follow-up depends on the unpublished, reviewed #989 scaffold.
It has no production caller, registered provider, API admission or dispatch.
It must not be published as an alternate delivery of that stopped PR.

The new wrapper binds one helper attempt and its successful receipt to the
existing platform native-invocation tuple: invocation, launch digest, task name,
Session UID, Pod UID, boot and owner generation. It also binds workspace,
repository, worktree, client, private state root and requested model. It reuses
the platform digest contract instead of accepting identity from helper JSON.
The plan's state root must exactly match the current daemon's `Settings.StateDir`
and pass the existing private managed-state directory checks under its HOME.

This slice supports only an already-admitted coordinated Codex **host** task
with its original immutable owner snapshot. That host identity does not become
Cursor authentication or served-model proof. Ordinary private tasks without this
owner binding are refused; no new platform AgentSession kind is introduced.

The future owning daemon must supply current trusted Session, Pod/boot and
private launch context, including existing writer/lease validation. The wrapper
checks a fresh context before attempting the helper and again before attaching
a completed receipt. A coherent replacement Session, Pod, boot or owner
generation cannot inherit the first binding. Missing or uncertain context fails
closed. The existing consumed-attempt fence prevents a repeat invocation only
while the same private state root and fence survive. Reconstructing a plan under
another root, or losing the fence after restart, is not prevented by this slice;
restart-safe persistence remains unproven. The two context observations are not
an atomic lifecycle lease. Reader freshness and lease correctness remain
external prerequisites.

These are consistency checks, not a new authority or proof that a context
reader has read Kubernetes. There is no production context reader in this
slice, no auth discovery, provider call, filesystem grant, lifecycle change or
runtime installation. The helper's native session ID remains separate from the
platform task. Requested model remains distinct from Unknown served model; no
assistant answer, tool payload or usable task deliverable is fabricated.

Tests use synthetic platform objects, private temporary fixture directories and
in-memory helper peers only. They cover exact matching identity, absent and
mismatched fields, coherent stale incarnations before/after helper completion,
uncertain context and replay. Existing #989 source/test files remain byte-for-byte
unchanged and preserved separately from these three follow-up files.
