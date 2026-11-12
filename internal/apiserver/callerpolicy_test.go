package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
)

const tokMeatball = "test-meatball"

func meatballPolicy() *v1alpha1.CallerPolicy {
	return &v1alpha1.CallerPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: "meatball", UID: "policy-uid"},
		Spec: v1alpha1.CallerPolicySpec{
			ServiceAccount: "dev-env-system/dev-env-meatball", Mode: "task", Profile: "dev",
			Models:        []string{"claude-opus-5-5", "gpt-6-astra", "claude-fable-5-2"},
			MaxConcurrent: 2, MaxTimeout: "4h", MaxWorkers: 4,
		},
	}
}

func meatballRequest() apiv1.CreateSessionRequest {
	workers := int32(4)
	return apiv1.CreateSessionRequest{Repo: "dev-env", Agent: "claude", Mode: "task", Model: "claude-opus-5-5",
		Profile: "dev", Prompt: "Complete the assigned story.", Limits: &apiv1.Limits{Timeout: "4h", MaxWorkers: &workers}}
}

func meatballFixture(t *testing.T, objects ...client.Object) *fixture {
	t.Helper()
	f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if obj.GetUID() == "" {
			obj.SetUID(uuid.NewUUID())
		}
		return c.Create(ctx, obj, opts...)
	}}, objects...)
	f.auth[tokMeatball] = saIdentity("dev-env-system/dev-env-meatball")
	return f
}

func TestCallerPolicyCreate(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, meatballRequest())
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	v := decode[apiv1.Session](t, w)
	var stored v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: v.Name}, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.Parent != "dev-env-system/dev-env-meatball" || stored.Labels[v1alpha1.LabelDepth] != "0" ||
		stored.Spec.Effort != "xhigh" || stored.Spec.Limits.MaxWorkers == nil || *stored.Spec.Limits.MaxWorkers != 4 {
		t.Fatalf("incorrect caller binding or limits: %+v", stored.Spec)
	}
}

func TestCallerPolicyModelsAndPresets(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-fable-5-2", "gpt-6-astra"} {
		t.Run(model, func(t *testing.T) {
			f := meatballFixture(t, meatballPolicy())
			bindCatalogFixture(t, f)
			r := meatballRequest()
			r.Project, r.Repo, r.Model = "sample", "alias", model
			effort := "xhigh"
			if model == "gpt-6-astra" {
				r.Agent, effort = "codex", "max"
			}
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r)
			if w.Code != http.StatusCreated {
				t.Fatal(w.Code, w.Body.String())
			}
			v := decode[apiv1.Session](t, w)
			if v.Model != model || v.Effort != effort || v.Profile != "dev" || v.Mode != "task" ||
				v.Limits == nil || v.Limits.Timeout != "4h0m0s" || v.Limits.MaxWorkers == nil || *v.Limits.MaxWorkers != 4 {
				t.Fatalf("preset or bounds were not preserved: %+v", v)
			}
		})
	}
}

func TestCallerPolicyRefusesInvalidCreates(t *testing.T) {
	cases := []struct {
		name, message string
		mutate        func(*apiv1.CreateSessionRequest)
	}{
		{"wrong profile", "profile dev", func(r *apiv1.CreateSessionRequest) { r.Profile = "full" }},
		{"workbench profile", "profile dev", func(r *apiv1.CreateSessionRequest) { r.Profile = "workbench" }},
		{"default profile", "profile dev", func(r *apiv1.CreateSessionRequest) { r.Profile = "" }},
		{"wrong model", "not allowed", func(r *apiv1.CreateSessionRequest) { r.Model = "claude-sonnet-5-5" }},
		{"missing model", "no default", func(r *apiv1.CreateSessionRequest) { r.Model = "" }},
		{"model alias", "not allowed", func(r *apiv1.CreateSessionRequest) { r.Model = "opus" }},
		{"effort setting", "no effort setting", func(r *apiv1.CreateSessionRequest) { r.Effort = "xhigh" }},
		{"wrong agent", "must match", func(r *apiv1.CreateSessionRequest) { r.Agent = "codex" }},
		{"too many hours", "at most 4h", func(r *apiv1.CreateSessionRequest) { r.Limits.Timeout = "4h1ns" }},
		{"zero hours", "positive", func(r *apiv1.CreateSessionRequest) { r.Limits.Timeout = "0s" }},
		{"negative hours", "positive", func(r *apiv1.CreateSessionRequest) { r.Limits.Timeout = "-1h" }},
		{"invalid hours", "positive", func(r *apiv1.CreateSessionRequest) { r.Limits.Timeout = "four hours" }},
		{"missing hours", "positive", func(r *apiv1.CreateSessionRequest) { r.Limits.Timeout = "" }},
		{"missing limits", "requires", func(r *apiv1.CreateSessionRequest) { r.Limits = nil }},
		{"five workers", "from 1 to 4", func(r *apiv1.CreateSessionRequest) { *r.Limits.MaxWorkers = 5 }},
		{"zero workers", "from 1 to 4", func(r *apiv1.CreateSessionRequest) { *r.Limits.MaxWorkers = 0 }},
		{"missing workers", "worker cap", func(r *apiv1.CreateSessionRequest) { r.Limits.MaxWorkers = nil }},
		{"local mode", "task sessions only", func(r *apiv1.CreateSessionRequest) { r.Mode = "local" }},
		{"remote mode", "task sessions only", func(r *apiv1.CreateSessionRequest) { r.Mode = "remote" }},
		{"missing mode", "task sessions only", func(r *apiv1.CreateSessionRequest) { r.Mode = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := meatballFixture(t, meatballPolicy())
			r := meatballRequest()
			tc.mutate(&r)
			e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
			if !strings.Contains(e.Message, tc.message) {
				t.Fatalf("refusal %q does not explain %q", e.Message, tc.message)
			}
			if f.sessionCount() != 0 {
				t.Fatal("a refused request created a session")
			}
		})
	}
}

func TestCallerPolicyRefusesEffortKeyPresence(t *testing.T) {
	for _, key := range []string{"effort", "Effort", "EFFORT"} {
		for _, value := range []any{"", nil, "max"} {
			f := meatballFixture(t, meatballPolicy())
			raw, err := json.Marshal(meatballRequest())
			if err != nil {
				t.Fatal(err)
			}
			var req map[string]any
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatal(err)
			}
			req[key] = value
			e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, req), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
			if !strings.Contains(e.Message, "no effort setting") {
				t.Fatal(e.Message)
			}
		}
	}
}

func TestCallerPolicyTightenedBounds(t *testing.T) {
	p := meatballPolicy()
	p.Spec.MaxConcurrent, p.Spec.MaxWorkers, p.Spec.MaxTimeout = 1, 2, "30m"
	p.Spec.Models = []string{"claude-fable-5-2"}
	f := meatballFixture(t, p)
	r := meatballRequest()
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r), 422, apiv1.CodeInvalid)
	r.Model = "claude-fable-5-2"
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r), 422, apiv1.CodeInvalid)
	r.Limits.Timeout = "30m"
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r), 422, apiv1.CodeInvalid)
	*r.Limits.MaxWorkers = 2
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r), 429, apiv1.CodeLimitExceeded)
}

func TestCallerPolicyUnknownAndRevoked(t *testing.T) {
	for _, where := range []string{"absent", "other-name", "other-namespace"} {
		t.Run(where, func(t *testing.T) {
			f := meatballFixture(t)
			p := meatballPolicy()
			switch where {
			case "other-name":
				p.Name = "other"
			case "other-namespace":
				p.Namespace = "elsewhere"
			}
			if where != "absent" {
				if err := f.c.Create(context.Background(), p); err != nil {
					t.Fatal(err)
				}
			}
			for _, token := range []string{tokMeatball, tokStranger} {
				e := wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, token, nil), 403, apiv1.CodeForbidden)
				want := fmt.Sprintf("%s may not call this API: it is not Tom's, a client's or a session's ServiceAccount, nor a summoning caller with a CallerPolicy", f.auth[token].serviceAccountRef())
				if e.Message != want {
					t.Fatalf("unknown caller response changed: %s", e.Message)
				}
			}
		})
	}
	p := meatballPolicy()
	f := meatballFixture(t, p)
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, tokStranger, nil), 403, apiv1.CodeForbidden)
	if err := f.c.Delete(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, tokMeatball, nil), 403, apiv1.CodeForbidden)
}

func TestCallerPolicyThirdUnfinishedAndIdempotency(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	bindCatalogFixture(t, f)
	r := meatballRequest()
	r.IdempotencyKey = "story-526"
	first := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r)
	if first.Code != http.StatusCreated {
		t.Fatal(first.Code, first.Body.String())
	}
	firstName := decode[apiv1.Session](t, first).Name
	second := meatballRequest()
	second.Project, second.Repo, second.Agent, second.Model = "sample", "alias", "codex", "gpt-6-astra"
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, second); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, meatballRequest()), 429, apiv1.CodeLimitExceeded)
	if !strings.Contains(e.Message, "at most 2 unfinished") {
		t.Fatal(e.Message)
	}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r); w.Code != 200 || decode[apiv1.Session](t, w).Name != firstName {
		t.Fatal("idempotent retry at capacity", w.Code, w.Body.String())
	}
	r.Prompt = "a different request"
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r), 409, apiv1.CodeConflict)
	finished := f.session(firstName)
	finished.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentExited}
	if err := f.c.Status().Update(context.Background(), finished); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, meatballRequest()); w.Code != 201 {
		t.Fatal("a finished session did not release capacity", w.Code, w.Body.String())
	}
}

func TestCallerPolicyOwnReadsAndStops(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	f.sessionPod("own", "dev", 0)
	own := f.session("own")
	own.Spec.Parent = meatballAccount
	if err := f.c.Update(context.Background(), own); err != nil {
		t.Fatal(err)
	}
	f.sessionPod("foreign", "dev", 0)
	f.sessionPod("grandchild", "dev", 1)
	grandchild := f.session("grandchild")
	grandchild.Spec.Parent = "own"
	if err := f.c.Update(context.Background(), grandchild); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?mine=false", "?mine=true", "?caller="} {
		w := f.do(http.MethodGet, apiv1.SessionsPath+query, tokMeatball, nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		list := decode[apiv1.SessionList](t, w)
		if len(list.Sessions) != 1 || list.Sessions[0].Name != "own" || list.Sessions[0].Prompt != "" {
			t.Fatal("list escaped own summaries", list)
		}
	}
	if w := f.do(http.MethodGet, apiv1.SessionPath("own"), tokMeatball, nil); w.Code != 200 || decode[apiv1.Session](t, w).UsageRollup != nil {
		t.Fatal("own read or descendant isolation", w.Code, w.Body.String())
	}
	var denial string
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		for _, name := range []string{"foreign", "grandchild", "missing"} {
			e := wantError(t, f.do(method, apiv1.SessionPath(name), tokMeatball, nil), 403, apiv1.CodeForbidden)
			if denial != "" && e.Message != denial {
				t.Fatal("missing and foreign sessions have different responses", e.Message)
			}
			denial = e.Message
		}
	}
	if w := f.do(http.MethodDelete, apiv1.SessionPath("own"), tokMeatball, nil); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.session("own").DeletionTimestamp.IsZero() || !f.session("foreign").DeletionTimestamp.IsZero() {
		t.Fatal("own reap did not retain its rescue finalizer, or touched another session")
	}
}

func TestCallerPolicyNoOtherRoutesOrLaunchEntrances(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	ex := &fakeExec{}
	f.srv.Exec = ex
	for _, route := range []struct{ method, path string }{
		{"GET", "/v1/sessions/own/log"}, {"POST", "/v1/sessions/own/messages"}, {"POST", "/v1/sessions/own/threads"},
		{"POST", "/v1/sessions/own/suspend"}, {"POST", "/v1/sessions/own/resume"}, {"POST", "/v1/sessions/own/heartbeat"},
		{"GET", "/v1/grants"}, {"POST", "/v1/grants"}, {"GET", "/v1/grants/foreign"}, {"DELETE", "/v1/grants/foreign"},
		{"GET", apiv1.FleetPath}, {"GET", apiv1.RescuesPath}, {"POST", apiv1.ActivitiesPath},
		{"POST", apiv1.KeeperLoginPath + "/codex/url"}, {"POST", apiv1.TaskBudgetsPath},
		{"POST", apiv1.PilotPreflightPath}, {"GET", apiv1.SessionDirectoryPath}, {"POST", apiv1.InboxPath},
	} {
		wantError(t, f.do(route.method, route.path, tokMeatball, map[string]any{}), 403, apiv1.CodeForbidden)
	}
	for _, mutate := range []func(*apiv1.CreateSessionRequest){
		func(r *apiv1.CreateSessionRequest) { r.Restore = "other/20261110-0300" },
		func(r *apiv1.CreateSessionRequest) { r.CanaryID = "some-canary" },
		func(r *apiv1.CreateSessionRequest) { r.CoordinatorInstructions = true },
		func(r *apiv1.CreateSessionRequest) { r.CoordinatorInstructionPublisher = true },
	} {
		r := meatballRequest()
		mutate(&r)
		wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r), 403, apiv1.CodeForbidden)
	}
	if len(ex.cmds) != 0 || f.sessionCount() != 0 {
		t.Fatal("forbidden route executed or created a session")
	}
}

func TestCallerPolicyWorkerCapAcrossProvidersAndScheduled(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	bindCatalogFixture(t, f)
	token := f.sessionPod("coordinator", "dev", 0)
	parent := f.session("coordinator")
	cap := int32(2)
	parent.Spec.Parent, parent.Spec.Limits = meatballAccount, &v1alpha1.SessionLimits{MaxWorkers: &cap}
	if err := f.c.Update(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude", "codex"} {
		r := coordinatorTask()
		r.Agent = agent
		if agent == "codex" {
			r.Model, r.Effort = "gpt-6.1-sol", "xhigh"
		}
		w := f.do(http.MethodPost, apiv1.SessionsPath, token, r)
		if w.Code != http.StatusCreated {
			t.Fatal(w.Code, w.Body.String())
		}
		child := f.session(decode[apiv1.Session](t, w).Name)
		if child.Labels[v1alpha1.LabelDepth] != "1" || child.Spec.Parent != parent.Name || child.Spec.Profile != "dev" {
			t.Fatal("existing depth or profile inheritance changed")
		}
	}
	e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, coordinatorTask()), 429, apiv1.CodeLimitExceeded)
	if !strings.Contains(e.Message, "2 unfinished direct workers across all agent kinds") {
		t.Fatal(e.Message)
	}
	d := swDispatch()
	d.Owner = &scheduledwork.SessionRef{Name: parent.Name, UID: parent.UID}
	_, _, _, err := f.srv.CreateScheduledSession(context.Background(), swRequest(), d)
	wantRefused(t, err, "worker cap")
}

func TestCallerPolicyExistingClassesKeepPrecedence(t *testing.T) {
	for _, kind := range []callerKind{kindHuman, kindClient} {
		f := meatballFixture(t, meatballPolicy())
		if kind == kindHuman {
			f.srv.Policy.Human = meatballAccount
		} else {
			f.srv.Policy.Clients = append(f.srv.Policy.Clients, meatballAccount)
		}
		c, err := f.srv.resolveCaller(context.Background(), f.auth[tokMeatball])
		if err != nil || c.kind != kind {
			t.Fatal("policy replaced an existing configured class", c, err)
		}
		r := task()
		// Existing class tests cover all other profile and route rights
		// without changed expectations. This create still accepts effort.
		r.Profile = "dev"
		if w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r); w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestCallerPolicyRefusesUnverifiedPolicyAndProfile(t *testing.T) {
	for _, fault := range []string{"no-reader", "invalid-policy", "no-templates", "failed-templates", "privileged-profile"} {
		f := meatballFixture(t, meatballPolicy())
		want := 503
		switch fault {
		case "no-reader":
			f.srv.Live = nil
		case "invalid-policy":
			p := meatballPolicy()
			if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(p), p); err != nil {
				t.Fatal(err)
			}
			p.Spec.MaxConcurrent = 3
			if err := f.c.Update(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			want = 403
		case "no-templates":
			f.srv.Templates = nil
		case "failed-templates":
			f.tmplOK = false
		case "privileged-profile":
			p := f.tmpl.Profiles["dev"]
			p.ServiceAccount = "dev-env-workbench"
			f.tmpl.Profiles["dev"] = p
			want = 403
		}
		w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, meatballRequest())
		if w.Code != want || f.sessionCount() != 0 {
			t.Fatalf("%s: %d %s", fault, w.Code, w.Body.String())
		}
	}
}

// One replica's create lock covers the live count and the create. Pause the
// first create inside its write: a second create on the same replica waits,
// then counts the committed first and is refused instead of passing a stale count.
func TestCallerPolicyAdmissionSerialisedWithinReplica(t *testing.T) {
	for _, boundedParent := range []bool{false, true} {
		t.Run(fmt.Sprint("workers=", boundedParent), func(t *testing.T) {
			f := meatballFixture(t, meatballPolicy())
			token, parent := tokMeatball, meatballAccount
			r := meatballRequest()
			if boundedParent {
				token = f.sessionPod("bounded-parent", "dev", 0)
				p := f.session("bounded-parent")
				cap := int32(2)
				p.Spec.Limits = &v1alpha1.SessionLimits{MaxWorkers: &cap}
				if err := f.c.Update(context.Background(), p); err != nil {
					t.Fatal(err)
				}
				parent, r = p.Name, task()
			}
			seed := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "seed", Namespace: sessionNS}, Spec: v1alpha1.AgentSessionSpec{Parent: parent, Agent: "claude"}}
			if err := f.c.Create(context.Background(), seed); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var enter, once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*v1alpha1.AgentSession); ok {
					enter.Do(func() { close(entered) })
					select {
					case <-release:
					case <-time.After(10 * time.Second):
						return context.DeadlineExceeded
					}
				}
				return c.Create(ctx, obj, opts...)
			}})
			first, second := make(chan int, 1), make(chan int, 1)
			go func() { first <- f.do(http.MethodPost, apiv1.SessionsPath, token, r).Code }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("first admission did not reach its create")
			}
			go func() { second <- f.do(http.MethodPost, apiv1.SessionsPath, token, r).Code }()
			select {
			case code := <-second:
				t.Fatalf("second create did not wait for the first: %d", code)
			case <-time.After(200 * time.Millisecond):
			}
			unblock()
			if code := <-first; code != http.StatusCreated {
				t.Fatalf("first create: %d", code)
			}
			if code := <-second; code != http.StatusTooManyRequests {
				t.Fatalf("second create passed a stale count: %d", code)
			}
			want := 2 // the seed and the first create
			if boundedParent {
				want++ // and the parent
			}
			if got := f.sessionCount(); got != want {
				t.Fatalf("sessions: %d, want %d", got, want)
			}
		})
	}
}

// A lost create response needs no recovery: nothing is held, the live count
// includes the session if it committed, and an idempotent retry finds it.
func TestCallerPolicyUnknownCreateNeedsNoRecovery(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint("committed=", committed), func(t *testing.T) {
			p := meatballPolicy()
			p.Spec.MaxConcurrent = 1
			f := meatballFixture(t, p)
			r := meatballRequest()
			r.IdempotencyKey = "lost-response"
			f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*v1alpha1.AgentSession); ok {
					if committed {
						if err := c.Create(ctx, obj, opts...); err != nil {
							return err
						}
					}
					return context.DeadlineExceeded
				}
				return c.Create(ctx, obj, opts...)
			}})
			wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r), 503, apiv1.CodeUnavailable)
			f.srv.Client = f.c
			wantNoConfigMaps(t, f)
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, r)
			want := http.StatusCreated
			if committed {
				want = http.StatusOK // the committed create is found by its key.
			}
			if w.Code != want {
				t.Fatal("unknown create retry", w.Code, w.Body.String())
			}
			wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, meatballRequest()), 429, apiv1.CodeLimitExceeded)
		})
	}
}

func TestCallerPolicyPolicyChangesDuringAdmission(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	reads := 0
	// The first policy read resolves the caller. Tighten the policy right
	// after it, so the re-read under the create lock sees another version.
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		if p, ok := obj.(*v1alpha1.CallerPolicy); ok {
			if reads++; reads == 1 {
				changed := p.DeepCopy()
				changed.Spec.MaxWorkers = 1
				return c.Update(ctx, changed)
			}
		}
		return nil
	}})
	e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, meatballRequest()), 403, apiv1.CodeForbidden)
	if !strings.Contains(e.Message, "changed during admission") || f.sessionCount() != 0 {
		t.Fatal("policy change was not fenced", e.Message)
	}
}

// configMapWrites records every ConfigMap write through the server's clients.
type configMapWrites struct {
	mu    sync.Mutex
	calls []string
}

func (w *configMapWrites) record(verb string, obj client.Object) {
	if _, ok := obj.(*corev1.ConfigMap); ok {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.calls = append(w.calls, verb+" "+obj.GetName())
	}
}

func (w *configMapWrites) wrap(c client.WithWatch) client.WithWatch {
	return interceptor.NewClient(c, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			w.record("create", obj)
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			w.record("update", obj)
			return c.Update(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			w.record("patch", obj)
			return c.Patch(ctx, obj, patch, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			w.record("delete", obj)
			return c.Delete(ctx, obj, opts...)
		},
		DeleteAllOf: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteAllOfOption) error {
			w.record("deleteAllOf", obj)
			return c.DeleteAllOf(ctx, obj, opts...)
		},
	})
}

func wantNoConfigMaps(t *testing.T, f *fixture) {
	t.Helper()
	var list corev1.ConfigMapList
	if err := f.c.List(context.Background(), &list, client.InNamespace(sessionNS)); err != nil || len(list.Items) != 0 {
		t.Fatal("admission left a ConfigMap in the session namespace", err, len(list.Items))
	}
}

// Capacity is a live count under the create lock; admission writes no lock
// object. Creates, a scheduled worker, and bounded suspends and resumes write
// no ConfigMap through either client.
func TestCallerPolicyAdmissionWritesNoConfigMap(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	bindCatalogFixture(t, f)
	writes := &configMapWrites{}
	f.srv.Client, f.srv.Live = writes.wrap(f.c), writes.wrap(f.c)
	root := policyCreated(t, f, tokMeatball, meatballRequest())
	token := f.sessionPod("coordinator", "dev", 0)
	parent := f.session("coordinator")
	cap := int32(2)
	parent.Spec.Parent, parent.Spec.Limits = meatballAccount, &v1alpha1.SessionLimits{MaxWorkers: &cap}
	if err := f.c.Update(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	worker := policyCreated(t, f, token, coordinatorTask())
	d := swDispatch()
	d.Owner = &scheduledwork.SessionRef{Name: parent.Name, UID: parent.UID}
	if _, _, _, err := f.srv.CreateScheduledSession(context.Background(), swRequest(), d); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{root, worker} {
		for _, path := range []string{apiv1.SessionSuspendPath(name), apiv1.SessionResumePath(name)} {
			if w := f.do(http.MethodPost, path, tokHuman, nil); w.Code != http.StatusAccepted {
				t.Fatal(path, w.Code, w.Body.String())
			}
		}
	}
	if len(writes.calls) != 0 {
		t.Fatal("admission wrote ConfigMaps", writes.calls)
	}
	wantNoConfigMaps(t, f)
}

func TestCallerPolicyReapUIDPrecondition(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, meatballRequest())
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	name := decode[apiv1.Session](t, w).Name
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok {
			options := (&client.DeleteOptions{}).ApplyOptions(opts)
			if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != obj.GetUID() {
				t.Fatal("reap omitted its UID precondition")
			}
			if err := c.Delete(ctx, obj); err != nil {
				return err
			}
			foreign := obj.(*v1alpha1.AgentSession).DeepCopy()
			foreign.UID, foreign.ResourceVersion, foreign.Spec.Parent = "replacement-uid", "", humanSA
			if err := c.Create(ctx, foreign); err != nil {
				return err
			}
			// The fake client ignores UID preconditions; simulate the API
			// server's refusal rather than deleting the replacement in the fake.
			return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, obj.GetName(), fmt.Errorf("UID precondition failed"))
		}
		return c.Delete(ctx, obj, opts...)
	}})
	if w := f.do(http.MethodDelete, apiv1.SessionPath(name), tokMeatball, nil); w.Code < 400 {
		t.Fatal("reap accepted a replacement", w.Code, w.Body.String())
	}
	if got := f.session(name); got.UID != "replacement-uid" || !got.DeletionTimestamp.IsZero() {
		t.Fatal("reap deleted the replacement")
	}
}

func TestCallerPolicyReadFailureDoesNotFallBack(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*v1alpha1.CallerPolicy); ok {
			return apierrors.NewServiceUnavailable("policy read unavailable")
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, tokMeatball, nil), 503, apiv1.CodeUnavailable)
	// Another unconfigured ServiceAccount still gets the original 403 without
	// reading a policy, even while the policy API is unavailable.
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, tokStranger, nil), 403, apiv1.CodeForbidden)
}

func TestCallerPolicyRetainedOutcomeAndLegacyLimits(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, meatballRequest())
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	created := decode[apiv1.Session](t, w)
	s := f.session(created.Name)
	s.Status.TaskOutcome = outcomeStatus(string(s.UID), "original-pod")
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	w = f.doHeader(http.MethodGet, apiv1.SessionPath(s.Name), tokMeatball, nil,
		http.Header{apiv1.TaskOutcomeHeader: []string{apiv1.TaskOutcomeV1}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	v := decode[apiv1.Session](t, w)
	if v.TaskOutcome == nil || v.TaskOutcome.Record.Summary != s.Status.TaskOutcome.Record.Summary || v.Limits.MaxWorkers == nil {
		t.Fatal("policy caller lost its own outcome or bounds", w.Body.String())
	}
	for _, token := range []string{tokHuman, tokClient} {
		w = f.do(http.MethodGet, apiv1.SessionPath(s.Name), token, nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		var legacyLimits struct {
			Timeout  string `json:"timeout,omitempty"`
			MaxTurns int32  `json:"maxTurns,omitempty"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw["limits"]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&legacyLimits); err != nil {
			t.Fatalf("old strict reader broke: %v", err)
		}
		w = f.doHeader(http.MethodGet, apiv1.SessionPath(s.Name), token, nil,
			http.Header{apiv1.CallerPolicyLimitsHeader: []string{apiv1.CallerPolicyLimitsV1}})
		if w.Code != 200 || decode[apiv1.Session](t, w).Limits.MaxWorkers == nil {
			t.Fatal("explicit limits negotiation failed", w.Code, w.Body.String())
		}
	}
}

func TestCallerPolicyLiveTokenAndSchema(t *testing.T) {
	l := startLive(t)
	ctx := context.Background()
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: callerPolicyNamespace, Name: "dev-env-meatball"}}
	if err := l.k8s.Create(ctx, sa); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.k8s.Delete(ctx, sa) })
	token := l.token(meatballAccount, []string{apiv1.TokenAudience}, nil)
	l.want(http.MethodGet, apiv1.SessionsPath, token, nil, http.StatusForbidden)
	p := meatballPolicy()
	p.UID = ""
	if err := l.k8s.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.k8s.Delete(ctx, p) })
	r := meatballRequest()
	r.IdempotencyKey = "live-caller-policy"
	raw := l.want(http.MethodPost, apiv1.SessionsPath, token, r, http.StatusCreated)
	var created apiv1.Session
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	var session v1alpha1.AgentSession
	if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: created.Name}, &session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.k8s.Delete(ctx, &session) })
	if session.Spec.Limits.MaxWorkers == nil || *session.Spec.Limits.MaxWorkers != 4 || session.Spec.Parent != meatballAccount || session.Spec.Effort != "xhigh" {
		t.Fatal("live schema pruned or changed caller bounds")
	}
	l.want(http.MethodPost, apiv1.SessionsPath, token, r, http.StatusOK)
	l.want(http.MethodGet, apiv1.SessionPath(created.Name), token, nil, http.StatusOK)
	l.want(http.MethodGet, apiv1.SessionPath("not-owned"), token, nil, http.StatusForbidden)
	l.want(http.MethodGet, apiv1.GrantsPath, token, nil, http.StatusForbidden)
	l.want(http.MethodDelete, apiv1.SessionPath(created.Name), token, nil, http.StatusAccepted)
	if err := l.k8s.Delete(ctx, p); err != nil {
		t.Fatal(err)
	}
	l.want(http.MethodGet, apiv1.SessionsPath, token, nil, http.StatusForbidden)
}

func TestCallerPolicyStaleSchemaCannotPruneWorkerCap(t *testing.T) {
	f := meatballFixture(t, meatballPolicy())
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if s, ok := obj.(*v1alpha1.AgentSession); ok {
			options := (&client.CreateOptions{}).ApplyOptions(opts)
			if options.FieldValidation == metav1.FieldValidationStrict {
				return apierrors.NewBadRequest("strict decoding error: unknown field spec.limits.maxWorkers")
			}
			// An older installed AgentSession CRD silently prunes this bound
			// unless the operator explicitly requests strict field validation.
			s.Spec.Limits.MaxWorkers = nil
		}
		return c.Create(ctx, obj, opts...)
	}})
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokMeatball, meatballRequest())
	if w.Code < 400 || f.sessionCount() != 0 {
		t.Fatal("stale schema silently removed the worker cap", w.Code, w.Body.String())
	}
}
