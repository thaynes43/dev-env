package summonoutcome

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Deferred inputs already occupy their design row; their future producers
// cannot displace a higher-priority durable input or add another ordering.
func TestFirstReasonOrder(t *testing.T) {
	in := Inputs{TaskResult: &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultTimedOut}, AgentStatus: "exited", NoUsableModel: true, PodTerminated: true, SessionLost: true, NeverStarted: true, Deadline: true, HeartbeatLost: true, Stale: true, EscalationBound: true, HoldExpired: true, QuotaHoldExpired: true, Suspended: true}
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
		{ReasonSuspendedUnreported, func() { in.Suspended = false }},
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
	if got := FirstReason(Inputs{NotCharged: true, HoldExpired: true, QuotaHoldExpired: true, Suspended: true}); got != ReasonNotCharged {
		t.Fatalf("reason %q", got)
	}
	if got := FirstReason(Inputs{NotCharged: true, EscalationBound: true}); got != ReasonEscalationBound {
		t.Fatalf("reason %q", got)
	}
	if len(ReasonNotCharged) > 32 {
		t.Fatal("closed.reason is at most 32 characters")
	}
}
