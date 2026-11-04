---
name: worker-qa-loop
description: Run a coding worker and an independent QA or review agent in a loop until the story's acceptance checks pass on the exact head. Use when a coordinator owns a code or docs change that needs tests, review and delivery. Skip for a read-only investigation, or when one direct edit and its own test finish the job.
---

# Worker and QA loop

The coordinator owns the loop, the status record and the final verdict. The coding
worker owns its claimed files. The QA agent owns the review. Nobody accepts their
own work. Use [supervised-dispatch](../supervised-dispatch/SKILL.md) for the goal,
the status record and the stop rules, and [timer-checkin](../timer-checkin/SKILL.md)
for the check-ins. This skill grants no scope, access or approval.

- Delivery gates, review rules, model and subagent tiers, the CPU rule: [CLAUDE.md](../../../CLAUDE.md) and `~/.claude/CLAUDE.md`
- Result custody and the offline handoff checker: [coordinator result handoff](../../../docs/coordinator-result-handoff.md)

## 1. Dispatch with ACK files and disjoint claims

Give each receiver a work order with its claims, its worktree and branch, the
acceptance checks, an ACK path and a RESULT path. The receiver writes the ACK
first, atomically: receiver, accountable session, worktree, branch, base commit,
claims, `accepted` and the next milestone. Read the ACK, then check that the
worktree and branch exist and the base matches. An ACK is not work; wait for a
commit.

Claims are disjoint. Each worker writes only its named paths, in its own worktree
on its own branch. A shared file (CLAUDE.md's Layout block, generated CRDs, a
design document) has exactly one named owner. When two stories must touch the same
file, run them in order. Example from 2026-11-04: S3c (#900) merged first with the
single definitions, then S3a (#901) rebased and deleted its duplicates before
anyone called them integrated.

Pick the tier by the work: `opus-worker` for hard or user-visible work and for
reviews of risky changes, `sonnet-worker` for routine work.

## 2. RED, then GREEN

Write the test first. Run it on the unfixed code and record it failing, with the
reason (RED). Then make the change and record it passing (GREEN). A test that never
failed proves nothing. For a change you cannot make fail first, such as a refactor,
a mutation sweep shows whether the tests bite.

## 3. Mutation sweeps only on a committed, clean tree

A sweep edits source files, so it can destroy work. Run one only when:

- `git status --porcelain --untracked-files=all` is empty;
- HEAD's SHA and branch are pinned at the start and checked before each mutant;
- original bytes come from `git show <pinned-sha>:<path>`, never from `HEAD:` and
  never from the working copy;
- after each mutant, the file is restored only if it still holds the mutant bytes;
  anything else stops the sweep without writing;
- the sweep ends with an empty `git status --porcelain`, on every path.

Run it at low parallelism under the CPU rule. On 2026-11-04 (#904) a first sweep
restored from `HEAD:` while review edits were still uncommitted, and wrote old
content over two files. Its own end check caught it. The edits were re-created and
proved byte-identical by SHA-256 in a scratch worktree, and the guard was rebuilt
with a regression suite. Independent reviews of the rebuilt guard found gaps twice
more. Each time the findings went back to the guard's owner, were fixed, and the
regression suite grew, from 8 cases to 18.

## 4. Count honestly

- A mutant that fails to build is BUILD-FAIL, not caught. Rewrite it so it
  compiles and run it again. Example: S5a's sweep was 55 RED and 1 BUILD-FAIL out
  of 56; the rewritten mutant kept the import and ran 1 of 1 RED.
- A timeout, or a test that cannot run, is an ERROR, not RED.
- Report every survivor.
- Label what you did not verify: "owner-reported launch setting", "per parent,
  not re-verified here", "source only".

## 5. QA reviews the exact head; findings go back to the same owner

The QA agent is a separate agent. Give it the code, the exact head SHA and the
acceptance checks, not the worker's conclusions. Each finding states severity,
file and line, why it is wrong, and how to prove the fix.

Send the findings to the same coding owner as a correction. Continue that subagent
with `SendMessage`, so it keeps its context. Do not fix it yourself and do not add
a second writer. The owner adds a test for each finding (RED, then GREEN), commits
and reports the new head. QA then re-reviews that exact new head: the delta and
every earlier finding. Repeat until no finding is open.

Example: #903, the scheduled-work executor core, went through six adversarial
native review rounds this way. Each round's findings went back to its one coding
worker as corrections, and each re-review read the new head. It merged with no
open finding, after `CI - Success` on its final head and an advisory review with
no findings.

## 6. Accept independently

The coordinator, or a fresh QA agent, checks every acceptance check against
evidence: the tests run on the exact head, or the CI run for that head. A worker's
RESULT file is a claim to check, not proof.

## 7. Deliver through the gates

1. Push, then confirm `gh pr view N --json headRefOid` equals the reviewed head.
2. Wait for `CI - Success` on that exact head.
3. Read the advisory Claude review for that head. It is optional; if it did not
   run, record it as unavailable. Fix each finding, or answer it on the PR with a
   concrete reason.
4. Squash-merge with `gh pr merge N --squash --match-head-commit <sha>`, unless the
   work order holds the PR as a draft or a runtime hold applies. Record an
   unmerged PR in the handoff.

Record merge, publication, deployment and usable acceptance as separate facts.
