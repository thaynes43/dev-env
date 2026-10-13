package controller

// DESIGN-025 section 15's cases for H5, against the fake client: canaries and
// unproven Sessions never expire (T4, T6), a resume or a deadline that moved
// cancels (T7), a new Pod cancels (T8), the owner's overrides (T9), Unknown
// changes nothing (T10), a replaced HOME is refused (T11), export refusals
// keep the HOME (T2, T3), restarts adopt the hold pod (T12), and a second
// operator's put answers exists (T13).

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// update changes the stored Session's metadata or spec.
func (f *expiryFixture) update(t *testing.T, edit func(*v1alpha1.AgentSession)) {
	t.Helper()
	s := f.session(t)
	edit(s)
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// updateStatus changes the stored Session's status.
func (f *expiryFixture) updateStatus(t *testing.T, edit func(*v1alpha1.AgentSessionStatus)) {
	t.Helper()
	s := f.session(t)
	edit(&s.Status)
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func (f *expiryFixture) due() time.Time { return f.observed.Add(DefaultHomeExpiryGrace) }

// failRescue makes the rescue unable to run, so the expiry stays in Rescuing
// with its hold pod.
func (f *expiryFixture) failRescue() {
	f.rescuer.mu.Lock()
	defer f.rescuer.mu.Unlock()
	f.rescuer.report = func(*corev1.Pod) (protocol.RescueReport, error) {
		return protocol.RescueReport{}, errors.New("unable to upgrade connection")
	}
}

func (f *expiryFixture) passRescue() {
	f.rescuer.mu.Lock()
	defer f.rescuer.mu.Unlock()
	f.rescuer.report = nil
}

// toRunningHold takes a fresh fixture past its grace to Rescuing with the
// adopted hold pod Running and the rescue unable to run.
func (f *expiryFixture) toRunningHold(t *testing.T) *corev1.Pod {
	t.Helper()
	f.failRescue()
	f.at(f.due().Add(time.Minute))
	f.reconcile(t, 6)
	hold := f.runHold(t)
	f.reconcile(t, 2)
	if e := f.expiry(t); e == nil || e.Phase != v1alpha1.HomeExpiryRescuing || e.HoldPodUID != string(hold.UID) {
		t.Fatalf("not at Rescuing with the hold pod: %+v", e)
	}
	return hold
}

// toExporting takes a fresh fixture to Exporting, with the shelf gone so the
// export is refused and the hold pod stays.
func (f *expiryFixture) toExporting(t *testing.T) *corev1.Pod {
	t.Helper()
	if err := f.c.Delete(context.Background(), shelfPodObject(f.s.Namespace)); err != nil {
		t.Fatal(err)
	}
	f.at(f.due().Add(time.Minute))
	f.reconcile(t, 6)
	hold := f.runHold(t)
	f.reconcile(t, 4)
	if e := f.expiry(t); e == nil || e.Phase != v1alpha1.HomeExpiryExporting || e.Refusal != expiryShelfUnavailable {
		t.Fatalf("not refused at Exporting: %+v", e)
	}
	return hold
}

func (f *expiryFixture) condition(t *testing.T, typ string) *metav1.Condition {
	t.Helper()
	return meta.FindStatusCondition(f.session(t).Status.Conditions, typ)
}

// T4 and T6: a canary original, a Session whose executor vanished before its
// record, one whose executor still runs, and every Session while a flag is off
// never get an expiry record, a hold pod or an export; release-now on a canary
// is refused (canary), once per 15 minutes.
func TestHomeExpiryNeverForCanariesUnprovenSessionsOrWithAFlagOff(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*terminalFixture)
		r     func(*Reconciler)
		check func(*testing.T, *expiryFixture)
	}{
		{name: "canary original", edit: func(tf *terminalFixture) {
			tf.s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{Version: 1, ID: "canary-a"}
			tf.s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryReleaseNow}
		}, check: func(t *testing.T, f *expiryFixture) {
			if n := refusedCount(f.counters, expiryCanary); n != 1 {
				t.Fatalf("canary counted %v times in 15 minutes, want once", n)
			}
			f.at(f.clock.Add(16 * time.Minute))
			f.reconcile(t, 2)
			if n := refusedCount(f.counters, expiryCanary); n != 2 {
				t.Fatalf("canary counted %v times after 15 minutes, want twice", n)
			}
		}},
		{name: "executor vanished before its record", edit: func(tf *terminalFixture) {
			h := tf.s.Status.PrivateHistory
			h.ExecutorMissed = &v1alpha1.PrivateExecutorMissedStatus{Version: 1, PodUID: h.LastExecutor.PodUID, ObservedAt: h.ExecutorTerminal.ObservedAt}
			h.ExecutorTerminal = nil
			tf.pod = nil
		}},
		{name: "executor still runs", edit: func(tf *terminalFixture) {
			tf.s.Status.PrivateHistory.ExecutorTerminal = nil
			tf.pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
		}},
		{name: "archive off", r: func(r *Reconciler) { r.HomeHistoryArchive = false }},
		{name: "terminal record off", r: func(r *Reconciler) { r.PrivateTerminalRecord = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExpiryFixture(t, tc.edit)
			f.at(f.due().Add(60 * 24 * time.Hour))
			r := f.reconciler()
			if tc.r != nil {
				tc.r(r)
			}
			for range 6 {
				if _, err := r.Reconcile(context.Background(), reqFor(f.s)); err != nil {
					t.Fatal(err)
				}
			}
			s := f.session(t)
			if s.Status.PrivateHistory.Expiry != nil || meta.FindStatusCondition(s.Status.Conditions, ConditionHomeExpiry) != nil {
				t.Fatalf("an expiry started: %+v", s.Status.PrivateHistory.Expiry)
			}
			if p := f.named(t); p != nil && isHoldPod(p) || len(f.x.called()) != 0 || len(f.rescuer.ranIn()) != 0 {
				t.Fatalf("a hold pod, export or rescue: pod %v, execs %v", p, f.x.called())
			}
			if f.pod != nil && f.named(t) == nil {
				t.Fatal("the executor Pod was removed")
			}
			f.claimIntact(t)
			if tc.check != nil {
				tc.check(t, f)
			}
		})
	}
}

// T7: a suspended Session whose executor ended on its own is triggered only
// past its archive deadline, from the terminal record's observedAt; a resume
// cancels and keeps the HOME; suspended and due again, a new record's 30 days
// start then. Templates that do not load are Unknown, never a cancel; an
// archiveAfter lengthened past now cancels.
func TestHomeExpiryResumeAndArchiveDeadline(t *testing.T) {
	f := newExpiryFixture(t, func(tf *terminalFixture) {
		tf.s.DeletionTimestamp = nil
		tf.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		tf.pod.Status.Reason = "Evicted"
		tf.s.Status.PrivateHistory.ExecutorTerminal.Reason = "Evicted"
		suspended := metav1.NewTime(tf.now)
		tf.s.Status.SuspendedAt = &suspended
	})
	after := 168 * time.Hour // the example templates' archiveAfter
	f.at(f.now.Add(after - time.Minute))
	f.reconcile(t, 4)
	if e := f.expiry(t); e != nil {
		t.Fatalf("a record before the archive deadline: %+v", e)
	}
	f.at(f.now.Add(after + time.Minute))
	f.reconcile(t, 4)
	e := f.expiry(t)
	if e == nil || e.Phase != v1alpha1.HomeExpiryWaiting || !e.GraceStart.Equal(&f.observed) {
		t.Fatalf("past the deadline, record %+v; want Waiting from the terminal record", e)
	}

	// Templates that do not load, with no archiveAfter of the Session's own:
	// Unknown, no cancel, the grace unchanged.
	unknownBefore := refusedCount(f.counters, expiryUnknown)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}}
	if err := f.c.Delete(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t, 3)
	if got := f.expiry(t); got == nil || !got.DueAt.Equal(&e.DueAt) || refusedCount(f.counters, expiryUnknown) <= unknownBefore {
		t.Fatalf("unreadable templates cancelled or changed the record (%+v), or were not counted Unknown", got)
	}
	if c := f.condition(t, ConditionHomeExpiry); c == nil || c.Status != metav1.ConditionUnknown {
		t.Fatalf("HomeExpiry condition %+v; want Unknown", c)
	}

	// Resumed: the record goes, the HOME stays, and no record is made while
	// the Session runs.
	f.update(t, func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeRunning })
	f.reconcile(t, 4)
	s := f.session(t)
	if s.Status.PrivateHistory.Expiry != nil || s.Status.PrivateHistory.ExpiryClearedAt == nil {
		t.Fatalf("a resume did not cancel: %+v", s.Status.PrivateHistory)
	}
	f.claimIntact(t)
	if p := f.named(t); p == nil || p.UID != f.pod.UID {
		t.Fatal("the cancel removed the ended executor")
	}

	// Suspended and due again: a new record whose 30 days start now.
	f.update(t, func(s *v1alpha1.AgentSession) {
		s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		s.Spec.Lifecycle = &v1alpha1.Lifecycle{ArchiveAfter: &metav1.Duration{Duration: time.Hour}}
	})
	again := f.clock.Add(time.Hour)
	f.updateStatus(t, func(st *v1alpha1.AgentSessionStatus) {
		at := metav1.NewTime(again.Add(-2 * time.Hour))
		st.SuspendedAt = &at
	})
	f.at(again)
	f.reconcile(t, 3)
	e2 := f.expiry(t)
	if e2 == nil || !e2.GraceStart.Time.Equal(recordTime(again).Time) || !e2.DueAt.Time.Equal(recordTime(again).Add(DefaultHomeExpiryGrace)) {
		t.Fatalf("the record after a cancel is %+v; want 30 days from %s", e2, again)
	}

	// archiveAfter lengthened past now: no longer triggered, cancelled.
	f.update(t, func(s *v1alpha1.AgentSession) {
		s.Spec.Lifecycle = &v1alpha1.Lifecycle{ArchiveAfter: &metav1.Duration{Duration: 1000 * time.Hour}}
	})
	f.reconcile(t, 3)
	if got := f.expiry(t); got != nil {
		t.Fatalf("a deadline that moved past now did not cancel: %+v", got)
	}
	f.claimIntact(t)
}

// T8: a new Pod cancels. A replaced executor at Waiting, a hold-labelled Pod
// with another intent, and a Pod of another name that mounts the HOME each
// cancel the record and give PreservationUnknown; the expiry's own hold pod
// goes first, and nothing else is deleted.
func TestHomeExpiryNewPodCancels(t *testing.T) {
	ctx := context.Background()
	preservationUnknown := func(t *testing.T, f *expiryFixture) {
		t.Helper()
		if c := f.condition(t, ConditionPrivateHistory); c == nil || c.Status != metav1.ConditionUnknown || c.Reason != "PreservationUnknown" {
			t.Fatalf("PrivateHistoryRetained %+v; want PreservationUnknown", c)
		}
	}

	t.Run("a replaced executor at Waiting", func(t *testing.T) {
		f := newExpiryFixture(t, nil)
		f.reconcile(t, 3)
		if f.expiry(t) == nil {
			t.Fatal("no record")
		}
		old := f.named(t)
		if err := f.c.Delete(ctx, old); err != nil {
			t.Fatal(err)
		}
		replacement := old.DeepCopy()
		replacement.UID, replacement.ResourceVersion = "4f1c2d3e-0000-4000-8000-0000000000ff", ""
		if err := f.c.Create(ctx, replacement); err != nil {
			t.Fatal(err)
		}
		f.reconcile(t, 4)
		if e := f.expiry(t); e != nil {
			t.Fatalf("a new Pod did not cancel: %+v", e)
		}
		preservationUnknown(t, f)
		if p := f.named(t); p == nil || p.UID != replacement.UID {
			t.Fatal("the new Pod was deleted")
		}
		f.claimIntact(t)
	})

	t.Run("a hold-labelled Pod with another intent", func(t *testing.T) {
		f := newExpiryFixture(t, nil)
		f.at(f.due().Add(time.Minute))
		f.reconcile(t, 4) // the record, Rescuing, the executor removed, the intent
		e := f.expiry(t)
		if e == nil || e.HoldIntent == "" || f.named(t) != nil {
			t.Fatalf("not at the hold pod's create: %+v", e)
		}
		s := f.session(t)
		s.Status.PrivateHistory.Expiry.HoldIntent = "another-intent"
		foreign, err := buildExpiryHoldPod(s, mustTemplates(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.c.Create(ctx, foreign); err != nil {
			t.Fatal(err)
		}
		f.reconcile(t, 4)
		if e := f.expiry(t); e != nil {
			t.Fatalf("a hold pod of another intent did not cancel: %+v", e)
		}
		preservationUnknown(t, f)
		if p := f.named(t); p == nil || p.UID != foreign.UID {
			t.Fatal("the foreign hold pod was deleted")
		}
	})

	t.Run("a Pod of another name mounts the HOME while the hold pod runs", func(t *testing.T) {
		f := newExpiryFixture(t, nil)
		hold := f.toRunningHold(t)
		debug := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "debug-home", Namespace: f.s.Namespace, UID: "4f1c2d3e-0000-4000-8000-0000000000dd"},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "debug", Image: "synthetic.invalid/debug"}},
				Volumes: []corev1.Volume{{Name: "h", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: f.home.Name}}}}}}
		if err := f.c.Create(ctx, debug); err != nil {
			t.Fatal(err)
		}
		f.reconcile(t, 1)
		if p := f.named(t); p != nil {
			t.Fatalf("the expiry's hold pod %s stays; it goes before the record", p.UID)
		}
		if e := f.expiry(t); e == nil || e.HoldPodUID != string(hold.UID) {
			t.Fatalf("the record went before its hold pod was confirmed gone: %+v", e)
		}
		f.reconcile(t, 3)
		if e := f.expiry(t); e != nil {
			t.Fatalf("a Pod mounting the HOME did not cancel: %+v", e)
		}
		var still corev1.Pod
		if err := f.c.Get(ctx, client.ObjectKeyFromObject(debug), &still); err != nil {
			t.Fatal("the debug Pod was deleted")
		}
		f.claimIntact(t)
	})
}

// T9: the owner's overrides. A hold stops every step and removes the hold
// pod without PreservationUnknown; lifted, the hold pod is made again. A
// cancel at Exporting removes the hold pod before the record. Release-now
// skips the grace and nothing else; any other value is counted and ignored.
func TestHomeExpiryOwnerOverrides(t *testing.T) {
	setOverride := func(t *testing.T, f *expiryFixture, v string) {
		t.Helper()
		f.update(t, func(s *v1alpha1.AgentSession) {
			if v == "" {
				delete(s.Annotations, v1alpha1.AnnotationHomeExpiry)
				return
			}
			if s.Annotations == nil {
				s.Annotations = map[string]string{}
			}
			s.Annotations[v1alpha1.AnnotationHomeExpiry] = v
		})
	}

	t.Run("a hold at Waiting past the grace", func(t *testing.T) {
		f := newExpiryFixture(t, nil)
		f.reconcile(t, 2)
		setOverride(t, f, v1alpha1.HomeExpiryHold)
		f.at(f.due().Add(time.Hour))
		f.reconcile(t, 4)
		if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryWaiting {
			t.Fatalf("moved on under a hold: %s", e.Phase)
		}
		if c := f.condition(t, ConditionHomeExpiry); c == nil || c.Reason != "Held" {
			t.Fatalf("HomeExpiry %+v; want Held", c)
		}
		if p := f.named(t); p == nil || p.UID != f.pod.UID {
			t.Fatal("the executor went under a hold")
		}
		setOverride(t, f, "")
		f.reconcile(t, 2)
		if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryRescuing {
			t.Fatalf("lifted, still %s", e.Phase)
		}
	})

	t.Run("a hold at Rescuing removes the hold pod, which comes back", func(t *testing.T) {
		f := newExpiryFixture(t, nil)
		hold := f.toRunningHold(t)
		first := f.expiry(t)
		setOverride(t, f, v1alpha1.HomeExpiryHold)
		f.reconcile(t, 3)
		if p := f.named(t); p != nil {
			t.Fatalf("the hold pod stays under an owner hold: %s", p.UID)
		}
		e := f.expiry(t)
		if e == nil || e.Phase != v1alpha1.HomeExpiryRescuing || e.HoldPodUID != string(hold.UID) {
			t.Fatalf("a hold changed the record: %+v", e)
		}
		if c := f.condition(t, ConditionPrivateHistory); c != nil && c.Status == metav1.ConditionUnknown {
			t.Fatalf("the hold pod's removal raised %+v", c)
		}
		setOverride(t, f, "")
		f.passRescue()
		f.reconcile(t, 3)
		again := f.named(t)
		e = f.expiry(t)
		if again == nil || !isHoldPod(again) || again.UID == hold.UID || e.HoldIntent == first.HoldIntent || e.HoldPodUID != string(again.UID) {
			t.Fatalf("lifted: hold pod %v, record %+v; want a new intent and a new hold pod", again, e)
		}
	})

	t.Run("release-now skips the grace and nothing else", func(t *testing.T) {
		f := newExpiryFixture(t, nil)
		setOverride(t, f, v1alpha1.HomeExpiryReleaseNow)
		f.reconcile(t, 3)
		e := f.expiry(t)
		if e == nil || e.Phase != v1alpha1.HomeExpiryRescuing || e.ReleaseNowAt == nil || f.clock.Before(e.DueAt.Time) == false {
			t.Fatalf("release-now: %+v; want Rescuing before the grace ended, releaseNowAt recorded", e)
		}
		f.reconcile(t, 4)
		f.runHold(t)
		f.reconcile(t, 8)
		if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryArchived || len(f.rescuer.ranIn()) != 1 || len(f.x.called()) != 4 {
			t.Fatalf("release-now skipped more than the grace: %+v, rescues %v, execs %v", e, f.rescuer.ranIn(), f.x.called())
		}
	})

	t.Run("any other value is counted and ignored", func(t *testing.T) {
		f := newExpiryFixture(t, nil)
		setOverride(t, f, "Hold")
		f.at(f.due().Add(time.Minute))
		f.reconcile(t, 3)
		if e := f.expiry(t); e == nil || e.Phase != v1alpha1.HomeExpiryRescuing {
			t.Fatalf("an invalid override held the expiry: %+v", e)
		}
		if n := refusedCount(f.counters, expiryOverrideInvalid); n != 1 {
			t.Fatalf("override_invalid counted %v times, want once per 15 minutes", n)
		}
	})
}

// A cancel at Exporting: the hold pod goes first, confirmed gone while the
// record still names it, then the record; the archive's other entries stay.
func TestHomeExpiryCancelAtExportingRemovesTheHoldPodFirst(t *testing.T) {
	f := newExpiryFixture(t, func(tf *terminalFixture) {
		tf.s.DeletionTimestamp = nil
		tf.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		tf.s.Spec.Lifecycle = &v1alpha1.Lifecycle{ArchiveAfter: &metav1.Duration{Duration: time.Hour}}
		suspended := metav1.NewTime(tf.now.Add(-2 * time.Hour))
		tf.s.Status.SuspendedAt = &suspended
	})
	hold := f.toExporting(t)
	f.update(t, func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeRunning })
	f.reconcile(t, 1)
	if p := f.named(t); p != nil {
		t.Fatalf("the hold pod %s stays", p.UID)
	}
	if e := f.expiry(t); e == nil || e.HoldPodUID != string(hold.UID) {
		t.Fatalf("the record went with the hold pod in one step: %+v", e)
	}
	f.reconcile(t, 3)
	s := f.session(t)
	if s.Status.PrivateHistory.Expiry != nil || s.Status.PrivateHistory.ExpiryClearedAt == nil {
		t.Fatalf("the record stays after its hold pod went: %+v", s.Status.PrivateHistory.Expiry)
	}
	f.claimIntact(t)
}

// T10: an injected timeout, server error or 403 on each uncached read the
// expiry makes, at each step: no transition, nothing written to a Pod, and
// unknown counted.
func TestHomeExpiryUnknownReadsChangeNothing(t *testing.T) {
	errs := map[string]error{
		"timeout": apierrors.NewTimeoutError("the read timed out", 1),
		"5xx":     apierrors.NewInternalError(errors.New("etcd is unavailable")),
		"403":     apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "x", errors.New("RBAC")),
	}
	reads := map[string]func(client.ObjectKey, runtime.Object) bool{
		"the Session":  func(_ client.ObjectKey, o runtime.Object) bool { _, ok := o.(*v1alpha1.AgentSession); return ok },
		"the Pod":      func(_ client.ObjectKey, o runtime.Object) bool { _, ok := o.(*corev1.Pod); return ok },
		"the HOME":     func(_ client.ObjectKey, o runtime.Object) bool { _, ok := o.(*corev1.PersistentVolumeClaim); return ok },
		"the Pod list": func(_ client.ObjectKey, o runtime.Object) bool { _, ok := o.(*corev1.PodList); return ok },
	}
	steps := map[string]func(*testing.T, *expiryFixture){
		"Waiting past the grace": func(t *testing.T, f *expiryFixture) {
			f.reconcile(t, 2)
			f.at(f.due().Add(time.Minute))
		},
		"Rescuing with the hold pod": func(t *testing.T, f *expiryFixture) { f.toRunningHold(t); f.passRescue() },
		"Exporting, re-checked after its refusal": func(t *testing.T, f *expiryFixture) {
			f.toExporting(t)
			if err := f.c.Create(context.Background(), shelfPodObject(f.s.Namespace)); err != nil {
				t.Fatal(err)
			}
			f.at(f.clock.Add(16 * time.Minute))
		},
	}
	for stepName, step := range steps {
		for readName, match := range reads {
			for errName, readErr := range errs {
				t.Run(stepName+"/"+readName+"/"+errName, func(t *testing.T) {
					f := newExpiryFixture(t, nil)
					step(t, f)
					// A minute on: unknown counts once a minute per Session.
					f.at(f.clock.Add(homeExpiryUnknownRetry))
					before := f.session(t).Status.PrivateHistory.Expiry.DeepCopy()
					pod := f.named(t)
					unknown := refusedCount(f.counters, expiryUnknown)
					rescues, execs := len(f.rescuer.ranIn()), len(f.x.called())
					f.readErr = func(key client.ObjectKey, o runtime.Object) error {
						if match(key, o) && calledFrom("readExpiryView") {
							return readErr
						}
						return nil
					}
					f.reconcile(t, 3)
					f.readErr = nil
					after := f.session(t).Status.PrivateHistory.Expiry
					if after.Phase != before.Phase || after.HoldIntent != before.HoldIntent || after.HoldPodUID != before.HoldPodUID || after.Refusal != before.Refusal {
						t.Fatalf("moved on an Unknown read: %+v -> %+v", before, after)
					}
					if now := f.named(t); (now == nil) != (pod == nil) || now != nil && now.UID != pod.UID {
						t.Fatalf("a Pod was written on an Unknown read")
					}
					// Three reconciles in one minute count once, not per event.
					if refusedCount(f.counters, expiryUnknown) != unknown+1 {
						t.Fatalf("unknown counted %v, want once more than %v", refusedCount(f.counters, expiryUnknown), unknown)
					}
					if len(f.rescuer.ranIn()) != rescues || len(f.x.called()) != execs {
						t.Fatalf("a rescue or an export ran on an Unknown read")
					}
				})
			}
		}
	}
}

// T11: a HOME claim with another UID, or one being deleted, is refused
// home_replaced: recorded, counted, re-checked every 15 minutes, nothing
// deleted.
func TestHomeExpiryReplacedHomeIsRefused(t *testing.T) {
	f := newExpiryFixture(t, nil)
	f.reconcile(t, 2)
	f.at(f.due().Add(time.Minute))
	ctx := context.Background()
	// A delete of the claim: the operator's finalizer keeps it, deleting.
	if err := f.c.Delete(ctx, f.home.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t, 3)
	e := f.expiry(t)
	if e.Phase != v1alpha1.HomeExpiryWaiting || e.Refusal != expiryHomeReplaced || refusedCount(f.counters, expiryHomeReplaced) != 1 {
		t.Fatalf("a deleting HOME: %+v, counted %v", e, refusedCount(f.counters, expiryHomeReplaced))
	}
	if p := f.named(t); p == nil || p.UID != f.pod.UID {
		t.Fatal("the executor was removed with the HOME replaced")
	}
	// Re-checked, and re-counted, every 15 minutes, not on every event.
	f.reconcile(t, 3)
	if n := refusedCount(f.counters, expiryHomeReplaced); n != 1 {
		t.Fatalf("re-counted %v times within 15 minutes", n)
	}
	f.at(f.clock.Add(16 * time.Minute))
	f.reconcile(t, 2)
	if n := refusedCount(f.counters, expiryHomeReplaced); n != 2 {
		t.Fatalf("counted %v times after 15 minutes, want 2", n)
	}
	var claim corev1.PersistentVolumeClaim
	if err := f.c.Get(ctx, client.ObjectKeyFromObject(f.home), &claim); err != nil || !controllerutil.ContainsFinalizer(&claim, Finalizer) {
		t.Fatalf("the claim lost its finalizer: %v", err)
	}
}

// T2 and T3: an export refused for a credential in the tree, an unclassified
// name or an unknown layout, and a shelf that is missing or has no archive,
// each stop the expiry at Exporting with the reason recorded: nothing is
// archived, the hold pod and the HOME stay.
func TestHomeExpiryExportRefusalsKeepTheHome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		setup  func(*testing.T, *expiryFixture)
		edit   func(*terminalFixture)
	}{
		{name: "a Codex login copied into the sessions tree", reason: protocol.HistoryErrCredentialInTree, setup: func(t *testing.T, f *expiryFixture) {
			writeExpiryFile(t, f.x.home+"/.codex/sessions/2026/10/12/auth.json", `{"tokens":{}}`)
		}},
		{name: "a temporary copy of the Claude login inside projects", reason: protocol.HistoryErrCredentialInTree, setup: func(t *testing.T, f *expiryFixture) {
			writeExpiryFile(t, f.x.home+"/.claude/projects/-home-dev/.credentials.json.tmp.x", `{}`)
		}},
		{name: "an unclassified name in the provider directory", reason: protocol.HistoryErrUnknownEntry, setup: func(t *testing.T, f *expiryFixture) {
			writeExpiryFile(t, f.x.home+"/.claude/brand-new-cache/x", "x")
		}},
		{name: "an unclassified name in agentd's state", reason: protocol.HistoryErrUnknownEntry, setup: func(t *testing.T, f *expiryFixture) {
			writeExpiryFile(t, f.x.home+"/.agentd/brand-new-state.json", "{}")
		}},
		{name: "a provider directory the executor did not set literally", reason: protocol.HistoryErrLayoutUnknown, edit: func(tf *terminalFixture) {
			tf.s.Status.PrivateHistory.ExecutorTerminal.CodexHome = ""
		}},
		{name: "no shelf", reason: expiryShelfUnavailable, setup: func(t *testing.T, f *expiryFixture) {
			if err := f.c.Delete(context.Background(), shelfPodObject(f.s.Namespace)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a shelf without the history archive", reason: expiryShelfUnavailable, setup: func(t *testing.T, f *expiryFixture) {
			f.x.archive.Home = f.x.archive.Root // the same device: not mounted
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExpiryFixture(t, tc.edit)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			f.at(f.due().Add(time.Minute))
			f.reconcile(t, 6)
			hold := f.runHold(t)
			f.reconcile(t, 5)
			e := f.expiry(t)
			if e.Phase != v1alpha1.HomeExpiryExporting || e.Refusal != tc.reason || e.Archive != nil || refusedCount(f.counters, tc.reason) != 1 {
				t.Fatalf("record %+v, %s counted %v; want refused %s at Exporting", e, tc.reason, refusedCount(f.counters, tc.reason), tc.reason)
			}
			if got := f.x.entries(t); len(got) != 0 {
				t.Fatalf("archived %v after a refusal", got)
			}
			if p := f.named(t); p == nil || p.UID != hold.UID {
				t.Fatal("the hold pod went after a refusal")
			}
			if c := f.condition(t, ConditionHomeExpiry); c == nil || c.Reason != "Refused" {
				t.Fatalf("HomeExpiry %+v; want Refused", c)
			}
			f.claimIntact(t)
		})
	}
}

// T12, the hold pod's adoption: an operator that stops between the hold
// pod's create and the write of holdPodUID finds the pod by its intent and
// adopts it, with no cancel and no PreservationUnknown. A hold lifted while
// the old hold pod still terminates writes the new intent only once it is
// gone.
func TestHomeExpiryAdoptsItsHoldPodAndWaitsForTheOldOne(t *testing.T) {
	ctx := context.Background()
	f := newExpiryFixture(t, nil)
	f.failRescue()
	f.at(f.due().Add(time.Minute))
	f.reconcile(t, 5) // the record, Rescuing, the executor removed, the intent, the create
	created := f.named(t)
	if created == nil || !isHoldPod(created) || f.expiry(t).HoldPodUID != "" {
		t.Fatalf("not between the create and the adoption: %v %+v", created, f.expiry(t))
	}
	f.reconcile(t, 1) // a new operator: adopts
	if e := f.expiry(t); e == nil || e.HoldPodUID != string(created.UID) {
		t.Fatalf("not adopted: %+v", e)
	}
	if c := f.condition(t, ConditionPrivateHistory); c != nil && c.Status == metav1.ConditionUnknown {
		t.Fatalf("adoption raised %+v", c)
	}

	// The hold pod terminates slowly (a finalizer plays the kubelet).
	held := f.named(t)
	held.Finalizers = []string{"test.dev-env/terminating"}
	if err := f.c.Update(ctx, held); err != nil {
		t.Fatal(err)
	}
	f.runHold(t)
	f.update(t, func(s *v1alpha1.AgentSession) {
		s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
	})
	f.reconcile(t, 2)
	if p := f.named(t); p == nil || p.DeletionTimestamp.IsZero() {
		t.Fatal("the hold pod was not removed under the owner hold")
	}
	f.update(t, func(s *v1alpha1.AgentSession) { s.Annotations = nil })
	intent := f.expiry(t).HoldIntent
	f.reconcile(t, 3)
	if e := f.expiry(t); e.HoldIntent != intent || e.HoldPodUID != string(created.UID) {
		t.Fatalf("a new intent while the old hold pod terminates: %+v", e)
	}
	if e := f.expiry(t); e == nil {
		t.Fatal("the terminating hold pod cancelled the record")
	}
	p := f.named(t)
	p.Finalizers = nil
	if err := f.c.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t, 3)
	if e := f.expiry(t); e.HoldIntent == intent || e.HoldPodUID == string(created.UID) {
		t.Fatalf("no new intent once the old hold pod is gone: %+v", e)
	}
}

// T13: two operators. One stores the archive entry and loses its Archived
// write; the other's put answers exists, and one entry is recorded.
func TestHomeExpirySecondOperatorFindsTheEntry(t *testing.T) {
	f := newExpiryFixture(t, nil)
	f.at(f.due().Add(time.Minute))
	f.reconcile(t, 6)
	f.runHold(t)
	f.reconcile(t, 2) // the rescue, then Exporting
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryExporting {
		t.Fatalf("not at Exporting: %s", e.Phase)
	}
	lost := false
	f.statusErr = func(o client.Object) error {
		if s, ok := o.(*v1alpha1.AgentSession); ok && !lost && s.Status.PrivateHistory != nil && s.Status.PrivateHistory.Expiry != nil &&
			s.Status.PrivateHistory.Expiry.Phase == v1alpha1.HomeExpiryArchived {
			lost = true
			return apierrors.NewConflict(schema.GroupResource{Resource: "agentsessions"}, s.Name, errors.New("another operator wrote first"))
		}
		return nil
	}
	f.reconcile(t, 1)
	if !lost || f.expiry(t).Phase != v1alpha1.HomeExpiryExporting || len(f.x.entries(t)) != 1 {
		t.Fatalf("the first operator: lost %v, phase %s, entries %v", lost, f.expiry(t).Phase, f.x.entries(t))
	}
	f.statusErr = nil
	f.reconcile(t, 2)
	e := f.expiry(t)
	if e.Phase != v1alpha1.HomeExpiryArchived || len(f.x.entries(t)) != 1 || f.x.entries(t)[0] != e.Archive.Entry {
		t.Fatalf("the second operator: %+v, entries %v", e, f.x.entries(t))
	}
	if counterValue(f.counters.ArchivedBytes) != float64(e.Archive.Bytes) {
		t.Fatalf("archived bytes counted %v, want once (%d)", counterValue(f.counters.ArchivedBytes), e.Archive.Bytes)
	}
}

// Section 14: with archive off, a record stays in its phase and the
// expiry's hold pod goes, because nothing needs it; nothing else moves.
func TestHomeExpiryArchiveOffRemovesItsHoldPod(t *testing.T) {
	f := newExpiryFixture(t, nil)
	hold := f.toRunningHold(t)
	r := f.reconciler()
	r.HomeHistoryArchive = false
	for range 3 {
		if _, err := r.Reconcile(context.Background(), reqFor(f.s)); err != nil {
			t.Fatal(err)
		}
	}
	if p := f.named(t); p != nil {
		t.Fatalf("the hold pod %s stays with archive off", p.UID)
	}
	if e := f.expiry(t); e == nil || e.Phase != v1alpha1.HomeExpiryRescuing || e.HoldPodUID != string(hold.UID) {
		t.Fatalf("archive off changed the record: %+v", e)
	}
	f.claimIntact(t)
}

func reqFor(s *v1alpha1.AgentSession) ctrl.Request {
	return ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}
}

func mustTemplates(t *testing.T) *templates.Templates {
	t.Helper()
	tm, err := templates.Parse(map[string]string{templates.Key: exampleDoc})
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// A recorded refusal goes at the re-check after its cause is fixed (5.2),
// also where the step has nothing to write: at Waiting before the grace ends,
// and at Archived, where H5 stops for good.
func TestHomeExpiryRefusalClearsWhenItsCauseIsFixed(t *testing.T) {
	ctx := context.Background()
	setAdmission := func(t *testing.T, f *expiryFixture, v string) {
		t.Helper()
		var c corev1.PersistentVolumeClaim
		if err := f.c.Get(ctx, client.ObjectKeyFromObject(f.home), &c); err != nil {
			t.Fatal(err)
		}
		c.Annotations[privateHistoryAdmissionAnnotation] = v
		if err := f.c.Update(ctx, &c); err != nil {
			t.Fatal(err)
		}
	}
	check := func(t *testing.T, f *expiryFixture, phase string) {
		t.Helper()
		setAdmission(t, f, "99")
		f.reconcile(t, 3)
		if e := f.expiry(t); e.Phase != phase || e.Refusal != expiryIdentityChanged || e.RefusalAt == nil {
			t.Fatalf("a changed claim binding at %s: %+v; want identity_changed recorded", phase, e)
		}
		setAdmission(t, f, "3")
		f.reconcile(t, 2)
		if e := f.expiry(t); e.Refusal == "" {
			t.Fatal("the refusal went before its 15-minute re-check")
		}
		f.at(f.clock.Add(homeExpiryRefusalRetry + time.Minute))
		f.reconcile(t, 2)
		e := f.expiry(t)
		if e.Phase != phase || e.Refusal != "" || e.RefusalAt != nil {
			t.Fatalf("at %s, the fixed cause's refusal stays: %+v", phase, e)
		}
		gauges := gaugeValues(t, &HomeExpiryCollector{Reader: f.c, Namespace: f.s.Namespace, Now: func() time.Time { return *f.clock }})
		if gauges[MetricHomeExpiryRefusing+"/"+expiryIdentityChanged] != 0 {
			t.Fatalf("the refusing gauge still counts it: %v", gauges)
		}
	}

	t.Run("at Waiting, before the grace ends", func(t *testing.T) {
		f := newExpiryFixture(t, nil)
		f.reconcile(t, 2)
		check(t, f, v1alpha1.HomeExpiryWaiting)
		if f.clock.After(f.due()) {
			t.Fatal("the test ran past the grace")
		}
	})
	t.Run("at Archived", func(t *testing.T) {
		f := newExpiryFixture(t, nil)
		f.at(f.due().Add(time.Minute))
		f.reconcile(t, 6)
		f.runHold(t)
		f.reconcile(t, 8)
		if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryArchived {
			t.Fatalf("not Archived: %+v", e)
		}
		check(t, f, v1alpha1.HomeExpiryArchived)
	})
}

// Section 14 and default off: with archive off, nothing of the expiry runs,
// and the archive timer of a Suspended private Session does not start while
// its ended executor's Pod stands; with archive on, it does (T7 relies on it).
func TestHomeExpiryTimerStartOnlyWithTheArchiveOn(t *testing.T) {
	for _, on := range []bool{false, true} {
		f := newExpiryFixture(t, func(tf *terminalFixture) {
			tf.s.DeletionTimestamp = nil
			tf.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		})
		r := f.reconciler()
		r.HomeHistoryArchive = on
		for range 3 {
			if _, err := r.Reconcile(context.Background(), reqFor(f.s)); err != nil {
				t.Fatal(err)
			}
		}
		if got := f.session(t).Status.SuspendedAt != nil; got != on {
			t.Fatalf("archive %v: suspendedAt set %v", on, got)
		}
	}
}

// A Pod of another name that mounts the HOME keeps PreservationUnknown after
// the cancel it caused, while it is there; once it goes, a new record starts
// with its own grace.
func TestHomeExpiryForeignMountStaysVisibleAfterTheCancel(t *testing.T) {
	ctx := context.Background()
	f := newExpiryFixture(t, nil)
	f.reconcile(t, 2)
	debug := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "debug-home", Namespace: f.s.Namespace},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "debug", Image: "synthetic.invalid/debug"}},
			Volumes: []corev1.Volume{{Name: "h", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: f.home.Name}}}}}}
	if err := f.c.Create(ctx, debug); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t, 4)
	s := f.session(t)
	if s.Status.PrivateHistory.Expiry != nil || s.Status.PrivateHistory.ExpiryClearedAt == nil {
		t.Fatalf("the foreign mount did not cancel: %+v", s.Status.PrivateHistory)
	}
	for range 2 {
		f.reconcile(t, 1)
		if c := f.condition(t, ConditionPrivateHistory); c == nil || c.Reason != "PreservationUnknown" {
			t.Fatalf("after the cancel, PrivateHistoryRetained is %+v; want PreservationUnknown while the Pod mounts the HOME", c)
		}
	}
	if err := f.c.Delete(ctx, debug); err != nil {
		t.Fatal(err)
	}
	f.at(f.clock.Add(time.Hour))
	f.reconcile(t, 3)
	e := f.expiry(t)
	if e == nil || !e.GraceStart.Time.Equal(recordTime(*f.clock).Time) {
		t.Fatalf("once the Pod went, the new record is %+v; want 30 days from now", e)
	}
	if c := f.condition(t, ConditionPrivateHistory); c == nil || c.Reason == "PreservationUnknown" {
		t.Fatalf("PreservationUnknown stays after the Pod went: %+v", c)
	}
}

// The relay reaches the hold pod by name. A hold pod replaced during the
// export is never recorded as the source of Archived: the entry put stored
// stays, unreferenced, and the replacement cancels the record.
func TestHomeExpiryExportFromAReplacedHoldPodIsNotRecorded(t *testing.T) {
	ctx := context.Background()
	f := newExpiryFixture(t, nil)
	f.at(f.due().Add(time.Minute))
	f.reconcile(t, 6)
	hold := f.runHold(t)
	f.reconcile(t, 2) // the rescue, then Exporting
	f.x.before = map[string]func(){"verify": nil, "put": func() {
		if err := f.c.Delete(ctx, hold.DeepCopy()); err != nil {
			t.Error(err)
		}
		replacement := hold.DeepCopy()
		replacement.UID, replacement.ResourceVersion = "", ""
		replacement.Annotations[homeExpiryHoldIntentAnnotation] = "someone-else"
		if err := f.c.Create(ctx, replacement); err != nil {
			t.Error(err)
		}
	}}
	f.reconcile(t, 1)
	if e := f.expiry(t); e == nil || e.Phase != v1alpha1.HomeExpiryExporting || e.Archive != nil {
		t.Fatalf("an export from a replaced hold pod was recorded: %+v", e)
	}
	for _, c := range f.x.called() {
		if c == "verify" {
			t.Fatal("verify ran after the hold pod was replaced")
		}
	}
	f.reconcile(t, 3)
	if e := f.expiry(t); e != nil {
		t.Fatalf("the replacement did not cancel: %+v", e)
	}
	if got := f.x.entries(t); len(got) != 1 {
		t.Fatalf("archive entries %v; the stored one stays", got)
	}
}

// A record parked where nothing can act (Waiting before its grace, Archived)
// costs no uncached reads: every one of them fails here, and nothing is
// counted Unknown.
func TestHomeExpiryParkedRecordsSkipTheUncachedReads(t *testing.T) {
	f := newExpiryFixture(t, nil)
	f.reconcile(t, 2)
	failReads := func(client.ObjectKey, runtime.Object) error {
		if calledFrom("readExpiryView") {
			return apierrors.NewInternalError(errors.New("etcd is unavailable"))
		}
		return nil
	}
	f.readErr = failReads
	f.at(f.due().Add(-time.Hour))
	f.reconcile(t, 3)
	f.readErr = nil
	if n := refusedCount(f.counters, expiryUnknown); n != 0 {
		t.Fatalf("a parked Waiting record read uncached: unknown %v", n)
	}
	f.at(f.due().Add(time.Minute))
	f.reconcile(t, 6)
	f.runHold(t)
	f.reconcile(t, 8)
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryArchived {
		t.Fatalf("not Archived: %+v", e)
	}
	f.readErr = failReads
	f.at(f.clock.Add(48 * time.Hour))
	f.reconcile(t, 3)
	f.readErr = nil
	if n := refusedCount(f.counters, expiryUnknown); n != 0 {
		t.Fatalf("a parked Archived record read uncached: unknown %v", n)
	}
}
