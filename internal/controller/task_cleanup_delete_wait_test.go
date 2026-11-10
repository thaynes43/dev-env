package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// taskDrainEvents reads the events a fake recorder has queued.
func taskDrainEvents(rec *events.FakeRecorder) []string {
	if rec == nil {
		return nil
	}
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// taskRescueReleaseRefusal is the check's reason inside a rescue-step
// volume_not_released event, and the event. The event ends with the wait
// that path ran: the 30-minute release wait, or the attempt's 30-minute
// bound when that bound is reached first.
func taskRescueReleaseRefusal(evs []string) (why, event string) {
	const prefix = "the task clean-up keeps the HOME: " + expiryVolumeNotReleased + ": "
	suffixes := []string{
		" for " + taskReleaseWait.String(),
		" at the attempt's " + taskAttemptDeadline.String() + " bound",
	}
	for _, e := range evs {
		i := strings.Index(e, prefix)
		if i < 0 {
			continue
		}
		body := e[i+len(prefix):]
		for _, suffix := range suffixes {
			if strings.HasSuffix(body, suffix) {
				return strings.TrimSuffix(body, suffix), e
			}
		}
	}
	return "", ""
}

func taskReconcile(t *testing.T, f *taskFixture) ctrl.Result {
	t.Helper()
	res, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func taskAdvance(t *testing.T, f *taskFixture, d time.Duration) {
	t.Helper()
	f.tick(t, f.clock.Add(d))
	var leases coordinationv1.LeaseList
	if err := f.c.List(context.Background(), &leases, client.InNamespace("kube-node-lease")); err != nil {
		t.Fatal(err)
	}
	for i := range leases.Items {
		l := &leases.Items[i]
		l.Spec.RenewTime = &metav1.MicroTime{Time: f.clock.Add(-time.Second)}
		if err := f.c.Update(context.Background(), l); err != nil {
			t.Fatal(err)
		}
	}
}

func setNodeVolumes(t *testing.T, f *taskFixture, name string, vols ...corev1.UniqueVolumeName) {
	t.Helper()
	var n corev1.Node
	if err := f.c.Get(context.Background(), client.ObjectKey{Name: name}, &n); err != nil {
		t.Fatal(err)
	}
	n.Status.VolumesInUse = vols
	if err := f.c.Status().Update(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
}

func deleteReleaseRemembered(f *taskFixture) (time.Time, bool) {
	st := &f.r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	at, ok := st.deleteReleaseWait[f.s.UID]
	return at, ok
}

// taskDeletingVolume is a record at Deleting whose hold pod is gone, with a
// node other than the first executor still listing the HOME's volume.
func taskDeletingVolume(t *testing.T) (*taskFixture, *corev1.Node) {
	t.Helper()
	f := taskAtPhase(t, v1alpha1.HomeExpiryDeleting)
	n := taskReadyOtherNode(t, f)
	n.Status.VolumesInUse = []corev1.UniqueVolumeName{homeVolumeUniqueName()}
	if err := f.c.Status().Update(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	return f, n
}

func assertDeleteWaiting(t *testing.T, f *taskFixture, res ctrl.Result) {
	t.Helper()
	e := f.expiry(t)
	if e == nil || e.Phase != v1alpha1.HomeExpiryDeleting || e.Refusal != "" || !taskActive(f.session(t)) {
		t.Fatalf("record %+v active=%v; want Deleting, no refusal, slot held", e, e != nil && taskActive(f.session(t)))
	}
	c := f.claim(t)
	if c == nil || !c.DeletionTimestamp.IsZero() {
		t.Fatal("the HOME claim is gone or being deleted")
	}
	if res.RequeueAfter != taskDeleteReleasePoll {
		t.Fatalf("requeue %s; want the delete poll %s", res.RequeueAfter, taskDeleteReleasePoll)
	}
	for _, typ := range []string{ConditionPodReady, ConditionHomeExpiry} {
		cond := f.condition(t, typ)
		if cond == nil || !strings.Contains(cond.Message, taskDeleteReleaseWaiting) {
			t.Fatalf("%s %+v; want the wait note", typ, cond)
		}
	}
	home := f.condition(t, ConditionHomeExpiry)
	if home.Reason != taskReasonDeleting {
		t.Fatalf("HomeExpiry reason %s; want %s", home.Reason, taskReasonDeleting)
	}
	if _, ok := deleteReleaseRemembered(f); !ok {
		t.Fatal("the delete step did not remember when the wait started")
	}
}

// TestTaskCleanupDeleteReleaseWait is the delete step's wait for the hold
// pod's node to drop the HOME's volume (DESIGN-061 1.1, H4 probes of
// 2026-11-10). The node that lists the volume is not the first executor.
func TestTaskCleanupDeleteReleaseWait(t *testing.T) {
	t.Run("waits", func(t *testing.T) {
		f, _ := taskDeletingVolume(t)
		res := taskReconcile(t, f)
		assertDeleteWaiting(t, f, res)
		if len(f.claimDeletes()) != 0 {
			t.Fatalf("the HOME was deleted: %v", f.claimDeletes())
		}
		f.kept(t)
	})

	t.Run("releases", func(t *testing.T) {
		f, n := taskDeletingVolume(t)
		rec := events.NewFakeRecorder(64)
		f.r.Recorder = rec
		taskReconcile(t, f)
		setNodeVolumes(t, f, n.Name)
		res := taskReconcile(t, f)
		e := f.expiry(t)
		if e == nil || e.Refusal != "" {
			t.Fatalf("record %+v; want no refusal", e)
		}
		if f.claim(t) != nil {
			t.Fatal("the HOME claim still exists")
		}
		if res.RequeueAfter != time.Second {
			t.Fatalf("requeue %s; want 1s after the delete", res.RequeueAfter)
		}
		dels := f.claimDeletes()
		if len(dels) != 1 || !strings.Contains(dels[0], "uid="+string(f.home.UID)) {
			t.Fatalf("claim deletes %v; want one delete of the bound HOME", dels)
		}
		if _, ok := deleteReleaseRemembered(f); ok {
			t.Fatal("the delete step kept the wait after the volume was released")
		}
		for range 6 {
			if f.sessionGone(t) {
				break
			}
			if cur := f.expiry(t); cur != nil && cur.Refusal != "" {
				t.Fatalf("a refusal was written on the way to Expired: %+v", cur)
			}
			if cur := f.expiry(t); cur != nil && cur.Phase == v1alpha1.HomeExpiryExpired {
				taskReconcile(t, f)
				break
			}
			taskReconcile(t, f)
		}
		if !f.claimGone(t) {
			t.Fatal("the HOME claim came back")
		}
		for _, ev := range taskDrainEvents(rec) {
			if strings.Contains(ev, "TaskCleanupRefused") || strings.Contains(ev, expiryVolumeNotReleased) {
				t.Fatalf("a refusal event was written: %s", ev)
			}
		}
	})

	t.Run("refuses", func(t *testing.T) {
		f, n := taskDeletingVolume(t)
		rec := events.NewFakeRecorder(16)
		f.r.Recorder = rec
		taskReconcile(t, f)
		taskAdvance(t, f, taskDeleteReleaseWait)
		res := taskReconcile(t, f)
		e := f.expiry(t)
		if e == nil || e.Phase != v1alpha1.HomeExpiryDeleting || e.Refusal != expiryVolumeNotReleased || taskActive(f.session(t)) {
			t.Fatalf("record %+v active=%v; want volume_not_released at Deleting, slot free", e, e != nil && taskActive(f.session(t)))
		}
		if f.claim(t) == nil || len(f.claimDeletes()) != 0 {
			t.Fatal("the HOME claim was deleted")
		}
		if _, ok := deleteReleaseRemembered(f); ok {
			t.Fatal("the delete step kept the wait after the refusal")
		}
		var refused string
		for _, ev := range taskDrainEvents(rec) {
			if strings.Contains(ev, "TaskCleanupRefused") {
				refused = ev
			}
		}
		want := "node " + n.Name + " still lists the HOME's volume"
		if !strings.Contains(refused, want) || strings.Contains(refused, "for 30m0s") || strings.Contains(refused, "within 30m0s") || strings.Contains(refused, "node "+f.node.Name+" still lists") {
			t.Fatalf("refusal event %q; want %q and no 30-minute claim", refused, want)
		}
		// The next reconcile writes the condition from a fresh check. The
		// record still has no duration, so the sentence does not invent one.
		res = taskReconcile(t, f)
		c := f.condition(t, ConditionPodReady)
		if c == nil || !strings.Contains(c.Message, want) || strings.Contains(c.Message, "30m0s") || strings.Contains(c.Message, "node "+f.node.Name+" still lists") {
			t.Fatalf("PodReady %+v; want %q and no duration", c, want)
		}
		if e := f.expiry(t); e.Refusal != expiryVolumeNotReleased || e.Phase != v1alpha1.HomeExpiryDeleting {
			t.Fatalf("record %+v changed while the refusal was waiting", e)
		}
		if f.claim(t) == nil {
			t.Fatal("the HOME claim was deleted after the refusal")
		}
		if res.RequeueAfter != homeExpiryRefusalRetry+time.Second {
			t.Fatalf("requeue %s; want the refusal retry %s", res.RequeueAfter, homeExpiryRefusalRetry+time.Second)
		}
	})

	t.Run("forgets after success", func(t *testing.T) {
		f, n := taskDeletingVolume(t)
		taskReconcile(t, f)
		taskAdvance(t, f, taskDeleteReleaseWait-10*time.Second)
		res := taskReconcile(t, f)
		assertDeleteWaiting(t, f, res)
		setNodeVolumes(t, f, n.Name)
		f.deleteErr = func(obj client.Object) error {
			if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
				return apierrors.NewConflict(corev1.Resource("persistentvolumeclaims"), obj.GetName(), errors.New("held by the test"))
			}
			return nil
		}
		taskReconcile(t, f)
		f.deleteErr = nil
		if f.claim(t) == nil || f.expiry(t).Refusal != "" {
			t.Fatalf("the conflict deleted the HOME or recorded a refusal: claim %v record %+v", f.claim(t), f.expiry(t))
		}
		if _, ok := deleteReleaseRemembered(f); ok {
			t.Fatal("a released volume left the old wait in place")
		}
		setNodeVolumes(t, f, n.Name, homeVolumeUniqueName())
		// Past the original 5 minutes. A stale start would refuse now.
		taskAdvance(t, f, 20*time.Second)
		res = taskReconcile(t, f)
		assertDeleteWaiting(t, f, res)
		taskAdvance(t, f, taskDeleteReleaseWait-time.Second)
		res = taskReconcile(t, f)
		assertDeleteWaiting(t, f, res)
		taskAdvance(t, f, time.Second)
		taskReconcile(t, f)
		e := f.expiry(t)
		if e == nil || e.Refusal != expiryVolumeNotReleased || f.claim(t) == nil {
			t.Fatalf("record %+v claim %v; want a fresh wait's refusal and the HOME kept", e, f.claim(t) != nil)
		}
	})

	t.Run("forgets after refusal", func(t *testing.T) {
		f, _ := taskDeletingVolume(t)
		taskReconcile(t, f)
		taskAdvance(t, f, taskDeleteReleaseWait)
		taskReconcile(t, f)
		if f.expiry(t).Refusal != expiryVolumeNotReleased {
			t.Fatal("the first wait did not refuse")
		}
		if _, ok := deleteReleaseRemembered(f); ok {
			t.Fatal("the refusal left the wait remembered")
		}
		taskAdvance(t, f, homeExpiryRefusalRetry+time.Second)
		res := taskReconcile(t, f)
		e := f.expiry(t)
		if e == nil || e.Phase != v1alpha1.HomeExpiryDeleting || e.Refusal != "" || !taskActive(f.session(t)) || f.claim(t) == nil {
			t.Fatalf("retry record %+v claim %v; want Deleting, admitted, HOME kept", e, f.claim(t) != nil)
		}
		if res.RequeueAfter != time.Second {
			t.Fatalf("retry requeue %s; want 1s", res.RequeueAfter)
		}
		// A stale start is already older than 5 minutes, so it would refuse
		// on this look. A forgotten start waits again.
		res = taskReconcile(t, f)
		assertDeleteWaiting(t, f, res)
		taskAdvance(t, f, taskDeleteReleaseWait-time.Second)
		res = taskReconcile(t, f)
		assertDeleteWaiting(t, f, res)
		taskAdvance(t, f, time.Second)
		taskReconcile(t, f)
		if f.expiry(t).Refusal != expiryVolumeNotReleased || f.claim(t) == nil {
			t.Fatalf("record %+v; want the fresh wait to refuse and keep the HOME", f.expiry(t))
		}
	})
}

func TestTaskCleanupHoldPodRemovedSaysTheRescueIsSaved(t *testing.T) {
	if holdReleaseArchived != "the history is archived" {
		t.Fatalf("guard identifier is %q; the archive path prints it", holdReleaseArchived)
	}
	f := taskAtPhase(t, v1alpha1.HomeExpiryArchived)
	rec := events.NewFakeRecorder(8)
	f.r.Recorder = rec
	taskReconcile(t, f)
	var note string
	for _, ev := range taskDrainEvents(rec) {
		if strings.Contains(ev, "HoldPodRemoved") {
			note = ev
		}
	}
	if !strings.Contains(note, taskHoldReleaseSaved) || strings.Contains(note, string(holdReleaseArchived)) {
		t.Fatalf("HoldPodRemoved %q; want %q", note, taskHoldReleaseSaved)
	}
	if f.named(t) != nil {
		t.Fatal("the hold pod stayed")
	}
}
