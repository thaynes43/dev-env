# Dev-env v2 quick start

How to use v2 day to day, mostly from your phone: Claude and Codex sessions,
headless tasks, stopping and resuming, upgrades, the shared logins, where notes
live, and what still needs v1.

State on 2026-10-19: release 2.35.1 is installed. "Proven" below means it was
done end to end on 2026-10-19, on your phone where a phone is involved.

## Which `agent-run`

- In a v2 session, `agent-run` is the v2 client. There is nothing to set up.
- In the v1 pod, `agent-run` is still v1's launcher. The v2 client is a separate
  binary. Set it up in each shell:

  ```bash
  kubectl -n dev-agents get configmap dev-env-api-ca \
    -o jsonpath='{.data.ca\.crt}' > ~/.cache/dev-env-api-ca.crt
  export DEV_ENV_API_CA_FILE=~/.cache/dev-env-api-ca.crt
  alias v2="$HOME/.local/bin/agent-run-v2.35.0"   # or the newest agent-run-v2.* there
  ```

  The CA is public. The examples below say `agent-run`; in the v1 pod, type `v2`.

## 1. A Claude session on your phone

Ask a coordinator to start one, or run it yourself:

```bash
agent-run --repo haynes-ops --interactive --model claude-opus-5-5
```

- The session is named after the repo and the time, such as
  `haynes-ops-1019-231500`.
- After a minute or two it appears in the Claude app under that name. Open it
  and type your first prompt.
- It uses the shared Claude login (section 6). Nobody signs in per session.
  agent-run still prints a hint about `agent-run login`. Ignore it: a session on
  the shared login refuses `login`.
- To check from a terminal: `agent-run show <name> -o json` has
  `remoteControl.state`. `registered` means it is on your phone.
  `login-required` means the shared login has lapsed (section 6).
- After 72 hours idle it suspends itself, and its phone entry is archived.
  `agent-run resume <name>` brings back the same entry and the same conversation.
- Proven: open on the phone, prompt, answer, suspend and resume as the same
  entry, and the earlier answer recalled after the resume.

Coming soon:

- **Workbench.** A v2 session in the `workbench` profile that can run
  `agent-run pair` and `attach` itself, so a v2 coordinator needs the v1 pod
  less. It is merged; it needs the next release, two haynes-ops changes and a
  live test.
- **Standby.** The operator keeps one Claude session ready on your phone at all
  times, like v1 does after a pod restart. It is being built. Until then, ask
  the v1 standby or any coordinator to start one.

## 2. Codex on your phone

A Codex phone session is a host: one pod that runs Codex's phone daemon. You
pair a phone with it once.

1. Create the host from the v1 pod (or ask its coordinator). A v2 session is
   refused, because the host runs on the shared ChatGPT login.

   ```bash
   agent-run --repo dev-env --agent codex --interactive --model gpt-6-astra
   ```

2. When `agent-run show <name>` says Running, get a pairing code, also from the
   v1 pod:

   ```bash
   agent-run pair <name>
   ```

   The code lasts about ten minutes and is a secret until you use it. The
   coordinator sends it to you in chat only.
3. In the ChatGPT app choose Remote, add a computer and type the code. The
   computer has the session's name.
4. Start a thread. As in v1, the phone picks the thread's folder; for repo work,
   ask the thread to make a worktree first.

- The pairing lives on the session's volume. Suspend and resume keep it;
  reaping the host loses it.
- A host never suspends itself, because its daemon counts as busy. Suspend or
  reap it when you are done.
- Proven: pairing, a prompt and answer, suspend and resume without pairing
  again, the earlier answer recalled, and an operator restart survived.

## 3. Headless tasks

A task runs its prompt once, with no TUI, then stops.

```bash
# Claude, on any repository
agent-run --repo dev-env --model claude-opus-5-5 -p "Summarize the open PRs."

# Codex, only on a catalog project: dev-env or sigo-alumni today
agent-run --project dev-env --agent codex --model gpt-6-luna -p "Summarize the open PRs."
```

- Follow it with `agent-run show <name>` and `agent-run log <name> --tail 40`.
- Cap it with `--timeout 40m` or `--max-turns 20`.
- A finished task suspends itself after an hour. Its prompt never runs twice.
- A Codex task with `--repo` is refused; use `--project`.
- Proven: a Codex project task finished in one turn, in about 20 seconds.
  Codex does not report the model it served, so v2 shows it as Unknown.

## 4. Stop, continue, finish

| Command | What it does |
|---|---|
| `agent-run list` | Every session, newest first. `--mine` shows yours. |
| `agent-run show <name>` | One session in detail. |
| `agent-run log <name>` | The end of its log. |
| `agent-run msg <name> "<text>"` | Types a message into its TUI. |
| `agent-run suspend <name>` | Saves its Git work, stops the pod, keeps the volume. |
| `agent-run resume <name>` | A new pod on the same volume, same conversation. |
| `agent-run reap <name>` | Saves its Git work, stops it, removes its volume. |
| `agent-run fleet` | What runs where, and what is on an old version. |

- Suspend anything you may want to continue. Reap only what is finished: the
  conversation goes with the volume. The Git work stays in a rescue bundle
  (`agent-run rescue list`, then `agent-run rescue restore`).
- By default a session suspended for 7 days is archived. After that it can be
  restored from its rescue, but not resumed.
- Pending means the session is waiting for room on a node. Do not start a
  second one.

## 5. Upgrades

- **Operator upgrade:** running sessions are left alone. Proven.
- **New agent image (a release):** a Claude session moves by itself once it is
  idle and nobody is attached. The pod is replaced; the phone entry and the
  conversation stay. Proven twice on 2026-10-19, and the session still knew its
  earlier answer. A session that has never had a turn stays put until it has one.
- **Codex host:** never moves by itself. `agent-run list` marks it `outdated`.
  At a quiet moment run `agent-run suspend <name>`, then `agent-run resume <name>`.
  The pairing stays. Suspend and resume of a host are proven; using them for an
  upgrade has not been done yet.

## 6. The shared logins

The keeper, a small service in `dev-env-system`, holds one Claude Max login and
one ChatGPT login and refreshes them. Sessions get a short-lived access token,
never the refresh token. So sessions cannot break each other's login, and
nobody signs in per session.

- **Check.** Inside a v2 session, `agent-run auth status` shows each login's
  state (Ready or NeedsLogin) and the time left on the current access token.
  Exit 0 is fine, 1 is expiring or expired, 2 means it cannot tell. It is
  coming in the next release; 2.35.1 does not have it. Until then a coordinator
  in the v1 pod checks the Claude login with:

  ```bash
  kubectl exec deploy/dev-env-keeper -c claude-login -n dev-env-system \
    -- agentd claude-login-status
  ```

  `"code":"Ready"` is fine.
- Headless Claude tasks do not use the keeper's login. They keep their own
  long-lived token, as in v1.
- **Renew the Claude login about once a month.** It lapses about 30 days after
  the last sign-in. The current one is from 2026-10-19, so plan for mid-November.
  Nothing in v2 shows the days left yet.
- **Who does what.** A coordinator in the v1 pod starts the keeper's login
  helper and sends you one sign-in link in chat. You open it on your phone, sign
  in with your Max account and paste back the code the page shows. Finish within
  15 minutes; after that the helper gives up and the coordinator starts again
  with a fresh link. The link and the code never go into git or notes.
- **ChatGPT login.** Same shape, a device sign-in. It is needed only when the
  keeper reports NeedsLogin. It was renewed on 2026-10-19.
- **Not proven yet:** the keeper's automatic refresh of the Claude access token
  (the first was due late on 2026-10-19), and the automatic "continue" a session
  gets when a turn fails on an expired token.

## 7. Where notes live

- `~/.shared` is one folder that every v2 session mounts:
  - `memory/`: Claude's auto-memory, one folder per repo, shared by every
    session on that repo.
  - `orders/`: work orders and receipts a v2 coordinator shares with its
    workers. Keep them small, and never put a secret there.
  - `rescue/`: rescue bundles of Git work.
  - `logs/`: task logs.
- `~/work` belongs to one session. It goes when that session is reaped or
  archived.
- Anything that must last (decisions, handoffs, backlog) goes in git or an
  issue, as in v1.
- The v1 pod does not mount `~/.shared`. v1's notes stay in its own
  `~/work/orders`.

## 8. What still needs v1

- **The keeper login ceremonies** (section 6). They run inside the keeper pod,
  which only the v1 pod or a laptop with admin access may reach. The workbench
  will not change this: it reaches session pods only.
- **`agent-run pair`, `attach` and `detach`**, until the workbench ships.
- **Creating a Codex host.** Only you (from a laptop) or the v1 pod may. A
  workbench session counts as a session, so it will not be able to either.
- **Restarting the v2 operator, keeper or shelf.** A v2 session is refused there.
- **Proxmox operator work, `hw-ssh`, Unraid and PiKVM.** A v2 session has no key
  and no LAN reach by default. Each use needs an access grant you approve, and no
  standing grant policy exists yet.
- **Codex tasks on a repo outside the catalog**, such as haynes-ops.
- **A session that is always on your phone after a restart** (v1's standby),
  until the v2 standby ships.
- **Playwright against internal `*.haynesops.com` sites.** Probably blocked from
  v2; not tested.

Retiring v1 is your call once these are covered. More detail is in the
[workflow guide](workflow-guide.md) and
[plan 03](../.agents/sagas/distributed-dev-env/backlog/03-remote-control.md).
