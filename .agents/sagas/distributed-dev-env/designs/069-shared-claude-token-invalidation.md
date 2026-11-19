# DESIGN-069: Diagnose and recover shared Claude token invalidation

- **Status:** Proposed. Investigation and design only; no auth operation, code,
  build, merge or installation is authorized by this document.
- **Date:** 2026-11-18. Design revision 1 incorporates the independent review
  of PR #1225 and Tom's ruling on OP927.
- **Tracking:** [OP927](https://work.haynesnetwork.com/work_packages/927), epic
  #91, distributed dev-env saga. Implementation and installed acceptance need
  separate stories after the migration coordinator's review in another model family.
- **Source pin:** `e913a490dcf328adaa62cfc2dde5a4e717280ec9` for the incident
  investigation. The observed keeper uses source `1ae9c7ca2c0595157ab93bab74ff8640b3896ad7`.
  Its Claude worker, transport and journal files are identical at both pins.
- **Ownership:** the migration coordinator owns OP927 and acceptance. The
  investigation delivers this document and its report.

## 1. Finding and limits

The October 6 S-1 experiment observed old access-token rejection once, 22 s
after a keeper refresh on CLI 2.1.292, followed by a successful request after
the credentials-file merge. S-1 had no immediate pre-refresh control. The
behavior has yet to be re-verified. V2 shares one access token across processes
that reload independently, so the observed behavior exposes a window between
provider refresh and each consumer's reload. [S-1](../backlog/00-spikes.md#s-1-claude-on-an-access-token-only-credential)
and [plan 03](../backlog/03-remote-control.md#scope).

On November 18, generation 127 was the only usable projected generation in
the affected remote session. Generation 126 would have expired around 19:58
UTC, before the session started at 22:28:02. Startup requires usable projected
access and refuses a pod-owned refresh credential. Remote launch strips the
static access and refresh environment variables. A credential source outside
the projection remains a residual uncertainty. The subagent was rejected by
22:36:22 and the keeper refreshed successfully at 22:38:36. The last accepted
request was after the four suspensions, as the new session successfully
dispatched the subagent. The exact lower bracket is pending. Propagation delay
remains open when evaluating an earlier lifecycle action. [Startup gate](../../../../internal/agentd/daemon.go#L369-L395),
[projection check](../../../../internal/agentd/claude_auth.go#L191-L214),
[expiry check](../../../../internal/claudeauth/access.go#L80-L87),
[remote launch](../../../../internal/agentd/launch.go#L172-L187).

The main thread was also rejected. Its `sent` continue receipt requires a
main-chain assistant auth error. A native in-process subagent shares the
parent CLI's credential, and the two rejections point to the process-wide
token. The receipt confirms delivery of a prompt to the main thread. It
provides no evidence that the subagent resumed or that a later request was
accepted. The legacy classifier skips sidechains, so a sidechain-only 401 can
disappear after a successful parent turn. [Continue eligibility and send](../../../../internal/agentd/claude_continue.go#L311-L348),
[sidechain filter](../../../../internal/agentd/claude_continue.go#L164-L181).

An unexpired access token was rejected, the keeper's refresh credential was
accepted two minutes and 14 seconds after the upper bound on that rejection,
and the grant deadline reportedly stayed fixed. This points to access-only
invalidation by a holder or the provider. It does not identify a particular
actor. Section 3 records the limits of that inference.

Task and local Claude launches use the plan's static token, so a cutoff of
the keeper's projection is outside their normal credential path. The task
skip is consistent with that boundary. Verify local profiles that set the
projection path: the continue guard excludes only task mode, so a local TUI
could receive a continue after an unrelated projected generation while still
using its static token. Gate recovery on the verified effective credential
source. [Static launch](../../../../internal/agentd/launch.go#L488-L509),
[continue guard](../../../../internal/agentd/claude_continue.go#L275-L281).

## 2. Evidence and UTC timeline

The private report requested on OP927 is
`~/.shared/handoffs/claude-token-revoke-20261118/report-927.md`. This document
retains the decision-relevant evidence in Git; the shared path is not a proven
cross-pod delivery route.

| UTC | Observation | Source and strength |
| --- | --- | --- |
| 2026-08-29 | Earlier multi-holder refresh-token family revocation is documented. | [R-02 §5.1](../research/R-02-remote-control-identity.md#51-verdict), historical record, not a new live observation. |
| 2026-10-06 | S-1 observed old access-token rejection after refresh, credentials-file reload on the next request, and an ineffective file-mode 401 wait setting. | [S-1 result](../backlog/00-spikes.md#s-1-claude-on-an-access-token-only-credential), CLI 2.1.292, historical experiment. |
| 2026-10-19 15:21–17:51 | Recovery and keeper refresh were introduced in PRs #449 and #452. | Git commits `5dfbbb9d` and `7936a4df`; source history, not deployment time. |
| 2026-11-12 07:07:24 | Current keeper app started; zero container restarts at observation. | Kubernetes pod status. |
| 2026-11-18 17:18:32.923502356 | Previous successful keeper refresh was logged. | Loki, keeper app, fixed `claude auth state`, state `refreshed`. |
| 2026-11-18 22:26:58–22:27:24 | Four contained sessions reached Suspended after verified rescue. | AgentSession `suspendedAt` and rescue status. No auth action is implied. |
| 2026-11-18 22:28:02 | Affected remote Claude session was created. | Supported session metadata; mode `remote`. |
| 2026-11-18 by 22:36:22 | A subagent request received 401, token revoked. The last accepted request after 22:28:02 bounds the other end of the window. | Coordinator's OP926 comment 9239 and OP927 review. The output file contained the error and its mtime supplies the upper bound. Exact request times have yet to be captured. |
| 2026-11-18 22:38:36.328561485 | Generation 128's calculated keeper receipt time, if lifetime is 28,800 seconds. | Reported expiry minus eight hours; corroborated within 9 ms by next line. This is not an observed provider issue timestamp. |
| 2026-11-18 22:38:36.337518656 | Keeper logged successful refresh. | Loki fixed state line, before publication. |
| 2026-11-18 22:38:36.349739703 | Keeper logged ready after successful publication. | Loki fixed state line; does not prove CLI reload or provider acceptance. |
| 2026-11-18 22:38:58 | Agentd merged generation 128 and sent one continue. | Coordinator's receipt metadata, not read independently. Delivery is not authenticated retry success. |

The worker refreshes at two-thirds of the observed access lifetime, polls every
30 seconds and saves `ExpiresAt = now + expires_in` after a successful response.
The last two refreshes are 5 h 20 m 3.414 s apart, consistent with an eight-hour
lifetime and that schedule. The reported generation-128 expiry is
2026-11-19T06:38:36.328561485Z. Thus eight hours is strongly corroborated for
this generation, not a universal provider contract. The provider's exact issue
time and raw `expires_in` were not collected. The transport's three-second
request budget cannot explain a response issued over two minutes earlier.
See [worker](../../../../internal/keeper/claude_worker.go) and
[transport](../../../../internal/keeper/claude_transport.go).

The reported login lapse is 2026-12-01T15:08:48.328561485Z. Its matching fractional
seconds have a local explanation: `applyClaudeRefresh` adds both integer-second
lifetimes to the same `now` when a refresh expiry is returned. If the response
omits both the refresh expiry and `refresh_token`, it retains the previous lapse.
A nonempty `refresh_token` without a stated expiry instead makes the lapse
unknown, even if the provider repeats the previous token value.
This field is an expiry estimate, not a login-ceremony identity or timestamp.
An unchanged lapse cannot prove that no other login happened, and matching
nanoseconds do not prove a new login. See
[journal](../../../../internal/keeper/claude_journal.go).
The work order reports that the lapse stayed unchanged across the day, but
only one exact reading was supplied. That stability was not independently
verified; paired earlier generation/expiry readings would be needed to test it.

## 3. Hypotheses

Slice 0 evidence on November 19 covers the deployed configuration, surviving
pod and AgentSession metadata, Kubernetes RBAC and the pinned Claude Code
binary. The incident bracket remains the accepted dispatch after the four
suspensions and the reported rejection by 22:36:22 UTC. The precise last
accepted request, the owners' action list and provider-side audit are pending.

| Hypothesis | Classification | Evidence and limit |
| --- | --- | --- |
| H1: routine keeper refresh invalidates the old access token while consumers reload | **OBSERVED ONCE** (S-1), yet to be re-verified. **RULED OUT** for this 401 on the projection path. | Generation 128 arrived after the failure and generations 126 and earlier were expired at startup. Use §5.1 to check rejection timing against the next routine refresh windows. |
| H2: a holder logged out or otherwise revoked the shared access token | **UNKNOWN** for a separate revocation action. **RULED OUT** for the traced 2.1.321 first-party logout with only the projected access credential. | Explicit logout can revoke stored refresh credentials. The projected bearer is outside those inputs. The keeper's successful refresh weakens whole-grant logout. Access opportunity and the incident action are separate evidence. |
| H3: another holder refreshed the keeper's refresh token | **UNKNOWN**, weakened for this event | Projection excludes refresh material. The keeper recovered with the same grant, whereas rotation with reuse detection would have spent its refresh credential. The transport accepts a response without a replacement `refresh_token`, so non-rotating provider behavior leaves H3 open. Record each refresh's actual rotation result. |
| H4: a separate login/setup-token on the same account revoked earlier tokens | **UNKNOWN** | The v1 Remote Control path can use a separate file-backed Max login. The static-token configuration gives no evidence of a new ceremony in the bracket. Neither the client code nor local login state establishes account-wide activity or cross-grant behavior. |
| H5: the keeper refreshed earlier than assumed | **RULED OUT** for generation 128 at the failure time, within the recorded clock/timestamp assumptions | Fixed keeper lines and bounded request agree with expiry arithmetic. Previous refresh was at 17:18:32. Clock skew was not measured; the 401 time is coordinator-supplied metadata. |
| H6a: stale projected generation or alternate source | **RULED OUT** for an older projected generation. Alternate source **UNKNOWN**. | The affected pod had the workbench projection and a Remote Control launch that strips both OAuth environment keys. The CLI can select environment, file-descriptor or stored credentials. The observed mount and launch establish the expected source. The credential used for a specific request and separately launched CLIs remain unattributed. |
| H6b: provider-side early invalidation or an account event | **UNKNOWN**, compatible with the observed access-only shape | Concurrent use is possible. Mount metadata provides no count of simultaneous requests or fleet-wide first rejections. The provider's reason and any server-side effect of a bridge action remain unknown. |
| H6c: suspend itself logs out | **RULED OUT** in the checked dev-env and 2.1.321 client paths. | Agentd stops the process by signal and rescues work. The traced CLI signal and exit handlers lead to normal shutdown and bridge cleanup. Provider-side archive effects, the exact CLI versions of removed pods and any configured exit hooks remain outside this check. |

The source paths cited for these limits are
[projection merge](../../../../internal/agentd/claude_auth.go),
[launch credential modes](../../../../internal/agentd/launch.go),
[stop](../../../../internal/agentd/stop.go),
[shutdown](../../../../internal/agentd/daemon.go),
[rescue](../../../../internal/agentd/rescue.go), and
[S-6](../backlog/00-spikes.md#s-6-resume-with-remote-control), with the optional
refresh response field in [the keeper transport](../../../../internal/keeper/claude_transport.go#L16-L24).

### 3.1 Projection holders and potential readers

The keeper app reads the access and refresh material from its private journal
in `dev-env-system` and publishes a separate access-only Secret in `dev-agents`.
Its Claude login sidecar shares ephemeral staging during a separately
authorized ceremony. The app owns durable journal access. The session volume
gets `access.json` and optional lapse metadata. Keeper code serializes no
refresh token into the projection. [Journal and Secret names](../../../../internal/keeper/claude_journal.go),
[publisher](../../../../internal/keeper/claude_public.go#L27-L66),
[keeper deployment](https://github.com/thaynes43/haynes-ops/blob/e00c68aaaa0b75dc1b0ce7928665497fe65b5762/kubernetes/main/apps/dev-env-system/keeper/app/helmrelease.yaml).

| Profile or workload | Delivery and effective use |
| --- | --- |
| `full` (default) and `workbench` | The base profile adds `AGENTD_CLAUDE_LIVE_FILE` and a read-only `claude-access` mount to ordinary pods across modes and providers. `full` uses `dev-env-agent` and `workbench` uses `dev-env-workbench`. A Codex pod can hold the mount even though agentd does the Claude merge only for a Claude session. In task and local Claude modes the controller also injects the separate static token and the CLI gives that environment source priority. The local-mode merge and recovery concern in §1 therefore applies to these profiles. |
| `ops` | The projection exists in the profile's `remote` block. The controller applies that block to ordinary remote agent pods across providers. Task, local, rescue hold, project canary and pilot pods skip that block. Hold pods also skip the base credential mount. |
| `dev`, `rule-canary-claude`, `rule-canary-codex` | The pinned templates define no Claude keeper mount for these profiles. A remote Claude launch without a projection uses the pod-owned login path. |
| V1 in `dev` | The running pod's app and auth-watch containers reference the v1 static Claude Secret and share the home PVC. The v1 and v2 static Secrets reference the same external property, separate from keeper publication. V1's `both` launch strips the static environment key and uses its PVC-backed login. The pod spec has no keeper Secret reference. Whether any PVC, Mac or other external login contains a copy of the keeper credential is unknown. |

The pinned profile definitions and observed projection pods declare no
`CLAUDE_CODE_OAUTH_REFRESH_TOKEN` environment key. This audit checks key
names and references in the templates and pod specs. Values delivered through
`envFrom`, user settings and process environments remain unverified. Remote
launch, including explicit agentd Remote Control, strips both Claude OAuth
keys. Task and local launch retains the static key and does not strip the
refresh key. At startup and on polls, agentd merges the current generation
atomically into the Claude config directory for Claude sessions with the
projection configured. It rejects an existing Claude OAuth refresh token and
requires usable projected access before the remote launch. [Templates](https://github.com/thaynes43/haynes-ops/blob/e00c68aaaa0b75dc1b0ce7928665497fe65b5762/kubernetes/main/apps/dev-env-system/templates/app/templates.yaml#L287-L452),
[pod construction](../../../../internal/controller/pod.go),
[projection sync](../../../../internal/agentd/claude_auth.go),
[agentd startup and poll](../../../../internal/agentd/daemon.go),
[launch environment](../../../../internal/agentd/launch.go#L164-L188),
[explicit Remote Control](../../../../internal/agentd/explicit_claude_rc.go#L404-L419),
[v1 deployment](https://github.com/thaynes43/haynes-ops/blob/e00c68aaaa0b75dc1b0ce7928665497fe65b5762/kubernetes/main/apps/dev/dev-env/app/helmrelease.yaml),
[v1 launcher](https://github.com/thaynes43/haynes-ops/blob/e00c68aaaa0b75dc1b0ce7928665497fe65b5762/kubernetes/main/apps/dev/dev-env/app/resources/agent-run.sh#L899-L925).

The November 19 pod snapshot showed eight read-only mount holders in
`dev-agents`. Seven surviving pods had started and reached Ready by the end
of the November 18 22:28–22:37 UTC window. They were three `full` remote Claude,
three `workbench` remote Claude and one `full` remote Codex. The affected
workbench pod became Ready at 22:28:13. The eighth holder began at 23:40:59.
Four additional remote Claude AgentSessions (two `full`, two `workbench`)
were Suspended by 22:27:24. Their retained profile and mode imply eligibility,
while their removed pod specs and executable versions are unavailable from
this snapshot. This is a surviving-state reconstruction. Deleted or replaced
pods and any unstored copies could make the historical set larger. Keep the
per-session metadata in the restricted story handoff.

The live RBAC join used Roles and RoleBindings in both namespaces and
ClusterRoles and ClusterRoleBindings. The keeper ServiceAccount in
`dev-env-system` has named `get`/`patch` to its journal through
`dev-env-keeper-claude-auth` and to the projection through `dev-env-keeper`.
It was the only namespaced binding with a matching read rule on either
target. Neither session ServiceAccount, v1's `dev-env` account, the operator
nor the broker had direct Secret-read RBAC on the two targets. Kubelet
projection is a separate delivery path.

Cluster-wide bindings also authorize reads of **both** objects. They cover
cluster administrators (including Flux reconcilers and the cluster UI),
other Flux controllers, certificate and External Secrets controllers,
network and ingress controllers, storage and database operators, secret
reflection and reload controllers, observability controllers, backup
controllers and Kubernetes control-plane controllers. Some bindings offer
only `list`/`watch`, which can still return object data. The built-in
`system:node` ClusterRole had a binding without subjects. Node authorization
for kubelets manages assigned-pod dependencies separately. The defined
`dev-env-grant-secrets-read` and `dev-env-grant-breakglass` ClusterRoles had
no binding covering either target in this snapshot. The restricted story
handoff records the matched role and ServiceAccount roster. Grants prove
opportunity. Actual reads, historical RBAC, indirect administrative access
and external copies require separate evidence. [Journal Role](https://github.com/thaynes43/haynes-ops/blob/e00c68aaaa0b75dc1b0ce7928665497fe65b5762/kubernetes/main/apps/dev-env-system/rbac/app/keeper-claude-auth.yaml),
[publisher Role](https://github.com/thaynes43/haynes-ops/blob/e00c68aaaa0b75dc1b0ce7928665497fe65b5762/kubernetes/main/apps/dev-env-system/rbac/app/keeper.yaml).

### 3.2 Offline Claude Code and review F3

Finding F3 in the independent review of PR #1225 listed seven candidate
causes and the evidence needed to distinguish them.

The affected pod used agent image 2.111.0. Its Dockerfile and the investigation
image both pin Claude Code 2.1.321. `--version` with a clean environment
returned `2.1.321 (Claude Code)`. The local x86-64 ELF build ID was
`69e0821094c6c8ff81436e0a0adfc15b651a2679`. The offline read followed embedded
JavaScript definitions and callers. The offsets below are decimal byte
offsets in that executable. They identify the inspected code without
running a provider or auth command. [Incident image pin](https://github.com/thaynes43/dev-env/blob/v2.111.0/images/agent/Dockerfile#L156-L163),
[current image pin](../../../../images/agent/Dockerfile#L156-L163).

| F3 candidate | Classification and evidence |
| --- | --- |
| Exit of the four suspended CLIs | **RULED OUT** as a direct logout or OAuth revoke in the traced 2.1.321 paths. `/exit` and `/quit` invoke the exit path (295443695). `SIGTERM` enters the shutdown coordinator (293534049, 293542027). It can run configured session-end hooks and wait for an in-flight refresh. The interactive bridge hook tears down the transport and can archive the session (324270679, 311687400). The standalone bridge signal handler aborts polling (312216763). Cleanup stops work and either archives sessions and deregisters the environment or marks it offline for resume (312183020–312184855). Those built-in paths did not reach the OAuth revocation helper. The successful request after all four suspensions also weakens an immediate cutoff. A delayed provider effect and other executable versions are still unknown. |
| `claude auth logout`, `/logout` or forwarded terminal input | **RULED OUT** as first-party revocation from the projected access-only state in 2.1.321. The explicit CLI command (315387870) and interactive command (333044056) both call `performLogout` (309085958). Its first-party remote branch sends stored refresh credentials from Claude, Design or Cloud Sessions OAuth entries, or a configured profile's stored refresh credential (309075620). It appends `/revoke` to the token endpoint (286672762). The branch never passes the projected access token. Local credential removal can still disrupt that pod. Enterprise gateway logout has a separate bearer revocation branch (286874928), and MCP OAuth revocation uses separate credentials. Actual command execution and alternate auth state are unknown. |
| Phone, web or Mac Remote Control end or archive | **UNKNOWN** as an incident cause. The inspected client maps a server-inactive session into bridge failure and suppresses re-archiving an already archived session (311686826–311687400). It can disconnect or reattach the transport. This establishes client behavior. The provider-side action list and server effects are unverified. |
| First start of the affected session | **WEAK, UNKNOWN** for a delayed effect. The inspected bridge startup creates or reattaches a session (311622640–311624004). Profile and flag reads and bridge registration supplied no client-side revoke path. An accepted dispatch after startup weakens an immediate effect. |
| CLI self-refresh after a 401 | **RULED OUT** for a first-party standalone process holding only the projection. The 401 handler clears the cache and reads credentials again (286933082–286937265). A different stored credential can recover a retry. Forced OAuth refresh needs a stored refresh token. Some bridge archive 401s can call the auth-recovery callback and retry within a deadline (311670413, 329204192), with the same credential prerequisite for CLI-owned refresh. SDK or desktop host callbacks and runner rotation are separate branches. The bounded wait polls environment or FD tokens (286932488). It does not poll the credentials file. Runtime file-reload behavior and independently configured refresh sources are unverified. |
| Provider policy against concurrent use | **UNKNOWN**. Multiple pods had access during the window. Establish the policy with correlated first rejections and request concurrency or provider evidence. |
| Other Secret readers | **OPPORTUNITY OBSERVED** in the RBAC snapshot. Attribution stays **UNKNOWN**. The journal and projection access paths above establish eligible principals. The slice collected no evidence of a read or revocation action by any principal. |

The offline read established client-side branches. It did not test account
policy, provider propagation, lifecycle actions on the Mac or v1, or the
loaded credential in a particular process. Transcript extraction awaits
Tom's separate decision under §6.

Public primary references checked on November 18:
[authentication](https://code.claude.com/docs/en/authentication),
[CLI commands](https://code.claude.com/docs/en/cli-reference), and
[error reference](https://code.claude.com/docs/en/errors#oauth-token-revoked-or-expired).
These document sign-out, token rejection and credential-source differences;
they do not identify this incident's actor or promise independent login survival.

## 4. Frequency and whether v2 introduced it

Keeper logs contain **125 successful refresh lines across October 20–November
18**, with November 18 partial through 22:50 UTC. That is a count of refreshes,
not 125 user-visible failures or 125 independently observed revocations.
For November 12–18 the daily counts are 5, 4, 5, 4, 5, 4, 5 (32 total), with no
`claude refresh failed` lines. October 25 has no successful-refresh samples in
the daily query; missing samples alone do not establish outage or log coverage.

`needs-login` state lines appear once on October 19, once on October 25 and
twice on November 3. Repeated state lines can come from a keeper restart; these
are not four proven revocation incidents. The refresh-failure diagnostic was
added on November 4, so its absence cannot classify older events. The November
3 prevention record documents separate ambiguous/unsent refresh handling.
See [that record](../verification/keeper-refresh-failure-prevention-2026-11-03.md).

Prometheus showed no sampled non-Ready Claude keeper state in the seven-day
window ending November 18 22:50. The gauge is keeper journal/login state; it
does not test acceptance of any consumer's access token. Scrape gaps and events
between scrapes remain possible. The consumer revocation count across days is
**unknown** with the permitted telemetry. The November 18 failure establishes
one occurrence. An authorized investigator can query Loki in `dev-agents`,
`container="agent"`, using daily `count_over_time` over the two fixed messages
`Claude session continued after the login was renewed` and
`Claude continue after renewal was not confirmed and will not be repeated`.
Keep the two counts distinct. They measure confirmed and unconfirmed
main-thread recovery deliveries. Textual auth markers can qualify and an
undated error can be counted again on a later generation, so the counts
cannot bound distinct 401 episodes. Both fire at merge-time recovery and
cannot distinguish H1 from other causes or prove provider acceptance. Use
aggregate counts only. The deduplicated history in §5.1 measures observed
rejections. [Classification and timestamp cutoff](../../../../internal/agentd/claude_continue.go#L185-L202),
[stale-error check](../../../../internal/agentd/claude_continue.go#L319-L321),
[confirmed and unconfirmed messages](../../../../internal/agentd/claude_continue.go#L344-L358).

V2 makes the observed refresh invalidation affect independently reloading
pods. That architecture is new; provider revocation and refresh-token reuse
were documented before v2. The evidence does not date the start of the current
failure pattern or tie it to a Meatball change. PR #741 (October 30) broadened
recovery for delayed rotation errors; PR #885 (November 4) changed failure
journaling, not the two-thirds refresh schedule. Neither is proved causal here.

## 5. Proposed fix and decision gates

Preserve one refresh owner. Do not copy refresh credentials to workers, run
login/logout to recover a consumer, or treat each 401 as authority to rotate
the shared login. A consumer can report a failure but cannot acquire lifecycle
authority over the whole fleet.

### 5.1 Capture sanitized evidence for attribution

**Keep the existing wire documents frozen.** Running sessions keep their
agentd. An added member in a document it decodes strictly can prevent it from
applying the next generation. The following documents use strict decoders.

- v1 `access.json` in [`internal/claudeauth/access.go:110–131`](../../../../internal/claudeauth/access.go#L110-L131).
- v1 `login.json` in [`internal/claudeauth/login.go:39–49`](../../../../internal/claudeauth/login.go#L39-L49).
- `claude-access-generation.json`, the policy in
  [`internal/agentd/claude_account.go:14–18`](../../../../internal/agentd/claude_account.go#L14-L18)
  and the decoder in [`internal/agentd/claude_auth.go:107–120`](../../../../internal/agentd/claude_auth.go#L107-L120).

Keeper provenance goes in a new, versioned key on the existing keeper journal
Secret. Agentd history goes in a new, versioned file in its private state
directory. Keep both records bounded. The receipt's SHA-256 covers a document
containing the bearer. Keep it out of the new records, status, metrics and
logs, along with other credential-derived values, token lengths, raw provider
responses, transcript text and session links.

The keeper records generation, a random incarnation set at adoption, a fixed
operation kind (adoption, scheduled refresh or early refresh), dispatch,
response and publication times, access lifetime and expiry provenance. For
each successful refresh, record two booleans: whether the refresh token
actually changed and whether a refresh expiry was present in the response.
Compare refresh values only in keeper memory and persist the boolean. For
each probed generation keep the last successful probe before the first 401,
the first 401 and the sanitized result of any early recovery. Keep the current
and two previous transitions. Use closed numeric codes, times, booleans and
integers in the new key. Record a publication time only after publication is
confirmed. Leave unavailable times unknown after a restart instead of
inferring them from journal writes. Counters use fixed outcome classes. Keep
the existing journal, fence and compare-and-swap (CAS) semantics.

Slice 3 uses `refresh-provenance.json` in the private journal. Timestamps are
Unix nanoseconds, with zero for an unknown time. The two integer halves of
the incarnation are random at adoption and carry forward through scheduled
refreshes. Adoption timing measures the profile request and the remaining
staged access lifetime. Refresh timing measures the dispatch commit and
validated response, with access expiry calculated from the response lifetime.
The publication timestamp records confirmation of the access write.
On a publication-only retry, a successor timestamps a new write with its own
clock. Skew relative to the predecessor's clock is unknown.
If the write's result is lost and a successor finds the projection already
current, the original publication time remains unknown. The process counter
`dev_env_keeper_claude_refresh_outcomes_total` uses fixed `outcome` values
`success`, `not-sent`, `possibly-spent`, `status`, `read`, `invalid`,
`account-mismatch` and `save-unavailable`. Success means the replacement was
saved to the journal. `save-unavailable` includes a lost acknowledgement after
a successful save. Probe and early-recovery observations belong to slice 6.

Agentd records source kind (projection, static environment, pod-owned or
unknown), a local launch identifier, the applied generation and the actual
successful credential-file write time. The legacy receipt is persisted before
the credentials write. Its timestamp can therefore precede the first moment
the CLI could load the generation. For the current and two prior generations
retain first and last rejection, last accepted request before the first
rejection, first and latest parent acceptance, the first parent acceptance
after the latest rejection, a saturating count, fixed error class and
main-chain or sidechain attribution. Deduplicate observations across polls
and restarts. Record a sidechain 401 even if the parent's next request succeeds.
Use the rejection interval with the latest possible end, at recorded precision,
as the last rejection for recovery. Its timestamp can precede a more precise
rejection.
When a poll first confirms a generation without writing credentials, leave
the write time unknown and skip observations that could predate its confirmation.
A new generation resolves the live rejection marker, which legacy agentd
currently deletes. Retain the resolved observation in the new history file
until the generation ages out. [Legacy marker resolution](../../../../internal/agentd/claude_rejection.go#L337-L354).

- Text-only 401s can set the legacy live rejection marker and are omitted from the structured history.
- The agent can forge this evidence. Slices 2 and 6 must keep it out of every refresh decision.
- If the CLI exits before the next poll, agentd can miss observations in the last 5 s of that launch.

Attribute in-process subagents through the parent's launch and source. Use
sidechain timestamps and error class only. Each poll scans at most 4,096 child
directory entries and reads bounded tails from the 32 newest files it finds.
Give a Bash-launched CLI or teammate process its own launch/source attribution
if the supported observations can identify it. Distinguish an applied
generation, delivery of a retry prompt, and independent evidence of provider
acceptance. Leave the request's actual loaded generation unknown unless a
supported CLI signal establishes it. Use an accepted-request timestamp as the
lower bound only at its recorded precision. Mark proxy timing and unknown
clock skew. An out-of-process child with unknown source stays unknown.

Send the bounded metadata through the existing heartbeat into AgentSession
status. The operator derives the consumer-rejection metric from that status
without a pod-to-keeper write path. Keep session and generation identifiers
out of metric labels. For the new status contract run `make generate`, add
acceptance and refusal cases in `api/v1alpha1/agentsession_validation_test.go`,
and land the haynes-ops CRD copy before the operator pin. Keep the legacy
Session response shape as the default. Expose the new fields to capable
`agent-run show` clients through an explicitly requested view. Exercise the
ordinary old client and its strict `show --json --once` variant against the
new operator. The latter rejects unknown fields on the existing response.
[Strict CLI decoder](../../../../internal/agentrun/show_once.go#L21-L47).

For every future cutoff, correlate the keeper's last accepted probe and first
401, the consumer's accepted-to-rejected interval, refresh dispatch and
publication, the credential write and the next provider acceptance. Compare
the first fleet rejections with each scheduled refresh's response-to-reload
window to test H1 on normal traffic. For out-of-window cutoffs, compare the
bracket with the holder actions in §5.3. The investigator records an attributed
cause and evidence in the incident story. If the available evidence leaves
multiple causes, record the candidates and the missing discriminating evidence
and keep attribution open. A status update or 401 alone cannot name a caller
or reveal a provider-side rule.

### 5.2 Close the refresh handoff window (H1)

A replacement did not exist until 22:38:36 on November 18. It reached agentd
at 22:38:58, at least 2 min 36 s after the first reported 401. Closing the H1
window could only have shortened the trailing handoff after 22:38:36. It
could neither prevent the cutoff nor shorten the outage before replacement.
A cutoff immediately after a refresh could otherwise wait almost 5 h 20 m for
the next scheduled one. Section 5.4 handles that case.

Qualify bounded reload and retry of the failed authentication request on a
newer same-account credential. Keep the one-continue fallback and account
change refusal. Keep automatic recovery at the authentication request level,
with no replay of the task or completed tools. A parent process reload can
cover an in-process subagent's next request. If the subagent already failed,
the parent decides whether to redispatch its unfinished work. Agentd's
continue prompts only the main thread. Check whether a separate Bash-launched
CLI reports `Not logged in` on 2.1.321. A separately launched CLI uses its own
credential path and needs its own qualification before claiming recovery.

Measure publication-to-file-write and file-write-to-CLI-acceptance separately.
S-1 found that `CLAUDE_CODE_OAUTH_401_WAIT_MS` saw rotation through the
environment or file-descriptor paths, with no wait for the credentials file.
Test the file path on 2.1.321 first. If it cannot cover the bounded wait,
qualify file-descriptor delivery as the first fallback. Check support for
the full-scope Remote Control login, rotation and whether shell or other
child processes inherit the descriptor. No token may enter any tool-shell
environment. Keep the bearer out of the agent environment and every
tool-shell inheritance path, including descriptors. Treat a delivery change
as an access change requiring its own docs-first design and cross-family
review. A successful credentials-file merge or Remote Control registration
gives no proof that an in-flight request loaded the replacement.

### 5.3 Prevent the initiating action

Start with the access-token holders and mechanisms compatible with the
observed shape. The order is a working priority based on likelihood and
the evidence available to distinguish candidates. No actor has been
identified for November 18.

| Priority and candidate | Evidence that would settle it | Guard or change if established |
| --- | --- | --- |
| 1. Logout or explicit revoke by a projection pod or another user of the shared access token (H2) | Inspect the 2.1.321 logout paths offline for access-only revocation. Have the authorized owners return only the action names and UTC times for `claude auth logout`, `/logout`, or an equivalent lifecycle action in the bracket. Cover terminal input, `agent-run msg`, Remote Control and external holders such as v1 and the Mac. Match the operation's scope and time to the observed cutoff. | Guard logout on projected sessions at launch and supported control inputs, including forwarded commands. Route ordinary session exit to local stop and bridge teardown. Move shared provider lifecycle operations to the credential owner. Command guards prevent accidents. If the bearer itself can revoke access, use a qualified transport that withholds the revocation-capable bearer from consumers or independently revocable keeper-owned grants. |
| 2. Other principals with Secret-read rights (H2) | Inventory RBAC metadata for direct reads and inherited or broad grants on the live projection. List projection-mode AgentSessions Running from 22:28 to 22:37 UTC by name, mode and lifecycle times in the restricted investigation. Match any authorized metadata-only audit of reads and lifecycle actions. Read access alone proves opportunity. | Remove unnecessary read grants and narrow publisher and consumer delivery paths to named duties. For required consumers, use the bearer isolation in row 1 if access alone can revoke. |
| 3. Provider-side invalidation, including a policy on concurrent clients (H6b) | Correlate fleet-wide first rejections per generation with active consumer counts and keeper refresh times. Rejections outside handoff windows with no holder action prioritize provider review. Obtain provider confirmation or reproduce with an approved separate-account qualification to establish the rule. | Use the provider-supported concurrency limit, cap clients per grant or qualify independent grants under the keeper. Seek a provider remedy for an unrelated account event or invalidation bug. |
| 4. Server-side Remote Control actions during containment (H2/H6b) | Ask Tom and Meatball for the 22:20–22:40 UTC sanitized action list, including ending or archiving entries from phone, claude.ai or the Mac. Inspect how 2.1.321 handles a server-closed bridge. Compare action type and time to the bracket and check for provider-side effects. | Use lifecycle controls that end or archive individual bridges without touching account auth. Stop using a revoking control in containment until a supported bridge-only route or qualified grant isolation is available. |
| 5. SIGTERM teardown in the four suspended CLIs (H2/H6c) | Inspect the pinned 2.1.321 binary offline for a revoke request on exit or archive. S-6 covered 2.1.292. The new session's first success after all four suspensions weakens immediate revocation. Compare the refined bracket against any established provider propagation delay. | Use a qualified non-revoking shutdown or pin a version with that behavior. If upstream couples exit to provider revocation, qualify a bridge-only session stop before rollout. |
| 6. The affected session's first start (H2/H6b) | Check offline whether profile and feature-flag reads or Remote Control registration can initiate revocation. Compare startup and any delayed effect to accepted requests after startup and the first 401. Requests succeeded for minutes after launch. | Remove or replace a revoking startup operation and qualify a start that preserves other consumers' access. |
| 7. CLI self-refresh on a 401 or another process holding refresh material (H3) | Inspect whether 2.1.321 refreshes on access-only 401. S-1 found no such refresh in 2.1.292. Audit environment key names only across profiles: `unsetForRemote` strips the refresh key, while `unsetForAgent` does not. Check authorized holder inventories for v1, the Mac, keepers or tools, and compare refresh rotation and outcome evidence in the bracket. | Strip refresh material from all non-keeper paths and remove any extra refresh owner through the credential owner's authorized process. Retain the keeper's existing one-shot journal and fencing. |
| 8. Independent login or setup-token on the same account (H4) | Compare a sanitized existing ceremony audit to the bracket. Establish cross-grant behavior through provider evidence or Tom-approved qualification under §6. | Keep additional ceremonies off the shared account until independently revocable grants have been qualified. Use a separate account for test or administrative sign-in where appropriate. |

The first evidence packet is the holder inventory, the pinned binary's
exit, logout, bridge and 401 behavior, and existing sanitized actions from the
owners. Keep subject identifiers in restricted evidence and use only action
kind and time in a public report. An authorized owner may use the extraction
in slice 0 to tighten rejection timing. An entry without an error flag does
not establish provider acceptance. Obtain the lower bound from independent
accepted-request evidence or leave it at the known startup bracket.
NetworkPolicy cannot enforce an HTTPS URL-path restriction. Any transport or
grant-isolation proposal needs its own design and proof.
[Existing accepted-response check](../../../../internal/agentd/claude_continue.go#L103-L130).

### 5.4 Keeper-only early revocation recovery

**Q-927-01, 2026-11-18.** The coordinator recorded Tom's
phone answer in OP927 comment 9257. Asked whether the keeper should check its
own token and refresh once promptly if it was cut off, while remaining the
sole refresher, Tom gave this answer.

> Yes but also we should prevent tokens from getting cut off in the first place and have a way to understand the bugs that cause them to get cut off

The ruling calls for keeper-only early recovery, prevention by removing the
attributed cause (§5.3), and evidence that lets an investigator explain the
next cutoff (§5.1). This ruling determines the design direction. Live checks
and rollout have separate authorization.

Have the elected keeper verify its own current, unexpired Ready access token
on a fixed cadence with the same read-only profile GET used at adoption.
Slice 6 defaults to a one-minute probe cadence and a five-minute minimum
interval between early attempts across generations. The minimum interval
also bounds the first verification of a usable replacement. Both durations
are configurable. The feature defaults off.
Validate the values against provider limits and the recovery bound before
live enablement. Classify HTTP 401 separately from the transport's current
generic profile error. A 403, timeout, 429, 5xx, malformed profile or account
mismatch yields a sanitized diagnostic and cannot qualify an early refresh.
A successful GET verifies profile
access and does not verify consumer inference. A consumer report never
changes the cadence, triggers a probe or authorizes a refresh.
[Adoption GET](../../../../internal/keeper/claude_control.go#L282-L289),
[profile response handling](../../../../internal/keeper/claude_transport.go#L244-L262).

A keeper-observed 401 qualifies at most one early refresh for the expected
login incarnation and generation. Re-read the Ready record under the worker's
existing serialization and leadership fence. If adoption, scheduled refresh
or any newer generation has won, discard the old probe result. Commit the
early-attempt latch in a separate, versioned journal Secret key in the same
CAS as the existing RefreshIntent and unsent marker. Persist the last early
attempt and its cooldown across generations and leader restarts. Reuse the
existing send fence, dispatch commit, replacement CAS and projection publish
path. Scheduled and early work share a single dispatch path. A proved-unsent
rollback keeps the early latch consumed for that generation. If the
replacement was saved but its publication failed, retry only publication
through the existing path.

Allow one early attempt per unresolved cutoff. If the succeeding generation
is rejected before it has verified healthy through the minimum interval,
hold further early refreshes and page Tom. The floor alone must never allow
a second dispatch for the same generation or re-arm a flapping login. A
healthy replacement verified through the interval closes a successful
recovery. A flapping hold requires Tom's decision after attribution or a
separately authorized keeper login. Each early refresh can create another
H1 window. Routine scheduling keeps its existing eligibility rules and never
replays a send that might have spent its refresh token.

If the family was revoked and the refresh credential is rejected, preserve
today's tombstone and `NeedsLogin` path. Preserve the same path for an
ambiguous spent-token attempt and the existing rollback for a proved-unsent
attempt. An early attempt that also fails, a replacement that cannot be made
usable within the qualified recovery bound, or a flapping hold pages Tom once
per unresolved incident via the keeper paging path where it supports that
incident. Include UTC time, generation, fixed failure class and the incident
reference. Slice 6 stores the page record and its undelivered status in the
private keeper journal. The existing login notifier pages for `NeedsLogin`
and lapses, and the raise-notice activation remains held. No automatic login
ceremony follows.
[Refresh and intent handling](../../../../internal/keeper/claude_worker.go#L149-L225),
[NeedsLogin](../../../../internal/keeper/claude_worker.go#L415-L432).

## 6. Implementation plan and acceptance

Take the slices in the order below. Each needs its own bounded implementation
or investigation story.
Review, source tests, installation and Tom's usable acceptance are separate
gates. The following tests are acceptance for the future slices. Run one
suite at a time on two CPUs under `nice -n 19`.

| Slice | Work and dependencies | Test or evidence | Tom's approval |
| --- | --- | --- | --- |
| 0. Incident evidence | Inventory projection holders and RBAC metadata. Read the 2.1.321 binary offline for exit, logout, bridge teardown and 401 handling. Collect the sanitized action list. | No code test. Update the design with the inventory, classified hypotheses, bracket evidence and dated rulings. | **Required for transcript extraction.** Before the owner reads the affected transcript, get Tom's explicit yes to a fixed-field filter limited to UTC timestamps, `isSidechain`, `isApiErrorMessage` and `apiErrorStatus`, with no text or raw output. Use it for rejection timing. If declined or pending, continue other evidence work and use slice 1 for future brackets. |
| 1. Agentd auth history | Add the new bounded file in §5.1. Preserve resolved observations. | Synthetic main-chain 401, sidechain-only 401 followed by a successful parent turn, accepted-then-401 bracket, receipt written but credential write failed, newer generation retaining the prior entry, replay deduplication and the three-generation cap. Golden fixtures cover the unchanged receipt and projection codecs. Verify the new record excludes fingerprints and token-length fields. | Synthetic fixtures. |
| 2. AgentSession status | Source built in [OP937](https://work.haynesnetwork.com/work_packages/937). Heartbeat copies the bounded history into `status.claudeAuthHistory` and records when it received a usable snapshot in `status.claudeAuthHistoryReportedAt`. `agent-run show --claude-auth-history -o json` requests both fields and requires the operator to acknowledge the view. Installation requires the haynes-ops CRD copy before the operator pin. | Validation accepts in-range data and refuses oversize or invalid data. Envtest covers persistence, report freshness and old agentd heartbeat compatibility. Older ordinary and strict CLI fixtures decode the legacy view from the new operator. A new client sees the bounded history and refuses the view against an older operator. | Source and fixture checks. |
| 3. Keeper provenance | Add the separate journal Secret key and counters in §5.1. Keep projection documents compatible and record rotation and refresh-expiry presence. | Fake-clock worker cases for adoption, refresh, publication and incomplete observations. Golden bytes preserve `access.json` and `login.json`. An older agentd consumes the projection with the keeper metadata present. The new key strict-decodes only bounded times, booleans and integers. | Source and fixture checks. |
| 4. CLI 2.1.321 qualification | Qualify on a scratch home with access-only credentials using the S-1 method. Check the file reload and 401 wait, FD rotation and Remote Control eligibility, child descriptor inheritance, an in-process subagent's next request after parent reload, and the source seen by a separate Bash-launched CLI. Check whether local profiles combine a static CLI token with a live projection. | A verification record identifies the pinned version, observations and delivery path qualified for slice 5. Include a pre-refresh control for any revocation test. | **Required before the scratch-home CLI check.** Apply the account and lifecycle gates below. |
| 5. H1 handoff | After slice 4, use the qualified wait and file delivery, or a separately reviewed FD delivery design. Shorten publication-to-write delay. | Fake-clock tests cover the old request still in flight after merge, CLI write-back of old `claudeAiOauth` during an `mcpOAuth` update, preserving CLI-owned data, account-change refusal, the local static-source guard and at most one continue. Use §5.1 to compare routine-refresh windows after installation. | Installed acceptance is gated below. |
| 6. Early recovery | Source implementation in [OP938](https://work.haynesnetwork.com/work_packages/938) under Q-927-01. The keeper's probe and recovery are off by default. Early evidence and undelivered incident records use the private journal's `early-recovery.json` v1 key. | Fake provider verifies that own-token 401 causes one dispatch. Exercise stale probes and scheduled races, non-401 errors, CAS and restart, proved-unsent rollback, family-wide revocation, ambiguous send, publish-only recovery, repeat cutoff reaching the floor and one sanitized page. A structural test shows no consumer input reaches the refresh decision. Existing intent, unsent-marker and fence tests pass. | Q-927-01 settles the design choice. Live enablement and installed acceptance have separate authorization. |
| 7. Attributed prevention | Use the established cause from slices 0–3 to select the control in §5.3. Qualify any access or transport change before delivery. | Show the attributed action cannot cut off another authorized consumer with the guard in place. Show ordinary inference, bridge lifecycle and keeper rotation still work. Leave this slice pending when attribution or authorized proof is missing. | Explicit approval for any live test that exercises a login, logout, revoke or comparable lifecycle action. |

Use a separately authorized account for any test login or installed
qualification by default. A scratch home isolates files and still uses
whichever account owns the grant. A disposable login or setup-token on Tom's
account requires his explicit approval for that exact ceremony and prior
qualification of H4. Production shared credentials must never be used for a
logout or revocation test. Installed H1 acceptance observes a scheduled
refresh, exact generation loading and successful requests from two existing
processes. Ordinary production refresh observations can establish normal
handoff latency. Prove revocation isolation and early recovery only under a
separate acceptance story with authorized test credentials and Tom's approval
for the specific lifecycle action.

Slice 1 has no off switch. Roll back the agent image pin. The older history exporter refuses volumes containing the new history files until roll-forward.
Roll back slice 2 by reverting the operator pin. The older operator ignores
history in heartbeats, so liveness continues while the stored history and
report time freeze. A full status update from the older operator can clear
both fields. Rolling back the CRD prunes both fields. For a later history
wire field, install the updated CRD and operator before the agent image.
An older operator strictly decodes the snapshot and drops reports with
unknown fields, leaving the last usable snapshot and its report time in place.
Roll back slice 3 by reverting the keeper image pin. Older keepers ignore and
preserve `refresh-provenance.json`.
Roll back slice 6 by disabling `--enable-claude-early-recovery`. A keeper
image rollback also preserves the separate `early-recovery.json` v1 key.
The older keeper leaves that key in place on adoption, scheduled refresh
and publication, so its early rows and cooldown survive roll-forward. Its
publication of a saved early replacement may leave the original publication
time unknown. Early rows use operation code 3 only in the new key. Slice 3's
`refresh-provenance.json` stays v1, and older keepers continue to append its
ordinary rows. Preserve in-flight intent handling and the current refresh
credential through rollback. Do not restart running sessions as a proof step.

The design remains Proposed. The table records source delivery by slice.
Installation and usable acceptance require their own evidence.

## 7. Investigation boundary and stopped path

No Secret, credentials file, raw transcript, task log or provider token was
read. No provider auth request, login, logout, refresh, setup-token, session
launch or Kubernetes/operator mutation was run. Local Git, report files and
the requested PR/tracker handoff are the only writes in scope.

One attempted keeper-only Loki range (October 19–November 18 22:50) was refused
because 742 h 50 m exceeds the server's 30 d 1 h query limit. That query path
was stopped without another route. The successful earlier daily queries cover
October 20–November 18. The exact provider issue time, the last accepted
request time, any alternate loaded credential source, Mac/v1 lifecycle
activity, the account-wide login audit, clock skew and complete consumer
rejection history remain unverified.
