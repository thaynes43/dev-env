package agentd

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCollectStatusStates(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	ws := &protocol.Workspace{Clone: "/c", Worktree: "/w", Branch: "agent/s"}
	git := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if strings.Contains(strings.Join(c.Args, " "), "--abbrev-ref") {
			return Result{Stdout: []byte("agent/s\n")}, nil
		}
		return Result{Stdout: []byte("abc123\n")}, nil
	}}
	me := agentPid{Pid: os.Getpid(), Start: procStartTime(os.Getpid())}

	cases := []struct {
		name  string
		setup func(s Settings)
		check func(t *testing.T, st protocol.Status)
	}{
		{"no boot yet", func(Settings) {}, func(t *testing.T, st protocol.Status) {
			if st.Boot != protocol.BootBooting || st.Agent.State != protocol.AgentPending || st.Workspace != nil {
				t.Errorf("%+v", st)
			}
		}},
		{"boot failed", func(s Settings) {
			_ = writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootFailed, AgentError: "plan credential unavailable",
				Steps: []Step{{Name: "git", State: StepWarn}, {Name: "mcp", State: StepOK}, {Name: "agent", State: StepFail}}})
		}, func(t *testing.T, st protocol.Status) {
			if st.Agent.State != protocol.AgentFailed || st.Agent.Error != "plan credential unavailable" || len(st.Problems) != 2 {
				t.Errorf("%+v", st)
			}
		}},
		{"busy", func(s Settings) {
			_ = writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady, Workspace: ws})
			_ = writeJSONFile(s.statePath(launchFile), Launch{BootID: "b1", ConversationID: "conv", CreatedAt: now.Add(-time.Hour), LogPath: s.LogPath("s")})
			_ = writeJSONFile(s.statePath(pidFile), me)
			writeFile(t, s.LogPath("s"), "log")
		}, func(t *testing.T, st protocol.Status) {
			if st.Agent.State != protocol.AgentBusy || st.Agent.ConversationID != "conv" || st.Agent.LastActivity == nil {
				t.Errorf("%+v", st.Agent)
			}
			if st.Workspace == nil || st.Workspace.Head != "abc123" || st.Workspace.Branch != "agent/s" {
				t.Errorf("workspace %+v", st.Workspace)
			}
		}},
		{"starting", func(s Settings) {
			_ = writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady})
			_ = writeJSONFile(s.statePath(launchFile), Launch{BootID: "b1", CreatedAt: now.Add(-10 * time.Second)})
		}, func(t *testing.T, st protocol.Status) {
			if st.Agent.State != protocol.AgentBusy {
				t.Errorf("%+v", st.Agent)
			}
		}},
		{"interrupted", func(s Settings) {
			_ = writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b2", Boot: protocol.BootReady})
			_ = writeJSONFile(s.statePath(launchFile), Launch{BootID: "b1", CreatedAt: now.Add(-10 * time.Second)})
			_ = writeJSONFile(s.statePath(pidFile), agentPid{Pid: me.Pid, Start: "1"}) // a reused pid
		}, func(t *testing.T, st protocol.Status) {
			if st.Agent.State != protocol.AgentInterrupted || !strings.Contains(st.Agent.Error, "D-42") {
				t.Errorf("%+v", st.Agent)
			}
		}},
		{"exited", func(s Settings) {
			_ = writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady})
			_ = writeJSONFile(s.statePath(launchFile), Launch{BootID: "b1"})
			_ = writeJSONFile(s.statePath(resultFile), taskResult{TaskResult: protocol.TaskResult{ExitCode: 0, Subtype: "success"}, ConversationID: "conv", Usage: &protocol.Usage{CostUSD: 1.5}})
		}, func(t *testing.T, st protocol.Status) {
			if st.Agent.State != protocol.AgentExited || st.Agent.Task == nil || st.Agent.Task.Subtype != "success" || st.Usage == nil || st.Usage.CostUSD != 1.5 {
				t.Errorf("%+v %+v", st.Agent, st.Usage)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testSettings(t, t.TempDir())
			if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			tc.setup(s)
			st := CollectStatus(context.Background(), git, s, "s", now)
			if st.Session != "s" || !strings.HasPrefix(st.Agentd, "agentd ") || !st.ObservedAt.Equal(now) {
				t.Errorf("header %+v", st)
			}
			tc.check(t, st)
		})
	}
}

func TestSharedStatusPendingRequiresExactUnlaunchedReceipt(t *testing.T) {
	now := rescueNow.Add(time.Hour)
	s, sess := sharedSettings(t, testSettings(t, t.TempDir()), "task-a")
	owner := taskOwner{Version: 1, Workspace: s.WorkspaceID, Task: sess.Name, Repo: sess.Repo,
		Clone: s.ClonePath(sess.Repo), Worktree: s.WorktreePath(sess.Name),
		SessionUID: sess.Workspace.SessionUID, PodUID: s.PodUID, Generation: 1, State: "owned", UpdatedAt: now.Add(-2 * time.Minute)}
	launch := Launch{Session: sess.Name, Dir: owner.Worktree, BootID: "b1", TUI: true, CreatedAt: owner.UpdatedAt, WorkspaceOwner: &owner}
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(launchFile), launch); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		edit  func(*taskOwner)
		at    time.Time
		state string
	}{
		{"queued admission", func(*taskOwner) {}, now, protocol.AgentPending},
		{"changed generation", func(o *taskOwner) { o.Generation++ }, now, protocol.AgentInterrupted},
		{"already admitted", func(o *taskOwner) { o.Launched = true }, now, protocol.AgentInterrupted},
		{"foreign Pod", func(o *taskOwner) { o.PodUID = "another-pod" }, now, protocol.AgentInterrupted},
		{"expired queue", func(*taskOwner) {}, launch.CreatedAt.Add(sharedGitAdminBudget + startWindow), protocol.AgentInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := owner
			tc.edit(&current)
			if err := writeWorkspaceJSON(s.ownerPath(sess.Name), current); err != nil {
				t.Fatal(err)
			}
			st := CollectStatus(context.Background(), &fakeRunner{}, s, sess.Name, tc.at)
			if st.Agent.State != tc.state {
				t.Fatalf("state %q, want %q: %+v", st.Agent.State, tc.state, st.Agent)
			}
		})
	}
}
