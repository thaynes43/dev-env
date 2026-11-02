package controller

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func notificationFixture(t *testing.T) (*v1alpha1.AgentSession, *v1alpha1.AgentSession, *RescueIncidentOwners, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s := sessionWith("protected-bootstrap", v1alpha1.PhaseSuspended)
	s.UID, s.Generation = "11111111-1111-1111-1111-111111111111", 3
	s.Spec.Repo = "haynes-ops"
	s.Finalizers = []string{Finalizer}
	s.DeletionTimestamp = &metav1.Time{Time: now.Add(-24 * time.Hour)}
	rep := interruptedReport()
	rep.Session = s.Name
	r, reason := verdict(s, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "hold"}}, rep, metav1.NewTime(now))
	s.Status.Rescue = r
	setRescueCondition(s, &s.Status, reason)
	owner := sessionWith("native-owner", v1alpha1.PhaseRunning)
	owner.UID = "22222222-2222-2222-2222-222222222222"
	owner.Status.Agent = &v1alpha1.AgentStatus{Boot: "ready", Status: "busy", ConversationID: "33333333-3333-3333-3333-333333333333", LastHeartbeat: &metav1.Time{Time: now}}
	c := &RescueIncidentOwners{Schema: "dev-env.rescue-incident-owners.v1", Owners: []RescueIncidentOwner{{
		SessionUID: string(s.UID), Generation: s.Generation, Reason: reason, NotificationKey: r.NotificationKey, PendingDelete: true,
		OwnerSessionUID: string(owner.UID), OwnerConversationID: owner.Status.Agent.ConversationID,
		AcknowledgedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), AckRef: "native-ack:fixture", AckSHA256: strings.Repeat("a", 64),
	}}}
	return s, owner, c, now
}

func TestRescueNotificationRetryAndMaterialFacts(t *testing.T) {
	s, _, _, now := notificationFixture(t)
	rep := interruptedReport()
	rep.Session = s.Name
	first := missingCloneNotificationKey(s, rep, "BootstrapInterrupted")
	if len(first) != 64 {
		t.Fatal("qualified report has no key")
	}
	rep.Stamp = "later-retry"
	rep.FinishedAt = now.Add(time.Hour)
	if got := missingCloneNotificationKey(s, rep, "BootstrapInterrupted"); got != first {
		t.Fatal("retry clock changed material identity")
	}
	rep.NoClone.BootstrapReason = protocol.ReasonStopped
	if got := missingCloneNotificationKey(s, rep, "BootstrapInterrupted"); got == first || got == "" {
		t.Fatal("changed typed bootstrap cause was hidden")
	}
	rep.NoClone = nil // Older producers do not establish bootstrap or transcript facts.
	legacy := missingCloneNotificationKey(s, rep, "NotProven")
	if len(legacy) != 64 || legacy == first {
		t.Fatal("legacy proof absence must remain distinct")
	}
	for name, edit := range map[string]func(*protocol.RescueReport){
		"running":                 func(r *protocol.RescueReport) { r.Agent.Running = true },
		"unlisted agent":          func(r *protocol.RescueReport) { r.Agent = nil },
		"unknown malformed proof": func(r *protocol.RescueReport) { r.NoClone = &protocol.NoCloneProof{Version: 7} },
		"other repository work":   func(r *protocol.RescueReport) { r.Repos = []protocol.RepoRescue{{Path: "other"}} },
		"bundle":                  func(r *protocol.RescueReport) { r.Bundle = &protocol.BundleReport{Manifest: "somewhere"} },
		"not ok":                  func(r *protocol.RescueReport) { r.OK = false },
	} {
		t.Run(name, func(t *testing.T) {
			x := rep
			a := *rep.Agent
			x.Agent = &a
			edit(&x)
			if missingCloneNotificationKey(s, x, "NotProven") != "" {
				t.Fatal("unknown or additional risk inherited typed notification key")
			}
		})
	}
}

func TestRescueNotificationOwnerRequiresCurrentExactNativeEvidence(t *testing.T) {
	s, owner, c, now := notificationFixture(t)
	cond := condition(&s.Status, ConditionRescueFailed)
	if c.ownerFor(s, cond, []v1alpha1.AgentSession{*owner}, now) != string(owner.UID) {
		t.Fatal("exact live native acknowledgment did not match")
	}
	for name, edit := range map[string]func(*v1alpha1.AgentSession, *v1alpha1.AgentSession){
		"new session UID":       func(s, _ *v1alpha1.AgentSession) { s.UID = types.UID("44444444-4444-4444-4444-444444444444") },
		"new generation":        func(s, _ *v1alpha1.AgentSession) { s.Generation++ },
		"deletion risk changed": func(s, _ *v1alpha1.AgentSession) { s.DeletionTimestamp = nil },
		"new risk":              func(s, _ *v1alpha1.AgentSession) { s.Status.Rescue.NotificationKey = strings.Repeat("b", 64) },
		"unknown risk":          func(s, _ *v1alpha1.AgentSession) { s.Status.Rescue.NotificationKey = "" },
		"superseded":            func(s, _ *v1alpha1.AgentSession) { s.Status.Rescue.Superseded = true },
		"changed reason":        func(s, _ *v1alpha1.AgentSession) { condition(&s.Status, ConditionRescueFailed).Reason = "FetchFailed" },
		"new native context": func(_, o *v1alpha1.AgentSession) {
			o.Status.Agent.ConversationID = "44444444-4444-4444-4444-444444444444"
		},
		"exited native": func(_, o *v1alpha1.AgentSession) { o.Status.Agent.Status = "exited" },
		"stale heartbeat": func(_, o *v1alpha1.AgentSession) {
			o.Status.Agent.LastHeartbeat = &metav1.Time{Time: now.Add(-4 * time.Minute)}
		},
		"missing heartbeat":       func(_, o *v1alpha1.AgentSession) { o.Status.Agent.LastHeartbeat = nil },
		"terminal task":           func(_, o *v1alpha1.AgentSession) { o.Status.Agent.Task = &v1alpha1.TaskStatus{} },
		"owner deleting":          func(_, o *v1alpha1.AgentSession) { o.DeletionTimestamp = &metav1.Time{Time: now} },
		"pod ready without agent": func(_, o *v1alpha1.AgentSession) { o.Status.Agent = nil },
	} {
		t.Run(name, func(t *testing.T) {
			x, o := s.DeepCopy(), owner.DeepCopy()
			edit(x, o)
			if c.ownerFor(x, condition(&x.Status, ConditionRescueFailed), []v1alpha1.AgentSession{*o}, now) != "" {
				t.Fatal("stale or mismatched ownership lowered notification urgency")
			}
		})
	}
	for _, at := range []time.Time{now.Add(-2 * time.Minute), c.Owners[0].ExpiresAt} {
		if c.ownerFor(s, cond, []v1alpha1.AgentSession{*owner}, at) != "" {
			t.Fatal("future or expired acknowledgment accepted")
		}
	}
}

func TestRescueNotificationConfigurationIsBoundedAndClosed(t *testing.T) {
	_, _, c, _ := notificationFixture(t)
	b, _ := json.Marshal(c)
	if _, err := ParseRescueIncidentOwners(b); err != nil {
		t.Fatal(err)
	}
	for _, b := range [][]byte{
		[]byte(`{"schema":"dev-env.rescue-incident-owners.v1","owners":[],"surprise":true}`),
		append(b, []byte(` {}`)...), []byte(strings.Repeat(" ", (32<<10)+1)),
	} {
		if _, err := ParseRescueIncidentOwners(b); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	for name, edit := range map[string]func(*RescueIncidentOwners){
		"permanent":      func(x *RescueIncidentOwners) { x.Owners[0].ExpiresAt = x.Owners[0].AcknowledgedAt.Add(25 * time.Hour) },
		"missing native": func(x *RescueIncidentOwners) { x.Owners[0].OwnerConversationID = "" },
		"duplicate":      func(x *RescueIncidentOwners) { x.Owners = append(x.Owners, x.Owners[0]) },
		"unknown key":    func(x *RescueIncidentOwners) { x.Owners[0].NotificationKey = "unclassified" },
		"missing ack":    func(x *RescueIncidentOwners) { x.Owners[0].AckSHA256 = "" },
	} {
		t.Run(name, func(t *testing.T) {
			x := *c
			x.Owners = append([]RescueIncidentOwner(nil), c.Owners...)
			edit(&x)
			b, _ := json.Marshal(x)
			if _, err := ParseRescueIncidentOwners(b); err == nil {
				t.Fatal("invalid acknowledgment accepted")
			}
		})
	}
}

func TestRescueNotificationOwnershipCannotReleasePendingDeletion(t *testing.T) {
	s, owner, c, now := notificationFixture(t)
	before := s.DeepCopy()
	if c.ownerFor(s, condition(&s.Status, ConditionRescueFailed), []v1alpha1.AgentSession{*owner}, now) == "" {
		t.Fatal("fixture is not owned")
	}
	if !reflect.DeepEqual(s, before) || rescued(s) || s.Status.Rescue.Result != v1alpha1.RescueFailed ||
		condition(&s.Status, ConditionRescueFailed).Status != metav1.ConditionTrue || len(s.Finalizers) == 0 {
		t.Fatal("notification ownership changed preservation/lifecycle")
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "hold", Labels: map[string]string{v1alpha1.LabelHold: "true"}}}
	if podRemovalAllowed(s, pod) == nil {
		t.Fatal("owned failed bootstrap released its pending-delete hold")
	}
	if volumeRemovalAllowed(s, false, true) == nil {
		t.Fatal("owned failed bootstrap authorized PVC removal")
	}
}
