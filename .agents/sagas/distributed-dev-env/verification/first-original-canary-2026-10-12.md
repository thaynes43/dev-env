# First original Claude canary — 2026-10-12 UTC

This record supersedes the zero-Create state in earlier checkpoints. It records
an original receipt, not provider, rules, history or MVP acceptance.

## Contract and result

The reviewed local sequencer runs one ordinary authorized Directory read and,
only for a complete empty page, one initial trusted fixed-ID Create in the same
process. It reuses the accepted catalog/canary client and preservation helpers.
Freshness remains 300 seconds from the actual Directory capture finish; neither
validation nor a review timestamp extends it. The permanent O_EXCL, fsynced key
fence precedes Create. Expiration, errors or ambiguous receipt consume their
recorded attempts without retry, replacement, rearm or cleanup. The sequencer
cannot create a native review verdict or authorize followup reads.

At 05:28:22 UTC, logical Directory attempt 4 returned a complete empty page with
resourceVersion `824748447`. The first Create completed 0.272948 seconds after
that capture finished and returned original HTTP 201, `CreatedOriginalSession`:

- Fixed ID: `rule-canary-claude-1011`.
- Session: `dev-agents/private-canary-fc5935c175c6fc15f842b051bf85aa8d`.
- Original UID: `45a614cb-f537-441e-a5c1-33b66a285521`.
- Parent: `dev/dev-env`; createdAt: `2026-10-12T05:28:22Z`.

Independent full sequencer preparation review `de0e96bf`, closed native gate
`f709dac5`, Root authorization `636fecf7`, actual outer result `d33b1b32`, raw
Create envelope `7ed38d46`, permanent fence `00f8aba5` and independent saved
review `fd25b915` are preserved under
`/home/dev/work/orders/dev-env-rule-canary-runtime-1011/`. Root receipt acceptance
`2d283d67` accepts only the original client receipt identity. The receipt's
Pending phase and Running desired operating mode do not establish an executor
or successful provider turn. Four Directory invocations include one retained
TLS failure and three complete successful pages; two earlier successful windows
expired before Create. Exactly one Create has run. No counter resets occurred.

The Claude fixed ID is consumed permanently. The fixed Codex ID remains
unstarted. A second-provider preparation must account for this exact existing
original Session rather than require or manufacture an empty directory.

## Followup acceptance

The single named Session read completed at 05:48:15 UTC. Independent saved
review `0a699335` and exact native binding `b601a7c7` accept the original retained
executor metadata: Pod UID `116efa31-11e3-41ad-962d-5d815e57a08c`, generation 1,
template revision `2.20.0-ec26880d2a`. They derive ordinal-zero invocation
`d1b06ba27ceaed961fda7ab31e30f5b8f194fbfee7de3504233ad0e028142ad1`
and context
`published-context.35c68aedf2e0aef291807b69fd25a838.6905e5355f40abc1f4346f3d234e4512`.
This accepts saved metadata, not an additional Pod observation or publication
existence. The Session reports Failed, an exited task with exit code 1,
`isError=true`, one turn and finish at 05:28:53 UTC. Its later PodEnded condition
reports DeadlineExceeded with the volume kept. Physical stop proof and underlying
failure cause remain Unknown. The task's embedded agentd source is `3aab27ce`,
matching v2.20; the template revision suffix is not a dev-env source commit.
No native history completeness or provider/rules success is inferred.

One Root result-plan data preparation stopped at an incorrect argument-position
assertion before writing a final plan or running a helper, client or GET. The
valid already-written Root binding was retained unchanged. A corrected plan
binds the actual null value after each unique flag; the failure receipt and all
absence evidence remain preserved. Native final gate `e103ac7f` and separate
Root one-read authorization `1b4d1d50` passed before the one result GET.

The separately accepted named reader bound the exact original Session's retained
executor UID and derived ordinal-zero context identity. Independent saved
discovery and a separate Root authority permitted the one fixed result GET.
Both named-read and result-read authorities are consumed. No further named
Session GET or result GET is authorized. Missing executor or publication evidence is a
refusal, not permission for another Create, a different identity, polling,
logs, provider-HOME reads or cleanup. The prompt asks for markers already loaded
in instructions; marker values must never be injected into the prompt or helper
inputs. Actual common and repository rule output must come from the retained
result. Full native history, stopped-context, claims/inbox, private dispatch,
exact served-model/cost provenance and concurrent owner-pilot acceptance remain
separate gates.

The operator API has no pre-Create provider credential-health predicate.
Configuration is not a readiness proof: Claude launch requires a nonempty plan
OAuth credential and refuses metered-key fallback; Codex expiry validation occurs
after Session creation. Root audit `9991cdcc` records this source limit.
Credential health remains Unknown until evidenced by the actual attempt. There
is no paid API substitution or new secret/enrollment-copy route.

The one fixed result read completed at 05:56:48 UTC. Its saved candidate
`2bc538e6` binds context UID `bef6e3e5-46a4-4fb7-941e-47b840a21387`, the original
Session UID and retained executor actor, exact catalog/rules revisions and
49 UTF-8 payload bytes/hash `a57a7318`. The raw result envelope is `c867fd75`;
Root execution is `92c79838`. The retained text is exactly:

> You've hit your weekly limit · resets 12pm (UTC)

No common or repository marker appears. This does not establish that instructions
were absent: the quota response prevented acceptance. Actual Claude rule loading
remains unaccepted, and no quota retry, new identity, credential repair, model
fallback or paid API substitution follows. The provider-reported reset time is
not a promise of readiness. Completeness, capture ACK, physical stop, native
history, served model, measured usage and cost remain Unknown. Independent
saved review `c3ad5658` passed transport, original identity and source assertions;
Root saved-result acceptance `54f89e02` accepts those separately from
`FAIL_EXPECTED_RULE_OUTPUT`. No wire-header/raw-body equality is inferred from
the CLI envelope. Counters are Directory 4, Create 1, named read 1 and result
read 1, with zero automatic retries or resets.

Offline failure-surface audit `56960a7a` distinguishes the task's early exit from
the later Pod deadline. The existing ordinary DTO reports the bounded failure
summary, but publication failures have no typed producer-error status field;
missing publication alone would be nondiagnostic. The ended-Pod log route is
unavailable because it requires a Running, Ready owned Pod and exec. The saved
DTO and fixed retained-result receipt are the bounded existing evidence; there
was no fallback log, HOME, credential or physical Pod read.

## Shipping and preservation

Source [PR #213](https://github.com/thaynes43/dev-env/pull/213) merged as
`b3aa4d93` after focused meaningful tests, required CI and independent native
review. Release [PR #214](https://github.com/thaynes43/dev-env/pull/214) merged as
`78879c4a`; [v2.24.0](https://github.com/thaynes43/dev-env/releases/tag/v2.24.0)
was published at 05:23:08 UTC. Hosted build/sign/verify run `38207702413`, job
`114674760795`, and native audit `a7264186` support Root signed-publication
acceptance `52cfff17` of image
`ghcr.io/thaynes43/dev-env:2.24.0@sha256:36d26662ed9e3a61b88a6c7f4fd841295358fb072018853df79e19585385367c`.
The exact tag identity and GitHub OIDC issuer were verified in hosted evidence;
there was no local image build or independent registry reread. The image is not
deployed. The private-budget ownership inspector is unwired and always refuses
reservation. This source increment does not enable dispatch,
change a running workload or require a GitOps deployment. Private-project
admission remains disabled; #154 and #160 remain open.

Accepted operator delivery remains haynes-ops #3825 / Root `f610a2d6`, whose
saved upgrade observation preserved all 11 existing peers. Task/template pins
remain v2.20. v2.23 signed agent publication is accepted separately and is not
deployed. No current dev-env pod restart or v1 cutover occurred. Claude advisory
reviews are optional during the outage; available error-only or skipped runs
were read and retained without retry or authentication repair. Required CI and
native independent review remain shipping gates. Q-26's one allowed correction
was completed by R4; this canary sequence is a separate bounded unit. Q-24's
native Codex routing decision remains pending; separate executor pods do not
prove isolation of the shared native app-server harness.

## Affected paths and limits

This checkpoint changes only `.agents/HANDOFF.md`, this verification record and
the saga README. Local runtime preparations are protected receipts, not installed
executables or repository implementation changes. Failed preparations, native
fixtures, ledgers, expired-window receipts and uncertain lifecycle evidence are
retained. Parent issues #91, #173 and #174 remain open; source tests and an
original 201 receipt do not establish a working multi-provider MVP.
