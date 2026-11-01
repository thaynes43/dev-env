# Driver qualification — 2026-11-01 16:14 UTC

The current delivery coordinator imported the frozen Grok patch without changing its two source/test files. Their SHA-256 values matched the worker handoff before and after qualification. On Go 1.27.3, the focused `TestQuestionAudit` portal suite passed with `-race -p 1 -count=1` in 1.324 s. Full `go vet -p 1` passed for `internal/apiserver` and `web/portal/session`; `gofmt -l` reported no files. Both commands ran locally with GOMAXPROCS=2, nice 19, the existing shared lock and cache. Independent native review passed the frozen source. Required final-head hosted CI remains pending. No runtime feature or permission was activated.

All sections below are the original Grok worker record from before these driver checks; its no-test and no-commit statements describe that earlier context only.

---

# OP321 CI lint and race correction — 2026-11-01

Writer: worktree `/home/dev/work/op321-ci-grok-1101-154146`, branch `agent/op321-ci-grok-1101-154146`, stated base `acf6d0baddb43581de53ae7c07d0deb9cece3019`. This note records a source correction for the retained PR 820 lint and portal-gate race. It does not record a test run, a commit, or acceptance.

## Diagnosis

Retained `OP321-123940-LINT-FAILED.log` reports four issues on the PR 820 merge checkout:

- `web/portal/session/product_question_audit_test.go` copylocks: the panel-failure table stores `questionAuditScript` by value, and that type contains `sync.Mutex`. Ranging the table and assigning `script := tc.script` copy the lock.
- `internal/apiserver/portal_question_audit.go` QF1008 at the acknowledgement time and the item `CreatedAt`: `.Time.UTC()` repeats the embedded `time.Time` selector.

Retained `OP321-123940-RACE-FAILED.log` is the hosted Portal PostgreSQL gate. Postgres became healthy. The job then failed in `TestQuestionAuditPanelFailuresPreserveInventory/timeout`: the test read `script.queries` while `ServeHTTP` appended that slice. The hung handler records the query and then waits on the request context, so the client timeout returns without the response write that orders the other cases. Expected negative TLS lines in the postgres log, including `pg_hba.conf` rejects for database `postgres`, are not the failure. The neutral 404 wording already in the base is unchanged.

## Edits

- `internal/apiserver/portal_question_audit.go` — acknowledgement time and item `CreatedAt` call `UTC()` on `CreationTimestamp`. The instant is the same. The zero check and `.Time.Before` comparison stay, because `metav1.Time.Before` is a different method.
- `web/portal/session/product_question_audit_test.go` — the panel-failure table holds `*questionAuditScript`, so ranging and assigning the case do not copy the mutex. Every observation of recorded queries uses `recordedQueries`, which copies the slice under the existing mutex. Timeout, parent cancellation, call counts, and the neutral 404 assertions are unchanged.

## Preserved

Portal question audit stays default-off. No operator flag, route registration, ACL, RBAC, credential, or production UI activation is changed. `questionAuditTimeout` remains 30ms for the timeout case and 5s for parent cancellation. `sessionReadTimeout` remains 12s. Missing copy stays the neutral not-found wording.

## Not claimed

This writer could not execute `go test -race`, `go vet`, or golangci-lint: the session rejected command execution. File hashes and `git rev-parse` were not taken. No live or hosted acceptance is claimed. The PostgreSQL gate was not re-run.
