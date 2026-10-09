package agentrun

import (
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestProjectTaskTransportsOnlySelectionAndPreservesPromptAndKey(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--project", "dev-env", "--repo", "dev-env", "-p", "Exact task\nKeep the newline.", "--idempotency-key", "project-retry", "--wait", "0"},
		{"--project", "single-repo", "-p", "Exact task\nKeep the newline.", "--idempotency-key", "project-retry", "--wait", "0"},
		{"dev-env", "-p", "Exact task\nKeep the newline.", "--project", "dev-env", "--base", "origin/main", "--idempotency-key", "project-retry", "--wait", "0"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t)
			serveCreate(h, runningOn(testSession(name, ""), "worker-a"))
			h.mustRun(ExitOK, args...)
			requests := h.api.requests()
			if len(requests) != 1 {
				t.Fatalf("project create made %d requests instead of one", len(requests))
			}
			got := decodeCreate(t, requests[0].body)
			if got.Project == "" || got.Prompt != "Exact task\nKeep the newline." || got.IdempotencyKey != "project-retry" || got.Mode != "task" {
				t.Fatalf("selection changed task identity/input: %+v", got)
			}
			if got.Project == "single-repo" && (got.Repo != "" || got.Base != "") {
				t.Fatal("client invented a single-project repository/default")
			}
			if got.Project == "dev-env" && got.Repo != "dev-env" {
				t.Fatal("explicit selected repository disappeared")
			}
			if args[0] == "dev-env" && got.Base != "origin/main" {
				t.Fatal("explicit base was not transported exactly")
			}
			for _, forbidden := range []string{"rules", "revision", "repositories", "snapshot"} {
				if strings.Contains(string(requests[0].body), forbidden) {
					t.Fatalf("client sent project authority %q", forbidden)
				}
			}
		})
	}
}

func TestProjectSelectionUsageRefusalsSendNoRequest(t *testing.T) {
	for _, args := range [][]string{
		{"--project", "", "--repo", "dev-env", "-p", "task"},
		{"--project", "../dev-env", "--repo", "dev-env", "-p", "task"},
		{"--project", "Dev-Env", "--repo", "dev-env", "-p", "task"},
		{"--project", strings.Repeat("a", 64), "--repo", "dev-env", "-p", "task"},
		{"--project", "dev-env", "--local", "--repo", "dev-env"},
		{"--project", "dev-env", "--interactive", "--repo", "dev-env"},
		{"rescue", "restore", "dev-env-1006-120000/20261009-1200", "--project", "dev-env", "-p", "task"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t)
			h.mustRun(ExitUsage, args...)
			if len(h.api.requests()) != 0 {
				t.Fatal("incompatible project selection reached management")
			}
		})
	}
}

func TestProjectCatalogRefusalsRemainServerAuthoritative(t *testing.T) {
	for _, args := range [][]string{
		{"--project", "multi-repo", "-p", "task", "--wait", "0"},
		{"--project", "dev-env", "--repo", "dev-env", "--base", "origin/other", "-p", "task", "--wait", "0"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
				if r.Method != http.MethodPost || r.URL.Path != apiv1.SessionsPath {
					t.Fatal("project refusal sent an unrelated request")
				}
				apiError(w, http.StatusUnprocessableEntity, apiv1.CodeInvalid, "project selection does not match the accepted catalog")
			}
			h.mustRun(ExitFailed, args...)
			if len(h.api.requests()) != 1 {
				t.Fatal("client retried a catalog refusal")
			}
			contains(t, "stderr", h.stderr.String(), "accepted catalog")
		})
	}
}
