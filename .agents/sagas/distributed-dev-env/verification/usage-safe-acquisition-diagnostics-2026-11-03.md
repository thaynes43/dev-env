# Scheduled acquisition diagnostics — source verification

The installed collector can report a successful Codex acquisition alongside
generic Claude/Cursor acquisition failures. A finite-command exit correction
passed its source tests and was installed, but did not resolve those live
failures. Neither authentication failure nor quota exhaustion is established by
the generic result. Another manual acquisition is not the diagnostic mechanism.

The already scheduled helper now emits a separate bounded stderr side channel
for known failure stages. It contains only the provider enum, a closed stage and
reason, and an optional strictly shaped public software version. Vendor stderr
remains discarded. Raw output, error strings, account identities, paths, URLs,
credentials and history never become diagnostic fields. Unknown, oversized,
incomplete or additional frames are dropped without echoing their bytes.

The operator logs only decoded fields, labelled as acquisition diagnostics and
correlated with the existing cycle number. The machine stdout receipt, request,
private ledger and journal schemas are unchanged. Older helpers may emit no
diagnostics; older operators discard the side channel. No new command, retry,
configuration, permission, account binding, version enrollment, provider clock,
cadence, backoff or expiry is introduced. Cleanup uncertainty still stops later
readers and retains the existing status-only recovery fence.

A signed-out reason requires an explicit `loggedIn: false`; a missing identity
field remains unqualified metadata. An unsupported version can be reported but
is still refused. Diagnostics do not authorize a login or relax a guard.

Adversarial tests cover unknown fields/codes/providers, duplicate and aliased
keys, malformed/truncated/oversized or extra lines, arbitrary error text, and
private text in a version field. Transport tests prove that only decoded fields
reach logs and that machine output stays unchanged. One test exposed promoted
`bytes.Buffer` methods bypassing the writer limit; making the buffer private
keeps optimized string/copy paths within the same bound. Tests also retain strict
receipt compatibility and the cleanup stop.

Both `internal/usagecollector` and `internal/allowancecollector` race suites
passed with two CPUs and one package at a time (2.271s and 2.248s). These are source
and synthetic transport results, not live failure localization or successful
three-provider recurrence. Publication, reviewed installation and diagnostics
from a real scheduled cycle remain separate evidence.
