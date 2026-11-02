# Claude --local on a declared project

`agent-run --project sample --local` is allowed for Claude, both by default and with explicit `--agent claude`.

The server and daemon already admit an ordinary fresh non-budget Claude local project session. The CLI change is only the project guard, left in its current place among the other checks.

Still refused: `--project` with rescue restore; `--project` with `--interactive`; `--project --local` for any agent other than Claude, including Codex; and `--local` with a prompt, `--timeout`, `--max-turns`, `--once`, `--dry-run=server`, `--coordinator-instructions`, or `--coordinator-instruction-publisher`.

Server admission, auth, and budget boundaries stay as they are. No API, schema, or image change is involved. A local session uses the normal idle and rescue lifecycle. New model context and source pin were already supported. This is not a retention measurement or a persistent-operations proof.
