package agentrun

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/pages"
)

func TestAlertmanagerCLI(t *testing.T) {
	h := newHarness(t)
	input := `{"observedAt":"2026-10-28T00:00:00Z","source":"test","alerts":[{"fingerprint":"f1","labels":{"alertname":"Example"},"status":{"state":"suppressed"}}]}`
	h.env.Stdin = strings.NewReader(input)
	h.mustRun(ExitOK, "pages", "list", "--source", "alertmanager", "--search", "example", "-o", "json")
	var s pages.AlertmanagerSnapshot
	if err := json.Unmarshal(h.stdout.Bytes(), &s); err != nil || len(s.Alerts) != 1 || s.Alerts[0].Status != "suppressed" {
		t.Fatal(s, err)
	}
	h.env.Stdin = strings.NewReader(input)
	h.mustRun(ExitOK, "pages", "show", "f1", "--source=alertmanager", "-o", "html")
	contains(t, "html", h.stdout.String(), "Example", "suppressed", "Unknown")
	h.env.Stdin = strings.NewReader(input)
	h.mustRun(ExitNotFound, "pages", "show", "missing", "--source", "alertmanager")
	path := h.write("snapshot.json", input)
	h.mustRun(ExitOK, "pages", "list", "--source", "alertmanager", "--from-file", path, "--status", "suppressed")
	contains(t, "file", h.stdout.String(), "Example")
	h.mustRun(ExitFailed, "pages", "list", "--source", "alertmanager", "--from-file", path+"-missing")
	contains(t, "missing", h.stdout.String(), "Unavailable")
	h.env.Stdin = strings.NewReader("broken")
	h.mustRun(ExitFailed, "pages", "list", "--source", "alertmanager", "-o", "json")
	contains(t, "failure", h.stdout.String(), "Unavailable")
}
