# DESIGN-020: explicit single-request directory client

Contract ratified 2026-10-11 UTC; parent #174, prerequisite for the next #173
fixed rule probes. D17's operator/API directory exists, but the shipped CLI has
no directory dispatcher. An unknown command can reach session creation. The
existing generic GET client also retries failures. Do not invoke an invented
command or use credentials/curl/another identity to bypass this missing seam.

## Bounded source change

Add `agent-run directory list --project dev-env --limit 20 --json --once`.
`-o json` is also accepted. Require explicit `--once`, project, limit 1–20 and a
bounded opaque continuation cursor. Reject unknown subcommands, positional
arguments and invalid options before authentication or an API request. Route
this command explicitly before the existing create fallback.

Use ordinary caller authentication, TLS and the existing query-capable client
with retry disabled. Clone only this command's HTTP client to refuse redirects.
Issue exactly one GET to `/v1/session-directory`; never POST, follow a redirect,
automatically retry, page automatically, create a Session or read a provider
HOME. Existing other client retry behavior remains unchanged. This operation
does not grant payload, claims, inbox or lifecycle authority.

Require HTTP 200 and strictly decode the existing standard-library page DTO.
Reject missing list/resource-version structure, excessive items, mismatched
project or malformed metadata. Preserve nullable times and the opaque returned
continuation. Print the validated original JSON page, preserving present null values, so
callers can distinguish a complete
page from remaining data. A returned continuation is not an empty directory.
The operator retains authoritative project scope and fresh authorization.

Affected source: `internal/agentrun/agentrun.go`, new `directory.go` and
`directory_test.go`. Root-owned help and workflow/HANDOFF/saga documentation
complete the callable contract. No schema, dependency, controller, client-auth,
image/profile or GitOps authority change. The existing client implementation
stays byte-exact; reuse its no-retry `callWith` seam.

## Verification and runtime acceptance

Meaningful focused tests prove safe routing/pre-auth refusal, exact GET and
query escaping, typed pagination/nullable times, one request on HTTP 503,
redirect refusal, and unchanged retries for existing GET callers. Run one
bounded standard-library CLI test/lint check at low parallelism under nice19;
no envtest startup, schema DNS retry, local image build, broad test loop or CPU
stress. Required hosted CI and independent native review remain merge gates.
Claude reviews are optional while unavailable; read any available findings.

After merge, a separately bound cached client build may be used. Source tests
and compilation do not establish a deployed or authorized directory read.
Before EACH first fixed canary creation, save one fresh authorized project-scoped
page with limit20, exact source/client/intent binding and its complete raw reply.
A nonempty continuation, error, foreign/ambiguous record or incomplete reply
holds creation. Report unrelated validated metadata separately. An existing
exact original record permits reporting/resume only; it never authorizes a new
Session, replacement UID, rearm or fresh attempt. Directory cardinality remains
Unknown until this read succeeds. Keep all earlier observer refusals/counters.

No provider attempt, global private admission, enrollment or fleet UX is
included. Q-24 remains pending. The actual both-provider loaded-rule gate and
the concurrent owner pilot remain unaccepted.

The first focused verification found DIRECTORY-CLI-R1: reencoding the typed DTO
omits a present null report timestamp. Preserve the failed source/tests and raw
fixture. Correct only output to use the already strictly validated raw JSON
page, retaining field presence/nulls without changing API/schema behavior.
Existing 502/504 exit mapping stays intact; test expectations must match it.
One bounded corrected focused verification is authorized; no runtime read.

Corrected focused verification passed on the same two-package command at low
parallelism (1.19s); first affected cached lint passed (0.99s). Packet
`925a5b936f8d10d249e8a86f396041e29b8a9bf7195fc4f9a54bc3ac9482ce19`
preserves the original HOLD and both source/test revisions. Present-null output,
invalid UTF-8 refusal, one-request failure, redirect refusal and existing retry
controls passed. No TestMain, schema-host retry, runtime API or provider call
occurred. Final integrated-head native review and required CI remain gates.
A bound client build and actual scoped read remain separate runtime acceptance.
