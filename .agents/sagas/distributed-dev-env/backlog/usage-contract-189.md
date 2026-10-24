# Story189 Packet A: provider allowance source contract

**Status:** source slice only, 2026-10-24. Epic187 is not accepted. Packets B–F
in the Story189 sequence stay held. Native188 stays in review.

**Depends on:** the reviewed Story189 schemas, nine observation examples, read
envelope, and Mac Draft 2020-12 format check. Those files stay outside this
repo. Testdata copies are hashed in package tests.

## Included

- `internal/providerallowance` types, strict decode, caller-supplied registry, and derivation checks
- Sanitized fixtures and Draft 2020-12 schema tests with format assertion
- DESIGN-030, this note, the saga decision-log pointer, and one CLAUDE.md Layout line

## Excluded

Collector storage, HTTP servers, agent-run commands, operator and portal
changes, provider adapters, quota polls, UsageVersion2, Story109 rollups, and
any deployment. The seventeen transition fixtures are not collector results.

## Acceptance for the source PR

Focused `go test -p 1 ./internal/providerallowance` covers the nine observation
roundtrips, the identical HTTP and CLI read document, trusted-context
positives and negatives, and schema positives and negatives. `make check-layout`
and `git diff --check` pass. Hosted `CI - Success` and independent native review
remain required before merge. Merge is not part of this slice.

## Next

Sol reviews the exact PR head, collects findings, and returns consolidated
repairs to the same implementation owner when a repair is authorized. Repair
01 on this PR corrects canonical field names, fail-closed measure
actionability, and typed shape checks. Repair 02 corrects alias sums,
schema-invalid nulls, projection ambiguity, the over-limit lower bound,
inventory promotion, availability conflict, and trusted entitlement bindings.
Fresh native review and current CI remain required. Packet B waits for its
own activation.
