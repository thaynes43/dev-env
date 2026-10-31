package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Real agentrun.Run over TLS into the inbox handlers: a child asks its
// recorded parent, the parent replies, the child acknowledges, and each side
// sees the derived state. No provider, prompt or live Session is involved.
func TestInboxCLIAskReplyAckRoundTrip(t *testing.T) {
	f, leadTok, childTok, _ := askReplyFixture(t)
	server := httptest.NewTLSServer(f.h)
	defer server.Close()
	dir := t.TempDir()
	caPath, tokenPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "token")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(token, stdin string, want int, args ...string) (string, string) {
		t.Helper()
		if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		env := agentrun.DefaultEnv()
		env.Stdout, env.Stderr, env.Stdin = &stdout, &stderr, strings.NewReader(stdin)
		env.Getenv = func(string) string { return "" }
		env.SessionTokenFile, env.ServiceAccountDir = filepath.Join(dir, "absent-session"), filepath.Join(dir, "absent-sa")
		env.LookPath = func(string) (string, error) { return "", errors.New("synthetic: no host command") }
		env.Run = func(context.Context, []string, io.Reader) (int, error) {
			return 0, errors.New("synthetic host process refused")
		}
		env.Sleep = func(context.Context, time.Duration) error {
			t.Errorf("inbox client retried")
			return errors.New("synthetic retry refused")
		}
		args = append(append([]string{}, args...), "--api-url", server.URL, "--token-file", tokenPath, "--ca-file", caPath)
		if code := agentrun.Run(context.Background(), args, env); code != want {
			t.Fatalf("%v: exit %d want %d\nstdout %s\nstderr %s", args[:len(args)-6], code, want, stdout.String(), stderr.String())
		}
		return stdout.String(), stderr.String()
	}
	jsonOut := func(out string, v any) {
		t.Helper()
		if err := json.Unmarshal([]byte(out), v); err != nil {
			t.Fatal(err, out)
		}
	}
	stateOf := func(token, name string, args ...string) apiv1.InboxThread {
		t.Helper()
		out, _ := run(token, "", agentrun.ExitOK, append([]string{"inbox", "list", "--project", "sample"}, args...)...)
		var list apiv1.InboxList
		jsonOut(out, &list)
		item := itemNamed(t, list, name)
		if item.Thread == nil {
			t.Fatal("no thread state", out)
		}
		return *item.Thread
	}

	out, _ := run(childTok, "Which base branch should I use?\n", agentrun.ExitOK, "inbox", "ask", "--project", "sample", "--key", "q-1")
	var asked apiv1.InboxResult
	jsonOut(out, &asked)
	q := asked.Message
	if asked.Outcome != "Created" || q.Recipient != owner(f, "lead") || q.Kind != apiv1.InboxKindQuestion {
		t.Fatal("ask did not reach the recorded parent", out)
	}
	run(childTok, "Which base branch should I use?\n", agentrun.ExitOK, "inbox", "ask", "--project", "sample", "--key", "q-1")
	run(childTok, "Changed question\n", agentrun.ExitFailed, "inbox", "ask", "--project", "sample", "--key", "q-1")
	if got := stateOf(leadTok, q.Name); got.State != apiv1.InboxStateUnanswered || got.WaitingOn.Name != "lead" {
		t.Fatal("coordinator view", got)
	}
	if got := stateOf(childTok, q.Name, "--sent"); got.State != apiv1.InboxStateUnanswered {
		t.Fatal("asker view", got)
	}
	text, stderr := run(leadTok, "", agentrun.ExitOK, "inbox", "read", q.Name, "--project", "sample")
	if text != "Which base branch should I use?\n" || !strings.Contains(stderr, "state=Unanswered") {
		t.Fatal("coordinator read", text, stderr)
	}

	out, _ = run(leadTok, "Use main.\n", agentrun.ExitOK, "inbox", "reply", q.Name, "--project", "sample", "--key", "r-1")
	var replied apiv1.InboxResult
	jsonOut(out, &replied)
	r := replied.Message
	if r.Recipient != q.Sender || r.InReplyTo == nil || r.InReplyTo.UID != q.UID {
		t.Fatal("reply did not bind", out)
	}
	run(leadTok, "Use stable.\n", agentrun.ExitFailed, "inbox", "reply", q.Name, "--project", "sample", "--key", "r-2")
	if got := stateOf(childTok, r.Name); got.State != apiv1.InboxStateAnswered || got.WaitingOn.Name != "child" {
		t.Fatal("asker inbox view", got)
	}
	run(childTok, "", agentrun.ExitOK, "inbox", "ack", r.Name, "--project", "sample")
	if got := stateOf(childTok, q.Name, "--sent"); got.State != apiv1.InboxStateAcknowledged || got.Reply.Name != r.Name {
		t.Fatal("acknowledged", got)
	}
	if got := stateOf(leadTok, q.Name); got.State != apiv1.InboxStateAcknowledged {
		t.Fatal("coordinator sees the ack", got)
	}
	if n := countMessages(t, f); n != 2 {
		t.Fatal("messages", n)
	}
}
