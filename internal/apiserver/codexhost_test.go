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
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		HandsOff:          DefaultCodexHostHandsOff(),
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
		Agent: &v1alpha1.AgentStatus{ProjectBootstrap: &v1alpha1.ProjectBootstrapStatus{
			State: codexHostBootstrapReady, Repositories: repos,
		}},
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
}

func (f *fixture) selectCodex(token, profile string, check bool) *httptest.ResponseRecorder {
	f.t.Helper()
	path := apiv1.CodexHostsSelectPath
	if check {
		path += "?check=true"
	}
	return f.do(http.MethodPost, path, token, apiv1.CodexHostSelectRequest{Repo: "dev-env", Profile: profile})
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

func (f *fixture) wantChoice(w *httptest.ResponseRecorder, action, host, reason string) apiv1.CodexHostChoice {
	f.t.Helper()
	if w.Code != http.StatusOK {
		f.t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := decode[apiv1.CodexHostChoice](f.t, w)
	if got.Action != action || got.Host != host || got.Reason != reason {
		f.t.Fatalf("choice %+v, want action %q host %q reason %q", got, action, host, reason)
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
	f.wantChoice(f.selectCodex(tokClient, "dev", true), "use", "paired", codexhostpool.ReasonFolderTrustPending)
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
	f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "paired", codexhostpool.ReasonFolderTrustPending)
	raw := f.session("paired").Annotations[codexHostLeaseAnnotation]
	holder, exp, ok := parseCodexHostLease(raw)
	if !ok || holder != clientHolder() || !exp.Equal(f.now.Add(codexHostLeaseTTL)) || strings.Count(raw, " ") != 2 {
		t.Fatalf("lease %q holder %q exp %s", raw, holder, exp)
	}
	if len(patches) != 1 || patches[0] != "paired" {
		t.Fatalf("patches %v", patches)
	}
	w := f.do(http.MethodPost, apiv1.CodexHostsReleasePath, tokClient, apiv1.CodexHostReleaseRequest{Host: "paired"})
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
	w = f.do(http.MethodPost, apiv1.CodexHostsReleasePath, tokClient, apiv1.CodexHostReleaseRequest{Host: "paired"})
	if w.Code != http.StatusOK || len(patches) != 2 {
		t.Fatalf("absent lease: %d patches %v", w.Code, patches)
	}

	foreign := formatCodexHostLease(codexHostLeasePurpose, f.now.Add(time.Hour), "client/other")
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
	w = f.do(http.MethodPost, apiv1.CodexHostsReleasePath, tokClient, apiv1.CodexHostReleaseRequest{Host: "paired"})
	e := wantError(t, w, http.StatusConflict, apiv1.CodexHostLeaseHeld)
	if e.Message != apiv1.CodexHostLeaseHeldSentence || f.session("paired").Annotations[codexHostLeaseAnnotation] != foreign || len(patches) != before {
		t.Fatalf("foreign lease: %s patches %d", e.Message, len(patches))
	}
}

// A heartbeat conflict is retried on the same host.
func TestCodexHostSelectHeartbeatConflictRetriesSameHost(t *testing.T) {
	var n atomic.Int32
	var mu sync.Mutex
	var names []string
	f := newCodexFixture(t, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			mu.Lock()
			names = append(names, obj.GetName())
			mu.Unlock()
			if obj.GetName() == "paired" && n.Add(1) == 1 {
				return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, "paired", errors.New("heartbeat"))
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	f.addCodexHost("paired", codexHostOpt{})
	f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "paired", codexhostpool.ReasonFolderTrustPending)
	mu.Lock()
	defer mu.Unlock()
	if len(names) != 2 || names[0] != "paired" || names[1] != "paired" {
		t.Fatalf("patches %v", names)
	}
	if f.session("paired").Annotations[codexHostLeaseAnnotation] == "" {
		t.Fatal("the retried lease was not written")
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
				sess.Annotations[codexHostLeaseAnnotation] = formatCodexHostLease(codexHostLeasePurpose, f.now.Add(time.Hour), "client/other")
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
		f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "second", codexhostpool.ReasonFolderTrustPending)
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
		f.addCodexHost("only", codexHostOpt{lease: formatCodexHostLease(codexHostLeasePurpose, f.now.Add(time.Hour), "client/other")})
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
	f.wantChoice(f.selectCodex(tok, "", true), "use", "paired", codexhostpool.ReasonFolderTrustPending)

	w = f.selectCodex(tokClient, "", false)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	got := decode[apiv1.CodexHostChoice](t, w)
	if got.Action == "use" && got.Host == "paired" {
		t.Fatal("a client that omits the profile reused the dev host")
	}
	f.wantChoice(f.selectCodex(tokClient, "dev", true), "use", "paired", codexhostpool.ReasonFolderTrustPending)
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
	f.wantChoice(f.selectCodex(tok, "", true), "resume", "mine", codexhostpool.ReasonFolderTrustPending)
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
				name = DefaultCodexHostHandsOff()[0]
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
		f.wantChoice(f.selectCodex(tokClient, "dev", true), "use", "live", codexhostpool.ReasonFolderTrustPending)
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
		f.wantChoice(f.selectCodex(tokClient, "dev", true), "use", "newer", codexhostpool.ReasonFolderTrustPending)
	})
}
