# Codex original result read under Q-27 — 2026-10-12 UTC

This record follows the [Codex probe record](codex-fixed-rule-probe-2026-10-12.md)
and Tom's Q-27 ruling, "Fix once, then read (Recommended)". It records the one
reader correction, the one named read and the one result read of the Codex
original. It accepts the two rule markers only. It is not provider success,
served-model, usage, cost, history, stop or MVP acceptance.

## Contract

Q-27 allowed one offline correction of the Codex reader preparation, using a
fixed list of already accepted evidence, focused tests and independent native
review. After that it allowed exactly one named read and one fixed result read of
Session `dev-agents/private-canary-3bfe28755095977c9462c269cc24dee3` (UID
`f0396ab3-1abf-41bf-be4e-93ce0e43608b`). It allowed no retries, no counter reset,
no new Create, no second correction and no cleanup of the original. Owner answer
receipt `eaf4e677` holds the ruling.

## Correction and gates

All receipts are under
`/home/dev/work/orders/dev-env-rule-canary-runtime-1011/codex-post-original-read-preparation-r2/`.

- Preparation result `47f67bf4` passed. It binds contract `119f225e`, a fixed
  evidence list `d65e8978` of 32 entries and frozen inputs `0953951b`.
- 23 focused tests passed with no failures or errors (`06318647`).
- Independent native review passed both plan gates before the reads: named read
  `9fa825de` and result read `60cf3dfa`. These gates reviewed the plans only.
- The r1 drafts, the three failed attempts and every earlier receipt are
  unchanged.

Before the reads, the coordinator checked that both common-rule ConfigMaps still
had the bound identity: `dev-agents/dev-env-config-claude` UID `35491776`,
resourceVersion `823889579`, and `dev-agents/dev-env-config-codex` UID
`265ca5ae`, resourceVersion `813018187`. The Session was at generation 1 with no
deletionTimestamp. The operator ran `sha-cd966d1@cc03ae82`.

## The two reads

| Read | Client | Return code | Time | Finished (UTC) | Retries |
| --- | --- | --- | --- | --- | --- |
| Named Session | [DESIGN-023](../designs/023-named-session-cli-once.md) | 0 | 0.07 s | 15:55:52 | none |
| Fixed result | [DESIGN-022](../designs/022-fixed-canary-result-reader.md) | 0 | 0.12 s | 15:56:02 | none |

The receipts call these clients D23 and D22.

The named read bound the retained executor Pod UID
`f76228d3-b104-4b21-97e8-0f8aff173591` and derived the ordinal-zero context name.
The result read returned that context:

- Name `published-context.5655bc17865934e2a874ca314532b7ff.05dd0dd393c61d4dfdc7e343bf00d0f8`,
  kind `transcript-excerpt`, ordinal 0.
- 81 bytes, content SHA256 `a2b594a3c3d882e38eddf4fb6c01d67bd3e2aa86fc323483225a55eaaf5d4976`.
  Root recomputed the hash from the saved text.
- Completeness `PartialManagedOutput`, published at 06:47:54 UTC.
- Rules revision `1cebe452…`, catalog revision `9c5cc110…`.
- Requested model `gpt-6-astra`. Observed model, usage, cost, stop state and
  initial pin state are Unknown.

The text is exactly two lines:

```text
dev-env-common-rules-20261011-4ca29d70
dev-env-repository-rules-20261011-6d807bf2
```

## What is accepted

The first line is the common marker. It sits in the bound common source,
`dev-env-config-claude` key `CLAUDE.md`. The second line is the repository
marker. It has been in this repo's `AGENTS.md`, which Codex loads, and in
`CLAUDE.md` since `d5433b3` (#196). That commit is an ancestor of the task head
`9eeb066c` the Session reports. The Root receipt names `CLAUDE.md` only. So the
Codex original loaded both the common rules and the repository rules in its
private pod.

This meets the DESIGN-011 bar: the provider output carries both markers and the
prompt carries neither. The prompt also said "Do not use tools or fetch files".
While full native history is Unknown, compliance with that line is unproven, so
a tool read of the rule files cannot be excluded.
*Update, 2026-10-12: "the DESIGN-011 bar" above means its marker condition only.
DESIGN-011's fresh source and pin, placement, private HOME and Git and worktree
conditions stay open. The paragraph above is the original record.*

Root acceptance receipt `48637874` records verdict
`ACCEPTED_CODEX_ORIGINAL_RULE_MARKERS`. It binds every receipt above by path and
SHA256 and is saved beside them as `root-codex-original-result-acceptance-r2.json`.

## Review after the reads

Root accepted the markers on its own checks. No independent review followed the
reads before that, and the Root receipt does not say so. The bound
`CONTRACT.md` ("Limits that remain") expected provider rule loading to stay
Unknown after both reads. Both saved candidates still carry state
`…_CANDIDATE_ONLY_AWAITING_INDEPENDENT_REVIEW`, with `markerAcceptance` and
`providerRulePass` Unknown. The original candidate files are kept as written.

The independent native review of this record's PR (#227) is the review after the
reads. It rechecked all 33 Root bindings, the recomputed result text, both UIDs,
the live ConfigMap identities, the marker sources, the ancestry and the
counters. It found no factual error and supports the marker acceptance; its
seven findings were documentation fixes, all made on #227. Its receipt is
`review/post-read-independent-review-r2.json`, SHA256 `6c9a5fe1`.

## What the named read also shows

The task exited 0 with subtype `success` after one turn, at 06:47:53 UTC, about
28 seconds after Create. The Session still reads phase `Failed`. Its Pod ended
later on `DeadlineExceeded` and kept its volume. The phase comes from the Pod
deadline, not the task result. That is finding history-04; a fix is proposed in
branch `agent/fix-canary-outcome-1012` (draft PR #222, unmerged when this was
written).

The Session DTO also carries provider-reported token counts for the first managed
invocation. They are source assertions. They are not accepted as charged usage
or cost.

## Still Unknown or unaccepted

- Served model.
- Usage and cost.
- Full native history.
- Physical stop.
- Capture completeness beyond the excerpt.
- Private GitHub authorization.
- A fresh task pin.
- Claude's rule loading. Its original returned a weekly-limit response, which
  neither proves nor disproves loading.

*Update, 2026-10-12: under Q-28 a new Claude canary returned both markers; see
the [Claude canary 1012 record](claude-canary-1012-2026-10-12.md). The list
above is the original record.*

## Counters

Directory 5. Claude Create 1, Codex Create 1. Claude named and result reads 1
each. Codex named and result reads 1 each. No automatic retries and no reset.
Both permanent IDs are consumed. Both Q-27 reads are consumed. No further read,
retry, Create, repost, rearm, replacement or cleanup follows from this record.
