package apiserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

// The log and message routes (DESIGN-001 3.4, 6.8, D-16, D-65). Both run an
// agentd command in the session's running pod by exec (D-08): agentd owns the
// log's path and the TUI's pane, so the API restates neither.

// PodExecutor runs a command in a pod's container (internal/podexec).
type PodExecutor interface {
	Run(ctx context.Context, namespace, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error
}

// Log tails: the default and the most a caller may ask for.
const (
	defaultLogTail = 200
	maxLogTail     = 5000
	maxLogBytes    = 4 << 20
	podCmdTimeout  = time.Minute
)

// agentd's exit codes: ctl deliver's for a session that takes no message now,
// and ctl log's for a session with no log yet.
const (
	exitNotAddressable = 3
	exitNoLog          = 4
)

// runningPod is the session's own pod, as the API server has it, when it runs
// an agent: not a hold pod (D-55), Running and Ready. Otherwise it is a 409
// saying why, and where the log is kept.
func (s *Server) runningPod(ctx context.Context, sess *v1alpha1.AgentSession) (*corev1.Pod, error) {
	var pod corev1.Pod
	err := s.Live.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, &pod)
	switch {
	case apierrors.IsNotFound(err):
		return nil, newError(http.StatusConflict, apiv1.CodeConflict,
			"session %s has no pod (phase %s); its log is kept on the shared volume at logs/%s.log", sess.Name, firstOf(string(sess.Status.Phase), "not observed yet"), sess.Name)
	case err != nil:
		return nil, fromKubeError(err, "pod "+sess.Name)
	case !metav1.IsControlledBy(&pod, sess) || pod.Labels[v1alpha1.LabelHold] == "true":
		return nil, newError(http.StatusConflict, apiv1.CodeConflict, "session %s has no pod of its own running an agent; its log is kept on the shared volume at logs/%s.log", sess.Name, sess.Name)
	case !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning || !podIsReady(&pod):
		return nil, newError(http.StatusConflict, apiv1.CodeConflict, "session %s's pod is not running (%s)", sess.Name, pod.Status.Phase)
	}
	return &pod, nil
}

func podIsReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func firstOf(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// liveSession reads the session in the path from the API server.
func (s *Server) liveSession(ctx context.Context, r *http.Request) (*v1alpha1.AgentSession, error) {
	key, err := s.sessionKey(r)
	if err != nil {
		return nil, err
	}
	var sess v1alpha1.AgentSession
	if err := s.Live.Get(ctx, key, &sess); err != nil {
		return nil, fromKubeError(err, "session "+key.Name)
	}
	return &sess, nil
}

// tailBuffer keeps the last max bytes written to it, and says whether it
// dropped any: a log's tail is its newest lines.
type tailBuffer struct {
	buf       []byte
	max       int
	truncated bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	// Trim only once it holds twice the cap, so the copying costs no more
	// than the bytes written.
	if over := len(b.buf) - b.max; over > b.max {
		b.buf = append(b.buf[:0], b.buf[over:]...)
		b.truncated = true
	}
	return len(p), nil
}

// String is the last max bytes, from the first whole line when some were
// dropped.
func (b *tailBuffer) String() string {
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = b.buf[over:]
		b.truncated = true
	}
	s := string(b.buf)
	if b.truncated {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return s
}

// limitedBuffer keeps at most max bytes and drops the rest. Its buffer is a
// field, not embedded: an embedded bytes.Buffer brings ReadFrom, which io.Copy
// in the exec stream would call instead of Write, past the cap.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			_, _ = b.buf.Write(p[:room])
		} else {
			_, _ = b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

// sessionLog serves GET /v1/sessions/{name}/log?tail=N: agentd ctl log in the
// running pod.
func (s *Server) sessionLog(ctx context.Context, _ http.ResponseWriter, r *http.Request, _ *caller) (int, any, error) {
	tail := defaultLogTail
	if v := r.URL.Query().Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLogTail {
			return 0, nil, badRequest("tail is a number of lines from 1 to %d, not %q", maxLogTail, v)
		}
		tail = n
	}
	sess, err := s.liveSession(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	pod, err := s.runningPod(ctx, sess)
	if err != nil {
		return 0, nil, err
	}
	if s.Exec == nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "this API cannot exec into pods")
	}
	ctx, cancel := context.WithTimeout(ctx, podCmdTimeout)
	defer cancel()
	stdout, stderr := &tailBuffer{max: maxLogBytes}, &limitedBuffer{max: 4 << 10}
	err = s.Exec.Run(ctx, pod.Namespace, pod.Name, controller.ContainerName, []string{"agentd", "ctl", "log", "--tail", strconv.Itoa(tail)}, nil, stdout, stderr)
	var code utilexec.ExitError
	if errors.As(err, &code) && code.ExitStatus() == exitNoLog {
		return 0, nil, notFound("session %s has no log yet", sess.Name)
	}
	if err != nil {
		return 0, nil, execError("read the log of "+sess.Name, err, stderr.String())
	}
	text := stdout.String() // sets truncated when it drops bytes
	return http.StatusOK, apiv1.SessionLog{Session: sess.Name, Tail: tail, Text: text, Truncated: stdout.truncated}, nil
}

// sendMessage serves POST /v1/sessions/{name}/messages: agentd ctl deliver in
// the running pod, with the text on stdin and the caller as the sender (D-16,
// D-65). Any caller may message any session; the agent reads it as
// information from that sender, not as its user's instruction.
func (s *Server) sendMessage(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	var req apiv1.MessageRequest
	if err := decodeJSON(w, r, 2*apiv1.MaxMessageBytes+1024, &req, true); err != nil {
		return 0, nil, err
	}
	text := strings.TrimSpace(req.Text)
	switch {
	case stripControl(text) != text:
		return 0, nil, invalid(fieldError("text", "the text holds control characters other than newline and tab; a terminal would act on them"))
	case text == "":
		return 0, nil, invalid(fieldError("text", "the message is empty"))
	case len(text) > apiv1.MaxMessageBytes:
		return 0, nil, invalid(fieldError("text", "%d bytes, more than %d", len(text), apiv1.MaxMessageBytes))
	}
	sess, err := s.liveSession(ctx, r)
	if err != nil {
		return 0, nil, err
	}
	pod, err := s.runningPod(ctx, sess)
	if err != nil {
		return 0, nil, err
	}
	if s.Exec == nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "this API cannot exec into pods")
	}
	from := c.String()
	ctx, cancel := context.WithTimeout(ctx, podCmdTimeout)
	defer cancel()
	stderr := &limitedBuffer{max: 4 << 10}
	err = s.Exec.Run(ctx, pod.Namespace, pod.Name, controller.ContainerName, []string{"agentd", "ctl", "deliver", "--from", from}, strings.NewReader(text), io.Discard, stderr)
	var code utilexec.ExitError
	if errors.As(err, &code) && code.ExitStatus() == exitNotAddressable {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "session %s takes no message now: %s", sess.Name, strings.TrimSpace(stderr.String()))
	}
	if err != nil {
		return 0, nil, execError("deliver to "+sess.Name, err, stderr.String())
	}
	s.Log.Info("message delivered", "session", sess.Name, "caller", from, "bytes", len(text))
	return http.StatusAccepted, apiv1.MessageResult{Session: sess.Name, From: from, Delivered: true}, nil
}

// execError turns a failed exec into a 500: the pod's agentd did not do it. Not a
// 502: agent-run retries those, and a message must not be sent twice.
func execError(what string, err error, stderr string) *apiError {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = err.Error()
	}
	return newError(http.StatusInternalServerError, apiv1.CodeInternal, "%s: %s", what, truncateText(detail, 400))
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// stripControl is agentd.StripControl's rule (D-65): C0 but newline and tab,
// DEL and C1 go. The API refuses a text with any, so the sender learns of it;
// agentd strips them again.
func stripControl(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, text)
}
