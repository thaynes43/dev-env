# OP323 publisher compatibility, 2026-11-01

Continuation base for the retained-current read:
`f2dd20151c63b89e25bad20b035256328ec0d425`.
This pin includes the completed compatibility record below. It overrides the
`11f0152d534e6c33fca6bd87b4af3f2d2e1f3b84` sentence in that frozen audit.
The audit itself stays the terminal record of the earlier inspection.

Source pin in the frozen audit: `11f0152d534e6c33fca6bd87b4af3f2d2e1f3b84` (v2.84.1).
Grok inspected the formatter, fixed-slot handler/store, operator configuration
and existing tests. Sol verified the frozen two-file patch and file hashes
before integration. No additional publisher test was justified.

## Executable finding

The selected publisher's owner checked `/usr/local/bin/agent-run` on
`dev-env-1027-010058` at 22:26 UTC. It reports v2.45.0, commit
`86cef5bb619bbde29610e3436299a3a4362b7a88`, built with Go 1.27.2.
Its `usage-source --help` returned general launcher help with exit 0.
That does not establish command support. Never pass report paths or
publication arguments to that legacy executable.

Explicit fixed-slot publication shipped in v2.84.0, PR #831, merge
`dbc90034eb091b9c3767f61c399b5611fcc1e9d7`. A compatible signed image
contains `/usr/local/bin/agent-run`. Use a verified artifact through an
explicit path after the required approval; preserve the active session.
No binary was installed or replaced by this work.

## Contract and remaining work

The formatter consumes caller-supplied input and preserves original retrieval
time. It derives remaining percentage as the complement of used percentage,
keeps the reported minute window and reset, and leaves provider update time
null. The source is unattributed: publisher identity does not establish a
provider account or entitlement. Unsupported shapes are refused.

Publication makes one authenticated fixed-path PUT. Existing synthetic tests
cover canonical/raw/legacy success, errors, redirects, uncertain responses and
local refusal without client I/O. The command neither acquires input nor
schedules another publish. The maximum retrieval age is not a cadence.

Activation therefore needs a compatible executable, fresh permitted input,
an acquisition/cadence decision and specific runtime approval. Operator flags,
storage, named RBAC and portal config remain separate. A first manual publish
would not establish periodic ingestion or all-provider coverage.

A separate source gap remains at this pin: `usage-source` formats or publishes,
while `usage -o json` uses local preflight or file input. Neither fetches the
retained fixed slot. An explicit read mode is the next bounded OP323 slice;
it must preserve default local behavior and existing caller fences.

## Validation and limits

Grok's audit ran from 22:27:41 to 22:33:21 UTC and exited 0. It added no test
and executed no commands or suites. Its original frozen patch SHA256 is
`e3a90f31f702f5f5005aab88ebd55a4fe41306edcef4acd6c6d9731805bc366c`.
Sol copyedited the technical documentation and confirmed the local source pin.

Sol used checksum-verified Go 1.27.3 with two CPUs and one package at a time.
`go test -p 1 ./internal/agentrun -run '^TestUsageSource' -count=1` passed
in 2.062 seconds. A static local CLI build passed actual-process synthetic
fresh formatting and stale refusal. This is local build evidence, not
signed-image extraction, installation in the publisher, or live acceptance.

No provider/account request, actual report replay, live PUT, flag/config,
ConfigMap/Role/Binding, collector, permission or runtime change occurred.
Private receipts and original observation bytes remain outside Git.

## Retained-current read

Inspection of `f2dd20151c63b89e25bad20b035256328ec0d425` found the explicit
GET seam undelivered. `usage-source` still only formats a caller-supplied
report or, with `--publish`, PUTs it. `usage -o json` remains local preflight
or `--from-file`. Neither command reads
`/v1/provider-source-reports/codex-current-source`. This slice adds
`usage-source --read-current` in source. It does not amend the frozen audit
above and it does not replace the publisher tests.

### Compatibility remedy

The selected publisher session is `dev-env-1027-010058`. Its checked client is
v2.45.0, commit `86cef5bb619bbde29610e3436299a3a4362b7a88`, built with Go
1.27.2. That is the actual client. It is not an assumed 2.71 client. Do not
treat a 2.71 label as this session's binary or as proof that the slot can be
read.

Fixed-slot publication is source from v2.84.0, PR #831, merge
`dbc90034eb091b9c3767f61c399b5611fcc1e9d7`. The retained-current read is source
on this continuation pin only. The installed v2.45.0 `usage-source --help`
returned general launcher help and exit 0, which does not establish command
support. Do not pass a report path, `--publish`, or `--read-current` to that
installed executable: an unknown command can fall through to the session
launcher.

The remedy is an explicit path to a verified signed artifact that contains the
v2.84.0 or later `agent-run`, after the required approval. Check that
artifact's `version`, then that `help usage-source` starts with
`Usage: agent-run usage-source` and names `--read-current`, before any retained
read. Keep the running session image and its `/usr/local/bin/agent-run` in
place. This record does not install, replace, or probe a binary.

### Still absent

Acquisition, cadence, and account attribution are not provided by v2.45.0, by
an assumed 2.71 client, or by this read.

- Acquisition stays with the caller. `--read-current` GETs the retained slot
  once. It does not call a provider or collect a report.
- Cadence is not `--max-age`. On this read the age must be greater than zero
  and at most 30 minutes, and that ceiling is checked before connect. It is
  not a schedule. The local formatter's 24 hour ceiling is unchanged.
- Account attribution is unchanged. Publisher, incarnation, and generation
  select a source. They do not name a provider account or an entitlement.
  `providerUpdatedAt` must be null. The command does not infer a served model,
  account usage, or a renewed reset.

Operator flags, the ConfigMap, RBAC, the portal mount, and runtime activation
remain separate and were not changed.
