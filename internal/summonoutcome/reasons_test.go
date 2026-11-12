package summonoutcome

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Deferred inputs already occupy their design row; their future producers
// cannot displace a higher-priority durable input or add another ordering.
// Ruling D1 (item 741 comment 7355, OP779): a recorded stop sits above every
// row but the two start failures; the old last row is gone.
func TestFirstReasonOrder(t *testing.T) {
	in := Inputs{TaskResult: &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultTimedOut}, AgentStatus: "exited", NoUsableModel: true, PodTerminated: true, SessionLost: true, NeverStarted: true, Deadline: true, HeartbeatLost: true, Stale: true, EscalationBound: true, HoldExpired: true, QuotaHoldExpired: true}
	for _, step := range []struct {
		want   string
		remove func()
	}{
		{ReasonTimedOut, func() { in.TaskResult = nil }},
		{ReasonAgentExited, func() { in.AgentStatus = "failed" }},
		{ReasonNoUsableModel, func() { in.NoUsableModel = false }},
		{ReasonAgentFailed, func() { in.AgentStatus = "interrupted" }},
		{ReasonAgentInterrupted, func() { in.AgentStatus = "busy" }},
		{ReasonPodTerminated, func() { in.PodTerminated = false }},
		{ReasonSessionLost, func() { in.SessionLost = false }},
		{ReasonNeverStarted, func() { in.NeverStarted = false }},
		{ReasonDeadline, func() { in.Deadline = false }},
		{ReasonHeartbeatLost, func() { in.HeartbeatLost = false }},
		{ReasonStale, func() { in.Stale = false }},
		{ReasonEscalationBound, func() { in.EscalationBound = false }},
		{ReasonQuotaHoldExpired, func() { in.QuotaHoldExpired = false }},
		{ReasonHoldExpired, func() { in.HoldExpired = false }},
	} {
		if got := FirstReason(in); got != step.want {
			t.Fatalf("reason %q, want %q", got, step.want)
		}
		step.remove()
	}
	if got := FirstReason(in); got != "" {
		t.Fatalf("no input returned %q", got)
	}
}

// Ruling D1, row by row: a stop recorded before the close wins over rows 1
// and 2 and 5 onward; rows 3 and 4, the start failures, keep winning. A
// person's stop is reason stopped; any other stop is suspended-unreported.
func TestFirstReasonStopRows(t *testing.T) {
	rows := []struct {
		name  string
		in    Inputs
		start string // the start failure that keeps winning, or ""
	}{
		{"task-result", Inputs{TaskResult: &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded}}, ""},
		{"exited", Inputs{AgentStatus: "exited"}, ""},
		{"no-usable-model", Inputs{NoUsableModel: true}, ReasonNoUsableModel},
		{"agent-failed", Inputs{AgentStatus: "failed"}, ReasonAgentFailed},
		{"interrupted", Inputs{AgentStatus: "interrupted"}, ""},
		{"pod-terminated", Inputs{PodTerminated: true}, ""},
		{"session-lost", Inputs{SessionLost: true}, ""},
		{"never-started", Inputs{NeverStarted: true}, ""},
		{"deadline", Inputs{Deadline: true}, ""},
		{"heartbeat-lost", Inputs{HeartbeatLost: true}, ""},
		{"stale", Inputs{Stale: true}, ""},
		{"escalation-bound", Inputs{EscalationBound: true}, ""},
		{"not-charged", Inputs{NotCharged: true}, ""},
		{"quota-hold-expired", Inputs{QuotaHoldExpired: true}, ""},
		{"hold-expired", Inputs{HoldExpired: true}, ""},
		{"nothing", Inputs{}, ""},
	}
	for _, row := range rows {
		for _, stop := range []struct {
			name string
			set  func(*Inputs)
			want string
		}{
			{"person", func(in *Inputs) { in.StoppedByPerson = true }, ReasonStopped},
			{"other", func(in *Inputs) { in.Suspended = true }, ReasonSuspendedUnreported},
			{"both", func(in *Inputs) { in.StoppedByPerson, in.Suspended = true, true }, ReasonStopped},
		} {
			t.Run(row.name+"/"+stop.name, func(t *testing.T) {
				in := row.in
				stop.set(&in)
				want := stop.want
				if row.start != "" {
					want = row.start
				}
				if got := FirstReason(in); got != want {
					t.Fatalf("reason %q, want %q", got, want)
				}
			})
		}
	}
	if len(ReasonStopped) > 32 {
		t.Fatal("closed.reason is at most 32 characters")
	}
}

func TestTurnCapRequiresResultReason(t *testing.T) {
	for _, tc := range []struct{ reason, want string }{{"TaskTurnCapReached", ReasonTurnCap}, {"turn-cap", ReasonTurnCap}, {"TaskReportedError", ReasonAgentExited}, {"TaskExitedNonZero", ReasonAgentExited}, {"", ReasonAgentExited}} {
		got := FirstReason(Inputs{TaskResult: &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultFailed, Reason: tc.reason, Message: "reached the turn cap"}})
		if got != tc.want {
			t.Fatalf("reason %q became %q", tc.reason, got)
		}
	}
}

func TestEscalationNameBound(t *testing.T) {
	for _, tc := range []struct{ name, want string }{{"rem-example", "esc-rem-example"}, {strings.Repeat("a", 56), "esc-" + strings.Repeat("a", 56)}, {strings.Repeat("a", 57), "esc-rem-ba7816bf"}} {
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: tc.name, UID: "abc"}}
		if got := EscalationName(s); got != tc.want {
			t.Fatalf("name %q, want %q", got, tc.want)
		}
	}
}

// OP777: not-charged is the leader's ledger evidence. It sits after the agent
// and watchdog rows and before the hold rows, and no other input displaces a
// row above it.
func TestNotChargedRow(t *testing.T) {
	if got := FirstReason(Inputs{NotCharged: true, HoldExpired: true, QuotaHoldExpired: true}); got != ReasonNotCharged {
		t.Fatalf("reason %q", got)
	}
	if got := FirstReason(Inputs{NotCharged: true, EscalationBound: true}); got != ReasonEscalationBound {
		t.Fatalf("reason %q", got)
	}
	if len(ReasonNotCharged) > 32 {
		t.Fatal("closed.reason is at most 32 characters")
	}
}
