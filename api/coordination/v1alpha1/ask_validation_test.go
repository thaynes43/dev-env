package v1alpha1_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

func TestAskSchemaAcceptsAndRefusesStatusRules(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	missing := newSchemaAsk(t, ctx, "ask-aaaaaaaaaaaaaaaa", "request-aa")
	missing.Status.State = "answered"
	if err := kube.Status().Update(ctx, missing); !apierrors.IsInvalid(err) {
		t.Fatal("answered without an answer", err)
	}

	early := newSchemaAsk(t, ctx, "ask-bbbbbbbbbbbbbbbb", "request-bb")
	early.Status.State = "open"
	early.Status.Answer = schemaAnswer(early)
	if err := kube.Status().Update(ctx, early); !apierrors.IsInvalid(err) {
		t.Fatal("answer on an open ask", err)
	}

	expired := newSchemaAsk(t, ctx, "ask-cccccccccccccccc", "request-cc")
	expired.Status.State = "expired"
	if err := kube.Status().Update(ctx, expired); err != nil {
		t.Fatal("expired", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(expired), expired); err != nil {
		t.Fatal(err)
	}
	expired.Status.State = "open"
	if err := kube.Status().Update(ctx, expired); !apierrors.IsInvalid(err) {
		t.Fatal("expired ask reopened", err)
	}

	open := newSchemaAsk(t, ctx, "ask-dddddddddddddddd", "request-dd")
	open.Status.State = "open"
	if err := kube.Status().Update(ctx, open); err != nil {
		t.Fatal("open", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(open), open); err != nil {
		t.Fatal(err)
	}
	if err := kube.Status().Patch(ctx, open.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(`{"status":null}`))); err != nil {
		t.Fatal("answer-free status removal", err)
	}

	kept := newSchemaAsk(t, ctx, "ask-eeeeeeeeeeeeeeee", "request-ee")
	kept.Status.State = "open"
	if err := kube.Status().Update(ctx, kept); err != nil {
		t.Fatal("open before answer", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(kept), kept); err != nil {
		t.Fatal(err)
	}
	kept.Status.State = "answered"
	kept.Status.Answer = schemaAnswer(kept)
	if err := kube.Status().Update(ctx, kept); err != nil {
		t.Fatal("answer", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(kept), kept); err != nil {
		t.Fatal(err)
	}
	if err := kube.Status().Update(ctx, kept.DeepCopy()); err != nil {
		t.Fatal("identical answer", err)
	}
	cleared := kept.DeepCopy()
	cleared.Status.Answer = nil
	if err := kube.Status().Update(ctx, cleared); !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "an ask answer is set once") {
		t.Fatal("answer removed", err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(kept), kept); err != nil {
		t.Fatal(err)
	}
	before := kept.DeepCopy()
	for _, patch := range []struct {
		name string
		kind types.PatchType
		body string
	}{
		{"status-null", types.MergePatchType, `{"status":null}`},
		{"status-remove", types.JSONPatchType, `[{"op":"remove","path":"/status"}]`},
	} {
		t.Run(patch.name, func(t *testing.T) {
			if err := kube.Status().Patch(ctx, kept.DeepCopy(), client.RawPatch(patch.kind, []byte(patch.body))); !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "an ask answer cannot be dropped with status") {
				t.Fatal("status removal dropped the answer", err)
			}
			var got coord.Ask
			if err := kube.Get(ctx, client.ObjectKeyFromObject(kept), &got); err != nil {
				t.Fatal(err)
			}
			if got.ResourceVersion != before.ResourceVersion || !reflect.DeepEqual(got.Status, before.Status) {
				t.Fatal("refused status removal changed the ask", got.ResourceVersion, got.Status)
			}
		})
	}
}

func newSchemaAsk(t *testing.T, ctx context.Context, name, requestID string) *coord.Ask {
	t.Helper()
	ask := schemaAsk(name, "")
	ask.Spec.RequestID = requestID
	ask.Spec.RequestDigest = strings.Repeat("ef", 32)
	if err := kube.Create(ctx, ask); err != nil {
		t.Fatal(err)
	}
	return ask
}

func schemaAnswer(ask *coord.Ask) *coord.AskAnswer {
	return &coord.AskAnswer{
		OptionID:   "a",
		AnsweredBy: coord.AskActor{Issuer: "https://issuer.example", Subject: "subject-1", DisplayName: "Board user"},
		AnsweredAt: metav1.NewTime(ask.Spec.CreatedAt.Add(time.Minute)),
	}
}
