package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func newCanaryFixture(t *testing.T, provider string, funcs interceptor.Funcs) (*fixture, testutil.Fixture) {
	t.Helper()
	binding := testutil.New(t, provider, clientSA)
	create := funcs.Create
	funcs.Create = func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		if s, ok := o.(*v1alpha1.AgentSession); ok {
			s.UID = "original-canary"
			s.Generation = 1
		}
		if create != nil {
			return create(ctx, c, o, opts...)
		}
		return c.Create(ctx, o, opts...)
	}
	f := newFixtureWith(t, funcs, binding.Catalog, binding.Template)
	f.tmpl = binding.Templates
	f.srv.PrivateProjectCanaries = binding.Policy
	f.srv.PrivateHistoryRetention = true
	f.srv.ManagedCodexTasks = true
	f.srv.Projects = &CatalogBinding{Key: client.ObjectKeyFromObject(binding.Catalog), CloneOwner: testutil.Owner}
	f.srv.PrivateCanaryTemplates = client.ObjectKeyFromObject(binding.Template)
	return f, binding
}

func TestProjectCanaryIDOnlyAndDisabledRequestsReadNoAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, token, body string
		disable           func(*Server)
	}{
		{"disabled", tokClient, `{"canaryID":"claude-rules"}`, func(s *Server) { s.PrivateProjectCanaries = nil }},
		{"retention-off", tokClient, `{"canaryID":"claude-rules"}`, func(s *Server) { s.PrivateHistoryRetention = false }},
		{"general-gate-on", tokClient, `{"canaryID":"claude-rules"}`, func(s *Server) { s.PrivateProjectTasks = true }},
		{"wrong-client", tokHuman, `{"canaryID":"claude-rules"}`, nil},
		{"unknown", tokClient, `{"canaryID":"unknown"}`, nil},
		{"empty", tokClient, `{"canaryID":""}`, nil},
		{"case", tokClient, `{"CanaryID":"claude-rules"}`, nil},
		{"duplicate", tokClient, `{"canaryID":"claude-rules","canaryID":"claude-rules"}`, nil},
		{"empty-prompt-override", tokClient, `{"canaryID":"claude-rules","prompt":""}`, nil},
		{"empty-workspace-override", tokClient, `{"canaryID":"claude-rules","workspace":null}`, nil},
		{"limits-override", tokClient, `{"canaryID":"claude-rules","limits":{"timeout":"1s"}}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, creates := 0, 0
			f, _ := newCanaryFixture(t, "claude", interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				if _, ok := o.(*corev1.ConfigMap); ok {
					reads++
				}
				return c.Get(ctx, key, o, opts...)
			}, Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				creates++
				return c.Create(ctx, o, opts...)
			}})
			if tc.disable != nil {
				tc.disable(f.srv)
			}
			w := f.do(http.MethodPost, apiv1.SessionsPath, tc.token, tc.body)
			if w.Code < 400 || reads != 0 || creates != 0 {
				t.Fatalf("refusal performed authority/resource operation: status=%d reads=%d creates=%d %s", w.Code, reads, creates, w.Body.String())
			}
		})
	}
}

func TestProjectCanaryOriginalSurvivesEarlyDeleteTerminalAndLostACK(t *testing.T) {
	for _, state := range []string{"early-delete", "terminal", "lost-ack"} {
		t.Run(state, func(t *testing.T) {
			creates, reads := 0, 0
			f, b := newCanaryFixture(t, "claude", interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				creates++
				if err := c.Create(ctx, o, opts...); err != nil {
					return err
				}
				if state == "lost-ack" {
					return errors.New("synthetic acknowledgement lost")
				}
				return nil
			}, Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				if _, ok := o.(*corev1.ConfigMap); ok {
					reads++
				}
				return c.Get(ctx, key, o, opts...)
			}})
			body := map[string]string{"canaryID": b.Record.ID}
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, body)
			wantStatus := http.StatusCreated
			if state == "lost-ack" {
				wantStatus = http.StatusOK
			}
			if w.Code != wantStatus {
				t.Fatal(w.Code, w.Body.String())
			}
			var original v1alpha1.AgentSession
			key := types.NamespacedName{Namespace: sessionNS, Name: b.Record.SessionName()}
			if err := f.c.Get(context.Background(), key, &original); err != nil {
				t.Fatal(err)
			}
			if original.UID == "" || !slices.Contains(original.Finalizers, v1alpha1.ProjectCanaryFinalizer) || original.Status.PrivateHistory != nil || original.Spec.Workspace != nil || f.srv.PrivateProjectTasks {
				t.Fatal("create lacks independent permanent guard or widened global admission")
			}
			if state == "early-delete" {
				if err := f.c.Delete(context.Background(), &original); err != nil {
					t.Fatal(err)
				}
			}
			if state == "terminal" {
				original.Status.Phase = v1alpha1.PhaseSuspended
				if err := f.c.Status().Update(context.Background(), &original); err != nil {
					t.Fatal(err)
				}
			}
			beforeReads := reads
			w = f.do(http.MethodPost, apiv1.SessionsPath, tokClient, body)
			if w.Code != http.StatusOK || creates != 1 || reads != beforeReads {
				t.Fatalf("retry rearmed/read fresh authority: %d creates=%d reads=%d %s", w.Code, creates, reads, w.Body.String())
			}
			var kept v1alpha1.AgentSession
			if err := f.c.Get(context.Background(), key, &kept); err != nil || kept.UID != original.UID || !slices.Contains(kept.Finalizers, v1alpha1.ProjectCanaryFinalizer) {
				t.Fatal("original identity was released", err)
			}
			if state == "early-delete" && kept.DeletionTimestamp.IsZero() {
				t.Fatal("early deletion was not retained")
			}
		})
	}
}

func TestProjectCanaryConcurrentReplicasRetainOneAtomicIdentity(t *testing.T) {
	f, b := newCanaryFixture(t, "codex", interceptor.Funcs{})
	// Independent replicas have independent in-process locks and share only the API.
	other := &Server{Client: f.srv.Client, Live: f.srv.Live, Auth: f.srv.Auth, Policy: f.srv.Policy, Templates: f.srv.Templates, Log: f.srv.Log, Now: f.srv.Now, ManagedCodexTasks: true, PrivateHistoryRetention: true, PrivateProjectCanaries: b.Policy, Projects: f.srv.Projects, PrivateCanaryTemplates: f.srv.PrivateCanaryTemplates}
	handlers := []http.Handler{f.h, other.Handler()}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, h := range handlers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, apiv1.SessionsPath, strings.NewReader(`{"canaryID":"`+b.Record.ID+`"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tokClient)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusOK && code != http.StatusCreated {
			t.Fatal("concurrent original confirmation refused", code)
		}
	}
	var sessions v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &sessions); err != nil || len(sessions.Items) != 1 || sessions.Items[0].Name != b.Record.SessionName() {
		t.Fatal("replicas created multiple identities", err)
	}
}

func TestProjectCanaryPolicyAndTemplateDriftNeverRearms(t *testing.T) {
	f, b := newCanaryFixture(t, "claude", interceptor.Funcs{})
	body := map[string]string{"canaryID": b.Record.ID}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, body); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	changed := b.Record
	changed.Prompt += " Changed reviewed instruction."
	doc, _ := json.Marshal([]projectcanary.Record{changed})
	f.srv.PrivateProjectCanaries, _ = projectcanary.Parse(string(doc))
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, body); w.Code != http.StatusConflict {
		t.Fatal("changed policy rearmed original", w.Code, w.Body.String())
	}
	var sessions v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &sessions); err != nil || len(sessions.Items) != 1 {
		t.Fatal(err, "multiple original sessions")
	}
	f, b = newCanaryFixture(t, "claude", interceptor.Funcs{})
	var cm corev1.ConfigMap
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(b.Template), &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data["templates.yaml"] = "invalid"
	if err := f.c.Update(context.Background(), &cm); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, body); w.Code < 400 {
		t.Fatal("unreviewed template created a canary")
	}
	if err := f.c.List(context.Background(), &sessions); err != nil || len(sessions.Items) != 0 {
		t.Fatal(err, "drift created session")
	}
}

func TestProjectCanaryUnconfirmedCreateRetainsSameNameOnRetry(t *testing.T) {
	creates, sessionReads := 0, 0
	f, b := newCanaryFixture(t, "claude", interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			creates++
			return c.Create(ctx, o, opts...)
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if _, ok := o.(*v1alpha1.AgentSession); ok {
				sessionReads++
				if sessionReads == 2 {
					return errors.New("synthetic original confirmation unavailable")
				}
			}
			return c.Get(ctx, key, o, opts...)
		},
	})
	body := map[string]string{"canaryID": b.Record.ID}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, body); w.Code < 500 {
		t.Fatal("unknown confirmation reported admission", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, body); w.Code != http.StatusOK || creates != 1 {
		t.Fatal("unconfirmed create rearmed", w.Code, creates, w.Body.String())
	}
	var sessions v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &sessions); err != nil || len(sessions.Items) != 1 || sessions.Items[0].Name != b.Record.SessionName() || !slices.Contains(sessions.Items[0].Finalizers, v1alpha1.ProjectCanaryFinalizer) {
		t.Fatal("lost original retained identity", err)
	}
}

func TestProjectCanaryRejectsSessionAndCoordinatorCallersBeforeCatalogRead(t *testing.T) {
	for _, kind := range []callerKind{kindSession, kindCoordinator} {
		reads := 0
		f, b := newCanaryFixture(t, "claude", interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			reads++
			return c.Get(ctx, key, o, opts...)
		}})
		_, _, err := f.srv.createProjectCanary(context.Background(), httptest.NewRecorder(), &caller{kind: kind, parent: clientSA}, b.Record.ID)
		if err == nil || reads != 0 {
			t.Fatal("session/coordinator canary admission widened", err, reads)
		}
	}
}

func TestProjectCanarySessionCannotUseLegacyChildCreate(t *testing.T) {
	f, b := newCanaryFixture(t, "claude", interceptor.Funcs{})
	executor := &fakeExec{}
	f.srv.Exec = executor
	body, err := json.Marshal(task())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, apiv1.SessionsPath, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	_, _, err = f.srv.createSession(context.Background(), httptest.NewRecorder(), req, &caller{kind: kindSession, session: b.Session, parent: b.Session.Name, profile: "canary", depth: 1})
	var refusal *apiError
	if !errors.As(err, &refusal) || refusal.status != http.StatusForbidden {
		t.Fatal("canary executor can dispatch legacy children")
	}
	var sessions v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &sessions); err != nil || len(sessions.Items) != 0 {
		t.Fatal("child refusal wrote a Session", err)
	}
	if len(executor.cmds) != 0 {
		t.Fatal("child refusal invoked a provider runner")
	}
}
