package apiserver

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/sessionraise"
)

func TestPendingRaiseRetryReportsWhenItsRecordCannotBeRecovered(t *testing.T) {
	for _, change := range []string{"podGone", "saidAt", "ref", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			tok := f.workbench("bench")
			target := f.remoteTarget("target", 2)
			uid := string(target.UID)
			path := apiv1.SessionRaisePath(target.Name)
			body := f.raiseReq(uid, "", "allow the target to start workers", sessionraise.WherePhone, true)
			f.srv.Client = sessionHook{Client: f.c, hook: func(obj client.Object) error {
				if _, ok := obj.(*v1alpha1.AgentSession); ok {
					return context.DeadlineExceeded
				}
				return nil
			}}
			wantSentence(t, f.do(http.MethodPost, path, tok, body), http.StatusServiceUnavailable,
				"The raise was recorded but the session annotation could not be written. Send the same request again.")
			before, err := f.srv.readLedger(context.Background(), f.srv.Raise.filled())
			if err != nil || len(before.Words) != 1 || f.ledgerEntry(uid).State != sessionraise.StatePending || f.session(target.Name).Annotations[v1alpha1.AnnotationRaise] != "" {
				t.Fatalf("first POST did not leave one spent word and a missing pending annotation: %v", err)
			}
			switch change {
			case "podGone":
				var pod corev1.Pod
				if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(target), &pod); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
				if f.srv.podProfile(context.Background(), target) == f.ledgerEntry(uid).FromProfile {
					t.Fatal("missing pod did not change fromProfile")
				}
			case "saidAt":
				body.Owner.SaidAt = f.now.Add(-2 * time.Minute).Format(time.RFC3339)
			case "ref":
				body.Owner.Ref = "op:830#retry"
			}
			writes := 0
			f.srv.Client = sessionHook{Client: f.c, hook: func(client.Object) error {
				writes++
				return nil
			}}
			w := f.do(http.MethodPost, path, tok, body)
			if change == "unchanged" {
				if w.Code != http.StatusOK || annotationState(*f.session(target.Name)) != sessionraise.StatePending || writes != 1 {
					t.Fatalf("unchanged retry did not recover the annotation: %d %s (writes %d)", w.Code, w.Body.String(), writes)
				}
			} else {
				wantSentence(t, w, http.StatusConflict,
					"Session target has a raise that was recorded but whose record could not be written, and this request does not match it. Take it back and raise again with a new word from Tom: agent-run raise target --take-back")
				if writes != 0 || f.session(target.Name).Annotations[v1alpha1.AnnotationRaise] != "" {
					t.Fatalf("refused recovery wrote %d times or supplied an annotation", writes)
				}
			}
			after, err := f.srv.readLedger(context.Background(), f.srv.Raise.filled())
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("retry changed the pending ledger or spent another word: %v", err)
			}
		})
	}
}

func TestSameRaiseRetryLeavesExistingPendingAnnotationAlone(t *testing.T) {
	for _, state := range []string{sessionraise.StatePending, sessionraise.StateActive} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			f.raiseReady(true)
			tok := f.workbench("bench")
			target := f.remoteTarget("target", 2)
			uid := string(target.UID)
			body := f.raiseReq(uid, "", "allow the target to start workers", sessionraise.WherePhone, true)
			path := apiv1.SessionRaisePath(target.Name)
			if w := f.do(http.MethodPost, path, tok, body); w.Code != http.StatusAccepted {
				t.Fatalf("POST %d %s", w.Code, w.Body.String())
			}
			// An active ledger row may still have the pending annotation while
			// the activator finishes its write. POST must leave that work to it.
			if state == sessionraise.StateActive {
				f.raiseSent(uid)
				f.editRaiseRow(uid, func(row *sessionraise.SessionEntry) {
					row.State = state
					row.NoticeSentAt = row.PerformedAt
					row.ActiveFrom = f.now.Format(time.RFC3339)
				})
			}
			before := f.session(target.Name).Annotations[v1alpha1.AnnotationRaise]
			f.srv.Client = sessionHook{Client: f.c, hook: func(client.Object) error {
				t.Error("same-raise retry attempted a write")
				return context.DeadlineExceeded
			}}
			if w := f.do(http.MethodPost, path, tok, body); w.Code != http.StatusOK {
				t.Fatalf("retry %d %s", w.Code, w.Body.String())
			}
			if f.session(target.Name).Annotations[v1alpha1.AnnotationRaise] != before {
				t.Fatal("retry changed the pending annotation")
			}
		})
	}
}
