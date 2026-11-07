package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// Future-only lifecycle comparison. The paired arms are the old image and the
// new image, each with rollout.newSessionsOnlyImage equal to that arm's own
// image. A separate same-new-image arm with no hold is the drain control.
// Assertions are stable reasons, requeues, and forwarded client operations.
//
// A reap pairs a Session whose deletionTimestamp is set with a scheduled
// Running Pod whose deletionTimestamp is nil. removePod returns before rescue
// and delete when that timestamp is already set. Executor rows and hold=true
// rows are separate. The blocked stage returns an error before any rescue
// report is recorded. A later answer on the same client is the only change.
// A persisted Failed result is characterized as the current guard: it can
// permit an executor Pod delete while the PVC remains, and it does not permit
// a hold Pod delete. That is not a production-defect claim.

const (
	futureRestartCount int32 = 3
	futurePodRV              = "7"
)

type futureArm struct {
	name string
	doc  string
	tmpl *templates.Templates
}

func futureArms(t *testing.T) (old, held futureArm) {
	t.Helper()
	old = futureArm{name: "old-held", doc: rolloutDoc(t, rolloutDigest1, rolloutDigest1), tmpl: parseRollout(t, rolloutDigest1, rolloutDigest1)}
	held = futureArm{name: "new-held", doc: rolloutDoc(t, rolloutDigest2, rolloutDigest2), tmpl: parseRollout(t, rolloutDigest2, rolloutDigest2)}
	if !old.tmpl.NewSessionsOnly() || !held.tmpl.NewSessionsOnly() || old.tmpl.Image == held.tmpl.Image || old.tmpl.Rollout.NewSessionsOnlyImage != old.tmpl.Image || held.tmpl.Rollout.NewSessionsOnlyImage != held.tmpl.Image {
		t.Fatalf("old hold %v image %s field %s; held hold %v image %s field %s", old.tmpl.NewSessionsOnly(), old.tmpl.Image, old.tmpl.Rollout.NewSessionsOnlyImage, held.tmpl.NewSessionsOnly(), held.tmpl.Image, held.tmpl.Rollout.NewSessionsOnlyImage)
	}
	return old, held
}

// futureOpen is the same image with no rollout hold. It is the drain control,
// not one of the paired held arms.
func futureOpen(t *testing.T, digest string) futureArm {
	t.Helper()
	arm := futureArm{name: "open", doc: rolloutDoc(t, digest, ""), tmpl: parseRollout(t, digest, "")}
	if arm.tmpl.NewSessionsOnly() || arm.tmpl.Image == "" {
		t.Fatalf("open arm hold %v image %q", arm.tmpl.NewSessionsOnly(), arm.tmpl.Image)
	}
	return arm
}

// futureDelete is one forwarded delete. The proof is read from the stored
// objects before the delete is forwarded.
type futureDelete struct {
	verb, kind, name, uid, image string
	preUID, preRV                string
	storedUID, storedRV          string
	hold                         bool
	rescueResult                 string
	rescuePodUID                 string
	rescueGen                    int64
	rescueSeen                   bool
	homeUID                      string
	homeOK                       bool
}

// futureForwarder records pod and volume writes and every delete, then
// forwards the call. An empty UID is filled with a synthetic value because
// the fake client does not mint one. Deletes are never swallowed.
type futureForwarder struct {
	client.Client
	mu             sync.Mutex
	ops            []string
	order          []string
	deletes        []futureDelete
	seq            int
	homeUID        string
	homeNamespace  string
	homeName       string
	homeFinalizers []string
	homeSpec       corev1.PersistentVolumeClaimSpec
}

func (f *futureForwarder) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if obj.GetUID() == "" {
		f.mu.Lock()
		f.seq++
		n := f.seq
		f.mu.Unlock()
		switch o := obj.(type) {
		case *corev1.Pod:
			o.UID = types.UID(fmt.Sprintf("synthetic-pod-%d", n))
		case *corev1.PersistentVolumeClaim:
			o.UID = types.UID(fmt.Sprintf("synthetic-vol-%d", n))
		}
	}
	err := f.Client.Create(ctx, obj, opts...)
	f.note(opWord("create", err), obj)
	return err
}

func (f *futureForwarder) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	options := &client.DeleteOptions{}
	for _, opt := range opts {
		opt.ApplyToDelete(options)
	}
	proof := futureDelete{verb: "delete", kind: futureKind(obj), name: obj.GetName(), uid: string(obj.GetUID())}
	if options.Preconditions != nil {
		if options.Preconditions.UID != nil {
			proof.preUID = string(*options.Preconditions.UID)
		}
		if options.Preconditions.ResourceVersion != nil {
			proof.preRV = *options.Preconditions.ResourceVersion
		}
	}
	if pod, ok := obj.(*corev1.Pod); ok {
		proof.image = agentImage(pod)
		var stored corev1.Pod
		if err := f.Client.Get(ctx, client.ObjectKeyFromObject(pod), &stored); err == nil {
			proof.storedUID = string(stored.UID)
			proof.storedRV = stored.ResourceVersion
			proof.hold = isHoldPod(&stored)
			if proof.image == "" {
				proof.image = agentImage(&stored)
			}
		}
		var session v1alpha1.AgentSession
		if err := f.Client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, &session); err == nil && session.Status.Rescue != nil && session.Status.Rescue.Result != "" {
			proof.rescueSeen = true
			proof.rescueResult = string(session.Status.Rescue.Result)
			proof.rescuePodUID = session.Status.Rescue.PodUID
			proof.rescueGen = session.Status.Rescue.Generation
		}
		f.mu.Lock()
		homeName, homeNamespace, homeUID := f.homeName, f.homeNamespace, f.homeUID
		homeFinalizers := append([]string(nil), f.homeFinalizers...)
		homeSpec := f.homeSpec
		f.mu.Unlock()
		if homeName != "" {
			var storedHome corev1.PersistentVolumeClaim
			if err := f.Client.Get(ctx, client.ObjectKey{Namespace: homeNamespace, Name: homeName}, &storedHome); err == nil {
				proof.homeUID = string(storedHome.UID)
				proof.homeOK = proof.homeUID == homeUID && reflect.DeepEqual(storedHome.Finalizers, homeFinalizers) && apiequality.Semantic.DeepEqual(&storedHome.Spec, &homeSpec)
			}
		}
	}
	err := f.Client.Delete(ctx, obj, opts...)
	if err != nil {
		proof.verb = "delete-error"
	}
	f.mu.Lock()
	f.deletes = append(f.deletes, proof)
	f.mu.Unlock()
	f.note(proof.verb, obj)
	return err
}

func (f *futureForwarder) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	err := f.Client.Update(ctx, obj, opts...)
	f.note(opWord("update", err), obj)
	return err
}

func (f *futureForwarder) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	err := f.Client.Patch(ctx, obj, patch, opts...)
	f.note(opWord("patch", err), obj)
	return err
}

func (f *futureForwarder) Status() client.SubResourceWriter {
	return futureStatus{SubResourceWriter: f.Client.Status(), f: f}
}

type futureStatus struct {
	client.SubResourceWriter
	f *futureForwarder
}

func (s futureStatus) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	err := s.SubResourceWriter.Update(ctx, obj, opts...)
	s.f.note(opWord("update-status", err), obj)
	return err
}

func (s futureStatus) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	err := s.SubResourceWriter.Patch(ctx, obj, patch, opts...)
	s.f.note(opWord("patch-status", err), obj)
	return err
}

func opWord(verb string, err error) string {
	if err != nil {
		return verb + "-error"
	}
	return verb
}

func futureKind(obj client.Object) string {
	switch obj.(type) {
	case *corev1.Pod:
		return "Pod"
	case *corev1.PersistentVolumeClaim:
		return "PersistentVolumeClaim"
	case *v1alpha1.AgentSession:
		return "AgentSession"
	default:
		return fmt.Sprintf("%T", obj)
	}
}

func (f *futureForwarder) note(verb string, obj client.Object) {
	kind := futureKind(obj)
	entry := fmt.Sprintf("%s %s %s uid=%s", verb, kind, obj.GetName(), obj.GetUID())
	if s, ok := obj.(*v1alpha1.AgentSession); ok && verb == "update-status" && s.Status.Rescue != nil && s.Status.Rescue.Result != "" && !s.Status.Rescue.Superseded {
		entry = "record rescue of " + s.Status.Rescue.PodUID
	}
	f.mu.Lock()
	f.order = append(f.order, entry)
	switch obj.(type) {
	case *corev1.Pod, *corev1.PersistentVolumeClaim:
		f.ops = append(f.ops, entry)
	}
	f.mu.Unlock()
}

func (f *futureForwarder) operations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

func (f *futureForwarder) orderLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

func (f *futureForwarder) forwardedDeletes() []futureDelete {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]futureDelete(nil), f.deletes...)
}

func (f *futureForwarder) expectHome(home *corev1.PersistentVolumeClaim) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.homeUID = string(home.UID)
	f.homeNamespace = home.Namespace
	f.homeName = home.Name
	f.homeFinalizers = append([]string(nil), home.Finalizers...)
	f.homeSpec = *home.Spec.DeepCopy()
}

func futureClient(t *testing.T, doc string, objs ...client.Object) *futureForwarder {
	t.Helper()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}, Data: map[string]string{templates.Key: doc}}
	all := append([]client.Object{cm.DeepCopy()}, objs...)
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}, &corev1.Pod{}).WithObjects(all...).Build()
	return &futureForwarder{Client: base}
}

type futureReconcile struct {
	client    *futureForwarder
	rescuer   *fakeRescuer
	restarter *fakeRestarter
	events    *events.FakeRecorder
	r         *Reconciler
}

func newFutureReconcile(t *testing.T, doc string, objs ...client.Object) futureReconcile {
	t.Helper()
	c := futureClient(t, doc, objs...)
	rescuer := &fakeRescuer{
		answers: map[string]func(*corev1.Pod) (protocol.RescueReport, error){},
		calls:   map[string]int{},
	}
	rt := &fakeRestarter{fn: func(pod *corev1.Pod) (protocol.RestartReport, error) {
		return protocol.RestartReport{Session: pod.Name, ConversationID: "conv-future", Resumable: true}, nil
	}}
	rec := events.NewFakeRecorder(32)
	return futureReconcile{client: c, rescuer: rescuer, restarter: rt, events: rec, r: &Reconciler{
		Client: c, APIReader: c, Templates: templatesKey, Rescuer: rescuer, Restarter: rt, Recorder: rec,
	}}
}

func (a *futureReconcile) useRescue(t *testing.T, name string, rescue func(*corev1.Pod) (protocol.RescueReport, error)) {
	t.Helper()
	if rescue == nil {
		return
	}
	a.rescuer.answer(t, name, rescue)
}

func eventReasons(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			parts := strings.SplitN(e, " ", 4)
			if len(parts) >= 2 {
				out = append(out, parts[1])
			} else {
				out = append(out, e)
			}
		default:
			return out
		}
	}
}

// pinRetainedPod stamps the identity both image arms share. The image is the
// old template image even in the held arm; only the templates change.
func pinRetainedPod(pod *corev1.Pod, image, uid string, hold bool) {
	pod.UID = types.UID(uid)
	pod.ResourceVersion = futurePodRV
	if len(pod.Spec.Containers) == 0 {
		pod.Spec.Containers = []corev1.Container{{Name: ContainerName}}
	}
	pod.Spec.Containers[0].Name = ContainerName
	pod.Spec.Containers[0].Image = image
	if hold {
		if pod.Labels == nil {
			pod.Labels = map[string]string{}
		}
		pod.Labels[v1alpha1.LabelHold] = "true"
	}
}

func futureReapObjects(revision, image, uid string, hold bool) (*v1alpha1.AgentSession, *corev1.Pod, *corev1.PersistentVolumeClaim) {
	s := taskSession()
	s.Name = "future-reap"
	s.Namespace = sessionNS
	s.UID = "future-reap-session"
	s.ResourceVersion = "3"
	s.Generation = 3
	s.Finalizers = []string{Finalizer}
	s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	deleting := metav1.NewTime(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	s.DeletionTimestamp = &deleting
	pod := readyDrainPod(s.Name, revision)
	pinRetainedPod(pod, image, uid, hold)
	pod.OwnerReferences = []metav1.OwnerReference{ownerRef(s)}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: ContainerName, Ready: true, RestartCount: futureRestartCount,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	home := homeFor(s)
	home.OwnerReferences = []metav1.OwnerReference{ownerRef(s)}
	return s, pod, home
}

func failedFutureRescue(session string) func(*corev1.Pod) (protocol.RescueReport, error) {
	return func(*corev1.Pod) (protocol.RescueReport, error) {
		return protocol.RescueReport{
			Session: session, Stamp: "20261107-1201", OK: false,
			Agent: &protocol.AgentStop{Running: true},
		}, nil
	}
}

func verifiedFutureRescue(session, repo string) func(*corev1.Pod) (protocol.RescueReport, error) {
	return func(*corev1.Pod) (protocol.RescueReport, error) {
		const commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		const ref = "refs/heads/agent/future-only"
		return protocol.RescueReport{
			Session: session, Stamp: "20261107-1200", OK: true,
			Agent: &protocol.AgentStop{},
			Repos: []protocol.RepoRescue{{
				Path: "/home/dev/repos/" + repo, Fetched: true,
				UnpushedRefs: []protocol.Ref{{Name: ref, Commit: commit}},
				Bundle:       &protocol.RepoBundle{File: "rescue/future.bundle", Verified: true, Refs: []protocol.BundleRef{{Name: ref, Source: ref, Commit: commit}}},
			}},
			Bundle: &protocol.BundleReport{Dir: "rescue/future", Manifest: "rescue/future/manifest.json"},
		}, nil
	}
}

type reapObservation struct {
	ops      []string
	order    []string
	deletes  []futureDelete
	results  []ctrl.Result
	rescues  int
	reads    int
	stops    int
	reasons  []string
	blocked  string
	outdated string
	podUID   types.UID
	podRV    string
	image    string
	hold     bool
	restarts int32
	podGone  bool
	podNilTS bool
	sessDel  bool
	homeUID  types.UID
	rescue   string
	message  string
}

// reapSession is one image arm's client. Later stages change only the rescuer
// answer; the Pod, HOME and Session objects stay the ones already stored.
type reapSession struct {
	t          *testing.T
	arm        futureArm
	run        futureReconcile
	session    types.NamespacedName
	pod        types.NamespacedName
	beforePod  corev1.Pod
	beforeHome corev1.PersistentVolumeClaim
}

func startReap(t *testing.T, arm futureArm, revision, image, uid string, hold bool) *reapSession {
	t.Helper()
	s, pod, home := futureReapObjects(revision, image, uid, hold)
	if s.DeletionTimestamp.IsZero() || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning || pod.Spec.NodeName == "" || !controllerHasFinalizer(s) {
		t.Fatal("reap fixture must pair a deleting Session and rescue finalizer with a scheduled Running Pod whose deletionTimestamp is nil")
	}
	if agentImage(pod) != image || isHoldPod(pod) != hold || pod.ResourceVersion != futurePodRV {
		t.Fatalf("reap fixture image %q hold %v rv %q", agentImage(pod), isHoldPod(pod), pod.ResourceVersion)
	}
	run := newFutureReconcile(t, arm.doc, s.DeepCopy(), pod.DeepCopy(), home.DeepCopy())
	ctx := context.Background()
	var before corev1.Pod
	var beforeHome corev1.PersistentVolumeClaim
	if err := run.client.Get(ctx, client.ObjectKeyFromObject(pod), &before); err != nil {
		t.Fatal(err)
	}
	if err := run.client.Get(ctx, client.ObjectKeyFromObject(home), &beforeHome); err != nil {
		t.Fatal(err)
	}
	if before.UID != pod.UID || before.ResourceVersion == "" || !before.DeletionTimestamp.IsZero() || before.Status.ContainerStatuses[0].RestartCount != futureRestartCount || agentImage(&before) != image || isHoldPod(&before) != hold {
		t.Fatalf("stored pod uid %s rv %q deleting %v image %q hold %v restarts %d", before.UID, before.ResourceVersion, !before.DeletionTimestamp.IsZero(), agentImage(&before), isHoldPod(&before), before.Status.ContainerStatuses[0].RestartCount)
	}
	run.client.expectHome(&beforeHome)
	return &reapSession{t: t, arm: arm, run: run, session: client.ObjectKeyFromObject(s), pod: client.ObjectKeyFromObject(pod), beforePod: before, beforeHome: beforeHome}
}

func (p *reapSession) stage(passes int, rescue func(*corev1.Pod) (protocol.RescueReport, error)) reapObservation {
	p.t.Helper()
	p.run.useRescue(p.t, p.session.Name, rescue)
	ctx := context.Background()
	obs := reapObservation{podUID: p.beforePod.UID, podRV: p.beforePod.ResourceVersion, image: agentImage(&p.beforePod), hold: isHoldPod(&p.beforePod)}
	for range passes {
		res, err := p.run.r.Reconcile(ctx, ctrl.Request{NamespacedName: p.session})
		if err != nil {
			p.t.Fatal(err)
		}
		obs.results = append(obs.results, res)
	}
	obs.ops = p.run.client.operations()
	obs.order = p.run.client.orderLog()
	obs.deletes = p.run.client.forwardedDeletes()
	obs.rescues = p.run.rescuer.callsFor(p.session.Name)
	obs.reads = p.run.restarter.readCount(p.session.Name)
	obs.stops = p.run.restarter.stopCount(p.session.Name)
	obs.reasons = eventReasons(p.run.events)
	var gotSession v1alpha1.AgentSession
	if err := p.run.client.Get(ctx, p.session, &gotSession); err != nil {
		p.t.Fatal(err)
	}
	obs.sessDel = !gotSession.DeletionTimestamp.IsZero()
	if gotSession.Status.Rescue != nil {
		obs.rescue = string(gotSession.Status.Rescue.Result)
	}
	if c := meta.FindStatusCondition(gotSession.Status.Conditions, ConditionRemovalBlocked); c != nil {
		obs.blocked = c.Reason
		obs.message = c.Message
	}
	if c := meta.FindStatusCondition(gotSession.Status.Conditions, ConditionOutdated); c != nil {
		obs.outdated = c.Reason
	}
	var gotPod corev1.Pod
	err := p.run.client.Get(ctx, p.pod, &gotPod)
	switch {
	case apierrors.IsNotFound(err):
		obs.podGone = true
	case err != nil:
		p.t.Fatal(err)
	default:
		obs.podUID = gotPod.UID
		obs.podRV = gotPod.ResourceVersion
		obs.podNilTS = gotPod.DeletionTimestamp.IsZero()
		obs.image = agentImage(&gotPod)
		obs.hold = isHoldPod(&gotPod)
		if len(gotPod.Status.ContainerStatuses) == 1 {
			obs.restarts = gotPod.Status.ContainerStatuses[0].RestartCount
		}
		if !apiequality.Semantic.DeepEqual(&p.beforePod, &gotPod) {
			p.t.Fatalf("%s pod changed while it was still retained", p.arm.name)
		}
	}
	var gotHome corev1.PersistentVolumeClaim
	if err := p.run.client.Get(ctx, client.ObjectKeyFromObject(&p.beforeHome), &gotHome); err != nil {
		p.t.Fatal(err)
	}
	if !apiequality.Semantic.DeepEqual(&p.beforeHome, &gotHome) {
		p.t.Fatalf("%s HOME changed", p.arm.name)
	}
	obs.homeUID = gotHome.UID
	return obs
}

func controllerHasFinalizer(s *v1alpha1.AgentSession) bool {
	for _, f := range s.Finalizers {
		if f == Finalizer {
			return true
		}
	}
	return false
}

func rescueBeforeDelete(order []string, uid string) bool {
	rescueAt, deleteAt := -1, -1
	rescue := "record rescue of " + uid
	for i, entry := range order {
		if rescueAt < 0 && entry == rescue {
			rescueAt = i
		}
		if deleteAt < 0 && strings.HasPrefix(entry, "delete Pod ") {
			deleteAt = i
		}
	}
	return rescueAt >= 0 && deleteAt >= 0 && rescueAt < deleteAt
}

func TestFutureOnlyLifecycleComparisonRunningReap(t *testing.T) {
	old, held := futureArms(t)
	revision, image := old.tmpl.Revision(), old.tmpl.Image
	unproved := func(*corev1.Pod) (protocol.RescueReport, error) {
		return protocol.RescueReport{}, errors.New("unable to upgrade connection: container not found (\"agent\")")
	}
	assertBlocked := func(t *testing.T, a, b reapObservation, uid string, hold bool) {
		t.Helper()
		if a.podGone || b.podGone || a.podUID != types.UID(uid) || a.podUID != b.podUID || a.podRV == "" || a.podRV != b.podRV || !a.podNilTS || !b.podNilTS || !a.sessDel || !b.sessDel || a.hold != hold || b.hold != hold || a.rescue != "" || b.rescue != "" {
			t.Fatalf("blocked pod retention old=%+v held=%+v", a, b)
		}
		if a.image != image || b.image != image || a.restarts != futureRestartCount || b.restarts != futureRestartCount || a.homeUID == "" || a.homeUID != b.homeUID {
			t.Fatalf("blocked image, restarts, or home old=%+v held=%+v", a, b)
		}
		if a.rescues != 2 || b.rescues != 2 || a.reads != 0 || b.reads != 0 || a.stops != 0 || b.stops != 0 || len(a.ops) != 0 || len(a.deletes) != 0 || len(a.reasons) != 0 {
			t.Fatalf("blocked calls old rescues=%d reads=%d stops=%d held rescues=%d reads=%d stops=%d ops %v reasons %v", a.rescues, a.reads, a.stops, b.rescues, b.reads, b.stops, a.ops, a.reasons)
		}
		if a.blocked != "DeleteNeedsRescue" || b.blocked != a.blocked || !reflect.DeepEqual(a.ops, b.ops) || !reflect.DeepEqual(a.deletes, b.deletes) || !reflect.DeepEqual(a.reasons, b.reasons) || !reflect.DeepEqual(a.results, b.results) || !reflect.DeepEqual(a.order, b.order) {
			t.Fatalf("blocked reasons %q/%q ops %v/%v results %v/%v", a.blocked, b.blocked, a.ops, b.ops, a.results, b.results)
		}
		for _, entry := range a.order {
			if strings.HasPrefix(entry, "delete ") || strings.HasPrefix(entry, "record rescue") {
				t.Fatalf("blocked reap recorded a rescue or delete: %v", a.order)
			}
		}
		for _, res := range a.results {
			if res.RequeueAfter != time.Minute {
				t.Fatalf("blocked requeue %+v, want %s", res, time.Minute)
			}
		}
	}
	assertPermitted := func(t *testing.T, a, b reapObservation, uid string, hold bool, result, reason string) {
		t.Helper()
		if !a.podGone || !b.podGone || !a.sessDel || !b.sessDel || a.podUID != types.UID(uid) || a.podRV == "" || a.podRV != b.podRV || a.rescue != result || b.rescue != result {
			t.Fatalf("permitted delete old gone %v held gone %v rescue %s/%s", a.podGone, b.podGone, a.rescue, b.rescue)
		}
		if a.rescues != 3 || b.rescues != 3 || a.reads != 0 || b.reads != 0 || a.stops != 0 || b.stops != 0 {
			t.Fatalf("permitted calls old rescues=%d reads=%d stops=%d held rescues=%d reads=%d stops=%d", a.rescues, a.reads, a.stops, b.rescues, b.reads, b.stops)
		}
		if len(a.deletes) != 1 || !reflect.DeepEqual(a.deletes, b.deletes) || !reflect.DeepEqual(a.reasons, []string{reason}) || !reflect.DeepEqual(a.reasons, b.reasons) || !reflect.DeepEqual(a.results, b.results) {
			t.Fatalf("deletes %+v/%+v reasons %v/%v results %v/%v", a.deletes, b.deletes, a.reasons, b.reasons, a.results, b.results)
		}
		d := a.deletes[0]
		if d.verb != "delete" || d.kind != "Pod" || d.uid != uid || d.preUID != uid || d.preUID != d.storedUID || d.preRV == "" || d.preRV != a.podRV || d.preRV != d.storedRV || d.image != image || d.hold != hold || !d.homeOK || d.homeUID == "" || d.homeUID != string(a.homeUID) {
			t.Fatalf("delete preconditions %+v stored rv %s home %s", d, a.podRV, a.homeUID)
		}
		if !d.rescueSeen || d.rescueResult != result || d.rescuePodUID != uid || d.rescueGen != 3 {
			t.Fatalf("persisted rescue missing before delete: %+v", d)
		}
		if !rescueBeforeDelete(a.order, uid) || !rescueBeforeDelete(b.order, uid) || !reflect.DeepEqual(a.order, b.order) {
			t.Fatalf("rescue record was not before delete: old %v held %v", a.order, b.order)
		}
		if a.homeUID == "" || a.homeUID != b.homeUID {
			t.Fatalf("home changed old %s held %s", a.homeUID, b.homeUID)
		}
		for _, res := range a.results {
			if res.RequeueAfter != 0 || res.Requeue {
				t.Fatalf("permitted requeue %+v", res)
			}
		}
	}
	for _, tc := range []struct {
		name, uid, result, reason string
		hold, permit              bool
		rescue                    func(*corev1.Pod) (protocol.RescueReport, error)
	}{
		{name: "executor error then verified", uid: "protected-running-pod", hold: false, permit: true, result: string(v1alpha1.RescueVerified), reason: "Verified", rescue: verifiedFutureRescue("future-reap", "haynes-ops")},
		{name: "executor error then persisted failed", uid: "protected-running-pod", hold: false, permit: true, result: string(v1alpha1.RescueFailed), reason: "AgentStillRunning", rescue: failedFutureRescue("future-reap")},
		{name: "hold error then verified", uid: "protected-running-hold", hold: true, permit: true, result: string(v1alpha1.RescueVerified), reason: "Verified", rescue: verifiedFutureRescue("future-reap", "haynes-ops")},
		{name: "hold error then persisted failed", uid: "protected-running-hold", hold: true, permit: false, result: string(v1alpha1.RescueFailed), reason: "AgentStillRunning", rescue: failedFutureRescue("future-reap")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			progress := func(arm futureArm) (blocked, next reapObservation) {
				t.Helper()
				session := startReap(t, arm, revision, image, tc.uid, tc.hold)
				blocked = session.stage(2, unproved)
				next = session.stage(1, tc.rescue)
				return blocked, next
			}
			oldBlocked, oldNext := progress(old)
			heldBlocked, heldNext := progress(held)
			assertBlocked(t, oldBlocked, heldBlocked, tc.uid, tc.hold)
			if !tc.hold && (oldBlocked.outdated != "CurrentRevision" || heldBlocked.outdated != "OlderRevisionHeld") {
				t.Fatalf("executor Outdated old %s held %s", oldBlocked.outdated, heldBlocked.outdated)
			}
			if tc.hold && (oldBlocked.outdated != "" || heldBlocked.outdated != "") {
				t.Fatalf("hold pod took an executor Outdated reason old %s held %s", oldBlocked.outdated, heldBlocked.outdated)
			}
			if tc.permit {
				assertPermitted(t, oldNext, heldNext, tc.uid, tc.hold, tc.result, tc.reason)
				return
			}
			if oldNext.podGone || heldNext.podGone || oldNext.rescue != tc.result || heldNext.rescue != tc.result || oldNext.podUID != types.UID(tc.uid) || !oldNext.podNilTS || !heldNext.podNilTS || len(oldNext.deletes) != 0 || len(heldNext.deletes) != 0 {
				t.Fatalf("hold Failed retained neither pod: old=%+v held=%+v", oldNext, heldNext)
			}
			if oldNext.rescues != 3 || heldNext.rescues != 3 || !reflect.DeepEqual(oldNext.reasons, []string{tc.reason}) || !reflect.DeepEqual(oldNext.reasons, heldNext.reasons) || oldNext.homeUID == "" || oldNext.homeUID != heldNext.homeUID || oldNext.blocked != "DeleteNeedsRescue" || heldNext.blocked != oldNext.blocked || !strings.Contains(oldNext.message, "hold pod stays") || !strings.Contains(heldNext.message, "hold pod stays") {
				t.Fatalf("hold Failed characterization old=%+v held=%+v", oldNext, heldNext)
			}
			if oldNext.reads != 0 || oldNext.stops != 0 || heldNext.reads != 0 || heldNext.stops != 0 {
				t.Fatalf("hold Failed restart calls old reads=%d stops=%d held reads=%d stops=%d", oldNext.reads, oldNext.stops, heldNext.reads, heldNext.stops)
			}
			for _, next := range []reapObservation{oldNext, heldNext} {
				for _, res := range next.results {
					if res.Requeue || res.RequeueAfter < time.Minute || res.RequeueAfter > HoldRescueRetry {
						t.Fatalf("hold Failed requeue %+v, want between one minute and %s without immediate requeue", res, HoldRescueRetry)
					}
				}
			}
		})
	}
}

func futureHistoryPrototype(image string) (*v1alpha1.AgentSession, *corev1.PersistentVolumeClaim, *corev1.Pod) {
	s, claim := privateHistoryFixture()
	deleting := metav1.NewTime(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	suspended := metav1.NewTime(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	s.DeletionTimestamp = &deleting
	s.Status.SuspendedAt = &suspended
	s.Status.Rescue = nil
	s.Status.ArchivedAt = nil
	h := s.Status.PrivateHistory
	h.ExecutorAttempted, h.ExecutorIntent = true, "original-intent"
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "original-executor", ResourceVersion: futurePodRV,
		OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Labels: map[string]string{v1alpha1.LabelRevision: "original-template"},
		Annotations: map[string]string{privateHistoryExecutorAnnotation: h.ExecutorIntent, privateHistoryHomeAnnotation: h.HomeUID}},
		Spec: corev1.PodSpec{NodeName: "worker-a", RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: ContainerName, Image: image}},
			Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim.Name}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Error", ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName, RestartCount: 1,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}}}}}}
	return s, claim, pod
}

type historyWitness struct {
	ops        []string
	rescues    int
	reads      int
	stops      int
	spec       v1alpha1.AgentSessionSpec
	finalizers []string
	history    *v1alpha1.PrivateHistoryStatus
	archived   *metav1.Time
	conditions []metav1.Condition
	pod        corev1.Pod
	home       corev1.PersistentVolumeClaim
}

func guardConditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Status != b[i].Status || a[i].Reason != b[i].Reason || a[i].ObservedGeneration != b[i].ObservedGeneration {
			return false
		}
		if a[i].Type == ConditionOutdated {
			continue
		}
		if a[i].Message != b[i].Message {
			return false
		}
	}
	return true
}

func TestFutureOnlyLifecycleComparisonPrivateHistory(t *testing.T) {
	old, held := futureArms(t)
	image := old.tmpl.Image
	protoS, protoClaim, protoPod := futureHistoryPrototype(image)
	if protoS.DeletionTimestamp.IsZero() || !protoPod.DeletionTimestamp.IsZero() || protoPod.Status.Phase != corev1.PodFailed || agentImage(protoPod) != image || !controllerHasFinalizer(protoS) || protoS.Status.PrivateHistory == nil || protoS.Status.ArchivedAt != nil {
		t.Fatal("private-history prototype must be one deleting Session, a Failed Pod with nil deletionTimestamp, the old image, and no archive claim")
	}
	observe := func(t *testing.T, arm futureArm) historyWitness {
		t.Helper()
		s, claim, pod := protoS.DeepCopy(), protoClaim.DeepCopy(), protoPod.DeepCopy()
		run := newFutureReconcile(t, arm.doc, s, claim, pod)
		ctx := context.Background()
		var beforeSession v1alpha1.AgentSession
		var beforePod corev1.Pod
		var beforeHome corev1.PersistentVolumeClaim
		if err := run.client.Get(ctx, client.ObjectKeyFromObject(s), &beforeSession); err != nil {
			t.Fatal(err)
		}
		if err := run.client.Get(ctx, client.ObjectKeyFromObject(pod), &beforePod); err != nil {
			t.Fatal(err)
		}
		if err := run.client.Get(ctx, client.ObjectKeyFromObject(claim), &beforeHome); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := run.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
				t.Fatal(err)
			}
		}
		var gotPod corev1.Pod
		var gotHome corev1.PersistentVolumeClaim
		var gotSession v1alpha1.AgentSession
		if err := run.client.Get(ctx, client.ObjectKeyFromObject(pod), &gotPod); err != nil {
			t.Fatal(err)
		}
		if err := run.client.Get(ctx, client.ObjectKeyFromObject(claim), &gotHome); err != nil {
			t.Fatal(err)
		}
		if err := run.client.Get(ctx, client.ObjectKeyFromObject(s), &gotSession); err != nil {
			t.Fatal(err)
		}
		if !apiequality.Semantic.DeepEqual(&beforeSession.Spec, &gotSession.Spec) || !reflect.DeepEqual(beforeSession.Finalizers, gotSession.Finalizers) || !controllerHasFinalizer(&gotSession) || !apiequality.Semantic.DeepEqual(beforeSession.Status.PrivateHistory, gotSession.Status.PrivateHistory) || !apiequality.Semantic.DeepEqual(beforeSession.Status.ArchivedAt, gotSession.Status.ArchivedAt) {
			t.Fatalf("%s changed spec, rescue finalizer, private-history latch, or archive claim", arm.name)
		}
		if gotSession.Status.PrivateHistory.LastExecutor == nil || gotSession.Status.PrivateHistory.LastExecutor.PodUID != "original-executor" || gotSession.Status.ArchivedAt != nil || gotSession.Status.PrivateHistory.Expiry != nil {
			t.Fatalf("%s original executor or archive claim changed: %+v", arm.name, gotSession.Status.PrivateHistory)
		}
		if !apiequality.Semantic.DeepEqual(&beforePod, &gotPod) || !apiequality.Semantic.DeepEqual(&beforeHome.Spec, &gotHome.Spec) || !reflect.DeepEqual(beforeHome.Finalizers, gotHome.Finalizers) || !reflect.DeepEqual(beforeHome.Annotations, gotHome.Annotations) || beforeHome.UID != gotHome.UID {
			t.Fatalf("%s changed the Failed executor or its HOME spec, finalizers, or identity", arm.name)
		}
		return historyWitness{ops: run.client.operations(), rescues: run.rescuer.callsFor(pod.Name), reads: run.restarter.readCount(pod.Name), stops: run.restarter.stopCount(pod.Name), spec: gotSession.Spec, finalizers: gotSession.Finalizers, history: gotSession.Status.PrivateHistory, archived: gotSession.Status.ArchivedAt, conditions: gotSession.Status.Conditions, pod: gotPod, home: gotHome}
	}
	a, b := observe(t, old), observe(t, held)
	if a.rescues != 0 || b.rescues != 0 || a.reads != 0 || b.reads != 0 || a.stops != 0 || b.stops != 0 || len(a.ops) != 0 || !reflect.DeepEqual(a.ops, b.ops) {
		t.Fatalf("legacy cleanup old ops=%v rescues=%d reads=%d stops=%d held ops=%v rescues=%d reads=%d stops=%d", a.ops, a.rescues, a.reads, a.stops, b.ops, b.rescues, b.reads, b.stops)
	}
	if !apiequality.Semantic.DeepEqual(&a.spec, &b.spec) || !reflect.DeepEqual(a.finalizers, b.finalizers) || !apiequality.Semantic.DeepEqual(a.history, b.history) || !apiequality.Semantic.DeepEqual(a.archived, b.archived) || !apiequality.Semantic.DeepEqual(&a.pod, &b.pod) || !apiequality.Semantic.DeepEqual(&a.home.Spec, &b.home.Spec) || !reflect.DeepEqual(a.home.Finalizers, b.home.Finalizers) || a.home.UID != b.home.UID || !guardConditionsEqual(a.conditions, b.conditions) {
		t.Fatalf("retention witnesses diverged")
	}
	oldBlocked := meta.FindStatusCondition(a.conditions, ConditionRemovalBlocked)
	heldBlocked := meta.FindStatusCondition(b.conditions, ConditionRemovalBlocked)
	oldOut := meta.FindStatusCondition(a.conditions, ConditionOutdated)
	heldOut := meta.FindStatusCondition(b.conditions, ConditionOutdated)
	if oldBlocked == nil || heldBlocked == nil || oldBlocked.Reason != "RetentionPending" || heldBlocked.Reason != oldBlocked.Reason || oldOut == nil || heldOut == nil || oldOut.Reason != "OlderRevisionHeld" || heldOut.Reason != oldOut.Reason {
		t.Fatalf("RemovalBlocked %#v/%#v Outdated %#v/%#v", oldBlocked, heldBlocked, oldOut, heldOut)
	}
	if a.pod.UID != "original-executor" || !a.pod.DeletionTimestamp.IsZero() || !b.pod.DeletionTimestamp.IsZero() || agentImage(&a.pod) != image || agentImage(&b.pod) != image {
		t.Fatalf("pod identity diverged %s/%s", a.pod.UID, b.pod.UID)
	}
}

func TestFutureOnlyLifecycleComparisonIdleDrain(t *testing.T) {
	old, held := futureArms(t)
	open := futureOpen(t, rolloutDigest2)
	if open.tmpl.Image != held.tmpl.Image || open.tmpl.NewSessionsOnly() || open.tmpl.Revision() == old.tmpl.Revision() {
		t.Fatalf("open image %s held image %s revisions %s %s", open.tmpl.Image, held.tmpl.Image, open.tmpl.Revision(), old.tmpl.Revision())
	}
	activity := metav1.NewTime(time.Now().UTC())
	s := taskSession()
	s.Name, s.Namespace, s.UID = "future-idle", sessionNS, "idle-session"
	s.Generation = 4
	s.Finalizers = []string{Finalizer}
	s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentIdle, LastActivity: &activity}
	pod := readyDrainPod(s.Name, old.tmpl.Revision())
	pinRetainedPod(pod, old.tmpl.Image, "idle-old-pod", false)
	pod.OwnerReferences = []metav1.OwnerReference{ownerRef(s)}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: ContainerName, Ready: true, RestartCount: 2,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	home := homeFor(s)
	home.OwnerReferences = []metav1.OwnerReference{ownerRef(s)}
	if !s.DeletionTimestamp.IsZero() || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning || isHoldPod(pod) {
		t.Fatal("idle drain fixture must be a live Session and a Running Pod whose deletionTimestamp is nil")
	}
	type drainObs struct {
		gone     bool
		uid      types.UID
		image    string
		reads    int
		stops    int
		ops      []string
		deletes  []futureDelete
		reasons  []string
		results  []ctrl.Result
		mode     v1alpha1.OperatingMode
		homeUID  types.UID
		outdated string
	}
	run := func(t *testing.T, arm futureArm) drainObs {
		t.Helper()
		ss, pp, hh := s.DeepCopy(), pod.DeepCopy(), home.DeepCopy()
		rec := newFutureReconcile(t, arm.doc, ss, pp, hh)
		ctx := context.Background()
		var before corev1.Pod
		var beforeHome corev1.PersistentVolumeClaim
		if err := rec.client.Get(ctx, client.ObjectKeyFromObject(pp), &before); err != nil {
			t.Fatal(err)
		}
		if err := rec.client.Get(ctx, client.ObjectKeyFromObject(hh), &beforeHome); err != nil {
			t.Fatal(err)
		}
		if agentImage(&before) != old.tmpl.Image || before.UID != "idle-old-pod" || before.ResourceVersion == "" {
			t.Fatalf("idle pod image %q uid %s rv %q", agentImage(&before), before.UID, before.ResourceVersion)
		}
		rec.client.expectHome(&beforeHome)
		var results []ctrl.Result
		for range 2 {
			res, err := rec.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ss)})
			if err != nil {
				t.Fatal(err)
			}
			results = append(results, res)
		}
		var gotSession v1alpha1.AgentSession
		if err := rec.client.Get(ctx, client.ObjectKeyFromObject(ss), &gotSession); err != nil {
			t.Fatal(err)
		}
		var gotHome corev1.PersistentVolumeClaim
		if err := rec.client.Get(ctx, client.ObjectKeyFromObject(hh), &gotHome); err != nil {
			t.Fatal(err)
		}
		if !apiequality.Semantic.DeepEqual(&beforeHome.Spec, &gotHome.Spec) || !reflect.DeepEqual(beforeHome.Finalizers, gotHome.Finalizers) || beforeHome.UID != gotHome.UID {
			t.Fatalf("%s HOME changed during drain", arm.name)
		}
		obs := drainObs{uid: before.UID, image: agentImage(&before), reads: rec.restarter.readCount(ss.Name), stops: rec.restarter.stopCount(ss.Name), ops: rec.client.operations(), deletes: rec.client.forwardedDeletes(), reasons: eventReasons(rec.events), results: results, mode: gotSession.Spec.OperatingMode, homeUID: gotHome.UID}
		if c := meta.FindStatusCondition(gotSession.Status.Conditions, ConditionOutdated); c != nil {
			obs.outdated = c.Reason
		}
		var gotPod corev1.Pod
		err := rec.client.Get(ctx, client.ObjectKeyFromObject(pp), &gotPod)
		switch {
		case apierrors.IsNotFound(err):
			obs.gone = true
		case err != nil:
			t.Fatal(err)
		default:
			if !apiequality.Semantic.DeepEqual(&before, &gotPod) || agentImage(&gotPod) != old.tmpl.Image {
				t.Fatalf("%s retained idle pod changed", arm.name)
			}
			obs.uid = gotPod.UID
			obs.image = agentImage(&gotPod)
		}
		return obs
	}
	kept, drained := run(t, held), run(t, open)
	if kept.gone || kept.uid != "idle-old-pod" || kept.image != old.tmpl.Image || kept.reads != 0 || kept.stops != 0 || len(kept.deletes) != 0 || kept.mode != v1alpha1.OperatingModeRunning || kept.outdated != "OlderRevisionHeld" || kept.homeUID == "" {
		t.Fatalf("matching hold did not retain the idle pod: %+v", kept)
	}
	for _, reason := range kept.reasons {
		if reason == "IdleDrain" || reason == "IdleSuspend" {
			t.Fatalf("held idle arm event %v", kept.reasons)
		}
	}
	for _, res := range kept.results {
		if res.RequeueAfter < 30*time.Minute {
			t.Fatalf("held idle requeue %+v", res)
		}
	}
	if !drained.gone || drained.reads < 1 || drained.stops < 1 || drained.mode != v1alpha1.OperatingModeRunning || drained.homeUID != kept.homeUID || len(drained.deletes) != 1 {
		t.Fatalf("same new image without a hold did not drain: %+v", drained)
	}
	d := drained.deletes[0]
	if d.verb != "delete" || d.kind != "Pod" || d.uid != "idle-old-pod" || d.preUID != d.uid || d.preUID != d.storedUID || d.preRV == "" || d.preRV != d.storedRV || d.image != old.tmpl.Image || !d.homeOK || d.homeUID != string(drained.homeUID) || !reflect.DeepEqual(drained.reasons, []string{"IdleDrain"}) {
		t.Fatalf("drain delete %+v reasons %v home %s", d, drained.reasons, drained.homeUID)
	}
	for _, op := range drained.ops {
		if strings.Contains(op, "PersistentVolumeClaim") && strings.Contains(op, "delete") {
			t.Fatalf("drain deleted HOME: %v", drained.ops)
		}
	}
}

// TestFutureOnlyLifecycleComparisonFreshPod keeps the new-session create
// control. A busy retained pod does not prove the rollout hold; IdleDrain does.
func TestFutureOnlyLifecycleComparisonFreshPod(t *testing.T) {
	old, held := futureArms(t)
	image := old.tmpl.Image
	type created struct {
		ops      []string
		image    string
		keptImg  string
		uid      types.UID
		kept     corev1.Pod
		reasons  []string
		keptRes  ctrl.Result
		newRes   ctrl.Result
		outdated string
	}
	run := func(t *testing.T, arm futureArm, keptProto, freshProto *v1alpha1.AgentSession, podProto *corev1.Pod, homeProto *corev1.PersistentVolumeClaim) created {
		t.Helper()
		kept, fresh := keptProto.DeepCopy(), freshProto.DeepCopy()
		pod, home := podProto.DeepCopy(), homeProto.DeepCopy()
		rec := newFutureReconcile(t, arm.doc, kept, fresh, pod, home)
		ctx := context.Background()
		var before corev1.Pod
		if err := rec.client.Get(ctx, client.ObjectKeyFromObject(pod), &before); err != nil {
			t.Fatal(err)
		}
		if agentImage(&before) != image || before.UID != pod.UID || before.ResourceVersion == "" {
			t.Fatalf("retained pod image %q uid %s rv %q", agentImage(&before), before.UID, before.ResourceVersion)
		}
		keptRes, err := rec.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(kept)})
		if err != nil {
			t.Fatal(err)
		}
		newRes, err := rec.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fresh)})
		if err != nil {
			t.Fatal(err)
		}
		var gotKept corev1.Pod
		if err := rec.client.Get(ctx, client.ObjectKeyFromObject(pod), &gotKept); err != nil {
			t.Fatal(err)
		}
		if !apiequality.Semantic.DeepEqual(&before, &gotKept) || !gotKept.DeletionTimestamp.IsZero() || gotKept.UID != before.UID || agentImage(&gotKept) != image {
			t.Fatalf("%s retained pod changed", arm.name)
		}
		if rec.restarter.readCount(kept.Name) != 0 || rec.restarter.stopCount(kept.Name) != 0 || rec.rescuer.callsFor(kept.Name) != 0 || rec.rescuer.callsFor(fresh.Name) != 0 {
			t.Fatalf("%s restarter or rescuer ran on create", arm.name)
		}
		var gotNew corev1.Pod
		if err := rec.client.Get(ctx, client.ObjectKeyFromObject(fresh), &gotNew); err != nil {
			t.Fatalf("%s fresh pod: %v", arm.name, err)
		}
		var gotSession v1alpha1.AgentSession
		if err := rec.client.Get(ctx, client.ObjectKeyFromObject(kept), &gotSession); err != nil {
			t.Fatal(err)
		}
		outdated := ""
		if c := meta.FindStatusCondition(gotSession.Status.Conditions, ConditionOutdated); c != nil {
			outdated = c.Reason
		}
		return created{ops: rec.client.operations(), image: agentImage(&gotNew), keptImg: agentImage(&gotKept), uid: gotNew.UID, kept: gotKept, reasons: eventReasons(rec.events), keptRes: keptRes, newRes: newRes, outdated: outdated}
	}
	kept := taskSession()
	kept.Name, kept.Namespace, kept.UID = "future-kept", sessionNS, "kept-session"
	kept.Generation = 4
	kept.Finalizers = []string{Finalizer}
	kept.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentBusy}
	pod := readyDrainPod(kept.Name, old.tmpl.Revision())
	pinRetainedPod(pod, image, "kept-pod", false)
	pod.OwnerReferences = []metav1.OwnerReference{ownerRef(kept)}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: ContainerName, Ready: true, RestartCount: 2,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	home := homeFor(kept)
	home.OwnerReferences = []metav1.OwnerReference{ownerRef(kept)}
	fresh := taskSession()
	fresh.Name, fresh.Namespace, fresh.UID = "future-fresh", sessionNS, "fresh-session"
	fresh.Generation = 2
	fresh.Finalizers = []string{Finalizer}
	a := run(t, old, kept, fresh, pod, home)
	b := run(t, held, kept, fresh, pod, home)
	if a.image != old.tmpl.Image || b.image != held.tmpl.Image || a.image == b.image || a.uid == "" || b.uid == "" || a.keptImg != image || b.keptImg != image {
		t.Fatalf("created images old %q held %q retained %q/%q uids %s %s", a.image, b.image, a.keptImg, b.keptImg, a.uid, b.uid)
	}
	if !reflect.DeepEqual(a.ops, b.ops) || len(a.ops) != 2 || !strings.HasPrefix(a.ops[0], "create PersistentVolumeClaim ") || !strings.HasPrefix(a.ops[1], "create Pod future-fresh ") {
		t.Fatalf("creates old %v held %v", a.ops, b.ops)
	}
	for _, op := range a.ops {
		if strings.Contains(op, "delete") || strings.Contains(op, "update") || strings.Contains(op, "patch") {
			t.Fatalf("retained pod was written: %v", a.ops)
		}
	}
	if a.kept.UID != b.kept.UID || a.kept.UID != "kept-pod" || !reflect.DeepEqual(a.keptRes, b.keptRes) || !reflect.DeepEqual(a.newRes, b.newRes) || !reflect.DeepEqual(a.reasons, b.reasons) {
		t.Fatalf("retained results old %+v/%+v held %+v/%+v events %v/%v", a.keptRes, a.newRes, b.keptRes, b.newRes, a.reasons, b.reasons)
	}
	if a.outdated != "CurrentRevision" || b.outdated != "OlderRevisionHeld" {
		t.Fatalf("Outdated old %s held %s", a.outdated, b.outdated)
	}
}
