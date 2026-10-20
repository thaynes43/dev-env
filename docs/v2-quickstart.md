# Dev-env v2 quick start

How to use v2 day to day, mostly from your phone: Claude and Codex sessions,
headless tasks, network and Proxmox access, stopping and resuming, messages
between sessions, upgrades and restarts, the shared logins, where notes live,
and what still needs v1.

State on 2026-10-20: release 2.38.1 runs the operator and the keeper
(haynes-ops #4133, about 06:53 UTC). Session pods and the templates stay on
2.38.0 (haynes-ops #4131, about 05:31 UTC), so no session had to move overnight;
the next release brings them up to date. Each result below carries one of four proof levels:

- **Proven on the phone:** you did it end to end on your phone.
- **Proven from a session:** an agent did it live in a v2 workbench session or
  from the v1 coordinator, not on your phone.
- **Installed, not exercised:** in the installed release, never run live.
- **Not built:** no code yet, or the code is off on the installed system.

Dates are UTC.

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
- One fault seen: on 2026-10-20, two seconds after the standby moved to 2.38.0,
  the operator made a second one. The unused duplicate was reaped, and one
  standby runs. 2.38.1 fixes it (#480): a standby that is moving to a new
  version now counts as present. Installed, not exercised: no session has moved
  since.

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
- Proven on the phone, on an ordinary Claude session: open, prompt, answer,
  suspend and resume as the same entry, and the earlier answer recalled after
  the resume. The operator made the standby by itself and it registered on the
  shared login; you have not yet steered the standby itself from the phone.

### What a workbench session can do

A workbench session runs as `dev-env-workbench`. On top of an ordinary session
it can:

- run `agent-run pair`, `attach`, `detach` and `login` against other v2
  sessions, with no v1 pod;
- create a Codex host (section 2);
- renew the keeper's logins with `agent-run keeper-login` (section 6);
- restart the operator, keeper, broker or shelf (section 5);
- read the cluster as v1 does: `kubectl get` on everything except Secrets,
  `flux get`, and read-only `omnictl` and `talosctl`;
- use GCP with the same credentials as v1 (`tofu`), and all ten MCP servers;
- get standing access grants for the LAN and Proxmox, as below.

The cluster read, Omni, GCP and the MCP credentials came with 2.38.0 (haynes-ops
#4127, installed with #4131). A workbench session started before that keeps its
old settings until its pod is replaced. Proven from a session on 2026-10-20, in
a fresh workbench session on 2.38.0: `kubectl get pods -A` worked and `kubectl
get secrets` was refused; `flux get ks -A`, `omnictl get clusters` and
`talosctl version` worked; GCP issued a token for sigo-alumni-prod; and all ten
MCP servers connected for Claude. Codex has the same servers configured, and its
HTTP servers answered a direct check. The claude.ai connectors (Docs, Gmail,
Calendar, Drive) showed up too.

A workbench session cannot change the cluster: no pod delete, no `flux
reconcile`, no Jobs. Whether it should is your decision (see the handoff). Until
then, use v1. A default (`full` profile) session has those rights in RBAC
outside the dev-env namespaces (checked on 2026-10-19), but nobody has used them
from v2.

**Access grants.** A standing policy approves each of these at once, with no
phone step. Ask from inside the session that needs it:

```bash
# Internal sites, https://<app>.haynesops.com (curl, Playwright). Also the default profile.
agent-run grant request --type egress --reason "internal sites" --ttl 8h \
  --endpoint network:app.kubernetes.io/instance=traefik-internal-network --port 8443

# Proxmox API for `pve` (read with the built-in token)
agent-run grant request --type egress --reason "pve" --ttl 8h \
  --fqdn pvedash.haynesnetwork --port 443

# Proxmox writes: a short-lived operator token; `pve` uses it by itself.
# Works since 2.38.1 (reads proven, writes not yet tried); it needs the
# Proxmox API grant above too. See below.
agent-run grant request --type credential --credential proxmox --reason "pve write" --ttl 4h

# hw-ssh to the PVE nodes, HaynesTower and PiKVM (port 22 to gasha01 too)
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
- `hw-ssh` has no `gasha01` entry, in v1 or v2: nobody has confirmed which user
  may log in there. Once that is known, `hw-ssh --raw <user>@gasha01.haynesnetwork`
  works under the grant above.
- Proven from a session (a fresh workbench session): internal ingress
  (Grafana's health page answered 200), `pve nodes` and `hw-ssh twin-top
  hostname`, each approved by policy with no phone step.
- **The Proxmox operator token works since 2.38.1, for reads so far.** Proven
  from a session on 2026-10-20 (06:54 to 06:56 UTC), in a workbench session:
  1. `agent-run grant request --type credential --credential proxmox
     --reason "..." --ttl 15m` went Active in 5 seconds. The keeper made the token and put it in the
     session (`agentd ctl credential-available --credential proxmox` exited 0).
  2. `agent-run grant request --type egress --reason "..." --fqdn
     pvedash.haynesnetwork --port 443 --ttl 15m` (the Proxmox API grant above)
     went Active at once. Without it `pve` cannot reach Proxmox.
  3. `pve ha` showed `tier=operator`; `pve nodes` listed all five nodes, quorum
     OK.
  4. `agent-run grant release <grant>` removed the token from Proxmox within 19
     seconds, and `pve ha` went back to `tier=read`.

  Only reads were run with the token. A write (`pve --yes ...`) has not been
  tried from v2 yet (section 8).
- Before 2.38.1 no token ever reached a session; earlier notes here that said a
  token was issued were wrong. In 2.37.0 the keeper's SSH client failed the
  nodes' host-key check (fixed in 2.38.0, #474). In 2.38.0 the keeper misread
  Proxmox's answer and deleted each new token at once (fixed in 2.38.1, #481).

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
- Proven on the phone: pairing, a prompt and answer, suspend and resume without
  pairing again, the earlier answer recalled, and an operator restart survived.
- Proven from a session: creating a host and getting a pairing code from a
  workbench session; moving a host to a new image (section 5); and a damaged
  ChatGPT access token in a pod put back from the keeper within about 4 seconds,
  after which the next Codex turn worked.

## 3. Headless tasks

A task runs its prompt once, with no TUI, then stops.

```bash
# Claude, on any repository
agent-run --repo dev-env --model claude-opus-5-5 -p "Summarize the open PRs."

# Codex, only on a catalog project (list below)
agent-run --project dev-env --agent codex --model gpt-6-luna -p "Summarize the open PRs."
```

- Follow it with `agent-run show <name>` and `agent-run log <name> --tail 40`.
- Cap it with `--timeout 40m` or `--max-turns 20`.
- Its prompt never runs twice. A finished task suspends itself after an hour,
  but its pod keeps running; reap it when you have read the result (section 4).
- **Catalog projects** (since haynes-ops #4122, 2026-10-20): `dev-env`,
  `sigo-alumni`, `haynes-ops`, `haynesnetwork`, `hass-sandbox`, `cigar-journal`
  and `libretto`. A Codex task with `--repo` is refused; use `--project`. A
  Codex task on a repo outside the catalog still runs from v1 (section 8).
- Every session clones the catalog repos when it starts. With eight repos, one
  slow clone sometimes hit the two-minute limit and the step reported
  `Partial`. The session still ran. 2.38.0 abandons a stalled clone and retries
  it (#478). On 2.38.0, five new sessions and the standby all reported Ready,
  8 of 8 repos (proven from a session). No clone stalled, so the retry itself
  has not been seen at work.
- `agent-run show <name>` reports the model that actually served the task, for
  Codex as well as Claude (Codex since 2.37.0; before, it said Unknown).
- Proven from a session: a Codex project task finished in one turn, in about 20
  seconds. On 2026-10-20 a Claude task and a Codex task both finished on
  `libretto`, a repo that was not in the catalog the day before.

## 4. Stop, continue, finish

| Command | What it does |
|---|---|
| `agent-run list` | Every session, newest first. `--mine` shows yours. |
| `agent-run show <name>` | One session in detail. |
| `agent-run log <name>` | The end of its log. |
| `agent-run msg <name> "<text>"` | Types a message into its TUI. |
| `agent-run suspend <name>` | Saves its Git work, stops the pod, keeps the volume. Tasks differ, below. |
| `agent-run resume <name>` | A new pod on the same volume, same conversation. |
| `agent-run reap <name>` | Saves its Git work, stops it, removes its volume. Tasks differ, below. |
| `agent-run fleet` | What runs where, and what is on an old version. |

**Interactive sessions** (`--interactive`, `--local`, the standby, a Codex host):

- Suspend anything you may want to continue. Reap only what is finished: the
  conversation goes with the volume. The Git work stays in a rescue bundle.
- `agent-run rescue list` shows the bundles. `agent-run rescue restore
  <session>/<stamp> --local` starts a new session that holds the saved work in a
  Git ref, `refs/rescued/heads/rescue/<session>-<stamp>`. The files are not put
  in its worktree; copy them from that ref (`git checkout <ref> -- <path>`).
- By default a session suspended for 7 days is archived. After that it can be
  restored from its rescue, but not resumed.
- Proven from a session, 2026-10-20, on `--local` sessions: reap with
  uncommitted files, then restore; the restored ref held the same files, byte
  for byte. Suspend and resume kept the worktree, an untracked file and the
  conversation (a code word was recalled after the resume). A remote session,
  the standby and a Codex host were not reaped in this test.

**Tasks** (`-p`). A task's HOME, which holds its conversation, is kept for a
history archive that is not switched on yet (ADR-008). So for a task:

- `reap` stops its pod (since haynes-ops #4128, 2026-10-20) and keeps its volume.
  It makes no rescue bundle. The task stays in `agent-run list` as `reaping`
  (phase Pending) until the history archive is switched on. Nothing is deleted.
  Proven from a session: on 2026-10-20 the pods of nine reaped tasks stopped,
  and their volumes stayed.
- `suspend`, and a task's own suspend after an hour, do not stop its pod
  (ADR-006). To stop a finished task, reap it.
- `agent-run history list|show|read` answers 404 today. Reading archived
  history is off, and nothing has been archived. Not built on the installed
  system.

Pending with `reaping` is a reaped task. Pending otherwise means the session is
waiting for room on a node; do not start a second one.

### Messages between sessions

- `agent-run msg <name> "<text>"` types into a running interactive session. A
  finished task answers 409 (not running). `agent-run log` shows agentd's own
  lines, not the reply; read the reply in the session. Proven from a session:
  the message arrived and the session answered.
- Inbox, task claims and shared notes: an agent in a project task uses
  `agentd`. Proven from a session, 2026-10-20, on `dev-env`.

  ```bash
  # send (the recipient's name and uid come from agent-run show)
  echo '{"project":"dev-env","recipient":{"namespace":"dev-agents","name":"<name>","uid":"<uid>"},"key":"<unique>","text":"..."}' \
    | agentd inbox-publish
  agentd inbox-list --project dev-env                       # your own messages
  agentd inbox-read --message <message> --project dev-env
  agentd inbox-ack  --message <message> --project dev-env
  echo '{"identity":{"project":"dev-env","repository":"dev-env","taskKey":"task:<slug>"}}' \
    | agentd task-claim-create                              # or issue:<n>
  agentd task-claim-list --project dev-env
  agentd ctl context-publish | context-list | context-read  # shared notes
  ```

- Only a session started as a project task (`agent-run --project <p> -p ...`)
  carries a project, so only such a session can send or receive. An interactive
  session, or a task started with `--repo` alone, gets 404.
- Only the recipient can read a message; the sender gets 404. A second claim on
  the same task fails and names the owner.
- Switched on today: the inbox and claims for `dev-env`, shared notes for
  `dev-env` and `sigo-alumni`. The session directory is off.
- `kubectl get` on these records is refused for every agent and for v1, on
  purpose: the message text is stored in them. Read them through `agentd`
  inside a project task. From the v1 pod:
  `KUBECTL_REMOTE_COMMAND_WEBSOCKETS=false kubectl -n dev-agents exec <task-pod> -- agentd ...`.
- A workbench session was refused by these routes before 2.38.0 (#472). Proven
  from a session on 2.38.0: a workbench task and a default task sent, read and
  acknowledged each other's messages, and shared a claim and a note.
- A project task cannot be fully reaped yet (section 4), so every test of
  messages, claims or notes leaves its sessions and volumes behind.

## 5. Upgrades and restarts

- **Operator upgrade:** running sessions are left alone. Proven on the phone.
- **New agent image (a release):** a Claude session moves by itself once it is
  idle and nobody is attached. The pod is replaced; the phone entry and the
  conversation stay. Proven on the phone twice on 2026-10-19, and the session
  still knew its earlier answer. With 2.38.0 the standby and one other session
  moved the same way at 05:31 on 2026-10-20 (seen from a session, not yet
  checked on the phone). A session that has never had a
  turn, such as an unused standby, stays put until it has one.
- **Codex host:** never moves by itself. `agent-run list` marks it `outdated`.
  At a quiet moment run `agent-run suspend <name>`, then `agent-run resume <name>`.
  The pairing stays. Proven from a session on 2026-10-20: the host PEAR moved
  from 2.35.0 to 2.37.0 this way. Same pairing, the daemon reconnected, and the
  thread history was intact. Checking it in the ChatGPT app is still to do. PEAR
  is still on 2.37.0; it moves to 2.38.0 only by another suspend and resume.

### Restart the operator, keeper or shelf from v2

A workbench session can restart the operator, keeper, broker and shelf with
`kubectl`, with no v1 pod. It can do nothing else to them: the only change it may
make is the restart annotation that `kubectl rollout restart` writes. An image or
an argument still goes through git. Run it from a workbench session (the standby
will do), not from a task.

Declare it first, so an alert from the restart is not taken for a fault:

```bash
declare-activity start "restart the v2 keeper" --scope dev-env-system,dev-agents --ttl 30m   # prints act-...
kubectl -n dev-env-system rollout restart deploy/dev-env-keeper
kubectl -n dev-env-system rollout status deploy/dev-env-keeper --timeout=300s
declare-activity end act-...
```

| What | Command |
|---|---|
| operator (2 replicas) | `kubectl -n dev-env-system rollout restart deploy/dev-env-operator` |
| keeper (1 replica, recreated, so it is gone for a couple of seconds) | `kubectl -n dev-env-system rollout restart deploy/dev-env-keeper` |
| broker (2 replicas) | `kubectl -n dev-env-system rollout restart deploy/dev-env-broker` |
| shelf (1 replica, in `dev-agents`) | `kubectl -n dev-agents rollout restart deploy/dev-env-shelf` |

Restart one at a time, and wait for `rollout status` before the next.

Every session pod keeps running. A restart never deletes or restarts a session;
the operator comes back and finds them again. The keeper keeps the logins on its
own volume, so its restart does not refresh or change them.

Proven from a session, 2026-10-20 between 03:45 and 04:01: a workbench session
restarted the keeper, the shelf and the operator. Every session pod kept its
name, start time and restart count, `agent-run list` was the same, the standby
stayed up, and `agent-run auth status` showed the same generation and expiry
before and after. The broker was only checked with a dry run. You have not done
it from the phone yet.

After a restart, check:

- `kubectl -n dev-env-system get deploy` shows the full READY count;
- `agent-run list` shows the same sessions as before;
- for the keeper, `agent-run auth status` shows the same generation and expiry,
  and `kubectl -n dev-env-system logs deploy/dev-env-keeper -c app --tail=30`
  shows the lease acquired and `claude auth state ready`.

**The shelf** holds rescues and archives, and the operator runs them there. Restart
it only when nothing is using it:

- the operator log shows no rescue or archive running, and the last one finished
  a few minutes ago:
  `kubectl -n dev-env-system logs deploy/dev-env-operator --since=10m | grep -i -E 'rescu|archiv'`
  (check both operator pods if need be);
- no hold pod is running: `kubectl -n dev-agents get pods | grep hold`;
- the archive is off today; once it is on, wait while any session is archiving.

A session that keeps retrying a rescue (about 80 ms every 15 minutes) is no
obstacle: restart between attempts. A restart during a copy loses nothing, but
the step fails and runs again a minute later, so wait rather than force it.

Two notes:

- To check what you may do, use `kubectl auth can-i <verb> <resource>
  --subresource=<log|exec|scale> -n <namespace>`. Written as `pods/log`, can-i
  answers yes when the real answer is no.
- If a restart is refused with `dev-env-workbench-admin-guard`, read the message:
  it names what else changed. Kyverno rewrites its own `kyverno.io/verify-images`
  annotations on an update; the guard allows exactly those (haynes-ops #4129).

You cannot read Secrets, exec into these pods, delete or scale them, or change
anything else about them from here. A pod that a restart does not fix is a git
change, or Tom.

## 6. The shared logins

The keeper, a small service in `dev-env-system`, holds one Claude Max login and
one ChatGPT login and refreshes them. Sessions get a short-lived access token,
never the refresh token. So sessions cannot break each other's login, and
nobody signs in per session.

- **Check.** In any v2 session, `agent-run auth status` shows each login's
  state (Ready or NeedsLogin), its generation and the time left on the current
  access token. Exit 0 is fine, 1 is expiring or expired, 2 means it cannot
  tell. Proven from a session.
- **Refresh is automatic.** The keeper renews the Claude access token at about
  two thirds of its 8-hour life. Proven at 00:29 on 2026-10-20: generation 2
  reached the session pods and every session stayed on the phone. Generation 3
  followed at 05:50, as expected, and sessions read it. The ChatGPT token is
  good until 2026-10-29, so its first automatic refresh has not come due yet.
- Headless Claude tasks do not use the keeper's login. They keep their own
  long-lived token, as in v1.
- **Renew the Claude login about once a month.** It lapses about 30 days after
  the last sign-in. Since 2.38.0, `agent-run keeper-login claude status` in a
  workbench session shows the date; `agent-run auth status` does not. On
  2026-10-20 it showed **2026-11-16 17:51 UTC**, two days before the 30-day
  estimate these notes gave. Nothing warns you yet (plan 13, story 13.1 adds a
  warning), so put it in your calendar.
- **Two ways to renew.** From a workbench session with `agent-run keeper-login`
  (below), or as before: a coordinator in the v1 pod starts the keeper's login
  helper and sends you one sign-in link in chat. Either way you open the link on
  your phone, sign in with your Max account and paste back the code the page
  shows. Finish within 15 minutes; after that the helper gives up and you start
  again with a fresh link. The link and the code never go into git or notes.
  The v1 route is proven on the phone (2026-10-19).
- **ChatGPT login.** Same shape, a device sign-in. It is needed only when the
  keeper reports NeedsLogin. It was renewed on 2026-10-19.
- **A turn that fails on a bad token.** agentd puts the keeper's token back in
  the pod within about 5 seconds, and the next turn works (proven from a session
  on 2026-10-20, Claude and Codex). Resend the failed message yourself. The
  automatic "continue" is meant for a turn that failed before the keeper
  published a new token. In 2.37.0 it did not recognise the Claude CLI's real
  error; 2.38.0 fixes that (#473). Proven from a session on 2.38.0, with a new
  token generation simulated: the session got the continue message and finished
  the prompt that had failed. A real keeper refresh setting it off has not been
  seen yet.

### Renew the keeper login from v2

**Status: installed** (2.38.0: dev-env #475, haynes-ops #4130 and #4131; the
Claude `cancel` fix in 2.38.1: dev-env #483, haynes-ops #4132 and #4133).
Proven from a session on 2026-10-20: `status` for both logins; `url` for both;
`url`, `cancel` and `url` again for both; a caller from the v1 pod refused; and
the keeper's logins unchanged after each abandoned try.

On 2.38.0 a Claude `cancel` left the keeper's helper running, so the next `url`
answered "held elsewhere" for up to 15 minutes. 2.38.1 fixes it: `cancel` now
runs a fixed command in the keeper, `agentd claude-login-abandon`, that stops the
helper. Proven from a session on 2.38.1: after `cancel` no helper was left, the
next `url` gave a fresh link, a `cancel` with nothing open answered "no keeper
login ceremony is open", and `status` did not change.

You have not run a full ceremony from v2 yet, with your own code, so keep the v1
route as the fallback until you have.

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
agent not to. If a code is wrong, expired or already used, nothing changes. Ask for
`cancel`, then a fresh `url`.

**From a terminal in the session** (the same commands the agent runs):

```bash
agent-run keeper-login claude status    # state, generation, when the access token
                                        # lapses and when the login itself lapses
agent-run keeper-login claude url       # opens a ceremony, prints the sign-in link
read -rs c; printf '%s' "$c" | agent-run keeper-login claude code   # paste the code
agent-run keeper-login claude cancel    # abandon a ceremony
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
route) still holds the keeper's one slot. For ChatGPT it expires by itself within 15
minutes. For Claude, `cancel` ends it too since 2.38.1 (built for this case; a slot
held elsewhere has not been tried live). The command reconnects by itself while the
operator's other replica answers.

After a ChatGPT `cancel`, `status` shows PendingPublication for about 25 to 35
seconds, then Ready again with the same login. That is expected and harmless: a
ceremony pauses the keeper's refresh, and the keeper republishes the login at its
next half-minute check. On 2.38.0 the keeper also logged an error for the pause;
2.38.1 no longer does (proven from a session: none logged).

The operator runs only the keeper pod's login helpers, and for Claude the
`claude-login-abandon` command. An admission policy, `dev-env-operator-exec-guard`,
refuses anything else it tries there: the keeper's main container, a shell, other
arguments.

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

- **A keeper login ceremony, until you have done one from v2.** The v2 route
  is installed and proven from a session, except the sign-in itself with your
  code (section 6). The Claude login lapses on 2026-11-16 at 17:51 UTC; the
  ChatGPT login is needed only when the keeper reports NeedsLogin.
- **A Proxmox write, until one is tried from v2.** The v2 operator token now
  reaches the session and `pve` runs at `tier=operator` (section 1), but only
  reads were run with it. v1's `pve`, with its own long-lived operator token, is
  the tested route for writes.
- **Headless Codex tasks on a repo outside the catalog** (section 3), such as
  haynes-quest or the private repos left out of it.
- **Cluster writes: pod deletes, `flux reconcile`, Jobs.** The workbench reads
  only (section 1). A default (`full` profile) session has the rights in RBAC
  but has not used them yet.
- **Retiring v1 itself.** That is your call.

More detail is in the [workflow guide](workflow-guide.md),
[plan 03](../.agents/sagas/distributed-dev-env/backlog/03-remote-control.md) and
[plan 13](../.agents/sagas/distributed-dev-env/backlog/13-phone-browser-management.md).
