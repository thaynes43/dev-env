# Dot's front-door catalog and runbook

Use the existing front-door bearer at `POST /mcp`. Start with `initialize`,
then use `tools/list`, `resources/list`, and `resources/templates/list`.
`ping` checks transport. The catalog is maintained by the dev-env front-door
maintainers under OP528. Change this page and its discovery test in the same
PR as any tool, parameter, authorization, resource or error contract change.
Change the operating rules revision with any edit to its text.

A missing or wrong bearer gets HTTP 401 and an empty body. Invalid JSON-RPC,
unknown methods and malformed tool calls return protocol errors. A tool
execution error returns `isError: true` with text. Tools and resource reads
share four concurrent slots and one 90-second deadline per admitted call.
A full queue returns `busy, try again`. The server and the operator enforce
the caller’s limits. Call a tool and report the result. A refusal is final.
Record it and do not work around it. When a mutation's outcome is uncertain, read its state using
the authorized scope before making another decision.

## Tools

The table gives `tools/call` argument objects. The examples are templates.
`42`, names beginning with `example-`, and the synthetic note identifiers
stand for a real in-scope target. Honor recorded holds.

- **Tracker** means the front-door `opwork` subprocess, the configured
  OpenProject credential, and the `haynes-engineering` project. Writes are
  attributed to `meatball`. The tracker must be reachable and authorize the
  operation. The project and lock guards apply to groom and order.
- **Operator** means the front door presents the projected
  `dev-env-system/dev-env-meatball` identity, bound by CallerPolicy
  `dev-env-system/meatball`. The operator and route must be reachable and
  enabled for operator calls. The server requires its operator credential
  at startup. Session and parent access is caller-owned. HTTP 401, 403 or 404
  are presented as `refused:` by session and parent tools. Transport failure,
  limits and conflict report their own diagnostics. Parent tools require
  `--enable-parent-notes` on the operator.
- **Local** means the front-door process reads its configured installed text
  or embedded content. The MCP bearer is the caller credential.

| Tool | Executor and action | Arguments and safe example | Scope and failure |
| --- | --- | --- | --- |
| `backlog_find` | Tracker read | Optional string `text`, string `status` (comma-separated names), integer `epic`. Example `{"text":"front door","epic":42}`. | Bounded project search. Unknown status or backend error fails. Results are a page. |
| `backlog_show` | Tracker read | Integer `id`. Example `{"id":528}`. | Reads one item in the configured project. An out-of-project item is refused before showing text. |
| `backlog_create` | Tracker write | Integer `epic`, nonblank strings `title` and `scope`, optional string `acceptance`. Example `{"epic":42,"title":"Example authorized story","scope":"Document the approved change","acceptance":"Record the verified result"}`. | Search first. Title is one line, at most 255 bytes. Scope and acceptance are at most 100,000 bytes each. Only a valid in-project parent. Same source key and matching text can confirm an earlier create. Conflicting text or likely duplicate refuses. |
| `backlog_groom` | Tracker write | Integer `id`, object `fields` with at least one of string `description`, `acceptance`, `constraints`. Example `{"id":42,"fields":{"constraints":"Keep the approved scope"}}`. | Each supplied value is nonblank and at most 100,000 bytes. Project, tracker authorization and lock refusals stop the update. An update with an unconfirmed readback keeps its attribution intent. |
| `backlog_order` | Tracker write | Integer `id`, nonblank string `priority`. Example `{"id":42,"priority":"High"}`. | Uses a known tracker priority name, case-insensitively. An unknown priority, project or tracker authorization refusal, or lock conflict stops it. Read back an uncertain write. |
| `backlog_close` | Tracker completion write | Integer `id`, nonblank string `evidence` up to 100,000 bytes. Example `{"id":42,"evidence":"Approved PR and CI receipt for the reviewed head"}`. | Explicit holds prevail. Any coordinator takeover needs exact owner and item match, terminal success or failure, pod reclaimed and unchanged lock. Unconfirmed claim or completion returns its readback state. Observe tracker acceptance before reporting it. |
| `coordinator_start` | Operator dispatch write | Integer `item`, exactly one string `repo` or `project`, string `model`, integers `hours` and `workers` from 1–4, optional string `title`. Example `{"item":42,"project":"dev-env","model":"gpt-6-astra","hours":1,"workers":1}`. | Requires approved ready work, chosen model and bounded budget. The caller is limited to fresh tasks on profile `dev` and two unfinished sessions. Invalid input, policy bounds, ambiguous retained lookup or uncertain creation stop. Check status before acting on an uncertain create. |
| `session_status` | Operator read | Exactly one nonempty string `name` or integer `item`. Example `{"item":528}`. | A caller-owned session can return a retained outcome with PR and head. An item lookup can be absent or ambiguous. Parent-note count can say `unavailable` or `incomplete` independently. |
| `session_stop` | Operator lifecycle write | String `name`. Example `{"name":"example-session"}`. | Reap requires separate authorization and preservation verification. Only a caller-owned session is eligible. `reaping: true` is provisional until an authorized readback confirms the outcome. |
| `waiting_on_tom` | Tracker and Operator read | No arguments. Example `{}`. | Tracker returns Needs approval and Needs answer. The Asks scope for this identity is unavailable and the tool reports that alongside the tracker half. A tracker error is a tool error. |
| `standing_authority` | Local read | No arguments. Example `{}`. | Uses the configured owner text only. An absent, missing or empty file returns a fixed no-authority sentence. Unreadable, non-regular, over 4096 bytes, invalid UTF-8 or control characters other than newline and tab produce a tool error. |
| `parent_waiting` | Operator read | Optional integer `limit` (1–100, default 100), optional string `continue`. Example `{"limit":10}`. | Reuse the opaque cursor from the same listing, at most 4096 bytes. Invalid cursor or scope refuses. An empty page with `continue:` still has a next page. |
| `parent_read` | Operator read | String `name`. Example `{"name":"message.00000000000000000000000000000001.00000000000000000000000000000002"}`. | Use the exact note name returned by `parent_waiting`. Missing, disabled or wrong-scope notes return no text. |
| `parent_answer` | Operator write | String `name`, `key`, `text`. Example `{"name":"message.00000000000000000000000000000001.00000000000000000000000000000002","key":"example-answer","text":"Proceed with the approved check."}`. | Reply to an authorized open note. Same key and text can confirm the answer. Changed or second different intent is refused. |
| `parent_send` | Operator write | String `session`, `key`, `text`. Example `{"session":"example-session","key":"example-note","text":"The approval has been recorded on the item."}`. | Send only to a caller-owned session for authorized work. The session sees it on its next parent read. Same key and text can confirm the send. Changed intent conflicts. |

Tool IDs are integers from 1 to 1,000,000,000. Session names for status and
stop follow the operator's session naming rules. Parent note names are
`message.<32 lowercase hex>.<32 lowercase hex>`. A parent key is 1–128 UTF-8
bytes without NUL, CR or LF. Parent text is 1–16,384 UTF-8 bytes. The
`parent_send` session name is at most 253 bytes with no slash, ASCII
whitespace or NUL.

For dispatch, give one repository name under the configured GitHub owner
or one declared project slug. The full model IDs are `claude-opus-5-5`,
`claude-fable-5-2` and `gpt-6-astra`. Codex requires `project`. The
operator supplies its preset effort. A client-supplied `effort` is refused.
The optional title is trimmed, set at create and at most 48 code points and
96 bytes. Use a short role title beginning with a letter or digit. The
per-item idempotency key protects an existing retained coordinator record.
A disappeared record leaves a repeat eligible for a new start.

## Resources

Each `resources/read` requires a string `uri`. The list methods have a single
page and accept omitted parameters or `{}`. All three methods ignore
`_meta` and refuse any other extra key. Each example below is the URI value
for the request.

| URI | Executor and action | Example and boundary |
| --- | --- | --- |
| `dev-env://catalog/v1` | Local read | `dev-env://catalog/v1` returns this page. |
| `dev-env://operating-rules/v1` | Local read | `dev-env://operating-rules/v1` returns rules, schema, version, revision and SHA-256 of the returned UTF-8 text. Use `standing_authority` for separately installed owner instructions. |
| `dev-env://handoff/v1` | Local read | `dev-env://handoff/v1` returns the lookup instructions and project boundary. |
| `dev-env://handoff/v1/{item}` | Tracker read | `dev-env://handoff/v1/528` resolves one item with the existing project and tracker permissions. Read its `owner`, `source`, `targetScope`, `workState`, `evidence`, `nextAction` and `capturedAt`. |

Use a canonical positive decimal item ID, within signed 64-bit range. The
handoff projects current tracker status and a bounded set of journal
references. Its pickup guidance is conservative when evidence is missing,
stale, malformed, blocked, carries a different owner label or comes from
incomplete history. The checkpoint check establishes label consistency
with the recorded owner. Authorship is unverified. When the state is
`present`, confirm custody with the recorded owner before acting. A caller
with tracker authorization can consult the referenced journal. The installed owner text, tracker record,
and an independently authorized dispatch each carry their own scope.

| Failure | Expected result |
| --- | --- |
| Missing or wrong bearer | HTTP 401 and zero body bytes. |
| Malformed URI, including alias, extra segment, query on the template or a leading zero | JSON-RPC `-32602 invalid resource URI`. |
| List parameters with a key other than `_meta`, including a cursor | `-32602 invalid resource list parameters`. |
| Unknown resource or absent or hidden item | `-32002 resource not found`. |
| Tracker authorization error or item outside the configured project | `-32003 handoff read refused`. Stop without another identity or route. |
| Tracker failure, timeout, inconsistent read or invalid projection | `-32004 handoff tracker unavailable`. |
| Four calls already executing | `-32005 busy, try again`. |

## Weekly allowance

The front-door bearer currently has no supported usage read. The usage
read is separate from both handoff reads and session status. Existing
authorized paths are the signed-in portal Usage page under its operator
reader identity, and one `agent-run usage --read-current -o json` call under
an admitted session's own operator identity. The latter performs one
cached `GET /v1/provider-allowances`. Its underlying session reader scope is
checked by the operator. The CLI reports `agent-run: usage read refused`
on 401 or 403 and `agent-run: usage read unavailable` on missing route,
connection failure or invalid response. An independently authorized
ConfigMap reader can inspect the store for its own work.

| Dimension | Meaning |
| --- | --- |
| Provider and source | Passive readings identify Claude (`anthropic`) via the statusline or Codex (`openai`) via rollout events. The cached read labels these `session-v1`. |
| Capture and report | Store `readAt` is when the CLI got the data. Store `reportedAt` is when the operator recorded it. The passive cached API uses `readAt` for both `observation.capturedAt` and `receivedAt`. Its true operator report time is Unknown from that response. The API serve time is separate. |
| Windows and resets | `10080` minutes is the seven-day allowance. A supplied short window is separate, commonly `300` minutes with its own reset. `usedPercent` is the fraction used in that particular window. `resetsAt` (API `window.resetAt`) is the provider-reported UTC instant. |
| Freshness | A passive portal reading is recent through 30 minutes after capture while the reset is ahead. Older is last reported. Once a window resets, its percent is no longer shown as usable. Provider freshness and account binding stay Unknown for passive observations. |
| Missing fields | Unknown is an unverified or absent reading or dimension. Keep it distinct from a refusal, a zero, a last-reported value and the serving clock. There is no per-model allowance here. Per-session model, token and cost metrics are separate. Cursor's plan allowance is on Cursor's dashboard. Grok is Not connected. |

The passive source and portal display are documented in
[`operator-session-allowances.md`](../../docs/operator-session-allowances.md).
OP509 tracks the seven-day durability and freshness acceptance.

## Dot's live smoke checklist

Use Dot's existing MCP client and bearer after the coordinator records the
installed image and authorized scope. The receiving agent must already be
authorized for the handoff read. Give it the resource URI and context for
the receipt. Keep credentials and raw answers out of the receipts.

1. Record UTC start, client identity and read scope. Check `initialize`,
   list tools and resources, read the rules and verify the digest.
   An unauthenticated check expects HTTP 401 and an empty body.
2. Verify the **usage** scope separately. With today's bearer,
   `resources/read` of `dev-env://usage/v1` gives
   `-32002 resource not found`. Record that gap. A least-privilege usage
   success can be recorded when a separately reviewed GET-only Dot
   contract is installed. Record response time, provider, source,
   capture and report time or Unknown, weekly and supplied short windows,
   percent freshness and reset. Record a 401/403 as a refusal and stop.
   Record a missing reading, old capture or elapsed reset as uncertainty.
3. Verify the **handoff** scope with an existing in-project item such as
   OP528. Read its URI, record the result and send the URI through the
   approved custody channel. The second receiving agent makes its own
   read and records UTC time, item, owner, status, journal references and
   next action. Compare the lock and source revisions or explain a newer
   observation. Record each reader's redacted receipt on OP529. A
   malformed URI gives `-32602 invalid resource URI`, a missing item
   gives `-32002 resource not found`, a scope refusal gives
   `-32003 handoff read refused`, and a tracker outage gives
   `-32004 handoff tracker unavailable`. Verify these branches from
   approved evidence, with no synthetic tracker writes or outage.
4. Link any existing approved OP529 status/dispatch evidence for its own
   scope. Record source PR and CI, installed pin and live results
   separately. Tom's disposition on OP529 completes acceptance.
