package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// testToken is the token the harness's token file holds.
const testToken = "test-token"

// recorded is one request the fake API received.
type recorded struct {
	method, path, auth string
	query              map[string][]string
	body               []byte
}

// fakeAPI is the operator's /v1 API as an httptest TLS server. Each test sets
// handle; every request is recorded first.
type fakeAPI struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []recorded
	handle func(w http.ResponseWriter, r *http.Request, body []byte)
}

func (f *fakeAPI) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

// harness runs the CLI against a fake API with a fake clock and fake files.
type harness struct {
	t              *testing.T
	api            *fakeAPI
	env            Env
	vars           map[string]string
	stdout, stderr bytes.Buffer
	now            time.Time
	sleeps         []time.Duration
	dir            string
	// kubectl answers the commands Env.Run runs with an exit code; nil means
	// kubectl is not on PATH. ran records each command line.
	kubectl func(argv []string) int
	ran     []string
}

var testNow = time.Date(2026, 10, 6, 21, 0, 0, 0, time.UTC)

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, now: testNow, dir: t.TempDir()}
	h.api = &fakeAPI{}
	h.api.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.api.mu.Lock()
		h.api.reqs = append(h.api.reqs, recorded{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), query: r.URL.Query(), body: body})
		handle := h.api.handle
		h.api.mu.Unlock()
		if handle == nil {
			writeTestJSON(w, http.StatusNotFound, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeNotFound, Message: "no handler"}})
			return
		}
		handle(w, r, body)
	}))
	t.Cleanup(h.api.srv.Close)

	caFile := h.write("api-ca.crt", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.api.srv.Certificate().Raw})))
	tokenFile := h.write("api-token", testToken+"\n")
	h.vars = map[string]string{
		envAPIURL:    h.api.srv.URL,
		envTokenFile: tokenFile,
		envCAFile:    caFile,
	}
	h.env = Env{
		Stdout: &h.stdout,
		Stderr: &h.stderr,
		Stdin:  strings.NewReader(""),
		Getenv: func(k string) string { return h.vars[k] },
		Now:    func() time.Time { return h.now },
		Sleep: func(ctx context.Context, d time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			h.sleeps = append(h.sleeps, d)
			h.now = h.now.Add(d)
			return nil
		},
		NewKey:            func() string { return "agent-run-testkey" },
		SessionTokenFile:  filepath.Join(h.dir, "no-session-token"),
		ServiceAccountDir: filepath.Join(h.dir, "no-serviceaccount"),
		LookPath: func(name string) (string, error) {
			if name == "kubectl" && h.kubectl != nil {
				return "/usr/bin/kubectl", nil
			}
			return "", errors.New("not found")
		},
		Run: func(_ context.Context, argv []string) (int, error) {
			h.ran = append(h.ran, strings.Join(argv, " "))
			if h.kubectl == nil {
				return 0, errors.New("no kubectl")
			}
			return h.kubectl(argv), nil
		},
	}
	return h
}

// write puts a file in the harness's directory and returns its path.
func (h *harness) write(name, content string) string {
	h.t.Helper()
	p := filepath.Join(h.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return p
}

// run runs the CLI and returns its exit code; stdout and stderr are kept.
func (h *harness) run(args ...string) int {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	return Run(context.Background(), args, h.env)
}

// mustRun runs the CLI and fails the test unless it exits with want.
func (h *harness) mustRun(want int, args ...string) {
	h.t.Helper()
	if code := h.run(args...); code != want {
		h.t.Fatalf("agent-run %q: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", args, code, want, h.stdout.String(), h.stderr.String())
	}
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, code, msg string, fields ...apiv1.FieldError) {
	writeTestJSON(w, status, apiv1.ErrorResponse{Error: apiv1.Error{Code: code, Message: msg, Fields: fields}})
}

// testSession is a session as the API shows it.
func testSession(name, phase string) apiv1.Session {
	return apiv1.Session{
		Name: name, Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Effort: "xhigh",
		Size: "M", Parent: "dev/dev-env", CreatedAt: testNow.Add(-5 * time.Minute), Phase: phase,
	}
}

func pendingOn(s apiv1.Session, reason, msg string) apiv1.Session {
	s.Phase, s.Pending = "Pending", msg
	s.Conditions = []apiv1.Condition{{Type: conditionPodReady, Status: "False", Reason: reason, Message: msg}}
	return s
}

func runningOn(s apiv1.Session, node string) apiv1.Session {
	s.Phase, s.Node = "Running", node
	s.Conditions = []apiv1.Condition{{Type: conditionPodReady, Status: "True", Reason: "PodReady", Message: "the pod is Ready on " + node}}
	return s
}

func decodeCreate(t *testing.T, body []byte) apiv1.CreateSessionRequest {
	t.Helper()
	var req apiv1.CreateSessionRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("the create body is not a CreateSessionRequest: %v\n%s", err, body)
	}
	return req
}

func contains(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s does not contain %q:\n%s", what, w, got)
		}
	}
}
