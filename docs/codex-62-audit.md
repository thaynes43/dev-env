# Codex 6.2 gate and compatibility audit (OP921)

Source preparation, 2026-11-18, based on dev-env main
`4f3ef3d9bf02ec9b9f4172b06345751d15389be2`. No installation, host or session
change, release, deployment pin, default-model change or credential action.
Model admission and provider entitlement are separate.

## Tiny installed-client probes

One `codex exec --json` per model, installed CLI 0.160.1, low effort, stdin
`/dev/null`, one-line reply request, no tools, in this worker's isolated runtime
directory. Each used a private, read-only copy of this pod's own access-only
login, checked unchanged and deleted immediately. User config/rules and project
documents were excluded. No login or refresh command, newer client, other
account or host was used.

| Exact request | UTC start | Client | Provider receipt |
| --- | --- | --- | --- |
| `gpt-6.2-sol` | 2026-11-18T19:56:25Z | Sent; native turn_context records this ID | HTTP 400, `model_not_found`, unsupported with a ChatGPT account; exit 1, turn.failed |
| `gpt-6.2-astra` | 2026-11-18T19:56:28Z | Sent; native turn_context records this ID | HTTP 400, `model_not_found`, unsupported with a ChatGPT account; exit 1, turn.failed |

Both are **refused by the provider**, not by the installed client. No usage
counts, served-model receipt or fallback was reported. The conditional
newer-client probe was not triggered: installed 0.160.1 could name and send both
IDs. Sanitized structured receipts are retained in the story handoff. The
coordinator assigned any additional newest-client probes separately.

[Official model guidance](https://developers.openai.com/api/docs/models) names
both models; that API documentation establishes neither this account's Codex
entitlement nor native effort support. The tagged native manifest is the effort
source: both 6.2 IDs list low (default), medium, high, xhigh, max and ultra.
This PR admits explicitly requested IDs; it selects no new default.

## Gate inventory

This table describes the baseline plus the ordinary admission change. All
platform admission gates use source rules; none fetches a live model catalog.
Only the native CLI owns its bundled/refreshed model catalog and provider call.
An unknown ID reaching a source allowlist is refused; shape-only or display
surfaces below do not promise provider support.

| Surface / source | Behavior for unknown IDs or versions; catalog source |
| --- | --- |
| `internal/agentd/protocol/session.go:372,407` | Central exact model allowlist and model/effort validation; Session.Validate uses both. Adds only gpt-6.2-sol/astra. Unknown/alias refused, empty effort retains native default. Source list, not runtime discovery. |
| `internal/apiserver/validate.go:184,190,253` | Ordinary remote and enabled project task creation reuse the central checks. Unknown refused before Session creation. Existing project/profile/caller/feature gates still apply. |
| `internal/apiserver/threads.go:169`; `internal/apiserver/apiv1/codex_rc.go:40` | Thread start and default-off one-use request reuse central model/effort checks. Unknown refused; explicit supported effort required. |
| `api/v1alpha1/agentsession_types.go:234`; `config/crd/dev-env.haynesops.com_agentsessions.yaml` | Ordinary model is a bounded string, not a Codex enum. Schema alone does not reject an unknown Codex ID; API/agentd do. Sealed private pilot CEL remains fixed gpt-6-luna/high at line 175. No CRD change needed. |
| `internal/agentrun/create.go:225` | Ordinary --agent codex requires an explicit nonempty model and forwards it unchanged to API; no local known-model list/default. API refuses an unknown ID. |
| `internal/agentrun/thread.go:144` | Explicit thread route applies central effort/model checks before API or exec; unknown refused. |
| `internal/agentrun/rc.go:205` | Generic rc start's Codex adapter remains unimplemented for every model. This is a route boundary, not a missing model ID. |
| `internal/agentd/managed_codex.go:179,267,295` | Managed task admission uses central checks; argv preserves model and nonempty effort. Exact managedCodexVersion=0.160.1 before process launch; other CLI versions refused. Remote host launch and observers reuse this version. |
| `internal/agentd/thread_launch_codex.go:289,603`; `codex_rc_create.go:39,65` | Thread wrapper adapter / one-use selection reuse central checks. Observer handshake requires exact managed version; unknown versions fail closed. No catalog lookup. |
| `internal/agentd/codex_host_config.go:339,396`; `codex_host_private.go:281` | Host config preparation and retained receipt force/verify gpt-6-astra/max, plus OpenAI/ChatGPT/file-store binding. They are host defaults/integrity gates, not ordinary explicit-thread admission. Unchanged. |
| `internal/agentd/codex_host.go:100,208,503`; `codex_host_rpc.go:228`; `codex_remote.go` | CLI/managed daemon and lifecycle receipt version checks use managedCodexVersion. Unknown/mismatched process, state or version refused, no repair or upgrade implied. |
| `scripts/codex-clean-session.py:62,124,166` | Model syntax only, accepts an otherwise unknown well-formed ID; native client/provider decide support. Effort choices low..max. Help must contain required flags. Does not alter config/auth, start a daemon or consult a catalog. |
| `internal/codexrc/discovery.go:15,152`; `decode.go` | Exact CLI 0.160.1 and pinned project/status response schemas. Unknown version/fields refused. This is host/project discovery, not model discovery. |
| `internal/codexhostpool/select.go:87` | Selects from profile, ownership, pairing/activity/repo facts and minimum agentd 2.102.0. No model field or Codex CLI-version catalog. Unknown/unparsable agentd version is image_outdated; a model must later pass thread admission. |
| `internal/agentd/hostexecutor/readiness_linux.go:18`; `executor.go:118`; `native_control_linux.go:148,152` | Separate owned native executor exact CLI=0.160.1, same-peer handshake. thread/start/resume and turn/start force gpt-6-luna; arbitrary model overrides are unavailable. Unchanged model restriction. |
| `internal/projectcanary/policy.go:143`; `pilot.go:29`; `testutil/fixtures.go:59` | Reviewed canary policy uses central validation; unknown refused. No policy is created here. Sealed pilot remains fixed Luna. Sol in testutil is fixture data, not runtime default. |
| `internal/agentd/portal_conversation.go:494,522`; `conversation_usage.go`; `codex_rollout.go` | Portal/conversation readers pin session_meta.cli_version to managed version, validate model syntax rather than admission list, and retain Unknown for absent/bad evidence. token_count / turn_context / cumulative counters are read from bounded JSONL; requested model is not entitlement. |
| `internal/agentd/codex_allowance.go:68,111`; `codex_owned_allowance.go`; `codex_rollout_allowance.go` | Exact daemon handshake pin for RPC. Rollout allowance uses bounded token_count/rate_limits fields, not a model allowlist. An unqualified version/data shape stays Unknown/unavailable. |
| `internal/usagecollector/remote_batch_linux.go:73`; `codex.go` | Remote helper refuses CLI versions other than 0.160.1 before account/quota calls. Account reads use refreshToken=false and refuse credential refresh requests; no model selection. Source qualification and public synthetic fixtures in internal/providerallowance are separate from a new version's entitlement. |
| `internal/agentd/protocol/usage*.go`; `internal/providerallowance`; `web/portal/session/product_*` | Observed model IDs are bounded/syntax checked and displayed, not selected from a Codex catalog. Unknown valid IDs may be displayed; absent counters/cost stay Unknown. Historical source/version fixtures are not live allowlists. |
| `web/portal/session/launch.go`; `internal/apiserver/portal_launch.go:49` | Portal launch is Claude-only with its own offered model list and server validation. Codex IDs are outside that launch route, not missing entries to add. Session titles classify any gpt- prefix as GPT (`internal/sessiontitle/title.go:154`). |
| `images/agent/Dockerfile:197`; `images/codex-login/Dockerfile:24`; `.github/workflows/ci.yml:439`; `images/agent/smoke-test.sh` | Both standalone image pins are 0.160.1; helper CI checks exact string. Agent smoke checks policy/flags, app-server initialize, wrapper and installed layout. Dockerfiles supply bundled catalogs; they do not establish installed versions of old sessions. |
| `internal/keeper/codex_helper.go:23,99` | CodexLoginVersion=0.160.1 is a separate exact helper CLI guard before a login ceremony. Any other native CLI version refuses. It stays unchanged; this is an additional reason an image-only helper bump is incomplete. |
| `internal/codexauth/access.go:204`; `internal/agentd/codex_auth.go:52`; `internal/keeper/codex_control.go:250` | Access-only projection writes legacy auth fields/empty refresh string; keeper strictly adopts full native login shape. Neither is a model gate. New helper login schema incompatibility below blocks bumping both images. No keeper changes in this story. |
| `docs/cli-model-upgrades.md`, `codex-clean-session.md`, `codex-coordinator-hosts.md`, `keeper-codex-auth.md`, `operator-allowance-collector.md`, `rc-launch-cli.md`, `shared-project-catalog.md`, `workflow-guide.md` | Current version guidance plus dated 0.160.1 evidence. Documents do not gate native IDs. Preserve dated observations; amend current pins only with the relevant reviewed CLI change. |
| `.agents/**`, docs examples, scripts/trials/portal_product/sessions.json, protocol/usage fixtures | Historical designs, rulings, examples and synthetic observations are source references, not runtime catalogs. No historical evidence, defaults or protected designs 066/067/068 are rewritten. |

### Privileged coordinator restrictions stay

The actual entry is `internal/agentd/codex_coordinator.go:22-26`: it requires
WorkbenchPod, exact host/session/pod identity, Coordinator=true and
`req.Model == "gpt-6.1-sol" && req.Effort == "medium"`. Matching checks are
`internal/apiserver/threads.go:171-172` and
`internal/agentrun/thread.go:105`; help at `internal/agentrun/help.go:821` states it.
Changing that boundary would touch all three checks, their negative/identity
regressions, ADR-030/DESIGN-067 policy review and an explicitly authorized
privileged pilot. Ordinary admission grants no authority to change it.

The separate Meatball front-door coordinator presets also remain:
`api/v1alpha1/callerpolicy_types.go:77`, its generated CRD,
`internal/apiserver/callerpolicy.go:64` and `internal/agentrun/coordinator.go:107`.
These restrict privileged coordinator creation to existing policy, including
Astra 6, rather than ordinary explicit model selection. No default, model policy
line in CLAUDE.md, host config or running session changes.

## Upstream version survey and compatibility

Stable tags 0.160.1, 0.161.0 through 0.169.0, and patch tags 0.162.1,
0.167.1, 0.167.2 have neither 6.2 slug. **0.169.1 is the first stable tag with
both**, and 0.170.0 has them too. Checked each tagged
`codex-rs/models-manager/models.json`, not just the latest tag or cached native
catalog. Minimum catalog tag resolves to commit
`c376cf7396eff83ce4c6ac61b9553df142e7a0be`; 0.170.0 resolves to
`29eb79e9866b3262a777fdf8195c1b9e63684c91`. Baseline 0.160.1 resolves to
`d27764b82f7118f674371e6d6e76271d9d606edb`.

Primary source links below are pinned to the 0.169.1 candidate. Source review and
synthetic tests are not a demonstrated live upgrade.

| Dependency | Finding |
| --- | --- |
| Thread discovery and first answer | [thread protocol](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/app-server-protocol/src/protocol/v2/thread.rs) and experimental exports preserve thread/list cwd/sourceKinds/limit, thread/turns/list, thread/items/list, ThreadItemEntry.item/turnId, agentMessage.text and statuses. Adds filters/paging options and moderation/root-turn metadata. agentd's bounded field readers ignore unrelated additions; schema fixtures must be repinned. |
| Config write and coordinator name | [config protocol](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/app-server-protocol/src/protocol/v2/config.rs) ConfigValueWriteParams/ConfigWriteResponse and ThreadSetName request/response schemas remain byte-identical. Expected version, sha256 user-layer versions, merge strategy, readback and conflict refusal remain needed. |
| One-use start/turn | [thread.rs](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/app-server-protocol/src/protocol/v2/thread.rs) keeps experimental environments and projectId; empty environments still expresses no environment selection. [turn.rs](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/app-server-protocol/src/protocol/v2/turn.rs) retains threadId/input/model/effort and adds optional parent/root turn fields. No idempotency guarantee was added; one-use fences must stay. |
| Discovery and remote daemon | [project.rs](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/app-server-protocol/src/protocol/v2/project.rs) and [remote_control.rs](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/app-server-protocol/src/protocol/v2/remote_control.rs) source definitions unchanged from baseline. CLI exact-version guards must move with an agent image. [cli main](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/cli/src/main.rs) still supports managed Unix daemon, stdio, --remote and no-daemon resume, and maps explicit remote-control suppression to DisabledEphemeral. No live pairing or lifecycle exercised. |
| Clean-session flags and sandbox | [cli main](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/cli/src/main.rs), [exec CLI](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/exec/src/cli.rs) and permission arguments retain --remote, --no-alt-screen, --model, --json and --dangerously-bypass-approvals-and-sandbox. The existing GitOps-requirements smoke matrix is still required; no flag/config/security-policy changes are authorized. |
| Exec and rollout usage | [exec_events.rs](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/exec/src/exec_events.rs) preserves thread.started/turn.completed usage counters; [JSONL processor](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/exec/src/event_processor_with_jsonl_output.rs) still maps native cumulative usage at turn completion. [protocol.rs](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/protocol/src/protocol.rs) retains session_meta id/cli_version/cwd, turn_context.model and token_count info.total_token_usage. New optional root-turn/account fields are additive. [history/rollout_payload.rs](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/history/src/rollout_payload.rs) retains type/payload and legacy sandbox wire fields. Existing compressed cold rollouts, fork/revert lineage and partial readers remain limitations; do not claim full history coverage. |
| Allowance | [account.rs](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/app-server-protocol/src/protocol/v2/account.rs) retains account/read with refreshToken=false and quota fields, including nullable ordinaryUsageAllowed. Exact collector version qualification is separate from the observation/source registry. Keep old host qualification; an image bump cannot imply a newer installed host. |
| Hooks, DESIGN-067 section 9 | [async_watcher.rs](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/core/src/unified_exec/async_watcher.rs) still emits execution completion against the original turn/call. [hook_runtime.rs:812](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/core/src/hook_runtime.rs#L812) drains async results at a safe turn boundary, before the next prompt or into an active turn. It does not establish automatic idle-thread wake. No design 067 edit, bridge, hook-policy change or wake claim. |
| Login read / new helper write | Read shape remains compatible because added metadata is optional. New full native login persistence adds fields the keeper rejects. Detailed prerequisite follows. An agent-only candidate requires the separate four-claim review recorded in the story handoff. |

The 0.169.1 CLI embeds exact precomputed experimental exports at
`codex-rs/app-server-protocol/schema/precomputed/app-server-exports-experimental.json.zst`,
SHA-256 `015daa0d0c1f645c1085ca11ae8e2057d02df357b50f2eaa5fc3b6726972014f`.
[precomputed_exports.rs](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/app-server-protocol/src/precomputed_exports.rs)
writes those exact schema strings for offline generation. This is the candidate
source-audit pin; historical R-05/0.160.1 evidence remains intact.

## Prerequisite for upgrading the codex-login helper

Upstream [login/src/server.rs:858-864](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/server.rs#L858)
persists `oauth_provenance: Some(provenance)` and
`login_owner_id: Some(generate_state())`. [token_data.rs:25-34](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/token_data.rs#L25)
serializes them inside tokens; absence is accepted using serde defaults.
`internal/keeper/codex_control.go:250-264` only decodes the four old token fields
and calls `codexauth.DecodeStrict`, whose `DisallowUnknownFields` is at
`internal/codexauth/access.go:71`. A fresh upgraded-helper login is therefore
rejected at keeper adoption. No real login is needed or authorized to establish
this source mismatch. **Do not merge Renovate #955 as it stands.** Its two-pin
bump both misses the platform guards and breaks fresh native login adoption.
Its helper image built but failed the stale CI 0.160.1 exact-version assertion;
the aggregate failure is the second failed check.

The prerequisite needs its own credential story, short design and independent
review by another model family. It should decide how to validate and retain the
issuer/client provenance and opaque login-owner identifier across keeper journal,
refresh, adoption and publication. Preserve them for any full-login round trip:
upstream [auth/account_grant.rs:129-137](https://github.com/openai/codex/blob/c376cf7396eff83ce4c6ac61b9553df142e7a0be/codex-rs/login/src/auth/account_grant.rs#L129)
compares stored/cached owner/account/user identity and provenance before a grant
can be used. An identifier was not necessarily minted by this reader process:
a matching persisted and cached login identity is compared, not a local
issuer-process registry. Do not invent an owner ID or infer issuer authority
from unverified JWT claims. Dropping metadata can disable grant eligibility or
invalidate an in-flight ownership comparison. The ordinary refresh path has a
narrow legacy-metadata-loss fallback in auth/manager.rs:1747-1755; that does not
establish preservation of stronger grant semantics. Access-only projections
should not gain a refresh grant as a side effect.

Requested synthetic tests: old/new native login acceptance with exact optional
field validation; malformed/duplicate/unknown fields still refused; ownership and
provenance survive keeper refresh/journal and intended round trips; mismatched
stored owner/account/user/issuer/client refuses stale grant publication; missing
metadata remains a legacy ordinary login without grant authority; access-only
output still has an empty refresh token; delayed or failed rotation cannot
publish stale material; no token/identity/prompt contents reach logs or fixtures.
No keeper source or test was changed in this story.

## Limits and delivery

`docs/cli-model-upgrades.md` was followed for source pins, explicit native
catalog/effort evidence, exact guards, source compatibility and tests. No live
upgrade, CLI install, native daemon pilot, pairing, login, rotation, default
promotion, signed-image publication or installed/accepted claim is made.
Source documentation cannot prove account entitlement. CI/image and advisory
review status belong to the corresponding PR/head, recorded in the story
handoff. The initial CLI delivery held its auth claim for review; the fix-round
ruling and current qualification are in the [0.169.1 audit](codex-169-upgrade-audit.md). Model-admission tests exercise each source-rule caller
and still refuse an unknown ID. No CRD, defaults or privileged policy changes.

### Later evidence from the separately assigned newest-client worker

The coordinator's probe worker completed the 0.170.0 control and both 6.2
requests at 2026-11-18T20:12Z. Sanitized receipts were appended to the story
handoff as `probe-receipts-newer-cli.json`; this worker checked their completion,
usage and unchanged-login/deletion fields without modifying that section.

| Requested ID | CLI | Receipt / input, cached-input, output tokens |
| --- | --- | --- |
| `gpt-6.2-sol` | 0.170.0 | exit 0, turn.completed / 13,785, 11,136, 6 |
| `gpt-6.2-astra` | 0.170.0 | exit 0, turn.completed / 13,734, 11,136, 6 |

Both requests completed successfully. Native turn_context.model names the exact
requested IDs, but the receipts expose no separate provider served-model field;
no fallback is evidenced or claimed. These additional results do not erase the
earlier 0.160.1 refusals or isolate client version from different pod/time. They
also do not test 0.169.1, install a platform upgrade or establish the required
unconditional no-refresh claim. Initial [PR #1220](https://github.com/thaynes43/dev-env/pull/1220)
was held as draft. In fix round 1 the coordinator closed the refresh concern:
an empty refresh grant carries no keeper credential and a failed refresh writes
nothing. The [replacement audit](codex-169-upgrade-audit.md) records retained-host
and layout qualification, rollout order and CI limits. The keeper prerequisite
remains separate, and #955 must not merge as it stands.
