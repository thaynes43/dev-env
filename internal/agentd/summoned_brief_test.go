package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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
			if tc.name != "missing-handoff" && tc.name != "no-launch" && tc.name != "refused-before-launch" {
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
	if summonedLaunched(s, sess) {
		t.Fatal("refusal consumed the one-shot latch")
	}
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "first", Boot: protocol.BootFailed, AgentError: why}); err != nil {
		t.Fatal(err)
	}
	st := CollectStatus(context.Background(), r, s, sess, loginNow)
	if st.Agent.State != protocol.AgentFailed || reportedBrief(t, st) != "not-launched" || !strings.Contains(st.Agent.Error, "brief must not start with '-'") {
		t.Fatalf("refusal status: %+v", st)
	}
}

// OP795 F1: unreadable or incomplete evidence is never a confirmed start,
// and the consumed one-shot latch still prohibits a resend on every boot.
func TestSummonedBriefIncompleteEvidenceNeverConfirms(t *testing.T) {
	for _, name := range []string{"dangling-refusal", "empty-refusal", "null-refusal", "conversation-only-refusal", "boot-only-refusal", "dangling-launch", "result-without-launch", "launch-without-identity"} {
		t.Run(name, func(t *testing.T) {
			s, sess, ws := summonedFixture(t, protocol.ModeRemote)
			first, err := BuildLaunch(s, sess, ws, "first", loginNow)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONFile(s.statePath(startHandoffFile), startHandoff{State: handoffStarted, ConversationID: first.ConversationID, BootID: first.BootID}); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "dangling-refusal":
				err = os.Symlink("missing-refusal", s.statePath(startRefusedFile))
			case "empty-refusal":
				err = os.WriteFile(s.statePath(startRefusedFile), []byte(`{}`), 0o600)
			case "null-refusal":
				err = os.WriteFile(s.statePath(startRefusedFile), []byte(`null`), 0o600)
			case "conversation-only-refusal":
				err = writeJSONFile(s.statePath(startRefusedFile), map[string]string{"conversationId": first.ConversationID})
			case "boot-only-refusal":
				err = writeJSONFile(s.statePath(startRefusedFile), map[string]string{"bootId": "other"})
			case "dangling-launch":
				if err = os.Remove(s.statePath(launchFile)); err == nil {
					err = os.Symlink("missing-launch", s.statePath(launchFile))
				}
			case "result-without-launch":
				if err = os.Remove(s.statePath(launchFile)); err == nil {
					err = writeJSONFile(s.statePath(resultFile), taskResult{})
				}
			case "launch-without-identity":
				first.ConversationID, first.BootID = "", ""
				err = writeJSONFile(s.statePath(launchFile), first)
				if err == nil {
					err = writeJSONFile(s.statePath(startHandoffFile), startHandoff{State: handoffStarted})
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			r := &fakeRunner{handle: func(Cmd) (Result, error) {
				t.Error("an uncertain first launch was resent")
				return Result{}, errors.New("must not run")
			}}
			for _, bootID := range []string{"first", "later"} {
				if !summonedLaunched(s, sess) {
					t.Fatal("the first-launch latch is free")
				}
				d := quietDaemon(s, sess, r, loginNow.Add(time.Hour))
				step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, bootID)
				if step.State != StepOK || why != "" || len(r.calls) != 0 {
					t.Fatalf("start: %+v %q, calls %v", step, why, r.calls)
				}
				for _, phase := range []string{protocol.BootBooting, protocol.BootReady, protocol.BootFailed} {
					if err := writeJSONFile(s.statePath(bootFile), bootRecord{Boot: phase, BootID: bootID}); err != nil {
						t.Fatal(err)
					}
					st := CollectStatus(context.Background(), &fakeRunner{}, s, sess, loginNow.Add(time.Hour))
					if got := reportedBrief(t, st); got != protocol.BriefUnconfirmed {
						t.Errorf("%s/%s brief=%q, want unconfirmed", phase, bootID, got)
					}
				}
			}
			if len(r.calls) != 0 {
				t.Fatalf("runner calls: %v", r.calls)
			}
			if _, err := os.Lstat(s.statePath(resumeFile)); !os.IsNotExist(err) {
				t.Fatalf("resume was written: %v", err)
			}
		})
	}
}

func TestSummonedBriefNULRefusalLeavesLatchFree(t *testing.T) {
	for _, mode := range []string{protocol.ModeRemote, protocol.ModeTask} {
		t.Run(mode, func(t *testing.T) {
			s, sess, ws := summonedFixture(t, mode)
			sess.Prompt = "Follow\x00the runbook. private brief text"
			l, err := BuildLaunch(s, sess, ws, "first", loginNow)
			if err == nil || !strings.Contains(err.Error(), "brief must not contain NUL") || len(l.Argv) != 0 {
				t.Error("BuildLaunch did not refuse the NUL brief")
			}
			r := &fakeRunner{}
			d := quietDaemon(s, sess, r, loginNow)
			step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, "first")
			if step.State != StepFail || !strings.Contains(why, "brief must not contain NUL") {
				t.Fatalf("not refused plainly: state=%s", step.State)
			}
			if strings.Contains(why, "private brief text") || len(r.calls) != 0 {
				t.Fatal("refusal echoed the brief or reached tmux")
			}
			for _, file := range []string{launchFile, startHandoffFile, startRefusedFile, resultFile, resumeFile} {
				if _, err := os.Lstat(s.statePath(file)); !os.IsNotExist(err) {
					t.Fatalf("refusal left %s: %v", file, err)
				}
			}
			if summonedLaunched(s, sess) {
				t.Fatal("refusal consumed the one-shot latch")
			}
			if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "first", Boot: protocol.BootFailed, AgentError: why}); err != nil {
				t.Fatal(err)
			}
			st := CollectStatus(context.Background(), r, s, sess, loginNow)
			if st.Agent.State != protocol.AgentFailed || !strings.Contains(st.Agent.Error, "brief must not contain NUL") {
				t.Fatalf("refusal status: state=%s", st.Agent.State)
			}
			if mode == protocol.ModeRemote && reportedBrief(t, st) != protocol.BriefNotLaunched {
				t.Fatal("refused remote brief is not reported not-launched")
			}
		})
	}
	// An ordinary task still carries arbitrary stdin bytes, including NUL.
	s, sess, ws := launchFixture(t)
	sess.Prompt = "ordinary\x00task"
	l, err := BuildLaunch(s, sess, ws, "ordinary", loginNow)
	if err != nil || l.Prompt != sess.Prompt {
		t.Fatalf("ordinary task stdin changed: %v", err)
	}
}

func TestSummonedBriefLiteralExecArguments(t *testing.T) {
	controls := ""
	for b := byte(1); b < 128; b++ {
		controls += string(b)
	}
	for _, tc := range []struct{ name, prompt string }{
		{"controls-and-unicode", "Follow " + controls + "\n'quotes' `literal` $variable; — 🦀\n --resume other"},
		{"maximum", strings.Repeat("x", protocol.MaxPromptBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, ws := summonedFixture(t, protocol.ModeRemote)
			sess.Prompt = tc.prompt
			if err := sess.Validate(); err != nil {
				t.Fatal(err)
			}
			l, err := BuildLaunch(s, sess, ws, "first", loginNow)
			if err != nil {
				t.Fatal(err)
			}
			// The stand-in prints each positional argument literally, separated
			// by NUL. No provider or shell evaluation of a brief is involved.
			args := append([]string{"-c", `for arg do printf '%s\000' "$arg"; done`, "stand-in"}, l.Argv[1:]...)
			cmd := exec.Command("/bin/sh", args...)
			cmd.Env = []string{"PATH=/usr/bin:/bin"} // no KUBERNETES_* or KUBECONFIG
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.Join(l.Argv[1:], "\x00") + "\x00"; string(out) != want {
				t.Fatal("exec did not preserve every argument byte")
			}
		})
	}
	_, sess, _ := summonedFixture(t, protocol.ModeRemote)
	sess.Prompt = strings.Repeat("x", protocol.MaxPromptBytes+1)
	if err := sess.Validate(); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatal("an over-limit brief passed Session validation")
	}
}

// R3-F1 / coordinator R2: a surviving handoff consumes the start even if
// launch.json was lost. Exercise both the fixture and a bounded start window.
func TestReview795OrphanHandoff(t *testing.T) {
	for _, state := range []string{handoffAsked, handoffAmbiguous, handoffStarted} {
		for _, deadline := range []string{"unset", "future"} {
			t.Run(state+"/"+deadline, func(t *testing.T) {
				s, sess, ws := summonedFixture(t, protocol.ModeRemote)
				if deadline == "future" {
					s.StartDeadline = loginNow.Add(time.Hour).Format(time.RFC3339)
				}
				first, err := BuildLaunch(s, sess, ws, "first", loginNow)
				if err != nil {
					t.Fatal(err)
				}
				if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
					t.Fatal(err)
				}
				path := s.statePath(startHandoffFile)
				if err := writeJSONFile(path, startHandoff{State: state, At: loginNow, ConversationID: first.ConversationID, BootID: first.BootID}); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				// Model loss of this entry, not a demonstrated power-loss failure.
				if err := os.Remove(s.statePath(launchFile)); err != nil {
					t.Fatal(err)
				}
				r := &fakeRunner{handle: func(c Cmd) (Result, error) {
					if len(c.Args) > 0 && c.Args[0] == "new-session" {
						return Result{}, nil
					}
					return Result{}, errNoSession
				}}
				for i, bootID := range []string{"first", "later"} {
					now := loginNow.Add(time.Duration(i) * time.Minute)
					if err := writeJSONFile(s.statePath(bootFile), bootRecord{Boot: protocol.BootReady, BootID: bootID}); err != nil {
						t.Fatal(err)
					}
					check := func() {
						t.Helper()
						st := CollectStatus(context.Background(), &fakeRunner{}, s, sess, now)
						if st.Brief != protocol.BriefUnconfirmed || !summonedLaunched(s, sess) {
							t.Errorf("%s: surviving handoff brief=%s latch=%t; want unconfirmed and consumed", bootID, st.Brief, summonedLaunched(s, sess))
						}
						if st.Agent.ConversationID != "" {
							t.Error("a new conversation was reported")
						}
					}
					check()
					d := quietDaemon(s, sess, r, now)
					step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, bootID)
					if step.State != StepOK || why != "" || len(r.calls) != 0 || d.remote != nil {
						t.Errorf("%s: surviving handoff start state=%s reason=%q calls=%d held=%t", bootID, step.State, why, len(r.calls), d.remote != nil)
					}
					check()
					for _, file := range []string{launchFile, resumeFile, resultFile} {
						if _, err := os.Lstat(s.statePath(file)); !os.IsNotExist(err) {
							t.Errorf("%s: new %s entry: %v", bootID, file, err)
						}
					}
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(before, after) {
						t.Errorf("%s: surviving handoff changed: %v", bootID, err)
					}
				}
			})
		}
	}
}

// All summoned modes use the same Lstat boundary, regardless of readability
// or the handoff's decoded state. Ordinary sessions ignore these entries.
func TestSummonedHandoffEntryConsumesStart(t *testing.T) {
	for _, mode := range []string{protocol.ModeRemote, protocol.ModeTask, protocol.ModeLocal} {
		for _, shape := range []string{handoffNothingStarted, "unknown", "corrupt", "unreadable", "directory", "dangling-link"} {
			t.Run(mode+"/"+shape, func(t *testing.T) {
				s, sess, ws := summonedFixture(t, mode)
				path := s.statePath(startHandoffFile)
				var err error
				switch shape {
				case "directory":
					err = os.Mkdir(path, 0o700)
				case "dangling-link":
					err = os.Symlink("missing-handoff", path)
				case "unreadable":
					err = os.WriteFile(path, []byte(`{}`), 0)
				case "corrupt":
					err = os.WriteFile(path, []byte(`{"state":`), 0o600)
				default:
					err = writeJSONFile(path, startHandoff{State: shape})
				}
				if err != nil {
					t.Fatal(err)
				}
				if !summonedLaunched(s, sess) {
					t.Error("surviving handoff did not consume the start")
				}
				r := &fakeRunner{}
				d := quietDaemon(s, sess, r, loginNow)
				_, _ = d.startAgent(context.Background(), ws, Step{State: StepOK}, "later")
				if len(r.calls) != 0 {
					t.Error("surviving handoff reached the runner")
				}
				sess.Caller = ""
				if summonedLaunched(s, sess) {
					t.Error("ordinary session consumed the summoned latch")
				}
			})
		}
	}
}

func TestSummonedMissingPrerequisiteLeavesStartFree(t *testing.T) {
	s, sess, ws := summonedFixture(t, protocol.ModeTask)
	s.Getenv = envOf(nil) // no admitted static-token prerequisite
	r := &fakeRunner{}
	d := quietDaemon(s, sess, r, loginNow)
	step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, "first")
	if step.State != StepFail || why == "" || len(r.calls) != 0 || summonedLaunched(s, sess) {
		t.Fatalf("missing prerequisite: state=%s calls=%d consumed=%t", step.State, len(r.calls), summonedLaunched(s, sess))
	}
	for _, file := range []string{launchFile, startHandoffFile, resultFile, resumeFile} {
		if _, err := os.Lstat(s.statePath(file)); !os.IsNotExist(err) {
			t.Fatalf("missing prerequisite left %s: %v", file, err)
		}
	}
}

func TestSummonedBriefHeldForLoginWindowStartsOnce(t *testing.T) {
	s, sess, ws := summonedFixture(t, protocol.ModeRemote)
	s.StartDeadline = loginNow.Add(time.Hour).Format(time.RFC3339)
	tm := &loginTmux{window: true, pane: loginPaneWithURL}
	r := &fakeRunner{handle: tm.handle}
	d := quietDaemon(s, sess, r, loginNow)
	release, err := acquireLoginLock(s)
	if err != nil {
		t.Fatal(err)
	}
	step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, "first")
	release()
	if step.State != StepOK || why != "" || d.remote == nil || !d.remote.deferred || tm.agent {
		t.Fatal("summoned first launch was not held for the login window")
	}
	assertFree := func() {
		t.Helper()
		if summonedLaunched(s, sess) {
			t.Fatal("held launch consumed the start")
		}
		for _, file := range []string{launchFile, startHandoffFile, resultFile, resumeFile} {
			if _, err := os.Lstat(s.statePath(file)); !os.IsNotExist(err) {
				t.Fatalf("held launch left %s: %v", file, err)
			}
		}
	}
	assertFree()
	d.pollRemoteLogin(context.Background())
	assertFree()
	tm.pane += "Login successful.\nLOGIN_EXIT=0\n"
	d.pollRemoteLogin(context.Background())
	d.pollRemoteLogin(context.Background())
	starts := 0
	for _, c := range r.calls {
		if len(c.Args) > 3 && c.Args[0] == "new-session" && c.Args[3] == TmuxSession {
			starts++
		}
	}
	var first Launch
	if err := readJSONFile(s.statePath(launchFile), &first); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || !summonedLaunched(s, sess) || first.Argv[len(first.Argv)-1] != sess.Prompt || summonedHandoff(s, first).State != handoffStarted {
		t.Fatalf("after window ended: starts=%d consumed=%t", starts, summonedLaunched(s, sess))
	}
}

// R3-F2 / coordinator R3: a last string alone proves neither a legacy
// remote launch nor a positional brief. Keep both complete shapes working.
func TestReview795IncompleteArgv(t *testing.T) {
	for _, tc := range []struct{ shape, want string }{
		{"null-argv", protocol.BriefUnconfirmed},
		{"empty-argv", protocol.BriefUnconfirmed},
		{"command-only", protocol.BriefUnconfirmed},
		{"empty-executable", protocol.BriefUnconfirmed},
		{"brief-as-executable", protocol.BriefUnconfirmed},
		{"option-as-executable", protocol.BriefUnconfirmed},
		{"missing-remote-control", protocol.BriefUnconfirmed},
		{"missing-remote-name", protocol.BriefUnconfirmed},
		{"wrong-remote-name", protocol.BriefUnconfirmed},
		{"complete-legacy", protocol.BriefNotLaunched},
		{"complete-brief", protocol.BriefLaunched},
		{"name-equals-brief-without-positional-brief", protocol.BriefNotLaunched},
	} {
		t.Run(tc.shape, func(t *testing.T) {
			s, sess, ws := summonedFixture(t, protocol.ModeRemote)
			if tc.shape == "name-equals-brief-without-positional-brief" {
				sess.Prompt = sess.Name
			}
			first, err := BuildLaunch(s, sess, ws, "first", loginNow)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.shape {
			case "null-argv":
				first.Argv = nil
			case "empty-argv":
				first.Argv = []string{}
			case "command-only":
				first.Argv = []string{sess.Prompt}
			case "empty-executable":
				first.Argv[0] = ""
			case "brief-as-executable":
				first.Argv[0] = sess.Prompt
			case "option-as-executable":
				first.Argv[0] = "--version"
			case "missing-remote-control":
				first.Argv = []string{s.ClaudeBin, sess.Prompt}
			case "missing-remote-name":
				first.Argv = []string{s.ClaudeBin, "--remote-control"}
			case "wrong-remote-name":
				first.Argv[slices.Index(first.Argv, "--remote-control")+1] = "other-session"
			case "complete-legacy", "name-equals-brief-without-positional-brief":
				first.Argv = []string{s.ClaudeBin, "--remote-control", sess.Name}
			}
			if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONFile(s.statePath(startHandoffFile), startHandoff{State: handoffStarted, At: loginNow, ConversationID: first.ConversationID, BootID: first.BootID}); err != nil {
				t.Fatal(err)
			}
			r := &fakeRunner{}
			for _, bootID := range []string{"first", "later"} {
				if err := writeJSONFile(s.statePath(bootFile), bootRecord{Boot: protocol.BootReady, BootID: bootID}); err != nil {
					t.Fatal(err)
				}
				st := CollectStatus(context.Background(), &fakeRunner{}, s, sess, loginNow)
				if st.Brief != tc.want || !summonedLaunched(s, sess) {
					t.Errorf("%s: incomplete argv=%s brief=%s latch=%t; want %s and consumed", bootID, tc.shape, st.Brief, summonedLaunched(s, sess), tc.want)
				}
				d := quietDaemon(s, sess, r, loginNow)
				step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, bootID)
				if step.State != StepOK || why != "" || len(r.calls) != 0 {
					t.Errorf("%s: incomplete argv reached a runner or failed: state=%s calls=%d", bootID, step.State, len(r.calls))
				}
				if _, err := os.Lstat(s.statePath(resumeFile)); !os.IsNotExist(err) {
					t.Fatalf("resume was written: %v", err)
				}
			}
		})
	}
}
