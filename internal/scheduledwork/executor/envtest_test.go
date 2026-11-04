package executor

// The envtest suite of DESIGN-054 S3b: the executor against a real
// kube-apiserver with the CRDs installed, creating through the API's real
// fixed-name entry (apiserver.Server.CreateScheduledSession). The clock, the
// Authorizer and the TemplateResolver are fakes, and the tests call Tick
// themselves, so each pass is one deterministic step. envtest runs no
// reconciler: the tests play the releaser (S3a) with the same
// scheduledwork.Record and scheduledwork.Outcome it uses, and reap a session
// by deleting it and lifting its finalizer once the record is stored, as the
// releaser does. Each test gets its own item and session namespaces, so the
// global caps count only its own items.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
	"github.com/thaynes43/dev-env/internal/templates"
	"github.com/thaynes43/dev-env/internal/testenv"
)

var (
	envOnce sync.Once
	env     *testenv.Env
	envErr  error
	k8s     client.WithWatch
	nsSeq   atomic.Int32
)

var testScheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := errors.Join(clientgoscheme.AddToScheme(s), v1alpha1.AddToScheme(s)); err != nil {
		panic(err)
	}
	return s
}()

func TestMain(m *testing.M) {
	code := m.Run()
	if env != nil {
		if err := env.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "envtest stop:", err)
		}
	}
	os.Exit(code)
}

// cluster starts the suite's API server on first use, so the unit tests need
// no binaries.
func cluster(t *testing.T) client.WithWatch {
	t.Helper()
	envOnce.Do(func() {
		env, envErr = testenv.Start()
		if envErr == nil {
			k8s, envErr = client.NewWithWatch(env.Config, client.Options{Scheme: testScheme})
		}
	})
	if envErr != nil {
		t.Fatalf("envtest: %v", envErr)
	}
	return k8s
}

// day0 is the first due time of most fixtures: far enough ahead that a real
// creation time (which the API server sets) is always before it.
var day0 = time.Date(2030, 6, 3, 14, 0, 0, 0, time.UTC)

// clock is the fake clock the executor and the releaser stand-in read.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *clock) Set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.now = t }

// authorizer is the fake Authorizer: it answers basis, or what fn says.
type authorizer struct {
	mu    sync.Mutex
	fn    func(*v1alpha1.ScheduledWork) (Authority, error)
	calls int
}

func (a *authorizer) Authorize(_ context.Context, w *v1alpha1.ScheduledWork, _ time.Time) (Authority, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.fn == nil {
		return Authority{Basis: BasisHuman}, nil
	}
	return a.fn(w)
}

func (a *authorizer) set(fn func(*v1alpha1.ScheduledWork) (Authority, error)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fn = fn
}

// resolver is the fake TemplateResolver over a fixed map.
type resolver map[string]Template

func (r resolver) Resolve(_ context.Context, name string) (Template, error) {
	t, ok := r[name]
	if !ok {
		return Template{}, &Refusal{Reason: "no such template"}
	}
	return t, nil
}

// opsAudit is the fixtures' headless template.
var opsAudit = Template{Agent: "claude", Model: "claude-opus-5-5", Effort: "xhigh", Profile: "dev",
	Repos: []string{"haynes-ops"}, MaxDuration: 40 * time.Minute, Admits: []Basis{BasisHuman, BasisApproved, BasisSession},
	Modes: []string{v1alpha1.ScheduledWorkHeadless, v1alpha1.ScheduledWorkInteractive}}

// creator wraps the API's real fixed-name entry, counting calls and the
// sessions it really created, with hooks a test sets to interleave another
// leader or lose a response.
type creator struct {
	srv *apiserver.Server
	mu  sync.Mutex
	// before runs before the real entry; an error it returns is the
	// entry's answer, and nothing is created. after replaces the answer.
	before                  func() error
	after                   func(name string, adopted bool, err error) (string, bool, error)
	calls, created, adopted int
}

func (c *creator) CreateScheduledSession(ctx context.Context, req apiv1.CreateSessionRequest, d scheduledwork.Dispatch) (string, types.UID, bool, error) {
	c.mu.Lock()
	before, after := c.before, c.after
	c.calls++
	c.mu.Unlock()
	if before != nil {
		if err := before(); err != nil {
			return "", "", false, err
		}
	}
	name, uid, adopted, err := c.srv.CreateScheduledSession(ctx, req, d)
	c.mu.Lock()
	switch {
	case err == nil && adopted:
		c.adopted++
	case err == nil:
		c.created++
	}
	c.mu.Unlock()
	if after != nil {
		name, adopted, err = after(name, adopted, err)
		if err != nil {
			uid = ""
		}
	}
	return name, uid, adopted, err
}

func (c *creator) hooks(before func() error, after func(string, bool, error) (string, bool, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.before, c.after = before, after
}

func (c *creator) counts() (calls, created, adopted int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.created, c.adopted
}

// fixture is one test's namespaces, clock, fakes and creation entry.
type fixture struct {
	t        *testing.T
	items    string
	sessions string
	clock    *clock
	auth     *authorizer
	tmpl     resolver
	create   *creator
	// failStatus fails the next n status writes of the executor's client.
	failStatus atomic.Int32
	// writes counts the status writes the executors' clients sent.
	writes atomic.Int32
	// beforeWrite, when set, runs once before the status write of an
	// executor's client numbered beforeWriteAt (counted in writes) reaches
	// the API server; beforeWriteAt zero is the next write.
	beforeWrite   atomic.Pointer[func()]
	beforeWriteAt atomic.Int32
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	c := cluster(t)
	n := nsSeq.Add(1)
	f := &fixture{t: t, items: fmt.Sprintf("sw-items-%d", n), sessions: fmt.Sprintf("sw-sessions-%d", n),
		clock: &clock{now: day0.Add(-time.Hour)}, auth: &authorizer{}, tmpl: resolver{"ops-audit": opsAudit}}
	for _, ns := range []string{f.items, f.sessions} {
		if err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := os.ReadFile("../../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := templates.Parse(map[string]string{templates.Key: string(doc)})
	if err != nil {
		t.Fatal(err)
	}
	f.create = &creator{srv: &apiserver.Server{
		Client: c,
		Live:   c,
		Policy: apiserver.Policy{SessionNamespace: f.sessions, SessionServiceAccount: controller.ServiceAccountName},
		Templates: func(context.Context) (*templates.Templates, error) {
			return tmpl, nil
		},
		Log: logr.Discard(),
		Now: f.clock.Now,
	}}
	return f
}

// executor is a fresh executor on the fixture: a new one is an operator
// restart or another leader. Its client writes status only; a read through it
// would be a cached read in the operator, so it fails the test.
func (f *fixture) executor() *Executor {
	f.t.Helper()
	writer := interceptor.NewClient(cluster(f.t), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			f.t.Error("the executor read through its writing client, which is the operator's cache")
			return errors.New("no cached reads")
		},
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			f.t.Error("the executor listed through its writing client, which is the operator's cache")
			return errors.New("no cached reads")
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			n := f.writes.Add(1)
			if at := f.beforeWriteAt.Load(); at == 0 || at == n {
				if hook := f.beforeWrite.Swap(nil); hook != nil {
					(*hook)()
				}
			}
			if f.failStatus.Load() > 0 {
				f.failStatus.Add(-1)
				return apierrors.NewServiceUnavailable("injected status write failure")
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	return &Executor{
		Client: writer, Reader: cluster(f.t),
		Namespace: f.items, SessionNamespace: f.sessions,
		Creator: f.create, Authorizer: f.auth, Templates: f.tmpl,
		Log: logr.Discard(), Now: f.clock.Now,
	}
}

// spec is a headless one-shot due at day0 in UTC.
func spec() v1alpha1.ScheduledWorkSpec {
	return v1alpha1.ScheduledWorkSpec{
		Title: "Loki retention trend audit", Owner: "human/dev-env-system/tom", Editor: "human/dev-env-system/tom",
		Due: day0.Format("2006-01-02T15:04"), TimeZone: "UTC", Mode: v1alpha1.ScheduledWorkHeadless,
		NotAfter: metav1.NewTime(day0.Add(90 * 24 * time.Hour)),
		Work:     v1alpha1.ScheduledWorkOrder{Template: "ops-audit", Repo: "haynes-ops", Prompt: "Audit the Loki retention trend and report."},
		Notify:   []string{"pushover:default"},
	}
}

func daily(s v1alpha1.ScheduledWorkSpec) v1alpha1.ScheduledWorkSpec {
	s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily}
	return s
}

// item creates a ScheduledWork in the fixture's item namespace.
func (f *fixture) item(name string, sp v1alpha1.ScheduledWorkSpec) types.NamespacedName {
	f.t.Helper()
	w := &v1alpha1.ScheduledWork{ObjectMeta: metav1.ObjectMeta{Namespace: f.items, Name: name}, Spec: sp}
	if err := cluster(f.t).Create(context.Background(), w); err != nil {
		f.t.Fatal(err)
	}
	return client.ObjectKeyFromObject(w)
}

func (f *fixture) get(key types.NamespacedName) *v1alpha1.ScheduledWork {
	f.t.Helper()
	var w v1alpha1.ScheduledWork
	if err := cluster(f.t).Get(context.Background(), key, &w); err != nil {
		f.t.Fatal(err)
	}
	return &w
}

// edit changes an item's spec, as the API would.
func (f *fixture) edit(key types.NamespacedName, fn func(*v1alpha1.ScheduledWorkSpec)) {
	f.t.Helper()
	w := f.get(key)
	fn(&w.Spec)
	if err := cluster(f.t).Update(context.Background(), w); err != nil {
		f.t.Fatal(err)
	}
}

// occ is the item's occurrence at at, or a zero one with no state.
func (f *fixture) occ(key types.NamespacedName, at time.Time) v1alpha1.ScheduledWorkOccurrence {
	f.t.Helper()
	if o := scheduledwork.Find(f.get(key), at); o != nil {
		return *o
	}
	return v1alpha1.ScheduledWorkOccurrence{}
}

// wantOcc fails unless the occurrence at at is in state, with reason when
// reason is not empty.
func (f *fixture) wantOcc(key types.NamespacedName, at time.Time, state, reason string) v1alpha1.ScheduledWorkOccurrence {
	f.t.Helper()
	o := f.occ(key, at)
	if o.State != state || (reason != "" && o.Reason != reason) {
		f.t.Fatalf("occurrence %s of %s: state %q reason %q, want %q %q", at.Format(time.RFC3339), key.Name, o.State, o.Reason, state, reason)
	}
	return o
}

// sessionsOf lists the fixture's sessions, from the API server.
func (f *fixture) sessionsOf() []v1alpha1.AgentSession {
	f.t.Helper()
	var list v1alpha1.AgentSessionList
	if err := cluster(f.t).List(context.Background(), &list, client.InNamespace(f.sessions)); err != nil {
		f.t.Fatal(err)
	}
	return list.Items
}

func (f *fixture) session(name string) *v1alpha1.AgentSession {
	f.t.Helper()
	var s v1alpha1.AgentSession
	if err := cluster(f.t).Get(context.Background(), types.NamespacedName{Namespace: f.sessions, Name: name}, &s); err != nil {
		f.t.Fatal(err)
	}
	return &s
}

// finish gives a session's status the task result state, as the reconciler
// records a headless task's verdict.
func (f *fixture) finish(name string, state v1alpha1.TaskResultState) {
	f.t.Helper()
	s := f.session(name)
	s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: state, Reason: "Task" + string(state)}
	if err := cluster(f.t).Status().Update(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
}

// release plays the releaser for an ended session: it records the
// occurrence's terminal outcome with scheduledwork.Record and Outcome, as
// controller.releaseScheduledWork does, and then lifts the finalizer, but, as
// the releaser, only once the fixture's clock is past
// scheduledwork.ReleaseHeldUntil: until then the finalizer keeps the derived
// name. With reap set, the session is deleted first (again if it already is),
// so it is gone once the finalizer goes. It reports whether it lifted it.
func (f *fixture) release(name string, reap bool) bool {
	f.t.Helper()
	ctx := context.Background()
	c := cluster(f.t)
	s := f.session(name)
	if reap && s.DeletionTimestamp.IsZero() {
		if err := c.Delete(ctx, s); err != nil {
			f.t.Fatal(err)
		}
		s = f.session(name)
	}
	at, err := scheduledwork.OccurrenceAt(s)
	if err != nil {
		f.t.Fatal(err)
	}
	w := f.get(types.NamespacedName{Namespace: f.items, Name: s.Annotations[v1alpha1.AnnotationScheduledWorkName]})
	switch r := scheduledwork.Record(w, at, s.Name, scheduledwork.Outcome(s, f.clock.Now())); r {
	case scheduledwork.Recorded:
		if err := c.Status().Update(ctx, w); err != nil {
			f.t.Fatal(err)
		}
	case scheduledwork.AlreadyTerminal:
	default:
		f.t.Fatalf("the releaser's record of %s: %s", name, r)
	}
	until, err := scheduledwork.ReleaseHeldUntil(s)
	if err != nil {
		f.t.Fatal(err)
	}
	if !f.clock.Now().After(until) {
		return false
	}
	orig := s.DeepCopy()
	controllerutil.RemoveFinalizer(s, v1alpha1.ScheduledWorkFinalizer)
	if err := c.Patch(ctx, s, client.MergeFrom(orig)); err != nil {
		f.t.Fatal(err)
	}
	return true
}

// pastHold is a time past day0's start deadline plus the release margin.
var pastHold = day0.Add(grace + scheduledwork.ReleaseMargin + time.Minute)

// derived is the session name of an item's occurrence.
func (f *fixture) derived(key types.NamespacedName, at time.Time) string {
	f.t.Helper()
	name, err := scheduledwork.SessionName(string(f.get(key).UID), at)
	if err != nil {
		f.t.Fatal(err)
	}
	return name
}

// at sets the clock and runs one pass of e.
func (f *fixture) at(e *Executor, t time.Time) {
	f.clock.Set(t)
	e.Tick(context.Background())
}
