package agentrun

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	testKeeperLink = "https://claude.com/cai/oauth/authorize?client_id=x&state=LINKSTATE&code_challenge=CHAL"
	testKeeperCode = "CLICODE9f3a#CLISTATE41"
)

func TestKeeperLoginStatus(t *testing.T) {
	h := newHarness(t)
	access := testNow.Add(3*time.Hour + 12*time.Minute)
	login := testNow.Add(28 * 24 * time.Hour)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.KeeperLoginStatus{Provider: "claude", State: "Ready", Generation: 2, AccessExpiresAt: &access, LoginExpiresAt: &login})
	}
	h.mustRun(ExitOK, "keeper-login", "claude")
	contains(t, "status", h.stdout.String(), "claude: Ready (generation 2)", "in 3h12m0s", "in 28 days", "the keeper renews it by itself")
	if got := h.api.requests()[0]; got.method != http.MethodGet || got.path != "/v1/keeper-login/claude" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	h.mustRun(ExitOK, "keeper-login", "claude", "status", "-o", "json")
	contains(t, "json", h.stdout.String(), `"state": "Ready"`, `"generation": 2`)

	// Not Ready exits 1; an old keeper without expiry says so.
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.KeeperLoginStatus{Provider: "codex", State: "NeedsLogin"})
	}
	h.mustRun(ExitFailed, "keeper-login", "codex", "status")
	contains(t, "needs login", h.stdout.String(), "codex: NeedsLogin", "does not report expiry yet")
}

func TestKeeperLoginUsage(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitUsage, "keeper-login")
	h.mustRun(ExitUsage, "keeper-login", "gemini", "status")
	h.mustRun(ExitUsage, "keeper-login", "claude", "renew")
	h.mustRun(ExitUsage, "keeper-login", "claude", "url", "extra")
	// A missing code sends nothing at all.
	h.mustRun(ExitUsage, "keeper-login", "claude", "code")
	if n := len(h.api.requests()); n != 0 {
		t.Fatalf("%d requests were sent", n)
	}
	// The code is never an argument.
	h.mustRun(ExitUsage, "keeper-login", "claude", "code", testKeeperCode)
	if n := len(h.api.requests()); n != 0 {
		t.Fatalf("%d requests were sent", n)
	}
}

func TestKeeperLoginForbidden(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		apiError(w, http.StatusForbidden, apiv1.CodeForbidden, "the keeper login is renewed from a workbench session only")
	}
	h.mustRun(ExitAuth, "keeper-login", "claude", "url")
	contains(t, "stderr", h.stderr.String(), "workbench session only")
}

// url prints the link and the Codex code to stdout; the code goes in the request
// body, from stdin, and nowhere else; a follower's not_leader is retried.
func TestKeeperLoginUrlAndCode(t *testing.T) {
	h := newHarness(t)
	var misses atomic.Int32
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.URL.Path {
		case "/v1/keeper-login/claude/url":
			if misses.Add(1) <= 2 {
				apiError(w, http.StatusServiceUnavailable, apiv1.CodeNotLeader, "this replica does not hold keeper login ceremonies")
				return
			}
			writeTestJSON(w, http.StatusOK, apiv1.KeeperLoginURL{Provider: "claude", URL: testKeeperLink, ExpiresAt: testNow.Add(15 * time.Minute)})
		case "/v1/keeper-login/claude/code":
			writeTestJSON(w, http.StatusOK, apiv1.KeeperLoginResult{Provider: "claude", Result: "Adopted", Generation: 3})
		case "/v1/keeper-login/codex/url":
			writeTestJSON(w, http.StatusOK, apiv1.KeeperLoginURL{Provider: "codex", URL: "https://auth.openai.com/codex/device", UserCode: "ABCD-1234", ExpiresAt: testNow.Add(15 * time.Minute)})
		}
	}
	h.mustRun(ExitOK, "keeper-login", "claude", "url")
	if misses.Load() != 3 {
		t.Fatalf("the call was sent %d times, want 3 (two not_leader answers, then the leader)", misses.Load())
	}
	lines := strings.Split(h.stdout.String(), "\n")
	foundLink := false
	for _, l := range lines {
		if l == testKeeperLink {
			foundLink = true // on its own line, bare
		}
	}
	if !foundLink {
		t.Errorf("the link is not on its own line:\n%s", h.stdout.String())
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr: %s", h.stderr.String())
	}

	h.mustRun(ExitOK, "keeper-login", "codex", "url")
	contains(t, "codex url", h.stdout.String(), "https://auth.openai.com/codex/device", "code: ABCD-1234")

	h.env.Stdin = strings.NewReader(testKeeperCode + "\n")
	h.mustRun(ExitOK, "keeper-login", "claude", "code")
	contains(t, "code", h.stdout.String(), "adopted", "generation 3")
	reqs := h.api.requests()
	last := reqs[len(reqs)-1]
	if last.method != http.MethodPost || last.path != "/v1/keeper-login/claude/code" || string(last.body) != `{"code":"`+testKeeperCode+`"}` {
		t.Errorf("code request = %s %s %s", last.method, last.path, last.body)
	}
	for _, r := range reqs {
		if strings.Contains(r.path, "CLICODE") {
			t.Errorf("the code reached a request path: %s", r.path)
		}
	}
	if strings.Contains(h.stdout.String(), "CLICODE") || strings.Contains(h.stderr.String(), "CLICODE") {
		t.Error("the code was echoed")
	}
}

// A code the keeper refused, a still-waiting flow and cancel.
func TestKeeperLoginCodeOutcomes(t *testing.T) {
	h := newHarness(t)
	h.env.Stdin = strings.NewReader(testKeeperCode)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusUnprocessableEntity, apiv1.KeeperLoginResult{Provider: "claude", Result: "NeedsLogin"})
	}
	h.mustRun(ExitFailed, "keeper-login", "claude", "code")
	contains(t, "refused", h.stderr.String(), "NeedsLogin", "start again with url")
	if strings.Contains(h.stderr.String()+h.stdout.String(), "CLICODE") {
		t.Error("the code was echoed")
	}

	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusAccepted, apiv1.KeeperLoginResult{Provider: "codex", Result: "Waiting"})
	}
	h.mustRun(ExitRetry, "keeper-login", "codex", "code")
	contains(t, "waiting", h.stdout.String(), "Still working", "keeper-login codex status")

	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) { w.WriteHeader(http.StatusNoContent) }
	h.mustRun(ExitOK, "keeper-login", "claude", "cancel")
	reqs := h.api.requests()
	if got := reqs[len(reqs)-1]; got.method != http.MethodDelete || got.path != "/v1/keeper-login/claude" {
		t.Errorf("cancel request = %s %s", got.method, got.path)
	}
}

// Past the reconnect budget, not_leader is a plain refusal.
func TestKeeperLoginNeverFindsTheLeader(t *testing.T) {
	h := newHarness(t)
	var n atomic.Int32
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		n.Add(1)
		apiError(w, http.StatusServiceUnavailable, apiv1.CodeNotLeader, "this replica does not hold keeper login ceremonies")
	}
	if code := h.run("keeper-login", "claude", "url"); code == ExitOK {
		t.Fatal("exit 0")
	}
	if got := n.Load(); got != keeperLoginReconnects+1 {
		t.Errorf("sent %d times, want %d", got, keeperLoginReconnects+1)
	}
}
