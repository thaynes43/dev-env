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
  agent-run inbox send|list|read|ack               durable inbox
  agent-run directory list --project <p> --once [-o json]
                                                  read one authorized directory page
  agent-run canary create --id <id> --once [-o json]
                                                  create or report a trusted fixed original
  agent-run canary result --id <id> --session-uid <uid> --context <name> --json --once
                                                  read one retained fixed publication
  agent-run show <name> [--captured-usage -o json]
                                              show one session; --captured-usage adds direct-child receipts
  agent-run usage-export --manifest <path> --output-dir <private-dir> [--allowance-read <path>]
                                              export selected usage evidence privately
  agent-run log <name> [--tail N]                 print the end of a session's log
  agent-run msg <name> "<text>"                   send a message into a session's TUI
  agent-run reap <name>...                        rescue, stop and archive sessions
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
  agent-run grant use <grant-id>|default         select that kube context in this pod
  agent-run budget create <task-uid> --success "RESULT" [flags]
                                                  create the host's assigned finite campaign
  agent-run budget show <task-uid>               inspect retained campaign authority
  agent-run mcp                                   access tools on stdin for the session
  agent-run breakglass --reason <why>             ask for break-glass; Tom still approves it
  declare-activity start "<what>" --scope <a,b>   declare dev work, so it is not taken for a fault
  agent-run maintenance check --input <file>      offline correlation check; a candidate is not alert deferral
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

Every command but version, help, and maintenance takes these flags:
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
     sent, got no confirmed answer and was not repeated. A message or create
     may have taken effect: observe it, do not repeat it. A one-shot request
     exits 5 only when nothing was sent
`

var commandHelp = map[string]string{
	"auth": `Usage: agent-run auth status [--warn <duration>] [-o json]

Shows how the keeper's shared Claude and Codex logins stand, for a session that
follows them. It reads the access projections mounted in this pod
(AGENTD_CLAUDE_LIVE_FILE and AGENTD_CODEX_ACCESS_FILE) and calls no API. They
hold no refresh token, and no token value is ever printed.

For each login it prints the state (Ready, or NeedsLogin when the access has
expired and the keeper has published nothing newer), the generation, when the
access expires and how long is left, and, for Codex, the keeper's last refresh.
A login the pod does not follow prints NotConfigured. For Claude it also prints
when the whole login lapses (about 30 days after each sign-in) and the days left,
from the non-secret login.json the keeper publishes beside the projection. A
login whose date the keeper did not publish prints "login lapses Unknown"; the
date is never guessed. Codex's login has no known lapse date.

Flags:
  --warn <duration>    report exit 1 when access ends within this window (default 30m)
  --claude-file <path> read this file instead of AGENTD_CLAUDE_LIVE_FILE
  --codex-file <path>  read this file instead of AGENTD_CODEX_ACCESS_FILE (or, when that
                       is unset, /opt/dev-env/codex-access/access.json if it exists)
  -o json              print the rows as JSON

Exit codes, as claude-login-check: 0 every configured login is ready, 1 one has
expired or is inside the warning window, or the Claude login has 7 whole days or fewer left
(rounded down, so 7d23h counts as 7) or has lapsed, 2 cannot tell (a file missing, unreadable or invalid, or
nothing configured). An Unknown lapse date does not change the exit code.
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
	"inbox": `Usage: agent-run inbox send --project <project> --recipient-namespace <namespace> --recipient-name <name> --recipient-uid <uid> --key <key> < message.txt
       agent-run inbox list --project <project> [--limit N] [--continue <cursor>]
       agent-run inbox read <message> --project <project> [-o json]
       agent-run inbox ack <message> --project <project>

Send reads 1..16384 UTF-8 bytes from stdin and requires a stable logical key.
It sends once. If unconfirmed, explicitly retry only the identical intent with
the same key; a changed intent conflicts. List prints a bounded metadata page;
read prints text without acknowledging it. Ack is explicit and proves an API
read acknowledgement, not provider consumption. The server authorizes all
operations; a configured coordinator may read authorized project content but
cannot send or acknowledge for a Session. Send, list and ack print JSON.
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
Control, so the session also shows in the Claude app; its Claude login lives in
its own pod, and agent-run login sets it up (an idle one is suspended after 72h,
which archives its phone entry until it resumes). When a session's pod starts
again later, the agent resumes the same conversation in the TUI; a task is never run twice.
agent-run prints the session's name, then waits up to --wait for the pod to
start. If the scheduler cannot place the pod, it prints the scheduler's reason
as soon as it is clear; the session stays Pending and starts when room frees up.

With --project, the operator selects the repository and rules from its accepted
project catalog. The task uses a private task worktree and the project rules, and keeps the agent's state in
its private home. Omit --repo for a project with one repository; select --repo
for a project with several. Declared projects take task mode only. Their rules
and catalog revision come from the server.

Flags:
  --dry-run=server           requires --project; admission only, never creates or falls back
  --once                     one create POST, no retry/redirect/pod wait; requires --project,
                             --idempotency-key, --wait=0 and -o json
  -p, --prompt <task>        the task, at most 64 KiB
  --local                    an interactive session instead of a task: the TUI, no prompt
  --interactive              --local plus Remote Control (claude: needs agent-run login; codex: a
                             phone daemon with no TUI, paired with agent-run pair)
  --prompt-file <path>       read the task from a file instead; - reads stdin
  --repo <name>              the repository under the GitHub owner, such as haynes-ops;
                             or give it as the first argument. With --project,
                             this selects a repository declared in that project.
  --project <name>           a project declared in the operator's accepted catalog
                             (tasks only; cannot combine with rescue restore)
  --agent claude             the agent (default claude; codex and opencode arrive
                             in plans 04 and 09)
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
                             default; a session's child always runs on its parent's)
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
only when nothing was sent (the API could not be reached at all). A Codex session in
remote mode is reached from the Codex app or its remote-control pairing, not by msg; a
newer pod's refusal says so.
`,
	"rescue": `Usage: agent-run rescue list [--session <name>] [-o json]
       agent-run rescue restore <session>/<stamp> (--local | -p "<task>") [--repo <name>] [flags]

A rescue is the bundle of a session's work, written to the shared volume when the
session is suspended or reaped (D-10, D-48). The bundles never leave the cluster.

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

A reap returns at once; agent-run show <name> follows it until the session is gone.
`,
	"grant": `Usage: agent-run grant list [--session <name>] [--phase <phase>] [--mine] [-o json]
       agent-run grant show <grant-id> [-o json]
       agent-run grant request --type <type> --reason <why> [--no-wait] [type flags] [-o json]
       agent-run grant release <grant-id> [-o json]
       agent-run grant attest <grant-id> --approved-by user|coordinator:<ref> --ref <nonsecret> [-o json]
       agent-run grant use <grant-id>|default

Read, request, release and use access grants (DESIGN-001 6.12). This is part
of plan 07 step 7. Request posts the ask. Release asks the broker to end the
grant. Use selects an installed kube context in this pod. None of these
commands approve a grant or install one. The in-pod tools are agent-run mcp.

grant list prints name, phase, type, session, TTL and expiry, newest first.
  --session <name>   only that session's grants
  --phase <phase>    Pending, Active, Denied, Expired, Released or Failed
  --mine             only the calling session's grants

grant show prints one grant. The name is grant-<id>. It does not print a token. For an
Active credential, kube or break-glass grant it also prints installed: <time> (the last
confirmed install), or installed: pending until the install is confirmed. An Active grant
is not usable in the pod before that; a script can read installedAt from -o json.

grant request asks for one grant for the calling session. It prints the
approval page, then reads the grant every 5s until the phase leaves Pending
or 30m passes. --no-wait returns the pending grant. An identical ask returns
the grant the session already holds.
  --type <type>      kube, egress, credential or breakglass
  --reason <why>     required; Tom reads it on the approval page
  --role <role>      kube catalog role
  --namespace <ns>   repeatable, for kube or breakglass
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
request_access, grant_status and release_access. They call /v1/grants.
request_access asks; Tom still approves. release_access asks the broker to
end one grant. grant_status reads. None of them approve or install a grant.

agentd registers this command as the dev-env MCP server when it renders the
session, unless mcp.json already names a dev-env server.
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
suspend, resume, rescue, declare-activity, maintenance, attach, detach, fleet, version.
`,
}
