package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/asks"
)

func TestSessionWithdrawRefusesGrantAsk(t *testing.T) {
	f := newFixture(t)
	f.srv.Asks = &asks.KubeStore{Client: f.c, Live: f.c, Namespace: asks.Namespace, Log: logr.Discard()}
	f.h = f.srv.Handler()
	tok := f.sessionPod("grant-withdraw", "full", 0)
	sess := f.session("grant-withdraw")
	const id = "ask-0123456789abcdef"
	ask := &coord.Ask{
		ObjectMeta: metav1.ObjectMeta{
			Name: id, Namespace: asks.Namespace,
			Labels: map[string]string{
				asks.OriginLabel: asks.BrokerOrigin,
				asks.TypeLabel:   string(apiv1.AskTypeGrant),
				asks.StateLabel:  string(apiv1.AskStateOpen),
			},
		},
		Spec: coord.AskSpec{
			ID: id, Type: string(apiv1.AskTypeGrant), Question: "Access for this session?",
			Context:       coord.AskContext{SessionName: sess.Name, SessionUID: string(sess.UID)},
			CreatedAt:     metav1.NewTime(f.now),
			RequestID:     "grant-request-0123456789",
			RequestDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Grant: &coord.AskGrant{
				UID: "01234567-89ab-cdef-0123-456789abcdef", ScopeDigest: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				Type: "kube", Target: "dev-env-grant-workloads", TTL: "10m", Detail: "dev",
			},
		},
		Status: coord.AskStatus{State: string(apiv1.AskStateOpen)},
	}
	if err := f.c.Create(context.Background(), ask); err != nil {
		t.Fatal(err)
	}
	refused := wantError(t, f.do(http.MethodPost, apiv1.AskWithdrawPath(id), tok, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	if !strings.Contains(refused.Message, "a session cannot withdraw a grant ask") {
		t.Fatalf("refusal %q", refused.Message)
	}
	var stored coord.Ask
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: asks.Namespace, Name: id}, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.State == string(apiv1.AskStateWithdrawn) {
		t.Fatal("the session route withdrew the grant ask")
	}
}
