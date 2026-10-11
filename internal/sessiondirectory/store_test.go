package sessiondirectory_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
)

var now = time.Date(2026, 10, 11, 19, 0, 0, 0, time.UTC)

func fixture(t *testing.T, funcs interceptor.Funcs) (*sessiondirectory.KubeStore, client.WithWatch, *v1alpha1.AgentSession) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1alpha1.AddToScheme, coord.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	s := testutil.New(t, "codex", "dev/client").Session
	s.CreationTimestamp = metav1.NewTime(now)
	s.Spec.SessionDirectory = &v1alpha1.SessionDirectoryEnrollment{Version: 1, Project: "sample"}
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: s.Name + "-home", BindingGeneration: 1, Admission: "NeverStarted", Provider: "codex", StopState: "Unknown"}
	create := funcs.Create
	funcs.Create = func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		o.SetCreationTimestamp(metav1.NewTime(now))
		if o.GetUID() == "" {
			o.SetUID(types.UID("directory-original"))
		}
		if create != nil {
			return create(ctx, c, o, opts...)
		}
		return c.Create(ctx, o, opts...)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s).WithStatusSubresource(&v1alpha1.AgentSession{}, &coord.SessionDirectory{}).WithInterceptorFuncs(funcs).Build()
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	return &sessiondirectory.KubeStore{Client: c, Live: c, Namespace: "dev-env-system"}, c, s
}
func record(t *testing.T, store *sessiondirectory.KubeStore, s *v1alpha1.AgentSession) *coord.SessionDirectory {
	t.Helper()
	if err := store.Ensure(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	d, err := store.Get(context.Background(), s.Status.SessionDirectory.Name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func executor(t *testing.T, c client.Client, s *v1alpha1.AgentSession, d *coord.SessionDirectory) {
	t.Helper()
	ctx := context.Background()
	h := s.Status.PrivateHistory
	h.Admission = "Started"
	h.HomeUID = "home-original"
	h.ExecutorAttempted = true
	h.ExecutorIntent = "one-attempt"
	h.LastExecutor = &v1alpha1.PrivateExecutorStatus{PodName: s.Name, PodUID: "pod-original", Generation: 1}
	s.Status.Agent = &v1alpha1.AgentStatus{Status: "busy", ConversationID: "declared-thread", LastHeartbeat: &metav1.Time{Time: now}}
	if err := c.Status().Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: s.Name, UID: "pod-original", Labels: map[string]string{v1alpha1.LabelRevision: string(d.Spec.Revisions.Template)}, Annotations: map[string]string{v1alpha1.LabelPrefix + "private-history-home-uid": "home-original", v1alpha1.LabelPrefix + "private-history-executor-intent": "one-attempt"}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}}, Spec: corev1.PodSpec{NodeName: "fixture-node", Containers: []corev1.Container{{Name: "agent", Image: string(d.Spec.Revisions.Image)}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if err := c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
}
func TestDirectoryOriginalIdentityAndLostPreExecutorRecord(t *testing.T) {
	store, c, s := fixture(t, interceptor.Funcs{})
	d := record(t, store, s)
	if d.Spec.Request.Model != "gpt-6.1-sol" || d.Spec.Request.Provider != "codex" || d.Spec.Revisions.Common.State != "Unknown" || len(d.OwnerReferences) != 0 || s.Status.SessionDirectory.State != "Confirmed" {
		t.Fatal("original metadata/receipt lost", d)
	}
	raw, _ := json.Marshal(d.Spec)
	if strings.Contains(string(raw), s.Spec.Prompt) || strings.Contains(string(raw), "HOME") {
		t.Fatal("content entered metadata")
	}
	if err := c.Delete(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(store.Ensure(context.Background(), s), sessiondirectory.ErrUnavailable) {
		t.Fatal("lost pre-executor record reconstructed")
	}
	if _, err := store.Get(context.Background(), d.Name); !errors.Is(err, sessiondirectory.ErrNotFound) {
		t.Fatal("missing record replaced", err)
	}
	other := d.Spec.Source
	other.UID = "new-session-uid"
	if sessiondirectory.Name(other) == d.Name {
		t.Fatal("name reused as logical identity")
	}
}
func TestDirectoryPreparingLostCreateRefusesSecondAttempt(t *testing.T) {
	attempts := 0
	store, _, s := fixture(t, interceptor.Funcs{Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
		attempts++
		return errors.New("lost create")
	}})
	first := store.Ensure(context.Background(), s)
	second := store.Ensure(context.Background(), s)
	if first == nil || second == nil || attempts != 1 {
		t.Fatal("uncertain create retried", attempts)
	}
}
func TestDirectoryRetainedWitnessAndFreshMissingArchiveForeign(t *testing.T) {
	store, c, s := fixture(t, interceptor.Funcs{})
	d := record(t, store, s)
	executor(t, c, s, d)
	if err := store.Observe(context.Background(), d.Name, now); err != nil {
		t.Fatal(err)
	}
	d, err := store.Get(context.Background(), d.Name)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status.LastWitness == nil || d.Status.LastWitness.DeclaredConversationID != "declared-thread" || !d.Status.LastWitness.Ready || d.Status.ActualModelState != "Unknown" {
		t.Fatal("owned reports missing or inference invented", d.Status)
	}
	s.Status.Phase = v1alpha1.PhaseArchived
	if err := c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	archived := store.Current(context.Background(), d, now)
	if archived.SourceState != "Present" || archived.PlacementState != "Unknown" || archived.Witness != nil || archived.StopState != "Unknown" {
		t.Fatal("archive proved placement or stop", archived)
	}
	s.Finalizers = nil
	if err := c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(context.Background(), d.Name, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	d, err = store.Get(context.Background(), d.Name)
	if err != nil {
		t.Fatal(err)
	}
	current := store.Current(context.Background(), d, now.Add(time.Second))
	if d.Status.LastWitness.PodUID != "pod-original" || current.SourceState != "SourceMissing" || current.Witness != nil || current.StopState != "Unknown" {
		t.Fatal("absence erased witness or proved stop", d.Status, current)
	}
	s.UID = "replacement-session"
	s.ResourceVersion = ""
	s.Status.Phase = v1alpha1.PhaseArchived
	if err := c.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if current = store.Current(context.Background(), d, now); current.SourceState != "Unknown" || current.Witness != nil {
		t.Fatal("replacement adopted", current)
	}
}
func TestDirectoryCASConflictDoesNotReplayOldWitness(t *testing.T) {
	armed, writes := false, 0
	store, c, s := fixture(t, interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
		if d, ok := o.(*coord.SessionDirectory); ok && armed {
			writes++
			var newer coord.SessionDirectory
			if err := c.Get(ctx, client.ObjectKeyFromObject(d), &newer); err != nil {
				return err
			}
			newer.Status = d.Status
			newer.Status.ObservedAt = metav1.NewTime(now.Add(time.Minute))
			newer.Status.LastWitness.ObservedAt = newer.Status.ObservedAt
			if err := c.Status().Update(ctx, &newer); err != nil {
				return err
			}
		}
		return c.SubResource(sub).Update(ctx, o, opts...)
	}})
	d := record(t, store, s)
	executor(t, c, s, d)
	armed = true
	if store.Observe(context.Background(), d.Name, now) == nil || writes != 1 {
		t.Fatal("stale CAS replayed", writes)
	}
	d, err := store.Get(context.Background(), d.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Status.LastWitness.ObservedAt.Equal(&metav1.Time{Time: now.Add(time.Minute)}) {
		t.Fatal("new witness overwritten")
	}
}
func TestDirectoryUnknownStatusAckNoAutomaticRetry(t *testing.T) {
	writes := 0
	store, c, s := fixture(t, interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
		if _, ok := o.(*coord.SessionDirectory); ok {
			writes++
			if err := c.SubResource(sub).Update(ctx, o, opts...); err != nil {
				return err
			}
			return errors.New("unknown acknowledgement")
		}
		return c.SubResource(sub).Update(ctx, o, opts...)
	}})
	d := record(t, store, s)
	executor(t, c, s, d)
	if store.Observe(context.Background(), d.Name, now) == nil || writes != 1 {
		t.Fatal("unknown status write retried", writes)
	}
}
func TestDirectoryLocalProviderIsExplicitAndDisabled(t *testing.T) {
	store, _, s := fixture(t, interceptor.Funcs{})
	d := record(t, store, s)
	spec := d.Spec
	spec.Request.CLI = "opencode"
	spec.Request.Provider = "Unknown"
	if !sessiondirectory.ValidSpec(spec) || spec.Request.Local != nil {
		t.Fatal("CLI inferred local provider")
	}
	spec.Request.Provider = "local"
	if sessiondirectory.ValidSpec(spec) {
		t.Fatal("untyped local inference admitted")
	}
	spec.Request.Local = &coord.DirectoryLocalProvider{Pool: "fixture", RequestedModel: spec.Request.Model, Backend: "local-model", ActualModelState: "Unknown", BackendReadinessState: "Unknown", GPUReadinessState: "Unknown", HouseholdReservationState: "Unknown", RuntimeAdapter: "Disabled"}
	if !sessiondirectory.ValidSpec(spec) {
		t.Fatal("explicit disabled local metadata refused")
	}
	spec.Request.Local.RuntimeAdapter = "Enabled"
	if sessiondirectory.ValidSpec(spec) {
		t.Fatal("directory enabled local runtime")
	}
}

func TestDirectoryCommonRevisionsUseOriginalBindingWithoutBodies(t *testing.T) {
	_, _, s := fixture(t, interceptor.Funcs{})
	metadata := func(name, key, uid, text string) commonrules.Source {
		return commonrules.Source{Namespace: commonrules.Namespace, Name: name, Key: key, UID: uid, ResourceVersion: "7", Text: text}
	}
	snapshot, err := commonrules.New(metadata(commonrules.CommonName, commonrules.CommonKey, "common-original", "PRIVATE_SYNTHETIC_COMMON_BODY"), metadata(commonrules.HeaderName, commonrules.HeaderKey, "header-original", "PRIVATE_SYNTHETIC_HEADER_BODY"), s.Spec.ProjectCanary.SnapshotDigest)
	if err != nil {
		t.Fatal(err)
	}
	r, err := projectcanary.BoundRecord(s)
	if err != nil {
		t.Fatal(err)
	}
	r.CommonRevision = snapshot.CommonRevision
	evidence := projectcanary.CatalogEvidence{Snapshot: []byte(s.Annotations[v1alpha1.AnnotationProjectSnapshot]), UID: s.Spec.ProjectCanary.CatalogUID, ResourceVersion: s.Spec.ProjectCanary.CatalogResourceVersion}
	s.Spec.ProjectCanary = r.Binding(evidence, testutil.Owner)
	s.Spec.CommonRules, err = snapshot.Binding()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := sessiondirectory.Spec(s)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(spec)
	if spec.Revisions.Common.State != "Bound" || string(spec.Revisions.Common.Revision) != snapshot.CommonRevision || spec.Revisions.Common.Common.UID != "common-original" || spec.Revisions.Common.Header.ResourceVersion != "7" || strings.Contains(string(raw), "PRIVATE_SYNTHETIC") {
		t.Fatal("common provenance recaptured, lost or leaked", string(raw))
	}
	spec.Request.Model = coord.DirectoryText(strings.Repeat("é", 129))
	if sessiondirectory.ValidSpec(spec) {
		t.Fatal("UTF-8 byte bound replaced by character bound")
	}
}
