# Dot's front-door catalog and runbook

Use the existing MCP bearer at `/mcp`. Read the schemas from `tools/list`.
Confirm separate authorization for each write, dispatch, or stop.

## Tools

| Tool | Action |
| --- | --- |
| `backlog_find` | Search the tracker. |
| `backlog_show` | Read one work item. |
| `backlog_create` | Create an authorized work item. |
| `backlog_groom` | Edit authorized scope, acceptance or constraints. |
| `backlog_order` | Set authorized priority. |
| `backlog_close` | Close with evidence through tracker guards. |
| `coordinator_start` | Start an approved coordinator. |
| `session_status` | Read a caller-owned session. |
| `session_stop` | Reap a caller-owned session with separate authorization. |
| `waiting_on_tom` | Read held tracker items and report Asks availability. |
| `standing_authority` | Read the installed authority text. |
| `parent_waiting` | List pending parent notes. |
| `parent_read` | Read an in-scope parent note. |
| `parent_answer` | Answer an in-scope parent note. |
| `parent_send` | Send an authorized parent note. |

## Resources

| URI | Content |
| --- | --- |
| `dev-env://catalog/v1` | This page. |
| `dev-env://operating-rules/v1` | Versioned operating rules and digest. |
| `dev-env://handoff/v1` | Work-item handoff lookup instructions. |
| `dev-env://handoff/v1/{item}` | Current handoff for one item in the configured project. |
