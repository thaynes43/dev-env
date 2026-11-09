package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	utilexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/threadlaunch"
)

// A synthetic handoff: newline, tab and the characters json.Marshal escapes,
// so the test sees it reach the host's stdin exactly as sent.
const threadTestHandoff = "SYNTHETIC_PRIVATE_THREAD_HANDOFF_495: make a worktree first.\n\t<a & b>"

// threadExec plays the host's agentd for the thread route: it records each
// command and its stdin and answers with out, stderr and code, or err.
type threadExec struct {
	mu     sync.Mutex
	cmds   []string
	stdin  []string
	out    string
	stderr string
	code   int
	err    error
	delay  time.Duration
}

func (e *threadExec) Run(ctx context.Context, ns, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var in []byte
	if stdin != nil {
		in, _ = io.ReadAll(stdin)
	}
	e.mu.Lock()
	e.cmds = append(e.cmds, ns+"/"+pod+"/"+container+": "+strings.Join(cmd, " "))
	e.stdin = append(e.stdin, string(in))
	e.mu.Unlock()
	if e.delay > 0 {
		select {
		case <-time.After(e.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	_, _ = io.WriteString(stdout, e.out)
	_, _ = io.WriteString(stderr, e.stderr)
	if e.err != nil {
		return e.err
	}
	if e.code != 0 {
		return utilexec.CodeExitError{Err: errors.New("command terminated with non-zero exit code"), Code: e.code}
	}
	return nil
}

func (e *threadExec) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.cmds)
}

// codexHostPod adds a Running Codex interactive host whose pod is Running and
// Ready, the kind agent-run pair serves.
func (f *fixture) codexHostPod(name string) {
	f.t.Helper()
	f.sessionPod(name, "full", 0)
	s := f.session(name)
	s.Spec.Agent, s.Spec.Mode, s.Spec.Model, s.Spec.Prompt = v1alpha1.AgentCodex, v1alpha1.ModeRemote, "gpt-6-astra", ""
	if err := f.c.Update(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
	s = f.session(name)
	s.Status.Phase = v1alpha1.PhaseRunning
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
	f.runPod(name)
}

// hostThreadOutput is what a host's agentd prints: one result line.
func hostThreadOutput(t *testing.T, host, outcome, code string) string {
	t.Helper()
	rec := threadlaunch.NewRecorder("codex", func() time.Time { return time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC) })
	rec.Result.Host, rec.Result.SessionName, rec.Result.Name, rec.Result.ThreadID, rec.Result.Source = host, host, "thread-1", "0199a000-0000-7000-8000-000000000002", "vscode"
	rec.Phase(apiv1.ThreadPhaseHostReady, "ok", "codex_daemon", "remote_connected")
	rec.Unknown(apiv1.ThreadPhasePaired, "host_marker", "pair_code_issued")
	if outcome == apiv1.ThreadResponded {
		rec.Phase(apiv1.ThreadPhaseFirstResponse, "ok", "thread_items", "agent_message")
	}
	b, err := threadlaunch.Encode(rec.Finish(outcome, code, apiv1.ThreadPhaseFirstResponse, 5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func threadRequest() apiv1.ThreadStartRequest {
	return apiv1.ThreadStartRequest{Model: "gpt-6-astra", Effort: "xhigh", Name: "thread-1", Repo: "dev-env", Wait: "5m0s", Handoff: threadTestHandoff}
}

// threadAnswer decodes the route's 200 strictly, as agent-run does.
func threadAnswer(t *testing.T, w *httptest.ResponseRecorder) apiv1.ThreadLaunchResult {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "SYNTHETIC_PRIVATE") {
		t.Fatal("the handoff reached the answer")
	}
	r, err := apiv1.DecodeThreadLaunch(bytes.TrimSpace(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("not one thread-launch.v1 result: %v %s", err, w.Body.String())
	}
	return r
}

func sameResult(t *testing.T, got apiv1.ThreadLaunchResult, host string) {
	t.Helper()
	want, err := apiv1.DecodeThreadLaunch([]byte(strings.TrimSuffix(host, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	g, _ := json.Marshal(got)
	h, _ := json.Marshal(want)
	if !bytes.Equal(g, h) {
		t.Fatalf("the host's result changed on the way\n got %s\nwant %s", g, h)
	}
}

// An ordinary session pod's caller, Tom and a trusted client are all served:
// the operator runs the host's own thread-start with the handoff on stdin only
// and relays its result unchanged, phases and permission included (OP495).
func TestStartThreadServesSessionCallers(t *testing.T) {
	f := newFixture(t)
	var logs bytes.Buffer
	f.srv.Log = funcr.New(func(prefix, args string) { logs.WriteString(prefix + " " + args + "\n") }, funcr.Options{})
	out := hostThreadOutput(t, "codex-host", apiv1.ThreadResponded, "")
	ex := &threadExec{out: out}
	f.srv.Exec = ex
	f.codexHostPod("codex-host")
	tok := f.sessionPod("s-caller", "full", 0)

	res := threadAnswer(t, f.do(http.MethodPost, apiv1.SessionThreadsPath("codex-host"), tok, threadRequest()))
	sameResult(t, res, out)
	if res.Outcome != apiv1.ThreadResponded || res.Permission.Mode != apiv1.ThreadPermissionAlwaysAllow || res.Permission.Flag != "--dangerously-bypass-approvals-and-sandbox" || res.Phases[apiv1.ThreadPhasePaired].Reason != "pair_code_issued" {
		t.Fatalf("result %+v", res)
	}
	want := "dev-agents/codex-host/agent: agentd ctl thread-start --model gpt-6-astra --effort xhigh --name thread-1 --wait 5m0s --repo dev-env"
	if len(ex.cmds) != 1 || ex.cmds[0] != want || ex.stdin[0] != threadTestHandoff {
		t.Fatalf("commands %q, stdin %q", ex.cmds, ex.stdin)
	}
	if strings.Contains(strings.Join(ex.cmds, " "), "SYNTHETIC") {
		t.Fatal("the handoff reached the argv")
	}
	line := logs.String()
	if strings.Contains(line, "SYNTHETIC_PRIVATE") || !strings.Contains(line, `"caller"="session/s-caller"`) || !strings.Contains(line, `"handoffBytes"=`+itoa(len(threadTestHandoff))) {
		t.Fatalf("log %s", line)
	}

	// Tom and a trusted client take the same route; the repo is optional.
	req := threadRequest()
	req.Repo = ""
	for _, tok := range []string{tokHuman, tokClient} {
		sameResult(t, threadAnswer(t, f.do(http.MethodPost, apiv1.SessionThreadsPath("codex-host"), tok, req)), out)
	}
	if len(ex.cmds) != 3 || strings.Contains(ex.cmds[2], "--repo") {
		t.Fatalf("commands %q", ex.cmds)
	}

	// A pending host result keeps its outcome; the route never resends.
	ex.out, ex.code = hostThreadOutput(t, "codex-host", apiv1.ThreadVisible, "response_pending"), 7
	if res := threadAnswer(t, f.do(http.MethodPost, apiv1.SessionThreadsPath("codex-host"), tok, threadRequest())); res.Outcome != apiv1.ThreadVisible || res.Error.Code != "response_pending" {
		t.Fatalf("result %+v", res)
	}
	if len(ex.cmds) != 4 {
		t.Fatalf("commands %q", ex.cmds)
	}
}

// A host that is not a Running Codex interactive session is refused before
// any exec, with the thread-launch code agent-run's exec path prints.
func TestStartThreadRefusesHostBeforeExec(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*fixture)
		code   string
	}{
		"claude remote": {func(f *fixture) {
			f.editSession("codex-host", func(s *v1alpha1.AgentSession) { s.Spec.Agent = v1alpha1.AgentClaude })
		}, "not_codex_host"},
		"codex task": {func(f *fixture) {
			f.editSession("codex-host", func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeTask })
		}, "not_codex_host"},
		"codex local": {func(f *fixture) {
			f.editSession("codex-host", func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeLocal })
		}, "not_codex_host"},
		"suspended": {func(f *fixture) {
			f.editSession("codex-host", func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended })
		}, "host_not_running"},
		"pending": {func(f *fixture) { f.editStatus("codex-host", v1alpha1.PhasePending) }, "host_not_running"},
		"pod not ready": {func(f *fixture) {
			f.editPod("codex-host", func(p *corev1.Pod) { p.Status.Conditions[0].Status = corev1.ConditionFalse })
		}, "host_not_running"},
		"hold pod": {func(f *fixture) {
			f.editPod("codex-host", func(p *corev1.Pod) { p.Labels[v1alpha1.LabelHold] = "true" })
		}, "host_not_running"},
		"no pod": {func(f *fixture) { f.deletePod("codex-host") }, "host_not_running"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ex := &threadExec{out: hostThreadOutput(t, "codex-host", apiv1.ThreadResponded, "")}
			f.srv.Exec = ex
			f.codexHostPod("codex-host")
			tc.mutate(f)
			res := threadAnswer(t, f.do(http.MethodPost, apiv1.SessionThreadsPath("codex-host"), tokHuman, threadRequest()))
			p := res.Phases[apiv1.ThreadPhaseHostReady]
			if res.Outcome != apiv1.ThreadFailed || res.Error.Code != tc.code || res.Error.Message != threadlaunch.Message(tc.code) || p.State != "error" || p.EvidenceSource != "api" ||
				res.Host != "codex-host" || res.Name != "thread-1" || res.Permission.Mode != apiv1.ThreadPermissionAlwaysAllow || ex.calls() != 0 {
				t.Fatalf("result %+v, %d execs", res, ex.calls())
			}
		})
	}
}

// Refusals of the request itself are API errors, and none of them execs:
// a missing session, a coordinator host (thread-start takes no target fences),
// the portal or an unknown identity, no token, and an API that cannot exec.
func TestStartThreadRefusesRequests(t *testing.T) {
	f := coordinatorFixture(t)
	ex := &threadExec{out: hostThreadOutput(t, "codex-host", apiv1.ThreadResponded, "")}
	f.srv.Exec = ex
	f.codexHostPod("codex-host")
	path := apiv1.SessionThreadsPath("codex-host")
	wantError(t, f.do(http.MethodPost, apiv1.SessionThreadsPath("missing"), tokHuman, threadRequest()), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodPost, path, tokCoordinator, threadRequest()), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, f.do(http.MethodPost, path, tokStranger, threadRequest()), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, f.do(http.MethodPost, path, "", threadRequest()), http.StatusUnauthorized, apiv1.CodeUnauthenticated)
	wantError(t, f.do(http.MethodGet, path, tokHuman, nil), http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed)
	f.srv.Exec = nil
	wantError(t, f.do(http.MethodPost, path, tokHuman, threadRequest()), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if ex.calls() != 0 {
		t.Fatalf("%d execs", ex.calls())
	}
}

// The route applies agent-run's input rules again before any read or exec.
func TestStartThreadRefusesInvalidInput(t *testing.T) {
	f := newFixture(t)
	ex := &threadExec{out: hostThreadOutput(t, "codex-host", apiv1.ThreadResponded, "")}
	f.srv.Exec = ex
	f.codexHostPod("codex-host")
	path := apiv1.SessionThreadsPath("codex-host")
	for name, edit := range map[string]func(*apiv1.ThreadStartRequest){
		"empty handoff":   func(r *apiv1.ThreadStartRequest) { r.Handoff = "" },
		"blank handoff":   func(r *apiv1.ThreadStartRequest) { r.Handoff = " \n\t" },
		"long handoff":    func(r *apiv1.ThreadStartRequest) { r.Handoff = strings.Repeat("x", apiv1.ThreadMaxHandoffBytes+1) },
		"escape handoff":  func(r *apiv1.ThreadStartRequest) { r.Handoff = "a\x1b[201~b" },
		"nul handoff":     func(r *apiv1.ThreadStartRequest) { r.Handoff = "a\x00b" },
		"c1 handoff":      func(r *apiv1.ThreadStartRequest) { r.Handoff = "a\u0085b" },
		"zero wait":       func(r *apiv1.ThreadStartRequest) { r.Wait = "0s" },
		"long wait":       func(r *apiv1.ThreadStartRequest) { r.Wait = "11m" },
		"word wait":       func(r *apiv1.ThreadStartRequest) { r.Wait = "soon" },
		"no wait":         func(r *apiv1.ThreadStartRequest) { r.Wait = "" },
		"bad name":        func(r *apiv1.ThreadStartRequest) { r.Name = "a b" },
		"no name":         func(r *apiv1.ThreadStartRequest) { r.Name = "" },
		"alias model":     func(r *apiv1.ThreadStartRequest) { r.Model = "gpt" },
		"no effort":       func(r *apiv1.ThreadStartRequest) { r.Effort = "" },
		"bad repo":        func(r *apiv1.ThreadStartRequest) { r.Repo = ".." },
		"claude model id": func(r *apiv1.ThreadStartRequest) { r.Model = "claude-opus-5-5" },
	} {
		t.Run(name, func(t *testing.T) {
			req := threadRequest()
			edit(&req)
			e := wantError(t, f.do(http.MethodPost, path, tokHuman, req), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
			if strings.Contains(e.Message, "SYNTHETIC") || strings.Contains(e.Message, "soon") {
				t.Fatalf("the refusal repeats the request: %s", e.Message)
			}
		})
	}
	wantError(t, f.do(http.MethodPost, path, tokHuman, `{"model":"gpt-6-astra","effort":"xhigh","name":"t","wait":"1m","handoff":"x","sandbox":"none"}`), http.StatusBadRequest, apiv1.CodeBadRequest)
	if ex.calls() != 0 {
		t.Fatalf("%d execs", ex.calls())
	}
	// The largest handoff fits the body even when every byte is escaped.
	req := threadRequest()
	req.Handoff = strings.Repeat("<", apiv1.ThreadMaxHandoffBytes)
	threadAnswer(t, f.do(http.MethodPost, path, tokHuman, req))
	if ex.calls() != 1 || ex.stdin[0] != req.Handoff {
		t.Fatalf("%d execs", ex.calls())
	}
}

// An agentd without thread-start is host_unsupported; a lost exec stream, a
// result that disagrees with its exit code, another host's result or anything
// that is not one result may have launched: unconfirmed, never retried.
func TestStartThreadHostAnswers(t *testing.T) {
	visible := hostThreadOutput(t, "codex-host", apiv1.ThreadVisible, "response_pending")
	for name, tc := range map[string]struct {
		ex            *threadExec
		outcome, code string
	}{
		"stream lost":    {&threadExec{err: errors.New("stream reset")}, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		"old agentd":     {&threadExec{code: 2, stderr: "agentd: usage: ctl status | rescue\n"}, apiv1.ThreadFailed, "host_unsupported"},
		"crash":          {&threadExec{code: 2, stderr: "panic: boom\n"}, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		"silent failure": {&threadExec{code: 1}, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		"exit mismatch":  {&threadExec{out: visible}, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		"garbage":        {&threadExec{out: "not a result\n", code: 1}, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		"two results":    {&threadExec{out: visible + visible, code: 7}, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		"other host":     {&threadExec{out: hostThreadOutput(t, "other-host", apiv1.ThreadVisible, "response_pending"), code: 7}, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		"oversize":       {&threadExec{out: strings.Repeat("x", apiv1.ThreadLaunchMaxBytes+10), code: 1}, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		"pending":        {&threadExec{out: visible, code: 7}, apiv1.ThreadVisible, "response_pending"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ex := tc.ex
			f.srv.Exec = ex
			f.codexHostPod("codex-host")
			res := threadAnswer(t, f.do(http.MethodPost, apiv1.SessionThreadsPath("codex-host"), tokHuman, threadRequest()))
			if res.Outcome != tc.outcome || res.Error == nil || res.Error.Code != tc.code || ex.calls() != 1 {
				t.Fatalf("result %+v, %d execs", res.Error, ex.calls())
			}
			if tc.outcome == apiv1.ThreadUnconfirmed && (res.Error.Phase != apiv1.ThreadPhaseClientStarted || res.Host != "codex-host") {
				t.Fatalf("result %+v", res)
			}
		})
	}
}

// The listener's read and write timeouts (30 s and 60 s in Runner) would cut
// a launch that takes its whole wait; the route moves both for its request,
// over HTTP/1.1 and HTTP/2. The message route, which does not, is the control.
func TestStartThreadOutlivesListenerTimeouts(t *testing.T) {
	f := newFixture(t)
	ex := &threadExec{out: hostThreadOutput(t, "codex-host", apiv1.ThreadResponded, ""), delay: 600 * time.Millisecond}
	f.srv.Exec = ex
	f.codexHostPod("codex-host")
	post := func(srv *httptest.Server, path string, body any) (*http.Response, error) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokHuman)
		return srv.Client().Do(req)
	}
	for _, h2 := range []bool{false, true} {
		srv := httptest.NewUnstartedServer(f.h)
		srv.Config.ReadTimeout, srv.Config.WriteTimeout = 200*time.Millisecond, 200*time.Millisecond
		srv.EnableHTTP2 = h2
		srv.StartTLS()
		resp, err := post(srv, apiv1.SessionThreadsPath("codex-host"), threadRequest())
		if err != nil {
			srv.Close()
			t.Fatalf("http2 %v: %v", h2, err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || (resp.ProtoMajor == 2) != h2 {
			srv.Close()
			t.Fatalf("http2 %v: %d %s %v %q", h2, resp.StatusCode, resp.Proto, err, raw)
		}
		if res, err := apiv1.DecodeThreadLaunch(bytes.TrimSpace(raw)); err != nil || res.Outcome != apiv1.ThreadResponded {
			srv.Close()
			t.Fatalf("http2 %v: %v %+v", h2, err, res)
		}
		// The control: a message held as long is cut by the same timeouts.
		resp, err = post(srv, apiv1.SessionMessagesPath("codex-host"), apiv1.MessageRequest{Text: "hi"})
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusAccepted {
				srv.Close()
				t.Fatalf("http2 %v: the control outlived the timeouts, so this test proves nothing", h2)
			}
		}
		srv.Close()
	}
}

func (f *fixture) editSession(name string, edit func(*v1alpha1.AgentSession)) {
	f.t.Helper()
	s := f.session(name)
	edit(s)
	if err := f.c.Update(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) editStatus(name string, phase v1alpha1.SessionPhase) {
	f.t.Helper()
	s := f.session(name)
	s.Status.Phase = phase
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) editPod(name string, edit func(*corev1.Pod)) {
	f.t.Helper()
	// The edit is applied twice, to the object and to its status: an update
	// reads the stored status back into the object.
	for _, write := range []func(context.Context, client.Object) error{
		func(ctx context.Context, o client.Object) error { return f.c.Update(ctx, o) },
		func(ctx context.Context, o client.Object) error { return f.c.Status().Update(ctx, o) },
	} {
		var pod corev1.Pod
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: name}, &pod); err != nil {
			f.t.Fatal(err)
		}
		edit(&pod)
		if err := write(context.Background(), &pod); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) deletePod(name string) {
	f.t.Helper()
	pod := &corev1.Pod{}
	pod.Name, pod.Namespace = name, sessionNS
	if err := f.c.Delete(context.Background(), pod); err != nil {
		f.t.Fatal(err)
	}
}
