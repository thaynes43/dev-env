package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const decisionRecordFile = "child-decision.json"

const managedChildDecisionGuard = "When progress needs an owner decision, record ONE question with `agentd ask-decision`, supplying a JSON object on stdin with `question`, optional `options` (recommended choice first; normally 2–3 choices), and optional `context` containing the facts and consequence needed to answer. Keep the question under 2 KiB, context under 8 KiB, and each option under 256 bytes, with no more than eight options. Use the command's returned decision ID in your BLOCKED outcome, then end the current turn. The configured parent must explicitly read the saved decision, present it through its native question tool, and record the answer. Phone delivery remains an acceptance requirement. Continue only after that recorded answer is delivered to this same task. If recording fails or its result is uncertain, report that failure and stop; do not repeat the question, invent an answer, or launch another task. Keep credentials, login codes and tokens out of decision records."

type privateDecision struct {
	protocol.DecisionRecord
	Source             nativeBinding `json:"source"`
	ResumeInvocationID string        `json:"resumeInvocationID,omitempty"`
}

func privateDecisionLock(s Settings) (func(), error) {
	return privateNativeLock(s, "child-decision.lock")
}

func readPrivateDecision(s Settings) (privateDecision, error) {
	var record privateDecision
	if err := privateManagedState(s); err != nil {
		return record, err
	}
	f, err := os.OpenFile(s.statePath(decisionRecordFile), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return record, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() > protocol.MaxDecisionRecordBytes {
		return record, errors.New("decision record is not a bounded private regular file")
	}
	d := json.NewDecoder(io.LimitReader(f, protocol.MaxDecisionRecordBytes))
	d.DisallowUnknownFields()
	if d.Decode(&record) != nil || d.Decode(new(any)) != io.EOF {
		return record, errors.New("decision record is invalid")
	}
	if record.Validate() != nil ||
		record.Session != record.Source.Session || record.SessionUID != record.Source.SessionUID || record.PodUID != record.Source.PodUID ||
		record.ThreadID != record.Source.ThreadID || record.WriterGeneration != record.Source.Generation || record.WriterGeneration == 0 {
		return record, errors.New("decision record lacks exact platform identity")
	}
	if (record.State == "Open" || record.State == "Answered") && record.ResumeInvocationID != "" {
		return record, errors.New("unconsumed decision has contradictory dispatch state")
	}
	if (record.State == "ResumeStarting" || record.State == "Dispatching" || record.State == "Delivered") && !nativeThreadID.MatchString(record.ResumeInvocationID) {
		return record, errors.New("consumed decision lacks an exact native invocation")
	}
	return record, nil
}

func writePrivateDecision(s Settings, record privateDecision) error {
	if err := privateManagedState(s); err != nil {
		return err
	}
	b, err := json.Marshal(record)
	if err != nil || len(b)+1 > protocol.MaxDecisionRecordBytes {
		return errors.New("decision record exceeds its private transport bound")
	}
	return writeWorkspaceJSON(s.statePath(decisionRecordFile), record)
}

// No caller-supplied session document is needed by ask-decision. The native
// process's AGENTD_SESSION is deliberately scrubbed; agentd reads its saved
// current launch and exact current durable writer instead.
func decisionAuthority(s Settings) (Launch, nativeBinding, error) {
	if !s.ManagedChildDecisions || !s.ManagedCodexTasks {
		return Launch{}, nativeBinding{}, errors.New("managed child decisions are disabled")
	}
	l, err := privateCurrentLaunch(s)
	if err != nil || !l.ChildDecisions || l.PodUID != s.PodUID || !l.NativeThreadConfirmed || !nativeThreadID.MatchString(l.ConversationID) {
		return Launch{}, nativeBinding{}, errors.New("decision lacks a confirmed current native task")
	}
	binding, err := invocationBinding(l)
	if err != nil {
		return Launch{}, binding, err
	}
	var owner taskOwner
	if err := readWorkspaceJSON(s.ownerPath(l.Session), &owner); err != nil {
		return Launch{}, binding, err
	}
	if owner.State != "owned" || !owner.Launched || owner.Task != l.Session || owner.SessionUID != l.SessionUID || owner.PodUID != s.PodUID || owner.Generation != binding.Generation ||
		owner.Workspace != s.WorkspaceID || owner.Worktree != l.Dir || owner.Clone != s.ClonePath(owner.Repo) {
		return Launch{}, binding, errors.New("decision does not retain the same launched task writer")
	}
	return l, binding, nil
}

func decisionBindingMatches(record privateDecision, binding nativeBinding) bool {
	return record.Session == binding.Session && record.SessionUID == binding.SessionUID && record.PodUID == binding.PodUID &&
		record.ThreadID == binding.ThreadID && record.WriterGeneration == binding.Generation && record.Source.BootID == binding.BootID
}

// AskDecision assigns all identity itself. It creates one immutable question
// while the exact native invocation is live, before the child reports BLOCKED.
func AskDecision(ctx context.Context, s Settings, question protocol.DecisionQuestion, now time.Time) (protocol.DecisionRecord, error) {
	if err := question.Validate(); err != nil {
		return protocol.DecisionRecord{}, err
	}
	gate, err := workspaceSupervisorLock(ctx, s)
	if err != nil {
		return protocol.DecisionRecord{}, err
	}
	defer gate()
	unlock, err := privateDecisionLock(s)
	if err != nil {
		return protocol.DecisionRecord{}, err
	}
	defer unlock()
	l, binding, err := decisionAuthority(s)
	if err != nil {
		return protocol.DecisionRecord{}, err
	}
	result, err := readNativeInvocation(s)
	prior := binding
	prior.ThreadID = result.Binding.ThreadID
	if err != nil || result.Phase != "Running" || result.Binding != prior || ownedNativeStarted(s, l) != nil {
		return protocol.DecisionRecord{}, errors.New("decision requires the currently owned native invocation")
	}
	if old, err := readPrivateDecision(s); err == nil {
		if old.State != "Delivered" || !decisionBindingMatches(old, binding) {
			return protocol.DecisionRecord{}, errors.New("a pending or uncertain decision cannot be replaced")
		}
		if err := archiveDeliveredDecision(s, old); err != nil {
			return protocol.DecisionRecord{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return protocol.DecisionRecord{}, err
	}
	if stop, err := workspaceStopForOwner(s, protocol.Session{Name: l.Session, Workspace: &protocol.WorkspaceBinding{ID: s.WorkspaceID, SessionUID: l.SessionUID}}, *l.WorkspaceOwner); err != nil || stop {
		return protocol.DecisionRecord{}, errors.New("decision refuses a requested or uncertain supervisor stop")
	}
	id, err := newUUID()
	if err != nil {
		return protocol.DecisionRecord{}, err
	}
	record := privateDecision{DecisionRecord: protocol.DecisionRecord{Version: 1, ID: id, Session: binding.Session, SessionUID: binding.SessionUID,
		PodUID: binding.PodUID, ThreadID: binding.ThreadID, WriterGeneration: binding.Generation, Question: question, State: "Open", CreatedAt: now.UTC()}, Source: binding}
	if err := writePrivateDecision(s, record); err != nil {
		return protocol.DecisionRecord{}, err
	}
	return record.DecisionRecord, nil
}

// Completed context remains private and durable when the same thread asks its
// next question. An exclusive archive write cannot overwrite a previous answer.
func archiveDeliveredDecision(s Settings, record privateDecision) error {
	if record.State != "Delivered" || !nativeThreadID.MatchString(record.ID) {
		return errors.New("only an exact completed decision may be archived")
	}
	archive := s
	archive.StateDir = filepath.Join(s.StateDir, "decisions")
	if err := ensureWorkspaceDirectory(archive.StateDir); err != nil {
		return err
	}
	if err := privateManagedState(archive); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil || len(data)+1 > protocol.MaxDecisionRecordBytes {
		return errors.New("completed decision exceeds its private archive bound")
	}
	path := archive.statePath(record.ID + ".json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if errors.Is(err, os.ErrExist) {
		var old privateDecision
		if err := readPrivateManagedJSON(archive, record.ID+".json", protocol.MaxDecisionRecordBytes, &old); err != nil {
			return err
		}
		x, _ := json.Marshal(old)
		if string(x) != string(data) {
			return errors.New("completed decision archive already contains different context")
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	dir, err := os.Open(archive.StateDir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func ReadDecision(ctx context.Context, s Settings) (protocol.DecisionResult, error) {
	gate, err := workspaceSupervisorLock(ctx, s)
	if err != nil {
		return protocol.DecisionResult{}, err
	}
	defer gate()
	_, binding, err := decisionAuthority(s)
	if err != nil {
		return protocol.DecisionResult{}, err
	}
	record, err := readPrivateDecision(s)
	if errors.Is(err, os.ErrNotExist) {
		return protocol.DecisionResult{}, nil
	}
	if err != nil || !decisionBindingMatches(record, binding) {
		return protocol.DecisionResult{}, errors.New("decision identity is missing, changed or uncertain")
	}
	return protocol.DecisionResult{Decision: &record.DecisionRecord}, nil
}

// AnswerDecision records one answer only. It never pastes, starts a process or
// retries a dispatch; only the owning supervisor may consume the answered record.
func AnswerDecision(ctx context.Context, s Settings, answer protocol.DecisionAnswer, now time.Time) (protocol.DecisionResult, error) {
	if answer.Validate() != nil || !nativeThreadID.MatchString(answer.ID) {
		return protocol.DecisionResult{}, errors.New("invalid decision answer")
	}
	gate, err := workspaceSupervisorLock(ctx, s)
	if err != nil {
		return protocol.DecisionResult{}, err
	}
	defer gate()
	unlock, err := privateDecisionLock(s)
	if err != nil {
		return protocol.DecisionResult{}, err
	}
	defer unlock()
	_, binding, err := decisionAuthority(s)
	if err != nil {
		return protocol.DecisionResult{}, err
	}
	record, err := readPrivateDecision(s)
	if err != nil || !decisionBindingMatches(record, binding) || record.ID != answer.ID {
		return protocol.DecisionResult{}, errors.New("decision answer does not bind the current exact question")
	}
	if record.State != "Open" {
		if record.Answer == answer.Text {
			return protocol.DecisionResult{Decision: &record.DecisionRecord}, nil
		}
		return protocol.DecisionResult{}, errors.New("the question already has a recorded answer")
	}
	at := now.UTC()
	record.Answer, record.AnsweredAt, record.State = answer.Text, &at, "Answered"
	if err := writePrivateDecision(s, record); err != nil {
		return protocol.DecisionResult{}, err
	}
	return protocol.DecisionResult{Decision: &record.DecisionRecord}, nil
}

func decisionAnswerText(record privateDecision) string {
	return "[Recorded owner answer for decision " + record.ID + "]\n\nQuestion:\n" + record.Question.Question + "\n\nAnswer:\n" + record.Answer
}
