# Ready-pool protocol cause verification (2026-11-03)

Disjoint reserved saga note. Links are relative to this verification path.

## Problem

Generic `native_protocol_refused` conflated parser refusal, a missing thread latch, and a missing terminal latch. The parser error was also dropped on the waitDone/readDone race: `perr` was declared in the `parseFailed` arm, so a winning `waitDone` left it unset.

## Behavior

See [runagent.go](../../../../internal/agentd/runagent.go) and [ready_pool_protocol_test.go](../../../../internal/agentd/ready_pool_protocol_test.go).

`perr` now outlives the select. After `waitDone`, a non-blocking read of `parseFailed` keeps a parser error that arrived on that race. The existing Codex gate now also trips when `perr != nil`.

Subtype stays `native_protocol_refused`, `IsError` stays true, and a zero exit becomes 1. Nonzero exits, including 143, stay unchanged. Strict parser, privateTask, auth, and resume behavior are unchanged.

One diagnostic line is added. It prints a single fixed cause and does not copy the raw stream or error payload:

`[agentd] native Codex protocol cause: <cause>`

`<cause>` is exactly one of `missing_thread`, `missing_terminal`, `second_terminal`, `parser_refused`.

- `perr == nil` and no thread latch: `missing_thread`
- `perr == nil`, thread set, no terminal latch: `missing_terminal`
- `perr != nil`, terminal latch set, and `errors.Is` matches `errSecondCodexTerminal` or `errInvalidCodexCompletion`: `second_terminal`
- otherwise, including a first invalid completion and unknown-after-success: `parser_refused`

`second_terminal` requires the actual terminal latch plus sentinel identity (`errors.Is` on `errSecondCodexTerminal` or `errInvalidCodexCompletion`). The `Error()` strings `invalid native Codex completion` and `native Codex emitted a second terminal event` are display words, not the classification contract. A first invalid completion leaves the terminal latch unset and stays `parser_refused`. Any other parser error remains `parser_refused`.

This only adds a cause line on failures that already fail. It is not a default-off feature, not an extra controller, and it does not auto-replay, restart, or grant authority.

## Tested limits

Six synthetic fakeCLI process cases were RED on unchanged code, then GREEN in 0.096s against the existing strict stdout and fail-closed baseline. That result is source-test evidence only. It is not installed runtime or provider acceptance, and it is not proof of the cause of a historical 143 failure. Source packaging, image, and adoption belong to the normal existing owner. No release or runtime state is claimed.

No new ADR, policy, or speculative architecture.
