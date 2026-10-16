package agentrun

import (
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestPilotPreflightNoCreateFallback(t *testing.T) {
	for _, kind := range []string{"success", "unsupported", "denied", "invalid", "server-error", "malformed", "persisted", "redirect", "missing-persisted", "alias", "unknown", "created-status", "connection-loss"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
				if r.Method != http.MethodPost || r.URL.Path != apiv1.PilotPreflightPath {
					t.Fatal("preflight reached create or other route", r.URL.Path)
				}
				if got := decodeCreate(t, body); got.Project != "dev-env" || got.Prompt != "exact prompt" {
					t.Fatal("request changed")
				}
				switch kind {
				case "unsupported":
					apiError(w, 404, apiv1.CodeNotFound, "unsupported")
				case "denied":
					apiError(w, 403, apiv1.CodeForbidden, "denied")
				case "invalid":
					apiError(w, 422, apiv1.CodeInvalid, "schema")
				case "server-error":
					apiError(w, 503, apiv1.CodeUnavailable, "server")
				case "malformed":
					_, _ = w.Write([]byte(`{"dryRun":"All"}`))
				case "connection-loss":
					c, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Fatal(err)
					}
					_ = c.Close()
				case "missing-persisted":
					_, _ = w.Write([]byte(`{"dryRun":"All","session":{"name":"x"}}`))
				case "alias":
					_, _ = w.Write([]byte(`{"DryRun":"All","persisted":false,"session":{"name":"x"}}`))
				case "unknown":
					_, _ = w.Write([]byte(`{"dryRun":"All","persisted":false,"unexpected":true,"session":{"name":"x"}}`))
				case "created-status":
					writeTestJSON(w, 201, apiv1.PilotPreflight{DryRun: "All", Persisted: false, Session: testSession(name, "")})
				case "redirect":
					w.Header().Set("Location", "/v1/sessions")
					w.WriteHeader(307)
				default:
					writeTestJSON(w, 200, apiv1.PilotPreflight{DryRun: "All", Persisted: kind == "persisted", Session: testSession(name, "")})
				}
			}
			code := ExitFailed
			if kind == "success" {
				code = ExitOK
			}
			if kind == "unsupported" {
				code = ExitNotFound
			}
			if kind == "server-error" || kind == "connection-loss" {
				code = ExitUnconfirmed
			}
			if kind == "denied" {
				code = ExitAuth
			}
			h.mustRun(code, "--project", "dev-env", "--repo", "dev-env", "-p", "exact prompt", "--dry-run=server", "-o", "json")
			if len(h.api.requests()) != 1 {
				t.Fatal("preflight retried or fell back", len(h.api.requests()))
			}
		})
	}
}
func TestPilotPreflightUsageRefusesBeforeConnection(t *testing.T) {
	for _, args := range [][]string{{"--project", "dev-env", "-p", "task", "--dry-run="}, {"--project", "dev-env", "-p", "task", "--dry-run", ""}, {"--project", "dev-env", "-p", "task", "--dry-run=", "--dry-run=server"}, {"dev-env", "-p", "task", "--dry-run=server"}, {"--project", "dev-env", "-p", "task", "--dry-run=client"}, {"--project", "dev-env", "--local", "--dry-run=server"}} {
		h := newHarness(t)
		h.mustRun(ExitUsage, args...)
		if len(h.api.requests()) != 0 {
			t.Fatal("invalid preflight connected")
		}
	}
}

func TestPilotPreflightHelpIsTaskOnly(t *testing.T) {
	if !strings.Contains(commandHelp["run"], "--dry-run=server") {
		t.Fatal("task help missing dry-run")
	}
	if strings.Contains(commandHelp["directory"], "--dry-run") {
		t.Fatal("directory advertises task dry-run")
	}
}
