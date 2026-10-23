# workflow_handoff

Offline checker for one coordinator handoff record. It validates the document, replays its events, and prints the recorded state plus the next manual action.

It does not dispatch, poll, grant authority, transfer TaskClaim, call a provider, or write tracker state. Stdlib only. One document, at most 128KiB, at most 48 events.

```bash
GOTOOLCHAIN=local CGO_ENABLED=0 go run . testdata/e-merge.json
go run . - < testdata/d-deadline.json
```

Exit 0 means the record reduced. Exit 2 means the input was refused. Exit 3 means a conflict needs manual disposition. The procedure and the JSON template are in `docs/coordinator-result-handoff.md`.

Fixtures in `testdata/` are synthetic. They do not prove a live subscriber, a deployed policy, or a native review.
