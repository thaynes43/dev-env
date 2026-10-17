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
  agent-run directory list --project <p> --once [-o json]
                                                  read one authorized directory page
  agent-run canary create --id <id> --once [-o json]
                                                  create or report a trusted fixed original
  agent-run canary result --id <id> --session-uid <uid> --context <name> --json --once
                                                  read one retained fixed publication
  agent-run show <name>                           show one session, with its task
  agent-run log <name> [--tail N]                 print the end of a session's log
  agent-run msg <name> "<text>"                   send a message into a session's TUI
  agent-run reap <name>...                        rescue, stop and archive sessions
  agent-run suspend <name>...                     rescue and stop sessions, keeping their volumes
  agent-run resume <name>                         start a suspended session again, conversation intact
  agent-run rescue list [--session <name>]        list the rescues on the shelf
  agent-run rescue restore <session>/<stamp> (--local | -p "<task>")
                                                  start a session that fetches a rescue
  agent-run history list [--project <p>]          list archived Session histories by project
  agent-run grant list [--session <s>] [--phase <p>] [--mine]
                                                  list access grants, newest first
  agent-run grant show <grant-id>                 show one access grant
  agent-run history show <uid>                    show one archived history's manifest
  agent-run history read <uid> <root/path>        print an archived file, a page or --all
  declare-activity start "<what>" --scope <a,b>   declare dev work, so it is not taken for a fault
  agent-run attach <name>                         attach to a session's TUI (Tom only)
  agent-run detach <name>                         detach every client from it (Tom only)
  agent-run fleet                                 show what runs and waits, by node and revision
  agent-run version                               print the version
  agent-run help [<command>]                      print help for agent-run or one command

agent-run run ... is the same as agent-run ... -p, as in v1. --interactive (a
TUI with Remote Control) arrives with plan 03; prune and sweep are gone, because
the operator reaps sessions itself.

Every command but version and help takes these flags:
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
       agent-run run [--repo] <repo> -p "<task>" [flags]

Starts a session: a pod on a worker node clones the repository and keeps its
work on the session's volume. With -p it runs the agent once on the task,
headless. With --local it starts the agent's TUI in tmux, which Tom attaches to
with agent-run attach. When a session's pod starts again later, the agent
resumes the same conversation in the TUI; a task is never run twice.
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

v1's --interactive (a TUI with Remote Control) arrives with plan 03. --safe is
gone: a session pod runs its agent without approval prompts, and the platform is
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

Read access grants from the operator (DESIGN-001 6.12). This is the list and
show slice of plan 07 step 7. It does not request, use, release or break glass,
and it does not start the in-pod access MCP server.

grant list prints name, phase, type, session, TTL and expiry, newest first.
  --session <name>   only that session's grants
  --phase <phase>    Pending, Active, Denied, Expired, Released or Failed
  --mine             only the calling session's grants

grant show prints one grant. The name is grant-<id>. It does not print a token.
`,
	"fleet": `Usage: agent-run fleet [-o json]

Shows the fleet: the current template revision, how many sessions are in each
phase, how many session pods each node runs, the sessions on an older revision,
and every session that holds or waits for a pod, with the scheduler's reason for
each that waits. It reports; it gates nothing.
`,
	"version": `Usage: agent-run version

Prints the version, the commit, the Go version and the platform.
`,
	"help": `Usage: agent-run help [<command>]

Prints help for agent-run, or for one command: run, list, show, log, msg, reap,
suspend, resume, rescue, declare-activity, attach, detach, fleet, version.
`,
}
