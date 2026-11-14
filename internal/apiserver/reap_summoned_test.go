package apiserver

// Story 779 fix round 1: the API records a stop, then the leader closes and
// deletes. These tests use the real HTTP routes and explicit write hooks.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

func TestSummonedReapFirstRecordedCallerWins(t *testing.T) {
	for _, first := range []string{"person", "session"} {
		t.Run(first, func(t *testing.T) {
			f := summonFixture(t)
			worker := f.sessionPod("worker", "default", 0)
			name := "rem-responder-race"
			summonedLifecycleSession(t, f, name, v1alpha1.OperatingModeRunning)
			firstToken, secondToken, want := tokHuman, worker, "human/"+humanSA
			if first == "session" {
				firstToken, secondToken, want = worker, tokHuman, "session/worker"
			}
			patches, deletes := 0, 0
			var winnerRV string
			f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
					patches++
					data, err := p.Data(o)
					if err != nil {
						t.Fatal(err)
					}
					var patch struct {
						Metadata struct{ ResourceVersion string }
					}
					if json.Unmarshal(data, &patch) != nil || patch.Metadata.ResourceVersion == "" {
						t.Fatal("reap attribution lacks the resourceVersion lock")
					}
					if patches == 1 {
						// The first reaper commits between the second reaper's GET
						// and PATCH. Its nested call uses the same real route.
						wantStatus(t, f.do(http.MethodDelete, apiv1.SessionPath(name), firstToken, nil), http.StatusAccepted)
						winnerRV = f.session(name).ResourceVersion
					}
					return c.Patch(ctx, o, p, opts...)
				},
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
					deletes++
					return errors.New("the API must not delete a summoned Session")
				},
			})
			wantStatus(t, f.do(http.MethodDelete, apiv1.SessionPath(name), secondToken, nil), http.StatusAccepted)
			wantStatus(t, f.do(http.MethodDelete, apiv1.SessionPath(name), firstToken, nil), http.StatusAccepted)
			wantStatus(t, f.do(http.MethodDelete, apiv1.SessionPath(name), secondToken, nil), http.StatusAccepted)
			s := f.session(name)
			if s.Annotations[v1alpha1.AnnotationReapedBy] != want || !s.DeletionTimestamp.IsZero() ||
				s.ResourceVersion != winnerRV || patches != 2 || deletes != 0 {
				t.Fatalf("first reap lost: marker=%q rv=%s wantRV=%s patches=%d deletes=%d", s.Annotations[v1alpha1.AnnotationReapedBy], s.ResourceVersion, winnerRV, patches, deletes)
			}
		})
	}
}

func TestSummonedReapRefusalsWriteNoMarker(t *testing.T) {
	for _, state := range []string{"unauthorized", "missing-finalizer", "already-deleting", "empty-marker"} {
		t.Run(state, func(t *testing.T) {
			f := summonFixture(t)
			name := "rem-responder-refused"
			summonedLifecycleSession(t, f, name, v1alpha1.OperatingModeRunning)
			s := f.session(name)
			want, token := http.StatusAccepted, tokHuman
			switch state {
			case "unauthorized":
				want, token = http.StatusForbidden, tokStranger
			case "missing-finalizer":
				s.Finalizers = nil
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
				s.Status.Phase = v1alpha1.PhaseRunning
				if err := f.c.Status().Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
				want = http.StatusConflict
			case "already-deleting":
				if err := f.c.Delete(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			case "empty-marker":
				s.Annotations = map[string]string{v1alpha1.AnnotationReapedBy: ""}
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			before := f.session(name)
			f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					t.Fatal("a refusal or existing marker must not write attribution")
					return nil
				},
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
					t.Fatal("the API must not delete this Session")
					return nil
				},
			})
			wantStatus(t, f.do(http.MethodDelete, apiv1.SessionPath(name), token, nil), want)
			after := f.session(name)
			if after.ResourceVersion != before.ResourceVersion {
				t.Fatal("the Session changed")
			}
		})
	}
}

func TestSummonedCreateThenReapClosesBeforeDelete(t *testing.T) {
	for _, who := range []string{"person", "session"} {
		t.Run(who, func(t *testing.T) {
			f := summonFixture(t, ledgerPolicy(4, 6))
			worker := f.sessionPod("worker", "default", 0)
			name := "rem-responder-immediate"
			wantStatus(t, summonPost(t, f, keyedRemediation(name, "immediate")), http.StatusCreated)
			created := f.session(name)
			if len(created.Finalizers) != 0 || created.Status.Outcome != nil {
				t.Fatal("the fixture reconciled before the reap")
			}
			token, marker, reason, state := tokHuman, "human/"+humanSA, summonoutcome.ReasonStopped, v1alpha1.OutcomeFailed
			if who == "session" {
				token, marker, reason, state = worker, "session/worker", summonoutcome.ReasonSuspendedUnreported, v1alpha1.OutcomeEscalated
			}
			wantStatus(t, f.do(http.MethodDelete, apiv1.SessionPath(name), token, nil), http.StatusAccepted)
			recorded := f.session(name)
			if !recorded.DeletionTimestamp.IsZero() || recorded.Annotations[v1alpha1.AnnotationReapedBy] != marker || len(recorded.Finalizers) != 0 {
				t.Fatalf("reap did not leave the unfinalized Session with its marker: %+v", recorded.ObjectMeta)
			}
			deletes, closes := 0, 0
			r := leaderOver(t, f)
			r.Client = interceptor.NewClient(f.c, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					if s, ok := o.(*v1alpha1.AgentSession); ok && s.Name == name && summonoutcome.Terminal(s.Status.Outcome) {
						closes++
					}
					return c.SubResource(sub).Patch(ctx, o, p, opts...)
				},
				Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
					s, ok := o.(*v1alpha1.AgentSession)
					if !ok || s.Name != name {
						t.Fatalf("unexpected delete of %T %s", o, o.GetName())
					}
					deletes++
					stored := f.session(name)
					st := stored.Status.Summon
					if stored.Status.Outcome == nil || stored.Status.Outcome.State != state || st == nil || st.Closed == nil ||
						st.Closed.Reason != reason || st.Closed.Source != summonoutcome.Unreported || !st.Closed.Released || closes != 1 {
						t.Fatalf("delete before exactly one stored close: outcome=%+v summon=%+v closes=%d", stored.Status.Outcome, st, closes)
					}
					if who == "person" && st.Escalation != nil || who == "session" && (st.Escalation == nil || st.Escalation.State != "Pending") {
						t.Fatalf("wrong pending filing: %+v", st.Escalation)
					}
					options := (&client.DeleteOptions{}).ApplyOptions(opts)
					p := options.Preconditions
					if p == nil || p.UID == nil || *p.UID != stored.UID || p.ResourceVersion == nil || *p.ResourceVersion != stored.ResourceVersion {
						t.Fatalf("delete does not bind the stored close: %+v", p)
					}
					return nil // Hold removal here to inspect the durable close.
				},
				Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					t.Fatal("a reaped Session must not build resources")
					return nil
				},
			})
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(recorded)}
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if deletes != 1 || closes != 1 {
				t.Fatalf("closes=%d deletes=%d, want one each", closes, deletes)
			}
		})
	}
}

func TestSummonMalformedCallerStillExpiresAndPrunes(t *testing.T) {
	ctx := context.Background()
	f := summonFixture(t, ledgerPolicy(4, 6))
	t0 := f.now
	name := "rem-responder-malformed"
	wantStatus(t, summonPost(t, f, keyedRemediation(name, "malformed")), http.StatusCreated)
	r := leaderOver(t, f)
	// Initialize the latch without ever running admission.
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.session(name))}
	for range 3 {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	var ledger corev1.ConfigMap
	key := types.NamespacedName{Namespace: ledgerTestNS, Name: ledgerTestName}
	if err := f.c.Get(ctx, key, &ledger); err != nil {
		t.Fatal(err)
	}
	caller := f.session(name).Spec.Caller
	saved := ledger.Data[caller]
	ledger.Data[caller] = "{malformed"
	ledger.Data[summonledger.GrantsKey] = `{"schema":"dev-env.summon-grants.v1","grants":[]}`
	if err := f.c.Update(ctx, &ledger); err != nil {
		t.Fatal(err)
	}
	closes := 0
	writer := interceptor.NewClient(f.c, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
			wasTerminal := summonoutcome.Terminal(f.session(name).Status.Outcome)
			err := c.SubResource(sub).Patch(ctx, o, p, opts...)
			if s, ok := o.(*v1alpha1.AgentSession); err == nil && ok && s.Name == name && !wasTerminal && summonoutcome.Terminal(s.Status.Outcome) {
				closes++
			}
			return err
		},
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			t.Fatal("a Queued hold must create no resources")
			return nil
		},
	})
	r.Client = writer
	f.now = t0.Add(20*time.Minute - time.Second)
	if err := r.SummonAdmissionPass(ctx); err != nil {
		t.Fatal(err)
	}
	if s := f.session(name); summonoutcome.Terminal(s.Status.Outcome) || s.Status.Summon != nil && s.Status.Summon.AdmittedAt != nil {
		t.Fatal("an unreadable entry allowed not-charged or admission before maxHold")
	}
	f.now = t0.Add(20 * time.Minute)
	if err := r.SummonAdmissionPass(ctx); err != nil {
		t.Fatal(err)
	}
	closed := f.session(name)
	st := closed.Status.Summon
	if closed.Status.Outcome == nil || closed.Status.Outcome.State != v1alpha1.OutcomeEscalated || st == nil || st.Closed == nil ||
		st.Closed.Reason != summonoutcome.ReasonHoldExpired || !st.Closed.Released || st.AdmittedAt != nil || st.Escalation == nil || st.Escalation.State != "Pending" {
		t.Fatalf("malformed accounting suppressed maxHold: outcome=%+v summon=%+v", closed.Status.Outcome, st)
	}
	closeBytes := func(s *v1alpha1.AgentSession) string {
		t.Helper()
		b, err := json.Marshal([]any{s.Status.Outcome, s.Status.Summon.Closed})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	originalClose := closeBytes(closed)
	f.now = t0.Add(21 * time.Minute)
	if err := r.SummonAdmissionPass(ctx); err != nil {
		t.Fatal(err)
	}
	// A queued hold closes after the filing scan. The next pass records
	// OP801's remote-off condition/event beside that unchanged close.
	blocked := f.session(name)
	condition := meta.FindStatusCondition(blocked.Status.Conditions, summonoutcome.ConditionEscalationPending)
	pendingEvents := 0
	for _, page := range blocked.Status.Summon.Pages {
		if page.Event == v1alpha1.PageEscalationPending {
			pendingEvents++
		}
	}
	if closes != 1 || closeBytes(blocked) != originalClose || pendingEvents != 1 || condition == nil ||
		condition.Status != metav1.ConditionTrue || condition.Reason != "Blocked" || condition.Message != string(summonoutcome.FilingRemoteOff) {
		t.Fatal("Blocked must preserve the exact outcome/close and record one remote-off pending event")
	}
	r = &controller.Reconciler{Client: writer, APIReader: f.c, SessionNamespace: sessionNS, SummonedLanes: true,
		PrivateHistoryRetention: true, PrivateTerminalRecord: true, Now: func() time.Time { return f.now }}
	if err := r.SummonAdmissionPass(ctx); err != nil {
		t.Fatal(err)
	}
	if closes != 1 || closeBytes(f.session(name)) != originalClose || f.session(name).ResourceVersion != blocked.ResourceVersion {
		t.Fatalf("a new leader rewrote the close or Blocked record: writes=%d", closes)
	}
	for _, o := range []client.Object{&corev1.Pod{}, &corev1.PersistentVolumeClaim{}} {
		n := name
		if _, ok := o.(*corev1.PersistentVolumeClaim); ok {
			n = controller.HomeClaimName(name)
		}
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: n}, o); !apierrors.IsNotFound(err) {
			t.Fatalf("held Session has a %T: %v", o, err)
		}
	}
	if err := f.c.Get(ctx, key, &ledger); err != nil {
		t.Fatal(err)
	}
	ledger.Data[caller] = saved
	if err := f.c.Update(ctx, &ledger); err != nil {
		t.Fatal(err)
	}
	f.now = t0.Add(80 * time.Minute)
	wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-next", "next")), http.StatusCreated)
	if _, kept := f.ledger().charge(closed.Spec.Summon.DispatchID); kept {
		t.Fatal("normal prune kept the closed Session's old charge after the entry recovered")
	}
}
