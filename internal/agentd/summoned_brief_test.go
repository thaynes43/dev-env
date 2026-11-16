package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Captured against unchanged production code at main 17b82f191e159a684ec1a031d68b84f77f479920.
// Only the minted conversation UUID is normalized. JSON preserves every argv byte.
func TestOrdinaryRemoteArgvGolden(t *testing.T) {
	s := testSettings(t, "/golden/home")
	sess := remoteSession()
	ws := protocol.Workspace{Worktree: s.WorktreePath(sess.Name), Branch: "agent/" + sess.Name}
	got := map[string][]string{}
	for _, title := range []string{"", "Main coordinator"} {
		sess.Title = title
		first, err := BuildLaunch(s, sess, ws, "first", loginNow)
		if err != nil {
			t.Fatal(err)
		}
		resume, err := BuildResume(s, sess, ws, first, "later", loginNow)
		if err != nil {
			t.Fatal(err)
		}
		for name, launch := range map[string]Launch{"first": first, "resume": resume} {
			argv := slices.Clone(launch.Argv)
			for i, arg := range argv {
				if arg == first.ConversationID {
					argv[i] = "<conversation-id>"
				}
			}
			got[title+"/"+name] = argv
		}
	}
	raw, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	want, err := os.ReadFile("testdata/ordinary_remote_argv.golden")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("ordinary remote argv changed:\n%s\nwant:\n%s", raw, want)
	}
}

func TestSummonedBriefOnlyAtFirstRemoteLaunch(t *testing.T) {
	for _, mode := range []string{protocol.ModeRemote, protocol.ModeTask, protocol.ModeLocal} {
		for _, caller := range []string{"", summonedCaller} {
			t.Run(mode+"/"+caller, func(t *testing.T) {
				s, sess, ws := launchFixture(t)
				sess.Mode, sess.Caller = mode, caller
				sess.Prompt = "Follow the runbook.\nA literal $variable; `no shell`."
				if mode == protocol.ModeLocal || (mode == protocol.ModeRemote && caller == "") {
					sess.Prompt = ""
				}
				l, err := BuildLaunch(s, sess, ws, "first", loginNow)
				if err != nil {
					t.Fatal(err)
				}
				wantBrief := mode == protocol.ModeRemote && caller != ""
				if wantBrief {
					if !l.TUI || l.Prompt != "" || l.Resume || l.Argv[len(l.Argv)-1] != sess.Prompt {
						t.Fatalf("brief is not the last positional argument: %#v", l.Argv)
					}
					rc := slices.Index(l.Argv, "--remote-control")
					rules := slices.Index(l.Argv, "--append-system-prompt")
					if rc < 0 || l.Argv[rc+1] != sess.Name || rules < rc || rules+2 != len(l.Argv)-1 || sess.Title != "" {
						t.Fatalf("name and rules before the brief: %#v", l.Argv)
					}
				} else if sess.Prompt != "" && slices.Contains(l.Argv, sess.Prompt) {
					t.Fatal("another launch carried a positional brief")
				}
				if mode == protocol.ModeTask && l.Prompt != sess.Prompt {
					t.Fatal("task stdin prompt changed")
				}
				resume, err := BuildResume(s, sess, ws, l, "later", loginNow.Add(time.Hour))
				if caller != "" {
					if err == nil || len(resume.Argv) != 0 {
						t.Fatal("a summoned session resumed")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if sess.Prompt != "" && slices.Contains(resume.Argv, sess.Prompt) {
					t.Fatal("a resume carried the brief")
				}
			})
		}
	}
	t.Run("summoned-remote-without-prompt", func(t *testing.T) {
		s, sess, ws := launchFixture(t)
		sess.Mode, sess.Caller, sess.Prompt = protocol.ModeRemote, summonedCaller, ""
		l, err := BuildLaunch(s, sess, ws, "first", loginNow)
		if err != nil || l.Argv[len(l.Argv)-2] != "--append-system-prompt" {
			t.Fatalf("empty brief changed the argv: %q %v", l.Argv, err)
		}
	})
}

func TestSummonedBriefLeadingDashRefusedAtLaunch(t *testing.T) {
	for _, mode := range []string{protocol.ModeRemote, protocol.ModeTask} {
		for _, prompt := range []string{"-", "--resume other", "-p\nprivate brief text"} {
			t.Run(mode+"/"+prompt, func(t *testing.T) {
				s, sess, ws := launchFixture(t)
				sess.Mode, sess.Caller, sess.Prompt = mode, summonedCaller, prompt
				l, err := BuildLaunch(s, sess, ws, "first", loginNow)
				if err == nil || !strings.Contains(err.Error(), "brief must not start with '-'") || len(l.Argv) != 0 {
					t.Fatalf("launch was not refused plainly: %q %v", l.Argv, err)
				}
				if strings.Contains(err.Error(), "private brief text") {
					t.Fatal("refusal echoed the brief")
				}
			})
		}
	}
	// Ordinary tasks have always accepted option-looking text on stdin.
	s, sess, ws := launchFixture(t)
	l, err := BuildLaunch(s, sess, ws, "ordinary", loginNow)
	if err != nil || l.Prompt != sess.Prompt {
		t.Fatalf("ordinary task changed: %v", err)
	}
}

func reportedBrief(t *testing.T, st protocol.Status) string {
	t.Helper()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Brief string `json:"brief"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Brief
}

func TestSummonedBriefDeliveryFromHandoff(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"no-launch", "pending"}, {"refused-before-launch", "not-launched"},
		{"asked", "unconfirmed"}, {"ambiguous", "unconfirmed"}, {"missing-handoff", "unconfirmed"},
		{"unknown-handoff", "unconfirmed"}, {"other-launch", "unconfirmed"}, {"truncated-handoff", "unconfirmed"},
		{"started", "launched"}, {"nothing-started", "not-launched"}, {"fence-refused", "not-launched"},
		{"other-fence", "launched"}, {"other-session", "unconfirmed"}, {"resume-record", "unconfirmed"},
		{"truncated-fence", "unconfirmed"}, {"directory-fence", "unconfirmed"},
		{"nothing-started-corrupt-fence", "not-launched"},
		{"legacy-without-brief", "not-launched"}, {"unreadable-launch", "unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, ws := launchFixture(t)
			sess.Mode, sess.Caller, sess.Prompt = protocol.ModeRemote, summonedCaller, "Follow the runbook."
			if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			first, err := BuildLaunch(s, sess, ws, "first", loginNow)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "other-session" {
				first.Session = "another-session"
			}
			if tc.name == "resume-record" {
				first.Resume = true
			}
			if tc.name != "no-launch" && tc.name != "refused-before-launch" {
				if tc.name == "legacy-without-brief" {
					first.Argv = []string{"claude", "--remote-control", sess.Name}
				}
				if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
					t.Fatal(err)
				}
			}
			h := startHandoff{State: tc.name, ConversationID: first.ConversationID, BootID: first.BootID}
			if tc.name == "other-launch" {
				h.State, h.ConversationID = handoffStarted, "other"
			}
			if tc.name == "fence-refused" || tc.name == "other-fence" || tc.name == "legacy-without-brief" {
				h.State = handoffStarted
			}
			if tc.name == "truncated-fence" || tc.name == "directory-fence" {
				h.State = handoffStarted
			}
			if tc.name == "nothing-started-corrupt-fence" {
				h.State = handoffNothingStarted
			}
			if tc.name == "truncated-fence" || tc.name == "nothing-started-corrupt-fence" {
				if err := os.WriteFile(s.statePath(startRefusedFile), []byte(`{"reason":`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "directory-fence" {
				if err := os.Mkdir(s.statePath(startRefusedFile), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name != "missing-handoff" {
				if err := writeJSONFile(s.statePath(startHandoffFile), h); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "truncated-handoff" {
				if err := os.WriteFile(s.statePath(startHandoffFile), []byte(`{"state":`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "unreadable-launch" {
				if err := os.WriteFile(s.statePath(launchFile), []byte(`{"argv":`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "fence-refused" || tc.name == "other-fence" {
				fenceBoot := first.BootID
				if tc.name == "other-fence" {
					fenceBoot = "other-boot"
				}
				if err := writeJSONFile(s.statePath(startRefusedFile), startRefused{ConversationID: first.ConversationID, BootID: fenceBoot, Reason: protocol.StartDeadlinePassed}); err != nil {
					t.Fatal(err)
				}
			}
			for _, phase := range []string{protocol.BootBooting, protocol.BootReady, protocol.BootFailed} {
				for _, bootID := range []string{"first", "later"} {
					boot := bootRecord{Boot: phase, BootID: bootID}
					if tc.name == "refused-before-launch" {
						boot.AgentError = "the brief must not start with '-'"
					}
					if err := writeJSONFile(s.statePath(bootFile), boot); err != nil {
						t.Fatal(err)
					}
					st := CollectStatus(context.Background(), &fakeRunner{}, s, sess, loginNow.Add(time.Hour))
					if got := reportedBrief(t, st); got != tc.want {
						t.Fatalf("%s/%s brief=%q, want %q", phase, bootID, got, tc.want)
					}
				}
			}
		})
	}
}

func TestSummonedBriefAbsentOutsideRemoteWithPrompt(t *testing.T) {
	for _, tc := range []struct{ mode, caller, prompt string }{
		{protocol.ModeRemote, "", ""}, {protocol.ModeRemote, summonedCaller, ""},
		{protocol.ModeTask, summonedCaller, "a task brief"}, {protocol.ModeTask, "", "a task"}, {protocol.ModeLocal, "", ""},
	} {
		s, sess, _ := launchFixture(t)
		sess.Mode, sess.Caller, sess.Prompt = tc.mode, tc.caller, tc.prompt
		st := CollectStatus(context.Background(), &fakeRunner{}, s, sess, loginNow)
		raw, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["brief"]; ok {
			t.Fatalf("%+v adds a brief key", tc)
		}
	}
}

func TestSummonedBriefAmbiguousStartNeverResent(t *testing.T) {
	for _, state := range []string{handoffAsked, handoffAmbiguous} {
		t.Run(state, func(t *testing.T) {
			s, sess, ws := launchFixture(t)
			sess.Mode, sess.Caller, sess.Prompt = protocol.ModeRemote, summonedCaller, "Follow the runbook."
			if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			first, err := BuildLaunch(s, sess, ws, "first", loginNow)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONFile(s.statePath(startHandoffFile), startHandoff{State: state, ConversationID: first.ConversationID, BootID: first.BootID}); err != nil {
				t.Fatal(err)
			}
			r := &fakeRunner{handle: func(Cmd) (Result, error) {
				t.Error("an ambiguous brief was resent")
				return Result{}, errors.New("must not run")
			}}
			for _, bootID := range []string{"first", "later"} {
				d := quietDaemon(s, sess, r, loginNow.Add(time.Hour))
				step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, bootID)
				if step.State != StepOK || why != "" || len(r.calls) != 0 {
					t.Fatalf("start: %+v %q, calls %v", step, why, r.calls)
				}
				st := CollectStatus(context.Background(), r, s, sess, loginNow.Add(time.Hour))
				if got := reportedBrief(t, st); got != "unconfirmed" {
					t.Fatalf("brief=%q, want unconfirmed", got)
				}
			}
			if _, err := os.Stat(filepath.Join(s.StateDir, resumeFile)); !os.IsNotExist(err) {
				t.Fatalf("resume was written: %v", err)
			}
		})
	}
}

func TestSummonedBriefRefusalLeavesNoLaunch(t *testing.T) {
	s, sess, ws := summonedFixture(t, protocol.ModeRemote)
	sess.Prompt = "--resume private brief text"
	r := &fakeRunner{}
	d := quietDaemon(s, sess, r, loginNow)
	step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, "first")
	if step.State != StepFail || !strings.Contains(why, "brief must not start with '-'") {
		t.Fatalf("not refused: %+v %q", step, why)
	}
	if len(r.calls) != 0 {
		t.Fatal("a refused brief reached tmux")
	}
	for _, file := range []string{launchFile, startHandoffFile, resumeFile} {
		if _, err := os.Lstat(s.statePath(file)); !os.IsNotExist(err) {
			t.Fatalf("refusal left %s: %v", file, err)
		}
	}
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "first", Boot: protocol.BootFailed, AgentError: why}); err != nil {
		t.Fatal(err)
	}
	st := CollectStatus(context.Background(), r, s, sess, loginNow)
	if st.Agent.State != protocol.AgentFailed || reportedBrief(t, st) != "not-launched" || !strings.Contains(st.Agent.Error, "brief must not start with '-'") {
		t.Fatalf("refusal status: %+v", st)
	}
}
