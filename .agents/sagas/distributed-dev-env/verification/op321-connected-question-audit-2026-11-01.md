# OP321 connected question audit — focused repair, 2026-11-01

Writer: exclusive OP321 worktree `/home/dev/work/op321-question-audit-grok-1101-113208`, base `a6b31d6dcebb1cf9901b3ccef7574c200e5cf666`. No competing source writer was used. This writer did not run tests, commit, push, or open a PR. The parent runs the qualified serial Go 1.27.3 suite.

## Diagnosis

`TestPortalQuestionAuditOnePageOmitsPlain` asked `ListSent` for `limit=1` after two questions existed. `KubeStore.ListSent` still refuses when `Live.List` returns more objects than `limit` or a continuation longer than 4096 bytes. The fixture client returns every label match and ignores `Limit` and `Continue`, so the store returned `ErrUnavailable` and the route returned 503. That refusal stays. The test now pages with the same slice-and-continue contract as `pagedFake` in `internal/inbox/fixround_test.go`.

`TestQuestionAuditSelectedReadsTwoBoxes` rejected `claude-opus-5-5` in the whole rendered session page. Session detail still shows the requested model from the activity fixture. The audit section and the audit route do not.

`attachQuestionAudit` used `context.Background` plus `questionAuditTimeout`, and `read` checked `ctx.Err` after the panel returned. A parent deadline that fired during that detached wait failed the whole read and dropped inventory that had already succeeded. History still fails the read. The panel now uses the request context, so cancellation stops the reads, and a deadline during the panel leaves the inventory page in place. `sessionReadTimeout` is still 12s.

## Edits

- `internal/apiserver/portal_question_audit_test.go` — one-page test publishes two questions and one plain message, pages at limit 1, requires one store list per request, passes the store continuation through, and requires the plain object to be an empty truncated page. Envelope and item keys are allowlisted. Text, idempotency key, prompt, history, requested model, and served-model list keys are refused. `ListSent` bounds are unchanged. Default limit constant remains 20.
- `web/portal/session/product_question_audit_test.go` — full page must still show the requested model and must omit the cursor, question text, and raw GitHub identity. Those secrets, plus `claude-opus-5-5` and requested-model wording, are refused inside the question-audit panel. The panel must render served model `Unknown`. `TestQuestionAuditCatalogProjectIsNotRepoAlias` sets the selected row repo and `ProductFilter.Project` to `alias` and `UseQuestionAudit` to `sample`, and requires the audit query project to be `sample`. The existing panel-failure table now cancels the parent while the panel is in flight and requires the group, count, and card to survive.
- `web/portal/session/product_question_audit.go` — panel context is `context.WithTimeout(ctx, questionAuditTimeout)`.
- `web/portal/session/product.go` — history deadline is still checked before the panel. The panel result is returned with the inventory page.

## Preserved

DESIGN-014 wire validation, exact `InReplyTo` and acknowledgement identity, source reuse, and the omission of plain text, idempotency keys, and history. Portal question audit and the question-audit project file stay default-off. No operator flag, route registration, or production UI activation is changed here. Store limit and continue refusal is unchanged.

## Not claimed

This writer did not execute the Go suite, envtest, or a browser. Production UI and route activation are not claimed. Parent runs the qualified serial tests under the existing lock, cache, `nice -n 19`, `GOMAXPROCS=2`, and `-p 1`.

## File scope

- `internal/apiserver/portal_question_audit_test.go`
- `web/portal/session/product.go`
- `web/portal/session/product_question_audit.go`
- `web/portal/session/product_question_audit_test.go`
- `verification/op321-connected-question-audit-2026-11-01.md`
