package agentd

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Ordinary private instruction tests use a synthetic CLI and a local TLS
// fixture. They do not contact a model or prove model consumption.
func TestOrdinaryPrivateRunAgentReachesCheckpointConsumeAndOwnedEnd(t *testing.T) {
	s, l, hold := ordinaryHeldLaunch(t)
	const (
		key    = "correction-1"
		text   = "synthetic correction posted after launch\n"
		nonce  = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
		digest = "ababababababababababababababababababababababababababababababab"
		sender = "parent-session-uid"
	)
	var mu sync.Mutex
	var calls []string
	var consumed map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionInstructionAttemptPath(l.Session):
			if body["attemptID"] != l.UsageSource.AttemptID {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			phase := apiv1.InstructionPhaseActive
			if body["ended"] == true {
				phase = apiv1.InstructionPhaseEnded
			}
			writeOrdinaryJSON(w, http.StatusOK, apiv1.CoordinatorInstructionAttemptResult{
				AttemptID: l.UsageSource.AttemptID, Phase: phase, SessionUID: l.SessionUID, PodUID: l.PodUID,
			})
		case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionInstructionsPath(l.Session):
			writeOrdinaryJSON(w, http.StatusOK, apiv1.CoordinatorInstructionCheckpoint{
				Outcome: apiv1.InstructionOutcomeConfirmed, AttemptID: l.UsageSource.AttemptID, SessionUID: l.SessionUID, PodUID: l.PodUID,
				Pending: []apiv1.CoordinatorInstructionView{{
					CoordinatorInstructionReceipt: apiv1.CoordinatorInstructionReceipt{
						Outcome: apiv1.InstructionOutcomeConfirmed, Key: key, MessageUID: "m-uid", TextSHA256: digest,
						TargetSessionUID: l.SessionUID, TargetPodUID: l.PodUID, AttemptID: l.UsageSource.AttemptID,
						Project: "sample", Repository: "alias", TaskKey: "task:ordinary",
					},
					SenderUID: sender, Text: text, Nonce: nonce,
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionInstructionConsumePath(l.Session, key):
			consumed = body
			writeOrdinaryJSON(w, http.StatusCreated, apiv1.CoordinatorInstructionConsumeACK{
				Outcome: apiv1.InstructionOutcomeConsumed, Key: key, MessageUID: "m-uid", AttemptID: l.UsageSource.AttemptID,
				Nonce: nonce, TextSHA256: digest, TargetSessionUID: l.SessionUID, TargetPodUID: l.PodUID,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	bindOrdinaryAPI(t, &s, srv, true)
	done := make(chan error, 1)
	go func() {
		defer func() { _, _ = hold.WriteString("release\n") }()
		deadline := time.Now().Add(12 * time.Second)
		registered := false
		for !registered && time.Now().Before(deadline) {
			mu.Lock()
			registered = len(calls) > 0
			mu.Unlock()
			if !registered {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if !registered {
			done <- errors.New("ordinary registration did not run while the owned process was live")
			return
		}
		if _, err := os.Stat(s.statePath(nativeInvocationFile)); !errors.Is(err, os.ErrNotExist) {
			done <- errors.New("ordinary task wrote native-invocation.json")
			return
		}
		client, err := NewInboxClient(s)
		if err != nil {
			done <- err
			return
		}
		got, err := client.CheckpointInstructions(context.Background(), s)
		if err != nil || len(got.Pending) != 1 || got.Pending[0].SenderUID != sender || got.Pending[0].Nonce != nonce || got.Pending[0].TextSHA256 != digest || got.AttemptID != l.UsageSource.AttemptID {
			done <- errors.New("checkpoint did not return the posted correction for the live ordinary attempt")
			return
		}
		ack, err := client.ConsumeInstruction(context.Background(), s, key, nonce, "m-uid", digest)
		if err != nil || ack.Outcome != apiv1.InstructionOutcomeConsumed || ack.Nonce != nonce || ack.TextSHA256 != digest || ack.MessageUID != "m-uid" {
			done <- errors.New("consumption did not correlate the checkpoint uid, digest and nonce")
			return
		}
		done <- nil
	}()
	pane := &syncBuffer{}
	code := RunAgent(s.statePath(launchFile), pane, make(chan os.Signal), 200*time.Millisecond)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if code != 0 || l.ChildDecisions || len(l.UsageSource.AttemptID) != 64 {
		t.Fatalf("ordinary task %d attempt %s child %v pane %q", code, l.UsageSource.AttemptID, l.ChildDecisions, pane.String())
	}
	if _, err := os.Stat(s.statePath(nativeInvocationFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ordinary end wrote native-invocation.json")
	}
	if _, err := os.Stat(s.statePath(instructionEndUnconfirmedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("confirmed end recorded an unconfirmed protocol file")
	}
	mu.Lock()
	joined := strings.Join(calls, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "POST "+apiv1.SessionInstructionAttemptPath(l.Session)) || !strings.Contains(joined, "GET "+apiv1.SessionInstructionsPath(l.Session)) || !strings.Contains(joined, "POST "+apiv1.SessionInstructionConsumePath(l.Session, key)) {
		t.Fatalf("lifecycle calls %v", calls)
	}
	if consumed["attemptID"] != l.UsageSource.AttemptID || consumed["nonce"] != nonce || consumed["targetSessionUID"] != l.SessionUID || consumed["targetPodUID"] != l.PodUID {
		t.Fatalf("consume body %+v", consumed)
	}
	if _, err := ActiveInstructionAttempt(s); err == nil {
		t.Fatal("ended ordinary process remained an active attempt")
	}
}

func TestOrdinaryInstructionEndBlipPreservesCLIExit(t *testing.T) {
	s, l, hold := ordinaryHeldLaunch(t)
	registered := make(chan struct{})
	var once sync.Once
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["ended"] == true {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"synthetic end blip"}}`))
			return
		}
		writeOrdinaryJSON(w, http.StatusOK, apiv1.CoordinatorInstructionAttemptResult{
			AttemptID: l.UsageSource.AttemptID, Phase: apiv1.InstructionPhaseActive, SessionUID: l.SessionUID, PodUID: l.PodUID,
		})
		once.Do(func() { close(registered) })
	}))
	t.Cleanup(srv.Close)
	bindOrdinaryAPI(t, &s, srv, true)
	go func() {
		select {
		case <-registered:
		case <-time.After(8 * time.Second):
		}
		_, _ = hold.WriteString("release\n")
	}()
	pane := &syncBuffer{}
	code := RunAgent(s.statePath(launchFile), pane, make(chan os.Signal), 200*time.Millisecond)
	raw, err := os.ReadFile(s.statePath(instructionEndUnconfirmedFile))
	if code != 0 || err != nil || !strings.Contains(string(raw), l.UsageSource.AttemptID) || !strings.Contains(string(raw), `"serverEnd": "Unconfirmed"`) || !strings.Contains(pane.String(), "native instruction attempt end is unconfirmed") {
		t.Fatalf("end blip changed the CLI exit or skipped the local record: %d %s %v pane %q", code, raw, err, pane.String())
	}
	if _, err := ActiveInstructionAttempt(s); err == nil {
		t.Fatal("unconfirmed end file was treated as an active attempt")
	}
}

func TestOrdinaryInstructionDisabledMakesNoRequest(t *testing.T) {
	s, _, _ := ordinaryHeldLaunch(t)
	called := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	bindOrdinaryAPI(t, &s, srv, false)
	cli := fakeCLI(t, t.TempDir(), `read -r prompt; exit 0`)
	var l Launch
	if err := readJSONFile(s.statePath(launchFile), &l); err != nil {
		t.Fatal(err)
	}
	l.Argv = []string{cli}
	l.Env = nil
	if err := writeJSONFile(s.statePath(launchFile), l); err != nil {
		t.Fatal(err)
	}
	if code := RunAgent(s.statePath(launchFile), &syncBuffer{}, make(chan os.Signal), 200*time.Millisecond); code != 0 || called {
		t.Fatalf("disabled ordinary launch called the API or failed: %d %v", code, called)
	}
	if _, err := os.Stat(s.statePath(nativeInvocationFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled launch wrote a native invocation")
	}
}

func TestOrdinaryInstructionRefusesForgedStaleAndReplacedBindings(t *testing.T) {
	s, l, cmd := ordinaryLiveProcess(t)
	got, err := ActiveInstructionAttempt(s)
	if err != nil || got.InvocationID != l.UsageSource.AttemptID || len(got.InvocationID) != 64 {
		t.Fatalf("live ordinary attempt %v %v", got, err)
	}
	s.PodUID = "other-pod"
	if _, err := ActiveInstructionAttempt(s); err == nil {
		t.Fatal("wrong pod was accepted")
	}
	s.PodUID = l.PodUID
	intent := filepath.Join(s.StateDir, initialUsageDir, "intent.json")
	original, err := os.ReadFile(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(intent, []byte(`{"source":{"attemptID":"`+strings.Repeat("ff", 32)+`"},"launchSHA256":"`+strings.Repeat("ee", 32)+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ActiveInstructionAttempt(s); err == nil {
		t.Fatal("forged intent was accepted")
	}
	if err := os.Remove(intent); err != nil {
		t.Fatal(err)
	}
	if _, err := ActiveInstructionAttempt(s); err == nil {
		t.Fatal("missing intent was accepted")
	}
	if err := os.WriteFile(intent, original, 0o600); err != nil {
		t.Fatal(err)
	}
	launchPath := s.statePath(launchFile)
	saved, err := os.ReadFile(launchPath)
	if err != nil {
		t.Fatal(err)
	}
	replaced := l
	source := *l.UsageSource
	source.AttemptID = strings.Repeat("ab", 32)
	replaced.UsageSource = &source
	if err := writeJSONFile(launchPath, replaced); err != nil {
		t.Fatal(err)
	}
	if _, err := ActiveInstructionAttempt(s); err == nil {
		t.Fatal("replaced attempt id was accepted")
	}
	if err := os.WriteFile(launchPath, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	stale := nativeInvocation{Version: 1, Phase: "Exited", StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(), LeaderReaped: true, GroupAbsent: true}
	if err := writeJSONFile(s.statePath(nativeInvocationFile), stale); err != nil {
		t.Fatal(err)
	}
	if _, err := ActiveInstructionAttempt(s); err == nil {
		t.Fatal("stale native file fell through to the ordinary attempt")
	}
	if err := os.Remove(s.statePath(nativeInvocationFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := ActiveInstructionAttempt(s); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
	_, _ = cmd.Process.Wait()
	if _, err := ActiveInstructionAttempt(s); err == nil {
		t.Fatal("exited process remained active")
	}
}

func ordinaryHeldLaunch(t *testing.T) (Settings, Launch, *os.File) {
	t.Helper()
	s, l := ordinaryLaunchFiles(t)
	holdPath := filepath.Join(t.TempDir(), "hold")
	if err := syscall.Mkfifo(holdPath, 0o600); err != nil {
		t.Fatal(err)
	}
	hold, err := os.OpenFile(holdPath, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Close() })
	cli := fakeCLI(t, t.TempDir(), `read -r prompt
[ "$prompt" = "ordinary private task" ] || exit 9
read -r ready < "$HOLD" || exit 3
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"synthetic","session_id":"conv-ordinary"}'
exit 0`)
	l.Argv = []string{cli}
	l.Env = []string{"HOLD=" + holdPath}
	if err := writeJSONFile(s.statePath(launchFile), l); err != nil {
		t.Fatal(err)
	}
	var stored Launch
	if err := readJSONFile(s.statePath(launchFile), &stored); err != nil {
		t.Fatal(err)
	}
	return s, stored, hold
}

func ordinaryLiveProcess(t *testing.T) (Settings, Launch, *exec.Cmd) {
	t.Helper()
	s, _ := ordinaryLaunchFiles(t)
	var stored Launch
	if err := readJSONFile(s.statePath(launchFile), &stored); err != nil {
		t.Fatal(err)
	}
	if err := beginInitialUsage(stored, s.StateDir); err != nil {
		t.Fatal(err)
	}
	cmd := ordinaryProcess(t)
	if err := writeJSONFile(s.statePath(pidFile), agentPid{Pid: cmd.Process.Pid, Start: procStartTime(cmd.Process.Pid)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	return s, stored, cmd
}

func ordinaryProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func ordinaryLaunchFiles(t *testing.T) (Settings, Launch) {
	t.Helper()
	home := t.TempDir()
	s := testSettings(t, home)
	s.PodUID = "pod-ordinary-1"
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.WorkDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	l := Launch{
		Session: "child", Provider: protocol.AgentClaude, BootID: "boot-ordinary", CreatedAt: now,
		Prompt: "ordinary private task\n", Dir: s.WorkDir(), CoordinatorInstructions: true,
		LogPath: s.LogPath("child"), EventsPath: s.statePath(eventsFile),
	}
	sess := protocol.Session{Name: "child", Agent: protocol.AgentClaude, Mode: protocol.ModeTask, Model: "claude-opus-5-5", SessionUID: "session-ordinary-1"}
	if err := bindInitialUsage(&l, sess, s.PodUID); err != nil || l.UsageSource == nil || !l.UsageSource.Valid() {
		t.Fatalf("usage source %v %v", l.UsageSource, err)
	}
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: l.BootID, BootedAt: now, Boot: protocol.BootReady}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(launchFile), l); err != nil {
		t.Fatal(err)
	}
	return s, l
}

func bindOrdinaryAPI(t *testing.T, s *Settings, srv *httptest.Server, enabled bool) {
	t.Helper()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("synthetic-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.APIURL, s.APICAFile, s.APITokenFile = srv.URL, ca, token
	t.Setenv("HOME", s.Home)
	t.Setenv(protocol.PodUIDEnv, s.PodUID)
	t.Setenv("AGENTD_API_URL", srv.URL)
	t.Setenv("AGENTD_API_CA_FILE", ca)
	t.Setenv("AGENTD_API_TOKEN_FILE", token)
	if enabled {
		t.Setenv("AGENTD_ENABLE_COORDINATOR_INSTRUCTIONS", "true")
	} else {
		t.Setenv("AGENTD_ENABLE_COORDINATOR_INSTRUCTIONS", "false")
	}
}

func writeOrdinaryJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
