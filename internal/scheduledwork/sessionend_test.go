package scheduledwork

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/standby"
)

var now = time.Date(2026, 12, 3, 14, 30, 0, 0, time.UTC)

// fixture is a task session whose pod is old enough for the pod-age proof and
// whose last heartbeat came after the pod's creation.
func fixture(edit func(*v1alpha1.AgentSession, *corev1.Pod)) (*v1alpha1.AgentSession, *corev1.Pod) {
	podCreated := now.Add(-2 * standby.PodGrace)
	hb := metav1.NewTime(now.Add(-time.Minute))
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "sw-0123456789ab-202612031400", UID: "session-uid"},
		Spec:       v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeTask, OperatingMode: v1alpha1.OperatingModeRunning},
		Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning,
			Agent: &v1alpha1.AgentStatus{Boot: protocol.BootReady, Status: protocol.AgentBusy, LastHeartbeat: &hb}},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, CreationTimestamp: metav1.NewTime(podCreated)}}
	if edit != nil {
		edit(s, pod)
	}
	return s, pod
}

func TestSessionEnded(t *testing.T) {
	agent := func(status string) func(*v1alpha1.AgentSession, *corev1.Pod) {
		return func(s *v1alpha1.AgentSession, _ *corev1.Pod) { s.Status.Agent.Status = status }
	}
	phase := func(p v1alpha1.SessionPhase) func(*v1alpha1.AgentSession, *corev1.Pod) {
		return func(s *v1alpha1.AgentSession, _ *corev1.Pod) { s.Status.Phase = p }
	}
	both := func(fs ...func(*v1alpha1.AgentSession, *corev1.Pod)) func(*v1alpha1.AgentSession, *corev1.Pod) {
		return func(s *v1alpha1.AgentSession, p *corev1.Pod) {
			for _, f := range fs {
				f(s, p)
			}
		}
	}
	remote := func(s *v1alpha1.AgentSession, _ *corev1.Pod) { s.Spec.Mode, s.Spec.Prompt = v1alpha1.ModeRemote, "" }
	for _, c := range []struct {
		name      string
		edit      func(*v1alpha1.AgentSession, *corev1.Pod)
		noPod     bool
		want      bool
		needsPod  bool
		whyPrefix string
	}{
		{name: "running and busy", want: false},
		{name: "operating mode suspended, pod still running", edit: func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		}, want: true, whyPrefix: "the session is suspended"},
		{name: "idle suspended interactive", edit: both(remote, func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			s.Status.Phase = v1alpha1.PhaseSuspended
		}), noPod: true, want: true},
		{name: "phase Suspended", edit: phase(v1alpha1.PhaseSuspended), noPod: true, want: true, whyPrefix: "the session's phase is Suspended"},
		{name: "phase Failed", edit: phase(v1alpha1.PhaseFailed), want: true},
		{name: "phase Archived", edit: phase(v1alpha1.PhaseArchived), noPod: true, want: true},
		{name: "phase Pending", edit: phase(v1alpha1.PhasePending), noPod: true, want: false},
		{name: "phase Draining with an interrupted agent", edit: both(phase(v1alpha1.PhaseDraining), agent(protocol.AgentInterrupted)), want: false},
		{name: "drain recreating the pod: Pending, old report", edit: both(phase(v1alpha1.PhasePending), agent(protocol.AgentInterrupted)), noPod: true, want: false},
		{name: "task result recorded while the pod runs", edit: func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded}
		}, want: true, whyPrefix: "the task result is recorded"},
		{name: "task outcome recorded", edit: func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Status.TaskOutcome = &v1alpha1.TaskOutcomeStatus{SessionUID: "session-uid", PodUID: "p", Record: v1alpha1.TaskOutcomeRecordStatus{Summary: "done"}}
		}, want: true},
		{name: "a task result does not end a remote session", edit: both(remote, func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded}
		}), want: false},
		{name: "agent exited, old pod, fresh heartbeat", edit: agent(protocol.AgentExited), want: true, needsPod: true, whyPrefix: "the agent exited"},
		{name: "agent interrupted while Idle", edit: both(phase(v1alpha1.PhaseIdle), agent(protocol.AgentInterrupted)), want: true, needsPod: true},
		{name: "remote agent exited (Tom typed /exit)", edit: both(remote, agent(protocol.AgentExited)), want: true, needsPod: true},
		{name: "agent exited, no pod", edit: agent(protocol.AgentExited), noPod: true, want: false, needsPod: true},
		{name: "agent exited, young pod", edit: both(agent(protocol.AgentExited), func(_ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.CreationTimestamp = metav1.NewTime(now.Add(-time.Minute))
		}), want: false, needsPod: true},
		{name: "agent exited, heartbeat older than the pod", edit: both(agent(protocol.AgentExited), func(s *v1alpha1.AgentSession, p *corev1.Pod) {
			hb := metav1.NewTime(p.CreationTimestamp.Add(-time.Second))
			s.Status.Agent.LastHeartbeat = &hb
		}), want: false, needsPod: true},
		{name: "agent exited, no heartbeat", edit: both(agent(protocol.AgentExited), func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Status.Agent.LastHeartbeat = nil
		}), want: false, needsPod: true},
		{name: "drained pod booting: the previous boot's interrupted report", edit: both(agent(protocol.AgentInterrupted), func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Status.Agent.Boot = protocol.BootBooting
		}), want: false},
		{name: "agent exited with no phase yet", edit: both(phase(""), agent(protocol.AgentExited)), want: false},
		{name: "never launched past its start deadline, old pod", edit: both(agent(protocol.AgentFailed), func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Status.Agent.Message = protocol.StartDeadlinePassed
		}), want: true, needsPod: true, whyPrefix: "the agent failed"},
		{name: "never launched past its start deadline, young pod", edit: both(agent(protocol.AgentFailed), func(s *v1alpha1.AgentSession, p *corev1.Pod) {
			s.Status.Agent.Message = protocol.StartDeadlinePassed
			p.CreationTimestamp = metav1.NewTime(now.Add(-time.Minute))
		}), want: false, needsPod: true},
		{name: "being deleted is not an end by itself", edit: func(s *v1alpha1.AgentSession, _ *corev1.Pod) {
			d := metav1.NewTime(now)
			s.DeletionTimestamp = &d
		}, want: false},
		{name: "no agent report", edit: func(s *v1alpha1.AgentSession, _ *corev1.Pod) { s.Status.Agent = nil }, want: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, pod := fixture(c.edit)
			if c.noPod {
				pod = nil
			}
			got, why := SessionEnded(s, pod, now)
			if got != c.want {
				t.Fatalf("ended %v (%q), want %v", got, why, c.want)
			}
			if got == (why == "") {
				t.Errorf("why %q for ended %v", why, got)
			}
			if c.whyPrefix != "" && !strings.HasPrefix(why, c.whyPrefix) {
				t.Errorf("why %q, want it to start %q", why, c.whyPrefix)
			}
			if n := NeedsPodProof(s); n != c.needsPod {
				t.Errorf("NeedsPodProof %v, want %v", n, c.needsPod)
			}
		})
	}
}

func TestOutcome(t *testing.T) {
	deadlinePassed := now.Add(-time.Minute).Format(time.RFC3339)
	deadlineAhead := now.Add(time.Minute).Format(time.RFC3339)
	result := func(state v1alpha1.TaskResultState, reason, msg string) func(*v1alpha1.AgentSession) {
		return func(s *v1alpha1.AgentSession) {
			s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: state, Reason: reason, Message: msg}
		}
	}
	deadline := func(at string) func(*v1alpha1.AgentSession) {
		return func(s *v1alpha1.AgentSession) {
			s.Annotations = map[string]string{v1alpha1.AnnotationScheduledWorkStartDeadline: at}
		}
	}
	remote := func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeRemote }
	noAgent := func(s *v1alpha1.AgentSession) { s.Status.Agent = nil }
	outcome := func(uid string) func(*v1alpha1.AgentSession) {
		return func(s *v1alpha1.AgentSession) {
			s.Status.TaskOutcome = &v1alpha1.TaskOutcomeStatus{SessionUID: uid, PodUID: "p",
				Record: v1alpha1.TaskOutcomeRecordStatus{Summary: "opened the PR", PRURLs: []string{"https://github.com/thaynes43/dev-env/pull/1"}}}
		}
	}
	reaped := func(s *v1alpha1.AgentSession) {
		d := metav1.NewTime(now.Add(-time.Second))
		s.DeletionTimestamp = &d
	}
	suspended := func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended }
	for _, c := range []struct {
		name      string
		edits     []func(*v1alpha1.AgentSession)
		state     string
		reason    string
		execution v1alpha1.TaskResultState
		reported  bool
	}{
		{"headless succeeded", []func(*v1alpha1.AgentSession){result(v1alpha1.TaskResultSucceeded, "TaskSucceeded", "the task exited 0 (success) after 2 turns")},
			v1alpha1.OccurrenceSucceeded, "the task exited 0 (success) after 2 turns", v1alpha1.TaskResultSucceeded, false},
		{"headless failed", []func(*v1alpha1.AgentSession){result(v1alpha1.TaskResultFailed, "TaskExitedNonZero", "the task exited 1")},
			v1alpha1.OccurrenceFailed, "the task exited 1", v1alpha1.TaskResultFailed, false},
		{"headless timed out", []func(*v1alpha1.AgentSession){result(v1alpha1.TaskResultTimedOut, "TaskTimedOut", "")},
			v1alpha1.OccurrenceFailed, "TaskTimedOut", v1alpha1.TaskResultTimedOut, false},
		{"headless unknown is Ended", []func(*v1alpha1.AgentSession){result(v1alpha1.TaskResultUnknown, "TaskResultMissing", "the task exited 0 with no success result event")},
			v1alpha1.OccurrenceEnded, "the task exited 0 with no success result event", v1alpha1.TaskResultUnknown, false},
		{"headless result with neither reason nor message", []func(*v1alpha1.AgentSession){result(v1alpha1.TaskResultSucceeded, "", "")},
			v1alpha1.OccurrenceSucceeded, "task result Succeeded", v1alpha1.TaskResultSucceeded, false},
		{"headless result decides even when reaped", []func(*v1alpha1.AgentSession){reaped, result(v1alpha1.TaskResultSucceeded, "TaskSucceeded", "ok")},
			v1alpha1.OccurrenceSucceeded, "ok", v1alpha1.TaskResultSucceeded, false},
		{"headless result beats a passed deadline with no agent report", []func(*v1alpha1.AgentSession){result(v1alpha1.TaskResultFailed, "TaskExitedNonZero", "x"), deadline(deadlinePassed), noAgent},
			v1alpha1.OccurrenceFailed, "x", v1alpha1.TaskResultFailed, false},
		{"headless ended without a result", nil, v1alpha1.OccurrenceFailed, ReasonNoTaskResult, "", false},
		{"headless reaped before it ended", []func(*v1alpha1.AgentSession){reaped}, v1alpha1.OccurrenceFailed, ReasonReaped, "", false},
		{"headless outcome copied", []func(*v1alpha1.AgentSession){outcome("session-uid"), result(v1alpha1.TaskResultSucceeded, "TaskSucceeded", "ok")},
			v1alpha1.OccurrenceSucceeded, "ok", v1alpha1.TaskResultSucceeded, true},
		{"headless outcome of another session is not copied", []func(*v1alpha1.AgentSession){outcome("other-uid")},
			v1alpha1.OccurrenceFailed, ReasonNoTaskResult, "", false},
		{"headless outcome without a result", []func(*v1alpha1.AgentSession){outcome("session-uid")},
			v1alpha1.OccurrenceFailed, ReasonNoTaskResult, "", true},
		{"interactive ended", []func(*v1alpha1.AgentSession){remote}, v1alpha1.OccurrenceEnded, ReasonParticipationNotObserved, "", false},
		{"interactive reaped", []func(*v1alpha1.AgentSession){reaped, remote}, v1alpha1.OccurrenceEnded, ReasonParticipationNotObserved, "", false},
		{"interactive ignores a task outcome", []func(*v1alpha1.AgentSession){remote, outcome("session-uid")}, v1alpha1.OccurrenceEnded, ReasonParticipationNotObserved, "", false},
		{"headless never reported, deadline passed", []func(*v1alpha1.AgentSession){deadline(deadlinePassed), noAgent},
			v1alpha1.OccurrenceFailed, ReasonNotStarted, "", false},
		{"interactive never reported, deadline passed", []func(*v1alpha1.AgentSession){reaped, remote, deadline(deadlinePassed), noAgent},
			v1alpha1.OccurrenceFailed, ReasonNotStarted, "", false},
		{"agentd refused the late first launch", []func(*v1alpha1.AgentSession){deadline(deadlinePassed), func(s *v1alpha1.AgentSession) {
			s.Status.Agent.Status, s.Status.Agent.Message = protocol.AgentFailed, protocol.StartDeadlinePassed
		}}, v1alpha1.OccurrenceFailed, ReasonNotStarted, "", false},
		{"interactive refused late launch", []func(*v1alpha1.AgentSession){remote, deadline(deadlinePassed), func(s *v1alpha1.AgentSession) {
			s.Status.Agent.Status, s.Status.Agent.Message = protocol.AgentFailed, protocol.StartDeadlinePassed
		}}, v1alpha1.OccurrenceFailed, ReasonNotStarted, "", false},
		{"an agent failure with another message is not a late launch", []func(*v1alpha1.AgentSession){remote, deadline(deadlinePassed), func(s *v1alpha1.AgentSession) {
			s.Status.Agent.Status, s.Status.Agent.Message = protocol.AgentFailed, "no such repo"
		}}, v1alpha1.OccurrenceEnded, ReasonParticipationNotObserved, "", false},
		{"an agent that ran is not unstarted after the deadline", []func(*v1alpha1.AgentSession){remote, deadline(deadlinePassed)},
			v1alpha1.OccurrenceEnded, ReasonParticipationNotObserved, "", false},
		{"no agent report before the deadline", []func(*v1alpha1.AgentSession){reaped, deadline(deadlineAhead), noAgent},
			v1alpha1.OccurrenceFailed, ReasonReaped, "", false},
		{"an unreadable deadline proves nothing", []func(*v1alpha1.AgentSession){deadline("tomorrow"), noAgent},
			v1alpha1.OccurrenceFailed, ReasonNoTaskResult, "", false},
		{"headless suspended, then reaped", []func(*v1alpha1.AgentSession){suspended, reaped}, v1alpha1.OccurrenceFailed, ReasonNoTaskResult, "", false},
		{"headless suspended", []func(*v1alpha1.AgentSession){suspended}, v1alpha1.OccurrenceFailed, ReasonNoTaskResult, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := fixture(nil)
			for _, e := range c.edits {
				e(s)
			}
			got := Outcome(s, now)
			if got.State != c.state || got.Reason != c.reason {
				t.Fatalf("outcome %s %q, want %s %q", got.State, got.Reason, c.state, c.reason)
			}
			switch {
			case c.execution == "" && got.Execution != nil:
				t.Errorf("execution %+v, want none", got.Execution)
			case c.execution != "" && (got.Execution == nil || got.Execution.State != c.execution || got.Execution.Reason != s.Status.TaskResult.Reason):
				t.Errorf("execution %+v, want %s %q", got.Execution, c.execution, s.Status.TaskResult.Reason)
			}
			if (got.Reported != nil) != c.reported {
				t.Errorf("reported %+v, want %v", got.Reported, c.reported)
			}
			if got.Reported != nil && (got.Reported == &s.Status.TaskOutcome.Record || got.Reported.Summary != "opened the PR") {
				t.Errorf("reported %+v is not a copy of the retained outcome", got.Reported)
			}
		})
	}
}

// A long task message is cut to the occurrence's 256 bytes, at a rune
// boundary, and a long reason to the execution's 128.
func TestOutcomeClipsToTheSchema(t *testing.T) {
	s, _ := fixture(nil)
	s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultFailed,
		Reason: strings.Repeat("R", 200), Message: strings.Repeat("界", 200)}
	got := Outcome(s, now)
	if len(got.Reason) > maxReason || !utf8.ValidString(got.Reason) || !strings.HasPrefix(strings.Repeat("界", 200), got.Reason) {
		t.Errorf("reason %d bytes, valid %v", len(got.Reason), utf8.ValidString(got.Reason))
	}
	if len(got.Execution.Reason) != maxExecutionReason {
		t.Errorf("execution reason %d bytes", len(got.Execution.Reason))
	}
}

func TestOccurrenceAnnotations(t *testing.T) {
	for _, c := range []struct {
		name, at, deadline string
		wantAt, wantDL     time.Time
		atErr, dlErr       bool
	}{
		{name: "valid", at: "1796306400", deadline: "2026-12-03T14:15:00Z", wantAt: time.Unix(1796306400, 0).UTC(), wantDL: time.Date(2026, 12, 3, 14, 15, 0, 0, time.UTC)},
		{name: "offset deadline", at: "1796306400", deadline: "2026-12-03T09:15:00-05:00", wantAt: time.Unix(1796306400, 0).UTC(), wantDL: time.Date(2026, 12, 3, 14, 15, 0, 0, time.UTC)},
		{name: "missing", atErr: true, dlErr: true},
		{name: "not numbers", at: "2026-12-03T14:00:00Z", deadline: "1796306400", atErr: true, dlErr: true},
		{name: "zero and negative", at: "0", deadline: "", atErr: true, dlErr: true},
		{name: "negative", at: "-5", deadline: "2026-12-03", atErr: true, dlErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &v1alpha1.AgentSession{}
			if c.name != "missing" {
				s.Annotations = map[string]string{v1alpha1.AnnotationScheduledWorkOccurrence: c.at, v1alpha1.AnnotationScheduledWorkStartDeadline: c.deadline}
			}
			at, err := OccurrenceAt(s)
			if (err != nil) != c.atErr || (err == nil && !at.Equal(c.wantAt)) {
				t.Errorf("at %v, %v", at, err)
			}
			dl, err := StartDeadline(s)
			if (err != nil) != c.dlErr || (err == nil && !dl.Equal(c.wantDL)) {
				t.Errorf("deadline %v, %v", dl, err)
			}
		})
	}
}

// The heartbeat stores agent.task (and the retained outcome after it) before
// the reconciler's next observe writes status.taskResult. TaskResult reads the
// same verdict from agent.task meanwhile, so neither rule takes a finished
// task for one with no result.
func TestTaskResultReadsTheHeartbeatsTask(t *testing.T) {
	finished := metav1.NewTime(now.Add(-time.Minute))
	success := &v1alpha1.TaskStatus{ExitCode: 0, Subtype: "success", NumTurns: 2, FinishedAt: finished}
	for _, c := range []struct {
		name   string
		edit   func(*v1alpha1.AgentSession)
		state  v1alpha1.TaskResultState
		reason string
	}{
		{"none before the task ends", func(*v1alpha1.AgentSession) {}, "", ""},
		{"derived from agent.task", func(s *v1alpha1.AgentSession) { s.Status.Agent.Task = success }, v1alpha1.TaskResultSucceeded, "TaskSucceeded"},
		{"a non-zero exit", func(s *v1alpha1.AgentSession) {
			s.Status.Agent.Task = &v1alpha1.TaskStatus{ExitCode: 2, FinishedAt: finished}
		}, v1alpha1.TaskResultFailed, "TaskExitedNonZero"},
		{"exit 0 without a success event", func(s *v1alpha1.AgentSession) {
			s.Status.Agent.Task = &v1alpha1.TaskStatus{FinishedAt: finished}
		}, v1alpha1.TaskResultUnknown, "TaskResultMissing"},
		{"the stored result wins", func(s *v1alpha1.AgentSession) {
			s.Status.Agent.Task = success
			s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultTimedOut, Reason: "TaskTimedOut"}
		}, v1alpha1.TaskResultTimedOut, "TaskTimedOut"},
		{"a private session's retained terminal task", func(s *v1alpha1.AgentSession) {
			s.Status.Agent.Task = nil
			s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{TaskTerminal: &v1alpha1.PrivateTaskTerminalStatus{Task: *success}}
		}, v1alpha1.TaskResultSucceeded, "TaskSucceeded"},
		{"no task result outside task mode", func(s *v1alpha1.AgentSession) {
			s.Spec.Mode = v1alpha1.ModeRemote
			s.Status.Agent.Task = success
		}, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, pod := fixture(nil)
			c.edit(s)
			got := TaskResult(s)
			if c.state == "" {
				if got != nil {
					t.Fatalf("task result %+v, want none", got)
				}
				return
			}
			if got == nil || got.State != c.state || got.Reason != c.reason {
				t.Fatalf("task result %+v, want %s %s", got, c.state, c.reason)
			}
			// A derived result ends the session and decides its record at once,
			// in any phase.
			s.Status.Phase = v1alpha1.PhasePending
			if ended, why := SessionEnded(s, pod, now); !ended {
				t.Errorf("not ended (%q)", why)
			}
			if o := Outcome(s, now); o.Execution == nil || o.Execution.State != c.state {
				t.Errorf("outcome %+v", o)
			}
		})
	}
}
