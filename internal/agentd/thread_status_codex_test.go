package agentd

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/threadlaunch"
)

// statusDeps scripts a host for a status pass (DESIGN-057 section 11) and
// records each thread read it makes. All is synthetic; no client, tmux, daemon
// or account is touched.
func statusDeps(t *testing.T, f *threadFake, calls *[]string) codexThreadDeps {
	t.Helper()
	d := f.deps()
	turn := d.turn
	d.list = func(_ context.Context, _ Settings, _ int, _ string, all bool) ([]codexThreadSummary, error) {
		*calls = append(*calls, "list")
		if f.listErrs > 0 {
			f.listErrs--
			return nil, errCodexHostUnknown
		}
		if all {
			*calls = append(*calls, "all")
			if f.allListErr {
				return nil, errCodexHostUnknown
			}
			return f.all, nil
		}
		return f.shown, nil
	}
	d.turn = func(ctx context.Context, s Settings, pid int, id string) (codexTurnObservation, error) {
		*calls = append(*calls, "turn")
		if f.turns == nil {
			return codexTurnObservation{}, errCodexHostUnknown
		}
		return turn(ctx, s, pid, id)
	}
	return d
}

// A status pass holds no launcher at all: the wrapper, its interpreter, its
// path and the poll clock are gone before the first read, with the real
// dependencies and with a test's. The host reads and observers stay.
func TestCodexThreadStatusHoldsNoLauncher(t *testing.T) {
	_, _, cwd := threadHost(t)
	for name, d := range map[string]codexThreadDeps{"real": realCodexThreadDeps(), "fake": newThreadFake(cwd).deps()} {
		if d.launch == nil || d.python == nil || d.wrapper == nil || d.sleep == nil {
			t.Fatalf("%s: the launch dependencies are not set to begin with", name)
		}
		o := d.observeOnly()
		if o.launch != nil || o.python != nil || o.wrapper != nil || o.sleep != nil {
			t.Errorf("%s: a status pass still holds a launcher or a poll clock", name)
		}
		if o.access == nil || o.daemon == nil || o.remote == nil || o.paired == nil || o.list == nil || o.turn == nil || o.now == nil {
			t.Errorf("%s: a status pass lost a read", name)
		}
	}
}

// observeThread runs one status pass and checks its result is one decodable
// thread-launch.v1 document that launched nothing.
func observeThread(t *testing.T, f *threadFake, s Settings, sess protocol.Session, req CodexThreadStatusRequest) (apiv1.ThreadLaunchResult, []string) {
	t.Helper()
	var calls []string
	res := observeCodexThread(context.Background(), s, sess, req, statusDeps(t, f, &calls))
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	back, err := apiv1.DecodeThreadLaunch(b)
	if err != nil {
		t.Fatalf("result does not decode: %v\n%s", err, b)
	}
	if len(f.launches) != 0 {
		t.Fatal("a status pass launched")
	}
	if back.DeadlineMs != 0 || back.Name != "" || back.RequestedModel != "" {
		t.Fatalf("a status result carries launch fields: %+v", back)
	}
	if back.Phases[apiv1.ThreadPhaseClientStarted].State != "unknown" || back.Phases[apiv1.ThreadPhaseClientStarted].Reason != "not_observed_by_status" {
		t.Fatalf("client_started %+v: a status pass does not observe the client", back.Phases[apiv1.ThreadPhaseClientStarted])
	}
	if back.Permission.Source != threadlaunch.StatusPermissionSource {
		t.Fatalf("permission source %q", back.Permission.Source)
	}
	return back, calls
}

// The three ends acceptance names (completed, response pending, failed) and
// the others a status pass can reach. Each is one read-only pass: at most two
// list reads and one turn read, no wrapper, no poll.
func TestCodexThreadStatusOutcomes(t *testing.T) {
	cases := []struct {
		name          string
		mutate        func(f *threadFake, sess *protocol.Session, req *CodexThreadStatusRequest)
		outcome, code string
		phase         string
		exit          int
		states        map[string]string
		calls         []string
	}{
		{name: "completed", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.turns = []codexTurnObservation{{TurnID: turnID, Status: "completed", AgentMessage: true}}
		}, outcome: apiv1.ThreadResponded, exit: 0, calls: []string{"list", "turn"},
			states: map[string]string{apiv1.ThreadPhaseHostReady: "ok", apiv1.ThreadPhasePaired: "unknown", apiv1.ThreadPhaseThreadCreated: "ok", apiv1.ThreadPhaseSourceVisible: "ok", apiv1.ThreadPhaseHandoff: "ok", apiv1.ThreadPhaseFirstResponse: "ok"}},
		{name: "responded while the turn still runs", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.turns = []codexTurnObservation{{TurnID: turnID, Status: "inProgress", AgentMessage: true}}
		}, outcome: apiv1.ThreadResponded, exit: 0, calls: []string{"list", "turn"}},
		{name: "response pending", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.turns = []codexTurnObservation{{TurnID: turnID, Status: "inProgress"}}
		}, outcome: apiv1.ThreadVisible, code: "response_pending", phase: apiv1.ThreadPhaseFirstResponse, exit: 7, calls: []string{"list", "turn"},
			states: map[string]string{apiv1.ThreadPhaseThreadCreated: "ok", apiv1.ThreadPhaseSourceVisible: "ok", apiv1.ThreadPhaseHandoff: "ok", apiv1.ThreadPhaseFirstResponse: "unknown"}},
		{name: "no turn yet is pending", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.turns = []codexTurnObservation{{}}
		}, outcome: apiv1.ThreadVisible, code: "response_pending", phase: apiv1.ThreadPhaseFirstResponse, exit: 7, calls: []string{"list", "turn"},
			states: map[string]string{apiv1.ThreadPhaseSourceVisible: "ok", apiv1.ThreadPhaseHandoff: "unknown", apiv1.ThreadPhaseFirstResponse: "unknown"}},
		{name: "failed turn", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.turns = []codexTurnObservation{{TurnID: turnID, Status: "failed"}}
		}, outcome: apiv1.ThreadFailed, code: "turn_failed", phase: apiv1.ThreadPhaseFirstResponse, exit: 1, calls: []string{"list", "turn"},
			states: map[string]string{apiv1.ThreadPhaseSourceVisible: "ok", apiv1.ThreadPhaseHandoff: "ok", apiv1.ThreadPhaseFirstResponse: "error"}},
		{name: "interrupted turn", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.turns = []codexTurnObservation{{TurnID: turnID, Status: "interrupted"}}
		}, outcome: apiv1.ThreadFailed, code: "turn_failed", phase: apiv1.ThreadPhaseFirstResponse, exit: 1, calls: []string{"list", "turn"}},
		{name: "completed without an observed message", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.turns = []codexTurnObservation{{TurnID: turnID, Status: "completed"}}
		}, outcome: apiv1.ThreadVisible, code: "response_not_observable", phase: apiv1.ThreadPhaseFirstResponse, exit: 7, calls: []string{"list", "turn"}},
		{name: "hidden source", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.all[0].Source = "exec"
			f.shown = f.shown[1:]
		}, outcome: apiv1.ThreadHidden, code: "hidden_source", phase: apiv1.ThreadPhaseSourceVisible, exit: 1, calls: []string{"list", "list", "all"},
			states: map[string]string{apiv1.ThreadPhaseThreadCreated: "ok", apiv1.ThreadPhaseSourceVisible: "error"}},
		{name: "thread not listed", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.all, f.shown = f.all[1:], f.shown[1:]
		}, outcome: apiv1.ThreadFailed, code: "thread_not_found", phase: apiv1.ThreadPhaseThreadCreated, exit: 1, calls: []string{"list", "list", "all"},
			states: map[string]string{apiv1.ThreadPhaseHostReady: "ok", apiv1.ThreadPhaseThreadCreated: "error"}},
		{name: "sub-agent threads crowd the all-sources list", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			// The default list still holds the thread, so it is found there
			// and the all-sources list is never read.
			f.all = []codexThreadSummary{{ID: twoThread, Cwd: f.all[0].Cwd, Source: "subAgent", CreatedAt: threadNow.Unix() + 5}}
			f.turns = []codexTurnObservation{{TurnID: turnID, Status: "completed", AgentMessage: true}}
		}, outcome: apiv1.ThreadResponded, exit: 0, calls: []string{"list", "turn"}},
		{name: "all-sources read fails", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.shown, f.allListErr = f.shown[1:], true
		}, outcome: apiv1.ThreadFailed, code: "observation_unavailable", phase: apiv1.ThreadPhaseThreadCreated, exit: 1, calls: []string{"list", "list", "all"}},
		{name: "thread listed for another cwd", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.all[0].Cwd, f.shown[0].Cwd = "/home/dev/repos/other", "/home/dev/repos/other"
		}, outcome: apiv1.ThreadFailed, code: "thread_not_found", phase: apiv1.ThreadPhaseThreadCreated, exit: 1, calls: []string{"list", "list", "all"}},
		{name: "default list read fails", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.listErrs = 1
		}, outcome: apiv1.ThreadFailed, code: "observation_unavailable", phase: apiv1.ThreadPhaseThreadCreated, exit: 1, calls: []string{"list"}},
		{name: "turn read fails", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.turns = nil
		}, outcome: apiv1.ThreadFailed, code: "observation_unavailable", phase: apiv1.ThreadPhaseFirstResponse, exit: 1, calls: []string{"list", "turn"},
			states: map[string]string{apiv1.ThreadPhaseSourceVisible: "ok", apiv1.ThreadPhaseFirstResponse: "unknown"}},
		{name: "only another thread is listed", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.all[0].ID, f.shown[0].ID = twoThread, twoThread
		}, outcome: apiv1.ThreadFailed, code: "thread_not_found", phase: apiv1.ThreadPhaseThreadCreated, exit: 1, calls: []string{"list", "list", "all"}},
		{name: "a host that never issued a pairing code is still observed", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.unpaired = true
			f.turns = []codexTurnObservation{{TurnID: turnID, Status: "completed", AgentMessage: true}}
		}, outcome: apiv1.ThreadResponded, exit: 0, calls: []string{"list", "turn"},
			states: map[string]string{apiv1.ThreadPhasePaired: "required", apiv1.ThreadPhaseFirstResponse: "ok"}},
		{name: "not a codex host", mutate: func(_ *threadFake, sess *protocol.Session, _ *CodexThreadStatusRequest) {
			sess.Agent = protocol.AgentClaude
		}, outcome: apiv1.ThreadFailed, code: "not_codex_host", phase: apiv1.ThreadPhaseHostReady, exit: 1},
		{name: "malformed thread id", mutate: func(_ *threadFake, _ *protocol.Session, req *CodexThreadStatusRequest) {
			req.ThreadID = "thread-1109-180000"
		}, outcome: apiv1.ThreadFailed, code: "invalid_input", phase: apiv1.ThreadPhaseHostReady, exit: 1},
		{name: "malformed repo", mutate: func(_ *threadFake, _ *protocol.Session, req *CodexThreadStatusRequest) {
			req.Repo = "../x"
		}, outcome: apiv1.ThreadFailed, code: "invalid_input", phase: apiv1.ThreadPhaseHostReady, exit: 1},
		{name: "repo not on the host", mutate: func(_ *threadFake, _ *protocol.Session, req *CodexThreadStatusRequest) {
			req.Repo = "absent"
		}, outcome: apiv1.ThreadFailed, code: "repo_unavailable", phase: apiv1.ThreadPhaseHostReady, exit: 1},
		{name: "access not current", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.noAccess = true
		}, outcome: apiv1.ThreadActionRequired, code: "login_required", phase: apiv1.ThreadPhaseHostReady, exit: 7},
		{name: "daemon not live", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.daemon = "Backoff"
		}, outcome: apiv1.ThreadFailed, code: "daemon_unavailable", phase: apiv1.ThreadPhaseHostReady, exit: 1},
		{name: "remote disconnected", mutate: func(f *threadFake, _ *protocol.Session, _ *CodexThreadStatusRequest) {
			f.remote = "disconnected"
		}, outcome: apiv1.ThreadFailed, code: "remote_disconnected", phase: apiv1.ThreadPhaseHostReady, exit: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, cwd := threadHost(t)
			f := newThreadFake(cwd)
			req := CodexThreadStatusRequest{ThreadID: newThread}
			tc.mutate(f, &sess, &req)
			got, calls := observeThread(t, f, s, sess, req)
			code, phase := "", ""
			if got.Error != nil {
				code, phase = got.Error.Code, got.Error.Phase
				if got.Error.Message == "" {
					t.Errorf("code %s has no fixed message", code)
				}
			}
			if got.Outcome != tc.outcome || code != tc.code || phase != tc.phase || apiv1.ThreadLaunchExit(got.Outcome) != tc.exit {
				t.Fatalf("got %s/%s at %q, exit %d", got.Outcome, code, phase, apiv1.ThreadLaunchExit(got.Outcome))
			}
			for phase, state := range tc.states {
				if got.Phases[phase].State != state {
					t.Errorf("%s = %+v, want %s", phase, got.Phases[phase], state)
				}
			}
			if !slices.Equal(calls, tc.calls) {
				t.Errorf("reads %v, want %v", calls, tc.calls)
			}
			found := got.Phases[apiv1.ThreadPhaseThreadCreated].State == "ok"
			if (got.ThreadID == newThread) != found || (found && (got.Cwd != cwd || got.Source == "")) {
				t.Errorf("threadId %q cwd %q source %q with thread_created %v", got.ThreadID, got.Cwd, got.Source, found)
			}
		})
	}
}

// A second pass sees what changed: status is a recheck, and each pass stands
// alone.
func TestCodexThreadStatusRecheck(t *testing.T) {
	s, sess, cwd := threadHost(t)
	f := newThreadFake(cwd)
	f.turns = []codexTurnObservation{{TurnID: turnID, Status: "inProgress"}, {TurnID: turnID, Status: "completed", AgentMessage: true}}
	req := CodexThreadStatusRequest{ThreadID: newThread, Repo: "dev-env"}
	if got, _ := observeThread(t, f, s, sess, req); got.Outcome != apiv1.ThreadVisible || got.Error.Code != "response_pending" {
		t.Fatalf("first pass %s/%+v", got.Outcome, got.Error)
	}
	if got, _ := observeThread(t, f, s, sess, req); got.Outcome != apiv1.ThreadResponded || got.Source != "vscode" {
		t.Fatalf("second pass %s/%+v", got.Outcome, got.Error)
	}
}

// Against the in-process app-server, a whole status pass sends only the
// handshake (initialize and its initialized notification), the Remote Control
// status read and the three thread reads. It never starts, resumes or forks a
// thread, starts or steers a turn, or sends any other request or notification.
func TestCodexThreadStatusSendsOnlyReads(t *testing.T) {
	for name, tc := range map[string]struct {
		turn    map[string]any
		items   []any
		outcome string
	}{
		"completed": {map[string]any{"id": turnID, "status": "completed"}, []any{map[string]any{"type": "agentMessage", "id": "i1", "text": "Done."}}, apiv1.ThreadResponded},
		"pending":   {map[string]any{"id": turnID, "status": "inProgress"}, []any{map[string]any{"type": "commandExecution", "id": "i2"}}, apiv1.ThreadVisible},
		"failed":    {map[string]any{"id": turnID, "status": "failed"}, []any{}, apiv1.ThreadFailed},
	} {
		t.Run(name, func(t *testing.T) {
			var cwd string
			s, seen := threadRPCFixture(t, func(method string, _ map[string]any) any {
				switch method {
				case "remoteControl/status/read":
					return map[string]any{"status": "connected"}
				case "thread/list":
					return map[string]any{"data": []any{map[string]any{"id": newThread, "cwd": cwd, "source": "vscode", "preview": "p", "createdAt": 1762711200}}}
				case "thread/turns/list":
					return map[string]any{"data": []any{tc.turn}}
				case "thread/items/list":
					return map[string]any{"data": tc.items}
				}
				return nil
			})
			s.Home = s.CodexHome
			cwd = s.ClonePath("dev-env")
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				t.Fatal(err)
			}
			// The daemon reads are the real ones; only the host's own files are faked.
			d := realCodexThreadDeps()
			d.access = func(Settings, time.Time) error { return nil }
			d.daemon = func(Settings) (codexHostProcess, error) {
				return codexHostProcess{State: "Live", PID: os.Getpid()}, nil
			}
			d.paired = func(Settings) bool { return true }
			sess := protocol.Session{Name: "codex-host-1", Repo: "dev-env", Agent: protocol.AgentCodex, Mode: protocol.ModeRemote, Model: "gpt-6-astra"}
			got := observeCodexThread(context.Background(), s, sess, CodexThreadStatusRequest{ThreadID: newThread}, d)
			if got.Outcome != tc.outcome || got.ThreadID != newThread {
				t.Fatalf("got %s/%+v", got.Outcome, got.Error)
			}
			var methods []string
			for _, r := range *seen {
				methods = append(methods, r["method"].(string))
			}
			slices.Sort(methods)
			methods = slices.Compact(methods)
			if want := []string{"initialize", "initialized", "remoteControl/status/read", "thread/items/list", "thread/list", "thread/turns/list"}; !slices.Equal(methods, want) {
				t.Fatalf("a status pass sent %v; it may send only %v", methods, want)
			}
		})
	}
}

// agent-run checks --thread with apiv1.ValidThreadID and the host with
// nativeThreadID: the two rules must agree.
func TestThreadIDRulesAgree(t *testing.T) {
	for _, id := range []string{newThread, strings.ToUpper(newThread), "thread-1109-180000", "", newThread + "0", newThread[1:], " " + newThread, newThread + "\n"} {
		if nativeThreadID.MatchString(id) != apiv1.ValidThreadID(id) {
			t.Errorf("the thread id rules disagree on %q", id)
		}
	}
}
