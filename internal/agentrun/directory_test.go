package agentrun

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestDirectoryRejectsBeforeConnect(t *testing.T) {
	for _, args := range [][]string{
		{"directory"},
		{"directory", "create", "--repo", "dev-env", "-p", "never create"},
		{"directory", "lis", "--project", "dev-env", "--once"},
		{"directory", "list", "--project", "dev-env"},
		{"directory", "list", "--once"},
		{"directory", "list", "--project", "Dev-env", "--once"},
		{"directory", "list", "--project", "-dev-env", "--once"},
		{"directory", "list", "--project", strings.Repeat("a", 64), "--once"},
		{"directory", "list", "--project", "dev-env", "--limit", "0", "--once"},
		{"directory", "list", "--project", "dev-env", "--limit", "21", "--once"},
		{"directory", "list", "--project", "dev-env", "--limit", "not-a-number", "--once"},
		{"directory", "list", "--project", "dev-env", "--continue", strings.Repeat("x", 4097), "--once"},
		{"directory", "list", "--project", "dev-env", "--continue", "bad\ncursor", "--once"},
		{"directory", "list", "--project", "dev-env", "--once", "--unexpected"},
		{"directory", "list", "--project", "dev-env", "--once", "-o", "name"},
		{"directory", "list", "--project", "dev-env", "--once", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t)
			reads := 0
			h.env.Getenv = func(string) string { reads++; return "" }
			h.env.NewKey = func() string { t.Fatal("directory reached session create"); return "" }
			h.mustRun(ExitUsage, args...)
			if reads != 0 || len(h.api.requests()) != 0 || len(h.ran) != 0 {
				t.Fatalf("invalid Directory command reached auth/API/subprocess: env=%d requests=%d ran=%v", reads, len(h.api.requests()), h.ran)
			}
		})
	}
}

func TestDirectoryListFullPageAndQuery(t *testing.T) {
	for _, output := range [][]string{{"--json"}, {"-o", "json"}} {
		t.Run(strings.Join(output, " "), func(t *testing.T) {
			h := newHarness(t)
			page := apiv1.SessionDirectoryList{
				Items: []apiv1.SessionDirectory{{
					Name: "session-directory.original", UID: "directory-uid", ResourceVersion: "directory-rv",
					Original: apiv1.DirectoryOriginal{Version: 1, Project: "dev-env", TaskKey: "canary:rule-canary-codex-1011", Source: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "original-session", UID: "original-uid"}},
					Retained: apiv1.DirectoryRetained{SourceState: "Unknown", StopState: "Unknown", LastWitness: &apiv1.DirectoryWitness{ReportedHeartbeatAt: &apiv1.DirectoryReportedTime{}}},
					Current:  apiv1.DirectoryCurrent{SourceState: "SourceMissing", PlacementState: "Unknown", ObservedAt: testNow, StopState: "Unknown"},
				}},
				ResourceVersion: "list-rv", Continue: "next-page",
			}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) { writeTestJSON(w, http.StatusOK, page) }
			cursor := "opaque+/=?&project=foreign"
			args := append([]string{"directory", "list", "--project", "dev-env", "--limit", "20", "--continue", cursor, "--once"}, output...)
			h.mustRun(ExitOK, args...)
			reqs := h.api.requests()
			if len(reqs) != 1 || reqs[0].method != http.MethodGet || reqs[0].path != apiv1.SessionDirectoryPath || reqs[0].auth != "Bearer "+testToken || len(reqs[0].body) != 0 {
				t.Fatalf("Directory request was not one authenticated GET: %+v", reqs)
			}
			wantQuery := map[string][]string{"project": {"dev-env"}, "limit": {"20"}, "continue": {cursor}}
			if !reflect.DeepEqual(reqs[0].query, wantQuery) {
				t.Fatalf("Directory query=%v, want %v", reqs[0].query, wantQuery)
			}
			var got, want any
			wire, err := json.Marshal(page)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(wire, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("validated wire page changed: got %+v, want %+v, error %v", got, want, err)
			}
			contains(t, "page", h.stdout.String(), `"continue": "next-page"`, `"reportedHeartbeatAt": null`, `"observedAt": null`, `"stopState": "Unknown"`)
			if len(h.sleeps) != 0 {
				t.Fatal("Directory read retried")
			}
		})
	}
}

func TestDirectoryListEmptyCompletePage(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.SessionDirectoryList{Items: []apiv1.SessionDirectory{}, ResourceVersion: "list-rv"})
	}
	h.mustRun(ExitOK, "directory", "list", "--project", "dev-env", "--json", "--once")
	contains(t, "page", h.stdout.String(), `"items": []`, `"resourceVersion": "list-rv"`)
	if reqs := h.api.requests(); len(reqs) != 1 || reqs[0].query["limit"][0] != "20" || len(reqs[0].query) != 2 {
		t.Fatalf("unexpected default page query: %+v", reqs)
	}
}

func TestDirectoryOnceFailures(t *testing.T) {
	// A one-shot read that reached the API never exits 5: every 5xx is an
	// unconfirmed answer (6).
	for _, tc := range []struct{ status, exit int }{{500, ExitUnconfirmed}, {502, ExitUnconfirmed}, {503, ExitUnconfirmed}, {504, ExitUnconfirmed}} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
				apiError(w, tc.status, apiv1.CodeUnavailable, "unconfirmed")
			}
			h.mustRun(tc.exit, "directory", "list", "--project", "dev-env", "--once", "--json")
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 {
				t.Fatalf("failed Directory read repeated or emitted a page: requests=%d sleeps=%d stdout=%q", len(h.api.requests()), len(h.sleeps), h.stdout.String())
			}
		})
	}
}

func TestDirectoryOnceRefusesRedirect(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		http.Redirect(w, r, apiv1.SessionsPath, http.StatusTemporaryRedirect)
	}
	h.mustRun(ExitFailed, "directory", "list", "--project", "dev-env", "--once", "--json")
	if reqs := h.api.requests(); len(reqs) != 1 || reqs[0].path != apiv1.SessionDirectoryPath || h.stdout.Len() != 0 {
		t.Fatalf("Directory followed redirect or emitted a page: %+v stdout=%q", reqs, h.stdout.String())
	}
}

func TestDirectoryOnceTransportFailure(t *testing.T) {
	h := newHarness(t)
	h.api.srv.Close()
	// The connection was never made, so nothing was sent: exit 5, no retry.
	h.mustRun(ExitRetry, "directory", "list", "--project", "dev-env", "--once", "--json")
	if len(h.sleeps) != 0 || h.stdout.Len() != 0 || !strings.Contains(h.stderr.String(), "nothing was sent") {
		t.Fatalf("Directory transport failure retried, emitted a page or misreported: sleeps=%v stdout=%q stderr=%q", h.sleeps, h.stdout.String(), h.stderr.String())
	}
}

func TestDirectoryOnceLeavesOtherGETRetries(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		apiError(w, http.StatusServiceUnavailable, apiv1.CodeUnavailable, "unconfirmed")
	}
	h.mustRun(ExitUnconfirmed, "directory", "list", "--project", "dev-env", "--once")
	h.mustRun(ExitRetry, "list", "-o", "json")
	reqs := h.api.requests()
	if len(reqs) != 4 || reqs[0].path != apiv1.SessionDirectoryPath || len(h.sleeps) != 2 {
		t.Fatalf("ordinary GET retries changed: requests=%+v sleeps=%v", reqs, h.sleeps)
	}
	for _, req := range reqs[1:] {
		if req.method != http.MethodGet || req.path != apiv1.SessionsPath {
			t.Fatalf("ordinary GET route changed: %+v", req)
		}
	}
}

func TestDirectoryRejectsUnconfirmedPage(t *testing.T) {
	for _, body := range []string{
		`not-json`,
		`{"items":[],"resourceVersion":"rv","unexpected":true}`,
		`{"items":null,"resourceVersion":"rv"}`,
		`{"resourceVersion":"rv"}`,
		`{"items":[],"resourceVersion":""}`,
		`{"items":[],"resourceVersion":"rv"} {}`,
		`{"items":[{"name":"d","uid":"u","resourceVersion":"r","original":{"project":"foreign"}}],"resourceVersion":"rv"}`,
		`{"items":[{"name":"d","uid":"u","resourceVersion":"","original":{"project":"dev-env"}}],"resourceVersion":"rv"}`,
		`{"items":[{"name":"d","uid":"u","resourceVersion":"r","original":{"project":"dev-env","request":{"model":"bad` + string([]byte{0xff}) + `"}}}],"resourceVersion":"rv"}`,
		`{"items":[],"resourceVersion":"rv","continue":"` + strings.Repeat("x", 4097) + `"}`,
	} {
		t.Run(body[:min(len(body), 90)], func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, payload []byte) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(body))
			}
			h.mustRun(ExitFailed, "directory", "list", "--project", "dev-env", "--once", "--json")
			if h.stdout.Len() != 0 || len(h.api.requests()) != 1 {
				t.Fatalf("unconfirmed page emitted or request repeated: stdout=%q requests=%d", h.stdout.String(), len(h.api.requests()))
			}
		})
	}
	for _, status := range []int{http.StatusAccepted, http.StatusNoContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) { w.WriteHeader(status) }
			h.mustRun(ExitFailed, "directory", "list", "--project", "dev-env", "--once")
			if h.stdout.Len() != 0 || len(h.api.requests()) != 1 {
				t.Fatal("non-200 Directory page accepted or retried")
			}
		})
	}
}

func TestDirectoryRejectsPageOverRequestedLimit(t *testing.T) {
	h := newHarness(t)
	item := apiv1.SessionDirectory{Name: "d", UID: "u", ResourceVersion: "r", Original: apiv1.DirectoryOriginal{Project: "dev-env"}}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.SessionDirectoryList{Items: []apiv1.SessionDirectory{item, item}, ResourceVersion: "rv"})
	}
	h.mustRun(ExitFailed, "directory", "list", "--project", "dev-env", "--limit", "1", "--once")
	if h.stdout.Len() != 0 || len(h.api.requests()) != 1 {
		t.Fatal("oversized Directory page accepted or retried")
	}
}
