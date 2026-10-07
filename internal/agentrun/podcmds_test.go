package agentrun

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// log prints the tail the API read in the pod (D-65).
func TestLog(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodGet && r.URL.Path == apiv1.SessionLogPath(name) && r.URL.Query().Get("tail") == "20" {
			writeTestJSON(w, http.StatusOK, apiv1.SessionLog{Session: name, Tail: 20, Text: "a\nb\n"})
			return
		}
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no")
	}
	h.mustRun(ExitOK, "log", name, "--tail", "20")
	if h.stdout.String() != "a\nb\n" {
		t.Errorf("stdout %q", h.stdout.String())
	}
	h.mustRun(ExitUsage, "log", name, "--tail", "0")
	h.mustRun(ExitUsage, "log")
}

// msg sends the text once, joined from its words or read from stdin, and never
// retries it: a lost answer must not deliver it twice (D-65).
func TestMsg(t *testing.T) {
	h := newHarness(t)
	var posts atomic.Int32
	var got string
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodPost && r.URL.Path == apiv1.SessionMessagesPath(name) {
			posts.Add(1)
			got = string(body)
			if strings.Contains(got, "fail") {
				apiError(w, http.StatusServiceUnavailable, apiv1.CodeUnavailable, "busy")
				return
			}
			writeTestJSON(w, http.StatusAccepted, apiv1.MessageResult{Session: name, From: "client/dev/dev-env", Delivered: true})
			return
		}
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no")
	}
	h.mustRun(ExitOK, "msg", name, "please", "rebase")
	if got != `{"text":"please rebase"}` {
		t.Errorf("body %s", got)
	}
	contains(t, "stdout", h.stdout.String(), "delivered to "+name+", from client/dev/dev-env")

	h.env.Stdin = strings.NewReader("from stdin\n")
	h.mustRun(ExitOK, "msg", name, "-")
	if got != `{"text":"from stdin"}` {
		t.Errorf("body %s", got)
	}

	n := posts.Load()
	h.mustRun(ExitRetry, "msg", name, "fail")
	if posts.Load() != n+1 {
		t.Errorf("a message was sent %d times", posts.Load()-n)
	}
	h.mustRun(ExitUsage, "msg", name)
	h.mustRun(ExitUsage, "msg", name, "   ")
}
