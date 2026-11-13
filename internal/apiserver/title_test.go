package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/sessiontitle"
)

func TestCreateTitle(t *testing.T) {
	f := newFixture(t)
	good := task()
	good.Title = "Main coordinator"
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, good)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	v := decode[apiv1.Session](t, w)
	if v.Title != "Main coordinator" || strings.Contains(w.Body.String(), "displayTitle") {
		t.Fatalf("view %s", w.Body.String())
	}
	if got := f.session(v.Name).Spec.Title; got != "Main coordinator" {
		t.Fatalf("stored %q", got)
	}

	blank := map[string]any{
		"repo": "haynes-ops", "agent": "claude", "mode": "task", "model": "claude-opus-5-5",
		"effort": "xhigh", "prompt": "fix the docs", "title": "",
	}
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, blank)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), `"title"`) {
		t.Fatalf("empty title: %d %s", w.Code, w.Body.String())
	}
	empty := decode[apiv1.Session](t, w)
	if f.session(empty.Name).Spec.Title != "" {
		t.Fatal("empty title was stored")
	}

	bounds := task()
	bounds.Title = strings.Repeat("é", 48)
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, bounds); w.Code != http.StatusCreated {
		t.Fatalf("96 bytes: %d %s", w.Code, w.Body.String())
	}
	bounds.Title = strings.Repeat("a", 48)
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, bounds); w.Code != http.StatusCreated {
		t.Fatalf("48 characters: %d %s", w.Code, w.Body.String())
	}

	bad := []struct{ title, reason string }{
		{strings.Repeat("a", 49), sessiontitle.ReasonTooLong},
		{strings.Repeat("é", 47) + "€", sessiontitle.ReasonCharacters},
		{"测", sessiontitle.ReasonCharacters},
		{"e\u0301", sessiontitle.ReasonCharacters},
		{"a\u200bb", sessiontitle.ReasonCharacters},
		{"a\u00adb", sessiontitle.ReasonCharacters},
		{"a|b", sessiontitle.ReasonCharacters},
		{"-Main", sessiontitle.ReasonCharacters},
		{".Main", sessiontitle.ReasonCharacters},
		{"!Main", sessiontitle.ReasonCharacters},
		{"€Main", sessiontitle.ReasonCharacters},
		{"a\u0007b", sessiontitle.ReasonCharacters},
		{"a\U00010000", sessiontitle.ReasonCharacters},
		{" Main", sessiontitle.ReasonSpacing},
		{"Main ", sessiontitle.ReasonSpacing},
		{"Main  coordinator", sessiontitle.ReasonSpacing},
	}
	for _, c := range bad {
		req := task()
		req.Title = c.title
		e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
		if len(e.Fields) != 1 || e.Fields[0].Field != "title" || e.Fields[0].Reason != c.reason || strings.Contains(e.Fields[0].Message, c.title) {
			t.Fatalf("title %q: %+v", c.title, e)
		}
	}
}

func TestCallerPolicyCreateStoresTitle(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	req := meatballRequest()
	req.Title = "Main coordinator"
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	v := decode[apiv1.Session](t, w)
	if v.Title != "Main coordinator" {
		t.Fatalf("view title %q", v.Title)
	}
	var stored v1alpha1.AgentSession
	if err := f.c.Get(t.Context(), types.NamespacedName{Namespace: sessionNS, Name: v.Name}, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.Title != "Main coordinator" {
		t.Fatalf("stored %q", stored.Spec.Title)
	}
}

func TestCreateTitleStrictFieldValidation(t *testing.T) {
	record := func(f *fixture) *[]client.CreateOptions {
		got := &[]client.CreateOptions{}
		f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.AgentSession); ok {
				*got = append(*got, *(&client.CreateOptions{}).ApplyOptions(opts))
			}
			return c.Create(ctx, obj, opts...)
		}})
		return got
	}
	bare := func(opts client.CreateOptions) bool {
		return opts.FieldValidation == "" && len(opts.DryRun) == 0 && opts.FieldManager == ""
	}

	f := newFixture(t)
	untitled := record(f)
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task()); w.Code != http.StatusCreated {
		t.Fatalf("untitled create: %d %s", w.Code, w.Body.String())
	}
	if len(*untitled) != 1 || !bare((*untitled)[0]) {
		t.Fatalf("untitled create options %+v", *untitled)
	}

	f = newFixture(t)
	titled := record(f)
	req := task()
	req.Title = "Main coordinator"
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("titled create: %d %s", w.Code, w.Body.String())
	}
	if len(*titled) != 1 || (*titled)[0].FieldValidation != metav1.FieldValidationStrict || len((*titled)[0].DryRun) != 0 || (*titled)[0].FieldManager != "" {
		t.Fatalf("titled create options %+v", *titled)
	}

	f = newFixture(t)
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Spec.Title != "" {
			if (&client.CreateOptions{}).ApplyOptions(opts).FieldValidation != metav1.FieldValidationStrict {
				t.Errorf("old CRD was not asked for strict validation")
			}
			return apierrors.NewBadRequest(`strict decoding error: unknown field "spec.title"`)
		}
		return c.Create(ctx, obj, opts...)
	}})
	e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req), http.StatusBadRequest, apiv1.CodeBadRequest)
	if e.Message != olderTitleCRD || f.sessionCount() != 0 {
		t.Fatalf("old CRD refusal %q sessions %d", e.Message, f.sessionCount())
	}
}
