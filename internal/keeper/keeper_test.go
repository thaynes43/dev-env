package keeper

// The loop against a fake GitHub and the controller-runtime fake client, on a
// fake clock: nothing here waits 40 minutes, and no wait depends on load.

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	fakeclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var ghSecret = types.NamespacedName{Namespace: "dev-agents", Name: DefaultGHTokenSecret}

// emptySecret is the Secret as GitOps creates it: no data, plus a label and an
// annotation of its own that the keeper must leave alone.
func emptySecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   ghSecret.Namespace,
			Name:        ghSecret.Name,
			Labels:      map[string]string{"app.kubernetes.io/name": "dev-env-keeper"},
			Annotations: map[string]string{"kustomize.toolkit.fluxcd.io/ssa": "IfNotPresent"},
		},
		Type: corev1.SecretTypeOpaque,
	}
}

// harness is one keeper on a fake clock.
type harness struct {
	t      *testing.T
	clock  *fakeclock.FakeClock
	gh     *fakeGitHub
	c      client.Client
	k      *Keeper
	logs   *logBuffer
	keyPEM []byte
	cancel context.CancelFunc
	done   chan struct{}
}

func newHarness(t *testing.T, objs []client.Object, funcs *interceptor.Funcs) *harness {
	t.Helper()
	k := keys(t)
	h := &harness{t: t, clock: fakeclock.NewFakeClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)), logs: &logBuffer{}, keyPEM: pkcs1PEM(k[0])}
	h.gh = newFakeGitHub(t, &k[0].PublicKey, h.clock.Now)
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	h.c = b.Build()
	app := h.gh.app(writeAppDir(t, k[0]))
	app.Clock = h.clock
	h.k = &Keeper{
		Jobs:   []Job{GitHubTokenJob("gh-token", app, ghSecret)},
		Writer: &SecretWriter{Client: h.c},
		Log:    h.logs.logger(),
		Clock:  h.clock,
		Rand:   func() float64 { return 0.5 }, // the unvaried wait
	}
	return h
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan struct{})
	go func() {
		defer close(h.done)
		if err := h.k.Start(ctx); err != nil {
			h.t.Errorf("Start: %v", err)
		}
	}()
	h.t.Cleanup(h.stop)
}

func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	<-h.done
	h.cancel = nil
}

// nextWait waits until the loop sleeps, and returns how long it asked for.
func (h *harness) nextWait() time.Duration {
	h.t.Helper()
	eventually(h.t, "the keeper to sleep", h.clock.HasWaiters)
	h.k.mu.Lock()
	defer h.k.mu.Unlock()
	return h.k.state["gh-token"].next.Sub(h.clock.Now())
}

// step moves the clock by d and waits until the loop has woken up.
func (h *harness) step(d time.Duration) {
	h.clock.Step(d)
	eventually(h.t, "the keeper to wake", func() bool { return !h.clock.HasWaiters() })
}

func (h *harness) secret() *corev1.Secret {
	h.t.Helper()
	s := &corev1.Secret{}
	if err := h.c.Get(context.Background(), ghSecret, s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

func TestKeeperMintsThenRotatesEvery40Minutes(t *testing.T) {
	h := newHarness(t, []client.Object{emptySecret()}, nil)
	if err := h.k.ReadyCheck(nil); err == nil {
		t.Error("ready before the first mint")
	}
	h.start()

	if w := h.nextWait(); w != 40*time.Minute {
		t.Errorf("first wait %s, want 40m", w)
	}
	_, _, tokens, errs, _ := h.gh.snapshot()
	if len(tokens) != 1 || len(errs) != 0 {
		t.Fatalf("tokens %d, GitHub refusals %v", len(tokens), errs)
	}
	s := h.secret()
	if got := string(s.Data[GHTokenKey]); got != tokens[0]+"\n" {
		t.Errorf("gh_token is not the minted token and a newline")
	}
	if got, want := s.Annotations[AnnotationExpiresAt], "2026-10-06T13:00:00Z"; got != want {
		t.Errorf("expires-at %q, want %q", got, want)
	}
	if got, want := s.Annotations[AnnotationWrittenAt], "2026-10-06T12:00:00Z"; got != want {
		t.Errorf("written-at %q, want %q", got, want)
	}
	if s.Labels["app.kubernetes.io/name"] != "dev-env-keeper" || s.Annotations["kustomize.toolkit.fluxcd.io/ssa"] != "IfNotPresent" {
		t.Errorf("GitOps' own metadata was lost: %v %v", s.Labels, s.Annotations)
	}
	if err := h.k.ReadyCheck(nil); err != nil {
		t.Errorf("not ready after a mint: %v", err)
	}

	// 40 minutes on, a new token replaces the first, 20 minutes before it ends.
	h.step(40 * time.Minute)
	if w := h.nextWait(); w != 40*time.Minute {
		t.Errorf("second wait %s, want 40m", w)
	}
	_, _, tokens, _, _ = h.gh.snapshot()
	if len(tokens) != 2 {
		t.Fatalf("%d mints after 40 minutes, want 2", len(tokens))
	}
	s = h.secret()
	if string(s.Data[GHTokenKey]) != tokens[1]+"\n" {
		t.Error("the Secret still holds the first token")
	}
	if got, want := s.Annotations[AnnotationExpiresAt], "2026-10-06T13:40:00Z"; got != want {
		t.Errorf("expires-at %q, want %q", got, want)
	}

	h.stop()
	_, _, tokens, _, jwt := h.gh.snapshot()
	assertNoMaterial(t, h.logs.String(), h.keyPEM, append(tokens, jwt)...)
	if !strings.Contains(h.logs.String(), `"msg":"credential written"`) {
		t.Errorf("no success line in the logs:\n%s", h.logs.String())
	}
}

func TestKeeperRefreshesAShortLivedTokenAfterTwoThirds(t *testing.T) {
	h := newHarness(t, []client.Object{emptySecret()}, nil)
	h.gh.life = 30 * time.Minute
	h.start()
	if w := h.nextWait(); w != 20*time.Minute {
		t.Errorf("wait %s for a 30-minute token, want 20m", w)
	}
}

func TestKeeperBacksOffAndRecovers(t *testing.T) {
	h := newHarness(t, []client.Object{emptySecret()}, nil)
	h.gh.failNext(
		failure{http.StatusInternalServerError, `{"message":"Server Error","token":"` + leakSentinel + `"}`},
		failure{http.StatusBadGateway, "<html>" + leakSentinel + "</html>"},
		failure{http.StatusUnprocessableEntity, `{"message":"The permissions requested are not granted to this installation."}`},
	)
	h.start()

	for i, want := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second} {
		if w := h.nextWait(); w != want {
			t.Errorf("wait after failure %d: %s, want %s", i+1, w, want)
		}
		if err := h.k.ReadyCheck(nil); err == nil {
			t.Error("ready with no token written")
		}
		h.step(want)
	}
	if w := h.nextWait(); w != 40*time.Minute {
		t.Errorf("wait after the recovery: %s, want 40m", w)
	}
	if err := h.k.ReadyCheck(nil); err != nil {
		t.Errorf("not ready after the recovery: %v", err)
	}
	h.k.mu.Lock()
	failures := h.k.state["gh-token"].failures
	h.k.mu.Unlock()
	if failures != 0 {
		t.Errorf("failures %d after a success, want 0", failures)
	}

	h.stop()
	logs := h.logs.String()
	for _, want := range []string{"HTTP 500: Server Error", "HTTP 502", "not granted to this installation", `"attempt":3`, `"retryIn":"40s"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the logs lack %q:\n%s", want, logs)
		}
	}
	_, _, tokens, _, jwt := h.gh.snapshot()
	assertNoMaterial(t, logs, h.keyPEM, append(tokens, jwt)...)
}

func TestKeeperGoesUnreadyWhenItsTokenExpires(t *testing.T) {
	h := newHarness(t, []client.Object{emptySecret()}, nil)
	h.start()
	h.nextWait()
	// From now on every mint fails.
	for range 40 {
		h.gh.failNext(failure{http.StatusServiceUnavailable, `{"message":"Service Unavailable"}`})
	}
	h.step(40 * time.Minute)
	for h.clock.Now().Before(time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)) {
		if err := h.k.ReadyCheck(nil); err != nil {
			t.Fatalf("unready at %s with a live token: %v", h.clock.Now(), err)
		}
		h.step(h.nextWait())
	}
	err := h.k.ReadyCheck(nil)
	if err == nil || !strings.Contains(err.Error(), "expired at 2026-10-06T13:00:00Z") {
		t.Errorf("readiness after expiry: %v", err)
	}
}

func TestKeeperNeverCreatesTheSecret(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.start()
	if w := h.nextWait(); w != 10*time.Second {
		t.Errorf("wait %s, want the first backoff", w)
	}
	h.stop()
	err := h.c.Get(context.Background(), ghSecret, &corev1.Secret{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("the keeper made the Secret (or Get failed): %v", err)
	}
	if !strings.Contains(h.logs.String(), "GitOps creates it empty") {
		t.Errorf("the log does not say who creates the Secret:\n%s", h.logs.String())
	}
	_, _, tokens, _, jwt := h.gh.snapshot()
	assertNoMaterial(t, h.logs.String(), h.keyPEM, append(tokens, jwt)...)
}

// An API server that quoted the patch back in its error must not put the token
// in the log.
func TestKeeperScrubsAWriteErrorThatQuotesTheToken(t *testing.T) {
	funcs := &interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, patch client.Patch, _ ...client.PatchOption) error {
			body, err := patch.Data(nil)
			if err != nil {
				return err
			}
			return apierrors.NewBadRequest("cannot apply " + string(body))
		},
	}
	h := newHarness(t, []client.Object{emptySecret()}, funcs)
	h.start()
	h.nextWait()
	h.stop()
	logs := h.logs.String()
	if !strings.Contains(logs, "cannot apply") || !strings.Contains(logs, redacted) {
		t.Errorf("the write error is not logged, or not scrubbed:\n%s", logs)
	}
	// The patch carries the token base64-encoded, as Secret data is; check that
	// form too.
	_, _, tokens, _, jwt := h.gh.snapshot()
	if len(tokens) == 0 {
		t.Fatal("nothing was minted")
	}
	material := []string{jwt}
	for _, tok := range tokens {
		material = append(material, tok, base64.StdEncoding.EncodeToString([]byte(tok+"\n")), base64.StdEncoding.EncodeToString([]byte(tok)))
	}
	assertNoMaterial(t, logs, h.keyPEM, material...)
}

func TestSecretWriterErrors(t *testing.T) {
	forbidden := &interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			return apierrors.NewForbidden(corev1.Resource("secrets"), ghSecret.Name, errors.New("RBAC"))
		},
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	w := &SecretWriter{Client: fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(*forbidden).Build()}
	err := w.Write(context.Background(), ghSecret, GHTokenKey, []byte("x"), time.Now(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "needs patch on it by name") || !apierrors.IsForbidden(errors.Unwrap(err)) {
		t.Errorf("forbidden: %v", err)
	}
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 300 * time.Second, 300 * time.Second}
	for i, w := range want {
		n := i + 1
		if got := backoff(n, 0.5); got != w {
			t.Errorf("backoff(%d) = %s, want %s", n, got, w)
		}
		if lo, hi := backoff(n, 0), backoff(n, 0.999999); lo != w*8/10 || hi <= w*119/100 || hi > w*12/10 {
			t.Errorf("backoff(%d) varies %s..%s, want 0.8x..1.2x of %s", n, lo, hi, w)
		}
	}
	if got := backoff(1000, 0.5); got != backoffCap {
		t.Errorf("backoff(1000) = %s", got)
	}
}

func TestRefreshAfter(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct{ life, interval, want time.Duration }{
		{time.Hour, 40 * time.Minute, 40 * time.Minute},
		{time.Hour, 50 * time.Minute, 40 * time.Minute}, // never past two thirds
		{30 * time.Minute, 40 * time.Minute, 20 * time.Minute},
		{10 * time.Second, 40 * time.Minute, minWait},
		{-time.Minute, 40 * time.Minute, minWait},
	} {
		if got := refreshAfter(now, now.Add(c.life), c.interval); got != c.want {
			t.Errorf("life %s, interval %s: %s, want %s", c.life, c.interval, got, c.want)
		}
	}
}
