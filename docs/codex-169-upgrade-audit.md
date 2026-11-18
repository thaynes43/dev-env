# Agent-only Codex 0.169.1 candidate (OP921)

**Draft; not ready to merge or deploy.** The coordinator's supported answer to
OP921 Q1 on 2026-11-18 split model admission from the CLI candidate and left the
login helper/keeper out of scope. This candidate changes only the agent-side
CLI/version qualification. `images/codex-login/Dockerfile`, the helper CI
expectation, keeper code, all defaults, privileged model restrictions and
running sessions remain unchanged.

The agent-side no-refresh claim required by that ruling cannot be established
unconditionally from upstream source. CI success does not remove this hold.
The [model admission PR #1219](https://github.com/thaynes43/dev-env/pull/1219)
contains the full gate inventory and helper prerequisite. Either PR can be
reviewed against main without silently depending on the other's source changes.
Ordinary 6.2 admission requires #1219; this CLI candidate alone grants none.

## Exact source and lowest catalog version

The candidate is `rust-v0.169.1`, commit
`c376cf7396eff83ce4c6ac61b9553df142e7a0be`. [Source audit manifest](codex-cli-source-audit.json)
pins 25 relied-on source file hashes, checked against immutable commit bytes.
Baseline `rust-v0.160.1` is commit
`d27764b82f7118f674371e6d6e76271d9d606edb`.

Every stable tag between them was checked via
`codex-rs/models-manager/models.json`: 0.160.1, 0.161.0 through 0.169.0 and
patch tags 0.162.1, 0.167.1, 0.167.2 lack both new slugs; 0.169.1 names both.
0.170.0 also names them. No later-only platform fix was identified that warrants
skipping the first stable catalog version. This is a catalog minimum, not a
claim that an unconditional platform-safe upgrade has been demonstrated.

Both 6.2 entries in that native manifest list low (default), medium, high,
xhigh, max and ultra. The installed 0.160.1 probes sent both exact IDs but each
was refused by the provider with HTTP 400 model_not_found, unsupported with a
ChatGPT account. No served-model or token-count evidence was returned. No model
fallback, account entitlement or successful upgraded-client inference is claimed.

## Four claims required before readiness

All upstream paths below refer to the pinned source commit above.

| Claim | Source evidence | Conclusion |
| --- | --- | --- |
| (a) Read an old-format access-only login | [login/src/auth/storage.rs:53-67](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/auth/storage.rs#L53) retains auth_mode, OPENAI_API_KEY, tokens, last_refresh. [token_data.rs:11-36](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/token_data.rs#L11) makes oauth_provenance/login_owner_id optional with serde defaults. [auth/manager.rs:657-678](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/auth/manager.rs#L657) obtains ordinary ChatGPT token data without those fields. dev-env `internal/codexauth/access.go:207-226` writes the legacy shape with a required empty refresh string. | Compatible deserialization/ordinary access use at source. No real login or installed-client test here. |
| (b) Nothing agent-side writes or refreshes it | `internal/agentd/codex_auth.go:84,110-130` checks expiry >5 minutes at projection install and atomically writes newer access-only generations. This expected projection writer is not a token rotation owner. Upstream [auth/manager.rs:3658-3675](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/auth/manager.rs#L3658) can schedule proactive refresh near expiry or for stale last_refresh. `:3542-3578` routes Chatgpt auth to `:3742-3789`, which sends the stored refresh string without a nonempty guard and persists successful returned credentials. [oauth/client.rs:97-143](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/oauth/client.rs#L97) constructs/sends the refresh request, including an empty string. | **Not established. Draft blocker.** Normal early keeper refresh reduces exposure but cannot prove an already-running client never enters this window after delay/outage. Baseline also had an empty-refresh window; this story does not change that credential contract or test a real refresh. |
| (c) Keeper refresh does not use new CLI | `internal/keeper/codex_transport.go:47-65` directly POSTs one fenced refresh grant with its existing HTTP client; keeper worker/journal owns rotation. Native CLI is used only for separately requested login ceremonies, with `CodexLoginVersion=0.160.1` at `codex_helper.go:23,99`. Helper Dockerfile and smoke expectation remain 0.160.1. | Holds at source; no keeper change or runtime action. |
| (d) Exact missing-metadata behavior | token_data.rs:25-34 defaults absent fields to None. No warning/refusal attributable to absence was found in the ordinary token-read path. [auth/manager.rs:2899-2916](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/auth/manager.rs#L2899) requires populated provenance/owner, a nonempty refresh token and matching account/user for account-grant eligibility; absent metadata yields no grant. | Ordinary legacy reads accepted; stronger grant unavailable. This does not disable ordinary native refresh attempts. |

No local newer CLI was installed: the initial work order's conditional newer
probe did not apply after provider refusals. The coordinator assigned additional
newest-client probes to another worker. Source qualification and CI are distinct
from those provider results.

## Compatibility findings and tests

| Platform contract | Pinned upstream source / qualification |
| --- | --- |
| Remote daemon/process identity | cli/src/main.rs:1317-1350 preserves managed-daemon Unix listeners and DisabledEphemeral for explicit remote-control suppression. app-server-transport/src/transport/remote_control/protocol.rs and cli/src/remote_control_cmd.rs retain process/lifecycle output fields consumed by agentd. managedCodexVersion and hostexecutor.PinnedNativeVersion move together. Exact version mismatches remain refused. |
| thread/list, turns/items and title | app-server-protocol/src/protocol/v2/thread.rs, thread_data.rs and item.rs retain the fields agentd consumes, source kinds and status enum. New filters/paging, root-turn and moderation fields are additive. The full experimental schemas are repinned, not hand trimmed. Coordinator name setter schemas remain unchanged. |
| config/value/write | protocol/v2/config.rs retains keyPath/value/mergeStrategy/expectedVersion and ConfigWriteResponse including its version. Both retained config schemas are byte-identical to baseline. Version-checked trust write/readback and conflict handling remain tested. |
| One-use thread/start and turn/start | protocol/v2/thread.rs keeps experimental environments and optional projectId; protocol/v2/turn.rs keeps threadId/input/model/effort and adds optional parent/root turn fields. Existing exactly-once authorization and unconfirmed-result behavior stay unchanged. No native idempotency promise is inferred. |
| Wrapper / approval / sandbox | cli/src/main.rs, exec/src/cli.rs and utils/cli retain required flags. CI image smoke checks exact installed version, wrapper flags, effective GitOps requirement policy matrix and stdio initialize. No requirements, permission defaults, security flags or hook policy is changed. |
| Exec JSONL and usage | exec/src/exec_events.rs keeps thread.started, turn.completed and usage counters. event_processor_with_jsonl_output.rs uses native token usage on turn completion. protocol/src/protocol.rs retains session_meta id/cwd/cli_version, turn_context.model and token_count total_token_usage; new metadata is additive. A minimal synthetic 0.169.1 rollout fixture tests those fields, repeated cumulative snapshots and ignored token_usage_record. |
| Retained history / discovery | protocol/v2/project.rs and remote_control.rs source definitions match baseline. Existing 0.160.1 schema fixtures are retained; candidate exports added with provenance. Discovery and portal retained-rollout readers accept both exact qualified versions and refuse unknown versions. Historical source evidence, including historyexport_layout.go's 2026-10-13 observations, is preserved. Cold .jsonl.zst rollouts, fork/revert lineage and bounded partial coverage already limit readers and remain unqualified; no full-history acceptance claim. |
| Allowance collector | protocol/v2/account.rs retains account/read refreshToken=false, quota fields and nullable ordinaryUsageAllowed. Remote helper admits exact old/new versions, preserving existing hosts; unknown versions still refuse. Provider registry source semantics are unchanged. No live account read or private-registry qualification is claimed. |
| DESIGN-067 section 9 hooks | core/src/unified_exec/async_watcher.rs retains original-turn/call exec-end delivery. core/src/hook_runtime.rs:812-854 drains async results at safe turn boundaries, before a next prompt or into an active turn. No supported automatic idle-thread wake is established; DESIGN-067 and ADR-024 stay untouched. |

The experimental export archive SHA-256 is
`015daa0d0c1f645c1085ca11ae8e2057d02df357b50f2eaa5fc3b6726972014f`.
`app-server-protocol/src/precomputed_exports.rs:15-18,98-121` loads that archive
and writes its exact strings for offline schema generation. Fixtures in
`internal/agentd/testdata/{codex_thread_launch,codex_rc_create}` and
`internal/codexrc/testdata/schema/codex-cli-0.169.1` come from that source.
The image smoke test compares all 16 retained schemas with the built CLI's own
offline generation. CI also runs the existing pinned native reconnect test with
one loopback synthetic Responses exchange, no account/provider connection, and
explicit remote-control suppression. Images are built only in CI.

## Prerequisite for upgrading the codex-login helper

The helper stays at 0.160.1. Its existing `CodexLoginVersion` check would refuse
a newer image before the ceremony. Separately, upstream login/src/server.rs:858-864
writes `oauth_provenance` and `login_owner_id` inside tokens, while dev-env
`internal/keeper/codex_control.go:250-264` plus
`internal/codexauth/access.go:54-71` reject those unknown fields. Renovate #955
must not merge as it stands; changing its stale CI expectation would not resolve
these runtime/schema guards.

That credential prerequisite needs its own story, short design and review by
another model family. Preserve validated issuer/client provenance and opaque
login owner through full-login journal/refresh/adoption round trips: upstream
auth/account_grant.rs:129-137 compares cached and stored owner/account/user and
provenance. It compares a persisted identity, not which process minted it;
matching externally issued owner IDs are not inherently rejected. A mismatched
owner must not publish a stale grant. Missing metadata remains legacy/no grant.
The narrow ordinary-refresh legacy fallback at auth/manager.rs:1747-1755 is not
evidence that dropping metadata preserves stronger account-grant semantics.
Keep access-only projections without refresh-grant authority.

Synthetic prerequisite tests should cover old/new adoption, valid/invalid and
duplicate fields, preservation through refresh/journal/full-login round trip,
changed owner/account/user/provenance fencing, missing-metadata/no-grant behavior,
access-only empty refresh output, failed/delayed rotation and no sensitive
material in logs. No keeper source, test, credential or grant was changed here.

## Readiness and what remains unknown

The source candidate and regression/CI evidence can be reviewed, but the four-claim
ruling holds it in draft because claim (b) fails. It needs an authorized resolution
of the native auth-refresh boundary and its own acceptance evidence before ready.
No alternate identity, token, flag, security rule or external route is used to
bypass that hold. The model PR is independent. No defaults, privileged
coordinator model/effort, running host/session, keeper, deployment pin, release,
installation, restart or runtime model switch is part of this candidate.
