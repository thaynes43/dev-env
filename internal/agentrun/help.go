package agentrun

// The help texts. Tom and agents both read them, so each says what a command
// does, what it needs and what it prints, in plain words.

const usageText = `agent-run starts and manages dev-env v2 agent sessions through the operator's API.
Each session is its own pod on a worker node.

Usage:
  agent-run [--repo] <repo> -p "<task>" [flags]   start a task session
  agent-run [--repo] <repo> --local [flags]       start an interactive session (a TUI)
  agent-run list [--repo <r>] [--state <s>] [--mine]
                                                  list sessions, newest first
  agent-run show <name>                           show one session, with its task
  agent-run reap <name>...                        rescue, stop and archive sessions
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

Finding the API. In a session pod agent-run uses the pod's projected token and
AGENTD_API_URL. In any other pod of the cluster, such as the v1 dev-env pod, it
mints a ten-minute token for the pod's own ServiceAccount and calls
` + DefaultAPIURL + `.
Anywhere else, give --api-url and --token-file; Tom's laptop mints its token with
kubectl create token dev-env-human -n dev-env-system --audience dev-env-operator.

Exit codes:
  0  done
  1  failed: the API refused the request, or the session failed to start
  2  usage error: nothing was sent
  3  not authenticated or not allowed: no credentials, a 401 or a 403
  4  no such session
  5  try again later: a limit was reached (429), or the API was unavailable
`

var commandHelp = map[string]string{
	"run": `Usage: agent-run [--repo] <repo> -p "<task>" [flags]
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

Flags:
  -p, --prompt <task>        the task, at most 64 KiB
  --local                    an interactive session instead of a task: the TUI, no prompt
  --prompt-file <path>       read the task from a file instead; - reads stdin
  --repo <name>              the repository under the GitHub owner, such as haynes-ops;
                             or give it as the first argument
  --agent claude             the agent (default claude; codex and opencode arrive
                             in plans 04 and 09)
  --model <id>               a full model id, never an alias such as opus (default
                             $DEV_ENV_CLAUDE_MODEL, else ` + DefaultClaudeModel + `)
  --effort <level>           low, medium, high, xhigh, max or ultracode, as the
                             model takes them (default xhigh, or the highest level
                             below it the model takes; none on models without effort
                             control, such as Haiku 4.5)
  --base <ref>               the ref the session's branch starts from (default origin/main)
  --size S|M|L               the pod's CPU and memory preset (default M)
  --profile <name>           a profile in dev-env-templates (default: the templates'
                             default; a session's child always runs on its parent's)
  --timeout <duration>       stop the task after this long, such as 40m (tasks only)
  --max-turns <n>            stop the task after this many turns (tasks only)
  --idempotency-key <key>    a repeat of the same request with the same key returns
                             the session the first one created, while it is
                             unfinished (default: a new key for each run, which
                             agent-run's own retries reuse)
  --wait <duration>          how long to wait for the pod to start (default 30s;
                             0 returns as soon as the session is created)
  -o name|json               print only the name, or the session as JSON

v1's --interactive (a TUI with Remote Control) arrives with plan 03. --safe is
gone: a session pod runs its agent without approval prompts, and the platform is
the boundary.
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

Shows one session: phase and node, the scheduler's reason while Pending, repo,
agent, model and effort, size, the agent's state, branch and head from its last
heartbeat, how the task ended, usage, conditions and the task itself.
`,
	"reap": `Usage: agent-run reap <name>... [-o json]

Reaps each session: the operator rescues its work to a bundle on the shared
volume, then stops its pod and archives its volume (D-10, D-45). Nothing skips
the rescue, so a reap loses no work; --force is accepted for v1's sake and
changes nothing. Any caller may reap any session. agent-run asks for no
confirmation: it reaps the sessions it is given.

A reap returns at once; agent-run show <name> follows it until the session is gone.
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

Prints help for agent-run, or for one command: run, list, show, reap, attach,
detach, fleet, version.
`,
}
