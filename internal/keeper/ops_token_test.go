package keeper

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
	t      *testing.T
	clock  *armClock
	dev    *fakeGitHub
	ops    *fakeGitHub
	c      client.Client
	k      *Keeper
	logs   *logBuffer
	key    []byte
	devDir string
	devApp *GitHubApp
	opsApp *GitHubApp
}

func newOpsLoop(t *testing.T, opsSecret bool, opsFail ...failure) *opsLoop {
	t.Helper()
	pair := keys(t)
	clk := &armClock{FakeClock: fakeclock.NewFakeClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))}
	const opsClient = "Iv23opsclientid"
	const opsInstallation = "4343"
	devGH := newFakeGitHub(t, &pair[0].PublicKey, clk.Now)
	opsGH := newFakeGitHub(t, &pair[1].PublicKey, clk.Now)
	opsGH.issuer = opsClient
	opsGH.installationID = opsInstallation
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
	devDir := writeAppDir(t, pair[0])
	opsDir := writeAppDir(t, pair[1])
	writeFile(t, filepath.Join(opsDir, AppFileClientID), opsClient+"\n")
	writeFile(t, filepath.Join(opsDir, AppFileInstallationID), opsInstallation+"\n")
	devApp := devGH.app(devDir)
	devApp.Clock = clk
	opsApp := opsGH.app(opsDir)
	opsApp.Clock = clk
	opsApp.Permissions = DefaultOpsBotPermissions
	opsApp.Repositories = []string{"haynes-ops", "cigar-journal"}
	logs := &logBuffer{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &opsLoop{
		t: t, clock: clk, dev: devGH, ops: opsGH, logs: logs,
		key: append(pkcs1PEM(pair[0]), pkcs1PEM(pair[1])...), c: c,
		devDir: devDir, devApp: devApp, opsApp: opsApp,
		k: &Keeper{
			Jobs: keeperJobs(Options{
				GitHubApp:        devApp,
				GHTokenSecret:    ghSecret,
				OpsGitHubApp:     opsApp,
				OpsGHTokenSecret: types.NamespacedName{Namespace: ghSecret.Namespace, Name: DefaultOpsGHTokenSecret},
			}),
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

func TestOpsSymlinkToTheDevAppBacksOffWithoutMinting(t *testing.T) {
	pair := keys(t)
	clk := &armClock{FakeClock: fakeclock.NewFakeClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))}
	devGH := newFakeGitHub(t, &pair[0].PublicKey, clk.Now)
	opsGH := newFakeGitHub(t, &pair[1].PublicKey, clk.Now)
	opsGH.issuer = "Iv23opsclientid"
	devDir := writeAppDir(t, pair[0])
	link := filepath.Join(t.TempDir(), "ops-app")
	if err := os.Symlink(devDir, link); err != nil {
		t.Fatal(err)
	}
	devApp := devGH.app(devDir)
	devApp.Clock = clk
	opsApp := opsGH.app(link)
	opsApp.Clock = clk
	opsApp.Permissions = DefaultOpsBotPermissions
	opsApp.Repositories = []string{"haynes-ops"}
	opsApp.DistinctFrom = devApp
	opsSecret := types.NamespacedName{Namespace: ghSecret.Namespace, Name: DefaultOpsGHTokenSecret}
	if err := validateOps(Options{
		GitHubApp:        devApp,
		GHTokenSecret:    ghSecret,
		OpsGitHubApp:     opsApp,
		OpsGHTokenSecret: opsSecret,
	}); err != nil {
		t.Fatalf("a symlink is a different path; validateOps refused it before the mint: %v", err)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(emptySecret(), secretNamed(DefaultOpsGHTokenSecret)).Build()
	logs := &logBuffer{}
	k := &Keeper{
		Jobs: []Job{
			GitHubTokenJob("gh-token", devApp, ghSecret),
			GitHubTokenJob("ops-gh-token", opsApp, opsSecret),
		},
		Writer: &SecretWriter{Client: c},
		Log:    logs.logger(),
		Clock:  clk,
		Rand:   func() float64 { return 0.5 },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := k.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	eventually(t, "both token loops to sleep", func() bool {
		if clk.arms() < 2 {
			return false
		}
		k.mu.Lock()
		defer k.mu.Unlock()
		for _, name := range []string{"gh-token", "ops-gh-token"} {
			st := k.state[name]
			if st == nil || st.attempts < 1 || st.next.IsZero() {
				return false
			}
		}
		return true
	})
	k.mu.Lock()
	opsWait := k.state["ops-gh-token"].next.Sub(clk.Now())
	devWait := k.state["gh-token"].next.Sub(clk.Now())
	k.mu.Unlock()
	if opsWait != 10*time.Second {
		t.Errorf("ops wait %s, want the first backoff", opsWait)
	}
	if devWait != 40*time.Minute {
		t.Errorf("dev wait %s, want a successful mint", devWait)
	}
	if _, reqs, _, _, _ := opsGH.snapshot(); reqs != 0 {
		t.Fatalf("the symlink reached GitHub: %d requests", reqs)
	}
	if _, reqs, _, _, _ := devGH.snapshot(); reqs != 1 {
		t.Fatalf("dev requests %d, want 1", reqs)
	}
	if err := k.ReadyCheck(nil); err == nil || !strings.Contains(err.Error(), "ops-gh-token") {
		t.Fatalf("readiness with a refused ops mint: %v", err)
	}
	if strings.Contains(logs.String(), testClientID) {
		t.Errorf("the log quotes the dev bot's client id:\n%s", logs.String())
	}
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

func TestNewOpsLoopUsesDistinctAppsAndKeeperJobs(t *testing.T) {
	o := newOpsLoop(t, true)
	if o.opsApp == nil || o.devApp == nil || o.opsApp.DistinctFrom != o.devApp {
		t.Fatal("keeperJobs did not compare the ops App with the dev bot's App")
	}
	dev, err := o.devApp.load()
	if err != nil {
		t.Fatal(err)
	}
	ops, err := o.opsApp.load()
	if err != nil {
		t.Fatal(err)
	}
	if dev.issuer == ops.issuer {
		t.Fatal("the two Apps share an issuer")
	}
	if dev.installationID == ops.installationID {
		t.Fatal("the two Apps share an installation")
	}
	if dev.key.N.Cmp(ops.key.N) == 0 {
		t.Fatal("the two Apps share a signing key")
	}
}

func TestOpsMintBacksOffWhenTheDevDirectoryBecomesUnreadable(t *testing.T) {
	o := newOpsLoop(t, true)
	// A 3-minute ops token is due again in 2 minutes. The dev token stays on
	// its 40-minute success wait, so this step wakes only the ops loop.
	o.ops.life = 3 * time.Minute
	o.start()
	o.settled(map[string]int{"gh-token": 1, "ops-gh-token": 1}, 2)
	if _, devReqs, _, _, _ := o.dev.snapshot(); devReqs != 1 {
		t.Fatalf("dev requests before the directory change: %d", devReqs)
	}
	if _, opsReqs, _, _, _ := o.ops.snapshot(); opsReqs != 1 {
		t.Fatalf("ops requests before the directory change: %d", opsReqs)
	}
	devSecret := o.secret(ghSecret.Name)
	if err := os.Chmod(o.devDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(o.devDir, 0o700) })
	beforeArms := o.clock.arms()
	o.clock.Step(2 * time.Minute)
	o.settled(map[string]int{"ops-gh-token": 2, "gh-token": 1}, beforeArms+1)
	if _, opsReqs, _, _, _ := o.ops.snapshot(); opsReqs != 1 {
		t.Fatalf("the ops mint contacted GitHub after the dev directory became unreadable: %d requests", opsReqs)
	}
	if w := o.waitOf("ops-gh-token"); w != 10*time.Second {
		t.Errorf("ops wait %s, want the first backoff", w)
	}
	if _, devReqs, _, _, _ := o.dev.snapshot(); devReqs != 1 {
		t.Fatalf("dev requests %d, want the first mint only", devReqs)
	}
	if w := o.waitOf("gh-token"); w != 38*time.Minute {
		t.Errorf("dev wait %s, want 38m still on the successful mint", w)
	}
	devAfter := o.secret(ghSecret.Name)
	if string(devAfter.Data[GHTokenKey]) != string(devSecret.Data[GHTokenKey]) || devAfter.Annotations[AnnotationExpiresAt] != devSecret.Annotations[AnnotationExpiresAt] {
		t.Error("the dev token changed while its directory was unreadable")
	}
}

// TestOpsMintRefusesNumericAppIDInClientIDFile is the mislabeled-file case:
// one App's client id in the dev directory's client-id, and that App's numeric
// id in the ops directory's client-id, with two keys and two installations.
// The value is an app id wherever the file sits, so the two forms cannot be
// compared. The ops mint is refused before any request and backs off; the dev
// bot's mint still succeeds.
func TestOpsMintRefusesNumericAppIDInClientIDFile(t *testing.T) {
	pair := keys(t)
	clk := &armClock{FakeClock: fakeclock.NewFakeClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))}
	devGH := newFakeGitHub(t, &pair[0].PublicKey, clk.Now)
	opsGH := newFakeGitHub(t, &pair[1].PublicKey, clk.Now)
	const numericAppID = "4291021"
	const opsInstallation = "4343"
	// A mint that treated the ops client-id file as a client id would send
	// this string. The fake accepts it, so the test fails when the mint runs.
	opsGH.issuer = numericAppID
	opsGH.installationID = opsInstallation
	devDir := writeAppDir(t, pair[0])
	opsDir := writeAppDir(t, pair[1])
	writeFile(t, filepath.Join(opsDir, AppFileClientID), numericAppID+"\n")
	writeFile(t, filepath.Join(opsDir, AppFileInstallationID), opsInstallation+"\n")
	devApp := devGH.app(devDir)
	devApp.Clock = clk
	opsApp := opsGH.app(opsDir)
	opsApp.Clock = clk
	opsApp.Permissions = DefaultOpsBotPermissions
	opsApp.Repositories = []string{"haynes-ops"}
	opsSecret := types.NamespacedName{Namespace: ghSecret.Namespace, Name: DefaultOpsGHTokenSecret}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(emptySecret(), secretNamed(DefaultOpsGHTokenSecret)).Build()
	logs := &logBuffer{}
	k := &Keeper{
		Jobs: keeperJobs(Options{
			GitHubApp:        devApp,
			GHTokenSecret:    ghSecret,
			OpsGitHubApp:     opsApp,
			OpsGHTokenSecret: opsSecret,
		}),
		Writer: &SecretWriter{Client: c},
		Log:    logs.logger(),
		Clock:  clk,
		Rand:   func() float64 { return 0.5 },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := k.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	eventually(t, "both token loops to sleep", func() bool {
		if clk.arms() < 2 {
			return false
		}
		k.mu.Lock()
		defer k.mu.Unlock()
		for _, name := range []string{"gh-token", "ops-gh-token"} {
			st := k.state[name]
			if st == nil || st.attempts < 1 || st.next.IsZero() {
				return false
			}
		}
		return true
	})
	k.mu.Lock()
	opsWait := k.state["ops-gh-token"].next.Sub(clk.Now())
	devWait := k.state["gh-token"].next.Sub(clk.Now())
	k.mu.Unlock()
	if opsWait != 10*time.Second {
		t.Errorf("ops wait %s, want the first backoff", opsWait)
	}
	if devWait != 40*time.Minute {
		t.Errorf("dev wait %s, want a successful mint", devWait)
	}
	if _, reqs, _, _, _ := opsGH.snapshot(); reqs != 0 {
		t.Fatalf("a numeric app id in client-id reached GitHub: %d requests", reqs)
	}
	if _, reqs, _, _, _ := devGH.snapshot(); reqs != 1 {
		t.Fatalf("dev requests %d, want 1", reqs)
	}
	if err := k.ReadyCheck(nil); err == nil || !strings.Contains(err.Error(), "ops-gh-token") {
		t.Fatalf("readiness with a refused ops mint: %v", err)
	}
	text := logs.String()
	if !strings.Contains(text, "cannot be compared") {
		t.Fatalf("the refusal is not the different-forms check:\n%s", text)
	}
	for _, id := range []string{numericAppID, testClientID, testInstallationID, opsInstallation} {
		if logQuotesID(text, id) {
			t.Errorf("the log quotes an id %q", id)
		}
	}
	dev := &corev1.Secret{}
	if err := c.Get(context.Background(), ghSecret, dev); err != nil {
		t.Fatal(err)
	}
	_, _, devTokens, _, _ := devGH.snapshot()
	if len(devTokens) != 1 || string(dev.Data[GHTokenKey]) != devTokens[0]+"\n" {
		t.Error("the dev bot's mint did not succeed")
	}
	opsWritten := &corev1.Secret{}
	if err := c.Get(context.Background(), opsSecret, opsWritten); err != nil {
		t.Fatal(err)
	}
	if len(opsWritten.Data[GHTokenKey]) != 0 {
		t.Error("the refused ops mint wrote a token")
	}
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

// logQuotesID reports whether a JSON log quotes id as a value (story 784).
// Each line's own "time" member is dropped first: a nanosecond timestamp can
// hold any run of digits. The id must then stand alone, not inside a longer
// run of digits. A line that is not JSON is checked whole.
func logQuotesID(logs, id string) bool {
	alone := regexp.MustCompile(`(^|[^0-9])` + regexp.QuoteMeta(id) + `($|[^0-9])`)
	for _, line := range strings.Split(logs, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil {
			delete(entry, "time")
			raw, err := json.Marshal(entry)
			if err != nil {
				return true
			}
			line = string(raw)
		}
		if alone.MatchString(line) {
			return true
		}
	}
	return false
}

// Story 784: the check finds an id quoted as a value, and never matches a
// timestamp that holds its digits.
func TestLogQuotesIDIgnoresTimestamps(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{`{"time":"2026-11-12T23:46:46.4242Z","level":"INFO","msg":"minted"}`, false},
		{`{"time":"2026-11-12T23:46:46.424212345Z","level":"INFO","msg":"minted"}`, false},
		{`{"time":"2026-11-12T23:42:42.000004242Z","level":"INFO","msg":"minted","n":42420}`, false},
		{`{"time":"2026-11-12T23:46:46.1Z","msg":"minted","installation":"4242"}`, true},
		{`{"time":"2026-11-12T23:46:46.1Z","msg":"mint refused for installation 4242"}`, true},
		{`{"time":"2026-11-12T23:46:46.1Z","msg":"POST /app/installations/4242/access_tokens"}`, true},
		{`{"time":"2026-11-12T23:46:46.1Z","msg":"minted","n":4242}`, true},
		{`{"time":"2026-11-12T23:46:46.1Z","msg":"installation4242"}`, true},
		{"not json 4242", true},
	} {
		if got := logQuotesID(tc.line+"\n", testInstallationID); got != tc.want {
			t.Errorf("logQuotesID(%s) = %v, want %v", tc.line, got, tc.want)
		}
	}
}
