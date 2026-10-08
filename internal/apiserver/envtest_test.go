package apiserver

// The envtest suite: the API served over HTTPS by a controller-runtime manager
// wired as cmd/dev-env-operator wires it (the cache scoped by
// controller.CacheOptions, the APIReader, a Runner), against a real
// kube-apiserver with the CRDs installed. TokenReview is not faked here: envtest's
// API server signs ServiceAccount tokens, so the suite mints real ones with
// TokenRequest, for the dev-env-operator audience and bound to a pod as the
// kubelet's projected token is, and the API reviews them as it does in the
// cluster. envtest has no reconciler, kubelet or garbage collector, so the suite
// creates a session's pod itself and adds the rescue finalizer by hand.
//
// It starts its API server only when an envtest case runs, so the unit tests
// above need no binaries. `make test` provides them (KUBEBUILDER_ASSETS).

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/templates"
	"github.com/thaynes43/dev-env/internal/testenv"
)

var (
	envOnce sync.Once
	env     *testenv.Env
	envErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if env != nil {
		if err := env.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "envtest stop:", err)
		}
	}
	os.Exit(code)
}

func envConfig(t *testing.T) *rest.Config {
	t.Helper()
	envOnce.Do(func() { env, envErr = testenv.Start() })
	if envErr != nil {
		t.Fatalf("envtest: %v", envErr)
	}
	return env.Config
}

// live is the suite's view of a running operator API.
type live struct {
	t    *testing.T
	k8s  client.Client
	cs   kubernetes.Interface
	base string
	http *http.Client
}

func startLive(t *testing.T) *live {
	t.Helper()
	cfg := envConfig(t)
	ctx := context.Background()
	k8s, err := client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{sessionNS, "dev-env-system", "upgrade-agent"} {
		if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
	for _, ref := range []string{humanSA, sessionNS + "/" + controller.ServiceAccountName, "upgrade-agent/alert-responder"} {
		ns, name, _ := strings.Cut(ref, "/")
		if err := k8s.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
	doc, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tkey := types.NamespacedName{Namespace: "dev-env-system", Name: templates.DefaultName}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: tkey.Namespace, Name: tkey.Name}, Data: map[string]string{templates.Key: string(doc)}}
	if err := k8s.Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 testScheme,
		Cache:                  controller.CacheOptions(sessionNS, tkey),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	if err != nil {
		t.Fatal(err)
	}
	api := &Server{
		Client: mgr.GetClient(),
		Live:   mgr.GetAPIReader(),
		Auth:   TokenReviewer{Client: mgr.GetClient()},
		Policy: Policy{
			Human:                 humanSA,
			Clients:               []string{clientSA},
			SessionNamespace:      sessionNS,
			SessionServiceAccount: controller.ServiceAccountName,
		},
		Templates: TemplatesFrom(mgr.GetClient(), tkey),
		Log:       logr.Discard(),
	}
	dir := t.TempDir()
	pool := writeCert(t, dir)
	runner := &Runner{Addr: "127.0.0.1:0", CertDir: dir, Handler: api.Handler()}
	if err := mgr.Add(runner); err != nil {
		t.Fatal(err)
	}
	mctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- mgr.Start(mctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("manager: %v", err)
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for runner.ReadyCheck(nil) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the API never listened")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &live{
		t: t, k8s: k8s, cs: cs, base: "https://" + runner.boundAddr(),
		http: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}},
	}
}

// token mints a ServiceAccount token, as the kubelet or `kubectl create token`
// would: for the given audiences (none means the API server's own), bound to
// pod when pod is not nil.
func (l *live) token(ref string, audiences []string, pod *corev1.Pod) string {
	l.t.Helper()
	ns, name, _ := strings.Cut(ref, "/")
	tr := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{Audiences: audiences}}
	if pod != nil {
		tr.Spec.BoundObjectRef = &authenticationv1.BoundObjectReference{Kind: "Pod", APIVersion: "v1", Name: pod.Name, UID: pod.UID}
	}
	out, err := l.cs.CoreV1().ServiceAccounts(ns).CreateToken(context.Background(), name, tr, metav1.CreateOptions{})
	if err != nil {
		l.t.Fatal(err)
	}
	return out.Status.Token
}

func (l *live) do(method, path, token string, body any) (int, []byte) {
	l.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			l.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, l.base+path, rd)
	if err != nil {
		l.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := l.http.Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		l.t.Fatal(err)
	}
	return resp.StatusCode, b
}

func (l *live) want(method, path, token string, body any, status int) []byte {
	l.t.Helper()
	got, b := l.do(method, path, token, body)
	if got != status {
		l.t.Fatalf("%s %s: %d, want %d: %s", method, path, got, status, b)
	}
	return b
}

func unmarshal[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

func TestEnvtestAPI(t *testing.T) {
	l := startLive(t)
	ctx := context.Background()
	human := l.token(humanSA, []string{apiv1.TokenAudience}, nil)

	// Tom creates a task; the AgentSession appears with its parent from the
	// token and the schema's defaults.
	b := l.want(http.MethodPost, apiv1.SessionsPath, human, task(), http.StatusCreated)
	created := unmarshal[apiv1.Session](t, b)
	var sess v1alpha1.AgentSession
	if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: created.Name}, &sess); err != nil {
		t.Fatalf("the AgentSession: %v", err)
	}
	if sess.Spec.Parent != humanSA || sess.Spec.Base != "origin/main" || sess.Spec.Size != v1alpha1.SizeM ||
		sess.Spec.OperatingMode != v1alpha1.OperatingModeRunning || sess.Labels[v1alpha1.LabelDepth] != "0" {
		t.Errorf("spec %+v labels %v", sess.Spec, sess.Labels)
	}
	if created.Base != "origin/main" || created.Size != "M" || created.Phase != "Pending" {
		t.Errorf("the answer lacks the defaults: %+v", created)
	}
	// Read back at once: the cache may not have it, the API server does.
	got := unmarshal[apiv1.Session](t, l.want(http.MethodGet, apiv1.SessionPath(created.Name), human, nil, http.StatusOK))
	if got.Prompt != "fix the docs" {
		t.Errorf("detail %+v", got)
	}

	t.Run("a token for the API server's own audience is refused", func(t *testing.T) {
		l.want(http.MethodGet, apiv1.FleetPath, l.token(humanSA, nil, nil), nil, http.StatusUnauthorized)
	})
	t.Run("a ServiceAccount the API does not serve is refused", func(t *testing.T) {
		l.want(http.MethodGet, apiv1.FleetPath, l.token("upgrade-agent/alert-responder", []string{apiv1.TokenAudience}, nil), nil, http.StatusForbidden)
	})
	t.Run("the schema's refusals come back by field", func(t *testing.T) {
		for field, mutate := range map[string]func(*apiv1.CreateSessionRequest){
			"size": func(r *apiv1.CreateSessionRequest) { r.Size = "XL" },
			"repo": func(r *apiv1.CreateSessionRequest) { r.Repo = strings.Repeat("r", 101) },
		} {
			req := task()
			mutate(&req)
			e := unmarshal[apiv1.ErrorResponse](t, l.want(http.MethodPost, apiv1.SessionsPath, human, req, http.StatusUnprocessableEntity)).Error
			if e.Code != apiv1.CodeInvalid || len(e.Fields) == 0 || e.Fields[0].Field != field {
				t.Errorf("%s: %+v", field, e)
			}
		}
	})

	// The operator's part, played by the suite: the session's pod, as the
	// reconciler builds it, and the rescue finalizer.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: sess.Name, Namespace: sessionNS,
			Labels:          map[string]string{v1alpha1.LabelProfile: "dev"},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&sess, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "agent"}}},
	}
	if err := l.k8s.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	controllerutil.AddFinalizer(&sess, controller.Finalizer)
	if err := l.k8s.Update(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	agent := l.token(sessionNS+"/"+controller.ServiceAccountName, []string{apiv1.TokenAudience}, pod)

	t.Run("the session's pod posts its heartbeat", func(t *testing.T) {
		l.want(http.MethodPost, protocol.HeartbeatPath(sess.Name), agent, status(sess.Name), http.StatusNoContent)
		var s v1alpha1.AgentSession
		if err := l.k8s.Get(ctx, client.ObjectKeyFromObject(&sess), &s); err != nil {
			t.Fatal(err)
		}
		if a := s.Status.Agent; a == nil || a.Branch != "agent/"+sess.Name || a.Task == nil || s.Status.Usage == nil || s.Status.Usage.CostUSD != "0.4217" {
			t.Errorf("status %+v", s.Status)
		}
		l.want(http.MethodPost, protocol.HeartbeatPath(sess.Name), human, status(sess.Name), http.StatusForbidden)
	})

	var child apiv1.Session
	t.Run("the session starts a child on its own profile", func(t *testing.T) {
		child = unmarshal[apiv1.Session](t, l.want(http.MethodPost, apiv1.SessionsPath, agent, task(), http.StatusCreated))
		if child.Parent != sess.Name || child.Profile != "dev" {
			t.Errorf("child %+v", child)
		}
		// The list is served from the informer cache, so a list right after the
		// create can miss the child until the watch event arrives (as the fleet
		// check below allows too).
		deadline := time.Now().Add(10 * time.Second)
		for {
			mine := unmarshal[apiv1.SessionList](t, l.want(http.MethodGet, apiv1.SessionsPath+"?mine=true", agent, nil, http.StatusOK))
			if len(mine.Sessions) == 1 && mine.Sessions[0].Name == child.Name {
				break
			}
			if len(mine.Sessions) > 1 || time.Now().After(deadline) {
				t.Fatalf("mine %+v", mine)
			}
			time.Sleep(50 * time.Millisecond)
		}
		wider := task()
		wider.Profile = "full"
		l.want(http.MethodPost, apiv1.SessionsPath, agent, wider, http.StatusForbidden)
	})

	t.Run("the fleet reports the templates' revision", func(t *testing.T) {
		var cm corev1.ConfigMap
		if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: "dev-env-system", Name: templates.DefaultName}, &cm); err != nil {
			t.Fatal(err)
		}
		tm, err := templates.Parse(cm.Data)
		if err != nil {
			t.Fatal(err)
		}
		// The cache catches up with the creates above.
		deadline := time.Now().Add(10 * time.Second)
		for {
			fl := unmarshal[apiv1.Fleet](t, l.want(http.MethodGet, apiv1.FleetPath, human, nil, http.StatusOK))
			if fl.Revision != tm.Revision() {
				t.Fatalf("revision %q, want %q (%s)", fl.Revision, tm.Revision(), fl.RevisionError)
			}
			if len(fl.Sessions) == 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("sessions %+v", fl.Sessions)
			}
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("a reap deletes the session and the finalizer holds it", func(t *testing.T) {
		// The child has no pod and no finalizer: nothing to rescue, so it goes.
		l.want(http.MethodDelete, apiv1.SessionPath(child.Name), agent, nil, http.StatusAccepted)
		l.want(http.MethodGet, apiv1.SessionPath(child.Name), human, nil, http.StatusNotFound)

		r := unmarshal[apiv1.Session](t, l.want(http.MethodDelete, apiv1.SessionPath(sess.Name), human, nil, http.StatusAccepted))
		if !r.Reaping {
			t.Errorf("reap answer %+v", r)
		}
		var s v1alpha1.AgentSession
		if err := l.k8s.Get(ctx, client.ObjectKeyFromObject(&sess), &s); err != nil || s.DeletionTimestamp.IsZero() {
			t.Fatalf("the session should wait on its finalizer: %v %v", err, s.DeletionTimestamp)
		}
		// The pod still runs, and still reports.
		l.want(http.MethodPost, protocol.HeartbeatPath(sess.Name), agent, status(sess.Name), http.StatusNoContent)
	})

	t.Run("a pod's token dies with the pod", func(t *testing.T) {
		if err := l.k8s.Delete(ctx, pod); err != nil {
			t.Fatal(err)
		}
		// The API refuses as soon as its cache sees the delete, because the
		// pod is gone (403). The pod is read from the informer cache first, so a
		// call that races the watch event can still pass (204) for a moment.
		// The API server's own TokenReview follows once its 10-second cache of
		// the review expires (401).
		start := time.Now()
		deadline := start.Add(30 * time.Second)
		for {
			code, b := l.do(http.MethodPost, protocol.HeartbeatPath(sess.Name), agent, status(sess.Name))
			if code == http.StatusUnauthorized {
				break
			}
			cacheLag := code == http.StatusNoContent && time.Since(start) < 5*time.Second
			if (code != http.StatusForbidden && !cacheLag) || time.Now().After(deadline) {
				t.Fatalf("a deleted pod's token: %d %s", code, b)
			}
			time.Sleep(500 * time.Millisecond)
		}
	})

	// Release the session the suite held.
	var s v1alpha1.AgentSession
	if err := l.k8s.Get(ctx, client.ObjectKeyFromObject(&sess), &s); err == nil {
		controllerutil.RemoveFinalizer(&s, controller.Finalizer)
		if err := l.k8s.Update(ctx, &s); err != nil && !apierrors.IsNotFound(err) {
			t.Error(err)
		}
	}
}

// TestEnvtestGrants runs the grant routes against the real AccessGrant schema
// (D-54, D-56): the schema's refusals come back as 422 fields, the schema's
// defaults do not split identical requests, and a release passes its rules.
func TestEnvtestGrants(t *testing.T) {
	l := startLive(t)
	ctx := context.Background()
	human := l.token(humanSA, []string{apiv1.TokenAudience}, nil)

	created := unmarshal[apiv1.Session](t, l.want(http.MethodPost, apiv1.SessionsPath, human, task(), http.StatusCreated))
	var sess v1alpha1.AgentSession
	if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: created.Name}, &sess); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: sess.Name, Namespace: sessionNS,
			Labels:          map[string]string{v1alpha1.LabelProfile: "dev"},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&sess, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "agent"}}},
	}
	if err := l.k8s.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = l.k8s.Delete(ctx, pod)
		_ = l.k8s.Delete(ctx, &sess)
	})
	agent := l.token(sessionNS+"/"+controller.ServiceAccountName, []string{apiv1.TokenAudience}, pod)

	t.Run("Tom does not request grants; sessions do", func(t *testing.T) {
		l.want(http.MethodPost, apiv1.GrantsPath, human, kubeReq(), http.StatusForbidden)
	})

	t.Run("the schema refuses a dev-env namespace and an over-long TTL", func(t *testing.T) {
		r := kubeReq()
		r.Namespaces = []string{"frontend", "dev-agents"}
		e := unmarshal[apiv1.ErrorResponse](t, l.want(http.MethodPost, apiv1.GrantsPath, agent, r, http.StatusUnprocessableEntity)).Error
		found := false
		for _, fe := range e.Fields {
			found = found || (fe.Field == "namespaces[1]" && strings.Contains(fe.Message, "no grant reaches the dev-env namespaces (DESIGN-001 6.12)"))
		}
		if e.Code != apiv1.CodeInvalid || !found {
			t.Errorf("dev-agents: %+v", e)
		}
		r = kubeReq()
		r.TTL = "9h"
		e = unmarshal[apiv1.ErrorResponse](t, l.want(http.MethodPost, apiv1.GrantsPath, agent, r, http.StatusUnprocessableEntity)).Error
		if !strings.Contains(e.Message, "ttl runs from 10m to 8h") {
			t.Errorf("9h: %+v", e)
		}
	})

	var kube apiv1.Grant
	t.Run("a request is created with its requester from the token", func(t *testing.T) {
		kube = unmarshal[apiv1.Grant](t, l.want(http.MethodPost, apiv1.GrantsPath, agent, kubeReq(), http.StatusCreated))
		var g v1alpha1.AccessGrant
		if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: kube.Name}, &g); err != nil {
			t.Fatal(err)
		}
		want := v1alpha1.GrantRequester{Session: sess.Name, SessionUID: sess.UID, Repo: "haynes-ops", Profile: "dev", Agent: "claude", Parent: humanSA}
		if g.Spec.Requester != want || g.Labels[v1alpha1.LabelSession] != sess.Name || g.Spec.TTL.Duration != 15*time.Minute {
			t.Errorf("spec %+v labels %v", g.Spec, g.Labels)
		}
		got := unmarshal[apiv1.Grant](t, l.want(http.MethodGet, apiv1.GrantPath(kube.Name), human, nil, http.StatusOK))
		if got.Name != kube.Name || got.Phase != apiv1.GrantPending || got.Session != sess.Name {
			t.Errorf("read back %+v", got)
		}
		mine := unmarshal[apiv1.GrantList](t, l.want(http.MethodGet, apiv1.GrantsPath+"?mine=true", agent, nil, http.StatusOK))
		if len(mine.Items) != 1 || mine.Items[0].Name != kube.Name {
			t.Errorf("mine %+v", mine)
		}
	})

	t.Run("the schema's protocol default does not split identical requests", func(t *testing.T) {
		r := egressReq()
		r.Ports = []apiv1.GrantPort{{Port: 443}}
		eg := unmarshal[apiv1.Grant](t, l.want(http.MethodPost, apiv1.GrantsPath, agent, r, http.StatusCreated))
		if len(eg.Ports) != 1 || eg.Ports[0].Protocol != "TCP" {
			t.Errorf("ports %+v", eg.Ports)
		}
		r.Ports = []apiv1.GrantPort{{Port: 443, Protocol: "TCP"}}
		r.FQDNs = []string{"b.example.com", "a.example.com"}
		if again := unmarshal[apiv1.Grant](t, l.want(http.MethodPost, apiv1.GrantsPath, agent, r, http.StatusOK)); again.Name != eg.Name {
			t.Errorf("merge %s, want %s", again.Name, eg.Name)
		}
	})

	t.Run("a release sets spec.release, which the schema allows", func(t *testing.T) {
		v := unmarshal[apiv1.Grant](t, l.want(http.MethodDelete, apiv1.GrantPath(kube.Name), agent, nil, http.StatusAccepted))
		var g v1alpha1.AccessGrant
		if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: kube.Name}, &g); err != nil {
			t.Fatal(err)
		}
		if !v.Release || !g.Spec.Release || g.Spec.Kube == nil || len(g.Spec.Kube.Namespaces) != 2 {
			t.Errorf("view %+v spec %+v", v, g.Spec)
		}
		l.want(http.MethodDelete, apiv1.GrantPath(kube.Name), agent, nil, http.StatusOK)
	})
}
