package controller

// Regressions for the coordinator's eight rulings on PR #1029 (OP520).
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func taskReadyOtherNode(t *testing.T, f *taskFixture) *corev1.Node {
	t.Helper()
	n := f.node.DeepCopy()
	n.Name, n.UID, n.ResourceVersion = "worker-b", "node-2", ""
	if err := f.c.Create(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	l := f.lease.DeepCopy()
	l.Name, l.UID, l.ResourceVersion = n.Name, "lease-2", ""
	l.Spec.HolderIdentity = &n.Name
	l.Spec.RenewTime = &metav1.MicroTime{Time: f.clock.Add(-time.Second)}
	l.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: n.Name, UID: n.UID}}
	if err := f.c.Create(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	return n
}

func taskAtPhase(t *testing.T, phase string) *taskFixture {
	t.Helper()
	f := newTaskFixture(t, nil)
	f.toHold(t)
	if phase != v1alpha1.HomeExpiryRescuing {
		f.passRescue()
		f.run(t, 2) // rescue, then save the bundle
	}
	if phase == v1alpha1.HomeExpiryDeleting {
		f.run(t, 2) // remove hold, then prepare deletion
	}
	if e := f.expiry(t); e.Phase != phase {
		t.Fatalf("setup reached %s, want %s", e.Phase, phase)
	}
	return f
}

func TestTaskCleanupDisabledAtRescuingKeepsHome(t *testing.T) {
	f := newTaskFixture(t, nil)
	f.run(t, 4) // admitted, executor gone, hold intent recorded
	before := f.expiry(t).DeepCopy()
	if before.Phase != v1alpha1.HomeExpiryRescuing || f.named(t) != nil {
		t.Fatal("setup did not reach Rescuing before the hold")
	}
	f.r = f.fresh()
	f.r.TaskSessionCleanup = false // all retired archive flags are off in production
	f.run(t, 4)
	if f.named(t) != nil || len(f.rescuer.ranIn()) != 0 || !reflect.DeepEqual(before, f.expiry(t)) {
		t.Fatal("disabled cleanup mounted, rescued, or moved the record")
	}
	f.kept(t)
}

func TestTaskCleanupUnobservedOwnerHoldDoesNotFreeSlot(t *testing.T) {
	f := taskAtPhase(t, v1alpha1.HomeExpiryRescuing)
	f.r.TaskCleanupMaxActive = 1
	ctx := context.Background()
	other := f.session(t).DeepCopy()
	other.Name, other.UID, other.ResourceVersion = "queued-b", "queued-b-uid", ""
	other.DeletionTimestamp = nil
	other.Status.PrivateHistory.SessionUID = string(other.UID)
	other.Status.PrivateHistory.Expiry.Phase = v1alpha1.HomeExpiryWaiting
	other.Status.PrivateHistory.Expiry.DueAt = metav1.NewTime(f.clock.Add(-time.Hour))
	status := other.Status.DeepCopy()
	if err := f.c.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	other.Status = *status
	if err := f.c.Status().Update(ctx, other); err != nil {
		t.Fatal(err)
	}
	// The queue observes A's owner hold before A can persist its refusal.
	// If that freed A's slot, B could start and lifting A's hold before its
	// reconcile would then let both run under max-active=1.
	s := f.session(t)
	s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
	if err := f.c.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	f.r = f.fresh()
	f.r.TaskCleanupMaxActive = 1
	if f.r.taskCleanupAdmittable(ctx, other, *f.clock, true) {
		t.Fatal("an owner hold without a recorded refusal freed an active slot")
	}
	f.run(t, 1) // persist owner_hold, making inactivity durable
	if f.expiry(t).Refusal != expiryOwnerHold {
		t.Fatal("the hold did not persist its refusal")
	}
	if !f.r.taskCleanupAdmittable(ctx, other, *f.clock, true) {
		t.Fatal("the recorded refusal did not free its slot")
	}
	f.kept(t)
}

func TestTaskCleanupLateHoldAdoptionCannotReadmitRefusal(t *testing.T) {
	for _, state := range []string{"enabled", "disabled"} {
		for _, reason := range []string{expiryOwnerHold, expiryAttemptStuck, expiryRescueRefused} {
			t.Run(state+"/"+reason, func(t *testing.T) {
				f := newTaskFixture(t, nil)
				f.run(t, 5) // hold exists but its UID is not yet adopted
				if f.named(t) == nil || f.expiry(t).HoldPodUID != "" {
					t.Fatal("setup did not leave a hold awaiting adoption")
				}
				// An earlier Create may complete after an ambiguous response and
				// a refusal write. Its intent still matches, but adoption is not
				// permission to resume the inactive attempt.
				f.updateStatus(t, func(st *v1alpha1.AgentSessionStatus) {
					at := recordTime(*f.clock)
					st.PrivateHistory.Expiry.Refusal, st.PrivateHistory.Expiry.RefusalAt = reason, &at
				})
				f.r = f.fresh()
				f.r.TaskCleanupMaxActive = 0
				f.r.TaskSessionCleanup = state == "enabled"
				f.run(t, 1)
				if e := f.expiry(t); e.Refusal != reason || e.HoldPodUID == "" || taskActive(f.session(t)) {
					t.Fatalf("adoption bypassed readmission: %+v", e)
				}
				f.r.TaskSessionCleanup = true
				f.run(t, 2)
				if f.named(t) != nil || len(f.rescuer.ranIn()) != 0 {
					t.Fatal("refused late hold was kept or rescued")
				}
				f.kept(t)
			})
		}
	}
}

func TestTaskCleanupEveryNodeMustProveRelease(t *testing.T) {
	for _, phase := range []string{v1alpha1.HomeExpiryRescuing, v1alpha1.HomeExpiryDeleting} {
		for _, problem := range []string{"mounted", "replaced", "not ready", "stale lease"} {
			t.Run(phase+"/"+problem, func(t *testing.T) {
				var f *taskFixture
				if phase == v1alpha1.HomeExpiryDeleting {
					f = taskAtPhase(t, phase)
				} else {
					f = newTaskFixture(t, nil)
					f.run(t, 4)
				}
				n := taskReadyOtherNode(t, f)
				switch problem {
				case "mounted":
					n.Status.VolumesInUse = []corev1.UniqueVolumeName{homeVolumeUniqueName()}
					if err := f.c.Status().Update(context.Background(), n); err != nil {
						t.Fatal(err)
					}
				case "not ready":
					n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
					if err := f.c.Status().Update(context.Background(), n); err != nil {
						t.Fatal(err)
					}
				case "replaced":
					if err := f.c.Delete(context.Background(), n); err != nil {
						t.Fatal(err)
					}
					n.UID, n.ResourceVersion = "replacement-node", ""
					// Empty volumesInUse is not proof: the lease belongs to the old UID.
					if err := f.c.Create(context.Background(), n); err != nil {
						t.Fatal(err)
					}
				case "stale lease":
					f.tick(t, f.clock.Add(time.Minute)) // renews only the executor's lease
				}
				f.r = f.fresh() // no memory of worker-b
				f.run(t, 2)
				if p := f.named(t); p != nil {
					t.Fatalf("mounted HOME before release: %s", p.UID)
				}
				f.kept(t)
			})
		}
	}
}

func TestTaskCleanupSessionNamedPodBetweenGetAndListKeepsHome(t *testing.T) {
	f := taskAtPhase(t, v1alpha1.HomeExpiryDeleting)
	v, err := f.r.readExpiryView(context.Background(), f.session(t))
	if err != nil {
		t.Fatal(err)
	}
	created := false
	f.r.APIReader = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		err := c.Get(ctx, key, obj, opts...)
		if _, ok := obj.(*corev1.Pod); ok && key.Name == f.s.Name && apierrors.IsNotFound(err) && !created {
			created = true
			p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: f.s.Name, Namespace: f.s.Namespace, UID: "racing-pod"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: f.home.Name}}}}}}
			if err := c.Create(ctx, p); err != nil {
				t.Fatal(err)
			}
		}
		return err
	}})
	if _, _, err := f.r.removeExpiredHome(context.Background(), v, &observation{}); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("the interleaving was not exercised")
	}
	f.kept(t)
}

func TestTaskCleanupGraceChangeDoesNotShortenStoredDueAt(t *testing.T) {
	f := newTaskFixture(t, func(tf *terminalFixture) { tf.s.DeletionTimestamp = nil; tf.pod = nil })
	f.run(t, 2)
	before := f.expiry(t).DeepCopy()
	f.tick(t, f.observed.Add(24*time.Hour))
	f.r = f.fresh()
	f.r.TaskCleanupGrace = time.Hour
	f.run(t, 4)
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryWaiting || !e.DueAt.Equal(&before.DueAt) || f.named(t) != nil {
		t.Fatalf("changed grace advanced an existing deadline: %+v", e)
	}
	f.kept(t)
	// A reap still overrides the stored due time without rewriting it.
	if err := f.c.Delete(context.Background(), f.session(t)); err != nil {
		t.Fatal(err)
	}
	f.run(t, 2)
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryRescuing || !e.DueAt.Equal(&before.DueAt) {
		t.Fatalf("reap did not become due: %+v", e)
	}
}

func TestTaskCleanupAllowlistParksActivePhases(t *testing.T) {
	for _, phase := range []string{v1alpha1.HomeExpiryRescuing, v1alpha1.HomeExpiryArchived, v1alpha1.HomeExpiryDeleting} {
		t.Run(phase, func(t *testing.T) {
			f := taskAtPhase(t, phase)
			before, deletes, rescues := f.expiry(t).DeepCopy(), len(f.deletes), len(f.rescuer.ranIn())
			f.r = f.fresh()
			f.r.TaskCleanupSessions = []string{"another-session"}
			f.run(t, 5)
			f.kept(t)
			if !reflect.DeepEqual(before, f.expiry(t)) || len(f.deletes) != deletes || len(f.rescuer.ranIn()) != rescues {
				t.Fatal("excluded active record did work")
			}
		})
	}
}

func TestTaskCleanupInactiveRecordsNeedReadmission(t *testing.T) {
	for _, phase := range []string{v1alpha1.HomeExpiryRescuing, v1alpha1.HomeExpiryArchived, v1alpha1.HomeExpiryDeleting} {
		for _, cause := range []string{"refusal", "owner hold"} {
			t.Run(phase+"/"+cause, func(t *testing.T) {
				f := taskAtPhase(t, phase)
				if cause == "owner hold" {
					f.update(t, func(s *v1alpha1.AgentSession) {
						s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
					})
					f.run(t, 4)
					f.update(t, func(s *v1alpha1.AgentSession) { delete(s.Annotations, v1alpha1.AnnotationHomeExpiry) })
				} else {
					f.updateStatus(t, func(st *v1alpha1.AgentSessionStatus) {
						e := st.PrivateHistory.Expiry
						at := recordTime(*f.clock)
						e.Refusal, e.RefusalAt = expiryShelfUnavailable, &at
					})
				}
				f.tick(t, f.clock.Add(taskAttemptRetry+time.Minute))
				f.r = f.fresh()
				f.r.TaskCleanupMaxActive = 0
				f.run(t, 5)
				f.kept(t)
				if e := f.expiry(t); e.Refusal == "" || e.Phase != phase || taskActive(f.session(t)) {
					t.Fatalf("resumed without admission: %+v", e)
				}
				f.r.TaskCleanupMaxActive = 1
				f.run(t, 5)
				if !f.sessionGone(t) && f.expiry(t).Refusal != "" {
					t.Fatalf("did not re-enter once admitted: %+v", f.expiry(t))
				}
			})
		}
	}
}

func TestTaskCleanupFailedReportsDoNotResetAttemptAge(t *testing.T) {
	for _, failure := range []string{"fetch", "bundle"} {
		t.Run(failure, func(t *testing.T) {
			f := taskAtPhase(t, v1alpha1.HomeExpiryRescuing)
			start := *f.clock
			f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
				if failure == "bundle" {
					rep := verifiedRescue(p.Name, "20261111-1200", "20261111-1200")
					rep.OK = false
					rep.Repos[0].Bundle.Verified, rep.Repos[0].Bundle.Error = false, "write failed"
					return rep, nil
				}
				rep := cleanRescue(p.Name, "20261111-1200")
				rep.CleanAndPushed, rep.StrictHome = false, &protocol.StrictHomeCheck{}
				rep.Repos[0].Fetched, rep.Repos[0].FetchError = false, "fetch failed"
				return rep, nil
			})
			f.run(t, 2)
			f.tick(t, start.Add(16*time.Minute))
			f.run(t, 2)
			f.tick(t, start.Add(taskAttemptDeadline+time.Second))
			f.run(t, 3)
			if e := f.expiry(t); e.Refusal != expiryAttemptStuck || taskActive(f.session(t)) || f.named(t) != nil {
				t.Fatalf("failed reports renewed the attempt: %+v", e)
			}
			f.kept(t)
		})
	}
}

func TestTaskCleanupRetryWaitHonorsAttemptDeadline(t *testing.T) {
	f := taskAtPhase(t, v1alpha1.HomeExpiryRescuing)
	f.tick(t, f.clock.Add(taskAttemptDeadline-time.Minute))
	f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
		rep := cleanRescue(p.Name, "20261111-1200")
		rep.CleanAndPushed, rep.StrictHome = false, &protocol.StrictHomeCheck{}
		rep.Repos[0].Fetched, rep.Repos[0].FetchError = false, "fetch failed"
		return rep, nil
	})
	f.run(t, 1)
	v, err := f.r.readExpiryView(context.Background(), f.session(t))
	if err != nil {
		t.Fatal(err)
	}
	res, _, err := f.r.stepTaskCleanup(context.Background(), v, &observation{})
	if err != nil || res.RequeueAfter <= 0 || res.RequeueAfter > time.Minute {
		t.Fatalf("retry sleeps past the attempt deadline: %s, %v", res.RequeueAfter, err)
	}
	f.kept(t)
}

func TestTaskCleanupReleaseWaitCannotExtendAttempt(t *testing.T) {
	f := taskAtPhase(t, v1alpha1.HomeExpiryRescuing)
	if err := f.c.Delete(context.Background(), f.named(t)); err != nil {
		t.Fatal(err)
	}
	f.tick(t, f.clock.Add(taskAttemptDeadline-time.Minute))
	f.nodeInUse(t, homeVolumeUniqueName())
	f.run(t, 2) // replacement intent, then wait for release
	f.tick(t, f.clock.Add(time.Minute))
	f.r = f.fresh()
	f.run(t, 1)
	if e := f.expiry(t); e.Refusal != expiryVolumeNotReleased || taskActive(f.session(t)) {
		t.Fatalf("release wait extended the attempt across a restart: %+v", e)
	}
	f.kept(t)
}

type taskRescueFunc struct {
	*expiryRescuer
	run func(context.Context, *corev1.Pod) (protocol.RescueReport, error)
}

func (r taskRescueFunc) RescueStrictHome(ctx context.Context, p *corev1.Pod) (protocol.RescueReport, error) {
	return r.run(ctx, p)
}

func TestTaskCleanupRescueCannotOverrunAttempt(t *testing.T) {
	f := taskAtPhase(t, v1alpha1.HomeExpiryRescuing)
	f.tick(t, f.clock.Add(taskAttemptDeadline-time.Minute))
	called := false
	f.r.Rescuer = taskRescueFunc{expiryRescuer: f.rescuer, run: func(ctx context.Context, p *corev1.Pod) (protocol.RescueReport, error) {
		called = true
		deadline, bounded := ctx.Deadline()
		if !bounded || time.Until(deadline) > time.Minute {
			t.Error("rescue context is not bounded by the remaining attempt")
		}
		f.tick(t, f.clock.Add(time.Minute))
		rep := cleanRescue(p.Name, "20261111-1200")
		rep.StrictHome = &protocol.StrictHomeCheck{}
		return rep, nil
	}}
	f.run(t, 1)
	if e := f.expiry(t); !called || e.Refusal != expiryAttemptStuck || taskActive(f.session(t)) || f.session(t).Status.Rescue != nil {
		t.Fatalf("late rescue report counted after the attempt expired: %+v", e)
	}
	f.kept(t)
}

func TestTaskCleanupTerminatingHoldCannotEscapeAttemptDeadline(t *testing.T) {
	f := taskAtPhase(t, v1alpha1.HomeExpiryRescuing)
	p := f.named(t)
	p.Finalizers = []string{"test.invalid/keep-terminating"}
	if err := f.c.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	f.tick(t, f.clock.Add(taskAttemptDeadline+time.Second))
	f.run(t, 3)
	if e := f.expiry(t); e.Refusal != expiryAttemptStuck || taskActive(f.session(t)) {
		t.Fatalf("terminating hold still owns a slot: %+v", e)
	}
	f.kept(t)
	// Even after retry, no replacement may mount while teardown still holds the volume.
	p = f.named(t)
	p.Finalizers = nil
	if err := f.c.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	f.nodeInUse(t, homeVolumeUniqueName())
	f.tick(t, f.clock.Add(taskAttemptRetry+time.Minute))
	f.r = f.fresh()
	f.run(t, 5)
	if f.named(t) != nil {
		t.Fatal("replacement mounted before release")
	}
	f.kept(t)
}

func TestTaskCleanupLastVerifyErrorsKeepHome(t *testing.T) {
	for _, failure := range []error{errors.New("unable to upgrade exec connection"), context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			f := newTaskFixture(t, nil)
			id := f.s.Name + "/20261111-1200"
			f.verifier.set(id, protocol.RescueVerifyResult{OK: true, Manifest: "rescue/" + id + "/manifest.json", ManifestSHA256: testDigest, Bundles: 1, Bytes: 2048})
			f.toHold(t)
			f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
				return verifiedRescue(p.Name, "20261111-1200", "20261111-1200"), nil
			})
			f.run(t, 4)
			if f.expiry(t).Phase != v1alpha1.HomeExpiryDeleting {
				t.Fatal("did not reach the last verify")
			}
			before := len(f.verifier.idCalls())
			f.verifier.err = failure
			f.run(t, 1)
			if len(f.verifier.idCalls()) != before+1 || f.expiry(t).Refusal != expiryShelfUnavailable {
				t.Fatal("last verify failure not observed")
			}
			f.kept(t)
		})
	}
}

func TestTaskCleanupHeldBeforeExecutorRemovalNeedsReadmission(t *testing.T) {
	f := newTaskFixture(t, nil)
	f.run(t, 2) // Rescuing, with the ended executor still present
	f.update(t, func(s *v1alpha1.AgentSession) {
		s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
	})
	f.run(t, 2)
	f.update(t, func(s *v1alpha1.AgentSession) { delete(s.Annotations, v1alpha1.AnnotationHomeExpiry) })
	f.tick(t, f.clock.Add(homeExpiryRefusalRetry+time.Minute))
	f.r = f.fresh()
	f.r.TaskCleanupMaxActive = 0
	f.run(t, 2)
	if e := f.expiry(t); e.Refusal != expiryOwnerHold {
		t.Fatalf("held record resumed outside admission: %+v", e)
	}
	f.r.TaskCleanupMaxActive = 1
	f.run(t, 6)
	if p := f.named(t); p == nil || !isHoldPod(p) || f.expiry(t).Refusal != "" {
		t.Fatal("admitted record could not remove its ended executor")
	}
	f.kept(t)
}

func TestTaskCleanupArchivedTerminatingHoldHasBound(t *testing.T) {
	f := taskAtPhase(t, v1alpha1.HomeExpiryArchived)
	p := f.named(t)
	p.Finalizers = []string{"test.invalid/keep-terminating"}
	if err := f.c.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	f.run(t, 1) // request deletion, which the test finalizer holds
	f.tick(t, f.clock.Add(taskAttemptDeadline+time.Second))
	f.r = f.fresh()
	f.run(t, 3)
	if e := f.expiry(t); e.Refusal != expiryAttemptStuck || taskActive(f.session(t)) {
		t.Fatalf("terminating archived hold still owns a slot: %+v", e)
	}
	f.kept(t)
}

func TestTaskCleanupAttemptAgeSurvivesReplacementAndLeaderChange(t *testing.T) {
	f := taskAtPhase(t, v1alpha1.HomeExpiryRescuing)
	start := *f.clock
	f.tick(t, start.Add(20*time.Minute))
	p := f.named(t)
	p.Status.Phase = corev1.PodFailed
	if err := f.c.Status().Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	f.run(t, 1)
	f.r = f.fresh()
	f.run(t, 4)
	if next := f.named(t); next == nil || next.UID == p.UID {
		t.Fatal("replacement hold was not exercised")
	}
	f.tick(t, start.Add(taskAttemptDeadline+time.Second))
	f.r = f.fresh()
	f.run(t, 3)
	if e := f.expiry(t); e.Refusal != expiryAttemptStuck || taskActive(f.session(t)) {
		t.Fatalf("leader change or replacement restarted the attempt: %+v", e)
	}
	f.kept(t)
}

func TestTaskCleanupMaxActiveZeroLetsActiveRecordFinish(t *testing.T) {
	f := taskAtPhase(t, v1alpha1.HomeExpiryDeleting)
	f.r.TaskCleanupMaxActive = 0
	f.run(t, 4)
	if !f.sessionGone(t) || !f.claimGone(t) {
		t.Fatal("the admission pause stopped an active record")
	}
}

func TestTaskCleanupAllowlistBeforeFinishedSessionDelete(t *testing.T) {
	f := newTaskFixture(t, func(tf *terminalFixture) {
		tf.s.DeletionTimestamp = nil
		tf.pod = nil
	})
	f.tick(t, f.observed.Add(DefaultTaskCleanupGrace+time.Minute))
	f.toHold(t)
	f.run(t, 6) // Archived, Deleting, HOME gone, Expired; before Session delete
	if f.expiry(t).Phase != v1alpha1.HomeExpiryExpired || !f.claimGone(t) {
		t.Fatal("did not reach Expired before the Session delete")
	}
	f.r.TaskCleanupSessions = []string{"another-session"}
	v, err := f.r.readExpiryView(context.Background(), f.session(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.r.removeCleanedTaskSession(context.Background(), v, &observation{}); err != nil {
		t.Fatal(err)
	}
	f.run(t, 3)
	if f.sessionGone(t) || !f.session(t).DeletionTimestamp.IsZero() {
		t.Fatal("excluded finished Session was deleted")
	}
}

type taskVerifyFunc func(context.Context, string) (protocol.RescueVerifyResult, error)

func (fn taskVerifyFunc) Verify(ctx context.Context, id string) (protocol.RescueVerifyResult, error) {
	return fn(ctx, id)
}

func TestTaskCleanupAllowlistRecheckedAfterLastVerify(t *testing.T) {
	f := newTaskFixture(t, nil)
	id := f.s.Name + "/20261111-1200"
	f.verifier.set(id, protocol.RescueVerifyResult{OK: true, Manifest: "rescue/" + id + "/manifest.json", ManifestSHA256: testDigest, Bundles: 1, Bytes: 2048})
	f.toHold(t)
	f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
		return verifiedRescue(p.Name, "20261111-1200", "20261111-1200"), nil
	})
	f.run(t, 4)
	if f.expiry(t).Phase != v1alpha1.HomeExpiryDeleting {
		t.Fatal("did not reach Deleting")
	}
	f.r.RescueVerifier = taskVerifyFunc(func(ctx context.Context, id string) (protocol.RescueVerifyResult, error) {
		result, err := f.verifier.Verify(ctx, id)
		if id != "" {
			f.r.TaskCleanupSessions = []string{"another-session"}
		}
		return result, err
	})
	f.run(t, 1)
	f.kept(t)
}

// taskFinishedFixture is a finished task past its grace, before a hold pod.
func taskFinishedFixture(t *testing.T) *taskFixture {
	t.Helper()
	f := newTaskFixture(t, func(tf *terminalFixture) {
		tf.s.DeletionTimestamp = nil
		tf.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		tf.pod = nil
	})
	f.tick(t, f.observed.Add(DefaultTaskCleanupGrace+time.Minute))
	return f
}

// taskRunUntil reconciles until done, and fails if that does not happen.
func taskRunUntil(t *testing.T, f *taskFixture, done func() bool) {
	t.Helper()
	for range 24 {
		if done() {
			return
		}
		f.run(t, 1)
	}
	t.Fatalf("not reached: record %+v sessionGone=%v claimGone=%v", f.expiry(t), f.sessionGone(t), f.claimGone(t))
}

func taskSetVerified(f *taskFixture) (id, manifest string) {
	id = f.s.Name + "/20261111-1200"
	manifest = "rescue/" + id + "/manifest.json"
	f.verifier.set(id, protocol.RescueVerifyResult{OK: true, Manifest: manifest, ManifestSHA256: testDigest, Bundles: 1, Bytes: 2048})
	return id, manifest
}

// TestTaskCleanupKeptWorkVerifiedWhenTheClaimIsGone is the coordinator's
// follow-up on PR #1036: a task record whose HOME claim is already gone
// verifies its kept work before Expired, and again before the Session is
// deleted. A refusal leaves the Session. A clean and pushed record uses the
// check keptWorkVerified already makes.
func TestTaskCleanupKeptWorkVerifiedWhenTheClaimIsGone(t *testing.T) {
	t.Run("claim already gone and the bundle missing", func(t *testing.T) {
		f := newTaskFixture(t, nil)
		id, _ := taskSetVerified(f)
		f.toHold(t)
		f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
			return verifiedRescue(p.Name, "20261111-1200", "20261111-1200"), nil
		})
		taskRunUntil(t, f, func() bool {
			e := f.expiry(t)
			return e != nil && e.Phase == v1alpha1.HomeExpiryDeleting
		})
		if f.claimGone(t) {
			t.Fatal("the claim was already gone at Deleting")
		}
		// The delete lands, and the shelf copy is gone before the next
		// reconcile records Expired.
		f.deleteErr = func(obj client.Object) error {
			if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
				f.verifier.mu.Lock()
				delete(f.verifier.answers, id)
				f.verifier.mu.Unlock()
			}
			return nil
		}
		f.run(t, 4)
		e := f.expiry(t)
		if e.Phase == v1alpha1.HomeExpiryExpired || e.HomeDeletedAt != nil || e.Refusal != expiryBundleMissing {
			t.Fatalf("record %+v; want Deleting, %s, and not Expired", e, expiryBundleMissing)
		}
		if !f.claimGone(t) {
			t.Fatal("the claim is not gone; this did not exercise the already-gone path")
		}
		if f.sessionGone(t) {
			t.Fatal("the Session is gone")
		}
	})

	t.Run("expired bundle tampered before the session delete", func(t *testing.T) {
		f := taskFinishedFixture(t)
		id, manifest := taskSetVerified(f)
		f.toHold(t)
		f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
			return verifiedRescue(p.Name, "20261111-1200", "20261111-1200"), nil
		})
		taskRunUntil(t, f, func() bool {
			e := f.expiry(t)
			return e != nil && e.Phase == v1alpha1.HomeExpiryExpired
		})
		if f.sessionGone(t) || !f.session(t).DeletionTimestamp.IsZero() {
			t.Fatal("the Session was already deleted at Expired")
		}
		f.verifier.set(id, protocol.RescueVerifyResult{OK: true, Manifest: manifest, ManifestSHA256: strings.Repeat("e", 64), Bundles: 1, Bytes: 2048})
		taskAssertExpiredKept(t, f, "not the recorded")
	})

	t.Run("clean and pushed rescue no longer names the hold", func(t *testing.T) {
		f := taskFinishedFixture(t)
		f.toHold(t)
		f.passRescue()
		taskRunUntil(t, f, func() bool {
			e := f.expiry(t)
			return e != nil && e.Phase == v1alpha1.HomeExpiryExpired
		})
		if e := f.expiry(t); e.Bundle == nil || e.Bundle.Result != bundleCleanAndPushed {
			t.Fatalf("record %+v; want a clean and pushed bundle", e)
		}
		f.updateStatus(t, func(st *v1alpha1.AgentSessionStatus) { st.Rescue.Superseded = true })
		taskAssertExpiredKept(t, f, "status.rescue no longer names the clean and pushed rescue")
	})

	t.Run("happy path still removes the session", func(t *testing.T) {
		f := taskFinishedFixture(t)
		taskSetVerified(f)
		f.toHold(t)
		f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
			return verifiedRescue(p.Name, "20261111-1200", "20261111-1200"), nil
		})
		taskRunUntil(t, f, func() bool { return f.sessionGone(t) })
		if !f.claimGone(t) {
			t.Fatal("the HOME claim stays")
		}
	})

	t.Run("clean and pushed still removes the session", func(t *testing.T) {
		f := taskFinishedFixture(t)
		f.toHold(t)
		f.passRescue()
		taskRunUntil(t, f, func() bool { return f.sessionGone(t) })
		if calls := f.verifier.idCalls(); len(calls) != 0 {
			t.Fatalf("a clean and pushed rescue was verified on the shelf: %v", calls)
		}
		if !f.claimGone(t) {
			t.Fatal("the HOME claim stays")
		}
	})
}

// TestTaskCleanupEndedAttemptDropsGradedHold: graded keeps one entry per hold
// pod, and that entry goes where the attempt entry goes.
func TestTaskCleanupEndedAttemptDropsGradedHold(t *testing.T) {
	f := newTaskFixture(t, nil)
	hold := k8stypes.UID("hold-graded")
	f.r.taskAttemptSince(hold, *f.clock)
	f.r.taskMarkGraded(hold, "20261111-1200")
	f.r.taskEndAttempt(f.s.UID, hold)
	if taskGradedHas(f, hold) {
		t.Fatal("graded still has the hold after the attempt ended")
	}

	ended := k8stypes.UID("hold-ended")
	f.r.taskAttemptSince(ended, *f.clock)
	f.r.taskMarkGraded(ended, "20261111-1201")
	if n := f.r.taskHoldEnded(f.s.UID, ended); n != 0 {
		t.Fatalf("a graded hold counted as ungraded: %d", n)
	}
	if taskGradedHas(f, ended) {
		t.Fatal("graded still has a hold whose attempt entry was removed")
	}

	old, fresh := k8stypes.UID("hold-old"), k8stypes.UID("hold-fresh")
	st := &f.r.taskCleanup
	st.mu.Lock()
	st.attempt[old] = f.clock.Add(-taskMemoryKeep - time.Second)
	st.graded[old] = "20261111-1202"
	st.attempt[fresh] = *f.clock
	st.graded[fresh] = "20261111-1203"
	st.prune(*f.clock)
	_, oldLeft := st.graded[old]
	_, freshLeft := st.graded[fresh]
	st.mu.Unlock()
	if oldLeft {
		t.Fatal("prune left a graded hold whose attempt entry aged out")
	}
	if !freshLeft {
		t.Fatal("prune dropped a graded hold whose attempt is still kept")
	}
}

// taskAssertExpiredKept checks a kept-work refusal on an Expired record: the
// record is unchanged, the Session stays, the note says why, the retry is 15
// minutes, and a second reconcile does not emit a second warning.
func taskAssertExpiredKept(t *testing.T, f *taskFixture, why string) {
	t.Helper()
	before, err := json.Marshal(f.expiry(t))
	if err != nil {
		t.Fatal(err)
	}
	rec := events.NewFakeRecorder(4)
	f.r.Recorder = rec
	res, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)})
	if err != nil {
		t.Fatal(err)
	}
	if f.sessionGone(t) || !f.session(t).DeletionTimestamp.IsZero() {
		t.Fatal("the Session was removed")
	}
	after, err := json.Marshal(f.expiry(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the record changed\nbefore %s\nafter  %s", before, after)
	}
	c := f.condition(t, ConditionHomeExpiry)
	if c == nil || !strings.Contains(c.Message, "the session is not removed") || !strings.Contains(c.Message, why) {
		t.Fatalf("HomeExpiry %+v; want a note that the session is not removed: %s", c, why)
	}
	if res.RequeueAfter != homeExpiryRefusalRetry {
		t.Fatalf("retry %s; want %s", res.RequeueAfter, homeExpiryRefusalRetry)
	}
	if got := eventReasons(rec); len(got) != 1 || got[0] != "TaskCleanupRefused" {
		t.Fatalf("events %v; want one TaskCleanupRefused", got)
	}
	if _, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
		t.Fatal(err)
	}
	if got := eventReasons(rec); len(got) != 0 {
		t.Fatalf("the second reconcile emitted %v", got)
	}
	again, err := json.Marshal(f.expiry(t))
	if err != nil || !bytes.Equal(before, again) {
		t.Fatalf("the second reconcile changed the record: %s", again)
	}
}

func taskGradedHas(f *taskFixture, hold k8stypes.UID) bool {
	st := &f.r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.graded[hold]
	return ok
}
