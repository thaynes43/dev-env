package agentd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func noticeLine(child string) string {
	return "[dev-env notice] a note is waiting from " + child + ": agent-run parent read message." +
		strings.Repeat("a", 32) + "." + strings.Repeat("b", 32)
}

// A Claude TUI receives the fixed line and Enter, with no message envelope
// and no note text. Codex and a headless task get nothing.
func TestDeliverNoticeTypesOnlyTheFixedLine(t *testing.T) {
	const noteText = "NOTE-TEXT-MUST-NOT-BE-TYPED"
	line := noticeLine("child-1")
	if strings.Contains(line, noteText) || strings.Contains(line, "question") || strings.Contains(line, "secret-key") {
		t.Fatal("fixture line is not fixed")
	}
	s, f, stdin := deliverRig(t, true)
	sess := protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}
	if err := DeliverNotice(context.Background(), f, s, sess, line+"\n"); err != nil {
		t.Fatal(err)
	}
	got := f.lines()
	if len(got) != 3 || !strings.HasPrefix(got[0], "tmux load-buffer -b agentd-notice-") || !strings.HasSuffix(got[0], " -") ||
		got[1] != "tmux paste-buffer -b "+strings.Fields(got[0])[3]+" -d -p -t =agent:" ||
		got[2] != "tmux send-keys -t =agent: Enter" {
		t.Fatalf("commands:\n%s", strings.Join(got, "\n"))
	}
	if stdin["tmux load-buffer"] != line || strings.Contains(stdin["tmux load-buffer"], "[Message from") || strings.Contains(stdin["tmux load-buffer"], noteText) {
		t.Fatalf("typed %q", stdin["tmux load-buffer"])
	}
	dotted := noticeLine("dev.child")
	if err := DeliverNotice(context.Background(), f, s, sess, dotted); err != nil {
		t.Fatal(err)
	}
	if stdin["tmux load-buffer"] != dotted {
		t.Fatalf("dotted child typed %q", stdin["tmux load-buffer"])
	}
}

func TestDeliverNoticeRefusesCodexHeadlessAndNoteText(t *testing.T) {
	line := noticeLine("child-1")
	s, f, _ := deliverRig(t, true)
	if err := DeliverNotice(context.Background(), f, s, protocol.Session{Name: "parent-1", Agent: protocol.AgentCodex}, line); !errors.Is(err, ErrNotAddressable) {
		t.Fatalf("codex: %v", err)
	}
	if len(f.lines()) != 0 {
		t.Fatalf("codex was typed into: %q", f.lines())
	}
	s, f, _ = deliverRig(t, false)
	if err := DeliverNotice(context.Background(), f, s, protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}, line); !errors.Is(err, ErrNotAddressable) || !strings.Contains(err.Error(), "headless") && !strings.Contains(err.Error(), "Claude TUI") {
		t.Fatalf("headless: %v", err)
	}
	if len(f.lines()) != 0 {
		t.Fatalf("headless was typed into: %q", f.lines())
	}
	s, f, _ = deliverRig(t, true)
	for _, bad := range []string{
		"NOTE-TEXT-MUST-NOT-BE-TYPED",
		line + " and the note text",
		"[dev-env notice] a note is waiting from child-1: agent-run parent read message.short",
		"",
	} {
		if err := DeliverNotice(context.Background(), f, s, protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}, bad); err == nil || errors.Is(err, ErrNotAddressable) {
			t.Fatalf("line %q: %v", bad, err)
		}
	}
	if len(f.lines()) != 0 {
		t.Fatalf("a bad line was typed: %q", f.lines())
	}
}
