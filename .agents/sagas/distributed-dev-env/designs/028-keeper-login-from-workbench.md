# DESIGN-028: renew the keeper login from a workbench session

Story NB-01, batch NEXT-BATCH-1020 (Saga85, Epic107). Source and unit tests
only until released, pinned and run live. It changes no ADR and no
accepted ruling. DESIGN-001 6.11 (identities), backlog 03 (increment 5, the
keeper-owned Claude ceremony) and D-78 (the Codex login helper) stay
controlling. The keeper's journal, refresh logic and published projection are not
changed.

## Problem

The keeper's shared Claude and Codex logins lapse (Claude about 30 days after each
login). Renewal ran as `kubectl exec -it deploy/dev-env-keeper -c claude-login`
from the v1 pod or Tom's laptop. v2 sessions cannot do it: no exec in
`dev-env-system`, and `agent-run login` is a client-side exec into a session pod,
not the keeper. Tom must be able to renew from a v2 workbench session, from his
phone, without v1.

## Decision

The operator does the exec, for the workbench only.

| Route (all under `/v1/keeper-login/{claude\|codex}`) | Does |
|---|---|
| `GET` | the keeper's state word (Ready, PendingPublication, NeedsLogin, Unavailable), generation, access expiry, login expiry. Times only. |
| `POST .../url` | opens a ceremony; the response carries the sign-in link (Codex: and the one-time device code). |
| `POST .../code` | Claude: body `{"code": ...}`, handed to the helper's terminal. Codex: no body; waits for the device sign-in to finish. 202 `Waiting` after 40 s, call again. |
| `DELETE` | abandons the open ceremony. |

CLI: `agent-run keeper-login claude|codex [status|url|code|cancel]`. The Claude code
is read from stdin, never an argument.

**Caller class.** Workbench only: a session whose pod runs as
`dev-agents/dev-env-workbench`, or that account's own unbound token (the two
shapes the Codex remote create rule already accepts). Tom's human account, the v1
pod, coordinators and every other session get 403 before the operator looks at the
keeper. (Tom can still exec himself with his own kubeconfig; this route adds no
path for him.)

**Where it execs.** Only the keeper pod's login container, named in a table in
code (`claude-login` running `agentd claude-login`, `codex-login` running
`dev-env-keeper codex-login`; status uses the existing `agentd claude-login-status`
and `dev-env-keeper codex-auth-status`). Never the `app` container, never a
container name from a request. The pod is found by the keeper's labels in
`dev-env-system`, and must be Running, Ready, not deleting, on the
`dev-env-keeper` ServiceAccount, with that container running. Zero or two matches
refuse. RBAC cannot scope exec to a container, a command or a generated pod name, so the
operator's Role in `dev-env-system` (`pods get, list`, `pods/exec create`, haynes-ops
`operator-keeper-login.yaml`) would on its own reach every pod there: the keeper's `app`
container (GitHub App key, SSH CA, the login journal's ServiceAccount) and the broker's.
The review (NB-01) made the pin a blocker, so the same haynes-ops PR adds a
ValidatingAdmissionPolicy, `dev-env-operator-exec-guard` (`operator-exec-guard.yaml`):
for the operator's ServiceAccount in `dev-env-system`, only `pods/exec` into pods named
`dev-env-keeper-*`, containers `claude-login` or `codex-login`, with exactly the five
helper argv arrays of the provider table; attach, port-forward and proxy are refused.
It is proven on kube-apiserver 1.35.0 (envtest) and matches no exec the operator makes
today (rescue, log, deliver and the shelf run in `dev-agents`, tool pods in `dev-tools`).
The operator's code fixes the same container and commands, and the route's caller class
is the first limit. The two lists must change together: the guard's arrays equal
`keeperLoginProviders`. `dev-env-exec-guard` and `dev-env-identity-guard` match
`dev-agents` identities only and do not apply. `dev-env-system` holds the operator,
broker and keeper pods and Job pods such as the h7 metadata audit (the shelf runs in
`dev-agents`).

**One long exec across two requests.** `claude auth login` prints a link and then
waits for the code in the same process, and the helper insists on a terminal (the
proven shape). So `url` starts the helper on a pseudo-terminal exec and the
operator keeps that exec open in memory until `code` or `cancel`. That state is
per replica, and the operator has two replicas behind one Service, so **only the
leader holds ceremonies**. A follower answers `503 not_leader` having done
nothing, and `agent-run` closes its connection and sends again (a new connection
may land on the leader; 12 tries). Status holds no state and any replica answers.

**Secrets.** The link, the Codex device code and the pasted code are secrets for
the ceremony's life. The operator reads only brace-led JSON lines of the helper's
fixed shape off the terminal and drops everything else unread (the terminal echoes
the typed code). The link is checked against the provider's host again before it
is returned. The code is written to the terminal, kept nowhere, and not echoed in
any answer. No error carries helper output: failures are the keeper's fixed words.
Logging is the existing one line per request (method, path, status, caller class).
The operator reads and writes no Secret and no keeper state: it has no Secret
rights, and a test fails on any Secret read or any write by its client.

**Abandon (NB-01b).** Closing the exec stream does not hang the helper up (live,
LIVE-238a D1: after `cancel` the helper and the provider's CLI stayed alive and the
keeper's slot stayed held to the 15-minute deadline), and a Ctrl-C does not end the
Claude CLI either. So Claude's cancel is a second, fixed, short exec in the same
`claude-login` container: `agentd claude-login-abandon`. It finds the running
`agentd claude-login` helpers by their exact argv, sends each SIGHUP (which the helper
treats as a cancel: it kills the CLI's process group, cancels the keeper's attempt and
wipes the staging), waits up to 8 s for them to leave and prints one fixed word
(`Abandoned`, `NoCeremony`, `Stuck`). It reads no credential and no socket, and
no helper running is a success, so it is idempotent. The operator runs it before it
cuts the exec; it also runs it for a `cancel` that finds no ceremony in memory
(restart, leader change, the v1 route's ceremony), which is how a "held elsewhere"
slot is freed. Codex has no abandon command: its Ctrl-C works live, and its cancel is
unchanged. The new argv is in `keeperLoginProviders` and in the haynes-ops
`operator-exec-guard.yaml`; a unit test pins the table so the two cannot drift. A
cancel that races a code already being adopted can still end with the new login
adopted, whole (the keeper's adoption is atomic); `status` shows the truth. An operator
restart or leader change drops the exec; `cancel` (or the keeper's 15-minute expiry)
frees the slot.

A ceremony that never reaches `code` leaves the journal and the
published projection exactly as they were: adoption is the only write and it
needs a code.

**Keeper change: expiry in status.** `claude-login-status` and
`codex-auth-status` print only a state word and a generation, but the story wants
expiry. The keeper's control `status` answer gains `accessExpiresAt` and
(Claude, when the provider said) `loginExpiresAt`, times only, from the journal it
already loaded. A keeper from before this change omits them and `status` says so.
Strict decoders on both sides mean the operator, keeper and helper images must
move together (the pin PR moves the keeper `app` and both login containers in one
rollout); a newer keeper with an older `claude-login` image reads `Unavailable`
until the helper image follows.

## Limits

- One ceremony per provider; none survives an operator restart. Run `url` again.
- Handlers answer inside the API's 60 s write timeout: the link wait is 45 s (a
  timeout answers first and abandons the ceremony in the background), the code
  wait 40 s plus at most 10 s to write, and status 20 s; the CLI client allows 75 s.
- Codex's device flow has no code to paste. `code` is "wait for the sign-in to
  finish"; if you prefer, run `status` until the generation moves.
- The Claude CLI's behavior on this exec shape (terminal for stdin, CLI stdout
  piped by the helper) is the one the keeper ceremony already ran live
  (backlog 03 increment 5); only the transport in front of it is new.

## Acceptance

Source and tested: `internal/apiserver/keeper_login_test.go` (403 for the human,
the v1 pod, a stranger and an ordinary session; status members; the link only in
the response and never in a log; the code on stdin byte for byte; Codex device
flow; abandon with no write and no Secret read; no-link, busy, refused and
waiting outcomes; pod selection; no canary in any body, log or command line),
`internal/keeper/claude_login_status_test.go` (an abandoned ceremony leaves the
journal and projection alone; expiry reported), `internal/agentrun/keeperlogin_test.go`.
Installed and usable: after release, the haynes-ops RBAC, and a pin that moves the
operator and the keeper together: from a disposable workbench session,
`keeper-login claude status` and `codex status` work, `url` returns a link and an
abandoned `cancel` leaves `status` as it was. A full ceremony with Tom's code is a
separate step with Tom.
