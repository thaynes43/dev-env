package agentd

// acceptCursorBoundAnswer is an unexported, unused join of one completed
// cursorBoundTaskResult and one assistant-answer-v1 document. Production
// dispatch, provider registration, and launch code do not call it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

// cursorBoundAnswer carries the bound helper result and the decoded answer
// only when every check succeeded. requestedModel stays the helper's requested
// label. servedModel stays the helper's Unknown. Text is not a served-model claim.
type cursorBoundAnswer struct {
	bound  cursorBoundTaskResult
	answer answerResult
}

var (
	errBoundAnswerRun     = errors.New("bound-answer-run")
	errBoundAnswerHelper  = errors.New("bound-answer-helper")
	errBoundAnswerContext = errors.New("bound-answer-context")
)

// acceptCursorBoundAnswer returns answer text only for a complete helper
// result whose binding still matches a trusted current context both before
// the read and after decoding. Any failure returns a zero result. Errors are
// fixed classes and do not include callback text, the document, or the answer.
// The two observations are not a lease. A blocked reader is not interrupted.
func acceptCursorBoundAnswer(ctx context.Context, task cursorBoundTask, completed cursorBoundTaskResult, runErr error, current func() (cursorTaskContext, error), answer io.Reader) (cursorBoundAnswer, error) {
	var zero cursorBoundAnswer
	if runErr != nil {
		return zero, errBoundAnswerRun
	}
	if !cursorBoundHelperReady(task, completed) {
		return zero, errBoundAnswerHelper
	}
	if err := confirmCursorBound(ctx, task, current); err != nil {
		return zero, err
	}
	decoded, decErr := decodeAssistantAnswer(answer, answerExpectation{
		SessionID:      completed.helper.sessionID,
		PromptSHA256:   completed.helper.promptSHA256,
		ModelRequested: completed.helper.requestedModel,
	})
	if err := confirmCursorBound(ctx, task, current); err != nil {
		return zero, err
	}
	if decErr != nil {
		return zero, decErr
	}
	return cursorBoundAnswer{bound: completed, answer: decoded}, nil
}

func cursorBoundHelperReady(task cursorBoundTask, completed cursorBoundTaskResult) bool {
	helper := completed.helper
	if !helper.promptComplete || helper.stopReason != answerStopEndTurn || helper.servedModel != answerServedNone {
		return false
	}
	if !cursorScaffoldLabel.MatchString(helper.sessionID) {
		return false
	}
	if helper.requestedModel == "" || helper.requestedModel != task.plan.model || helper.requestedModel != task.binding.requestedModel {
		return false
	}
	if helper.promptSHA256 == "" || helper.promptSHA256 != cursorBoundPromptHash(task.plan.prompt) {
		return false
	}
	return completed.binding == task.binding
}

func cursorBoundPromptHash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

func confirmCursorBound(ctx context.Context, task cursorBoundTask, current func() (cursorTaskContext, error)) error {
	if ctx == nil || current == nil || ctx.Err() != nil {
		return errBoundAnswerContext
	}
	observed, err := current()
	if err != nil || ctx.Err() != nil {
		return errBoundAnswerContext
	}
	binding, err := bindCursorTask(task.plan, observed)
	if err != nil || binding != task.binding {
		return errCursorTaskBinding
	}
	return nil
}
