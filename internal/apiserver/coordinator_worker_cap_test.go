package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestCoordinatorWorkerCapHumanAndClient(t *testing.T) {
	for _, token := range []string{tokHuman, tokClient} {
		for _, cap := range []int32{0, 1, 4, 5} {
			t.Run(fmt.Sprintf("%s/%d", token, cap), func(t *testing.T) {
				f := meatballFixture(t)
				r := meatballRequest()
				r.Limits.MaxWorkers = &cap
				w := f.do(http.MethodPost, apiv1.SessionsPath, token, r)
				if cap < 1 || cap > 4 {
					e := wantError(t, w, 422, apiv1.CodeInvalid)
					if !strings.Contains(e.Message, "1 to 4") || f.sessionCount() != 0 {
						t.Fatal("cap not refused", e)
					}
					return
				}
				if w.Code != http.StatusCreated {
					t.Fatal(w.Code, w.Body.String())
				}
				stored := f.session(decode[apiv1.Session](t, w).Name)
				parent := humanSA
				if token == tokClient {
					parent = clientSA
				}
				if stored.Spec.Parent != parent || stored.Spec.Limits.MaxWorkers == nil || *stored.Spec.Limits.MaxWorkers != cap || stored.Spec.Effort != "xhigh" {
					t.Fatalf("wrong bounded coordinator: %+v", stored.Spec)
				}
			})
		}
	}
}

func TestCoordinatorWorkerCapOtherCallersStayRefused(t *testing.T) {
	for _, kind := range []callerKind{kindSession, kindCoordinator, kindPortalReader} {
		f := newFixture(t)
		_, err := f.srv.newSession(context.Background(), meatballRequest(), &caller{kind: kind})
		var e *apiError
		if !errors.As(err, &e) || e.status != http.StatusForbidden || !strings.Contains(e.body.Message, "limits.maxWorkers") {
			t.Fatalf("%s worker cap was not refused: %v", kind, err)
		}
	}
	for _, profile := range []string{"dev", "full", "workbench"} {
		f := newFixture(t)
		token := f.sessionPod("ordinary", profile, 0)
		wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, meatballRequest()), 403, apiv1.CodeForbidden)
		if f.sessionCount() != 1 {
			t.Fatal("refused session created a child")
		}
	}
}

func TestCoordinatorWorkerCapStrictForHumanAndClient(t *testing.T) {
	for _, token := range []string{tokHuman, tokClient} {
		f := meatballFixture(t)
		strict := false
		f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if s, ok := obj.(*v1alpha1.AgentSession); ok {
				strict = (&client.CreateOptions{}).ApplyOptions(opts).FieldValidation == metav1.FieldValidationStrict
				if strict {
					return apierrors.NewBadRequest("strict decoding error: unknown field spec.limits.maxWorkers")
				}
				s.Spec.Limits.MaxWorkers = nil
			}
			return c.Create(ctx, obj, opts...)
		}})
		w := f.do(http.MethodPost, apiv1.SessionsPath, token, meatballRequest())
		if !strict || w.Code < 400 || f.sessionCount() != 0 {
			t.Fatal("stale CRD could prune cap", w.Code, w.Body.String())
		}
	}
}

func TestCoordinatorWorkerCapPresetsOnlyWhenOmitted(t *testing.T) {
	for _, token := range []string{tokHuman, tokClient} {
		for _, model := range []string{"claude-opus-5-5", "claude-fable-5-2", "gpt-6-astra"} {
			f := meatballFixture(t)
			bindCatalogFixture(t, f)
			r := meatballRequest()
			r.Project, r.Repo, r.Model = "sample", "alias", model
			want := "xhigh"
			if model == "gpt-6-astra" {
				r.Agent, want = "codex", "max"
			}
			w := f.do(http.MethodPost, apiv1.SessionsPath, token, r)
			if w.Code != 201 {
				t.Fatal(w.Code, w.Body.String())
			}
			if got := decode[apiv1.Session](t, w).Effort; got != want {
				t.Fatalf("%s preset %q, want %q", model, got, want)
			}
			// These trusted callers' existing explicit effort rights do not change.
			r.Effort = "medium"
			w = f.do(http.MethodPost, apiv1.SessionsPath, token, r)
			if w.Code != 201 || decode[apiv1.Session](t, w).Effort != "medium" {
				t.Fatal("explicit effort changed", w.Code, w.Body.String())
			}
		}
	}
}

func TestCoordinatorWorkerCapEnforcedForHumanAndClientRoots(t *testing.T) {
	for _, token := range []string{tokHuman, tokClient} {
		t.Run(token, func(t *testing.T) {
			f := meatballFixture(t)
			bindCatalogFixture(t, f)
			r := meatballRequest()
			*r.Limits.MaxWorkers = 2
			r.IdempotencyKey = "coordinator-527"
			name := policyCreated(t, f, token, r)
			repeat := f.do(http.MethodPost, apiv1.SessionsPath, token, r)
			if repeat.Code != http.StatusOK || decode[apiv1.Session](t, repeat).Name != name {
				t.Fatal("bounded create retry did not return the same session", repeat.Code, repeat.Body.String())
			}
			root := f.session(name)
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: "coordinator-pod",
					Labels:          map[string]string{v1alpha1.LabelProfile: "dev"},
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(root, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
				Spec: corev1.PodSpec{ServiceAccountName: "dev-env-agent", Containers: []corev1.Container{{Name: "agent", Image: "test"}}},
			}
			if err := f.c.Create(context.Background(), pod); err != nil {
				t.Fatal(err)
			}
			id := saIdentity(sessionNS + "/dev-env-agent")
			id.PodName, id.PodUID = name, string(pod.UID)
			f.auth["bounded-root"] = id
			var first string
			for _, agent := range []string{"claude", "codex"} {
				child := coordinatorTask()
				child.Agent = agent
				if agent == "codex" {
					child.Model, child.Effort = "gpt-6.1-sol", "xhigh"
				}
				created := policyCreated(t, f, "bounded-root", child)
				stored := f.session(created)
				if stored.Spec.Parent != name || stored.Labels[v1alpha1.LabelDepth] != "1" || stored.Annotations[callerParentUID] != string(root.UID) {
					t.Fatal("worker lost parent or resume fence")
				}
				if first == "" {
					first = created
				}
			}
			e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, "bounded-root", coordinatorTask()), 429, apiv1.CodeLimitExceeded)
			if !strings.Contains(e.Message, "2 unfinished direct workers across all agent kinds") {
				t.Fatal(e.Message)
			}
			if w := f.do(http.MethodPost, apiv1.SessionSuspendPath(first), tokHuman, nil); w.Code != 202 {
				t.Fatal(w.Code, w.Body.String())
			}
			policyCreated(t, f, "bounded-root", coordinatorTask())
			wantError(t, f.do(http.MethodPost, apiv1.SessionResumePath(first), tokHuman, nil), 429, apiv1.CodeLimitExceeded)
			wantNoConfigMaps(t, f)
		})
	}
}
