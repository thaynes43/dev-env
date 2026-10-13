package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHistoryTestFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryExportCommands(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	writeHistoryTestFile(t, filepath.Join(home, ".claude", "projects", "-home-dev", "t.jsonl"), "{}\n")
	writeHistoryTestFile(t, filepath.Join(home, ".codex", "sessions", "2026", "s.jsonl"), "{}\n")
	writeHistoryTestFile(t, filepath.Join(home, ".agentd", "boot.json"), "{}")
	writeHistoryTestFile(t, filepath.Join(home, "work", "s-1.log"), "log\n")
	env := map[string]string{"HOME": home, "AGENTD_GH_TOKEN_FILE": filepath.Join(base, "creds", "gh_token")}
	flags := []string{"--session", "s-1", "--claude-config-dir", ".claude", "--codex-home", ".codex"}

	code, plan, errOut := runArgs(append([]string{"ctl", "history-export", "plan"}, flags...), env)
	if code != exitOK || !strings.HasPrefix(plan, `{"bytes":`) || !strings.HasSuffix(plan, "}\n") {
		t.Fatalf("plan: %d %q %q", code, plan, errOut)
	}
	sum := sha256.Sum256([]byte(strings.TrimSuffix(plan, "\n")))
	digest := hex.EncodeToString(sum[:])

	var stdout, stderr bytes.Buffer
	args := append(append([]string{"ctl", "history-export", "stream"}, flags...), "--plan-sha256", digest)
	code = run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, func(k string) string { return env[k] }, noRunner{})
	if code != exitOK {
		t.Fatalf("stream: %d %q", code, stderr.String())
	}
	var names []string
	tr := tar.NewReader(&stdout)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "agentd/boot.json,claude/projects/-home-dev/t.jsonl,codex/sessions/2026/s.jsonl,log/s-1.log" {
		t.Errorf("stream: %v", names)
	}

	// A wrong digest: the error goes to stderr, and stdout stays empty.
	stdout.Reset()
	stderr.Reset()
	args[len(args)-1] = strings.Repeat("0", 64)
	code = run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, func(k string) string { return env[k] }, noRunner{})
	if code != exitFailure || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"error":"export_changed"`) {
		t.Errorf("stream with a wrong digest: %d %q %q", code, stdout.String(), stderr.String())
	}

	// An unknown provider directory is refused as one JSON object.
	code, out, _ := runArgs([]string{"ctl", "history-export", "plan", "--session", "s-1", "--claude-config-dir", ".claude"}, env)
	if code != exitFailure || !strings.HasPrefix(out, `{"error":"layout_unknown"`) {
		t.Errorf("plan without CODEX_HOME: %d %q", code, out)
	}

	// The archive is not mounted in this HOME.
	code, out, _ = runArgs([]string{"ctl", "history-archive", "list", "--project", "dev-env"}, env)
	if code != exitFailure || !strings.HasPrefix(out, `{"error":"no_archive"`) {
		t.Errorf("list without the archive: %d %q", code, out)
	}

	code, out, _ = runArgs([]string{"ctl", "history-archive", "manifest", "--session-uid", "6f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f", "--offset", "3", "--limit", "10"}, env)
	if code != exitFailure || !strings.HasPrefix(out, `{"error":"no_archive"`) {
		t.Errorf("manifest without the archive: %d %q", code, out)
	}

	for _, bad := range [][]string{
		{"ctl", "history-export"},
		{"ctl", "history-export", "fly"},
		{"ctl", "history-export", "plan", "extra"},
		{"ctl", "history-export", "stream", "--session", "s-1"},
		{"ctl", "history-archive", "verify", "--session-uid", "u"},
		{"ctl", "history-archive", "list"},
		{"ctl", "history-archive", "read", "--session-uid", "u", "--content-sha256", "h"},
		{"ctl", "history-archive", "put", "--project", "x"},
		{"ctl", "history-archive", "manifest", "--session-uid", "u", "--limit", "x"},
		{"ctl", "history-archive", "manifest", "--session-uid", "u", "--file", "x"},
		{"shelf", "--other"},
	} {
		if code, _, _ := runArgs(bad, env); code != exitUsage {
			t.Errorf("%v: exit %d, want usage", bad, code)
		}
	}
}
