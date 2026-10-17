package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
)

func neverStartedReleaseSession() *v1alpha1.AgentSession {
	s, _ := privateHistoryFixture()
	at := metav1.Now()
	s.DeletionTimestamp = &at
	s.Status.Rescue = nil
	h := s.Status.PrivateHistory
	h.Admission = sharedNeverStarted
	h.HomeUID = ""
	h.ExecutorAttempted = false
	h.ExecutorIntent = ""
	h.ExecutorNotCreated = false
	h.LastExecutor = nil
	h.ExecutorTerminal = nil
	h.ExecutorMissed = nil
	h.StopRequest = nil
	h.Expiry = nil
	h.ExpiryClearedAt = nil
	return s
}

func TestNeverStartedReleasePredicate(t *testing.T) {
	if err := neverStartedReleaseAllowed(neverStartedReleaseSession()); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*v1alpha1.AgentSession)
	}{
		{"live", func(s *v1alpha1.AgentSession) { s.DeletionTimestamp = nil }},
		{"shared workspace", func(s *v1alpha1.AgentSession) {
			s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
		}},
		{"canary", func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{} }},
		{"pilot", func(s *v1alpha1.AgentSession) {
			s.Annotations = map[string]string{projectcanary.PilotAnnotation: "marked"}
		}},
		{"unknown admission", func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory.Admission = "Unknown" }},
		{"started", func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory.Admission = sharedStarted }},
		{"recorded home", func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory.HomeUID = "home-1" }},
		{"executor attempted", func(s *v1alpha1.AgentSession) {
			s.Status.PrivateHistory.ExecutorAttempted = true
			s.Status.PrivateHistory.ExecutorIntent = "intent"
		}},
		{"known non-attempt", func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory.ExecutorNotCreated = true }},
		{"last executor", func(s *v1alpha1.AgentSession) {
			s.Status.PrivateHistory.LastExecutor = &v1alpha1.PrivateExecutorStatus{PodUID: "pod"}
		}},
		{"terminal record", func(s *v1alpha1.AgentSession) {
			s.Status.PrivateHistory.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{}
		}},
		{"missed executor", func(s *v1alpha1.AgentSession) {
			s.Status.PrivateHistory.ExecutorMissed = &v1alpha1.PrivateExecutorMissedStatus{}
		}},
		{"stop request", func(s *v1alpha1.AgentSession) {
			s.Status.PrivateHistory.StopRequest = &v1alpha1.PrivateExecutorStopRequestStatus{}
		}},
		{"expiry", func(s *v1alpha1.AgentSession) {
			s.Status.PrivateHistory.Expiry = &v1alpha1.PrivateHomeExpiryStatus{}
		}},
		{"cleared expiry", func(s *v1alpha1.AgentSession) {
			at := metav1.Now()
			s.Status.PrivateHistory.ExpiryClearedAt = &at
		}},
		{"missing history", func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory = nil }},
		{"missing finalizer", func(s *v1alpha1.AgentSession) { s.Finalizers = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := neverStartedReleaseSession()
			tc.edit(s)
			if err := neverStartedReleaseAllowed(s); err == nil {
				t.Fatal("predicate allowed a Session outside section 23")
			}
		})
	}
}

func TestNeverStartedReleaseDoesNotLiftTheFinalizer(t *testing.T) {
	s := neverStartedReleaseSession()
	if err := neverStartedReleaseAllowed(s); err != nil {
		t.Fatal(err)
	}
	c := privateHistoryClient(s)
	r := &Reconciler{Client: c, APIReader: c}
	allowed, err := r.releaseIfEmpty(context.Background(), s)
	if allowed || err == nil {
		t.Fatal("releaseIfEmpty lifted a never-started Session before it consults the predicate")
	}
	var got v1alpha1.AgentSession
	if getErr := c.Get(context.Background(), client.ObjectKeyFromObject(s), &got); getErr != nil {
		t.Fatal(getErr)
	}
	if !controllerutil.ContainsFinalizer(&got, Finalizer) {
		t.Fatal("finalizer was removed")
	}
	if err := releaseSession(context.Background(), c, s); err == nil {
		t.Fatal("releaseSession bypassed the private-history guard")
	}
}
