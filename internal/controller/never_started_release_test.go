package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
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

func TestNeverStartedReleaseLiftsFinalizerOnlyWhenPodAndHomeAreAbsent(t *testing.T) {
	ctx := context.Background()
	stored := func(t *testing.T, c client.Client, s *v1alpha1.AgentSession) *v1alpha1.AgentSession {
		t.Helper()
		var fresh v1alpha1.AgentSession
		if err := c.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
			t.Fatal(err)
		}
		if fresh.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(&fresh, Finalizer) {
			t.Fatal("stored Session lost its reap timestamp or finalizer")
		}
		return &fresh
	}
	finalizerGone := func(t *testing.T, c client.Client, s *v1alpha1.AgentSession) {
		t.Helper()
		var got v1alpha1.AgentSession
		if err := c.Get(ctx, client.ObjectKeyFromObject(s), &got); err != nil {
			t.Fatal(err)
		}
		if controllerutil.ContainsFinalizer(&got, Finalizer) {
			t.Fatal("finalizer stayed after both uncached reads were NotFound")
		}
	}

	s := neverStartedReleaseSession()
	c := privateHistoryClient(s)
	fresh := stored(t, c, s)
	r := &Reconciler{Client: c, APIReader: c}
	allowed, err := r.releaseIfEmpty(ctx, fresh)
	if !allowed || err != nil {
		t.Fatalf("absent Pod and HOME: released=%v err=%v", allowed, err)
	}
	finalizerGone(t, c, fresh)
	if err := releaseSession(ctx, c, fresh); err != nil {
		t.Fatal(err)
	}

	s = neverStartedReleaseSession()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "still-here"}}
	c = privateHistoryClient(s, pod)
	fresh = stored(t, c, s)
	r = &Reconciler{Client: c, APIReader: c}
	if allowed, err = r.releaseIfEmpty(ctx, fresh); allowed || err != nil {
		t.Fatalf("present Pod: released=%v err=%v", allowed, err)
	}
	if !controllerutil.ContainsFinalizer(fresh, Finalizer) {
		t.Fatal("present Pod lost the finalizer")
	}

	s = neverStartedReleaseSession()
	home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, UID: "home-kept", Finalizers: []string{Finalizer}}}
	c = privateHistoryClient(s, home)
	fresh = stored(t, c, s)
	r = &Reconciler{Client: c, APIReader: c}
	if allowed, err = r.releaseIfEmpty(ctx, fresh); allowed || err != nil {
		t.Fatalf("present HOME: released=%v err=%v", allowed, err)
	}
	var kept corev1.PersistentVolumeClaim
	if err := c.Get(ctx, client.ObjectKeyFromObject(home), &kept); err != nil || !kept.DeletionTimestamp.IsZero() {
		t.Fatal("present HOME was deleted")
	}

	started, _ := privateHistoryFixture()
	at := metav1.Now()
	started.DeletionTimestamp = &at
	c = privateHistoryClient(started)
	fresh = stored(t, c, started)
	r = &Reconciler{Client: c, APIReader: c}
	if allowed, err = r.releaseIfEmpty(ctx, fresh); allowed || !errors.Is(err, errPrivateHistoryRetained) {
		t.Fatalf("started Session: released=%v err=%v", allowed, err)
	}

	s = neverStartedReleaseSession()
	home = &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, UID: "home-kept", Finalizers: []string{Finalizer}}}
	c = privateHistoryClient(s, home)
	r = &Reconciler{Client: c, APIReader: c}
	obs := observation{claim: owned[*corev1.PersistentVolumeClaim]{obj: home}}
	if _, done, err := r.archive(ctx, s, &obs, true); done || err != nil || obs.removalBlocked == nil {
		t.Fatalf("archive with a HOME: done=%v err=%v blocked=%v", done, err, obs.removalBlocked)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(home), &kept); err != nil || !kept.DeletionTimestamp.IsZero() {
		t.Fatal("archive deleted a HOME")
	}

	s = neverStartedReleaseSession()
	c = privateHistoryClient(s)
	fresh = stored(t, c, s)
	r = &Reconciler{Client: c, APIReader: c}
	obs = observation{claim: owned[*corev1.PersistentVolumeClaim]{missing: true}}
	if _, done, err := r.archive(ctx, fresh, &obs, true); !done || err != nil {
		t.Fatalf("archive with no HOME: done=%v err=%v", done, err)
	}
	finalizerGone(t, c, fresh)
}
