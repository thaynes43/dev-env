package controller

// DESIGN-025 unit H6: the HOME expiry past Archived, behind
// --enable-home-expiry-delete. Every attempt that can remove the HOME runs the
// archive's verify and the Pod checks again in its own call path; the delete
// carries the claim's UID and resourceVersion; a reaped Session is released
// once its record is Expired and nothing of it is left.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// toArchived runs H5's flow: the grace, the hold pod, the rescue and the
// export, up to Archived with the HOME intact.
func (f *expiryFixture) toArchived(t *testing.T) *v1alpha1.PrivateHomeExpiryStatus {
	t.Helper()
	f.reconcile(t, 4)
	e := f.expiry(t)
	if e == nil {
		t.Fatal("no expiry record")
	}
	f.at(e.DueAt.Add(time.Minute))
	f.reconcile(t, 6)
	f.runHold(t)
	f.reconcile(t, 8)
	if e = f.expiry(t); e == nil || e.Phase != v1alpha1.HomeExpiryArchived || e.Archive == nil {
		t.Fatalf("the record is %+v; want Archived", e)
	}
	if f.named(t) != nil {
		t.Fatal("the hold pod stays at Archived")
	}
	f.claimIntact(t)
	return e
}

// deleting is the reconciler with the delete on, a fresh operator each time.
func (f *expiryFixture) deleting() *Reconciler {
	r := f.reconciler()
	r.HomeExpiryDelete = true
	return r
}

func (f *expiryFixture) reconcileDeleting(t *testing.T, times int) {
	t.Helper()
	for range times {
		if _, err := f.deleting().Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
			t.Fatal(err)
		}
	}
}

// claim is the HOME claim as stored, or nil once it is gone.
func (f *expiryFixture) claim(t *testing.T) *corev1.PersistentVolumeClaim {
	t.Helper()
	var c corev1.PersistentVolumeClaim
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.home), &c); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		t.Fatal(err)
	}
	return &c
}

// claimDeletes are the recorded deletes of the HOME claim.
func (f *expiryFixture) claimDeletes() []string {
	var out []string
	for _, d := range f.deletes {
		if strings.HasPrefix(d, "*v1.PersistentVolumeClaim ") {
			out = append(out, d)
		}
	}
	return out
}

// entryDir is the archive entry's directory on the test shelf.
func (f *expiryFixture) entryDir(e *v1alpha1.PrivateHomeExpiryStatus) string {
	return filepath.Join(f.x.archive.Root, filepath.FromSlash(e.Archive.Entry))
}

// T1 to the end, and T17's first half: with the delete off nothing moves past
// Archived. On, the next reconcile verifies the archive again and records
// Deleting; the one after verifies again, checks for Pods, deletes the claim
// with its UID and resourceVersion and lifts the finalizer; then Expired, and
// the reaped Session is released. The verify of each attempt comes before its
// delete.
func TestHomeExpiryDeletesOnlyAfterVerifyAndReleases(t *testing.T) {
	f := newExpiryFixture(t, nil)
	e := f.toArchived(t)
	verifies := strings.Count(strings.Join(f.x.called(), " "), "verify")

	// Delete off: Archived stays, the HOME stays, nothing is verified again.
	f.reconcile(t, 4)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryArchived || len(f.claimDeletes()) != 0 {
		t.Fatalf("delete off moved on: %+v", got)
	}
	f.claimIntact(t)

	// Delete on: verify, then Deleting, the HOME still intact.
	f.reconcileDeleting(t, 1)
	got := f.session(t)
	d := got.Status.PrivateHistory.Expiry
	if d.Phase != v1alpha1.HomeExpiryDeleting || d.DeletingAt == nil || d.Archive == nil || *d.Archive != *e.Archive || d.Refusal != "" {
		t.Fatalf("the record is %+v; want Deleting with its archive unchanged", d)
	}
	if got.Status.ArchivedAt != nil {
		t.Fatal("a reaped Session got archivedAt: only a suspended one needs it")
	}
	if n := strings.Count(strings.Join(f.x.called(), " "), "verify"); n != verifies+1 {
		t.Fatalf("verifies %d; want one more before Deleting", n-verifies)
	}
	f.claimIntact(t)

	// The delete: verify again first, then the claim with UID and
	// resourceVersion, then the finalizer.
	f.reconcileDeleting(t, 1)
	dels := f.claimDeletes()
	if len(dels) != 1 || !strings.Contains(dels[0], "uid="+string(f.home.UID)+" rv=") || !strings.Contains(dels[0], "after-verifies="+strconv.Itoa(verifies+2)) {
		t.Fatalf("claim deletes %v; want one, with UID and resourceVersion, after a second verify", dels)
	}
	if c := f.claim(t); c != nil {
		t.Fatalf("the claim stays: %+v", c.ObjectMeta)
	}
	f.reconcileDeleting(t, 1)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryExpired || got.HomeDeletedAt == nil {
		t.Fatalf("the record is %+v; want Expired", got)
	}
	if counterValue(f.counters.Expired) != 1 {
		t.Fatalf("expired counted %v, want 1", counterValue(f.counters.Expired))
	}
	f.reconcileDeleting(t, 1)
	var gone v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("the reaped Session was not released: %v %+v", err, gone.Finalizers)
	}
	// The archive still verifies: it is the copy that remains.
	if _, err := f.x.archive.Verify(string(f.s.UID), e.Archive.ContentSHA256); err != nil {
		t.Fatalf("the archive no longer verifies: %v", err)
	}
	if len(f.claimDeletes()) != 1 {
		t.Fatalf("claim deletes %v; want exactly one", f.claimDeletes())
	}
}

// T2 and T12's archive part: the entry removed, or one byte changed, after
// Archived refuses archive_missing or archive_mismatch, and nothing is
// deleted; the same in Deleting, also after a restart that fell between the
// claim delete and the finalizer lift, where the claim stays deleting, held by
// the finalizer. No shelf refuses shelf_unavailable.
func TestHomeExpiryDeleteRefusesAMissingOrChangedArchive(t *testing.T) {
	f := newExpiryFixture(t, nil)
	e := f.toArchived(t)
	dir := f.entryDir(e)
	moved := dir + ".moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	f.reconcileDeleting(t, 2)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryArchived || got.Refusal != expiryArchiveMissing || len(f.claimDeletes()) != 0 {
		t.Fatalf("a missing entry: %+v, deletes %v", got, f.claimDeletes())
	}
	if err := os.Rename(moved, dir); err != nil {
		t.Fatal(err)
	}
	// One byte of an archived file changed.
	file := filepath.Join(dir, protocol.HistoryFilesDir, "claude", "history.jsonl")
	orig, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	changed := append([]byte{orig[0] ^ 1}, orig[1:]...)
	if err := os.WriteFile(file, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	f.at(f.clock.Add(homeExpiryRefusalRetry + time.Second))
	f.reconcileDeleting(t, 2)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryArchived || got.Refusal != expiryArchiveMismatch || len(f.claimDeletes()) != 0 {
		t.Fatalf("a changed entry: %+v", got)
	}
	f.claimIntact(t)
	if c := f.condition(t, ConditionHomeExpiry); c == nil || c.Reason != "Refused" {
		t.Fatalf("HomeExpiry condition %+v; want Refused", c)
	}
	if err := os.WriteFile(file, orig, 0o600); err != nil {
		t.Fatal(err)
	}

	// Fixed: the re-check passes and Deleting is recorded. Then a restart
	// falls between the claim delete and the finalizer lift.
	f.at(f.clock.Add(homeExpiryRefusalRetry + time.Second))
	f.reconcileDeleting(t, 1)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryDeleting || got.Refusal != "" {
		t.Fatalf("after the fix: %+v; want Deleting", got)
	}
	f.patchErr = func(obj client.Object) error {
		if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
			return errors.New("the operator stopped here")
		}
		return nil
	}
	f.reconcileDeletingTolerant(t, 1)
	f.patchErr = nil
	c := f.claim(t)
	if c == nil || c.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(c, Finalizer) || len(f.claimDeletes()) != 1 {
		t.Fatalf("the claim is %+v; want deleting, held by the finalizer", c)
	}
	// The archive changed in between: the retry verifies first and refuses;
	// the claim stays deleting, still held.
	if err := os.WriteFile(file, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	f.reconcileDeleting(t, 2)
	got := f.expiry(t)
	if got.Phase != v1alpha1.HomeExpiryDeleting || got.Refusal != expiryArchiveMismatch {
		t.Fatalf("a changed archive in Deleting: %+v", got)
	}
	if c := f.claim(t); c == nil || !controllerutil.ContainsFinalizer(c, Finalizer) {
		t.Fatal("the finalizer was lifted after a failed verify")
	}
	if err := os.WriteFile(file, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	f.at(f.clock.Add(homeExpiryRefusalRetry + time.Second))
	f.reconcileDeleting(t, 2)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryExpired || got.HomeDeletedAt == nil || got.Refusal != "" {
		t.Fatalf("after the fix in Deleting: %+v; want Expired", got)
	}
	if f.claim(t) != nil || len(f.claimDeletes()) != 1 {
		t.Fatalf("the claim stays, or was deleted twice: %v", f.claimDeletes())
	}
}

// reconcileDeletingTolerant is reconcileDeleting that takes a failed write as
// the restart it stands for.
func (f *expiryFixture) reconcileDeletingTolerant(t *testing.T, times int) {
	t.Helper()
	for range times {
		_, _ = f.deleting().Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)})
	}
}

// T2's shelf part: with no shelf, verify cannot run: shelf_unavailable, and
// nothing is deleted.
func TestHomeExpiryDeleteNeedsTheShelf(t *testing.T) {
	f := newExpiryFixture(t, nil)
	f.toArchived(t)
	if err := f.c.Delete(context.Background(), shelfPodObject(f.s.Namespace)); err != nil {
		t.Fatal(err)
	}
	f.reconcileDeleting(t, 2)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryArchived || got.Refusal != expiryShelfUnavailable {
		t.Fatalf("no shelf: %+v", got)
	}
	f.claimIntact(t)
}

// T12's Pod part and T11's claim_in_use: from Deleting on nothing cancels, so
// a Pod that mounts the claim refuses claim_in_use and nothing is deleted;
// once it is gone the delete goes on. An owner hold in Deleting stops it too.
func TestHomeExpiryDeleteRefusesAClaimInUseAndHonoursTheHold(t *testing.T) {
	f := newExpiryFixture(t, nil)
	f.toArchived(t)
	f.reconcileDeleting(t, 1)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryDeleting {
		t.Fatalf("want Deleting: %+v", got)
	}
	debug := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "debug", Namespace: f.s.Namespace},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "x", Image: "x"}},
			Volumes: []corev1.Volume{{Name: "h", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: f.home.Name}}}}}}
	if err := f.c.Create(context.Background(), debug); err != nil {
		t.Fatal(err)
	}
	f.reconcileDeleting(t, 2)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryDeleting || got.Refusal != expiryClaimInUse || len(f.claimDeletes()) != 0 {
		t.Fatalf("a debug Pod in Deleting: %+v, deletes %v", got, f.claimDeletes())
	}
	f.claimIntact(t)
	if err := f.c.Delete(context.Background(), debug); err != nil {
		t.Fatal(err)
	}

	// The owner holds it: nothing moves, the HOME stays.
	f.update(t, func(s *v1alpha1.AgentSession) {
		s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
	})
	f.at(f.clock.Add(homeExpiryRefusalRetry + time.Second))
	f.reconcileDeleting(t, 3)
	if len(f.claimDeletes()) != 0 {
		t.Fatalf("a held Deleting record deleted the HOME: %v", f.claimDeletes())
	}
	f.claimIntact(t)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryDeleting {
		t.Fatalf("held: %+v", got)
	}
	f.update(t, func(s *v1alpha1.AgentSession) { delete(s.Annotations, v1alpha1.AnnotationHomeExpiry) })
	f.reconcileDeleting(t, 2)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryExpired || len(f.claimDeletes()) != 1 {
		t.Fatalf("after the hold: %+v, deletes %v", got, f.claimDeletes())
	}
}

// Delete off once Deleting is recorded: the record waits there, the HOME
// stays, and nothing is verified or deleted until the flag is on again.
func TestHomeExpiryDeletingWaitsWithTheDeleteOff(t *testing.T) {
	f := newExpiryFixture(t, nil)
	f.toArchived(t)
	f.reconcileDeleting(t, 1)
	calls := len(f.x.called())
	f.reconcile(t, 4)
	if got := f.expiry(t); got.Phase != v1alpha1.HomeExpiryDeleting || len(f.claimDeletes()) != 0 || len(f.x.called()) != calls {
		t.Fatalf("delete off at Deleting: %+v, deletes %v, execs %v", got, f.claimDeletes(), f.x.called()[calls:])
	}
	f.claimIntact(t)
}

// T5 to the end: the executor Pod lost after its terminal record (a node
// reboot) still expires, through Archived, Deleting and Expired, and the
// Session is released.
func TestHomeExpiryPodLostAfterTheRecordStillExpires(t *testing.T) {
	f := newExpiryFixture(t, func(tf *terminalFixture) { tf.pod = nil })
	f.toArchived(t)
	f.reconcileDeleting(t, 6)
	var gone v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("the Session was not released: %v; record %+v", err, gone.Status.PrivateHistory.Expiry)
	}
	if f.claim(t) != nil || len(f.claimDeletes()) != 1 {
		t.Fatalf("claim deletes %v", f.claimDeletes())
	}
}

// T7's delete part: a suspended Session past its archive deadline whose
// executor was evicted. Deleting and status.archivedAt are written in one
// write, so archivedAt never exists without Deleting. A longer archiveAfter at
// Deleting cancels nothing, nor do templates that do not load; the delete goes
// on. The Session is not released while it is not reaped, and is at once
// when it is.
func TestHomeExpirySuspendedSessionArchivedAtGoesWithDeleting(t *testing.T) {
	f := newExpiryFixture(t, func(tf *terminalFixture) {
		tf.s.DeletionTimestamp = nil
		tf.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		tf.pod.Status.Reason = "Evicted"
		tf.s.Status.PrivateHistory.ExecutorTerminal.Reason = "Evicted"
		suspended := metav1.NewTime(tf.now)
		tf.s.Status.SuspendedAt = &suspended
	})
	f.at(f.now.Add(168*time.Hour + time.Minute))
	f.toArchived(t)
	if f.session(t).Status.ArchivedAt != nil {
		t.Fatal("archivedAt before Deleting")
	}
	// Each write is watched: archivedAt only ever arrives with Deleting.
	f.statusErr = func(obj client.Object) error {
		s, ok := obj.(*v1alpha1.AgentSession)
		if ok && s.Status.ArchivedAt != nil && (s.Status.PrivateHistory == nil || s.Status.PrivateHistory.Expiry == nil ||
			!expiryPhaseAtLeast(s.Status.PrivateHistory.Expiry.Phase, v1alpha1.HomeExpiryDeleting)) {
			t.Errorf("a status write holds archivedAt without Deleting: %+v", s.Status.PrivateHistory.Expiry)
		}
		return nil
	}
	f.reconcileDeleting(t, 1)
	got := f.session(t)
	if got.Status.PrivateHistory.Expiry.Phase != v1alpha1.HomeExpiryDeleting || got.Status.ArchivedAt == nil {
		t.Fatalf("Deleting %s, archivedAt %v; want both, in one write", got.Status.PrivateHistory.Expiry.Phase, got.Status.ArchivedAt)
	}
	// archiveAfter lengthened, and the templates gone: no cancel.
	f.update(t, func(s *v1alpha1.AgentSession) {
		s.Spec.Lifecycle = &v1alpha1.Lifecycle{ArchiveAfter: &metav1.Duration{Duration: 10000 * time.Hour}}
	})
	if err := f.c.Delete(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}}); err != nil {
		t.Fatal(err)
	}
	f.reconcileDeleting(t, 4)
	got = f.session(t)
	if got == nil || got.Status.PrivateHistory.Expiry == nil || got.Status.PrivateHistory.Expiry.Phase != v1alpha1.HomeExpiryExpired || f.claim(t) != nil {
		t.Fatalf("the delete did not go on: %+v", got.Status.PrivateHistory.Expiry)
	}
	if got.Status.PrivateHistory.ExpiryClearedAt != nil {
		t.Fatal("a cancel was recorded from Deleting")
	}
	// Not reaped: it stays, Expired and Archived, its HOME's absence
	// expected. Reaped: released at once.
	f.reconcileDeleting(t, 2)
	if got := f.session(t); got.Status.Phase != v1alpha1.PhaseArchived {
		t.Fatalf("a suspended Session past its delete is %s; want Archived", got.Status.Phase)
	}
	if c := f.condition(t, ConditionPrivateHistory); c == nil || c.Reason != "HomeExpired" || c.Status != metav1.ConditionTrue {
		t.Fatalf("PrivateHistoryRetained %+v; want HomeExpired, not PreservationUnknown", c)
	}
	if err := f.c.Delete(context.Background(), f.session(t)); err != nil {
		t.Fatal(err)
	}
	f.reconcileDeleting(t, 2)
	var gone v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("the reaped Session was not released: %v", err)
	}
}

// T13's delete part: two overlapping operators. The second one's reconcile
// runs inside the first one's verify, so the first acts on reads the second
// made stale. One delete lands; the other finds the claim deleting or gone
// and deletes nothing again.
func TestHomeExpiryDeleteTwoOperatorsDeleteOnce(t *testing.T) {
	f := newExpiryFixture(t, nil)
	f.toArchived(t)
	f.reconcileDeleting(t, 1)
	f.x.mu.Lock()
	f.x.before = map[string]func(){"verify": func() {
		if _, err := f.deleting().Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
			t.Error(err)
		}
	}}
	f.x.mu.Unlock()
	f.reconcileDeleting(t, 1)
	f.reconcileDeleting(t, 4)
	if len(f.claimDeletes()) != 1 {
		t.Fatalf("claim deletes %v; want one", f.claimDeletes())
	}
	if f.claim(t) != nil {
		t.Fatal("the claim stays")
	}
}

// The guard of the HOME delete (DESIGN-025 6.5, 11): a private Session, no
// canary, its record Deleting with its archive, resting on its terminal
// record, the claim the bound one; a suspended Session needs archivedAt. And
// the release exception: only an Expired record of a reaped private Session.
func TestHomeExpiryVolumeAndReleaseGuardTable(t *testing.T) {
	base := func() (*v1alpha1.AgentSession, *corev1.PersistentVolumeClaim) {
		f := newTerminalFixture()
		s, c := f.s, f.home
		s.UID, c.UID = "s-uid", "home-uid"
		c.ResourceVersion = "7"
		c.OwnerReferences = []metav1.OwnerReference{ownerRef(s)}
		s.Finalizers = []string{Finalizer}
		h := s.Status.PrivateHistory
		h.SessionUID, h.HomeUID = string(s.UID), string(c.UID)
		at := metav1.NewTime(f.now)
		h.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: h.LastExecutor.PodUID, Phase: "Failed", ObservedAt: at}
		h.Expiry = &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: h.LastExecutor.PodUID, HomeUID: h.HomeUID, Phase: v1alpha1.HomeExpiryDeleting,
			DeletingAt: &at, RescueStamp: "20261111-1200", Archive: &v1alpha1.PrivateHomeArchiveStatus{Entry: "v1/_none/s-uid/0123456789abcdef", ContentSHA256: strings.Repeat("a", 64)}}
		s.DeletionTimestamp = &at
		return s, c
	}
	s, c := base()
	if err := expiryVolumeRemovalAllowed(s, c); err != nil {
		t.Fatalf("the base case is refused: %v", err)
	}
	for name, edit := range map[string]func(*v1alpha1.AgentSession, *corev1.PersistentVolumeClaim){
		"canary": func(s *v1alpha1.AgentSession, _ *corev1.PersistentVolumeClaim) {
			s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{}
		},
		"shared": func(s *v1alpha1.AgentSession, _ *corev1.PersistentVolumeClaim) {
			s.Spec.Workspace = &v1alpha1.WorkspaceSpec{}
		},
		"no record": func(s *v1alpha1.AgentSession, _ *corev1.PersistentVolumeClaim) { s.Status.PrivateHistory.Expiry = nil },
		"archived": func(s *v1alpha1.AgentSession, _ *corev1.PersistentVolumeClaim) {
			s.Status.PrivateHistory.Expiry.Phase = v1alpha1.HomeExpiryArchived
		},
		"expired": func(s *v1alpha1.AgentSession, _ *corev1.PersistentVolumeClaim) {
			s.Status.PrivateHistory.Expiry.Phase = v1alpha1.HomeExpiryExpired
		},
		"no archive": func(s *v1alpha1.AgentSession, _ *corev1.PersistentVolumeClaim) {
			s.Status.PrivateHistory.Expiry.Archive = nil
		},
		"no terminal": func(s *v1alpha1.AgentSession, _ *corev1.PersistentVolumeClaim) {
			s.Status.PrivateHistory.ExecutorTerminal = nil
		},
		"other home":    func(_ *v1alpha1.AgentSession, c *corev1.PersistentVolumeClaim) { c.UID = "other" },
		"other name":    func(_ *v1alpha1.AgentSession, c *corev1.PersistentVolumeClaim) { c.Name = "home-other" },
		"foreign owner": func(_ *v1alpha1.AgentSession, c *corev1.PersistentVolumeClaim) { c.OwnerReferences = nil },
		"no rv":         func(_ *v1alpha1.AgentSession, c *corev1.PersistentVolumeClaim) { c.ResourceVersion = "" },
		"unbound identity": func(s *v1alpha1.AgentSession, _ *corev1.PersistentVolumeClaim) {
			s.Status.PrivateHistory.SessionUID = "x"
		},
		"suspended, no archivedAt": func(s *v1alpha1.AgentSession, _ *corev1.PersistentVolumeClaim) {
			s.DeletionTimestamp = nil
			s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		},
	} {
		s, c := base()
		edit(s, c)
		if err := expiryVolumeRemovalAllowed(s, c); err == nil {
			t.Errorf("%s: the HOME delete is allowed", name)
		}
	}

	// The release exception.
	s, _ = base()
	s.Status.PrivateHistory.Expiry.Phase = v1alpha1.HomeExpiryExpired
	at := metav1.NewTime(time.Now())
	s.Status.PrivateHistory.Expiry.HomeDeletedAt = &at
	if err := expiryReleaseAllowed(s); err != nil {
		t.Fatalf("an Expired reaped Session is not released: %v", err)
	}
	for name, edit := range map[string]func(*v1alpha1.AgentSession){
		"canary":     func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{} },
		"deleting":   func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory.Expiry.Phase = v1alpha1.HomeExpiryDeleting },
		"not reaped": func(s *v1alpha1.AgentSession) { s.DeletionTimestamp = nil },
		"other home": func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory.Expiry.HomeUID = "other" },
		"no record":  func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory.Expiry = nil },
	} {
		s, _ := base()
		s.Status.PrivateHistory.Expiry.Phase = v1alpha1.HomeExpiryExpired
		s.Status.PrivateHistory.Expiry.HomeDeletedAt = &at
		edit(s)
		if err := expiryReleaseAllowed(s); err == nil {
			t.Errorf("%s: released", name)
		}
	}
	// releaseSession keeps every other protected Session, canaries always.
	s, _ = base()
	if err := releaseSession(context.Background(), nil, s); !errors.Is(err, errPrivateHistoryRetained) {
		t.Errorf("releaseSession released a Deleting Session: %v", err)
	}
}

// The overdue gauge (DESIGN-025 10): with the delete off a record is done at
// Archived; with it on, only at Expired, so DevEnvHomeExpiryStalled (H9)
// sees a delete that stalls.
func TestHomeExpiryOverdueCountsToExpiredWithTheDeleteOn(t *testing.T) {
	tf := newTerminalFixture()
	s := tf.s
	h := s.Status.PrivateHistory
	at := metav1.NewTime(tf.now)
	h.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: h.LastExecutor.PodUID, Phase: "Failed", ObservedAt: at}
	due := metav1.NewTime(tf.now.Add(-time.Hour))
	h.Expiry = &v1alpha1.PrivateHomeExpiryStatus{Version: 1, Phase: v1alpha1.HomeExpiryArchived, DueAt: due}
	for _, tc := range []struct {
		phase    string
		deleteOn bool
		overdue  bool
	}{
		{v1alpha1.HomeExpiryArchived, false, false},
		{v1alpha1.HomeExpiryArchived, true, true},
		{v1alpha1.HomeExpiryDeleting, true, true},
		{v1alpha1.HomeExpiryDeleting, false, false},
		{v1alpha1.HomeExpiryExpired, true, false},
		{v1alpha1.HomeExpiryExporting, false, true},
	} {
		h.Expiry.Phase = tc.phase
		phases, _, overdue := homeExpiryGauges([]v1alpha1.AgentSession{*s}, tf.now, tc.deleteOn)
		if (overdue > 0) != tc.overdue {
			t.Errorf("%s, delete on %v: overdue %v, want overdue %v", tc.phase, tc.deleteOn, overdue, tc.overdue)
		}
		if tc.phase == v1alpha1.HomeExpiryDeleting && phases[expiryGaugeDeleting] != 1 {
			t.Errorf("the deleting gauge: %v", phases)
		}
	}
}
