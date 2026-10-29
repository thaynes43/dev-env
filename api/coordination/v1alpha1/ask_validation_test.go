package v1alpha1_test

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

func schemaAsk(name, state string) *coord.Ask {
	created := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	return &coord.Ask{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: coord.AskSpec{
			ID: name, Type: "info", Question: "Which option?",
			Options: []coord.AskOption{{ID: "a", Label: "Alpha"}}, AllowFreeText: true,
			Context:       coord.AskContext{SessionName: "sess", SessionUID: "uid-1"},
			CreatedAt:     metav1.NewTime(created),
			ExpiresAt:     metav1.NewTime(created.Add(time.Hour)),
			RequestID:     "request-01",
			RequestDigest: strings.Repeat("ab", 32),
		},
		Status: coord.AskStatus{State: state},
	}
}

func TestAskSchemaImmutableSpecAndSetOnceAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ask := schemaAsk("ask-0123456789abcdef", "")
	if err := kube.Create(ctx, ask); err != nil {
		t.Fatal(err)
	}
	mismatched := schemaAsk("ask-0123456789abcdef", "")
	mismatched.Name = "ask-fedcba9876543210"
	mismatched.Spec.ID = ask.Spec.ID
	mismatched.Spec.RequestID = "request-99"
	if err := kube.Create(ctx, mismatched); !apierrors.IsInvalid(err) {
		t.Fatal("name and id were allowed to differ", err)
	}
	bad := ask.DeepCopy()
	bad.Spec.Question = "Changed question"
	if err := kube.Update(ctx, bad); !apierrors.IsInvalid(err) {
		t.Fatal("spec was mutated", err)
	}

	if err := kube.Get(ctx, client.ObjectKeyFromObject(ask), ask); err != nil {
		t.Fatal(err)
	}
	ask.Status.State = "open"
	if err := kube.Status().Update(ctx, ask); err != nil {
		t.Fatal("open status", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(ask), ask); err != nil {
		t.Fatal(err)
	}
	ask.Status.State = "answered"
	ask.Status.Answer = &coord.AskAnswer{
		OptionID:   "a",
		AnsweredBy: coord.AskActor{Issuer: "https://issuer.example", Subject: "subject-1", DisplayName: "Board user"},
		AnsweredAt: metav1.NewTime(ask.Spec.CreatedAt.Add(time.Minute)),
	}
	if err := kube.Status().Update(ctx, ask); err != nil {
		t.Fatal("answer", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(ask), ask); err != nil {
		t.Fatal(err)
	}
	changed := ask.DeepCopy()
	changed.Status.Answer.Text = "a different note"
	if err := kube.Status().Update(ctx, changed); !apierrors.IsInvalid(err) {
		t.Fatal("answer was replaced", err)
	}
	reopened := ask.DeepCopy()
	reopened.Status.State = "open"
	if err := kube.Status().Update(ctx, reopened); !apierrors.IsInvalid(err) {
		t.Fatal("answered ask reopened", err)
	}

	withdrawn := schemaAsk("ask-abcdef0123456789", "")
	withdrawn.Spec.RequestID = "request-02"
	withdrawn.Spec.RequestDigest = strings.Repeat("cd", 32)
	if err := kube.Create(ctx, withdrawn); err != nil {
		t.Fatal(err)
	}
	withdrawn.Status.State = "withdrawn"
	if err := kube.Status().Update(ctx, withdrawn); err != nil {
		t.Fatal("withdraw", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(withdrawn), withdrawn); err != nil {
		t.Fatal(err)
	}
	withdrawn.Status.State = "open"
	if err := kube.Status().Update(ctx, withdrawn); !apierrors.IsInvalid(err) {
		t.Fatal("withdrawn ask reopened", err)
	}
}
