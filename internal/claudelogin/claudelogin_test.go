package claudelogin

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodLink = "https://claude.com/cai/oauth/authorize?client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&state=s&code_challenge=c"

func TestValidLink(t *testing.T) {
	for link, want := range map[string]bool{
		goodLink: true,
		"https://claude.ai/oauth/authorize?client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&state=s&code_challenge=c":                   true,
		"http://claude.com/cai/oauth/authorize?client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&state=s&code_challenge=c":               false,
		"https://evil.example/cai/oauth/authorize?client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&state=s&code_challenge=c":            false,
		"https://claude.com.evil.example/cai/oauth/authorize?client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&state=s&code_challenge=c": false,
		"https://user@claude.com/cai/oauth/authorize?client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&state=s&code_challenge=c":         false,
		"https://claude.com/other?client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&state=s&code_challenge=c":                            false,
		"https://claude.com/cai/oauth/authorize?client_id=another&state=s&code_challenge=c":                                           false,
		"https://claude.com/cai/oauth/authorize?client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&code_challenge=c":                      false,
		goodLink + "#fragment": false,
		"":                     false,
	} {
		if validLink(link) != want {
			t.Errorf("validLink(%q) = %v, want %v", link, !want, want)
		}
	}
}

func TestPromptWriterForwardsOnlyTheFirstValidLink(t *testing.T) {
	var out bytes.Buffer
	w := &promptWriter{Out: &out, Challenge: make(chan struct{})}
	noise := "token=SECRET_SYNTHETIC_CANARY error body\n"
	// A split write, ANSI color and a foreign link before the real one.
	for _, chunk := range []string{noise, "visit https://evil.example/x\n", "\x1b[1mOpen: ", goodLink[:40], goodLink[40:] + "\x1b[0m\n", "again " + goodLink + "\n", noise} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var got map[string]string
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &got) != nil || got["signInUrl"] != goodLink || strings.Contains(out.String(), "SECRET") {
		t.Fatalf("output is not exactly one validated link: %q", out.String())
	}
	select {
	case <-w.Challenge:
	default:
		t.Fatal("challenge not signalled")
	}
	if !w.presented() {
		t.Fatal("presented flag not set")
	}
}

func TestEnvironmentInheritsNothing(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "SECRET_SYNTHETIC_CANARY")
	t.Setenv("ANTHROPIC_API_KEY", "SECRET_SYNTHETIC_CANARY")
	env := strings.Join(Environment("/home/x", "/home/x/tmp"), "\n")
	if strings.Contains(env, "SECRET") || strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN") || strings.Contains(env, "ANTHROPIC_API_KEY") {
		t.Fatal("a credential reached the CLI environment")
	}
	for _, want := range []string{"HOME=/home/x\n", "CLAUDE_CONFIG_DIR=/home/x/.claude\n", "TMPDIR=/home/x/tmp\n"} {
		if !strings.Contains(env+"\n", want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestControlRefusesUnknownCodesAndOversizeAnswers(t *testing.T) {
	dir, err := os.MkdirTemp("", "cc")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	l, err := net.Listen("unix", filepath.Join(dir, ControlSocket))
	if err != nil {
		t.Fatal(err)
	}
	answers := make(chan string, 4)
	s := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(<-answers)) })}
	go func() { _ = s.Serve(l) }()
	defer func() { _ = s.Close() }()
	for answer, ok := range map[string]bool{
		`{"ok":true,"code":"Started","attemptUID":"0a1b2c3d-0000-4000-8000-0123456789ab"}`: true,
		`{"ok":true,"code":"Adopted","generation":3}`:                                      true,
		`{"ok":true,"code":"Adopted"}`:                                                     false, // no generation
		`{"ok":false,"code":"SomethingElse"}`:                                              false,
		`{"ok":false,"code":"Busy","token":"x"}`:                                           false, // unknown member
		`{"ok":false,"code":"Busy","attemptUID":"../x"}`:                                   false,
		`{"ok":false,"code":"Busy","pad":"` + strings.Repeat("x", 2000) + `"}`:             false,
	} {
		answers <- answer
		_, err := Control(context.Background(), dir, "begin", "")
		if (err == nil) != ok {
			t.Errorf("answer %.60q: err=%v, want ok=%v", answer, err, ok)
		}
	}
}

func TestHelperRefusesAnUnsupportedCLIBeforeAnyControlCall(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'something else 1.0'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	res, err := Helper(context.Background(), dir, Options{Bin: bin}, strings.NewReader(""), &out)
	if err == nil || res.Code != "Unavailable" || res.Login == nil || res.Login.FailureCode != "UnsupportedCLI" || out.Len() != 0 {
		t.Fatalf("got %+v %v", res, err)
	}
}
