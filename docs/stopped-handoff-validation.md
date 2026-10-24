# Stopped handoff validation

This is the source check for story [#101](https://work.haynesnetwork.com/work_packages/101). It proves a synthetic predecessor-to-recipient fixture offline. It does not accept #101. An installed drill is a separate reviewed stage and is not run here.

`scripts/trials/workflow_handoff` stays an advisory custody checker. It does not see these fixtures, and this driver does not call it. Neither command dispatches work, grants authority, or changes a TaskClaim.

## What a passing source run shows

The predecessor and recipient are separate synthetic identities. The predecessor fixture has one public commit plus staged, unstaged, and untracked files. The manifest records each path, git state, and SHA-256. The recipient restore copies those states into its own directory and checks every byte. The predecessor snapshot, including HEAD, status, diff, and file hashes, must match afterward.

The record keeps the source UID, head, source time, rules revision, scope reference, authority reference, holds, pending findings, consumed budget, and attempt count. A public excerpt is labeled `public-handoff-excerpt`. Native history and native import stay false. The checker refuses a record that claims either.

Phase durations are the time this process spent parsing, validating, and reducing that record. There is no deadline and no fixed minute stop. A result that arrives after the coordinator's initial wait is an explicit `result-after-wait` event, not a clock inside the tool.

## What it refuses or blocks

| Record | Result |
| --- | --- |
| Hash-matched stopped predecessor, explicit pickup, acknowledgment, and next owner | Synthetic restore is authorized. This is not an installed journey. |
| Missing or changed context or WIP | Blocked custody. No restore and no successor. |
| Running, uncertain, unavailable, ambiguous, or unverified predecessor | Blocked custody. An exited process is not a verified stop. |
| Observed UID or head differs | Blocked custody. No successor. |
| Repeated identical event | Ignored replay. No second result or owner. |
| Same event identity with a different payload | Conflict. Manual disposition. No chosen owner, result, or successor. |
| Reconnect or delayed running | Recorded as a refusal. It does not replace a verified stop or create another result. |
| Late result after the initial wait | One observed hash. `installedJourney` stays false. |
| Missing pickup, acknowledgment, or next owner | Partial. The tool does not invent them. |
| Continuation unavailable, or assignment not independently authorized | No successor. The recorded next owner stays the one in the record. |
| A later different successor request | Does not replace the first recorded successor. |
| `taskClaim` other than `unchanged` | Refused. Nothing is applied. |

`checkerAuthority` and `advisoryAuthority` are `none`. `successorAction`, publication, deployment, and the installed drill are `not-performed`.

## Commands

From a checkout, with no network and no model call:

```bash
go run ./scripts/trials/stopped_handoff check scripts/trials/stopped_handoff/testdata/source-accepted.json
go run ./scripts/trials/stopped_handoff preflight
```

`check` reads one JSON document, or stdin when the argument is `-`, and writes only its result. `preflight` prints a capability skeleton. Source presence is `checker-present`. Release, profile, operator settings, and route activation are unknown or not read. `deployedCapability` is false. Live stop, read, restore, and assignment are `not-performed`. H7 retention is not required for this excerpt check.

`materialize`, `verify`, and `restore` are explicit fixture commands. They refuse a directory inside this source worktree. `restore` writes the recipient only after `check` authorizes restore, and only inside the empty `--into` directory. Tests keep those directories under the test temp directory.

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
