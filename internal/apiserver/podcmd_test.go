package apiserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	utilexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// fakeExec plays a pod's agentd: it records each command and its stdin, and
// answers with out and code.
type fakeExec struct {
	cmds  []string
	stdin []string
	out   string
	code  int
}

func (e *fakeExec) Run(_ context.Context, ns, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	e.cmds = append(e.cmds, ns+"/"+pod+"/"+container+": "+strings.Join(cmd, " "))
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		e.stdin = append(e.stdin, string(b))
	}
	_, _ = io.WriteString(stdout, e.out)
	if e.code != 0 {
		_, _ = io.WriteString(stderr, "agentd: deliver: not addressable: a headless task (-p) reads no messages")
		return utilexec.CodeExitError{Err: errors.New("command terminated with non-zero exit code"), Code: e.code}
	}
	return nil
}

// runPod marks the session's pod Running and Ready.
func (f *fixture) runPod(name string) {
	f.t.Helper()
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: name}, &pod); err != nil {
		f.t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := f.c.Status().Update(context.Background(), &pod); err != nil {
		f.t.Fatal(err)
	}
}

// The log route tails the log in the running pod with agentd (D-65).
func TestSessionLog(t *testing.T) {
	f := newFixture(t)
	ex := &fakeExec{out: "line one\nline two\n"}
	f.srv.Exec = ex
	f.sessionPod("s-log", "full", 0)
	wantError(t, f.do(http.MethodGet, apiv1.SessionLogPath("s-log"), tokHuman, nil), http.StatusConflict, apiv1.CodeConflict)
	f.runPod("s-log")
	w := f.do(http.MethodGet, apiv1.SessionLogPath("s-log")+"?tail=50", tokHuman, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("log: %d %s", w.Code, w.Body.String())
	}
	if l := decode[apiv1.SessionLog](t, w); l.Text != "line one\nline two\n" || l.Tail != 50 {
		t.Errorf("log %+v", l)
	}
	if len(ex.cmds) != 1 || ex.cmds[0] != "dev-agents/s-log/agent: agentd ctl log --tail 50" {
		t.Errorf("commands %q", ex.cmds)
	}
	wantError(t, f.do(http.MethodGet, apiv1.SessionLogPath("s-log")+"?tail=0", tokHuman, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	wantError(t, f.do(http.MethodGet, apiv1.SessionLogPath("nope"), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
}

// A message goes to agentd ctl deliver on stdin, with the caller as the
// sender; a session that takes none is a 409 (D-16, D-65).
func TestSendMessage(t *testing.T) {
	f := newFixture(t)
	ex := &fakeExec{}
	f.srv.Exec = ex
	tok := f.sessionPod("s-from", "full", 0)
	f.sessionPod("s-to", "full", 0)
	f.runPod("s-to")
	w := f.do(http.MethodPost, apiv1.SessionMessagesPath("s-to"), tok, apiv1.MessageRequest{Text: "  please rebase  "})
	if w.Code != http.StatusAccepted {
		t.Fatalf("message: %d %s", w.Code, w.Body.String())
	}
	if r := decode[apiv1.MessageResult](t, w); !r.Delivered || r.From != "session/s-from" {
		t.Errorf("result %+v", r)
	}
	if len(ex.cmds) != 1 || ex.cmds[0] != "dev-agents/s-to/agent: agentd ctl deliver --from session/s-from" || ex.stdin[0] != "please rebase" {
		t.Errorf("commands %q, stdin %q", ex.cmds, ex.stdin)
	}

	ex.code = 3
	e := wantError(t, f.do(http.MethodPost, apiv1.SessionMessagesPath("s-to"), tokHuman, apiv1.MessageRequest{Text: "hi"}), http.StatusConflict, apiv1.CodeConflict)
	if !strings.Contains(e.Message, "headless task") {
		t.Errorf("message %q", e.Message)
	}
	ex.code = 1
	wantError(t, f.do(http.MethodPost, apiv1.SessionMessagesPath("s-to"), tokHuman, apiv1.MessageRequest{Text: "hi"}), http.StatusInternalServerError, apiv1.CodeInternal)
	ex.code = 0

	wantError(t, f.do(http.MethodPost, apiv1.SessionMessagesPath("s-to"), tokHuman, apiv1.MessageRequest{Text: "  "}), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodPost, apiv1.SessionMessagesPath("s-to"), tokHuman, apiv1.MessageRequest{Text: strings.Repeat("x", apiv1.MaxMessageBytes+1)}), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	// A session with no running pod of its own.
	wantError(t, f.do(http.MethodPost, apiv1.SessionMessagesPath("s-from"), tokHuman, apiv1.MessageRequest{Text: "hi"}), http.StatusConflict, apiv1.CodeConflict)
	// A hold pod runs no agent.
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: "s-to"}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Labels[v1alpha1.LabelHold] = "true"
	if err := f.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionMessagesPath("s-to"), tokHuman, apiv1.MessageRequest{Text: "hi"}), http.StatusConflict, apiv1.CodeConflict)
}
