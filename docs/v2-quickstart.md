# Dev-env v2 quick start

How to use v2 day to day, mostly from your phone: Claude and Codex sessions,
headless tasks, network and Proxmox access, stopping and resuming, upgrades, the
shared logins, where notes live, and what still needs v1.

State on 2026-10-20: release 2.37.0 is installed. "Proven" below means it was
done end to end on 2026-10-19 or 2026-10-20 (UTC), on your phone where a phone
is involved.

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
  You need the v1 pod only for the few jobs in section 8.

## 1. A Claude session on your phone

**One is always waiting.** The operator keeps a standby session on your phone:
Opus 5.5 (`claude-opus-5-5`) on haynes-ops, in the workbench profile (below).
Open it in the Claude app and type. It is named like `haynes-ops-1020-002115`;
`agent-run show <name>` gives its parent as
`dev-env-system/dev-env-operator-standby`.

- There is one standby at a time. A suspended standby still counts: resume it.
  When it is reaped or its agent ends, the operator makes a new one within about
  a minute. It makes at most 3 in 30 minutes, then waits, so a fault cannot loop.
- Once you use it, it is an ordinary session. It is not replaced while it lives.
  For a fresh one, reap it when you are done with it.

To start another session, ask the standby or any coordinator, or run:

```bash
agent-run --repo haynes-ops --interactive --model claude-opus-5-5
```

- A session started from a workbench session is a workbench session too. From
  the v1 pod, add `--profile workbench` for one; without it you get the default
  profile.
- The session is named after the repo and the time. After a minute or two it
  appears in the Claude app under that name.
- It uses the shared Claude login (section 6). Nobody signs in per session.
  agent-run still prints a hint about `agent-run login`. Ignore it: a session on
  the shared login refuses `login`.
- To check from a terminal: `agent-run show <name> -o json` has
  `remoteControl.state`. `registered` means it is on your phone.
  `login-required` means the shared login has lapsed (section 6).
- After 72 hours idle it suspends itself, and its phone entry is archived.
  `agent-run resume <name>` brings back the same entry and the same conversation.
- Proven: open on the phone, prompt, answer, suspend and resume as the same
  entry, and the earlier answer recalled after the resume. The operator made
  the standby by itself and it registered on the shared login.

### What a workbench session can do

A workbench session runs as `dev-env-workbench`. On top of an ordinary session
it can:

- run `agent-run pair`, `attach`, `detach` and `login` against other v2
  sessions, with no v1 pod;
- create a Codex host (section 2);
- get standing access grants for the LAN and Proxmox, as below.

**Access grants.** A standing policy approves each of these at once, with no
phone step. Ask from inside the session that needs it:

```bash
# Internal sites, https://<app>.haynesops.com (curl, Playwright). Also the default profile.
agent-run grant request --type egress --reason "internal sites" --ttl 8h \
  --endpoint network:app.kubernetes.io/instance=traefik-internal-network --port 8443

# Proxmox API for `pve` (read with the built-in token)
agent-run grant request --type egress --reason "pve" --ttl 8h \
  --fqdn pvedash.haynesnetwork --port 443

# Proxmox writes: a short-lived operator token; `pve --yes ...` uses it by itself
agent-run grant request --type credential --credential proxmox --reason "pve write" --ttl 4h

# hw-ssh to the PVE nodes, HaynesTower, PiKVM and gasha01
agent-run grant request --type egress --reason "hw-ssh" --ttl 8h --port 22 \
  $(for h in haynesintelligence twin-top twin-bottom pve04 pve-filet02 \
      haynestower pikvm gasha01; do echo "--fqdn $h.haynesnetwork"; done)
```

- Egress grants last up to 8 hours, the Proxmox token up to 4. When one
  expires, ask again. `agent-run grant list --mine` shows what you hold.
- Two more policies exist for workbench sessions: each PVE node's API on port
  8006, and the Ceph manager's metrics. Their exact flags are in each policy's
  description, in haynes-ops `kubernetes/main/apps/dev-env-system/grant-policies/`.
- The `hw-ssh` key is mounted only in workbench sessions. The same rules as v1
  apply: read first, declare disruptive work, never reboot a node.
- Proven from a fresh workbench session: internal ingress (Grafana's health
  page answered 200), `pve nodes`, `hw-ssh twin-top hostname`, and a Proxmox
  token issued. All four were approved by policy with no phone step.

## 2. Codex on your phone

A Codex phone session is a host: one pod that runs Codex's phone daemon. You
pair a phone with it once.

1. Create the host from a workbench session (the standby will do) or the v1
   pod. An ordinary session is refused, because the host runs on the shared
   ChatGPT login. The host itself runs on the default profile.

   ```bash
   agent-run --repo dev-env --agent codex --interactive --model gpt-6-astra
   ```

2. When `agent-run show <name>` says Running, get a pairing code from the same
   place:

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
  again, the earlier answer recalled, and an operator restart survived. From a
  workbench session, creating a host and getting a pairing code both worked.

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
- A Codex task with `--repo` is refused; use `--project`. A Codex task on a
  repo outside the catalog, such as haynes-ops, still runs from v1.
- `agent-run show <name>` reports the model that actually served the task, for
  Codex as well as Claude (Codex since 2.37.0; before, it said Unknown).
- Proven: a Codex project task finished in one turn, in about 20 seconds.

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
  earlier answer. A session that has never had a turn, such as an unused
  standby, stays put until it has one.
- **Codex host:** never moves by itself. `agent-run list` marks it `outdated`.
  At a quiet moment run `agent-run suspend <name>`, then `agent-run resume <name>`.
  The pairing stays. Suspend and resume of a host are proven; using them for an
  upgrade has not been done yet.

## 6. The shared logins

The keeper, a small service in `dev-env-system`, holds one Claude Max login and
one ChatGPT login and refreshes them. Sessions get a short-lived access token,
never the refresh token. So sessions cannot break each other's login, and
nobody signs in per session.

- **Check.** In any v2 session, `agent-run auth status` shows each login's
  state (Ready or NeedsLogin), its generation and the time left on the current
  access token. Exit 0 is fine, 1 is expiring or expired, 2 means it cannot
  tell. Proven in a workbench session.
- **Refresh is automatic.** The keeper renews the Claude access token at about
  two thirds of its 8-hour life. Proven at 00:29 UTC on 2026-10-20: generation
  2 reached the session pods and every session stayed on the phone. The ChatGPT
  token is good until 2026-10-29, so its first automatic refresh has not come
  due yet.
- Headless Claude tasks do not use the keeper's login. They keep their own
  long-lived token, as in v1.
- **Renew the Claude login about once a month.** It lapses about 30 days after
  the last sign-in. The current one is from 2026-10-19, so it lapses around
  2026-11-18. `agent-run auth status` shows the access token, not this date;
  put it in your calendar.
- **Who does what.** A coordinator in the v1 pod starts the keeper's login
  helper and sends you one sign-in link in chat. You open it on your phone, sign
  in with your Max account and paste back the code the page shows. Finish within
  15 minutes; after that the helper gives up and the coordinator starts again
  with a fresh link. The link and the code never go into git or notes.
- **ChatGPT login.** Same shape, a device sign-in. It is needed only when the
  keeper reports NeedsLogin. It was renewed on 2026-10-19.
- **Not proven yet:** the automatic "continue" a session gets when a turn fails
  on an expired token. No turn has needed it.

### Renew the keeper login from v2

**Status: source and unit-tested only.** It works after the release that carries it
is pinned and the operator's new access is in place (the haynes-ops access change
merges first). Until then the v1 route above is the only one. Not yet run live.

You do this from a workbench session (the standby session on your phone is one). No
v1 pod, no laptop. Only a workbench session may do this; any other session gets
"not allowed". From the phone you have no terminal, so the agent in that session
runs the commands and you give it the sign-in code in the chat.

**From your phone**

1. Tell the workbench session: "renew the keeper's Claude login". It runs
   `agent-run keeper-login claude status` first and tells you the state and when the
   login lapses.
2. It runs `agent-run keeper-login claude url` and puts the sign-in link in the chat.
   Open it, sign in with your Max account, and copy the code the page shows.
3. Paste the code into the same chat. The agent passes it to
   `agent-run keeper-login claude code` on standard input and tells you the result.
4. It runs `status` again. The generation should be higher and `login expires` about
   30 days out.

The code is single-use and short-lived: it works only for the ceremony that is open,
and the ceremony closes after 15 minutes. It and the link appear in that chat and
nowhere else. Never put either in a file, git, a PR, a handoff or notes, and tell the
agent not to. If a code is wrong, expired or already used, nothing changes: ask for
`cancel`, then a fresh `url`.

**From a terminal in the session** (the same commands the agent runs):

```bash
agent-run keeper-login claude status    # state, generation, when the access token
                                        # lapses and when the login itself lapses
agent-run keeper-login claude url       # opens a ceremony, prints the sign-in link
read -rs c; printf '%s' "$c" | agent-run keeper-login claude code   # paste the code
agent-run keeper-login claude cancel    # abandon a ceremony this operator holds
```

`status` exits 0 for Ready (or PendingPublication) and changes nothing. If `code` says
it is still working, run `status`.

ChatGPT is the same with `codex`. Its `url` prints a link and a one-time code to type
on that page; there is nothing to paste, and `agent-run keeper-login codex code`
waits for the sign-in to finish (run it again if it says it is still waiting).

If you give up, `cancel` leaves the keeper's current login exactly as it was: only a
valid code replaces it. (If `cancel` races a code that is already being adopted, the
new login can still land, whole and never partial: `status` shows which.) If the
operator restarts mid-ceremony, run `url` again. If `url` says the keeper's attempt is
"held elsewhere", an earlier ceremony (before an operator restart, or from the v1
route) still holds the keeper's one slot: `cancel` cannot end it, and it expires by
itself within 15 minutes. The command reconnects by itself while the operator's other
replica answers.

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

- **The keeper login ceremonies** (section 6): the Claude login about once a
  month, next around 2026-11-18, and the ChatGPT login when it reports
  NeedsLogin. Today the v1 pod (or a laptop with admin access) runs them in the
  keeper pod. From a workbench session they move to `agent-run keeper-login` once
  that is released and pinned (section 6, "Renew the keeper login from v2"); it is
  not run live yet, so this still needs v1 until it is.
- **Restarting the v2 operator, keeper or shelf.** A v2 session is refused there.
- **Headless Codex tasks on a repo outside the catalog** (section 3).
- **Retiring v1 itself.** That is your call.

More detail is in the [workflow guide](workflow-guide.md) and
[plan 03](../.agents/sagas/distributed-dev-env/backlog/03-remote-control.md).
