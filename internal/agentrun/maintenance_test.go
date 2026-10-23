package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaintenanceCheck(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 10, 23, 5, 1, 0, 0, time.UTC)
	h.env.Now = func() time.Time { return h.now }
	refuseExternal(t, h)
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "examples", "upgrade-shepherd", "claude-haynes-ops.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := h.write("check.json", string(body))
	h.mustRun(ExitOK, "maintenance", "check", "--input", path)
	var res map[string]any
	if err := json.Unmarshal(h.stdout.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res["decision"] != "candidate" || res["retry"] != false || res["authorizationGranted"] != false || res["candidateIsOperationalDeferral"] != false || res["unverifiedInput"] != true {
		t.Fatalf("result %s", h.stdout.String())
	}
	if !strings.Contains(h.stdout.String(), "not alert deferral") || h.stderr.Len() != 0 {
		t.Fatalf("stdout %s stderr %s", h.stdout.String(), h.stderr.String())
	}
	if n := len(h.api.requests()); n != 0 || len(h.ran) != 0 {
		t.Fatalf("requests %d commands %v", n, h.ran)
	}

	h.env.Stdin = bytes.NewReader(body)
	h.mustRun(ExitOK, "maintenance", "check", "--input", "-", "--now", "2026-10-23T05:01:00Z")
	if !strings.Contains(h.stdout.String(), `"decision":"candidate"`) {
		t.Fatalf("stdin %s", h.stdout.String())
	}

	h.env.Stdin = strings.NewReader(`{"authorized":true}`)
	h.mustRun(ExitUsage, "maintenance", "check", "--input", "-")
	if !strings.Contains(h.stdout.String(), `"error":"invalid-input"`) || !strings.Contains(h.stdout.String(), "cannot grant authority") || strings.Contains(h.stdout.String(), `"decision"`) {
		t.Fatalf("invalid %s", h.stdout.String())
	}
	if !strings.Contains(h.stderr.String(), "invalid maintenance input") {
		t.Fatalf("stderr %s", h.stderr.String())
	}

	h.mustRun(ExitUsage, "maintenance", "check", "--api-url", "https://example.test")
	if !strings.Contains(h.stderr.String(), "flag provided but not defined") {
		t.Fatalf("api flag %s", h.stderr.String())
	}
	h.mustRun(ExitOK, "maintenance", "-h")
	if !strings.Contains(h.stdout.String(), "Usage: agent-run maintenance check") || len(h.api.requests()) != 0 {
		t.Fatalf("help %s requests %d", h.stdout.String(), len(h.api.requests()))
	}
}

func refuseExternal(t *testing.T, h *harness) {
	t.Helper()
	h.env.Getenv = func(key string) string {
		t.Fatalf("read environment %s", key)
		return ""
	}
	h.env.LookPath = func(name string) (string, error) {
		t.Fatalf("looked up %s", name)
		return "", errors.New("not found")
	}
	h.env.Run = func(_ context.Context, argv []string, _ io.Reader) (int, error) {
		t.Fatalf("ran %v", argv)
		return 0, errors.New("ran")
	}
	h.env.Output = func(_ context.Context, argv []string) ([]byte, error) {
		t.Fatalf("output %v", argv)
		return nil, errors.New("output")
	}
	h.env.ExecOutput = func(_ context.Context, argv []string) (int, string, string, error) {
		t.Fatalf("exec %v", argv)
		return 0, "", "", errors.New("exec")
	}
	h.env.PortForward = func(_ context.Context, argv []string) (string, func(), error) {
		t.Fatalf("forward %v", argv)
		return "", func() {}, errors.New("forward")
	}
}
