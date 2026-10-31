package agentrun

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestAlertmanagerCLIOrdersSnapshot(t *testing.T) {
	h := newHarness(t)
	input := `[{"fingerprint":"synthetic","startsAt":"2026-10-30T11:00:00Z","labels":{"alertname":"Example"},"status":{"state":"active"}}]`
	orders := h.write("orders.json", `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"upgrade-work-orders","namespace":"upgrade-agent","resourceVersion":"12345"},"data":{"rem-responder-0123abcd":"{\"status\":\"done\",\"created\":\"1793358000\",\"updated\":\"1793358060\",\"note\":\"PRIVATE-ORDER-TEXT\"}"}}`)
	h.env.Now = func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	for _, verb := range []string{"list", "show"} {
		for _, output := range []string{"json", "html", ""} {
			h.env.Stdin = strings.NewReader(input)
			args := []string{"pages", verb, "--orders-from-file", orders, "--orders-observed-at", "2026-10-30T12:00:00Z", "--source", "alertmanager"}
			if verb == "show" {
				args = append(args, "synthetic")
			}
			if output != "" {
				args = append(args, "-o", output)
			}
			h.mustRun(ExitOK, args...)
			contains(t, "unjoined", h.stdout.String(), "Unjoined", "Unknown")
			if strings.Contains(h.stdout.String(), "PRIVATE-ORDER-TEXT") || strings.Contains(h.stdout.String(), "rem-responder-0123abcd") {
				t.Fatal("unjoined order attribution/text")
			}
		}
	}
	for _, tc := range []struct {
		file, stamp, want string
		code              int
	}{
		{orders, "2026-10-30T11:50:00Z", "Stale", ExitOK},
		{orders, "2026-10-30T11:55:00Z", "Unjoined", ExitOK},
		{orders, "2026-10-30T12:01:00Z", "Invalid", ExitOK},
		{h.write("empty-orders.json", `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"upgrade-work-orders","namespace":"upgrade-agent"},"data":{}}`), "2026-10-30T12:00:00Z", "Empty", ExitOK},
		{h.write("huge-orders.json", strings.Repeat(" ", pages.V1OrdersMaxBytes+1)), "2026-10-30T12:00:00Z", "Invalid", ExitFailed},
		{orders + "-missing", "2026-10-30T12:00:00Z", "Unavailable", ExitFailed},
		{h.write("invalid-orders.json", `broken`), "2026-10-30T12:00:00Z", "Invalid", ExitFailed},
	} {
		h.env.Stdin = strings.NewReader(input)
		h.mustRun(tc.code, "pages", "list", "--source", "alertmanager", "--orders-from-file", tc.file, "--orders-observed-at", tc.stamp, "-o", "json")
		contains(t, "condition", h.stdout.String(), tc.want)
	}
	h.env.Stdin = strings.NewReader(input)
	h.mustRun(ExitUsage, "pages", "list", "--source", "alertmanager", "--orders-from-file", orders)
	h.env.Stdin = strings.NewReader(input)
	h.mustRun(ExitOK, "pages", "list", "--source", "alertmanager", "-o", "json")
	contains(t, "default", h.stdout.String(), "Unconfigured")
}
