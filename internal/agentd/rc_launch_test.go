package agentd

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestRCRemoteLaunchFreshConversationAndProjectRules(t *testing.T) {
	s, sess, ws, now, _ := ruleInputLaunchFixture(t, protocol.AgentClaude, false)
	sess.Mode, sess.Prompt, sess.Limits = protocol.ModeRemote, "", nil
	s.Getenv = envOf(nil)
	first, err := BuildLaunch(s, sess, ws, "first", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildLaunch(s, sess, ws, "second", now)
	if err != nil {
		t.Fatal(err)
	}
	if first.ConversationID == second.ConversationID {
		t.Fatal("fresh launch reused a conversation")
	}
	for _, l := range []Launch{first, second} {
		if len(l.ConversationID) != 36 || l.ConversationID[14] != '4' || !l.TUI || l.Resume {
			t.Fatal("not a fresh remote launch")
		}
		for flag, value := range map[string]string{"--session-id": l.ConversationID, "--remote-control": sess.Name} {
			i := slices.Index(l.Argv, flag)
			if i < 0 || i+1 == len(l.Argv) || l.Argv[i+1] != value {
				t.Fatal("missing remote conversation argument", flag)
			}
		}
		i := slices.Index(l.Argv, "--append-system-prompt-file")
		if i < 0 || i+1 == len(l.Argv) {
			t.Fatal("project rules file argument absent")
		}
		rules, err := os.ReadFile(l.Argv[i+1])
		if err != nil || !strings.Contains(string(rules), "PROJECT_RULE_IDENTIFIER") {
			t.Fatal("launch did not carry the accepted rules", err)
		}
		argv := strings.Join(l.Argv, " ")
		if !strings.Contains(argv, "--append-system-prompt") || !slices.Contains(l.Argv, "--append-system-prompt-file") || slices.Contains(l.Argv, "--resume") || slices.Contains(l.Argv, "-p") {
			t.Fatal("missing project rules or fresh interactive launch")
		}
	}
}
