package agentd

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// R5-F1 / coordinator R8: losing the login during a held launch does not
// refuse it. The one start stays free until login returns or the deadline wins.
func TestReview5HeldLoginLostPrerequisite(t *testing.T) {
	s, sess, ws := summonedFixture(t, protocol.ModeRemote)
	s.StartDeadline = loginNow.Add(10 * time.Minute).Format(time.RFC3339)
	// Only this test's synthetic login is written and read.
	writeCredentials(t, s, loginNow.Add(time.Minute).UnixMilli())
	tm := &loginTmux{window: true, pane: loginPaneWithURL}
	r := &fakeRunner{handle: tm.handle}
	now := loginNow
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "first", Boot: protocol.BootReady, Steps: []Step{{Name: "agent", State: StepOK}}}); err != nil {
		t.Fatal(err)
	}
	d := quietDaemon(s, sess, r, now)
	d.Now = func() time.Time { return now }
	release, err := acquireLoginLock(s)
	if err != nil {
		t.Fatal(err)
	}
	step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, "first")
	release()
	if step.State != StepOK || why != "" || d.remote == nil || !d.remote.deferred || tm.agent {
		t.Fatal("not held for login")
	}
	starts := func() int {
		n := 0
		for _, c := range r.calls {
			if len(c.Args) > 3 && c.Args[0] == "new-session" && c.Args[3] == TmuxSession {
				n++
			}
		}
		return n
	}
	assertFree := func() {
		t.Helper()
		if starts() != 0 || summonedLaunched(s, sess) {
			t.Fatalf("held launch: starts=%d consumed=%t", starts(), summonedLaunched(s, sess))
		}
		for _, file := range []string{launchFile, startHandoffFile, resultFile, resumeFile} {
			if _, err := os.Lstat(s.statePath(file)); !os.IsNotExist(err) {
				t.Fatalf("held launch left %s: %v", file, err)
			}
		}
	}
	assertFree()
	now = loginNow.Add(2 * time.Minute)
	tm.pane += "Login failed.\nLOGIN_EXIT=1\n"
	for poll := 0; poll < 2; poll++ {
		d.pollRemoteLogin(context.Background())
		st := CollectStatus(context.Background(), &fakeRunner{}, s, sess, now)
		if d.remote == nil || !d.remote.deferred || st.Agent.State != protocol.AgentLoginRequired || st.Brief != protocol.BriefPending {
			t.Fatalf("poll %d: held=%t agent=%s brief=%s", poll, d.remote != nil, st.Agent.State, st.Brief)
		}
		assertFree()
	}
	// Restore only the synthetic login, without advancing the fake clock.
	writeCredentials(t, s, loginNow.Add(time.Hour).UnixMilli())
	d.pollRemoteLogin(context.Background())
	if starts() != 1 || !summonedLaunched(s, sess) {
		t.Fatalf("login restored: starts=%d consumed=%t", starts(), summonedLaunched(s, sess))
	}
	var first Launch
	if err := readJSONFile(s.statePath(launchFile), &first); err != nil {
		t.Fatal(err)
	}
	if first.Argv[len(first.Argv)-1] != sess.Prompt || summonedHandoff(s, first).State != handoffStarted {
		t.Fatal("restored login did not hand off the brief")
	}
	d.pollRemoteLogin(context.Background())
	if starts() != 1 {
		t.Fatalf("later poll started again: starts=%d", starts())
	}
	for _, file := range []string{resultFile, resumeFile} {
		if _, err := os.Lstat(s.statePath(file)); !os.IsNotExist(err) {
			t.Fatalf("later poll left %s: %v", file, err)
		}
	}
}
