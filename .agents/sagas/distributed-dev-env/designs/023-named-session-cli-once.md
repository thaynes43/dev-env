# DESIGN-023: one named Session read before fixed result retrieval

## Contract

The fixed result reader requires a named context bound to the original Session
UID and executor Pod UID. The existing authorized Session GET supplies retained
executor provenance, but `agent-run show` retries and follows redirects. No fixed
provider probe starts until the initiating ordinary client has a bounded route
for this read.

Add explicit `agent-run show <original-name> --json --once`. Require exactly one
valid DNS-label name and explicit JSON/once options before connection or auth.
Clone the existing HTTP client with redirects refused and reuse `callWith` with
retry disabled for one GET of the existing Session endpoint. Require HTTP200 and
the exact requested name. Enforce the response limit on the HTTP body before
JSON decoding removes whitespace; a truncated valid prefix must be refused.
Return the validated raw JSON so omission, optional
null, zero and decimal strings preserve the server's meaning. Ordinary `show`
behavior and its existing kube-context selector remain unchanged.

Validate bounded UTF-8 JSON against the current standard-library Session DTO's
canonical exported JSON tags before typed decoding: reject duplicate keys,
case aliases, unknown fields, trailing values, null scalars and invalid nested
types. Support the DTO's booleans, slices, pointers and typed decimal strings;
skip unexported implementation fields. `UsageObservation.UnmarshalJSON` is
deliberately tolerant for heartbeat liveness, so typed decode alone cannot prove
the once response is valid. Reuse existing strict parsing helpers where their
contracts fit; preserve D22 result validation and the stdlib-only client boundary.

This is a client-only slice: no API, ACL, schema, authority, provider, credential,
enrollment, task-template or operator argument change. No new result discovery,
publication list, polling, logs, exec, HOME/PVC access or rearm route is added.

Affected paths are `internal/agentrun/{sessions.go,show_once.go,show_once_test.go,
help.go,client.go,creds.go}`, this contract and `docs/workflow-guide.md`. The
transport option defaults off and is enabled only on the once connection clone.
Independent review `afc20186` held the first candidate for its post-decode size
check; the original frozen checks remain retained alongside the correction.

## Acceptance

Meaningful focused tests must prove invalid arguments fail before auth, one GET
without retry or redirect, exact-name/HTTP-status refusal and strict nested wire
validation, including the tolerant usage decoder trap and an actual HTTP response
whose padded valid prefix reaches the byte limit before trailing data. Validate raw JSON
preservation and ordinary-show compatibility. Use low parallelism and limits;
no stress, CPU burners, wide or looped tests, or local image build.

Required CI, independent native review, squash merge, release and a client bound
to actual merged source are separate shipping gates. Claude review is optional
during the outage; read available findings without retrying the service. This
client slice does not require another operator rollout because the Session GET
already exists. Runtime requests remain unexecuted during source validation.

After installed-image and client acceptance, one separately reviewed named read
must match the original HTTP201 UID/name and fixed parent/project/task identity,
then retained `privateHistory.sessionUID` and `lastExecutor.podUID/podName`.
Missing, stale or mismatched provenance remains Unknown. Derive ordinal-zero
capture identity using the existing Session UID plus NUL plus executor Pod UID
hash and existing context naming helpers; then one D22 result GET reads that
exact publication. A stored bounded excerpt does not establish private delivery
acknowledgement, all chunks, full native history or billing.

The finite source audit is
`/home/dev/work/orders/dev-env-rule-canary-runtime-1011/review/named-context-discovery-source-audit-r1.json`
(`7918f0c4`), on merged D22 source `cd966d17`. Both fixed provider IDs remain
unstarted. Private admission is disabled; Q-24 and the owner pilot remain open.


## Shared walker amendment — 2026-10-12

The strict wire walk is now generic over the DTO type and also guards
`canary create` (Session) and `directory list` (SessionDirectoryList), both of
which also enable the body fence (#174, history-05). Its rules are unchanged;
the directory's reported time adds one wire form: null or an RFC 3339 string.
A one-shot read that reached the API and got a lost answer, a stop or a 5xx
exits 6 (unconfirmed), never 5; a 429 exits 1 (history-06). It exits 5 only when
nothing was sent (D-86).
