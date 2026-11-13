package summonledger

import (
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const queueTestCaller = "upgrade-agent.alert-responder"

func queueTestPolicy() *v1alpha1.CallerPolicy {
	entry := func(kind v1alpha1.SummonKind, lane v1alpha1.Lane, p v1alpha1.SummonPriority) v1alpha1.CallerPolicySummonSession {
		return v1alpha1.CallerPolicySummonSession{Kind: kind, Lane: lane, Priority: p}
	}
	return &v1alpha1.CallerPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: queueTestCaller},
		Spec: v1alpha1.CallerPolicySpec{Summon: &v1alpha1.CallerPolicySummon{Sessions: []v1alpha1.CallerPolicySummonSession{
			entry(v1alpha1.SummonKindTask, v1alpha1.LaneRemediation, v1alpha1.SummonPriorityUrgent),
			entry(v1alpha1.SummonKindRemote, v1alpha1.LaneEscalation, v1alpha1.SummonPriorityUrgent),
			entry(v1alpha1.SummonKindRemote, v1alpha1.LaneUpgrade, v1alpha1.SummonPriorityNormal),
			entry(v1alpha1.SummonKindTask, v1alpha1.LaneCuration, v1alpha1.SummonPriorityNormal),
		}}},
	}
}

func queueTestSession(name, caller string, lane v1alpha1.Lane, p v1alpha1.SummonPriority, st *v1alpha1.SummonStatus) v1alpha1.AgentSession {
	return v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)},
		Spec: v1alpha1.AgentSessionSpec{Caller: caller, Lane: lane,
			Summon: &v1alpha1.SessionSummon{DispatchID: "id-" + name, Priority: p}},
		Status: v1alpha1.AgentSessionStatus{Summon: st},
	}
}

// The count of each queue, from one ledger write's reads: the caller's Queued
// sessions in it, and its charges in it with no Session and an attempt mark
// younger than AttemptHold, skipping the dispatch being decided.
func TestQueueCount(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	at := metav1.NewTime(now.Add(-time.Hour))
	p := queueTestPolicy()
	urgent, normal := v1alpha1.SummonPriorityUrgent, v1alpha1.SummonPriorityNormal
	rem, esc, up, cur := v1alpha1.LaneRemediation, v1alpha1.LaneEscalation, v1alpha1.LaneUpgrade, v1alpha1.LaneCuration
	sessions := []v1alpha1.AgentSession{
		queueTestSession("q-rem", queueTestCaller, rem, urgent, nil),
		queueTestSession("q-esc", queueTestCaller, esc, urgent, nil),
		queueTestSession("q-up", queueTestCaller, up, normal, nil),
		queueTestSession("q-cur", queueTestCaller, cur, normal, &v1alpha1.SummonStatus{}),
		queueTestSession("admitted", queueTestCaller, rem, urgent, &v1alpha1.SummonStatus{AdmittedAt: &at}),
		queueTestSession("granted", queueTestCaller, esc, urgent, nil),
		queueTestSession("closed", queueTestCaller, rem, urgent, &v1alpha1.SummonStatus{Closed: &v1alpha1.SummonClosedStatus{At: at}}),
		queueTestSession("other-caller", "someone.else", rem, urgent, nil),
		queueTestSession("skipped", queueTestCaller, rem, urgent, nil),
		{ObjectMeta: metav1.ObjectMeta{Name: "not-summoned"}, Spec: v1alpha1.AgentSessionSpec{Caller: queueTestCaller, Lane: rem}},
	}
	byID := map[string]*v1alpha1.AgentSession{}
	for i := range sessions {
		if s := sessions[i].Spec.Summon; s != nil {
			byID[s.DispatchID] = &sessions[i]
		}
	}
	grants := Grants{Items: []Grant{{Name: "granted", UID: "uid-granted", Lane: "escalation", Caller: queueTestCaller, Pool: PoolEscalations, At: now}}}
	fresh, stale := now.Add(-time.Minute), now.Add(-AttemptHold)
	charge := func(id string, attempt time.Time, mode, lane, requested string, prio v1alpha1.SummonPriority, name string) Charge {
		return Charge{ID: id, At: attempt, Attempt: attempt, Hash: RequestHash(mode, lane, requested, prio), Name: name}
	}
	charges := []Charge{
		charge("c-rem", fresh, "task", "remediation", "rem-x", urgent, "rem-x"),
		charge("c-rem-suffix", fresh, "task", "remediation", "rem-y", urgent, "rem-y-3"),
		charge("c-esc", fresh, "remote", "escalation", "esc-x", urgent, "esc-x"),
		charge("c-up", fresh, "remote", "upgrade", "wo-x", normal, "wo-x"),
		{ID: "c-unknown", At: fresh, Attempt: fresh, Hash: strings.Repeat("0", 64), Name: "gone-entry"},
		charge("c-stale", stale, "task", "remediation", "rem-z", urgent, "rem-z"),
		charge("id-q-rem", fresh, "task", "remediation", "q-rem", urgent, "q-rem"),
	}
	normalQ, urgentQ, escalationsQ := Queue{}, Queue{Urgent: true}, Queue{Urgent: true, Escalations: true}
	for _, tc := range []struct {
		name  string
		q     Queue
		skip  string
		want  int
		items string
	}{
		{"normal", normalQ, "id-skipped", 4, "q-up q-cur c-up c-unknown"},
		{"urgent, concurrent pool", urgentQ, "id-skipped", 4, "q-rem c-rem c-rem-suffix c-unknown"},
		{"urgent, escalations pool", escalationsQ, "id-skipped", 3, "q-esc c-esc c-unknown"},
		{"urgent, concurrent pool, nothing skipped", urgentQ, "", 5, "and the session skipped"},
		{"urgent, escalations pool, a charge skipped", escalationsQ, "c-esc", 2, "q-esc c-unknown"},
	} {
		if got := QueueCount(queueTestCaller, p, charges, sessions, byID, grants, tc.q, tc.skip, now); got != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, got, tc.want, tc.items)
		}
	}
}

// With no policy, a policy that is gone, QueueCount does not dereference it:
// a charge with no Session and a young attempt mark counts in every queue,
// even one whose hash a policy entry would place in one queue, and the
// caller's Queued sessions still count by their own priority and lane.
func TestQueueCountNilPolicy(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute)
	sessions := []v1alpha1.AgentSession{
		queueTestSession("q-rem", queueTestCaller, v1alpha1.LaneRemediation, v1alpha1.SummonPriorityUrgent, nil),
		queueTestSession("other-caller", "someone.else", v1alpha1.LaneUpgrade, v1alpha1.SummonPriorityNormal, nil),
	}
	byID := map[string]*v1alpha1.AgentSession{"id-q-rem": &sessions[0], "id-other-caller": &sessions[1]}
	charges := []Charge{{ID: "c-esc", At: fresh, Attempt: fresh,
		Hash: RequestHash("remote", "escalation", "esc-x", v1alpha1.SummonPriorityUrgent), Name: "esc-x"}}
	for _, tc := range []struct {
		name string
		q    Queue
		want int
	}{
		{"normal", Queue{}, 1},
		{"urgent, concurrent pool", Queue{Urgent: true}, 2},
		{"urgent, escalations pool", Queue{Urgent: true, Escalations: true}, 1},
	} {
		if got := QueueCount(queueTestCaller, nil, charges, sessions, byID, Grants{}, tc.q, "", now); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

// A charge counts in the queue of the entry its hash names, and in every
// queue, in this order, when no entry does.
func TestChargeQueues(t *testing.T) {
	p := queueTestPolicy()
	h := RequestHash("remote", "escalation", "esc-1", v1alpha1.SummonPriorityUrgent)
	if got := ChargeQueues(p, h, "esc-1-7"); !slices.Equal(got, []Queue{{Urgent: true, Escalations: true}}) {
		t.Fatalf("escalation: %v", got)
	}
	all := []Queue{{}, {Urgent: true}, {Urgent: true, Escalations: true}}
	if got := ChargeQueues(p, strings.Repeat("0", 64), "esc-1"); !slices.Equal(got, all) || !slices.Equal(AllQueues(), all) {
		t.Fatalf("no entry: %v, AllQueues %v", got, AllQueues())
	}
	if got := ChargeQueues(nil, h, "esc-1"); !slices.Equal(got, all) {
		t.Fatalf("no policy: %v", got)
	}
}
