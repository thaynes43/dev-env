package agentd

// runCursorAnswerTask is an unused connection from one bound task to the
// answer file. It has no production caller and does not start the helper.

import (
	"context"
	"errors"
	"os"
)

var errCursorAnswerTask = errors.New("answer-task-refused")

// runCursorAnswerTask requires the caller to inject start. It appends the
// existing answer-file option to a copy of the generated arguments, reuses
// runCursorBoundTask, and opens the artifact only after that result is complete.
func runCursorAnswerTask(ctx context.Context, task cursorBoundTask, current func() (cursorTaskContext, error), decisions *os.File, start cursorScaffoldStart) (cursorBoundAnswer, error) {
	var zero cursorBoundAnswer
	if ctx == nil || current == nil || decisions == nil || start == nil {
		return zero, errCursorAnswerTask
	}
	if ctx.Err() != nil {
		return zero, errCursorTaskBinding
	}
	name, err := cursorAnswerBasename(task.binding)
	if err != nil {
		return zero, err
	}
	completed, err := runCursorBoundTask(ctx, task, current, decisions, func(command cursorScaffoldCommand) (cursorScaffoldPeer, error) {
		next := command
		next.args = append(append([]string{}, command.args...), "--assistant-answer-file", name)
		return start(next)
	})
	if err != nil {
		return zero, err
	}
	return acceptCursorAnswerFile(ctx, task, completed, nil, current)
}
