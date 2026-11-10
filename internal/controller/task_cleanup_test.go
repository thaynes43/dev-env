package controller

// DESIGN-061's task clean-up against the fake client: the policy on the HOME
// expiry pipeline for spec.mode task, with agentd's strict rescue and the
// shelf's rescue-verify played by fakes. The cases follow OpenProject #520's
// acceptance: the flags off write no record; the due time by cause, which a
// resume, a message or a restart does not move; the whole path for a purged
// and a finished Session; the queue; and (task_cleanup_refusal_test.go) each
// refusal, each protected group, the release check of 1.1, a tampered or
// missing bundle at the second verify, and the nil safety of the readers of
// Expiry.Archive.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// RescueStrictHome plays agentd's rescue with --strict-home: the test's report,
// or a clean and pushed one that passed the strict check.
func (f *expiryRescuer) RescueStrictHome(ctx context.Context, pod *corev1.Pod) (protocol.RescueReport, error) {
	rep, err := f.Rescue(ctx, pod)
	f.mu.Lock()
	strictSet := f.report != nil
	f.mu.Unlock()
	if err == nil && rep.StrictHome == nil && !strictSet {
		rep.StrictHome = &protocol.StrictHomeCheck{}
	}
	return rep, err
}

// DropLog plays `agentd ctl drop-log`: it counts the pods it ran in.
func (f *expiryRescuer) DropLog(_ context.Context, pod *corev1.Pod) (protocol.DropLogResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = append(f.dropped, string(pod.UID))
	return protocol.DropLogResult{Session: pod.Name, Path: "logs/" + pod.Name + ".log", Removed: true, Bytes: 12}, nil
}

// fakeVerifier plays shelf.RescueVerifier: free bytes for an empty id, and for
// a rescue id the answer the test set, or bundle_missing.
type fakeVerifier struct {
	mu      sync.Mutex
	free    int64
	err     error
	answers map[string]protocol.RescueVerifyResult
	calls   []string
}

func (v *fakeVerifier) Verify(_ context.Context, id string) (protocol.RescueVerifyResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, id)
	if v.err != nil {
		return protocol.RescueVerifyResult{}, v.err
	}
	res := protocol.RescueVerifyResult{Version: 1, ID: id, OK: true, SharedFreeBytes: v.free, SharedTotalBytes: 20 << 30}
	if id == "" {
		return res, nil
	}
	a, ok := v.answers[id]
	if !ok {
		return protocol.RescueVerifyResult{Version: 1, ID: id, Refusal: protocol.RescueVerifyBundleMissing, Detail: "rescue/" + id + " is absent",
			SharedFreeBytes: v.free, SharedTotalBytes: 20 << 30}, nil
	}
	a.Version, a.ID, a.SharedFreeBytes, a.SharedTotalBytes = 1, id, v.free, 20<<30
	return a, nil
}

func (v *fakeVerifier) set(id string, res protocol.RescueVerifyResult) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.answers[id] = res
}

func (v *fakeVerifier) idCalls() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for _, c := range v.calls {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// taskFixture is an expiryFixture for the task clean-up: the HOME claim bound
// to a CSI PersistentVolume that the terminal record's node no longer lists in
// use, the clean-up's flags on, a fake shelf verifier, and one operator whose
// memory persists between reconciles (r); fresh() is a new one, as after a
// restart.
type taskFixture struct {
	*expiryFixture
	pv           *corev1.PersistentVolume
	verifier     *fakeVerifier
	taskCounters *TaskCleanupCounters
	r            *Reconciler
	// writes records every status write's expiry phase, in order.
	writes []string
}

const testVolumeHandle = "0001-0009-rbd-home-1"

func newTaskFixture(t *testing.T, edit func(*terminalFixture)) *taskFixture {
	t.Helper()
	f := &taskFixture{verifier: &fakeVerifier{free: 10 << 30, answers: map[string]protocol.RescueVerifyResult{}}, taskCounters: NewTaskCleanupCounters()}
	f.expiryFixture = newExpiryFixture(t, func(tf *terminalFixture) {
		tf.home.Spec.VolumeName = "pvc-home-1"
		if edit != nil {
			edit(tf)
		}
	})
	f.pv = &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pvc-home-1"},
		Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{Namespace: f.home.Namespace, Name: f.home.Name, UID: f.home.UID},
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "rbd.csi.ceph.com", VolumeHandle: testVolumeHandle}}}}
	if err := f.c.Create(context.Background(), f.pv); err != nil {
		t.Fatal(err)
	}
	prev := f.statusErr
	f.statusErr = func(obj client.Object) error {
		if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Status.PrivateHistory != nil && s.Status.PrivateHistory.Expiry != nil {
			f.writes = append(f.writes, s.Status.PrivateHistory.Expiry.Phase)
		}
		if prev != nil {
			return prev(obj)
		}
		return nil
	}
	f.r = f.fresh()
	return f
}

// fresh is a new operator with the clean-up on, as the rollout's H4 runs it,
// the history archive off.
func (f *taskFixture) fresh() *Reconciler {
	r := f.reconciler()
	r.HomeHistoryArchive = false
	r.PrivateExecutorStop = true
	r.TaskSessionCleanup = true
	r.TaskCleanupMaxActive = DefaultTaskCleanupMaxActive
	r.TaskCleanupMinSharedFree = DefaultTaskCleanupMinSharedFree
	r.RescueVerifier = f.verifier
	r.TaskCleanupCounters = f.taskCounters
	return r
}

// run reconciles the Session times with the fixture's one operator.
func (f *taskFixture) run(t *testing.T, times int) {
	t.Helper()
	for range times {
		if _, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
			t.Fatal(err)
		}
	}
}

// tick moves the clock and keeps the node's lease fresh, as a live kubelet
// does.
func (f *taskFixture) tick(t *testing.T, at time.Time) {
	t.Helper()
	f.at(at)
	var l = f.lease.DeepCopy()
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.lease), l); err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		t.Fatal(err)
	}
	l.Spec.RenewTime = &metav1.MicroTime{Time: at.Add(-time.Second)}
	if err := f.c.Update(context.Background(), l); err != nil {
		t.Fatal(err)
	}
}

// nodeInUse sets the terminal record's node's volumesInUse.
func (f *taskFixture) nodeInUse(t *testing.T, names ...corev1.UniqueVolumeName) {
	t.Helper()
	var n corev1.Node
	if err := f.c.Get(context.Background(), client.ObjectKey{Name: f.node.Name}, &n); err != nil {
		t.Fatal(err)
	}
	n.Status.VolumesInUse = names
	if err := f.c.Status().Update(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
}

func homeVolumeUniqueName() corev1.UniqueVolumeName {
	return corev1.UniqueVolumeName("kubernetes.io/csi/rbd.csi.ceph.com^" + testVolumeHandle)
}

// sessionGone reports whether the Session is NotFound.
func (f *taskFixture) sessionGone(t *testing.T) bool {
	t.Helper()
	var s v1alpha1.AgentSession
	err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &s)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return apierrors.IsNotFound(err)
}

// claimGone reports whether the HOME claim is NotFound.
func (f *taskFixture) claimGone(t *testing.T) bool {
	t.Helper()
	var c corev1.PersistentVolumeClaim
	err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.home), &c)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return apierrors.IsNotFound(err)
}

// kept fails unless the Session and its HOME claim are both still there,
// intact.
func (f *taskFixture) kept(t *testing.T) {
	t.Helper()
	if f.sessionGone(t) {
		t.Fatal("the Session is gone")
	}
	f.claimIntact(t)
	for _, d := range f.deletes {
		if strings.HasPrefix(d, "*v1.PersistentVolumeClaim") || strings.HasPrefix(d, "*v1alpha1.AgentSession") {
			t.Fatalf("deleted %s", d)
		}
	}
}

// toHold takes the fixture's due record to Rescuing with its adopted hold pod
// Running. The rescue has not run.
func (f *taskFixture) toHold(t *testing.T) *corev1.Pod {
	t.Helper()
	f.rescuer.mu.Lock()
	saved := f.rescuer.report
	f.rescuer.report = func(*corev1.Pod) (protocol.RescueReport, error) {
		return protocol.RescueReport{}, errors.New("unable to upgrade connection")
	}
	f.rescuer.mu.Unlock()
	f.run(t, 8)
	hold := f.named(t)
	if hold == nil || !isHoldPod(hold) {
		t.Fatalf("no hold pod: %+v; record %+v", hold, f.expiry(t))
	}
	f.runHold(t)
	f.run(t, 2)
	f.rescuer.mu.Lock()
	f.rescuer.report = saved
	f.rescuer.mu.Unlock()
	if e := f.expiry(t); e == nil || e.Phase != v1alpha1.HomeExpiryRescuing || e.HoldPodUID != string(hold.UID) {
		t.Fatalf("not at Rescuing with the hold pod: %+v", e)
	}
	return f.named(t)
}

// verifiedRescue is a report that bundled one unpushed branch into
// rescue/<session>/<dir>.
func verifiedRescue(session, stamp, dir string) protocol.RescueReport {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	manifest := "rescue/" + session + "/" + dir + "/manifest.json"
	return protocol.RescueReport{Session: session, Stamp: stamp, OK: true, Agent: &protocol.AgentStop{}, StrictHome: &protocol.StrictHomeCheck{},
		Repos: []protocol.RepoRescue{{Path: "/home/dev/repos/haynes-ops", Fetched: true,
			UnpushedRefs: []protocol.Ref{{Name: "refs/heads/agent/" + session, Commit: commit}},
			Bundle:       &protocol.RepoBundle{File: "haynes-ops.bundle", Verified: true, Refs: []protocol.BundleRef{{Source: "refs/heads/agent/" + session, Commit: commit}}}}},
		Bundle: &protocol.BundleReport{Dir: "rescue/" + session + "/" + dir, Manifest: manifest}}
}

const testDigest = "5f3c1a9e0b7d2c4f6a8e1b3d5f7092c4e6a8b0d2f4a6c8e0b2d4f6a8c0e2b4d6"

func (f *taskFixture) report(rep func(*corev1.Pod) (protocol.RescueReport, error)) {
	f.rescuer.mu.Lock()
	defer f.rescuer.mu.Unlock()
	f.rescuer.report = rep
}

// TestTaskCleanupPurgedCleanAndPushedEndsReleased is the purged path to the
// end (ADR-020 point 1): the record is due at once from the terminal record,
// the ended executor goes, the old node shows the HOME's volume released, a
// hold pod rescues with --strict-home, a clean and pushed verdict goes straight
// to Archived with its bundle, drop-log runs in the hold pod before it goes,
// the HOME is deleted with UID and resourceVersion, and the reaped Session is
// released. The events come in section 6's order, and the counters count once.
func TestTaskCleanupPurgedCleanAndPushedEndsReleased(t *testing.T) {
	f := newTaskFixture(t, nil)
	f.run(t, 2)
	e := f.expiry(t)
	if e == nil || e.Phase == v1alpha1.HomeExpiryWaiting && !e.DueAt.Equal(&e.GraceStart) || !e.GraceStart.Equal(&f.observed) {
		t.Fatalf("record %+v; want one due at its graceStart, the terminal record's observedAt", e)
	}
	hold := f.toHold(t)
	f.run(t, 12)
	if !f.sessionGone(t) {
		s := f.session(t)
		t.Fatalf("the purged Session stays: record %+v, conditions %+v", s.Status.PrivateHistory.Expiry, s.Status.Conditions)
	}
	if !f.claimGone(t) {
		t.Fatal("the HOME claim stays")
	}
	var claimDeletes []string
	for _, d := range f.deletes {
		if strings.HasPrefix(d, "*v1.PersistentVolumeClaim") {
			claimDeletes = append(claimDeletes, d)
		}
	}
	if len(claimDeletes) != 1 || !strings.Contains(claimDeletes[0], "uid="+string(f.home.UID)+" rv=") {
		t.Fatalf("claim deletes %v; want one with the UID and resourceVersion preconditions", claimDeletes)
	}
	if d := f.rescuer.dropped; len(d) != 1 || d[0] != string(hold.UID) {
		t.Fatalf("drop-log ran in %v; want once, in the hold pod", d)
	}
	if calls := f.verifier.idCalls(); len(calls) != 0 {
		t.Fatalf("a clean and pushed rescue was verified on the shelf: %v", calls)
	}
	if got := strings.Join(slices.Compact(slices.Clone(f.writes)), ","); got != "Waiting,Rescuing,Archived,Deleting,Expired" {
		t.Fatalf("the record moved %s; want Waiting, Rescuing, Archived, Deleting, Expired", got)
	}
	if v := terminalCount(f.taskCounters.Deleted, "purged"); v != 1 {
		t.Fatalf("purged deletes counted %v, want 1", v)
	}
	if v := terminalCount(f.taskCounters.Deleted, "finished"); v != 0 {
		t.Fatalf("finished deletes counted %v, want 0", v)
	}
	if len(f.x.called()) != 0 {
		t.Fatalf("history execs ran: %v", f.x.called())
	}
}

// TestTaskCleanupVerifiedBundleIsVerifiedTwiceAndKept: a Verified rescue's
// bundle is re-hashed on the shelf before Archived and again before the
// delete, its id taken from the manifest (a -2 directory), and the record keeps
// its digest, bundles and bytes.
func TestTaskCleanupVerifiedBundleIsVerifiedTwiceAndKept(t *testing.T) {
	f := newTaskFixture(t, nil)
	id := f.s.Name + "/20261111-1200-2"
	f.verifier.set(id, protocol.RescueVerifyResult{OK: true, Manifest: "rescue/" + id + "/manifest.json", ManifestSHA256: testDigest, Bundles: 1, Bytes: 41984})
	f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
		return verifiedRescue(p.Name, "20261111-1200", "20261111-1200-2"), nil
	})
	f.run(t, 2)
	hold := f.toHold(t)
	f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
		return verifiedRescue(p.Name, "20261111-1200", "20261111-1200-2"), nil
	})
	f.run(t, 3)
	e := f.expiry(t)
	want := v1alpha1.PrivateHomeBundleStatus{Stamp: "20261111-1200", Result: "Verified", Manifest: "rescue/" + id + "/manifest.json",
		ManifestSHA256: testDigest, Bundles: 1, Bytes: 41984}
	if e == nil || e.Phase != v1alpha1.HomeExpiryArchived || e.Bundle == nil {
		t.Fatalf("record %+v; want Archived with its bundle", e)
	}
	got := *e.Bundle
	got.VerifiedAt = metav1.Time{}
	if got != want || e.RescueStamp != want.Stamp || e.Archive != nil {
		t.Fatalf("bundle %+v; want %+v", got, want)
	}
	if r := f.session(t).Status.Rescue; r == nil || r.PodUID != string(hold.UID) || r.Result != v1alpha1.RescueVerified || r.LastBundle != want.Manifest {
		t.Fatalf("status.rescue %+v; want the Verified rescue in the hold pod", r)
	}
	f.run(t, 10)
	if !f.sessionGone(t) || !f.claimGone(t) {
		t.Fatal("the Session or its HOME stays after a verified bundle")
	}
	if calls := f.verifier.idCalls(); len(calls) < 2 || calls[0] != id || calls[len(calls)-1] != id {
		t.Fatalf("verifies %v; want the rescue %s before Archived and again before the delete", calls, id)
	}
}

// TestTaskCleanupFinishedSessionIsDeletedThenReleased: a finished task (its
// compute reclaimed, Suspended) waits the grace from its terminal record, then
// its HOME goes, then the Session itself is deleted with UID and
// resourceVersion preconditions and background propagation, and released.
// status.archivedAt goes with Deleting, so a resume after that is refused.
func TestTaskCleanupFinishedSessionIsDeletedThenReleased(t *testing.T) {
	f := newTaskFixture(t, func(tf *terminalFixture) {
		tf.s.DeletionTimestamp = nil
		tf.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		tf.pod = nil
	})
	var sessionDeletes []string
	f.c = interceptor.NewClient(f.c, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok {
			var o client.DeleteOptions
			o.ApplyOptions(opts)
			rec := "session"
			if p := o.Preconditions; p != nil && p.UID != nil && p.ResourceVersion != nil && *p.UID == f.s.UID && *p.ResourceVersion != "" {
				rec += " uid rv"
			}
			if o.PropagationPolicy != nil && *o.PropagationPolicy == metav1.DeletePropagationBackground {
				rec += " background"
			}
			sessionDeletes = append(sessionDeletes, rec)
		}
		return c.Delete(ctx, obj, opts...)
	}})
	f.r = f.fresh()
	f.run(t, 2)
	e := f.expiry(t)
	due := f.observed.Add(DefaultTaskCleanupGrace)
	if e == nil || e.Phase != v1alpha1.HomeExpiryWaiting || !e.DueAt.Time.Equal(due) {
		t.Fatalf("record %+v; want Waiting, due 168h after the terminal record", e)
	}
	if c := f.condition(t, ConditionPodReady); c == nil || !strings.Contains(c.Message, "the task clean-up deletes its HOME and this session from "+due.UTC().Format(time.RFC3339)+", 7 days after that") {
		t.Fatalf("PodReady %+v; want the due time", c)
	}
	f.tick(t, due.Add(-time.Minute))
	f.run(t, 4)
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryWaiting || f.named(t) != nil {
		t.Fatalf("moved before it was due: %+v", e)
	}
	f.tick(t, due.Add(time.Minute))
	f.toHold(t)
	f.run(t, 14)
	if !f.sessionGone(t) || !f.claimGone(t) {
		s := f.session(t)
		t.Fatalf("the finished Session or its HOME stays: %+v", s.Status.PrivateHistory.Expiry)
	}
	if len(sessionDeletes) != 1 || sessionDeletes[0] != "session uid rv background" {
		t.Fatalf("Session deletes %v; want one with UID, resourceVersion and background propagation", sessionDeletes)
	}
	if v := terminalCount(f.taskCounters.Deleted, "finished"); v != 1 {
		t.Fatalf("finished deletes counted %v, want 1", v)
	}
	if v := terminalCount(f.taskCounters.Deleted, "purged"); v != 0 {
		t.Fatalf("purged deletes counted %v, want 0", v)
	}
}

// TestTaskCleanupDueTimeByCause: purged at once; finished (Suspended) and
// Failed (its Pod ended, operatingMode Running) 168 hours after the terminal
// record's observedAt. A resume, a message, an operator restart and the
// stored dueAt do not move it; a reap during the wait makes it due at once.
func TestTaskCleanupDueTimeByCause(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*terminalFixture)
		purged bool
	}{
		{name: "purged", purged: true},
		{name: "finished", edit: func(tf *terminalFixture) {
			tf.s.DeletionTimestamp = nil
			tf.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			tf.pod = nil
		}},
		{name: "failed", edit: func(tf *terminalFixture) {
			tf.s.DeletionTimestamp = nil
			tf.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
			tf.pod.Status.Reason = "Evicted"
			tf.s.Status.PrivateHistory.ExecutorTerminal.Reason = "Evicted"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTaskFixture(t, tc.edit)
			f.run(t, 2)
			e := f.expiry(t)
			if e == nil || !e.GraceStart.Equal(&f.observed) {
				t.Fatalf("record %+v; want graceStart at the terminal record", e)
			}
			if tc.purged {
				if e.Phase == v1alpha1.HomeExpiryWaiting {
					f.run(t, 1)
				}
				if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryRescuing {
					t.Fatalf("a purged record waits: %+v", e)
				}
				return
			}
			due := f.observed.Add(168 * time.Hour)
			if !e.DueAt.Time.Equal(due) {
				t.Fatalf("dueAt %s, want %s", e.DueAt, due)
			}
			// A resume, a message record, and operator restarts in the wait.
			f.tick(t, f.observed.Add(24*time.Hour))
			f.update(t, func(s *v1alpha1.AgentSession) {
				s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
				if s.Annotations == nil {
					s.Annotations = map[string]string{}
				}
				s.Annotations[v1alpha1.AnnotationResumedAt] = f.clock.Format(time.RFC3339)
			})
			f.update(t, func(s *v1alpha1.AgentSession) { s.Annotations["test.invalid/inbox-message"] = "a message was recorded" })
			for range 3 {
				f.r = f.fresh()
				f.run(t, 2)
			}
			if after := f.expiry(t); after == nil || after.Phase != v1alpha1.HomeExpiryWaiting || !after.DueAt.Equal(&e.DueAt) || !after.GraceStart.Equal(&e.GraceStart) {
				t.Fatalf("after a resume, a message and restarts the record is %+v; want it unchanged", after)
			}
			if f.named(t) != nil && isHoldPod(f.named(t)) {
				t.Fatal("a hold pod before the due time")
			}
			f.tick(t, due.Add(-time.Minute))
			f.run(t, 3)
			if after := f.expiry(t); after.Phase != v1alpha1.HomeExpiryWaiting {
				t.Fatalf("moved a minute early: %+v", after)
			}
			// A reap during the wait: due at once from graceStart.
			if err := f.c.Delete(context.Background(), f.session(t)); err != nil {
				t.Fatal(err)
			}
			f.run(t, 2)
			if after := f.expiry(t); after.Phase != v1alpha1.HomeExpiryRescuing || !after.DueAt.Equal(&e.DueAt) {
				t.Fatalf("a reap in the wait: record %+v; want Rescuing with the stored dueAt unchanged", after)
			}
		})
	}
}

// TestTaskCleanupFlagsOffWriteNoRecord fails if any status write carries an
// expiry record while a clean-up flag, or a flag it needs, is off, for every
// kind of Session the clean-up would take and those it never takes. The
// existing operator flags stay on, as live today.
func TestTaskCleanupFlagsOffWriteNoRecord(t *testing.T) {
	sessions := map[string]func(*terminalFixture){
		"purged task": nil,
		"finished task": func(tf *terminalFixture) {
			tf.s.DeletionTimestamp, tf.pod = nil, nil
			tf.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		},
		"failed task": func(tf *terminalFixture) { tf.s.DeletionTimestamp = nil },
		"remote session": func(tf *terminalFixture) {
			tf.s.Spec.Mode = v1alpha1.ModeRemote
			tf.s.DeletionTimestamp = nil
		},
	}
	flags := map[string]func(*Reconciler){
		"cleanup off":         func(r *Reconciler) { r.TaskSessionCleanup = false },
		"terminal record off": func(r *Reconciler) { r.PrivateTerminalRecord = false },
		"executor stop off":   func(r *Reconciler) { r.PrivateExecutorStop = false },
		"all off": func(r *Reconciler) {
			r.TaskSessionCleanup, r.PrivateTerminalRecord, r.PrivateExecutorStop = false, false, false
		},
	}
	for sname, edit := range sessions {
		for fname, off := range flags {
			t.Run(sname+"/"+fname, func(t *testing.T) {
				f := newTaskFixture(t, edit)
				var wrote []string
				f.statusErr = func(obj client.Object) error {
					if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Status.PrivateHistory != nil && s.Status.PrivateHistory.Expiry != nil {
						wrote = append(wrote, s.Status.PrivateHistory.Expiry.Phase)
					}
					return nil
				}
				f.r = f.fresh()
				f.r.TerminalTaskPodReclaim = true
				off(f.r)
				for _, at := range []time.Duration{0, time.Hour, 8 * 24 * time.Hour, 60 * 24 * time.Hour} {
					f.tick(t, f.now.Add(at))
					f.run(t, 4)
				}
				if len(wrote) != 0 || f.expiry(t) != nil {
					t.Fatalf("an expiry record was written with %s: %v", fname, wrote)
				}
				if p := f.named(t); p != nil && isHoldPod(p) {
					t.Fatal("a hold pod was made")
				}
				if c := f.condition(t, ConditionHomeExpiry); c != nil {
					t.Fatalf("a HomeExpiry condition: %+v", c)
				}
				f.kept(t)
				if len(f.verifier.calls) != 0 || len(f.rescuer.ranIn()) != 0 {
					t.Fatalf("the shelf or a rescue ran: %v %v", f.verifier.calls, f.rescuer.ranIn())
				}
			})
		}
	}
}

// TestTaskCleanupQueueCapAndOrder: with --task-cleanup-max-active 2 and one
// slot held, the purged Session due goes first, then the older finished one,
// so the fixture's finished Session is Queued; an admission is reserved until
// the cache shows it, so a third is refused beside two; a second operator
// with no memory admits the same head of the queue and no other; once the
// slots free the fixture's Session starts. 0 pauses every new start.
func TestTaskCleanupQueueCapAndOrder(t *testing.T) {
	f := newTaskFixture(t, func(tf *terminalFixture) {
		tf.s.DeletionTimestamp = nil
		tf.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		tf.pod = nil
	})
	mk := func(name string, reaped bool, observed time.Time, phase string) {
		s := f.session(t).DeepCopy()
		s.ObjectMeta = metav1.ObjectMeta{Name: name, Namespace: s.Namespace, UID: k8stypes.UID("uid-" + name), Finalizers: []string{Finalizer}}
		obs := metav1.NewTime(observed)
		st := s.Status.DeepCopy()
		h := st.PrivateHistory
		h.SessionUID, h.HomeClaim = string(s.UID), HomeClaimName(name)
		h.ExecutorTerminal.ObservedAt = obs
		h.Expiry = &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: h.LastExecutor.PodUID, HomeUID: h.HomeUID, GraceStart: obs, DueAt: obs, Phase: phase}
		if err := f.c.Create(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
			t.Fatal(err)
		}
		s.Status = *st
		if err := f.c.Status().Update(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		if reaped {
			if err := f.c.Delete(context.Background(), s); err != nil {
				t.Fatal(err)
			}
		}
	}
	due := f.observed.Add(DefaultTaskCleanupGrace)
	f.run(t, 2)
	f.tick(t, due.Add(time.Hour))
	mk("purged-a", true, f.observed.Time, v1alpha1.HomeExpiryRescuing)
	mk("purged-b", true, f.observed.Time, v1alpha1.HomeExpiryWaiting)
	mk("finished-older", false, f.observed.Add(-time.Hour), v1alpha1.HomeExpiryWaiting)

	f.run(t, 3)
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryWaiting {
		t.Fatalf("the third in line started: %+v", e)
	}
	if c := f.condition(t, ConditionHomeExpiry); c == nil || c.Reason != taskReasonQueued {
		t.Fatalf("HomeExpiry %+v; want Queued", c)
	}
	if c := f.condition(t, ConditionPodReady); c == nil || !strings.Contains(c.Message, "the clean-up is queued: at most 2 run at once") {
		t.Fatalf("PodReady %+v; want the queued text", c)
	}
	ctx := context.Background()
	if !f.r.taskCleanupAdmittable(ctx, f.sessionNamed(t, "purged-b"), *f.clock, true) {
		t.Fatal("purged-b, first in line, is not admitted")
	}
	if f.r.taskCleanupAdmittable(ctx, f.sessionNamed(t, "finished-older"), *f.clock, true) {
		t.Fatal("a third clean-up is admitted beside two, one of them only reserved")
	}
	other := f.fresh()
	if other.taskCleanupAdmittable(ctx, f.sessionNamed(t, "finished-older"), *f.clock, false) {
		t.Fatal("a second operator admits the second in line ahead of the first")
	}
	if !other.taskCleanupAdmittable(ctx, f.sessionNamed(t, "purged-b"), *f.clock, false) {
		t.Fatal("a second operator does not admit the head of the queue")
	}
	// The two purged Sessions are released: both slots free, and the
	// fixture's Session is second in line behind the older finished one.
	for _, name := range []string{"purged-a", "purged-b"} {
		s := f.sessionNamed(t, name)
		s.Finalizers = nil
		if err := f.c.Update(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	f.r = f.fresh()
	f.run(t, 2)
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryRescuing {
		t.Fatalf("with two free slots the second in line did not start: %+v", e)
	}
	// Max active 0: nothing new starts, and the gauge says why.
	g := newTaskFixture(t, nil)
	g.r.TaskCleanupMaxActive = 0
	g.run(t, 4)
	if e := g.expiry(t); e == nil || e.Phase != v1alpha1.HomeExpiryWaiting {
		t.Fatalf("with max-active 0 the record is %+v; want Waiting", e)
	}
	if c := g.condition(t, ConditionHomeExpiry); c == nil || c.Reason != taskReasonPaused || !strings.Contains(c.Message, "--task-cleanup-max-active is 0") {
		t.Fatalf("HomeExpiry %+v; want Paused", c)
	}
	if !g.r.TaskCleanupPaused(*g.clock)[taskPauseMaxActiveZero] {
		t.Fatal("the paused gauge does not say max_active_zero")
	}
	g.kept(t)
}

func (f *taskFixture) sessionNamed(t *testing.T, name string) *v1alpha1.AgentSession {
	t.Helper()
	var s v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: f.s.Namespace, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

// TestTaskCleanupPausesItself: under 2Gi free, a shelf that does not answer,
// and three failed rescues in a row each pause new starts with their reason;
// a valid rescue resets the breaker.
func TestTaskCleanupPausesItself(t *testing.T) {
	t.Run("shared volume low", func(t *testing.T) {
		f := newTaskFixture(t, nil)
		f.verifier.free = 1 << 30
		f.run(t, 4)
		if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryWaiting {
			t.Fatalf("started with 1 GiB free: %+v", e)
		}
		if c := f.condition(t, ConditionHomeExpiry); c == nil || c.Reason != taskReasonPaused || !strings.Contains(c.Message, "less than 2 GiB free") {
			t.Fatalf("HomeExpiry %+v", c)
		}
		if !f.r.TaskCleanupPaused(*f.clock)[taskPauseSharedVolumeLow] {
			t.Fatal("the gauge does not say shared_volume_low")
		}
		f.kept(t)
	})
	t.Run("shelf unavailable", func(t *testing.T) {
		f := newTaskFixture(t, nil)
		f.verifier.err = shelf.ErrNoShelf
		f.run(t, 4)
		if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryWaiting {
			t.Fatalf("started without a shelf: %+v", e)
		}
		if !f.r.TaskCleanupPaused(*f.clock)[taskPauseShelfUnavailable] {
			t.Fatal("the gauge does not say shelf_unavailable")
		}
		f.kept(t)
	})
	t.Run("rescue failures", func(t *testing.T) {
		f := newTaskFixture(t, nil)
		for range taskBreakerTrips {
			f.r.taskBreaker(true, *f.clock)
		}
		f.run(t, 4)
		if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryWaiting {
			t.Fatalf("started with the breaker open: %+v", e)
		}
		if !f.r.TaskCleanupPaused(*f.clock)[taskPauseRescueFailures] {
			t.Fatal("the gauge does not say rescue_failures")
		}
		f.tick(t, f.clock.Add(taskBreakerPause+time.Minute))
		f.run(t, 2)
		if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryRescuing {
			t.Fatalf("still paused an hour later: %+v", e)
		}
		f.r.taskBreaker(false, *f.clock)
		if f.r.TaskCleanupPaused(*f.clock)[taskPauseRescueFailures] {
			t.Fatal("a valid rescue does not reset the breaker")
		}
	})
}

// TestTaskCleanupConditionsSayTheStep: PodReady, PrivateHistoryRetained and
// RemovalBlocked carry one text at each step, and HomeExpiry its own reason,
// never Archived.
func TestTaskCleanupConditionsSayTheStep(t *testing.T) {
	f := newTaskFixture(t, nil)
	check := func(step, homeReason, removal string, contains ...string) {
		t.Helper()
		s := f.session(t)
		ready := meta.FindStatusCondition(s.Status.Conditions, ConditionPodReady)
		kept := meta.FindStatusCondition(s.Status.Conditions, ConditionPrivateHistory)
		blocked := meta.FindStatusCondition(s.Status.Conditions, ConditionRemovalBlocked)
		home := meta.FindStatusCondition(s.Status.Conditions, ConditionHomeExpiry)
		if ready == nil || kept == nil || blocked == nil || home == nil {
			t.Fatalf("%s: a condition is missing: %+v", step, s.Status.Conditions)
		}
		if ready.Message != kept.Message || kept.Message != blocked.Message {
			t.Fatalf("%s: the texts differ:\n%s\n%s\n%s", step, ready.Message, kept.Message, blocked.Message)
		}
		if home.Reason != homeReason || blocked.Reason != removal {
			t.Fatalf("%s: HomeExpiry %s and RemovalBlocked %s; want %s and %s", step, home.Reason, blocked.Reason, homeReason, removal)
		}
		for _, want := range contains {
			if !strings.Contains(ready.Message, want) {
				t.Fatalf("%s: the text %q does not say %q", step, ready.Message, want)
			}
		}
	}
	f.r.TaskCleanupMaxActive = 0
	f.run(t, 3)
	check("paused", taskReasonPaused, reasonExecutorEnded, "reap accepted; the agent stopped: its Pod ended Failed (DeadlineExceeded)", "the task clean-up deletes its HOME now", "the clean-up is paused: --task-cleanup-max-active is 0")
	f.r.TaskCleanupMaxActive = 2
	f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
		return verifiedRescue(p.Name, "20261111-1200", "20261111-1200"), nil
	})
	id := f.s.Name + "/20261111-1200"
	f.verifier.set(id, protocol.RescueVerifyResult{OK: true, Manifest: "rescue/" + id + "/manifest.json", ManifestSHA256: testDigest, Bundles: 1, Bytes: 2048})
	// Waiting to Rescuing, the executor's delete, the intent, the release
	// check and the hold pod, its adoption, then a reconcile that writes.
	f.run(t, 6)
	check("rescuing", taskReasonSaving, reasonExecutorEnded, "the task clean-up has started; the hold pod "+f.s.Name+" is starting")
	f.runHold(t)
	f.run(t, 2)
	// The delete's verify is busy, so a reconcile at Archived writes.
	f.verifier.mu.Lock()
	f.verifier.err = shelf.ErrSlotsBusy
	f.verifier.mu.Unlock()
	f.run(t, 2)
	check("saved", taskReasonSaved, reasonExecutorEnded, "saved work that never reached GitHub as rescue "+id+" (1 refs, 2 KB), kept 30 days after this session is gone; the HOME is deleted next")
	if d := f.rescuer.dropped; len(d) != 1 {
		t.Fatalf("drop-log ran %d times before the hold pod went; want once", len(d))
	}
	// Deleting: the claim delete conflicts once, then the verify is busy.
	f.verifier.mu.Lock()
	f.verifier.err = nil
	f.verifier.mu.Unlock()
	f.deleteErr = func(obj client.Object) error {
		if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
			f.deleteErr = nil
			f.verifier.mu.Lock()
			f.verifier.err = shelf.ErrSlotsBusy
			f.verifier.mu.Unlock()
			return apierrors.NewConflict(corev1.Resource("persistentvolumeclaims"), obj.GetName(), errors.New("held by the test"))
		}
		return nil
	}
	f.run(t, 4)
	check("deleting", taskReasonDeleting, "HomeDeleting", "the HOME "+f.home.Name+" is being deleted and the session can no longer be resumed; rescue "+id+" is kept")
	f.claimIntact(t)
}
