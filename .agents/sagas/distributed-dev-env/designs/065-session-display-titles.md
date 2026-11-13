# DESIGN-065: Session display titles and safe renaming

- **Status:** Slice 1 (the field, creation and the launch argument) is built,
  not installed (2026-11-12). The owner's phone test of section 5 is
  withdrawn: asked to do it on 2026-11-13, Tom answered that he could always
  rename a session himself and that his ask is for agents to do the naming
  (ruling 7, as amended). Slice 1 merges on its reviews and checks.
  The coordinator amended ruling 1, safety rule 6 and the slice table on
  2026-11-13; that amendment is not Tom's. Slice 2 is not built. Slice 3 (an
  agent renames a running session) is built, not installed (2026-11-13, OP787).
  Rulings in section 4 stand.
  Slice 1 is installed except its front door pin (item 785 comment 7560,
  2026-11-13). Section 9 designs slice 3 (amendment of 2026-11-13, OP787):
  Tom chose a typed rename under a guard (Q-065-07), and ruling 8 and the
  rules of section 9 are the coordinator's design of it. That amendment
  also changes ruling 4, safety rule 1, Q-065-05, Q-065-07 and the slice 3
  and 4 rows. Slice 3's code is built and not installed. Slice 4 is not built.
- **Tracking:** [Story OP774](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/774)
  under [Epic #91](https://work.haynesnetwork.com/work_packages/91). Builds
  on [DESIGN-038](038-remote-control-launch.md),
  [DESIGN-060](060-meatball-front-door.md) and D-39 in
  [DESIGN-001](001-dev-env-v2.md).
- **Origin:** Tom, relayed by Meatball on 2026-11-12 (item 774 comment 7348),
  quoted in section 1. Nothing else here is Tom's.
- **ADR:** [ADR-026](../adrs/026-agents-rename-a-running-session.md),
  Accepted 2026-11-13, for slice 3. In slice 1 the title is
  set at create and cannot change, like the rest of spec, so D-39 stands
  (decision 23: spec is immutable except `operatingMode` and `lifecycle`).
  Slice 3 lets it change after create, ruling 4 decides who may change
  it, and ruling 8 lets the platform type the rename into a running
  session under a guard. That amends D-39 and adds an access rule and one
  kind of typed input, so ADR-026 is accepted before slice 3 merges. Slice 3
  is required since ruling 7 was amended.
- **Evidence:** dev-env `59169f1f` (origin/main), source reads only. Section
  9 reads dev-env `5b52d334` and the tracker comments it names, plus one
  read-only `kubectl get agentsessions` of 2026-11-13. Nothing in the
  cluster or the tracker was written for this document.

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

**Ruling 4. Changing it later (amended 2026-11-13 by ruling 8, the
coordinator).** `agent-run title <session> "<text>"` and
`agent-run title <session> --clear` call `PUT /v1/sessions/{name}/title`
with `{"title": "...", "expect": "<title last read>"}`. The API writes
`spec.title` only, with the `resourceVersion` precondition and retry of
`setOperatingMode` (`internal/apiserver/lifecycle.go`), and answers 409
`title_changed` when the stored title is not `expect`, so two renames cannot
overwrite each other unseen. Allowed: the session's parent, Tom's human
caller, a trusted client, and a workbench session for any session but
itself. Refused: the session itself, always, and every session that is
neither its parent nor a workbench session. So no session can call itself
the main coordinator. Rule 8.2 sets this out by caller kind and adds a rate
limit. The command prints the composed text and says that the session
applies it when it is idle with an empty input line (rule 8.8).

As first written, the platform never typed into a running session or sent
`/rename`, a new title reached the provider only at the next launch or
resume, and the command printed the text for the owner to type in the
Claude app. Tom's choice of 2026-11-13 (Q-065-07) replaces that: agentd in
the session's own pod types the provider's rename command, only under the
guard of ruling 8. A rename in the Claude app is still the owner's own, and
the platform never types to undo one (rule 8.4).

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

1. No terminal input, with one exception from slice 3: agentd's typed
   rename of ruling 8, under its guard (amended 2026-11-13, the
   coordinator). Otherwise no tmux keys, no `/rename`, no prompt, no answer
   to a question, nothing that can clear unsent input.
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
   change, which is a second copy; that copy also carries slice 3's status
   fields (rule 8.8). An operator older than this slice must
   not manage titled sessions, because its full-object updates drop the
   field and the immutability rule then refuses them; roll the operator
   back only when no session has a title (coordinator, 2026-11-13, not Tom).
   From slice 3 the rule no longer refuses them, so the title is lost
   without an error (rule 8.1); the rollback rule stands.
7. Display only: no routing, authority, idempotency or selection decides by
   the title.

**Ruling 7. No slice waits for a reading by Tom (amended 2026-11-13, the
coordinator).** As first written, slice 1 did not merge before Tom's reading
of `|` and `é` on a test session. On 2026-11-13 he was asked by phone prompt
to rename the test session and answered, word for word: "I could always do
this my ask is agents can rename or set useful names that follow a pattern"
(item 774 comment 7543). The coordinator reads that as follows; the reading
is not Tom's. He renames sessions in the app already, so that path needs no
proof from him. What he asks for is that agents set names that follow a
pattern and that agents rename. So:

- Slice 1 merges on its reviews and checks. The first titled session Tom
  sees is the reading of the bar and of a non-ASCII letter. The separator is
  one constant in the composer, and the schema only refuses a bar inside the
  role text, so a different separator is a code change, not a CRD change.
- Slice 3 (an agent changes a title) is required, not conditional. How the
  new title reaches a running session was Q-065-07; Tom answered it on
  2026-11-13, and ruling 8 is the route.
- Slice 4 waits for slices 1 to 3 installed.
- The readings of section 5 are taken by agents on a test session when
  slice 3 is built. They no longer need Tom.

**Ruling 8. An agent renames a running session (the coordinator,
2026-11-13).** Tom's choice of 2026-11-13 (Q-065-07, item 774 comment 7547)
decides the route: the platform types the provider's rename command into the
running session, only when that session is idle with an empty input line,
and otherwise holds the rename and tries later. How is the coordinator's
design, in section 9 (rules 8.1 to 8.12). `spec.title` changes through
ruling 4's route only. agentd in the session's own pod learns the recorded
title from its heartbeat answer. It types `/rename <composed title>` and
Enter into its own pane under the guard of rule 8.5, and checks again before
the Enter. It confirms the result from the CLI's own record and reports it
in status. Nothing else is ever typed for a title.
[ADR-026](../adrs/026-agents-rename-a-running-session.md) records the
decision.

## 5. The proof: one phone test

Withdrawn as a gate on 2026-11-13 (ruling 7). The readings and the outcomes
below stay as the method for slice 3's own proof: agents take them on an
unused test session, and the rename is made by the route slice 3 builds, not
by Tom. Agents took R0 to R2 on 2026-11-13 on the test session
`dev-env-1113-004614`, with a rename typed by the coordinator instead of
Tom's (item 774 comment 7546); section 9.1 uses them. The text below is as
first written.

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

Each is one worker session. No worker types into, restarts or resumes a
working session. From slice 3 a session's own agentd types that session's
rename, under ruling 8. Tests use fakes at two CPUs. The new package gets
its line in `docs/repo-layout.md`.

| Slice | What it changes | Acceptance | CRD copy | Operator pin | Agent image pin | Front door pin |
| --- | --- | --- | --- | --- | --- | --- |
| 1. Field and creation | `spec.title`, immutable after create, and `status.remoteControl.title` with CEL bounds; the composer; `title` on create, `--title` on create and `rc`, `title` on `coordinator_start`; agentd passes the composed title at launch and resume; `title` in the API view; `title` in `show`; agentd reports `status.remoteControl.title` once the CLI was launched with it | `agentsession_validation_test.go` accepts and refuses each bound: empty, 48 and 49 characters, 96 and 97 bytes, `|`, a control character, U+10000, spacing. Launch and resume argv with and without a title; an untitled argv is byte-identical to today's; an old agentd ignores the key | Yes, first | Yes | Yes | Yes |
| 2. Listings | TITLE in `list` and `fleet`; `displayTitle` in the API view; `displayTitle` in the portal; `session_status` | Golden tests with no, long and Unicode titles; the portal decoder takes the key and still refuses others; no script in dev-env or haynes-ops that parses these tables breaks | No | Yes, after a portal pin | Yes (agent-run) | Yes |
| 3. Change it and apply it to a running session (amended 2026-11-13) | Required since ruling 7 was amended; designed in section 9 (ruling 8). ADR-026 accepted first; the two CEL rules of rule 8.1; `PUT /v1/sessions/{name}/title` with `expect`, the access rule and rate limit of rule 8.2; the title in the heartbeat answer (rule 8.3); agentd's guarded typed rename, its check before Enter, its confirmation and `title.json` (rules 8.4 to 8.8); `status.remoteControl.providerTitle` and `rename`; `agent-run title`; `show` drops `registered as <name>` | Every test of rule 8.11 with fakes. Live proof on the unused test session `dev-env-1113-004614` only, by rule 8.11. An adversarial review by another model family before merge, required because it types into live sessions and adds a write route with an access rule; ADR-024's findings are its starting list | Yes, first | Yes | Yes | No |
| 4. Backfill (amended 2026-11-13) | For each session below: one recorded title by `agent-run title`, from a caller rule 8.2 allows, applied by that session's own agentd under its guard (rule 8.10), with R0 and R1 | Title in `agent-run list`; `rename` state Applied and `providerTitle` equal to the composed title; no identity reading changed; nothing resumed or restarted for it. A session whose agentd predates slice 3 shows the record and waits for its next resume | No | No | No | No |

The five, with the labels proposed on item 774 (not applied):
`dev-env-fable-coordinator-1109` (Main dev-env coordinator),
`dev-env-1111-143337` (Ops migration coordinator), `haynes-ops-1109-230612`
(Upgrade coordinator), `haynes-ops-1111-141157` (Nightly upgrades) and
`haynes-ops-1111-144013` (Music recovery). Slice 4 first checks each still
runs in that role; the family comes from its `spec.model`, not the proposed
label. A session whose agentd predates slice 3 shows the record in listings
at once, and gets the provider rename only after its next resume onto an
image whose agentd applies it (rule 8.10).

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
  undocumented local file is not a contract. Revisited 2026-11-13 (the
  coordinator's reading, not Tom's): agentd already depends on that file,
  `sessions/<pid>.json`, for the Remote Control id (`readRemoteRecord`) and
  the agent state (`claudeStatus`). After a rename it holds the new `name`
  with `nameSource` "user"; for a session only named at launch it holds a
  derived name with `nameSource` "derived" (item 774 comment 7546, item 785
  comment 7560). New recommendation: report the record's `name` as
  `status.remoteControl.providerTitle` only when `nameSource` is "user";
  never report a derived name; never write spec from it; and report
  Unknown, typing nothing, when the file's shape changes (rule 8.8).
- **Q-065-07. How an agent's rename reaches a running session.** Answered
  by Tom on 2026-11-13 (item 774 comment 7547). Asked "Agents renaming a
  session that is already running: I tested it on a spare session. The
  only way that works is the platform typing the rename command into that
  session. It worked, kept the same link and conversation, and the name
  survived a restart. The risk: if a session has a question open or
  half-typed text, typed input can land there. Should I build agent renames
  that way, typed only when the session is idle with an empty input line and
  held back otherwise?", he picked "Yes, with that guard (Recommended)",
  whose text was: "Agents can rename any running session, including the five
  coordinators you have now. The platform types the rename only when the
  session is idle and its input line is empty; otherwise it waits and tries
  later." So route (b) below, under ruling 8's guard. The text below is as
  first written. Tom's
  answer of 2026-11-13 makes agent renames a requirement. Two routes exist.
  (a) The platform records the title and passes it at the session's next
  launch or resume; nothing is typed, and an agent can suspend and resume an
  idle session to apply it. Whether the launch name replaces an earlier title
  at resume is not documented and is read on a test session. (b) The platform
  types the provider's rename command into the session. It is immediate, and
  it is terminal input: on 2026-11-12 a routine `agent-run msg` landed in an
  open phone question on another coordinator and came back as its answer
  (item 501 comment 7450). Recommendation: build (a); try (b) on an unused
  test session first, and use it on a working session only if Tom chooses it
  knowing that risk.
- **Q-065-06. Codex.** Recommendation: read what the ChatGPT app shows at the
  next Codex pairing, and design nothing until then.
- **Q-065-08. May a session title itself?** Ruling 4 says never. Tom's ask
  is that agents name sessions, which a session titling itself would also
  meet. Recommendation: keep never. The role is given by whoever started
  the session, and the claim shows on Tom's phone. A workbench session can
  title any other session, so the main coordinator's own title is recorded
  by another workbench session (rule 8.10).
- **Q-065-09. Meatball and the coordinators it starts.** Meatball's caller
  kind (`kindPolicy`) has its own list of routes, and slice 3 does not add
  the title route to it. Recommendation: let Meatball title its own
  children only (a session whose `spec.parent` is its account), in the
  front door story that adds the route to that list. Not in slice 3.
- **Q-065-10. A title that ends in `;`.** Validate allows it. tmux reads an
  argument that ends in `;` as the end of a command (tmux's own argument
  parsing; not tested here), so `send-keys -l` could type the text without
  it. Recommendation: the build types such a title on the test session. If
  tmux drops the `;`, escape it as tmux documents (`\;`) and prove that
  live. If neither is proven, refuse a trailing `;` in `sessiontitle.Validate`
  and the CEL rule before slice 3 merges.
- **Q-065-11. An owner's rename in the Claude app.** If it reaches the CLI's
  record (the docs say Claude Code applies it locally; not tested), it shows
  as `providerTitle`. Recommendation: the platform never adopts it as
  `spec.title` and never types to undo it. Listings show the record, `show`
  shows both, and an agent records the owner's text if it should stay.
- **Q-065-12. Screen fixtures per CLI version.** Answered by the coordinator
  on 2026-11-13 (rule 8.5, condition 8). The empty-input test reads the
  screen, which a CLI release can change. agentd holds fixtures for the CLI
  versions the build recorded and accepts any patch release of the same
  major and minor version; on another minor or major it holds every rename
  with reason `cli-version`. The agent image PR that moves the CLI to a new
  minor version records new fixtures on a test session, read-only, or
  renames hold until a later PR does. An unrecognised screen holds in every
  case (condition 9).
- **Q-065-13. Text agentd typed but did not send.** After a failed second
  check its text stays in the input line (rule 8.6). Recommendation: agentd
  never removes it, since a person may have typed beside it. It reports
  `left-in-input`, and the session's owner sends or deletes the text. If
  someone sends it, the record shows the title and the state becomes
  Applied.
- **Q-065-14. The numbers.** Recommendation: 30 seconds of idle before
  typing, 300 ms before the second check, under 1 second from that check to
  Enter, 10 seconds to confirm, 10 minutes between typed renames, 2 attempts
  per title, 60 seconds between recorded changes. Review them after slice 4,
  from the held reasons the five coordinators report.

## 8. Not built here

Any code, CRD, API or generated file. ADR-026 is written with section 9 and
is Accepted. As first written, a live rename by the platform was out: the
provider documents no way for another process to do it, and typing
`/rename` was ruled out. Section 9 designs it, under ruling 8, and builds
nothing. A Codex rename, or a change to the explicit
continuation's title rule. Renaming any working coordinator before slice 4.
A title for summoned sessions. Any change to a session's name.

## 9. Amendment 2026-11-13: agents rename a running session (OP787)

Slice 3, story [OP787](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/787).
Slice 3 is built and not installed (amended 2026-11-13, fix round 1, the
coordinator, not Tom). The words here were "Builds nothing." Section 9.3
records each change from that round. ADR-026 stays Accepted as it is on
main, including its statement that nothing it decides is built yet; that
ADR is not edited.

**Origin.** Tom, by phone prompt in coordinator session
`dev-env-fable-coordinator-1109` on 2026-11-13, recorded on item 774
comment 7543, word for word: "I could always do this my ask is agents can
rename or set useful names that follow a pattern". Then, by phone prompt in
the same session that day, recorded on item 774 comment 7547, the question
in Q-065-07; he picked "Yes, with that guard (Recommended)". Those are the
only words and choices of Tom's in this section. The option's full text is
as the coordinator gave it in this story's work order; comment 7547 gives
it in short.

Rules 8.1 to 8.12 are the coordinator's, dated 2026-11-13, drafted by a
worker for its review. None is Tom's.

### 9.1 Evidence

Readings by the coordinator on the unused test session
`dev-env-1113-004614` (item 774 comment 7546):

- `agent-run msg` cannot carry the command. `messageText` in
  `internal/agentd/deliver.go` wraps every message in a notice, so the
  session's model received the command as a message and answered it.
- Typed into the session's terminal, `/rename Test | Title probe é` renamed
  the session at once; the CLI printed "Session renamed to: ...". Name, UID,
  conversation id, Remote Control session id and link, pod UID and restart
  count were unchanged.
- After a suspend and resume the name was still the renamed one, although
  agentd passed the session name to `--remote-control` again
  (`remoteControlArgs` in `internal/agentd/launch.go`). A title passed at
  resume does not replace a renamed title.
- The CLI's own record for its process, `~/.claude/sessions/<pid>.json`,
  held `name` "Test | Title probe é" and `nameSource` "user" after the
  rename. For a session only named at launch it holds a derived local name
  with `nameSource` "derived" (item 785 comment 7560). So the record shows a
  rename and does not show a launch title. Its `status` read "idle" and
  "busy" in those readings. On 2026-11-13 at about 02:50Z the coordinator read
  the same record, without typing anything, on four working coordinators:
  two read "idle" and two read "shell", a value whose meaning is not
  established. Rule 8.5 holds on every value but "idle".
- A second incident of the same kind: on 2026-11-13 at 03:02Z a long
  `agent-run msg` from another coordinator landed in a phone prompt the main
  coordinator had open for Tom and came back as its answer; the sender had
  read "agent state: busy" one second earlier (item 774 comment 7573). The
  state a peer reads is as old as the last heartbeat. agentd reads its own
  pod, so rule 8.5 does not depend on a heartbeat.
- The bar and the non-ASCII letter were accepted by the CLI.
- The risk: on 2026-11-12 a routine `agent-run msg` landed in an open phone
  question on another coordinator and came back as that question's answer
  (item 501 comment 7450).

From source, dev-env `5b52d334`:

- `Deliver` (`deliver.go`) checks that this boot's launch is a TUI and that
  its pid is alive, then pastes into the tmux target `=agent:`, which is the
  session's active pane. It reads no agent state and no screen.
- `applyActivity` (`internal/agentd/activity.go`) turns the record's
  `status` into busy, idle or waiting. `claudeStatus` reads the record by
  pid and does not check that it belongs to the live process.
  `readRemoteRecord` (`internal/agentd/remote.go`) does: it refuses a link,
  a file over 64 KiB and a `procStart` that is not the pid file's start time.
- `tmuxClientCount` (`activity.go`) returns an error on a timeout;
  `tmuxClients` turns that error into zero clients.
- `StartAgent` (`launch.go`) runs `agentd run-agent` as the pane's process
  with `tmux new-session`. `runTUI` (`internal/agentd/runagent.go`) starts
  the CLI as its child, in its own process group, made the terminal's
  foreground group.
- The heartbeat route (`heartbeat` in `internal/apiserver/heartbeat.go`)
  reads the session, checks that the caller is that session's own pod, and
  answers 204 with no body. `HeartbeatClient.Send`
  (`internal/agentd/heartbeat.go`) discards any 2xx body. agentd sends one
  every 60 seconds (`heartbeatInterval` in `cmd/agentd/main.go`).
- agentd gets its session once per pod (`sessionDocument` in
  `internal/controller/pod.go`), so a title recorded after the pod starts
  does not reach it today.
- Two CEL rules keep `spec.title` fixed (`api/v1alpha1/agentsession_types.go`):
  the spec rule that no field may be added or removed, which includes
  `has(self.title) == has(oldSelf.title)`, and `self == oldSelf` on the
  `Title` field.
- [ADR-024](../adrs/024-parent-held-wait.md) withdrew a typed wake line after
  two reviews of dev-env PR 1072 found HIGH faults: an unknown client state
  passed as busy; keys went to the active pane; tmux pane synchronisation
  copied Enter to a second pane; a record left under a reused pid passed
  after a clock step; the deadline did not reach the process at the
  keyboard. And no ordering of checks makes the read atomic with the
  keystroke. PR 1072's branch, `agent/dev-env-1111-043640`, holds a pane
  resolver the build may start from.

From the tracker: the five coordinators of slice 4 are all `workbench`
sessions in `remote` mode with no title. `dev-env-fable-coordinator-1109` is
the parent of `dev-env-1111-143337` and `haynes-ops-1109-230612`, and
`haynes-ops-1109-230612` is the parent of `haynes-ops-1111-141157` and
`haynes-ops-1111-144013` (read-only `kubectl -n dev-agents get
agentsessions`, 2026-11-13).

Tested after the first draft, on the same unused test session at about
03:28Z on 2026-11-13 (the coordinator): text typed into the input line and
not sent left the record's `status`, `updatedAt` and `statusUpdatedAt`
unchanged, and the input row showed the prompt mark followed by exactly that
text. So the second check of rule 8.6 can require an unchanged `updatedAt`.

Measured by the coordinator on 2026-11-13, fix round 1, and written here by
that round (not Tom): with a question open the CLI record's `status` is
"waiting", and the visible screen is the fixture
`question-open.2.1.321.ansi`. Typed, unsent text leaves `status` and
`updatedAt` unchanged, which the paragraph above already records. "shell"
holds with its own reason `shell` and is never treated as idle. Amended
later the same day, the coordinator, not Tom (story 787 comment 7645,
section 9.4): that last sentence does not stand. "shell" is accepted as
"idle" is, on the same 30 seconds of quiet.

Still not tested: whether a session that keeps a background command running
ever reads "idle" (two of the four working coordinators read "shell", one
of them for hours; a held rename has no expiry); whether a bracketed paste
of the command runs it; whether the owner's rename in the Claude app
reaches the record; whether a resume with a different name replaces a name
that was only set at launch.

Measured by the coordinator on 2026-11-13, fix round 2, and written here by
that round (not Tom). These are the coordinator's readings, quoted, not
measured again:

- The cursor: idle empty input is column 2 on the input row; "/rename zz"
  typed is column 12; "/rename é|z" is column 13; ten Backspace keys return
  it to column 2 with an empty row.
- The screen does not show that a turn is running. In 70 frames of one
  running turn the record status was "busy" every time, the footer never
  showed "esc to interrupt", 54 frames had no spinner row, and 16 had a row
  "<mark> <Word>… (...)" with the marks U+273B, U+2722, U+273D, U+2736,
  U+00B7 and U+002A. The record is the only busy signal.
- "/rename <text>" and Enter sent while a turn runs renames at once and does
  not disturb the turn. Backspace keys during a running turn erase typed
  text and do not disturb the turn.

Measured by the coordinator on 2026-11-13, fix round 3, on its own idle test
session, and written here from `wrap-facts.2.1.321.txt` (not measured again).
The pane was 200 columns by 50 rows. The display-message format was
`#{cursor_x} #{cursor_y} #{pane_width} #{pane_height}`. At N = 196 the cursor
read `198 46 200 50`, and after 196 Backspace keys it read `2 46 200 50`. At
N = 197 the cursor read `3 46 200 50`, and after 197 Backspace keys it read
`2 46 200 50`. The file's reading: text of N characters stays on the one
input row while N <= pane_width - 4, with the cursor at column 2 + N. At
N = pane_width - 3 the input wraps and the cursor column is no longer 2 + N.
N Backspace keys still empty a wrapped input. Not measured at any other pane
width, and not measured whether tmux keeps a narrow size after a client
detaches.

### 9.2 Rules

**8.1 The record.** `spec.title` becomes the third spec field that may
change after create. Exactly two CEL rules in
`api/v1alpha1/agentsession_types.go` change. The spec rule whose message is
"immutable after create: no spec field may be added or removed, except
operatingMode and lifecycle" drops its term
`has(self.title) == has(oldSelf.title)`, and its message names `title` as
well. The `self == oldSelf` rule on the `Title` field is removed. The
field's bounds (`MinLength`, `MaxLength`, the byte, character and spacing
rules) stay and now also run on every update. So a title can be added to an
untitled session, changed, and removed. Reason: the five coordinators have
none and need an add; a clear undoes a wrong title.

Amended 2026-11-13, fix round 1, the coordinator, not Tom (ADR-027, section
9.4): this paragraph does not stand. `spec.title` stays immutable, as slice
1 installed it. The two CEL relaxations are not made. D-39 is not amended.
The changed title is the annotation `dev-env.haynesops.com/title`.

The only code that writes `spec.title` after create is
`PUT /v1/sessions/{name}/title`. The schema cannot tell routes apart, but
agent identities have no write on AgentSessions (`dev-env-agent`'s access in
the common rules), so for agents the route is the only path.

- Body `{"title": "<role text>", "expect": "<role text last read>"}`. Both
  are required strings; `expect` "" means "I read no title". A missing key
  is 422.
- Stored title not `expect`: 409, code `conflict`, reason `title_changed`.
  Same as stored: 200 and nothing written.
- Otherwise the API patches `spec.title` and two annotations,
  `dev-env.haynesops.com/title-set-by` (the caller, as `caller.String()`)
  and `dev-env.haynesops.com/title-set-at` (RFC 3339), with the
  `resourceVersion` precondition and retry of `setOperatingMode`
  (`internal/apiserver/lifecycle.go`). It answers 200 with the view.
  Amended 2026-11-13, fix round 1, the coordinator, not Tom: the patch
  writes `dev-env.haynesops.com/title` and those two annotations, and does
  not write `spec.title`. `expect` is compared with the effective title.
- A bad title is ruling 3's 422. A session being reaped is 409. A summoned
  session (`spec.caller`) is 409: summoned sessions take no title (ruling 3,
  section 8).
- `agent-run title` against an operator without the route says so and does
  nothing else.

**Clearing.** `"title": ""` removes `spec.title`. Amended 2026-11-13, fix
round 1, the coordinator, not Tom: a clear writes the title annotation with
an empty value and leaves `spec.title`. On the provider, agentd
types `/rename <session name>`, so the phone shows the session name, as it
does for an untitled session. Reason: a resume does not replace a typed
title (item 774 comment 7546), so only a typed rename takes back a title that
was typed. Leaving it would show a role nobody records.

**CRD copy.** Amended 2026-11-13, fix round 2, the coordinator, not Tom.
haynes-ops still gets a copy of `config/crd/` before the operator pin that
needs it. The title route writes only annotations, so an old CRD does not
refuse the PUT. An old CRD does still prune
`status.remoteControl.providerTitle` and `status.remoteControl.rename`
(haynes-ops #3502). `spec.title` stays immutable, so rolling the operator
back does not drop a spec title.

**8.2 Who may change it.** Ruling 4's rule stands. By caller kind
(`internal/apiserver/caller.go`):

| Caller | May set or clear the title of |
| --- | --- |
| `kindHuman` (Tom) | any session |
| `kindClient` (a trusted client) | any session |
| `kindSession` whose pod runs as the workbench account (`isWorkbenchSession`) | any session but itself |
| `kindSession`, any other | its own children: sessions whose `spec.parent` is its name |
| `kindCoordinator` (a coordinator host) | its own children, checked by `authorizeChildMutation` |
| `kindPolicy` (Meatball), `kindSummon`, `kindPortalReader`, `kindPortalLaunch` | none (Q-065-09 for Meatball) |
| the target session itself, of any kind | never |

This meets Tom's ask. A coordinator titles its own children by the parent
rule. A workbench coordinator titles its peers by the workbench rule: all
five coordinators are workbench sessions (section 9.1). The main
coordinator cannot title itself; another workbench session does (rule 8.10,
Q-065-08).

An ordinary session may title the sessions it created, at create (ruling 3)
or later. It may not title itself, its parent, a sibling or any other
session. Reason, as in ruling 4: a title is a claim about a session's role,
shown on Tom's phone, and the session that holds the role should not be the
one to make it.

**Rate limit.** The API refuses a change within 60 seconds of the last one
on the same session, read from `title-set-at`: 429, code `limit_exceeded`.
agentd also types at most one rename per session every 10 minutes (rule
8.5, condition 10). Reason: the annotation keeps the limit across API
replicas and restarts, and agentd's pacing bounds the keystrokes, which are
the risk, whatever the API lets through.

**8.3 How agentd learns the recorded title.** From its heartbeat answer.
The heartbeat route answers 200 with `{"title": {"text": "<role text>"}}`
instead of 204; `text` is "" when the session has no title. agentd composes
the target with `remoteControlTitle` (`sessiontitle.Compose` from
`spec.model`, then the letter-or-digit check), and checks the role text
again with `sessiontitle.Validate`. A value that fails either is never typed
(Failed, reason `invalid-title`).

- An answer without the `title` object tells agentd nothing. An operator
  older than slice 3 answers 204. agentd then keeps what it had and types
  nothing; it never reads a missing title as a clear. Reason: otherwise an
  operator rollback would make agentd type `/rename <name>` into every
  titled session.
- An older agentd discards the body (`Send`), so it does nothing.
- The session document's title (slice 1) stays the launch argument only.
  agentd never types because of it. Reason: the session document cannot
  tell "no title" from "an operator that does not know titles".

Reason for the heartbeat: it already reads the session and checks the
caller's pod, it comes every 60 seconds, which is the retry cadence, and it
needs no exec into the pod and no new route.

**8.4 When agentd types.** agentd keeps one file, `~/.agentd/title.json`
(`statePath`, on the session's volume, so it survives a suspend and resume).
It holds the session UID, `applied` (the composed text agentd last knew the
provider to have), `lastTypedAt`, and `attempts` at the current target. A
file naming another UID counts as missing. The target T is the composed
title from the latest heartbeat answer, or the session name when the title
is empty.

agentd types only when T is not `applied`. It never types because the
record's `name` differs from T: the record also shows a rename made by
someone else, and the platform never types to undo the owner's rename in
the app (if that reaches the record; not tested). `applied` is set:

- at a session's first launch (`launch.json` written by this boot) to the
  value passed to `--remote-control`: the composed title, or the session
  name. Nothing is typed after a first launch. Reason: the record never
  shows a launch title (item 785 comment 7560), so a check on its name would
  type into every new titled session for nothing.
- to T whenever the record shows `name` equal to T, before or after typing,
  whatever the current state.
- to the session name when `title.json` is missing and the session has no
  title. So an untitled session is never typed into for a title, and a
  resume onto a new image does not type into every session.

When `title.json` is missing and the session has a title (a session titled
before its agentd could apply titles, as the five will be), `applied` stays
empty and agentd applies T once, under the guard. Reason: whether a
resume's `--remote-control` value replaces a launch-only name is not
tested, and a typed rename gives a result the record confirms.

So, for launch titles: a first launch is never followed by a typed rename.
A resume after a change made while the session was suspended applies the
change by a typed rename, because the resume argument does not replace a
typed title (item 774 comment 7546). A resume with no change types nothing.

**8.5 The guard.** agentd types only when every condition below holds. It
reads them in its own pod, in this order, and the first that fails is the
held reason. A read that fails or times out holds the rename.

1. **Kind.** `sess.Agent` is claude, `sess.Mode` is remote, `sess.Caller`
   is empty, and no explicit continuation runs (`explicitClaudeRCStatus`
   finds none). Otherwise agentd reports nothing and never types: a task, a
   local session and a Codex session keep the title for listings only
   (ruling 3).
2. **This boot's CLI.** `currentLaunch` is a TUI launch of this boot that
   names this session and its UID. The pid file names a live process
   (`pidAlive`: pid and start time). agentd reads the record
   `sessions/<pid>.json` once, the way `readRemoteRecord` does (no link, at
   most 64 KiB, `procStart` equal to the pid file's start), and that one read
   supplies every record field below. Reason `process`.
3. **Registered.** The record's `bridgeSessionId` matches `bridgeSessionID`.
   Reason `not-registered`.
4. **Record shape.** `status`, `updatedAt`, `name` and `nameSource` are
   present, with the types seen. Otherwise the state is Unknown, reason
   `record-shape`, and nothing is typed (rule 8.8).
5. **Idle and quiet.** `status` is exactly "idle", and `updatedAt` is at
   least 30 seconds before now and not after now. Amended 2026-11-13, fix
   round 1, the coordinator, not Tom (story 787 comment 7645, section 9.4):
   `status` "shell" is accepted on the same 30 seconds. It is what a session
   shows between turns while a background shell command is running. "busy"
   holds with `busy`, "waiting" with `waiting`, any other value with `status-unknown`,
   an "idle" whose `updatedAt` is less than 30 seconds old with `quiet`,
   a future `updatedAt` with `clock`. Reason "waiting" is never acceptable:
   it means the CLI waits on a person (`claudeWaiting` in `activity.go`), so
   the open input is that person's answer, which is the harm of item 501
   comment 7450 and of ADR-024. The 30 seconds keep a rename away from a
   turn that has just ended, when a person or a background result may start
   the next one. A future time is a clock step, one of ADR-024's findings.
6. **Nobody at the keyboard.** `tmuxClientCount` reports zero attached
   clients; an error holds. Reason `attached`. The guard never uses
   `tmuxClients`, which turns an error into zero.
7. **The pane.** `tmux list-panes -s -t =agent` shows one window with one
   pane. That pane is not dead, not in a mode (copy mode), and has
   `synchronize-panes` off. Its pid is the CLI's parent (run-agent), and
   the CLI's process group is the terminal's foreground group (`tpgid` in
   `/proc/<pid>/stat`). agentd keeps that pane id (`%N`), and every
   keystroke targets it, never `=agent:`. Reason `pane`. Reason for the
   rule: ADR-024's active-pane and pane-synchronisation findings, and a
   shell or tool the CLI runs in the foreground would take the keys.
8. **CLI version.** The CLI's version (one `claude --version` per boot)
   has the same major and minor number as a version for which the build
   recorded screen fixtures (rule 8.11); a patch release does not hold
   renames. Reason `cli-version`. Coordinator's ruling on Q-065-12: the
   screen check of condition 9 already holds on any screen it does not
   recognise, so an exact-version gate would only stop every rename at each
   routine CLI patch until someone recorded fixtures again.
9. **An empty input line and no prompt.** `tmux capture-pane -p -e -t %N`
   (the visible screen only, with attributes) matches the recorded fixture
   of an idle screen with an empty input line: the CLI's input box is the
   last element on the screen, and its input row holds the prompt mark and
   nothing else. A placeholder counts as empty only if the fixture shows
   one, recognised by its attributes. A choice list, a permission or
   question prompt, any text in the input row, or a screen the parser does
   not recognise holds the rename: reason `input` for text,
   `screen-unknown` for anything else. Two independent signals must agree:
   the CLI's own status (condition 5) and the screen.
10. **Pacing.** At least 10 minutes since `lastTypedAt`, and fewer than 2
    attempts at this T. Reason `paced`.
11. **The delivery lock.** agentd takes `deliver.lock` (`deliverLockFile` in
    `deliver.go`) without waiting, right after condition 1 and before any
    other read, and holds it to the Enter, so an `agent-run msg` delivery
    and a rename never interleave. If a delivery holds the lock, the rename
    is held with reason `delivering` and conditions 2 to 10 are not read.
    It is listed last only because it brackets the others.

**8.6 The keystrokes.** The text is exactly `/rename ` followed by T. T is
the `remoteControlTitle` value (`sessiontitle.Compose` of a title that passes
`Validate`: it starts with a letter or a digit and holds no control or
format character) or the session name (a DNS label). So the text cannot
start with a hyphen and holds no control character. agentd then:

1. Runs `tmux send-keys -t %N -l -- "/rename <T>"`: literal, so tmux reads
   no word as a key name. A title ending in `;` is Q-065-10.
2. Waits 300 ms and checks again. `list-panes` shows the same pane id, pid
   and flags. The record, read again, has the same `procStart`, `status`
   "idle" and the same `updatedAt` as at condition 2. No client is
   attached. `capture-pane` shows the input row holding exactly the typed
   text, and no prompt. While a slash command is typed the CLI may show its
   own hint for that command under the input box; the typed-state fixture
   records that hint, and the check accepts it and nothing else there.
3. If that check passes, and less than 1 second has gone by since it began,
   runs `tmux send-keys -t %N Enter`.

If the second check fails, agentd does not press Enter. The text stays in
the input line, unsent. agentd records Failed, reason `left-in-input`, and
does not type this T again (Q-065-13). Amended 2026-11-13, fix round 1, the
coordinator, not Tom (section 9.4): when the input row still holds exactly
the text just typed, with the cursor directly after it, agentd sends one
Backspace per character of that text, checks the row is empty, and records
Held, reason `interrupted`. That does not count as one of the two attempts
when the row came back empty. In every other case the line is left alone.
This is the only case in which a key other than the text and one Enter is
sent. Reason: a prompt that opened between
the first read and the typing would have taken the text, and an Enter there
would choose an option, which is the 7450 harm. Withholding the Enter turns
that case into visible, unsent text.

agentd never sends Escape, Backspace or any Ctrl key, never clears the input
line (its own text included, since a person may have typed beside it).
Amended 2026-11-13, fix round 1, the coordinator, not Tom: the Backspace
named in the paragraph above is the one exception, and only for text this
pod just typed. The sentence continues: agentd never clears an input line
a person typed,
never pastes other text, never sends a second Enter, never types while the
guard holds, and never retries inside one heartbeat.

The risk that remains: no ordering of reads makes them atomic with the
keystroke (ADR-024). The window runs from the second check to the Enter,
under one second. As far as is known, and not tested here, a question or
permission prompt comes from a turn, and a turn starts from a typed line, a
phone message or a background result. The unchanged `updatedAt` at the
second check shows that the CLI's status did not change before it. That a
prompt cannot appear inside the window is an inference, not a measurement. Tom chose this route with the risk stated (item 774 comment
7547).

**8.7 Held renames.** When the guard refuses, the rename is held: no keys,
state Held, the reason code, and `since` set at the first refusal for this
T. agentd tries again at each heartbeat answer (every 60 seconds) and at no
other time.

- **No expiry.** A held rename waits as long as the session runs, because
  waiting types nothing. `agent-run show` prints the reason and how long it
  has waited, so the agent that asked sees "busy" or "waiting" and does not
  reach for another route, and a reason that never clears (such as
  `screen-unknown`) shows the parser needs work.
- **A newer title replaces a held one** at the next heartbeat answer: T
  changes, `attempts` goes back to 0, and `since` starts again. The pacing
  from `lastTypedAt` still holds. A clear replaces a held title the same
  way. Two writers cannot replace each other unseen, because of `expect`
  (rule 8.1).
- **Suspend and resume.** Nothing held needs saving. The new pod's first
  heartbeat answer gives T, and `title.json` gives `applied`,
  `lastTypedAt` and `attempts`. Rule 8.4 then decides.
- **An older agentd** ignores the answer (rule 8.3) and does nothing. Its
  session shows the recorded title in listings and no rename state.

**8.8 Confirmation and status.** After the Enter, agentd reads the record
(same pid and `procStart`) every 500 ms for up to 10 seconds.

- `name` equals T: `applied` becomes T, state Applied, reason `confirmed`.
- Otherwise state Typed. At each later heartbeat agentd reads the record
  again, and `name` equal to T makes it Applied. After 2 minutes without
  that, with fewer than 2 attempts, the rename is Held (`paced`) and may be
  typed once more, under the whole guard, after 10 minutes. At 2 attempts it
  is Failed, reason `not-confirmed`, and agentd does not type it again. A
  newer title starts over.

The record is an undocumented provider file. agentd already depends on it
for the Remote Control id (`readRemoteRecord`) and the agent state
(`claudeStatus`). If its shape changes (a field missing or of another type,
the file over 64 KiB, or unparseable), the state is Unknown with reason
`record-shape`, and agentd types nothing, because it could not confirm what
it typed. It never types again blindly.

Status fields, in `status.remoteControl` (`protocol.RemoteControl` and
`RemoteControlStatus`), each copied by `remoteControlStatus` with a clip:

- `title` keeps slice 1's meaning: what agentd passed at this pod's launch.
- `providerTitle` (new; `MaxLength` 128 and at most 128 bytes): the
  record's `name` when its `nameSource` is "user", else empty. It is what
  the provider has after a rename, by the CLI's own record. It is empty for
  a session only named at launch, because the record then holds a derived
  name that is not the phone's title (item 785 comment 7560).
- `rename` (new object), absent while nothing was ever to apply:
  - `state`: Applied, Held, Typed, Failed or Unknown (enum, `MaxLength` 16);
  - `reason`: one code (enum, `MaxLength` 32): `launch`, `confirmed` or
    `already` when Applied; `process`, `not-registered`, `busy`, `waiting`,
    `status-unknown`, `quiet`, `clock`, `attached`, `pane`, `cli-version`, `input`,
    `screen-unknown`, `paced` or `delivering` when Held; none when Typed;
    `left-in-input`, `not-confirmed` or `invalid-title` when Failed;
    `record-shape` when Unknown;
  - `since`: when this state began for this T;
  - `attempts`: 0 to 2.

  It carries no title text: the recorded title is in spec, and the
  provider's is `providerTitle`.

An older operator's `remoteControlStatus` copies named fields only, so it
drops the new ones, and the CRD copy (rule 8.1) comes before the pin.
`agent-run show` replaces `registered as <name>` (section 3) with the
registration state, the recorded title, `provider title`, and one rename
line, for example: "applied"; "held since 14:02Z: busy; it is typed when
the session is idle with an empty input line"; "typed at 14:12Z, not yet
confirmed"; "failed: the rename text is in the session's input line,
unsent; its owner may send or delete it"; "unknown: the CLI's record has a
shape agentd does not know, so nothing is typed". The API view carries the
same fields. Reason: the agent that recorded the title needs to know whether
it landed, and why not, without reading the pod.

**8.9 Safety rules.**

1. The only text the platform types for a title is `/rename ` plus the
   target, then Enter, by rule 8.6.
2. Never while the CLI says "waiting", while a question or permission
   prompt is on the screen, or while the input line holds any text.
3. Never into a session whose state, screen, pane or process agentd cannot
   read: every failed or unknown read holds.
4. A rename never submits a prompt to the model. Enter is pressed only when
   the input row holds exactly the command, on a CLI version whose screens
   were recorded and where `/rename` ran locally (item 774 comment 7546).
5. Nothing is typed into a Codex session, a task, a local session, a
   summoned session or an explicit continuation.
6. The typing happens in the session's own pod, by its own agentd. No pod
   exec, no operator keystroke route, no `agent-run msg`.
7. No restart, no new conversation, no change of owner, parent, profile or
   access: safety rules 2 to 4 stand.
8. Audit. The API logs one line per change: session, caller, and the first
   12 hex of the SHA-256 of the new composed title. The session keeps
   `title-set-by` and `title-set-at`. agentd logs each state change with its
   reason code, attempt and the same hash prefix, and logs each keystroke
   call by kind (text, Enter), never by content. No log line holds title
   text. Reason: a title is free text a caller chose; pod and operator logs
   spread wider and last longer than the record; and a log of what was typed
   into a person's terminal should not hold the text. The record holds the
   current title for anyone allowed to read it, and the hash prefix matches
   a log line to it.
9. Rate: 60 seconds between recorded changes, 10 minutes between typed
   renames, 2 attempts per title.

**8.10 The five coordinators (slice 4).** Each rename is one recorded title,
made through the route by a caller rule 8.2 allows. The main coordinator
records the titles of the other four (the workbench rule; it is also the
parent of two). Another workbench session, one of the four, records the main
coordinator's, since no session titles itself (Q-065-08). Each title is
applied by that session's own agentd when its guard allows. A session whose
agentd predates slice 3 shows the record in listings at once, and gets the
provider rename only after its next resume onto an image whose agentd
applies it (rule 8.4: no `title.json`, and a title). Nobody suspends or
resumes a coordinator for this. R0 and R1 of section 5 are taken for each.

**8.11 What the build must test.** Unit tests with fakes, at two CPUs: a
fake runner for tmux (list-panes, list-clients, capture-pane, send-keys), a
record file the test writes, a fake clock and a fake heartbeat answer.

- **Every guard condition refusing on its own**, with its reason and no
  `send-keys` call: each of rule 8.5's eleven, and within them: status
  "waiting"; status "busy"; an unknown status; idle for only 10 seconds; an
  `updatedAt` in the future; a record whose `procStart` is another
  process's; a record over 64 KiB; a record that is a link; a record
  without `name` or `nameSource` (Unknown); two panes; two windows; a pane
  in copy mode; `synchronize-panes` on; a pane whose pid is not the CLI's
  parent; a CLI that is not the foreground group; a list-clients error; one
  attached client; an unknown CLI version; a capture-pane error; screens
  with a question prompt, a permission prompt, text in the input row, and
  an unrecognised screen; `deliver.lock` held.
- **The exact keystrokes**: the two `send-keys` argvs byte for byte, both on
  the pane id, for a title with "é" and for the session name on a clear;
  no other tmux write in any path.
- **The second check**: each change between the text and the Enter (status,
  `updatedAt`, pane id, a client attaching, an input row that is not the
  text, a prompt on screen) withholds the Enter and gives Failed
  `left-in-input`; more than 1 second also withholds it.
- **Held, replaced and applied**: held, then applied when the guard clears;
  a newer title replaces a held one; a clear replaces a held title;
  confirmed within 10 seconds; not confirmed, then confirmed at a later
  heartbeat; not confirmed twice, then Failed `not-confirmed` with no third
  typing; pacing across a restart through `title.json`; text left in the
  input and later sent by someone turns Applied.
- **Launch and resume**: a first launch with a title types nothing; a resume
  after a change types once; a resume with no change types nothing; no
  `title.json` and no title types nothing; no `title.json` and a title
  applies once; a record with a "user" name that is not T, while `applied`
  is T, types nothing.
- **Versions**: an older agentd ignores the new heartbeat body (a test on
  today's `Send`); a new agentd on a 204 answer types nothing; an answer
  without the `title` object types nothing; a title in the answer that
  fails `Validate` is never typed.
- **The access rule** (API server, fakes): Tom, a trusted client, a parent
  session, a workbench session on another session, and a coordinator host
  on its child are allowed. The session itself (ordinary and workbench), a
  sibling, a child on its parent, an ordinary session on a session it did
  not create, Meatball, a summoning caller and the portal reader are
  refused. A summoned target is 409; a stale `expect` is 409
  `title_changed`; the same title is 200 with no write; a second change
  within 60 seconds is 429. A change leaves the pod, its UID and its
  annotations alone, and a resume after a change passes the new title and
  the same `--resume` id.
- **Schema** (`agentsession_validation_test.go`): adding, changing and
  removing a title are accepted; each bound is still refused on an update;
  no other spec field becomes changeable.
- **Status**: the bounds of `providerTitle` and `rename`, the clip, and an
  older operator dropping both.

**Live proof**, on the unused test session `dev-env-1113-004614` only (or
another fresh, unused one), after install, by the coordinator:

1. Record the screen fixtures from it first, read-only, with `capture-pane`:
   an idle screen with an empty input line, with and without the CLI's
   placeholder text, and the screen while the command is typed. Read what
   `status` the record shows while a background command runs and while a
   question is open, and write both into section 9.1 before slice 4.
2. R0 of section 5.
3. A recorded change while it is idle with an empty input line: within two
   heartbeats, `rename` is Applied, `providerTitle` is the composed title,
   and R1 equals R0 in every identity reading.
4. A recorded change while its input line holds text the tester typed into
   that test session: Held `input`, nothing typed, the text unchanged. Then
   the tester clears the line: Applied.
5. A suspend and resume: R2 keeps the title, `rename` stays Applied, and
   nothing is typed.
6. A clear: `providerTitle` becomes the session name.

Record the readings on item 787, with SHA-256 prefixes in place of ids and
links, as section 5 says.

**8.12 Review.** The build types into live sessions and adds a write route
with an access rule. So it gets an adversarial review by another model
family before it merges, read-only, on its final head, with ADR-024's
findings as the starting list, and every finding answered on the PR. Its
first live use is on the unused test session.

### 9.3 Fix round 1 (the coordinator, 2026-11-13, not Tom)

Each item below is the coordinator's decision on the adversarial review of
pull request 1127. It amends the sentence it names. The first text of
sections 9.1 and 9.2 stays.

1. **The screen (review F1).** Rule 8.5 condition 9's "last four non-blank
   rows" reading is not enough. The parser reads the whole visible screen
   and answers "empty input" only when all of these hold: exactly one input
   box, and it is the last element before the status row or rows; the input
   row holds the prompt mark, the no-break space and nothing else, or the
   dim placeholder in the placeholder fixture; no row anywhere is a
   selection hint ("Enter to select", "Esc to cancel"), a numbered option
   list, or a spinner or interrupt hint; the cursor is on the input row
   directly after the prompt mark and the no-break space. The cursor is
   read with `tmux display-message -p -t <pane id>` for `cursor_x` and
   `cursor_y`. A read error is unknown. Spaces-only, newline-only or
   multi-row input is not empty. "Holds exactly this text" needs the same
   structure, the exact text, and the cursor directly after it. Everything
   else is unknown. The recorded fixture `question-open.2.1.321.ansi` is
   unknown, and nothing is typed on it. Answered on the advisory review of
   this fix, and not a new ruling by Tom: a numbered list in the
   conversation is one of those rows. That hold is expected, because the
   check is the whole screen, not only the rows next to the box.
2. **The off switch (F1b).** The operator flag `--title-rename-sessions` is
   a comma-separated list of session names, or the single value `*`. Empty,
   the default, names no session. The heartbeat answer carries the title
   object only for a session the flag names. For every other session the
   answer is 204 with no body, which is what main answers today, so agentd
   types nothing (the first bullet of rule 8.3). The PUT title route stays
   on and only records. `agent-run show` says "not enabled for this
   session" in the rename line when the flag does not name it. A malformed
   value refuses startup. This supersedes the sentence in rule 8.3 that
   every heartbeat answers 200 with a title object.
3. **The state file before the keys (F2).** agentd writes `title.json` with
   the new `lastTypedAt` and the incremented `attempts`, and syncs the file
   and its directory, before it types. If that write fails, it types
   nothing and holds with reason `state`.
4. **Time limits (F3).** Every subprocess of the guard and of the typing,
   each tmux call and `claude --version`, has its own timeout of 2 seconds.
   `claude --version` runs once per boot, outside the delivery lock, and is
   cached. The whole guard-and-type sequence has a budget of 5 seconds,
   after which it holds. A timeout or error holds with the reason of the
   condition being read. The delivery lock is released on every path. A
   hung tmux does not stop a later heartbeat. The advisory review of this
   fix is answered here, and it is not a new ruling by Tom. agentd does not
   send the text unless one 2-second call still fits in the budget;
   otherwise it holds with reason `screen-unknown` and types nothing, and
   it does not keep the attempt. After the text is sent, the second check,
   the clear and Enter run on a 4-second deadline taken from the caller's
   context, so the budget ending does not skip the clear. That deadline is
   the only time past the 5 seconds, and only once the text is already
   sent. The confirmation poll of rule 8.8 runs only while the 5-second
   budget remains. A poll that stops leaves Typed, and later heartbeats
   continue the 10-second window.
5. **A later title in the same boot (F4).** "Nothing is typed after a first
   launch" in rule 8.4 means only that the launch title itself is not typed
   again. A title change recorded later, in the same pod's life, is applied
   under the guard. At a first launch `applied` is set to what was passed.
   A later answer whose target differs is applied. This supersedes "a first
   launch is never followed by a typed rename" for a target that is not the
   launch value.
6. **The pane format (F5).** The list-panes format is the one recorded in
   `internal/sessiontitle/testdata/tmux-facts.txt`. It uses
   `pane_synchronized`, which tmux 3.3a has. It does not use
   `window_synchronized`. A line whose field count is not the recorded
   eight, or whose flags are not exactly `0` or `1`, is reason `pane`.
7. **The last read (F6).** The CLI record is read again as the last read
   before the first `send-keys`, and the rename holds if anything changed
   since condition 5. Nothing else runs between that read and the
   keystrokes. This supersedes rule 8.6's window, which started at the
   second check.
8. **`updatedAt` (F7).** It must be a positive number. Null, zero, a string
   or a missing value is Unknown, reason `record-shape`.
9. **Attached clients (F8).** Any attached client counts, whatever its
   fields. Empty output is zero. One newline is one client. An error holds
   as `attached` and is never read as zero.
10. **Foreground again (F9).** The second check also re-checks that the CLI
    is still the terminal's foreground process group.
11. **Semicolon and backslash (F10).** A title may not end with a semicolon
    and may not contain a backslash. The reason is `title_characters`, in
    `sessiontitle.Validate` and in the CEL rules. A semicolon in the middle
    stays allowed. agentd also refuses to type any text ending in a
    semicolon (Failed, `invalid-title`), because tmux's argument parser
    strips a trailing semicolon. This decides the question rule 8.6 left
    as Q-065-10.
12. **The cursor again (F11).** The second check reads the cursor, as in
    item 1.
13. **A clear (F12).** A clear types only when `applied` is a title the
    platform put there. If `applied` already equals the session name, the
    state is Applied with reason `already` and nothing is typed.
14. **The title text (F13).** The heartbeat title object must hold a string
    `text`. A missing or null `text` is unknown: nothing is typed and
    nothing is cleared. `{"title": null}` stays "not present".
15. **Generation (F14).** A title-only change bumps `metadata.generation`
    and does not start a drain or replace a pod: drain eligibility compares
    the pod template revision, and `ensure` returns when the pod is already
    there. The title is not in the pod template. The title route does not
    clear a stored decision answer. These comparisons were left unchanged
    and asked of the coordinator on 2026-11-13: the drain restart proof,
    an in-flight decision write, a published canary's generation, and a
    rescue record. Same-pass freshness checks retry on the next reconcile
    and do not clear the stored record.
16. **When the file is written (F15).** `title.json` is written only when
    its content changes. A session that never had a title reports no rename
    object: this supersedes the sentence in rule 8.4 that sets `applied` to
    the session name when the file is missing and the session has no title.
    `since` is set once per state, not on every heartbeat.
17. **`agent-run title` (F16).** It prints the composed text, or the session
    name when the title is cleared, and one sentence that it is typed when
    the session is idle with an empty input line. A session that does not
    exist gets "no such session". A missing route still says the operator
    has no session title route and that nothing was changed.
18. **A command named rename (F17).** Before typing, agentd checks whether a
    command or skill named `rename` exists. The paths are
    `<project>/.claude/commands`, `<project>/.claude/skills`,
    `<claude-config>/commands`, `<claude-config>/skills`, and the same two
    directories under each plugin that a readable settings file lists as
    enabled. The settings files are `<claude-config>/settings.json`,
    `<project>/.claude/settings.json` and
    `<project>/.claude/settings.local.json`. Enabled plugins are read from
    `<claude-config>/plugins/installed_plugins.json`. The project directory
    is the launch record's `dir`. The claude config directory is
    `$CLAUDE_CONFIG_DIR`, or `~/.claude`. If one of those names exists, or
    the check cannot be made, the rename holds with reason
    `command-shadowed`.
19. **`shell` (with F18).** The record status "shell" holds with reason
    `shell`, not `status-unknown`, and is never treated as idle. The reason
    codes `state`, `shell` and `command-shadowed` are in the status enum,
    the CRD and the show text. Show says how long a held rename has waited.
20. **Tests (F18).** A test that expects nothing to be typed fails if the
    fake runner saw any tmux subcommand other than `list-panes`,
    `list-clients`, `capture-pane` and `display-message`. `claude --version`
    is allowed. The cases added with this round are a launch for another
    session, UID or a launch that is not a TUI; a title change during a
    first-launch boot; a failed `title.json` write; `updatedAt` null, zero,
    a string and missing; a hung runner; whitespace-only and multi-row
    input; and a title ending in `;`.

### 9.4 The title stays out of spec (the coordinator, 2026-11-13, not Tom)

The coordinator answered the generation question the same day. These
rulings replace the sentences they name. ADR-026 stays Accepted and is not
edited. [ADR-027](../adrs/027-session-title-annotation.md) is Proposed. It
supersedes ADR-026's decision that `spec.title` may change, and it keeps
ADR-026's guarded typed rename.

1. **The record (replaces rule 8.1's opening, the patch bullet, the clearing
   sentence, and 9.3 item 15).** `spec.title` stays exactly as slice 1
   installed it: set at create, immutable. The two CEL relaxations are
   removed. D-39 is not amended. `PUT /v1/sessions/{name}/title` writes
   `dev-env.haynesops.com/title` and keeps `title-set-by` and `title-set-at`.
   The access rule, the `expect` check, the `resourceVersion` precondition
   and retry, the 60-second limit and the refusals (a summoned target, a
   session being reaped) stay. The value is validated with
   `sessiontitle.Validate` before it is written. An empty value means no
   title. The effective title is the annotation when it is present and
   valid, and `spec.title` otherwise. `expect` is compared with the
   effective title. A clear writes the annotation with an empty value.
   Every reader validates the annotation. A value that fails is ignored,
   the session keeps `spec.title`, and `agent-run show` says the annotation
   was ignored. Nothing types a value that failed validation. The heartbeat
   and the API view use the effective title. The view also carries the
   title given at create when it differs. A title change leaves
   `metadata.generation` unchanged, so a drain restart proof, an in-flight
   decision write, a published canary and a rescue record are unchanged by
   it. The same-pass generation checks are unchanged because they no longer
   see a new generation.
2. **`shell` (replaces the work order's hold, the sentence in 9.1, and 9.3
   item 19).** Story 787 comment 7645: `shell` is what a session shows
   between turns while a background shell command is running, and `idle` is
   the same with none running. Condition 5 accepts `shell` exactly as it
   accepts `idle`, including the 30 seconds of quiet. `waiting`, `busy` and
   any unknown value still hold. The reason code `shell` is not in the enum.
3. **An interrupted line (rule 8.6 and 8.9).** When the second check fails
   and the input row still holds exactly the text agentd typed, with the
   cursor directly after it, agentd sends one Backspace per character,
   checks that the row is empty, and records Held with reason `interrupted`.
   That attempt does not count toward the two when the row came back empty.
   Otherwise the line is left alone and the state is Failed,
   `left-in-input`. This is the only key other than the text and one Enter.
   A title with a non-ASCII letter counts one Backspace per character. A
   row that differs by one character, a cursor elsewhere, or a capture that
   fails sends no Backspace.

### 9.5 Fix round 2 (the coordinator, 2026-11-13, not Tom)

These rulings are the coordinator's, from the second review of pull request
1127. They are not Tom's. ADR-027 stays Proposed. ADR-026 stays Accepted and
is not edited. Tom's ruling stands: a rename is typed only while the session
is idle with an empty input line. A rename is not typed while a turn runs.

1. **Characters.** A title may hold only printable ASCII (U+0020 to U+007E)
   and U+00A1 to U+024F, except U+00AD. `sessiontitle.Validate` refuses
   anything else with reason `title_characters`, so creation and rename share
   one rule. The same bound is a CEL rule on `spec.title`. agentd refuses to
   type any text that holds another character, whatever its source: the state
   is Failed, `invalid-title`, and nothing is typed. A session that already
   has such a title keeps it. It is never typed. When the heartbeat answer is
   that launch title, the state stays Applied with reason `launch`. It is not
   Failed `invalid-title`, because the provider already shows it. A resume
   does the same when `title.json` already records that composed text, or
   when the CLI record's name is that text: the state is Applied, reason
   `already` when the record is what shows it. A different text that the
   character rule refuses is still Failed `invalid-title`, and nothing is
   typed. A wide character, a combining mark, a zero-width character and
   U+00AD are refused. é is accepted.
2. **Cleanup.** The Backspace cleanup of this pod's own unsent text does not
   depend on busy or idle, and a spinner row does not block it. All of these
   are read fresh, directly before the keys: no attached client; the same
   pane id; the CLI still the pane's foreground process with the same pid;
   the record readable with a status that is not "waiting"; the screen has
   the one input box whose row holds exactly the typed text, with the cursor
   directly after it.    `ClassifyBox` answers that question and does not treat
   a spinner row as a disqualifier. A numbered line or a selection hint
   anywhere on the screen still holds before typing. `ClassifyBox` applies
   those checks only to the three rows of the input box, so a numbered line
   left in the conversation does not block cleanup or delivery once the input
   row is empty. A question, a selection or a permission screen has no input
   box and stays unknown. After the Backspaces, the row
   must be empty with the cursor at column 2. Otherwise the state is
   `left-in-input`.
3. **Self-heal.** While `title.json` records `left-in-input`, every later
   pass for a session the flag names first tries that cleanup with the
   recorded text. If the one input box shows an empty row instead, cursor at
   column 2, the leftover is gone and the mark is cleared. No new text is
   typed in the same pass as a cleanup or a cleared mark. When the flag does
   not name the session, agentd sends no key at all.
4. **Delivery.** `Deliver` checks `title.json` under its lock. When the file
   records `left-in-input`, Deliver reads the screen and the cursor, read
   only, with the same two-second limits. It pastes only when the one input
   box shows an empty row with the cursor at column 2, and then clears the
   mark. Otherwise it sends nothing, including no Backspace, and returns
   `not delivered: the session's input line holds unsent rename text`.
   `agent-run msg` prints that sentence. With no mark, Deliver does what it
   does on main.
5. **Spinner rows.** A spinner row is a row whose first character is one of
   U+273B, U+2722, U+273D, U+2736, U+00B7, U+002A, U+2733 and U+273A, followed
   by one space and a word that ends in "…". It is an extra hold before
   typing. It is not the busy signal. The record is the busy signal. A
   finished row ("✻ Crunched for 15s · done 8:31 AM", "✻ Worked for 12s ·
   done 8:29 AM") does not match. A streaming frame with no spinner row reads
   as an empty input, and the guard still holds when the record says busy.
6. **Interrupted.** At most three interrupted cycles for one target. The
   fourth sets Failed with reason `interrupted`. A new target starts the
   count again. The cleanup conditions are item 2.
7. **The read after the write.** agentd reads the client count, the screen
   and the cursor again after the staged state write, directly before the
   last record read. The earlier reads stay, so a hold before the write costs
   no write. Any hold or failure after the staged write and before the text
   is sent refunds the attempt and `lastTypedAt`. A failure of the text
   send-keys stays `left-in-input`: the text may have been sent.
8. **The command scan.** Each scanned directory is opened with `O_DIRECTORY`
   and `O_NONBLOCK`. A link to a directory is followed. A FIFO, or a link to
   one, fails at once and the rename holds `command-shadowed`. The whole scan
   runs inside the five-second budget. A hard stop holds `command-shadowed`
   and releases the lock. Commands directories are walked to depth 3, with a
   cap on entries. Each skill's frontmatter `name` is read with `readBounded`.
   Still not checked: managed settings, `--plugin-dir` and `--add-dir`.
   agentd's launch line does not pass those flags. `claudeArgs` passes the
   model, the effort and `--dangerously-skip-permissions`.
9. **An untyped clear, and an untitled launch.** When `applied` is empty,
   `attempts` is 0 and the target is the session name, agentd sets `applied`
   to that name and does not type it. `attempts` and the state are the values
   from before a target change. A title that was typed, and whose confirm did
   not finish, is not treated as never typed: a later clear types the session
   name under the guard. A missing `title.json` is not created for an untyped
   clear. An untitled first launch, whose text is empty and whose launch
   value is the session name, is not seeded and does not report a rename.
10. **The version cache.** `cliMinor` caches only a parsed answer. A timeout
    or unparsed output is not cached. A parsed minor that is not one of the
    recorded minors is cached as not ok.
11. **`agent-run title`.** When the session's title rename is off, the
    command prints "not enabled for this session" instead of the idle
    sentence. `show` already says that.
12. **A failed Enter.** The result of the Enter send-keys is kept. On an
    error or a timeout, agentd reads the record once. If the record's name is
    the target, the state is Applied. Otherwise it runs the cleanup in item
    2. Otherwise the state is `left-in-input`. It does not set Typed.

### 9.6 Fix round 3 (the coordinator, 2026-11-13, not Tom)

These rulings are the coordinator's, from the third review of pull request
1127. They are not Tom's. ADR-027 stays Proposed. Tom's ruling stands: a
rename is typed only while the session is idle with an empty input line.

1. **A refused text does not wipe the mark.** While `title.json` records
   `left-in-input`, a pass does the cleanup or the empty-row clear and
   nothing else, whatever text the heartbeat answer carries. That branch
   runs directly after the file is loaded, above first-launch seeding, above
   `renameTarget` and its refusal, and above `keepShownTitle`.
2. **A clear after any typed title is typed.** The untyped-clear shortcut
   also needs `lastTypedAt` to be zero. If anything was ever typed in this
   file's life, a clear is typed under the guard. `lastTypedAt` is not
   cleared when the target changes.
3. **Selection phrases are refused.** `sessiontitle.Validate` refuses a title
   that contains "enter to select", "esc to cancel" or "esc to interrupt",
   compared without regard to letter case, with reason `title_characters`.
   The Explain sentence names the phrases. agentd refuses to type a text
   that holds one of them, whatever its source: Failed, `invalid-title`,
   nothing typed. Validate and agentd share one function. The CEL rule on
   `spec.title` does not carry the phrases: the phrases are a property of
   the screen this build types into, and `spec.title` stays the immutable
   create-time field. The screen parser stays strict and is not changed. The
   title is drawn in the input box's top rule, so a title that contained one
   of the phrases would make every later screen unknown.
4. **The typed line must fit the pane.** agentd reads the pane width in the
   same display-message call as the cursor, format
   `#{cursor_x} #{cursor_y} #{pane_width} #{pane_height}`. It types only when
   `runes(typed) + 8 <= pane_width`, four columns more than the measured
   limit in section 9.1. Otherwise the state is Held, reason `pane`, nothing
   is typed and no attempt is spent. The check runs on the early cursor read
   and again on the read after the staged write. A hold on the second read
   refunds the attempt, as the other post-write holds do. A width that is
   missing or not a positive number is a cursor read error. A height that is
   not a positive number is the same error, because the four numbers are one
   read.
5. **The later Backspace is bound to the process that received the text.**
   The mark records the pane id, the CLI pid and that process's `procStart`.
   They are written when the mark is set and cleared when it clears. On a
   later pass, Backspace is sent only when the live pane id, pid and
   `procStart` equal the recorded ones and the CLI minor is a recorded one,
   on top of the round-2 conditions. If they differ, no key is sent. The
   empty-row clear is not bound to them. A mark written by an older file,
   with none of the fields, gets no Backspace, only the empty-row clear.
   Deliver does not gain this check. When Deliver clears the mark because
   the row is empty, it clears the fields with it.
6. **The command scan reads only the entries the cap still allows.**
   `ReadDir` is given a count, not -1. More entries than the cap still holds
   `command-shadowed`. The stop is checked between entries. Every open is
   non-blocking. A hung network mount is not bounded. The home and project
   directories are on the pod's own volume. There is no goroutine. The bound
   is per directory read: a subdirectory's read does not subtract the parent's
   entries that were read and not yet counted, so the whole scan can hold up
   to about three times the cap at depth 3; the total stays bounded and the
   hold is unchanged.
7. **A resume whose record does not show the title.** A launch title does
   not appear in the CLI record, and `nameSource` is "user" only after a
   rename. The branch that keeps a shown title stays for a title someone
   typed by hand: the record's name equals the composed text and
   `nameSource` is "user". A resumed session with no `title.json`, a spec
   title outside the set, and the measured record (a derived name) reports
   Failed `invalid-title`, and nothing is typed. On 2026-11-13 the
   coordinator found no live session with a title outside the set: 6 titles
   on 365 sessions, all inside the set.
8. **Guards that a removed line must fail.** Tests cover the "waiting" check
   before Backspace in the same pass and in a later pass, the screen and
   cursor re-read after the staged write, the check that the row is empty
   after the Backspace keys, the refund when a client appears on the
   post-write client read, and the box-row selection check in `ClassifyBox`.
   The box-row test uses a derived screen, not a recording. An answer with
   no title object still sends no key when every cleanup condition is true
   and the exact text is on the screen.
9. **The interrupt cap, and the gap after the keys.** Three interrupted
   cycles end Held `interrupted`. The fourth types, erases and sets Failed
   `interrupted`. A send that is skipped because agentd is stopping costs
   one attempt and no key. Known limit: the mark is written after the keys.
   A kill of agentd ends the container and the CLI with it, because tini is
   process 1, so no text outlives agentd. The only gap is a final
   `title.json` write that fails after a good staged write, together with a
   failed cleanup.
