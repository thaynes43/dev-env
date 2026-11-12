package apiserver

// Item 741 comment 7228 (OP777): the create ledger's two known windows,
// replayed through the API with the schedules story 758 asserted as stated
// limits, then closed by the operator leader over the same store. The
// uncharged session (F1) and the surplus session (F2) are closed by their
// onUnreported with reason not-charged and get no volume and no pod; the
// session the ledger binds is admitted and built.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
	"github.com/thaynes43/dev-env/internal/templates"
)

// leaderOver is the operator leader over the API fixture's store, with
// --enable-summoned-lanes and both task latch flags on.
func leaderOver(t *testing.T, f *fixture) *controller.Reconciler {
	t.Helper()
	b, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: "dev-env-system", Name: templates.DefaultName}
	if err := f.c.Create(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}, Data: map[string]string{templates.Key: string(b)}}); err != nil {
		t.Fatal(err)
	}
	return &controller.Reconciler{Client: f.c, APIReader: f.c, Templates: key, SessionNamespace: sessionNS, SummonedLanes: true,
		PrivateHistoryRetention: true, PrivateTerminalRecord: true, PrivateExecutorStop: true, TerminalTaskPodReclaim: true,
		Now: func() time.Time { return f.now }}
}

// lead lets the leader's reconciler see each session, runs one admission
// pass, then reconciles each again.
func lead(t *testing.T, f *fixture, r *controller.Reconciler, names ...string) {
	t.Helper()
	// A real API server sets metadata.generation 1 at create; the fake one
	// does not, and the task latch binds the generation.
	for _, n := range names {
		s := f.session(n)
		if s.Generation == 0 {
			s.Generation = 1
			if err := f.c.Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
		}
	}
	reconcile := func() {
		for _, n := range names {
			for range 6 {
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: sessionNS, Name: n}}); err != nil {
					t.Fatalf("reconcile %s: %v", n, err)
				}
			}
		}
	}
	reconcile()
	if err := r.SummonAdmissionPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	reconcile()
}

func wantClosedNotCharged(t *testing.T, f *fixture, name string) {
	t.Helper()
	s := f.session(name)
	c := s.Status.Summon
	if s.Status.Outcome == nil || s.Status.Outcome.State != v1alpha1.OutcomeEscalated || c == nil || c.Closed == nil ||
		c.Closed.Reason != summonoutcome.ReasonNotCharged || c.Closed.Source != summonoutcome.Unreported || !c.Closed.Released || c.AdmittedAt != nil {
		t.Fatalf("%s: outcome %+v summon %+v, want closed escalated (its onUnreported), not-charged, released, never admitted", name, s.Status.Outcome, c)
	}
	for _, obj := range []client.Object{&corev1.Pod{}, &corev1.PersistentVolumeClaim{}} {
		n := name
		if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
			n = controller.HomeClaimName(name)
		}
		if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: n}, obj); !apierrors.IsNotFound(err) {
			t.Fatalf("%s has a %T (err %v)", name, obj, err)
		}
	}
}

func wantAdmittedAndBuilt(t *testing.T, f *fixture, name string) {
	t.Helper()
	s := f.session(name)
	if s.Status.Summon == nil || s.Status.Summon.AdmittedAt == nil || summonoutcome.Terminal(s.Status.Outcome) {
		t.Fatalf("%s: summon %+v outcome %+v, want admitted and open", name, s.Status.Summon, s.Status.Outcome)
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		for _, c := range s.Status.Conditions {
			t.Logf("condition %s %s %s %s", c.Type, c.Status, c.Reason, c.Message)
		}
		t.Fatalf("%s was admitted and not built: %v", name, err)
	}
}

// F1: X's create reached the API server before a pause; another replica
// pruned X; the session of X was then persisted with no charge.
func TestSummonLeaderClosesTheUnchargedSession(t *testing.T) {
	f := summonFixture(t, ledgerPolicy(4, 6))
	t0 := f.now
	body := keyedRemediation("rem-responder-1", "alert-1")
	x := crashFirstCreate(t, f, body)
	b := replica(f, f.c)
	var bw *httptest.ResponseRecorder
	f.summonLimit = true
	sent := false
	a := replica(f, interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); !ok || sent {
			return c.Create(ctx, obj, opts...)
		}
		sent = true
		f.now = t0.Add(80*time.Minute + 59*time.Second)
		bw = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2"))
		return c.Create(context.Background(), obj, opts...)
	}}))
	a.SummonClock = newFakeSummonClock()
	f.now = t0.Add(64*time.Minute + 59*time.Second)
	aw := post(t, a.Handler(), tokStranger, body)
	wantStatus(t, bw, http.StatusCreated)
	wantStatus(t, aw, http.StatusCreated)
	if _, ok := f.ledger().charge(x); ok || len(sessionsOfID(f, x)) != 1 {
		t.Fatalf("not the F1 window: charges %+v sessions of X %v", f.ledger().Charges, sessionsOfID(f, x))
	}
	lead(t, f, leaderOver(t, f), "rem-responder-1", "rem-responder-2")
	wantClosedNotCharged(t, f, "rem-responder-1")
	wantAdmittedAndBuilt(t, f, "rem-responder-2")
}

// F2: a stale destination after a rebind: two sessions carry X, and the
// ledger binds X to N-2 only.
func TestSummonLeaderClosesTheSurplusSession(t *testing.T) {
	const n, n2 = "rem-responder-1", "rem-responder-1-2"
	f := summonFixture(t, ledgerPolicy(4, 6))
	f.summonLimit = true
	run, bw := rebindSchedule(t, f, false)
	a := replica(f, holdSessionCreate(f, run))
	aw := post(t, a.Handler(), tokStranger, keyedRemediation(n, "alert-1"))
	wantStatus(t, *bw, http.StatusCreated)
	wantStatus(t, aw, http.StatusCreated)
	x := summonDispatchID(summonTestAccount, "alert-1", nil)
	if got := sessionsOfID(f, x); len(got) != 2 {
		t.Fatalf("not the F2 window: sessions of X %v", got)
	}
	lead(t, f, leaderOver(t, f), n, n2)
	wantClosedNotCharged(t, f, n)
	wantAdmittedAndBuilt(t, f, n2)
}
