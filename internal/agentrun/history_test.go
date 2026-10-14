package agentrun

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	histUID = "6f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	histSum = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

var histAt = time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)

// serveRaw answers one path with a raw JSON body, so a test can send fields
// this client does not know.
func serveRaw(h *harness, routes map[string]func(r *http.Request) (int, string)) {
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		route, ok := routes[r.URL.Path]
		if !ok {
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route "+r.Method+" "+r.URL.Path)
			return
		}
		status, body := route(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestHistoryListTableAndNewFields(t *testing.T) {
	h := newHarness(t)
	list := `{"archived":true,"project":"sample","futureField":{"x":1},"continue":"2026-11/next.json","items":[` +
		`{"archived":true,"archivedAt":"2026-11-12T01:00:00Z","project":"sample","session":"task-1","sessionUID":"` + histUID + `","provider":"claude","contentSHA256":"` + histSum + `","bytes":12288,"files":7,"terminal":{"podUID":"p","phase":"Failed","reason":"DeadlineExceeded","observedAt":"2026-10-11T01:00:00Z"},"newItemField":true},` +
		`{"archived":true,"archivedAt":"2026-11-11T01:00:00Z","project":"sample","sessionUID":"22222222-4a5b-4c6d-8e7f-0a1b2c3d4e5f","contentSHA256":"` + histSum + `","error":"corrupt"}]}`
	serveRaw(h, map[string]func(*http.Request) (int, string){apiv1.HistoryPath: func(*http.Request) (int, string) { return http.StatusOK, list }})

	h.mustRun(ExitOK, "history", "list", "--project", "sample")
	contains(t, "stdout", h.stdout.String(),
		"SESSION", "UID", "PROVIDER", "ARCHIVED", "FILES", "SIZE", "TERMINAL",
		"task-1", histUID, "claude", "2026-11-12 01:00", "7", "12K", "Failed (DeadlineExceeded)",
		"corrupt",
		"more: agent-run history list --project sample --continue 2026-11/next.json",
	)
	reqs := h.api.requests()
	if len(reqs) != 1 || reqs[0].query["project"][0] != "sample" || reqs[0].query["limit"] != nil {
		t.Errorf("requests %+v", reqs)
	}

	h.stdout.Reset()
	h.mustRun(ExitOK, "history", "list", "--project", "sample", "--limit", "5", "--continue", "2026-11/next.json", "-o", "json")
	if !strings.Contains(h.stdout.String(), `"sessionUID": "`+histUID+`"`) || !strings.Contains(h.stdout.String(), `"archived": true`) {
		t.Errorf("json: %s", h.stdout.String())
	}
	if q := h.api.requests()[1].query; q["limit"][0] != "5" || q["continue"][0] != "2026-11/next.json" {
		t.Errorf("query %v", q)
	}
}

func TestHistoryListProjectlessAndContinuation(t *testing.T) {
	for _, args := range [][]string{{"history", "list"}, {"history", "list", "--project", ""}} {
		h := newHarness(t)
		serveRaw(h, map[string]func(*http.Request) (int, string){apiv1.HistoryPath: func(r *http.Request) (int, string) {
			if values, ok := r.URL.Query()["project"]; !ok || len(values) != 1 || values[0] != "" {
				t.Error("projectless list did not select the empty project bucket")
			}
			return http.StatusOK, `{"archived":true,"project":"","items":[],"continue":"2026-11/next.json"}`
		}})
		h.mustRun(ExitOK, args...)
		contains(t, "stdout", h.stdout.String(), "no archived history without a project", "more: agent-run history list --continue 2026-11/next.json")
		if strings.Contains(h.stdout.String(), "--project  --continue") || len(h.api.requests()) != 1 {
			t.Fatal("projectless pagination hint or request count changed")
		}
	}
}

func TestHistoryShowAndItsPages(t *testing.T) {
	h := newHarness(t)
	manifest := `{"archived":true,"archivedAt":"2026-11-12T01:00:00Z","contentSHA256":"` + histSum + `","session":{"namespace":"dev-agents","name":"task-1","uid":"` + histUID + `"},` +
		`"project":"sample","provider":"claude","homeUID":"h","terminal":{"podUID":"p","phase":"Failed","reason":"DeadlineExceeded","observedAt":"2026-10-11T01:00:00Z"},` +
		`"rescue":{"stamp":"20261012-0102","result":"Verified"},"roots":[{"name":"claude","dir":".claude"}],"git":{"rescue":"task-1/20261012-0102","files":[{"name":"manifest.json","bytes":10,"sha256":"` + histSum + `"}]},` +
		`"bytes":5,"fileCount":1,"excludedCount":1,"offset":0,"files":[{"root":"claude","path":"projects/-home-dev/a.jsonl","bytes":5,"sha256":"` + histSum + `","modTime":"2026-10-11T00:00:00Z"}],` +
		`"excluded":[{"root":"claude","path":"file-history","class":"user_copy","bytes":2048}],"continue":"2","later":"ignored"}`
	serveRaw(h, map[string]func(*http.Request) (int, string){apiv1.HistoryEntryPath(histUID): func(*http.Request) (int, string) { return http.StatusOK, manifest }})

	h.mustRun(ExitOK, "history", "show", histUID)
	contains(t, "stdout", h.stdout.String(),
		"session:    task-1 ("+histUID+")", "project:    sample", "archived:   2026-11-12 01:00 UTC", "content:    "+histSum,
		"terminal:   Failed (DeadlineExceeded), observed 2026-10-11 01:00 UTC", "rescue:     20261012-0102 Verified, Git files in task-1/20261012-0102: 1",
		"files:      1 (5B)", "excluded:   1",
		"claude/projects/-home-dev/a.jsonl", "file-history", "user_copy", "2.0K",
		"more: agent-run history show "+histUID+" --content "+histSum+" --continue 2",
	)
	h.stdout.Reset()
	h.mustRun(ExitOK, "history", "show", histUID, "--content", histSum, "--limit", "1", "--continue", "2")
	if q := h.api.requests()[1].query; q["content"][0] != histSum || q["limit"][0] != "1" || q["continue"][0] != "2" {
		t.Errorf("query %v", q)
	}
}

func TestHistoryReadFollowsThePagesAndChecksTheHash(t *testing.T) {
	data := "hello, archived world"
	sum := sha256.Sum256([]byte(data))
	fileSum := hex.EncodeToString(sum[:])
	page := func(r *http.Request) (int, string) {
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		end := min(off+8, len(data))
		p := apiv1.HistoryFilePage{Archived: true, ArchivedAt: histAt, SessionUID: histUID, ContentSHA256: histSum, Path: r.URL.Query().Get("path"),
			FileSHA256: fileSum, Bytes: int64(len(data)), Offset: int64(off), Next: int64(end), EOF: end == len(data), Data: []byte(data[off:end])}
		b := mustJSON(t, p)
		return http.StatusOK, b
	}
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){apiv1.HistoryFilePath(histUID): page})

	h.mustRun(ExitOK, "history", "read", histUID, "claude/projects/a.jsonl")
	if h.stdout.String() != data[:8] {
		t.Errorf("one page: %q", h.stdout.String())
	}
	h.stdout.Reset()
	h.mustRun(ExitOK, "history", "read", histUID, "claude/projects/a.jsonl", "--all")
	if h.stdout.String() != data {
		t.Errorf("--all: %q", h.stdout.String())
	}
	reqs := h.api.requests()
	if len(reqs) != 4 || reqs[1].query["offset"][0] != "0" || reqs[2].query["offset"][0] != "8" || reqs[3].query["offset"][0] != "16" || reqs[3].query["path"][0] != "claude/projects/a.jsonl" {
		t.Errorf("pages: %+v", reqs)
	}

	h.stdout.Reset()
	h.mustRun(ExitOK, "history", "read", histUID, "claude/projects/a.jsonl", "--offset", "8", "-o", "json")
	if !strings.Contains(h.stdout.String(), `"next": 16`) || !strings.Contains(h.stdout.String(), `"archived": true`) {
		t.Errorf("json page: %s", h.stdout.String())
	}

	// A file whose bytes do not hash to the archive's digest is no read.
	fileSum = strings.Repeat("0", 64)
	h.stdout.Reset()
	h.mustRun(ExitFailed, "history", "read", histUID, "claude/projects/a.jsonl", "--all")
	contains(t, "stderr", h.stderr.String(), "SHA-256")

	// A page that does not move on is no read either.
	h2 := newHarness(t)
	serveRaw(h2, map[string]func(*http.Request) (int, string){apiv1.HistoryFilePath(histUID): func(r *http.Request) (int, string) {
		return http.StatusOK, mustJSON(t, apiv1.HistoryFilePage{Archived: true, Path: "claude/x", Bytes: 10, Offset: 0, Next: 0, Data: nil})
	}})
	h2.mustRun(ExitFailed, "history", "read", histUID, "claude/x", "--all")
}

// An operator without the routes (older than H6), or with the reads off,
// answers 404: a clear refusal, never a crash or a retry.
func TestHistoryOnAnOperatorWithoutTheReads(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, nil)
	for _, args := range [][]string{
		{"history", "list", "--project", "sample"},
		{"history", "show", histUID},
		{"history", "read", histUID, "claude/x"},
	} {
		h.stderr.Reset()
		h.mustRun(ExitNotFound, args...)
		contains(t, "stderr", h.stderr.String(), "archived history")
	}
	if len(h.sleeps) != 0 {
		t.Errorf("a 404 was retried: %v", h.sleeps)
	}
}

func TestHistoryUsage(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"history"},
		{"history", "fly"},
		{"history", "list", "extra", "--project", "x"},
		{"history", "show"},
		{"history", "show", histUID, "extra"},
		{"history", "read", histUID},
		{"history", "read", histUID, "claude/x", "--offset", "-1"},
		{"history", "read", histUID, "claude/x", "--all", "-o", "json"},
		// A Session UID goes into the request path: anything that is not a
		// Kube UID could reach another route or add query parameters, so it
		// is refused before any request.
		{"history", "show", "../sessions/x"},
		{"history", "show", histUID + "?content=x"},
		{"history", "show", strings.ToUpper(histUID)},
		{"history", "read", "../sessions/x", "claude/x"},
		{"history", "read", "a?b=c", "claude/x"},
		{"history", "read", histUID + "/file", "claude/x"},
	} {
		h.mustRun(ExitUsage, args...)
	}
	if len(h.api.requests()) != 0 {
		t.Errorf("a usage error sent a request: %+v", h.api.requests())
	}
	h.stdout.Reset()
	h.mustRun(ExitOK, "help", "history")
	contains(t, "stdout", h.stdout.String(), "agent-run history list", "agent-run history show", "agent-run history read", "archived: true")
}
