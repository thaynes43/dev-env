package threadlaunch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// fakeAdapter scripts each step; visibleAfter and respondAfter count polls.
type fakeAdapter struct {
	preflight, create      error
	visibleAfter           int
	visibleErr             error
	respondAfter           int
	respondErr             error
	creates, visible, resp int
}

func (f *fakeAdapter) Preflight(_ context.Context, r *Recorder) error {
	if f.preflight == nil {
		r.Phase(apiv1.ThreadPhaseHostReady, "ok", "fake", "ready")
	}
	return f.preflight
}

func (f *fakeAdapter) Create(_ context.Context, r *Recorder) error {
	f.creates++
	if f.create == nil {
		r.Phase(apiv1.ThreadPhaseClientStarted, "ok", "fake", "running")
	}
	return f.create
}

func (f *fakeAdapter) Visible(_ context.Context, r *Recorder) (bool, error) {
	f.visible++
	if f.visibleErr != nil {
		return false, f.visibleErr
	}
	if f.visible > f.visibleAfter {
		r.Result.ThreadID = "0199a000-0000-7000-8000-000000000002"
		r.Phase(apiv1.ThreadPhaseThreadCreated, "ok", "fake", "new")
		r.Phase(apiv1.ThreadPhaseSourceVisible, "ok", "fake", "vscode")
		return true, nil
	}
	return false, nil
}

func (f *fakeAdapter) FirstResponse(_ context.Context, r *Recorder) (bool, error) {
	f.resp++
	if f.respondErr != nil {
		return false, f.respondErr
	}
	if f.resp > f.respondAfter {
		r.Phase(apiv1.ThreadPhaseFirstResponse, "ok", "fake", "agent_message")
		return true, nil
	}
	return false, nil
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }
func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.now = c.now.Add(d)
	return nil
}

func run(t *testing.T, f *fakeAdapter, wait time.Duration) apiv1.ThreadLaunchResult {
	t.Helper()
	c := &clock{now: time.Date(2026, 11, 9, 18, 0, 0, 0, time.UTC)}
	rec := NewRecorder("codex", c.Now)
	res := Run(context.Background(), f, rec, Options{Wait: wait, Poll: 2 * time.Second, Sleep: c.Sleep})
	b, err := Encode(res)
	if err != nil {
		t.Fatal(err)
	}
	back, err := apiv1.DecodeThreadLaunch(b[:len(b)-1])
	if err != nil {
		t.Fatalf("result does not decode: %v\n%s", err, b)
	}
	if f.creates > 1 {
		t.Fatalf("create ran %d times", f.creates)
	}
	return back
}

func code(r apiv1.ThreadLaunchResult) string {
	if r.Error == nil {
		return ""
	}
	return r.Error.Code
}

// Only an observed first response is a success; every other end names its
// outcome, code and phase, and Create runs at most once.
func TestRunOutcomes(t *testing.T) {
	cases := []struct {
		name               string
		f                  *fakeAdapter
		outcome, code, phs string
	}{
		{"responded", &fakeAdapter{visibleAfter: 2, respondAfter: 3}, apiv1.ThreadResponded, "", ""},
		{"response pending", &fakeAdapter{respondAfter: 1000}, apiv1.ThreadVisible, "response_pending", apiv1.ThreadPhaseFirstResponse},
		{"no thread", &fakeAdapter{visibleAfter: 1000}, apiv1.ThreadTimedOut, "thread_not_observed", apiv1.ThreadPhaseThreadCreated},
		{"not observable", &fakeAdapter{respondErr: ErrNotObservable}, apiv1.ThreadVisible, "response_not_observable", apiv1.ThreadPhaseFirstResponse},
		{"pairing", &fakeAdapter{preflight: Halt(apiv1.ThreadActionRequired, "pairing_required", apiv1.ThreadPhasePaired)}, apiv1.ThreadActionRequired, "pairing_required", apiv1.ThreadPhasePaired},
		{"preflight unexpected", &fakeAdapter{preflight: errors.New("boom")}, apiv1.ThreadFailed, "observation_unavailable", apiv1.ThreadPhaseHostReady},
		{"create unexpected", &fakeAdapter{create: errors.New("boom")}, apiv1.ThreadUnconfirmed, "observation_unavailable", apiv1.ThreadPhaseClientStarted},
		{"hidden", &fakeAdapter{visibleErr: Halt(apiv1.ThreadHidden, "hidden_source", apiv1.ThreadPhaseSourceVisible)}, apiv1.ThreadHidden, "hidden_source", apiv1.ThreadPhaseSourceVisible},
		{"visible unexpected", &fakeAdapter{visibleErr: errors.New("rpc")}, apiv1.ThreadUnconfirmed, "observation_unavailable", apiv1.ThreadPhaseThreadCreated},
		{"blocked", &fakeAdapter{preflight: Halt(apiv1.ThreadBlocked, "provider_not_qualified", "")}, apiv1.ThreadBlocked, "provider_not_qualified", apiv1.ThreadPhaseHostReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, tc.f, time.Minute)
			if got.Outcome != tc.outcome || code(got) != tc.code || (got.Error != nil && got.Error.Phase != tc.phs) {
				t.Fatalf("got %s/%s at %+v", got.Outcome, code(got), got.Error)
			}
			if got.Error != nil && got.Error.Message == "" {
				t.Fatalf("code %s has no fixed message", got.Error.Code)
			}
			if tc.f.preflight != nil && tc.f.creates != 0 {
				t.Fatal("a refused preflight must create nothing")
			}
		})
	}
}

// fakeObserver is fakeAdapter's read-only half. It has no Create to call.
type fakeObserver struct {
	*fakeAdapter
	located int
}

func (f *fakeObserver) Locate(_ context.Context, r *Recorder) error {
	f.located++
	if f.preflight == nil {
		r.Phase(apiv1.ThreadPhaseHostReady, "ok", "fake", "ready")
	}
	return f.preflight
}

// Observe is one pass: each step runs at most once, nothing is created, no
// poll clock exists, and a failed observation is a plain failure, never an
// unconfirmed launch.
func TestObserveOutcomes(t *testing.T) {
	cases := []struct {
		name               string
		f                  *fakeAdapter
		outcome, code, phs string
		visible, resp      int
	}{
		{"completed", &fakeAdapter{}, apiv1.ThreadResponded, "", "", 1, 1},
		{"response pending", &fakeAdapter{respondAfter: 1}, apiv1.ThreadVisible, "response_pending", apiv1.ThreadPhaseFirstResponse, 1, 1},
		{"turn failed", &fakeAdapter{respondErr: Halt(apiv1.ThreadFailed, "turn_failed", apiv1.ThreadPhaseFirstResponse)}, apiv1.ThreadFailed, "turn_failed", apiv1.ThreadPhaseFirstResponse, 1, 1},
		{"not observable", &fakeAdapter{respondErr: ErrNotObservable}, apiv1.ThreadVisible, "response_not_observable", apiv1.ThreadPhaseFirstResponse, 1, 1},
		{"response read failed", &fakeAdapter{respondErr: errors.New("rpc")}, apiv1.ThreadFailed, "observation_unavailable", apiv1.ThreadPhaseFirstResponse, 1, 1},
		{"hidden", &fakeAdapter{visibleErr: Halt(apiv1.ThreadHidden, "hidden_source", apiv1.ThreadPhaseSourceVisible)}, apiv1.ThreadHidden, "hidden_source", apiv1.ThreadPhaseSourceVisible, 1, 0},
		{"list read failed", &fakeAdapter{visibleErr: errors.New("rpc")}, apiv1.ThreadFailed, "observation_unavailable", apiv1.ThreadPhaseSourceVisible, 1, 0},
		{"not visible without a reason", &fakeAdapter{visibleAfter: 1}, apiv1.ThreadFailed, "observation_unavailable", apiv1.ThreadPhaseSourceVisible, 1, 0},
		{"thread not found", &fakeAdapter{preflight: Halt(apiv1.ThreadFailed, "thread_not_found", apiv1.ThreadPhaseThreadCreated)}, apiv1.ThreadFailed, "thread_not_found", apiv1.ThreadPhaseThreadCreated, 0, 0},
		{"host refused", &fakeAdapter{preflight: Halt(apiv1.ThreadActionRequired, "login_required", "")}, apiv1.ThreadActionRequired, "login_required", apiv1.ThreadPhaseHostReady, 0, 0},
		{"locate unexpected", &fakeAdapter{preflight: errors.New("boom")}, apiv1.ThreadFailed, "observation_unavailable", apiv1.ThreadPhaseHostReady, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &clock{now: time.Date(2026, 11, 10, 16, 0, 0, 0, time.UTC)}
			o := &fakeObserver{fakeAdapter: tc.f}
			res := Observe(context.Background(), o, NewRecorder("codex", c.Now))
			b, err := Encode(res)
			if err != nil {
				t.Fatal(err)
			}
			got, err := apiv1.DecodeThreadLaunch(b[:len(b)-1])
			if err != nil {
				t.Fatalf("result does not decode: %v\n%s", err, b)
			}
			if got.Outcome != tc.outcome || code(got) != tc.code || (got.Error != nil && got.Error.Phase != tc.phs) {
				t.Fatalf("got %s/%s at %+v", got.Outcome, code(got), got.Error)
			}
			if got.Error != nil && got.Error.Message == "" {
				t.Fatalf("code %s has no fixed message", got.Error.Code)
			}
			if got.Outcome == apiv1.ThreadUnconfirmed || got.Outcome == apiv1.ThreadTimedOut || got.DeadlineMs != 0 {
				t.Fatalf("a status pass reported a launch outcome or a wait: %s %d", got.Outcome, got.DeadlineMs)
			}
			if tc.f.creates != 0 || o.located != 1 || tc.f.visible != tc.visible || tc.f.resp != tc.resp {
				t.Fatalf("creates %d locates %d visible %d resp %d", tc.f.creates, o.located, tc.f.visible, tc.f.resp)
			}
		})
	}
}

// A deadline is a pending result, never a retry: polls stop and Create is not
// repeated.
func TestRunDeadlineStopsPolling(t *testing.T) {
	f := &fakeAdapter{visibleAfter: 1000}
	got := run(t, f, 10*time.Second)
	if got.Outcome != apiv1.ThreadTimedOut || f.creates != 1 || f.visible > 6 {
		t.Fatalf("outcome %s creates %d polls %d", got.Outcome, f.creates, f.visible)
	}
	if got.DeadlineMs != 10000 || got.TotalMs < 10000 {
		t.Fatalf("timing %d/%d", got.DeadlineMs, got.TotalMs)
	}
}

func TestExitAndRegistry(t *testing.T) {
	for outcome, want := range map[string]int{apiv1.ThreadResponded: 0, apiv1.ThreadVisible: 7, apiv1.ThreadActionRequired: 7, apiv1.ThreadTimedOut: 7, apiv1.ThreadUnconfirmed: 6, apiv1.ThreadHidden: 1, apiv1.ThreadFailed: 1, apiv1.ThreadBlocked: 1, "other": 1} {
		if apiv1.ThreadLaunchExit(outcome) != want {
			t.Errorf("exit for %s", outcome)
		}
	}
	for name, route := range map[string]Route{"codex": RouteHost, "claude": RouteSession, "cursor": RouteUnqualified, "grok": RouteUnqualified} {
		p, ok := Lookup(name)
		if !ok || p.Route != route || (route == RouteUnqualified) != (p.Unqualified != "") {
			t.Errorf("provider %s: %+v", name, p)
		}
	}
	for name, flag := range map[string]string{"codex": "--dangerously-bypass-approvals-and-sandbox", "claude": "--dangerously-skip-permissions", "cursor": "", "grok": "", "gemini": ""} {
		p := PermissionFor(name)
		mode := apiv1.ThreadPermissionAlwaysAllow
		if flag == "" {
			mode = apiv1.ThreadPermissionNotQualified
		}
		if p.Mode != mode || p.Flag != flag || p.NotBypassed == nil {
			t.Errorf("permission for %s: %+v", name, p)
		}
	}
	if p := PermissionFor("claude"); !slices.Contains(p.NotBypassed, "claude_critical_path_check") {
		t.Error("claude must report its critical-path check as not bypassed")
	}
	if _, ok := Lookup("Codex"); ok {
		t.Error("lookup must be exact")
	}
	for code := range messages {
		if Message(code) == "" {
			t.Errorf("empty message for %s", code)
		}
	}
}

// The decoder refuses anything but one complete, consistent result.
func TestDecodeRefuses(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 9, 18, 0, 0, 0, time.UTC)}
	good, _ := Encode(NewRecorder("codex", c.Now).Finish(apiv1.ThreadFailed, "invalid_input", "", time.Minute))
	if _, err := apiv1.DecodeThreadLaunch(good); err != nil {
		t.Fatalf("good result refused: %v", err)
	}
	for name, raw := range map[string]string{
		"empty":    "",
		"trailing": string(good) + "{}",
		"brace":    strings.TrimSuffix(string(good), "\n") + "}",
		"unknown":  `{"version":"thread-launch.v1","outcome":"failed","provider":"codex","phases":{},"extra":1}`,
		"version":  `{"version":"rc-launch.v1","outcome":"failed","provider":"codex","phases":{}}`,
		"phases":   `{"link":null,"version":"thread-launch.v1","outcome":"failed","provider":"codex","phases":{},"totalMs":0,"deadlineMs":0,"error":{"code":"x","message":"y"}}`,
	} {
		if _, err := apiv1.DecodeThreadLaunch([]byte(raw)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	cursor := NewRecorder("cursor", c.Now).Finish(apiv1.ThreadResponded, "", "", time.Minute)
	if b, _ := Encode(cursor); b == nil {
		t.Fatal("encode")
	} else if _, err := apiv1.DecodeThreadLaunch(b); err == nil {
		t.Error("responded without always-allow accepted")
	}
	noPerm := NewRecorder("codex", c.Now).Finish(apiv1.ThreadFailed, "invalid_input", "", time.Minute)
	noPerm.Permission = nil
	if b, _ := Encode(noPerm); b != nil {
		if _, err := apiv1.DecodeThreadLaunch(b); err == nil {
			t.Error("a result without its permission mode accepted")
		}
	}
	unobserved := NewRecorder("codex", c.Now)
	unobserved.Result.ThreadID = "0199a000-0000-7000-8000-000000000002"
	if b, _ := Encode(unobserved.Finish(apiv1.ThreadResponded, "", "", time.Minute)); b != nil {
		if _, err := apiv1.DecodeThreadLaunch(b); err == nil {
			t.Error("responded without an observed first response accepted")
		}
	}
	ok := NewRecorder("codex", c.Now)
	responded := ok.Finish(apiv1.ThreadResponded, "", "", time.Minute)
	responded.Error = &apiv1.RCLaunchError{Code: "x"}
	b, _ := Encode(responded)
	if _, err := apiv1.DecodeThreadLaunch(b); err == nil {
		t.Error("responded with an error accepted")
	}
}
