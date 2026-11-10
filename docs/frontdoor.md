# Meatball's front door

`dev-env-frontdoor` is the MCP server Meatball calls. It serves streamable HTTP
at `POST /mcp` and speaks JSON-RPC. This page is the behaviour. The design is
[DESIGN-060](../.agents/sagas/distributed-dev-env/designs/060-meatball-front-door.md).
This repository does not install the cluster objects below, does not create
a secret, and does not install the standing-authority text.

The process accepts one bearer token. A request without that exact token gets
401 and an empty body, on every path, including `OPTIONS *`, with no
`WWW-Authenticate` header. The token is read from a file on each request and
is never logged. A log line names a fixed HTTP method, the path `/mcp` or
`unknown`, and a fixed JSON-RPC method (`initialize`, `ping`, `tools/list`,
`tools/call`, or `unknown`). It does not contain a raw path or a raw method.
Tool text is scrubbed of that token and of an OpenProject `opapi-` token
before it is returned.

## Flags

| Flag | Required | What it is |
|---|---|---|
| `--token-file` | yes | The one MCP bearer token. |
| `--tracker-token-file` | yes | The OpenProject credential opwork already uses. A separate file from the bearer. |
| `--operator-token-file` | no | This process's projected ServiceAccount token for the operator. Empty uses `/var/run/secrets/dev-env/token`. |
| `--api-url` | no | Operator API. Empty uses `https://dev-env-operator.dev-env-system.svc.cluster.local:8443`. |
| `--ca-file` | no | PEM CA for that API. |
| `--listen` | no | Listen address. Default `:8080`. |
| `--opwork` | no | opwork executable. Default `opwork`, which the image installs on `PATH`. |
| `--project` | no | OpenProject project. Default `haynes-engineering`. |
| `--authority-file` | no | Standing-authority text for this caller. `standing_authority` re-reads it on every call. Absent, missing or empty returns a fixed sentence. |

The operator token is sent only to the operator, as `Authorization: Bearer`.
The tracker credential is passed to opwork by file path (`OPWORK_TOKEN_FILE`)
and is not an argument. Neither file is logged. The process does not call the
Kubernetes API and does not mint a token. A missing bearer file or tracker
file stops it before it listens. A missing authority file does not.

## Tools

`tools/list` returns these eleven tools. Each description is at most 30 words,
and the descriptions together are 142 words.

| Tool | What it does |
|---|---|
| `backlog_find` | Find work items by text, status, or parent epic. Read only. |
| `backlog_show` | Read one work item, including scope, acceptance, and constraints. |
| `backlog_create` | Create a story under an epic. Search first. The write is attributed to meatball, not Tom. |
| `backlog_groom` | Replace an item's scope, acceptance, or constraints, then read the item back. |
| `backlog_order` | Set an item's priority, which decides what goes next, then read it back. |
| `backlog_close` | Close an item with evidence. An explicit hold stays a hold. |
| `coordinator_start` | Start one coordinator for an item. Model is required and is the full id Tom named. No effort. |
| `session_status` | Read one session this caller started, by name or item, including any retained outcome. |
| `session_stop` | Stop one session this caller started. A refusal from the operator is reported plainly. |
| `waiting_on_tom` | List open Asks for this caller's sessions and items that need Tom's approval or answer. |
| `standing_authority` | Read the owner's standing instruction installed for this caller. |

A tool failure is a result with `isError` true and a plain-text message. An
unknown tool name is a JSON-RPC error.

### Backlog

The backlog tools run `opwork` with a fixed argument list. They do not build a
shell command. Every write sets `OPWORK_WRITE_ORIGIN=meatball` and
`OPWORK_SESSION=meatball`, so the activity comment or the create provenance
carries a line `Origin: meatball`. opwork refuses a write whose session is Tom.

`backlog_groom` takes `id` and `fields` (`description`, `acceptance`,
`constraints`). At least one field. The text is written to a private temp file
and passed as `--description`, `--acceptance` or `--constraints`.
`backlog_order` takes `id` and `priority`. `backlog_close` takes `id` and
`evidence` and runs `opwork complete` without `--force`. `backlog_create` takes
`epic`, `title` and `scope`, and optional `acceptance`, and sends a source key
so a repeat finds the same item.

The same two commands exist on opwork itself, for a person or a session:

```
opwork groom ID [--description FILE|-] [--acceptance FILE|-] [--constraints FILE|-]
opwork order ID --priority PRIORITY
```

One groom field may be stdin (`-`). Both commands read the item back and print
it. They keep opwork's rules: the write is attributed, it sends `lockVersion`,
create stays search-first, and nothing writes as Tom.

`backlog_show`, `backlog_groom`, `backlog_order`, `backlog_close`, and the
epic on `backlog_create` refuse when that item is not in `--project`. opwork
checks the item when it resolves it, before it prints the item or writes. The
refusal names the project and does not include the item's text. The same check
applies to `opwork show`, `groom`, `order`, `complete`, and the parent epic of
`create`.

`order` reads priority names from the exact path `/api/v3/priorities`. The
tracker client allows that path and no other priorities path.

If groom or order applied a change, or may have, and the tracker then failed,
opwork reports an unconfirmed mutation and keeps the intent. It does not say
the read failed. The retained groom record stores the field text, not the path
of the temp file this server removes when the process exits. `flush` retries
the attribution note with the original text.

opwork rejects a tracker HTTP body larger than 8 MiB before it decodes JSON.
A scan, which `search` uses for a source key or an owner and which create uses
to find a source key and likely duplicates, reads 100 items to a page and keeps
only the rows that match. Its memory is one page plus the matches, however
large the project is. It stops at 500 pages (50,000 items read) or 2000
matching rows, and past a bound it refuses instead of returning a partial
list. These bounds are opwork's own, so they hold for every agent, not only for
this server. On 2026-11-10 the project had 326 items and a 100-item page was
about 1.2 MiB.

### Coordinator sessions

`coordinator_start` takes `item`, exactly one of `repo` or `project`, `model`,
`hours` (1 to 4) and `workers` (1 to 4). `model` is required. There is no
default and there is no effort argument. An `effort` field is refused before
any operator request, and so is a missing or unknown model. The accepted full
ids are `claude-opus-5-5`, `claude-fable-5-2` and `gpt-6-astra`. The Codex id
needs `project`.

The call is `agent-run coordinator start`: the same prepare, lookup and
`POST /v1/sessions` body, including the embedded brief, the idempotency key
`coordinator-<item>`, profile `dev`, mode `task`, and `limits.timeout` and
`limits.maxWorkers`. The body has no effort key. The operator writes the
model's preset itself.

`session_status` takes a session `name` or an `item`, not both. `session_stop`
takes a session `name`. The operator allows either only for a session this
caller started. A 401, 403 or 404 comes back as `refused:` and the operator's
message. The text includes the session name, state, the OpenProject item link
when the item is known, and a retained outcome when the session has one.

### Waiting on Tom

`waiting_on_tom` asks the operator for this caller's open Asks and asks opwork
for items in `Needs approval` or `Needs answer`. The operator's Asks route
admits a session caller only, so this ServiceAccount has no route. The tool
then returns the tracker half and one sentence: the operator has no route that
lists open Asks for sessions this caller started. When a route exists, the
tool lists open Asks by id, session name, item and question. It does not print
a session UID.

## Annotations

Every tool carries an `annotations` object with `readOnlyHint`,
`destructiveHint`, `idempotentHint` and `openWorldHint`. Those four fields are
`Tool.annotations` (`ToolAnnotations`) in the MCP schema for protocol
2025-03-26 and for 2025-11-25. The names and the nesting are the same in both.
This server follows that shared object and sets every field, so a client does
not fall back to the schema defaults. In both schemas an omitted
`readOnlyHint` means false, an omitted `destructiveHint` means true, an
omitted `idempotentHint` means false, and an omitted `openWorldHint` means
true. An unannotated tool is therefore not read-only, destructive and
open-world.

| Tool | readOnlyHint | destructiveHint | idempotentHint | openWorldHint | Why |
|---|---|---|---|---|---|
| `backlog_find` | true | false | true | false | It only searches the tracker. A repeat writes nothing and deletes nothing. |
| `backlog_show` | true | false | true | false | It only reads one item. A repeat writes nothing and deletes nothing. |
| `backlog_create` | false | false | true | false | It creates an item and keeps it. A repeat with the same epic and title reuses that source key and writes nothing when the text still matches. |
| `backlog_groom` | false | false | false | false | It sets field text. `opwork groom` patches and adds a journal note on every call, so a repeat is not the same state. Nothing is deleted. |
| `backlog_order` | false | false | true | false | It sets priority. `opwork order` writes nothing when that priority already matches. Nothing is deleted. |
| `backlog_close` | false | false | true | false | It closes an item and keeps it. `opwork complete` writes nothing once the item is already Accepted. |
| `coordinator_start` | false | false | true | true | It starts an agent that can reach the web. A repeat returns the retained session for `coordinator-<item>` and does not post another create while that record exists. |
| `session_status` | true | false | true | false | It only reads one session this caller started. A repeat writes nothing. |
| `session_stop` | false | true | true | false | It reaps the session. Clean-up then removes the task and keeps only rescued unpushed work. A second reap during that delete changes nothing. |
| `waiting_on_tom` | true | false | true | false | It only lists Asks and held items. A repeat writes nothing and deletes nothing. |
| `standing_authority` | true | false | true | false | It only reads the configured authority file. A repeat writes nothing and reads no other path. |

`backlog_create` builds the source key `meatball-<epic>-` plus the first eight
bytes of SHA-256 of the epic, a newline and the title. `opwork create` searches
for that key first. When one item exists and its subject and supplied text
still match, it reuses the item and writes nothing. When the text no longer
matches, it refuses and writes nothing.

`coordinator_start` calls `ExecuteCoordinatorStart`, which looks up the
caller's retained session for `coordinator-<item>` and returns it without
posting another create. This server does not pass a run label. After that
Session record is deleted, a repeat starts a new session. The lookup is not a
cross-replica lock; section 7.2 of the design states that limit. The started
agent uses profile `dev`, which has web egress, so the tool is open-world.

`opwork complete` writes an evidence comment and then accepts. A repeat after
Accepted writes nothing, which is why the hint is idempotent. A close that
stopped short of Accepted can record evidence again.

## Instructions

`initialize` returns `instructions`, the `InitializeResult` field in both
schemas, beside `protocolVersion`, `capabilities` and `serverInfo`. The text
is 58 words:

> This server is Meatball's front door to the haynes-engineering tracker and to coordinator sessions. The server and the operator enforce the caller's limits, so call a tool and report the result rather than asking the owner first. A refusal is final. Record it and do not work around it. standing_authority returns the owner's standing instruction for this caller.

## Standing authority

`standing_authority` takes no arguments. On every call it re-reads the file
named by `--authority-file` (`Config.AuthorityFile`). The path comes only from
that flag. A call cannot name a path. The file must be a regular file of at
most 4096 bytes and valid UTF-8. A larger file is a tool error and is never
truncated. A control character other than newline (U+000A) and tab (U+0009),
using Go's `unicode.IsControl`, is a tool error. Carriage return is refused.
With no flag, or a missing or empty file, the tool returns exactly: "No
standing authority is installed for this caller. Ask the owner once, and
record the answer on the work item."

The authority text is installed by haynes-ops as a ConfigMap and is Tom's
text, changed only by a PR he approves. This repository installs none.

## Limits

Each tool call has one deadline of 90 seconds. The operator client and the
opwork process share it, and a client disconnect cancels both. At most 4 tool
calls run at once. Another call is a tool error, `busy, try again`, and starts
no operator request and no opwork process.

## What the cluster needs

haynes-ops installs these. This repository does not.

- **ServiceAccount** `dev-env-system/dev-env-meatball`. A projected token for
  the audience `dev-env-operator`, mounted at `/var/run/secrets/dev-env/token`.
  The process needs no other Kubernetes right.
- **CallerPolicy** `dev-env-system/meatball`, admitting that account. The
  contract is DESIGN-060 section 7.1: mode `task`, profile `dev`, the three
  full model ids, at most 4 hours, at most 4 workers, at most 2 at once. The
  account may `POST` and `GET /v1/sessions` and `GET` and `DELETE` one session
  it started. It gets no exec, no grants, no workbench profile, and no other
  caller's sessions. This server does not change that policy.
- **Internal ingress** to the Deployment, path `/mcp`.
- **One Secret** for the MCP bearer, mounted as a file and passed as
  `--token-file`.
- **The tracker credential**, a separate file passed as
  `--tracker-token-file`. It is the OpenProject token, not the bearer.
- **Standing authority**, optional. A ConfigMap of Tom's text, mounted as a
  file and passed as `--authority-file`. Changed only by a PR Tom approves.
  This repository does not install it. With no file the tool returns the
  fixed sentence above.

The image is `ghcr.io/thaynes43/dev-env-frontdoor`. `publish.yml` tags it
`sha-<short>` from main and signs that digest. A pull request builds the image
and does not push it.
