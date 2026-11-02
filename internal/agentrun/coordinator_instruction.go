package agentrun

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

var instructionKeyPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,118}[a-z0-9])?$`)
var instructionAttemptPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var instructionNoncePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var instructionNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

func (a *app) instruction(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return a.help([]string{"instruction"})
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "send", "read", "consume", "attempt", "checkpoint":
	default:
		return usageError("instruction requires send, read, consume, checkpoint or attempt; see agent-run help instruction")
	}
	var session, key, project, repository, taskKey, expectUID, expectPod, attempt, nonce, messageUID, textSHA string
	var ended bool
	cmd := newCommand("instruction", outputJSON)
	cmd.fs.StringVar(&session, "session", "", "")
	cmd.fs.StringVar(&key, "key", "", "")
	cmd.fs.StringVar(&project, "project", "", "")
	cmd.fs.StringVar(&repository, "repository", "", "")
	cmd.fs.StringVar(&taskKey, "task-key", "", "")
	cmd.fs.StringVar(&expectUID, "expect-uid", "", "")
	cmd.fs.StringVar(&expectPod, "expect-pod", "", "")
	cmd.fs.StringVar(&attempt, "attempt", "", "")
	cmd.fs.StringVar(&nonce, "nonce", "", "")
	cmd.fs.StringVar(&messageUID, "message-uid", "", "")
	cmd.fs.StringVar(&textSHA, "text-sha256", "", "")
	cmd.fs.BoolVar(&ended, "ended", false, "")
	pos, err := cmd.parse(a, rest)
	if err != nil {
		return err
	}
	if len(pos) != 0 || !instructionNamePattern.MatchString(session) {
		return usageError("instruction %s requires --session; see agent-run help instruction", verb)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	switch verb {
	case "attempt":
		if !instructionAttemptPattern.MatchString(attempt) || key != "" || project != "" || nonce != "" {
			return usageError("instruction attempt requires --attempt and no key, project or nonce")
		}
		var got apiv1.CoordinatorInstructionAttemptResult
		status, callErr := a.callWith(ctx, c, http.MethodPost, apiv1.SessionInstructionAttemptPath(session), nil, apiv1.CoordinatorInstructionAttemptRequest{AttemptID: attempt, Ended: ended}, &got, false)
		if callErr != nil {
			return callErr
		}
		if status != http.StatusOK || got.AttemptID != attempt || got.SessionUID == "" || got.PodUID == "" || (ended && got.Phase != apiv1.InstructionPhaseEnded) || (!ended && got.Phase != apiv1.InstructionPhaseActive) {
			return fail(ExitUnconfirmed, "instruction attempt returned an unconfirmed binding; retry the same attempt")
		}
		return a.printJSON(got)
	case "checkpoint":
		if key != "" || project != "" || repository != "" || taskKey != "" || attempt != "" || nonce != "" || messageUID != "" || textSHA != "" || expectUID != "" || expectPod != "" || ended {
			return usageError("instruction checkpoint requires --session and no key")
		}
		var got apiv1.CoordinatorInstructionCheckpoint
		status, callErr := a.callWith(ctx, c, http.MethodGet, apiv1.SessionInstructionsPath(session), nil, nil, &got, false)
		if callErr != nil {
			return callErr
		}
		if status != http.StatusOK || got.Outcome != apiv1.InstructionOutcomeConfirmed || !instructionAttemptPattern.MatchString(got.AttemptID) || got.SessionUID == "" || got.PodUID == "" || len(got.Pending) > 4 {
			return fail(ExitUnconfirmed, "instruction checkpoint returned an unconfirmed list; retry the same attempt")
		}
		seen := map[string]bool{}
		for _, item := range got.Pending {
			if seen[item.Key] || item.Consumed || !instructionKeyPattern.MatchString(item.Key) || item.SenderUID == "" || !validInstructionMessageUID(item.MessageUID) || !instructionNoncePattern.MatchString(item.TextSHA256) || !instructionNoncePattern.MatchString(item.Nonce) || item.Text == "" || item.Project == "" || item.Repository == "" || item.TaskKey == "" || item.AttemptID != got.AttemptID || item.TargetSessionUID != got.SessionUID || item.TargetPodUID != got.PodUID || item.Outcome != apiv1.InstructionOutcomeConfirmed {
				return fail(ExitUnconfirmed, "instruction checkpoint returned an unconfirmed correction; nothing was consumed")
			}
			seen[item.Key] = true
		}
		return a.printJSON(got)
	case "read":
		if !instructionKeyPattern.MatchString(key) {
			return usageError("instruction read requires --key")
		}
		var got instructionRead
		status, callErr := a.callWith(ctx, c, http.MethodGet, apiv1.SessionInstructionPath(session, key), nil, nil, &got, false)
		if callErr != nil {
			return callErr
		}
		if status != http.StatusOK || got.Key != key || got.MessageUID == "" || got.SenderUID == "" {
			return fail(ExitFailed, "instruction read returned an unconfirmed delivery")
		}
		return a.printJSON(got)
	case "send":
		if !instructionKeyPattern.MatchString(key) || !instructionAttemptPattern.MatchString(attempt) || project == "" || repository == "" || taskKey == "" || expectUID == "" || expectPod == "" {
			return usageError("instruction send requires --key, --project, --repository, --task-key, --expect-uid, --expect-pod and --attempt")
		}
		text, err := io.ReadAll(io.LimitReader(a.env.Stdin, instructionMaxText+1))
		if err != nil || len(text) == 0 || len(text) > instructionMaxText || !utf8.Valid(text) {
			return usageError("instruction send requires 1..8192 bytes of UTF-8 on stdin")
		}
		req := apiv1.CoordinatorInstructionRequest{
			Key: key, Project: project, Repository: repository, TaskKey: taskKey,
			TargetSessionUID: expectUID, TargetPodUID: expectPod, AttemptID: attempt, Text: string(text),
		}
		var got instructionDelivery
		status, callErr := a.callWith(ctx, c, http.MethodPost, apiv1.SessionInstructionsPath(session), nil, req, &got, false)
		if status == http.StatusConflict {
			return fail(ExitFailed, "instruction intent conflicts with the existing key; no replacement key was generated")
		}
		if callErr != nil {
			return callErr
		}
		created := status == http.StatusCreated && got.Outcome == apiv1.InstructionOutcomeCreated
		confirmed := status == http.StatusOK && got.Outcome == apiv1.InstructionOutcomeConfirmed
		if (!created && !confirmed) ||
			got.Key != key || got.TargetSessionUID != expectUID || got.TargetPodUID != expectPod || got.AttemptID != attempt || got.Project != project || got.Repository != repository || got.TaskKey != taskKey || got.Nonce != "" || got.Text != "" || got.MessageUID == "" ||
			(created && got.Consumed) {
			return fail(ExitUnconfirmed, "instruction send returned an unconfirmed delivery; retry only the identical intent with the same key")
		}
		return a.printJSON(got.CoordinatorInstructionReceipt)
	case "consume":
		if !instructionKeyPattern.MatchString(key) || !instructionAttemptPattern.MatchString(attempt) || !instructionNoncePattern.MatchString(nonce) || !validInstructionMessageUID(messageUID) || !instructionNoncePattern.MatchString(textSHA) || expectUID == "" || expectPod == "" {
			return usageError("instruction consume requires --key, --attempt, --nonce, --message-uid, --text-sha256, --expect-uid and --expect-pod")
		}
		req := apiv1.CoordinatorInstructionConsumeRequest{AttemptID: attempt, Nonce: nonce, TargetSessionUID: expectUID, TargetPodUID: expectPod}
		var got apiv1.CoordinatorInstructionConsumeACK
		status, callErr := a.callWith(ctx, c, http.MethodPost, apiv1.SessionInstructionConsumePath(session, key), nil, req, &got, false)
		if status == http.StatusConflict {
			return fail(ExitFailed, "instruction consumption conflicts with the stored receipt; nothing was replaced")
		}
		if callErr != nil {
			return callErr
		}
		if (status != http.StatusCreated || got.Outcome != apiv1.InstructionOutcomeConsumed) && (status != http.StatusOK || got.Outcome != apiv1.InstructionOutcomeConfirmed) ||
			got.Key != key || got.AttemptID != attempt || got.Nonce != nonce || got.TargetSessionUID != expectUID || got.TargetPodUID != expectPod || got.MessageUID != messageUID || got.TextSHA256 != textSHA {
			return fail(ExitUnconfirmed, "instruction consume returned an unconfirmed acknowledgement; retry only the same nonce and attempt")
		}
		return a.printJSON(got)
	default:
		return usageError("instruction requires send, read, consume, checkpoint or attempt; see agent-run help instruction")
	}
}

const instructionMaxText = 8 << 10

func validInstructionMessageUID(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, r := range s {
		if r <= 0x20 || r >= 0x7f {
			return false
		}
	}
	return true
}

type instructionDelivery struct {
	apiv1.CoordinatorInstructionReceipt
	Nonce string `json:"nonce,omitempty"`
	Text  string `json:"text,omitempty"`
}

type instructionRead struct {
	apiv1.CoordinatorInstructionView
}
