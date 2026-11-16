package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestSummonCreateRefusesLeadingDashBrief(t *testing.T) {
	for _, kind := range []string{"task", "remote"} {
		for _, template := range []string{"-", "--resume other", "-p\nprivate brief text"} {
			t.Run(kind+"/"+template, func(t *testing.T) {
				policy := summonTestPolicy()
				index := 0
				body := remediationBody("rem-responder-1", nil)
				if kind == "remote" {
					index = 1
					body = map[string]any{"name": "esc-1", "mode": "remote", "lane": "escalation", "agent": "claude"}
				}
				policy.Spec.Summon.Sessions[index].Brief.Template = template
				f := summonFixture(t, policy)
				before := &corev1.ConfigMap{}
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ledgerTestNS, Name: ledgerTestName}, before); err != nil {
					t.Fatal(err)
				}
				w := summonPost(t, f, body)
				if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "brief must not start with '-'") {
					t.Fatalf("not refused plainly: %d %s", w.Code, w.Body.String())
				}
				if strings.Contains(w.Body.String(), "private brief text") {
					t.Fatal("refusal echoed the brief")
				}
				sessions := &v1alpha1.AgentSessionList{}
				if err := f.c.List(context.Background(), sessions); err != nil {
					t.Fatal(err)
				}
				for _, s := range sessions.Items {
					if s.Spec.Caller == policy.Name {
						t.Fatal("refusal created a summoned session")
					}
				}
				after := &corev1.ConfigMap{}
				if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(before), after); err != nil {
					t.Fatal(err)
				}
				if before.ResourceVersion != after.ResourceVersion {
					t.Fatal("refusal charged the create ledger")
				}
			})
		}
	}
}
