package keeper

// The envtest suite: Run, wired as cmd/dev-env-keeper wires it, against a real
// kube-apiserver, as the keeper's ServiceAccount with exactly the Roles that
// haynes-ops gives it (plan 01, "In haynes-ops"; D-52). It shows that those
// Roles are enough (patch on the Secret by name; no create, get, list or watch),
// that the Lease admits one keeper at a time, and that a keeper that stops hands
// the Lease straight to the next.
//
// It starts its API server only when it runs, so the unit tests need no
// binaries. `make test` provides them (KUBEBUILDER_ASSETS).

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

const (
	keeperNS = "dev-env-system"
	keeperSA = "dev-env-keeper"
)

// keeperSecretNames are the four Secrets the keeper's Role names (DESIGN-001
// 6.11). Plan 01 writes the first.
var keeperSecretNames = []string{"dev-env-gh-token", "dev-env-ops-gh-token", "dev-env-claude-live", "dev-env-codex-live"}

// keeperRBAC is the keeper's RBAC as plan 01 asks haynes-ops for it.
func keeperRBAC() []client.Object {
	sa := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: keeperNS, Name: keeperSA}
	return []client.Object{
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Namespace: ghSecret.Namespace, Name: keeperSA},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""}, Resources: []string{"secrets"},
				Verbs: []string{"patch"}, ResourceNames: keeperSecretNames,
			}},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Namespace: ghSecret.Namespace, Name: keeperSA},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: keeperSA},
			Subjects:   []rbacv1.Subject{sa},
		},
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Namespace: keeperNS, Name: keeperSA},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: []string{"get", "create", "update"}},
				{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
			},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Namespace: keeperNS, Name: keeperSA},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: keeperSA},
			Subjects:   []rbacv1.Subject{sa},
		},
	}
}

// onServer polls a condition that reads the API server, gently: every 100 ms,
// for up to 30 s.
func onServer(t *testing.T, what string, cond func() bool) {
	t.Helper()
	poll(t, what, 100*time.Millisecond, 30*time.Second, cond)
}

// running is one keeper process.
type running struct {
	cancel context.CancelFunc
	done   chan error
	logs   *logBuffer
}

func startKeeper(t *testing.T, cfg *rest.Config, appDir string, gh *fakeGitHub) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan error, 1), logs: &logBuffer{}}
	app := gh.app(appDir)
	go func() {
		r.done <- Run(ctx, cfg, Options{
			Namespace:     keeperNS,
			GHTokenSecret: ghSecret,
			GitHubApp:     app,
			Interval:      time.Hour, // one mint per leader in this test
			LeaderElect:   true,
			LeaseDuration: 4 * time.Second,
			RenewDeadline: 3 * time.Second,
			RetryPeriod:   250 * time.Millisecond,
			MetricsAddr:   "0",
			ProbeAddr:     "0",
			Log:           r.logs.logger(),
		})
	}()
	t.Cleanup(func() { r.stop(t) })
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
			t.Errorf("Run returned %v after a cancel", err)
		}
	case <-time.After(30 * time.Second):
		t.Error("Run did not return within 30 s of a cancel")
	}
}

func TestKeeperWithItsRolesOnARealAPIServer(t *testing.T) {
	admin := envConfig(t)
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(admin, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	objs := []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: keeperNS}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ghSecret.Namespace}},
		emptySecret(),
	}
	for _, o := range append(objs, keeperRBAC()...) {
		if err := c.Create(ctx, o); err != nil {
			t.Fatalf("create %T %s: %v", o, o.GetName(), err)
		}
	}

	// The keeper's own identity: its ServiceAccount, impersonated.
	cfg := rest.CopyConfig(admin)
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:" + keeperNS + ":" + keeperSA,
		Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:" + keeperNS, "system:authenticated"},
	}

	k := keys(t)
	gh := newFakeGitHub(t, &k[0].PublicKey, time.Now)
	dir := writeAppDir(t, k[0])

	token := func() string {
		s := &corev1.Secret{}
		if err := c.Get(ctx, ghSecret, s); err != nil {
			t.Fatal(err)
		}
		return string(s.Data[GHTokenKey])
	}
	holder := func() string {
		l := &coordinationv1.Lease{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: keeperNS, Name: LeaderElectionID}, l); err != nil || l.Spec.HolderIdentity == nil {
			return ""
		}
		return *l.Spec.HolderIdentity
	}

	a := startKeeper(t, cfg, dir, gh)
	onServer(t, "the first keeper to write the token", func() bool { return token() != "" })
	_, _, tokens, errs, _ := gh.snapshot()
	if len(tokens) != 1 || token() != tokens[0]+"\n" || len(errs) != 0 {
		t.Fatalf("mints %d, GitHub refusals %v", len(tokens), errs)
	}
	first := holder()
	if first == "" {
		t.Fatal("the Lease has no holder")
	}
	s := &corev1.Secret{}
	if err := c.Get(ctx, ghSecret, s); err != nil {
		t.Fatal(err)
	}
	if s.Annotations[AnnotationExpiresAt] == "" || s.Annotations["kustomize.toolkit.fluxcd.io/ssa"] != "IfNotPresent" {
		t.Errorf("annotations %v", s.Annotations)
	}

	// A second keeper waits on the Lease and mints nothing.
	b := startKeeper(t, cfg, dir, gh)
	time.Sleep(2 * time.Second) // eight of its retry periods
	if mints, _, _, _, _ := gh.snapshot(); mints != 1 {
		t.Fatalf("%d mints with two keepers running, want 1: the Lease did not hold the second", mints)
	}

	// The first stops: it releases the Lease and the second takes over at once.
	a.stop(t)
	onServer(t, "the second keeper to take the Lease and mint", func() bool {
		mints, _, _, _, _ := gh.snapshot()
		return mints == 2
	})
	_, _, tokens, _, jwt := gh.snapshot()
	onServer(t, "the second keeper's token in the Secret", func() bool { return token() == tokens[1]+"\n" })
	if second := holder(); second == "" || second == first {
		t.Errorf("Lease holder %q after the handover, first was %q", second, first)
	}
	b.stop(t)

	for _, r := range []*running{a, b} {
		assertNoMaterial(t, r.logs.String(), pkcs1PEM(k[0]), append(tokens, jwt)...)
	}
}
