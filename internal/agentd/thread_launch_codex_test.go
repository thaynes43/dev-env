package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	oldThread = "0199a000-0000-7000-8000-000000000001"
	newThread = "0199a000-0000-7000-8000-000000000002"
	twoThread = "0199a000-0000-7000-8000-000000000003"
	turnID    = "0199a000-0000-7000-8000-0000000000aa"
)

var threadNow = time.Date(2026, 11, 9, 18, 0, 0, 0, time.UTC)

// threadFake is a scripted host: daemon, pairing, thread lists and turns. All
// is synthetic; no client, tmux, daemon or account is touched.
type threadFake struct {
	mu       sync.Mutex
	now      time.Time
	unpaired bool
	noAccess bool
	daemon   string
	remote   string
	// all and shown answer the all-sources and default lists after a launch.
	all, shown []codexThreadSummary
	turns      []codexTurnObservation // one per poll; the last repeats
	listErrs   int
	state      string
	launches   []codexWrapperCall
	polls      int
}

func (f *threadFake) deps() codexThreadDeps {
	return codexThreadDeps{
		access: func(Settings, time.Time) error {
			if f.noAccess {
				return errors.New("no projection")
			}
			return nil
		},
		daemon: func(Settings) (codexHostProcess, error) {
			if f.daemon != "" {
				return codexHostProcess{State: f.daemon}, nil
			}
			return codexHostProcess{State: "Live", PID: 4242}, nil
		},
		remote: func(_ context.Context, _ Settings, pid int) (string, error) {
			if pid != 4242 {
				return "", errors.New("wrong pid")
			}
			if f.remote != "" {
				return f.remote, nil
			}
			return "connected", nil
		},
		paired: func(Settings) bool { return !f.unpaired },
		list: func(_ context.Context, _ Settings, pid int, cwd string, all bool) ([]codexThreadSummary, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.launches) == 0 {
				return []codexThreadSummary{{ID: oldThread, Cwd: cwd, Source: "vscode", CreatedAt: threadNow.Unix() - 600}}, nil
			}
			if f.listErrs > 0 {
				f.listErrs--
				return nil, errCodexHostUnknown
			}
			if all {
				return f.all, nil
			}
			return f.shown, nil
		},
		turn: func(_ context.Context, _ Settings, _ int, id string) (codexTurnObservation, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if id != newThread {
				return codexTurnObservation{}, errors.New("wrong thread")
			}
			i := min(f.polls, len(f.turns)-1)
			f.polls++
			return f.turns[i], nil
		},
		launch: func(_ context.Context, c codexWrapperCall) ([]byte, error) {
			f.launches = append(f.launches, c)
			state := f.state
			if state == "" {
				state = "client-running-unverified"
			}
			if state == "garbage" {
				return []byte("not json"), nil
			}
			return []byte(`{"state":"` + state + `","name":"x","detail":"d"}` + "\n"), nil
		},
		python: func() (string, error) { return "/usr/bin/python3", nil },
		now:    func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
		sleep: func(ctx context.Context, d time.Duration) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			f.mu.Lock()
			f.now = f.now.Add(d)
			f.mu.Unlock()
			return nil
		},
	}
}

func threadHost(t *testing.T) (Settings, protocol.Session, string) {
	t.Helper()
	home := t.TempDir()
	s := Settings{Home: home, CodexHome: filepath.Join(home, ".codex"), CodexBin: "codex", StateDir: filepath.Join(home, ".agentd")}
	cwd := s.ClonePath("dev-env")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	sess := protocol.Session{Name: "codex-host-1", Repo: "dev-env", Agent: protocol.AgentCodex, Mode: protocol.ModeRemote, Model: "gpt-6-astra"}
	return s, sess, cwd
}

func goodThreadRequest() CodexThreadRequest {
	return CodexThreadRequest{Model: "gpt-6-astra", Effort: "xhigh", Name: "thread-1109-180000", Handoff: "Make a worktree, then summarize the open PRs.", Wait: time.Minute}
}

func newThreadFake(cwd string) *threadFake {
	fresh := codexThreadSummary{ID: newThread, Cwd: cwd, Source: "vscode", Preview: "Make a worktree", CreatedAt: threadNow.Unix()}
	old := codexThreadSummary{ID: oldThread, Cwd: cwd, Source: "vscode", CreatedAt: threadNow.Unix() - 600}
	return &threadFake{now: threadNow, all: []codexThreadSummary{fresh, old}, shown: []codexThreadSummary{fresh, old},
		turns: []codexTurnObservation{{}, {TurnID: turnID, Status: "inProgress"}, {TurnID: turnID, Status: "completed", AgentMessage: true}}}
}

func launchThread(t *testing.T, f *threadFake, s Settings, sess protocol.Session, req CodexThreadRequest) apiv1.ThreadLaunchResult {
	t.Helper()
	res := startCodexThread(context.Background(), s, sess, req, f.deps())
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	back, err := apiv1.DecodeThreadLaunch(b)
	if err != nil {
		t.Fatalf("result does not decode: %v\n%s", err, b)
	}
	if strings.Contains(string(b), req.Handoff) {
		t.Fatal("the result must never carry the handoff")
	}
	if len(f.launches) > 1 {
		t.Fatal("the wrapper ran more than once")
	}
	return back
}

// The full route: a ready, paired host, one wrapper launch, a new vscode
// thread in the default list, then its first agent message.
func TestCodexThreadResponded(t *testing.T) {
	s, sess, cwd := threadHost(t)
	f := newThreadFake(cwd)
	got := launchThread(t, f, s, sess, goodThreadRequest())
	if got.Permission == nil || got.Permission.Mode != apiv1.ThreadPermissionAlwaysAllow || got.Permission.Flag != "--dangerously-bypass-approvals-and-sandbox" {
		t.Fatalf("permission %+v", got.Permission)
	}
	if got.Outcome != apiv1.ThreadResponded || got.Error != nil || got.ThreadID != newThread || got.Source != "vscode" || got.Cwd != cwd || got.Host != sess.Name {
		t.Fatalf("result %+v", got)
	}
	for phase, state := range map[string]string{
		apiv1.ThreadPhaseHostReady: "ok", apiv1.ThreadPhasePaired: "unknown", apiv1.ThreadPhaseClientStarted: "ok",
		apiv1.ThreadPhaseThreadCreated: "ok", apiv1.ThreadPhaseSourceVisible: "ok", apiv1.ThreadPhaseHandoff: "ok", apiv1.ThreadPhaseFirstResponse: "ok",
	} {
		if got.Phases[phase].State != state {
			t.Errorf("%s = %+v, want %s", phase, got.Phases[phase], state)
		}
	}
	// The one launch is the packaged wrapper (the --remote TUI): the handoff on
	// stdin only, no exec or queue subcommand, no sandbox or approval flag.
	c := f.launches[0]
	if c.Argv[3] != CodexThreadWrapper || string(c.Stdin) != goodThreadRequest().Handoff {
		t.Fatalf("launch %q", c.Argv)
	}
	joined := strings.Join(c.Argv, " ")
	for _, banned := range []string{" exec", " queue", "-s ", "--sandbox", "-a ", "--ask-for-approval", "--approve-for-me", "--full-auto", goodThreadRequest().Handoff} {
		if strings.Contains(joined, banned) {
			t.Errorf("launch argv carries %q: %s", banned, joined)
		}
	}
	socket := filepath.Join(s.CodexHome, "app-server-control", "app-server-control.sock")
	for _, pair := range [][2]string{{"--repo", cwd}, {"--socket", socket}, {"--model", "gpt-6-astra"}, {"--effort", "xhigh"}, {"--name", "thread-1109-180000"}, {"--client", "codex"}, {"--task-file", "-"}} {
		i := slices.Index(c.Argv, pair[0])
		if i < 0 || c.Argv[i+1] != pair[1] {
			t.Errorf("argv %s = %q", pair[0], c.Argv)
		}
	}
	if !slices.Contains(c.Env, "HOME="+s.Home) || !slices.Contains(c.Env, "CODEX_HOME="+s.CodexHome) {
		t.Errorf("env lacks HOME/CODEX_HOME")
	}
}

// Regression for OP481 item 2: a thread with a source the phone hides (exec)
// is reported hidden, never responded or visible.
func TestCodexThreadHiddenSourceNeverSucceeds(t *testing.T) {
	for _, source := range []string{"exec", "appServer", "subAgent"} {
		t.Run(source, func(t *testing.T) {
			s, sess, cwd := threadHost(t)
			f := newThreadFake(cwd)
			f.all[0].Source = source
			f.shown = f.shown[1:]
			got := launchThread(t, f, s, sess, goodThreadRequest())
			if got.Outcome != apiv1.ThreadHidden || got.Error.Code != "hidden_source" || got.Phases[apiv1.ThreadPhaseThreadCreated].State != "ok" || got.Phases[apiv1.ThreadPhaseSourceVisible].State != "error" {
				t.Fatalf("result %+v", got)
			}
			if apiv1.ThreadLaunchExit(got.Outcome) == 0 {
				t.Fatal("a hidden thread must not exit 0")
			}
		})
	}
}

// Preflight refusals create nothing: the wrapper never runs.
func TestCodexThreadPreflightRefusals(t *testing.T) {
	cases := []struct {
		name          string
		mutate        func(*threadFake, *protocol.Session, *CodexThreadRequest, Settings)
		outcome, code string
	}{
		{"claude host", func(_ *threadFake, s *protocol.Session, _ *CodexThreadRequest, _ Settings) {
			s.Agent = protocol.AgentClaude
		}, apiv1.ThreadFailed, "not_codex_host"},
		{"codex task", func(_ *threadFake, s *protocol.Session, _ *CodexThreadRequest, _ Settings) {
			s.Mode = protocol.ModeTask
		}, apiv1.ThreadFailed, "not_codex_host"},
		{"unpaired", func(f *threadFake, _ *protocol.Session, _ *CodexThreadRequest, _ Settings) { f.unpaired = true }, apiv1.ThreadActionRequired, "pairing_required"},
		{"no access", func(f *threadFake, _ *protocol.Session, _ *CodexThreadRequest, _ Settings) { f.noAccess = true }, apiv1.ThreadActionRequired, "login_required"},
		{"daemon absent", func(f *threadFake, _ *protocol.Session, _ *CodexThreadRequest, _ Settings) { f.daemon = "Absent" }, apiv1.ThreadFailed, "daemon_unavailable"},
		{"disconnected", func(f *threadFake, _ *protocol.Session, _ *CodexThreadRequest, _ Settings) { f.remote = "errored" }, apiv1.ThreadFailed, "remote_disconnected"},
		{"no repo dir", func(_ *threadFake, _ *protocol.Session, r *CodexThreadRequest, _ Settings) { r.Repo = "haynes-ops" }, apiv1.ThreadFailed, "repo_unavailable"},
		{"bad repo", func(_ *threadFake, _ *protocol.Session, r *CodexThreadRequest, _ Settings) { r.Repo = "../x" }, apiv1.ThreadFailed, "invalid_input"},
		{"symlinked repo", func(_ *threadFake, _ *protocol.Session, r *CodexThreadRequest, s Settings) {
			_ = os.Symlink(s.ClonePath("dev-env"), s.ClonePath("linked"))
			r.Repo = "linked"
		}, apiv1.ThreadFailed, "repo_unavailable"},
		{"bad name", func(_ *threadFake, _ *protocol.Session, r *CodexThreadRequest, _ Settings) { r.Name = "-x" }, apiv1.ThreadFailed, "invalid_input"},
		{"alias model", func(_ *threadFake, _ *protocol.Session, r *CodexThreadRequest, _ Settings) { r.Model = "gpt" }, apiv1.ThreadFailed, "invalid_input"},
		{"wrapper effort", func(_ *threadFake, _ *protocol.Session, r *CodexThreadRequest, _ Settings) { r.Effort = "ultra" }, apiv1.ThreadFailed, "invalid_input"},
		{"no effort", func(_ *threadFake, _ *protocol.Session, r *CodexThreadRequest, _ Settings) { r.Effort = "" }, apiv1.ThreadFailed, "invalid_input"},
		{"blank handoff", func(_ *threadFake, _ *protocol.Session, r *CodexThreadRequest, _ Settings) { r.Handoff = " \n" }, apiv1.ThreadFailed, "invalid_input"},
		{"long handoff", func(_ *threadFake, _ *protocol.Session, r *CodexThreadRequest, _ Settings) {
			r.Handoff = strings.Repeat("x", 4097)
		}, apiv1.ThreadFailed, "invalid_input"},
		{"nul handoff", func(_ *threadFake, _ *protocol.Session, r *CodexThreadRequest, _ Settings) { r.Handoff = "a\x00b" }, apiv1.ThreadFailed, "invalid_input"},
		{"wait", func(_ *threadFake, _ *protocol.Session, r *CodexThreadRequest, _ Settings) { r.Wait = 11 * time.Minute }, apiv1.ThreadFailed, "invalid_input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, cwd := threadHost(t)
			f := newThreadFake(cwd)
			req := goodThreadRequest()
			tc.mutate(f, &sess, &req, s)
			got := launchThread(t, f, s, sess, req)
			if got.Outcome != tc.outcome || got.Error == nil || got.Error.Code != tc.code || len(f.launches) != 0 {
				t.Fatalf("got %s/%+v launches %d", got.Outcome, got.Error, len(f.launches))
			}
		})
	}
}

// Every wrapper state maps to one outcome. Only a running client goes on;
// an unknown answer is unconfirmed and never retried.
func TestCodexThreadWrapperStates(t *testing.T) {
	for state, want := range map[string][2]string{
		"invalid-input":            {apiv1.ThreadFailed, "invalid_input"},
		"name-consumed":            {apiv1.ThreadFailed, "name_consumed"},
		"prerequisite-unavailable": {apiv1.ThreadFailed, "client_unavailable"},
		"unsupported-client":       {apiv1.ThreadFailed, "client_unavailable"},
		"client-not-running":       {apiv1.ThreadUnconfirmed, "client_not_running"},
		"launch-unconfirmed":       {apiv1.ThreadUnconfirmed, "launch_unconfirmed"},
		"dry-run":                  {apiv1.ThreadUnconfirmed, "launch_unconfirmed"},
		"garbage":                  {apiv1.ThreadUnconfirmed, "launch_unconfirmed"},
	} {
		t.Run(state, func(t *testing.T) {
			s, sess, cwd := threadHost(t)
			f := newThreadFake(cwd)
			f.state = state
			got := launchThread(t, f, s, sess, goodThreadRequest())
			if got.Outcome != want[0] || got.Error.Code != want[1] || got.Error.Phase != apiv1.ThreadPhaseClientStarted {
				t.Fatalf("got %s/%+v", got.Outcome, got.Error)
			}
		})
	}
}

func TestCodexThreadObservationEnds(t *testing.T) {
	t.Run("no thread appears", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		f := newThreadFake(cwd)
		f.all = f.all[1:]
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadTimedOut || got.Error.Code != "thread_not_observed" {
			t.Fatalf("got %s/%+v", got.Outcome, got.Error)
		}
	})
	t.Run("old thread at the same cwd is not new", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		f := newThreadFake(cwd)
		f.all = []codexThreadSummary{{ID: twoThread, Cwd: cwd, Source: "vscode", CreatedAt: threadNow.Unix() - 30}}
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadTimedOut || got.ThreadID != "" {
			t.Fatalf("got %s %s", got.Outcome, got.ThreadID)
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		f := newThreadFake(cwd)
		f.all = append(f.all, codexThreadSummary{ID: twoThread, Cwd: cwd, Source: "vscode", Preview: "something else", CreatedAt: threadNow.Unix()})
		f.all[0].Preview = "also different"
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadUnconfirmed || got.Error.Code != "thread_ambiguous" {
			t.Fatalf("got %s/%+v", got.Outcome, got.Error)
		}
	})
	t.Run("preview narrows two", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		f := newThreadFake(cwd)
		f.all = append(f.all, codexThreadSummary{ID: twoThread, Cwd: cwd, Source: "vscode", Preview: "something else", CreatedAt: threadNow.Unix()})
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadResponded || got.ThreadID != newThread {
			t.Fatalf("got %s %s", got.Outcome, got.ThreadID)
		}
	})
	t.Run("turn failed", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		f := newThreadFake(cwd)
		f.turns = []codexTurnObservation{{TurnID: turnID, Status: "failed"}}
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadFailed || got.Error.Code != "turn_failed" || got.Phases[apiv1.ThreadPhaseHandoff].State != "ok" {
			t.Fatalf("got %s/%+v", got.Outcome, got.Error)
		}
	})
	t.Run("completed without message", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		f := newThreadFake(cwd)
		f.turns = []codexTurnObservation{{TurnID: turnID, Status: "completed"}}
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadVisible || got.Error.Code != "response_not_observable" {
			t.Fatalf("got %s/%+v", got.Outcome, got.Error)
		}
	})
	t.Run("response pending at the deadline", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		f := newThreadFake(cwd)
		f.turns = []codexTurnObservation{{TurnID: turnID, Status: "inProgress"}}
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadVisible || got.Error.Code != "response_pending" || apiv1.ThreadLaunchExit(got.Outcome) != 7 {
			t.Fatalf("got %s/%+v", got.Outcome, got.Error)
		}
	})
	t.Run("two observation errors are tolerated", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		f := newThreadFake(cwd)
		f.listErrs = 2
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadResponded {
			t.Fatalf("got %s/%+v", got.Outcome, got.Error)
		}
	})
	t.Run("three are unconfirmed", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		f := newThreadFake(cwd)
		f.listErrs = 3
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadUnconfirmed || got.Error.Code != "observation_unavailable" {
			t.Fatalf("got %s/%+v", got.Outcome, got.Error)
		}
	})
}

// threadRPCFixture is an inert in-process app-server on a Unix socket. It
// records each request's params; answer gives the result.
func threadRPCFixture(t *testing.T, answer func(method string, params map[string]any) any) (Settings, *[]map[string]any) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "thread-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	dir := filepath.Join(home, "app-server-control")
	if os.Mkdir(dir, 0o700) != nil {
		t.Fatal("rpc directory")
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "app-server-control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := []map[string]any{}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			var request struct {
				ID     *int           `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if conn.ReadJSON(&request) != nil {
				return
			}
			if request.ID == nil {
				continue
			}
			mu.Lock()
			seen = append(seen, map[string]any{"method": request.Method, "params": request.Params})
			mu.Unlock()
			result := answer(request.Method, request.Params)
			if request.Method == "initialize" {
				result = map[string]any{"userAgent": "codex/0.160.1 fixture"}
			}
			if conn.WriteJSON(map[string]any{"id": *request.ID, "result": result}) != nil {
				return
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return Settings{CodexHome: home}, &seen
}

func TestCodexThreadObserverRPC(t *testing.T) {
	const cwd = "/home/dev/repos/dev-env"
	big := strings.Repeat("p", 70<<10) // over the 64 KiB default frame bound
	s, seen := threadRPCFixture(t, func(method string, params map[string]any) any {
		switch method {
		case "thread/list":
			return map[string]any{"data": []any{
				map[string]any{"id": newThread, "cwd": cwd, "source": "vscode", "preview": big, "createdAt": 1762711200},
				map[string]any{"id": twoThread, "cwd": cwd, "source": map[string]any{"custom": "x"}, "preview": "", "createdAt": 1762711100},
				map[string]any{"id": oldThread, "cwd": cwd, "source": "weird value!", "preview": "", "createdAt": 1762711000},
			}, "nextCursor": nil}
		case "thread/turns/list":
			return map[string]any{"data": []any{map[string]any{"id": turnID, "status": "completed", "items": []any{}}}}
		case "thread/items/list":
			return map[string]any{"data": []any{
				map[string]any{"type": "commandExecution", "id": "i2"},
				map[string]any{"type": "agentMessage", "id": "i1", "text": "Done."},
			}}
		}
		return nil
	})
	got, err := listCodexThreads(context.Background(), s, os.Getpid(), cwd, false)
	if err != nil || len(got) != 3 || got[0].Source != "vscode" || got[1].Source != "custom" || got[2].Source != "unknown" || got[0].CreatedAt != 1762711200 {
		t.Fatalf("list %v %+v", err, got)
	}
	if _, err := listCodexThreads(context.Background(), s, os.Getpid(), cwd, true); err != nil {
		t.Fatal(err)
	}
	obs, err := readCodexFirstTurn(context.Background(), s, os.Getpid(), newThread)
	if err != nil || obs.TurnID != turnID || obs.Status != "completed" || !obs.AgentMessage {
		t.Fatalf("turn %v %+v", err, obs)
	}
	var lists []map[string]any
	for _, r := range *seen {
		if r["method"] == "thread/list" {
			lists = append(lists, r["params"].(map[string]any))
		}
	}
	// The default list sends no sourceKinds (the phone's interactive default);
	// the all-sources list names every kind, exec included.
	if len(lists) != 2 || lists[0]["sourceKinds"] != nil || lists[0]["cwd"] != cwd {
		t.Fatalf("default list params %v", lists)
	}
	kinds, _ := lists[1]["sourceKinds"].([]any)
	if len(kinds) != len(codexAllSourceKinds) || !slices.Contains(kinds, any("exec")) {
		t.Fatalf("all-sources params %v", lists[1])
	}
	if _, err := listCodexThreads(context.Background(), s, os.Getpid()+1, cwd, false); err == nil {
		t.Fatal("a socket peer other than the daemon was accepted")
	}
	if _, err := readCodexFirstTurn(context.Background(), s, os.Getpid(), "not-a-thread"); err == nil {
		t.Fatal("a malformed thread id was sent")
	}
}

func TestCodexThreadObserverRefusesMalformed(t *testing.T) {
	for name, answer := range map[string]func(string) any{
		"no data": func(string) any { return map[string]any{} },
		"bad id": func(string) any {
			return map[string]any{"data": []any{map[string]any{"id": "x", "cwd": "/a", "source": "vscode", "createdAt": 1}}}
		},
		"no createdAt": func(string) any {
			return map[string]any{"data": []any{map[string]any{"id": newThread, "cwd": "/a", "source": "vscode"}}}
		},
		"relative cwd": func(string) any {
			return map[string]any{"data": []any{map[string]any{"id": newThread, "cwd": "a", "source": "vscode", "createdAt": 1}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := threadRPCFixture(t, func(string, map[string]any) any { return answer("") })
			if _, err := listCodexThreads(context.Background(), s, os.Getpid(), "/a", false); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	s, _ := threadRPCFixture(t, func(method string, _ map[string]any) any {
		return map[string]any{"data": []any{map[string]any{"id": turnID, "status": "paused"}}}
	})
	if _, err := readCodexFirstTurn(context.Background(), s, os.Getpid(), newThread); err == nil {
		t.Fatal("unknown turn status accepted")
	}
	s, _ = threadRPCFixture(t, func(string, map[string]any) any { return map[string]any{"data": []any{}} })
	if obs, err := readCodexFirstTurn(context.Background(), s, os.Getpid(), newThread); err != nil || obs.TurnID != "" {
		t.Fatal("no turn yet must be an empty observation")
	}
}

// The observer's requests and fields exist in the pinned 0.160.1 protocol.
func TestCodexThreadPinnedSchema(t *testing.T) {
	read := func(name, digest string) map[string]any {
		raw, err := os.ReadFile(filepath.Join("testdata", "codex_thread_launch", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != digest {
			t.Fatal("pinned schema changed", name)
		}
		var schema map[string]any
		if json.Unmarshal(raw, &schema) != nil {
			t.Fatal("schema JSON", name)
		}
		return schema
	}
	props := func(m map[string]any) map[string]any { return m["properties"].(map[string]any) }
	defs := func(m map[string]any) map[string]any { return m["definitions"].(map[string]any) }
	list := read("ThreadListParams", "5daf371b2852c409d4405c95f79cddb2d92a1ee89da808b2c5112ef16a57432c")
	for _, f := range []string{"cwd", "limit", "sourceKinds"} {
		if props(list)[f] == nil {
			t.Fatal("thread/list lacks", f)
		}
	}
	if !strings.Contains(props(list)["sourceKinds"].(map[string]any)["description"].(string), "defaults to interactive sources") {
		t.Fatal("sourceKinds default changed")
	}
	var kinds []string
	for _, k := range defs(list)["ThreadSourceKind"].(map[string]any)["enum"].([]any) {
		kinds = append(kinds, k.(string))
	}
	if !slices.Equal(kinds, codexAllSourceKinds) {
		t.Fatalf("ThreadSourceKind %v", kinds)
	}
	thread := defs(read("ThreadListResponse", "70d0fb1a5d747515459317281d9aa654e930122a174e8535c56de83b9f3b5dcf"))["Thread"].(map[string]any)
	for _, f := range []string{"id", "cwd", "source", "preview", "createdAt"} {
		if !slices.Contains(thread["required"].([]any), any(f)) {
			t.Fatal("Thread no longer requires", f)
		}
	}
	turns := read("ThreadTurnsListParams", "2a8b93d7d4437cc25e16a1dde16d8db3c338d39e16c5eece2a56e943a21577d7")
	for _, f := range []string{"threadId", "limit", "sortDirection", "itemsView"} {
		if props(turns)[f] == nil {
			t.Fatal("thread/turns/list lacks", f)
		}
	}
	turn := defs(read("ThreadTurnsListResponse", "8db83d38553204a223cf349680c59b0cc674ba95a4637e1995bfeabd73c80219"))
	var statuses []string
	for _, s := range turn["TurnStatus"].(map[string]any)["enum"].([]any) {
		statuses = append(statuses, s.(string))
	}
	if !slices.Equal(statuses, []string{"completed", "interrupted", "failed", "inProgress"}) {
		t.Fatalf("TurnStatus %v", statuses)
	}
	items := read("ThreadItemsListParams", "d5cf9784413f1d1abba756e04e48b273a9d12ab2c5675442e91b6a4ee55e19f0")
	for _, f := range []string{"threadId", "turnId", "limit", "sortDirection"} {
		if props(items)[f] == nil {
			t.Fatal("thread/items/list lacks", f)
		}
	}
	found := false
	for _, v := range defs(read("ThreadItemsListResponse", "bc8a8dcb9a48d9ca38b0a5faa89b80ec2fd57398cd3a8dcc15097776153f6c74"))["ThreadItem"].(map[string]any)["oneOf"].([]any) {
		item := v.(map[string]any)
		typ := item["properties"].(map[string]any)["type"].(map[string]any)["enum"].([]any)
		if len(typ) == 1 && typ[0] == "agentMessage" && slices.Contains(item["required"].([]any), any("text")) {
			found = true
		}
	}
	if !found {
		t.Fatal("agentMessage item no longer requires text")
	}
}

// The argv agentd builds is accepted by the real packaged wrapper (its
// --dry-run invokes no client, tmux or socket connection).
func TestCodexThreadWrapperContract(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	wrapper, err := filepath.Abs(filepath.Join("..", "..", "scripts", "codex-clean-session.py"))
	if err != nil {
		t.Fatal(err)
	}
	s, sess, cwd := threadHost(t)
	short, err := os.MkdirTemp("/tmp", "thread-wrap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	s.CodexHome = short
	if os.MkdirAll(filepath.Join(short, "app-server-control"), 0o700) != nil {
		t.Fatal("socket dir")
	}
	listener, err := net.Listen("unix", filepath.Join(short, "app-server-control", "app-server-control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	s.CodexBin = "/bin/sh"
	f := newThreadFake(cwd)
	d := f.deps()
	var call codexWrapperCall
	d.launch = func(_ context.Context, c codexWrapperCall) ([]byte, error) {
		call = c
		return nil, errors.New("captured only")
	}
	d.python = func() (string, error) { return python, nil }
	_ = startCodexThread(context.Background(), s, sess, goodThreadRequest(), d)
	if len(call.Argv) == 0 {
		t.Fatal("no launch built")
	}
	call.Argv[3] = wrapper
	call.Argv = append(call.Argv, "--tmux", "/bin/sh", "--dry-run")
	out, err := runCodexThreadWrapper(context.Background(), call)
	var st wrapperState
	if err != nil || json.Unmarshal(out, &st) != nil || st.State != "dry-run" {
		t.Fatalf("wrapper refused agentd's argv: %v %s", err, out)
	}
}
