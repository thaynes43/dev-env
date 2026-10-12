package agentrun

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	claudeCanaryID = "rule-canary-claude-1011"
	codexCanaryID  = "rule-canary-codex-1011"
)

// These names are independent fixed-record source witnesses for dev/dev-env,
// not values computed by the CLI implementation under test.
func originalCanary(id string) apiv1.Session {
	s := apiv1.Session{
		UID: "original-session-uid", Name: "private-canary-fc5935c175c6fc15f842b051bf85aa8d",
		Repo: "dev-env", Base: "main", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Effort: "xhigh",
		Size: "S", Profile: "rule-canary-claude", Parent: "dev/dev-env", IdempotencyKey: id + "-once", CreatedAt: testNow,
	}
	if id == codexCanaryID {
		s.Name, s.Agent, s.Model, s.Effort, s.Profile = "private-canary-3bfe28755095977c9462c269cc24dee3", "codex", "gpt-6-astra", "max", "rule-canary-codex"
	}
	return s
}

func TestCanaryIDOnlyOriginalReceipt(t *testing.T) {
	for _, tc := range []struct {
		id          string
		status      int
		disposition string
		output      []string
	}{
		{claudeCanaryID, http.StatusCreated, "CreatedOriginalSession", []string{"--json"}},
		{codexCanaryID, http.StatusOK, "ExistingOriginalSession", []string{"-o", "json"}},
	} {
		t.Run(tc.disposition, func(t *testing.T) {
			h := newHarness(t)
			keys := 0
			h.env.NewKey = func() string { keys++; return "unexpected-new-key" }
			// Explicit null survives strict DTO validation and the receipt envelope.
			raw := []byte(strings.TrimSuffix(mustJSON(t, originalCanary(tc.id)), "}") + `,"suspendedAt":null}`)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write(raw)
			}
			args := append([]string{"canary", "create", "--id", tc.id, "--once"}, tc.output...)
			h.mustRun(ExitOK, args...)
			reqs := h.api.requests()
			if len(reqs) != 1 {
				t.Fatalf("got %d requests, want exactly one POST with no polling", len(reqs))
			}
			r := reqs[0]
			if r.method != http.MethodPost || r.path != apiv1.SessionsPath || len(r.query) != 0 || r.auth != "Bearer "+testToken {
				t.Errorf("request = %+v, want ordinary authenticated ID-only POST", r)
			}
			if want := `{"canaryID":"` + tc.id + `"}`; string(r.body) != want {
				t.Errorf("body = %s, want exact %s", r.body, want)
			}
			var receipt struct {
				CanaryID    string          `json:"canaryID"`
				HTTPStatus  int             `json:"httpStatus"`
				Disposition string          `json:"disposition"`
				Session     json.RawMessage `json:"session"`
			}
			d := json.NewDecoder(&h.stdout)
			d.DisallowUnknownFields()
			if err := d.Decode(&receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.CanaryID != tc.id || receipt.HTTPStatus != tc.status || receipt.Disposition != tc.disposition {
				t.Errorf("receipt = %+v", receipt)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, receipt.Session); err != nil || !bytes.Equal(compact.Bytes(), raw) {
				t.Errorf("validated raw receipt changed: %s; error %v", receipt.Session, err)
			}
			if keys != 0 || len(h.sleeps) != 0 || len(h.ran) != 0 || h.stderr.Len() != 0 {
				t.Errorf("keys=%d sleeps=%v subprocess=%v stderr=%s", keys, h.sleeps, h.ran, &h.stderr)
			}
		})
	}
}

func TestCanaryUsageRefusesBeforeAuthentication(t *testing.T) {
	valid := []string{"canary", "create", "--id", claudeCanaryID, "--once"}
	cases := [][]string{
		{"canary"}, {"canary", "unknown", "-p", "task"}, {"canary", "list"},
		{"canry", "-p", "task"},
		{"canary", "create", "--once"}, {"canary", "create", "--id", claudeCanaryID},
		{"canary", "create", "--id", claudeCanaryID, "--once=false"},
		{"canary", "create", "--id", "UPPER", "--once"},
		{"canary", "create", "--id", "dev/record", "--once"},
		{"canary", "create", "--id", strings.Repeat("x", 64), "--once"},
		{"canary", "create", "--id", "-bad", "--once"},
		{"canary", "create", "--id", claudeCanaryID, "--id", codexCanaryID, "--once"},
		{"canary", "create", "--id", claudeCanaryID, "--once", codexCanaryID},
	}
	for _, extra := range [][]string{
		{"-p", "task"}, {"--prompt", "task"}, {"--prompt-file", "missing"},
		{"--repo", "dev-env"}, {"--project", "dev-env"}, {"--agent", "codex"},
		{"--model", "gpt-6-astra"}, {"--profile", "full"}, {"--idempotency-key", "new-key"},
		{"--wait", "0"}, {"--timeout", "1m"}, {"--unknown"}, {"-o", "name"},
	} {
		cases = append(cases, append(append([]string(nil), valid...), extra...))
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t)
			envReads, keys := 0, 0
			h.env.Getenv = func(string) string { envReads++; return "" }
			h.env.NewKey = func() string { keys++; return "unexpected-new-key" }
			h.mustRun(ExitUsage, args...)
			if envReads != 0 || keys != 0 || len(h.api.requests()) != 0 || len(h.ran) != 0 || h.stdout.Len() != 0 {
				t.Errorf("refusal crossed authentication/create boundary: env=%d keys=%d requests=%v subprocess=%v stdout=%s", envReads, keys, h.api.requests(), h.ran, &h.stdout)
			}
		})
	}
}

// Every 5xx leaves the one POST's outcome unknown: exit 6, never a retry hint.
func TestCanaryErrorsMakeOneAttempt(t *testing.T) {
	for _, tc := range []struct{ status, exit int }{
		{http.StatusServiceUnavailable, ExitUnconfirmed}, {http.StatusInternalServerError, ExitUnconfirmed},
		{http.StatusBadGateway, ExitUnconfirmed}, {http.StatusGatewayTimeout, ExitUnconfirmed},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				apiError(w, tc.status, apiv1.CodeUnavailable, "original acknowledgement unavailable")
			}
			h.mustRun(tc.exit, "canary", "create", "--id", claudeCanaryID, "--once", "--json")
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 || !strings.Contains(h.stderr.String(), "do not repeat") || strings.Contains(h.stderr.String(), "try again") {
				t.Errorf("error retried, invented a receipt or advised a retry: requests=%v sleeps=%v stdout=%s stderr=%s", h.api.requests(), h.sleeps, &h.stdout, &h.stderr)
			}
		})
	}
}

func TestCanaryRefusesRedirects(t *testing.T) {
	for _, status := range []int{http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				http.Redirect(w, r, h.api.srv.URL+"/redirected", status)
			}
			h.mustRun(ExitFailed, "canary", "create", "--id", claudeCanaryID, "--once", "--json")
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 {
				t.Errorf("redirect was followed or receipt invented: requests=%v sleeps=%v stdout=%s", h.api.requests(), h.sleeps, &h.stdout)
			}
		})
	}
}

func TestCanaryRefusesOtherSuccessStatuses(t *testing.T) {
	for _, status := range []int{http.StatusAccepted, http.StatusNoContent, http.StatusPartialContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				writeTestJSON(w, status, originalCanary(claudeCanaryID))
			}
			h.mustRun(ExitUnconfirmed, "canary", "create", "--id", claudeCanaryID, "--once", "--json")
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 {
				t.Errorf("unacknowledged status retried or accepted: requests=%v sleeps=%v stdout=%s", h.api.requests(), h.sleeps, &h.stdout)
			}
		})
	}
}

func TestCanaryRefusesUnconfirmedReceipt(t *testing.T) {
	cases := map[string][]byte{
		"null": []byte("null"), "empty": nil, "non JSON": []byte("unfinished"),
		"array": []byte("[]"), "trailing JSON": []byte("{}{}"),
	}
	for label, mutate := range map[string]func(*apiv1.Session){
		"missing UID":              func(s *apiv1.Session) { s.UID = "" },
		"oversized UID":            func(s *apiv1.Session) { s.UID = strings.Repeat("x", 129) },
		"control UID":              func(s *apiv1.Session) { s.UID = "original\nuid" },
		"different name":           func(s *apiv1.Session) { s.Name = "replacement-session" },
		"different original ID":    func(s *apiv1.Session) { s.Name = originalCanary(codexCanaryID).Name },
		"missing parent":           func(s *apiv1.Session) { s.Parent = "" },
		"log parent prefix":        func(s *apiv1.Session) { s.Parent = "client/dev/dev-env" },
		"foreign parent same name": func(s *apiv1.Session) { s.Parent = "other/dev-env" },
		"invalid parent component": func(s *apiv1.Session) { s.Parent = "dev/UPPER" },
		"non task":                 func(s *apiv1.Session) { s.Mode = "terminal" },
		"missing createdAt":        func(s *apiv1.Session) { s.CreatedAt = time.Time{} },
		"unknown provider":         func(s *apiv1.Session) { s.Agent = "unknown" },
		"missing key":              func(s *apiv1.Session) { s.IdempotencyKey = "" },
		"full profile":             func(s *apiv1.Session) { s.Profile = "full" },
	} {
		s := originalCanary(claudeCanaryID)
		mutate(&s)
		cases[label] = []byte(mustJSON(t, s))
	}
	raw := mustJSON(t, originalCanary(claudeCanaryID))
	cases["unknown field"] = []byte(strings.TrimSuffix(raw, "}") + `,"canaryPolicy":{"trusted":true}}`)
	cases["wrong field type"] = []byte(strings.Replace(raw, `"uid":"original-session-uid"`, `"uid":42`, 1))
	cases["invalid UTF8"] = []byte(strings.Replace(raw, `"original-session-uid"`, "\"original-\xff-uid\"", 1))
	for label, raw := range cases {
		t.Run(label, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(raw)
			}
			h.mustRun(ExitUnconfirmed, "canary", "create", "--id", claudeCanaryID, "--once", "--json")
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 {
				t.Errorf("unconfirmed original retried or accepted: requests=%v sleeps=%v stdout=%s", h.api.requests(), h.sleeps, &h.stdout)
			}
		})
	}
}
