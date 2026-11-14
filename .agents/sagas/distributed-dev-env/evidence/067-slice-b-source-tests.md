# DESIGN-067 slice (b): source test evidence

OP858 recovery worker `dev-env-1114-160847`, 2026-11-14. Source base
`dc824832d8cfd1a96fc5dc5c65622ad5ebe2ec37`, with the required ancestors
`308e442ab70f12424a1c73fac9bdec15f9260275` (PR1156) and
`29575187b733f5b2fa27fd3349787013edc0f621` (PR1145). Duplicate-work checks used
OP858 history, all-state PR search and running session metadata before edits.
No duplicate slice-(b) owner or PR was found. The worker's branch is
`agent/dev-env-1114-160847`; the deliverable is draft PR #1165.

This records source and local tests only. Another-family review is unavailable
and pending. No host, flag, live policy, account, credentials, deployment,
provider reset, managed worker dispatch or Studio resource was changed. No
advisory review was manually invoked. Existing owners retain all custody.
Meatball must obtain any missing ruling; silence is not consent.

## Acceptance coverage

| Case | Test |
| --- | --- |
| Human/client; workbench by creation with actual workbench pod; target and caller defaults/aliases | `TestCoordinatorThreadEntry` |
| Ordinary same-profile/ancestor spoof, authenticated named account, signed active raise despite workbench pod/profile label; missing creation profile and unreadable templates | `TestCoordinatorThreadEntry` |
| Wrong target account, unlisted host, malformed target, ordinary intent | `TestCoordinatorThreadEntry` |
| Explicit coordinator intent to an ordinary host; missing host; no pool or intent fallback | `TestCoordinatorIntentNeverUsesPoolHost`, `TestCoordinatorLaunchRequiresExplicitHostAndModel`, `TestCoordinatorLaunchUsesAuthorizedAPINoFallback` |
| Exact coordinator model/effort at CLI and API | `TestCoordinatorLaunchRejectsDifferentModelOrEffort`, `TestCoordinatorAPIRejectsDifferentModelOrEffort` |
| Session/Pod replacement, account/profile drift and concurrent suspension before exec | `TestCoordinatorThreadTargetRace` |
| One request template snapshot for target and caller | `TestCoordinatorGatePinsTemplatesOnce` |
| One-use route refusal before reservation/exec, and host-side refusal | `TestCoordinatorOneUseRCRefusesBeforeReservation`, `TestCodexCoordinatorOneUseHostRefuses` |
| Metadata host/Session binding, expected target UIDs, explicit purpose, workbench marker and fixed model/effort before native access | `TestCodexCoordinatorEntryMetadataAndUIDs`, `TestCtlCoordinatorThreadMetadataAndTargetFences` |
| Controller-owned exact host/UID metadata; ordinary and hold pods carry no explicit metadata; nil builder admission and missing Session UID refuse | `TestCodexCoordinatorPodMetadata` |
| Pure pool selection, named-host bypass, suitability and capacity reject reserved/workbench/unknown-account hosts; only reserved host present | `TestCoordinatorPoolExclusions` |
| Requested workbench/default/unknown profile cannot form an ordinary pool cell | `TestCoordinatorPoolExcludedRequest`, `TestCoordinatorPoolRequestedAccount` |
| Fresh facts and leases reject reserved names, workbench aliases/defaults/actual pod accounts and unknown profiles without annotation/hands-off configuration | `TestCoordinatorPoolFactsAndLease` |
| Protection races between usable list snapshot and fresh lease check | `TestCoordinatorLeaseRejectsProtectionRace` |
| Exact new-ID binding before title; ambiguity refuses; lost ACK stays unconfirmed with one launch | `TestCodexCoordinatorTitleAfterExactBinding` |
| Exact thread/cwd reads; exact native title setter, mutation ACK and name readback; no resume/turn/shell rename | `TestCodexCoordinatorNativeTitleRPC` |
| Invalid title binding cannot connect; offline 0.160.1 setter schema and native name field | `TestCodexCoordinatorTitleInvalidBindingDoesNotConnect`, `TestCodexCoordinatorTitlePinnedSchema` |
| L3 removed-profile ordinary running host matches the complete ordinary path status/result (only wall-clock condition timestamps normalized), pod/HOME preserved | `TestOrdinaryRunningRemovedProfileMatchesMainPath` |
| Existing templates-outage behavior, all construction/shape guards, explicit rescue holds, T30 and ordinary pod byte equality | Existing full controller/API suites, including `TestOrdinaryRunningSessionTemplatesErrorMatchesMain`, `TestCodexWorkbenchT30PrecedesTemplateRead`, `TestPodsWithoutStartDeadlineAreUnchanged` |

L1 changed the internal builder admission arguments to required parameters;
ordinary test callers supply explicit resolution. L2 documents the existing
`templates.Validate` account restriction; no policy was broadened. L3 applies
only to observation of an owned ordinary-account pod when resolution is
unavailable, including a removed profile. It never admits missing-pod
construction or a new thread with an unresolved account.

## Test boundaries

Tests use fakes or a local envtest API server. Every test process was launched
with `KUBECONFIG` and all `KUBERNETES_*` variables removed, `GOMAXPROCS=2`,
`nice -n 19`, and `go test -p 2`, one suite at a time. Tests that spawn a child
inherit that stripped environment; the new launch tests use synthetic env maps
and fake native daemons. `TMPDIR` remains unchanged; `GOTMPDIR` points to a
bounded local scratch directory. Disk was checked before builds; 2 GiB is the
stop reserve. Local envtest binaries were obtained through `make envtest`.
No provider turn, live cluster API, remote host or installed state was tested.
The offline schema generator starts no client, daemon or model.

The full API, controller and agentd suites passed. Full CLI, ctl, pool, apiv1,
protocol, shared policy and thread-launch suites passed. The controller run was
unfiltered, including syntax guards and ordinary pod golden comparisons. The
PR/handoff supplies the exact final head and CI result separately.

## Mutation experiments

A bounded sequential pass changes one guard at a time, runs its focused test
with `-count=1`, and restores the original source bytes in a `finally` block.
Only a named test failure counts as a kill; build errors do not. These are
local experiments, not a stress loop or live proof. No mutant is pushed.

**58 killed; 0 surviving or invalid mutants. All original source bytes restored and hash-checked.**

| # | Guard broken | Test that failed | Source |
| --- | --- | --- | --- |
| 1 | CLI explicit coordinator host/name/model/effort | `TestCoordinatorLaunchRequiresExplicitHostAndModel/--host` | [thread.go](../../../../internal/agentrun/thread.go) |
| 2 | CLI coordinator stays on the authorized API route | `TestCoordinatorLaunchUsesAuthorizedAPINoFallback/confirmed` | [thread.go](../../../../internal/agentrun/thread.go) |
| 3 | API shared host shape admission | `TestCoordinatorThreadEntry/target-shape-drift` | [thread_coordinator.go](../../../../internal/apiserver/thread_coordinator.go) |
| 4 | API ordinary intent cannot enter reserved host | `TestCoordinatorThreadEntry/ordinary-intent` | [thread_coordinator.go](../../../../internal/apiserver/thread_coordinator.go) |
| 5 | API coordinator intent cannot enter ordinary host | `TestCoordinatorIntentNeverUsesPoolHost` | [thread_coordinator.go](../../../../internal/apiserver/thread_coordinator.go) |
| 6 | API target actual workbench pod account | `TestCoordinatorThreadEntry/target-account-drift` | [thread_coordinator.go](../../../../internal/apiserver/thread_coordinator.go) |
| 7 | API caller creation profile (active raise and patched label) | `TestCoordinatorThreadEntry/raised` | [thread_coordinator.go](../../../../internal/apiserver/thread_coordinator.go) |
| 8 | API caller actual pod account | `TestCoordinatorThreadEntry/wrong-pod-account` | [thread_coordinator.go](../../../../internal/apiserver/thread_coordinator.go) |
| 9 | API privileged caller allow-list | `TestCoordinatorThreadEntry/ordinary-same-profile` | [thread_coordinator.go](../../../../internal/apiserver/thread_coordinator.go) |
| 10 | API final live target recheck | `TestCoordinatorThreadTargetRace/account` | [threads.go](../../../../internal/apiserver/threads.go) |
| 11 | API target UID comparison | `TestCoordinatorThreadTargetRace/pod-uid` | [threads.go](../../../../internal/apiserver/threads.go) |
| 12 | API rejects concurrent suspension | `TestCoordinatorThreadTargetRace/suspended` | [threads.go](../../../../internal/apiserver/threads.go) |
| 13 | API passes both expected UIDs to exec | `TestCoordinatorThreadEntry/human` | [threads.go](../../../../internal/apiserver/threads.go) |
| 14 | API fixed coordinator model and effort | `TestCoordinatorAPIRejectsDifferentModelOrEffort/model` | [threads.go](../../../../internal/apiserver/threads.go) |
| 15 | One-use API route refuses before exec/reservation | `TestCoordinatorOneUseRCRefusesBeforeReservation` | [codex_rc.go](../../../../internal/apiserver/codex_rc.go) |
| 16 | Agentd preflight invokes metadata/purpose guard | `TestCtlCoordinatorThreadMetadataAndTargetFences/session-uid` | [thread_launch_codex.go](../../../../internal/agentd/thread_launch_codex.go) |
| 17 | Agentd explicit coordinator purpose | `TestCodexCoordinatorEntryMetadataAndUIDs/ordinary-intent` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 18 | Agentd workbench marker on admitted host | `TestCodexCoordinatorEntryMetadataAndUIDs/pod-account` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 19 | Agentd controller host name binding | `TestCodexCoordinatorEntryMetadataAndUIDs/metadata-host` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 20 | Agentd controller Session UID binding | `TestCodexCoordinatorEntryMetadataAndUIDs/metadata-uid` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 21 | Agentd explicit target host binding | `TestCodexCoordinatorEntryMetadataAndUIDs/target-host` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 22 | Agentd expected Session UID fence | `TestCodexCoordinatorEntryMetadataAndUIDs/target-session-uid` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 23 | Agentd expected Pod UID fence | `TestCodexCoordinatorEntryMetadataAndUIDs/target-pod-uid` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 24 | Agentd coordinator intent cannot enter ordinary host | `TestCodexCoordinatorEntryMetadataAndUIDs/coordinator-on-pool` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 25 | Agentd one-use route refuses coordinator | `TestCodexCoordinatorOneUseHostRefuses` | [codex_rc_ctl.go](../../../../internal/agentd/codex_rc_ctl.go) |
| 26 | Title setter runs after exact new-ID binding | `TestCodexCoordinatorTitleAfterExactBinding/confirmed` | [thread_launch_codex.go](../../../../internal/agentd/thread_launch_codex.go) |
| 27 | Title lost ACK stops unconfirmed | `TestCodexCoordinatorTitleAfterExactBinding/lost-ack` | [thread_launch_codex.go](../../../../internal/agentd/thread_launch_codex.go) |
| 28 | Native title read exact ID | `TestCodexCoordinatorNativeTitleRPC/read-id` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 29 | Native title read exact cwd | `TestCodexCoordinatorNativeTitleRPC/read-cwd` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 30 | Native name metadata readback | `TestCodexCoordinatorNativeTitleRPC/readback-name` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 31 | Native setter requires thread ID | `TestCodexCoordinatorTitleInvalidBindingDoesNotConnect/id` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 32 | Native setter requires host metadata | `TestCodexCoordinatorTitleInvalidBindingDoesNotConnect/host-metadata` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 33 | Native setter requires Session metadata | `TestCodexCoordinatorTitleInvalidBindingDoesNotConnect/session-metadata` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 34 | Native setter requires Pod UID | `TestCodexCoordinatorTitleInvalidBindingDoesNotConnect/pod-uid` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 35 | Pure selector reserved-host exclusion | `TestCoordinatorPoolExclusions/reserved-name` | [select.go](../../../../internal/codexhostpool/select.go) |
| 36 | Pure selector workbench-account exclusion | `TestCoordinatorPoolExclusions/workbench-account` | [select.go](../../../../internal/codexhostpool/select.go) |
| 37 | Pure selector unknown-account exclusion | `TestCoordinatorPoolExclusions/unknown-account` | [select.go](../../../../internal/codexhostpool/select.go) |
| 38 | Pure selector excludes privileged capacity | `TestCoordinatorPoolExclusions/reserved-name` | [select.go](../../../../internal/codexhostpool/select.go) |
| 39 | Pure selector named-host bypass is fenced | `TestCoordinatorPoolExclusions/reserved-name` | [select.go](../../../../internal/codexhostpool/select.go) |
| 40 | Pure selector excludes requested workbench/unknown profile | `TestCoordinatorPoolExcludedRequest` | [select.go](../../../../internal/codexhostpool/select.go) |
| 41 | Pool facts resolve profile accounts | `TestCoordinatorPoolFactsAndLease/workbench-alias` | [codexhost.go](../../../../internal/apiserver/codexhost.go) |
| 42 | Pool facts inspect actual pod account | `TestCoordinatorPoolFactsAndLease/workbench-pod` | [codexhost.go](../../../../internal/apiserver/codexhost.go) |
| 43 | Pool facts refuse unavailable account | `TestCoordinatorPoolFactsAndLease/unknown-profile` | [codexhost.go](../../../../internal/apiserver/codexhost.go) |
| 44 | Pool facts reserve name without annotation/hands-off config | `TestCoordinatorPoolFactsAndLease/reserved` | [codexhost.go](../../../../internal/apiserver/codexhost.go) |
| 45 | Excluded pool host cannot supply login facts | `TestCoordinatorPoolFactsAndLease/reserved` | [codexhost.go](../../../../internal/apiserver/codexhost.go) |
| 46 | Lease repeats account suitability on fresh facts | `TestCoordinatorLeaseRejectsProtectionRace/profile` | [codexhost.go](../../../../internal/apiserver/codexhost.go) |
| 47 | Pool requested-account classification | `TestCoordinatorPoolRequestedAccount/wb-alias` | [codexhost.go](../../../../internal/apiserver/codexhost.go) |
| 48 | Explicit builder admission has no nil fallback | `TestCodexCoordinatorPodMetadata` | [codex_workbench.go](../../../../internal/controller/codex_workbench.go) |
| 49 | Controller supplies exact admitted host name | `TestCodexCoordinatorPodMetadata/admitted` | [pod.go](../../../../internal/controller/pod.go) |
| 50 | Controller supplies exact admitted Session UID | `TestCodexCoordinatorPodMetadata/admitted` | [pod.go](../../../../internal/controller/pod.go) |
| 51 | Controller requires Session UID before metadata | `TestCodexCoordinatorPodMetadata` | [pod.go](../../../../internal/controller/pod.go) |
| 52 | L3 ordinary pod exempts unavailable resolution | `TestOrdinaryRunningRemovedProfileMatchesMainPath` | [reconciler.go](../../../../internal/controller/reconciler.go) |
| 53 | L3 ensure does not reintroduce admission on existing pod | `TestOrdinaryRunningRemovedProfileMatchesMainPath` | [reconciler.go](../../../../internal/controller/reconciler.go) |
| 54 | Settings forwards CodexCoordinatorHost | `TestCtlCoordinatorThreadMetadataAndTargetFences/none` | [settings.go](../../../../internal/agentd/settings.go) |
| 55 | Settings forwards CodexCoordinatorUID | `TestCtlCoordinatorThreadMetadataAndTargetFences/none` | [settings.go](../../../../internal/agentd/settings.go) |
| 56 | Settings forwards WorkbenchPod | `TestCtlCoordinatorThreadMetadataAndTargetFences/none` | [settings.go](../../../../internal/agentd/settings.go) |
| 57 | Native setter requires mutation ACK | `TestCodexCoordinatorNativeTitleRPC/set-ack` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |
| 58 | Native title is the exact coordinator title | `TestCodexCoordinatorNativeTitleRPC/none` | [codex_coordinator.go](../../../../internal/agentd/codex_coordinator.go) |

## Remaining gates

Independent-family review is pending; a skipped advisory job is no review.
The PR stays draft. No merge, release, installation, flags, host operations or
acceptance is authorized to this worker. Slices (c1/c2/d/e) were not started.
Owner confirmation and the concrete install packet remain required. Source
merely merged is not installed. Watch lanes and OP812 custody remain with their
existing owners; neither this PR nor successful CI transfers them.
