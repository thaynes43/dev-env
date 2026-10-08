package broker

// The envtest suite's harness: a real kube-apiserver (internal/testenv) with the
// CRDs installed and the broker's RBAC and admission guard exactly as haynes-ops
// deploys them, copied verbatim into testdata/haynes-ops/ from haynes-ops main
// 567afd76 (#3528, plan 07 H1; #3531; #3533; #3535), kubernetes/main/apps/dev-env-system/
// rbac/app/. Copy them again when haynes-ops changes them:
//
//   - broker.yaml: the broker's ClusterRole (bindings; bind on the catalog by
//     resourceNames) and its Roles in dev-agents and dev-env-system;
//   - broker-guard.yaml: the ValidatingAdmissionPolicy dev-env-broker-guard,
//     which holds the broker to objects named grant-<id>;
//   - grant-catalog.yaml: the catalog roles (dev-env-grant-breakglass is not
//     there yet; the broker binds it by name all the same);
//   - operator.yaml: the operator's Roles, for the checks that it cannot bind.
//
// Each broker runs as cmd/dev-env-operator runs it (NewManager), as the
// impersonated ServiceAccount dev-env-system/dev-env-broker, with leader
// election on its Lease. envtest has no kubelet, scheduler or garbage collector:
// the tests create the session pods and set them Running by hand.
//
// It starts its API server only when an envtest case runs, so the unit tests
// need no binaries. `make test` provides them (KUBEBUILDER_ASSETS). The cases run
// one after another, each with its own broker.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/testenv"
)

const (
	sessionNS    = "dev-agents"
	systemNS     = "dev-env-system"
	toolsNS      = "dev-tools"
	brokerUser   = "system:serviceaccount:" + systemNS + ":dev-env-broker"
	operatorUser = "system:serviceaccount:" + systemNS + ":dev-env-operator"
	// outsideRole is a ClusterRole that is not in the catalog.
	outsideRole = "dev-env-grant-other"
	// wait bounds every poll; the broker reacts in milliseconds.
	wait = 30 * time.Second
)

// The granted namespaces the tests use.
var appNamespaces = []string{"frontend", "home-automation", "media"}

var (
	envOnce   sync.Once
	env       *testenv.Env
	envErr    error
	scheme    = runtime.NewScheme()
	admin     client.Client
	adminCfg  *rest.Config
	brokerCfg *rest.Config
	seq       atomic.Int32
	logs      = &logBuffer{}
)

// logBuffer collects log lines from many goroutines.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestMain(m *testing.M) {
	code := m.Run()
	if env != nil {
		if err := env.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "envtest stop:", err)
		}
	}
	os.Exit(code)
}

// suite starts the API server once and installs what haynes-ops provides.
func suite(t *testing.T) {
	t.Helper()
	envOnce.Do(func() { envErr = startSuite() })
	if envErr != nil {
		t.Fatalf("envtest: %v", envErr)
	}
}

func startSuite() error {
	var err error
	if env, err = testenv.Start(); err != nil {
		return err
	}
	adminCfg = env.Config
	// Every line the brokers log, to check that no token is ever logged; also
	// on stderr with BROKER_TEST_LOG=1.
	var sink io.Writer = logs
	if os.Getenv("BROKER_TEST_LOG") != "" {
		sink = io.MultiWriter(logs, os.Stderr)
	}
	ctrl.SetLogger(logr.FromSlogHandler(slog.NewTextHandler(sink, nil)))
	if err := errors.Join(clientgoscheme.AddToScheme(scheme), v1alpha1.AddToScheme(scheme)); err != nil {
		return err
	}
	if admin, err = client.New(adminCfg, client.Options{Scheme: scheme}); err != nil {
		return err
	}
	ctx := context.Background()
	objs := []client.Object{}
	for _, ns := range append([]string{sessionNS, systemNS, toolsNS}, appNamespaces...) {
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	}
	objs = append(objs,
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "dev-env-agent"}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "dev-env-workbench"}},
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: outsideRole},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"}}},
		},
	)
	for _, o := range objs {
		if err := admin.Create(ctx, o); err != nil {
			return fmt.Errorf("create %T %s: %w", o, o.GetName(), err)
		}
	}
	files, err := filepath.Glob(filepath.Join("testdata", "haynes-ops", "*.yaml"))
	if err != nil {
		return err
	}
	if len(files) != 4 {
		return fmt.Errorf("want the 4 haynes-ops fixtures, found %v", files)
	}
	for _, f := range files {
		if err := apply(ctx, f); err != nil {
			return err
		}
	}
	credentialFiles, err := filepath.Glob(filepath.Join("testdata", "credentials", "*.yaml"))
	if err != nil {
		return err
	}
	for _, f := range credentialFiles {
		if err := apply(ctx, f); err != nil {
			return err
		}
	}
	// The emitted policy surface, projected from the live Cilium schema. No
	// Cilium controller or dataplane runs in envtest.
	if err := apply(ctx, filepath.Join("testdata", "cilium", "ciliumnetworkpolicies.yaml")); err != nil {
		return err
	}
	brokerCfg = impersonate(brokerUser)
	return waitForGuard(ctx)
}

// apply creates every object of a multi-document YAML file.
func apply(ctx context.Context, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	for {
		var u unstructured.Unstructured
		if err := dec.Decode(&u.Object); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("%s: %w", path, err)
		}
		if len(u.Object) == 0 {
			continue
		}
		if err := admin.Create(ctx, &u); err != nil {
			return fmt.Errorf("%s: create %s %s: %w", path, u.GetKind(), u.GetName(), err)
		}
	}
}

// waitForGuard waits until dev-env-broker-guard is enforced: the API server
// loads a new admission policy a moment after it is created.
func waitForGuard(ctx context.Context) error {
	c, err := client.New(brokerCfg, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	for {
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "not-a-grant"}}
		err := c.Create(ctx, sa)
		switch {
		case apierrors.IsForbidden(err) && strings.Contains(err.Error(), "dev-env-broker-guard:"):
			return nil
		case err == nil:
			if err := admin.Delete(ctx, sa); err != nil {
				return err
			}
		case !apierrors.IsForbidden(err):
			return fmt.Errorf("probe the broker guard: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dev-env-broker-guard was not enforced within %s: last %w", wait, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// impersonate is a config that acts as a ServiceAccount of dev-env-system.
func impersonate(user string) *rest.Config {
	cfg := rest.CopyConfig(adminCfg)
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: user,
		Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:" + systemNS, "system:authenticated"},
	}
	return cfg
}

// running is one broker process.
type running struct {
	b      *Broker
	clock  *clocktesting.FakePassiveClock
	notes  *notes
	inst   *recorder
	cancel context.CancelFunc
	done   chan error

	mu   sync.Mutex
	seen map[string]int
}

// startBroker runs a broker as its ServiceAccount until the test ends. A nil
// clock is a fake one at the real time; a nil installer installs nothing.
func startBroker(t *testing.T, clk *clocktesting.FakePassiveClock, inst *recorder, configure ...func(*Options)) *running {
	t.Helper()
	if clk == nil {
		clk = clocktesting.NewFakePassiveClock(time.Now())
	}
	r := &running{clock: clk, notes: &notes{}, inst: inst, done: make(chan error, 1), seen: map[string]int{}}
	o := Options{
		SessionNamespace: sessionNS, PolicyNamespace: systemNS,
		LeaderElect:   true,
		LeaseDuration: 4 * time.Second, RenewDeadline: 3 * time.Second, RetryPeriod: 250 * time.Millisecond,
		MetricsAddr: "0", ProbeAddr: "0",
		Clock: clk, Notifier: r.notes,
		skipNameValidation: true,
		reconciled: func(n types.NamespacedName) {
			r.mu.Lock()
			r.seen[n.Name]++
			r.mu.Unlock()
		},
	}
	if inst != nil {
		o.Installer = inst
	}
	for _, edit := range configure {
		edit(&o)
	}
	mgr, b, err := NewManager(brokerCfg, o)
	if err != nil {
		t.Fatal(err)
	}
	r.b = b
	ctx, cancel := context.WithCancel(ctrl.LoggerInto(context.Background(), ctrl.Log))
	r.cancel = cancel
	go func() { r.done <- mgr.Start(ctx) }()
	t.Cleanup(func() { r.stop(t) })
	select {
	case <-mgr.Elected():
	case err := <-r.done:
		t.Fatalf("the broker stopped before it was elected: %v", err)
	case <-time.After(wait):
		t.Fatal("the broker was not elected")
	}
	return r
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	if r.cancel == nil {
		return
	}
	r.cancel()
	r.cancel = nil
	select {
	case err := <-r.done:
		if err != nil {
			t.Errorf("the broker returned %v after a cancel", err)
		}
	case <-time.After(wait):
		t.Error("the broker did not stop")
	}
}

// reconciles is how many reconciles of the grant this broker finished.
func (r *running) reconciles(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[name]
}

// settle pokes a grant and waits for this broker to reconcile it again.
func (r *running) settle(t *testing.T, name string) {
	t.Helper()
	before := r.reconciles(name)
	poke(t, name)
	eventually(t, "a reconcile of "+name, func() (bool, string) {
		n := r.reconciles(name)
		return n > before, fmt.Sprintf("%d reconciles", n)
	})
}

// notes is a Notifier that counts.
type notes struct {
	mu sync.Mutex
	n  map[string]int
}

func (n *notes) NotifyPending(_ context.Context, g *v1alpha1.AccessGrant) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.n == nil {
		n.n = map[string]int{}
	}
	n.n[g.Name]++
	return nil
}

func (n *notes) count(name string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.n[name]
}

// recorder is an Installer that keeps what it was given.
type recorder struct {
	mu       sync.Mutex
	installs []install
	removes  []remove
}

type remove struct {
	pod   types.UID
	grant string
}

type install struct {
	pod     types.UID
	grant   string
	token   string
	expires time.Time
}

func (r *recorder) Install(_ context.Context, pod *corev1.Pod, g *v1alpha1.AccessGrant, token string, expires time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.installs = append(r.installs, install{pod: pod.UID, grant: g.Name, token: token, expires: expires})
	return nil
}

func (r *recorder) Remove(_ context.Context, pod *corev1.Pod, g *v1alpha1.AccessGrant) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removes = append(r.removes, remove{pod: pod.UID, grant: g.Name})
	return nil
}

// of returns what the installer did for one grant. A broker reconciles every
// grant in the namespace, so it also installs the active grants that earlier
// cases left behind.
func (r *recorder) of(grant string) (installs []install, removes []types.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, i := range r.installs {
		if i.grant == grant {
			installs = append(installs, i)
		}
	}
	for _, rm := range r.removes {
		if rm.grant == grant {
			removes = append(removes, rm.pod)
		}
	}
	return installs, removes
}

// eventually polls cond every 100 ms until it holds or wait passes.
func eventually(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		ok, last := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited %s for %s; last: %s", wait, what, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// name gives each test object a unique name.
func name(prefix string) string {
	return fmt.Sprintf("%s-t%d", prefix, seq.Add(1))
}

type sessionOpt func(*v1alpha1.AgentSession)

func profile(p string) sessionOpt { return func(s *v1alpha1.AgentSession) { s.Spec.Profile = p } }
func repo(r string) sessionOpt    { return func(s *v1alpha1.AgentSession) { s.Spec.Repo = r } }

// newSession creates a session and its Running pod.
func newSession(t *testing.T, opts ...sessionOpt) (*v1alpha1.AgentSession, *corev1.Pod) {
	t.Helper()
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name("s")},
		Spec: v1alpha1.AgentSessionSpec{
			Repo: "haynesnetwork", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask,
			Model: "claude-opus-5-5", Effort: "xhigh", Prompt: "fix the docs", Profile: "full",
		},
	}
	for _, o := range opts {
		o(s)
	}
	if err := admin.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s, runningPod(t, s)
}

// runningPod creates the session's pod, as the operator would, and sets it
// Running, as the kubelet would.
func runningPod(t *testing.T, s *v1alpha1.AgentSession) *corev1.Pod {
	t.Helper()
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: sessionNS, Name: s.Name,
			Labels: map[string]string{
				v1alpha1.LabelAppName: v1alpha1.AppNameSession,
				v1alpha1.LabelSession: s.Name,
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName:           "dev-env-agent",
			AutomountServiceAccountToken: ptr.To(false),
			Containers:                   []corev1.Container{{Name: "agent", Image: "ghcr.io/thaynes43/dev-env:2.2.0"}},
		},
	}
	if err := admin.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	if err := admin.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	return pod
}

type grantOpt func(*v1alpha1.AccessGrant)

func ttl(d time.Duration) grantOpt { return func(g *v1alpha1.AccessGrant) { g.Spec.TTL.Duration = d } }

func role(r string, ns ...string) grantOpt {
	return func(g *v1alpha1.AccessGrant) { g.Spec.Kube = &v1alpha1.KubeGrant{Role: r, Namespaces: ns} }
}

func breakglass(g *v1alpha1.AccessGrant) {
	g.Spec.Type = v1alpha1.GrantBreakglass
	g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass}
	g.Spec.TTL.Duration = 30 * time.Minute
}

// newGrant creates a grant as the /v1 API would (D-56): workloads in
// home-automation for an hour unless opts say otherwise.
func newGrant(t *testing.T, s *v1alpha1.AgentSession, opts ...grantOpt) string {
	t.Helper()
	g := &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: sessionNS, Name: name("grant"),
			Labels: map[string]string{v1alpha1.LabelSession: s.Name},
		},
		Spec: v1alpha1.AccessGrantSpec{
			Requester: v1alpha1.GrantRequester{Session: s.Name, SessionUID: s.UID, Repo: s.Spec.Repo, Profile: s.Spec.Profile, Agent: s.Spec.Agent},
			Type:      v1alpha1.GrantKube,
			Kube:      &v1alpha1.KubeGrant{Role: v1alpha1.RoleWorkloads, Namespaces: []string{"home-automation"}},
			TTL:       metav1.Duration{Duration: time.Hour},
			Reason:    "patch an image to test it",
		},
	}
	for _, o := range opts {
		o(g)
	}
	if err := admin.Create(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	return g.Name
}

// newPolicy creates a standing policy for workloads in the namespaces, for
// profile full, and deletes it when the test ends.
func newPolicy(t *testing.T, policyName string, edit func(*v1alpha1.GrantPolicy), ns ...string) {
	t.Helper()
	p := &v1alpha1.GrantPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: policyName},
		Spec: v1alpha1.GrantPolicySpec{
			Description: "test",
			Profiles:    []string{"full"},
			Type:        v1alpha1.GrantKube,
			Kube:        &v1alpha1.KubePolicy{Roles: []string{v1alpha1.RoleWorkloads}, Namespaces: ns},
			MaxTTL:      metav1.Duration{Duration: 2 * time.Hour},
		},
	}
	if edit != nil {
		edit(p)
	}
	if err := admin.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Delete(context.Background(), p); err != nil && !apierrors.IsNotFound(err) {
			t.Error(err)
		}
	})
}

func grant(t *testing.T, n string) *v1alpha1.AccessGrant {
	t.Helper()
	g := &v1alpha1.AccessGrant{}
	if err := admin.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: n}, g); err != nil {
		t.Fatal(err)
	}
	return g
}

// waitPhase waits for a grant's phase and returns the grant.
func waitPhase(t *testing.T, n string, phase v1alpha1.GrantPhase) *v1alpha1.AccessGrant {
	t.Helper()
	var g v1alpha1.AccessGrant
	eventually(t, n+" "+string(phase), func() (bool, string) {
		if err := admin.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: n}, &g); err != nil {
			return false, err.Error()
		}
		return g.Status.Phase == phase, fmt.Sprintf("%+v", g.Status)
	})
	return &g
}

// poke changes an annotation on a grant so the broker reconciles it: a test's
// stand-in for a requeue timer that runs on real time while the broker's clock
// is a fake one.
func poke(t *testing.T, n string) {
	t.Helper()
	g := grant(t, n)
	patch := client.RawPatch(types.MergePatchType, fmt.Appendf(nil, `{"metadata":{"annotations":{"test/poke":"%d"}}}`, seq.Add(1)))
	if err := admin.Patch(context.Background(), g, patch); err != nil {
		t.Fatal(err)
	}
}

func release(t *testing.T, n string) {
	t.Helper()
	if err := admin.Patch(context.Background(), grant(t, n), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"release":true}}`))); err != nil {
		t.Fatal(err)
	}
}

// exists reports whether the object is on the API server.
func exists(t *testing.T, o client.Object) bool {
	t.Helper()
	err := admin.Get(context.Background(), client.ObjectKeyFromObject(o), o)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func sa(n string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: n}}
}

func rb(ns, n string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: n}}
}

func crb(n string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: n}}
}

// waitGone waits until none of the objects exists.
func waitGone(t *testing.T, objs ...client.Object) {
	t.Helper()
	eventually(t, "the grant's objects to go", func() (bool, string) {
		for _, o := range objs {
			if exists(t, o) {
				return false, fmt.Sprintf("%T %s is still there", o, o.GetName())
			}
		}
		return true, ""
	})
}

// review asks the API server who a token is.
func review(t *testing.T, token string) authenticationv1.TokenReviewStatus {
	t.Helper()
	tr := &authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token}}
	if err := admin.Create(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	return tr.Status
}

// tokenClient acts with a bearer token, as an agent with the grant installed.
func tokenClient(t *testing.T, token string) client.Client {
	t.Helper()
	cfg := rest.AnonymousClientConfig(adminCfg)
	cfg.BearerToken = token
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// allowed asks the API server's authorizer whether a ServiceAccount of
// dev-env-system may do something.
func allowed(t *testing.T, user string, ra authorizationv1.ResourceAttributes) bool {
	t.Helper()
	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User:               user,
		Groups:             []string{"system:serviceaccounts", "system:serviceaccounts:" + systemNS, "system:authenticated"},
		ResourceAttributes: &ra,
	}}
	if err := admin.Create(context.Background(), sar); err != nil {
		t.Fatal(err)
	}
	return sar.Status.Allowed
}
