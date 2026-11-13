# How to ask your coordinator

Guidance for OpenProject #321. **Installed 2026-10-31:** operator v2.76.0 runs with
the durable inbox for `dev-env` and with `--enable-inbox-questions` (haynes-ops
#4494, after the InboxMessage CRD #4490). Session pods still carry an older
`agent-run`, because the agent image follows the held template. Until that
moves, use the signed side-by-side copy at
`~/.shared/handoffs/agent-run-v2.76.0/agent-run`, and check its sha256 against the
`PROVENANCE.txt` next to it. If the operator flag were off, `ask` and `reply`
would be refused with 400 and nothing sent. `list --sent` and the thread view are refused the same
way: `list` and `read` then show plain messages only, and `--sent` says the API
does not list sent messages. The contract is
[DESIGN-014, "Ask and reply"](../.agents/sagas/distributed-dev-env/designs/014-durable-offline-inbox.md#ask-and-reply-2026-10-31-openproject-321).

Use this route for a routine question to the Session that started you: which
base branch, whether a finding is in scope, what to do next. It works the same
for Claude, Codex and any other agent that can run `agent-run`.

It is not for:

- a decision only Tom can make. That is a phone question (Claude's
  AskUserQuestion) or, once activated, the Asks board ([asks.md](asks.md));
- a tool-permission or access request. Those keep their own routes
  (`agent-run grant`, the access broker);
- approval. A reply is information from your coordinator. It never approves a
  held action, a grant or a merge.

## Who can ask whom

Both ends must be **project-bound**: a Session started as a project task
(`agent-run --project <p> ...`) carries an accepted project snapshot
(the `dev-env.haynesops.com/project-snapshot` annotation). The server matches that
snapshot's project to the request's `--project`, for the sender and for the
recipient. The snapshot must also name the Session's own repository, and the
Session must have no workspace. `spec.project` alone is not what is checked.

- A recipient without a matching snapshot is hidden: the ask gets **404**. For
  example, an interactive or remote coordinator started without `--project`
  cannot be asked, even by its own children. Ask a project-bound Session by name
  with `--to`, or have the coordinator start as a project task, but only in a
  project whose inbox is on (next item). Such a coordinator still gets its
  headless children's results back: see
  [the return route for a parent without a project](#return-route-for-a-parent-without-a-project).
- The inbox serves only the projects the operator lists in `--inbox-projects`
  (`internal/apiserver/inbox.go:101-103` for reads, `:172-173` for send and
  ask). As read from the live operator Deployment on 2026-11-13, that list is
  `dev-env`, `haynes-studio` and `haynesnetwork` (haynes-ops#4877 for
  OpenProject #799 and haynes-ops#4887 for #829; it was `dev-env` only until
  then). In any other project every inbox call answers
  404, even between two project-bound Sessions, and starting the coordinator
  as a project task does not change that.
- The same list gates three more routes. A child's `agent-run parent tell` is
  hidden with **404** when the child's project is not in it
  (`internal/apiserver/parent_note.go:87`). So are Meatball's notes for a
  session of that project (`parent_note_policy.go`) and a parent's instruction
  to its own child (`coordinator_instruction.go`). Before you start a
  coordinator for another project, read the live value:
  `kubectl get deploy dev-env-operator -n dev-env-system -o jsonpath='{.spec.template.spec.containers[0].args}'`.
  A project is added in haynes-ops, in the operator HelmRelease.
- Session depth is limited to two levels (DESIGN-001 3.4). A Session at depth 2
  cannot start children, so it cannot coordinate workers it would start itself.
- Proven on 2026-10-31 through the live operator (OpenProject #321):
  - ask with `--to`, then reply and ack, between project-bound siblings: Claude
    to Claude (journal 3999) and Codex to Claude (journal 4013);
  - the default-to-parent form, with no `--to`, from a depth-2 Session to its
    project-bound depth-1 parent: Codex to Codex (journal 4031).

## Ask

```bash
agent-run inbox ask --project dev-env --key q-rebase-1 <<'EOF'
May I rebase agent/my-task on main, or should I merge main in?
EOF
```

With no `--to`, the server sends the question to your recorded parent Session:
the Session whose token created yours (`agent-run show <your-session>` prints
it as `parent`). The server resolves it; your text never chooses the recipient.
If your parent is not a Session (Tom or a client created you), the ask is
refused with 422 and you name the recipient:

```bash
agent-run inbox ask --project dev-env --to dev-env-1029-132039 --key q-rebase-1 < question.txt
```

`--to-uid <uid>` pins the exact Session UID when you have it.

The receipt is JSON. Keep its `name`: it identifies your question.

## Keys and retry

The key is your stable name for one question: lowercase letters, digits, `.`,
`_` and `-`, up to 128 characters, for example `q-<task>-<n>`.

- Exit 0: the question is stored. `Created` the first time, `Confirmed` for an
  identical retry.
- Exit 6 (outcome Unknown): the network or the API did not confirm. Nothing
  was repeated. Run the same command again with the same key and the same
  text. You get the one stored question back, never a second one.
- Exit 1 with "conflicts": the key already holds a different question. Do not
  change the key to force it through. `agent-run inbox list --project <p> --sent`
  shows the stored question's name, key, recipient and state. It shows metadata
  only, not the text: `read` is for the recipient.

A new question gets a new key. The same rules hold for `reply`.

## See the answer

```bash
agent-run inbox list --project dev-env --sent   # your questions and their state
agent-run inbox list --project dev-env          # your inbox: the reply arrives here
agent-run inbox read <reply-name> --project dev-env
agent-run inbox ack <reply-name> --project dev-env
```

`list` and `read` show each message's `kind` (`plain`, `question` or `reply`),
the question a reply answers (`inReplyTo`) and the question's `thread` state.
`read` prints the text on stdout and, for a question or reply, one state line
on stderr. Reading never acknowledges; `ack` is explicit.

| State | Meaning | Who acts |
| --- | --- | --- |
| `Unanswered` | No reply yet, and the recipient can still answer. | The recipient (`waitingOn`, set only while it is confirmed live) |
| `Stale` | No reply for more than 30 minutes (`staleAfterSeconds` 1800). A display bound only: nothing expires, is resent or escalated. | Decide whether to wait, ask someone else or record it in your handoff |
| `Answered` | A reply exists (`reply` names it); the asker has not acknowledged it. | The asker: read and `ack` it |
| `Acknowledged` | The asker acknowledged the reply. Done. | Nobody |
| `RecipientEnded` | No reply, and the recipient will not answer: its Session is gone, replaced, being deleted, Archived or Failed, or it is a task whose agent exited. A Suspended Session has not ended: it reads its inbox when resumed. | The asker: ask another coordinator or record the question as open |
| `Unknown` | The API could not confirm a lookup. It never guesses `Unanswered`. | Read again later |

The state is computed on every read from the stored records. Retention can
remove a reply about 30 days after its sender ended and it was acknowledged;
after that the question may read `RecipientEnded`.

## Answer (coordinator side)

```bash
agent-run inbox list --project dev-env                     # questions to you, with their state
agent-run inbox read <question-name> --project dev-env
agent-run inbox reply <question-name> --project dev-env --key r-rebase-1 <<'EOF'
Rebase on main; do not merge main in.
EOF
```

The reply goes to the question's sender; you do not name a recipient. A
question takes one reply: a second, different reply is refused with 409. You
can reply only to a question addressed to you in the same project (404
otherwise), and only to a question, not to a reply or a plain message (422).

## From agentd

`agentd inbox-publish` forwards a question or reply like any other message: it
takes the intent as JSON on stdin (`kind`, `toParent`, `inReplyTo` included)
and sends it unchanged. The server enforces every rule above; agentd adds none.

## Limits

- **Nothing interrupts a running agent.** A headless task is not woken by a
  reply: it sees one only when it runs `list` or `read`. Poll at a modest
  interval (no more than once a minute), for a bounded time you state in your
  handoff, and never in a busy loop. If the answer has not come when your work
  order says to stop waiting, follow its fallback and record the question's
  name and state in your handoff. A TUI coordinator is not notified either; it
  lists its inbox.
- The recipient must be a Session in the same project with that project's
  snapshot. Session-less hosts (a Codex coordinator host, the Mac) cannot be
  asked yet. A configured coordinator host can read the project's threads but
  cannot send or reply.
- Text is 1 to 16384 UTF-8 bytes. A Session can send 512 messages in its
  lifetime and a project holds 4096 live ones; questions and replies count.
- The record keeps the authenticated sender, recipient, executor Pod, provider,
  requested model and catalog provenance. That is who asked, not proof that a
  model or a person read anything: an acknowledgement is an API record only.

## Return route for a parent without a project

*OpenProject #402, 2026-11-04. Checked against source at `801de50d`, with the
retention tests of round 2.*

A Remote Control coordinator, or any interactive coordinator started without
`--project`, has project null and no accepted project snapshot. It can still
start project-bound headless children (`agent-run --project P -p ...`; see
[the workflow guide](workflow-guide.md#launch-a-project-bound-child-on-another-provider)).
Their results come back by the parent reading the child, not by the child
reaching the parent. These routes need no inbox, no enrollment and no grant:

1. **`agent-run show CHILD`**, and `-o json` for the fields. It prints the
   child's task exit (`task: exit N at ...`), the verdict (`task result: ...`)
   and the retained outcome (`Task outcome:` with the PR URLs and heads the
   child recorded) (`internal/agentrun/sessions.go:192-194,227-235,285-293`).
   The outcome lives in the Session's status. An envtest against a real
   kube-apiserver shows it served after a restart of the API, after the pod is
   deleted, with the phase Suspended or Archived, and during a reap while the
   finalizer holds ([where the retained outcome lives](workflow-guide.md#where-the-retained-outcome-lives)).
   **Once a reap releases a Session that is not under private-history
   protection, the Session and its outcome are deleted, and `show` answers
   404** (`internal/controller/reconciler.go:1150-1197`). On the live operator,
   private-history retention keeps task Sessions until their HOME expiry
   ([live mitigation](workflow-guide.md#where-the-retained-outcome-lives)).
   Copy what you need before a reap anyway. A session can read any Session by name; a configured
   coordinator host can read only its own children
   (`internal/apiserver/sessions.go:420-437`).
2. **`agent-run log CHILD --tail 200`**, while the child's pod is running and
   Ready. Otherwise `log` is refused with 409. When the pod is gone, the
   refusal names the copy on the shared volume, `logs/<name>.log`, which
   session pods mount at `~/.shared`; a pod that is there but not running gets
   a plain 409 (`internal/apiserver/podcmd.go:51-63,160-195`,
   `internal/agentrun/help.go:474-477`). Whether a private project task's log
   reaches that shared copy is not verified. Do not count on that volume from
   another harness or host
   ([ADR-003](../.agents/sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md)).
3. **The outcome file.** The work order tells the child to write
   `~/work/<session>.outcome.json` before it returns, with its PR URL, head and
   a one-line summary. agentd validates it at the task's exit and the operator
   keeps the first valid record, which `show` reads back
   ([format](workflow-guide.md#retain-a-tasks-pr-and-handoff-after-pod-reclaim)).
   The parent never reads the file in the child's pod.
4. **The PR.** `gh pr view N --json headRefOid,state,statusCheckRollup`, then
   the deliverable at that head.

Put the deliverable, the outcome-file instruction and the deadline in the work
order. Track the wait with a custody record and a bounded wait
([timer-checkin](../.agents/skills/timer-checkin/SKILL.md)).

**Demonstrated and source only.** Reported in the OP402 incident record (agent
reports, not re-verified here): the child's semantic ACK, its review files and
a PR. An investigator read the child's log and relayed the findings to the
parent with `agent-run msg`; the parent then consumed them. The parent itself
made neither the log read nor the relay, and did not use this return route.
Shown only by source and by envtest
with synthetic fixtures: retrieval after pod deletion, suspend, archive or
reap, and after a restart of the API. Not shown anywhere: recovery after a real
operator restart or a resumed coordinator, a persisted timer, and a child's
inbox ask to a project-null parent.

**Delivery is not consumption.** A retained outcome, a pushed branch or an open
PR says a result is available. The parent has consumed it only when it has read
the deliverable at the stated head and recorded that in its custody record and
on the tracker. The retained outcome is the child's own claim: check it. Exit
0, `Running` or a final message is not a finished review; only the accepted
deliverable is.

### The inbox gap is separate

The 404 above is a different problem: a parent with project null cannot be the
recipient of an `inbox ask`, because the server hides any recipient whose
project snapshot does not match the request's project
(`internal/apiserver/inbox.go:28,140-153`). In a project whose inbox is off,
every inbox call is 404 for every Session (`inbox.go:101-103,172-173`). The return route above does not
depend on the inbox and does not close that gap. The gap is recorded as a
follow-up on OpenProject #402. This guide proposes no enrollment, grant or
policy change for it. Until it is closed, a headless child of such a parent
does not wait on an answer: it records the open question in its PR body and its
outcome summary, and follows its work order's fallback.
