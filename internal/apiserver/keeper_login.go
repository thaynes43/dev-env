package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/claudelogin"
)

// The keeper login routes (NB-01). A workbench session renews the keeper's shared
// Claude or Codex login without the v1 pod or Tom's laptop: the operator execs
// into the keeper pod's LOGIN container, the helper that already runs the
// ceremony and owns the only path to the keeper's private control socket. The
// keeper's main container is never the target: the container name is fixed in
// the provider table below, not read from a request, and the pod is found by
// the keeper's own labels and ServiceAccount.
//
// What the operator keeps. Nothing secret. The sign-in link and the Codex device
// code are read off the helper's own validated JSON lines, returned in the
// response that asks for them and dropped. The pasted Claude code is written to
// the helper's terminal and kept nowhere. Everything else the terminal prints
// (the terminal echoes the code) is discarded unread: only a line that starts
// with a brace and decodes as the helper's fixed shape is looked at. No error
// carries output, and the access log line holds a path and a status only.
//
// A ceremony is one long exec that must live across two requests (link, then
// code), so the operator holds it in this replica's memory. Only the leader
// holds ceremonies: a follower answers 503 not_leader before doing anything, and
// agent-run reconnects, because a new connection may land on the leader. A
// ceremony lost to a leader change or a restart is only abandoned: the helper
// treats the hangup as a cancel, and the keeper's attempt is wiped or expires
// within claudelogin.Lifetime. Status holds no state and any replica answers it.

// KeeperLoginExec is what the routes need to run in the keeper's login
// containers: the plain exec for status and the terminal exec for a ceremony.
type KeeperLoginExec interface {
	PodExecutor
	RunTTY(ctx context.Context, namespace, pod, container string, cmd []string, stdin io.Reader, stdout io.Writer) error
}

const (
	keeperPodLabelName       = "app.kubernetes.io/name"
	keeperPodLabelController = "app.kubernetes.io/controller"
	keeperPodName            = "dev-env-keeper"
	// keeperServiceAccount is the ServiceAccount the keeper pod runs as; a pod
	// that matches the labels but not this is never entered.
	keeperServiceAccount = "dev-env-keeper"

	defaultKeeperURLWait  = 45 * time.Second
	defaultKeeperCodeWait = 40 * time.Second
	defaultKeeperStopWait = 10 * time.Second
	// keeperStatusTimeout bounds the status exec well inside the API's write timeout.
	keeperStatusTimeout = 20 * time.Second
	maxKeeperCodeBytes  = 1024
	// maxKeeperOutputLine bounds one terminal line the parser will consider.
	maxKeeperOutputLine = 16 << 10
)

// loginProvider is one provider's fixed commands. The container and the commands
// are constants here, never request input.
type loginProvider struct {
	name      string
	container string
	status    []string
	login     []string
	// hosts are the sign-in link's allowed hosts (the helper already validates
	// the link; the operator checks again before it hands one to a caller).
	hosts []string
	// takesCode: the ceremony ends when the caller pastes a code (Claude). The
	// Codex device flow shows a code instead and ends when the sign-in finishes.
	takesCode bool
}

var keeperLoginProviders = map[string]loginProvider{
	apiv1.KeeperLoginClaude: {
		name: apiv1.KeeperLoginClaude, container: "claude-login",
		status:    []string{"agentd", "claude-login-status"},
		login:     []string{"agentd", "claude-login"},
		hosts:     []string{"claude.com", "claude.ai"},
		takesCode: true,
	},
	apiv1.KeeperLoginCodex: {
		name: apiv1.KeeperLoginCodex, container: "codex-login",
		status: []string{"dev-env-keeper", "codex-auth-status"},
		login:  []string{"dev-env-keeper", "codex-login"},
		hosts:  []string{"auth.openai.com"},
	},
}

// The keeper's fixed words (internal/claudelogin, internal/keeper).
var (
	keeperStateWords  = map[string]bool{"Ready": true, "PendingPublication": true, "NeedsLogin": true, "Unavailable": true}
	keeperResultWords = map[string]bool{
		"Adopted": true, "AdoptedPendingPublication": true, "NeedsLogin": true, "AccountMismatch": true,
		"Busy": true, "Unavailable": true, "InvalidRequest": true, "UnsupportedCLI": true,
		"AuthenticatedTerminalRequired": true,
	}
	keeperCodePattern = regexp.MustCompile(`^[A-Za-z0-9._~:/+=%#-]+$`)
	ansiPattern       = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
)

// KeeperLogin is the routes' state. A nil Server.KeeperLogin answers them 503.
type KeeperLogin struct {
	// Namespace holds the keeper pod (dev-env-system).
	Namespace string
	Exec      KeeperLoginExec
	// Leader reports whether this replica leads. Nil means it does not.
	Leader func() bool
	// Waits; zero takes the defaults. A handler must answer inside the API's
	// write timeout, so none of them is long.
	URLWait, CodeWait, StopWait time.Duration

	mu   sync.Mutex
	open map[string]*ceremony
}

func (k *KeeperLogin) urlWait() time.Duration { return firstDuration(k.URLWait, defaultKeeperURLWait) }
func (k *KeeperLogin) codeWait() time.Duration {
	return firstDuration(k.CodeWait, defaultKeeperCodeWait)
}
func (k *KeeperLogin) stopWait() time.Duration {
	return firstDuration(k.StopWait, defaultKeeperStopWait)
}

func firstDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (k *KeeperLogin) isLeader() bool { return k.Leader != nil && k.Leader() }

// helperLine is the only shape read from a helper's terminal output. Unknown
// members are ignored and nothing is kept but these fields.
type helperLine struct {
	SignInURL       string     `json:"signInUrl"`
	VerificationURL string     `json:"verificationUrl"`
	UserCode        string     `json:"userCode"`
	Code            string     `json:"code"`
	Generation      uint64     `json:"generation"`
	AccessExpiresAt *time.Time `json:"accessExpiresAt"`
	LoginExpiresAt  *time.Time `json:"loginExpiresAt"`
}

// final: a line carrying the keeper's result word ends the helper's output.
func (l helperLine) final() bool { return l.Code != "" }

// lineWriter turns a terminal's byte stream into helperLines. It never blocks,
// and it drops every line that is not a brace-led JSON object of helperLine's
// shape, so echoed input and terminal noise leave no trace.
type lineWriter struct {
	emit    func(helperLine)
	pending []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := w.pending[:i]
		w.line(line)
		w.pending = append([]byte(nil), w.pending[i+1:]...)
	}
	if len(w.pending) > maxKeeperOutputLine {
		w.pending = nil
	}
	return len(p), nil
}

func (w *lineWriter) line(raw []byte) {
	s := strings.TrimSpace(ansiPattern.ReplaceAllString(string(raw), ""))
	if len(s) == 0 || len(s) > maxKeeperOutputLine || s[0] != '{' {
		return
	}
	var l helperLine
	if json.Unmarshal([]byte(s), &l) == nil {
		w.emit(l)
	}
}

// ceremony is one open login exec.
type ceremony struct {
	provider loginProvider
	cancel   context.CancelFunc
	stdin    *io.PipeWriter
	events   chan helperLine
	done     chan struct{}
	started  time.Time

	mu       sync.Mutex
	final    *helperLine
	codeSent bool
}

func (c *ceremony) result() *helperLine {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.final
}

func (c *ceremony) isDone() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// write sends bytes to the helper's terminal, bounded: the terminal reads input
// as it arrives, so a write that does not return is a helper that is gone.
func (c *ceremony) write(b []byte, wait time.Duration) bool {
	ok := make(chan struct{})
	go func() {
		_, _ = c.stdin.Write(b)
		close(ok)
	}()
	select {
	case <-ok:
		return true
	case <-c.done:
		return false
	case <-time.After(wait):
		return false
	}
}

// stop abandons the ceremony the way Tom would: a Ctrl-C ends the provider's
// CLI, the helper cancels the keeper's attempt and wipes its staging, and only
// then the exec is cut. A helper that does not leave within wait is cut anyway;
// its hangup handler (or the keeper's fifteen-minute expiry) finishes the job.
func (c *ceremony) stop(wait time.Duration) {
	if !c.isDone() {
		c.write([]byte{0x03}, wait)
		select {
		case <-c.done:
		case <-time.After(wait):
		}
	}
	c.cancel()
	<-c.done
}

// keeperPod finds the keeper pod's single running instance and checks that the
// container the caller wants is running in it.
func (k *KeeperLogin) keeperPod(ctx context.Context, live client.Reader, container string) (*corev1.Pod, error) {
	var pods corev1.PodList
	if err := live.List(ctx, &pods, client.InNamespace(k.Namespace),
		client.MatchingLabels{keeperPodLabelName: keeperPodName, keeperPodLabelController: keeperPodName}); err != nil {
		return nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the keeper pod cannot be listed; try again shortly")
	}
	var found []*corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning || p.Spec.ServiceAccountName != keeperServiceAccount {
			continue
		}
		ready := false
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		running := false
		for _, st := range p.Status.ContainerStatuses {
			if st.Name == container && st.State.Running != nil {
				running = true
			}
		}
		if ready && running {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		return nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
			"the keeper pod is not running with its %s container (found %d); try again shortly", container, len(found))
	}
	return found[0], nil
}

// isWorkbench reports whether c is the workbench: a session whose pod runs as
// the workbench ServiceAccount, or that account's own unbound token (the same
// two shapes the Codex remote create rule accepts). Tom's human account, the v1
// pod, coordinators and every ordinary session are not.
func (s *Server) isWorkbench(c *caller) bool {
	if s.isWorkbenchSession(c) {
		return true
	}
	return c.kind == kindClient && s.Policy.WorkbenchServiceAccount != "" &&
		c.identity.Namespace == s.Policy.SessionNamespace && c.identity.ServiceAccount == s.Policy.WorkbenchServiceAccount
}

// keeperLoginGate authorizes a keeper login call and resolves its provider. The
// refusal precedes anything else, so a caller that is not the workbench learns
// nothing about the keeper.
func (s *Server) keeperLoginGate(r *http.Request, c *caller) (loginProvider, *KeeperLogin, error) {
	if !s.isWorkbench(c) {
		return loginProvider{}, nil, forbidden("the keeper login is renewed from a workbench session only")
	}
	p, ok := keeperLoginProviders[r.PathValue("provider")]
	if !ok {
		return loginProvider{}, nil, notFound("no keeper login %q: it is claude or codex", r.PathValue("provider"))
	}
	if s.KeeperLogin == nil || s.KeeperLogin.Exec == nil || s.KeeperLogin.Namespace == "" {
		return loginProvider{}, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the keeper login routes are not configured on this operator")
	}
	return p, s.KeeperLogin, nil
}

// keeperLoginStatus answers GET: the keeper's state word and times, taken from
// its own status command. Nothing from the helper's output reaches the answer
// but the validated fields of apiv1.KeeperLoginStatus.
func (s *Server) keeperLoginStatus(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	p, k, err := s.keeperLoginGate(r, c)
	if err != nil {
		return 0, nil, err
	}
	pod, err := k.keeperPod(ctx, s.Live, p.container)
	if err != nil {
		return 0, nil, err
	}
	ectx, cancel := context.WithTimeout(ctx, keeperStatusTimeout)
	defer cancel()
	var out boundedBuffer
	// A non-zero exit still carries the keeper's fixed word on stdout.
	_ = k.Exec.Run(ectx, pod.Namespace, pod.Name, p.container, p.status, nil, &out, io.Discard)
	status := apiv1.KeeperLoginStatus{Provider: p.name, State: "Unavailable"}
	var first helperLine
	got := false
	_, _ = (&lineWriter{emit: func(l helperLine) {
		if !got && l.final() {
			first, got = l, true
		}
	}}).Write(out.Bytes())
	if got && keeperStateWords[first.Code] {
		status.State = first.Code
		status.Generation = first.Generation
		status.AccessExpiresAt = plausibleTime(first.AccessExpiresAt)
		status.LoginExpiresAt = plausibleTime(first.LoginExpiresAt)
	}
	return http.StatusOK, status, nil
}

func plausibleTime(t *time.Time) *time.Time {
	if t == nil || t.IsZero() || t.Year() < 2020 || t.Year() > 2200 {
		return nil
	}
	u := t.UTC()
	return &u
}

// boundedBuffer keeps at most 4 KiB of what a status command prints.
type boundedBuffer struct{ b bytes.Buffer }

func (w *boundedBuffer) Write(p []byte) (int, error) {
	if room := 4096 - w.b.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.b.Write(p[:room])
	}
	return len(p), nil
}

func (w *boundedBuffer) Bytes() []byte { return w.b.Bytes() }

func notLeader() *apiError {
	return newError(http.StatusServiceUnavailable, apiv1.CodeNotLeader,
		"this replica does not hold keeper login ceremonies; reconnect and send the request again (nothing was done)")
}

// linkParts collects what the helper's lines say before the answer can be made.
type linkParts struct {
	link, userCode string
}

func (lp *linkParts) absorb(p loginProvider, l helperLine) {
	if u := firstOf(l.SignInURL, l.VerificationURL); u != "" && validKeeperLink(u, p.hosts) {
		lp.link = u
	}
	if l.UserCode != "" && !p.takesCode {
		lp.userCode = l.UserCode
	}
}

func (lp *linkParts) complete(p loginProvider) bool {
	return lp.link != "" && (p.takesCode || lp.userCode != "")
}

// keeperLoginURL answers POST .../url: opens a ceremony and returns the link.
func (s *Server) keeperLoginURL(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	p, k, err := s.keeperLoginGate(r, c)
	if err != nil {
		return 0, nil, err
	}
	if !k.isLeader() {
		return 0, nil, notLeader()
	}
	pod, err := k.keeperPod(ctx, s.Live, p.container)
	if err != nil {
		return 0, nil, err
	}
	cer, err := k.begin(p, pod)
	if err != nil {
		return 0, nil, err
	}
	var parts linkParts
	timer := time.NewTimer(k.urlWait())
	defer timer.Stop()
	for !parts.complete(p) {
		select {
		case l := <-cer.events:
			parts.absorb(p, l)
		case <-cer.done:
			// A line that arrived with the exit still counts.
			for drained := false; !drained; {
				select {
				case l := <-cer.events:
					parts.absorb(p, l)
				default:
					drained = true
				}
			}
			if parts.complete(p) {
				break
			}
			k.forget(cer)
			return 0, nil, helperEnded(cer.result())
		case <-timer.C:
			// Answer first: stopping a stuck helper can take twice the stop wait,
			// which would push the answer past the API's write timeout. The
			// ceremony stays registered until it is stopped, so a quick retry
			// meets a conflict, not a second helper.
			go k.abandon(cer)
			return 0, nil, newError(http.StatusGatewayTimeout, apiv1.CodeUnavailable,
				"the login helper showed no sign-in link in time; the ceremony is being abandoned, run url again in a moment")
		case <-ctx.Done():
			go k.abandon(cer)
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
				"the request ended before the sign-in link came; the ceremony is being abandoned")
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	return http.StatusOK, apiv1.KeeperLoginURL{
		Provider: p.name, URL: parts.link, UserCode: parts.userCode, ExpiresAt: cer.started.Add(claudelogin.Lifetime),
	}, nil
}

func validKeeperLink(s string, hosts []string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || len(s) > 8192 {
		return false
	}
	for _, h := range hosts {
		if u.Host == h {
			return true
		}
	}
	return false
}

// helperEnded turns a helper that ended without a link into a fixed message.
func helperEnded(final *helperLine) error {
	code := "Unavailable"
	if final != nil && keeperResultWords[final.Code] {
		code = final.Code
	}
	if code == "Busy" {
		return newError(http.StatusConflict, apiv1.CodeConflict,
			"the keeper already has a login ceremony open (it expires within %d minutes): finish it, or abandon it with keeper-login cancel", int(claudelogin.Lifetime/time.Minute))
	}
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the login helper ended without a sign-in link (%s)", code)
}

// begin starts the helper's exec on a terminal and registers it. One ceremony
// per provider; the keeper allows one attempt at a time anyway.
func (k *KeeperLogin) begin(p loginProvider, pod *corev1.Pod) (*ceremony, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.open == nil {
		k.open = map[string]*ceremony{}
	}
	if old := k.open[p.name]; old != nil {
		if !old.isDone() {
			return nil, newError(http.StatusConflict, apiv1.CodeConflict,
				"a %s login ceremony is already open on this operator: paste its code, or abandon it with keeper-login cancel", p.name)
		}
		delete(k.open, p.name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudelogin.Lifetime+time.Minute)
	pr, pw := io.Pipe()
	cer := &ceremony{provider: p, cancel: cancel, stdin: pw, events: make(chan helperLine, 8), done: make(chan struct{}), started: time.Now()}
	lw := &lineWriter{emit: func(l helperLine) {
		if l.final() {
			cer.mu.Lock()
			cer.final = &l
			cer.mu.Unlock()
			return
		}
		select {
		case cer.events <- l:
		default:
		}
	}}
	k.open[p.name] = cer
	go func() {
		defer close(cer.done)
		defer cancel()
		defer func() { _ = pr.Close() }()
		// The error is dropped on purpose: what matters is the helper's own
		// fixed result word, and an exec error is not worth a leak.
		_ = k.Exec.RunTTY(ctx, pod.Namespace, pod.Name, p.container, p.login, pr, lw)
	}()
	return cer, nil
}

// forget drops a ceremony from the table.
func (k *KeeperLogin) forget(cer *ceremony) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.open[cer.provider.name] == cer {
		delete(k.open, cer.provider.name)
	}
}

// abandon stops a ceremony and forgets it.
func (k *KeeperLogin) abandon(cer *ceremony) {
	cer.stop(k.stopWait())
	k.forget(cer)
}

func (k *KeeperLogin) current(p loginProvider) *ceremony {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.open[p.name]
}

// keeperLoginCode answers POST .../code. Claude: the one-time code goes to the
// helper's terminal. Codex: no body; it waits for the device flow to finish.
func (s *Server) keeperLoginCode(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	p, k, err := s.keeperLoginGate(r, c)
	if err != nil {
		return 0, nil, err
	}
	if !k.isLeader() {
		return 0, nil, notLeader()
	}
	var code string
	if p.takesCode {
		var body apiv1.KeeperLoginCode
		if err := decodeJSON(w, r, 4<<10, &body, true); err != nil {
			return 0, nil, err
		}
		code = strings.TrimSpace(body.Code)
		if code == "" || len(code) > maxKeeperCodeBytes || !keeperCodePattern.MatchString(code) {
			// Never the value.
			return 0, nil, invalid(fieldError("code", "must be the code the sign-in page shows: one token of letters, digits and . _ ~ : / + = %% # -"))
		}
	}
	cer := k.current(p)
	if cer == nil {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict,
			"no %s login ceremony is open on this operator: run url first (an open ceremony is lost when the operator restarts or its leader changes)", p.name)
	}
	if p.takesCode && !cer.isDone() {
		cer.mu.Lock()
		already := cer.codeSent
		cer.codeSent = true
		cer.mu.Unlock()
		if already {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "the code was already submitted: check keeper-login status for the result")
		}
		// The terminal's Enter key is a carriage return.
		if !cer.write([]byte(code+"\r"), k.stopWait()) && !cer.isDone() {
			return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
				"the login helper did not take the code; abandon the ceremony and start again")
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	select {
	case <-cer.done:
	case <-time.After(k.codeWait()):
		return http.StatusAccepted, apiv1.KeeperLoginResult{Provider: p.name, Result: "Waiting"}, nil
	case <-ctx.Done():
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the request ended while waiting; check keeper-login status")
	}
	k.forget(cer)
	result, generation := "Unavailable", uint64(0)
	if final := cer.result(); final != nil && keeperResultWords[final.Code] {
		result, generation = final.Code, final.Generation
	}
	switch result {
	case "Adopted", "AdoptedPendingPublication":
		return http.StatusOK, apiv1.KeeperLoginResult{Provider: p.name, Result: result, Generation: generation}, nil
	case "Busy":
		return http.StatusConflict, apiv1.KeeperLoginResult{Provider: p.name, Result: result}, nil
	default:
		return http.StatusUnprocessableEntity, apiv1.KeeperLoginResult{Provider: p.name, Result: result}, nil
	}
}

// keeperLoginCancel answers DELETE: abandon the open ceremony. It answers the
// same whether one was open or not.
func (s *Server) keeperLoginCancel(_ context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	p, k, err := s.keeperLoginGate(r, c)
	if err != nil {
		return 0, nil, err
	}
	if !k.isLeader() {
		return 0, nil, notLeader()
	}
	if cer := k.current(p); cer != nil {
		k.abandon(cer)
	}
	return http.StatusNoContent, nil, nil
}
