package agentrun

import (
	"context"
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
	// The message may have arrived: exit 6 says so and never advises a retry.
	h.mustRun(ExitUnconfirmed, "msg", name, "fail")
	if !strings.Contains(h.stderr.String(), "do not repeat") || strings.Contains(h.stderr.String(), "try again") {
		t.Errorf("uncertain message advice: %s", h.stderr.String())
	}
	if posts.Load() != n+1 {
		t.Errorf("a message was sent %d times", posts.Load()-n)
	}
	h.mustRun(ExitUsage, "msg", name)
	h.mustRun(ExitUsage, "msg", name, "   ")
}

func TestLogOnceOfflineWireSemantics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"accepted", 200, `{"session":"synthetic-a","tail":200,"text":"bounded receipt"}`, ExitOK},
		{"unavailable", 503, `{}`, ExitUnconfirmed},
		{"redirect", 307, `{}`, ExitFailed},
		{"wrong-name", 200, `{"session":"wrong","text":"receipt"}`, ExitFailed},
		{"truncated", 200, `{"session":"synthetic-a","text":"receipt","truncated":true}`, ExitFailed},
		{"malformed", 200, `{broken`, ExitFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, calls, sleeps, stderr := onceMemoryCLI(t, tc.status, tc.body)
			code := Run(context.Background(), []string{"log", "synthetic-a", "--once", "-o", "json"}, env)
			if code != tc.want || calls.Load() != 1 || sleeps.Load() != 0 {
				t.Fatalf("exit%d want%d calls%d sleeps%d %s", code, tc.want, calls.Load(), sleeps.Load(), stderr.String())
			}
		})
	}
	env, calls, _, _ := onceMemoryCLI(t, 200, `{}`)
	if code := Run(context.Background(), []string{"log", "synthetic-a", "--once"}, env); code != ExitUsage || calls.Load() != 0 {
		t.Fatalf("bad output sent a request: exit%d requests%d", code, calls.Load())
	}
}
func TestLogDefaultStillRetries(t *testing.T) {
	env, calls, sleeps, _ := onceMemoryCLI(t, 503, `{}`)
	code := Run(context.Background(), []string{"log", "synthetic-a"}, env)
	if code != ExitRetry || calls.Load() != 3 || sleeps.Load() != 2 {
		t.Fatalf("default exit%d calls%d sleeps%d", code, calls.Load(), sleeps.Load())
	}
}
