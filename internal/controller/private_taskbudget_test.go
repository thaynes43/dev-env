package controller

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
	"github.com/thaynes43/dev-env/internal/taskbudget"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

type privateBudgetFixture struct {
	s       *v1alpha1.AgentSession
	home    *corev1.PersistentVolumeClaim
	pod     *corev1.Pod
	d       *coord.SessionDirectory
	client  client.WithWatch
	store   *sessiondirectory.KubeStore
	service *taskbudget.Service
	ledger  *taskbudget.Ledger
	ref     taskbudget.ManagedWorkerRef
	now     time.Time
}

// The canary binding supplies accepted original directory metadata only. These
// synthetic budget annotations do not make this a legally dispatched canary;
// existing API/controller canary guards continue to refuse that combination.
func newPrivateBudgetFixture(t *testing.T, provider string) privateBudgetFixture {
	t.Helper()
	ctx := context.Background()
	f := testutil.New(t, provider, "system/parent")
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	s := f.Session
	s.CreationTimestamp = metav1.NewTime(now)
	s.Finalizers = append(s.Finalizers, Finalizer)
	s.Spec.SessionDirectory = &v1alpha1.SessionDirectoryEnrollment{Version: 1, Project: f.Record.Project}
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: HomeClaimName(s.Name), HomeUID: "home-original",
		BindingGeneration: 1, Admission: sharedStarted, ExecutorAttempted: true, ExecutorIntent: "intent-original", Provider: provider, StopState: "Unknown",
		LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "pod-original", PodName: s.Name, Revision: f.Record.TemplateRevision, Generation: 1}}
	home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: HomeClaimName(s.Name), UID: "home-original", Finalizers: []string{Finalizer},
		OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Annotations: map[string]string{privateHistoryAdmissionAnnotation: "1"}}}
	pod, err := buildManagedPod(s, f.Templates, "https://synthetic.invalid", true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	pod.UID = "pod-original"
	pod.Annotations[privateHistoryHomeAnnotation] = "home-original"
	pod.Annotations[privateHistoryExecutorAnnotation] = "intent-original"
	spec, err := sessiondirectory.Spec(s)
	if err != nil {
		t.Fatal(err)
	}
	d := &coord.SessionDirectory{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: sessiondirectory.Name(spec.Source), UID: "directory-original", CreationTimestamp: metav1.NewTime(now), Labels: map[string]string{taskclaim.ProjectLabel: f.Record.Project}}, Spec: spec}
	digest := digestPrivateDirectory(t, spec)
	s.Status.SessionDirectory = &v1alpha1.SessionDirectoryReceipt{State: "Confirmed", Name: d.Name, UID: string(d.UID), SpecDigest: digest}
	local := runtime.NewScheme()
	if err := errors.Join(corev1.AddToScheme(local), v1alpha1.AddToScheme(local), coord.AddToScheme(local)); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(local).WithStatusSubresource(&v1alpha1.AgentSession{}, &corev1.Pod{}, &coord.SessionDirectory{}).WithObjects(s, home, pod, d).Build()
	svc, b := budgetService(t, c, now)
	id := "child:stable-dispatch"
	for k, v := range budgetAnnotations(b, id) {
		s.Annotations[k] = v
	}
	if err := c.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Record(ctx, apiv1.TaskBudgetEvent{Binding: b, ID: "dispatch:stable-dispatch", Kind: "worker-start", WorkerID: id, AttemptID: "server-nonce"}); err != nil {
		t.Fatal(err)
	}
	ref := taskbudget.ManagedWorkerRef{Namespace: s.Namespace, Name: s.Name, SessionUID: string(s.UID), PodUID: string(pod.UID)}
	l, err := svc.BindManagedWorker(ctx, b, id, ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range []client.Object{s, home, pod, d} {
		if err := c.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
			t.Fatal(err)
		}
	}
	return privateBudgetFixture{s: s, home: home, pod: pod, d: d, client: c, store: &sessiondirectory.KubeStore{Client: c, Live: c, Namespace: d.Namespace}, service: svc, ledger: l, ref: ref, now: now}
}

func digestPrivateDirectory(t *testing.T, spec coord.SessionDirectorySpec) string {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return commonrules.Digest(raw)
}

func TestPrivateBudgetOwnershipBindsOriginalResourcesWithoutAdmissionOrStop(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newPrivateBudgetFixture(t, provider)
			writes := 0
			c := interceptor.NewClient(f.client, interceptor.Funcs{
				Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					writes++
					return errors.New("unexpected create")
				},
				Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
					writes++
					return errors.New("unexpected update")
				},
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					writes++
					return errors.New("unexpected patch")
				},
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
					writes++
					return errors.New("unexpected delete")
				},
			})
			store := &sessiondirectory.KubeStore{Client: c, Live: c, Namespace: f.d.Namespace}
			proof, err := provePrivateBudgetOwnership(context.Background(), c, store, f.ledger, f.ref)
			if err != nil {
				t.Fatal(err)
			}
			want := &privateBudgetOwnership{Binding: f.ledger.Binding, WorkerID: f.ledger.Workers[0].ID, Worker: f.ref, SessionResourceVersion: f.s.ResourceVersion, SessionGeneration: 1,
				HomeUID: "home-original", ExecutorIntent: "intent-original", DirectoryNamespace: f.d.Namespace, DirectoryName: f.d.Name,
				DirectoryUID: "directory-original", DirectorySpecDigest: digestPrivateDirectory(t, f.d.Spec), StopState: "Unknown"}
			if !reflect.DeepEqual(proof, want) {
				t.Fatalf("original identity not bound: %+v", proof)
			}
			inspector := PrivateManagedAdmissionInspector{Reader: c, Directory: store}
			if err := inspector.InspectManagedAdmission(context.Background(), f.ledger, f.ref); !errors.Is(err, taskbudget.ErrDenied) || writes != 0 {
				t.Fatal("ownership was treated as private admission", err, writes)
			}
			// A terminated-looking retained Pod still is not independent stop proof.
			f.pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: ContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(f.now), ExitCode: 0}}}}
			if err := f.client.Status().Update(context.Background(), f.pod); err != nil {
				t.Fatal(err)
			}
			proof, err = provePrivateBudgetOwnership(context.Background(), c, store, f.ledger, f.ref)
			if err != nil || proof.StopState != "Unknown" || !f.ledger.Workers[0].Active || writes != 0 {
				t.Fatal("container status implied stop", err, writes)
			}
		})
	}
}

func TestPrivateBudgetOwnershipRefusesChangedOrUnconfirmedOriginals(t *testing.T) {
	cases := map[string]func(*privateBudgetFixture){
		"session-replaced": func(f *privateBudgetFixture) { f.s.UID = "foreign-session" },
		"session-deleting": func(f *privateBudgetFixture) { f.s.DeletionTimestamp = ptr.To(metav1.NewTime(f.now)) },
		"parent-foreign":   func(f *privateBudgetFixture) { f.s.Spec.Parent = "foreign/parent" },
		"shared-topology":  func(f *privateBudgetFixture) { f.s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "foreign"} },
		"task-deadline-changed": func(f *privateBudgetFixture) {
			f.s.Annotations[taskbudget.DeadlineAnnotation] = f.now.Add(time.Hour).Format(time.RFC3339Nano)
		},
		"worker-foreign":         func(f *privateBudgetFixture) { f.ledger.Workers[0].Managed.SessionUID = "foreign-session" },
		"worker-inactive":        func(f *privateBudgetFixture) { f.ledger.Workers[0].Active = false },
		"home-replaced":          func(f *privateBudgetFixture) { f.home.UID = "foreign-home" },
		"home-deleting":          func(f *privateBudgetFixture) { f.home.DeletionTimestamp = ptr.To(metav1.NewTime(f.now)) },
		"home-foreign-owner":     func(f *privateBudgetFixture) { f.home.OwnerReferences[0].UID = "foreign-session" },
		"home-finalizer-missing": func(f *privateBudgetFixture) { f.home.Finalizers = nil },
		"directory-replaced":     func(f *privateBudgetFixture) { f.d.UID = "foreign-directory" },
		"directory-deleting": func(f *privateBudgetFixture) {
			f.d.Finalizers = []string{"synthetic-retained"}
			f.d.DeletionTimestamp = ptr.To(metav1.NewTime(f.now))
		},
		"directory-foreign":   func(f *privateBudgetFixture) { f.d.Spec.Source.UID = "foreign-session" },
		"directory-preparing": func(f *privateBudgetFixture) { f.s.Status.SessionDirectory.State = "Preparing" },
		"directory-digest-changed": func(f *privateBudgetFixture) {
			f.s.Status.SessionDirectory.SpecDigest = commonrules.Digest([]byte("different"))
		},
		"intent-changed":       func(f *privateBudgetFixture) { f.s.Status.PrivateHistory.ExecutorIntent = "foreign-intent" },
		"intent-not-attempted": func(f *privateBudgetFixture) { f.s.Status.PrivateHistory.ExecutorAttempted = false },
		"executor-unconfirmed": func(f *privateBudgetFixture) { f.s.Status.PrivateHistory.LastExecutor = nil },
		"executor-replaced":    func(f *privateBudgetFixture) { f.pod.UID = "foreign-pod" },
		"executor-deleting": func(f *privateBudgetFixture) {
			f.pod.Finalizers = []string{"synthetic-retained"}
			f.pod.DeletionTimestamp = ptr.To(metav1.NewTime(f.now))
		},
		"executor-foreign-owner": func(f *privateBudgetFixture) { f.pod.OwnerReferences[0].UID = "foreign-session" },
		"executor-home-alias":    func(f *privateBudgetFixture) { f.pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "foreign-home" },
		"executor-shared-pvc": func(f *privateBudgetFixture) {
			f.pod.Spec.Volumes = append(f.pod.Spec.Volumes, corev1.Volume{Name: "shared", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "foreign-shared"}}})
		},
		"executor-restartable":   func(f *privateBudgetFixture) { f.pod.Spec.RestartPolicy = corev1.RestartPolicyAlways },
		"executor-image-changed": func(f *privateBudgetFixture) { f.pod.Spec.Containers[0].Image = "foreign-image" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPrivateBudgetFixture(t, "codex")
			change(&f)
			before, err := json.Marshal(f.ledger)
			if err != nil {
				t.Fatal(err)
			}
			// Rebuild the reader from the changed synthetic originals; mutations
			// are fixture setup, never performed by the proof or inspector.
			c := fake.NewClientBuilder().WithScheme(f.client.Scheme()).WithObjects(f.s, f.home, f.pod, f.d).Build()
			store := &sessiondirectory.KubeStore{Client: c, Live: c, Namespace: f.d.Namespace}
			if proof, err := provePrivateBudgetOwnership(context.Background(), c, store, f.ledger, f.ref); err == nil || proof != nil {
				t.Fatalf("uncertain original accepted: %+v", proof)
			}
			after, err := json.Marshal(f.ledger)
			if err != nil || string(before) != string(after) || f.s.Status.PrivateHistory.StopState != "Unknown" {
				t.Fatal("refusal released effort or changed stop state")
			}
		})
	}
}

func TestPrivateBudgetOwnershipMissingOrUnconfirmedReadsRetainEffort(t *testing.T) {
	for _, kind := range []string{"session", "home", "directory", "pod"} {
		for _, missing := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/unknown", true: "/missing"}[missing], func(t *testing.T) {
				f := newPrivateBudgetFixture(t, "claude")
				objects := []client.Object{f.s, f.home, f.pod, f.d}
				if missing {
					kept := objects[:0]
					for _, object := range objects {
						if privateBudgetObjectKind(object) != kind {
							kept = append(kept, object)
						}
					}
					objects = kept
				}
				base := fake.NewClientBuilder().WithScheme(f.client.Scheme()).WithObjects(objects...).Build()
				c := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
					if !missing && privateBudgetObjectKind(object) == kind {
						return errors.New("synthetic source read unavailable")
					}
					return c.Get(ctx, key, object, opts...)
				}})
				inspector := PrivateManagedAdmissionInspector{Reader: c, Directory: &sessiondirectory.KubeStore{Client: c, Live: c, Namespace: f.d.Namespace}}
				if err := inspector.InspectManagedAdmission(context.Background(), f.ledger, f.ref); err == nil || !f.ledger.Workers[0].Active || f.s.Status.PrivateHistory.StopState != "Unknown" {
					t.Fatal("uncertainty admitted or stopped private work", err)
				}
			})
		}
	}
}

func privateBudgetObjectKind(object client.Object) string {
	switch object.(type) {
	case *v1alpha1.AgentSession:
		return "session"
	case *corev1.PersistentVolumeClaim:
		return "home"
	case *coord.SessionDirectory:
		return "directory"
	case *corev1.Pod:
		return "pod"
	}
	return "other"
}

func TestPrivateBudgetOwnershipRefusesChangesDuringIndependentReads(t *testing.T) {
	for _, kind := range []string{"session", "home", "directory", "pod"} {
		t.Run(kind, func(t *testing.T) {
			f := newPrivateBudgetFixture(t, "codex")
			reads := 0
			c := interceptor.NewClient(f.client, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, object, opts...); err != nil {
					return err
				}
				if privateBudgetObjectKind(object) == kind {
					reads++
					if reads == 2 {
						object.SetResourceVersion("changed-after-first-read")
					}
				}
				return nil
			}})
			store := &sessiondirectory.KubeStore{Client: c, Live: c, Namespace: f.d.Namespace}
			if proof, err := provePrivateBudgetOwnership(context.Background(), c, store, f.ledger, f.ref); err == nil || proof != nil || reads != 2 {
				t.Fatal("changed observation accepted", err, reads)
			}
		})
	}
}

func TestPrivateBudgetReservationSeamHasNoDefaultAuthorityOrDispatchMutations(t *testing.T) {
	f := newPrivateBudgetFixture(t, "codex")
	reads := 0
	c := interceptor.NewClient(f.client, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		reads++
		return errors.New("unexpected read")
	}})
	for _, inspector := range []PrivateManagedAdmissionInspector{{}, {Reader: c}} {
		if err := inspector.InspectManagedAdmission(context.Background(), f.ledger, f.ref); !errors.Is(err, taskbudget.ErrUnavailable) || reads != 0 {
			t.Fatal("unconfigured seam gained authority", err, reads)
		}
	}
	// Existing shared admission remains the production inspector and refuses a
	// private Session even when its original ownership can be proved separately.
	shared := ManagedPodAdmissionInspector{Reader: f.client, Image: f.pod.Spec.Containers[0].Image}
	ref := f.ref
	ref.PodUID = ""
	if err := shared.InspectManagedAdmission(context.Background(), f.ledger, ref); !errors.Is(err, taskbudget.ErrDenied) {
		t.Fatal("shared admission gained private authority", err)
	}
	if _, err := (&Reconciler{APIReader: f.client, TaskBudgets: f.service}).observeTaskBudget(context.Background(), f.s, f.pod); !errors.Is(err, taskbudget.ErrUnavailable) {
		t.Fatal("controller budget observation gained private authority", err)
	}
}

type privateBudgetUnknownACKStore struct {
	taskbudget.Store
	updates int
}

func (s *privateBudgetUnknownACKStore) Update(ctx context.Context, l *taskbudget.Ledger, rv string) error {
	s.updates++
	if err := s.Store.Update(ctx, l, rv); err != nil {
		return err
	}
	return taskbudget.ErrUnavailable
}

type privateBudgetContentionStore struct {
	taskbudget.Store
	reads int
}

func (s *privateBudgetContentionStore) Read(ctx context.Context, uid string) (*taskbudget.Ledger, string, error) {
	l, rv, err := s.Store.Read(ctx, uid)
	s.reads++
	if err != nil || s.reads != 1 {
		return l, rv, err
	}
	// Inject a retained concurrent reservation after this read, without
	// constructing a positive private admission path. The stale reader must
	// not erase it, whether or not it writes.
	prior, priorRV, err := s.Store.Read(ctx, uid)
	if err != nil {
		return nil, "", err
	}
	prior.Workers[0].Managed.LaunchRequested = true
	if err := s.Update(ctx, prior, priorRV); err != nil {
		return nil, "", err
	}
	return l, rv, nil
}

func TestPrivateBudgetRetainedIntentReservationCannotRearmOrResetHistory(t *testing.T) {
	for _, failure := range []string{"lost-ack", "contention", "no-capability"} {
		t.Run(failure, func(t *testing.T) {
			f := newPrivateBudgetFixture(t, "codex")
			ctx := context.Background()
			if err := f.client.Delete(ctx, f.pod); err != nil {
				t.Fatal(err)
			}
			f.s.Status.PrivateHistory.LastExecutor = nil
			if err := f.client.Status().Update(ctx, f.s); err != nil {
				t.Fatal(err)
			}
			original, rv, err := f.service.Store.Read(ctx, f.ledger.Binding.TaskUID)
			if err != nil {
				t.Fatal(err)
			}
			// Fixture setup represents the original uncertain create: its intent
			// and worker remain recorded, with no independently bound Pod UID.
			original.Workers[0].Managed.PodUID = ""
			original.Workers[0].Managed.LaunchRequested = failure == "lost-ack"
			original.Events = append(original.Events, taskbudget.Event{At: f.now, Request: apiv1.TaskBudgetEvent{Binding: original.Binding,
				ID: "retained-failure", Kind: "failure", WorkerID: original.Workers[0].ID, AttemptID: "retained-attempt", BlockerID: canonicalCheckpointBlocker(original.Binding),
				Evidence: apiv1.TaskBudgetEvidence{ID: "retained-receipt", Kind: "retained-fixture", Reference: "original"}}})
			if err := f.service.Store.Update(ctx, original, rv); err != nil {
				t.Fatal(err)
			}
			store := f.service.Store
			f.service.Now = func() time.Time { return f.now.Add(time.Minute) }
			f.service.ManagedInspector = PrivateManagedAdmissionInspector{Reader: f.client, Directory: f.store}
			var unknown *privateBudgetUnknownACKStore
			var contending *privateBudgetContentionStore
			switch failure {
			case "lost-ack":
				unknown = &privateBudgetUnknownACKStore{Store: store}
				f.service.Store = unknown
			case "contention":
				contending = &privateBudgetContentionStore{Store: store}
				f.service.Store = contending
			}
			if _, err := f.service.ReserveManagedLaunch(ctx, original.Binding, original.Workers[0].ID); err == nil {
				t.Fatal("retained intent gained private launch authority")
			}
			// A refusal changes nothing durable but elapsed accounting, so it
			// need not write; an unknown ACK must never be replayed either way.
			if unknown != nil && unknown.updates > 1 || contending != nil && contending.reads == 0 {
				t.Fatal("unknown ACK retried or contention not exercised")
			}
			f.service.Store = store
			if _, err := f.service.ReserveManagedLaunch(ctx, original.Binding, original.Workers[0].ID); err == nil {
				t.Fatal("retry rearmed uncertain private executor")
			}
			retained, _, err := store.Read(ctx, original.Binding.TaskUID)
			if err != nil || retained.Binding != original.Binding || !retained.Workers[0].Active || retained.Workers[0].Managed.PodUID != "" ||
				retained.Workers[0].Managed.LaunchRequested != (failure != "no-capability") ||
				!reflect.DeepEqual(retained.Events, original.Events) || retained.LastProgress != original.LastProgress || retained.StallAnchor != original.StallAnchor || retained.NextCheckpoint != original.NextCheckpoint {
				t.Fatalf("original accounting/history changed: %+v %v", retained, err)
			}
			// Elapsed effort the refusals did not write is charged by the next
			// observation or write, from the retained AccountedAt.
			if observed, err := f.service.Observe(ctx, original.Binding); err != nil || observed.EffortMilliseconds != 60000 {
				t.Fatal("charged effort lost", err)
			}
			var source v1alpha1.AgentSession
			if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.s), &source); err != nil || !reflect.DeepEqual(source.Status.PrivateHistory, f.s.Status.PrivateHistory) || source.Status.PrivateHistory.StopState != "Unknown" {
				t.Fatal("reservation changed retained private history", err)
			}
			var pods corev1.PodList
			if err := f.client.List(ctx, &pods); err != nil || len(pods.Items) != 0 {
				t.Fatal("proof core created or replaced executor", err)
			}
		})
	}
}
