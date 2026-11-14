package apiserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/keeper"
	"github.com/thaynes43/dev-env/internal/sessionraise"
)

func (f *fixture) editRaiseRow(uid string, edit func(*sessionraise.SessionEntry)) {
	f.t.Helper()
	err := f.srv.commitLedger(context.Background(), f.srv.Raise.filled(), func(led *sessionraise.Ledger) (bool, error) {
		row := led.Find(uid)
		if row == nil {
			f.t.Fatal("missing row")
		}
		edit(row)
		return true, nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) raiseSent(uid string) {
	f.t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		f.t.Fatal(err)
	}
	f.publishKeeper(pub)
	row := f.ledgerEntry(uid)
	n, err := sessionraise.SignNotice(sessionraise.Notice{ID: row.ID, SessionUID: uid, Kind: sessionraise.KindRaise, Attempt: 1, Outcome: sessionraise.OutcomeSent, At: row.PerformedAt, SentAt: row.PerformedAt}, priv)
	if err != nil {
		f.t.Fatal(err)
	}
	f.putNotice(n)
	at, err := time.Parse(time.RFC3339, row.PerformedAt)
	if err != nil {
		f.t.Fatal(err)
	}
	f.now = at.Add(sessionraise.EffectDelay)
}

func TestLedgerAloneNeverRaisesThroughTheRoutes(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	target := f.remoteTarget("target", 2)
	targetToken := "tok-session-target"
	body := f.raiseReq(string(target.UID), "", "allow the target to start workers", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), f.workbench("bench"), body); w.Code != http.StatusAccepted {
		t.Fatalf("POST %d %s", w.Code, w.Body.String())
	}
	before := f.session("target").Annotations[v1alpha1.AnnotationRaise]
	f.editRaiseRow(string(target.UID), func(row *sessionraise.SessionEntry) { row.State = sessionraise.StateActive })
	// A public key exists, but there is no Sent.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.publishKeeper(pub)
	f.srv.activateRaises(context.Background())
	if got := f.session("target").Annotations[v1alpha1.AnnotationRaise]; got != before {
		t.Fatal("ledger-only active state was signed")
	}
	c := &caller{kind: kindSession, depth: 3, parent: target.Name, session: target}
	if f.srv.depthExempt(context.Background(), c) {
		t.Fatal("ledger-only active state grants depth exemption")
	}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, targetToken, task()); w.Code != http.StatusForbidden {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
}

func TestActivatorNeverRevivesATerminalAnnotation(t *testing.T) {
	for _, pair := range [][2]string{{sessionraise.StateTakenBack, sessionraise.StateActive}, {sessionraise.StateTakenBack, sessionraise.StatePending}, {sessionraise.StateUnannounced, sessionraise.StateActive}, {sessionraise.StateUnannounced, sessionraise.StatePending}, {sessionraise.StateActive, sessionraise.StatePending}, {sessionraise.StateActive, sessionraise.StateUnannounced}} {
		t.Run(pair[0]+"-to-"+pair[1], func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			f.sessionPod("target", "full", 2)
			f.putPending(f.session("target"))
			uid := string(f.session("target").UID)
			f.raiseSent(uid)
			f.resign("target", pair[0], "2", true)
			before := f.session("target").Annotations[v1alpha1.AnnotationRaise]
			f.editRaiseRow(uid, func(row *sessionraise.SessionEntry) { row.State = pair[1] })
			f.srv.activateRaises(context.Background())
			if got := f.session("target").Annotations[v1alpha1.AnnotationRaise]; got != before {
				t.Fatal("invalid transition re-signed")
			}
			if f.srv.depthExempt(context.Background(), &caller{kind: kindSession, depth: 3, session: f.session("target")}) {
				t.Fatal("mismatched record counts")
			}
		})
	}
}

func TestActivatorRejectsEditedGrantFields(t *testing.T) {
	edits := map[string]func(*sessionraise.SessionEntry){
		"id":            func(r *sessionraise.SessionEntry) { r.ID = "other" },
		"uid":           func(r *sessionraise.SessionEntry) { r.UID = "other" },
		"profile":       func(r *sessionraise.SessionEntry) { r.Profile = "workbench" },
		"fromProfile":   func(r *sessionraise.SessionEntry) { r.FromProfile = "other" },
		"startSessions": func(r *sessionraise.SessionEntry) { r.StartSessions = false },
		"depth":         func(r *sessionraise.SessionEntry) { r.Depth = "1" },
		"wordsHash":     func(r *sessionraise.SessionEntry) { r.WordsHash = "other" },
		"where":         func(r *sessionraise.SessionEntry) { r.Where = sessionraise.WhereChat },
		"ownerSession":  func(r *sessionraise.SessionEntry) { r.OwnerSession = "other" },
		"saidAt":        func(r *sessionraise.SessionEntry) { r.SaidAt = "other" },
		"ref":           func(r *sessionraise.SessionEntry) { r.Ref = "op:other" },
		"performedBy":   func(r *sessionraise.SessionEntry) { r.PerformedBy = "human/other" },
		"performedAt":   func(r *sessionraise.SessionEntry) { r.PerformedAt = "2026-01-01T00:00:00Z" },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			f.sessionPod("target", "full", 2)
			f.putPending(f.session("target"))
			uid := string(f.session("target").UID)
			f.raiseSent(uid)
			before := f.session("target").Annotations[v1alpha1.AnnotationRaise]
			f.editRaiseRow(uid, func(row *sessionraise.SessionEntry) { row.State = sessionraise.StateActive; edit(row) })
			f.srv.activateRaises(context.Background())
			if got := f.session("target").Annotations[v1alpha1.AnnotationRaise]; got != before {
				t.Fatal("edited grant was signed")
			}
		})
	}
}

func TestFinishWithoutAnnotationRepairsUsingSentEvidence(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("target", "full", 2)
	f.putPending(f.session("target"))
	uid := string(f.session("target").UID)
	f.raiseSent(uid)
	inner := f.c
	f.srv.Client = sessionHook{Client: inner, hook: func(obj client.Object) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok {
			return context.DeadlineExceeded
		}
		return nil
	}}
	f.srv.activateRaises(context.Background())
	if f.ledgerEntry(uid).State != sessionraise.StateActive || annotationState(*f.session("target")) != sessionraise.StatePending {
		t.Fatal("did not leave Finish/annotation split")
	}
	f.srv.Client = inner
	f.srv.activateRaises(context.Background())
	rec, err := sessionraise.Parse(f.session("target").Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != sessionraise.StateActive || rec.ActiveFrom != f.now.Format(time.RFC3339) {
		t.Fatalf("repair state %s from %s", rec.State, rec.ActiveFrom)
	}
}

func TestDepthExemptionRequiresAnElapsedActiveFrom(t *testing.T) {
	for _, at := range []string{"", "2026-11-14T20:00:00Z"} {
		t.Run(at, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			f.sessionPod("target", "full", 2)
			f.putActive(f.session("target"), true)
			f.rewriteAnnotation("target", func(r *sessionraise.Record) { r.ActiveFrom = at })
			if f.srv.depthExempt(context.Background(), &caller{kind: kindSession, depth: 3, session: f.session("target")}) {
				t.Fatal("missing or future activeFrom counted")
			}
		})
	}
}

func TestActiveRepairRequiresTheFullSentEvidence(t *testing.T) {
	for _, mode := range []string{"unsigned", "wrong-id", "wrong-uid", "take-back", "before-raise", "delay"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			f.sessionPod("target", "full", 2)
			f.putPending(f.session("target"))
			uid := string(f.session("target").UID)
			row := f.ledgerEntry(uid)
			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			f.publishKeeper(pub)
			n := sessionraise.Notice{ID: row.ID, SessionUID: uid, Kind: sessionraise.KindRaise, Attempt: 1, Outcome: sessionraise.OutcomeSent, At: row.PerformedAt, SentAt: row.PerformedAt}
			switch mode {
			case "wrong-id":
				n.ID = "other"
			case "wrong-uid":
				n.SessionUID = "other"
			case "take-back":
				n.Kind = sessionraise.KindTakeBack
			case "before-raise":
				n.SentAt = f.now.Add(-time.Second).Format(time.RFC3339)
			}
			n, err = sessionraise.SignNotice(n, priv)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unsigned" {
				n.Sig = ""
			}
			f.putNotice(n)
			if mode != "delay" {
				f.now = f.now.Add(sessionraise.EffectDelay)
			}
			f.editRaiseRow(uid, func(row *sessionraise.SessionEntry) { row.State = sessionraise.StateActive })
			before := f.session("target").Annotations[v1alpha1.AnnotationRaise]
			f.srv.activateRaises(context.Background())
			if f.session("target").Annotations[v1alpha1.AnnotationRaise] != before {
				t.Fatal("active repair accepted incomplete evidence")
			}
		})
	}
}

func TestReview4LedgerOnlyTakeBackBeforeWrite(t *testing.T) {
	for _, path := range []string{"repair", "activate", "unannounce"} {
		t.Run(path, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			f.sessionPod("target", "full", 2)
			f.putPending(f.session("target"))
			uid := string(f.session("target").UID)
			wantWritten := sessionraise.StateActive
			if path == "unannounce" {
				wantWritten = sessionraise.StateUnannounced
				pub, priv, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				f.publishKeeper(pub)
				row := f.ledgerEntry(uid)
				n, err := sessionraise.SignNotice(sessionraise.Notice{ID: row.ID, SessionUID: uid, Kind: sessionraise.KindRaise, Attempt: sessionraise.NoticeAttempts, Outcome: sessionraise.OutcomeFailed, At: row.PerformedAt}, priv)
				if err != nil {
					t.Fatal(err)
				}
				f.putNotice(n)
			} else {
				f.raiseSent(uid)
			}
			if path == "repair" {
				f.editRaiseRow(uid, func(r *sessionraise.SessionEntry) { r.State = sessionraise.StateActive })
			}
			fired := false
			f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
				sess, ok := obj.(*v1alpha1.AgentSession)
				if !ok || fired || annotationState(*sess) != wantWritten {
					return nil
				}
				fired = true
				// Only the ledger moves. The annotation resourceVersion is unchanged,
				// so the activator's write succeeds and must be followed in this pass.
				return f.srv.commitLedger(context.Background(), f.srv.Raise.filled(), func(l *sessionraise.Ledger) (bool, error) {
					_, err := l.TakeBack(uid, "human/"+humanSA, f.now.Format(time.RFC3339), f.now)
					return err == nil, err
				})
			}}
			f.srv.activateRaises(context.Background())
			if !fired {
				t.Fatal("did not reach the selected write path")
			}
			rec, err := sessionraise.Parse(f.session("target").Annotations[v1alpha1.AnnotationRaise])
			if err != nil {
				t.Fatal(err)
			}
			if f.ledgerEntry(uid).State != sessionraise.StateTakenBack || rec.State != sessionraise.StateTakenBack {
				t.Fatalf("after ONE pass: ledger %s annotation %s", f.ledgerEntry(uid).State, rec.State)
			}
			if rec.TakenBackBy != "human/"+humanSA {
				t.Fatal("take-back fields were lost")
			}
		})
	}
}

func TestActivatorWritesTheDecisionResourceVersion(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	f.sessionPod("target", "full", 2)
	f.putPending(f.session("target"))
	snapshot := f.session("target")
	fresh := snapshot.DeepCopy()
	fresh.Annotations["example.com/concurrent"] = "kept"
	if err := f.c.Update(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	err := f.srv.writeRaiseAnnotationWhen(context.Background(), snapshot, snapshot.Annotations[v1alpha1.AnnotationRaise])
	if !errors.Is(err, errRaiseAnnotationMoved) {
		t.Fatalf("stale decision write returned %v", err)
	}
	if f.session("target").Annotations["example.com/concurrent"] != "kept" {
		t.Fatal("concurrent metadata lost")
	}
}

func (f *fixture) raiseReplica() *fixture {
	f.t.Helper()
	s := &Server{Client: f.c, Live: f.c, Auth: f.auth, Policy: f.srv.Policy, Templates: f.srv.Templates, Raise: f.srv.Raise, Now: f.srv.Now, Log: logr.Discard()}
	return &fixture{t: f.t, c: f.c, auth: f.auth, srv: s, h: s.Handler()}
}

type noticeCommittedClient struct {
	client.Client
	sent chan struct{}
}

func (c *noticeCommittedClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	err := c.Client.Update(ctx, obj, opts...)
	if cm, ok := obj.(*corev1.ConfigMap); ok && err == nil && cm.Name == sessionraise.NoticeLedgerName {
		led, e := sessionraise.UnmarshalNotice([]byte(cm.Data[sessionraise.LedgerField]))
		if e == nil {
			for _, n := range led.Items {
				if n.Outcome == sessionraise.OutcomeSent {
					close(c.sent)
					break
				}
			}
		}
	}
	return err
}

func (f *fixture) announceAndActivateRaise(uid, id string) keeper.Notice {
	f.t.Helper()
	ctx := context.Background()
	if err := f.c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: sessionraise.ObjectNamespace, Name: sessionraise.KeeperSecretName}}); err != nil {
		f.t.Fatal(err)
	}
	sender := &recSender{sent: make(chan struct{}, 1)}
	writer := &noticeCommittedClient{Client: f.c, sent: make(chan struct{})}
	pager := &keeper.RaisePager{Client: writer, Live: f.c, Namespace: sessionraise.ObjectNamespace, SessionNamespace: sessionNS, Sender: sender, Now: f.srv.Now, Log: logr.Discard()}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pager.Start(runCtx) }()
	select {
	case <-writer.sent:
	case <-time.After(3 * time.Second):
		f.t.Fatal("new raise was not announced")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			f.t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		f.t.Fatal("pager did not stop")
	}
	if sender.n != 1 {
		f.t.Fatalf("sent %d notices", sender.n)
	}
	notices, pub, err := f.srv.readNotices(ctx, f.srv.Raise.filled())
	if err != nil {
		f.t.Fatal(err)
	}
	row := f.ledgerEntry(uid)
	if row.ID != id {
		f.t.Fatal("new raise id lost before activation")
	}
	sent, ok := sessionraise.SentTime(row, notices.Items, pub)
	if !ok {
		f.t.Fatal("no verified Sent for new raise")
	}
	at, err := time.Parse(time.RFC3339, sent)
	if err != nil {
		f.t.Fatal(err)
	}
	f.now = at.Add(sessionraise.EffectDelay)
	f.srv.activateRaises(ctx)
	rec, err := sessionraise.Parse(f.session("target").Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		f.t.Fatal(err)
	}
	if rec.ID != id || rec.State != sessionraise.StateActive || f.ledgerEntry(uid).State != sessionraise.StateActive {
		f.t.Fatal("new raise did not activate")
	}
	return sender.last
}

func TestReview4OldDeleteCannotOverwriteANewerRaise(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	uid := string(target.UID)
	a := f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, a); w.Code != http.StatusAccepted {
		t.Fatalf("A POST %d %s", w.Code, w.Body.String())
	}
	oldID := f.ledgerEntry(uid).ID
	f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
		if s, ok := obj.(*v1alpha1.AgentSession); ok && annotationState(*s) == sessionraise.StateTakenBack {
			return context.DeadlineExceeded
		}
		return nil
	}}
	back := apiv1.TakeBackRequest{ExpectUID: uid}
	if w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, back); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("partial DELETE %d %s", w.Code, w.Body.String())
	}
	f.now = f.now.Add(61 * time.Second)
	replica := f.raiseReplica()
	fired := false
	newID := ""
	f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
		if s, ok := obj.(*v1alpha1.AgentSession); ok && !fired && annotationState(*s) == sessionraise.StateTakenBack {
			fired = true
			b := f.raiseReq(uid, "", "allow second raise", sessionraise.WherePhone, true)
			w := replica.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, b)
			if w.Code != http.StatusAccepted {
				t.Fatalf("B POST %d %s", w.Code, w.Body.String())
			}
			newID = f.ledgerEntry(uid).ID
		}
		return nil
	}}
	if w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, back); w.Code != http.StatusOK {
		t.Fatalf("DELETE retry %d %s", w.Code, w.Body.String())
	}
	if !fired || oldID == newID || annotationState(*f.session("target")) != sessionraise.StatePending {
		t.Fatal("B annotation overwritten by A DELETE retry")
	}
	f.srv.Client = f.c
	f.announceAndActivateRaise(uid, newID)
}

func TestReview4OldPostCannotOverwriteANewerRaise(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	uid := string(target.UID)
	replica := f.raiseReplica()
	fired := false
	newID := ""
	f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
		if s, ok := obj.(*v1alpha1.AgentSession); ok && !fired && annotationState(*s) == sessionraise.StatePending {
			fired = true
			w := replica.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: uid})
			if w.Code != http.StatusOK {
				t.Fatalf("A DELETE %d %s", w.Code, w.Body.String())
			}
			f.now = f.now.Add(61 * time.Second)
			w = replica.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, f.raiseReq(uid, "", "allow second raise", sessionraise.WherePhone, true))
			if w.Code != http.StatusAccepted {
				t.Fatalf("B POST %d %s", w.Code, w.Body.String())
			}
			newID = f.ledgerEntry(uid).ID
		}
		return nil
	}}
	w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("delayed A POST %d %s", w.Code, w.Body.String())
	}
	rec, err := sessionraise.Parse(f.session("target").Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	if !fired || rec.ID != newID || rec.State != sessionraise.StatePending {
		t.Fatal("B annotation overwritten by delayed A POST")
	}
	f.srv.Client = f.c
	f.announceAndActivateRaise(uid, newID)
}

func TestReview4AmbiguousDeleteAudit(t *testing.T) {
	for _, repair := range []string{"request", "pass"} {
		t.Run(repair, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			tok := f.workbench("bench")
			target := f.remoteTarget("target", 2)
			uid := string(target.UID)
			if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true)); w.Code != http.StatusAccepted {
				t.Fatalf("POST %d %s", w.Code, w.Body.String())
			}
			sink := &lineSink{}
			f.srv.Log = logr.New(sink)
			applied := false
			f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
				cm, ok := obj.(*corev1.ConfigMap)
				if !ok || cm.Name != sessionraise.LedgerName || applied {
					return nil
				}
				applied = true
				if err := f.c.Update(context.Background(), cm); err != nil {
					return err
				}
				return context.DeadlineExceeded
			}}
			back := apiv1.TakeBackRequest{ExpectUID: uid}
			if w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, back); w.Code != http.StatusServiceUnavailable {
				t.Fatalf("ambiguous DELETE %d %s", w.Code, w.Body.String())
			}
			if !applied || f.ledgerEntry(uid).State != sessionraise.StateTakenBack {
				t.Fatal("ledger write was not applied")
			}
			f.srv.Client = f.c
			if repair == "request" {
				if w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, back); w.Code != http.StatusOK {
					t.Fatalf("retry %d %s", w.Code, w.Body.String())
				}
			} else {
				pub, _, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				f.publishKeeper(pub)
				f.srv.activateRaises(context.Background())
			}
			if annotationState(*f.session("target")) != sessionraise.StateTakenBack {
				t.Fatal("annotation not repaired")
			}
			if n := strings.Count(strings.Join(sink.msgs, "\n"), "took back a raise of target"); n != 1 {
				t.Fatalf("take-back audit lines %d, want one: %v", n, sink.msgs)
			}
			// A settled retry is silent in this operator process too.
			if w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, back); w.Code != http.StatusOK {
				t.Fatalf("settled retry %d", w.Code)
			}
			if n := strings.Count(strings.Join(sink.msgs, "\n"), "took back a raise of target"); n != 1 {
				t.Fatalf("duplicate audit line: %v", sink.msgs)
			}
		})
	}
}

func TestReview4SuccessfulPostRetryAuditAcrossReplicas(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	uid := string(target.UID)
	sink := &lineSink{}
	f.srv.Log = logr.New(sink)
	body := f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("POST %d %s", w.Code, w.Body.String())
	}
	replica := f.raiseReplica()
	replica.srv.Log = logr.New(sink)
	if w := replica.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusOK {
		t.Fatalf("retry %d %s", w.Code, w.Body.String())
	}
	if n := strings.Count(strings.Join(sink.msgs, "\n"), "Raised target"); n != 1 {
		t.Fatalf("raised audit lines %d, want one: %v", n, sink.msgs)
	}
}

func TestDelayedPostCannotOverwriteTakeBack(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	uid := string(target.UID)
	replica := f.raiseReplica()
	fired := false
	f.srv.Live = round4SessionReadHook{Reader: f.c, before: func() {
		if fired {
			return
		}
		led, err := f.srv.readLedger(context.Background(), f.srv.Raise.filled())
		if err != nil {
			t.Fatal(err)
		}
		if led.Find(uid) == nil {
			return // Earlier target reads precede Admit.
		}
		fired = true
		w := replica.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: uid})
		if w.Code != http.StatusOK {
			t.Fatalf("DELETE %d %s", w.Code, w.Body.String())
		}
	}}
	w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST %d %s", w.Code, w.Body.String())
	}
	// DELETE precedes the first pending annotation. It has no verified grant
	// to sign taken-back; the ledger revocation stands, and POST must leave
	// the annotation absent when it reads that changed state.
	if !fired || f.session("target").Annotations[v1alpha1.AnnotationRaise] != "" || f.ledgerEntry(uid).State != sessionraise.StateTakenBack {
		t.Fatal("delayed POST overwrote take-back of the same id")
	}
}

func TestSameBodyRetryNeverSignsALedgerOnlyActiveGrant(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	uid := string(target.UID)
	body := f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("POST %d", w.Code)
	}
	before := f.session("target").Annotations[v1alpha1.AnnotationRaise]
	f.editRaiseRow(uid, func(r *sessionraise.SessionEntry) {
		r.State = sessionraise.StateActive
		r.ActiveFrom = f.now.Add(-time.Minute).Format(time.RFC3339)
	})
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusOK {
		t.Fatalf("retry %d %s", w.Code, w.Body.String())
	}
	if f.session("target").Annotations[v1alpha1.AnnotationRaise] != before {
		t.Fatal("same-body POST signed ledger-only active grant")
	}
	if f.srv.depthExempt(context.Background(), &caller{kind: kindSession, depth: 3, session: f.session("target")}) {
		t.Fatal("same-body POST granted depth exemption without Sent")
	}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, "tok-session-target", task()); w.Code != http.StatusForbidden {
		t.Fatalf("child create %d %s", w.Code, w.Body.String())
	}
}

func TestSameBodyRetryNeverRevivesATerminalAnnotation(t *testing.T) {
	for _, state := range []string{sessionraise.StateTakenBack, sessionraise.StateUnannounced} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			tok := f.workbench("bench")
			target := f.remoteTarget("target", 2)
			uid := string(target.UID)
			body := f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true)
			if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
				t.Fatalf("POST %d", w.Code)
			}
			f.raiseSent(uid)
			if state == sessionraise.StateTakenBack {
				if w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: uid}); w.Code != http.StatusOK {
					t.Fatalf("DELETE %d", w.Code)
				}
			} else {
				f.editRaiseRow(uid, func(r *sessionraise.SessionEntry) { r.State = state })
				f.rewriteAnnotation("target", func(r *sessionraise.Record) { r.State = state })
			}
			before := f.session("target").Annotations[v1alpha1.AnnotationRaise]
			f.editRaiseRow(uid, func(r *sessionraise.SessionEntry) { r.State = sessionraise.StatePending })
			if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusOK {
				t.Fatalf("retry %d %s", w.Code, w.Body.String())
			}
			f.srv.activateRaises(context.Background())
			if f.session("target").Annotations[v1alpha1.AnnotationRaise] != before {
				t.Fatal("same-body POST revived terminal grant with an old Sent")
			}
		})
	}
}

func TestMissingPendingAnnotationRepairMatchesTheAuthorizedRequest(t *testing.T) {
	for _, field := range []string{"unchanged", "depth", "fromProfile", "ownerSession", "saidAt", "ref", "performedAt"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			tok := f.workbench("bench")
			target := f.remoteTarget("target", 2)
			uid := string(target.UID)
			body := f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true)
			f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
				if _, ok := obj.(*v1alpha1.AgentSession); ok {
					return context.DeadlineExceeded
				}
				return nil
			}}
			if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusServiceUnavailable {
				t.Fatalf("partial POST %d", w.Code)
			}
			f.srv.Client = f.c
			f.editRaiseRow(uid, func(r *sessionraise.SessionEntry) {
				switch field {
				case "depth":
					r.Depth = "1"
				case "fromProfile":
					r.FromProfile = "other"
				case "ownerSession":
					r.OwnerSession = "other"
				case "saidAt":
					r.SaidAt = f.now.Add(-2 * time.Minute).Format(time.RFC3339)
				case "ref":
					r.Ref = "op:changed"
				case "performedAt":
					r.PerformedAt = f.now.Add(-time.Minute).Format(time.RFC3339)
				}
			})
			if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusOK {
				t.Fatalf("retry %d %s", w.Code, w.Body.String())
			}
			raw := f.session("target").Annotations[v1alpha1.AnnotationRaise]
			if field == "unchanged" {
				if raw == "" {
					t.Fatal("authorized pending recovery failed")
				}
			} else if raw != "" {
				t.Fatal("pending retry signed edited ledger grant")
			}
		})
	}
}

func TestPostTitleRedactionUsesKeeperFallback(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	uid := string(target.UID)
	target.Spec.Title = strings.TrimSpace(strings.Repeat("a.io ", 9))
	if err := f.c.Update(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	saidAt := f.now.Add(-time.Minute).Format(time.RFC3339)
	words := ""
	// A small deterministic string search chooses a request that fits the
	// placeholder but whose legal title expands past it under redaction.
	for n := 20; n <= 55; n++ {
		candidate := strings.TrimSpace(strings.Repeat("a.io ", n))
		fits, err := sessionraise.NoticeFits(target.Name, "session/bench", sessionraise.WherePhone, saidAt, candidate, keeper.NoticeRedact)
		if err != nil {
			t.Fatal(err)
		}
		text := sessionraise.NoticeText{Session: target.Name, Title: target.Spec.Title, Performer: "session/bench", Words: candidate, Where: sessionraise.WherePhone, SaidAt: saidAt, Effect: f.now.Add(sessionraise.EffectDelay).Format(time.RFC3339)}
		_, body, err := sessionraise.Message(text, keeper.NoticeRedact)
		if err != nil {
			t.Fatal(err)
		}
		if fits && len(body) > sessionraise.NoticeBodyMax {
			words = candidate
			break
		}
	}
	if words == "" {
		t.Fatal("no title expansion fixture found")
	}
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, f.raiseReq(uid, "", words, sessionraise.WherePhone, true)); w.Code != http.StatusAccepted {
		t.Fatalf("POST %d %s", w.Code, w.Body.String())
	}
	notice := f.announceAndActivateRaise(uid, f.ledgerEntry(uid).ID)
	if len(notice.Message) > sessionraise.NoticeBodyMax || strings.Contains(notice.Message, "Its title is") {
		t.Fatalf("keeper did not use the bounded fallback (%d bytes)", len(notice.Message))
	}
	if !strings.Contains(notice.Message, keeper.NoticeRedact(words)) {
		t.Fatal("fallback cut the claimed words")
	}
}

func TestReplacementPendingRaiseRecoversAfterAnnotationFailure(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	uid := string(target.UID)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true)); w.Code != http.StatusAccepted {
		t.Fatalf("A POST %d", w.Code)
	}
	if w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: uid}); w.Code != http.StatusOK {
		t.Fatalf("A DELETE %d", w.Code)
	}
	f.now = f.now.Add(61 * time.Second)
	body := f.raiseReq(uid, "", "allow second raise", sessionraise.WherePhone, true)
	f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok {
			return context.DeadlineExceeded
		}
		return nil
	}}
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("B partial POST %d", w.Code)
	}
	id := f.ledgerEntry(uid).ID
	if annotationState(*f.session("target")) != sessionraise.StateTakenBack {
		t.Fatal("expected previous annotation to remain")
	}
	f.srv.Client = f.c
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusOK {
		t.Fatalf("B retry %d %s", w.Code, w.Body.String())
	}
	rec, err := sessionraise.Parse(f.session("target").Annotations[v1alpha1.AnnotationRaise])
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != id || rec.State != sessionraise.StatePending {
		t.Fatal("replacement pending annotation was stranded")
	}
	f.announceAndActivateRaise(uid, id)
}

type round4SessionReadHook struct {
	client.Reader
	before func()
}

func (h round4SessionReadHook) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*v1alpha1.AgentSession); ok && key.Name == "target" {
		h.before()
	}
	return h.Reader.Get(ctx, key, obj, opts...)
}

func TestPendingRequestChecksEveryAttemptSnapshot(t *testing.T) {
	for _, point := range []string{"helper-read", "conflict-retry", "historical-helper-read", "historical-conflict-retry"} {
		t.Run(point, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			tok := f.workbench("bench")
			target := f.remoteTarget("target", 2)
			uid := string(target.UID)
			body := f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true)
			replica := f.raiseReplica()
			fired := false
			terminal := ""
			attack := func() {
				if fired {
					return
				}
				led, err := f.srv.readLedger(context.Background(), f.srv.Raise.filled())
				if err != nil {
					t.Fatal(err)
				}
				if led.Find(uid) == nil {
					return
				} // Earlier target reads precede Admit.
				fired = true
				original := *led.Find(uid)
				if w := replica.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusOK {
					t.Fatalf("concurrent POST %d %s", w.Code, w.Body.String())
				}
				f.raiseSent(uid)
				if w := replica.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: uid}); w.Code != http.StatusOK {
					t.Fatalf("DELETE %d", w.Code)
				}
				terminal = f.session("target").Annotations[v1alpha1.AnnotationRaise]
				if strings.HasPrefix(point, "historical-") {
					f.now = f.now.Add(61 * time.Second)
					w := replica.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, f.raiseReq(uid, "", "allow second raise", sessionraise.WherePhone, true))
					if w.Code != http.StatusAccepted {
						t.Fatalf("B POST %d %s", w.Code, w.Body.String())
					}
					terminal = f.session("target").Annotations[v1alpha1.AnnotationRaise]
				}
				// Only the unsigned ledger write fence is missing. Keep the genuine
				// authenticated terminal annotation and the old verified Sent.
				f.editRaiseRow(uid, func(r *sessionraise.SessionEntry) { *r = original })
			}
			if strings.HasSuffix(point, "helper-read") {
				f.srv.Live = round4SessionReadHook{Reader: f.c, before: attack}
			} else {
				f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
					if s, ok := obj.(*v1alpha1.AgentSession); ok && annotationState(*s) == sessionraise.StatePending {
						attack()
					}
					return nil
				}}
			}
			if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
				t.Fatalf("delayed POST %d %s", w.Code, w.Body.String())
			}
			f.srv.Live = f.c
			f.srv.Client = f.c
			f.srv.activateRaises(context.Background())
			if !fired || f.session("target").Annotations[v1alpha1.AnnotationRaise] != terminal {
				t.Fatal("pending request replayed over a terminal or newer snapshot")
			}
			if f.srv.depthExempt(context.Background(), &caller{kind: kindSession, depth: 3, session: f.session("target")}) {
				t.Fatal("replayed old Sent granted an exemption")
			}
		})
	}
}

func TestSameBodyRetryCannotReplayAHistoricalRaise(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	uid := string(target.UID)
	a := f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, a); w.Code != http.StatusAccepted {
		t.Fatalf("A POST %d", w.Code)
	}
	original := f.ledgerEntry(uid)
	f.raiseSent(uid)
	f.srv.activateRaises(context.Background())
	if w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: uid}); w.Code != http.StatusOK {
		t.Fatalf("A DELETE %d", w.Code)
	}
	f.now = f.now.Add(61 * time.Second)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, f.raiseReq(uid, "", "allow second raise", sessionraise.WherePhone, true)); w.Code != http.StatusAccepted {
		t.Fatalf("B POST %d", w.Code)
	}
	newer := f.session("target").Annotations[v1alpha1.AnnotationRaise]
	f.editRaiseRow(uid, func(r *sessionraise.SessionEntry) { *r = original })
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, a); w.Code != http.StatusOK {
		t.Fatalf("A replay %d %s", w.Code, w.Body.String())
	}
	f.srv.activateRaises(context.Background())
	if f.session("target").Annotations[v1alpha1.AnnotationRaise] != newer {
		t.Fatal("historical A replaced newer B and reused A's Sent")
	}
	if f.srv.depthExempt(context.Background(), &caller{kind: kindSession, depth: 3, session: f.session("target")}) {
		t.Fatal("historical replay granted an exemption")
	}
}

func TestTakeBackNeverAuthenticatesAnUnsignedGrantTime(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	uid := string(target.UID)
	body := f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusAccepted {
		t.Fatalf("POST %d", w.Code)
	}
	original := f.ledgerEntry(uid)
	f.raiseSent(uid)
	f.srv.activateRaises(context.Background())
	before := f.session("target").Annotations[v1alpha1.AnnotationRaise]
	f.editRaiseRow(uid, func(r *sessionraise.SessionEntry) {
		r.ID = "0123456789abcdef"
		r.PerformedAt = f.now.Add(-time.Hour).Format(time.RFC3339)
	})
	if w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: uid}); w.Code != http.StatusOK {
		t.Fatalf("DELETE %d", w.Code)
	}
	if f.session("target").Annotations[v1alpha1.AnnotationRaise] != before {
		t.Fatal("take-back authenticated an unsigned ledger grant/time")
	}
	f.editRaiseRow(uid, func(r *sessionraise.SessionEntry) { *r = original })
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, body); w.Code != http.StatusOK {
		t.Fatalf("retry %d", w.Code)
	}
	f.srv.activateRaises(context.Background())
	if f.srv.depthExempt(context.Background(), &caller{kind: kindSession, depth: 3, session: f.session("target")}) {
		t.Fatal("forged take-back metadata enabled reuse of an old Sent")
	}
}

// TestKnownLimit10LedgerRollbackBeforeTakenBackAnnotation documents known
// limit 10, not the installed fence. Section 11 rule 3 (ledger admission)
// closes it. The operator cannot distinguish a rollback of the only unsigned
// revocation evidence before its protected terminal annotation is committed.
func TestKnownLimit10LedgerRollbackBeforeTakenBackAnnotation(t *testing.T) {
	f := newFixture(t)
	f.raiseReady(true)
	tok := f.workbench("bench")
	target := f.remoteTarget("target", 2)
	uid := string(target.UID)
	if w := f.do(http.MethodPost, apiv1.SessionRaisePath("target"), tok, f.raiseReq(uid, "", "allow first raise", sessionraise.WherePhone, true)); w.Code != http.StatusAccepted {
		t.Fatalf("POST %d", w.Code)
	}
	f.raiseSent(uid)
	f.srv.activateRaises(context.Background())
	original := f.ledgerEntry(uid)
	f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok {
			return context.DeadlineExceeded
		}
		return nil
	}}
	if w := f.do(http.MethodDelete, apiv1.SessionRaisePath("target"), tokHuman, apiv1.TakeBackRequest{ExpectUID: uid}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("partial DELETE %d", w.Code)
	}
	f.srv.Client = f.c
	c := &caller{kind: kindSession, depth: 3, session: f.session("target")}
	if f.srv.depthExempt(context.Background(), c) {
		t.Fatal("committed take-back did not stop the active record")
	}
	// Simulate rule 3 missing: only the ledger is rolled back, while the
	// stored annotation is still the genuine active signature.
	f.editRaiseRow(uid, func(r *sessionraise.SessionEntry) { *r = original })
	if !f.srv.depthExempt(context.Background(), c) {
		t.Fatal("known limit 10 fixture no longer demonstrates the documented missing-rule boundary")
	}
}
