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

func TestAlertmanagerSelectionTerminalSafety(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"pages", "list", "--source", "alertmanager", "--search", "\x1b[2J"}, ExitOK},
		{[]string{"pages", "show", "\x1b]0;bad\x07", "--source", "alertmanager"}, ExitNotFound},
	} {
		h.env.Stdin = strings.NewReader(`[]`)
		h.mustRun(tc.code, tc.args...)
		if strings.ContainsAny(h.stdout.String(), "\x1b\x07") {
			t.Fatalf("terminal control in output %q", h.stdout.String())
		}
		contains(t, "coverage", h.stdout.String(), "Available", "Current Alertmanager snapshot")
	}
}
func TestAlertmanagerDispatchRespectsKeeperValuesAndSeparator(t *testing.T) {
	h := newHarness(t)
	h.write("--source=alertmanager", pagesConfigMap(t, samplePages(t)))
	t.Chdir(h.dir)
	h.mustRun(ExitOK, "pages", "list", "--from-file", "--source=alertmanager")
	contains(t, "keeper", h.stdout.String(), "KEEPER PAGES ONLY")
	h.env.Stdin = strings.NewReader(pagesConfigMap(t, samplePages(t)))
	h.mustRun(ExitNotFound, "pages", "show", "--", "--source=alertmanager")
	contains(t, "keeper", h.stdout.String(), "KEEPER PAGES ONLY")
}

func TestAlertmanagerHelp(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"pages", "list", "--source", "alertmanager", "-h"}, {"pages", "show", "--source=alertmanager", "--help"}} {
		h.mustRun(ExitOK, args...)
		contains(t, "Alertmanager help", h.stdout.String(), "--source alertmanager", "--from-file", "--search", "-o json|html", "not notification or delivery history")
	}
	h.mustRun(ExitOK, "help", "pages")
	contains(t, "pages help", h.stdout.String(), "default source", "KEEPER PAGES ONLY", "Alertmanager snapshot source", "--source alertmanager")
}
