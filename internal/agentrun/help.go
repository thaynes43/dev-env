package agentrun

// The help texts. Tom and agents both read them, so each says what a command
// does, what it needs and what it prints, in plain words.

const usageText = `agent-run starts and manages dev-env v2 agent sessions through the operator's API.
Each session is its own pod on a worker node.

Usage:
  agent-run [--repo] <repo> -p "<task>" [flags]   start a task session
  agent-run --project <project> [--repo <repo>] -p "<task>" [flags]
                                                  start a task in a declared project
  agent-run [--repo] <repo> --local [flags]       start an interactive session (a TUI)
  agent-run list [--repo <r>] [--state <s>] [--mine]
                                                  list sessions, newest first
  agent-run rc start|status|fresh -o json         launch or observe Remote Control
  agent-run coordinator start|status|stop|brief   run bounded work on a tracker item
  agent-run thread start --agent <provider> ...   start a fresh phone-visible thread (Codex on a host, Claude as a session)
  agent-run inbox send|ask|reply|list|read|ack|ack-status
                                                  durable inbox
  agent-run instruction send|read|checkpoint|consume|address|attempt designated parent delivery and native consumption
  agent-run directory list --project <p> --once [-o json]
                                                  read one authorized directory page
  agent-run canary create --id <id> --once [-o json]
                                                  create or report a trusted fixed original
  agent-run canary result --id <id> --session-uid <uid> --context <name> --json --once
                                                  read one retained fixed publication
  agent-run show <name> [--captured-usage -o json]
                                              show one session; --captured-usage adds direct-child receipts
  agent-run usage -o json [--from-file FILE|-]  offline allowance read
  agent-run usage-export --manifest <path> --output-dir <private-dir> [--allowance-read <path>]
                                              export selected usage evidence privately
  agent-run log <name> [--tail N]                 print the end of a session's log
  agent-run msg <name> "<text>"                   send a message into a session's TUI
  agent-run reap <name>...                        end sessions: rescue and archive, or stop a private one
  agent-run suspend <name>...                     rescue and stop sessions, keeping their volumes
  agent-run resume <name>                         start a suspended session again, conversation intact
  agent-run rescue list [--session <name>]        list the rescues on the shelf
  agent-run rescue restore <session>/<stamp> (--local | -p "<task>")
                                                  start a session that fetches a rescue
  agent-run history list [--project <p>]          list archived Session histories by project
  agent-run history show <uid>                    show one archived history's manifest
  agent-run history read <uid> <root/path>        print an archived file, a page or --all
  agent-run grant list [--session <s>] [--phase <p>] [--mine]
                                                  list access grants, newest first
  agent-run grant show <grant-id>                 show one access grant
  agent-run grant request --type <t> --reason <why> [--no-wait]
                                                  ask for a grant; waits up to 30m unless --no-wait
  agent-run grant release <grant-id>              ask the broker to end one grant
  agent-run grant attest <grant-id> --approved-by user|coordinator:<ref> --ref <id>
                                                  record your claim that you obtained approval
  agent-run grant decide <grant-id> --grant-uid <uid> --digest <digest> --ref <ref> [--approve] [--ttl 10m] [--reason TEXT]
                                                  an allowlisted delegate's decision; not a human decision
  agent-run grant use <grant-id>|default         select that kube context in this pod
  agent-run budget create <task-uid> --success "RESULT" [flags]
                                                  create the host's assigned finite campaign
  agent-run budget show <task-uid>               inspect retained campaign authority
  agent-run mcp                                   access and asks tools on stdin for the session
  agent-run pages-mcp [--source-config FILE]      read-only current snapshot tools on stdin
  agent-run breakglass --reason <why>             ask for break-glass; Tom still approves it
  declare-activity start "<what>" --scope <a,b>   declare dev work, so it is not taken for a fault
  agent-run maintenance check --input <file>      offline correlation check; a candidate is not alert deferral
  agent-run pages list|show [--from-file F|-]     look up KEEPER pages only (not all Pushover); offline
  agent-run remind create|list|show|edit|cancel   durable reminders: Pushover when due (needs the reminders API)
  agent-run schedule create|list|show|edit|cancel|approve|retry
                                                  agent work at a set time, headless or interactive; approve
                                                  and retry are Tom's (needs the scheduled-work API)
  agent-run attach <name>                         attach to a session's TUI (Tom only)
  agent-run detach <name>                         detach every client from it (Tom only)
  agent-run login <name> [url|code|status]        set up an --interactive session's Claude login (Tom only)
  agent-run pair <name>                           print a phone pairing code for a Codex --interactive session (Tom only)
  agent-run auth status [-o json]                 show the shared Claude and Codex logins' state and expiry
  agent-run keeper-login claude|codex [status|url|code|cancel]
                                                  renew the keeper's shared login (workbench only)
  agent-run fleet [--resources]                   show what runs and waits; --resources adds the bound Pod spec
  agent-run version                               print the version
  agent-run help [<command>]                      print help for agent-run or one command

agent-run run ... is the same as agent-run ... -p, as in v1. prune and sweep are
gone, because the operator reaps sessions itself.

Every command but version, help, maintenance, pages, and pages-mcp takes these flags:
  -o, --output json       print the API's answer as JSON (list and run also take -o name)
  --api-url <url>         the API's https:// address (or DEV_ENV_API_URL)
  --token-file <path>     a file holding a token for the audience dev-env-operator
                          (or DEV_ENV_API_TOKEN_FILE)
  --ca-file <path>        PEM CA certificates to trust for the API, besides the
                          system's (or DEV_ENV_API_CA_FILE)
  --kubeconfig <path>     the kubeconfig for laptop access and attach/detach
  --context <name>        the kubeconfig context to use for every Kubernetes call

Finding the API. In a session pod agent-run uses the pod's projected token and
AGENTD_API_URL. In any other pod of the cluster, such as the v1 dev-env pod, it
mints a ten-minute token for the pod's own ServiceAccount and calls
` + DefaultAPIURL + `.
On a laptop, kubectl and a reachable admin kubeconfig supply access. agent-run
reads the pinned CA, mints a ten-minute dev-env-human token in memory and opens
a loopback port-forward. TLS checks the operator's service name. The forward
ends with the command. --kubeconfig and --context select the cluster; explicit
--api-url, --token-file and --ca-file settings still take precedence.
In a pod, kubeconfig/context selectors need an explicit API URL and token;
automatic pod discovery refuses them. With a manual API endpoint, select the
kubeconfig context for that same cluster before attach or detach.

Exit codes:
  0  done
  1  failed: the API refused the request, or the session failed to start
  2  usage error: nothing was sent
  3  not authenticated or not allowed: no credentials, a 401 or a 403
  4  no such session
  5  try again later: a limit was reached (429), or the API was unavailable
  6  unconfirmed: a one-shot request (msg, canary create or a --once read) was
     sent, got no confirmed answer and was not repeated; also a retried grant
     decision ending in 409 after an uncertain earlier attempt. A message or create
     may have taken effect: observe it, do not repeat it. A one-shot request
     exits 5 only when nothing was sent
  7  rc accepted/pending, timed out with a handle, or owner action required
`

var commandHelp = map[string]string{
	"coordinator": `Usage: agent-run coordinator start|status|stop|brief

Start a bounded task on an OpenProject item, read its state and retained outcome,
or stop it. brief prints the embedded instructions offline. Use help coordinator
start, help coordinator status, or help coordinator stop for flags.
`,
	"coordinator start": `Usage: agent-run coordinator start --item ID (--repo R | --project P) --model FULL_ID --hours H --workers N [--run LABEL] [-o json]

Start a dev-profile task; no tracker credential needed. Model is required;
effort is preset. Hours and workers: 1 to 4. Repeating an item returns its
retained run. Use a new --run label for a deliberate second run.
`,
	"coordinator status": `Usage: agent-run coordinator status NAME [-o json]
agent-run coordinator status --item ID [--run LABEL] [-o json]

Read state, task verdict and retained outcome: summary, PR links and head SHAs.
Item lookup finds the caller's run. JSON is one object with item and session.
`,
	"coordinator stop": `Usage: agent-run coordinator stop NAME [-o json]

Reap one session through the existing API. The operator preserves work under
the session's rescue and retention rules. This requests a stop; it does not
wait for completion. JSON is one Session document.
`,
	"coordinator brief": `Usage: agent-run coordinator brief [--item ID --hours H --workers N]

Print the embedded coordinator instructions without connecting. Supply all three
flags to render a concrete item and budget; otherwise placeholders are shown.
`,
	"rc": `Usage: agent-run rc start --project P [--repo R] --agent claude --model <full id> [--effort e] --handoff FILE [--wait 5m] [--idempotency-key K] -o json
       agent-run rc status <session> -o json
       agent-run rc fresh --session NAME --request FILE -o json

fresh submits one qualified Codex thread+turn request to an existing paired host.
FILE is a strict CodexRCRequest JSON document: requestID, sessionUID, podUID,
project, repository, model, effort, handoff (1 to 4096 bytes), deadline (UTC, at
most 30 seconds ahead). It accepts no thread ID, native method or arbitrary cwd.
The existing API requires Tom, a configured client, or his recognized workbench;
this command grants no caller access. The endpoint and target qualification must
already be installed. One POST only, no redirect/retry/fallback. Exit 0 proves
native thread+turn ACKs; phoneRegistration remains unobserved until a supported
client witnesses it. An uncertain result must never be blindly repeated.

Launches a Claude remote session through the existing session API and observes
its bounded startup phases. The project catalog chooses the repository/base.
The idempotency key is always sent and printed; retain it and the session handle.
--wait 0 creates only, returning exit 7 without polling or sending the handoff.
--wait defaults to 5m and cannot exceed 10m. A positive wait that expires reports
timed_out with the session handle. Agent start follows that wait, not a fixed
90s budget: the session status is agentd's heartbeat and can lag the process by
up to a minute. A start that never appears times out at --wait with the phase
named. Scheduling, PodReady, registration and the link keep their own budgets,
each still capped by --wait. Status makes one read and never submits a handoff.
Codex is blocked locally with codex_adapter_not_implemented:
Codex remote launch is not implemented in 2a; no inventory was probed
For a fresh Codex thread on a paired v2 host, use agent-run thread start.

The handoff file must contain 1 to 8192 bytes of UTF-8 text, with no control
characters except newline and tab. It is submitted at most once after the link
appears and is never printed. A 202 proves transport submission only.
On a reused idempotency key (HTTP 200), the handoff is never sent; after following
to the link, the result is unconfirmed / handoff_unconfirmed, exit 6.
Provider readiness and CLI input recording remain unknown. The result is link_available,
never ready. JSON puts the validated link first and is at most 32 KiB.

Exit 0: link available (and transport submitted for start); 1: blocked/refused;
2: invalid input; 3: authentication/authorization; 5: nothing sent;
6: unconfirmed exchange, never resend the handoff; 7: pending, deadline, or
owner action required. Use rc status with the retained handle to observe progress.
No login, pairing, permission, operator flag, or runtime configuration changes.
`,
	"auth": `Usage: agent-run auth status [--warn <duration>] [-o json]

Shows how the keeper's shared Claude and Codex logins stand, for a session that
follows them. It reads the access projections mounted in this pod
(AGENTD_CLAUDE_LIVE_FILE and AGENTD_CODEX_ACCESS_FILE) and calls no API. They
hold no refresh token, and no token value is ever printed.

For each login it prints the state (Ready; Refreshing when the access expired
less than 3 minutes ago, because the keeper's next generation is probably still
on its way; or NeedsLogin when the access has expired for longer and the keeper
has published nothing newer, or the login itself has lapsed), the generation, when the
access expires and how long is left, and, for Codex, the keeper's last refresh.
A login the pod does not follow prints NotConfigured. For Claude, a Ready row
becomes Rejected when this pod's agentd saw the provider reject the generation it
applied, while that access token was still unexpired (marker:
~/.agentd/claude-rejected.json, generation and time only, no token). A 401 cannot
say whether the keeper is late or the login was revoked: a newer generation may or
may not arrive, this tool requests no sign-in, and after ten minutes the reason
says to check the keeper. Age never turns it back into Ready: a newer generation,
or an assistant turn that carries a real model response after the rejection,
clears it, and an expired access token keeps its Refreshing or NeedsLogin state
with the rejection added to the reason. A newer generation is not shown as proven
healthy, only as not yet observed. A marker that exists but cannot be read or
believed makes the row Unknown ("cannot tell"), never Ready. Rejected exits 1,
which does not mean "renew the login": a script that treats exit 1 as "renew"
should read the state first. The marker is read for the pod's own projection only;
with another --claude-file, or no HOME, a note on stderr says it was not applied.
The Rejected state is not yet proven live: agentd's signal has been tested only
against synthetic transcripts, never observed in a real session.

For Claude it also prints when the whole login lapses (about 30 days after each sign-in) and the days left,
from the non-secret login.json the keeper publishes beside the projection. A
login whose date the keeper did not publish prints "login lapses Unknown"; the
date is never guessed. Codex's login has no known lapse date.

Flags:
  --warn <duration>    report exit 1 when access ends within this window (default 30m)
  --claude-file <path> read this file instead of AGENTD_CLAUDE_LIVE_FILE
  --rejected-file <path> read the rejection marker here instead of $HOME/.agentd/claude-rejected.json
  --codex-file <path>  read this file instead of AGENTD_CODEX_ACCESS_FILE (or, when that
                       is unset, /opt/dev-env/codex-access/access.json if it exists)
  -o json              print the rows as JSON

Exit codes, as claude-login-check: 0 every configured login is ready, 1 one has
expired (Refreshing included, so a script still sees a login it cannot use right now),
is Rejected (not "renew the login": see above) or is inside the warning window, or the Claude login has 7 whole days or fewer left
(rounded down, so 7d23h counts as 7) or has lapsed, 2 cannot tell (a file missing, unreadable or invalid, a rejection
record that cannot be believed, or nothing configured). An Unknown lapse date does not change the exit code.
`,
	"budget": `Usage: agent-run budget create <task-uid> --success "RESULT" [flags]
       agent-run budget show <task-uid> [-o json]
       agent-run budget continue <task-uid> --decision <id> [-o json]

Create runs inside the already configured owning coordinator host. The operator
selects its host/Pod binding and enforces the assigned task UID. The request is
sent once; an unconfirmed answer exits 6. Inspect with show before deciding what
to do next. Repeating create does not reset an existing ledger.

Create flags (positive whole-second durations):
  --success <text>        observable result (required, at most 1024 bytes)
  --overall <duration>    wall-time ceiling, at most 45m (default 45m)
  --effort <duration>     summed-worker ceiling, at most 1h (default 1h)
  --checkpoint <duration> next checkpoint, at most 10m (default 10m);
                          cannot exceed --overall
  -o json                complete status, including the immutable binding

Show reads the retained ledger through the existing caller policy. Observation
can record a due stop latch. Create/show report authority; neither admits or
launches work, resets an epoch, extends a deadline or clears a latch. Native
launch still requires the owned executor's independent admission checks.
The stall line reports 60 minutes without validated progress (saga decision
94): a signal to diagnose, steer and checkpoint. It is not a latch and stops
nothing; the budget limits still do.

Native control: agent-run budget native TASK --once < request.json
The existing authenticated human sends a complete original binding, explicit ID
and operation on stdin: thread/start, thread/resume, thread/read, turn/start,
turn/interrupt, or host/stop. Only the explicitly configured owned root is used;
thread/turn creation pins gpt-6-luna. No prompt, model, pairing, credential or
approval override is accepted in flags. Mutation IDs are reserved, then independently
consumed by the immutable helper BEFORE dispatch; passive reads add no counter.
lost acknowledgement is unconfirmed, never automatically retried. thread/read
uses supported native history RPC, not JSONL/SQLite. turn/start only confirms
acceptance; inspect thread/read for actual completion. turn/interrupt is not a
whole-tree stop. host/stop requests original-guard termination; await independent
stop confirmation before continue. Native content is not budget/stop evidence.

Continue requires the existing authenticated trusted owner and an explicit
policy opt-in. It permits native-process continuation once on the original
still-live guard/Pod/HOME only after independently proven root and child stops.
The ORIGINAL deadline/effort/failure/checkpoint accounting and receipts remain.
It cannot extend an exhausted budget, recover a lost Pod or let a model resume
itself. A lost mutation acknowledgement is not retried.
`,
	"instruction": `Usage: agent-run instruction address --session <name>
       agent-run instruction send --session <name> --key <key> --project <project> --repository <repository> --task-key <task> --expect-uid <uid> --expect-pod <pod> --attempt <id> < correction.txt
       agent-run instruction read --session <name> --key <key>
       agent-run instruction checkpoint --session <name>
       agent-run instruction consume --session <name> --key <key> --attempt <id> --nonce <nonce> --message-uid <uid> --text-sha256 <digest> --expect-uid <uid> --expect-pod <pod>
       agent-run instruction attempt --session <name> --attempt <id> [--ended]

Address is the designated parent's read of the target's current attempt. It
returns the attempt ID, phase, session UID and live pod UID. It returns no
nonce and no instruction text. Copy those three values into send as
--attempt, --expect-uid and --expect-pod. Address is not a status read and
not the recipient checkpoint.

Send submits one in-scope correction from the authenticated parent Session.
The server derives the sender. There is no --from or --sender flag. Scope
fields must match the target's stored project, repository and task; they do
not widen it. A newly created receipt has no nonce and consumed false. Send
does not create consumption. An identical retry returns the stored receipt,
including consumed true when a separate consume already happened. A changed
intent conflicts and no new key is chosen.

Read shows one known key. The target session also receives the sender UID,
text and nonce. An inbox read acknowledgement is reported separately and is
not consumption. The one-shot native exec does not push this read into the
running process.

Checkpoint lists pending corrections for the caller's own active attempt.
It takes no key. Each item includes the server-authored sender UID, text,
message UID, text digest and nonce. An empty pending list is a confirmed
read of nothing new. Codex exec has no live steer: the original prompt is
written to stdin once, and a resumed Codex TUI reads the terminal. Neither
is interrupted when a correction is stored. A model sees a later correction
only by running this command. The command does not consume.

Consume is the target task's explicit acknowledgement. --message-uid and
--text-sha256 must be the values from that checkpoint or read; a nonempty
acknowledgement with different values is unconfirmed. It is not an inbox
read ACK, a grant, or a human decision. Retry only the same nonce and
attempt. A checkpoint, a read, or a file written by the daemon is not
consumption.

Attempt registers this pod's active native attempt, or --ended for that same
attempt. A replaced or ended attempt cannot accept a new delivery.
Registration runs only for a launch built with the instruction capability.
A legacy or disabled native task does not call this route.

Source tests do not prove an installed operator or a live native task.
`,
	"inbox": `Usage: agent-run inbox send --project <project> --recipient-namespace <namespace> --recipient-name <name> --recipient-uid <uid> --key <key> < message.txt
       agent-run inbox ask --project <project> [--to <session> [--to-uid <uid>]] --key <key> < question.txt
       agent-run inbox reply <question> --project <project> --key <key> < reply.txt
       agent-run inbox list --project <project> [--sent] [--limit N] [--continue <cursor>] [-o audit|json]
       agent-run inbox read <message> --project <project> [-o json]
       agent-run inbox ack <message> --project <project>
       agent-run inbox ack-status <message> --project <project> [-o json]

Send, ask and reply read 1..16384 UTF-8 bytes from stdin and require a stable
logical key. Each sends once. If unconfirmed, explicitly retry only the
identical intent with the same key; a changed intent conflicts.

Ask sends a routine question. With no --to it goes to this Session's recorded
parent Session (its coordinator), which the server resolves; text never picks
the recipient. Reply answers one question addressed to this Session and goes
to the question's sender; a question takes one reply. A reply is information,
not an approval.

List prints a bounded metadata page of this Session's inbox, or with --sent
the messages it sent. List and read show each message's kind (plain, question
or reply), the question a reply answers, and the question's state:
Unanswered, Answered (with the reply), Acknowledged (the asker acked the
reply), Stale (unanswered for more than 30 minutes), RecipientEnded (the
recipient Session is gone or finished without replying) or Unknown. Read
prints text without acknowledging it; for a question or reply it also prints
the state on stderr. Ack is explicit and proves an API read acknowledgement,
not provider consumption. Nothing interrupts a running task: it sees a reply
when it lists or reads its inbox. The server authorizes all operations; a
configured coordinator may read authorized project content but cannot send or
acknowledge for a Session. Send, ask, reply, list, ack and ack-status print JSON.
Ack-status reads one existing API acknowledgement receipt without reading text
or acknowledging a message. A missing or hidden receipt is not proof of absence.
An API acknowledgement proves neither provider consumption nor approval.
List -o audit prints escaped metadata, identities, question state and the
continuation cursor without reading text or acknowledging a message. Missing
or contradictory thread metadata is Unknown. It makes no approval claim.
`,
	"directory": `Usage: agent-run directory list --project <project> --once [flags]

Reads one authorized page of retained session metadata through the operator.
This command makes one API request. It does not retry or follow redirects.
It cannot create or control a session, or read its private history.

Flags:
  --project <name>      the authorized project to read (required)
  --once               acknowledge one request with no retry (required)
  --limit <1..20>       maximum records in this page (default 20)
  --continue <cursor>  the opaque continuation from a previous page
  --json, -o json      print the complete validated page as JSON

A nonempty continuation means more records remain. An incomplete or refused
read cannot establish an empty directory. JSON preserves Unknown observations.
Common API URL, token, CA and context flags use ordinary caller access.
`,
	"canary": `Usage: agent-run canary create --id <fixedID> --once [flags]
       agent-run canary result --id <fixedID> --session-uid <originalUID> --context <contextName> --json --once [flags]

Calls the operator's trusted ID-only canary route once. The server selects the
provider, task and bounded profile from its approved record. This command cannot
choose a prompt, model, repository, new key or replacement executor.

Flags:
  --id <fixedID>   the server's approved permanent record ID (required)
  --once           acknowledge one request with no retry (required)
  --json, -o json  print the original Session receipt as JSON

The receipt distinguishes new original creation from an existing original.
It does not prove provider execution or loaded rules. The command does not wait,
poll, retry or follow redirects. A failed or uncertain acknowledgement requires
safe observation of the retained original, rather than another create attempt;
an uncertain one exits 6 (unconfirmed). It exits 5 only when nothing was sent.
Common API URL, token, CA and context flags use ordinary caller access.

The result subcommand reads one named immutable publication from the retained
original Session. Its three selection flags are explicit and required. Here
--context names the publication and is consumed before connection resolution;
the ordinary default kubeconfig context remains separate. It performs one GET
without retry or redirects. A read proves stored text and existing receipt
fields, not complete history, served model, billed cost or confirmed stop.
`,
	"run": `Usage: agent-run [--repo] <repo> -p "<task>" [flags]
       agent-run --project <project> [--repo <repo>] -p "<task>" [flags]
       agent-run [--repo] <repo> --local [flags]
       agent-run [--repo] <repo> --interactive [flags]
       agent-run run [--repo] <repo> -p "<task>" [flags]

Starts a session: a pod on a worker node clones the repository and keeps its
work on the session's volume. With -p it runs the agent once on the task,
headless. With --local it starts the agent's TUI in tmux, which Tom attaches to
with agent-run attach. With --interactive it starts the same TUI with Remote
Control, so the session also shows in the Claude app, about two minutes after it
is created (agent-run show says remote control: registered). A profile that
mounts the keeper's projection (today the default and workbench profiles)
follows the keeper's shared Claude login, so nobody signs in; agent-run login
<name> status says which login a session uses, and its url and code are only for
a pod that holds its own login. An idle one is suspended after 72h, which
archives its phone entry until it resumes. When a session's pod starts
again later, the agent resumes the same conversation in the TUI; a task is never run twice.
agent-run prints the session's name, then waits up to --wait for the pod to
start. If the scheduler cannot place the pod, it prints the scheduler's reason
as soon as it is clear; the session stays Pending and starts when room frees up.

With --project, the operator selects the repository and rules from its accepted
project catalog. The task uses a private task worktree and the project rules, and keeps the agent's state in
its private home. Omit --repo for a project with one repository; select --repo
for a project with several. Declared projects take task mode or a fresh Claude --local session, not --interactive. Their rules
and catalog revision come from the server.

Flags:
  --dry-run=server           requires --project; admission only, never creates or falls back
  --once                     one create POST, no retry/redirect/pod wait; requires --project,
                             --idempotency-key, --wait=0 and -o json
  -p, --prompt <task>        the task, at most 64 KiB
  --local                    an interactive session instead of a task: the TUI, no prompt
  --interactive              --local plus Remote Control (claude: the keeper's shared login on
                             the default profile; codex: a phone daemon with no TUI, paired
                             with agent-run pair)
  --prompt-file <path>       read the task from a file instead; - reads stdin
  --repo <name>              the repository under the GitHub owner, such as haynes-ops;
                             or give it as the first argument. With --project,
                             this selects a repository declared in that project.
  --project <name>           a project declared in the operator's accepted catalog
                             (tasks or fresh Claude --local, not --interactive; cannot combine with rescue restore)
  --coordinator-instructions explicit opt-in for one new ordinary private project
                             task child; requires a live authenticated parent and
                             the dev profile. Default off; does not enroll an
                             existing Session or grant authority.
  --coordinator-instruction-publisher
                             qualified control client for a fresh ordinary dev
                             project parent created by a trusted client. Default
                             off; does not enroll a receiver or change depth.
  --agent claude             the agent: claude (default), codex or cursor; opencode
                             arrives in plan 09. cursor runs a headless --project
                             task only, needs --model (a Cursor model id; Grok is
                             one), and takes no --effort: Cursor puts effort in
                             the model id. Cursor takes the prompt as one exec
                             argument, so a task over 64 KiB is refused as for
                             every agent
  --model <id>               a full model id, never an alias such as opus (default
                             $DEV_ENV_CLAUDE_MODEL, else ` + DefaultClaudeModel + `)
  --effort <level>           low, medium, high, xhigh, max or ultracode, as the
                             model takes them (default xhigh, or the highest level
                             below it the model takes; none on models without effort
                             control, such as Haiku 4.5)
  --base <ref>               the ref the session's branch starts from (default origin/main).
                             With --project, omit it to use the repository's
                             declared default branch; an explicit ref must match.
  --size S|M|L               the pod's CPU and memory preset (default M)
  --profile <name>           a profile in dev-env-templates (default: the templates'
                             default). A session's child runs on its parent's; a
                             workbench session's child runs on the one it names, or
                             the default, so name workbench to keep it. A Codex
                             host a workbench session starts takes no profile
  --timeout <duration>       stop the task after this long, such as 40m (tasks only)
  --max-turns <n>            stop the task after this many turns (tasks only)
  --idempotency-key <key>    a repeat of the same request with the same key returns
                             the session the first one created, while it is
                             unfinished (default: a new key for each run, which
                             agent-run's own retries reuse)
  --idle-suspend-after <d>   suspend the session once its agent has been idle this
                             long (default: the templates', 1h for a task, 72h otherwise)
  --archive-after <d>        archive the volume this long after a suspend (default:
                             the templates', 168h); an archived session is restored,
                             not resumed
  --wait <duration>          how long to wait for the pod to start (default 30s;
                             0 returns as soon as the session is created)
  -o name|json               print only the name, or the session as JSON

--safe is gone: a session pod runs its agent without approval prompts, and the platform is
the boundary.
`,
	"suspend": `Usage: agent-run suspend <name>... [-o json]

Suspends each session: the operator rescues its work to a bundle on the shared
volume, then stops its pod. The volume stays, with the worktree, the conversation
and gitignored build output, so agent-run resume brings the session back as it
was. The operator also suspends a session by itself once it has been idle for its
window (a finished task after 1h, an interactive session after 72h); show says
who suspended it.
`,
	"resume": `Usage: agent-run resume <name> [--wait <duration>] [-o json]

Starts a suspended session again: a new pod on its volume, where the agent
resumes the same conversation in its TUI (claude --resume). A task's prompt is
never run again. agent-run waits up to --wait (default 30s) for the pod, then
prints how to attach. A reaped session cannot be resumed.
`,
	"log": `Usage: agent-run log <name> [--tail N] [-o json] [--once]

With --once and -o json, makes one GET without retry or redirect and refuses
truncated or mismatched log receipts. Ordinary log behavior is unchanged.

Prints the last N lines (default 200, at most 5000) of the session's log, read in
its running pod. A task's log is its readable event stream; a TUI's log has a line
for each start and exit. Once the pod is gone, the log stays on the shared volume
at logs/<name>.log, which every session pod mounts at ~/.shared.
`,
	"msg": `Usage: agent-run msg <name> "<text>" [-o json]
       agent-run msg <name> -          (the text on stdin)

Sends a message, at most 16 KiB, into the session's TUI: it arrives as one paste,
with a line saying who sent it and that it is information, not the session user's
instruction, and how to answer (agent-run msg <sender>). A headless task (-p)
takes no messages; it reports through its log and its PR. Any session or person
may message any session. agent-run sends a message once and never retries it, so
it is never delivered twice. A lost answer or a 5xx exits 6 (unconfirmed): the
message may have arrived, so do not send it again before checking. It exits 5
only when nothing was sent (the API could not be reached at all).
`,
	"rescue": `Usage: agent-run rescue list [--session <name>] [-o json]
       agent-run rescue restore <session>/<stamp> (--local | -p "<task>") [--repo <name>] [flags]

A rescue is the bundle of a session's work, written to the shared volume when the
session is suspended or reaped (D-10, D-48), or when the task clean-up deletes a
task session's HOME (DESIGN-061: only commits, tracked edits and untracked files
that never reached GitHub). The bundles never leave the cluster.

rescue list shows them, newest first: id, when it was made (UTC), the repos it
holds a bundle for, its size, its state, and how long it is kept. The state is
complete (every bundle written and verified), incomplete (finished, but a bundle
is missing), unfinished (no manifest yet) or an error in the manifest. A rescue of
a session that still exists is kept; once the session is gone, the operator prunes
the rescue after the retention (D-09, 30 days). Paths under rescue/ that do not fit
the layout are listed as unrecognized and never pruned.
  --session <name>   only this session's rescues
  -o json            the API's list as JSON

rescue restore starts a new session that fetches one rescue (D-67). It is a run:
it takes the same flags (-p or --local, --agent, --model, --effort, --base, --size,
--wait and the rest; agent-run help run lists them). Only a complete rescue can be
restored. The repo is the rescue's only clone, or --repo <name> when it holds several.
On its first boot the new session's clone fetches the bundle into refs/rescued/. If
you give no --base and the bundle holds the old session's branch, the worktree
starts at refs/rescued/heads/agent/<session>. A rescue branch (uncommitted work,
refs/rescued/heads/rescue/...) is never the base: look at it and copy from it, and
never push it, because it can hold secrets (D-10).
`,
	"history": `Usage: agent-run history list [--project <project>] [--limit N] [--continue <cursor>] [-o json]
       agent-run history show <session-uid> [--content <sha256>] [--limit N] [--continue <n>] [-o json]
       agent-run history read <session-uid> <root/path> [--content <sha256>] [--offset N] [--all | -o json]

The archived provider history of private Sessions (DESIGN-025). Before a private
Session's HOME is deleted, the operator exports its chat history (never its
credentials) and its Git rescue into the shelf's history archive. These commands
read that archive. Every answer is the archive's copy and says archived: true;
nothing here changes or removes it.

Existing trusted clients, live sessions and configured coordinators read the
shared history pool across projects (D-92). Project labels filter the index;
they do not partition these readers. Untrusted callers remain refused.
An absent history or an operator whose archived-history reads are off or older
than these routes answers "not found" (exit 4). Single-entry reads also hide
unauthorized readers with "not found".

history list shows one project's histories, newest first: the Session, its UID,
provider, when it was archived (UTC), its file count and size, and how its
executor ended. A history whose archive check failed is listed as corrupt.
Omit --project to list histories without a project.
  --project <p>      the project filter (empty selects histories without a project)
  --limit N          at most N a page (1 to 100, default 20)
  --continue <c>     the next page, as the last line of the previous one says

history show prints one history's manifest: identity, terminal record, rescue,
counts, then a page of its files and of the names the export left out (copies of
user files, settings, credentials by name only), with their class and size.
  --content <sha256> an older archive entry of the same Session (default: newest)
  --limit N          files and excluded names a page (1 to 1000, default 100)
  --continue <n>     the next page

history read writes one file's bytes to stdout, 64 KiB a page, each page checked
against the archive's hashes. <root/path> is a FILE of history show, such as
claude/projects/-home-dev/<id>.jsonl.
  --offset N         start at byte N (default 0)
  --all              follow the pages to the end; from offset 0 it also checks
                     the whole file against its SHA-256
  -o json            print one page as the API's JSON (base64 data)

The Git bundles are not served: restore code with agent-run rescue restore.
`,
	"declare-activity": `Usage: agent-run declare-activity start "<what you are doing>" --scope <a,b,c> [--ttl 45m]
       agent-run declare-activity end <id>
       agent-run declare-activity list
       (or the same as declare-activity, which the agent image links to agent-run)

Tells the remediation agent "this is me, not a fault" (D-17). Before work in the
cluster that can trip a critical alert (restarting a stateful app, suspending
Flux, draining a node, deleting pods, rolling storage), declare it; end it as soon
as you finish. A declaration is evidence the remediation lane weighs, never a
mute: a real incident is still handled.

--scope is required: the namespaces, apps or nodes your work can disturb, comma
separated, or cluster for everything. --ttl defaults to 45m and is capped at 8h,
and at 2h for cluster. The operator deletes a declaration when it expires. The
declarer is recorded from your token, so the remediation lane can message you.
`,
	"attach": `Usage: agent-run attach <name>

Attaches your terminal to the session's TUI: kubectl exec -it into the session's
pod and tmux attach-session -t agent, with your own Kubernetes rights. Detach
with ctrl-b d; the agent keeps running. The session must be Running.

It is for Tom, from the workbench, a laptop or the v1 pod. An agent cannot exec
into another session's pod (D-19), so inside a session pod attach refuses and
points at agent-run msg. kubectl must be on PATH.
`,
	"login": `Usage: agent-run login <name> [url|code|status]

Sets up, renews or recovers the Claude login of a --interactive session. The
login is the pod's own; nothing is copied from another pod. Run url, open the
link and sign in, then pipe the code the page shows into code, for example:
read -rs c; printf '%s' "$c" | agent-run login <name> code. The link and the code
are never logged or stored. status (the default) says where it stands.

Only one CLI may own the login. While the session's agent runs, url refuses: exit
its TUI first (agent-run attach <name>, then /exit). When no agent runs, url works
even if the login still looks valid (renew before it lapses, or replace one the
server revoked). The agent starts by itself once the new login lands and the login
window ends; a first login on a fresh pod needs no TUI exit.

Like attach, it is for Tom's workbench, a laptop or the v1 pod: it runs kubectl
exec into the session's pod, which an agent in a session pod cannot do (D-19).
`,
	"keeper-login": `Usage: agent-run keeper-login claude|codex [status|url|code|cancel] [-o json]

Renews the keeper's shared Claude or Codex login from a workbench session, with
no v1 pod and no laptop. The operator runs the keeper's own login helper for you
and does nothing else in the keeper pod. Only the workbench may call it; any other
caller gets a 403.

  status   the keeper's state (Ready, PendingPublication, NeedsLogin or
           Unavailable), its generation, when the access token lapses (the keeper
           renews that itself) and when the login itself lapses, or "Unknown" when the
           provider gave no date. Exit 0 when it is Ready or PendingPublication,
           1 otherwise, and 1 when 7 whole days (rounded down) or fewer remain. It changes
           nothing.
  url      opens a ceremony and prints the sign-in link on its own line (Codex
           prints a one-time code too). Open it and sign in. The link is a secret:
           it goes to your terminal only, never a log, and is not stored.
  code     Claude: reads the one-time code the page shows from stdin and hands it to
           the helper, for example: read -rs c; printf '%s' "$c" | agent-run
           keeper-login claude code. The code is never an argument. Codex: takes no
           code; it waits for the device sign-in to finish, and exits 5 if it is
           still waiting (run it again).
  cancel   abandons a ceremony. For Claude it also tells the keeper's login helper to
           stop, so a ceremony the operator lost (a restart) or the v1 route holds is
           ended too and the next url works. The keeper's login is untouched until a
           code is adopted. A Codex ceremony held elsewhere expires within 15 minutes.

One ceremony at a time per provider, open for at most 15 minutes. Only the
operator's leader holds a ceremony, so the command reconnects by itself while a
follower answers; if the operator restarts mid-ceremony, run url again. Adoption
needs a valid code and the same account: a wrong code leaves the current login as
it was. Renew well before the login lapses (status says when).
`,
	"thread": `Usage: agent-run thread start --agent codex --host <codex-host> --model <full id> --effort <e>
                         --handoff FILE [--repo R] [--name N] [--wait 5m] [-o json]
       agent-run thread start --agent claude --project P [--repo R] --model <full id> [--effort e]
                         --handoff FILE [--wait 5m] [--idempotency-key K] [-o json]

Starts one fresh interactive thread that Tom can see in his provider's phone app
(DESIGN-057) and prints one thread-launch.v1 JSON line. The handoff is the
thread's first message, one rule for every provider: 1 to 4096 UTF-8 bytes,
not blank, no control character but newline and tab. Ask the thread to make its own
worktree, as on the phone. --wait (at most 10m) bounds the whole launch.

--agent codex     an existing Codex --interactive session (--host), paired once with
                  agent-run pair. It runs agentd ctl thread-start in that pod. From Tom's
                  laptop, the v1 pod or a workbench session it uses the exec route pair
                  uses. An ordinary session pod has no exec (D-19), so the operator's API
                  runs it there instead (POST /v1/sessions/<host>/threads, as it delivers
                  agent-run msg); an operator released before that route is a refusal
                  (route_unsupported), never a fallback. The API is narrower than msg:
                  a session reaches only a host of its own profile, or one it or its
                  children started, because the handoff is the first instruction of an
                  always-allow thread that acts with the host's profile; anything else
                  is caller_not_allowed. The thread is the
                  native codex --remote TUI (scripts/codex-clean-session.py) in a tmux
                  session named --name (default thread-<UTC mmdd-HHMMSS>) in
                  ~/repos/<repo> on the host (default: the host's repo). Never codex exec
                  or queue, and no sandbox or approval flag.
--agent claude    a new remote session, through rc start. It follows --wait (default
                  5m, at most 10m). Agent start is read from the session status,
                  which agentd publishes on its heartbeat, so it can show up to
                  a minute after the process is up. A start that never shows
                  times out at --wait with that phase named.
--agent cursor|grok
                  refused: not yet qualified.

Every launch is always-allow, as in v1; no flag is the caller's to pick. The
result's permission field says so: Codex --dangerously-bypass-approvals-and-sandbox,
Claude --dangerously-skip-permissions (its critical-path check, such as rm inside
sh -c, is the one prompt that mode does not cover), Cursor not qualified.

Phases: host_ready, paired, client_started, thread_created, source_visible
(in the phone's default list), handoff, first_response. Outcomes and exits:
  responded 0         the first owner-visible response was observed
  visible 7           created and listed; the response is pending or not observable
  action_required 7   pairing or login needs Tom; nothing was created
  timed_out 7         the wait ended first; phases show where
  hidden 1            created with a source the phone hides
  unconfirmed 6       the launch may have happened: inspect the host, never retry
  failed 1, blocked 1 refused; nothing created unless phases say otherwise
  (invalid input that agent-run itself refuses exits 2)
Never resend a handoff after 6 or 7.
A Claude session whose agent state is login-required, and whose status is not a pending keeper refresh, the projection warning, or a logged-in, rejected or unverifiable projection line, is action_required with claude_login_required at client_started (exit 7) as soon as that state is seen; the handoff is not sent.
`,
	"pair": `Usage: agent-run pair <name>

Prints a short-lived pairing code for a Codex --interactive session (agent-run
--agent codex --interactive --model <id>). In the ChatGPT app choose Remote, add a
computer and type the code. A phone needs this once: the enrollment lives on the
session's volume and survives a suspend and resume. The code is valid for about ten
minutes and is a secret until then: it goes to stdout only, never to a log or a file,
and the session must be Running with the daemon up (agent-run show says running).

Like login, it is for Tom's workbench, a laptop or the v1 pod: it runs kubectl exec
into the session's pod, which an agent in a session pod cannot do (D-19).
`,
	"detach": `Usage: agent-run detach <name>

Detaches every client from the session's TUI (tmux detach-client -s agent in its
pod, through kubectl exec). The agent keeps running. For Tom, as attach.
`,
	"list": `Usage: agent-run list [--repo <name>] [--state <phase>] [--mine] [-o name|json]

Lists sessions, newest first: name, phase, the agent's state, node, age, who
started it, and a note (the scheduler's reason while Pending, why it failed,
reaping, outdated).

Flags:
  --repo <name>     only this repository's sessions
  --state <phase>   only sessions in this phase: Pending, Running, Idle, Draining,
                    Suspended, Archived or Failed
  --mine            only the sessions this caller started
  -o name|json      print only the names, or the API's list as JSON
`,
	"show": `Usage: agent-run show <name> [-o json]
       agent-run show <name> --json --once

Shows one session: phase and node, the scheduler's reason while Pending, repo,
agent, model and effort, size, the agent's state, branch and head from its last
heartbeat, how the task ended, usage, conditions and the task itself.

With explicit --json --once, reads one named Session without retry or redirect
and prints its validated raw JSON. Missing retained provenance remains Unknown;
the response does not prove publication acknowledgement or complete history.
Without --once, ordinary show behavior is unchanged. --context still selects a
kubeconfig context; it is not a published-context selector on this command.
--captured-usage -o json asks for Dev-Env-Usage-Capture: direct-v1. agent-run also
sends Dev-Env-Attribution: provider-v1 on that read. The capture view is a
separate capability: ordinary show output still omits the ledger. Text and
--once are refused before a request. A missing, older, disabled, malformed, or
oversized view is unavailable, not zero usage and not a partial receipt list.
`,
	"usage": `Usage: agent-run usage -o json [--from-file FILE|- | --claude-statusline | --read-current | --publish-file FILE|-]
       agent-run usage capture -o json --input FILE|- --trust-file FILE
       agent-run usage collect claude-statusline --state-dir DIR --producer REF --incarnation REF
       agent-run usage collect codex -o json --executable /absolute/official/codex --access-only-auth
       agent-run usage collect claude-report -o json --input FILE|- --received-at RFC3339

Without --from-file, --claude-statusline, --read-current, or --publish-file,
reports local unconfigured acquisition preflight refusals. No provider is
contacted and no operator connection is opened. --claude-statusline explicitly
reads a bounded native callback from stdin; its provider freshness and account
binding are Unknown. --from-file reads an untrusted bounded cached read, with
original observation and served times preserved. It does not configure live
acquisition or establish provider coverage, account binding or freshness.

--read-current is off unless set. It cannot be combined with --from-file,
--claude-statusline, or --publish-file. It performs one GET /v1/provider-allowances
through the configured operator client and prints that providerallowance.read.v1
document. Redirects are not followed and the GET is not retried. Denial, absence,
and an invalid or oversized body are not echoed. The serving clock does not
refresh provider freshness. --publish-file FILE|- is off unless set. It cannot
be combined with --from-file, --claude-statusline, or --read-current. It reads
one bounded regular file, or stdin when FILE is -, and performs one PUT
/v1/provider-allowances. Redirects are not followed and the PUT is not retried.
A refusal is not echoed. The command does not acquire from a provider.
usage capture explicitly assembles up to six sanitized retained captures using
a separate bounded caller-owned aggregate trust file. It preserves original
capture and receipt times and leaves provider observation time/current values
unknown on these inputs. It opens no operator/provider connection, credential
or subprocess and never publishes implicitly.
usage collect is explicit and separate: claude-statusline sanitizes one callback
into a private two-sample journal without changing provider settings. codex
starts one bounded official app-server child for non-forced personal account
and quota reads, outputs only sanitized quota/hash metadata and stops it.
It does not start a thread, model turn, login, forced refresh or recurrence.
claude-report requires the original local --received-at timestamp and sanitizes
one already supplied SDK assistant usage_report; it
starts no provider or model. Missing rows remain distinct from an empty list.
No collection mode publishes or opens an operator connection.
This is not the Codex current-source slot; usage-source
--read-current is unchanged. The command does not grant, poll a provider, or
open a credential.
`,
	"usage-source": `Usage: agent-run usage-source -o json --incarnation ID --generation N --max-age 30m (--codex-tool-report FILE|- | --codex-raw-tool-report FILE|- --observed-at RFC3339) [--publish]
       agent-run usage-source -o json --incarnation ID --generation N --max-age 30m --read-current

Sanitizes one explicitly supplied retained Codex observation into a selected
source report. --codex-tool-report is the flattened observation.
--codex-raw-tool-report is the nested official tool envelope and requires
--observed-at, the original retrieval time, copied as given. Observation time
is never taken from the clock, a file time, or the reset instant. The two input
modes cannot be combined, and a duplicate flag is a usage error before any read.

Used percentage is reported; remaining is its derived complement. The window is
the supplied minute count, not a calendar week. Retrieval time is preserved,
providerUpdatedAt stays null, and account limits stay unattributed. Automation
ceilings, credit and reset receipts, and token totals are not allowance. Parsing
does not authenticate an account or prove entitlement.

The default is local formatting only: canonical JSON and a newline, with no
operator or provider request. --publish is off unless set. It uses the configured
operator client for one PUT /v1/provider-source-reports/codex-current-source.
The body is the report's canonical JSON, with no added newline. Path and method
are fixed. Redirects are not followed. Conflict, denial, absence, timeout, and
any uncertain answer are not retried and are not echoed. Success prints only
that the fixed slot was published. The server's session, pod, and ServiceAccount
fence stays authoritative. This command does not grant or impersonate.

--read-current is off unless set. agent-run usage is unchanged. The flag
conflicts with both report paths, --observed-at, and --publish. A duplicate
flag is a usage error before any read or connection. The incarnation must be
a selected-source identifier, the generation must be positive, and --max-age
must be greater than zero and at most 30 minutes. Those checks happen before
the operator connection. A longer age does not connect, and it does not change
the local formatter's limit.

The mode uses the configured operator client for one GET of the same fixed
path. Redirects are not followed and the GET is not retried. The response is
at most 16 KiB and is never copied into an error. A validated report is
printed as canonical JSON with providerUpdatedAt null. The exact object
{"availability":"Unknown"} prints that object and does not add a percentage
or a freshness time. Any other shape, a stale retrieval, a passed reset, or
a selection mismatch fails closed. This mode does not collect a provider
report, schedule a cadence, or attribute an account.
`,
	"usage-export": `Usage: agent-run usage-export --manifest <path> --output-dir <private-dir> [--allowance-read <path>] [flags]

Writes a private, bounded report for only the Sessions explicitly named in a
local manifest. Every selection binds a DNS-label name to its expected UID; a
mismatch, refusal, malformed answer, redirect, unconfirmed read, invalid direct
observation, or direct source-UID mismatch stops later reads. The command makes no
list, history, log, console, provider, or auth-store request. Each selected Session
is read once, without retry or redirect, with the existing provider-v1 attribution
and direct-v1 capture headers.

The manifest is a strict JSON object with a nonempty ` + "`selections`" + ` array.
Each item is ` + "`{\"name\":\"...\",\"expectedUID\":\"...\"}`" + ` and may add
` + "`project`" + `, ` + "`repository`" + `, and ` + "`task`" + ` labels. Labels are metadata only
and apply only to an exactly matching session UID. Duplicate names or UIDs are
refused before connecting.

Each optional --allowance-read is a caller-provided providerallowance.read.v1
snapshot claim. Local decoding validates only the DTO contract; it is not an
independent source, binding, validation, or authorization attestation. This command
has no provider reader, credentials, currentness inference, or account/model/cost
totals. With none, allowance input is explicitly Unconfigured.

--output-dir must not exist. The command creates it mode 0700 and creates only
report.json and summary.txt mode 0600. It refuses symlink path components and
never overwrites output. Those files are private local evidence, not a public
artifact or monitoring feed. stdout and stderr contain only a safe status, never
raw Session fields, prompts, logs, credentials, or API error bodies.

Flags:
  --manifest <path>          required strict local selection manifest
  --output-dir <path>        required new private directory
  --allowance-read <path>    repeatable local providerallowance.read.v1 input
`,
	"reap": `Usage: agent-run reap <name>... [-o json]

Reaps each session: the operator rescues its work to a bundle on the shared
volume, then stops its pod and archives its volume (D-10, D-45). Nothing skips
the rescue, so a reap loses no work; --force is accepted for v1's sake and
changes nothing. Any caller may reap any session. agent-run asks for no
confirmation: it reaps the sessions it is given.

A private session (one with a privateHistory record) is reaped differently
(DESIGN-025): the operator asks its agent to stop and keeps its private HOME.
It makes no rescue bundle and archives or deletes nothing then. The session
stays, reaping, until the HOME expiry has archived its history and deleted the
HOME. Its RemovalBlocked condition usually says ExecutorEnded once the agent's
stop is recorded, and RetentionPending, with the reason, while it is not. The
other reasons (TaskComputeReclaimed, PreservationUnknown, HomeDeleting,
HomeExpired) say what they mean in their text.

A private task session (mode task) is cleaned up instead while the operator's
task clean-up is on (ADR-020, DESIGN-061); its conditions then name the "task
clean-up (DESIGN-061)". The operator stops its agent, waits until the old node
has released the HOME's volume, saves any work that never reached GitHub as a
git bundle (agent-run rescue list --session <name>; kept 30 days), deletes its
HOME and then the session, normally within minutes of the reap. A task that
ended on its own goes the same way 7 days after its end. Gitignored files,
files outside a repository and the conversation transcripts go with the HOME.
A rescue that refuses (a merge in progress, a nested or stray repository,
more than 50 MiB untracked) keeps the HOME for a person; an owner hold set
before the reap keeps both. agent-run show <name> says "reaping: clean-up
queued", "saving unpushed work" or "deleting the HOME".

A reap returns at once; agent-run show <name> follows it until the session is gone.
`,
	"grant": `Maintenance source routes are held default OFF. They require reviewed activation.
  maintenance-request --task-json <envelope JSON> --reason <why> [--ttl 30m]
  maintenance-decide --grant-uid <UID> --digest <digest> [--approve] <grant>
  maintenance-exec [--restore-evidence <report>] <grant> <command> [args...]
The authenticated human approves an immutable task envelope. Listed workers share
its remaining lease and choose their commands. Extensions use a new request with
extensionOf and a new approval. Expiry stops compute; it does not restore hardware.

Usage: agent-run grant list [--session <name>] [--phase <phase>] [--mine] [-o json]
       agent-run grant show <grant-id> [-o json]
       agent-run grant request --type <type> --reason <why> [--no-wait] [type flags] [-o json]
       agent-run grant release <grant-id> [-o json]
       agent-run grant attest <grant-id> --approved-by user|coordinator:<ref> --ref <nonsecret> [-o json]
       agent-run grant decide <grant-id> --grant-uid <uid> --digest <digest> --ref <ref> [--approve] [--ttl 10m] [--reason TEXT] [-o json]
       agent-run grant use <grant-id>|default

Read, request, release and use access grants (DESIGN-001 6.12). This is part
of plan 07 step 7. Request posts the ask. Release asks the broker to end the
grant. Use selects an installed kube context in this pod. Request, release,
attest and use do not approve a grant or install one. grant decide posts one
delegate decision and does not install a grant. The in-pod tools are agent-run mcp.

grant list prints name, phase, type, session, TTL and expiry, newest first.
  --session <name>   only that session's grants
  --phase <phase>    Pending, Active, Denied, Expired, Released or Failed
  --mine             only the calling session's grants

grant show prints one grant. The name is grant-<id>. It does not print a token.

grant request asks for one grant for the calling session. It prints the
approval page, then reads the grant every 5s until the phase leaves Pending
or 30m passes. --no-wait returns the pending grant. An identical ask returns
the grant the session already holds.
  --type <type>      kube, egress, credential or breakglass
  --reason <why>     required; Tom reads it on the approval page
  --role <role>      kube catalog role
  --namespace <ns>   repeatable, for kube or breakglass
  --secret <name>    repeatable Secret name for role dev-env-grant-namespace-operator only.
                     DNS-1123, at most 63 characters, at most 16 names. Refused for every
                     other type or role. Names only; secret values are never read or sent
  --fqdn <name>      repeatable egress destination
  --cidr <cidr>      repeatable egress destination
  --endpoint <ns>    NAMESPACE or NAMESPACE:key=value,key=value
  --port <port>      443, 53/udp; repeatable
  --credential <id>  proxmox or hw-ssh. hw-ssh leases the ONE shared SSH key into
                     this pod's tmpfs: anything in the pod can copy it while it is
                     installed; removal at expiry, release or session end is neither
                     containment nor revocation, and the key stays valid on every host
  --ttl <duration>   such as 45m; empty uses the API default

grant release names one grant. The grant object stays as the audit record.

grant attest records your claim that you obtained approval for this exact grant.
Order: request with grant request ... --no-wait (without it that command waits
up to 30m on your own pending grant and you cannot attest meanwhile), then
attest, then follow the grant with grant show. First ask your coordinator to
approve; where the coordinator cannot approve it, ask the user. Attest only an
approval you actually received, for the scope and TTL the command prints. It is
your claim, recorded under your session: an agent attestation, not verified
human identity. It never replaces a confirmation your coordinator must get
itself. It helps only where a deployed policy names attestation (otherwise the
grant waits for Tom); one attestation per grant.
  --approved-by <who>  user, or coordinator:<ref> (the reference is not verified)
  --ref <id>           nonsecret message or journal id of the approval

grant decide records one allowlisted delegate's decision on another agent's
pending grant. The caller must be an allowlisted delegate. It cannot decide
its own or an ancestor's request. Tree reach is the default. Only an
explicitly configured designated delegate can reach eligible unrelated requesters;
requester descendants and maintenance participants remain refused. The operator
rechecks the live actor and exact policy or reserved bounds.
The digest is Grant.ScopeDigest from
grant show -o json. A delegate decision is not a human decision. Omit
--approve to deny. The command prints the scope digest and secret names it is
binding, then posts the decision. HTTP 202 means the decision was accepted; it
does not mean the grant is Active.
  --grant-uid <uid>  the grant's UID (may be absent when delegate decisions are disabled)
  --digest <digest>  Grant.ScopeDigest the delegate reviewed
  --ref <ref>        required; the message being answered (letters, digits and . _ : / # -)
  --approve          approve; omit to deny
  --ttl <duration>   optional Go duration, at least 10m and at most the requested TTL
  --reason <text>    optional; at most 256 characters, no control or format characters
  --reserved         explicit default-off S7 case, no standing/attested approval
  --tom-ref <ref>     required with --reserved; nonsecret decision reference
                     (Tom message or configured delegated-authority reference); agent assertion, not verified human consent

The Tom reference is an agent assertion, not verified human consent. Reserved TTL
is at most 1h; maintenance keeps its 30m default. Use grant request --reserved with
--all-namespaces for an approval-time Secret-read namespace UID snapshot, or repeat
--storage-target Kind/name@UID (1–8) with one --namespace for exact storage deletes.
The configured proposed namespace exclusions can have explicitly reviewed bounded
exceptions; no missing-policy or lookup failure widens them.

grant hardware-intent <grant> --grant-uid <uid> --digest <digest>
  --operation <operation> --host <host> --device <device> --firmware <id>
  --tom-ref <ref>
Records audit intent only on the authenticated requester's own installed Active
C4 lease. Credential authority and keeper policy remain unchanged. It enforces no
command or target, reads no key, calls no hardware, and cannot extend a lease.

grant use runs agentd ctl grant-use. The name is grant-<id> or default.
It does not call the API.
`,
	"breakglass": `Usage: agent-run breakglass --reason <why> [--ttl <duration>] [-o json]

Ask for a break-glass grant (DESIGN-001 6.12). This posts type breakglass to
/v1/grants and prints the approval page. The API sets the role. Tom still
approves it. This command does not approve the grant or install it.
`,
	"mcp": `Usage: agent-run mcp

Serve the dev-env access tools on stdin and stdout (MCP). The tools are
request_access, grant_status, release_access, decide_grant and hardware_intent. They call
/v1/grants. request_access asks; Tom still approves. release_access asks the
broker to end one grant. grant_status reads. decide_grant is for an
allowlisted delegate only and is not a human decision. None of them install
a grant.

The asks tools need an operator running with --enable-asks:
  ask          create an info or approval ask; returns its Ask JSON immediately
  ask_status   read your ask by id, including its state and any answer
  list_my_asks read one page of your own asks, newest first (limit, cursor)
  withdraw     withdraw your own open ask by id

ask takes a stable requestId for retries of the same intent, type (info or
approval), question, optional options, recommended, allowFreeText, context and
expiresIn (a Go duration in whole seconds, from 1m to 168h, for example "24h"
or "90m"; "7d" is not accepted). Approval asks require scope with action,
target and expiresAt (RFC 3339, 1 minute to 24 hours after creation). With
expiresIn omitted or empty, the ask defaults to 24h, capped by the scope's
expiry for approvals. An explicit expiresIn must not outlive that scope.
The operator fills the session identity. Reuse the requestId only for the
same ask. Creation returns without waiting for an answer; keep working and
check ask_status later. Only the authenticated portal can answer an ask.
Read answers with ask_status or list_my_asks (default limit 20, maximum 100).
Their state reflects expiry at read time. Approval records also include
computed.approvalValid, computed locally from the answer and scope expiry.
Answerer identity contains displayName only. An approval applies only to its
recorded scope and expiry.

agentd registers this command as the dev-env MCP server when it renders the
session, unless mcp.json already names a dev-env server.
`,
	"pages-mcp": `Usage: agent-run pages-mcp [--source-config FILE]

Serve only pages_list and pages_get over stdin/stdout. Sources are disabled by
default. A private owner-controlled config can enable fixed current-snapshot
sources; this command does not use operator global connection flags, register
itself, or establish notification/delivery history. See docs/pages-mcp-readonly.md.
`,
	"maintenance": `Usage: agent-run maintenance check --input <file|-> [--now <RFC3339>]

Checks one offline upgrade-shepherd envelope and the supplied snapshot. It does
not contact the API, read a token, launch a session, or suppress an alert.

--input is a JSON file, or - for stdin. --now injects the clock. Without
--now, the clock is this process's wall time, not a network time service.

Exit 0 means the check produced JSON. Read decision: candidate, respond, or
insufficient-evidence. Exit 0 is not authorization. A candidate is not alert
deferral. Exit 2 means the input is invalid and the JSON names the fields.
Nothing is sent either way.

The numeric limits are proposed source-contract defaults, not production SLOs.
`,
	"pages": `The default source is KEEPER PAGES ONLY - not all Pushover history. Recorded or sent (accepted by
Pushover) does not mean delivered or read.

Usage: agent-run pages list [--sender S] [--state recorded|sent|failed|ambiguous] [--since 36h|7d|RFC3339] [--limit N] [-o json]
       agent-run pages show <key> [-o json]
       both take --from-file <file|-> (default -, stdin)

Looks up the Pushover pages that dev-env's own keeper sent to Tom. Feed it the
keeper's notice ConfigMap, which is readable to any session:

  kubectl -n dev-env-system get cm dev-env-keeper-notices -o json | agent-run pages list

It does not contact the API or send anything. Each page shows its time in UTC
and America/New_York, sender and source, title and body, key and correlation,
link, attempts and Pushover's request id.

States are exact. recorded: written before the send, outcome not stored (a crash
or a send in flight). sent: Pushover answered HTTP 200 with status 1; it queued
the page, and that is not proof the phone received it. failed: a definite 4xx, or
no request made. ambiguous: a timeout, transport error, 5xx or unconfirmed 2xx;
the page may have reached Tom.

Every keeper answer starts with the keeper banner and prints its coverage; an error answer
prints them on stderr. Only the keeper records, and only since the first keeper
image with this code. Alertmanager, Gatus, upgrade-gate, alert-responder,
dev-env-ops, the v1 pod, haynesnetwork, scribe-notes, Home Assistant and keeper
notify-test are not covered, and older pages are Unknown. A page that is not
here is Unknown, not "no page". list exits 0 whatever it finds; show exits 4
when the key has no record. The record keeps the newest 100 pages (256 KiB). A
damaged record is reset by the keeper and the coverage says how many records
were lost.

Alertmanager snapshot source (explicit):
  agent-run pages list --source alertmanager [--search TEXT] [--status active|suppressed|unprocessed] [--from-file FILE|-] [-o json|html]
  agent-run pages show FINGERPRINT --source alertmanager [--from-file FILE|-] [-o json|html]

Reads a caller-supplied Alertmanager API v2 alert array or snapshot envelope
from a file or stdin. This is a current snapshot, not notification or delivery
history. Source and observation time are caller assertions; disposition and
recurrence are Unknown. Unreadable or invalid input is Unavailable. It makes no
network requests. Optional --orders-from-file CM.json requires
--orders-observed-at RFC3339 (caller-asserted capture time, five-minute maximum
age; take it after the read). A malformed file or a future capture time is Invalid
and exits 1. An order joins an alert only when its alertIdentity (fingerprint and
startsAt) equals the alert's by exact decoded string equality, it was created no
earlier than startsAt minus 30 seconds. Multiple assertions require one explicit
parentOrder chain; its leaf joins and orderChain lists keys from root to leaf.
Broken, malformed or duplicate lineage remains Unjoined. Real joins need the held
producer drafts merged and installed. The portal
and operator readers stay Unconfigured. See docs/alertmanager-pages-poc.md for
limits and details.
`,
	"fleet": `Usage: agent-run fleet [--resources] [-o json]

Shows the fleet: the current template revision, how many sessions are in each
phase, how many session pods each node runs, the sessions on an older revision,
and every session that holds or waits for a pod, with the scheduler's reason for
each that waits. It reports; it gates nothing.

--resources asks for Dev-Env-Fleet: resources-v1 and adds the bound Pod UID,
the Pod spec node, and each ordinary and init container's configured CPU and
memory requests and limits. Those values are the Pod spec. They are not
measured use or free node capacity. Unknown means the binding or the server
did not supply the observation. absent means the spec does not set that
quantity. 0 means the spec sets zero. An older operator omits the observation
and the command shows Unknown. The request is not sent again without the header.
`,
	"version": `Usage: agent-run version

Prints the version, the commit, the Go version and the platform.
`,
	"help": `Usage: agent-run help [<command>]

Prints help for agent-run, or for one command: run, list, show, log, msg, reap,
suspend, resume, rescue, declare-activity, maintenance, pages, pages-mcp, attach, detach, fleet, version.
`,
}
