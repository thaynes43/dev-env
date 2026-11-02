package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Active native attempts are the platform's Running invocation record.
// An exited, uncertain or replaced record is not an instruction target.
func ActiveInstructionAttempt(s Settings) (nativeBinding, error) {
	inv, err := readNativeInvocation(s)
	if err != nil {
		return nativeBinding{}, err
	}
	if inv.Version != 1 || inv.Phase != "Running" || !inv.FinishedAt.IsZero() || inv.LeaderReaped || inv.GroupAbsent {
		return nativeBinding{}, errors.New("native instruction attempt is not the active invocation")
	}
	b := inv.Binding
	if b.InvocationID == "" || b.Session == "" || b.SessionUID == "" || b.PodUID == "" || (s.PodUID != "" && b.PodUID != s.PodUID) {
		return nativeBinding{}, errors.New("native instruction attempt does not match this pod")
	}
	return b, nil
}

var instructionClientPath = regexp.MustCompile(`^/v1/sessions/[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?/(?:instruction-attempt|instructions(?:/[a-z0-9](?:[a-z0-9._-]{0,118}[a-z0-9])?(?:/consume)?)?)$`)

// nativeInstructionPullGuidance is launch text for an enabled native child.
// It describes an explicit checkpoint. It is not a live push, a steer, or a
// consumption receipt. Codex exec and a resumed Codex TUI are not interrupted.
const nativeInstructionPullGuidance = "This native exec does not receive a live coordinator push or steer. Codex exec writes the original task prompt to stdin once and has no channel that interrupts the process with a later correction. A resumed Codex TUI reads the terminal; that keyboard attach is not a coordinator push. Claude Code is not enrolled by this launch and is not interrupted either. A correction posted after launch is visible to this process only when you run `agent-run instruction checkpoint --session <this-session>` at a meaningful checkpoint: before an irreversible step, before declaring the task done, and when you resume work. The checkpoint lists pending corrections for this active attempt, including the sender UID, project, repository, task key, text, message UID, text digest and nonce. It does not consume them. Acknowledge one correction only with an explicit `agent-run instruction consume` that copies that checkpoint's message UID, text digest and nonce. A checkpoint, a keyed read, a daemon file, or a nonempty reply is not consumption. If the checkpoint result is unknown, stop and report that; do not invent an acknowledgement or a key."

// Instruction performs one coordinator-instruction call on the existing inbox
// transport. It does not follow redirects or retry. Paths outside the attempt,
// read and consume routes are refused.
func (c *InboxClient) Instruction(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	if !instructionClientPath.MatchString(path) {
		return nil, 0, errors.New("invalid bounded instruction operation")
	}
	return c.doBounded(ctx, method, path, body, "instruction", 4096)
}

// RegisterInstructionAttempt posts the active native attempt. The server's
// session and pod must match the local binding. This is not consumption.
func (c *InboxClient) RegisterInstructionAttempt(ctx context.Context, s Settings) (apiv1.CoordinatorInstructionAttemptResult, error) {
	binding, err := ActiveInstructionAttempt(s)
	if err != nil {
		return apiv1.CoordinatorInstructionAttemptResult{}, err
	}
	body, _ := json.Marshal(apiv1.CoordinatorInstructionAttemptRequest{AttemptID: binding.InvocationID})
	raw, status, err := c.Instruction(ctx, http.MethodPost, apiv1.SessionInstructionAttemptPath(binding.Session), body)
	if err != nil || status != http.StatusOK {
		return apiv1.CoordinatorInstructionAttemptResult{}, errors.New("instruction attempt outcome Unknown; retry the same attempt")
	}
	var got apiv1.CoordinatorInstructionAttemptResult
	if err := decodeInstructionJSON(raw, &got); err != nil || got.AttemptID != binding.InvocationID || got.SessionUID != binding.SessionUID || got.PodUID != binding.PodUID || got.Phase != apiv1.InstructionPhaseActive {
		return apiv1.CoordinatorInstructionAttemptResult{}, errors.New("instruction attempt reply is not correlated")
	}
	return got, nil
}

// ConsumeInstruction posts the active attempt and checks the correlated
// consumption ACK. A daemon file write is not consumption. A mismatched reply
// is an error and is not retried as a new intent.
func (c *InboxClient) ConsumeInstruction(ctx context.Context, s Settings, key, nonce, messageUID, digest string) (apiv1.CoordinatorInstructionConsumeACK, error) {
	binding, err := ActiveInstructionAttempt(s)
	if err != nil {
		return apiv1.CoordinatorInstructionConsumeACK{}, err
	}
	req := apiv1.CoordinatorInstructionConsumeRequest{AttemptID: binding.InvocationID, Nonce: nonce, TargetSessionUID: binding.SessionUID, TargetPodUID: binding.PodUID}
	body, _ := json.Marshal(req)
	raw, status, err := c.Instruction(ctx, http.MethodPost, apiv1.SessionInstructionConsumePath(binding.Session, key), body)
	if err != nil || (status != http.StatusCreated && status != http.StatusOK) {
		return apiv1.CoordinatorInstructionConsumeACK{}, errors.New("instruction consumption outcome Unknown; retry the same nonce and attempt")
	}
	var ack apiv1.CoordinatorInstructionConsumeACK
	if err := decodeInstructionJSON(raw, &ack); err != nil {
		return apiv1.CoordinatorInstructionConsumeACK{}, errors.New("instruction consumption reply is not correlated")
	}
	wantOutcome := apiv1.InstructionOutcomeConsumed
	if status == http.StatusOK {
		wantOutcome = apiv1.InstructionOutcomeConfirmed
	}
	if ack.Outcome != wantOutcome || ack.Key != key || ack.MessageUID != messageUID || ack.AttemptID != binding.InvocationID || ack.Nonce != nonce || ack.TextSHA256 != digest || ack.TargetSessionUID != binding.SessionUID || ack.TargetPodUID != binding.PodUID {
		return apiv1.CoordinatorInstructionConsumeACK{}, errors.New("instruction consumption reply is not correlated")
	}
	return ack, nil
}

// ReadInstruction returns the stored instruction for the active attempt.
// The read is not an inbox acknowledgement and not native consumption.
func (c *InboxClient) ReadInstruction(ctx context.Context, s Settings, key string) (apiv1.CoordinatorInstructionView, error) {
	binding, err := ActiveInstructionAttempt(s)
	if err != nil {
		return apiv1.CoordinatorInstructionView{}, err
	}
	raw, status, err := c.Instruction(ctx, http.MethodGet, apiv1.SessionInstructionPath(binding.Session, key), nil)
	if err != nil || status != http.StatusOK {
		return apiv1.CoordinatorInstructionView{}, errors.New("instruction read outcome Unknown; retry the same key")
	}
	var view apiv1.CoordinatorInstructionView
	if err := decodeInstructionJSON(raw, &view); err != nil {
		return apiv1.CoordinatorInstructionView{}, errors.New("instruction read reply is not correlated")
	}
	if view.Outcome != apiv1.InstructionOutcomeConfirmed || view.Key != key || view.SenderUID == "" || view.MessageUID == "" || view.TextSHA256 == "" || view.Nonce == "" || view.Text == "" || view.Project == "" || view.Repository == "" || view.TaskKey == "" || view.AttemptID != binding.InvocationID || view.TargetSessionUID != binding.SessionUID || view.TargetPodUID != binding.PodUID {
		return apiv1.CoordinatorInstructionView{}, errors.New("instruction read reply is not correlated")
	}
	return view, nil
}

// CheckpointInstructions lists pending corrections for the active attempt.
// The caller does not supply a key. The reply is a read, not consumption,
// and the daemon does not call this from the lifecycle path.
func (c *InboxClient) CheckpointInstructions(ctx context.Context, s Settings) (apiv1.CoordinatorInstructionCheckpoint, error) {
	binding, err := ActiveInstructionAttempt(s)
	if err != nil {
		return apiv1.CoordinatorInstructionCheckpoint{}, err
	}
	raw, status, err := c.Instruction(ctx, http.MethodGet, apiv1.SessionInstructionsPath(binding.Session), nil)
	if err != nil || status != http.StatusOK {
		return apiv1.CoordinatorInstructionCheckpoint{}, errors.New("instruction checkpoint outcome Unknown; retry the same attempt")
	}
	var got apiv1.CoordinatorInstructionCheckpoint
	if err := decodeInstructionJSON(raw, &got); err != nil {
		return apiv1.CoordinatorInstructionCheckpoint{}, errors.New("instruction checkpoint reply is not correlated")
	}
	if got.Outcome != apiv1.InstructionOutcomeConfirmed || got.AttemptID != binding.InvocationID || got.SessionUID != binding.SessionUID || got.PodUID != binding.PodUID || len(got.Pending) > 4 {
		return apiv1.CoordinatorInstructionCheckpoint{}, errors.New("instruction checkpoint reply is not correlated")
	}
	seen := map[string]bool{}
	for _, item := range got.Pending {
		if seen[item.Key] || item.Outcome != apiv1.InstructionOutcomeConfirmed || item.Consumed || item.Key == "" || item.SenderUID == "" || item.MessageUID == "" || item.TextSHA256 == "" || item.Nonce == "" || item.Text == "" || item.Project == "" || item.Repository == "" || item.TaskKey == "" || item.AttemptID != binding.InvocationID || item.TargetSessionUID != binding.SessionUID || item.TargetPodUID != binding.PodUID {
			return apiv1.CoordinatorInstructionCheckpoint{}, errors.New("instruction checkpoint reply is not correlated")
		}
		seen[item.Key] = true
	}
	return got, nil
}

// AcknowledgeReadInstruction consumes only when the model-issued message UID
// and text digest are the values from this exact read. Empty or different
// reply fields are refused before consumption. A read alone does not consume.
func (c *InboxClient) AcknowledgeReadInstruction(ctx context.Context, s Settings, key, messageUID, digest string) (apiv1.CoordinatorInstructionConsumeACK, error) {
	view, err := c.ReadInstruction(ctx, s, key)
	if err != nil {
		return apiv1.CoordinatorInstructionConsumeACK{}, err
	}
	if messageUID == "" || digest == "" || view.MessageUID != messageUID || view.TextSHA256 != digest {
		return apiv1.CoordinatorInstructionConsumeACK{}, errors.New("instruction consumption reply is not correlated")
	}
	return c.ConsumeInstruction(ctx, s, key, view.Nonce, view.MessageUID, view.TextSHA256)
}

func verifiedEndedInstructionAttempt(s Settings) (nativeBinding, error) {
	inv, err := readNativeInvocation(s)
	if err != nil {
		return nativeBinding{}, err
	}
	if inv.Version != 1 || inv.Phase != "Exited" || inv.StartedAt.IsZero() || inv.FinishedAt.IsZero() || inv.FinishedAt.Before(inv.StartedAt) || !inv.LeaderReaped || !inv.GroupAbsent {
		return nativeBinding{}, errors.New("native instruction attempt is not a verified end")
	}
	b := inv.Binding
	if b.InvocationID == "" || b.Session == "" || b.SessionUID == "" || b.PodUID == "" || (s.PodUID != "" && b.PodUID != s.PodUID) {
		return nativeBinding{}, errors.New("native instruction attempt does not match this pod")
	}
	return b, nil
}

// EndInstructionAttempt posts the end of the attempt that this pod verified
// locally. An uncertain or still-running record is not an end. This is not
// consumption.
func (c *InboxClient) EndInstructionAttempt(ctx context.Context, s Settings) (apiv1.CoordinatorInstructionAttemptResult, error) {
	binding, err := verifiedEndedInstructionAttempt(s)
	if err != nil {
		return apiv1.CoordinatorInstructionAttemptResult{}, err
	}
	body, _ := json.Marshal(apiv1.CoordinatorInstructionAttemptRequest{AttemptID: binding.InvocationID, Ended: true})
	raw, status, err := c.Instruction(ctx, http.MethodPost, apiv1.SessionInstructionAttemptPath(binding.Session), body)
	if err != nil || status != http.StatusOK {
		return apiv1.CoordinatorInstructionAttemptResult{}, errors.New("instruction attempt outcome Unknown; retry the same attempt")
	}
	var got apiv1.CoordinatorInstructionAttemptResult
	if err := decodeInstructionJSON(raw, &got); err != nil || got.AttemptID != binding.InvocationID || got.SessionUID != binding.SessionUID || got.PodUID != binding.PodUID || got.Phase != apiv1.InstructionPhaseEnded {
		return apiv1.CoordinatorInstructionAttemptResult{}, errors.New("instruction attempt reply is not correlated")
	}
	return got, nil
}

// publishNativeInstructionLifecycle registers the local Running attempt, or
// ends a verified local exit, through the existing inbox client. Legacy and
// disabled launches return before any request, including when an API origin
// is configured. An enabled launch with an empty origin also makes no request.
// A configured origin that fails is returned to the caller, which stops that
// native attempt. This path does not checkpoint or consume.
func publishNativeInstructionLifecycle(lease *nativeInvocationLease, enabled, ended bool) error {
	if lease == nil || !enabled || !lease.s.CoordinatorInstructions || lease.s.APIURL == "" {
		return nil
	}
	client, err := NewInboxClient(lease.s)
	if err != nil {
		return err
	}
	if ended {
		_, err = client.EndInstructionAttempt(context.Background(), lease.s)
		return err
	}
	_, err = client.RegisterInstructionAttempt(context.Background(), lease.s)
	return err
}

func decodeInstructionJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid instruction JSON")
	}
	return nil
}
