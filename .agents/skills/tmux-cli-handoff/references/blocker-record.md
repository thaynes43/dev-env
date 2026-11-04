# Blocked-handoff record

The record that [tmux-cli-handoff](../SKILL.md) step 6 writes. List every blocker
found, even two in the same step, each with its own evidence. `at` is the time
of the evidence command; `recordedAt` is when you write the record. Use the
classes and closed errors from the skill's step 1 table.

```json
{"recordedAt": "<UTC>", "actor": "<session, and its ServiceAccount if any>",
 "target": "<task, host, session, PR and head>",
 "staged": "<path of the correction, and who can read it>",
 "blockers": [
  {"step": 1, "class": "actor-rbac", "closedError": "pods-exec-denied",
   "evidence": [{"command": "kubectl auth can-i create pods/exec -n <ns>",
                 "exitStatus": 1, "output": "no", "at": "<UTC of the command>"}]},
  {"step": 2, "class": "wrong-session", "closedError": "session-unmapped",
   "evidence": [{"record": "<path>", "finding": "names no local chat or pane"}]}],
 "unknown": ["<host checks not run, for example whether the author has ended>"],
 "notes": ["<for example: CLI absent on the actor>"],
 "notDone": ["exec", "new session", "grant request", "send-keys"],
 "whoCanAct": "<the custodian, or a session that already holds that terminal>",
 "decisionNeeded": "<what would clear each blocker, and who decides>"}
```

Field notes:

- `staged`: where the correction waits, and who can read it there.
- `unknown`: checks that could not run, for example host checks after
  `actor-rbac`, or whether a cloud agent itself has ended.
- `notes`: findings that are not blockers, such as a CLI missing only on the
  actor.
- `decisionNeeded`: what would clear each blocker and who decides. For
  `no-live-session`, an explicit single-writer successor or a verified follow-up
  interface, after evidence that the author ended or released custody. For
  `modal-pending`, the specific approval of the access-decision owner (Tom,
  unless a recorded rule delegates it) for exactly that answer and path.
