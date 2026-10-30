package agentd

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCollectStatusBootingDefersLoginAndPreviousLaunch(t *testing.T) {
	for _, agent := range []string{protocol.AgentClaude, protocol.AgentCodex} {
		for _, previous := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/previous=%t", agent, previous), func(t *testing.T) {
				s := testSettings(t, t.TempDir())
				s.ClaudeLiveFile = filepath.Join(s.Home, "fake-missing-projection")
				sess := remoteSession()
				sess.Agent = agent
				r := &fakeRunner{handle: func(Cmd) (Result, error) {
					t.Fatal("boot status ran a command")
					return Result{}, nil
				}}
				if previous {
					writeJSON(t, s.statePath(launchFile), Launch{BootID: "old", ConversationID: "old"})
					writeJSON(t, s.statePath(resultFile), taskResult{TaskResult: protocol.TaskResult{ExitCode: 0}})
				}
				for _, step := range []string{"", "rendering configuration", "bootstrapping projects: project 2/7", "preparing repo", "starting agent"} {
					if step != "" {
						writeJSON(t, s.statePath(bootFile), bootRecord{BootID: "new", Boot: protocol.BootBooting, CurrentStep: step})
					}
					st := CollectStatus(context.Background(), r, s, sess, loginNow)
					if st.Boot != protocol.BootBooting || st.Agent.State != protocol.AgentPending || st.Agent.Error != step || st.Agent.Task != nil || st.Agent.ConversationID != "" || st.RemoteControl == nil || st.RemoteControl.State != protocol.RemoteStarting {
						t.Fatalf("step %q: %+v", step, st)
					}
				}
			})
		}
	}
}

// Delay real boot operations on the fake clock, while Git stays inside the
// existing file-origin fixture. No CLI, network or real credential is used.
type slowBootRunner struct {
	git    Runner
	fake   *fakeRunner
	s      Settings
	pauses map[string]bool
}

func (r *slowBootRunner) LookPath(name string) (string, error) { return r.fake.LookPath(name) }

func (r *slowBootRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	var boot bootRecord
	if readJSONFile(r.s.statePath(bootFile), &boot) == nil && boot.Boot == protocol.BootBooting && r.pauses[boot.CurrentStep] {
		delete(r.pauses, boot.CurrentStep)
		time.Sleep(25 * time.Second)
	}
	if c.Name == "git" {
		return r.git.Run(ctx, c)
	}
	return r.fake.Run(ctx, c)
}

func TestDaemonSlowBootHeartbeatsStepsBeforeLoginRequired(t *testing.T) {
	for _, usable := range []bool{false, true} {
		t.Run(fmt.Sprintf("usable-projection=%t", usable), func(t *testing.T) {
			testDaemonSlowBootHeartbeats(t, usable)
		})
	}
}

func testDaemonSlowBootHeartbeats(t *testing.T, usableProjection bool) {
	t.Helper()
	// Two repositories and projects exercise incrementing counters in both
	// bootstrap phases, without publishing any catalog names in progress text.
	declaration := strings.Replace(projectTestCatalog, `"defaultBranch":"main"}},`, `"defaultBranch":"main"},"extra":{"github":"fixture/extra"}},`, 1)
	declaration = strings.Replace(declaration, `"projects":{`, `"projects":{"other":{"repositories":[{"name":"extra"}],"rules":"Other rules."},`, 1)
	_, s, git, source := privateBootstrapFixture(t, declaration)
	s.PrivateProjectRoots = true
	s.Getenv = envOf(nil)
	s.ClaudeLiveFile = filepath.Join(s.Home, "fake-projection")
	if usableProjection {
		publish(t, s.ClaudeLiveFile, projAccess(1, time.Hour))
	}
	rig := newDaemonRig(t, nil)
	rig.d.S, rig.d.ProjectCatalog = s, source
	rig.d.Session.Mode = protocol.ModeRemote
	rig.d.Interval, rig.d.Poll = time.Minute, time.Minute
	expected := []string{"rendering configuration", "bootstrapping projects: repository 1/2", "bootstrapping projects: repository 2/2", "bootstrapping projects: project 1/2", "bootstrapping projects: project 2/2", "preparing repo"}
	runner := &slowBootRunner{git: git, fake: rig.tmux, s: s, pauses: map[string]bool{}}
	for _, step := range expected {
		runner.pauses[step] = true
	}
	rig.d.R = runner
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		rig.d.Now = func() time.Time { return loginNow.Add(time.Since(start)) }
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rig.d.Send = func(ctx context.Context, st protocol.Status) error {
			if st.Boot == protocol.BootBooting {
				local := CollectStatus(ctx, runner, s, rig.d.Session, rig.d.now())
				if local.Agent.State != protocol.AgentPending || local.Agent.Error == "" || local.Boot != protocol.BootBooting {
					t.Errorf("local boot status lost progress: %+v", local)
				}
			}
			_ = rig.beats.send(ctx, st)
			if st.Boot != protocol.BootBooting {
				cancel()
			}
			return nil
		}
		if err := rig.d.Run(ctx); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		var previous time.Time
		for _, st := range rig.beats.all {
			if st.Boot != protocol.BootBooting {
				continue
			}
			if st.Agent.State != protocol.AgentPending || st.RemoteControl == nil || st.RemoteControl.State != protocol.RemoteStarting || st.Model != rig.d.Session.Model {
				t.Fatalf("premature agent/login state: %+v", st)
			}
			if !previous.IsZero() && st.ObservedAt.Sub(previous) < 10*time.Second {
				t.Fatalf("boot heartbeat too fast: %v", st.ObservedAt.Sub(previous))
			}
			previous = st.ObservedAt
			seen[st.Agent.Error] = true
		}
		for _, step := range expected {
			if !seen[step] {
				t.Errorf("no heartbeat during %q; saw %+v", step, seen)
			}
		}
		if len(runner.pauses) != 0 {
			t.Errorf("boot skipped operations: %+v", runner.pauses)
		}
		st, _ := rig.beats.last()
		if usableProjection {
			// Fake tmux creates no CLI pid/registration; only the launch and
			// successful login check are observed here.
			if st.Boot != protocol.BootReady || st.Agent.State != protocol.AgentBusy || st.Agent.Error != "" || st.RemoteControl == nil || st.RemoteControl.State == protocol.RemoteLoginRequired || !rig.tmuxStarted() {
				t.Fatalf("agent step did not start with the keeper projection: %+v, remote %+v", st, st.RemoteControl)
			}
		} else {
			if st.Boot != protocol.BootReady || st.Agent.State != protocol.AgentLoginRequired || !strings.Contains(st.Agent.Error, "waiting for the keeper's Claude projection") || st.RemoteControl == nil || st.RemoteControl.State != protocol.RemoteLoginRequired || rig.d.remote == nil || !rig.d.remote.waiting || rig.tmuxStarted() {
				t.Fatalf("agent step did not report actual missing login: %+v", st)
			}
		}
	})
}

func TestBootHeartbeatRateWithSlowSenderAndJoin(t *testing.T) {
	s := testSettings(t, t.TempDir())
	writeJSON(t, s.statePath(bootFile), bootRecord{BootID: "b", Boot: protocol.BootBooting, CurrentStep: "preparing repo"})
	synctest.Test(t, func(t *testing.T) {
		var sent []time.Time
		d := &Daemon{S: s, Session: remoteSession(), Log: slog.Default(), Send: func(_ context.Context, st protocol.Status) error {
			sent = append(sent, time.Now())
			time.Sleep(15 * time.Second)
			return nil
		}}
		stop := d.bootHeartbeat(context.Background())
		synctest.Wait()
		time.Sleep(60 * time.Second)
		synctest.Wait()
		stop()
		stop() // both explicit and deferred shutdown are safe
		if len(sent) < 3 {
			t.Fatalf("boot sender stopped heartbeating: %v", sent)
		}
		for i := 1; i < len(sent); i++ {
			if sent[i].Sub(sent[i-1]) < 25*time.Second {
				t.Fatalf("slow sender caught up too quickly: %v", sent)
			}
		}
		before := slices.Clone(sent)
		time.Sleep(time.Minute)
		synctest.Wait()
		if !slices.Equal(before, sent) {
			t.Fatal("boot sender sent after stop returned")
		}
	})
}
