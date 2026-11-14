package apiserver

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/threadlaunch"
)

// The thread route (DESIGN-057 section 10, OP495) gives an ordinary session
// operator-mediated thread start without granting exec. Ordinary hosts retain
// their profile/strict-ancestry fence. DESIGN-067 reserves the named workbench
// host: explicit intent and the privileged caller gate run first, and agentd
// receives both expected UIDs. Intent is never caller authorization.

const (
	// threadExecGrace stops a stuck exec this long after the host's own wait
	// ends, as agent-run's kubectl exec does: the host bounds the launch.
	threadExecGrace = time.Minute
	// threadAnswerGrace is the time left to write the answer once the exec ends.
	threadAnswerGrace = 30 * time.Second
	// threadMaxBody holds the largest handoff even when every byte is
	// JSON-escaped to six (json.Marshal escapes <, > and &), plus the fields.
	threadMaxBody = 6*apiv1.ThreadMaxHandoffBytes + 4<<10
	// exitCtlUsage is agentd ctl's exit code for a verb it does not know: a
	// host whose agentd predates thread-start prints its usage and exits 2.
	exitCtlUsage = 2
)

// startThread serves POST /v1/sessions/{name}/threads. Refusals of the request
// itself (body, session, budget, no exec) are API errors. From the host check
// on, the answer is 200 with one thread-launch.v1 result whose outcome says
// what happened, the same document `agentd ctl thread-start` prints.
func (s *Server) startThread(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	var req apiv1.ThreadStartRequest
	if err := decodeJSON(w, r, threadMaxBody, &req, true); err != nil {
		return 0, nil, err
	}
	wait, fields := validThreadStart(req)
	if len(fields) > 0 {
		return 0, nil, invalid(fields...)
	}
	sess, err := s.liveSession(ctx, r, c)
	if err != nil {
		return 0, nil, err
	}
	rec := threadlaunch.NewRecorder("codex", s.now)
	rec.Result.Host, rec.Result.SessionName, rec.Result.Name, rec.Result.RequestedModel = sess.Name, sess.Name, req.Name, req.Model
	answer := func(res apiv1.ThreadLaunchResult) (int, any, error) {
		code := ""
		if res.Error != nil {
			code = res.Error.Code
		}
		// The caller and the handoff's size, never its text.
		s.Log.Info("thread start", "session", sess.Name, "caller", c.String(), "handoffBytes", len(req.Handoff), "outcome", res.Outcome, "code", code)
		return http.StatusOK, res, nil
	}
	refuse := func(code string) (int, any, error) {
		rec.Phase(apiv1.ThreadPhaseHostReady, "error", "api", code)
		return answer(rec.Finish(apiv1.ThreadFailed, code, apiv1.ThreadPhaseHostReady, wait))
	}
	// The kind `agent-run pair` serves: a Codex interactive host, Running.
	if sess.Spec.Agent != v1alpha1.AgentCodex || sess.Spec.Mode != v1alpha1.ModeRemote {
		return refuse("not_codex_host")
	}
	if sess.Status.Phase != v1alpha1.PhaseRunning || sess.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || !sess.DeletionTimestamp.IsZero() {
		return refuse("host_not_running")
	}
	if err := s.checkSessionTaskBudget(ctx, sess); err != nil {
		return 0, nil, err
	}
	pod, err := s.runningPod(ctx, sess)
	var notRunning *apiError
	if errors.As(err, &notRunning) && notRunning.status == http.StatusConflict {
		return refuse("host_not_running")
	}
	if err != nil {
		return 0, nil, err
	}
	if err := s.admitThreadHost(ctx, c, sess, pod, req.Coordinator); err != nil {
		return 0, nil, err
	}
	if !req.Coordinator {
		if err := s.threadProfileFence(ctx, c, sess, pod); err != nil {
			return 0, nil, err
		}
	}
	if s.Exec == nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "this API cannot exec into pods")
	}
	if err := holdResponse(w, wait+threadExecGrace+threadAnswerGrace); err != nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "this API cannot hold the request for the wait")
	}
	ctx, cancel := context.WithTimeout(ctx, wait+threadExecGrace)
	defer cancel()
	argv := []string{"agentd", "ctl", "thread-start", "--model", req.Model, "--effort", req.Effort, "--name", req.Name, "--wait", wait.String()}
	if req.Repo != "" {
		argv = append(argv, "--repo", req.Repo)
	}
	if req.Coordinator {
		// Recheck the live target immediately before name-addressed exec. The
		// receiver checks both UIDs too, closing a replacement after this read.
		current, err := s.liveSession(ctx, r, c)
		if err != nil {
			return 0, nil, err
		}
		if current.Status.Phase != v1alpha1.PhaseRunning || current.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || !current.DeletionTimestamp.IsZero() {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "coordinator host is stopping")
		}
		currentPod, err := s.runningPod(ctx, current)
		if err != nil {
			return 0, nil, err
		}
		if current.UID != sess.UID || currentPod.UID != pod.UID || sess.UID == "" || pod.UID == "" {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "coordinator target identity changed")
		}
		if err := s.admitThreadHost(ctx, c, current, currentPod, true); err != nil {
			return 0, nil, err
		}
		argv = append(argv, "--coordinator", "--host", sess.Name, "--expected-session-uid", string(sess.UID), "--expected-pod-uid", string(pod.UID))
	}
	stdout, stderr := &limitedBuffer{max: apiv1.ThreadLaunchMaxBytes + 1}, &limitedBuffer{max: 4 << 10}
	// The handoff goes on stdin only, exactly as given: never in argv or a log.
	err = s.Exec.Run(ctx, pod.Namespace, pod.Name, controller.ContainerName, argv, strings.NewReader(req.Handoff), stdout, stderr)
	return answer(hostThreadResult(rec, sess.Name, wait, []byte(stdout.String()), stderr.String(), err))
}

// threadProfileFence admits a session caller only to a host of its own profile
// or to a host it is a strict ancestor of (DESIGN-057 section 10). A handoff is
// the first instruction of an always-allow thread, which then acts with the
// host's profile: its credentials and egress. So a session on a narrower
// profile must not drive a host on a wider one. Each side's profile is its
// pod's label, else its spec, as resolveSession reads the caller's. The
// controller labels every pod it builds with the resolved profile name (the
// templates' default when the spec names none), so an empty profile is
// unresolved and never equal to another, even another empty one. Callers that
// are not sessions keep the message route's rule.
func (s *Server) threadProfileFence(ctx context.Context, c *caller, host *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if c.kind != kindSession {
		return nil
	}
	hostProfile := pod.Labels[v1alpha1.LabelProfile]
	if hostProfile == "" {
		hostProfile = host.Spec.Profile
	}
	if c.profile != "" && c.profile == hostProfile {
		return nil
	}
	if c.session != nil && s.sessionAncestry(ctx, host, c.session.Name, c.session.UID) == ancestryFound {
		return nil
	}
	return forbidden("a session starts a thread only on a Codex host of its own profile, or on one it or its children started: you run profile %q and %s runs %q",
		c.profile, host.Name, hostProfile)
}

// validThreadStart applies agent-run's own input rules again; no message
// repeats a value the caller sent.
func validThreadStart(req apiv1.ThreadStartRequest) (time.Duration, []apiv1.FieldError) {
	var fields []apiv1.FieldError
	if req.Coordinator && (req.Model != "gpt-6.1-sol" || req.Effort != "medium") {
		fields = append(fields, fieldError("coordinator", "requires gpt-6.1-sol and medium effort"))
	}
	if err := protocol.ValidateCodexEffort(req.Model, req.Effort); err != nil || req.Effort == "" {
		fields = append(fields, fieldError("model", "a full Codex model id and an effort it takes"))
	}
	if !apiv1.ValidThreadName(req.Name) {
		fields = append(fields, fieldError("name", "1 to 64 letters, digits, - or _, starting with a letter or digit"))
	}
	if req.Repo != "" && !apiv1.ValidRepoName(req.Repo) {
		fields = append(fields, fieldError("repo", "not a repository name"))
	}
	wait, err := time.ParseDuration(req.Wait)
	if err != nil || wait <= 0 || wait > apiv1.ThreadMaxWait {
		fields = append(fields, fieldError("wait", "a positive Go duration of at most %s", apiv1.ThreadMaxWait))
	}
	if !apiv1.ValidThreadHandoff(req.Handoff) {
		fields = append(fields, fieldError("handoff", "1 to %d UTF-8 bytes, not blank, no control character but newline and tab", apiv1.ThreadMaxHandoffBytes))
	}
	return wait, fields
}

// holdResponse lets this one request outlive the listener's read and write
// timeouts (Runner: 30 s and 60 s) for d, so a launch can take its whole wait.
// A writer with no deadlines to move (a test recorder) needs nothing.
func holdResponse(w http.ResponseWriter, d time.Duration) error {
	rc := http.NewResponseController(w)
	// Deadlines are wall-clock times on the connection, never the fake clock.
	deadline := time.Now().Add(d)
	for _, set := range []func(time.Time) error{rc.SetReadDeadline, rc.SetWriteDeadline} {
		if err := set(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
	}
	return nil
}

// hostThreadResult is agent-run's own reading of a host's answer
// (threadOnHost), moved here: one valid result whose exit code matches its
// outcome passes through unchanged, an agentd without thread-start is
// host_unsupported, and anything else may have launched, so it is unconfirmed
// and never retried.
func hostThreadResult(rec *threadlaunch.Recorder, host string, wait time.Duration, out []byte, stderr string, err error) apiv1.ThreadLaunchResult {
	unconfirmed := func() apiv1.ThreadLaunchResult {
		return rec.Finish(apiv1.ThreadUnconfirmed, "host_result_unconfirmed", apiv1.ThreadPhaseClientStarted, wait)
	}
	code := 0
	var exit utilexec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitStatus()
	default:
		// The stream failed or was cut: the host may have run.
		return unconfirmed()
	}
	empty := len(bytes.TrimSpace(out)) == 0
	res, decodeErr := apiv1.DecodeThreadLaunch(bytes.TrimSuffix(out, []byte("\n")))
	switch {
	case decodeErr != nil && empty && code == exitCtlUsage && strings.Contains(stderr, "usage: ctl"):
		return rec.Finish(apiv1.ThreadFailed, "host_unsupported", apiv1.ThreadPhaseHostReady, wait)
	case decodeErr != nil || code != apiv1.ThreadLaunchExit(res.Outcome) || res.Provider != "codex" || (res.Host != "" && res.Host != host):
		return unconfirmed()
	}
	return res
}
