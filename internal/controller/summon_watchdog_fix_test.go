package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// O1: only missing-heartbeat rows wait out the leader's first ten minutes.
// Repeated passes do not move that start; a new Reconciler has its own start.
func TestSummonWatchdogWaitsForLeaderHeartbeats(t *testing.T) {
	for _, row := range []string{"heartbeat-lost", "never-started-task", "never-started-remote"} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restart=%v", row, restart), func(t *testing.T) {
				var w *summonWorld
				var s *v1alpha1.AgentSession
				if row == "never-started-task" {
					w, s, _ = launchSummoned(t)
				} else {
					w, s = remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-restart")
				}
				t0 := summonT0.Add(20 * time.Minute)
				if row == "heartbeat-lost" {
					w.now = t0.Add(-11 * time.Minute)
					w.heartbeat(s.Name, protocol.AgentIdle)
				}
				w.now = t0
				w.r = w.leader(nil) // the process has just become leader
				w.pass()
				w.wantOpen("first pass after the outage", s.Name)
				w.now = t0.Add(9 * time.Minute)
				w.pass()
				w.wantOpen("nine minutes since the first pass", s.Name)
				if restart {
					w.r = w.leader(nil)
					w.pass()
					w.wantOpen("replacement leader's first pass", s.Name)
					w.now = t0.Add(10 * time.Minute)
					w.pass()
					w.wantOpen("old leader's grace elapsed, new leader's has not", s.Name)
					t0 = t0.Add(9 * time.Minute)
					w.now = t0.Add(9 * time.Minute)
					w.pass()
					w.wantOpen("replacement leader at nine minutes", s.Name)
				}
				w.now = t0.Add(10 * time.Minute)
				w.pass()
				reason, state := summonoutcome.ReasonNeverStarted, v1alpha1.OutcomeFailed
				switch row {
				case "heartbeat-lost":
					reason = summonoutcome.ReasonHeartbeatLost
				case "never-started-task":
					state = v1alpha1.OutcomeEscalated
				}
				w.wantClosed(s.Name, reason, state)
			})
		}
	}
}

func TestSummonRecordedReapDeletePreconditions(t *testing.T) {
	for _, failure := range []string{"none", "conflict", "not-found", "other"} {
		t.Run(failure, func(t *testing.T) {
			s := summonedFor("rem-recorded-reap", callerA, v1alpha1.LaneRemediation, 0)
			s.Annotations = map[string]string{v1alpha1.AnnotationReapedBy: "session/worker"}
			w := newSummonWorld(t, s)
			if _, err := summonoutcome.Write(context.Background(), w.base, w.get(s.Name), summonoutcome.Decision{
				State: v1alpha1.OutcomeEscalated, Source: summonoutcome.Unreported, Reason: summonoutcome.ReasonSuspendedUnreported, Released: true,
			}, w.now); err != nil {
				t.Fatal(err)
			}
			before := w.get(s.Name)
			deletes := 0
			refused := errors.New("delete unavailable")
			w.r = w.leader(&interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
				deletes++
				if _, ok := o.(*v1alpha1.AgentSession); !ok {
					t.Fatalf("unexpected delete %T", o)
				}
				p := (&client.DeleteOptions{}).ApplyOptions(opts).Preconditions
				if p == nil || p.UID == nil || *p.UID != before.UID || p.ResourceVersion == nil || *p.ResourceVersion != before.ResourceVersion {
					t.Fatalf("missing close-bound preconditions: %+v", p)
				}
				switch failure {
				case "conflict":
					return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, s.Name, refused)
				case "not-found":
					return apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, s.Name)
				case "other":
					return refused
				default:
					return nil // intercept removal, as in the API-to-controller proof
				}
			}})
			_, err := w.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)})
			if failure == "other" && !errors.Is(err, refused) || failure != "other" && err != nil {
				t.Fatalf("delete error handling: %v", err)
			}
			if deletes != 1 || w.get(s.Name).ResourceVersion != before.ResourceVersion || len(w.pods)+len(w.claims) != 0 {
				t.Fatalf("reconcile did not return after its one delete: deletes=%d pods=%v claims=%v", deletes, w.pods, w.claims)
			}
		})
	}
}

// L1: exercise both complete entry points with the stop/reclaim flags on.
// A cached open snapshot must not hide a fresh report-released outcome.
func TestSummonFullReconcileSafetyMatrix(t *testing.T) {
	for _, kind := range []string{"reported-task", "reported-remote", "open-task", "open-remote", "ordinary-task", "ordinary-remote", "ordinary-reaped-task", "ordinary-reaped-remote"} {
		for _, stale := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stale=%v", kind, stale), func(t *testing.T) {
				ctx := context.Background()
				var w *summonWorld
				var s *v1alpha1.AgentSession
				isTask := strings.HasSuffix(kind, "-task")
				ordinary := strings.HasPrefix(kind, "ordinary-")
				if isTask {
					w, s, _ = launchSummoned(t)
					w.schedule(s.Name, nil)
				} else {
					w, s = remoteAdmitted(t, v1alpha1.LaneEscalation, "esc-safe")
				}
				w.heartbeat(s.Name, protocol.AgentBusy)
				w.reconcile(s.Name)
				live := w.get(s.Name)
				if ordinary {
					// Turn the initialized fixture into the equivalent ordinary
					// session. Its existing private task protection stays on.
					live.Spec.Caller, live.Spec.Lane, live.Spec.Parent, live.Spec.Summon = "", "", "", nil
					if strings.HasPrefix(kind, "ordinary-reaped-") {
						if live.Annotations == nil {
							live.Annotations = map[string]string{}
						}
						live.Annotations[v1alpha1.AnnotationReapedBy] = "human/dev-env-system/dev-env-human"
					}
					if err := w.base.Update(ctx, live); err != nil {
						t.Fatal(err)
					}
					live.Status.Summon, live.Status.Outcome = nil, nil
					if err := w.base.Status().Update(ctx, live); err != nil {
						t.Fatal(err)
					}
					putTestGrants(t, w.base, []testSummonGrant{}...)
				}
				cached := w.get(s.Name)
				if !ordinary && (stale || kind == "reported-task" || kind == "reported-remote") {
					if _, err := summonoutcome.Write(ctx, w.base, w.get(s.Name), summonoutcome.Decision{
						State: v1alpha1.OutcomeDone, Source: summonoutcome.Report, Reason: "done", Released: true,
					}, w.now); err != nil {
						t.Fatal(err)
					}
				}
				before := w.get(s.Name)
				var podBefore corev1.Pod
				if !w.exists(&podBefore, s.Name) {
					t.Fatal("the safety fixture has no pod")
				}
				if podBefore.Spec.ActiveDeadlineSeconds != nil {
					t.Fatal("fixture already has an executor stop")
				}
				mutations := []string{}
				checkWrite := func(o client.Object) {
					switch o := o.(type) {
					case *corev1.Pod:
						mutations = append(mutations, "pod mutation: "+o.Name)
					case *v1alpha1.AgentSession:
						if o.Name == s.Name && o.Spec.OperatingMode != before.Spec.OperatingMode {
							mutations = append(mutations, "operatingMode changed")
						}
					}
				}
				w.r = w.leader(&interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
						if dst, ok := o.(*v1alpha1.AgentSession); ok && stale && key == client.ObjectKeyFromObject(cached) {
							*dst = *cached.DeepCopy()
							return nil
						}
						return c.Get(ctx, key, o, opts...)
					},
					Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
						checkWrite(o)
						return c.Patch(ctx, o, p, opts...)
					},
					Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
						checkWrite(o)
						return c.Update(ctx, o, opts...)
					},
					Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
						mutations = append(mutations, fmt.Sprintf("delete %T %s", o, o.GetName()))
						return c.Delete(ctx, o, opts...)
					},
				})
				if _, err := w.r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
					t.Fatal(err)
				}
				w.pass()
				if len(mutations) != 0 {
					t.Fatalf("unsafe lifecycle mutations: %v", mutations)
				}
				var podAfter corev1.Pod
				if !w.exists(&podAfter, s.Name) || !apiequality.Semantic.DeepEqual(podBefore.Spec, podAfter.Spec) ||
					w.get(s.Name).Spec.OperatingMode != before.Spec.OperatingMode {
					t.Fatal("the session mode or its pod changed")
				}
				if isTask && !w.exists(&corev1.PersistentVolumeClaim{}, HomeClaimName(s.Name)) {
					t.Fatal("the task's volume disappeared")
				}
				if ordinary && !apiequality.Semantic.DeepEqual(before, w.get(s.Name)) {
					t.Fatal("the ordinary session changed")
				}
				if !ordinary && (stale || kind == "reported-task" || kind == "reported-remote") {
					after := w.get(s.Name)
					if after.Status.Outcome.State != v1alpha1.OutcomeDone || !after.Status.Summon.Closed.Released {
						t.Fatal("the reported outcome or release changed")
					}
				}
			})
		}
	}
}

// A pre-existing durable deadline does not acquire the heartbeat grace.
func TestSummonDeadlineDoesNotWaitForLeaderHeartbeats(t *testing.T) {
	w, s, _ := launchSummoned(t)
	w.heartbeat(s.Name, protocol.AgentBusy)
	w.now = summonT0.Add(65 * time.Minute)
	w.r = w.leader(nil)
	w.pass()
	w.wantClosed(s.Name, summonoutcome.ReasonDeadline, v1alpha1.OutcomeEscalated)
}
