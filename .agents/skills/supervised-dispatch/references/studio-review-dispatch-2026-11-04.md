# Historical example: the Studio review child (2026-11-04)

**Historical.** This is the earlier review child of November 4, 2026. It is
**not** Tom's current adversarial Sol review of Studio. It proves nothing about
that review, about Studio's current backlog, or about its roadmap. It shows one
dispatch and return loop that closed once, and it is not operational acceptance
of anything.

Sources:
- GitHub reads made here on 2026-11-05: the metadata of haynes-studio PR 20,
  its files at its head, and the rulesets and protection of haynes-studio's
  `main`;
- the OP402 sections of the [workflow guide](../../../../docs/workflow-guide.md#launch-a-project-bound-child-on-another-provider)
  and [ask your coordinator](../../../../docs/ask-your-coordinator.md#return-route-for-a-parent-without-a-project);
- two inbox messages from the coordination task `dev-env-1105-001704`
  (`14fadf51...` at 00:52:33Z and `ba132a2c...` at 00:58:54Z, 2026-11-05),
  passed on by the coordinator.

A fact marked "relayed" comes only from those messages and was not re-verified
here. No Studio session was contacted or read for this page.

## What happened

| Step (supervised-dispatch, section 4) | Fact | Evidence |
|---|---|---|
| 1. Launch | An Opus parent ran `agent-run --project haynes-studio --agent codex --model gpt-6-astra --effort max --prompt-file <order> --wait 3m`. | Relayed; the same flags are recorded for the OP402 launch in the workflow guide. |
| Before the launch | The first attempt had no `haynes-studio` project in the catalog. haynes-ops #4645 added it through the reviewed catalog path. An earlier in-pod `codex exec` hit a bubblewrap failure, reported exit 0 and wrote no files. | Workflow guide (OP402). |
| 2. Identity | The child was `haynes-studio-1104-151051`, UID `48146147-7e75-4682-b204-997ced38bab3`. | Relayed. Not re-read here. |
| 3. Destination | The parent had no accepted project snapshot, so the child could not ask it through the inbox. Results came back by reading the child. | Relayed; the return route is in ask your coordinator. |
| 4. Semantic ACK | The child acknowledged the work order. | Relayed (the OP402 record, agent reports). |
| 5-6. Observe and watch | The parent ran a bounded PR-or-exit watcher. Meanwhile an investigator read the child's log and relayed interim findings to the parent with `agent-run msg`. That relay is not automatic child delivery. | Relayed; ask your coordinator records the relay. |
| 7. Return | haynes-studio PR 20, "docs(design): studio UI mockup v2 (story 401)", from branch `agent/haynes-studio-1104-151051`, head `edbd1647`. It holds `review.md` (12 findings), `questions.md` (5 questions) and `index.html`. | Read here: PR metadata and both files at `edbd1647`. |
| 7. Review and merge | The parent picked up and reviewed the result. PR 20 merged at 2026-11-04T15:36:49Z (merge commit `195e5a98`). | Pickup and review relayed; merge read here. |
| Rung 7 | haynes-studio's `main` has no required checks: no ruleset rules (`gh api repos/thaynes43/haynes-studio/rules/branches/main` returned `[]`) and no classic protection (`gh api repos/thaynes43/haynes-studio/branches/main` reports `protected: false`). Rung 7 cannot be reached there. | Read here. |

Do not carry Studio's missing required checks to another repository. dev-env
requires `CI - Success` on the exact head before merge.

## Not established

- Automatic inbox return from a child to its parent.
- Reading an archived child, reading after a reap, recovery after a restart
  or resume, and recovery of a timer.
- Testing on the actual claude.ai host.
- Anything about Tom's current Sol review, Studio's current work, its backlog
  or its roadmap.

## A second observation: a new task's log not found yet (2026-11-05)

Relayed, about the coordination task `dev-env-1105-001704`:
- **Launch.** It was created at 00:17:04Z, configured as Opus 5.5 at high
  effort. Exactly one explicitly approved 600-second bootstrap token was
  minted at 00:17:02Z, kept only in memory and not renewed.
- **Observer.** Its pod ran, but during startup the API first answered that
  the log was not found. The observer ended on that first not-found. That was
  the bug: the task was starting, not failing.
- **Later reads.** Later authorized reads of only that task found its semantic
  reply (`message.8d9f8557...`, 00:19:17Z), which named all three defects it
  was sent. That reply proves rung 3. It does not prove that the correction or
  the review finished.
- **Model.** The agent log reports `claude-opus-5-5` from the CLI. The served
  model is Unknown.

The rule this gives is step 5 of the loop: re-observe the same task. Never
launch it again. The bootstrap token serves that one launch only: never renew
it, never reuse it for observation, and never mint another because the log is
not ready.
