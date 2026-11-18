# Agent-only Codex 0.169.1 candidate (OP921)

Fix round 1 supersedes draft PR #1220 under the coordinator's 2026-11-18
K1–K5 rulings. The login helper and keeper remain out of scope; the helper stays
on 0.160.1. No platform default, privileged coordinator model/effort, deployment
or running session changes here. Source qualification, CI builds, installation
and runtime acceptance are separate evidence.

The refresh concern is closed by source review (claim (b) below). Readiness now
requires the retained-host migration and names-only layout checks in image CI.
Ordinary 6.2 admission is supplied by [PR #1219](https://github.com/thaynes43/dev-env/pull/1219).
Pin an agent image containing both the model admission and 0.169.1 client before
or with the operator that admits 6.2 requests. Older agentd validates and refuses
a Session naming either 6.2 model; an operator-only rollout strands that request.
Existing sessions move images on suspend/resume or pod replacement, never because
this source PR itself changes a running session.

## Exact source and lowest catalog version

The candidate is `rust-v0.169.1`, commit
`c376cf7396eff83ce4c6ac61b9553df142e7a0be`. [Source audit manifest](codex-cli-source-audit.json)
pins 31 relied-on source file hashes, checked against immutable commit bytes.
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
| (b) Access-only copies cannot rotate or revoke the keeper login through refresh | [manager.rs:3742-3767](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/auth/manager.rs#L3742) calls [oauth/client.rs:97-113](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/oauth/client.rs#L97): the body contains only the public client id, grant type, reason and empty refresh token. No access token, account id or Authorization header is sent (default client at auth/default_client.rs:217,493; manager.rs:918,942). The empty string cannot identify, rotate or revoke the keeper token family. Failed refresh returns before persist_tokens (manager.rs:1758-1802,3767), so writes nothing. Successful persistence uses compare-and-save under a write lock (storage.rs:195-231). agentd's byte comparison restores its canonical access-only projection on the next valid poll (daemon.go SyncCodexAccess; codex_auth.go:122-129). | Holds for refresh at source. No background refresh timer or CPU loop: permanent failure is memoized per snapshot (manager.rs:3549-3554,3586-3590), unauthorized recovery is bounded (2164-2223); transient errors follow request/retry rate. Both clients have the same five-minute window and guarded reload (old manager.rs:203-204,2393-2407,2848-2882,2897-2940; new 232-233,2701-2715,3473-3518,3658-3676). The server's actual answer to an empty refresh token was not observed. |
| (c) Keeper refresh does not use new CLI | `internal/keeper/codex_transport.go:47-65` directly POSTs one fenced refresh grant with its existing HTTP client; keeper worker/journal owns rotation. Native CLI is used only for separately requested login ceremonies, with `CodexLoginVersion=0.160.1` at `codex_helper.go:23,99`. Helper Dockerfile and smoke expectation remain 0.160.1. | Holds at source; no keeper change or runtime action. |
| (d) Exact missing-metadata behavior | token_data.rs:25-34 defaults absent fields to None. No warning/refusal attributable to absence was found in the ordinary token-read path. [auth/manager.rs:2899-2916](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/auth/manager.rs#L2899) requires populated provenance/owner, a nonempty refresh token and matching account/user for account-grant eligibility; absent metadata yields no grant. | Ordinary legacy reads accepted; stronger grant unavailable. This does not disable ordinary native refresh attempts. |

No local newer CLI was installed: the initial work order's conditional newer
probe did not apply after provider refusals. The coordinator assigned additional
newest-client probes to another worker. Source qualification and CI are distinct
from those provider results.

## Compatibility findings and tests

| Platform contract | Pinned upstream source / qualification |
| --- | --- |
| Remote daemon/process identity | cli/src/main.rs:1317-1350 preserves managed-daemon Unix listeners and DisabledEphemeral for explicit remote-control suppression. app-server-transport/src/transport/remote_control/protocol.rs and cli/src/remote_control_cmd.rs retain process/lifecycle output fields consumed by agentd. managedCodexVersion and hostexecutor.PinnedNativeVersion move together. Live daemon/package probes still require 0.169.1. Only receipts and positively absent managed packages from the named predecessor 0.160.1 receive the bounded migration below; other versions refuse. |
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

The coordinator closed the refresh hold after independent source review. Image CI
must demonstrate K2/K3 before this replacement PR is ready. No provider inference,
installation, deployment pin, release, restart or runtime model switch is included.
The server's actual answer to an empty refresh token remains unobserved.

### Separate newest-client probe evidence received during source work

The coordinator's probe worker appended `probe-receipts-newer-cli.json` to the
story handoff. Its 0.170.0 control and both requested 6.2 IDs completed
successfully on 2026-11-18T20:12Z: Sol input/output 13,785/6, Astra 13,734/6
(both cached input 11,136). Native turn_context names the exact requested IDs;
no separate provider served-model field is exposed and no fallback is evidenced.
Copies/original login remained unchanged and copies were deleted in those
receipts. This establishes successful completion for each requested model in
that worker's test, not an unconditional no-refresh guarantee or a test of the
0.169.1 candidate. Different pod/time/client prevent isolating client version as
the cause of the earlier 0.160.1 refusals. Those probes do not qualify 0.169.1; the refresh ruling rests on the source review.


Additional source checks: `config/src/fingerprint.rs:54-65` still emits the
`sha256:` plus 64 lowercase-hex user-layer version that agentd validates;
`app-server/src/config_manager_service.rs:341-345` refuses mismatched expected
versions before applying edits. `models-manager/src/manager.rs:297-309` preserves
an explicitly supplied model rather than choosing a native default. The bundled
catalog can change its own default; platform managed-task arguments and host
config continue to specify their existing model, so this candidate changes no
platform default. Remote enrollment's auth path at
`app-server-transport/src/transport/remote_control/auth.rs:174-201` still accepts
Codex-backend ChatGPT auth with an account ID; native refresh/recovery in that
path has the refresh properties in claim (b); live pairing remains unqualified.

## Retained host package replacement (M1)

A fresh host installs the invoking complete CLI package: upstream
[lib.rs:474-482](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/app-server-daemon/src/lib.rs#L474)
calls [prepare_install.rs:30-42,105-119](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/app-server-daemon/src/prepare_install.rs#L30).
Its Missing mode preserves an existing current selection, even 0.160.1. The
[0.160.1 installer:99-119,248-267](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/app-server-daemon/src/prepare_install.rs#L99)
uses the same dedicated releases/current/bin layout for a fresh stable start. Remote
start uses the same path (lib.rs:771-792); without replacement the exact native
RPC version check would report Unknown on the retained old daemon.

The supported `app-server daemon update --from-cli --yes` command uses Replace
mode (cli/main.rs:1423-1445; prepare_install.rs:47-65). It verifies and stages the
invoking image's complete package, holds native operation/install locks,
rechecks the selection/backend, and atomically switches current
(prepare_install.rs:157-239,293-318,382-421). With no backend it starts none.
No download, login mutation or session/history deletion is needed. Native package
pruning concerns only package releases; the old receipt is never removed by
agentd's package preparation.

`previousPinnedCodexVersion` admits exactly 0.160.1. Every lifecycle state keeps
its existing meaning: only a missing or Stopped receipt permits start; interrupted
states still need review, malformed/mismatched receipts refuse. Before replacement
agentd checks the image version and repeats its existing process-absence proof
(PID boot/start identity, updater identities, native locks, socket rules), then
checks absence and the selected 0.169.1 package again. A replacement failure leaves
the original receipt bytes. The usual durable Attempting receipt still precedes
the single native start; Confirmed requires observed readiness. Remote-mode
sessions share this helper after their own existing absence proof; live or unknown
process state never permits replacement.

Image CI extracts the complete package from the built image, not a bare binary.
A synthetic retained HOME with a 0.160.1 Stopped receipt, old release/current/bin
layout and history sentinel must start through real 0.169.1 package replacement;
an unrelated receipt version or package version each refuses before mutation. This exercises
native daemon startup and peer/version RPC, with remote enrollment suppressed,
mount preflight and auth projection stubbed. It does not prove actual pairing,
relay connectivity or keeper-generation adoption. The same job also temporarily
removes predecessor admission and the auth.lock classification, checks that each
real-binary test fails at that guard, and restores the exact source bytes.

## CODEX_HOME qualification and real-binary CI limits (M2, L2, L3)

`auth.lock` is not-history: native storage.rs:195-213 creates a coordination lock
with no login contents. The image job's TestPinnedCodexHomeLayout uses one scratch
HOME for exact version, offline schema export, one loopback `exec --json`, and an
offline file-auth write with a made-up API key through app-server stdio. No CLI
login/logout, real account, refresh or provider authentication is used. It prints
only top-level names and refuses every name absent from codexLayout; the fixture
must include sessions, auth.json and auth.lock. Observed names and classifications
are recorded after CI qualification below. The earlier 0.160.1 observations remain
historical evidence, not an assertion that the layout never changes.

The bundled catalog's first model changes from gpt-6.1-sol to gpt-6.2-sol in
0.169.1. Base config and host config still name gpt-6-astra; managed/clean launch
arguments still name their model. Every platform path selects explicitly, so no
platform default changes. A bare invocation without config or a model argument
can follow the bundled catalog instead.

CI proves the exact version, required help flags (presence only), 16 byte-equal
offline schema exports, effective policy matrix, wrapper dry-run, real native
control/reconnect/history with one synthetic loopback response, retained-package
migration/refusal, and the top-level layout for the listed offline actions.
It does not prove ChatGPT auth/refresh, provider entitlement or successful 6.2
inference, keeper adoption, remote-control enrollment/pairing, TUI remote behavior,
thread/list/items, folder-trust config/value/write, coordinator name setter,
rc-create or allowance account/read against a real daemon. The rollout/usage
fixture is synthetic, not a real provider rollout. The offline API-key file write
qualifies file layout, not the shared ChatGPT access-only path. Logout revocation
is a pre-existing separate story (L1), unchanged here.
