package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func retentionFixture() workspaceStopFixture {
	f := stoppedWorkspaceFixture()
	at := metav1.NewTime(f.now)
	f.s.DeletionTimestamp = &at
	f.s.Finalizers = []string{Finalizer, "fixture/keep-session"}
	f.s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueNoWorkAdmitted, PreservationKind: "NoWorkAdmitted",
		SourcePodUID: string(f.pod.UID), PodUID: "hold-1", Generation: f.s.Generation, Stamp: "20261009-1200", At: &at,
		// An older locator must not be mistaken for this no-work result.
		LastBundle: "rescue/previous/20261009-1100/manifest.json",
		SharedProof: &v1alpha1.SharedRescueProof{Version: 1, Workspace: f.s.Spec.Workspace.ID, Task: f.s.Name,
			Repo: f.s.Spec.Repo, SessionUID: string(f.s.UID), SourcePodUID: string(f.pod.UID), PrivateHomeUID: string(f.home.UID), Kind: "NoWorkAdmitted", NoOwner: true}}
	f.home.Labels = map[string]string{"fixture/keep": "label"}
	f.home.Annotations = map[string]string{"fixture/keep": "annotation"}
	f.home.OwnerReferences = append(f.home.OwnerReferences, metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "other-owner", UID: "other-owner-uid", Controller: ptr.To(false)})
	f.home.Finalizers = append(f.home.Finalizers, "fixture/keep-home")
	f.home.Spec.VolumeName = "original-provider-home"
	return f
}

func retentionClient(f workspaceStopFixture, extra ...client.Object) client.Client {
	objects := append([]client.Object{f.s, f.home}, extra...)
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}).WithObjects(objects...).Build()
}

func requireRetentionBlocked(t *testing.T, c client.Client, s *v1alpha1.AgentSession, obs observation) {
	t.Helper()
	var current v1alpha1.AgentSession
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), &current); err != nil || !controllerutil.ContainsFinalizer(&current, Finalizer) || obs.removalBlocked == nil {
		t.Fatalf("uncertain retention released Session: err=%v finalizers=%v block=%v", err, current.Finalizers, obs.removalBlocked)
	}
	var home corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: s.Namespace, Name: HomeClaimName(s.Name)}, &home); err != nil {
		t.Fatalf("home disappeared: %v", err)
	}
	if !home.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(&home, Finalizer) {
		t.Fatal("uncertain retention changed the home deletion state/finalizer")
	}
}

func TestSharedReapRetainsOriginalPrivateHomeAndEveryOtherField(t *testing.T) {
	f := retentionFixture()
	c := retentionClient(f)
	r, obs := workspaceFixtureController(t, f, c)
	if _, err := r.reconcileSharedWorkspace(context.Background(), f.s, &obs); err != nil || obs.removalBlocked != nil {
		t.Fatalf("retention failed: err=%v block=%v", err, obs.removalBlocked)
	}
	var home corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.home), &home); err != nil {
		t.Fatal(err)
	}
	if home.UID != f.home.UID || !home.DeletionTimestamp.IsZero() || home.Spec.VolumeName != f.home.Spec.VolumeName ||
		!apiequality.Semantic.DeepEqual(home.Spec, f.home.Spec) || !apiequality.Semantic.DeepEqual(home.Finalizers, f.home.Finalizers) ||
		!apiequality.Semantic.DeepEqual(home.OwnerReferences, f.home.OwnerReferences[1:]) || home.Labels["fixture/keep"] != "label" || home.Annotations["fixture/keep"] != "annotation" {
		t.Fatalf("retention changed fields beyond its exact owner and receipt: %+v", home)
	}
	if home.Labels[retainedPrivateHomeLabel] != "true" || home.Labels[retainedSessionUIDLabel] != string(f.s.UID) || home.Annotations[privateHomeReceiptAnnotation] == "" {
		t.Fatal("retained private home cannot be discovered")
	}
	if strings.Contains(home.Annotations[privateHomeReceiptAnnotation], "previous") || strings.Contains(home.Annotations[privateHomeReceiptAnnotation], "auth") {
		t.Fatal("receipt inherited an older backup locator or private provider data")
	}
	var s v1alpha1.AgentSession
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &s); err != nil {
		t.Fatal(err)
	}
	if controllerutil.ContainsFinalizer(&s, Finalizer) || !controllerutil.ContainsFinalizer(&s, "fixture/keep-session") || s.Status.ArchivedAt != nil {
		t.Fatal("retention did not release only the Session rescue finalizer")
	}
}

type retentionPatchClient struct {
	client.Client
	beforeHome    func(context.Context)
	afterHome     func(context.Context)
	beforeSession func(context.Context)
	lostACK       bool
	reject        bool
	homeWrites    int
}

func (c *retentionPatchClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if _, ok := obj.(*corev1.PersistentVolumeClaim); !ok {
		if c.beforeSession != nil {
			c.beforeSession(ctx)
		}
		return c.Client.Patch(ctx, obj, patch, opts...)
	}
	c.homeWrites++
	if c.beforeHome != nil {
		c.beforeHome(ctx)
	}
	if c.reject {
		return errors.New("fixture write never committed")
	}
	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}
	if c.afterHome != nil {
		c.afterHome(ctx)
	}
	if c.lostACK {
		return errors.New("fixture lost write acknowledgement")
	}
	return nil
}

type retentionReader struct {
	client.Reader
	beforeGet func(client.Object) error
}

func (r retentionReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := r.beforeGet(obj); err != nil {
		return err
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestSharedRetentionSuccessfulACKStillNeedsUncachedConfirmation(t *testing.T) {
	f := retentionFixture()
	base := retentionClient(f)
	c := &retentionPatchClient{Client: base}
	r, obs := workspaceFixtureController(t, f, c)
	r.APIReader = retentionReader{Reader: base, beforeGet: func(obj client.Object) error {
		if _, ok := obj.(*corev1.PersistentVolumeClaim); ok && c.homeWrites > 0 {
			return errors.New("fixture uncached confirmation unavailable")
		}
		return nil
	}}
	if _, err := r.retainSharedPrivateHome(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	requireRetentionBlocked(t, base, f.s, obs)
	if c.homeWrites != 1 {
		t.Fatal("fixture did not acknowledge the atomic retention write")
	}
}

func TestSharedRetentionFinalizerWriteRejectsChangedSessionIdentityOrVersion(t *testing.T) {
	for _, changeUID := range []bool{false, true} {
		t.Run(map[bool]string{false: "resourceVersion", true: "UID"}[changeUID], func(t *testing.T) {
			f := retentionFixture()
			base := retentionClient(f)
			c := &retentionPatchClient{Client: base}
			c.beforeSession = func(ctx context.Context) {
				var current v1alpha1.AgentSession
				if err := base.Get(ctx, client.ObjectKeyFromObject(f.s), &current); err != nil {
					t.Fatal(err)
				}
				if changeUID {
					current.UID = "new-session"
				} else {
					current.Labels = map[string]string{"fixture/changed": "true"}
				}
				if err := base.Update(ctx, &current); err != nil {
					t.Fatal(err)
				}
			}
			r, obs := workspaceFixtureController(t, f, c)
			if _, err := r.retainSharedPrivateHome(context.Background(), f.s, &obs); err != nil {
				t.Fatal(err)
			}
			requireRetentionBlocked(t, base, f.s, obs)
		})
	}
}

func TestSharedRetentionLostAcknowledgementWaitsForIndependentConfirmation(t *testing.T) {
	f := retentionFixture()
	base := retentionClient(f)
	c := &retentionPatchClient{Client: base, lostACK: true}
	r, obs := workspaceFixtureController(t, f, c)
	if _, err := r.retainSharedPrivateHome(context.Background(), f.s, &obs); err != nil {
		t.Fatal(err)
	}
	requireRetentionBlocked(t, base, f.s, obs)
	var detached corev1.PersistentVolumeClaim
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.home), &detached); err != nil {
		t.Fatal(err)
	}
	if controlledBySessionObject(&detached, f.s) || detached.Annotations[privateHomeReceiptAnnotation] == "" {
		t.Fatal("fixture did not commit detach before losing ACK")
	}
	c.lostACK = false
	if _, err := r.retainSharedPrivateHome(context.Background(), f.s, &obs); err != nil || obs.removalBlocked != nil {
		t.Fatalf("durable receipt not recovered: %v %v", err, obs.removalBlocked)
	}
	if c.homeWrites != 1 {
		t.Fatal("receipt recovery repeated the detach")
	}
}

func TestSharedRetentionRejectsUncommittedOrReplacedHomeWrites(t *testing.T) {
	for _, mode := range []string{"uncommitted", "PVC UID replaced", "owner changed"} {
		t.Run(mode, func(t *testing.T) {
			f := retentionFixture()
			base := retentionClient(f)
			c := &retentionPatchClient{Client: base, reject: mode == "uncommitted"}
			if mode != "uncommitted" {
				c.beforeHome = func(ctx context.Context) {
					var home corev1.PersistentVolumeClaim
					if err := base.Get(ctx, client.ObjectKeyFromObject(f.home), &home); err != nil {
						t.Fatal(err)
					}
					if mode == "PVC UID replaced" {
						home.UID = "replacement-home"
					} else {
						home.OwnerReferences[0].UID = "foreign-session"
					}
					if err := base.Update(ctx, &home); err != nil {
						t.Fatal(err)
					}
				}
			}
			r, obs := workspaceFixtureController(t, f, c)
			if _, err := r.retainSharedPrivateHome(context.Background(), f.s, &obs); err != nil {
				t.Fatal(err)
			}
			requireRetentionBlocked(t, base, f.s, obs)
			var home corev1.PersistentVolumeClaim
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.home), &home); err != nil {
				t.Fatal(err)
			}
			if home.Annotations[privateHomeReceiptAnnotation] != "" {
				t.Fatal("conditional write detached a changed/uncommitted home")
			}
		})
	}
}

func TestSharedRetentionNewPodAfterDetachKeepsSessionFinalizer(t *testing.T) {
	for _, name := range []string{"executor", "hold"} {
		t.Run(name, func(t *testing.T) {
			f := retentionFixture()
			base := retentionClient(f)
			c := &retentionPatchClient{Client: base}
			c.afterHome = func(ctx context.Context) {
				podName := f.s.Name
				if name == "hold" {
					podName = workspaceHoldName(f.s)
				}
				if err := base.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: f.s.Namespace, Name: podName, UID: types.UID("new-" + name)}}); err != nil {
					t.Fatal(err)
				}
			}
			r, obs := workspaceFixtureController(t, f, c)
			if _, err := r.retainSharedPrivateHome(context.Background(), f.s, &obs); err != nil {
				t.Fatal(err)
			}
			requireRetentionBlocked(t, base, f.s, obs)
		})
	}
}

func TestSharedRetentionRefusesIncompleteAndContradictoryEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*workspaceStopFixture)
	}{
		{"legacy untyped", func(f *workspaceStopFixture) { f.s.Status.Rescue.SharedProof = nil }},
		{"original home unknown", func(f *workspaceStopFixture) { f.s.Status.SharedPrivateHomeUID = "" }},
		{"original home replaced before rescue", func(f *workspaceStopFixture) { f.home.UID = "replacement" }},
		{"foreign owner", func(f *workspaceStopFixture) { f.home.OwnerReferences[0].UID = "foreign" }},
		{"wrong owner kind", func(f *workspaceStopFixture) { f.home.OwnerReferences[0].Kind = "Other" }},
		{"additional session reference", func(f *workspaceStopFixture) {
			f.home.OwnerReferences = append(f.home.OwnerReferences, metav1.OwnerReference{APIVersion: "v1", Kind: "AgentSession", UID: f.s.UID})
		}},
		{"writer zero without no-owner", func(f *workspaceStopFixture) { f.s.Status.Rescue.SharedProof.NoOwner = false }},
		{"admitted writer with no-owner", func(f *workspaceStopFixture) { f.s.Status.Rescue.SharedProof.WriterGeneration = 1 }},
		{"wrong Session UID", func(f *workspaceStopFixture) { f.s.Status.Rescue.SharedProof.SessionUID = "other" }},
		{"wrong source UID", func(f *workspaceStopFixture) { f.s.Status.Rescue.SharedProof.SourcePodUID = "other" }},
		{"wrong workspace", func(f *workspaceStopFixture) { f.s.Status.Rescue.SharedProof.Workspace = "other" }},
		{"wrong repo", func(f *workspaceStopFixture) { f.s.Status.Rescue.SharedProof.Repo = "other" }},
		{"old generation", func(f *workspaceStopFixture) { f.s.Status.Rescue.Generation-- }},
		{"superseded", func(f *workspaceStopFixture) { f.s.Status.Rescue.Superseded = true }},
		{"same executor/hold", func(f *workspaceStopFixture) { f.s.Status.Rescue.PodUID = f.s.Status.Rescue.SourcePodUID }},
		{"untyped no-work", func(f *workspaceStopFixture) { f.s.Status.Rescue.PreservationKind = "" }},
		{"no-work manifest", func(f *workspaceStopFixture) { f.s.Status.Rescue.SharedProof.Manifest = f.s.Status.Rescue.LastBundle }},
		{"inherited older manifest", func(f *workspaceStopFixture) {
			rec, p := f.s.Status.Rescue, f.s.Status.Rescue.SharedProof
			rec.Result, rec.PreservationKind = v1alpha1.RescueVerified, ""
			p.Kind, p.WriterGeneration, p.NoOwner = "TaskWorkPreserved", 2, false
			p.Manifest = "rescue/" + f.s.Name + "/20261009-1100/manifest.json"
			rec.LastBundle = p.Manifest
		}},
		{"existing bad receipt", func(f *workspaceStopFixture) { f.home.Annotations[privateHomeReceiptAnnotation] = "{}" }},
		{"failed rescue", func(f *workspaceStopFixture) { f.s.Status.Rescue.Result = v1alpha1.RescueFailed }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := retentionFixture()
			tc.edit(&f)
			base := retentionClient(f)
			c := &retentionPatchClient{Client: base}
			r, obs := workspaceFixtureController(t, f, c)
			if _, err := r.retainSharedPrivateHome(context.Background(), f.s, &obs); err != nil {
				t.Fatal(err)
			}
			requireRetentionBlocked(t, base, f.s, obs)
			if c.homeWrites != 0 {
				t.Fatal("incomplete evidence caused a PVC write")
			}
		})
	}
}

func TestSharedRetentionAdmittedWriterKeepsCurrentRescueLocator(t *testing.T) {
	for _, result := range []v1alpha1.RescueResult{v1alpha1.RescueNoWorkAdmitted, v1alpha1.RescueCleanAndPushed, v1alpha1.RescueVerified} {
		t.Run(string(result), func(t *testing.T) {
			f := retentionFixture()
			rec, p := f.s.Status.Rescue, f.s.Status.Rescue.SharedProof
			p.WriterGeneration, p.NoOwner = 3, false
			rec.Result = result
			if result != v1alpha1.RescueNoWorkAdmitted {
				p.Kind, rec.PreservationKind = "TaskWorkPreserved", ""
			}
			if result == v1alpha1.RescueVerified {
				p.Manifest = "rescue/" + f.s.Name + "/20261009-1200/manifest.json"
				rec.LastBundle = p.Manifest
			}
			c := retentionClient(f)
			r, obs := workspaceFixtureController(t, f, c)
			if _, err := r.retainSharedPrivateHome(context.Background(), f.s, &obs); err != nil || obs.removalBlocked != nil {
				t.Fatalf("admitted result rejected: %v %v", err, obs.removalBlocked)
			}
		})
	}
}

func TestSharedPrivateHomeBindingPrecedesFirstExecutorAndRefusesReplacement(t *testing.T) {
	f := stoppedWorkspaceFixture()
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	f.s.Status.PodName, f.s.Status.SharedPrivateHomeUID = "", ""
	c := retentionClient(f)
	r, obs := workspaceFixtureController(t, f, c)
	obs.pod = owned[*corev1.Pod]{missing: true}
	if err := r.bindSharedPrivateHome(context.Background(), f.s); err != nil {
		t.Fatal(err)
	}
	var bound v1alpha1.AgentSession
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &bound); err != nil || bound.Status.SharedPrivateHomeUID != string(f.home.UID) {
		t.Fatalf("original home not durable: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("binding created executor: %v", err)
	}
	var home corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.home), &home); err != nil {
		t.Fatal(err)
	}
	home.UID = "replacement"
	if err := c.Update(context.Background(), &home); err != nil {
		t.Fatal(err)
	}
	if err := r.ensure(context.Background(), f.s, obs.templates, &obs); err != nil {
		t.Fatal(err)
	}
	if obs.removalBlocked == nil {
		t.Fatal("executor admitted replacement of original home")
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("replacement executor created: %v", err)
	}
}

type admissionStatusClient struct {
	client.Client
	lostACK      bool
	statusWrites int
	admitted     bool
	t            *testing.T
}

type admissionStatusWriter struct {
	client.SubResourceWriter
	c *admissionStatusClient
}

func (c *admissionStatusClient) Status() client.SubResourceWriter {
	return &admissionStatusWriter{SubResourceWriter: c.Client.Status(), c: c}
}

func (w *admissionStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	w.c.statusWrites++
	if err := w.SubResourceWriter.Update(ctx, obj, opts...); err != nil {
		return err
	}
	if w.c.lostACK {
		return errors.New("fixture lost private home binding ACK")
	}
	return nil
}

func (c *admissionStatusClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if pod, ok := obj.(*corev1.Pod); ok {
		var session v1alpha1.AgentSession
		if err := c.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, &session); err != nil {
			c.t.Fatal(err)
		}
		if session.Status.SharedPrivateHomeUID != "home-1" {
			c.t.Fatal("executor created before durable original home binding")
		}
		c.admitted = true
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestSharedExecutorAdmissionWaitsAfterLostBindingAcknowledgement(t *testing.T) {
	f := stoppedWorkspaceFixture()
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	f.s.Status.PodName, f.s.Status.SharedPrivateHomeUID = "", ""
	c := &admissionStatusClient{Client: retentionClient(f), lostACK: true, t: t}
	r, obs := workspaceFixtureController(t, f, c)
	obs.pod = owned[*corev1.Pod]{missing: true}
	if err := r.ensure(context.Background(), f.s, obs.templates, &obs); err != nil {
		t.Fatal(err)
	}
	if c.admitted || obs.removalBlocked == nil {
		t.Fatal("lost admission ACK created an executor")
	}
	c.lostACK, obs.removalBlocked = false, nil
	if err := r.ensure(context.Background(), f.s, obs.templates, &obs); err != nil {
		t.Fatal(err)
	}
	if !c.admitted || obs.removalBlocked != nil || c.statusWrites != 1 {
		t.Fatalf("durable admission not recovered: admitted=%v block=%v writes=%d", c.admitted, obs.removalBlocked, c.statusWrites)
	}
}

func TestRetainedPrivateHomeHasNoOrphanCleanupOrTaskReusePath(t *testing.T) {
	f := retentionFixture()
	base := retentionClient(f)
	c := &retentionPatchClient{Client: base}
	r, obs := workspaceFixtureController(t, f, c)
	if _, err := r.retainSharedPrivateHome(context.Background(), f.s, &obs); err != nil || obs.removalBlocked != nil {
		t.Fatalf("retention: %v %v", err, obs.removalBlocked)
	}
	var deleted v1alpha1.AgentSession
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.s), &deleted); err != nil {
		t.Fatal(err)
	}
	deleted.Finalizers = nil
	if err := base.Update(context.Background(), &deleted); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
		t.Fatal(err)
	}
	var retained corev1.PersistentVolumeClaim
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.home), &retained); err != nil || !controllerutil.ContainsFinalizer(&retained, Finalizer) || c.homeWrites != 1 {
		t.Fatalf("orphan retained home changed: %v", err)
	}
	newSession := f.s.DeepCopy()
	newSession.UID, newSession.ResourceVersion = "new-session", ""
	newSession.DeletionTimestamp = nil
	newSession.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	newSession.Status = v1alpha1.AgentSessionStatus{}
	if err := base.Create(context.Background(), newSession); err != nil {
		t.Fatal(err)
	}
	obs.claim, _ = getOwned(context.Background(), base, newSession, retained.Name, &corev1.PersistentVolumeClaim{})
	obs.pod = owned[*corev1.Pod]{missing: true}
	if err := r.ensure(context.Background(), newSession, obs.templates, &obs); err != nil {
		t.Fatal(err)
	}
	if c.homeWrites != 1 {
		t.Fatal("another task reused a retained private home")
	}
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("retained home got a new executor: %v", err)
	}
}

func TestSharedArchiveCannotReleaseAnEmptyOrForeignClaimLifecycle(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "foreign"}[foreign], func(t *testing.T) {
			f := retentionFixture()
			c := retentionClient(f)
			if !foreign {
				c = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}).WithObjects(f.s).Build()
			}
			r, obs := workspaceFixtureController(t, f, c)
			obs.claim = owned[*corev1.PersistentVolumeClaim]{missing: !foreign}
			if foreign {
				obs.claim.foreign = "other owner"
			}
			if _, _, err := r.archive(context.Background(), f.s, &obs, true); err != nil {
				t.Fatal(err)
			}
			var current v1alpha1.AgentSession
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &current); err != nil || !controllerutil.ContainsFinalizer(&current, Finalizer) || obs.removalBlocked == nil {
				t.Fatalf("shared archive released missing/foreign lifecycle: %v", err)
			}
		})
	}
}

func TestSharedRetentionRequiresAPresentNonDeletingOriginalClaim(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "deleting", true: "missing"}[missing], func(t *testing.T) {
			f := retentionFixture()
			f.home.DeletionTimestamp = f.s.DeletionTimestamp.DeepCopy()
			objects := []client.Object{f.s}
			if !missing {
				objects = append(objects, f.home)
			}
			base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}).WithObjects(objects...).Build()
			c := &retentionPatchClient{Client: base}
			r, obs := workspaceFixtureController(t, f, c)
			if _, err := r.retainSharedPrivateHome(context.Background(), f.s, &obs); err != nil {
				t.Fatal(err)
			}
			var current v1alpha1.AgentSession
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.s), &current); err != nil || !controllerutil.ContainsFinalizer(&current, Finalizer) || obs.removalBlocked == nil || c.homeWrites != 0 {
				t.Fatalf("missing/deleting original home was accepted: %v", err)
			}
			if !missing {
				var home corev1.PersistentVolumeClaim
				if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.home), &home); err != nil || !apiequality.Semantic.DeepEqual(home.Finalizers, f.home.Finalizers) || !apiequality.Semantic.DeepEqual(home.DeletionTimestamp, f.home.DeletionTimestamp) {
					t.Fatalf("deleting home was not preserved: %v", err)
				}
			}
		})
	}
}
