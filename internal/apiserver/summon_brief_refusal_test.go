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
	for _, template := range []string{"-", "--resume other", "-p\nprivate brief text"} {
		checkSummonCreateRefusesBrief(t, template, "brief must not start with '-'")
	}
}

func TestSummonCreateRefusesNULBrief(t *testing.T) {
	checkSummonCreateRefusesBrief(t, "Follow\x00the runbook. private brief text", "brief must not contain NUL")
}

func TestSummonCreateSanitizesDataNULBeforeBriefValidation(t *testing.T) {
	f := summonFixture(t, summonTestPolicy())
	w := summonPost(t, f, remediationBody("rem-responder-1", map[string]string{"alert": "Follow\x00the runbook"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("sanitized data was refused: %d", w.Code)
	}
	stored := f.session("rem-responder-1")
	if strings.ContainsRune(stored.Spec.Prompt, 0) || !strings.Contains(stored.Spec.Prompt, "alert: Followthe runbook") {
		t.Fatal("stored brief did not retain the sanitized data")
	}
}

func checkSummonCreateRefusesBrief(t *testing.T, template, message string) {
	t.Helper()
	for _, kind := range []string{"task", "remote"} {
		t.Run(kind+"/"+message, func(t *testing.T) {
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
			if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), message) {
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
