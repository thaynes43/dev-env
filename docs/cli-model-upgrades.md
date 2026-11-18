# CLI and model upgrade checklist

Model catalog, requested model, configured model, and served model are different evidence. The existing Codex 0.160.1 native discovery lists `gpt-6.1-terra` with low as native default and low, medium, high, xhigh, max and ultra as supported efforts. A coordinator moving a bounded task from Sol 6.1 to Terra 6.1 can explicitly request `--model gpt-6.1-terra --effort medium`; do not infer a served model from the request or discovery. Empty effort leaves the native default intact. This source admission does not prove account entitlement or a successful model turn.

For each future model promotion, compare the official exact model ID and release guidance with the installed Codex native catalog: record its CLI version, catalog fetch time, supported efforts and default. Do not substitute API-surface defaults for native evidence. Compare those facts with the common `ValidateCodexModel` and `ValidateCodexEffort` checks and the API, agentd and CLI regression tests before promotion; update the existing checks and tests only when needed, and select an explicit supported effort. Discovery or a request does not prove the model served. A real pilot also requires separately authorized, non-disruptive capacity and the corrected operator and agentd images.

For future CLI currency work:

1. Check image source pins and installed artifacts separately. This source candidate pins the agent image to Codex standalone 0.169.1; codex-login stays at 0.160.1 pending its separate keeper prerequisite. The installed worker probe still used 0.160.1. An image tag alone does not prove the version installed in an existing session.
2. Renovate's annotated ARG manager tracks the agent image's Codex and Claude pins and proposes updates for the separately pinned codex-login helper on the overnight America/New_York schedule; tool pin updates do not auto-merge. Review and coordinate both Codex pins explicitly under D-78. The official Codex standalone installer checks the downloaded archive digest; Claude uses an exact npm package pin. Verify the normal published OCI signature and identity.
3. A Codex bump must also update and qualify the exact `managedCodexVersion` guard and its agent-image qualification. Keep codex-login and its CI expectation at their separately approved pin. Test protocol/readback, managed launch, and single remote-control daemon compatibility before adoption; an ARG update alone does not establish compatibility. Do not self-update a running user HOME or change credential ownership.
4. Busy or attached sessions are protected by the current drain checks. Idle, unattached outdated eligible sessions can automatically drain after prepare-restart proof; a global template change is therefore not a new-sessions-only update. Source and simulated tests do not prove a safe live rollout, retained history, or pairing. Plan an explicitly authorized safe handoff and verify those properties before adoption.
5. Managed Codex tasks require both an operator accepting the model and an agentd image accepting it, plus the existing project and profile gates. The current PilotAgent selector covers Claude remote/local and Codex remote, not Codex tasks. An exact isolated managed-task installation route and capacity remain prerequisites for a pilot; no global template change or task pilot is implied by this checklist.

Cursor's inspected local HOME version symlink points to `2026.10.22-27dcf5f`; that is local artifact provenance, not a verified executable version or managed Cursor integration. The inspected agent image and Renovate pins do not track it. An owner must establish its production tracking and supported update route separately.


The [agent-only 0.169.1 source audit](codex-169-upgrade-audit.md) records the
lowest catalog tag containing both 6.2 IDs, the pinned upstream hashes, protocol
compatibility and the coordinator's four auth claims. The independent review
closed the refresh concern: an empty refresh grant carries no keeper credential
and a failed refresh writes nothing. Readiness requires retained-host and layout
qualification in image CI; CI does not grant rollout or installation authority. The helper is
intentionally not bumped; its native-login schema requires a separate reviewed
keeper story. Historical 0.160.1 catalog/probe evidence above is preserved.

For 6.2 admission, pin the agent image containing both the model ids and client
0.169.1 before or with the operator that admits those ids. An older agentd
refuses a Session naming a 6.2 model. Retained 0.160.1 hosts use the bounded
[package replacement](codex-coordinator-hosts.md#single-predecessor-client-upgrade);
other versions and incomplete lifecycle evidence remain refused. The 0.169.1
catalog ranks gpt-6.2-sol first, but platform config/launch paths name their
models explicitly, so no platform default changes.
