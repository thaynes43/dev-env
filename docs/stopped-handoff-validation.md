# Stopped handoff validation

This is the source check for story [#101](https://work.haynesnetwork.com/work_packages/101). It proves a synthetic predecessor-to-recipient fixture offline. It does not accept #101. An installed drill is a separate reviewed stage and is not run here.

`scripts/trials/workflow_handoff` stays an advisory custody checker. It does not see these fixtures, and this driver does not call it. Neither command dispatches work, grants authority, or changes a TaskClaim.

## What a passing source run shows

The predecessor and recipient are separate synthetic identities. The predecessor fixture has one public commit plus staged, unstaged, and untracked files. The manifest records each canonical path, git state, and SHA-256. A path must already be canonical: `./` aliases and any `.git` component are refused. A committed entry must be one regular blob in `HEAD`, with mode `100644` or `100755`, and the worktree mode must match that blob. Staged and unstaged entries must be stage zero in the index, and the worktree mode must match the index. Untracked files must be mode `0644` or `0755`. A recipient restore is allowed only for a clean commit, staged `A ` or `M `, unstaged ` M`, and untracked files. Unsupported modes, including a tracked exec-bit change and mode `0600`, are refused before `git init` or any other recipient write. The restore then copies those bytes and sets the source mode after checkout, so a restrictive umask does not drop an allowed mode. The predecessor and recipient Git HEAD must both equal the record's predecessor head and observed head before that write. The predecessor snapshot, including HEAD, status, diff, index hash, and file hashes, must match afterward. If a later failure leaves that snapshot unchanged, the result says the original was preserved.

This overlay is a fixture stand-in. The supported code-rescue path commits staged and unstaged work into one tree and does not keep that split. An installed drill has to prove its own preservation. `preflight` names that rescue route and does not activate it.

The record keeps the source UID, head, source time, rules revision, scope reference, authority reference, holds, pending findings, consumed budget, and attempt count. A public excerpt is labeled `public-handoff-excerpt`. Native history and native import stay false. The checker refuses a record that claims either.

Phase durations are the time this process spent parsing, validating, and reducing that record. There is no deadline and no fixed minute stop. A result that arrives after the coordinator's initial wait is an explicit `result-after-wait` event, not a clock inside the tool.

## What it refuses or blocks

| Record | Result |
| --- | --- |
| Record-asserted stopped predecessor, with explicit pickup, acknowledgment, and next owner | `check` without `--verify-root` accepts an internally consistent record. It does not read fixture bytes, and the file booleans do not grant authority. This is not an installed journey. |
| Missing or changed context or WIP | Blocked custody. No restore and no successor. |
| Running, uncertain, unavailable, ambiguous, or unverified predecessor | Blocked custody. An exited process is not a verified stop. |
| Observed UID or head differs | Blocked custody. No successor. |
| Repeated identical event | Ignored replay. No second result or owner. |
| Same event identity with a different payload | Conflict. Manual disposition. No chosen owner, result, or successor. |
| Reconnect or delayed running at or after `predecessor.sourceTime` | Blocked custody. That time is the verified-stop instant. A running or reconnect observation before the stop stays a stale refusal and does not replace the stop. |
| Late result at or after the verified stop | The earliest `result-after-wait` by `sourceTime` is the one observed hash. A later different hash does not replace it. Two different hashes at the same instant conflict. `installedJourney` stays false. A result before the verified stop is not that hash and blocks custody. |
| Pickup, acknowledgment, next owner, or successor-request before the verified stop | Blocked custody. Nothing is backdated into authority. A successor request is compared with the stop, not with pickup or acknowledgment, so one that follows the stop can still be the earliest successor. |
| Missing pickup, acknowledgment, or next owner | Partial. The tool does not invent them. Pickup and acknowledgment notes are not an actor identity. The next-owner string is kept as recorded and is not compared with either identity. |
| Continuation unavailable, or the record does not assert an authorized supported assignment | No successor. The recorded next owner stays the one in the record. A record assertion is not an authorization. |
| A later different successor request | Does not replace the earliest successor by `sourceTime`. Two different successors at the same instant conflict, and no successor is recorded. |
| `taskClaim` other than `unchanged` | Refused. Nothing is applied. |

`checkerAuthority` and `advisoryAuthority` are `none`. `successorAction`, publication, deployment, and the installed drill are `not-performed`.

## Commands

From a checkout, with no network and no model call:

```bash
go run ./scripts/trials/stopped_handoff check scripts/trials/stopped_handoff/testdata/source-accepted.json
go run ./scripts/trials/stopped_handoff preflight
```

`check` reads one JSON document, or stdin when the argument is `-`, and writes only its result. `preflight` prints a capability skeleton. Source presence is `checker-present`. Release, profile, operator settings, and route activation are unknown or not read. `deployedCapability` is false. Live stop, read, restore, and assignment are `not-performed`. H7 retention is not required for this excerpt check.

`materialize`, `verify`, and `restore` are explicit fixture commands. Roots are resolved with `EvalSymlinks` before confinement checks. A target is refused when its parent directory is inside a Git worktree, including when the process cwd is elsewhere and when the path is a symlink. Fixture reads and writes go through `os.OpenRoot`, so a symlink cannot leave that root. `--into` must not resolve inside the predecessor or its `.git`. `.git` must be a plain directory. Local Git config may contain only the keys `git init` writes here. Commands disable hooks and fsmonitor and set `GIT_OPTIONAL_LOCKS=0`, so a read does not rewrite the predecessor index.

`restore` writes the recipient only after `check` authorizes restore and the predecessor preflight passes, and only inside the resolved empty `--into` directory. Exit 0 means that write was verified. Exit 4 means the record did not authorize a write and the recipient was left untouched. Exit 2 means the fixture was refused. Exit 3 is a record conflict. `restore --record -` and `verify --manifest -` are refused because those commands do not read stdin. `verify --manifest` uses the same canonical JSON key rules as `check`. A fixture report binds `HEAD` before the byte check. A bare manifest has no head and checks paths, status, tree or index membership, modes, and bytes only. Tests keep fixture directories under the test temp directory.

Git commands also set `core.excludesFile` and `core.attributesFile` to `/dev/null`, so a developer home directory does not change status. Repository `info/exclude` still applies, and an ignored file cannot be labeled committed. Each of at most 8 manifest entries is read whole. The JSON document cap is 128KiB. This fixture does not add a per-file byte cap, a turn limit, or a deadline. Phase durations are measurements. A path-string race between resolution and Git is outside this single-user synthetic model. `make check-layout` can fail once when `grep -q` gets SIGPIPE under pipefail; that flake is pre-existing and this package does not change the layout check.

```bash
go test -p 1 -count=1 ./scripts/trials/stopped_handoff/
```

## Installed stage, not this packet

Preflight names one blocked operation and its owner:

- Operation: installed disposable predecessor stop and supported read/restore/assignment.
- Owner: separate reviewed installed-drill stage.

That stage needs accepted disposable identities and a supported stop, read, rescue, and assignment route. Retrieval for #101 has to work without a Ready source pod and without a new model turn. This source package does not perform that drill, does not stop a live predecessor, and does not mark #101 Accepted.

Proof classes stay separate:

| Class | This package |
| --- | --- |
| Source synthetic fixture | Exercised by the tests |
| Explicit-context installed proof | Not performed |
| Full-history proof | Not claimed |
| Exact native import | Not performed |
