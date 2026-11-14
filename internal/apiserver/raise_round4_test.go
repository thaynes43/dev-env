package apiserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
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
