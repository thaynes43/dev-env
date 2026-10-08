package apiserver

// Unit-test fixtures: the controller-runtime fake client stands in for the API
// server and the cache, and fakeAuth for the TokenReview. The envtest suite
// (envtest_test.go) runs the same handlers against a real kube-apiserver and a
// real TokenReview.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/templates"
)

const (
	sessionNS = "dev-agents"
	humanSA   = "dev-env-system/dev-env-human"
	clientSA  = "dev/dev-env"

	// Tokens fakeAuth knows. A test token is never a real one.
	tokHuman    = "tok-human"
	tokClient   = "tok-client"
	tokStranger = "tok-stranger"
)

var testScheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		panic(err)
	}
	return s
}()

// fakeAuth maps tokens to identities. An unknown token is a 401, as the
// TokenReview's would be.
type fakeAuth map[string]Identity

func (f fakeAuth) Authenticate(_ context.Context, token string) (Identity, error) {
	id, ok := f[token]
	if !ok {
		return Identity{}, unauthenticated("the token was not accepted for audience %s", apiv1.TokenAudience)
	}
	return id, nil
}

func saIdentity(ref string) Identity {
	ns, name, _ := strings.Cut(ref, "/")
	return Identity{Username: serviceAccountPrefix + ns + ":" + name, Namespace: ns, ServiceAccount: name}
}

// fixture is a Server on a fake client.
type fixture struct {
	t      *testing.T
	c      client.WithWatch
	auth   fakeAuth
	srv    *Server
	h      http.Handler
	now    time.Time
	tmpl   *templates.Templates
	tmplOK bool
}

func newFixture(t *testing.T, objs ...client.Object) *fixture {
	return newFixtureWith(t, interceptor.Funcs{}, objs...)
}

func newFixtureWith(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) *fixture {
	t.Helper()
	f := &fixture{t: t, now: time.Date(2026, 10, 6, 17, 22, 26, 0, time.UTC)}
	// The fake client sets no creation time; the API server would.
	create := funcs.Create
	funcs.Create = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		obj.SetCreationTimestamp(metav1.NewTime(f.now))
		if create != nil {
			return create(ctx, c, obj, opts...)
		}
		return c.Create(ctx, obj, opts...)
	}
	f.c = fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.AgentSession{}, &v1alpha1.AccessGrant{}).
		WithInterceptorFuncs(funcs).
		Build()
	f.auth = fakeAuth{
		tokHuman:    saIdentity(humanSA),
		tokClient:   saIdentity(clientSA),
		tokStranger: saIdentity("upgrade-agent/alert-responder"),
	}
	b, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if f.tmpl, err = templates.Parse(map[string]string{templates.Key: string(b)}); err != nil {
		t.Fatal(err)
	}
	f.tmplOK = true
	f.srv = &Server{
		Client: f.c,
		Live:   f.c,
		Auth:   f.auth,
		Policy: Policy{
			Human:                 humanSA,
			Clients:               []string{clientSA},
			SessionNamespace:      sessionNS,
			SessionServiceAccount: controller.ServiceAccountName,
			ActivityNamespace:     "dev-env-system",
		},
		Templates: func(context.Context) (*templates.Templates, error) {
			if !f.tmplOK {
				return nil, os.ErrNotExist
			}
			return f.tmpl, nil
		},
		Log: logr.Discard(),
		Now: func() time.Time { return f.now },
	}
	f.h = f.srv.Handler()
	return f
}

// sessionPod adds a session, as the reconciler would have built it, with its
// pod, and returns a token bound to that pod.
func (f *fixture) sessionPod(name, profile string, depth int) string {
	f.t.Helper()
	ctx := context.Background()
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uuid.NewUUID(),
			Labels:     map[string]string{v1alpha1.LabelDepth: itoa(depth)},
			Finalizers: []string{controller.Finalizer}},
		Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: humanSA},
	}
	if err := f.c.Create(ctx, s); err != nil {
		f.t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uuid.NewUUID(),
			Labels:          map[string]string{v1alpha1.LabelProfile: profile},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "x"}}},
	}
	if err := f.c.Create(ctx, pod); err != nil {
		f.t.Fatal(err)
	}
	tok := "tok-session-" + name
	id := saIdentity(sessionNS + "/" + controller.ServiceAccountName)
	id.PodName, id.PodUID = name, string(pod.UID)
	f.auth[tok] = id
	return tok
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// do sends a request and returns the recorder.
func (f *fixture) do(method, path, token string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		j, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rd = bytes.NewReader(j)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	return w
}

// task is a valid plan 01 create request.
func task() apiv1.CreateSessionRequest {
	return apiv1.CreateSessionRequest{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Effort: "xhigh", Prompt: "fix the docs"}
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

// wantError checks a refusal's status and code.
func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) apiv1.Error {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	e := decode[apiv1.ErrorResponse](t, w).Error
	if e.Code != code {
		t.Fatalf("code %q, want %q: %s", e.Code, code, e.Message)
	}
	return e
}

func (f *fixture) session(name string) *v1alpha1.AgentSession {
	f.t.Helper()
	var s v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &s); err != nil {
		f.t.Fatal(err)
	}
	return &s
}
