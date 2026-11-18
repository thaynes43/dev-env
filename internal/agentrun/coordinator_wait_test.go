package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	testingclock "k8s.io/utils/clock/testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/coordinatorwait"
)

type waitTestClock struct{ *testingclock.FakeClock }

func (c *waitTestClock) NewTimer(d time.Duration) coordinatorwait.Timer {
	return c.FakeClock.NewTimer(d)
}

func waitHarness(t *testing.T) (*harness, []string) {
	t.Helper()
	h := newHarness(t)
	h.vars["HOME"] = h.dir
	h.vars[protocol.SessionEnv] = `{"name":"coordinator","sessionUID":"session-uid","repo":"dev-env","agent":"codex","mode":"remote","model":"gpt-6.1-sol"}`
	h.env.WaitClock = &waitTestClock{testingclock.NewFakeClock(testNow)}
	dir := filepath.Join(h.dir, "handoffs")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"coordinator", "wait", "--thread", "native-thread", "--wait-id", "tool-call", "--directory", dir, "--budget-end", testNow.Add(4 * time.Hour).Format(time.RFC3339)}
	return h, args
}
func resultFrom(t *testing.T, h *harness) coordinatorwait.Result {
	t.Helper()
	var r coordinatorwait.Result
	if json.Unmarshal(h.stdout.Bytes(), &r) != nil {
		t.Fatalf("invalid result %q", h.stdout.String())
	}
	return r
}
func TestCoordinatorWaitAddressedAPIAndOutputAllowlist(t *testing.T) {
	h, args := waitHarness(t)
	calls := 0
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		calls++
		if r.Method != "GET" || r.URL.Path != apiv1.ParentNotesWaitAnyPath || len(body) != 0 {
			t.Error("not an existing held read")
		}
		q := r.URL.Query()
		if calls == 1 {
			if q.Get("seconds") != "0" || q.Get("after") != "" {
				t.Error("bad baseline")
			}
			writeTestJSON(w, 200, apiv1.ParentNoteWaitAnyResult{State: "waiting", Cursor: "42"})
			return
		}
		if q.Get("after") != "42" || q.Get("seconds") != "900" {
			t.Error("bad addressed hold")
		}
		writeTestJSON(w, 200, map[string]any{"state": "note", "name": parentTestMetadata().Name, "child": "child", "kind": "question", "createdAt": testNow, "cursor": "43", "text": "PRIVATE_NOTE", "command": "PRIVATE_COMMAND"})
	}
	h.mustRun(ExitOK, args...)
	r := resultFrom(t, h)
	if calls != 2 || r.Kind != "note" || r.Cursor != "43" || r.Note != parentTestMetadata().Name || len(h.ran) != 0 || strings.Contains(h.stdout.String(), "PRIVATE") {
		t.Fatal("unsafe or wrong result")
	}
	var raw map[string]any
	_ = json.Unmarshal(h.stdout.Bytes(), &raw)
	if len(raw) != 6 || raw["kind"] != "note" || raw["id"] == nil || raw["watermark"] == nil || raw["time"] == nil {
		t.Fatal("result outside allowlist")
	}
	// A lost ACK reaches neither the API nor a second result, and explicit wrong
	// thread binding cannot adopt the record by cwd, wait-id or title.
	h.mustRun(ExitUnconfirmed, args...)
	if h.stdout.Len() != 0 || len(h.api.requests()) != 2 {
		t.Fatal("lost ACK replay")
	}
	changed := append([]string(nil), args...)
	changed[3] = "other-thread"
	h.mustRun(ExitUnconfirmed, append(changed, "--ack", r.ID)...)
	if h.stdout.Len() != 0 || len(h.api.requests()) != 2 {
		t.Fatal("another thread adopted wait")
	}
}
func TestCoordinatorWaitResyncAndNoRetryOnRefusal(t *testing.T) {
	for _, mode := range []string{"resync", "refused", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			h, args := waitHarness(t)
			calls := 0
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				calls++
				if calls == 1 {
					writeTestJSON(w, 200, apiv1.ParentNoteWaitAnyResult{State: "waiting", Cursor: "42"})
					return
				}
				switch mode {
				case "refused":
					apiError(w, 503, apiv1.CodeUnavailable, "PRIVATE_REFUSAL")
				case "invalid":
					writeTestJSON(w, 200, map[string]any{"state": "note", "cursor": "43", "name": "unsafe/../../path"})
				default:
					writeTestJSON(w, 200, apiv1.ParentNoteWaitAnyResult{State: "resync", Cursor: "77"})
				}
			}
			code := ExitOK
			if mode != "resync" {
				code = ExitUnconfirmed
			}
			h.mustRun(code, args...)
			if calls != 2 || strings.Contains(h.stderr.String(), "PRIVATE") {
				t.Fatal("retried or leaked refusal")
			}
			if mode == "resync" {
				r := resultFrom(t, h)
				if r.Kind != "resync" || r.Cursor != "77" {
					t.Fatal("lost server cursor")
				}
				h.mustRun(ExitUnconfirmed, append(args, "--ack", r.ID)...)
				if len(h.api.requests()) != 2 {
					t.Fatal("resync without source reconciliation")
				}
			} else if h.stdout.Len() != 0 {
				t.Fatal("unconfirmed API result printed")
			}
		})
	}
}
func TestCoordinatorWaitBudgetEndDoesNotConnect(t *testing.T) {
	h, args := waitHarness(t)
	h.env.WaitClock = &waitTestClock{testingclock.NewFakeClock(testNow.Add(5 * time.Hour))}
	h.mustRun(ExitOK, args...)
	r := resultFrom(t, h)
	if r.Kind != "budget_end" || len(h.api.requests()) != 0 || len(h.ran) != 0 {
		t.Fatal("budget opened connection")
	}
	h.mustRun(ExitUnconfirmed, append(args, "--ack", r.ID)...)
}
func TestCoordinatorWaitPathsAndRequiredBinding(t *testing.T) {
	for _, which := range []string{"thread", "budget", "uid", "directory", "symlink-dir", "scan-cadence"} {
		t.Run(which, func(t *testing.T) {
			h, args := waitHarness(t)
			switch which {
			case "thread":
				args[3] = ""
			case "budget":
				args[len(args)-1] = ""
			case "uid":
				h.vars[protocol.SessionEnv] = ""
			case "directory":
				args[7] += "/../elsewhere"
			case "scan-cadence":
				args = append(args, "--scan-every", "1ms")
			case "symlink-dir":
				orig := args[7]
				args[7] = filepath.Join(h.dir, "linked")
				if err := os.Symlink(orig, args[7]); err != nil {
					t.Fatal(err)
				}

			}
			code := ExitUsage
			if strings.HasPrefix(which, "symlink") {
				code = ExitFailed
			}
			h.mustRun(code, args...)
			if len(h.api.requests()) != 0 || h.stdout.Len() != 0 {
				t.Fatal("unsafe bind reached API")
			}
		})
	}
}
func TestCoordinatorWaitSessionFileAndHelp(t *testing.T) {
	h, args := waitHarness(t)
	h.vars[protocol.SessionFileEnv] = h.write("session.json", h.vars[protocol.SessionEnv])
	h.vars[protocol.SessionEnv] = ""
	h.env.WaitClock = &waitTestClock{testingclock.NewFakeClock(testNow.Add(5 * time.Hour))}
	h.mustRun(ExitOK, args...)
	if resultFrom(t, h).Kind != "budget_end" {
		t.Fatal("session file identity")
	}
	h.mustRun(ExitOK, "help", "coordinator", "wait")
	for _, s := range []string{"--thread", "--budget-end", "--ack", "--resume", "resync", "not start a Codex turn"} {
		if !strings.Contains(h.stdout.String(), s) {
			t.Fatalf("help omits %s", s)
		}
	}
}

type failedWaitOutput struct{}

func (failedWaitOutput) Write([]byte) (int, error) { return 0, errors.New("lost output") }
func TestCoordinatorWaitPersistsBeforeLostOutput(t *testing.T) {
	h, args := waitHarness(t)
	h.env.WaitClock = &waitTestClock{testingclock.NewFakeClock(testNow.Add(5 * time.Hour))}
	h.env.Stdout = failedWaitOutput{}
	if code := h.run(args...); code == ExitOK {
		t.Fatal("lost output confirmed")
	}
	h.env.Stdout = &h.stdout
	s, err := coordinatorwait.OpenStore(h.dir, "session-uid", "tool-call")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Load()
	s.Close()
	if err != nil || r == nil || r.LastID == "" || !r.Ended {
		t.Fatal("output before durable receipt")
	}
	h.mustRun(ExitUnconfirmed, args...)
	if h.stdout.Len() != 0 {
		t.Fatal("lost output replayed")
	}
}
func TestCoordinatorWaitCancelledContextPersistsStop(t *testing.T) {
	h, args := waitHarness(t)
	// Seed a valid stopped record without any live call, then require an explicit
	// resume and demonstrate cancellation is passed to the wait as a stop.
	o := coordinatorwait.Options{SessionUID: "session-uid", ThreadID: "native-thread", WaitID: "tool-call", Directory: args[7], Timer: time.Hour, ScanEvery: 10 * time.Second, MaxEntries: 256, MaxPending: 64, BudgetEnd: testNow.Add(4 * time.Hour)}
	s, err := coordinatorwait.OpenStore(h.dir, o.SessionUID, o.WaitID)
	if err != nil {
		t.Fatal(err)
	}
	r := coordinatorwait.New(o, testNow, "42", nil)
	v := r.Take(testNow, true)
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}
	s.Close()
	h.mustRun(ExitUnconfirmed, append(args, "--ack", v.ID)...)
	h.mustRun(ExitUsage, append(args, "--ack", v.ID, "--resume", "--after", "41")...)
	h.mustRun(ExitUsage, append(args, "--ack", v.ID, "--resume", "--watermark", "wrong")...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code := Run(ctx, append(args, "--ack", v.ID, "--resume"), h.env)
	if code != ExitOK {
		t.Fatal(h.stderr.String())
	}
	out := resultFrom(t, h)
	if out.Kind != "stopped" || out.Cursor != "42" || len(h.api.requests()) != 0 {
		t.Fatal("cancel did not persist cursors")
	}
}

var _ io.Writer = failedWaitOutput{}

func TestCoordinatorWaitFreshCancellationDoesNotConnect(t *testing.T) {
	h, args := waitHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Run(ctx, args, h.env) != ExitOK || resultFrom(t, h).Kind != "stopped" || len(h.api.requests()) != 0 {
		t.Fatal("fresh cancellation opened read")
	}
}

func TestCoordinatorWaitBudgetDuringCursorCapture(t *testing.T) {
	h, args := waitHarness(t)
	c := h.env.WaitClock.(*waitTestClock)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) { c.Step(4 * time.Hour); <-r.Context().Done() }
	h.mustRun(ExitOK, args...)
	if r := resultFrom(t, h); r.Kind != "budget_end" || len(h.api.requests()) != 1 {
		t.Fatal("cursor capture outlived budget or rearmed")
	}
}

func TestCoordinatorWaitResumeWithoutCursorRequiresFreshResync(t *testing.T) {
	h, args := waitHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Run(ctx, args, h.env) != ExitOK {
		t.Fatal("fresh stop failed")
	}
	stopped := resultFrom(t, h)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("seconds") != "0" {
			t.Error("resume skipped cursor capture")
			writeTestJSON(w, 200, apiv1.ParentNoteWaitAnyResult{State: "note", Name: parentTestMetadata().Name, Child: "child", Kind: "result", CreatedAt: testNow, Cursor: "77"})
			return
		}
		writeTestJSON(w, 200, apiv1.ParentNoteWaitAnyResult{State: "waiting", Cursor: "77"})
	}
	h.mustRun(ExitOK, append(args, "--ack", stopped.ID, "--resume")...)
	resync := resultFrom(t, h)
	if resync.Kind != "resync" || resync.Cursor != "77" || resync.ID == stopped.ID || len(h.api.requests()) != 1 {
		t.Fatal("unconfirmed baseline resumed without resync")
	}
	h.mustRun(ExitUnconfirmed, append(args, "--ack", resync.ID)...)
	if len(h.api.requests()) != 1 {
		t.Fatal("unreconciled cursor rearmed")
	}
}

func TestCoordinatorWaitRefusedEntryThenNote(t *testing.T) {
	for _, name := range []string{"bad\xffname", "bad\nname", "link"} {
		t.Run(name[:3], func(t *testing.T) {
			h, args := waitHarness(t)
			path := filepath.Join(args[7], name)
			var err error
			if name == "link" {
				err = os.Symlink("missing", path)
			} else {
				err = os.WriteFile(path, nil, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.URL.Query().Get("seconds") == "0" {
					writeTestJSON(w, 200, apiv1.ParentNoteWaitAnyResult{State: "waiting", Cursor: "42"})
					return
				}
				writeTestJSON(w, 200, apiv1.ParentNoteWaitAnyResult{State: "note", Name: parentTestMetadata().Name, Child: "child", Kind: "result", CreatedAt: testNow, Cursor: "43"})
			}
			h.mustRun(ExitOK, args...)
			refusal := resultFrom(t, h)
			if refusal.Kind != "resync" || refusal.Reason == "" || refusal.Path != "" || refusal.Note != "" || strings.Contains(h.stdout.String(), "bad") {
				t.Fatal("unsafe refusal output")
			}
			h.mustRun(ExitOK, append(args, "--ack", refusal.ID)...)
			if resultFrom(t, h).Kind != "note" || len(h.api.requests()) != 2 {
				t.Fatal("refused entry prevented later note")
			}
		})
	}
}
