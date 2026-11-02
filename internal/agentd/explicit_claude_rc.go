package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	explicitClaudeRCFile       = "explicit-claude-rc.json"
	explicitClaudeRCStarting   = "starting"
	explicitClaudeRCSupervised = "supervised"
	explicitClaudeRCUncertain  = "uncertain"
	explicitClaudeRCExited     = "exited"
	explicitClaudeRCMaxFile    = 16 << 10
	explicitTranscriptMax      = 32 << 20
)

// ExplicitClaudeRCRequest names one pod-local operation. Audit strings do not
// grant authority. The existing caller must already be authorized to control
// this pod; no file or free-text sentence authenticates a human decision.
type ExplicitClaudeRCRequest struct {
	Version            int    `json:"version"`
	RequestID          string `json:"requestID"`
	SessionUID         string `json:"sessionUID"`
	NativeConversation string `json:"nativeConversation"`
	OriginalHOME       string `json:"originalHOME"`
	Title              string `json:"title"`
	UserAuthority      string `json:"userAuthority,omitempty"`
	FreezeEvidence     string `json:"freezeEvidence,omitempty"`
}

type ExplicitClaudeRCLaunch struct {
	Version            int    `json:"version"`
	RequestID          string `json:"requestID"`
	SessionUID         string `json:"sessionUID"`
	NativeConversation string `json:"nativeConversation"`
	Title              string `json:"title"`
}

// The journal is durable before resume.json or tmux changes. One request has
// one generation and one spawn intent. Uncertain starts and completed TUIs are
// never replayed, including after a new daemon boot.
type ExplicitClaudeRCRecord struct {
	Version              int       `json:"version"`
	Phase                string    `json:"phase"`
	RequestID            string    `json:"requestID"`
	Session              string    `json:"session"`
	SessionUID           string    `json:"sessionUID"`
	PodUID               string    `json:"podUID"`
	NativeConversation   string    `json:"nativeConversation"`
	OriginalHOME         string    `json:"originalHOME"`
	Title                string    `json:"title"`
	UserAuthority        string    `json:"userAuthority,omitempty"`
	FreezeEvidence       string    `json:"freezeEvidence,omitempty"`
	OriginalExitCode     int       `json:"originalExitCode"`
	OriginalFinishedAt   time.Time `json:"originalFinishedAt"`
	OriginalLaunchSHA256 string    `json:"originalLaunchSHA256"`
	OriginalResultSHA256 string    `json:"originalResultSHA256"`
	ResumeSHA256         string    `json:"resumeSHA256"`
	BootID               string    `json:"bootID"`
	TranscriptBytes      int64     `json:"transcriptBytes"`
	WriterGeneration     uint64    `json:"writerGeneration"`
	StartedAt            time.Time `json:"startedAt"`
	Pid                  int       `json:"pid,omitempty"`
	Start                string    `json:"start,omitempty"`
	FinishedAt           time.Time `json:"finishedAt,omitempty"`
	ExitCode             int       `json:"exitCode,omitempty"`
}

var (
	explicitRequestID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,80}$`)
	explicitTitlePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,79}$`)
)

func StartExplicitClaudeRC(ctx context.Context, r Runner, s Settings, sess protocol.Session, req ExplicitClaudeRCRequest, now time.Time, self string) (ExplicitClaudeRCRecord, error) {
	boot, err := readyExplicitBoot(s)
	if err != nil {
		return ExplicitClaudeRCRecord{}, err
	}
	if err := explicitDaemonMatches(s, sess, boot, self); err != nil {
		return ExplicitClaudeRCRecord{}, err
	}
	return superviseExplicitClaudeRC(ctx, r, s, sess, *boot.Workspace, req, boot.BootID, now, self, true)
}

// ContinueExplicitClaudeRC reconciles the existing process or exit; it never
// spawns. Retained-private BuildResume remains forbidden.
func ContinueExplicitClaudeRC(ctx context.Context, r Runner, s Settings, sess protocol.Session, ws protocol.Workspace, bootID string, now time.Time, self string) (ExplicitClaudeRCRecord, error) {
	var boot bootRecord
	if readPrivateManagedJSON(s, bootFile, 64<<10, &boot) != nil || bootID == "" || boot.BootID != bootID {
		return ExplicitClaudeRCRecord{}, errors.New("explicit continuation boot does not match the current boot")
	}
	if err := explicitDaemonMatches(s, sess, boot, self); err != nil {
		return ExplicitClaudeRCRecord{}, err
	}
	rec, err := readExplicitClaudeRCRecord(s)
	if err != nil {
		return ExplicitClaudeRCRecord{}, errors.New("explicit continuation record is unreadable")
	}
	return superviseExplicitClaudeRC(ctx, r, s, sess, ws, rec.request(), bootID, now, self, false)
}

func (d *Daemon) restartExplicitClaudeRC(ctx context.Context, ws protocol.Workspace, bootID string) (Step, string, bool) {
	if _, err := os.Lstat(d.S.statePath(explicitClaudeRCFile)); errors.Is(err, os.ErrNotExist) {
		return Step{}, "", false
	}
	rec, err := ContinueExplicitClaudeRC(ctx, d.R, d.S, d.Session, ws, bootID, d.now(), d.Self)
	if err != nil {
		return newStep("agent", nil, err), err.Error(), true
	}
	return newStep("agent", []string{"explicit Claude continuation " + rec.Phase + "; no provider replay"}, nil), "", true
}

func superviseExplicitClaudeRC(ctx context.Context, r Runner, s Settings, sess protocol.Session, ws protocol.Workspace, req ExplicitClaudeRCRequest, bootID string, now time.Time, self string, allowInitial bool) (ExplicitClaudeRCRecord, error) {
	unlock, err := privateNativeLock(s, "explicit-claude-rc.lock")
	if err != nil {
		return ExplicitClaudeRCRecord{}, err
	}
	defer unlock()
	if err := req.validate(); err != nil {
		return ExplicitClaudeRCRecord{}, err
	}
	if err := explicitSessionAllowed(s, sess, req); err != nil {
		return ExplicitClaudeRCRecord{}, err
	}
	current, err := currentBootID(s)
	if err != nil || bootID == "" || bootID != current {
		return ExplicitClaudeRCRecord{}, errors.New("explicit continuation boot does not match the current boot")
	}
	if ws.Worktree == "" || ws.Worktree != s.WorktreePath(sess.Name) {
		return ExplicitClaudeRCRecord{}, errors.New("current workspace is unknown")
	}
	first, result, launchBytes, resultBytes, err := explicitOriginal(s, sess, ws, req, now)
	if err != nil {
		return ExplicitClaudeRCRecord{}, err
	}
	rec, readErr := readExplicitClaudeRCRecord(s)
	if readErr == nil {
		if !sameExplicitBinding(rec, req, sess, s.Home) || rec.PodUID != s.PodUID || rec.OriginalLaunchSHA256 != explicitDigest(launchBytes) || rec.OriginalResultSHA256 != explicitDigest(resultBytes) {
			return rec, errors.New("explicit continuation is already owned by a different request or executor")
		}
		return reconcileExplicitClaudeRC(ctx, r, s, rec, now)
	}
	if !errors.Is(readErr, os.ErrNotExist) {
		return rec, errors.New("explicit continuation record is unreadable")
	}
	if !allowInitial {
		return rec, errors.New("explicit continuation has no prior intent")
	}
	state, err := explicitPidState(s)
	if err != nil {
		return rec, err
	}
	if state == "alive" {
		return rec, errors.New("original native owner is live")
	}
	present, err := tmuxAgentPresent(ctx, r, s)
	if err != nil {
		return rec, err
	}
	if present {
		return rec, errors.New("tmux session agent already exists; refusing to start or kill it")
	}
	transcriptSize, err := explicitTranscriptBytes(s, ws.Worktree, req.NativeConversation)
	if err != nil {
		return rec, err
	}
	if !checkLogin(s).Usable(now) {
		return rec, errors.New("existing Claude login is not usable; continuation was not started")
	}
	l, err := buildExplicitClaudeRCLaunch(s, sess, ws, first, req, bootID, now)
	if err != nil {
		return rec, err
	}
	resumeBytes, err := json.Marshal(l)
	if err != nil {
		return rec, err
	}
	// writeWorkspaceJSON uses the same canonical JSON followed by one newline.
	resumeBytes = append(resumeBytes, '\n')
	rec = ExplicitClaudeRCRecord{
		Version: 1, Phase: explicitClaudeRCStarting, RequestID: req.RequestID, Session: sess.Name,
		SessionUID: sess.SessionUID, PodUID: s.PodUID, NativeConversation: req.NativeConversation,
		OriginalHOME: s.Home, Title: req.Title, UserAuthority: req.UserAuthority, FreezeEvidence: req.FreezeEvidence,
		OriginalExitCode: result.ExitCode, OriginalFinishedAt: result.FinishedAt.UTC(),
		OriginalLaunchSHA256: explicitDigest(launchBytes), OriginalResultSHA256: explicitDigest(resultBytes),
		ResumeSHA256: explicitDigest(resumeBytes), BootID: bootID, TranscriptBytes: transcriptSize,
		WriterGeneration: 1, StartedAt: now.UTC(),
	}
	if err := writeExplicitClaudeRCRecord(s, rec); err != nil {
		return rec, err
	}
	// This is deliberately separate from legacy StartAgent: conflict must never
	// overwrite or unlink a previous resume, and uncertain tmux keeps the intent.
	if err := startExplicitClaudeRCPane(ctx, r, s, l, self, launchBytes, resultBytes); err != nil {
		rec.Phase = explicitClaudeRCUncertain
		_ = writeExplicitClaudeRCRecord(s, rec)
		return rec, err
	}
	return reconcileExplicitClaudeRC(ctx, r, s, rec, now)
}

func explicitOriginal(s Settings, sess protocol.Session, ws protocol.Workspace, req ExplicitClaudeRCRequest, now time.Time) (Launch, taskResult, []byte, []byte, error) {
	var first Launch
	var result taskResult
	a, err := regularPrivateBytes(s.statePath(launchFile), 512<<10)
	if err != nil || json.Unmarshal(a, &first) != nil || first.Provider != protocol.AgentClaude || first.Session != sess.Name || first.SessionUID != sess.SessionUID || first.SessionUID == "" || first.PodUID != s.PodUID || first.PodUID == "" || first.ConversationID != req.NativeConversation || first.Dir != ws.Worktree || first.BootID == "" || first.CreatedAt.IsZero() {
		return first, result, nil, nil, errors.New("original launch is missing or does not match this executor and conversation")
	}
	b, err := regularPrivateBytes(s.statePath(resultFile), 64<<10)
	if err != nil || json.Unmarshal(b, &result) != nil || result.ConversationID != first.ConversationID || result.BootID != first.BootID || result.StartedAt.IsZero() || result.StartedAt.Before(first.CreatedAt) || result.FinishedAt.Before(result.StartedAt) || result.FinishedAt.IsZero() || result.FinishedAt.After(now) {
		return first, result, nil, nil, errors.New("original task completion does not match this launch")
	}
	return first, result, a, b, nil
}

func startExplicitClaudeRCPane(ctx context.Context, r Runner, s Settings, l Launch, self string, first, result []byte) error {
	present, err := tmuxAgentPresent(ctx, r, s)
	if err != nil {
		return err
	}
	if present {
		return errors.New("tmux session agent already exists; prior resume preserved")
	}
	if err := writeWorkspaceJSON(s.statePath(resumeFile), l); err != nil {
		return err
	}
	launchSame, launchErr := sameRegularBytes(s.statePath(launchFile), first)
	resultSame, resultErr := sameRegularBytes(s.statePath(resultFile), result)
	if launchErr != nil || resultErr != nil || !launchSame || !resultSame {
		return errors.New("original launch or outcome changed before continuation start")
	}
	_, err = r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"new-session", "-d", "-s", TmuxSession, "-x", "200", "-y", "50", "-c", l.Dir, self, "run-agent", "--launch", s.statePath(resumeFile)}})
	if err != nil {
		return errors.New("explicit continuation start is unconfirmed; intent retained, no retry")
	}
	return nil
}

func reconcileExplicitClaudeRC(ctx context.Context, r Runner, s Settings, rec ExplicitClaudeRCRecord, now time.Time) (ExplicitClaudeRCRecord, error) {
	state, pid, exit, err := observeExplicitClaudeRC(ctx, r, s, rec, now)
	if err != nil {
		return rec, err
	}
	changed := rec.Phase != state
	rec.Phase = state
	if state == explicitClaudeRCSupervised && rec.Pid == 0 {
		rec.Pid, rec.Start, changed = pid.Pid, pid.Start, true
	}
	if state == explicitClaudeRCExited && rec.FinishedAt.IsZero() {
		rec.FinishedAt, rec.ExitCode, changed = exit.FinishedAt, exit.ExitCode, true
	}
	if changed {
		if err := writeExplicitClaudeRCRecord(s, rec); err != nil {
			return rec, err
		}
	}
	return rec, nil
}

func observeExplicitClaudeRC(ctx context.Context, r Runner, s Settings, rec ExplicitClaudeRCRecord, now time.Time) (string, agentPid, tuiExit, error) {
	var pid agentPid
	var exit tuiExit
	uncertain := func() (string, agentPid, tuiExit, error) {
		return explicitClaudeRCUncertain, pid, exit, errors.New("explicit continuation process or exit proof is uncertain; no replay")
	}
	if rec.Version != 1 || rec.WriterGeneration != 1 || rec.BootID == "" || rec.PodUID != s.PodUID || rec.StartedAt.IsZero() {
		return uncertain()
	}
	switch rec.Phase {
	case explicitClaudeRCStarting, explicitClaudeRCSupervised, explicitClaudeRCUncertain, explicitClaudeRCExited:
	default:
		return uncertain()
	}
	if (rec.Pid == 0) != (rec.Start == "") || rec.Pid < 0 || (rec.Phase == explicitClaudeRCSupervised && rec.Pid == 0) {
		return uncertain()
	}
	b, err := regularPrivateBytes(s.statePath(resumeFile), 512<<10)
	var l Launch
	if err != nil || explicitDigest(b) != rec.ResumeSHA256 || json.Unmarshal(b, &l) != nil || !explicitInteractiveRC(l) || l.PodUID != rec.PodUID || l.SessionUID != rec.SessionUID || l.BootID != rec.BootID || l.ExplicitClaudeRC.RequestID != rec.RequestID || l.ConversationID != rec.NativeConversation {
		return uncertain()
	}
	state, err := explicitPidState(s)
	if err != nil {
		return uncertain()
	}
	if state == "alive" {
		if readPrivateManagedJSON(s, pidFile, 4096, &pid) != nil || (rec.Pid != 0 && (pid.Pid != rec.Pid || pid.Start != rec.Start)) || rec.Phase == explicitClaudeRCExited {
			return uncertain()
		}
		return explicitClaudeRCSupervised, pid, exit, nil
	}
	// A removed pid file is not stop proof for the exact process we recorded.
	if rec.Pid != 0 && pidAlive(agentPid{rec.Pid, rec.Start}) {
		return uncertain()
	}
	present, err := tmuxAgentPresent(ctx, r, s)
	if err != nil {
		return uncertain()
	}
	if readPrivateManagedJSON(s, tuiExitFile, 4096, &exit) == nil && exit.BootID == rec.BootID && exit.ConversationID == rec.NativeConversation && !exit.StartedAt.Before(rec.StartedAt.Truncate(time.Second)) && !exit.FinishedAt.Before(exit.StartedAt) && !exit.FinishedAt.After(now) && !present {
		return explicitClaudeRCExited, pid, exit, nil
	}
	if rec.Phase == explicitClaudeRCStarting && (state == "absent" || state == "dead") && present && !now.Before(rec.StartedAt) && now.Sub(rec.StartedAt) < startWindow {
		return explicitClaudeRCStarting, pid, exit, nil
	}
	return uncertain()
}

func (req ExplicitClaudeRCRequest) validate() error {
	if req.Version != 1 || !explicitRequestID.MatchString(req.RequestID) || !protocol.ValidTaskBudgetIdentifier(req.SessionUID) || !conversationIDPattern.MatchString(req.NativeConversation) || !explicitTitlePattern.MatchString(req.Title) || !filepath.IsAbs(req.OriginalHOME) || filepath.Clean(req.OriginalHOME) != req.OriginalHOME || len(req.UserAuthority) > 200 || len(req.FreezeEvidence) > 200 {
		return errors.New("explicit continuation request is incomplete")
	}
	return nil
}

func explicitSessionAllowed(s Settings, sess protocol.Session, req ExplicitClaudeRCRequest) error {
	if sess.Agent != protocol.AgentClaude || sess.Mode != protocol.ModeTask || sess.Workspace != nil || !sess.PrivateHistoryRetained || req.SessionUID != sess.SessionUID || !retainedPrivateTask(s, sess) {
		return errors.New("explicit continuation is only for the retained private Claude task on this pod")
	}
	if s.PodUID == "" || !protocol.ValidTaskBudgetIdentifier(s.PodUID) {
		return errors.New("explicit continuation does not match this pod")
	}
	if req.OriginalHOME != s.Home || filepath.Clean(s.Home) != s.Home || noSymlinkComponents(s.Home) != nil || privateManagedState(s) != nil {
		return errors.New("explicit continuation HOME does not match this pod")
	}
	return nil
}

func (rec ExplicitClaudeRCRecord) request() ExplicitClaudeRCRequest {
	return ExplicitClaudeRCRequest{Version: 1, RequestID: rec.RequestID, SessionUID: rec.SessionUID, NativeConversation: rec.NativeConversation, OriginalHOME: rec.OriginalHOME, Title: rec.Title, UserAuthority: rec.UserAuthority, FreezeEvidence: rec.FreezeEvidence}
}
func sameExplicitBinding(rec ExplicitClaudeRCRecord, req ExplicitClaudeRCRequest, sess protocol.Session, home string) bool {
	return rec.Version == 1 && rec.RequestID == req.RequestID && rec.Session == sess.Name && rec.SessionUID == req.SessionUID && rec.SessionUID == sess.SessionUID && rec.NativeConversation == req.NativeConversation && rec.OriginalHOME == home && rec.OriginalHOME == req.OriginalHOME && rec.Title == req.Title
}

func explicitPidState(s Settings) (string, error) {
	var p agentPid
	err := readPrivateManagedJSON(s, pidFile, 4096, &p)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	if err != nil || p.Pid <= 0 || p.Start == "" {
		return "", errors.New("agent stop proof is uncertain")
	}
	if pidAlive(p) {
		return "alive", nil
	}
	return "dead", nil
}

// Only metadata at the exact worktree-keyed transcript is inspected. No
// transcript content or authentication material is read by this operation.
func explicitTranscriptBytes(s Settings, worktree, conversation string) (int64, error) {
	path := filepath.Join(s.ClaudeConfigDir, "projects", claudeProjectKey(worktree), conversation+".jsonl")
	if !conversationIDPattern.MatchString(conversation) || s.ClaudeConfigDir == "" || noSymlinkComponents(filepath.Dir(path)) != nil {
		return 0, errors.New("native transcript is absent or not a regular nonempty file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, errors.New("native transcript is absent or not a regular nonempty file")
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() <= 0 || fi.Size() > explicitTranscriptMax {
		return 0, errors.New("native transcript is absent or not a regular nonempty file")
	}
	return fi.Size(), nil
}

func buildExplicitClaudeRCLaunch(s Settings, sess protocol.Session, ws protocol.Workspace, first Launch, req ExplicitClaudeRCRequest, bootID string, now time.Time) (Launch, error) {
	if !retainedPrivateTask(s, sess) {
		return Launch{}, errors.New("explicit continuation is only for the retained private Claude task on this pod")
	}
	remote := sess
	remote.Mode = protocol.ModeRemote
	if err := claudeCredentialCheck(s, remote); err != nil {
		return Launch{}, err
	}
	if err := validateCommonResume(s, sess, first); err != nil {
		return Launch{}, err
	}
	if first.Provider != protocol.AgentClaude || first.SessionUID != sess.SessionUID || first.PodUID != s.PodUID || first.ConversationID != req.NativeConversation || first.ConversationID == "" || bootID == "" {
		return Launch{}, errors.New("original launch is missing or does not match this conversation")
	}
	rules, err := providerProjectArgs(s, sess, interactiveGuard(ws, s.WorkDir()), true)
	if err != nil {
		return Launch{}, err
	}
	argv := append(claudeArgs(s, sess), "--resume", first.ConversationID, "--remote-control", req.Title)
	argv = append(argv, rules...)
	common, err := commonLaunchBinding(s, sess)
	if err != nil {
		return Launch{}, err
	}
	l := Launch{CommonInputs: common, Session: sess.Name, Provider: protocol.AgentClaude, SessionUID: sess.SessionUID, PodUID: s.PodUID,
		Argv: argv, Dir: ws.Worktree, Env: []string{"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=dev-env"}, Unset: append([]string(nil), unsetForRemote...),
		LogPath: s.LogPath(sess.Name), EventsPath: s.statePath(eventsFile), ConversationID: first.ConversationID,
		BootID: bootID, CreatedAt: now.UTC(), TUI: true, Resume: true,
		ExplicitClaudeRC: &ExplicitClaudeRCLaunch{Version: 1, RequestID: req.RequestID, SessionUID: req.SessionUID, NativeConversation: req.NativeConversation, Title: req.Title},
	}
	if !explicitInteractiveRC(l) {
		return Launch{}, errors.New("explicit continuation argv is not one interactive remote-control resume")
	}
	return l, nil
}

func explicitInteractiveRC(l Launch) bool {
	if l.ExplicitClaudeRC == nil || l.ExplicitClaudeRC.Version != 1 || !l.TUI || !l.Resume || l.Prompt != "" {
		return false
	}
	for _, banned := range []string{"-p", "--print", "--clear", "--fork", "--fork-session", "--session-id"} {
		if argvHas(l.Argv, banned) {
			return false
		}
	}
	title, ok := argvFlagValue(l.Argv, "--remote-control")
	conv, rok := argvFlagValue(l.Argv, "--resume")
	return ok && rok && argvFlagCount(l.Argv, "--remote-control") == 1 && argvFlagCount(l.Argv, "--resume") == 1 && title == l.ExplicitClaudeRC.Title && conv == l.ExplicitClaudeRC.NativeConversation && conv == l.ConversationID
}

func explicitRCAccepted(s Settings, sess protocol.Session, l Launch) bool {
	if sess.Mode != protocol.ModeTask || sess.Agent != protocol.AgentClaude || !explicitInteractiveRC(l) || l.ExplicitClaudeRC.SessionUID != sess.SessionUID {
		return false
	}
	rec, err := readExplicitClaudeRCRecord(s)
	b, readErr := regularPrivateBytes(s.statePath(resumeFile), 512<<10)
	return err == nil && readErr == nil && explicitDigest(b) == rec.ResumeSHA256 && rec.RequestID == l.ExplicitClaudeRC.RequestID && rec.NativeConversation == l.ConversationID && rec.SessionUID == sess.SessionUID && rec.Title == l.ExplicitClaudeRC.Title && rec.PodUID == s.PodUID
}

// This read prevents an explicit uncertain/live continuation being hidden by
// the original finished task. Interrupted is not terminal-task reclaim proof.
func explicitClaudeRCStatus(ctx context.Context, r Runner, s Settings, sess protocol.Session, now time.Time) (Launch, string, bool) {
	if _, err := os.Lstat(s.statePath(explicitClaudeRCFile)); errors.Is(err, os.ErrNotExist) {
		return Launch{}, "", false
	}
	rec, err := readExplicitClaudeRCRecord(s)
	var l Launch
	if err != nil || !sameExplicitBinding(rec, rec.request(), sess, s.Home) || rec.PodUID != s.PodUID || readPrivateManagedJSON(s, resumeFile, 512<<10, &l) != nil || !explicitRCAccepted(s, sess, l) {
		return Launch{}, explicitClaudeRCUncertain, true
	}
	a, ea := regularPrivateBytes(s.statePath(launchFile), 512<<10)
	b, eb := regularPrivateBytes(s.statePath(resultFile), 64<<10)
	if ea != nil || eb != nil || explicitDigest(a) != rec.OriginalLaunchSHA256 || explicitDigest(b) != rec.OriginalResultSHA256 {
		return Launch{}, explicitClaudeRCUncertain, true
	}
	state, _, _, err := observeExplicitClaudeRC(ctx, r, s, rec, now)
	if err != nil {
		return Launch{}, explicitClaudeRCUncertain, true
	}
	return l, state, true
}

func explicitDaemonMatches(s Settings, sess protocol.Session, boot bootRecord, self string) error {
	d := boot.Daemon
	if d == nil || d.SessionUID != sess.SessionUID || d.PodUID != s.PodUID || !pidAlive(agentPid{d.Pid, d.Start}) || !filepath.IsAbs(self) {
		return errors.New("explicit continuation requires the same running daemon binary; installation is not qualified")
	}
	a, errA := os.Stat(self)
	b, errB := os.Stat(fmt.Sprintf("/proc/%d/exe", d.Pid))
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		return errors.New("explicit continuation requires the same running daemon binary; side binaries are refused")
	}
	return nil
}

func readyExplicitBoot(s Settings) (bootRecord, error) {
	var boot bootRecord
	if readPrivateManagedJSON(s, bootFile, 64<<10, &boot) != nil || boot.BootID == "" || boot.Boot != protocol.BootReady || boot.Workspace == nil || boot.Workspace.Worktree == "" {
		return boot, errors.New("current boot workspace is unknown")
	}
	return boot, nil
}
func currentBootID(s Settings) (string, error) {
	var boot bootRecord
	if readPrivateManagedJSON(s, bootFile, 64<<10, &boot) != nil || boot.BootID == "" {
		return "", errors.New("current boot is unknown")
	}
	return boot.BootID, nil
}
func readExplicitClaudeRCRecord(s Settings) (ExplicitClaudeRCRecord, error) {
	var rec ExplicitClaudeRCRecord
	err := readPrivateManagedJSON(s, explicitClaudeRCFile, explicitClaudeRCMaxFile, &rec)
	return rec, err
}
func writeExplicitClaudeRCRecord(s Settings, rec ExplicitClaudeRCRecord) error {
	return writeWorkspaceJSON(s.statePath(explicitClaudeRCFile), rec)
}
func explicitDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func regularPrivateBytes(path string, bound int64) ([]byte, error) {
	if noSymlinkComponents(filepath.Dir(path)) != nil {
		return nil, errors.New("state path is not private")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() <= 0 || fi.Size() > bound {
		return nil, errors.New("state file is not a bounded private regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, bound+1))
	if err != nil || int64(len(b)) > bound {
		return nil, errors.New("state file exceeds bound")
	}
	return b, nil
}
func sameRegularBytes(path string, before []byte) (bool, error) {
	b, err := regularPrivateBytes(path, int64(len(before))+1)
	return string(b) == string(before), err
}
func tmuxAgentPresent(ctx context.Context, r Runner, s Settings) (bool, error) {
	_, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"has-session", "-t", "=" + TmuxSession}})
	if err == nil {
		return true, nil
	}
	var ce *CmdError
	if errors.As(err, &ce) && ce.ExitCode == 1 {
		return false, nil
	}
	return false, errors.New("tmux state is uncertain")
}
func argvHas(argv []string, flag string) bool { return argvFlagCount(argv, flag) > 0 }
func argvFlagCount(argv []string, flag string) int {
	n := 0
	for _, arg := range argv {
		if arg == flag {
			n++
		}
	}
	return n
}
func argvFlagValue(argv []string, flag string) (string, bool) {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) && argv[i+1] != "" && argv[i+1][0] != '-' {
			return argv[i+1], true
		}
	}
	return "", false
}
