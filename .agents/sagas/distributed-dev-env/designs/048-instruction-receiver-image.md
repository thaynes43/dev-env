# DESIGN-048: Qualified ordinary instruction receiver image

Status: source implementation, 2026-11-02, OP354. Default off. No runtime
activation or native consumption acceptance follows from this document.

A new ordinary private task explicitly enrolled with
`coordinatorInstructions: true` needs an agent release that implements its
native checkpoint/consume contract. The installed legacy worker image is not
qualified by a newer operator. Reusing a pilot image selector would obscure
that boundary and is excluded.

Add the independent operator setting
`--coordinator-instruction-agent-image=ghcr.io/thaynes43/dev-env:2.X.Y@sha256:...`.
It defaults empty. It requires private project task admission and a bounded
release reference; signature verification remains a delivery prerequisite.
The API refuses new true enrollment without that setting. Plain tasks and
false/omitted enrollment retain their existing admission.

Only construction of a new owned ordinary explicitly enrolled receiver may
use this image. The Session needs the authenticated parent UID, accepted
project snapshot selecting the repository, private dev profile and supported
headless provider. Shared, canary, budget, hold and pilot lifecycles refuse it.
An overlapping legacy parent/key image mapping is an error, even for the same
image. Existing Pods are neither patched nor restarted. Removing configuration
cannot downgrade an enrolled pending receiver to the template image; its new
Pod construction refuses instead. No new role, identity, network policy,
credential or permission is added.

The legitimate proof route remains an existing authorized ordinary client
creating a fresh project-bound private parent, which creates one enrolled
child through ordinary admission. Both need matching accepted snapshots;
the child owns its one immutable task claim. The parent UID comes from the
live authenticated creator. A legacy root with no project snapshot cannot
publish, and a depth-two owner cannot create a child. No retroactive binding,
claim transfer, denied dry-run retry or capability-probe worker is introduced.

Before runtime use, record the exact signed receiver digest, operator/schema
pins, legitimate client and parent route, project/repository/task identity,
idempotency keys, bounded task limits, expected child depth, preservation and
stop conditions. Any required activation authority stays separate. Readiness
requires an actual model-issued checkpoint, authenticated designated-parent
instruction and exact message/digest consume receipt. Inbox ACK, fixtures and
PodReady do not substitute for that receipt.
