package apiserver

// The proofs of story 758 (DESIGN-064 5.9 "The create ledger" and its
// "Proof of the charge order (N1)", 6.4 "Idempotency" and "Limits"). Each
// test reads the ledger and the sessions themselves, not only a status code.
// The fixture's create hook (summonFixture) also checks, on every summoned
// create, that the session's dispatch id is charged in the ledger at that
// moment (proof 9).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// The ledger's coordinates are written out here on purpose: haynes-ops
// precreates this object and grants the operator get and update on it by
// name, so a rename must fail a test.
const (
	ledgerTestNS   = "dev-env-system"
	ledgerTestName = "dev-env-summon-creates"
	ledgerTestKey  = "upgrade-agent.alert-responder"
)

func summonLedgerObject() *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ledgerTestNS, Name: ledgerTestName}}
}

type ledgerTestCharge struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

type ledgerTestEntry struct {
	Schema              string             `json:"schema"`
	Charges             []ledgerTestCharge `json:"charges"`
	UrgentRefusals      int64              `json:"urgentRefusals"`
	LastUrgentRefusalAt *time.Time         `json:"lastUrgentRefusalAt"`
}

func (e ledgerTestEntry) charge(id string) (ledgerTestCharge, bool) {
	for _, c := range e.Charges {
		if c.ID == id {
			return c, true
		}
	}
	return ledgerTestCharge{}, false
}

// readLedgerConfigMap reads the ledger object; a missing caller key is an
// empty entry.
func readLedgerConfigMap(ctx context.Context, r client.Reader, key string) (ledgerTestEntry, *corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: ledgerTestNS, Name: ledgerTestName}, &cm); err != nil {
		return ledgerTestEntry{}, nil, err
	}
	var e ledgerTestEntry
	if raw := cm.Data[key]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return ledgerTestEntry{}, &cm, err
		}
	}
	return e, &cm, nil
}

func (f *fixture) ledger() ledgerTestEntry {
	f.t.Helper()
	e, _, err := readLedgerConfigMap(context.Background(), f.c, ledgerTestKey)
	if err != nil {
		f.t.Fatalf("read the ledger: %v", err)
	}
	return e
}

func (f *fixture) ledgerVersion() string {
	f.t.Helper()
	_, cm, err := readLedgerConfigMap(context.Background(), f.c, ledgerTestKey)
	if err != nil {
		f.t.Fatalf("read the ledger: %v", err)
	}
	return cm.ResourceVersion
}

// checkChargedAtCreate is the store's create hook for proof 9: a summoned
// session's dispatch id must be charged in the ledger when it is created.
func checkChargedAtCreate(ctx context.Context, r client.Reader, sess *v1alpha1.AgentSession) error {
	if sess.Spec.Summon == nil {
		return nil
	}
	e, _, err := readLedgerConfigMap(ctx, r, sess.Spec.Caller)
	if err != nil {
		return err
	}
	if _, ok := e.charge(sess.Spec.Summon.DispatchID); !ok {
		return errors.New("dispatch id " + sess.Spec.Summon.DispatchID + " is not charged")
	}
	return nil
}

// summonedSessions are the stored sessions that carry spec.summon.
func (f *fixture) summonedSessions() []v1alpha1.AgentSession {
	f.t.Helper()
	var list v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &list, client.InNamespace(sessionNS)); err != nil {
		f.t.Fatal(err)
	}
	var out []v1alpha1.AgentSession
	for _, s := range list.Items {
		if s.Spec.Summon != nil {
			out = append(out, s)
		}
	}
	return out
}

func keyedRemediation(name, key string) map[string]any {
	b := remediationBody(name, nil)
	b["idempotencyKey"] = key
	return b
}

func ledgerPolicy(concurrent, perHour int32) *v1alpha1.CallerPolicy {
	p := summonTestPolicy()
	p.Spec.Summon.Limits.Concurrent = concurrent
	p.Spec.Summon.Limits.CreatesPerHour = perHour
	return p
}

// failSessionCreate makes the server's writes fail on chosen AgentSession
// creates. after=false fails before the write: a crash after the charge and
// before the session. after=true writes the session, then fails: a crash after
// the session and before the response.
func failSessionCreate(f *fixture, after bool, fail func(n int) bool) *int {
	n := 0
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); !ok {
			return c.Create(ctx, obj, opts...)
		}
		n++
		if !fail(n) {
			return c.Create(ctx, obj, opts...)
		}
		if after {
			if err := c.Create(ctx, obj, opts...); err != nil {
				return err
			}
		}
		return errors.New("connection reset by peer")
	}})
	return &n
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
}

// Proof 1: a crash after the charge and before the session. The retry with
// the same key maps to the same id, is not charged twice, and creates the
// session.
func TestSummonLedgerCrashAfterChargeRetries(t *testing.T) {
	f := summonFixture(t)
	failSessionCreate(f, false, func(n int) bool { return n == 1 })
	body := keyedRemediation("rem-responder-1", "alert-1")
	first := summonPost(t, f, body)
	if first.Code < 500 {
		t.Fatalf("crash: %d %s", first.Code, first.Body.String())
	}
	if n := len(f.summonedSessions()); n != 0 {
		t.Fatalf("%d sessions after the crash", n)
	}
	e := f.ledger()
	if len(e.Charges) != 1 {
		t.Fatalf("charges after the crash %+v", e.Charges)
	}
	id, at := e.Charges[0].ID, e.Charges[0].At

	f.now = f.now.Add(10 * time.Minute)
	retry := summonPost(t, f, body)
	wantStatus(t, retry, http.StatusCreated)
	sess := f.session("rem-responder-1")
	if sess.Spec.Summon.DispatchID != id {
		t.Fatalf("retry id %q, charged %q", sess.Spec.Summon.DispatchID, id)
	}
	e = f.ledger()
	if len(e.Charges) != 1 || e.Charges[0].ID != id || !e.Charges[0].At.Equal(at) {
		t.Fatalf("retry charged again: %+v", e.Charges)
	}
}

// Proof 2: a crash after the session and before the response. The retry
// returns that session with 200 and charges nothing.
func TestSummonLedgerCrashAfterSessionReturnsIt(t *testing.T) {
	f := summonFixture(t)
	failSessionCreate(f, true, func(n int) bool { return n == 1 })
	body := keyedRemediation("rem-responder-1", "alert-1")
	first := summonPost(t, f, body)
	if first.Code < 500 {
		t.Fatalf("lost response: %d %s", first.Code, first.Body.String())
	}
	sess := f.session("rem-responder-1")
	version := f.ledgerVersion()
	if e := f.ledger(); len(e.Charges) != 1 || e.Charges[0].ID != sess.Spec.Summon.DispatchID {
		t.Fatalf("charges %+v, session id %q", e.Charges, sess.Spec.Summon.DispatchID)
	}

	f.now = f.now.Add(time.Minute)
	retry := summonPost(t, f, body)
	wantStatus(t, retry, http.StatusOK)
	got := decode[apiv1.Session](t, retry)
	if got.Name != sess.Name || got.UID != string(sess.UID) {
		t.Fatalf("retry returned %s %s, want %s %s", got.Name, got.UID, sess.Name, sess.UID)
	}
	if f.ledgerVersion() != version {
		t.Fatal("the retry wrote the ledger")
	}
	if n := len(f.summonedSessions()); n != 1 {
		t.Fatalf("%d sessions", n)
	}
}

// Proof 3: a retry in the next hour. An hour-old charge whose session was
// never created is dropped, and its retry is charged again and creates one
// session; and the retry of proof 1 across the hour's rollover, where the
// re-charge takes a slot of the new hour.
func TestSummonLedgerRetryInTheNextHour(t *testing.T) {
	t.Run("dropped and charged again", func(t *testing.T) {
		f := summonFixture(t)
		failSessionCreate(f, false, func(n int) bool { return n == 1 })
		body := keyedRemediation("rem-responder-1", "alert-1")
		if w := summonPost(t, f, body); w.Code < 500 {
			t.Fatalf("crash: %d %s", w.Code, w.Body.String())
		}
		e := f.ledger()
		if len(e.Charges) != 1 {
			t.Fatalf("charges %+v", e.Charges)
		}
		id, t0 := e.Charges[0].ID, e.Charges[0].At

		f.now = t0.Add(time.Hour + time.Minute)
		wantStatus(t, summonPost(t, f, body), http.StatusCreated)
		e = f.ledger()
		if len(e.Charges) != 1 || e.Charges[0].ID != id || !e.Charges[0].At.Equal(f.now) {
			t.Fatalf("want one charge for %s at %s, got %+v", id, f.now, e.Charges)
		}
		if s := f.summonedSessions(); len(s) != 1 || s[0].Spec.Summon.DispatchID != id {
			t.Fatalf("sessions %+v", s)
		}
	})

	t.Run("across the rollover", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 1))
		failSessionCreate(f, false, func(n int) bool { return n == 1 })
		a := keyedRemediation("rem-responder-a", "alert-a")
		b := keyedRemediation("rem-responder-b", "alert-b")
		t0 := f.now
		if w := summonPost(t, f, a); w.Code < 500 {
			t.Fatalf("crash: %d %s", w.Code, w.Body.String())
		}
		e0 := f.ledger()
		if len(e0.Charges) != 1 {
			t.Fatalf("charges after the crash %+v", e0.Charges)
		}
		idA := e0.Charges[0].ID

		// The orphan charge spends its hour: another key is refused.
		f.now = t0.Add(30 * time.Minute)
		wantError(t, summonPost(t, f, b), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		if e := f.ledger(); len(e.Charges) != 1 || e.UrgentRefusals != 1 {
			t.Fatalf("after the refusal %+v", e)
		}

		// Past the hour the orphan is dropped and a's retry is charged again.
		f.now = t0.Add(61 * time.Minute)
		wantStatus(t, summonPost(t, f, a), http.StatusCreated)
		e := f.ledger()
		if len(e.Charges) != 1 || e.Charges[0].ID != idA || !e.Charges[0].At.Equal(f.now) {
			t.Fatalf("after the retry %+v", e.Charges)
		}

		// That re-charge is a slot of the new hour.
		f.now = t0.Add(62 * time.Minute)
		wantError(t, summonPost(t, f, b), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		if e := f.ledger(); len(e.Charges) != 1 || e.UrgentRefusals != 2 {
			t.Fatalf("after the second refusal %+v", e)
		}

		// An hour after the re-charge it is dropped once its session exists.
		f.editStatus("rem-responder-a", v1alpha1.PhaseFailed)
		f.now = t0.Add(125 * time.Minute)
		wantStatus(t, summonPost(t, f, b), http.StatusCreated)
		e = f.ledger()
		if len(e.Charges) != 1 || e.Charges[0].ID == idA {
			t.Fatalf("the settled charge stayed: %+v", e.Charges)
		}
		if n := len(f.summonedSessions()); n != 2 {
			t.Fatalf("%d sessions", n)
		}
	})
}

// replica is a second API replica over the same store: its own Server, its
// own create lock, the same objects.
func replica(f *fixture, c client.Client) *Server {
	return &Server{
		Client:         c,
		Live:           f.c,
		Auth:           f.auth,
		Policy:         f.srv.Policy,
		Templates:      f.srv.Templates,
		Log:            logr.Discard(),
		Now:            f.srv.Now,
		SummonBoundary: f.srv.SummonBoundary,
	}
}

func post(t *testing.T, h http.Handler, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	j, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, apiv1.SessionsPath, strings.NewReader(string(j)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// interleaved records what replica A's ledger writes returned.
type interleaved struct {
	writes []error
}

// interleave returns a client for replica A whose first ledger update first
// runs replica B's whole create, synchronously. So B reads and writes the
// ledger between A's read and A's write: the writes are interleaved by the
// hook, not by goroutine timing.
func interleave(f *fixture, b func()) (client.Client, *interleaved) {
	got := &interleaved{}
	return interceptor.NewClient(f.c, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok || cm.Name != ledgerTestName {
			return c.Update(ctx, obj, opts...)
		}
		if len(got.writes) == 0 {
			b()
		}
		err := c.Update(ctx, obj, opts...)
		got.writes = append(got.writes, err)
		return err
	}}), got
}

// wantLostTheWrite checks that A's first ledger write was refused by the
// optimistic lock, so A decided again on the ledger B had written.
func wantLostTheWrite(t *testing.T, got *interleaved) {
	t.Helper()
	if len(got.writes) == 0 || !apierrors.IsConflict(got.writes[0]) {
		t.Fatalf("replica A's ledger writes %v, want a conflict first", got.writes)
	}
}

// Proof 4: two API replicas compete for the hour's last charge. Exactly one
// session and one charge come of it, and the other answers 429. With one key
// the two map to one id: one session, one charge, and both get it.
func TestSummonLedgerReplicasRaceForTheLastCharge(t *testing.T) {
	t.Run("two keys", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 2))
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-0", "alert-0")), http.StatusCreated)
		f.now = f.now.Add(time.Minute)
		b := replica(f, f.c)
		var bw *httptest.ResponseRecorder
		ac, writes := interleave(f, func() {
			bw = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2"))
		})
		a := replica(f, ac)
		aw := post(t, a.Handler(), tokStranger, keyedRemediation("rem-responder-1", "alert-1"))
		wantStatus(t, bw, http.StatusCreated)
		wantError(t, aw, http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		wantLostTheWrite(t, writes)
		// A's second write is its refusal, counted once.
		if len(writes.writes) != 2 || writes.writes[1] != nil {
			t.Fatalf("replica A's ledger writes %v", writes.writes)
		}

		e := f.ledger()
		sessions := f.summonedSessions()
		if len(sessions) != 2 || len(e.Charges) != 2 {
			t.Fatalf("sessions %d charges %+v, want 2 and 2", len(sessions), e.Charges)
		}
		for _, s := range sessions {
			if s.Name == "rem-responder-1" {
				t.Fatal("the loser created a session")
			}
			if _, ok := e.charge(s.Spec.Summon.DispatchID); !ok {
				t.Fatalf("session %s is not charged", s.Name)
			}
		}
		if e.UrgentRefusals != 1 {
			t.Fatalf("urgent refusals %d, want 1", e.UrgentRefusals)
		}
	})

	t.Run("one key", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 2))
		body := keyedRemediation("rem-responder-1", "alert-1")
		b := replica(f, f.c)
		var bw *httptest.ResponseRecorder
		ac, writes := interleave(f, func() { bw = post(t, b.Handler(), tokStranger, body) })
		a := replica(f, ac)
		aw := post(t, a.Handler(), tokStranger, body)
		wantStatus(t, bw, http.StatusCreated)
		wantStatus(t, aw, http.StatusOK)
		wantLostTheWrite(t, writes)
		// On the re-read the id is charged: A writes nothing more.
		if len(writes.writes) != 1 {
			t.Fatalf("replica A's ledger writes %v", writes.writes)
		}
		if got := decode[apiv1.Session](t, aw); got.Name != "rem-responder-1" {
			t.Fatalf("a got %s", got.Name)
		}
		if s, e := f.summonedSessions(), f.ledger(); len(s) != 1 || len(e.Charges) != 1 || e.UrgentRefusals != 0 {
			t.Fatalf("sessions %d ledger %+v", len(s), e)
		}
	})
}

// Proof 5: a re-fire of a key whose session ended gets a new id, a new
// charge and the suffix name, and the earlier record stays. Ended is a
// finished session, or one with a terminal outcome.
func TestSummonLedgerRefireAfterTheSessionEnded(t *testing.T) {
	// concurrent 4: a session with a terminal outcome whose pod still runs is
	// unfinished and still holds its pool slot until story 741.
	f := summonFixture(t, ledgerPolicy(4, 6))
	body := keyedRemediation("rem-responder-1", "alert-1")
	wantStatus(t, summonPost(t, f, body), http.StatusCreated)
	first := f.session("rem-responder-1")

	f.editStatus("rem-responder-1", v1alpha1.PhaseFailed)
	f.now = f.now.Add(time.Minute)
	second := summonPost(t, f, body)
	wantStatus(t, second, http.StatusCreated)
	if got := decode[apiv1.Session](t, second).Name; got != "rem-responder-1-2" {
		t.Fatalf("re-fire name %s", got)
	}
	s2 := f.session("rem-responder-1-2")
	if s2.Spec.Summon.DispatchID == first.Spec.Summon.DispatchID {
		t.Fatal("the re-fire reused the id")
	}
	if f.session("rem-responder-1").UID != first.UID {
		t.Fatal("the earlier record was replaced")
	}
	if e := f.ledger(); len(e.Charges) != 2 {
		t.Fatalf("charges %+v", e.Charges)
	}

	// A terminal outcome ends it too, though the phase is still Running.
	f.editStatus("rem-responder-1-2", v1alpha1.PhaseRunning)
	s2 = f.session("rem-responder-1-2")
	at := metav1.NewTime(f.now)
	s2.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeDone, At: &at}
	if err := f.c.Status().Update(context.Background(), s2); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	third := summonPost(t, f, body)
	wantStatus(t, third, http.StatusCreated)
	if got := decode[apiv1.Session](t, third).Name; got != "rem-responder-1-3" {
		t.Fatalf("second re-fire name %s", got)
	}
	ids := map[string]bool{}
	for _, s := range f.summonedSessions() {
		ids[s.Spec.Summon.DispatchID] = true
	}
	if len(ids) != 3 || len(f.ledger().Charges) != 3 {
		t.Fatalf("ids %v charges %+v", ids, f.ledger().Charges)
	}

	// The third is unfinished: its key returns it.
	f.now = f.now.Add(time.Minute)
	again := summonPost(t, f, body)
	wantStatus(t, again, http.StatusOK)
	if got := decode[apiv1.Session](t, again).Name; got != "rem-responder-1-3" {
		t.Fatalf("repeat returned %s", got)
	}
	if len(f.ledger().Charges) != 3 {
		t.Fatal("the repeat charged")
	}
}

// Proof 6: an urgent create refused by createsPerHour, by its pool, and by
// both is counted once each time, with no charge and no session. A refused
// normal create is not counted.
func TestSummonLedgerUrgentRefusalsCountedOnce(t *testing.T) {
	cases := []struct {
		name                string
		concurrent, perHour int32
	}{
		{"createsPerHour", 4, 1},
		{"pool", 1, 6},
		{"both", 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := summonFixture(t, ledgerPolicy(tc.concurrent, tc.perHour))
			wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
			for i := 1; i <= 2; i++ {
				f.now = f.now.Add(time.Minute)
				wantError(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
				e := f.ledger()
				if e.UrgentRefusals != int64(i) || e.LastUrgentRefusalAt == nil || !e.LastUrgentRefusalAt.Equal(f.now) {
					t.Fatalf("refusal %d: count %d last %v", i, e.UrgentRefusals, e.LastUrgentRefusalAt)
				}
				if len(e.Charges) != 1 || len(f.summonedSessions()) != 1 {
					t.Fatalf("refusal %d charged %+v or created", i, e.Charges)
				}
			}
		})
	}

	t.Run("normal is not counted", func(t *testing.T) {
		f := summonFixture(t)
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
		version := f.ledgerVersion()
		up := map[string]any{"name": "wo-1", "mode": "remote", "lane": "upgrade", "idempotencyKey": "wo-1"}
		wantError(t, summonPost(t, f, up), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		if f.ledgerVersion() != version || f.ledger().UrgentRefusals != 0 {
			t.Fatal("a refused normal create wrote the ledger")
		}
	})

	t.Run("escalations are their own pool", func(t *testing.T) {
		f := summonFixture(t) // concurrent 1, escalations 2
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
		for _, name := range []string{"esc-1", "esc-2"} {
			w := summonPost(t, f, map[string]any{"name": name, "mode": "remote", "lane": "escalation"})
			wantStatus(t, w, http.StatusCreated)
		}
		w := summonPost(t, f, map[string]any{"name": "esc-3", "mode": "remote", "lane": "escalation"})
		wantError(t, w, http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		if e := f.ledger(); e.UrgentRefusals != 1 || len(e.Charges) != 3 {
			t.Fatalf("ledger %+v", e)
		}
		// Ending an escalation frees its pool, not the remediation's.
		f.editStatus("esc-1", v1alpha1.PhaseFailed)
		wantStatus(t, summonPost(t, f, map[string]any{"name": "esc-3", "mode": "remote", "lane": "escalation"}), http.StatusCreated)
		wantError(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	})
}

// Proof 7: a repeated key with other data returns the same session with 200,
// charges nothing and leaves the brief as it was. Another mode, lane or name
// under the key is 409 and charges nothing. A name held by a session with
// another key, or by any session for a keyless create, is 409.
func TestSummonRepeatedKeyKeepsTheSession(t *testing.T) {
	f := summonFixture(t)
	first := keyedRemediation("rem-responder-1", "alert-1")
	first["data"] = map[string]string{"alert": "disk full"}
	wantStatus(t, summonPost(t, f, first), http.StatusCreated)
	sess := f.session("rem-responder-1")
	version := f.ledgerVersion()

	f.now = f.now.Add(time.Minute)
	fresher := keyedRemediation("rem-responder-1", "alert-1")
	fresher["data"] = map[string]string{"alert": "disk full, fresher text", "diagnosis": "x"}
	w := summonPost(t, f, fresher)
	wantStatus(t, w, http.StatusOK)
	if got := decode[apiv1.Session](t, w); got.UID != string(sess.UID) {
		t.Fatalf("repeat returned %s", got.UID)
	}
	if f.session("rem-responder-1").Spec.Prompt != sess.Spec.Prompt || strings.Contains(f.session("rem-responder-1").Spec.Prompt, "fresher") {
		t.Fatal("the repeat changed the brief")
	}

	for _, body := range []map[string]any{
		keyedRemediation("rem-responder-2", "alert-1"),
		{"name": "esc-1", "mode": "remote", "lane": "escalation", "idempotencyKey": "alert-1"},
		{"name": "wo-1", "mode": "remote", "lane": "upgrade", "idempotencyKey": "alert-1"},
		keyedRemediation("rem-responder-1", "alert-2"),
		remediationBody("rem-responder-1", nil),
	} {
		wantError(t, summonPost(t, f, body), http.StatusConflict, apiv1.CodeConflict)
	}
	if f.ledgerVersion() != version || len(f.summonedSessions()) != 1 {
		t.Fatal("a repeat or a conflict wrote the ledger or a session")
	}
}

// Proof 8: a request refused with 422, 403 or 409 leaves no charge.
func TestSummonRefusalsLeaveNoCharge(t *testing.T) {
	f := summonFixture(t)
	version := f.ledgerVersion()
	refusals := []struct {
		body   map[string]any
		status int
	}{
		{map[string]any{"name": "rem-responder-1", "mode": "task", "lane": "remediation", "prompt": "x"}, http.StatusUnprocessableEntity},
		{map[string]any{"name": "rem-responder-1", "mode": "task", "lane": "remediation", "data": map[string]string{"secret": "x"}}, http.StatusUnprocessableEntity},
		{map[string]any{"name": "other-1", "mode": "task", "lane": "remediation"}, http.StatusUnprocessableEntity},
		{map[string]any{"name": "rem-responder-1", "mode": "task", "lane": "remediation", "idempotencyKey": "not a label"}, http.StatusUnprocessableEntity},
		{map[string]any{"name": "esc-rem-1", "mode": "remote", "lane": "escalation"}, http.StatusForbidden},
	}
	for _, r := range refusals {
		wantStatus(t, summonPost(t, f, r.body), r.status)
	}
	if f.ledgerVersion() != version {
		t.Fatal("a refused request wrote the ledger")
	}
	wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
	version = f.ledgerVersion()
	wantError(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-1")), http.StatusConflict, apiv1.CodeConflict)
	if f.ledgerVersion() != version || len(f.ledger().Charges) != 1 {
		t.Fatal("a 409 wrote the ledger")
	}
}

// Proof 9 is the store's create hook; this shows the hook refuses a session
// whose id is not charged, so a passing suite means none was created.
func TestSummonFixtureRefusesAnUnchargedSession(t *testing.T) {
	f := summonFixture(t)
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "rem-responder-x"},
		Spec:       v1alpha1.AgentSessionSpec{Caller: ledgerTestKey, Summon: &v1alpha1.SessionSummon{DispatchID: "not-charged"}},
	}
	if err := checkChargedAtCreate(context.Background(), f.c, sess); err == nil {
		t.Fatal("an uncharged id passed the hook")
	}
}

// The ledger is required: missing, unreadable or immutable refuses a
// summoning create with 503 and writes no session. A repeat of a key whose
// session exists creates nothing and does not read the ledger.
func TestSummonLedgerRequired(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		f := summonFixture(t)
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
		if err := f.c.Delete(context.Background(), summonLedgerObject()); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusOK)
		// An urgent create past its pool cannot be counted without the ledger.
		wantError(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
		// A normal one past its pool is not counted, so it needs no ledger.
		wantError(t, summonPost(t, f, map[string]any{"name": "wo-1", "mode": "remote", "lane": "upgrade"}), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		f.editStatus("rem-responder-1", v1alpha1.PhaseFailed)
		wantError(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
		wantError(t, summonPost(t, f, map[string]any{"name": "esc-1", "mode": "remote", "lane": "escalation"}), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
		if n := len(f.summonedSessions()); n != 1 {
			t.Fatalf("%d sessions", n)
		}
	})
	for _, tc := range []struct {
		name string
		edit func(*corev1.ConfigMap)
	}{
		{"unreadable entry", func(cm *corev1.ConfigMap) { cm.Data = map[string]string{ledgerTestKey: "{not json"} }},
		{"wrong schema", func(cm *corev1.ConfigMap) {
			cm.Data = map[string]string{ledgerTestKey: `{"schema":"other","charges":[]}`}
		}},
		{"immutable", func(cm *corev1.ConfigMap) { yes := true; cm.Immutable = &yes }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm := summonLedgerObject()
			tc.edit(cm)
			f := summonFixture(t, summonTestPolicy(), cm)
			wantError(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
			if n := len(f.summonedSessions()); n != 0 {
				t.Fatalf("%d sessions", n)
			}
		})
	}
}

// A keyless create is never a repeat: each gets its own random id and charge.
func TestSummonKeylessIsNeverARepeat(t *testing.T) {
	f := summonFixture(t)
	for _, name := range []string{"esc-1", "esc-2"} {
		wantStatus(t, summonPost(t, f, map[string]any{"name": name, "mode": "remote", "lane": "escalation"}), http.StatusCreated)
	}
	a, b := f.session("esc-1").Spec.Summon.DispatchID, f.session("esc-2").Spec.Summon.DispatchID
	if a == b || len(f.ledger().Charges) != 2 {
		t.Fatalf("ids %s %s charges %+v", a, b, f.ledger().Charges)
	}
}

// A key's other sessions and a name taken during the create. A name another
// request took after the check is a clash when its id differs: the create
// takes the next suffix under the same charge.
func TestSummonNameTakenDuringCreateTakesTheNextSuffix(t *testing.T) {
	f := summonFixture(t)
	taken := false
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Name == "rem-responder-1" && !taken {
			taken = true
			other := &v1alpha1.AgentSession{
				ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "rem-responder-1"},
				Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: humanSA},
			}
			if err := c.Create(ctx, other); err != nil {
				return err
			}
		}
		return c.Create(ctx, obj, opts...)
	}})
	w := summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1"))
	wantStatus(t, w, http.StatusCreated)
	if got := decode[apiv1.Session](t, w).Name; got != "rem-responder-1-2" {
		t.Fatalf("name %s", got)
	}
	if len(f.ledger().Charges) != 1 {
		t.Fatalf("charges %+v", f.ledger().Charges)
	}
}

// The suffix walk keeps the 63-character bound and stops at -99.
func TestSummonSuffixesEndAtNinetyNine(t *testing.T) {
	f := summonFixture(t)
	long := "rem-responder-" + strings.Repeat("a", 46)
	body := keyedRemediation(long, "alert-1")
	wantStatus(t, summonPost(t, f, body), http.StatusCreated)
	f.editStatus(long, v1alpha1.PhaseFailed)
	f.now = f.now.Add(time.Minute)
	w := summonPost(t, f, body)
	wantStatus(t, w, http.StatusCreated)
	if got := decode[apiv1.Session](t, w).Name; got != long+"-2" || len(got) != 62 {
		t.Fatalf("name %s", got)
	}
	// Every suffix to -99 held by other sessions: no free name, and no charge.
	f2 := summonFixture(t)
	wantStatus(t, summonPost(t, f2, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
	f2.editStatus("rem-responder-1", v1alpha1.PhaseFailed)
	for i := 2; i <= 99; i++ {
		other := &v1alpha1.AgentSession{
			ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "rem-responder-1-" + itoa(i)},
			Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: humanSA},
		}
		if err := f2.c.Create(context.Background(), other); err != nil {
			t.Fatal(err)
		}
	}
	version := f2.ledgerVersion()
	f2.now = f2.now.Add(time.Minute)
	wantError(t, summonPost(t, f2, keyedRemediation("rem-responder-1", "alert-1")), http.StatusConflict, apiv1.CodeConflict)
	if f2.ledgerVersion() != version {
		t.Fatal("the refusal wrote the ledger")
	}
}

// The ledger keeps fields it does not know, so a newer replica's field
// survives an older replica's write during a rollout.
func TestSummonLedgerKeepsUnknownFields(t *testing.T) {
	cm := summonLedgerObject()
	cm.Data = map[string]string{
		ledgerTestKey:  `{"schema":"dev-env.summon-creates.v1","charges":[],"urgentRefusals":2,"pagedRefusals":1}`,
		"other.caller": `{"schema":"dev-env.summon-creates.v1","charges":[]}`,
	}
	f := summonFixture(t, summonTestPolicy(), cm)
	wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
	_, got, err := readLedgerConfigMap(context.Background(), f.c, ledgerTestKey)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got.Data[ledgerTestKey]), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["pagedRefusals"]) != "1" || string(raw["urgentRefusals"]) != "2" {
		t.Fatalf("entry %s", got.Data[ledgerTestKey])
	}
	if got.Data["other.caller"] != cm.Data["other.caller"] {
		t.Fatal("another caller's key changed")
	}
}
