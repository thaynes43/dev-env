package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// Spell out the on-disk contract so these regressions also run on the
// pre-fix head, which has no grant codec yet.
const testGrantsKey = "_admission"

type testSummonGrant struct {
	Name   string    `json:"name"`
	UID    string    `json:"uid"`
	Lane   string    `json:"lane"`
	Caller string    `json:"caller"`
	Pool   string    `json:"pool"`
	At     time.Time `json:"at"`
}

func readTestGrants(t *testing.T, c client.Reader) []testSummonGrant {
	t.Helper()
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: summonledger.Namespace, Name: summonledger.Name}, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data[testGrantsKey] == "" {
		return nil
	}
	var doc struct {
		Schema string            `json:"schema"`
		Grants []testSummonGrant `json:"grants"`
	}
	if err := json.Unmarshal([]byte(cm.Data[testGrantsKey]), &doc); err != nil || doc.Schema != "dev-env.summon-grants.v1" {
		t.Fatalf("invalid grants: %v", err)
	}
	return doc.Grants
}

func putTestGrants(t *testing.T, c client.Client, grants ...testSummonGrant) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"schema": "dev-env.summon-grants.v1", "grants": grants})
	if err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: summonledger.Namespace, Name: summonledger.Name}, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[testGrantsKey] = string(b)
	if err := c.Update(context.Background(), &cm); err != nil {
		t.Fatal(err)
	}
}

func grantFor(s *v1alpha1.AgentSession, at time.Time) testSummonGrant {
	pool := "concurrent"
	if s.Spec.Lane == v1alpha1.LaneEscalation {
		pool = "escalations"
	}
	return testSummonGrant{Name: s.Name, UID: string(s.UID), Lane: string(s.Spec.Lane), Caller: s.Spec.Caller, Pool: pool, At: at}
}

func admissionPair(t *testing.T, collision string) (*summonWorld, *v1alpha1.AgentSession, *v1alpha1.AgentSession) {
	t.Helper()
	a := summonedFor("older", callerA, v1alpha1.LaneRemediation, 2*time.Minute)
	b := summonedFor("younger", callerA, v1alpha1.LaneRemediation, time.Minute)
	cap := int32(2)
	if collision == "pool" {
		b.Spec.Lane = v1alpha1.LaneCuration
		cap = 1
	}
	w := newSummonWorld(t, summonPolicyObject(callerA, cap, ptr.To[int32](1)))
	return w, a, b
}

// A1: both the shared reservation and the subsequent Session PATCH can
// commit before their reply is lost. A fresh leader must recover that exact
// reservation, with its original time, before considering another grant.
func TestSummonAdmissionLostResponsesKeepReservations(t *testing.T) {
	for _, collision := range []string{"lane", "pool"} {
		for _, lost := range []string{"ledger", "status"} {
			t.Run(collision+"/"+lost, func(t *testing.T) {
				w, a, b := admissionPair(t, collision)
				w.queue(a, b)
				fired := false
				lostReply := errors.New("fixture: write committed, response lost")
				r := w.leader(&interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						err := c.Update(ctx, obj, opts...)
						if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == summonledger.Name && lost == "ledger" && !fired && err == nil {
							fired = true
							return lostReply
						}
						return err
					},
					SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						err := c.SubResource(sub).Patch(ctx, obj, patch, opts...)
						if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Name == a.Name && s.Status.Summon != nil && s.Status.Summon.AdmittedAt != nil && lost == "status" && !fired && err == nil {
							fired = true
							return lostReply
						}
						return err
					},
				})
				if err := r.SummonAdmissionPass(context.Background()); err == nil || !fired {
					t.Fatalf("lost %s response was not exercised: fired=%v err=%v", lost, fired, err)
				}
				w.wantAdmitted("lost response", map[string]bool{a.Name: lost == "status", b.Name: false})
				if grants := readTestGrants(t, w.base); len(grants) != 1 || grants[0].UID != string(a.UID) {
					t.Fatalf("reservation lost: %+v", grants)
				}
				w.now = summonT0.Add(2 * time.Minute)
				w.r = w.leader(nil)
				w.pass()
				w.wantAdmitted("new leader", map[string]bool{a.Name: true, b.Name: false})
				if at := w.get(a.Name).Status.Summon.AdmittedAt; !at.Time.Equal(summonT0) {
					t.Fatalf("recovery extended the start deadline: %v", at)
				}
				w.pass()
				w.wantAdmitted("next pass", map[string]bool{a.Name: true, b.Name: false})
			})
		}
	}
}

// A2's exact request schedule, without sleeps or goroutines. The old writer
// parks Y's grant request; X becomes eligible; the new leader admits X; at
// the new leader's hold-Y request, the old write completes. The shared CAS
// must conflict. On the old implementation this parks the Session PATCH
// instead: both different-Session admission writes can then succeed.
func TestSummonAdmissionHandoverCAS(t *testing.T) {
	for _, collision := range []string{"lane", "pool"} {
		t.Run(collision, func(t *testing.T) {
			w, x, y := admissionPair(t, collision)
			w.charge(x, x.Name)
			w.add(x) // X has no private latch in the old leader's list.
			w.queue(y)
			var pending func() error
			var pendingErr error
			parked, completed := false, false
			handover := func(request func() error) error {
				parked, pending = true, request
				w.reconcile(x.Name) // Reconciler makes X eligible after the old list.
				fresh := w.leader(&interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if obj.GetName() == y.Name && !completed {
						completed = true
						pendingErr = pending()
					}
					return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
				}})
				if err := fresh.SummonAdmissionPass(context.Background()); err != nil {
					t.Fatal(err)
				}
				if !completed {
					t.Fatal("new leader never reached the hold-Y request barrier")
				}
				return pendingErr
			}
			old := w.leader(&interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == summonledger.Name && !parked {
						return handover(func() error { return c.Update(ctx, obj, opts...) })
					}
					return c.Update(ctx, obj, opts...)
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Name == y.Name && s.Status.Summon != nil && s.Status.Summon.AdmittedAt != nil && !parked {
						return handover(func() error { return c.SubResource(sub).Patch(ctx, obj, patch, opts...) })
					}
					return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
				},
			})
			if err := old.SummonAdmissionPass(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !parked || !completed {
				t.Fatal("handover schedule not exercised")
			}
			w.wantAdmitted("overlapping leaders", map[string]bool{x.Name: true, y.Name: false})
			if grants := readTestGrants(t, w.base); len(grants) != 1 || grants[0].UID != string(x.UID) {
				t.Fatalf("overlapping grants: %+v", grants)
			}
			w.r = w.leader(nil)
			w.pass()
			w.wantAdmitted("restart after overlap", map[string]bool{x.Name: true, y.Name: false})
		})
	}
}

func TestSummonGrantSurvivesLeaderBeforeStatus(t *testing.T) {
	for _, collision := range []string{"lane", "pool"} {
		t.Run(collision, func(t *testing.T) {
			w, x, y := admissionPair(t, collision)
			w.charge(x, x.Name)
			w.add(x)
			w.queue(y)
			parked := false
			old := w.leader(&interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Name == y.Name && s.Status.Summon != nil && s.Status.Summon.AdmittedAt != nil && !parked {
					parked = true
					w.reconcile(x.Name)
					w.r = w.leader(nil)
					w.pass()
				}
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			}})
			if err := old.SummonAdmissionPass(context.Background()); err != nil || !parked {
				t.Fatalf("status barrier: parked=%v err=%v", parked, err)
			}
			w.wantAdmitted("committed grant recovered", map[string]bool{x.Name: false, y.Name: true})
			w.pass()
			w.wantAdmitted("subsequent pass", map[string]bool{x.Name: false, y.Name: true})
		})
	}
}

func TestSummonGrantRecoveryAndCleanup(t *testing.T) {
	w, a, b := admissionPair(t, "lane")
	w.queue(a, b)
	grant := grantFor(a, summonT0.Add(-time.Minute))
	putTestGrants(t, w.base, grant)
	// A granted session is completed even if policy/quota now deny it. It
	// already owns capacity; redeciding could hand that capacity to B.
	w.r.SummonQuota = func(*v1alpha1.AgentSession) bool { return false }
	w.pass()
	w.wantAdmitted("complete existing grant", map[string]bool{a.Name: true, b.Name: false})
	if !w.get(a.Name).Status.Summon.AdmittedAt.Time.Equal(grant.At) {
		t.Fatal("completion changed the grant time")
	}
	w.close(a.Name, summonoutcome.Report)
	w.r.SummonQuota = nil
	w.pass()
	if grants := readTestGrants(t, w.base); len(grants) != 1 || grants[0].UID != string(b.UID) {
		t.Fatalf("released grant survived: %+v", grants)
	}
	// A deleted Session, including a same-name replacement with a different
	// UID, cannot keep its grant. Keep a live summoned record for the pass.
	putTestGrants(t, w.base, grantFor(b, summonT0), testSummonGrant{Name: a.Name, UID: "old-uid", Lane: string(a.Spec.Lane), Caller: a.Spec.Caller, Pool: "concurrent", At: summonT0},
		testSummonGrant{Name: "gone", UID: "gone-uid", Lane: "curation", Caller: callerA, Pool: "concurrent", At: summonT0})
	w.pass()
	if grants := readTestGrants(t, w.base); len(grants) != 1 || grants[0].UID != string(b.UID) {
		t.Fatalf("gone/replaced grants survived: %+v", grants)
	}
}

// R1 uses the pinned agent's pre-provider states: launch.json exists,
// run-agent has not spawned yet, and the heartbeat can be Busy/Interrupted.
// This is a controller reservation proof; it does not run a provider.
func TestSummonPendingFirstSpawnHoldsUntilPodGone(t *testing.T) {
	for _, state := range []string{protocol.AgentBusy, protocol.AgentInterrupted, protocol.AgentIdle, protocol.AgentWaiting, protocol.AgentExited} {
		t.Run(state, func(t *testing.T) {
			w, a, b := admissionPair(t, "lane")
			// OP779: the 20 minutes below would pass b's 20m maxHold, and the
			// hold bound would close it hold-expired; this proof is about a's
			// release, so b waits without one.
			b.Spec.Summon.MaxHold = "24h"
			w.queue(a, b)
			w.pass()
			w.reconcile(a.Name)
			s := w.get(a.Name)
			s.Status.Agent = &v1alpha1.AgentStatus{Status: state}
			if state == protocol.AgentBusy || state == protocol.AgentInterrupted {
				// A deterministic pause after launch.json, before any provider
				// command. Use agentd's real status reader (also in v2.105.0),
				// not an assumption that Interrupted proves a completed spawn.
				dir := t.TempDir()
				for name, value := range map[string]any{
					"boot.json":   map[string]string{"bootId": "first", "boot": protocol.BootReady},
					"launch.json": agentd.Launch{Session: a.Name, BootID: "first", CreatedAt: summonT0},
				} {
					data, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				now := summonT0.Add(30 * time.Second)
				if state == protocol.AgentInterrupted {
					now = summonT0.Add(2 * time.Minute)
				}
				heartbeat := agentd.CollectStatus(context.Background(), summonNoProviderRunner{t}, agentd.Settings{StateDir: dir, Home: dir, TmuxBin: "tmux"},
					protocol.Session{Name: a.Name, Agent: protocol.AgentClaude, Mode: protocol.ModeTask, PrivateHistoryRetained: true}, now)
				if heartbeat.Agent.State != state || heartbeat.Agent.Task != nil {
					t.Fatalf("pre-spawn heartbeat=%s result=%v", heartbeat.Agent.State, heartbeat.Agent.Task != nil)
				}
				s.Status.Agent.Status = heartbeat.Agent.State
			}
			if err := w.base.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if state == protocol.AgentInterrupted {
				if _, err := w.r.reconcileSummonOutcome(context.Background(), w.get(a.Name)); err != nil {
					t.Fatal(err)
				}
			} else {
				w.close(a.Name, summonoutcome.Unreported)
			}
			w.now = summonT0.Add(20 * time.Minute) // elapsed time cannot cancel a pending first spawn
			changed, err := w.r.reconcileSummonRelease(context.Background(), w.get(a.Name))
			if err != nil || changed || w.get(a.Name).Status.Summon.Closed.Released {
				t.Fatalf("heartbeat without result released the pending first spawn: changed=%v err=%v", changed, err)
			}
			w.pass()
			w.wantAdmitted("while old first spawn can resume", map[string]bool{b.Name: false})
			var pod corev1.Pod
			if !w.exists(&pod, a.Name) || !podRetainsPrivateHistory(&pod) {
				t.Fatal("missing retained executor")
			}
			if err := w.base.Delete(context.Background(), &pod); err != nil {
				t.Fatal(err)
			}
			if changed, err := w.r.reconcileSummonRelease(context.Background(), w.get(a.Name)); err != nil || !changed {
				t.Fatalf("gone recorded executor did not release: %v %v", changed, err)
			}
			w.pass()
			w.wantAdmitted("old pod gone", map[string]bool{b.Name: true})
		})
	}
}

type summonNoProviderRunner struct{ t *testing.T }

func (r summonNoProviderRunner) Run(_ context.Context, cmd agentd.Cmd) (agentd.Result, error) {
	if cmd.Name == "tmux" && len(cmd.Args) > 0 && cmd.Args[0] == "list-clients" {
		return agentd.Result{}, nil // read-only heartbeat observation, no attached clients
	}
	r.t.Fatal("pre-spawn status fixture attempted a command other than listing clients")
	return agentd.Result{}, errors.New("no commands permitted")
}

func (r summonNoProviderRunner) LookPath(string) (string, error) {
	r.t.Fatal("pre-spawn status fixture attempted to find a command")
	return "", errors.New("no commands permitted")
}

// R2 parks the actual first Create request after the executor latch, then
// reconstructs the operator and closes it while the uncached Pod GET is
// NotFound. Completing the old request must not overlap a new reservation.
func TestSummonDelayedFirstCreateHoldsAcrossRestartAndClose(t *testing.T) {
	w, a, b := admissionPair(t, "lane")
	w.queue(a, b)
	w.pass()
	parked := false
	old := w.leader(&interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*corev1.Pod); !ok || obj.GetName() != a.Name || parked {
			return c.Create(ctx, obj, opts...)
		}
		parked = true
		s := w.get(a.Name)
		if !s.Status.PrivateHistory.ExecutorAttempted || s.Status.PrivateHistory.LastExecutor != nil {
			t.Fatal("first create was not parked after the latch and before its result")
		}
		w.r = w.leader(nil)
		w.close(a.Name, summonoutcome.Unreported)
		if changed, err := w.r.reconcileSummonRelease(ctx, w.get(a.Name)); err != nil || changed {
			t.Fatalf("absent but pending first create released: %v %v", changed, err)
		}
		w.pass()
		w.wantAdmitted("old request still pending", map[string]bool{b.Name: false})
		if err := c.Create(ctx, obj, opts...); err != nil {
			return err
		}
		w.now = summonT0.Add(17 * time.Minute)
		if changed, err := w.r.reconcileSummonRelease(ctx, w.get(a.Name)); err != nil || changed {
			t.Fatalf("late first pod present released after deadline: %v %v", changed, err)
		}
		// The reply is lost too, keeping lastExecutor absent after the old
		// controller returns. Its late Pod remains the reason to hold.
		return errors.New("fixture: delayed first create committed, response lost")
	}})
	// Drive exactly to the first create; no retry loop is needed because the
	// queued fixture already has the finalizer and private-history admission.
	s := w.get(a.Name)
	obs := observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{missing: true}}
	if err := old.ensure(context.Background(), s, exampleTemplates(t), &obs); err == nil || !parked {
		t.Fatalf("first-create barrier: parked=%v err=%v blocked=%v", parked, err, obs.removalBlocked)
	}
	w.pass()
	w.wantAdmitted("late pod holds", map[string]bool{b.Name: false})
	var pod corev1.Pod
	if !w.exists(&pod, a.Name) {
		t.Fatal("delayed first create did not persist")
	}
	if err := w.base.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	if changed, err := w.r.reconcileSummonRelease(context.Background(), w.get(a.Name)); err != nil || !changed {
		t.Fatalf("absent after deadline plus grace did not release: %v %v", changed, err)
	}
	w.pass()
	w.wantAdmitted("late pod gone", map[string]bool{b.Name: true})
}

func TestSummonUnknownAttemptReleaseDeadline(t *testing.T) {
	for _, elapsed := range []time.Duration{15 * time.Minute, 16*time.Minute - time.Nanosecond, 16 * time.Minute} {
		t.Run(elapsed.String(), func(t *testing.T) {
			w, a, _ := admissionPair(t, "lane")
			w.queue(a)
			w.pass()
			s := w.get(a.Name)
			s.Status.PrivateHistory.ExecutorAttempted = true
			s.Status.PrivateHistory.ExecutorIntent = "uncertain"
			if err := w.base.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			w.close(a.Name, summonoutcome.Unreported)
			w.now = summonT0.Add(elapsed)
			changed, err := w.r.reconcileSummonRelease(context.Background(), w.get(a.Name))
			if err != nil || changed != (elapsed >= 16*time.Minute) {
				t.Fatalf("elapsed=%v changed=%v err=%v", elapsed, changed, err)
			}
		})
	}
}

func TestSummonStoredSharedWorkspaceGate(t *testing.T) {
	for _, lanes := range []bool{false, true} {
		for _, closed := range []bool{false, true} {
			t.Run(fmt.Sprintf("lanes=%v/closed=%v", lanes, closed), func(t *testing.T) {
				s := summonedFor("stored-shared", callerA, v1alpha1.LaneRemediation, time.Minute)
				s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
				s.Finalizers = []string{Finalizer}
				s.Status.SharedAdmission = &v1alpha1.SharedAdmissionStatus{Version: 1, SessionUID: string(s.UID), State: sharedStarted}
				if closed {
					at := metav1.NewTime(summonT0)
					s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeFailed, At: &at}
					s.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at, Closed: &v1alpha1.SummonClosedStatus{At: at, Source: "unreported", Reason: "not-charged", Released: true}}
				}
				w := newSummonWorld(t, s)
				f := stoppedWorkspaceFixture()
				f.s = s
				r, obs := workspaceFixtureController(t, f, w.r.Client)
				r.SummonedLanes = lanes
				obs.pod = owned[*corev1.Pod]{missing: true}
				obs.claim = owned[*corev1.PersistentVolumeClaim]{missing: true}
				if _, err := r.reconcileSharedWorkspace(context.Background(), s, &obs); err != nil {
					t.Fatal(err)
				}
				w.wantNoPodOrVolume(s.Name)
			})
		}
	}
}

// Call the gate itself: the earlier outcome reconciler cannot refresh s.
func TestSummonGateOwnUncachedRead(t *testing.T) {
	for _, fault := range []string{"closed", "replaced", "read-error"} {
		t.Run(fault, func(t *testing.T) {
			w, a, _ := admissionPair(t, "lane")
			w.queue(a)
			w.pass()
			stale := w.get(a.Name)
			reads := 0
			w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				reads++
				if fault == "read-error" {
					return errors.New("fixture uncached read failed")
				}
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				live := obj.(*v1alpha1.AgentSession)
				if fault == "closed" {
					live.Status.Summon.Closed = &v1alpha1.SummonClosedStatus{Source: "unreported"}
				} else {
					live.UID = "replacement"
				}
				return nil
			}})
			ok, err := w.r.summonGate(context.Background(), stale)
			if ok || reads != 1 || (err != nil) != (fault == "read-error") {
				t.Fatalf("gate ok=%v reads=%d err=%v", ok, reads, err)
			}
		})
	}
}

func TestSummonAdmissionEqualCreationUsesUID(t *testing.T) {
	w, a, b := admissionPair(t, "lane")
	a.CreationTimestamp = b.CreationTimestamp
	a.UID, b.UID = types.UID("z-uid"), types.UID("a-uid")
	w.queue(a, b)
	w.pass()
	w.wantAdmitted("UID breaks creation-time tie, not name", map[string]bool{a.Name: false, b.Name: true})
}

func TestSummonAdmissionLedgerUnavailableVersusEmpty(t *testing.T) {
	for _, fault := range []string{"missing", "read-error", "empty"} {
		t.Run(fault, func(t *testing.T) {
			w, a, _ := admissionPair(t, "lane")
			w.queue(a)
			var cm corev1.ConfigMap
			key := client.ObjectKey{Namespace: summonledger.Namespace, Name: summonledger.Name}
			if err := w.base.Get(context.Background(), key, &cm); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "missing":
				if err := w.base.Delete(context.Background(), &cm); err != nil {
					t.Fatal(err)
				}
			case "empty":
				cm.Data = map[string]string{}
				if err := w.base.Update(context.Background(), &cm); err != nil {
					t.Fatal(err)
				}
			case "read-error":
				w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if k == key {
						return errors.New("fixture ledger read failed")
					}
					return c.Get(ctx, k, obj, opts...)
				}})
			}
			_ = w.r.SummonAdmissionPass(context.Background())
			st := w.get(a.Name).Status.Summon
			closed := st != nil && st.Closed != nil
			if w.admitted(a.Name) || closed != (fault == "empty") {
				t.Fatalf("ledger %s: admitted=%v closed=%v", fault, w.admitted(a.Name), closed)
			}
			if closed && (st.Closed.Reason != "not-charged" || !st.Closed.Released) {
				t.Fatalf("empty ledger close: %+v", st.Closed)
			}
			w.wantNoPodOrVolume(a.Name)
		})
	}
}

func TestSummonStaleGrantsNeverHoldCapacity(t *testing.T) {
	for _, stale := range []string{"deleted", "replaced"} {
		t.Run(stale, func(t *testing.T) {
			w, a, _ := admissionPair(t, "pool")
			w.queue(a)
			grant := grantFor(a, summonT0.Add(-time.Hour))
			grant.UID = "old-uid"
			if stale == "deleted" {
				grant.Name = "deleted"
			}
			putTestGrants(t, w.base, grant)
			w.pass()
			w.wantAdmitted("stale grant ignores lane and pool", map[string]bool{a.Name: true})
			if grants := readTestGrants(t, w.base); len(grants) != 1 || grants[0].UID != string(a.UID) {
				t.Fatalf("stale grant not removed in admitting pass: %+v", grants)
			}
		})
	}
}

func TestSummonTaskResultAndLatchPermitRelease(t *testing.T) {
	for _, stored := range []string{"heartbeat-result", "terminal-record"} {
		w, a, _ := admissionPair(t, "lane")
		w.queue(a)
		w.pass()
		w.reconcile(a.Name)
		s := w.get(a.Name)
		result := v1alpha1.TaskStatus{FinishedAt: metav1.NewTime(summonT0), ExitCode: 1}
		if stored == "heartbeat-result" {
			s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentExited, Task: &result}
		} else {
			s.Status.PrivateHistory.TaskTerminal = &v1alpha1.PrivateTaskTerminalStatus{Task: result}
		}
		if err := w.base.Status().Update(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		w.close(a.Name, summonoutcome.Unreported)
		if changed, err := w.r.reconcileSummonRelease(context.Background(), w.get(a.Name)); err != nil || !changed {
			t.Fatalf("%s plus retained pod did not release: %v %v", stored, changed, err)
		}
	}
}

func TestSummonMalformedGrantsHold(t *testing.T) {
	valid := `{"schema":"dev-env.summon-grants.v1","grants":[{"name":"older","uid":"uid-older","lane":"remediation","caller":"alert-ns.alert-responder","pool":"concurrent","at":"2026-11-12T16:00:00Z"}]}`
	for _, raw := range []string{"", "null", "[]", "{}", strings.Replace(valid, "concurrent", "escalations", 1), strings.Replace(valid, `"uid":"uid-older"`, `"uid":"uid-older","uid":"other"`, 1), strings.Replace(valid, `"at":"2026-11-12T16:00:00Z"`, `"at":null`, 1)} {
		w, a, _ := admissionPair(t, "lane")
		w.queue(a)
		var cm corev1.ConfigMap
		if err := w.base.Get(context.Background(), client.ObjectKey{Namespace: summonledger.Namespace, Name: summonledger.Name}, &cm); err != nil {
			t.Fatal(err)
		}
		cm.Data[testGrantsKey] = raw
		if err := w.base.Update(context.Background(), &cm); err != nil {
			t.Fatal(err)
		}
		_ = w.r.SummonAdmissionPass(context.Background())
		w.wantAdmitted("unreadable grants hold", map[string]bool{a.Name: false})
		if s := w.get(a.Name); s.Status.Summon != nil && s.Status.Summon.Closed != nil {
			t.Fatal("unreadable grants caused a close")
		}
	}
}

func TestSummonGrantCodecKeepsUnknownFields(t *testing.T) {
	w := newSummonWorld(t, summonPolicyObject(callerA, 2, ptr.To[int32](1)))
	a := summonedFor("older", callerA, v1alpha1.LaneRemediation, 2*time.Minute)
	b := summonedFor("younger", callerA, v1alpha1.LaneCuration, time.Minute)
	w.queue(a, b)
	var cm corev1.ConfigMap
	key := client.ObjectKey{Namespace: summonledger.Namespace, Name: summonledger.Name}
	if err := w.base.Get(context.Background(), key, &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data[testGrantsKey] = `{"schema":"dev-env.summon-grants.v1","future":7,"grants":[{"name":"older","uid":"uid-older","lane":"remediation","caller":"alert-ns.alert-responder","pool":"concurrent","at":"2026-11-12T16:00:00Z","future":9}]}`
	if err := w.base.Update(context.Background(), &cm); err != nil {
		t.Fatal(err)
	}
	w.pass()
	w.wantAdmitted("two distinct slots", map[string]bool{a.Name: true, b.Name: true})
	if grants := readTestGrants(t, w.base); len(grants) != 2 {
		t.Fatalf("existing grant counted twice or new grant missing: %+v", grants)
	}
	if err := w.base.Get(context.Background(), key, &cm); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Future int `json:"future"`
		Grants []struct {
			Name   string `json:"name"`
			Future int    `json:"future"`
		} `json:"grants"`
	}
	if err := json.Unmarshal([]byte(cm.Data[testGrantsKey]), &doc); err != nil || doc.Future != 7 || len(doc.Grants) != 2 || doc.Grants[0].Name != a.Name || doc.Grants[0].Future != 9 {
		t.Fatalf("grant codec discarded future fields: %+v %v", doc, err)
	}
}

// Both orders matter: a new Session after the ledger read cannot be closed
// against that older charge snapshot, and a new grant after the first list
// cannot be pruned against that older Session snapshot.
func TestSummonAdmissionSnapshotOrdering(t *testing.T) {
	for _, boundary := range []string{"after-ledger", "after-first-list"} {
		t.Run(boundary, func(t *testing.T) {
			w, a, b := admissionPair(t, "lane")
			w.queue(a)
			if boundary == "after-ledger" {
				// No earlier grant CAS may mask the stale charge decision by
				// conflicting first. A legitimate quota hold leaves only B.
				w.r.SummonQuota = func(*v1alpha1.AgentSession) bool { return false }
			}
			fired := false
			inject := func() {
				fired = true
				w.queue(b)
				if boundary == "after-first-list" {
					putTestGrants(t, w.base, grantFor(b, summonT0))
				}
			}
			w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					err := c.Get(ctx, key, obj, opts...)
					if err == nil && key.Name == summonledger.Name && boundary == "after-ledger" && !fired {
						inject()
					}
					return err
				},
				List: func(ctx context.Context, c client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
					err := c.List(ctx, obj, opts...)
					if _, ok := obj.(*v1alpha1.AgentSessionList); ok && err == nil && boundary == "after-first-list" && !fired {
						inject()
					}
					return err
				},
			})
			w.pass()
			if !fired {
				t.Fatal("snapshot barrier not exercised")
			}
			if boundary == "after-ledger" {
				if st := w.get(b.Name).Status.Summon; st != nil && st.Closed != nil {
					t.Fatal("newly charged Session closed against a ledger read before its create")
				}
				w.wantAdmitted("first snapshot", map[string]bool{a.Name: false, b.Name: false})
			} else {
				w.wantAdmitted("new grant must survive earlier list absence", map[string]bool{a.Name: false, b.Name: true})
			}
		})
	}
}

func TestSummonGrantUpdatePreservesLedger(t *testing.T) {
	w, a, _ := admissionPair(t, "lane")
	w.queue(a)
	var before corev1.ConfigMap
	key := client.ObjectKey{Namespace: summonledger.Namespace, Name: summonledger.Name}
	if err := w.base.Get(context.Background(), key, &before); err != nil {
		t.Fatal(err)
	}
	before.Labels = map[string]string{"fixture": "kept"}
	before.Annotations = map[string]string{"kustomize.toolkit.fluxcd.io/ssa": "Merge", "kustomize.toolkit.fluxcd.io/prune": "disabled", "fixture": "kept"}
	before.Finalizers = []string{"fixture/keep"}
	before.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "fixture-owner", UID: "fixture-uid"}}
	before.Data["other.caller"] = "opaque caller entry"
	before.BinaryData = map[string][]byte{"fixture": []byte("kept")}
	if err := w.base.Update(context.Background(), &before); err != nil {
		t.Fatal(err)
	}
	readVersion := ""
	writes := 0
	w.r = w.leader(&interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok || client.ObjectKeyFromObject(cm) != key || readVersion == "" || cm.ResourceVersion != readVersion {
			t.Fatal("ledger update did not use this pass's exact uncached ConfigMap/resourceVersion")
		}
		writes++
		return c.Update(ctx, obj, opts...)
	}})
	w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		err := c.Get(ctx, k, obj, opts...)
		if err == nil && k == key {
			readVersion = obj.GetResourceVersion()
		}
		return err
	}})
	w.pass()
	if writes != 1 || len(readTestGrants(t, w.base)) != 1 {
		t.Fatalf("expected exactly one grant Update, got %d", writes)
	}
	var after corev1.ConfigMap
	if err := w.base.Get(context.Background(), key, &after); err != nil {
		t.Fatal(err)
	}
	delete(after.Data, testGrantsKey)
	after.ResourceVersion = before.ResourceVersion // the server advances just this metadata
	if !apiequality.Semantic.DeepEqual(before, after) {
		t.Fatal("grant Update changed something beyond the reserved data key/resourceVersion")
	}
}

func TestSummonGrantWriterRefusesWrongLedger(t *testing.T) {
	for _, field := range []string{"name", "namespace", "resourceVersion"} {
		t.Run(field, func(t *testing.T) {
			w, a, _ := admissionPair(t, "lane")
			w.queue(a)
			writes := 0
			w.r = w.leader(&interceptor.Funcs{Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				writes++
				return errors.New("fixture refuses the wrong ledger write")
			}})
			w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				if cm, ok := obj.(*corev1.ConfigMap); ok && key.Name == summonledger.Name {
					switch field {
					case "name":
						cm.Name = "another-ledger"
					case "namespace":
						cm.Namespace = "another-namespace"
					case "resourceVersion":
						cm.ResourceVersion = ""
					}
				}
				return nil
			}})
			_ = w.r.SummonAdmissionPass(context.Background())
			if writes != 0 || w.admitted(a.Name) {
				t.Fatalf("invalid ledger %s: writes=%d admitted=%v", field, writes, w.admitted(a.Name))
			}
		})
	}
}
