# Original Codex rule probe — 2026-10-12 UTC

This is the next bounded #173 acceptance unit after the original Claude probe
returned its subscription weekly-limit response. It uses the existing trusted
Codex record; it does not choose native fleet UX or create a paired computer.

## Contract

Run one ordinary authorized fresh Directory read. Require a complete page with
exactly the original Claude Session UID
`45a614cb-f537-441e-a5c1-33b66a285521`, its fixed name and trusted metadata, plus
`current.sourceState=Present`. The existing Directory handler checks the live
source UID/spec and confirmed receipt against returned Directory name/UID and
spec digest. The pre-Create page establishes its observed cardinality and
Directory UID. Directory generation is not exposed, and post-Create cardinality
remains Unknown. Empty, extra, malformed, replaced or paginated records
refuse execution. No original record is deleted to produce an empty page.

Only that validated page may permit the conditional first trusted Codex Create
within 300 seconds of actual capture finish. The new per-ID permanent fence is
written exclusively and fsynced before the first Create CLI/POST. The final
pre-spawn check also enforces freshness and Root expiry. Original HTTP 201 is
required; HTTP 200 replay, ambiguity, expiration and errors cannot rearm the ID,
replace a Session or permit retries/followups. Machine validation is explicitly
not a native review verdict. Before this execution the counters were Directory
4, Claude Create 1, Claude named/result reads 1 each and Codex Create 0.

Fixed Codex policy is `rule-canary-codex-1011`, source name
`private-canary-3bfe28755095977c9462c269cc24dee3`, provider `codex`, requested model
`gpt-6-astra`, effort `max`, maxTurns absent or zero and timeout 180 seconds.
The configured private Pod keeps its 600-second deadline, two-CPU/four-GiB bound,
private HOME and existing v2.20 image/template pin. Requested model is not served
model proof. Pre-Create subscription credential/quota health remains Unknown;
the existing five-minute Codex token projection guard runs after Create.
No helper reads or copies credential/enrollment material, writes provider HOME,
pairs a computer or permits paid API fallback.

## Affected paths and gates

The new local preparation is
`/home/dev/work/orders/dev-env-rule-canary-runtime-1011/sequenced-initial-codex-preparation-r1/`:
one Codex-specific sequence helper, closed contract, explicit frozen inputs,
inert plan and offline validation receipts. It reuses accepted Directory capture,
caller, source, CA and preservation helpers plus bound D22/D23 clients. The old
Claude-only Create adapter cannot be reused unchanged: the new adapter enforces
Codex provider/model/effort and absent-or-zero maxTurns. Predecessor helpers and
failed receipts remain immutable. No repository/API implementation, permissions,
running flags, image pins, credentials, enrollment or provider homes change.

Offline contract audit `1ed68f8f` binds the source route and its limits. Preparation
requires a pre-test freeze, one short meaningful fake campaign without live
clients/providers, independent native full review and a separate Root-bound
one-program authorization. Only then may the one Directory read and conditional
initial Create run. Saved results require independent review; named provenance
and retained output need their own later one-read gates. No helper can chain
these calls automatically. No CPU burners, stress, wide/looped tests or local
image builds are allowed.

## Current acceptance

Preparation candidate `7bc9fc6a` and explicit manifest `e8e180d3` bind helper
`19aa5f0a`, pre-test freeze `1999c189` and validation receipt `55b37cde`. All 16
offline fake checks passed in one campaign, with no live clients, network,
provider, credential or predecessor-definition calls. The inert proposal remains
preserved; Root's executable-plan data `005790f9` changes only its `kind` and
grants no authority. Independent full review `55e27946` and six-field native
gate `af7c3bdf` passed with no findings. Root's separate one-program authority
`2d9170d6` preceded the one actual execution `75e768cd` at 06:47:25 UTC.

Directory attempt 5 returned a complete page containing exactly the known
Claude source UID and trusted immutable metadata with source Present. The
observed Directory UID is `c7b87417-f221-4f8c-9de1-a86688979244`, list resource
version `824866074`; source generation is 1 and Directory generation is not
exposed. Capture finished at 06:47:25.380844 UTC. The permanent Codex fence was
fsynced at .442186, before Create capture at .449170. Create finished at .575084,
within 0.194240 seconds of Directory finish and the unchanged freshness window.
The original legacy nonempty-page HOLD remains saved separately; the new
machine predicate does not fabricate a native page review.

The 653-byte Create envelope `55d37538` returned original HTTP 201, UID
`f0396ab3-1abf-41bf-be4e-93ce0e43608b`, fixed Codex name, parent `dev/dev-env`,
requested `gpt-6-astra`/`max`, timeout `3m0s`, absent maxTurns and phase Pending.
Native saved review `c3bcc13b` and Root receipt acceptance `f189f079` accept
transport/source binding and original identity only. The actual executor UID,
provider execution, served model, output, loaded rules, quota, history, stop,
usage/cost and post-Create directory cardinality remain Unknown. HTTP wire
headers/counts and authentication discovery counts were not captured.
Current counters are Directory 5, Claude Create 1, Codex Create 1, Claude named
1/result 1 and Codex named 0/result 0. Both initial IDs and this sequence
authority are consumed; no repost, replacement, rearm or followup read follows.

The original Claude ID and both read authorities remain consumed permanently.
Its stored weekly-limit response, failed task, preserved volume and uncertain
stop/history receipts are retained. Common/repository instruction loading was
not established; quota is not evidence that instructions were absent.

## Q-27: one final reader-preparation correction

The two offline Codex reader drafts were saved under
`/home/dev/work/orders/dev-env-rule-canary-runtime-1011/codex-post-original-read-preparation-r1/`
but remain untested and unreviewed. Named helper `6efcf4ec` and result helper
`80560088` adapt the existing Claude methods; actual dynamic provenance gates
remain null. Three attempts to index historical evidence stopped preparation
under the owner's logical-task limit. Receipt `dae50690` preserves all attempts;
correction `0b6eeed4` establishes that the third attempt hit the 32 MiB file cap
on a 41,412,165-byte historical receipt. A 512-reference crossing was not proved.
No oversized content, helper, fake campaign, live API or provider was run by
that preparation.

The native owner question asks whether to allow one final offline correction
using a fixed list of already accepted evidence, then tests and independent
review before any separately gated result read. Pending question receipt
`9a3a7a1d` grants no fourth attempt or runtime authority. Failed drafts, counters
and the original remain preserved. A stopped-context read and actual rule
acceptance cannot be inferred from the successful Create.

**Ruling, 2026-10-12.** Tom answered Q-27 through the Claude native prompt:
"Fix once, then read (Recommended)". The ruling allows one offline correction of
the reader preparation. The correction uses a fixed list of already accepted
evidence, focused tests and independent review. After that, the ruling allows
exactly one named read and one result read of the Codex original. There are no
retries and no counter reset. The pending text above is the original record and
stays as written; receipt `9a3a7a1d`, the failed drafts and the counters stay
preserved. This entry records the ruling only; it does not run the correction or
either read.

The answer is retained in
`/home/dev/work/orders/dev-env-rule-canary-runtime-1011/root-q27-owner-answer-r1.json`
(SHA256 `eaf4e6774c0c42bee210371dfa01d7c70f37c2f8d95f5ae42c8becd04106aeb9`). That
receipt binds pending receipt `9a3a7a1d` by its full hash. The question Claude
sent read:

> Q-27: Codex's one-time rules test (rule-canary-codex-1011) finished about 8
> hours ago, but nobody has read its result. Codex's prep script for that read
> failed three times on its own evidence bookkeeping and stopped. May I fix that
> prep once (with a short fixed list of already-accepted evidence), have it
> independently reviewed, and then do the single named read and single result
> read it was built for?

The options were "Fix once, then read (Recommended)", "Fix once, ask before
reading" and "Stop the reader work". The receipt names the original as Session
`private-canary-3bfe28755095977c9462c269cc24dee3` (UID
`f0396ab3-1abf-41bf-be4e-93ce0e43608b`). The receipt grants no new Create, retry,
counter reset or widened cap. It also grants no second correction, no retry of a
failed read, no new canary, no credential or auth repair and no cleanup of the
original.

Global private-project admission remains disabled. Actual both-provider rules,
fresh private GitHub worktrees, claims/inbox, authorized full stopped native
history, private dispatch/recovery, exact served-model/usage/cost provenance and
the concurrent owner pilot remain unaccepted. Q-24 still gates native Codex
isolation and fleet UX: pinned native chats share an app-server harness, and
separate tool executors do not establish full native isolation. The local-model
contract remains included; live inference waits for GPU readiness and household
reservations. Source, signed publication, deployment and operational acceptance
remain separate. Claude advisory is optional/down; required CI and native review
remain shipping gates. Existing sessions and the current dev-env pod are not
restarted by this local acceptance preparation.
