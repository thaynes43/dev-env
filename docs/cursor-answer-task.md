# Answer task connection

`runCursorAnswerTask` in `internal/agentd/cursor_answer_task_linux.go` connects one bound task to the existing answer file opener. It is unexported, Linux-only, and has no production caller. The scaffold, binding, decoder, join, and file opener stay unchanged.

The caller injects `start`. A nil context, current callback, decision descriptor, or start returns `answer-task-refused` and does not call the runner. A context that is already canceled returns the existing binding error and does not start. There is no default helper start and no constructed command execution.

On a copied argument list, the function appends `--assistant-answer-file` and the basename from `cursorAnswerBasename`. The command name, environment, working directory, private prompt file, and decision descriptor stay as the frozen runner generated them. The frozen plan command itself is unchanged and still omits the answer option.

A successful `runCursorBoundTask` result is passed to `acceptCursorAnswerFile` with the same context and current callback. Any failed, incomplete, canceled, stale, replayed, mismatched, or uncertain result returns a zero answer. The existing error class is returned. Callback text, paths, prompts, and artifact bytes are not added to it. The requested model remains the frozen plan's `grok-4.7-xhigh`. The served model remains `Unknown`.

The invocation fence stays in the state root. This function does not remove it, retry after uncertainty, or delete the answer artifact. The opener does not establish writer ownership. The two context observations are not an atomic lifecycle lease. The file checks are not atomic filesystem proof. A stalled read is not interrupted. This is not a provider or live readiness claim.
