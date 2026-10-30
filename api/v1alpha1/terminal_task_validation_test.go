package v1alpha1_test

import (
	"strings"
	"testing"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestTerminalTaskObservationIsImmutableBoundAndPreserved(t *testing.T) {
	s := taskSession()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	at := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	source := &v1alpha1.UsageSourceStatus{SessionUID: string(s.UID), PodUID: "original-pod", Provider: string(s.Spec.Agent), RequestedModel: s.Spec.Model, AttemptID: strings.Repeat("a", 64)}
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: "original-home", HomeUID: "original-home-uid", BindingGeneration: s.Generation, Admission: "Started", ExecutorAttempted: true, ExecutorIntent: "original-intent", Provider: string(s.Spec.Agent), StopState: "Unknown", LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: source.PodUID, PodName: s.Name, Generation: s.Generation}}
	base := s.DeepCopy()
	record := &v1alpha1.PrivateTaskTerminalStatus{Task: v1alpha1.TaskStatus{ExitSource: source, ExitCode: 0, Subtype: "success", FinishedAt: at}, ObservedAt: at, DueAt: metav1.NewTime(at.Add(time.Minute))}
	for _, test := range []struct {
		name string
		edit func(*v1alpha1.PrivateTaskTerminalStatus)
	}{
		{"missing-source", func(r *v1alpha1.PrivateTaskTerminalStatus) { r.Task.ExitSource = nil }},
		{"wrong-pod", func(r *v1alpha1.PrivateTaskTerminalStatus) { r.Task.ExitSource.PodUID = "other-pod" }},
		{"wrong-session", func(r *v1alpha1.PrivateTaskTerminalStatus) { r.Task.ExitSource.SessionUID = "other-session" }},
		{"wrong-provider", func(r *v1alpha1.PrivateTaskTerminalStatus) { r.Task.ExitSource.Provider = "codex" }},
		{"future-result", func(r *v1alpha1.PrivateTaskTerminalStatus) { r.Task.FinishedAt = metav1.NewTime(at.Add(time.Hour)) }},
		{"no-grace", func(r *v1alpha1.PrivateTaskTerminalStatus) { r.DueAt = at }},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := base.DeepCopy()
			bad.Status.PrivateHistory.TaskTerminal = record.DeepCopy()
			test.edit(bad.Status.PrivateHistory.TaskTerminal)
			if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
				t.Fatalf("invalid task record accepted: %v", err)
			}
		})
	}
	s.Status.PrivateHistory.TaskTerminal = record.DeepCopy()
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	got := get(t, s)
	if !apiequality.Semantic.DeepEqual(record, got.Status.PrivateHistory.TaskTerminal) {
		t.Fatal("terminal task record pruned")
	}
	for _, test := range []struct {
		name string
		edit func(*v1alpha1.PrivateHistoryStatus)
	}{
		{"remove", func(h *v1alpha1.PrivateHistoryStatus) { h.TaskTerminal = nil }},
		{"change-result", func(h *v1alpha1.PrivateHistoryStatus) { h.TaskTerminal.Task.ExitCode = 1 }},
		{"restart-grace", func(h *v1alpha1.PrivateHistoryStatus) { h.TaskTerminal.DueAt = metav1.NewTime(at.Add(time.Hour)) }},
		{"change-attempt", func(h *v1alpha1.PrivateHistoryStatus) {
			h.TaskTerminal.Task.ExitSource.AttemptID = strings.Repeat("b", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := get(t, s)
			test.edit(bad.Status.PrivateHistory)
			if err := k8s.Status().Update(ctx(t), bad); !apierrors.IsInvalid(err) {
				t.Fatalf("immutable task record changed: %v", err)
			}
		})
	}
	got.Status.Agent = nil
	if err := k8s.Status().Update(ctx(t), got); err != nil {
		t.Fatal(err)
	}
	if !apiequality.Semantic.DeepEqual(record, get(t, s).Status.PrivateHistory.TaskTerminal) {
		t.Fatal("heartbeat loss erased outcome")
	}
}
