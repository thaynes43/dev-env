package apiserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// Canaries: none of these values may appear in any response body, error, log or
// recorded command line, except where a test says a response carries one.
const (
	canaryCode    = "CANARYCODE9f3a7c#CANARYSTATE41d2"
	canaryToken   = "sk-ant-CANARYTOKEN-0123456789"
	canaryNoise   = "CANARYNOISE-echo-line"
	claudeLinkURL = "https://claude.com/cai/oauth/authorize?client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&state=LINKSTATE77&code_challenge=LINKCHAL88"
	codexLinkURL  = "https://auth.openai.com/codex/device"
	codexUserCode = "ABCD-12345"
)

// fakeKeeperExec stands in for the keeper pod's login containers. RunTTY plays
// the helper: it prints terminal noise and the link as JSON lines, echoes what
// it reads (a terminal does), and ends with the keeper's fixed result word.
type fakeKeeperExec struct {
	mu sync.Mutex
	// calls records namespace/pod/container: argv for every exec, plain or TTY.
	calls []string
	stdin []byte
	// statusOut is what the status command prints.
	statusOut string
	// hold: RunTTY waits for the code (or a Ctrl-C) before it ends.
	// link is what the helper prints first; empty prints none.
	link, userCode string
	// afterLink lines print right after the link (extra members, noise).
	afterLink []string
	// onCode is the helper's final line for a code; cancelFinal for a Ctrl-C.
	finalForCode, finalForCancel string
	// noExit: the helper ignores Ctrl-C until the context is cut (a stuck helper).
	noExit bool
	// endNow: the helper ends right after its output (a refusal such as Busy).
	endNow bool
	// hangAfterCode: the helper takes the code and never answers (a slow adoption).
	hangAfterCode bool
	ttyN          int
}

func (e *fakeKeeperExec) record(ns, pod, container string, cmd []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, ns+"/"+pod+"/"+container+": "+strings.Join(cmd, " "))
}

func (e *fakeKeeperExec) callList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (e *fakeKeeperExec) stdinBytes() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return string(e.stdin)
}

func (e *fakeKeeperExec) Run(_ context.Context, ns, pod, container string, cmd []string, stdin io.Reader, stdout, _ io.Writer) error {
	e.record(ns, pod, container, cmd)
	if stdin != nil {
		return errors.New("a status command takes no stdin")
	}
	_, _ = io.WriteString(stdout, e.statusOut)
	return nil
}

func (e *fakeKeeperExec) RunTTY(ctx context.Context, ns, pod, container string, cmd []string, stdin io.Reader, stdout io.Writer) error {
	e.record(ns, pod, container, cmd)
	e.mu.Lock()
	e.ttyN++
	e.mu.Unlock()
	w := func(s string) { _, _ = io.WriteString(stdout, strings.ReplaceAll(s, "\n", "\r\n")) }
	w("\x1b[2Jterminal noise " + canaryNoise + "\n")
	if e.link != "" {
		w(fmt.Sprintf("{\"signInUrl\":%q}\n", e.link))
	}
	if e.userCode != "" {
		w(fmt.Sprintf("{\"verificationUrl\":%q}\n", e.link))
		w(fmt.Sprintf("{\"userCode\":%q}\n", e.userCode))
	}
	for _, l := range e.afterLink {
		w(l + "\n")
	}
	if e.endNow {
		return nil
	}
	// Read the terminal until Enter or Ctrl-C, echoing, as a tty does.
	buf := make([]byte, 1)
	var got []byte
	read := make(chan byte)
	go func() {
		for {
			n, err := stdin.Read(buf)
			if err != nil || n == 0 {
				close(read)
				return
			}
			read <- buf[0]
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case b, ok := <-read:
			if !ok {
				return io.ErrClosedPipe
			}
			e.mu.Lock()
			e.stdin = append(e.stdin, b)
			e.mu.Unlock()
			switch b {
			case 0x03:
				if e.noExit {
					continue
				}
				w(e.finalForCancel + "\n")
				return nil
			case '\r':
				w(string(got) + "\n") // the echo of the typed line
				if e.hangAfterCode {
					<-ctx.Done()
					return ctx.Err()
				}
				w(e.finalForCode + "\n")
				return nil
			default:
				got = append(got, b)
			}
		}
	}
}

// codexExec is the device flow: no stdin, it ends on its own after a pause.
type codexExec struct {
	*fakeKeeperExec
	finish chan struct{}
}

func (c *codexExec) RunTTY(ctx context.Context, ns, pod, container string, cmd []string, stdin io.Reader, stdout io.Writer) error {
	c.record(ns, pod, container, cmd)
	_, _ = io.WriteString(stdout, "{\"verificationUrl\":\""+codexLinkURL+"\"}\r\n{\"userCode\":\""+codexUserCode+"\"}\r\n")
	select {
	case <-c.finish:
		_, _ = io.WriteString(stdout, "{\"ok\":true,\"code\":\"Adopted\",\"generation\":5,\"login\":{\"phase\":\"Adopted\"}}\r\n")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

const keeperNS = "dev-env-system"

func keeperPodObject(name string, mut ...func(*corev1.Pod)) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: keeperNS, Labels: map[string]string{
			keeperPodLabelName: keeperPodName, keeperPodLabelController: keeperPodName}},
		Spec: corev1.PodSpec{ServiceAccountName: keeperServiceAccount, Containers: []corev1.Container{{Name: "app"}, {Name: "claude-login"}, {Name: "codex-login"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				{Name: "claude-login", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				{Name: "codex-login", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			}},
	}
	for _, m := range mut {
		m(p)
	}
	return p
}

type keeperFixture struct {
	*fixture
	exec *fakeKeeperExec
	logs *syncBuf
	// writes records every write the operator's client made during a test.
	writes *[]string
	// allowSecretReads lifts the guard for the test's own reads.
	allowSecretReads *atomic.Bool
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

const (
	tokWorkbench    = "tok-workbench-unbound"
	journalName     = "dev-env-keeper-claude-auth"
	projectionName  = "dev-env-claude-live"
	journalMarker   = "journal-untouched-marker"
	projectionValue = "projection-untouched-marker"
)

// newKeeperFixture is a server with the keeper pod, its journal and projection
// Secrets, a client that fails on any write or any Secret access, and a log
// that records everything.
func newKeeperFixture(t *testing.T, ex *fakeKeeperExec) *keeperFixture {
	t.Helper()
	var writes []string
	var wmu sync.Mutex
	noteWrite := func(what string) {
		wmu.Lock()
		writes = append(writes, what)
		wmu.Unlock()
	}
	isSecret := func(o client.Object) bool { _, ok := o.(*corev1.Secret); return ok }
	var allow atomic.Bool
	funcs := interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			noteWrite("create " + o.GetName())
			return c.Create(ctx, o, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			noteWrite("update " + o.GetName())
			return c.Update(ctx, o, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			noteWrite("patch " + o.GetName())
			return c.Patch(ctx, o, p, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
			noteWrite("delete " + o.GetName())
			return c.Delete(ctx, o, opts...)
		},
		Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if isSecret(o) && !allow.Load() {
				t.Errorf("the operator read Secret %s", k)
			}
			return c.Get(ctx, k, o, opts...)
		},
	}
	journal := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: journalName, Namespace: keeperNS}, StringData: map[string]string{"marker": journalMarker}}
	projection := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: projectionName, Namespace: sessionNS}, StringData: map[string]string{"marker": projectionValue}}
	f := newFixtureWith(t, funcs, keeperPodObject("dev-env-keeper-abc"), journal, projection)
	logs := &syncBuf{}
	f.srv.Log = funcr.New(func(prefix, args string) { _, _ = logs.Write([]byte(prefix + " " + args + "\n")) }, funcr.Options{Verbosity: 10})
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
	f.auth[tokWorkbench] = saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
	f.srv.KeeperLogin = &KeeperLogin{Namespace: keeperNS, Exec: ex, Leader: func() bool { return true },
		URLWait: 2 * time.Second, CodeWait: 2 * time.Second, StopWait: time.Second}
	f.h = f.srv.Handler()
	// Writes made while building the fixture do not count.
	wmu.Lock()
	writes = nil
	wmu.Unlock()
	return &keeperFixture{fixture: f, exec: ex, logs: logs, writes: &writes, allowSecretReads: &allow}
}

// secretsUntouched fails if the operator's client wrote anything at all.
func (k *keeperFixture) secretsUntouched() {
	k.t.Helper()
	if len(*k.writes) != 0 {
		k.t.Fatalf("the operator's client wrote: %v", *k.writes)
	}
}

func claudeExec() *fakeKeeperExec {
	return &fakeKeeperExec{
		link:           claudeLinkURL,
		afterLink:      []string{`{"signInUrl":"` + claudeLinkURL + `","accessToken":"` + canaryToken + `"}`, canaryNoise},
		finalForCode:   `{"ok":true,"code":"Adopted","generation":3,"login":{"phase":"Adopted"}}`,
		finalForCancel: `{"ok":false,"code":"NeedsLogin","login":{"phase":"Native","failureCode":"NativeExit"}}`,
	}
}

func (k *keeperFixture) noLeak(what string, bodies ...string) {
	k.t.Helper()
	all := strings.Join(append([]string{k.logs.String(), strings.Join(k.exec.callList(), "\n")}, bodies...), "\n")
	for _, secret := range []string{canaryCode, canaryToken, canaryNoise} {
		if strings.Contains(all, secret) {
			k.t.Fatalf("%s: canary %q reached a response, error, log or command line", what, secret[:12])
		}
	}
}

// Only the workbench may call the routes. Everyone else is refused before the
// operator looks at the keeper.
func TestKeeperLoginRefusesEveryCallerButTheWorkbench(t *testing.T) {
	ex := claudeExec()
	k := newKeeperFixture(t, ex)
	stranger := k.sessionPod("ordinary-task", "dev", 0)
	for name, tok := range map[string]string{"human": tokHuman, "v1 client": tokClient, "stranger": tokStranger, "ordinary session": stranger} {
		for _, rq := range []struct{ method, path string }{
			{http.MethodGet, apiv1.KeeperLoginPathFor("claude")},
			{http.MethodPost, apiv1.KeeperLoginPathFor("claude") + "/url"},
			{http.MethodPost, apiv1.KeeperLoginPathFor("claude") + "/code"},
			{http.MethodDelete, apiv1.KeeperLoginPathFor("claude")},
			{http.MethodGet, apiv1.KeeperLoginPathFor("codex")},
		} {
			w := k.do(rq.method, rq.path, tok, apiv1.KeeperLoginCode{Code: "x"})
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s %s: status %d, want 403: %s", name, rq.method, rq.path, w.Code, w.Body.String())
			}
		}
	}
	if len(ex.callList()) != 0 {
		t.Fatalf("a refused caller reached the keeper pod: %v", ex.callList())
	}
	// No token at all and an unknown token are 401, as everywhere.
	if w := k.do(http.MethodGet, apiv1.KeeperLoginPathFor("claude"), "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	// The workbench's unbound token and a workbench session's own pod token pass.
	ex.statusOut = `{"ok":true,"code":"Ready","generation":2}` + "\r\n"
	if w := k.do(http.MethodGet, apiv1.KeeperLoginPathFor("claude"), tokWorkbench, nil); w.Code != http.StatusOK {
		t.Fatalf("workbench client: %d %s", w.Code, w.Body.String())
	}
	wbPod := k.sessionPod("wb-1020-000000", "workbench", 0)
	id := k.auth[wbPod]
	id.ServiceAccount, id.Username = templates.WorkbenchServiceAccount, serviceAccountPrefix+sessionNS+":"+templates.WorkbenchServiceAccount
	k.auth[wbPod] = id // the pod runs as the workbench account
	if w := k.do(http.MethodGet, apiv1.KeeperLoginPathFor("claude"), wbPod, nil); w.Code != http.StatusOK {
		t.Fatalf("workbench session: %d %s", w.Code, w.Body.String())
	}
}

func TestKeeperLoginUnknownProvider(t *testing.T) {
	k := newKeeperFixture(t, claudeExec())
	wantError(t, k.do(http.MethodGet, apiv1.KeeperLoginPath+"/gemini", tokWorkbench, nil), http.StatusNotFound, apiv1.CodeNotFound)
}

// status returns the state word, the generation and two times, and nothing
// else, whatever the helper prints; and it runs in the login container only.
func TestKeeperLoginStatusReturnsStateAndExpiryOnly(t *testing.T) {
	ex := claudeExec()
	ex.statusOut = "terminal noise " + canaryNoise + "\r\n" +
		`{"ok":true,"code":"Ready","generation":4,"attemptUID":"` + canaryToken + `","accessExpiresAt":"2026-10-20T08:29:48Z","loginExpiresAt":"2026-11-17T12:00:00Z","accessToken":"` + canaryToken + `","login":{"phase":"x"}}` + "\r\n"
	k := newKeeperFixture(t, ex)
	w := k.do(http.MethodGet, apiv1.KeeperLoginPathFor("claude"), tokWorkbench, nil)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	got := decode[apiv1.KeeperLoginStatus](t, w)
	wantAccess := time.Date(2026, 10, 20, 8, 29, 48, 0, time.UTC)
	wantLogin := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	if got.Provider != "claude" || got.State != "Ready" || got.Generation != 4 ||
		got.AccessExpiresAt == nil || !got.AccessExpiresAt.Equal(wantAccess) || got.LoginExpiresAt == nil || !got.LoginExpiresAt.Equal(wantLogin) {
		t.Fatalf("status = %+v", got)
	}
	// Exactly the five members of the wire type.
	for _, member := range []string{"attemptUID", "accessToken", "login\"", "ok\""} {
		if strings.Contains(w.Body.String(), member) {
			t.Errorf("status body carries %q: %s", member, w.Body.String())
		}
	}
	calls := ex.callList()
	if len(calls) != 1 || calls[0] != "dev-env-system/dev-env-keeper-abc/claude-login: agentd claude-login-status" {
		t.Fatalf("exec calls = %v", calls)
	}
	k.noLeak("status", w.Body.String())
	k.secretsUntouched()
}

func TestKeeperLoginStatusCodexAndUnavailable(t *testing.T) {
	ex := claudeExec()
	ex.statusOut = `{"ok":true,"code":"PendingPublication","generation":1,"accessExpiresAt":"2026-10-29T19:44:00Z"}` + "\r\n"
	k := newKeeperFixture(t, ex)
	got := decode[apiv1.KeeperLoginStatus](t, k.do(http.MethodGet, apiv1.KeeperLoginPathFor("codex"), tokWorkbench, nil))
	if got.State != "PendingPublication" || got.Generation != 1 || got.AccessExpiresAt == nil || got.LoginExpiresAt != nil {
		t.Fatalf("codex status = %+v", got)
	}
	if calls := ex.callList(); len(calls) != 1 || !strings.HasSuffix(calls[0], "/codex-login: dev-env-keeper codex-auth-status") {
		t.Fatalf("exec calls = %v", calls)
	}
	// A word the keeper never says, or no output, is Unavailable and nothing more.
	for _, out := range []string{`{"ok":true,"code":"Pwned","generation":9}`, "", "not json"} {
		ex.statusOut = out
		got = decode[apiv1.KeeperLoginStatus](t, k.do(http.MethodGet, apiv1.KeeperLoginPathFor("claude"), tokWorkbench, nil))
		if got.State != "Unavailable" || got.Generation != 0 || got.AccessExpiresAt != nil {
			t.Errorf("status for %q = %+v", out, got)
		}
	}
}

// A keeper pod that is missing, ambiguous, not Ready, on another account or
// without its container running is never entered.
func TestKeeperLoginPodSelection(t *testing.T) {
	for name, pods := range map[string][]*corev1.Pod{
		"none":          nil,
		"two":           {keeperPodObject("a"), keeperPodObject("b")},
		"not ready":     {keeperPodObject("a", func(p *corev1.Pod) { p.Status.Conditions[0].Status = corev1.ConditionFalse })},
		"other account": {keeperPodObject("a", func(p *corev1.Pod) { p.Spec.ServiceAccountName = "default" })},
		"container down": {keeperPodObject("a", func(p *corev1.Pod) {
			p.Status.ContainerStatuses[1].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
		})},
		"deleting": {keeperPodObject("a", func(p *corev1.Pod) { n := metav1.Now(); p.DeletionTimestamp = &n; p.Finalizers = []string{"x"} })},
	} {
		t.Run(name, func(t *testing.T) {
			ex := claudeExec()
			objs := make([]client.Object, 0, len(pods))
			for _, p := range pods {
				objs = append(objs, p)
			}
			f := newFixture(t, objs...)
			f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
			f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
			f.auth[tokWorkbench] = saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
			f.srv.KeeperLogin = &KeeperLogin{Namespace: keeperNS, Exec: ex, Leader: func() bool { return true }}
			f.h = f.srv.Handler()
			wantError(t, f.do(http.MethodGet, apiv1.KeeperLoginPathFor("claude"), tokWorkbench, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
			wantError(t, f.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/url", tokWorkbench, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
			if len(ex.callList()) != 0 {
				t.Fatalf("exec ran: %v", ex.callList())
			}
		})
	}
}

func TestKeeperLoginNotConfiguredAndNotLeader(t *testing.T) {
	ex := claudeExec()
	k := newKeeperFixture(t, ex)
	k.srv.KeeperLogin.Leader = func() bool { return false }
	for _, rq := range []struct{ method, path string }{
		{http.MethodPost, apiv1.KeeperLoginPathFor("claude") + "/url"},
		{http.MethodPost, apiv1.KeeperLoginPathFor("claude") + "/code"},
		{http.MethodDelete, apiv1.KeeperLoginPathFor("claude")},
	} {
		wantError(t, k.do(rq.method, rq.path, tokWorkbench, apiv1.KeeperLoginCode{Code: "x"}), http.StatusServiceUnavailable, apiv1.CodeNotLeader)
	}
	// Status needs no ceremony, so a follower answers it.
	ex.statusOut = `{"ok":true,"code":"Ready","generation":1}`
	if w := k.do(http.MethodGet, apiv1.KeeperLoginPathFor("claude"), tokWorkbench, nil); w.Code != http.StatusOK {
		t.Fatalf("follower status: %d", w.Code)
	}
	if len(ex.callList()) != 1 {
		t.Fatalf("a follower ran a ceremony: %v", ex.callList())
	}
	k.srv.KeeperLogin = nil
	wantError(t, k.do(http.MethodGet, apiv1.KeeperLoginPathFor("claude"), tokWorkbench, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
}

// url answers the link to the caller, only, and the operator logs none of it;
// the code then reaches the helper's stdin byte for byte; and no canary shows
// up in a response, an error, a log line or a command line.
func TestKeeperLoginClaudeCeremony(t *testing.T) {
	ex := claudeExec()
	k := newKeeperFixture(t, ex)
	w := k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/url", tokWorkbench, nil)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", w.Header().Get("Cache-Control"))
	}
	link := decode[apiv1.KeeperLoginURL](t, w)
	if link.URL != claudeLinkURL || link.UserCode != "" || link.Provider != "claude" || link.ExpiresAt.IsZero() {
		t.Fatalf("link = %+v", link)
	}
	if strings.Contains(w.Body.String(), canaryToken) || strings.Contains(w.Body.String(), canaryNoise) {
		t.Fatalf("a canary reached the link response: %s", w.Body.String())
	}
	// The link is in the response and in no log line.
	if strings.Contains(k.logs.String(), "LINKSTATE77") || strings.Contains(k.logs.String(), "code_challenge") {
		t.Fatalf("the link reached the log: %s", k.logs.String())
	}
	// A second ceremony while one is open is a conflict and starts no exec.
	wantError(t, k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/url", tokWorkbench, nil), http.StatusConflict, apiv1.CodeConflict)

	// A malformed code is refused without echoing it and without writing it.
	bad := k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/code", tokWorkbench, apiv1.KeeperLoginCode{Code: "has space " + canaryCode})
	wantError(t, bad, http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if strings.Contains(bad.Body.String(), "CANARY") || ex.stdinBytes() != "" {
		t.Fatalf("a refused code was echoed or sent: %s / %q", bad.Body.String(), ex.stdinBytes())
	}
	wantError(t, k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/code", tokWorkbench, `{"code":"a","extra":1}`), http.StatusBadRequest, apiv1.CodeBadRequest)

	w = k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/code", tokWorkbench, apiv1.KeeperLoginCode{Code: canaryCode})
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	res := decode[apiv1.KeeperLoginResult](t, w)
	if res.Result != "Adopted" || res.Generation != 3 || res.Provider != "claude" {
		t.Fatalf("result = %+v", res)
	}
	// The helper read the code on its terminal, and an Enter after it.
	if got := ex.stdinBytes(); got != canaryCode+"\r" {
		t.Fatalf("helper stdin = %q, want the code and a carriage return", got)
	}
	// The ceremony is over: a second code has nothing to go to.
	wantError(t, k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/code", tokWorkbench, apiv1.KeeperLoginCode{Code: "again"}), http.StatusConflict, apiv1.CodeConflict)

	// Every exec went to the login container, never the keeper's own.
	for _, c := range ex.callList() {
		if !strings.Contains(c, "/claude-login: agentd claude-login") {
			t.Errorf("exec call %q is not the claude-login container's helper", c)
		}
		if strings.Contains(c, "/app:") {
			t.Errorf("exec reached the keeper's main container: %q", c)
		}
	}
	// The code travelled on stdin only: not in a command line, a log, or a body.
	k.noLeak("ceremony", w.Body.String())
	if strings.Contains(k.logs.String(), "LINKSTATE") {
		t.Errorf("the link reached the log")
	}
	k.secretsUntouched()
}

// An abandoned ceremony, by cancel: the helper is told like a Ctrl-C, ends with
// NeedsLogin, and the operator changes nothing: no write, no Secret read, the
// journal and projection Secrets exactly as they were.
func TestKeeperLoginAbandonedCeremonyTouchesNothing(t *testing.T) {
	ex := claudeExec()
	k := newKeeperFixture(t, ex)
	if w := k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/url", tokWorkbench, nil); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	before := secretData(t, k)
	w := k.do(http.MethodDelete, apiv1.KeeperLoginPathFor("claude"), tokWorkbench, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if got := ex.stdinBytes(); got != "\x03" {
		t.Fatalf("the helper was sent %q, want one Ctrl-C", got)
	}
	// Cancel with nothing open is the same answer.
	if w := k.do(http.MethodDelete, apiv1.KeeperLoginPathFor("claude"), tokWorkbench, nil); w.Code != http.StatusNoContent {
		t.Fatalf("second cancel: %d", w.Code)
	}
	// A code now has no ceremony.
	wantError(t, k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/code", tokWorkbench, apiv1.KeeperLoginCode{Code: "late"}), http.StatusConflict, apiv1.CodeConflict)
	k.secretsUntouched()
	if after := secretData(t, k); after != before {
		t.Fatalf("journal or projection changed: %q -> %q", before, after)
	}
	// And a new ceremony may start.
	if w := k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/url", tokWorkbench, nil); w.Code != http.StatusOK {
		t.Fatalf("restart after cancel: %d %s", w.Code, w.Body.String())
	}
	k.noLeak("abandon")
}

// waitNoCeremony waits for the operator to finish abandoning a ceremony.
func (k *keeperFixture) waitNoCeremony(provider string) {
	k.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for k.srv.KeeperLogin.current(keeperLoginProviders[provider]) != nil {
		if time.Now().After(deadline) {
			k.t.Fatal("the abandoned ceremony was never forgotten")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// secretData reads the two Secrets with a client that has no guard, as one string.
func secretData(t *testing.T, k *keeperFixture) string {
	t.Helper()
	k.allowSecretReads.Store(true)
	defer k.allowSecretReads.Store(false)
	var out []string
	for _, key := range []client.ObjectKey{{Namespace: keeperNS, Name: journalName}, {Namespace: sessionNS, Name: projectionName}} {
		var s corev1.Secret
		if err := k.c.Get(context.Background(), key, &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s@%s=%s", key.Name, s.ResourceVersion, s.StringData["marker"]))
	}
	return strings.Join(out, ";")
}

// A helper that ignores Ctrl-C is cut after the stop wait, and the ceremony is
// forgotten either way.
func TestKeeperLoginStuckHelperIsCut(t *testing.T) {
	ex := claudeExec()
	ex.noExit = true
	k := newKeeperFixture(t, ex)
	k.srv.KeeperLogin.StopWait = 100 * time.Millisecond
	if w := k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/url", tokWorkbench, nil); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	start := time.Now()
	if w := k.do(http.MethodDelete, apiv1.KeeperLoginPathFor("claude"), tokWorkbench, nil); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("cancel took %s", time.Since(start))
	}
	if w := k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/url", tokWorkbench, nil); w.Code != http.StatusOK {
		t.Fatalf("restart: %d", w.Code)
	}
}

// A helper that ends without a link, or shows one off the provider's host, gives
// the caller a fixed message and nothing from its output.
func TestKeeperLoginNoLink(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*fakeKeeperExec)
		status int
		code   string
	}{
		"busy": {func(e *fakeKeeperExec) {
			e.link, e.afterLink, e.endNow = "", []string{`{"ok":false,"code":"Busy"}`}, true
		}, http.StatusConflict, apiv1.CodeConflict},
		"unavailable": {func(e *fakeKeeperExec) {
			e.link, e.afterLink, e.endNow = "", []string{`{"ok":false,"code":"Unavailable","login":{"failureCode":"` + canaryToken + `"}}`}, true
		}, http.StatusServiceUnavailable, apiv1.CodeUnavailable},
		"link on another host": {func(e *fakeKeeperExec) {
			e.link, e.afterLink = "https://evil.example/cai/oauth/authorize?state=1", nil
		}, http.StatusGatewayTimeout, apiv1.CodeUnavailable},
		"no link in time": {func(e *fakeKeeperExec) { e.link, e.afterLink = "", nil }, http.StatusGatewayTimeout, apiv1.CodeUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			ex := claudeExec()
			tc.mutate(ex)
			// The helper ends at once when it has printed its last word.
			ex.finalForCancel = `{"ok":false,"code":"NeedsLogin"}`
			k := newKeeperFixture(t, ex)
			k.srv.KeeperLogin.URLWait = 300 * time.Millisecond
			w := k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/url", tokWorkbench, nil)
			wantError(t, w, tc.status, tc.code)
			if strings.Contains(w.Body.String(), "CANARY") || strings.Contains(w.Body.String(), "evil.example") {
				t.Fatalf("the refusal carries helper output: %s", w.Body.String())
			}
			// The failed ceremony is abandoned (in the background when it timed out):
			// once it is gone a new one may start.
			k.waitNoCeremony("claude")
			ex2 := claudeExec()
			k.srv.KeeperLogin.Exec = ex2
			if w := k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/url", tokWorkbench, nil); w.Code != http.StatusOK {
				t.Fatalf("restart: %d %s", w.Code, w.Body.String())
			}
			k.noLeak(name, w.Body.String())
		})
	}
}

// A code the keeper refuses comes back as its fixed word and the right status; a
// slow adoption is a 202 Waiting that leaves the ceremony in place.
func TestKeeperLoginCodeRefusedAndWaiting(t *testing.T) {
	ex := claudeExec()
	ex.finalForCode = `{"ok":false,"code":"NeedsLogin","login":{"phase":"Native"}}`
	k := newKeeperFixture(t, ex)
	k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/url", tokWorkbench, nil)
	w := k.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/code", tokWorkbench, apiv1.KeeperLoginCode{Code: canaryCode})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("refused code: %d %s", w.Code, w.Body.String())
	}
	if r := decode[apiv1.KeeperLoginResult](t, w); r.Result != "NeedsLogin" || r.Generation != 0 {
		t.Fatalf("result = %+v", r)
	}
	k.noLeak("refused", w.Body.String())

	// Waiting: the helper never answers after the code.
	ex2 := claudeExec()
	ex2.hangAfterCode = true
	k2 := newKeeperFixture(t, ex2)
	t.Cleanup(func() { k2.do(http.MethodDelete, apiv1.KeeperLoginPathFor("claude"), tokWorkbench, nil) })
	k2.srv.KeeperLogin.CodeWait = 200 * time.Millisecond
	k2.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/url", tokWorkbench, nil)
	w = k2.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/code", tokWorkbench, apiv1.KeeperLoginCode{Code: canaryCode})
	if w.Code != http.StatusAccepted || decode[apiv1.KeeperLoginResult](t, w).Result != "Waiting" {
		t.Fatalf("waiting: %d %s", w.Code, w.Body.String())
	}
	// The code is not sent twice.
	wantError(t, k2.do(http.MethodPost, apiv1.KeeperLoginPathFor("claude")+"/code", tokWorkbench, apiv1.KeeperLoginCode{Code: "again"}), http.StatusConflict, apiv1.CodeConflict)
	k2.noLeak("waiting", w.Body.String())
}

// The Codex device flow: url answers the link and the one-time code; code takes
// no body and waits for the sign-in to finish.
func TestKeeperLoginCodexDeviceFlow(t *testing.T) {
	base := claudeExec()
	ex := &codexExec{fakeKeeperExec: base, finish: make(chan struct{})}
	k := newKeeperFixture(t, base)
	k.srv.KeeperLogin.Exec = ex
	w := k.do(http.MethodPost, apiv1.KeeperLoginPathFor("codex")+"/url", tokWorkbench, nil)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	got := decode[apiv1.KeeperLoginURL](t, w)
	if got.URL != codexLinkURL || got.UserCode != codexUserCode {
		t.Fatalf("codex link = %+v", got)
	}
	if strings.Contains(k.logs.String(), codexUserCode) {
		t.Fatal("the device code reached the log")
	}
	k.srv.KeeperLogin.CodeWait = 200 * time.Millisecond
	w = k.do(http.MethodPost, apiv1.KeeperLoginPathFor("codex")+"/code", tokWorkbench, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("still waiting: %d %s", w.Code, w.Body.String())
	}
	close(ex.finish)
	k.srv.KeeperLogin.CodeWait = 2 * time.Second
	w = k.do(http.MethodPost, apiv1.KeeperLoginPathFor("codex")+"/code", tokWorkbench, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("finished: %d %s", w.Code, w.Body.String())
	}
	if r := decode[apiv1.KeeperLoginResult](t, w); r.Result != "Adopted" || r.Generation != 5 {
		t.Fatalf("result = %+v", r)
	}
	calls := base.callList()
	if len(calls) != 1 || !strings.HasSuffix(calls[0], "/codex-login: dev-env-keeper codex-login") {
		t.Fatalf("exec calls = %v", calls)
	}
	k.secretsUntouched()
}

// The terminal parser keeps brace-led JSON of the helper's shape and nothing
// else, across split writes, ANSI sequences and carriage returns.
func TestKeeperLoginLineWriter(t *testing.T) {
	var got []helperLine
	w := &lineWriter{emit: func(l helperLine) { got = append(got, l) }}
	for _, chunk := range []string{
		"\x1b[31mnoise ", canaryNoise + "\r\n", `{"signInUrl":"https://claude.com/x"`, "}\r\n",
		canaryCode + "\r\n", "{not json}\r\n", `{"ok":true,"code":"Ready","generation":2,"unknown":{"a":1}}` + "\r\n",
		strings.Repeat("x", 3*maxKeeperOutputLine), "\r\n",
	} {
		_, _ = w.Write([]byte(chunk))
	}
	if len(got) != 2 || got[0].SignInURL != "https://claude.com/x" || !got[1].final() || got[1].Generation != 2 {
		t.Fatalf("parsed %+v", got)
	}
}

// The route table gives no other method on the keeper routes.
func TestKeeperLoginMethods(t *testing.T) {
	k := newKeeperFixture(t, claudeExec())
	for _, rq := range []struct{ method, path string }{
		{http.MethodPut, apiv1.KeeperLoginPathFor("claude")},
		{http.MethodGet, apiv1.KeeperLoginPathFor("claude") + "/url"},
		{http.MethodGet, apiv1.KeeperLoginPathFor("claude") + "/code"},
	} {
		wantError(t, k.do(rq.method, rq.path, tokWorkbench, nil), http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed)
	}
}
