# OP321 CLI to question-audit trace, 2026-11-02

This source test connects the existing CLI question/reply/ACK flow to the
portal question-audit projection on the same server handler and store.
Production code, flags, authorization and configuration are unchanged.

The earlier CLI round-trip and portal-state tests exercised those paths
separately. `TestInboxCLIQuestionAuditSameStore` now uses the actual
`agentrun.Run` over synthetic TLS for ask, read, reply, read and ACK. After
each transition it compares the audit GET with the CLI receipts and thread
view: exact message names/UIDs, actors, sender/recipient, timestamps,
question/reply references and waiting-on owner.

The state progresses from Unanswered to Answered to Acknowledged. Reading a
question or reply does not acknowledge it. The audit's recorded ACK must
match the recipient's explicit API ACK, including its actor and timestamp.
Wrong-UID reads and portal POSTs cannot authorize an acknowledgement or
expose message text. `TestInboxCLIQuestionAuditDefaultOff` exercises the same
CLI flow while the audit route remains unavailable.

Both envelope and items preserve origin Unknown, approval not recorded,
provider consumption not recorded and served model Unknown. The existing
allowlist and explicit checks exclude question/reply text, keys, prompts,
history, provider errors and requested-model fields.

## Validation

Grok authored the two tests. The coordinator verified the frozen artifact,
reviewed the complete source independently, and integrated the test unchanged.
The qualified Go 1.27.3 runner executed these commands serially, under the
existing lock with two CPUs and one package:

```sh
go test -p 1 -race -count=1 -timeout 120s -v \
  -run '^TestInboxCLIQuestionAudit(SameStore|DefaultOff)$' ./internal/apiserver
go vet -p 1 ./internal/apiserver
```

Both named tests passed at 01:35:10 UTC (package 1.796s); vet passed at
01:35:23 UTC. The tested file SHA256 is
`ee758d477fe99e494f475847d876390c5b9ed8168feb0f75361cf927b4576d77`.
The coordinator's local pinned gofmt check also passed. Hosted CI and
advisory review are separate checks.

## Acceptance limits

The source pin is `4fb35ee0b52c9e3124cbdaa608caef61a3a259e2`;
integration also includes the independent OP323 test-only commit #837.
All tokens, identities, messages and TLS assets are synthetic. No live inbox,
provider, account, native prompt, cluster or installed UI was exercised.
These tests do not establish provider understanding, a cross-provider
installation, Pages or Shepherd acceptance. Existing human-decision,
observer and runtime-activation holds remain.
