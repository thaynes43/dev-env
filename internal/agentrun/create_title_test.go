package agentrun

import (
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestCreateTitle(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodPost || r.URL.Path != apiv1.SessionsPath {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		req := decodeCreate(t, body)
		s := testSession(name, "Pending")
		s.Title = req.Title
		s.Model = req.Model
		s.Mode = req.Mode
		writeTestJSON(w, http.StatusCreated, s)
	}

	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "fix the docs", "--wait", "0", "--title", "  Main coordinator  ")
	posts := sessionPosts(h)
	if len(posts) != 1 || decodeCreate(t, posts[0].body).Title != "Main coordinator" {
		t.Fatalf("task create posts %d", len(posts))
	}

	resetRequests(h)
	h.mustRun(ExitOK, "--repo", "haynes-ops", "--interactive", "--model", "claude-opus-5-5", "--wait", "0", "--title", "Main coordinator")
	if !strings.Contains(h.stdout.String(), "Opus | Main coordinator") || !strings.Contains(h.stdout.String(), name) {
		t.Fatalf("phone line %s", h.stdout.String())
	}
	if got := decodeCreate(t, sessionPosts(h)[0].body); got.Title != "Main coordinator" || got.Mode != "remote" {
		t.Fatalf("interactive body %+v", got)
	}

	resetRequests(h)
	h.mustRun(ExitUsage, "--repo", "haynes-ops", "-p", "fix the docs", "--wait", "0", "--title", strings.Repeat("x", 49))
	if len(h.api.requests()) != 0 || !strings.Contains(h.stderr.String(), "title_too_long") || strings.Contains(h.stderr.String(), strings.Repeat("x", 49)) {
		t.Fatalf("bad title stderr %q requests %d", h.stderr.String(), len(h.api.requests()))
	}

	resetRequests(h)
	h.mustRun(ExitUsage, "--repo", "haynes-ops", "-p", "fix the docs", "--wait", "0", "--title", "bad|title")
	if len(h.api.requests()) != 0 || !strings.Contains(h.stderr.String(), "title_characters") {
		t.Fatalf("bar stderr %q", h.stderr.String())
	}
	for _, title := range []string{"-Main", ".Main", "!Main", "€Main"} {
		resetRequests(h)
		h.mustRun(ExitUsage, "--repo", "haynes-ops", "-p", "fix the docs", "--wait", "0", "--title", title)
		if len(h.api.requests()) != 0 || !strings.Contains(h.stderr.String(), "title_characters") {
			t.Fatalf("leading %q stderr %q requests %d", title, h.stderr.String(), len(h.api.requests()))
		}
	}
	h.mustRun(ExitUsage, "--repo", "haynes-ops", "-p", "fix the docs", "--wait", "0", "--title", "Main  coordinator")
	if len(h.api.requests()) != 0 || !strings.Contains(h.stderr.String(), "title_spacing") {
		t.Fatalf("spacing stderr %q requests %d", h.stderr.String(), len(h.api.requests()))
	}

	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodPost && r.URL.Path == apiv1.SessionsPath {
			apiError(w, http.StatusBadRequest, apiv1.CodeBadRequest, `the body is not a valid request: json: unknown field "title"`)
			return
		}
		t.Errorf("retried %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
	resetRequests(h)
	h.mustRun(ExitFailed, "--repo", "haynes-ops", "-p", "fix the docs", "--wait", "0", "--title", "Main coordinator")
	posts = sessionPosts(h)
	if len(posts) != 1 || decodeCreate(t, posts[0].body).Title != "Main coordinator" {
		t.Fatalf("older operator posts %d", len(posts))
	}
	if !strings.Contains(h.stderr.String(), "did not retry") || strings.Contains(h.stderr.String(), "unknown field") || strings.Contains(h.stderr.String(), "Main coordinator") {
		t.Fatalf("older operator stderr %q", h.stderr.String())
	}
}

func resetRequests(h *harness) {
	h.api.mu.Lock()
	h.api.reqs = nil
	h.api.mu.Unlock()
}

func sessionPosts(h *harness) []recorded {
	var posts []recorded
	for _, req := range h.api.requests() {
		if req.method == http.MethodPost && req.path == apiv1.SessionsPath {
			posts = append(posts, req)
		}
	}
	return posts
}
