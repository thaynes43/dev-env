package agentd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// An access token that lapses before the keeper's next generation is visible
// is a pending refresh, not a lapsed login: the keeper refreshes ahead of
// expiry and the Secret volume trails by about a minute. Times are relative to
// the moment the applied token expired (t0); every value is synthetic.

func pendingHome(t *testing.T) (Settings, time.Time) {
	t.Helper()
	s := projHome(t, t.TempDir()+"/claude-live/access.json")
	publish(t, s.ClaudeLiveFile, projAccess(1, 10*time.Minute))
	if err := SyncClaudeAccess(s, loginNow); err != nil {
		t.Fatal(err)
	}
	return s, loginNow.Add(10 * time.Minute)
}

func TestProjectionStatusSeparatesPendingRefreshFromLapse(t *testing.T) {
	s, t0 := pendingHome(t)
	cases := []struct {
		name string
		at   time.Duration
		want string
	}{
		{"still valid", -time.Minute, "logged in on the keeper's projection"},
		{"expired a second ago", time.Second, "refreshing: the access token lapsed 1s ago"},
		{"expired 59 s ago", 59 * time.Second, "refreshing: the access token lapsed 59s ago"},
		{"expired beyond the pending window", 10 * time.Minute, "login-required: waiting for the keeper's Claude projection"},
		{"expired an hour ago", time.Hour, "login-required: waiting for the keeper's Claude projection"},
	}
	for _, tc := range cases {
		if got := projectionLoginStatus(s, t0.Add(tc.at)); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
	// No credentials at all is still a login-required wait, never refreshing.
	empty := projHome(t, t.TempDir()+"/claude-live/access.json")
	empty.ClaudeConfigDir = t.TempDir()
	if got := projectionLoginStatus(empty, t0); !strings.HasPrefix(got, "login-required:") {
		t.Errorf("no credentials: %q", got)
	}
}

// The unstarted remote session reports the pending refresh in its status text,
// keeps the wire state, and recovers on the generation that lands.
func TestPendingRefreshReachesTheStatusAndClearsOnTheNextGeneration(t *testing.T) {
	s, t0 := pendingHome(t)
	s.TmuxBin = "tmux"
	sess := remoteSession()
	r := &fakeRunner{handle: func(c Cmd) (Result, error) { return Result{}, &CmdError{Name: "tmux", ExitCode: 1} }}
	_ = writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady})
	at := t0.Add(59 * time.Second)
	st := CollectStatus(context.Background(), r, s, sess, at)
	if st.Agent.State != protocol.AgentLoginRequired || !strings.HasPrefix(st.Agent.Error, "refreshing:") {
		t.Fatalf("pending: %+v", st.Agent)
	}
	if strings.Contains(st.Agent.Error, "agent-run login") {
		t.Errorf("a pending refresh offers a login: %q", st.Agent.Error)
	}
	publish(t, s.ClaudeLiveFile, projAccess(2, 9*time.Hour))
	if err := SyncClaudeAccess(s, loginNow); err != nil {
		t.Fatal(err)
	}
	if got := projectionLoginStatus(s, at.Add(time.Second)); !strings.HasPrefix(got, "logged in on the keeper's projection") {
		t.Errorf("after generation 2: %q", got)
	}
}
