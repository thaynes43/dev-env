# DESIGN-069: Diagnose and recover shared Claude token invalidation

- **Status:** Proposed. Investigation and design only; no auth operation, code,
  build, merge or installation is authorized by this document.
- **Date:** 2026-11-18.
- **Tracking:** [OP927](https://work.haynesnetwork.com/work_packages/927), epic
  #91, distributed dev-env saga. Implementation and installed acceptance need
  separate stories after the migration coordinator's review in another model family.
- **Source pin:** `e913a490dcf328adaa62cfc2dde5a4e717280ec9`, freshly fetched
  from main. The observed keeper uses source `1ae9c7ca2c0595157ab93bab74ff8640b3896ad7`;
  its Claude worker, transport and journal files are identical at both pins.
- **Ownership:** the migration coordinator owns OP927 and acceptance. This
  investigation owns this document and its report, not provider credentials.

## 1. Finding and limits

There are two questions. Routine refresh is a documented source of fleet-wide
access-token invalidation: the October 6 S-1 experiment observed the previous
token rejected after refresh, and a successful next request after merging the
replacement. V2 distributes one access token to many independently reloading
processes, so each refresh creates a failure window. A single refresh owner
prevents refresh-token races; it does not eliminate this access-token window.
See [S-1](../backlog/00-spikes.md#s-1-claude-on-an-access-token-only-credential)
and [plan 03](../backlog/03-remote-control.md#scope).

The November 18 failure precedes the next keeper refresh by over two minutes.
That refresh cannot have revoked the token before the failure. It could explain
an old token only if the failing process still held an earlier generation; the
process's actual credential generation was not captured. Neither a Mac client
nor Meatball is identified as the actor by this evidence. Do not turn temporal
proximity to session suspension into an attribution.

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
| 2026-11-18 approximately 22:35:22–22:36:22 | A subagent request received 401, token revoked. | Coordinator's OP926 comment 9239 / work order: output-file mtime 22:36:22, possibly up to one minute late. Transcript and file were not read here. |
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
lifetimes to the same `now` when a refresh expiry is returned. If that field is
absent and the refresh token does not rotate, it retains the previous lapse.
A rotated token without a stated expiry instead makes the lapse unknown.
This field is an expiry estimate, not a login-ceremony identity or timestamp.
An unchanged lapse cannot prove that no other login happened, and matching
nanoseconds do not prove a new login. See
[journal](../../../../internal/keeper/claude_journal.go).

## 3. Hypotheses

| Hypothesis | Classification | Evidence and limit |
| --- | --- | --- |
| H1: routine keeper refresh invalidates the old access token while consumers reload | **CONFIRMED** as a historical mechanism; **RULED OUT** for generation 128 causing this earlier 401 | S-1 observed the mechanism. Keeper refreshed at 22:38:36, after the failure. An older cached generation remains **UNKNOWN** because the failed process's token generation is unavailable. |
| H2: a holder logged out or otherwise revoked the shared access token | **UNKNOWN** | No actor/action audit was available. Checked `stopAgent`, daemon shutdown and rescue paths signal the CLI; they contain no Claude logout/revoke call. CLI internals, hooks, v1 and Mac activity were not observed. Public docs document logout but do not establish the exact access-only revocation scope. |
| H3: another holder refreshed the keeper's refresh token | **UNKNOWN**, weakened for this event | Source excludes refresh material from the projection, refuses pod-owned refresh credentials, serializes the keeper, journals dispatch and fences leadership. Keeper refreshed successfully afterwards without NeedsLogin. These facts argue against a family-wide revocation, but do not inventory external holders or prove provider behavior. |
| H4: a separate login/setup-token on the same account revoked earlier tokens | **UNKNOWN** | No ceremony audit or provider guarantee about cross-login revocation was obtained. Absence of local LoginInProgress is not an account-wide audit. |
| H5: the keeper refreshed earlier than assumed | **RULED OUT** for generation 128 at the failure time, within the recorded clock/timestamp assumptions | Fixed keeper lines and bounded request agree with expiry arithmetic. Previous refresh was at 17:18:32. Clock skew was not measured; the 401 time is coordinator-supplied metadata. |
| H6a: stale CLI/subagent credentials or a different credential source | **UNKNOWN** | A merged file is not evidence of which token a running request used. Source strips static OAuth env vars in remote mode, while task/local mode intentionally uses a static token. Native children require separate attribution. |
| H6b: provider-side early invalidation or an account event | **UNKNOWN** | A 401 is an observation of rejection, not proof of its initiating action. Public error documentation lists several rejection causes. No provider audit was available. |
| H6c: suspend itself explicitly calls logout in dev-env | **RULED OUT** in the checked source paths | Stop/shutdown forward signals and rescue preserves work. Historical S-6 distinguishes RC-entry archival at exit from auth revocation. Upstream exit hooks remain outside this finding. |

The source paths cited for these limits are
[projection merge](../../../../internal/agentd/claude_auth.go),
[launch credential modes](../../../../internal/agentd/launch.go),
[stop](../../../../internal/agentd/stop.go),
[shutdown](../../../../internal/agentd/daemon.go),
[rescue](../../../../internal/agentd/rescue.go), and
[S-6](../backlog/00-spikes.md#s-6-remote-control-on-pod-owned-credentials).

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
**unknown** with the permitted telemetry; at least the one supplied November 18
failure is known. No session logs were searched to manufacture a count.

V2 makes a documented refresh invalidation affect independently reloading
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

### 5.1 Capture enough evidence to choose the prevention change

Add a bounded metadata record to the existing keeper and consumer paths. The
keeper records generation, operation kind (`scheduled-refresh` or `adopt`),
dispatch time, response-received time, publication time, access lifetime and
expiry provenance. Record a login incarnation identifier generated locally at
adoption; never derive it from a token. Keep the existing journal/fence/CAS
semantics. Export counters for successful refresh/adoption, failed refresh by
the existing fixed class, and consumer authentication rejection. No bearer,
token hash, refresh field, provider response, prompt or transcript text may
enter status or metrics.

Agentd records applied generation and actual successful credential-file write
time separately from the generation receipt. The current receipt is persisted
**before** credentials, so its mtime alone is not a merge-completion timestamp.
Record source kind (`projection`, `static-env`, `pod-owned`, `unknown`), launch
identity and first/last rejection times with a bounded count. A native child
must report its own launch/source metadata; the parent's latest generation is
not proof of the child's request generation. Without a supported CLI receipt,
`requestGeneration` stays unknown. Separate `applied`, `retry-delivered` and
`provider-accepted` states; the last requires independent authenticated-result
evidence, not delivery or keeper Ready.

Retain at most one record per active launch and the previous two completed
generation transitions. This is diagnostics, not a transcript store. Define
the exact status compatibility and retention contract in the implementation
story before adding API fields. Routine metrics use bounded labels; session
and generation identifiers belong in bounded status, not metric labels.

### 5.2 Close the confirmed handoff failure

Qualify the pinned CLI's supported credential-replacement mechanism for both
the main session and native children. The target is a **bounded reload and
retry of the failed authentication request** when a newer same-account
credential arrives, without replaying an agent task or its completed tools.
Keep the existing one-continue limit and account-change refusal as a fallback.
Do not assume that file merge, env mutation, a feature flag or RC registration
proves reload. S-1 found that `CLAUDE_CODE_OAUTH_401_WAIT_MS` alone did not cover
the file path. A newer release needs qualification rather than that old assumption.

Measure publication-to-file-write and file-write-to-CLI-acceptance separately.
If the supported CLI cannot wait/reload on the credentials-file path, specify
and review the credential delivery change before implementation. Simply
refreshing more often creates more invalidation opportunities. Moving the
schedule earlier cannot remove a gap that starts at refresh itself. Stop any
unproven zero-window claim at this gate.

### 5.3 Choose the incident-specific prevention after attribution

If H2 is demonstrated, remove that actor's shared-login lifecycle operation
and separate local session exit from provider sign-out. A shell wrapper or
instruction is an accidental-use guard, not a credential security boundary.
If the provider allows an inference bearer itself to invalidate the shared
login, a real containment design must remove that bearer capability from
workers or use independently revocable grants still owned by the keeper.
NetworkPolicy alone cannot enforce an HTTPS URL-path deny rule. Such a change
needs its own supported-transport design and provider qualification; this note
does not claim an existing gateway or safe concurrent-login behavior.

If H3 is demonstrated, remove the extra refresh owner using the credential
owner's authorized process; preserve existing one-shot journaling and fencing.
If H4 is demonstrated, qualify independent grants before any login separation.
If the provider revoked only an access token while the keeper login remains
usable, a future operator-authorized, expected-generation refresh-once action
may reuse the fenced keeper path. It must be explicit, coalesced and durably
bounded, and must not be triggered automatically by untrusted consumer claims.
There is no such new operation in this PR.

For this incident, the minimum next evidence is the failing child's source
kind and applied/loaded generation at failure, plus any existing, sanitized
lifecycle audit from its owner or the Mac-side coordinator. Ask through OP927;
do not inspect another pod's files or access a Mac under this story.

## 6. Future acceptance, rollback and present delivery

Source checks for a later implementation: fake-clock generation transitions;
receipt written but credential write failed; old in-flight request after merge;
new-generation request rejected before expiry; native child retaining an older
credential; account change; concurrent duplicate reports; keeper restart after
dispatch; and bounded telemetry with no credential-derived identifiers. Use
two CPUs, one suite at a time, under `nice -n 19`.

Installed acceptance must separately authorize a disposable, isolated login
and two consumers. Observe a scheduled refresh, exact generation loading and a
successful request from each existing process. Exercise lifecycle isolation
only with explicit approval for the exact action and isolated credentials;
never log out the production shared login as a test. A failed or missing proof
leaves acceptance open. Ordinary production refresh observations can establish
normal handoff latency but cannot validate destructive revocation containment.

Roll back new diagnostic/recovery behavior behind its feature switch without
changing or replaying the keeper journal. A rollback never restores an old
refresh credential. Do not restart or replace running sessions as a proof step.

This story delivers a Proposed design and a metadata-only report. CI validates
the documentation head; it does not test a fix. Another model family's read,
implementation, installation and Tom's usable acceptance remain distinct gates.

## 7. Investigation boundary and stopped path

No Secret, credentials file, raw transcript, task log or provider token was
read. No provider auth request, login, logout, refresh, setup-token, session
launch or Kubernetes/operator mutation was run. Local Git, report files and
the requested PR/tracker handoff are the only writes in scope.

One attempted keeper-only Loki range (October 19–November 18 22:50) was refused
because 742 h 50 m exceeds the server's 30 d 1 h query limit. That query path
was stopped without another route. The successful earlier daily queries cover
October 20–November 18. Exact raw provider issue time, loaded child generation,
Mac/v1 lifecycle activity, account-wide login audit, clock skew and complete
consumer rejection history were not checked. These limits remain part of the
conclusion, not evidence that an unobserved action did not happen.
