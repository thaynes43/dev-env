package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexhostpool"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
	"github.com/thaynes43/dev-env/internal/templates"
)

func codexInterceptors(t *testing.T, extra interceptor.Funcs) interceptor.Funcs {
	t.Helper()
	get, list := extra.Get, extra.List
	extra.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.Secret); ok {
			t.Errorf("read Secret %s/%s", key.Namespace, key.Name)
			return errors.New("secret read")
		}
		if get != nil {
			return get(ctx, c, key, obj, opts...)
		}
		return c.Get(ctx, key, obj, opts...)
	}
	extra.List = func(ctx context.Context, c client.WithWatch, listObj client.ObjectList, opts ...client.ListOption) error {
		if _, ok := listObj.(*corev1.SecretList); ok {
			t.Errorf("listed Secrets")
			return errors.New("secret list")
		}
		if list != nil {
			return list(ctx, c, listObj, opts...)
		}
		return c.List(ctx, listObj, opts...)
	}
	return extra
}

func newCodexFixture(t *testing.T, extra ...interceptor.Funcs) *fixture {
	t.Helper()
	var fns interceptor.Funcs
	if len(extra) > 0 {
		fns = extra[0]
	}
	f := newFixtureWith(t, codexInterceptors(t, fns))
	f.enableCodexHosts()
	return f
}

func (f *fixture) enableCodexHosts() {
	f.t.Helper()
	f.srv.CodexHosts = &CodexHostConfig{
		MaxRunningPerCell: codexhostpool.DefaultMaxRunningPerCell,
		MaxRunning:        codexhostpool.DefaultMaxRunning,
		HandsOff:          []string{codexHandsOffName},
	}
	f.h = f.srv.Handler()
}

type codexHostOpt struct {
	profile, remote, parent, lease string
	blankRemote                    bool
	phase                          v1alpha1.SessionPhase
	mode                           v1alpha1.OperatingMode
	heartbeat, activity            *time.Time
	repos                          []string
	skipPod, notReady              bool
	protected, pool, outdated      bool
	omitAgentd                     bool
	agentd                         string
	rescueFailed, removalBlocked   bool
	superseded                     bool
	rescue                         v1alpha1.RescueResult
	unpushed                       int
}

func (f *fixture) addCodexHost(name string, o codexHostOpt) {
	f.t.Helper()
	if o.profile == "" {
		o.profile = "dev"
	}
	if o.remote == "" && !o.blankRemote {
		o.remote = "pair-code-issued"
	}
	if o.phase == "" {
		o.phase = v1alpha1.PhaseRunning
	}
	if o.mode == "" {
		o.mode = v1alpha1.OperatingModeRunning
	}
	if o.heartbeat == nil && o.phase == v1alpha1.PhaseRunning {
		at := f.now
		o.heartbeat = &at
	}
	if o.repos == nil {
		o.repos = []string{"dev-env"}
	}
	if o.parent == "" {
		o.parent = humanSA
	}
	ann := map[string]string{}
	if o.protected {
		ann[codexHostProtectedAnnotation] = "true"
	}
	if o.lease != "" {
		ann[codexHostLeaseAnnotation] = o.lease
	}
	labels := map[string]string{}
	if o.pool {
		labels[codexHostPoolLabel] = "true"
	}
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uuid.NewUUID(), Labels: labels, Annotations: ann},
		Spec: v1alpha1.AgentSessionSpec{
			Repo: "dev-env", Agent: v1alpha1.AgentCodex, Mode: v1alpha1.ModeRemote, Model: "gpt-6-luna",
			Profile: o.profile, OperatingMode: o.mode, Parent: o.parent,
		},
	}
	ctx := context.Background()
	if err := f.c.Create(ctx, sess); err != nil {
		f.t.Fatal(err)
	}
	var saved v1alpha1.AgentSession
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: name}, &saved); err != nil {
		f.t.Fatal(err)
	}
	repos := make([]v1alpha1.ProjectBootstrapRepositoryStatus, 0, len(o.repos))
	for _, repo := range o.repos {
		repos = append(repos, v1alpha1.ProjectBootstrapRepositoryStatus{Name: repo, State: codexHostBootstrapReady})
	}
	saved.Status = v1alpha1.AgentSessionStatus{
		Phase:         o.phase,
		RemoteControl: &v1alpha1.RemoteControlStatus{Name: name, State: o.remote},
		Agent: &v1alpha1.AgentStatus{
			Agentd: codexHostAgentd(o),
			ProjectBootstrap: &v1alpha1.ProjectBootstrapStatus{
				State: codexHostBootstrapReady, Repositories: repos,
			},
		},
	}
	if o.phase == v1alpha1.PhaseRunning && !o.skipPod {
		saved.Status.PodName = name
	}
	if o.heartbeat != nil {
		hb := metav1.NewTime(*o.heartbeat)
		saved.Status.Agent.LastHeartbeat = &hb
		saved.Status.Agent.LastActivity = &hb
	}
	if o.activity != nil {
		at := metav1.NewTime(*o.activity)
		saved.Status.Agent.LastActivity = &at
	}
	podReady := o.phase == v1alpha1.PhaseRunning && !o.skipPod && !o.notReady
	if podReady {
		saved.Status.Conditions = append(saved.Status.Conditions, metav1.Condition{
			Type: controller.ConditionPodReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.NewTime(f.now),
		})
	}
	if o.rescue != "" || o.superseded || o.unpushed > 0 {
		saved.Status.Rescue = &v1alpha1.RescueStatus{Result: o.rescue, Superseded: o.superseded, UnpushedRefs: make([]v1alpha1.RescuedRef, o.unpushed)}
	}
	if o.rescueFailed {
		saved.Status.Conditions = append(saved.Status.Conditions, metav1.Condition{
			Type: controller.ConditionRescueFailed, Status: metav1.ConditionTrue, Reason: "WorktreeRefused", LastTransitionTime: metav1.NewTime(f.now),
		})
	}
	if o.removalBlocked {
		saved.Status.Conditions = append(saved.Status.Conditions, metav1.Condition{
			Type: controller.ConditionRemovalBlocked, Status: metav1.ConditionTrue, Reason: "RescueFailed", LastTransitionTime: metav1.NewTime(f.now),
		})
	}
	if o.outdated {
		saved.Status.Conditions = append(saved.Status.Conditions, metav1.Condition{
			Type: controller.ConditionOutdated, Status: metav1.ConditionTrue, Reason: "Revision", LastTransitionTime: metav1.NewTime(f.now),
		})
	}
	if err := f.c.Status().Update(ctx, &saved); err != nil {
		f.t.Fatal(err)
	}
	if o.phase != v1alpha1.PhaseRunning || o.skipPod {
		return
	}
	rev := f.tmpl.Revision()
	if o.outdated {
		rev = "old"
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: sessionNS, UID: uuid.NewUUID(),
			Labels:          map[string]string{v1alpha1.LabelProfile: o.profile, v1alpha1.LabelRevision: rev},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&saved, v1alpha1.GroupVersion.WithKind("AgentSession"))},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "x"}}},
	}
	if err := f.c.Create(ctx, pod); err != nil {
		f.t.Fatal(err)
	}
	if o.notReady {
		return
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := f.c.Status().Update(ctx, pod); err != nil {
		f.t.Fatal(err)
	}
}

const (
	codexTestLaunch   = "abc123def456ghi7"
	codexOtherLaunch  = "0123456789abcdef"
	codexHandsOffName = "kept-off"
)

func (f *fixture) selectCodex(token, profile string, check bool) *httptest.ResponseRecorder {
	f.t.Helper()
	path := apiv1.CodexHostsSelectPath
	if check {
		path += "?check=true"
	}
	return f.do(http.MethodPost, path, token, apiv1.CodexHostSelectRequest{Repo: "dev-env", Profile: profile, LaunchID: codexTestLaunch})
}

func (f *fixture) workbenchCaller(name, profile string) string {
	f.t.Helper()
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	f.sessionPod(name, profile, 0)
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		f.t.Fatal(err)
	}
	id := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
	id.PodName, id.PodUID = name, string(pod.UID)
	tok := "tok-wb-" + name
	f.auth[tok] = id
	return tok
}

func (f *fixture) coordinatorCaller() string {
	f.t.Helper()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "codex-host-a-0", Namespace: "dev-env-system", UID: "host-pod-uid",
		Labels: map[string]string{v1alpha1.LabelProfile: "dev"}, Annotations: map[string]string{CoordinatorHostAnnotation: "host-a"}},
		Spec: corev1.PodSpec{ServiceAccountName: "codex-host-a", Containers: []corev1.Container{{Name: "host", Image: "synthetic"}}}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		f.t.Fatal(err)
	}
	id := saIdentity(coordinatorSA)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth[tokCoordinator] = id
	f.srv.Policy.CoordinatorEnabled = true
	f.srv.Policy.Coordinators = []CoordinatorHost{{ServiceAccount: coordinatorSA, PodName: pod.Name, HostID: "host-a"}}
	return tokCoordinator
}

func clientHolder() string { return "client/" + clientSA }

// codexHostAgentd is the reported status.agent.agentd line. The form is
// version.Info.String("agentd"). An empty option is the minimum version
// automatic selection accepts. omitAgentd leaves the line missing.
func codexHostAgentd(o codexHostOpt) string {
	switch {
	case o.omitAgentd:
		return ""
	case o.agentd != "":
		return o.agentd
	default:
		return "agentd v" + codexhostpool.MinAgentdVersion + " (commit abcdef012345, go1.27.1, linux/amd64)"
	}
}

func (f *fixture) wantChoice(w *httptest.ResponseRecorder, action, host, reason string) apiv1.CodexHostChoice {
	f.t.Helper()
	if w.Code != http.StatusOK {
		f.t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := decode[apiv1.CodexHostChoice](f.t, w)
	if got.Action != action || got.Host != host || got.Reason != reason || got.Ready != (action == "use") {
		f.t.Fatalf("choice %+v, want action %q host %q reason %q ready %v", got, action, host, reason, action == "use")
	}
	return got
}

// Ruling F: with the flag off neither route is registered.
func TestCodexHostRoutesUnregisteredWhenDisabled(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{apiv1.CodexHostsSelectPath, apiv1.CodexHostsReleasePath} {
		w := f.do(http.MethodPost, path, tokClient, apiv1.CodexHostSelectRequest{Repo: "dev-env"})
		e := wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
		if !strings.Contains(e.Message, "no route") {
			t.Fatalf("%s: %s", path, e.Message)
		}
	}
}

// Ruling G: ?check=true returns the object and never patches or updates.
func TestCodexHostSelectCheckWritesNothing(t *testing.T) {
	var reject atomic.Bool
	f := newCodexFixture(t, interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			if reject.Load() {
				t.Error("check patched a session")
				return errors.New("patch")
			}
			return errors.New("patch before the request")
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if reject.Load() {
				t.Error("check updated a session")
				return errors.New("update")
			}
			return c.Update(ctx, obj, opts...)
		},
	})
	f.addCodexHost("paired", codexHostOpt{})
	reject.Store(true)
	f.wantChoice(f.selectCodex(tokClient, "dev", true), "use", "paired", "")
	if got := f.session("paired").Annotations[codexHostLeaseAnnotation]; got != "" {
		t.Fatalf("check wrote %q", got)
	}
}

// Ruling G and I: use writes one lease, and release removes only this caller's.
func TestCodexHostSelectUseLeasesAndRelease(t *testing.T) {
	var patches []string
	f := newCodexFixture(t, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			patches = append(patches, obj.GetName())
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	f.addCodexHost("paired", codexHostOpt{})
	f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "paired", "")
	raw := f.session("paired").Annotations[codexHostLeaseAnnotation]
	holder, launchID, exp, ok := parseCodexHostLease(raw)
	if !ok || holder != clientHolder() || launchID != codexTestLaunch || !exp.Equal(f.now.Add(codexHostLeaseTTL)) || strings.Count(raw, " ") != 3 {
		t.Fatalf("lease %q holder %q launch %q exp %s", raw, holder, launchID, exp)
	}
	if len(patches) != 1 || patches[0] != "paired" {
		t.Fatalf("patches %v", patches)
	}
	w := f.do(http.MethodPost, apiv1.CodexHostsReleasePath, tokClient, apiv1.CodexHostReleaseRequest{Host: "paired", LaunchID: codexTestLaunch})
	got := decode[apiv1.CodexHostReleaseResponse](t, w)
	if w.Code != http.StatusOK || !got.Released || got.Host != "paired" {
		t.Fatalf("release %d %+v", w.Code, got)
	}
	if f.session("paired").Annotations[codexHostLeaseAnnotation] != "" {
		t.Fatal("the caller's lease remained")
	}
	if len(patches) != 2 {
		t.Fatalf("patches after release %v", patches)
	}
	w = f.do(http.MethodPost, apiv1.CodexHostsReleasePath, tokClient, apiv1.CodexHostReleaseRequest{Host: "paired", LaunchID: codexTestLaunch})
	if w.Code != http.StatusOK || len(patches) != 2 {
		t.Fatalf("absent lease: %d patches %v", w.Code, patches)
	}

	foreign := formatCodexHostLease(codexHostLeasePurpose, f.now.Add(time.Hour), "client/other", codexOtherLaunch)
	sess := f.session("paired")
	orig := sess.DeepCopy()
	if sess.Annotations == nil {
		sess.Annotations = map[string]string{}
	}
	sess.Annotations[codexHostLeaseAnnotation] = foreign
	if err := f.c.Patch(context.Background(), sess, client.MergeFrom(orig)); err != nil {
		t.Fatal(err)
	}
	before := len(patches)
	w = f.do(http.MethodPost, apiv1.CodexHostsReleasePath, tokClient, apiv1.CodexHostReleaseRequest{Host: "paired", LaunchID: codexTestLaunch})
	e := wantError(t, w, http.StatusConflict, apiv1.CodexHostLeaseHeld)
	if e.Message != apiv1.CodexHostLeaseHeldSentence || f.session("paired").Annotations[codexHostLeaseAnnotation] != foreign || len(patches) != before {
		t.Fatalf("foreign lease: %s patches %d", e.Message, len(patches))
	}
}

// A real intervening write conflicts, and the optimistic lock is what makes it
// conflict. The same host is leased on the retry.
func TestCodexHostSelectHeartbeatConflictRetriesSameHost(t *testing.T) {
	var n atomic.Int32
	var conflicted atomic.Bool
	f := newCodexFixture(t, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if obj.GetName() != "paired" || n.Add(1) != 1 {
				return c.Patch(ctx, obj, patch, opts...)
			}
			var live v1alpha1.AgentSession
			if err := c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &live); err != nil {
				t.Fatal(err)
			}
			before := live.ResourceVersion
			if live.Annotations == nil {
				live.Annotations = map[string]string{}
			}
			live.Annotations[v1alpha1.LabelPrefix+"side-write"] = "1"
			if err := c.Update(ctx, &live); err != nil {
				t.Fatal(err)
			}
			if live.ResourceVersion == before {
				t.Fatal("the intervening write did not change resourceVersion")
			}
			err := c.Patch(ctx, obj, patch, opts...)
			if !apierrors.IsConflict(err) {
				t.Errorf("a stale lease patch did not conflict: %v", err)
			} else {
				conflicted.Store(true)
			}
			return err
		},
	})
	f.addCodexHost("paired", codexHostOpt{})
	f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "paired", "")
	if !conflicted.Load() {
		t.Fatal("the optimistic lock was not required")
	}
	if _, _, _, ok := parseCodexHostLease(f.session("paired").Annotations[codexHostLeaseAnnotation]); !ok {
		t.Fatal("the retried lease was not written")
	}
	if f.session("paired").Annotations[v1alpha1.LabelPrefix+"side-write"] != "1" {
		t.Fatal("the intervening write was lost")
	}
}

// Another caller's live lease moves to the next host, or is host_busy.
func TestCodexHostSelectForeignLeaseMovesOrBusy(t *testing.T) {
	t.Run("moves to the next host", func(t *testing.T) {
		var once atomic.Bool
		var arm atomic.Bool
		var mu sync.Mutex
		var names []string
		var f *fixture
		f = newCodexFixture(t, interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				sess, ok := obj.(*v1alpha1.AgentSession)
				if !arm.Load() || !ok || sess.Name != "preferred" || !once.CompareAndSwap(false, true) {
					return nil
				}
				if sess.Annotations == nil {
					sess.Annotations = map[string]string{}
				}
				sess.Annotations[codexHostLeaseAnnotation] = formatCodexHostLease(codexHostLeasePurpose, f.now.Add(time.Hour), "client/other", codexOtherLaunch)
				return c.Update(ctx, sess.DeepCopy())
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				mu.Lock()
				names = append(names, obj.GetName())
				mu.Unlock()
				return c.Patch(ctx, obj, patch, opts...)
			},
		})
		newer := f.now
		older := f.now.Add(-time.Hour)
		f.addCodexHost("preferred", codexHostOpt{pool: true, activity: &newer})
		f.addCodexHost("second", codexHostOpt{activity: &older})
		arm.Store(true)
		f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "second", "")
		mu.Lock()
		defer mu.Unlock()
		if len(names) != 1 || names[0] != "second" {
			t.Fatalf("patches %v", names)
		}
		if !strings.Contains(f.session("preferred").Annotations[codexHostLeaseAnnotation], "client/other") {
			t.Fatal("the other caller's lease was overwritten")
		}
	})

	t.Run("host_busy", func(t *testing.T) {
		var patched atomic.Bool
		f := newCodexFixture(t, interceptor.Funcs{
			Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
				patched.Store(true)
				return errors.New("patch")
			},
		})
		f.addCodexHost("only", codexHostOpt{lease: formatCodexHostLease(codexHostLeasePurpose, f.now.Add(time.Hour), "client/other", codexOtherLaunch)})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "", "", codexhostpool.ReasonHostBusy)
		if len(got.Skipped) != 1 || got.Skipped[0].Reason != codexhostpool.ReasonHostBusy || patched.Load() {
			t.Fatalf("choice %+v patched %v", got, patched.Load())
		}
	})
}

func TestCodexHostSessionProfileFence(t *testing.T) {
	f := newCodexFixture(t)
	tok := f.sessionPod("caller", "dev", 0)
	f.addCodexHost("paired", codexHostOpt{})
	w := f.selectCodex(tok, "full", false)
	e := wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
	if e.Message != codexHostProfileDenied {
		t.Fatalf("message %q", e.Message)
	}
	f.wantChoice(f.selectCodex(tok, "", true), "use", "paired", "")

	w = f.selectCodex(tokClient, "", false)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	got := decode[apiv1.CodexHostChoice](t, w)
	if got.Action == "use" && got.Host == "paired" {
		t.Fatal("a client that omits the profile reused the dev host")
	}
	f.wantChoice(f.selectCodex(tokClient, "dev", true), "use", "paired", "")
}

func TestCodexHostWorkbenchOmitsProfile(t *testing.T) {
	f := newCodexFixture(t)
	tok := f.workbenchCaller("wb", "workbench")
	f.addCodexHost("dev-host", codexHostOpt{profile: "dev"})
	// The host is the wrong cell, so the omitted profile falls through to create
	// and writes no lease. The command test continues that create.
	got := f.wantChoice(f.selectCodex(tok, "", false), "create", "", "")
	if len(got.Skipped) != 1 || got.Skipped[0].Name != "dev-host" || got.Skipped[0].Reason != codexhostpool.ReasonProfileMismatch {
		t.Fatalf("skipped %+v", got.Skipped)
	}
	if f.session("dev-host").Annotations[codexHostLeaseAnnotation] != "" {
		t.Fatal("a create wrote a lease")
	}
}

func TestCodexHostCoordinatorDoesNotResumeForeignHost(t *testing.T) {
	var patched atomic.Bool
	f := newCodexFixture(t, interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			patched.Store(true)
			return errors.New("patch")
		},
	})
	tok := f.coordinatorCaller()
	f.addCodexHost("asleep", codexHostOpt{
		phase: v1alpha1.PhaseSuspended, mode: v1alpha1.OperatingModeSuspended,
		rescue: v1alpha1.RescueCleanAndPushed, parent: humanSA,
	})
	got := f.wantChoice(f.selectCodex(tok, "", true), "", "", codexhostpool.ReasonCallerCannotCreate)
	if got.Action == "resume" || len(got.Skipped) != 1 || got.Skipped[0].Reason != codexhostpool.ReasonCallerCannotResume || patched.Load() {
		t.Fatalf("choice %+v patched %v", got, patched.Load())
	}
}

func TestCodexHostAdmittedResumeWritesNothing(t *testing.T) {
	var patched atomic.Bool
	f := newCodexFixture(t, interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			patched.Store(true)
			return errors.New("patch")
		},
	})
	f.addCodexHost("asleep", codexHostOpt{
		phase: v1alpha1.PhaseSuspended, mode: v1alpha1.OperatingModeSuspended,
		rescue: v1alpha1.RescueCleanAndPushed,
	})
	got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "resume", "asleep", "")
	if patched.Load() || got.Ready {
		t.Fatalf("resume wrote %v choice %+v", patched.Load(), got)
	}
}

func TestCodexHostCoordinatorResumeWritesNothing(t *testing.T) {
	var patched atomic.Bool
	f := newCodexFixture(t, interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			patched.Store(true)
			return errors.New("patch")
		},
	})
	tok := f.coordinatorCaller()
	f.addCodexHost("mine", codexHostOpt{
		phase: v1alpha1.PhaseSuspended, mode: v1alpha1.OperatingModeSuspended,
		rescue: v1alpha1.RescueCleanAndPushed, parent: coordinatorSA,
	})
	f.wantChoice(f.selectCodex(tok, "", true), "resume", "mine", "")
	if patched.Load() {
		t.Fatal("resume wrote a lease")
	}
}

func TestCodexHostProtectedAndRescueAreNotLeased(t *testing.T) {
	cases := []struct {
		name   string
		opt    codexHostOpt
		reason string
	}{
		{"protected", codexHostOpt{protected: true}, codexhostpool.ReasonProtected},
		{"rescue failed", codexHostOpt{rescue: v1alpha1.RescueFailed}, codexhostpool.ReasonRescueBlocked},
		{"hands-off", codexHostOpt{}, codexhostpool.ReasonProtected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var patched atomic.Bool
			f := newCodexFixture(t, interceptor.Funcs{
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					patched.Store(true)
					return errors.New("patch")
				},
			})
			name := "kept"
			if tc.name == "hands-off" {
				name = codexHandsOffName
			}
			f.addCodexHost(name, tc.opt)
			got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "create", "", "")
			if patched.Load() || f.session(name).Annotations[codexHostLeaseAnnotation] != "" {
				t.Fatal("leased a host selection must not use")
			}
			if len(got.Skipped) != 1 || got.Skipped[0].Name != name || got.Skipped[0].Reason != tc.reason {
				t.Fatalf("skipped %+v", got.Skipped)
			}
		})
	}
}

func TestCodexHostCallerCannotCreate(t *testing.T) {
	var patched atomic.Bool
	f := newCodexFixture(t, interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			patched.Store(true)
			return errors.New("patch")
		},
	})
	tok := f.sessionPod("caller", "dev", 0)
	got := f.wantChoice(f.selectCodex(tok, "", false), "", "", codexhostpool.ReasonCallerCannotCreate)
	sentence, ok := apiv1.CodexHostSentence(got.Reason)
	if !ok || sentence == "" || patched.Load() {
		t.Fatalf("choice %+v patched %v", got, patched.Load())
	}
}

// Ruling J: the login fact is the newest fresh running host's remote state.
// No test reads a Secret; the fixture fails the test if one is read.
func TestCodexHostLoginFact(t *testing.T) {
	t.Run("fresh login-required is lapsed", func(t *testing.T) {
		f := newCodexFixture(t)
		f.addCodexHost("live", codexHostOpt{remote: "login-required"})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "", "", codexhostpool.ReasonLoginRequired)
		if len(got.Skipped) != 0 || f.session("live").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatalf("choice %+v", got)
		}
	})
	t.Run("fresh pair-code-issued is current", func(t *testing.T) {
		f := newCodexFixture(t)
		f.addCodexHost("live", codexHostOpt{remote: "pair-code-issued"})
		f.wantChoice(f.selectCodex(tokClient, "dev", true), "use", "live", "")
	})
	for _, state := range []string{"offline", "starting", "errored", "unpaired", ""} {
		t.Run("fresh "+state+" is current", func(t *testing.T) {
			f := newCodexFixture(t)
			opt := codexHostOpt{remote: state}
			if state == "" {
				opt.blankRemote = true
			}
			f.addCodexHost("live", opt)
			want := codexhostpool.ReasonRemoteUnavailable
			if state == "unpaired" {
				want = codexhostpool.ReasonUnpaired
			}
			got := f.wantChoice(f.selectCodex(tokClient, "dev", true), "report", "live", want)
			if got.Reason == codexhostpool.ReasonLoginRequired {
				t.Fatal("a non-lapsed state blocked selection")
			}
		})
	}
	t.Run("suspended frozen login-required is unknown", func(t *testing.T) {
		f := newCodexFixture(t)
		at := f.now
		f.addCodexHost("asleep", codexHostOpt{
			phase: v1alpha1.PhaseSuspended, mode: v1alpha1.OperatingModeSuspended,
			remote: "login-required", heartbeat: &at,
		})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "create", "", "")
		if got.Reason == codexhostpool.ReasonLoginRequired {
			t.Fatal("a suspended snapshot blocked selection")
		}
	})
	t.Run("stale heartbeat is unknown", func(t *testing.T) {
		f := newCodexFixture(t)
		at := f.now.Add(-authority.DefaultHeartbeatStaleAfter - time.Second)
		f.addCodexHost("stale", codexHostOpt{remote: "login-required", heartbeat: &at})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", true), "report", "stale", codexhostpool.ReasonRemoteUnavailable)
		if got.Reason == codexhostpool.ReasonLoginRequired {
			t.Fatal("a stale heartbeat blocked selection")
		}
	})
	t.Run("heartbeat at the bound still counts", func(t *testing.T) {
		f := newCodexFixture(t)
		at := f.now.Add(-authority.DefaultHeartbeatStaleAfter)
		f.addCodexHost("bound", codexHostOpt{remote: "login-required", heartbeat: &at})
		f.wantChoice(f.selectCodex(tokClient, "dev", true), "", "", codexhostpool.ReasonLoginRequired)
	})
	t.Run("newest heartbeat wins", func(t *testing.T) {
		f := newCodexFixture(t)
		older := f.now.Add(-time.Minute)
		f.addCodexHost("older", codexHostOpt{heartbeat: &older})
		f.addCodexHost("newer", codexHostOpt{remote: "login-required", protected: true})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", true), "", "", codexhostpool.ReasonLoginRequired)
		if len(got.Skipped) != 0 {
			t.Fatalf("lapsed login considered hosts: %+v", got.Skipped)
		}
	})
	t.Run("older lapsed reading loses", func(t *testing.T) {
		f := newCodexFixture(t)
		older := f.now.Add(-time.Minute)
		f.addCodexHost("older", codexHostOpt{remote: "login-required", heartbeat: &older})
		f.addCodexHost("newer", codexHostOpt{})
		f.wantChoice(f.selectCodex(tokClient, "dev", true), "use", "newer", "")
	})
}

func TestCodexHostAgentdVersionAndRevision(t *testing.T) {
	line := func(v string) string {
		return "agentd v" + v + " (commit abcdef012345, go1.27.1, linux/amd64)"
	}
	t.Run("exactly the minimum", func(t *testing.T) {
		f := newCodexFixture(t)
		f.addCodexHost("paired", codexHostOpt{})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", true), "use", "paired", "")
		if got.Outdated || !got.Ready {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("above the minimum and not current", func(t *testing.T) {
		f := newCodexFixture(t)
		f.addCodexHost("behind", codexHostOpt{agentd: line("2.104.0"), outdated: true})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "behind", "")
		if !got.Outdated || !got.Ready {
			t.Fatalf("%+v", got)
		}
		if _, id, _, ok := parseCodexHostLease(f.session("behind").Annotations[codexHostLeaseAnnotation]); !ok || id != codexTestLaunch {
			t.Fatal("a host one revision behind was not leased")
		}
	})
	t.Run("one patch below fills the cell", func(t *testing.T) {
		f := newCodexFixture(t)
		f.addCodexHost("old", codexHostOpt{agentd: line("2.101.1")})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "old", codexhostpool.ReasonImageOutdated)
		if got.Ready || got.Action == "create" || f.session("old").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatalf("used or leased %+v", got)
		}
	})
	t.Run("missing", func(t *testing.T) {
		f := newCodexFixture(t)
		f.addCodexHost("none", codexHostOpt{omitAgentd: true})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", true), "report", "none", codexhostpool.ReasonImageOutdated)
		if got.Action == "create" {
			t.Fatal("a missing version did not fill the cell")
		}
	})
	t.Run("unparsable", func(t *testing.T) {
		f := newCodexFixture(t)
		f.addCodexHost("bad", codexHostOpt{agentd: "agentd v2.102.0"})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", true), "report", "bad", codexhostpool.ReasonImageOutdated)
		if got.Action == "create" {
			t.Fatal("an unparsable version did not fill the cell")
		}
	})
}

// The lease step re-reads the chosen session and its pod and runs the same
// suitability test. A host that no longer qualifies is not leased.
func TestCodexHostLeaseRechecksSuitability(t *testing.T) {
	mutate := func(name string, change func(context.Context, client.WithWatch, *v1alpha1.AgentSession)) interceptor.Funcs {
		var once atomic.Bool
		return interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				sess, ok := obj.(*v1alpha1.AgentSession)
				// addCodexHost reads the session once before it writes status.
				// The lease read is the first one that already has a phase.
				if !ok || sess.Name != name || sess.Status.Phase == "" || !once.CompareAndSwap(false, true) {
					return nil
				}
				change(ctx, c, sess)
				return c.Get(ctx, key, obj, opts...)
			},
		}
	}
	two := func(t *testing.T, change func(context.Context, client.WithWatch, *v1alpha1.AgentSession)) {
		t.Helper()
		f := newCodexFixture(t, mutate("preferred", change))
		newer := f.now
		older := f.now.Add(-time.Hour)
		f.addCodexHost("preferred", codexHostOpt{pool: true, activity: &newer})
		f.addCodexHost("second", codexHostOpt{activity: &older})
		f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "second", "")
		if f.session("preferred").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatal("leased the host that stopped qualifying")
		}
		if _, id, _, ok := parseCodexHostLease(f.session("second").Annotations[codexHostLeaseAnnotation]); !ok || id != codexTestLaunch {
			t.Fatal("the next host was not leased")
		}
	}
	t.Run("protected", func(t *testing.T) {
		two(t, func(ctx context.Context, c client.WithWatch, sess *v1alpha1.AgentSession) {
			if sess.Annotations == nil {
				sess.Annotations = map[string]string{}
			}
			sess.Annotations[codexHostProtectedAnnotation] = "true"
			if err := c.Update(ctx, sess); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("suspended", func(t *testing.T) {
		two(t, func(ctx context.Context, c client.WithWatch, sess *v1alpha1.AgentSession) {
			sess.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			if err := c.Update(ctx, sess); err != nil {
				t.Fatal(err)
			}
			var fresh v1alpha1.AgentSession
			if err := c.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, &fresh); err != nil {
				t.Fatal(err)
			}
			fresh.Status.Phase = v1alpha1.PhaseSuspended
			if err := c.Status().Update(ctx, &fresh); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("login-required", func(t *testing.T) {
		f := newCodexFixture(t, mutate("only", func(ctx context.Context, c client.WithWatch, sess *v1alpha1.AgentSession) {
			if sess.Status.RemoteControl == nil {
				sess.Status.RemoteControl = &v1alpha1.RemoteControlStatus{}
			}
			sess.Status.RemoteControl.State = "login-required"
			if err := c.Status().Update(ctx, sess); err != nil {
				t.Fatal(err)
			}
		}))
		f.addCodexHost("only", codexHostOpt{})
		f.wantChoice(f.selectCodex(tokClient, "dev", false), "", "", codexhostpool.ReasonLoginRequired)
		if f.session("only").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatal("leased a host whose login had lapsed")
		}
	})
	t.Run("agentd below the minimum", func(t *testing.T) {
		f := newCodexFixture(t, mutate("only", func(ctx context.Context, c client.WithWatch, sess *v1alpha1.AgentSession) {
			if sess.Status.Agent == nil {
				t.Fatal("no agent status")
			}
			sess.Status.Agent.Agentd = "agentd v2.101.1 (commit abcdef012345, go1.27.1, linux/amd64)"
			if err := c.Status().Update(ctx, sess); err != nil {
				t.Fatal(err)
			}
		}))
		f.addCodexHost("only", codexHostOpt{})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "only", codexhostpool.ReasonImageOutdated)
		if got.Ready || f.session("only").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatal("leased a host whose agentd dropped below the minimum")
		}
	})
	t.Run("replaced", func(t *testing.T) {
		var old string
		f := newCodexFixture(t, mutate("preferred", func(ctx context.Context, c client.WithWatch, sess *v1alpha1.AgentSession) {
			old = string(sess.UID)
			if err := c.Delete(ctx, sess); err != nil {
				t.Fatal(err)
			}
			repl := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: sess.Name, Namespace: sess.Namespace, UID: uuid.NewUUID()}, Spec: v1alpha1.AgentSessionSpec{
				Repo: "dev-env", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Profile: "dev",
			}}
			if err := c.Create(ctx, repl); err != nil {
				t.Fatal(err)
			}
			if string(repl.UID) == old || old == "" {
				t.Fatalf("replacement uid %s old %s", repl.UID, old)
			}
		}))
		newer := f.now
		older := f.now.Add(-time.Hour)
		f.addCodexHost("preferred", codexHostOpt{pool: true, activity: &newer})
		f.addCodexHost("second", codexHostOpt{activity: &older})
		f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "second", "")
		if got := string(f.session("preferred").UID); got == old || f.session("preferred").Spec.Agent != "claude" {
			t.Fatalf("replacement uid %s agent %s", got, f.session("preferred").Spec.Agent)
		}
		if f.session("preferred").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatal("leased the replacement")
		}
	})
	t.Run("suitable replacement", func(t *testing.T) {
		var old string
		var replaced, patchedBeforeRelist atomic.Bool
		var listsAfter atomic.Int32
		f := newCodexFixture(t, interceptor.Funcs{
			Get: mutate("preferred", func(ctx context.Context, c client.WithWatch, sess *v1alpha1.AgentSession) {
				old = string(sess.UID)
				replaced.Store(true)
				repl := sess.DeepCopy()
				repl.ResourceVersion = ""
				repl.UID = uuid.NewUUID()
				status := repl.Status
				if err := c.Delete(ctx, sess); err != nil {
					t.Fatal(err)
				}
				repl.Status = v1alpha1.AgentSessionStatus{}
				if err := c.Create(ctx, repl); err != nil {
					t.Fatal(err)
				}
				var saved v1alpha1.AgentSession
				if err := c.Get(ctx, types.NamespacedName{Namespace: repl.Namespace, Name: repl.Name}, &saved); err != nil {
					t.Fatal(err)
				}
				saved.Status = status
				if err := c.Status().Update(ctx, &saved); err != nil {
					t.Fatal(err)
				}
				var pod corev1.Pod
				if err := c.Get(ctx, types.NamespacedName{Namespace: saved.Namespace, Name: saved.Name}, &pod); err != nil {
					t.Fatal(err)
				}
				ref := metav1.NewControllerRef(&saved, v1alpha1.GroupVersion.WithKind("AgentSession"))
				pod.OwnerReferences = []metav1.OwnerReference{*ref}
				if err := c.Update(ctx, &pod); err != nil {
					t.Fatal(err)
				}
				if string(saved.UID) == old || old == "" || saved.Spec.Agent != v1alpha1.AgentCodex {
					t.Fatalf("replacement uid %s old %s agent %s", saved.UID, old, saved.Spec.Agent)
				}
			}).Get,
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if replaced.Load() {
					listsAfter.Add(1)
				}
				return c.List(ctx, list, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if sess, ok := obj.(*v1alpha1.AgentSession); ok && sess.Annotations[codexHostLeaseAnnotation] != "" && listsAfter.Load() == 0 {
					patchedBeforeRelist.Store(true)
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		})
		newer := f.now
		older := f.now.Add(-time.Hour)
		f.addCodexHost("preferred", codexHostOpt{pool: true, activity: &newer})
		f.addCodexHost("second", codexHostOpt{activity: &older})
		f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "preferred", "")
		if string(f.session("preferred").UID) == old || old == "" {
			t.Fatal("the host was not replaced")
		}
		if _, id, _, ok := parseCodexHostLease(f.session("preferred").Annotations[codexHostLeaseAnnotation]); !ok || id != codexTestLaunch {
			t.Fatal("the suitable replacement was not leased after reselection")
		}
		if patchedBeforeRelist.Load() {
			t.Fatal("leased the replacement before the UID check caused a reselection")
		}
	})
}

func TestCodexHostPodReadyNotSessionCondition(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("live", codexHostOpt{remote: "login-required"})
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "live"}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	if err := f.c.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "live", codexhostpool.ReasonNotRunning)
	if got.Reason == codexhostpool.ReasonLoginRequired {
		t.Fatal("a pod that is not Ready supplied the login fact")
	}
	if f.session("live").Annotations[codexHostLeaseAnnotation] != "" {
		t.Fatal("leased a pod that is not Ready")
	}

	held := newCodexFixture(t)
	held.addCodexHost("held", codexHostOpt{})
	if err := held.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "held"}, &pod); err != nil {
		t.Fatal(err)
	}
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[v1alpha1.LabelHold] = "true"
	if err := held.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	held.wantChoice(held.selectCodex(tokClient, "dev", true), "report", "held", codexhostpool.ReasonNotRunning)
}

func TestCodexHostMalformedCheckWritesNothing(t *testing.T) {
	var patched atomic.Bool
	f := newCodexFixture(t, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			patched.Store(true)
			return errors.New("patch")
		},
	})
	f.addCodexHost("paired", codexHostOpt{})
	body := apiv1.CodexHostSelectRequest{Repo: "dev-env", Profile: "dev", LaunchID: codexTestLaunch}
	for _, q := range []string{"?check=true;", "?check=true&other=1", "?other=1", "?check=false", "?check=true&check=true"} {
		w := f.do(http.MethodPost, apiv1.CodexHostsSelectPath+q, tokClient, body)
		if w.Code == http.StatusOK || patched.Load() {
			t.Fatalf("%s status %d patched %v body %s", q, w.Code, patched.Load(), w.Body.String())
		}
		wantError(t, w, http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	}
	w := f.do(http.MethodPost, apiv1.CodexHostsSelectPath, tokClient, apiv1.CodexHostSelectRequest{Repo: "dev-env", Profile: "dev"})
	wantError(t, w, http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if patched.Load() {
		t.Fatal("a missing launch id wrote")
	}
}

func TestCodexHostLeaseCarriesLaunchID(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{lease: formatCodexHostLease(codexHostLeasePurpose, f.now.Add(time.Hour), clientHolder(), codexTestLaunch)})
	f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "paired", "")
	if _, id, _, ok := parseCodexHostLease(f.session("paired").Annotations[codexHostLeaseAnnotation]); !ok || id != codexTestLaunch {
		t.Fatal("the same launch id was not renewed")
	}

	busy := newCodexFixture(t)
	busy.addCodexHost("busy", codexHostOpt{lease: formatCodexHostLease(codexHostLeasePurpose, busy.now.Add(time.Hour), clientHolder(), codexOtherLaunch)})
	busy.wantChoice(busy.selectCodex(tokClient, "dev", false), "", "", codexhostpool.ReasonHostBusy)
	if _, id, _, ok := parseCodexHostLease(busy.session("busy").Annotations[codexHostLeaseAnnotation]); !ok || id != codexOtherLaunch {
		t.Fatal("a different launch id was overwritten")
	}

	oldF := newCodexFixture(t)
	old := "use " + oldF.now.Add(time.Hour).UTC().Format(time.RFC3339) + " client/other"
	oldF.addCodexHost("old", codexHostOpt{lease: old})
	oldF.wantChoice(oldF.selectCodex(tokClient, "dev", false), "", "", codexhostpool.ReasonHostBusy)
	if got := oldF.session("old").Annotations[codexHostLeaseAnnotation]; got != old {
		t.Fatalf("an unexpired three-field lease was overwritten: %q", got)
	}
	if !codexLeaseHeldByOther(old, clientHolder(), codexTestLaunch, oldF.now) {
		t.Fatal("an unexpired three-field lease was not busy")
	}
	w := oldF.do(http.MethodPost, apiv1.CodexHostsReleasePath, tokClient, apiv1.CodexHostReleaseRequest{Host: "old", LaunchID: codexTestLaunch})
	wantError(t, w, http.StatusConflict, apiv1.CodexHostLeaseHeld)
	if oldF.session("old").Annotations[codexHostLeaseAnnotation] != old {
		t.Fatal("release cleared a malformed lease")
	}

	same := newCodexFixture(t)
	sameLease := "use " + same.now.Add(time.Hour).UTC().Format(time.RFC3339) + " " + clientHolder()
	same.addCodexHost("same", codexHostOpt{lease: sameLease})
	same.wantChoice(same.selectCodex(tokClient, "dev", false), "", "", codexhostpool.ReasonHostBusy)
	if same.session("same").Annotations[codexHostLeaseAnnotation] != sameLease {
		t.Fatal("an unexpired lease with this holder and no launch id was renewed")
	}

	junkF := newCodexFixture(t)
	junk := "nope " + junkF.now.Add(time.Hour).UTC().Format(time.RFC3339) + " extra fields here"
	junkF.addCodexHost("junk", codexHostOpt{lease: junk})
	junkF.wantChoice(junkF.selectCodex(tokClient, "dev", true), "", "", codexhostpool.ReasonHostBusy)
	if !codexLeaseHeldByOther(junk, clientHolder(), codexTestLaunch, junkF.now) || junkF.session("junk").Annotations[codexHostLeaseAnnotation] != junk {
		t.Fatal("a future second field was not busy")
	}

	pastF := newCodexFixture(t)
	past := "use " + pastF.now.Add(-time.Hour).UTC().Format(time.RFC3339) + " client/other"
	pastF.addCodexHost("past", codexHostOpt{lease: past})
	pastF.wantChoice(pastF.selectCodex(tokClient, "dev", false), "use", "past", "")
	if _, id, _, ok := parseCodexHostLease(pastF.session("past").Annotations[codexHostLeaseAnnotation]); !ok || id != codexTestLaunch {
		t.Fatal("an expired three-field lease was not vacant")
	}
	if codexLeaseHeldByOther(past, clientHolder(), codexTestLaunch, pastF.now) {
		t.Fatal("an expired second field was busy")
	}

	unread := newCodexFixture(t)
	bad := "use not-a-time " + clientHolder() + " " + codexTestLaunch
	unread.addCodexHost("bad", codexHostOpt{lease: bad})
	unread.wantChoice(unread.selectCodex(tokClient, "dev", false), "use", "bad", "")
	if _, id, _, ok := parseCodexHostLease(unread.session("bad").Annotations[codexHostLeaseAnnotation]); !ok || id != codexTestLaunch {
		t.Fatal("a lease with no readable expiry was not vacant")
	}

	rel := newCodexFixture(t)
	kept := formatCodexHostLease(codexHostLeasePurpose, rel.now.Add(time.Hour), clientHolder(), codexTestLaunch)
	rel.addCodexHost("paired", codexHostOpt{lease: kept})
	w = rel.do(http.MethodPost, apiv1.CodexHostsReleasePath, tokClient, apiv1.CodexHostReleaseRequest{Host: "paired", LaunchID: codexOtherLaunch})
	wantError(t, w, http.StatusConflict, apiv1.CodexHostLeaseHeld)
	if rel.session("paired").Annotations[codexHostLeaseAnnotation] != kept {
		t.Fatal("release cleared another launch id")
	}
}

func TestCodexHostWorkbenchNamesProfile(t *testing.T) {
	f := newCodexFixture(t)
	tok := f.workbenchCaller("wb", "workbench")
	f.addCodexHost("dev-host", codexHostOpt{profile: "dev"})
	f.addCodexHost("full-host", codexHostOpt{profile: "full"})
	f.wantChoice(f.selectCodex(tok, "dev", true), "use", "dev-host", "")
	f.wantChoice(f.selectCodex(tok, "", true), "use", "full-host", "")

	bare := newCodexFixture(t)
	tok = bare.workbenchCaller("wb", "workbench")
	got := bare.wantChoice(bare.selectCodex(tok, "dev", false), "", "", codexhostpool.ReasonCallerCannotCreate)
	if got.Action == "create" {
		t.Fatal("a workbench created off the default profile")
	}
}

func TestCodexHostCoordinatorNonCheckWritesNothing(t *testing.T) {
	var patched atomic.Bool
	f := newCodexFixture(t, interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			patched.Store(true)
			return errors.New("patch")
		},
	})
	tok := f.coordinatorCaller()
	f.addCodexHost("mine", codexHostOpt{
		phase: v1alpha1.PhaseSuspended, mode: v1alpha1.OperatingModeSuspended,
		rescue: v1alpha1.RescueCleanAndPushed, parent: coordinatorSA,
	})
	w := f.selectCodex(tok, "", false)
	e := wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
	if e.Message != codexHostCoordinatorCheckOnly || patched.Load() {
		t.Fatalf("message %q patched %v", e.Message, patched.Load())
	}
}
