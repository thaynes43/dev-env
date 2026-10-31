# How to ask your coordinator

Source guidance, 2026-10-31 (OpenProject #321). Nothing here is installed by
this change. The commands work once three things are true: the operator runs
with the durable inbox enabled for your project, it also runs with
`--enable-inbox-questions` (installed after its CRD), and the agent image
carries the matching `agent-run`. Until then `ask` and `reply` are refused with
400 and nothing is sent. `list --sent` and the thread view are refused the same
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
  change the key to force it through; read what you sent with
  `agent-run inbox list --project <p> --sent`.

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
