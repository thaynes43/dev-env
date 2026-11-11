package keeper

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/clock"
	fakeclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// armClock counts After calls. The waiter is registered before the count
// moves, so a test can step only once the sleep is armed. Two Jobs share one
// clock, and HasWaiters stays true while either of them sleeps.
type armClock struct {
	*fakeclock.FakeClock
	mu    sync.Mutex
	armed int
}

func (c *armClock) After(d time.Duration) <-chan time.Time {
	ch := c.FakeClock.After(d)
	c.mu.Lock()
	c.armed++
	c.mu.Unlock()
	return ch
}

func (c *armClock) arms() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.armed
}

func secretNamed(name string) *corev1.Secret {
	s := emptySecret()
	s.Name = name
	return s
}

type opsLoop struct {
	t     *testing.T
	clock *armClock
	dev   *fakeGitHub
	ops   *fakeGitHub
	c     client.Client
	k     *Keeper
	logs  *logBuffer
	key   []byte
}

func newOpsLoop(t *testing.T, opsSecret bool, opsFail ...failure) *opsLoop {
	t.Helper()
	pair := keys(t)
	clk := &armClock{FakeClock: fakeclock.NewFakeClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))}
	devGH := newFakeGitHub(t, &pair[0].PublicKey, clk.Now)
	opsGH := newFakeGitHub(t, &pair[0].PublicKey, clk.Now)
	opsGH.repoEcho = "not-for-logs"
	if len(opsFail) > 0 {
		opsGH.failNext(opsFail...)
	}
	objs := []client.Object{emptySecret()}
	if opsSecret {
		objs = append(objs, secretNamed(DefaultOpsGHTokenSecret))
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	devApp := devGH.app(writeAppDir(t, pair[0]))
	devApp.Clock = clk
	opsApp := opsGH.app(writeAppDir(t, pair[0]))
	opsApp.Clock = clk
	opsApp.Permissions = DefaultOpsBotPermissions
	opsApp.Repositories = []string{"haynes-ops", "cigar-journal"}
	logs := &logBuffer{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &opsLoop{
		t: t, clock: clk, dev: devGH, ops: opsGH, logs: logs, key: pkcs1PEM(pair[0]), c: c,
		k: &Keeper{
			Jobs: []Job{
				GitHubTokenJob("gh-token", devApp, ghSecret),
				GitHubTokenJob("ops-gh-token", opsApp, types.NamespacedName{Namespace: ghSecret.Namespace, Name: DefaultOpsGHTokenSecret}),
			},
			Writer: &SecretWriter{Client: c},
			Log:    logs.logger(),
			Clock:  clk,
			Rand:   func() float64 { return 0.5 },
		},
	}
}

func (o *opsLoop) start() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := o.k.Start(ctx); err != nil {
			o.t.Errorf("Start: %v", err)
		}
	}()
	o.t.Cleanup(func() {
		cancel()
		<-done
	})
}

func (o *opsLoop) settled(attempts map[string]int, arms int) {
	o.t.Helper()
	eventually(o.t, "both token loops to sleep", func() bool {
		if o.clock.arms() < arms {
			return false
		}
		o.k.mu.Lock()
		defer o.k.mu.Unlock()
		for name, n := range attempts {
			st := o.k.state[name]
			if st == nil || st.attempts < n || st.next.IsZero() {
				return false
			}
		}
		return true
	})
}

func (o *opsLoop) waitOf(name string) time.Duration {
	o.k.mu.Lock()
	defer o.k.mu.Unlock()
	return o.k.state[name].next.Sub(o.clock.Now())
}

func (o *opsLoop) step(name string, d time.Duration, attempts, arms int) {
	o.t.Helper()
	o.clock.Step(d)
	o.settled(map[string]int{name: attempts, "gh-token": 1}, arms)
}

func TestOpsTokenMintsBesideTheDevTokenAndARefusalBacksOff(t *testing.T) {
	o := newOpsLoop(t, true, failure{
		http.StatusUnprocessableEntity,
		`{"message":"The permissions requested are not granted to this installation.","token":"` + leakSentinel + `"}`,
	})
	if err := o.k.ReadyCheck(nil); err == nil {
		t.Fatal("ready before either mint")
	}
	o.start()
	o.settled(map[string]int{"gh-token": 1, "ops-gh-token": 1}, 2)

	if w := o.waitOf("gh-token"); w != 40*time.Minute {
		t.Errorf("dev wait %s, want 40m", w)
	}
	if w := o.waitOf("ops-gh-token"); w != 10*time.Second {
		t.Errorf("ops wait after a refusal %s, want 10s", w)
	}
	if _, reqs, _, _, _ := o.ops.snapshot(); reqs != 1 {
		t.Fatalf("ops requests while waiting: %d, want 1", reqs)
	}
	err := o.k.ReadyCheck(nil)
	if err == nil || !strings.Contains(err.Error(), "ops-gh-token") {
		t.Fatalf("readiness with a refused ops mint: %v", err)
	}
	devBefore := o.secret(ghSecret.Name)
	if !reflect.DeepEqual(o.dev.lastBody["permissions"], DefaultDevBotPermissions) || strings.Contains(string(o.dev.lastRaw), "repositories") {
		t.Errorf("dev request changed: %s", o.dev.lastRaw)
	}
	if !reflect.DeepEqual(o.ops.lastBody["permissions"], DefaultOpsBotPermissions) || !reflect.DeepEqual(o.ops.lastRepositories, []string{"haynes-ops", "cigar-journal"}) {
		t.Errorf("ops request permissions %v repositories %v", o.ops.lastBody["permissions"], o.ops.lastRepositories)
	}

	o.step("ops-gh-token", 10*time.Second, 2, 3)
	if w := o.waitOf("ops-gh-token"); w != 40*time.Minute {
		t.Errorf("ops wait after the mint %s, want 40m", w)
	}
	if w := o.waitOf("gh-token"); w != 40*time.Minute-10*time.Second {
		t.Errorf("dev wait moved: %s", w)
	}
	if err := o.k.ReadyCheck(nil); err != nil {
		t.Fatalf("not ready once both tokens are written: %v", err)
	}
	if _, devReqs, _, _, _ := o.dev.snapshot(); devReqs != 1 {
		t.Errorf("dev bot was minted %d times during the ops retry", devReqs)
	}
	if _, opsReqs, _, _, _ := o.ops.snapshot(); opsReqs != 2 {
		t.Errorf("ops requests %d, want the refusal and one mint", opsReqs)
	}

	devAfter := o.secret(ghSecret.Name)
	if string(devAfter.Data[GHTokenKey]) != string(devBefore.Data[GHTokenKey]) || devAfter.Annotations[AnnotationExpiresAt] != devBefore.Annotations[AnnotationExpiresAt] {
		t.Error("the ops retry rewrote the dev token")
	}
	opsSecret := o.secret(DefaultOpsGHTokenSecret)
	_, _, opsTokens, _, _ := o.ops.snapshot()
	if len(opsTokens) != 1 || string(opsSecret.Data[GHTokenKey]) != opsTokens[0]+"\n" {
		t.Error("ops gh_token is not the minted token and a newline")
	}
	if opsSecret.Annotations[AnnotationExpiresAt] != "2026-10-06T13:00:10Z" || opsSecret.Annotations[AnnotationWrittenAt] != "2026-10-06T12:00:10Z" {
		t.Errorf("ops annotations: %v", opsSecret.Annotations)
	}
	wantBody, err := json.Marshal(map[string]any{
		"permissions":  DefaultOpsBotPermissions,
		"repositories": []string{"haynes-ops", "cigar-journal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(o.ops.lastRaw) != string(wantBody) {
		t.Errorf("ops mint body %s, want %s", o.ops.lastRaw, wantBody)
	}

	logs := o.logs.String()
	for _, want := range []string{
		"not granted to this installation",
		`"credential":"ops-gh-token"`,
		`"retryIn":"10s"`,
		`"repositorySelection":"selected"`,
		"contents:write",
		"checks:read",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
	if strings.Count(logs, "not granted to this installation") != 1 {
		t.Errorf("the refusal was logged more than once:\n%s", logs)
	}
	if strings.Contains(logs, "not-for-logs") || strings.Contains(logs, "Bearer") || strings.Contains(logs, "Authorization") {
		t.Errorf("the log carries repository names or a request header:\n%s", logs)
	}
	_, _, devTokens, _, devJWT := o.dev.snapshot()
	_, _, opsTokens, _, opsJWT := o.ops.snapshot()
	assertNoMaterial(t, logs, o.key, append(append(devTokens, opsTokens...), devJWT, opsJWT)...)
}

func TestOpsTokenIsNotCreatedWhenTheSecretIsMissing(t *testing.T) {
	o := newOpsLoop(t, false)
	o.start()
	o.settled(map[string]int{"gh-token": 1, "ops-gh-token": 1}, 2)
	if w := o.waitOf("ops-gh-token"); w != 10*time.Second {
		t.Errorf("wait %s, want the first backoff", w)
	}
	if err := o.k.ReadyCheck(nil); err == nil {
		t.Fatal("ready without the ops token")
	}
	if _, reqs, _, _, _ := o.ops.snapshot(); reqs != 1 {
		t.Fatalf("retried while waiting: %d requests", reqs)
	}
	err := o.c.Get(context.Background(), types.NamespacedName{Namespace: ghSecret.Namespace, Name: DefaultOpsGHTokenSecret}, &corev1.Secret{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("the keeper made the ops Secret (or Get failed): %v", err)
	}
	dev := o.secret(ghSecret.Name)
	_, _, devTokens, _, _ := o.dev.snapshot()
	if len(devTokens) != 1 || string(dev.Data[GHTokenKey]) != devTokens[0]+"\n" {
		t.Error("the missing ops Secret stopped the dev mint")
	}
	if !strings.Contains(o.logs.String(), "GitOps creates it empty") {
		t.Errorf("the log does not say who creates the Secret:\n%s", o.logs.String())
	}
	_, _, opsTokens, _, opsJWT := o.ops.snapshot()
	_, _, devTokens, _, devJWT := o.dev.snapshot()
	assertNoMaterial(t, o.logs.String(), o.key, append(append(devTokens, opsTokens...), devJWT, opsJWT)...)
}

func (o *opsLoop) secret(name string) *corev1.Secret {
	o.t.Helper()
	s := &corev1.Secret{}
	if err := o.c.Get(context.Background(), types.NamespacedName{Namespace: ghSecret.Namespace, Name: name}, s); err != nil {
		o.t.Fatal(err)
	}
	return s
}

// clock.Clock is implemented by armClock through the embedded fake clock.
var _ clock.Clock = (*armClock)(nil)
