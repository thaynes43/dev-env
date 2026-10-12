package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

func privateHistoryFixture() (*v1alpha1.AgentSession, *corev1.PersistentVolumeClaim) {
	f := stoppedWorkspaceFixture()
	s, home := f.s, f.home
	s.Spec.Workspace = nil
	s.Status.SharedAdmission = nil
	s.Status.SharedPrivateHomeUID = ""
	s.Status.PodName = ""
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: home.Name,
		HomeUID: string(home.UID), BindingGeneration: s.Generation, Admission: sharedStarted, Provider: string(s.Spec.Agent), StopState: "Unknown",
		LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "original-executor", PodName: s.Name, NodeName: "worker-a", Revision: "original-template", Generation: s.Generation, ConversationID: "synthetic-conversation"}}
	s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueVerified, Generation: s.Generation, PodUID: "original-executor", Stamp: "20261009-1200"}
	s.Status.SuspendedAt = &metav1.Time{Time: time.Now().Add(-30 * 24 * time.Hour)}
	home.Annotations = map[string]string{privateHistoryAdmissionAnnotation: "3", "fixture/untouched": "unchanged"}
	home.Spec.VolumeName = "original-provider-home"
	return s, home
}

func privateHistoryClient(objects ...client.Object) client.WithWatch {
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}, &corev1.Pod{}).WithObjects(objects...).Build()
}

func TestPrivateHistoryRetainsOriginalPodBeforeLegacyCleanup(t *testing.T) {
	for _, lifecycle := range []string{"suspend", "reap"} {
		for _, state := range []string{"terminal", "never-started", "running", "running-rescued", "unknown-home", "unknown-causal", "unknown-admission", "canary-missing-history"} {
			t.Run(lifecycle+"-"+state, func(t *testing.T) {
				s, home := privateHistoryFixture()
				s.Status.Rescue = nil
				h := s.Status.PrivateHistory
				h.ExecutorAttempted, h.ExecutorIntent = true, "original-intent"
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "original-executor",
					OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Labels: map[string]string{v1alpha1.LabelRevision: "original-template"},
					Annotations: map[string]string{privateHistoryExecutorAnnotation: h.ExecutorIntent, privateHistoryHomeAnnotation: h.HomeUID}},
					Spec: corev1.PodSpec{NodeName: "worker-a", RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: ContainerName}},
						Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: home.Name}}}}},
					Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
				switch state {
				case "never-started", "unknown-admission":
					pod.Status = corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName,
						State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}}}
				case "running", "running-rescued", "unknown-causal":
					pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName,
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
				}
				switch state {
				case "running-rescued":
					s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueVerified, Generation: s.Generation, PodUID: string(pod.UID)}
				case "unknown-home":
					home.UID = "unconfirmed-home"
				case "unknown-causal":
					pod.Annotations[privateHistoryExecutorAnnotation] = "unconfirmed-intent"
				case "unknown-admission":
					h.Admission, h.HomeUID, h.ExecutorIntent, h.ExecutorAttempted = "Unknown", "", "", false
				case "canary-missing-history":
					s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{}
					s.Status.PrivateHistory = nil
				}
				if lifecycle == "reap" {
					at := metav1.Now()
					s.DeletionTimestamp = &at
				}
				originalSession := s.DeepCopy()
				base := privateHistoryClient(s, home, pod)
				originalPod, originalHome := pod.DeepCopy(), home.DeepCopy()
				ctx := context.Background()
				if err := base.Get(ctx, client.ObjectKeyFromObject(pod), originalPod); err != nil {
					t.Fatal(err)
				}
				if err := base.Get(ctx, client.ObjectKeyFromObject(home), originalHome); err != nil {
					t.Fatal(err)
				}
				if lifecycle == "suspend" && state == "terminal" {
					fixture, err := json.Marshal(struct {
						Session *v1alpha1.AgentSession
						Pod     *corev1.Pod
						Home    *corev1.PersistentVolumeClaim
					}{originalSession, originalPod, originalHome})
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("preserved synthetic fixture: %s", fixture)
				}
				var deletedPodUIDs []string
				deletes := 0
				c := interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
					deletes++
					if p, ok := o.(*corev1.Pod); ok {
						deletedPodUIDs = append(deletedPodUIDs, string(p.UID))
					}
					// Forward the delete: the pre-fix regression proves actual loss.
					return c.Delete(ctx, o, opts...)
				}})
				rescue := &fakeRescuer{calls: map[string]int{}}
				for range 2 {
					// Reconstruct with retention/canary flags off: the latch must suffice.
					r := &Reconciler{Client: c, APIReader: base, Rescuer: rescue}
					if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
						t.Fatal(err)
					}
				}
				var currentPod corev1.Pod
				if err := base.Get(ctx, client.ObjectKeyFromObject(pod), &currentPod); err != nil {
					t.Fatalf("original Pod UID %s was lost: %v; forwarded delete UIDs=%v, deletes=%d", originalPod.UID, err, deletedPodUIDs, deletes)
				}
				var currentHome corev1.PersistentVolumeClaim
				var currentSession v1alpha1.AgentSession
				if err := base.Get(ctx, client.ObjectKeyFromObject(home), &currentHome); err != nil {
					t.Fatal(err)
				}
				if err := base.Get(ctx, client.ObjectKeyFromObject(s), &currentSession); err != nil {
					t.Fatal(err)
				}
				if deletes != 0 || rescue.callsFor(pod.Name) != 0 || !apiequality.Semantic.DeepEqual(originalPod, &currentPod) || !apiequality.Semantic.DeepEqual(originalHome, &currentHome) {
					t.Fatalf("retained Pod/HOME changed or legacy cleanup ran: deletes=%d, rescues=%d", deletes, rescue.callsFor(pod.Name))
				}
				if currentSession.UID != originalSession.UID || !apiequality.Semantic.DeepEqual(currentSession.Spec, originalSession.Spec) || !apiequality.Semantic.DeepEqual(currentSession.Finalizers, originalSession.Finalizers) ||
					!apiequality.Semantic.DeepEqual(currentSession.Status.Rescue, originalSession.Status.Rescue) || currentSession.Status.ArchivedAt != nil || currentSession.Status.PrivateHistory.StopState != "Unknown" {
					t.Fatal("retention changed Session identity/rescue or claimed stop/archive")
				}
				if state == "canary-missing-history" {
					if currentSession.Status.PrivateHistory.Admission != "Unknown" || currentSession.Status.PrivateHistory.HomeUID != "" || currentSession.Status.PrivateHistory.ExecutorAttempted || currentSession.Status.PrivateHistory.ExecutorIntent != "" {
						t.Fatal("missing history was replaced with guessed admission")
					}
				} else if !apiequality.Semantic.DeepEqual(currentSession.Status.PrivateHistory, originalSession.Status.PrivateHistory) {
					t.Fatal("retention changed the private history latch or original executor")
				}
			})
		}
	}
}

func TestPrivateHistoryReapAndTimerPreserveIdentityAcrossOperatorReconstruction(t *testing.T) {
	for _, reaping := range []bool{false, true} {
		t.Run(map[bool]string{false: "timer", true: "reap"}[reaping], func(t *testing.T) {
			s, home := privateHistoryFixture()
			if reaping {
				at := metav1.Now()
				s.DeletionTimestamp = &at
			}
			originalHome := home.DeepCopy()
			originalHistory := s.Status.PrivateHistory.DeepCopy()
			// A bounded synthetic backing file: any PVC delete would destroy it.
			fixtureFile := filepath.Join(t.TempDir(), "provider-history.fixture")
			payload := []byte("synthetic provider history\n")
			if err := os.WriteFile(fixtureFile, payload, 0600); err != nil {
				t.Fatal(err)
			}
			base := privateHistoryClient(s, home)
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(home), originalHome); err != nil {
				t.Fatal(err)
			}
			c := interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
				if _, ok := o.(*corev1.PersistentVolumeClaim); ok {
					if err := os.Remove(fixtureFile); err != nil {
						t.Fatal(err)
					}
				}
				return c.Delete(ctx, o, opts...)
			}})
			// The durable latch protects after reconstruction with the flag off.
			for range 2 {
				r := &Reconciler{Client: c, APIReader: base}
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
					t.Fatal(err)
				}
			}
			var got v1alpha1.AgentSession
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &got); err != nil {
				t.Fatal(err)
			}
			var current corev1.PersistentVolumeClaim
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(home), &current); err != nil {
				t.Fatal(err)
			}
			if !apiequality.Semantic.DeepEqual(originalHome, &current) || !apiequality.Semantic.DeepEqual(originalHistory, got.Status.PrivateHistory) || !controllerutil.ContainsFinalizer(&got, Finalizer) || got.Status.ArchivedAt != nil || got.Status.Phase == v1alpha1.PhaseArchived {
				t.Fatal("private retention changed HOME/identity or claimed archival")
			}
			if !reaping && got.Status.Phase != v1alpha1.PhaseSuspended {
				t.Fatal("retained suspension changed phase")
			}
			if meta.IsStatusConditionTrue(got.Status.Conditions, ConditionPodReady) || meta.FindStatusCondition(got.Status.Conditions, ConditionRemovalBlocked).Reason != "RetentionPending" {
				t.Fatal("retention reported Ready or legacy Git rescue completion")
			}
			if data, err := os.ReadFile(fixtureFile); err != nil || string(data) != string(payload) {
				t.Fatal("synthetic private HOME bytes were lost")
			}
		})
	}
}

func TestPrivateHistoryAllReleaseFallbacksFailClosed(t *testing.T) {
	for _, name := range []string{"missing", "replaced", "foreign", "deleting", "no-home-finalizer", "old-binding", "stale-generation"} {
		t.Run(name, func(t *testing.T) {
			s, home := privateHistoryFixture()
			at := metav1.Now()
			s.DeletionTimestamp = &at
			objects := []client.Object{s, home}
			switch name {
			case "missing":
				objects = []client.Object{s}
			case "replaced":
				home.UID = "replacement"
			case "foreign":
				home.OwnerReferences = nil
			case "deleting":
				home.DeletionTimestamp = &at
			case "no-home-finalizer":
				home.Finalizers = nil
			case "old-binding":
				s.Status.PrivateHistory.Admission = "Unknown"
				s.Status.PrivateHistory.HomeUID = ""
			case "stale-generation":
				home.Annotations[privateHistoryAdmissionAnnotation] = "2"
			}
			c := privateHistoryClient(objects...)
			r := &Reconciler{Client: c, APIReader: c}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
				t.Fatal(err)
			}
			var got v1alpha1.AgentSession
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), &got); err != nil {
				t.Fatal(err)
			}
			condition := meta.FindStatusCondition(got.Status.Conditions, ConditionPrivateHistory)
			if !controllerutil.ContainsFinalizer(&got, Finalizer) || condition == nil || condition.Reason != "PreservationUnknown" || condition.Status != metav1.ConditionUnknown || got.Status.PrivateHistory.StopState != "Unknown" || got.Status.ArchivedAt != nil {
				t.Fatal("uncertain lifecycle escaped preservation")
			}
			if allowed, err := r.releaseIfEmpty(context.Background(), &got); allowed || err == nil {
				t.Fatal("empty/foreign fallback released identity")
			}
			if err := releaseSession(context.Background(), c, &got); err == nil {
				t.Fatal("direct Session release bypassed guard")
			}
			if err := deleteVolume(context.Background(), c, &got, home, false, true); err == nil {
				t.Fatal("delete volume bypassed guard")
			}
			if err := releaseVolume(context.Background(), c, &got, home, false, true); err == nil {
				t.Fatal("release volume bypassed guard")
			}
			if written, err := r.recordArchive(context.Background(), &got, time.Now()); written || err == nil {
				t.Fatal("archive completion bypassed guard")
			}
		})
	}
}

func TestPrivateHistoryOldRunningSessionIsNotRestartedOrBackfilled(t *testing.T) {
	s, home := privateHistoryFixture()
	s.Status.PrivateHistory = nil
	s.Status.Rescue = nil
	s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "existing-executor", OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Labels: map[string]string{v1alpha1.LabelRevision: "existing-template"}}, Spec: corev1.PodSpec{NodeName: "worker-a"}}
	c := privateHistoryClient(s, home, pod)
	r := &Reconciler{Client: c, APIReader: c, PrivateHistoryRetention: true}
	for range 3 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
			t.Fatal(err)
		}
	}
	var got v1alpha1.AgentSession
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), &got); err != nil {
		t.Fatal(err)
	}
	var current corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &current); err != nil {
		t.Fatal(err)
	}
	if !apiequality.Semantic.DeepEqual(pod, &current) || got.Status.PrivateHistory.Admission != "Unknown" || got.Status.PrivateHistory.HomeUID != "" || got.Status.PrivateHistory.LastExecutor.PodUID != string(pod.UID) {
		t.Fatal("old Session was restarted or its original HOME was guessed")
	}
}

func TestPrivateHistoryBindingRejectsLostACKAndChangedGeneration(t *testing.T) {
	for _, unconfirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "generation", true: "confirmation"}[unconfirmed], func(t *testing.T) {
			s, home := privateHistoryFixture()
			s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
			s.Status.Rescue = nil
			s.Status.PrivateHistory.HomeUID = ""
			s.Status.PrivateHistory.LastExecutor = nil
			base := privateHistoryClient(s, home)
			r := &Reconciler{Client: base, APIReader: base}
			if unconfirmed {
				reads := 0
				r.APIReader = retentionReader{Reader: base, beforeGet: func(o client.Object) error {
					if _, ok := o.(*v1alpha1.AgentSession); ok {
						reads++
						if reads > 1 {
							return errors.New("synthetic confirmation unavailable")
						}
					}
					return nil
				}}
			} else {
				s.Generation++
			}
			if err := r.bindPrivateHistoryHome(context.Background(), s); err == nil {
				t.Fatal("unconfirmed binding admitted an executor")
			}
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &corev1.Pod{}); !apierrors.IsNotFound(err) {
				t.Fatal("binding failure created an executor")
			}
		})
	}
}

func TestPrivateHistoryExecutorObservationSurvivesAbsenceWithoutStopProof(t *testing.T) {
	s, home := privateHistoryFixture()
	s.Status.PrivateHistory.LastExecutor = nil
	s.Status.PrivateHistory.ExecutorAttempted = true
	s.Status.PrivateHistory.ExecutorIntent = "synthetic-intent"
	s.Status.PodName = s.Name
	s.Status.Agent = &v1alpha1.AgentStatus{ConversationID: "synthetic-conversation"}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "observed-executor", OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Annotations: map[string]string{privateHistoryHomeAnnotation: string(home.UID), privateHistoryExecutorAnnotation: "synthetic-intent"}, Labels: map[string]string{v1alpha1.LabelRevision: "observed-template"}}, Spec: corev1.PodSpec{NodeName: "worker-a", Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: home.Name}}}}}}
	c := privateHistoryClient(s, home, pod)
	r := &Reconciler{Client: c, APIReader: c}
	obs := observation{}
	r.observePrivateHistory(context.Background(), s, &obs)
	last := s.Status.PrivateHistory.LastExecutor.DeepCopy()
	if last == nil || last.ConversationID != "synthetic-conversation" {
		t.Fatal("executor provenance missing")
	}
	if err := c.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	obs.pod = owned[*corev1.Pod]{missing: true}
	obs.claim = owned[*corev1.PersistentVolumeClaim]{obj: home}
	obs.now = time.Now()
	r.observePrivateHistory(context.Background(), s, &obs)
	st := s.Status.DeepCopy()
	observe(s, obs, st)
	if st.PodName != "" || st.NodeName != "" || !apiequality.Semantic.DeepEqual(last, st.PrivateHistory.LastExecutor) || st.PrivateHistory.StopState != "Unknown" || strings.Contains(meta.FindStatusCondition(st.Conditions, ConditionPrivateHistory).Message, "Confirmed") {
		t.Fatal("pod absence erased provenance or invented private stop evidence")
	}
}

// history-01 (DESIGN-019): a non-attempt mark only ever covers absence. With
// the intent's Pod absent the attempt is not PreservationUnknown; an observed
// executor wins, clearing the mark so the attempt is latched and never re-arms.
func TestPrivateHistoryNotCreatedMarkYieldsToObservedExecutor(t *testing.T) {
	s, home := privateHistoryFixture()
	h := s.Status.PrivateHistory
	h.LastExecutor = nil
	h.ExecutorAttempted, h.ExecutorIntent, h.ExecutorNotCreated = true, "synthetic-intent", true
	c := privateHistoryClient(s, home)
	r := &Reconciler{Client: c, APIReader: c}
	obs := observation{}
	r.observePrivateHistory(context.Background(), s, &obs)
	if obs.privateHistoryUnknown != nil || !s.Status.PrivateHistory.ExecutorNotCreated {
		t.Fatal("a recorded non-attempt read as an absent executor", obs.privateHistoryUnknown)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "observed-executor", OwnerReferences: []metav1.OwnerReference{ownerRef(s)},
		Annotations: map[string]string{privateHistoryHomeAnnotation: string(home.UID), privateHistoryExecutorAnnotation: "synthetic-intent"}},
		Spec: corev1.PodSpec{NodeName: "worker-a", Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: home.Name}}}}}}
	if err := c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	r.observePrivateHistory(context.Background(), s, &obs)
	if got := s.Status.PrivateHistory; got.ExecutorNotCreated || got.LastExecutor == nil || got.LastExecutor.PodUID != "observed-executor" {
		t.Fatalf("observed executor did not latch the attempt: %+v", got)
	}
}

// history-01 review (#222): a stale CRD schema prunes executorNotCreated. The
// API server then stores and returns the record without the mark, so the
// response alone matches the stored record. The confirmation compares the
// stored record with the record the write meant to store, so the mark is
// unconfirmed and the attempt stays latched.
func TestPrivateHistoryWriteRefusesAFieldTheSchemaPruned(t *testing.T) {
	s, home := privateHistoryFixture()
	h := s.Status.PrivateHistory
	h.LastExecutor = nil
	h.ExecutorAttempted, h.ExecutorIntent = true, "synthetic-intent"
	base := privateHistoryClient(s, home)
	c := interceptor.NewClient(base, interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
		if typed, ok := o.(*v1alpha1.AgentSession); ok && typed.Status.PrivateHistory != nil {
			typed.Status.PrivateHistory.ExecutorNotCreated = false // what a schema without the field prunes
		}
		return c.SubResource(sub).Update(ctx, o, opts...)
	}})
	r := &Reconciler{Client: c, APIReader: base}
	ctx := context.Background()
	current, err := r.privateHistorySession(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	fresh := current.DeepCopy()
	fresh.Status.PrivateHistory.ExecutorNotCreated = true
	if err := r.writePrivateHistory(ctx, current, fresh); err == nil {
		t.Fatal("a pruned non-attempt mark confirmed as written")
	}
	if current.Status.PrivateHistory.ExecutorNotCreated {
		t.Fatal("an unconfirmed write replaced the caller's Session")
	}
	stored, err := r.privateHistorySession(ctx, s)
	if err != nil || stored.Status.PrivateHistory.ExecutorNotCreated || !stored.Status.PrivateHistory.ExecutorAttempted {
		t.Fatal("the attempt did not stay latched without a mark", err)
	}
}

// envtest has no GC controller, so this test plays its part. It submits both
// owner delete policies against a session with a real HOME volume and a real
// running original executor pod, both owned by the session, then deletes what
// the garbage collector would. A background delete leaves the dependents alone
// while the session's finalizer holds it. A foreground cascade, which only a
// human can ask for (D-45), deletes them first: the pod, which has no
// finalizer, goes; the volume stays terminating under its finalizer. Neither
// policy may lift protection, and the operator must neither delete the pod
// itself, run a rescue in it, replace it, nor read its absence as a stop.
func TestPrivateHistoryForegroundAndBackgroundDeleteKeepProtectiveFinalizers(t *testing.T) {
	for _, policy := range []metav1.DeletionPropagation{metav1.DeletePropagationForeground, metav1.DeletePropagationBackground} {
		t.Run(string(policy), func(t *testing.T) {
			ctx := context.Background()
			s := taskSession()
			s.Name = "history-gc-" + strings.ToLower(string(policy))
			s.Namespace = sessionNS
			s.UID = ""
			s.ResourceVersion = ""
			s.Finalizers = []string{Finalizer}
			if err := k8s.Create(ctx, s); err != nil {
				t.Fatal(err)
			}
			home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Finalizers: []string{Finalizer}}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
			if err := k8s.Create(ctx, home); err != nil {
				t.Fatal(err)
			}
			const intent = "original-intent"
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef(s)},
				Labels:      map[string]string{v1alpha1.LabelRevision: "original-template"},
				Annotations: map[string]string{privateHistoryExecutorAnnotation: intent, privateHistoryHomeAnnotation: string(home.UID)}},
				Spec: corev1.PodSpec{NodeName: "worker-a", RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: ContainerName, Image: "synthetic.invalid/agent:original"}},
					Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: home.Name}}}}}}
			if err := k8s.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName, Image: pod.Spec.Containers[0].Image,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}}}}}
			if err := k8s.Status().Update(ctx, pod); err != nil {
				t.Fatal(err)
			}
			originalPod := pod.DeepCopy()
			s.Status.PodName = pod.Name
			s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: home.Name, HomeUID: string(home.UID), BindingGeneration: s.Generation, Admission: sharedStarted, Provider: string(s.Spec.Agent), StopState: "Unknown",
				ExecutorAttempted: true, ExecutorIntent: intent,
				LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: string(pod.UID), PodName: pod.Name, NodeName: pod.Spec.NodeName, Revision: "original-template", Generation: s.Generation, ConversationID: "synthetic-conversation"}}
			if err := k8s.Status().Update(ctx, s); err != nil {
				t.Fatal(err)
			}
			originalHistory := s.Status.PrivateHistory.DeepCopy()
			if err := k8s.Delete(ctx, s, client.PropagationPolicy(policy)); err != nil {
				t.Fatal(err)
			}
			// The garbage collector's part. Background: nothing while the
			// owner exists; the volume is still deleted directly, the
			// conservative case. Foreground: every dependent, and a pod with
			// no finalizer is gone once its process ends (grace 0 models the
			// end state, which no kubelet reaches in envtest).
			if policy == metav1.DeletePropagationForeground {
				if err := k8s.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
					t.Fatal(err)
				}
			}
			if err := k8s.Delete(ctx, home, client.PropagationPolicy(policy)); err != nil {
				t.Fatal(err)
			}
			rescue := &fakeRescuer{calls: map[string]int{}}
			r := &Reconciler{Client: k8s, APIReader: k8s, Rescuer: rescue}
			for range 2 {
				if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := k8s.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil || !controllerutil.ContainsFinalizer(s, Finalizer) {
				t.Fatal("owner deletion bypassed retention")
			}
			// The delete bumps the session's generation, and observing the
			// same live pod records that; the executor's identity must not move.
			last, want := s.Status.PrivateHistory.LastExecutor, originalHistory.LastExecutor
			if last == nil || last.PodUID != string(originalPod.UID) || last.PodName != want.PodName || last.NodeName != want.NodeName || last.Revision != want.Revision || last.ConversationID != want.ConversationID ||
				s.Status.PrivateHistory.StopState != "Unknown" || s.Status.PrivateHistory.ExecutorIntent != intent || !s.Status.PrivateHistory.ExecutorAttempted || s.Status.ArchivedAt != nil {
				t.Fatalf("the original executor's record changed, or its loss was read as a stop: %+v -> %+v", want, last)
			}
			if err := k8s.Get(ctx, client.ObjectKeyFromObject(home), home); err != nil || !controllerutil.ContainsFinalizer(home, Finalizer) || home.DeletionTimestamp.IsZero() {
				t.Fatal("dependent deletion bypassed protective finalizer")
			}
			if n := rescue.callsFor(pod.Name); n != 0 {
				t.Fatalf("the operator ran %d rescues in the retained original executor", n)
			}
			var current corev1.Pod
			err := k8s.Get(ctx, client.ObjectKeyFromObject(pod), &current)
			switch policy {
			case metav1.DeletePropagationForeground:
				// D-45's human-only exception: the cascade took the process.
				// The operator must not put a replacement in its place.
				if !apierrors.IsNotFound(err) {
					t.Fatalf("after a foreground cascade the operator recreated or kept a pod: uid=%s err=%v", current.UID, err)
				}
			default:
				if err != nil || current.UID != originalPod.UID || !current.DeletionTimestamp.IsZero() || !apiequality.Semantic.DeepEqual(current.Spec, originalPod.Spec) || current.Status.Phase != corev1.PodRunning {
					t.Fatalf("a background delete lost or changed the original executor pod: err=%v", err)
				}
			}
		})
	}
}

func TestPrivateHistoryFreshAdmissionBindsBeforeExecutorAndNeverAutoReplaces(t *testing.T) {
	s := taskSession()
	s.Generation = 1
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}, Data: map[string]string{templates.Key: exampleDoc}}
	base := privateHistoryClient(s, cm)
	creates := 0
	c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		switch obj := o.(type) {
		case *corev1.PersistentVolumeClaim:
			obj.UID = "fresh-original-home"
		case *corev1.Pod:
			var admitted v1alpha1.AgentSession
			if err := c.Get(ctx, client.ObjectKeyFromObject(s), &admitted); err != nil {
				t.Fatal(err)
			}
			if admitted.Status.PrivateHistory == nil || admitted.Status.PrivateHistory.HomeUID != "fresh-original-home" || !admitted.Status.PrivateHistory.ExecutorAttempted || obj.Annotations[privateHistoryHomeAnnotation] != "fresh-original-home" {
				t.Fatal("executor preceded durable original HOME and attempt binding")
			}
			creates++
			obj.UID = "fresh-original-executor"
		}
		return c.Create(ctx, o, opts...)
	}})
	r := &Reconciler{Client: c, APIReader: base, Templates: templatesKey, PrivateHistoryRetention: true}
	for range 3 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
			t.Fatal(err)
		}
	}
	var pod corev1.Pod
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &pod); err != nil {
		t.Fatal(err)
	}
	if err := base.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
			t.Fatal(err)
		}
	}
	if creates != 1 {
		t.Fatal("missing private executor launched replacement model")
	}
	var retained v1alpha1.AgentSession
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &retained); err != nil {
		t.Fatal(err)
	}
	h := retained.Status.PrivateHistory
	if h.LastExecutor == nil || h.LastExecutor.PodUID != "fresh-original-executor" || h.StopState != "Unknown" {
		t.Fatal("absent executor erased original provenance or asserted stop")
	}
	ready := meta.FindStatusCondition(retained.Status.Conditions, ConditionPodReady)
	boundary := meta.FindStatusCondition(retained.Status.Conditions, ConditionPrivateHistory)
	if ready == nil || ready.Reason != "PreservationUnknown" || ready.Status != metav1.ConditionFalse || boundary == nil || boundary.Reason != "PreservationUnknown" || strings.Contains(retained.Status.PendingReason, "creating") {
		t.Fatal("recorded-then-absent executor falsely reported Creating")
	}
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &pod); !apierrors.IsNotFound(err) {
		t.Fatal("replacement executor exists")
	}
}

func TestPrivateHistoryLostCreateACKAdoptsOnlySameIntentAndNeverRetriesAbsence(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncommitted", true: "committed"}[committed], func(t *testing.T) {
			s := taskSession()
			s.Generation = 1
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}, Data: map[string]string{templates.Key: exampleDoc}}
			base := privateHistoryClient(s, cm)
			attempts := 0
			c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				switch obj := o.(type) {
				case *corev1.PersistentVolumeClaim:
					obj.UID = "original-home"
				case *corev1.Pod:
					attempts++
					obj.UID = "original-executor"
					if committed {
						if err := c.Create(ctx, o, opts...); err != nil {
							return err
						}
					}
					return errors.New("synthetic lost create acknowledgement")
				}
				return c.Create(ctx, o, opts...)
			}})
			r := &Reconciler{Client: c, APIReader: base, Templates: templatesKey, PrivateHistoryRetention: true}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err == nil {
				t.Fatal("lost create ACK was treated as success")
			}
			r = &Reconciler{Client: c, APIReader: base, Templates: templatesKey}
			for range 3 {
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
					t.Fatal(err)
				}
			}
			var got v1alpha1.AgentSession
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &got); err != nil {
				t.Fatal(err)
			}
			h := got.Status.PrivateHistory
			if attempts != 1 || !h.ExecutorAttempted || h.ExecutorIntent == "" || h.StopState != "Unknown" {
				t.Fatal("missing ACK rearmed execution")
			}
			if committed {
				if h.LastExecutor == nil || h.LastExecutor.PodUID != "original-executor" {
					t.Fatal("same causal intent was not independently observed")
				}
				var pod corev1.Pod
				if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &pod); err != nil {
					t.Fatal(err)
				}
				if err := base.Delete(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
				pod.ResourceVersion = ""
				pod.UID = "replacement-executor"
				if err := base.Create(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
					t.Fatal(err)
				}
				if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &got); err != nil {
					t.Fatal(err)
				}
				if got.Status.PrivateHistory.LastExecutor.PodUID != "original-executor" || meta.FindStatusCondition(got.Status.Conditions, ConditionPrivateHistory).Reason != "PreservationUnknown" {
					t.Fatal("replacement executor was adopted")
				}
			} else if h.LastExecutor != nil || meta.FindStatusCondition(got.Status.Conditions, ConditionPrivateHistory).Reason != "PreservationUnknown" {
				t.Fatal("uncommitted create invented executor identity or claimed preservation completeness")
			}
		})
	}
}
