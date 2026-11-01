# OP323 publisher compatibility, 2026-11-01

Source pin: `11f0152d534e6c33fca6bd87b4af3f2d2e1f3b84` (v2.84.1).
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
