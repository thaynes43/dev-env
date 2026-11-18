package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/summonbrief"
)

func review4Record(t *testing.T, s Settings, first Launch) {
	t.Helper()
	if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(startHandoffFile), startHandoff{
		State: handoffStarted, ConversationID: first.ConversationID, BootID: first.BootID,
	}); err != nil {
		t.Fatal(err)
	}
}

func review4WantBrief(t *testing.T, s Settings, sess protocol.Session, ws protocol.Workspace, first Launch, want string) {
	t.Helper()
	review4Record(t, s, first)
	got := summonedBriefState(s, sess, first, nil, "")
	if got != want {
		t.Errorf("brief=%s want=%s", got, want)
	}
	if !summonedLaunched(s, sess) {
		t.Fatal("start freed")
	}
	// Both uncertain and absent-brief records keep the one start consumed.
	for _, boot := range []string{"first", "later"} {
		r := &fakeRunner{}
		d := quietDaemon(s, sess, r, loginNow)
		step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, boot)
		if step.State != StepOK || why != "" || len(r.calls) != 0 {
			t.Fatalf("%s: state=%s calls=%d", boot, step.State, len(r.calls))
		}
		if _, err := os.Lstat(s.statePath(resumeFile)); !os.IsNotExist(err) {
			t.Fatalf("resume: %v", err)
		}
	}
}

func TestReview4BriefInOptionSlot(t *testing.T) {
	s, sess, ws := summonedFixture(t, protocol.ModeRemote)
	sess.Prompt = summonbrief.Render(sess.Name, sess.Caller, "upgrade",
		"Follow the runbook.", nil, nil, "0123456789abcdef")
	first, err := BuildLaunch(s, sess, ws, "first", loginNow)
	if err != nil {
		t.Fatal(err)
	}
	first.Argv = first.Argv[:len(first.Argv)-1]
	if first.Argv[len(first.Argv)-2] != "--append-system-prompt" {
		t.Fatal("unexpected builder shape")
	}
	first.Argv[len(first.Argv)-1] = sess.Prompt
	review4WantBrief(t, s, sess, ws, first, protocol.BriefNotLaunched)
}

func TestReview4LegacyRuleValueIsNotPositionalBrief(t *testing.T) {
	s, sess, ws := summonedFixture(t, protocol.ModeRemote)
	first, err := review4MainBuildLaunch(s, sess, ws, "first", loginNow)
	if err != nil {
		t.Fatal(err)
	}
	sess.Prompt = first.Argv[len(first.Argv)-1]
	if err := sess.Validate(); err != nil {
		t.Fatal(err)
	}
	first, err = review4MainBuildLaunch(s, sess, ws, "first", loginNow)
	if err != nil {
		t.Fatal(err)
	}
	if first.Argv[len(first.Argv)-2] != "--append-system-prompt" || first.Argv[len(first.Argv)-1] != sess.Prompt {
		t.Fatal("main builder did not reproduce the rule-value collision")
	}
	review4WantBrief(t, s, sess, ws, first, protocol.BriefNotLaunched)
}

func TestReview4LegacyOrdinaryBrief(t *testing.T) {
	s, sess, ws := summonedFixture(t, protocol.ModeRemote)
	first, err := review4MainBuildLaunch(s, sess, ws, "first", loginNow)
	if err != nil {
		t.Fatal(err)
	}
	review4WantBrief(t, s, sess, ws, first, protocol.BriefNotLaunched)
}

func TestReview4FinalOptionValue(t *testing.T) {
	for _, option := range []string{"--model", "--effort", "--session-id", "--max-turns", "--output-format", "--append-system-prompt", "--append-system-prompt-file", "--future-option", "-z"} {
		t.Run(option, func(t *testing.T) {
			s, sess, ws := summonedFixture(t, protocol.ModeRemote)
			first, err := BuildLaunch(s, sess, ws, "first", loginNow)
			if err != nil {
				t.Fatal(err)
			}
			first.Argv = append(first.Argv[:slices.Index(first.Argv, "--remote-control")+2], option, sess.Prompt)
			want := protocol.BriefNotLaunched
			if option == "--future-option" || option == "-z" {
				want = protocol.BriefUnconfirmed
			}
			review4WantBrief(t, s, sess, ws, first, want)
		})
	}
}

// This is a construction/reading matrix, not a provider-support allowlist.
func TestReview4EveryBuilderRemoteBrief(t *testing.T) {
	for _, rules := range []string{"inline-guard", "project-rules"} {
		t.Run(rules, func(t *testing.T) {
			s, sess, ws := summonedFixture(t, protocol.ModeRemote)
			if rules != "inline-guard" {
				s, sess, ws, _, _ = ruleInputLaunchFixture(t, protocol.AgentClaude, false)
				sess.Mode, sess.Caller, sess.Limits = protocol.ModeRemote, summonedCaller, nil
			}
			for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-2", "claude-haiku-4-5", "claude-opus-5-5[1m]", "claude-future-9-1"} {
				for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max", "ultra"} {
					for _, title := range []string{"", "Main coordinator"} {
						for _, brief := range []string{"Follow the runbook.", sess.Name} {
							t.Run(fmt.Sprintf("%s/%s/%s/%t", model, effort, title, brief == sess.Name), func(t *testing.T) {
								sess.Model, sess.Effort, sess.Title, sess.Prompt = model, effort, title, brief
								first, err := BuildLaunch(s, sess, ws, "first", loginNow)
								if err != nil {
									t.Fatal(err)
								}
								if first.Argv[len(first.Argv)-1] != brief {
									t.Fatal("builder lost positional brief")
								}
								review4WantBrief(t, s, sess, ws, first, protocol.BriefLaunched)
							})
						}
					}
				}
			}
		})
	}
}

func TestReview4HeldDeadline(t *testing.T) {
	s, sess, ws := summonedFixture(t, protocol.ModeRemote)
	s.StartDeadline = loginNow.Add(time.Minute).Format(time.RFC3339)
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
	now = loginNow.Add(2 * time.Minute)
	tm.pane += "Login successful.\nLOGIN_EXIT=0\n"
	d.pollRemoteLogin(context.Background())
	d.pollRemoteLogin(context.Background())
	starts := 0
	for _, c := range r.calls {
		if len(c.Args) > 3 && c.Args[0] == "new-session" && c.Args[3] == TmuxSession {
			starts++
		}
	}
	st := CollectStatus(context.Background(), &fakeRunner{}, s, sess, now)
	if starts != 0 || summonedLaunched(s, sess) || st.Brief != protocol.BriefNotLaunched || st.Agent.State != protocol.AgentFailed {
		t.Fatalf("starts=%d consumed=%t brief=%s agent=%s", starts, summonedLaunched(s, sess), st.Brief, st.Agent.State)
	}
	for _, file := range []string{launchFile, startHandoffFile, resultFile, resumeFile} {
		if _, err := os.Lstat(s.statePath(file)); !os.IsNotExist(err) {
			t.Fatalf("left %s: %v", file, err)
		}
	}
}

// Test-only historical writer, copied from main 5ed238165515023e88949a1edc92e7853c1f0752.
// Only the two function names were changed. Its helpers are unchanged in behavior.
func review4MainBuildLaunch(s Settings, sess protocol.Session, ws protocol.Workspace, bootID string, now time.Time) (Launch, error) {
	l, err := review4MainBuildFirstLaunch(s, sess, ws, bootID, now)
	if err != nil {
		return Launch{}, err
	}
	// A summoned session's first launch carries the start deadline to
	// run-agent's pre-spawn fence (DESIGN-064 5.9, N2). Only a summoned
	// session gets it: a scheduled session keeps today's launch, whose
	// deadline bounds the launch record alone (DESIGN-054).
	if sess.Caller != "" {
		l.StartDeadline = s.StartDeadline
	}
	// A summoned task checks its model before it starts and falls back once
	// (DESIGN-064 6.9, OP791). A remote session's TUI gets no pre-flight in
	// this slice (743d); no ordinary session ever gets one.
	if sess.Caller != "" && sess.Mode == protocol.ModeTask && sess.Agent == protocol.AgentClaude && !l.TUI {
		l.Preflight = &launchPreflight{Model: sess.Model, Fallback: sess.FallbackModel}
	}
	return l, nil
}

func review4MainBuildFirstLaunch(s Settings, sess protocol.Session, ws protocol.Workspace, bootID string, now time.Time) (Launch, error) {
	switch sess.Agent {
	case protocol.AgentCodex:
		return buildCodexLaunch(s, sess, ws, nil, bootID, now)
	case protocol.AgentCursor:
		return buildCursorLaunch(s, sess, ws, bootID, now)
	case protocol.AgentClaude:
	default:
		return Launch{}, fmt.Errorf("%w: agent %q has no launch in this agentd", ErrNotInPlan01, sess.Agent)
	}
	if err := claudeCredentialCheck(s, sess); err != nil {
		return Launch{}, err
	}
	conv, err := newUUID()
	if err != nil {
		return Launch{}, err
	}
	argv := claudeArgs(s, sess)
	argv = append(argv, "--session-id", conv)
	l := Launch{
		Session:        sess.Name,
		Dir:            ws.Worktree,
		Env:            []string{"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=dev-env"},
		Unset:          unsetFor(sess),
		LogPath:        s.LogPath(sess.Name),
		EventsPath:     s.statePath(eventsFile),
		ConversationID: conv,
		BootID:         bootID,
		CreatedAt:      now.UTC(),
	}
	// A first interactive conversation is bound only when both immutable
	// identities were supplied by the operator and downward API. Legacy
	// launches lacking either value keep their existing behavior.
	if (sess.Mode == protocol.ModeLocal || sess.Mode == protocol.ModeRemote) && apiv1.ValidContextUID(sess.SessionUID) && apiv1.ValidContextUID(s.PodUID) {
		l.Provider, l.SessionUID, l.PodUID = protocol.AgentClaude, sess.SessionUID, s.PodUID
	}
	if sess.CaptureOutput {
		if sess.Validate() != nil || !apiv1.ValidContextUID(s.PodUID) {
			return Launch{}, errors.New("managed capture requires original task and pod")
		}
		l.CaptureOutput, l.CaptureModel, l.SessionUID, l.PodUID, l.Provider = true, sess.Model, sess.SessionUID, s.PodUID, protocol.AgentClaude
	}
	if sess.Mode == protocol.ModeLocal || sess.Mode == protocol.ModeRemote {
		l.TUI = true
		if sess.Mode == protocol.ModeRemote {
			argv = append(argv, remoteControlArgs(sess)...)
		}
		guard := interactiveGuard(ws, s.WorkDir())
		if sess.Workspace != nil {
			guard = sharedGuard(ws, false)
		}
		rules, err := providerProjectArgs(s, sess, guard)
		if err != nil {
			return Launch{}, err
		}
		l.Argv = append(argv, rules...)
		return l, nil
	}
	if n := sess.MaxTurns(); n > 0 {
		argv = append(argv, "--max-turns", strconv.Itoa(int(n)))
	}
	guard := taskGuard(ws, s.WorkDir())
	if sess.Workspace != nil {
		guard = sharedGuard(ws, true)
	}
	ordinaryInstructions := s.CoordinatorInstructions && sess.Workspace == nil &&
		protocol.ValidTaskBudgetIdentifier(sess.SessionUID) && protocol.ValidTaskBudgetIdentifier(s.PodUID)
	if ordinaryInstructions {
		guard += "\n\n" + ordinaryInstructionPullGuidance
	}
	rules, err := providerProjectArgs(s, sess, guard)
	if err != nil {
		return Launch{}, err
	}
	argv = append(argv, rules...)
	l.CommonInputs, err = commonLaunchBinding(s, sess)
	if err != nil {
		return Launch{}, err
	}
	l.Argv = append(argv,
		"--output-format", "stream-json", "--verbose",
		"-p")
	l.Prompt = sess.Prompt
	l.Timeout = sess.TimeoutDuration()
	if err := bindInitialUsage(&l, sess, s.PodUID); err != nil {
		return Launch{}, err
	}
	if ordinaryInstructions && l.UsageSource != nil && l.UsageSource.Valid() && !l.ChildDecisions {
		l.CoordinatorInstructions = true
	}
	return l, nil
}
