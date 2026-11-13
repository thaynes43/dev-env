# DESIGN-065: Session display titles and safe renaming

- **Status:** Slice 1 (the field, creation and the launch argument) is built,
  not installed (2026-11-12). The phone test (section 5) has not run, and
  slice 1 does not merge before step 2's reading of "|" and "é" (ruling 7).
  The coordinator amended ruling 1, safety rule 6 and the slice table on
  2026-11-13; that amendment is not Tom's. Slices 2 and 3 are not built.
  Rulings in section 4 stand.
- **Tracking:** [Story OP774](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/774)
  under [Epic #91](https://work.haynesnetwork.com/work_packages/91). Builds
  on [DESIGN-038](038-remote-control-launch.md),
  [DESIGN-060](060-meatball-front-door.md) and D-39 in
  [DESIGN-001](001-dev-env-v2.md).
- **Origin:** Tom, relayed by Meatball on 2026-11-12 (item 774 comment 7348),
  quoted in section 1. Nothing else here is Tom's.
- **ADR:** needed before slice 3, not written here. In slice 1 the title is
  set at create and cannot change, like the rest of spec, so D-39 stands
  (decision 23: spec is immutable except `operatingMode` and `lifecycle`).
  Slice 3 lets it change after create, and ruling 4 decides who may change
  it. That amends D-39 and adds an access rule, so ADR-026 (the next free
  number) is accepted before slice 3 merges, and only if the phone test
  shows slice 3 is needed.
- **Evidence:** dev-env `59169f1f` (origin/main), source reads only. Nothing
  in the cluster or the tracker was written for this document.

## 1. Problem

Tom's words, relayed by Meatball on 2026-11-12 and recorded on item 774
comment 7348: "We should get a better naming convention and rename
capability on the dev-env v2 coordinators radar but not disrupt work in
flight we should finish things before starting new things"

He cannot tell on his phone which Fable conversation is the main
coordinator. The Claude app lists each Remote Control session by the name
agentd gave it at launch, which is the generated infrastructure name, such
as `dev-env-fable-coordinator-1109` (item 774 description). The goal is a
short title beside that stable name, which never moves the name, UID,
conversation or URL.

## 2. Facts checked

Source, dev-env `59169f1f`:

- `remoteControlArgs` in `internal/agentd/launch.go` returns
  `--remote-control <sess.Name>`. `BuildLaunch` and `BuildResume` both call
  it, so a resume passes the name again beside `--resume <conversation>`.
  Both set `CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=dev-env`; its effect
  when a name is also passed is not established.
- The private-task continuation passes its own title:
  `buildExplicitClaudeRCLaunch` in `internal/agentd/explicit_claude_rc.go`,
  limited by `explicitTitlePattern` to 80 ASCII characters (letters, digits,
  space, `.`, `_`, `-`; no `|`). This design leaves that rule alone.
- `collectRemoteControl` in `internal/agentd/remote.go` builds
  `status.remoteControl` from the CLI's own record for the live process
  (`readRemoteRecord`: `bridgeSessionId` and `procStart` in
  `sessions/<pid>.json`). The URL is `https://claude.ai/code/` plus that id.
  The title plays no part in the id, the URL or the state.
- `sessionDocument` in `internal/controller/pod.go` gives agentd its session
  once per pod, as an env value or a projected pod annotation. `parseSession`
  in `internal/agentd/protocol/session.go` uses plain `json.Unmarshal`, so an
  older agentd ignores a new key.
- The CEL rules on `AgentSessionSpec` (`api/v1alpha1/agentsession_types.go`)
  allow only `operatingMode` and `lifecycle` to change after create.
  `CreateSessionRequest` (`internal/apiserver/apiv1/types.go`) has no title,
  and `createSessionMode` (`internal/apiserver/sessions.go`) refuses unknown
  fields.
- The drain replaces a pod only for a new template revision, never for a
  spec change or a remote session (`drainEligible` in
  `internal/controller/drain.go`, `sessionTemplateRevision` in `pod.go`).
  `agent-run` has no rename command (item 774 description).

Provider, from the Claude Code docs on code.claude.com as read by a research
subagent on 2026-11-12 (nobody tested it on a live session): the name given
to `--remote-control` or `--name` is the title in the claude.ai/code and
Claude app session list. Precedence is the launch name, then `/rename`, then
the last meaningful message, then a generated name. A running Remote Control
session can be renamed from claude.ai or the Claude app, and Claude Code
applies the same name locally. `/rename` also works, typed or sent from the
phone or web. No documented way exists for another process to rename a
running session. Length, Unicode and `|` are not documented. A resume with a
different name is not documented.

Codex, from source: the daemon runs `codex remote-control start --json` with
no name (`codexRemote.start` in `internal/agentd/codex_remote.go`,
`startCodexHost` in `internal/agentd/codex_host.go`), and
`collectCodexRemote` reports the session's name. What the ChatGPT app calls
the host is unknown; the pod hostname is the session name (`dnsLabel` in
`protocol/session.go`), not verified as what it shows. A thread gets the
caller's `--name`, at most 64 ASCII characters without spaces
(`codexThreadName` in `internal/agentd/thread_launch_codex.go`); whether the
app shows it is unknown. No Codex rename is designed here.

## 3. Who reads `status.remoteControl.name`

`RemoteControlStatus.Name` is documented as equal to the session's name.

| Where | What it does | If the provider title differed |
| --- | --- | --- |
| agentd `collectRemoteControl`, `collectCodexRemote`, `CollectStatus` | Writes `sess.Name` | Nothing |
| `remoteControlStatus` (`internal/apiserver/heartbeat.go`) | Copies it, clipped to 128 bytes | Nothing |
| `view` (`internal/apiserver/view.go`) | Puts it in the API session view | `-o json` shows the session name, which a reader could take for the phone title |
| `remoteControlLine` (`internal/agentrun/sessions.go`, `show`) | Prints `registered as <name>` | Misleads: says the phone entry carries the session name |
| `portalRemoteControl` (`internal/apiserver/portal_reader.go`) | Drops it; only state and link reach the portal | Nothing |
| `internal/agentrun/rc.go`, `internal/apiserver/codexhost.go`, `internal/standby/metrics.go` | Read state and URL only | Nothing |
| `internal/frontdoor` | No reader | Nothing |

No logic keys on the name. Ruling 1 keeps it the session name, and slice 3
rewrites the one misleading line.

## 4. Rulings (the coordinator, 2026-11-12)

**Ruling 1. The field.** `spec.title`, an optional string holding the role
text, for example `Main dev-env coordinator`: 1 to 48 characters and at most
96 bytes of UTF-8 (`MaxLength` plus a CEL `size(bytes(self))` rule, as on
`OutcomeStatus.Note`). Allowed: letters, marks, digits, punctuation, symbols
and the ASCII space. Refused: `|`, control and format characters, anything
above U+FFFF, a leading or trailing space, two spaces in a row. The first
character must be a letter or a digit, because the CLI's `--remote-control`
takes an optional value, so a value that begins with a hyphen would be read
as an option (coordinator, 2026-11-13, not Tom). agent-run
trims the ends; the API refuses an unclean value and never rewrites one.
Empty means no title; `""` is treated as absent and never stored.

Not an annotation: the schema cannot bound one, any client with patch can
write one, and the API view does not carry it. Not the name: it is the pod
name, the hostname, the volume `home-<name>` and every tool's key. In slice 1 the
title is set at create and cannot change. From slice 3 it is the third spec
field that may change after create, only through the ruling 4 route. It
never changes `metadata.name`, the UID,
`status.agent.conversationId`, `status.remoteControl.sessionId` or `url`,
the pod or the volume. `status.remoteControl.name` stays the session name. A
new `status.remoteControl.title` records what agentd passed at this pod's
launch, empty when it passed the name. The report stays empty until that
CLI process is live (coordinator, 2026-11-13, not Tom). The API view's
`title`, the `show` row and this report stay in slice 1.

**Ruling 2. The convention.** The display title is `<Family> | <title>`, as
in the labels proposed on item 774 (`Fable | Main dev-env coordinator`).
The platform composes it; the caller gives only the role text. The family
comes from `spec.model`, fixed at create: `claude-fable-*` Fable,
`claude-opus-*` Opus, `claude-sonnet-*` Sonnet, `claude-haiku-*` Haiku,
`gpt-*` GPT, any other model no prefix. One table in one new package, used
by the operator (API view, portal reader) and agentd (launch argument). So
the prefix cannot be wrong, claim another model or go stale, and `|` is one
constant. The longest result is 57 characters and 105 bytes, inside the
128-byte clip. With no title the display title is the session name, as
today, so no existing session changes on the phone.

**Ruling 3. Setting it at creation.** `--title "<text>"` on `agent-run`
create and on `agent-run rc`; `title` in `POST /v1/sessions`; an optional
`title` on the front door's `coordinator_start`, carried in
`agentrun.CoordinatorStartInput`. Only the creator sets it, in its own
create. The summon route takes none. agentd passes the composed title to
`--remote-control` at a remote session's first launch and every resume,
where it passes the name today. A local session and a task, including a
coordinator start (`PrepareCoordinatorStart` in
`internal/agentrun/coordinator.go` creates mode `task`), have no Remote
Control entry, so their title shows in listings only. A Codex host keeps it
for listings and agentd does not pass it to Codex. A bad value is HTTP 422,
code `invalid`, field `title`, reason `title_too_long`, `title_characters`
or `title_spacing`. A new agent-run against an older operator gets the
unknown-field refusal, says so, and does not retry without the title.

**Ruling 4. Changing it later.** `agent-run title <session> "<text>"` and
`agent-run title <session> --clear` call `PUT /v1/sessions/{name}/title`
with `{"title": "...", "expect": "<title last read>"}`. The API writes
`spec.title` only, with the `resourceVersion` precondition and retry of
`setOperatingMode` (`internal/apiserver/lifecycle.go`), and answers 409
`title_changed` when the stored title is not `expect`, so two renames cannot
overwrite each other unseen. Allowed: the session's parent, Tom's human
caller, a trusted client, and a workbench session for any session but
itself. Refused: the session itself, always, and every session that is
neither its parent nor a workbench session. So no session can call itself
the main coordinator. The command prints the composed text for the owner to type in
the Claude app.

The platform never types into a running session or sends `/rename`; a new
title reaches the provider at the next launch or resume. A live change is
the owner's own rename in the Claude app or on claude.ai. The record and the
provider's title differ after `agent-run title` until the next resume, and
after an app rename until someone records the same text, since the platform
cannot see app renames. Platform listings show the record; `show` adds
`status.remoteControl.title` and says the app may show a newer name. The app
shows the provider's title. If the launch name wins at resume (section 5),
recording the app's text before the next resume keeps them equal.

**Ruling 5. Where it is shown.** `agent-run list` and `fleet`: a TITLE
column after NAME. `show`: `title` and, for a remote session, `title at
launch`. The API view and `-o json`: `title` and `displayTitle`. The portal
summary: an optional `displayTitle` shown with the name. The front door's
`session_status`: beside the name. The stable name is always shown beside
the title, never replaced; an untitled session shows its name once.
`decodeSessionRow` in `web/portal/session/product.go` refuses any key
outside its six, so haynes-ops pins the portal that accepts `displayTitle`
before the operator that sends it.

**Ruling 6. Safety rules.**

1. No terminal input: no tmux keys, no `/rename`, no prompt, no answer to a
   question, nothing that can clear unsent input.
2. No restart: a title change does not touch the pod, its annotations or its
   session document, and the controller never compares titles.
3. No new conversation: a resume keeps `--resume <conversationId>`; only the
   `--remote-control` value changes.
4. No change of owner, parent, profile, grant or access.
5. Absent means today: an untitled session launches, resumes and lists as
   now. Older agent-run, front door and agentd ignore the new fields; an
   older portal is handled by pin order.
6. CRD first: haynes-ops gets the `config/crd/` copy before the operator pin
   that writes a new field, or the API server prunes it (haynes-ops #3502).
   Slice 1 adds `spec.title` and `status.remoteControl.title` together, so
   one copy serves both. Slice 3 changes the rule that lets the title
   change, which is a second copy. An operator older than this slice must
   not manage titled sessions, because its full-object updates drop the
   field and the immutability rule then refuses them; roll the operator
   back only when no session has a title (coordinator, 2026-11-13, not Tom).
7. Display only: no routing, authority, idempotency or selection decides by
   the title.

**Ruling 7. The proof gates the change path.** Slices 1 and 2 may start
before the phone test: they use only the launch name, the top of the
documented precedence. Slice 1 does not merge before step 2's reading of `|`
and `é`, because it ships the separator and the character rule, and a change
to either after it merged would be a second CRD copy. Slice 3 waits for its readings; slice 4 for slices 1
to 3 installed and the test passed. If the readings show that a resume keeps
the owner's rename, slice 3 is scoped again before it starts, because the
record is then not needed to keep a title on the phone.

## 5. The proof: one phone test

The coordinator later creates one fresh Remote Control session for the test,
T, on the current image. Nobody types into T or uses it for work. Tom spends
about a minute.

Readings before the rename (R0), after it (R1) and after the resume (R2):

- `kubectl -n dev-agents get agentsession T -o json`: `metadata.name`,
  `metadata.uid`, `status.agent.conversationId`, `status.agent.podUID`,
  `status.remoteControl.name` and `state`, and the first 12 hex of the
  SHA-256 of `status.remoteControl.sessionId` and of `url` (never the id or
  link itself).
- `kubectl -n dev-agents get pod T -o json`: `metadata.uid` and the agent
  container's `restartCount`. `agent-run show T`: state and remote line.
- Local title: a read-only `grep -rlF` for the exact text in the CLI's config
  directory on T's pod, file names only. No tmux, no attach. Else Unknown.
- Provider title: what the Claude app list and claude.ai/code show.

Steps:

1. R0. The app shows T's session name.
2. Tom renames T in the Claude app to `Test | Title probe é` (a bar and one
   letter outside ASCII). After a minute, R1.
3. `agent-run suspend T`, wait for Suspended; `agent-run resume T`, wait for
   Running and `registered`. R2. This resume passes T's name, as today.
4. Record R0 to R2 on item 774, then reap T under the normal rules.

Expected: R1 equals R0 in every identity reading, pod UID and restart count
included, and shows the new text. R2 keeps the name, UID, conversation id and
both hashes, with a new pod UID.

What changes the plan:

- R1 differs in an identity reading, the pod restarted, or the agent left
  `idle`: the app rename is not safe on a working session. Stop, record it on
  774, skip slice 4, and ask whether titles become listing-only.
- R2 shows T's name: the launch name wins at resume. Slice 3 stands, and
  slice 4 records each title before any resume.
- R2 keeps Tom's text: a resume does not override a rename. Slice 3 needs
  its own proof that a resume with a different name changes the title; until
  then the record reaches the phone only at a first launch.
- R2 changes either hash: a resume no longer brings back the same phone
  entry. File that bug; slice 3 waits.
- The app refuses or changes `|` or `é`: Q-065-02 or Q-065-01 is answered
  no, and the separator or character set changes before slice 1 merges
  (ruling 7).

## 6. Slices

Each is one worker session. None types into, restarts or resumes a working
session. Tests use fakes at two CPUs. The new package gets its line in
`docs/repo-layout.md`.

| Slice | What it changes | Acceptance | CRD copy | Operator pin | Agent image pin | Front door pin |
| --- | --- | --- | --- | --- | --- | --- |
| 1. Field and creation | `spec.title`, immutable after create, and `status.remoteControl.title` with CEL bounds; the composer; `title` on create, `--title` on create and `rc`, `title` on `coordinator_start`; agentd passes the composed title at launch and resume; `title` in the API view; `title` in `show`; agentd reports `status.remoteControl.title` once the CLI was launched with it | `agentsession_validation_test.go` accepts and refuses each bound: empty, 48 and 49 characters, 96 and 97 bytes, `|`, a control character, U+10000, spacing. Launch and resume argv with and without a title; an untitled argv is byte-identical to today's; an old agentd ignores the key | Yes, first | Yes | Yes | Yes |
| 2. Listings | TITLE in `list` and `fleet`; `displayTitle` in the API view; `displayTitle` in the portal; `session_status` | Golden tests with no, long and Unicode titles; the portal decoder takes the key and still refuses others; no script in dev-env or haynes-ops that parses these tables breaks | No | Yes, after a portal pin | Yes (agent-run) | Yes |
| 3. Change it, keep it on resume | ADR-026; the CEL rule lets `spec.title` change; `PUT /v1/sessions/{name}/title` with `expect`; `agent-run title`; `show` drops `registered as <name>` | Fakes: parent, human, trusted client and workbench allowed; the session itself and another session refused; stale `expect` is 409; a change leaves the pod, its UID and annotations alone; a resume after a change passes the new title and the same `--resume` id. Live on a fresh test session: section 5's readings after a change and one resume. An adversarial review by another model family before merge, because it adds a write route with an access rule | Yes, first | Yes | Yes | No |
| 4. Backfill | For each session below: `agent-run title`, then the owner's app rename to the printed text, with R0 and R1 | Title in `agent-run list` and the same text in the app; no identity reading changed; nothing typed, resumed or restarted | No | No | No | No |

The five, with the labels proposed on item 774 (not applied):
`dev-env-fable-coordinator-1109` (Main dev-env coordinator),
`dev-env-1111-143337` (Ops migration coordinator), `haynes-ops-1109-230612`
(Upgrade coordinator), `haynes-ops-1111-141157` (Nightly upgrades) and
`haynes-ops-1111-144013` (Music recovery). Slice 4 first checks each still
runs in that role; the family comes from its `spec.model`, not the proposed
label. A session on an older image shows the record in listings at once, and
on the phone only by the app rename until its next resume.

## 7. Open questions

- **Q-065-01. The length limit.** The provider documents none; the explicit
  continuation allows 80 ASCII characters. Recommendation: 48 characters and
  96 bytes of role text (57 composed). The phone list is narrow and the
  longest proposed label is 33 characters. Raise it only after a phone
  reading shows a longer title displayed whole.
- **Q-065-02. Is `|` safe in a provider title?** Not documented; agentd
  passes argv, so no shell parses it. Recommendation: keep it, test it in
  section 5 step 2, and change the one constant if the app refuses it.
- **Q-065-03. Duplicates.** Recommendation: allowed. The name stays unique, a
  fleet-wide check races, and it would block a successor taking over a role.
  agent-run warns at create and at `title` when an unfinished session
  already shows the same display title.
- **Q-065-04. A default for a coordinator start.** Recommendation:
  `Coordinator for item <n>` when Meatball gives no title.
- **Q-065-05. Reading the provider's title back.** Recommendation: no; an
  undocumented local file is not a contract.
- **Q-065-06. Codex.** Recommendation: read what the ChatGPT app shows at the
  next Codex pairing, and design nothing until then.

## 8. Not built here

Any code, CRD, API, generated file or ADR. A live rename by the platform:
the provider documents no way for another process to do it, and typing
`/rename` is ruled out. A Codex rename, or a change to the explicit
continuation's title rule. Renaming any working coordinator before slice 4.
A title for summoned sessions. Any change to a session's name.
