# Model pre-flight streams

Each file is the stdout of one real pre-flight turn of Claude Code 2.1.321,
captured on 2026-11-13 for story 791 fix round 1 (coordinator rulings in
PR 1120 comment 6843361182). Each ran from an empty directory with
`reply with ok` on stdin and the pre-flight's flags, `--setting-sources ""`
included where the table says so:

```
claude --model <m> --tools "" [--setting-sources ""] --strict-mcp-config \
  --no-session-persistence --max-turns 1 --output-format stream-json --verbose -p
```

| File | Model asked | Environment | Exit | Captured by |
|---|---|---|---|---|
| `answer.jsonl` | `claude-opus-5-5` | a pod with a login in reach, with `--setting-sources ""` | 0 | coordinator session `dev-env-1111-143337` |
| `refused-model.jsonl` | `claude-nonexistent-0-0` | the same, with `--setting-sources ""` | 1 | coordinator session `dev-env-1111-143337` |
| `login-missing.jsonl` | `claude-opus-5-5` | a session pod's tool shell, which has no login in reach of a child `claude` | 1 | worker session `dev-env-1113-035641` (twice, with and without `--setting-sources ""`: the same stream) |
| `login-rejected.jsonl` | `claude-opus-5-5` | `env -i`, an empty throwaway `HOME` and a made-up `CLAUDE_CODE_OAUTH_TOKEN` | 1 | worker session `dev-env-1113-035641` (without `--setting-sources`) |

Only the entries and fields a classifier could read are kept. Every
`session_id`, `uuid`, `request_id`, message id, path and timing is dropped.
Non-assistant entries keep only their type, subtype and `is_error`. Nothing
in these files is a token or an account detail.

What they show:

- The stream spells the API-error flag `is_api_error_message`, and the
  status `api_error_status` (a number). The transcript's `isApiErrorMessage`
  and `apiErrorStatus` do not appear. The top-level `error` is the CLI's
  error code: `authentication_failed`, `model_not_found`.
- A missing login and a rejected token are both `authentication_failed`; only
  the rejected token carries `api_error_status` 401. Neither text starts with
  "API Error".
- An answer's `message.model` is exactly the asked id, with no date suffix,
  and carries non-zero usage. It has no `error` or flag at all.

No quota refusal could be captured. Its cases in `preflight_test.go` are
built from the CLI's own strings and marked "not verified against a real
stream".
