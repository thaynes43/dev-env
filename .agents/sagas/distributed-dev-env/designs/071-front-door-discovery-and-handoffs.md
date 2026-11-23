# DESIGN-071 | Front-door discovery, handoffs and retired sessions

2026-11-23. Work order wo-528a, OP528 journals 9574–9576 and 9578. OP509 owns
durable usage and OP529 owns live acceptance. Decision record:
[ADR-033](../adrs/033-front-door-read-boundaries.md).

## Baseline

At b113a505, the bearer gives Dot fifteen MCP tools for the tracker, scoped
sessions and parent notes, and a configured standing-authority file. The
comparison is in OP528 journal 9583. The bearer supplies neither a weekly usage
reader nor a transferable work-item handoff. The installed operator session
allowance store and the portal described in [DESIGN-059](059-durable-usage-collection.md)
have a separate reader boundary. The approved OP529 start, status, outcome and
close exercised session and tracker authority. The usage and handoff smoke
tests have their own acceptance evidence.

## Discovery and reads

Keep streamable HTTP and the bearer check at the existing exact `/mcp`
endpoint. `initialize` advertises `resources`. `tools/list` returns the
tool schemas and annotations. `resources/list` is one fixed page with three
entries:

| URI | Content |
| --- | --- |
| `dev-env://catalog/v1` | The maintained Dot catalog and runbook, embedded from source. |
| `dev-env://operating-rules/v1` | A JSON envelope with schema `dev-env.operating-rules`, numeric version, content revision, SHA-256 and UTF-8 text. The digest covers the exact text returned. |
| `dev-env://handoff/v1` | The scope, lookup template and usage of the work-item resource. The index contains no item IDs. |

`resources/templates/list` advertises `dev-env://handoff/v1/{item}`.
Replace `{item}` with one canonical positive decimal OpenProject ID. Every
entry is readable using `resources/read` with a `uri` parameter. Queries,
fragments, encoded aliases, leading zeroes, signs, foreign authorities and
extra path segments have no meaning. These URIs address server handlers. A
client cannot make the server fetch a URI or open a file.

The rules are an embedded, reviewed operating guide based on the common rules,
the repository's `CLAUDE.md`, and OP528's retirement ruling. Its revision
advances with a content edit. It includes the coordination and hold boundaries
necessary for pickup, and points to the controlling sources. An installed
session's exact common inputs and revision stay in the existing
`internal/commonrules` contract. Owner-specific installed authority stays in
`standing_authority`. The packaged rules grant no authority and claim no
matching installed revision. [MCP resources specification](https://modelcontextprotocol.io/specification/2025-03-26/server/resources)

### Rules maintenance proposal for Tom

Add this line to the repository's `CLAUDE.md` under the rule about updating
docs in the same PR as behavior changes.

> When pickup, custody, hold or retirement rules change in `CLAUDE.md` or the
> common rules, whoever changes the source rule also updates
> `internal/frontdoor/operating-rules.md` and its revision in the same PR.

### Initialize instructions

`initialize` returns this 68-word text.

> This server is Meatball's front door to the haynes-engineering tracker and to coordinator sessions. The server and the operator enforce the caller's limits, so call a tool and report the result rather than asking the owner first. A refusal is final. Record it and do not work around it. standing_authority returns the owner's standing instruction for this caller. Read dev-env://catalog/v1 for how to use each tool and resource.

The final sentence points clients to the catalog for tool and resource usage.
The preceding sentences retain the original instructions.

## Work-item handoff v1

The front door runs a fixed `opwork show ID --handoff-json` invocation using
its existing tracker credential. The same `resolve_item` project fence as
`backlog_show` applies before the activity read and is checked again before
returning the projection. The credential's existing item and activity GET
permissions apply. Read one bounded activity page using the check-in
contract. A changing item lock or inconsistent metadata fails unavailable.
The projection stays within the current item scope. It does not consult an old
session, local pending files, provider homes, parent notes or the operator.
The server requires the operator credential at startup and rereads it for
each operator request. A loopback resource test uses a non-secret placeholder
operator token, a closed local API port and a stub opwork.
An offline `opwork checkpoint` reaches the durable source only when
`flush` confirms it in the tracker. An existing `checkpoint-update` marker
can identify a structured checkpoint and its sourcing journal.

The resource's JSON text uses these fields, all present:

| Field | Meaning |
| --- | --- |
| `schema`, `version` | `dev-env.work-item-handoff`, `1`. |
| `owner` | The recorded execution owner in the recognized opwork identity format, or `Unknown`. |
| `source` | OpenProject, the item URL and lock version, item update time, and checkpoint observation state. |
| `targetScope` | Configured tracker project and exact item. |
| `workState` | Tracker status and whether it carries an explicit hold, or an unknown hold when status is unrecognized. Only known workflow names are projected. |
| `evidence` | Bounded journal references with capture timestamps and evidence kind. An empty list means no confirmed reference is in the projection. A journal reference records an available source, with no claim that its contents satisfy acceptance. |
| `nextAction` | Fixed pickup guidance derived from the item hold/state and the checkpoint observation. An unheld in-progress item calls for confirming custody with the owner. A held item stays held. For `present`, confirm custody with the recorded owner before acting. |
| `capturedAt` | UTC time of the projection following the tracker reads. |

The resource retains structured checkpoint references to make pickup possible
after the prior session is gone. Raw comment bodies, a checkpoint's free-form
text, item descriptions, arbitrary author labels, arbitrary URLs, credentials,
private addresses and transcripts stay out of this projection. The recipient
uses the references to consult records under their own authorization. A
receiver with only the bearer learns the owner, state, any hold and whether a
checkpoint exists. It cannot read the referenced journal or the recorded
next step.
Tracker text and the resource are context. Record the receiving agent's
receipt on the item and verify the owner and hold through the supported
workflow before any action. The resource makes no claim, transfer, dispatch,
acceptance or lifecycle decision.

The checkpoint check establishes label consistency with the recorded owner.
Authorship is unverified. Another tracker user can post the same label and
produce a `present` checkpoint. Missing, malformed, stale, blocked,
owner-unconfirmed and incomplete observations have explicit states. A
checkpoint whose check-after has passed or that marks itself stale supplies
no current pickup guidance. A blocked or missing-ACK checkpoint takes
priority over staleness. While an unchanged checkpoint’s far-future
check-after remains in the future, later ordinary journal activity leaves it
`present` until the owner changes. The work-item state remains observed.
Dates are recorded at their source. Reading the handoff does not refresh an
old checkpoint.

## Authorization and audit

| Action | Authority and executor | Audit |
| --- | --- | --- |
| Discovery and packaged rules | Current MCP bearer. The front door reads embedded files. | Fixed RPC and resource labels. |
| Handoff read | MCP bearer, configured project fence, existing opwork/OpenProject item and activities GET. A cross-project or tracker auth denial stops this operation. | Front-door `resources/read handoff` and a fixed outcome. Tracker request auditing supplies the actual upstream actor and target when configured. |
| Usage read | Existing `GET /v1/provider-allowances` enforces its configured portal or selected session reader scope using that reader's own token. Dot's front-door identity lacks this grant. A future Dot read needs a reviewed exact GET-only admission with a bounded response projection. | Operator method, fixed path, authenticated caller and status. A future front-door read would also log a distinct fixed usage label and outcome. |
| Handoff write or dispatch | A separately authorized action by the recorded item owner in opwork, with its ownership and hold guards. Dispatch uses existing `coordinator_start` and needs the caller's applicable approval, ready item, model and budget, and CallerPolicy admission. | Attributed tracker journal and readback or fixed `tools/call coordinator_start` plus operator create audit and the retained item outcome. |

The shared transport has a single bearer identity. Its audit log cannot
distinguish people or clients holding it. It logs request time, allowlisted
operation and result, with no URI, item ID, arguments, result text or bearer.
The client receipt can reference the item, timestamp and redacted status.
Every resource read uses the same four slots and 90-second deadline as tool
calls. The resource list methods take no call slot. A client disconnect
cancels tracker work.

## Failure and uncertainty

| Branch | Front-door result or source behavior |
| --- | --- |
| Wrong or absent bearer | HTTP 401, empty body. |
| Invalid read parameters or malformed URI | JSON-RPC `-32602`, `invalid resource URI`. No tracker call. All three methods ignore `_meta` and refuse other extra keys. List methods have no cursor. |
| Well-formed unregistered resource or absent/hidden work item | `-32002`, `resource not found`. A masked tracker 404 cannot establish why the item is hidden. |
| Project exclusion or tracker 401/403 | `-32003`, `handoff read refused`. Stop and record the result. |
| Tracker failure, deadline, missing CLI support, changing or malformed source | `-32004`, `handoff tracker unavailable`. No partial handoff. |
| All call slots in use | `-32005`, `busy, try again`. No upstream call. |
| Missing, stale, blocked, malformed or unconfirmed checkpoint | Successful current item projection with explicit checkpoint uncertainty where the item and bounded history can be read. No checkpoint authority is inferred. |
| Usage with only the front-door bearer | No usage resource or tool is registered. A guessed usage URI is `resource not found`. |
| Existing usage GET unauthorized | HTTP 401 or 403. Stop on an auth or scope refusal and retain its diagnostic. |
| Existing usage GET missing, conflicting or unavailable | A disabled or absent route, conflict, transport failure and invalid body keep their own CLI outcome. The usage CLI does not follow redirects or retry. |
| Passive usage telemetry absent, too old or past reset | Report Unknown or last reported with the source time. The seven-day window is 10080 minutes. A shorter supplied window has its own reset. Age is measured from CLI capture (`readAt`); storage time (`reportedAt`) is a separate receipt. The 30-minute display threshold does not verify the provider or bind an account. No allowance per model is inferred. |

## Retired-session rule

The operating rules carry this rule. A session is active when it has live or
pending work, a hold, a live or unconfirmed thread, or unconfirmed activity.
Unconfirmed pending work also keeps the session active for custody. A
confirmed inactive session with a clear hold and reap state remains
`abandoned` and kept for review when the retirement decision or successor
disposition is unconfirmed. Other incomplete observations and preservation
checks produce `blocked` and keep the session. A retire candidate is a
confirmed inactive session with a separately reviewed retirement decision
and verified preservation. A paused or suspended
phase and elapsed time alone are insufficient.

Before retirement, verify the durable checkpoint on its work item, the
session's exact branch and head, every unpushed commit and uncommitted edit,
and the receiving agent's acknowledgment if there is a successor. If no
successor is needed, record that decision. Verify the applicable reviewed
retention and legal or operational holds. One published branch alone doesn't
prove a clean or preserved worktree. A listed rescue alone doesn't prove that
it contains or can recover the required edits.

After an exact authorized retirement decision the supported lifecycle reaps
the retired session. Record request, rescue and observed outcome on the work
item, retaining only what the reviewed retention rule calls for. If
preservation or removal is unconfirmed, keep the session where possible,
report the uncertainty and seek a supported readback. Never treat the reap
request as a removal receipt. No repeated or destructive action follows an
uncertain result. The classifier only proposes review. Unknown input keeps
custody, and the inventory is read-only. The platform already archives a
session suspended for seven days with a valid rescue, independently of this
rule.

For a dry run, build the offline classifier and give it a sanitized snapshot:

```sh
GOMAXPROCS=2 nice -n 19 go build -p 2 -o bin/session-retire-dry-run ./cmd/session-retire-dry-run
bin/session-retire-dry-run < /private/sanitized-records.json
```

The input is a JSON array of records with `session` and `phase`. Record
`execution` as `live` or `stopped`, `hold` as `present` or `clear`, and
`reap` as `pending` or `clear` only when confirmed. Record `nativeThread`
and `activity` as `live` or `clear`, and `pendingWork` as `present` or
`clear`. Each needs an explicit `clear` to classify the session inactive.
For `retirement`,
`checkpoint`, `branchAndHead`, `unpushedCommits`, `uncommittedChanges`,
and `retention`, use `verified` only for reviewed evidence for that exact
session and stopped workspace. Use `acknowledged` or `not-required`
for `successor` only with the matching receipt or recorded decision.
Leave unknown fields absent. The output provides the class, keep or
review decision and fixed reasons. Snapshot assertions are evidence inputs,
and any later authorized action requires fresh, independently verified proof.

## Access changes proposed, not made

Manifest references below were read at haynes-ops commit
`158abfd8232a78d00d935236f6027f8e3f4db5c1`.

- Serve resources during an operator credential outage.
- Verified checkpoint provenance needs a per-session tracker identity or a signed marker.
- A handoff v2 could return the checkpoint’s `next_action` and `evidence`
  only for state `present`. Bound and scrub both fields and mark them as
  unverified tracker text.
- Dot usage needs an exact `GET /v1/provider-allowances` admission for the
  identity bound by `CallerPolicy/dev-env-system/meatball.spec.serviceAccount`
  in `frontdoor/app/callerpolicy.yaml`. Review the operator’s
  `policyCallerAllowed` route list and the allowance handler’s
  `AggregateAllowanceReports.allow` reader predicate. Use the operator’s
  existing store access with a narrow projection of provider, source, times,
  windows and freshness. OP509 owns freshness and OP529 owns the distinct
  least-privilege receipt.
- A later rollout would pin the signed front-door image in
  `Deployment/dev-env-system/dev-env-frontdoor.spec.template.spec.containers[name=frontdoor].image`
  in `frontdoor/app/deployment.yaml`. Serving the exact installed common
  rules would need future review of that Deployment’s
  `spec.template.spec.volumes` and
  `spec.template.spec.containers[name=frontdoor].volumeMounts` or a scoped
  operator route. This slice publishes the reviewed packaged text.
- The same Deployment’s `spec.template.spec.serviceAccountName` selects
  `ServiceAccount/dev-env-system/dev-env-meatball`. The bearer and tracker
  Secret names come from `spec.target.name` on
  `ExternalSecret/dev-env-system/dev-env-frontdoor-token` and
  `ExternalSecret/dev-env-system/dev-env-frontdoor-openproject`.
  The `IngressRoute/dev/dev-env-frontdoor`
  binds `/mcp` in `spec.routes[].match`. The
  `CiliumNetworkPolicy/dev-env-system/dev-env-frontdoor` and
  `CiliumNetworkPolicy/frontend/openproject-frontdoor-api` constrain
  traffic in `spec.ingress` and, for the first policy, `spec.egress`.
  Give an independently authorized recipient the work-item URI and a receipt
  request. Identity and token distribution need separate authorization.
- The classifier reads offline records. Future changes to task cleanup or
  archive flags in `HelmRelease/dev-env-operator.spec.values.controllers.dev-env-operator.containers.app.args`
  in `operator/app/helmrelease.yaml`, or to rescue, log or PVC retention,
  need their own reviewed design. See [DESIGN-061](061-task-session-cleanup.md)
  and [ADR-020](../adrs/020-task-session-cleanup.md).

## Evidence

Source is a ready PR on the worker branch. Tested means exact-head CI,
bounded local suites and a loopback smoke read through a fresh client with
only the throwaway bearer. The worker's allowance read uses only its existing
authorized route. If that route refuses it, the receipt records the refusal.
Installation is a later GitOps pin. Acceptance is Dot's two independently
scoped live reads and Tom's disposition on OP529.

## Follow-ups for Tom

- Review exact front-door access for the usage GET.
- Decide whether handoff v2 should return bounded, scrubbed and explicitly
  unverified checkpoint text for `present`.
- Decide whether to verify checkpoint authorship through a per-session
  tracker identity or a signed marker.
- Decide whether to serve resource reads during an operator credential outage.
- Review the proposed repository `CLAUDE.md` maintenance line above.
- Review adding the retired-session rule to the haynes-ops common rules
  in `session-config/app/config/claude/CLAUDE.md`.
