# Dev-env v2 quick start

How to use v2 day to day, mostly from your phone: Claude and Codex sessions,
headless tasks, network and Proxmox access, stopping and resuming, messages
between sessions, upgrades and restarts, the shared logins, where notes live,
and what still needs v1.

State on 2026-10-20, 23:00 UTC: release 2.39.0 is installed everywhere: the
operator, the keeper, the templates and the task worker (haynes-ops #4140). The
standby and one other idle session moved onto it by themselves; the Codex host
did not (it moves only by suspend and resume, section 5). Since haynes-ops #4141
(22:41 UTC) a workbench session has v1's cluster write tier too (section 1). The
v1 cutover is planned for 2026-10-21, after the v1 sessions wind down, and only
with your written approval; the date alone authorizes nothing (section 8). Each result below carries one of four
proof levels:

- **Proven on the phone:** you did it end to end on your phone.
- **Proven from a session:** an agent did it live in a v2 workbench session or
  from the v1 coordinator, not on your phone.
- **Installed, not exercised:** in the installed release, never run live.
- **Not built:** no code yet, or the code is off on the installed system.

Dates are UTC.

## Which `agent-run`

- In a v2 session, `agent-run` is the v2 client. There is nothing to set up.
- In the v1 pod, `agent-run` is still v1's launcher. The v2 client is a separate
  binary. Do not overwrite the v1 launcher. Select a v2 executable that is
  already present in that environment, store its path in a shell variable, and
  record its exact version before using it:

  ```bash
  v2=/path/to/an/already-installed/agent-run-v2
  "$v2" version
  ```

  The older `agent-run-v2.35.0` example was a historical filename, not an
  install or release-asset contract. This guide does not establish where to
  download a v2 binary; use a release-approved binary supplied to the caller.
  The examples below say `agent-run`; in the v1 pod, type `"$v2"` instead.

  API access is discovered from the caller. A v2 session uses its projected
  API identity; an already-authorized in-cluster caller such as v1 uses its
  own ServiceAccount identity. If that caller needs the operator CA supplied
  explicitly, set `DEV_ENV_API_CA_FILE` (or pass `--ca-file`) to the approved
  PEM file; the CA is trust material, not an authorization token. A laptop
  path uses its existing authorized kubeconfig and a temporary local
  port-forward. None of these paths is a general new laptop-login ceremony.
  Use `agent-run --help` from the selected binary for its current discovery
  and precedence rules. You need the v1 pod only for the few jobs in section 8.

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
- A fault, now fixed: on 2026-10-20 the operator made a second standby right
  after the standby moved to a new version, twice (2.38.0 at 05:31 and 2.38.2
  at 07:47). Both duplicates were reaped. The first fix (#480, 2.38.1) was not
  enough: just after a move, the new pod still reports the old pod's ended
  agent for about a minute. 2.38.3 fixes that (#489). Proven from a session:
  the standby moved to 2.38.3 at 08:23 and no second one appeared through
  08:31.

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
  shared login. Proven on the phone on 2026-10-20: you steered the standby on
  Wi-Fi (about 12:09) and on cellular, off the home network (about 12:11).

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
- change the cluster as v1 does (since haynes-ops #4141, below): pod delete,
  exec, `rollout restart`, `flux reconcile` and suspend, Jobs, CronJob suspend,
  and PVC delete in `database`;
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

**The cluster write tier** (haynes-ops #4141, merged 2026-10-20 at 22:41). A
workbench session holds exactly v1's OPERATOR tier, no more:

- pod delete, exec into pods, and `services/proxy` and `pods/proxy`;
- the restart patch on Deployments, StatefulSets and DaemonSets;
- patches on Flux objects (`flux reconcile`, `flux suspend`) and ExternalSecrets;
- Job create and delete, and CronJob patches (suspend);
- PVC delete in `database`; PVC create and delete and StatefulSet delete in
  `observability` (v1's migration grant).

It still cannot read Secrets, change RBAC or admission policies, scale or delete
Deployments, or delete PVCs anywhere else. Deploys still go through git.

Proven from a session, 2026-10-20 between 22:42 and 22:48, in a fresh workbench
session: every `kubectl auth can-i` answer matched v1, yes and no; `flux
reconcile ks dev-env-rbac` applied the merge; a Job ran, then its pod and the Job
were deleted; the shelf restarted with `rollout restart`; exec into a pod worked;
and the guard refused a replica change and `kubectl set env`. Not proven live:
the `restartedAt` restart of an ordinary app (in `media`) and a `create job
--from=cronjob` were server dry runs only (no safe CronJob was named), and no PR
merge from v2 was run (a draft PR was opened and closed). The first PR merge from
v2 after the cutover is the live merge proof.

What to know (the review's follow-ups F1 to F7):

- **F1.** A task or session started from a workbench session runs on the
  workbench profile, with this tier, as v1's tasks shared v1's identity. A
  narrower worker profile is not built.
- **F2.** As in v1, Job create and Flux patches can reach cluster-admin for
  someone who sets out to. The workbench has exact v1 parity (#4141), including
  exec into `dev-env-system` pods, so the earlier condition that exec into the
  keeper stay impossible is not met. A fence around `dev-env-system` would
  reduce today's parity rights. It is held for your decision, and it is not a
  cutover prerequisite.
- **F3.** Stricter than v1: the guard allows only the restart annotation on
  workloads. So no `rollout undo`, `pause` or `resume`, and no `set image`, `set
  env` or `set resources`. A break-glass rollback is your admin kubeconfig.
- **F4.** The exec guard's list of privileged identities does not name the
  workbench yet. It has no effect today.
- **F5.** The archive guard refuses exec into the shelf pod. Restarting the shelf
  still works.
- **F6.** `kubectl delete pod` on a session pod skips the rescue that `agent-run
  reap` and `suspend` do. Use those for sessions.
- **F7.** A comment in haynes-ops' session templates still says the workbench
  has exec in `dev-agents` only.

A default (`full` profile) session has the write tier in RBAC too, outside the
dev-env namespaces and within `dev-env-agent-guard`'s limits (checked on
2026-10-19), but nobody has used it from v2.

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
# Reads work since 2.38.1, a write was proven on 2.39.0; it needs the
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
- A `full`-profile haynes-ops session (and its haynes-ops children) can instead lease the same
  shared key for up to an hour (D-88, D-89). Ask for both grants without waiting, then attest
  each after your coordinator (or the user) approves:

  ```bash
  agent-run grant request --type credential --credential hw-ssh --ttl 1h --no-wait \
    --reason "why you need the shared hw-ssh key"
  agent-run grant request --type egress --fqdn haynesintelligence.haynesnetwork --port 22 \
    --ttl 1h --no-wait --reason "ssh to haynesintelligence for the hw-ssh lease"
  # once per grant. Seek your coordinator's approval first and attest it:
  agent-run grant attest <grant> --approved-by coordinator:<session-name> --ref <id>
  # only where the coordinator cannot approve and the user did:
  agent-run grant attest <grant> --approved-by user --ref <id>
  ```

  The lease appears up to about 15 s after the grant shows Active (the keeper polls every
  15 s; 10 s was observed in the pilot): wait for the key file
  (`$XDG_RUNTIME_DIR/dev-env-hwssh/<grant>/key`), not only the phase. It is the one shared
  root-level key: anything in the pod can copy it while installed, and removing it at expiry or
  release does not revoke a copy.
- `hw-ssh` has no `gasha01` entry, in v1 or v2: nobody has confirmed which user
  may log in there. Once that is known, `hw-ssh --raw <user>@gasha01.haynesnetwork`
  works under the grant above.
- Proven from a session (a fresh workbench session): internal ingress
  (Grafana's health page answered 200), `pve nodes` and `hw-ssh twin-top
  hostname`, each approved by policy with no phone step.
- **The Proxmox operator token works: reads since 2.38.1, a write proven on
  2.39.0.** Reads, proven from a session on 2026-10-20 (06:54 to 06:56 UTC), in a
  workbench session:
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

  A write, proven from a session on 2026-10-20 at about 22:40, in a fresh
  workbench session holding both grants and a `declare-activity`: `pve --yes vm
  113 onboot 1` set talosw03's onboot flag to the value it already had. `pve`
  sent the PUT at `tier=operator`, the VM kept running, and the token was gone
  from Proxmox about 20 seconds after the release. After the credential grant
  turns Active, wait a few seconds and check that `pve ha` says `tier=operator`
  before you write.
- Before 2.38.1 no token ever reached a session; earlier notes here that said a
  token was issued were wrong. In 2.37.0 the keeper's SSH client failed the
  nodes' host-key check (fixed in 2.38.0, #474). In 2.38.0 the keeper misread
  Proxmox's answer and deleted each new token at once (fixed in 2.38.1, #481).

### A terminal instead of code-server

v2 has no code-server, by your ruling of 2026-10-20
([decision 78](../.agents/sagas/distributed-dev-env/README.md#decision-log)). A proper
front end comes later (plan 13). For a terminal, exec into a session pod from a
machine with an admin kubeconfig, or from a workbench session:

```bash
kubectl -n dev-agents exec -it <name> -c agent -- bash   # a shell in the session
agent-run attach <name>                                   # the agent's own TUI
```

The shell runs as the session's user, in its home. Leave the TUI with tmux's
`Ctrl-b d`, or `agent-run detach <name>` from elsewhere. For a terminal of your
own, start a `--local` session and reap it when you are done.

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
  On 2026-10-20 at about 12:14, over cellular, PEAR showed up as the same
  computer after its image move, a new turn worked, and it recalled its
  history.
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
- `--repo` names an ordinary repository; it does not select an accepted project
  snapshot, Kubernetes profile, or native-thread profile. `--project` asks the
  server to select a declared project's repository and rules for a **new
  headless task**. It cannot be combined with `--local` or `--interactive`.
  The server, rather than this flag, remains authoritative for catalog
  admission. `--profile` is separate: a child task inherits its parent's
  profile. Starting a native thread does not choose a profile.
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
  still knew its earlier answer. On 2026-10-20 the standby and one other session
  moved the same way to 2.38.0, 2.38.2, 2.38.3 and 2.39.0 (seen from a session;
  you used the standby on the phone after its move to 2.38.3). A session that has
  never had a turn stays put until it has one.
- **Codex host:** never moves by itself. `agent-run list` marks it `outdated`.
  At a quiet moment run `agent-run suspend <name>`, then `agent-run resume <name>`.
  The pairing stays. Proven from a session on 2026-10-20: the host PEAR moved
  from 2.35.0 to 2.37.0 this way. Same pairing, the daemon reconnected, and the
  thread history was intact. Proven on the phone too, on 2026-10-20 (section 2).
  PEAR is still on 2.37.0; it moves to 2.39.0 only by another suspend and
  resume.

### Restart the operator, keeper or shelf from v2

A workbench session can restart the operator, keeper, broker and shelf with
`kubectl`, with no v1 pod. On their Deployments the only change it may make is
the restart annotation that `kubectl rollout restart` writes. An image or an
argument still goes through git. Run it from a workbench session (the standby
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
before and after. The broker was only checked with a dry run. At about 12:20 a
workbench session restarted the keeper again, at your request, with the same
result, and on 2026-10-20 at about 22:45 a fresh workbench session restarted the
shelf (story CR-1020).

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
  --subresource=<log|exec|scale> -n <namespace>`. Written as `pods/log` or
  `pods/exec`, can-i can give the wrong answer.
- If a restart is refused with `dev-env-workbench-admin-guard`, read the message:
  it names what else changed. Kyverno rewrites its own `kyverno.io/verify-images`
  annotations on an update; the guard allows exactly those (haynes-ops #4129).

You cannot read Secrets, scale these Deployments or change anything else about
them from here. Since haynes-ops #4141 you can delete their pods and exec into
them, as from v1 (section 1), but a restart is the safer tool: the keeper,
operator and broker images are distroless, so exec runs only their own binaries.
A pod that a restart does not fix is a git change, or Tom.

## 6. The shared logins

The keeper, a small service in `dev-env-system`, holds one Claude Max login and
one ChatGPT login and refreshes them. Sessions get a short-lived access token,
never the refresh token. So sessions cannot break each other's login, and
nobody signs in per session.

- **Check.** In any v2 session, `agent-run auth status` shows each login's
  state (Ready or NeedsLogin), its generation and the time left on the current
  access token. Exit 0 is fine, 1 is expiring or expired, 2 means it cannot
  tell. Proven from a session. Since 2.39.0 it also shows when the Claude login
  lapses (see "Lapse date and notices" below).
- **Refresh is automatic.** The keeper renews the Claude access token at about
  two thirds of its 8-hour life. Proven at 00:29 on 2026-10-20: generation 2
  reached the session pods and every session stayed on the phone. Generation 3
  followed at 05:50, as expected, and sessions read it, and generation 4 by
  12:13, when you checked the status on your phone. The ChatGPT token is good
  until 2026-10-29, so its first automatic refresh has not come due yet.
- Headless Claude tasks do not use the keeper's login. They keep their own
  long-lived token, as in v1.
- **Renew the Claude login about once a month.** It lapses about 30 days after
  the last sign-in. `agent-run keeper-login claude status` in a workbench session
  shows the date. On 2026-10-20 it showed **2026-11-16 17:51 UTC**, two days
  before the 30-day estimate these notes gave. Since 2.39.0 the keeper warns on
  your phone from 7 days before, and `agent-run auth status` shows the date
  ("Lapse date and notices", below). The first warning is due about 2026-11-08.
- **Two ways to renew.** From a workbench session with `agent-run keeper-login`
  (below), or as before: a coordinator in the v1 pod starts the keeper's login
  helper and sends you one sign-in link in chat. Either way you open the link on
  your phone, sign in with your Max account and paste back the code the page
  shows. Finish within 15 minutes; after that the helper gives up and you start
  again with a fresh link. The link and the code never go into git or notes.
  The v1 route is proven on the phone (2026-10-19). It goes away with v1 at the
  cutover; after that only the v2 route is left.
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

You have not run a full ceremony from v2 yet, with your own code. On 2026-10-20
you checked `status` on the phone and chose to skip the full renewal, so the
first real one is the renewal due before 2026-11-16. Until the cutover the v1
route stays as a fallback.

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

After a ChatGPT `cancel`, `status` shows PendingPublication for about half a
minute or a little more (25 seconds once, over 35 the next time), then Ready
again with the same login. That is expected and harmless: a
ceremony pauses the keeper's refresh, and the keeper republishes the login at its
next half-minute check. On 2.38.0 the keeper also logged an error for the pause;
2.38.1 no longer does (proven from a session: none logged).

The operator runs only the keeper pod's login helpers, and for Claude the
`claude-login-abandon` command. An admission policy, `dev-env-operator-exec-guard`,
refuses anything else it tries there: the keeper's main container, a shell, other
arguments.

### Lapse date and notices

**Status: installed and accepted** (plan 13, story 13.1: dev-env #495, release
2.39.0; haynes-ops #4138 and #4140). Proven on 2026-10-20: the keeper logged
"notifier enabled", Prometheus shows both gauges (the Claude lapse is
2026-11-16 17:51:36 UTC), `auth status` and `keeper-login ... status` in a
workbench session show the date (Unknown for ChatGPT), and the test notice
below reached your phone at about 17:45 under the dev-env app.

**The date.** The keeper publishes the Claude login's lapse date next to the
access token, in a small non-secret file, `login.json`, in the same projection
Secret. Sessions that still run an older agent ignore it. Two commands show it:

```bash
agent-run auth status                    # in any v2 session
agent-run keeper-login claude status     # in a workbench session
```

`auth status` adds `login lapses <date> (N days left)` to the Claude line. N is
the time left in whole days, rounded down (7 days 23 hours is 7). Both commands
exit 1 when N is 7 or fewer, or when the login has lapsed, and print how to
renew. The phone notice counts days the same way. When the keeper has no date they print `Unknown`; they never
guess one, and an Unknown date does not change the exit code. The ChatGPT login
has no known lapse date, so it is always Unknown.

**The notices.** The keeper sends them straight to the "dev-env" Pushover
application, not through Alertmanager. Each links to `https://claude.ai/code`,
the session list where the standby shows on your phone.

- *Claude login lapses in N days* (normal priority). Once N (whole days, rounded
  down) is 7 or fewer, at most one a day, only between 09:00 and 21:00 New York
  wall-clock time (also on the days the clocks change), until the login is
  renewed. Renewal moves the lapse date later and ends the warnings.
- *Keeper needs login (claude or codex)* (priority 1, any hour). Once when a
  login reaches NeedsLogin, with the same renewal steps in the message. One
  *Keeper login restored* notice (normal priority) when it leaves NeedsLogin.

A keeper restart repeats nothing: the keeper remembers what it sent in a
ConfigMap, `dev-env-keeper-notices`, which holds only times and incident ids.
If that ConfigMap is missing, forbidden or corrupt, the keeper still sends,
keeps its record in memory, logs the reason once and tries the ConfigMap again
every minute; only a restart during that time can repeat a notice. After a crash
between a send and the save, one notice can repeat. If Pushover refuses a notice
with a 4xx other than 429 (a wrong token or user key), the keeper logs it once
and stops sending that notice until it restarts or the situation changes (a new
incident, a later lapse date); 429 and 5xx are retried every 5 minutes.
If the Pushover files are missing the keeper logs "notifier disabled: no
Pushover credentials" once, sends nothing, and keeps the gauges.

**The gauges** (the keeper's metrics port, 8080, for dashboards):

- `dev_env_keeper_login_state{provider,state}`: 1 for the current state
  (`Ready`, `NeedsLogin`, `LoginInProgress`, `Unknown`) and 0 for the others.
- `dev_env_keeper_login_lapse_timestamp_seconds{provider}`: Unix time of the
  lapse. Only Claude has one, and only while a date is known; there is no series
  at all when it is not.

**Send a test notice.** One fixed notice, "dev-env test notice", to check the
phone path. Run it from the v1 pod, or from a workbench session, which has the
same exec right since haynes-ops #4141 (not yet run from there):

```bash
KUBECTL_REMOTE_COMMAND_WEBSOCKETS=false kubectl exec -n dev-env-system \
  deploy/dev-env-keeper -c app -- /usr/local/bin/dev-env-keeper notify-test
```

(The setting makes `kubectl` use the older exec protocol, which needs only the
exec right that v1 and the workbench hold.)

It prints `sent`, or an error that names no credential. The keeper image is
distroless, so there is no shell: that one binary is all it runs.

### Look up the pages the keeper sent

**This lists keeper pages only. It is not all Pushover history.** The keeper
writes each page it sends into its notice ConfigMap before it sends, then the
outcome. `agent-run pages` reads that record. It is offline: it calls no API and
sends nothing. Any session can read the ConfigMap.

```bash
kubectl -n dev-env-system get cm dev-env-keeper-notices -o json | agent-run pages list
kubectl -n dev-env-system get cm dev-env-keeper-notices -o json | agent-run pages list --state ambiguous --since 7d
kubectl -n dev-env-system get cm dev-env-keeper-notices -o json | agent-run pages show 'critical:claude:1790000000'
```

Each page shows its time in UTC and in New York, sender and source, title and
body, key and correlation, link, the number of attempts and Pushover's request
id. `-o json` prints the same. `--from-file` takes a saved copy.

States: *recorded* is written before the send and the outcome was not stored (a
crash, or a send in flight). *sent* means Pushover answered HTTP 200 with
status 1: it queued the page, which is not proof the phone received it.
*failed* is a definite 4xx, or no request made. *ambiguous* is a timeout, a
transport error, a 5xx or an unconfirmed 2xx: the page may have reached Tom.

Coverage is printed on every answer. Only the keeper records, and only from the
first keeper image that has this code. Alertmanager, Gatus, upgrade-gate,
alert-responder, dev-env-ops, the v1 pod, haynesnetwork, scribe-notes and Home
Assistant are not covered, and older pages are Unknown. A page that is not in
the record is Unknown, never "no page". The record keeps the newest 100 pages
(256 KiB) and says how many it dropped. A link or text that could carry a
credential is stored as `REDACTED`. Design: DESIGN-031.

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
- The v1 pod does not mount `~/.shared`. Until the cutover, v1's notes stay in
  its own `~/work/orders`. At the cutover they are copied, with each repo's
  auto-memory and bundles of v1's local-only branches, into
  `~/.shared/v1-home/` (`work-orders/`, `memory/`, `bundles/`, `manifests/`).
  The memory is then merged into `~/.shared/memory/<name>`, so v2 sessions
  load it; nothing there is overwritten. The steps are plan 05's
  [cutover runbook](../.agents/sagas/distributed-dev-env/backlog/05-cutover.md#cutover-runbook-2026-10-21).

## 8. What still needs v1

Since 2026-10-20 almost nothing. What is left:

- **A keeper login ceremony with your code, until you have done one from v2.**
  The v2 route is installed and proven from a session, except the sign-in
  itself (section 6). You skipped the full renewal on 2026-10-20, so the first
  real one is the renewal due before 2026-11-16 17:51 UTC. After the cutover
  the v2 route is the only one.
- **Headless Codex tasks on a repo outside the catalog** (section 3), such as
  haynes-quest or the private repos left out of it. Or use a Codex host on
  your phone, which works on any repo, or add a public repo to the catalog.

Closed on 2026-10-20 (story CR-1020):

- **A Proxmox write.** Proven from a session at about 22:40 (section 1).
- **Cluster writes: pod deletes, exec, `flux reconcile`, Jobs, restarts.** A
  workbench session has v1's OPERATOR tier since haynes-ops #4141, proven from a
  session (section 1, with follow-ups F1 to F7), except an ordinary-app restart
  and a Job from a CronJob, which were server dry runs only. A PR merge from v2
  has not been run yet.
- **A Claude session or task on a repo outside the catalog.** `agent-run --repo
  <name>` clones a repo under your GitHub account whether or not it is in the
  catalog. A task on the public `ytdrivarr` succeeded. A private repo outside
  the catalog was not tried; it uses the same bot token that works for the
  private catalog repos.

Goes away with v1, by your rulings of 2026-10-20:

- **code-server.** Use a terminal in a session pod (section 1, "A terminal
  instead of code-server"). A proper front end comes later (plan 13).
- **The two Cursor "My Machines" workers.** They go offline and are not moved.

**Retiring v1.** Planned for 2026-10-21, after the v1 sessions wind down and
write handoffs for new agents. It needs your written approval and your go at
that moment. The steps, the drain and the rollback are plan 05's
[cutover runbook](../.agents/sagas/distributed-dev-env/backlog/05-cutover.md#cutover-runbook-2026-10-21).
v1's volume is kept for at least 30 days, so a rollback is a revert.

More detail is in the [workflow guide](workflow-guide.md),
[plan 03](../.agents/sagas/distributed-dev-env/backlog/03-remote-control.md),
[plan 05](../.agents/sagas/distributed-dev-env/backlog/05-cutover.md) and
[plan 13](../.agents/sagas/distributed-dev-env/backlog/13-phone-browser-management.md).
