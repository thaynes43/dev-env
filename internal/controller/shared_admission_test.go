package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func freshSharedSession() *v1alpha1.AgentSession {
	s := taskSession()
	s.Generation = 1
	s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
	s.Finalizers = []string{"fixture/keep"}
	return s
}

func sharedAdmissionClient(s *v1alpha1.AgentSession, extra ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}).
		WithObjects(append([]client.Object{s}, extra...)...).Build()
}

func assertSharedResourcesMissing(t *testing.T, c client.Reader, s *v1alpha1.AgentSession) {
	t.Helper()
	for _, name := range []string{s.Name, workspaceHoldName(s)} {
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: s.Namespace, Name: name}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
			t.Fatalf("unexpected Pod %s: %v", name, err)
		}
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: s.Namespace, Name: HomeClaimName(s.Name)}, &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected private home: %v", err)
	}
}

func TestFreshSharedSessionDeletedBeforeAdmissionReapsWithoutAHome(t *testing.T) {
	s := freshSharedSession()
	c := sharedAdmissionClient(s)
	r := &Reconciler{Client: c, APIReader: c}
	// Missing templates leave a confirmed NeverStarted Session with D-45's
	// finalizer. This is the actual before-resource path the review identified.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil || !sharedAdmissionIs(s, sharedNeverStarted) || !controllerutil.ContainsFinalizer(s, Finalizer) {
		t.Fatalf("fresh admission was not durable before finalizer: %+v, %v", s.Status, err)
	}
	assertSharedResourcesMissing(t, c, s)
	if err := c.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil || controllerutil.ContainsFinalizer(s, Finalizer) || len(s.Finalizers) != 1 || s.Finalizers[0] != "fixture/keep" || s.Status.ArchivedAt != nil {
		t.Fatalf("never-started reap failed or removed another finalizer: %+v, %v", s, err)
	}
	assertSharedResourcesMissing(t, c, s)
}

func TestSharedDeletionBeforeFirstReconcileKeepsExistingNoResourceBehavior(t *testing.T) {
	s := freshSharedSession()
	s.Finalizers = nil
	c := sharedAdmissionClient(s)
	if err := c.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c, APIReader: c}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), &v1alpha1.AgentSession{}); !apierrors.IsNotFound(err) {
		t.Fatalf("before-reconcile deletion acquired a new lifecycle: %v", err)
	}
	assertSharedResourcesMissing(t, c, s)
}

type sharedAdmissionFaultClient struct {
	client.Client
	afterStatus func(*v1alpha1.AgentSession)
	lostState   string
	writes      int
	creates     int
}

type sharedAdmissionFaultWriter struct {
	client.SubResourceWriter
	c *sharedAdmissionFaultClient
}

func (c *sharedAdmissionFaultClient) Status() client.SubResourceWriter {
	return &sharedAdmissionFaultWriter{SubResourceWriter: c.Client.Status(), c: c}
}

func (w *sharedAdmissionFaultWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	w.c.writes++
	if err := w.SubResourceWriter.Update(ctx, obj, opts...); err != nil {
		return err
	}
	s := obj.(*v1alpha1.AgentSession)
	if w.c.afterStatus != nil {
		w.c.afterStatus(s)
	}
	if s.Status.SharedAdmission != nil && s.Status.SharedAdmission.State == w.c.lostState {
		return errors.New("fixture admission write committed but ACK lost")
	}
	return nil
}

func (c *sharedAdmissionFaultClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.creates++
	var session v1alpha1.AgentSession
	if err := c.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: "haynes-ops-1006-120000"}, &session); err != nil {
		return err
	}
	if !sharedAdmissionIs(&session, sharedStarted) || !controllerutil.ContainsFinalizer(&session, Finalizer) {
		return errors.New("resource write preceded confirmed Started/finalizer")
	}
	if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
		obj.SetUID("home-1")
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestFreshSharedAdmissionLostACKStopsBeforeFinalizerAndRecoversByRead(t *testing.T) {
	s := freshSharedSession()
	c := &sharedAdmissionFaultClient{Client: sharedAdmissionClient(s), lostState: sharedNeverStarted}
	r := &Reconciler{Client: c, APIReader: c}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}
	if _, err := r.Reconcile(context.Background(), req); err == nil {
		t.Fatal("lost NeverStarted ACK was accepted")
	}
	if err := c.Get(context.Background(), req.NamespacedName, s); err != nil || controllerutil.ContainsFinalizer(s, Finalizer) || !sharedAdmissionIs(s, sharedNeverStarted) || c.creates != 0 {
		t.Fatalf("lost ACK reached finalizer/resources: %+v, %v", s, err)
	}
	c.lostState = ""
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), req.NamespacedName, s); err != nil || !controllerutil.ContainsFinalizer(s, Finalizer) || !sharedAdmissionIs(s, sharedNeverStarted) {
		t.Fatalf("confirmed persisted marker did not recover: %+v, %v", s, err)
	}
	assertSharedResourcesMissing(t, c, s)
}

func TestSharedDeletionBetweenAdmissionStagesNeverCreatesResources(t *testing.T) {
	for _, state := range []string{sharedNeverStarted, sharedStarted} {
		t.Run(state, func(t *testing.T) {
			s := freshSharedSession()
			if state == sharedStarted {
				s.Finalizers = append(s.Finalizers, Finalizer)
				s.Status.SharedAdmission = &v1alpha1.SharedAdmissionStatus{Version: 1, SessionUID: string(s.UID), State: sharedNeverStarted}
			}
			c := &sharedAdmissionFaultClient{Client: sharedAdmissionClient(s)}
			c.afterStatus = func(current *v1alpha1.AgentSession) {
				if current.Status.SharedAdmission.State == state {
					if err := c.Delete(context.Background(), current.DeepCopy()); err != nil {
						t.Fatal(err)
					}
				}
			}
			r := &Reconciler{Client: c, APIReader: c}
			if state == sharedNeverStarted {
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err == nil {
					t.Fatal("deletion between marker and finalizer was accepted")
				}
			} else if err := r.startSharedAdmission(context.Background(), s); err == nil {
				t.Fatal("deletion between Started and first resource was accepted")
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil || s.DeletionTimestamp.IsZero() || c.creates != 0 {
				t.Fatalf("stage deletion failed: %+v, %v", s, err)
			}
			if controllerutil.ContainsFinalizer(s, Finalizer) != (state == sharedStarted) {
				t.Fatal("marker/finalizer ordering changed")
			}
			c.afterStatus = nil
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil || controllerutil.ContainsFinalizer(s, Finalizer) != (state == sharedStarted) {
				t.Fatalf("Started/unknown empty lifecycle was released: %+v, %v", s, err)
			}
			assertSharedResourcesMissing(t, c, s)
		})
	}
}

func TestSharedStartedLostACKStopsAllResourceWrites(t *testing.T) {
	s := freshSharedSession()
	s.Finalizers = append(s.Finalizers, Finalizer)
	s.Status.SharedAdmission = &v1alpha1.SharedAdmissionStatus{Version: 1, SessionUID: string(s.UID), State: sharedNeverStarted}
	c := &sharedAdmissionFaultClient{Client: sharedAdmissionClient(s), lostState: sharedStarted}
	f := stoppedWorkspaceFixture()
	f.s = s
	r, obs := workspaceFixtureController(t, f, c)
	obs.pod = owned[*corev1.Pod]{missing: true}
	obs.claim = owned[*corev1.PersistentVolumeClaim]{missing: true}
	if err := r.ensure(context.Background(), s, obs.templates, &obs); err != nil || obs.removalBlocked == nil || c.creates != 0 {
		t.Fatalf("lost Started ACK admitted resources: %v, %v, %d", err, obs.removalBlocked, c.creates)
	}
	assertSharedResourcesMissing(t, c, s)
	c.lostState = ""
	obs.removalBlocked = nil
	if err := r.ensure(context.Background(), s, obs.templates, &obs); err != nil || obs.removalBlocked != nil || c.creates != 2 {
		t.Fatalf("confirmed Started did not recover admission: %v, %v, %d", err, obs.removalBlocked, c.creates)
	}
	if !sharedAdmissionIs(s, sharedStarted) || s.Status.SharedPrivateHomeUID != "home-1" {
		t.Fatal("resource creation lacked original admission evidence")
	}
}

func TestSharedAdmissionNeverBackfillsLegacyOrDeletingSessions(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*v1alpha1.AgentSession)
	}{
		{"legacy finalizer", func(s *v1alpha1.AgentSession) { s.Finalizers = append(s.Finalizers, Finalizer) }},
		{"deleting no marker", func(s *v1alpha1.AgentSession) { at := metav1.Now(); s.DeletionTimestamp = &at }},
		{"observed legacy", func(s *v1alpha1.AgentSession) { s.Status.Phase = v1alpha1.PhasePending }},
		{"resource history", func(s *v1alpha1.AgentSession) { s.Status.PodName = s.Name }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := freshSharedSession()
			tc.edit(s)
			c := &sharedAdmissionFaultClient{Client: sharedAdmissionClient(s)}
			r := &Reconciler{Client: c, APIReader: c}
			if err := r.initializeSharedAdmission(context.Background(), s); err == nil || c.writes != 0 {
				t.Fatalf("unknown history was backfilled: %v, %d", err, c.writes)
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil || s.Status.SharedAdmission != nil {
				t.Fatalf("unknown status was altered: %v", err)
			}
			assertSharedResourcesMissing(t, c, s)
		})
	}
}

func TestNeverStartedSharedReapRefusesActualResourcesAndContradictoryHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*v1alpha1.AgentSession) []client.Object
	}{
		{"foreign executor", func(s *v1alpha1.AgentSession) []client.Object {
			return []client.Object{&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: s.Name, UID: "late-executor"}}}
		}},
		{"foreign hold", func(s *v1alpha1.AgentSession) []client.Object {
			return []client.Object{&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: workspaceHoldName(s), UID: "late-hold"}}}
		}},
		{"any private home", func(s *v1alpha1.AgentSession) []client.Object {
			return []client.Object{&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: HomeClaimName(s.Name), UID: "late-home"}}}
		}},
		{"original binding", func(s *v1alpha1.AgentSession) []client.Object { s.Status.SharedPrivateHomeUID = "old-home"; return nil }},
		{"provider history", func(s *v1alpha1.AgentSession) []client.Object { s.Status.Agent = &v1alpha1.AgentStatus{}; return nil }},
		{"rescue history", func(s *v1alpha1.AgentSession) []client.Object { s.Status.Rescue = &v1alpha1.RescueStatus{}; return nil }},
		{"wrong marker UID", func(s *v1alpha1.AgentSession) []client.Object {
			s.Status.SharedAdmission.SessionUID = "replaced-session"
			return nil
		}},
		{"Started", func(s *v1alpha1.AgentSession) []client.Object {
			s.Status.SharedAdmission.State = sharedStarted
			return nil
		}},
		{"legacy marker absent", func(s *v1alpha1.AgentSession) []client.Object { s.Status.SharedAdmission = nil; return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := freshSharedSession()
			s.Finalizers = append(s.Finalizers, Finalizer)
			at := metav1.Now()
			s.DeletionTimestamp = &at
			s.Status.SharedAdmission = &v1alpha1.SharedAdmissionStatus{Version: 1, SessionUID: string(s.UID), State: sharedNeverStarted}
			c := sharedAdmissionClient(s, tc.edit(s)...)
			r := &Reconciler{Client: c, APIReader: c}
			obs := observation{}
			if _, err := r.retainSharedPrivateHome(context.Background(), s, &obs); err != nil || obs.removalBlocked == nil {
				t.Fatalf("uncertain never-started proof accepted: %v, %v", err, obs.removalBlocked)
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil || !controllerutil.ContainsFinalizer(s, Finalizer) {
				t.Fatalf("uncertain lifecycle was released: %v", err)
			}
		})
	}
}

func TestSharedAdmissionConfirmationRefusesSessionUIDGenerationAndVersionRaces(t *testing.T) {
	for _, stage := range []string{sharedNeverStarted, sharedStarted} {
		for _, change := range []string{"UID", "generation", "resourceVersion"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				s := freshSharedSession()
				if stage == sharedStarted {
					s.Finalizers = append(s.Finalizers, Finalizer)
					s.Status.SharedAdmission = &v1alpha1.SharedAdmissionStatus{Version: 1, SessionUID: string(s.UID), State: sharedNeverStarted}
				}
				base := sharedAdmissionClient(s)
				reads := 0
				reader := retentionReader{Reader: base, beforeGet: func(obj client.Object) error {
					if _, ok := obj.(*v1alpha1.AgentSession); !ok {
						return nil
					}
					reads++
					if reads == 2 {
						var changed v1alpha1.AgentSession
						if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &changed); err != nil {
							t.Fatal(err)
						}
						switch change {
						case "UID":
							changed.UID = "replacement-session"
						case "generation":
							changed.Generation++
						case "resourceVersion":
							changed.Labels = map[string]string{"fixture/change": "true"}
						}
						if err := base.Update(context.Background(), &changed); err != nil {
							t.Fatal(err)
						}
					}
					return nil
				}}
				r := &Reconciler{Client: base, APIReader: reader}
				var err error
				if stage == sharedStarted {
					err = r.startSharedAdmission(context.Background(), s)
				} else {
					err = r.initializeSharedAdmission(context.Background(), s)
				}
				if err == nil {
					t.Fatal("changed Session was confirmed for resource admission")
				}
				assertSharedResourcesMissing(t, base, s)
			})
		}
	}
}

func TestNeverStartedSharedReapRechecksLateResourcesAndSessionRaces(t *testing.T) {
	for _, change := range []string{"executor", "hold", "private home", "UID", "generation", "marker"} {
		t.Run(change, func(t *testing.T) {
			s := freshSharedSession()
			s.Finalizers = append(s.Finalizers, Finalizer)
			s.Status.SharedAdmission = &v1alpha1.SharedAdmissionStatus{Version: 1, SessionUID: string(s.UID), State: sharedNeverStarted}
			at := metav1.Now()
			s.DeletionTimestamp = &at
			base := sharedAdmissionClient(s)
			reads := 0
			r := &Reconciler{Client: base, APIReader: retentionReader{Reader: base, beforeGet: func(obj client.Object) error {
				if _, ok := obj.(*v1alpha1.AgentSession); !ok {
					return nil
				}
				reads++
				if reads != 2 {
					return nil
				}
				switch change {
				case "executor", "hold":
					name := s.Name
					if change == "hold" {
						name = workspaceHoldName(s)
					}
					if err := base.Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: name, UID: "late-pod"}}); err != nil {
						t.Fatal(err)
					}
				case "private home":
					if err := base.Create(context.Background(), &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: HomeClaimName(s.Name), UID: "late-home"}}); err != nil {
						t.Fatal(err)
					}
				default:
					var current v1alpha1.AgentSession
					if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &current); err != nil {
						t.Fatal(err)
					}
					if change == "marker" {
						current.Status.SharedAdmission.State = sharedStarted
						if err := base.Status().Update(context.Background(), &current); err != nil {
							t.Fatal(err)
						}
					} else {
						if change == "UID" {
							current.UID = "replacement-session"
						} else {
							current.Generation++
						}
						if err := base.Update(context.Background(), &current); err != nil {
							t.Fatal(err)
						}
					}
				}
				return nil
			}}}
			obs := observation{}
			if _, err := r.retainSharedPrivateHome(context.Background(), s, &obs); err != nil || obs.removalBlocked == nil {
				t.Fatalf("late proof change was accepted: %v, %v", err, obs.removalBlocked)
			}
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil || !controllerutil.ContainsFinalizer(s, Finalizer) {
				t.Fatalf("late resource/session change removed finalizer: %v", err)
			}
		})
	}
}

func TestNeverStartedSharedFinalizerPatchRefusesReplacedSessionOrChangedVersion(t *testing.T) {
	for _, changeUID := range []bool{false, true} {
		t.Run(map[bool]string{false: "resourceVersion", true: "UID"}[changeUID], func(t *testing.T) {
			s := freshSharedSession()
			s.Finalizers = append(s.Finalizers, Finalizer)
			s.Status.SharedAdmission = &v1alpha1.SharedAdmissionStatus{Version: 1, SessionUID: string(s.UID), State: sharedNeverStarted}
			at := metav1.Now()
			s.DeletionTimestamp = &at
			base := sharedAdmissionClient(s)
			c := &retentionPatchClient{Client: base, beforeSession: func(ctx context.Context) {
				var current v1alpha1.AgentSession
				if err := base.Get(ctx, client.ObjectKeyFromObject(s), &current); err != nil {
					t.Fatal(err)
				}
				if changeUID {
					current.UID = "replacement-session"
				} else {
					current.Labels = map[string]string{"fixture/changed": "true"}
				}
				if err := base.Update(ctx, &current); err != nil {
					t.Fatal(err)
				}
			}}
			r := &Reconciler{Client: c, APIReader: base}
			obs := observation{}
			if _, err := r.retainSharedPrivateHome(context.Background(), s, &obs); err != nil || obs.removalBlocked == nil {
				t.Fatalf("conditional finalization accepted replacement/version race: %v, %v", err, obs.removalBlocked)
			}
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil || !controllerutil.ContainsFinalizer(s, Finalizer) {
				t.Fatalf("conditional finalization lost current Session: %v", err)
			}
			assertSharedResourcesMissing(t, base, s)
		})
	}
}
