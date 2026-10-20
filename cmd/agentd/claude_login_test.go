package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/claudelogin"
)

// A pod log, a pipe or a plain file is not an authenticated terminal: the
// helper refuses before it looks for the CLI or the keeper, with a fixed code.
func TestClaudeLoginRefusesWithoutATerminal(t *testing.T) {
	out, err := os.Create(filepath.Join(t.TempDir(), "result"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	if claudeLogin(context.Background(), nil, in, out) != exitFailure {
		t.Fatal("noninteractive ceremony accepted")
	}
	raw, _ := os.ReadFile(out.Name())
	var got struct {
		OK   bool
		Code string
	}
	if json.Unmarshal(raw, &got) != nil || got.OK || got.Code != "AuthenticatedTerminalRequired" {
		t.Fatalf("no fixed refusal: %q", raw)
	}
	// Non-file readers (a pipe wrapper) and stray arguments are refused too.
	var buf bytes.Buffer
	if claudeLogin(context.Background(), nil, strings.NewReader(""), &buf) != exitFailure || !strings.Contains(buf.String(), "AuthenticatedTerminalRequired") {
		t.Fatal("non-file stdin accepted")
	}
	buf.Reset()
	if claudeLogin(context.Background(), []string{"extra"}, in, out) != exitFailure {
		t.Fatal("extra argument accepted")
	}
}

func TestClaudeLoginIdleWaitsForShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- claudeLoginIdle(ctx, nil, &bytes.Buffer{}) }()
	select {
	case <-done:
		t.Fatal("idle returned before shutdown")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if code := <-done; code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if claudeLoginIdle(context.Background(), []string{"x"}, &bytes.Buffer{}) != exitUsage {
		t.Fatal("arguments accepted")
	}
}

func TestClaudeLoginStatusWithoutAKeeperIsFixedUnavailable(t *testing.T) {
	var buf bytes.Buffer
	if claudeLoginStatus(context.Background(), []string{"--login-dir", t.TempDir()}, &buf) != exitFailure || !strings.Contains(buf.String(), `"code":"Unavailable"`) {
		t.Fatalf("got %q", buf.String())
	}
	buf.Reset()
	if claudeLoginStatus(context.Background(), []string{"extra"}, &buf) != exitFailure || !strings.Contains(buf.String(), `"code":"InvalidRequest"`) {
		t.Fatalf("got %q", buf.String())
	}
}

// NB-01b: claude-login-abandon takes no arguments, needs no terminal, prints one
// fixed word and succeeds when no helper runs (this test process is not one).
func TestClaudeLoginAbandonWithNoHelper(t *testing.T) {
	var out bytes.Buffer
	if code := claudeLoginAbandon(context.Background(), nil, &out); code != exitOK {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	var r claudelogin.Response
	if json.Unmarshal(out.Bytes(), &r) != nil || !r.OK || r.Code != claudelogin.AbandonNone {
		t.Fatalf("output %q", out.String())
	}
	out.Reset()
	if code := claudeLoginAbandon(context.Background(), []string{"--x"}, &out); code != exitFailure || !strings.Contains(out.String(), "InvalidRequest") {
		t.Fatalf("arguments: exit %d %q", code, out.String())
	}
}
