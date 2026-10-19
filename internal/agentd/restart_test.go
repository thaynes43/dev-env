package agentd

import (
	"os"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// A remote Claude pod that a later boot started through the resume path has
// launch.json (the first launch, kept) beside resume.json. The drain read
// takes the conversation from launch.json and proves it once the CLI has
// written the transcript.
func TestLaunchRestartRemoteStartedByResume(t *testing.T) {
	s, sess, ws := launchFixture(t)
	sess.Mode = protocol.ModeRemote
	sess.Prompt = ""
	first, err := BuildLaunch(s, sess, ws, "boot-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := BuildResume(s, sess, ws, first, "boot-2", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Resume || !resumed.TUI || resumed.ConversationID != first.ConversationID {
		t.Fatalf("resume launch %+v", resumed)
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(resumeFile), resumed); err != nil {
		t.Fatal(err)
	}

	// Never used: the id was chosen but the CLI wrote no transcript, so
	// `claude --resume` would find nothing. The pod is not drained.
	if rep := launchRestart(s, sess.Name); rep.Resumable || rep.ConversationID != "" {
		t.Fatalf("never-used remote session proved a conversation: %+v", rep)
	}

	writeTranscript(t, s, first.ConversationID)
	rep := launchRestart(s, sess.Name)
	if !rep.Resumable || rep.ConversationID != first.ConversationID || rep.Session != sess.Name {
		t.Fatalf("used remote session: %+v", rep)
	}
}

// Without launch.json the read proves nothing, whatever resume.json says.
func TestLaunchRestartNeedsTheFirstLaunch(t *testing.T) {
	s, _, _ := launchFixture(t)
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(resumeFile), Launch{ConversationID: "conv-1"}); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, s, "conv-1")
	if rep := launchRestart(s, "s-1"); rep.Resumable {
		t.Fatalf("resume.json alone proved a conversation: %+v", rep)
	}
}
