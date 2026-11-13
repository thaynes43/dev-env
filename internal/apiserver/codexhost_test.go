package apiserver

import (
	"context"
	"encoding/json"
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
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
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
	// podStart overrides the pod's start time. noPodStart leaves it unset.
	// A heartbeat counts as this pod's report when status.agent.podUID is
	// this pod's UID (ruling Q). omitPodUID leaves that field empty.
	podStart                     *time.Time
	noPodStart                   bool
	omitPodUID                   bool
	rescueFailed, removalBlocked bool
	superseded                   bool
	rescue                       v1alpha1.RescueResult
	unpushed                     int
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
	var reportUID types.UID
	if o.phase == v1alpha1.PhaseRunning && !o.skipPod && o.heartbeat != nil && !o.omitPodUID {
		reportUID = uuid.NewUUID()
		saved.Status.Agent.PodUID = string(reportUID)
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
	podUID := reportUID
	if podUID == "" {
		podUID = uuid.NewUUID()
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: sessionNS, UID: podUID,
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
	if !o.noPodStart {
		start := f.now
		if o.heartbeat != nil {
			start = *o.heartbeat
		}
		if o.podStart != nil {
			start = *o.podStart
		}
		at := metav1.NewTime(start)
		pod.Status.StartTime = &at
	}
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

func codexAgentdLine(v string) string {
	return "agentd v" + v + " (commit abcdef012345, go1.27.1, linux/amd64)"
}

// replaceCodexPod swaps the host's pod for a new UID whose start time is start.
// The session status is left as it was.
func (f *fixture) replaceCodexPod(name string, start time.Time) types.UID {
	f.t.Helper()
	ctx := context.Background()
	var pod corev1.Pod
	key := types.NamespacedName{Namespace: sessionNS, Name: name}
	if err := f.c.Get(ctx, key, &pod); err != nil {
		f.t.Fatal(err)
	}
	old := pod.UID
	if err := f.c.Delete(ctx, &pod); err != nil {
		f.t.Fatal(err)
	}
	sess := f.session(name)
	neu := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: sessionNS, UID: uuid.NewUUID(),
			Labels:          pod.Labels,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sess, v1alpha1.GroupVersion.WithKind("AgentSession"))},
		},
		Spec: pod.Spec,
	}
	if neu.UID == "" || neu.UID == old {
		f.t.Fatalf("replacement uid %s old %s", neu.UID, old)
	}
	if err := f.c.Create(ctx, neu); err != nil {
		f.t.Fatal(err)
	}
	neu.Status.Phase = corev1.PodRunning
	neu.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	at := metav1.NewTime(start)
	neu.Status.StartTime = &at
	if err := f.c.Status().Update(ctx, neu); err != nil {
		f.t.Fatal(err)
	}
	return neu.UID
}

func (f *fixture) setHeartbeat(name string, at time.Time) {
	f.t.Helper()
	sess := f.session(name)
	hb := metav1.NewTime(at)
	sess.Status.Agent.LastHeartbeat = &hb
	if err := f.c.Status().Update(context.Background(), sess); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) setReportedPod(name string, uid types.UID) {
	f.t.Helper()
	sess := f.session(name)
	sess.Status.Agent.PodUID = string(uid)
	if err := f.c.Status().Update(context.Background(), sess); err != nil {
		f.t.Fatal(err)
	}
}

// Ruling O. Status keeps the previous pod's version, remote state and
// repository report. Those facts count only for the pod that is present.
func TestCodexHostPresentPodMustHaveReported(t *testing.T) {
	t.Run("replaced pod", func(t *testing.T) {
		f := newCodexFixture(t)
		earlier := f.now.Add(-time.Minute)
		f.addCodexHost("paired", codexHostOpt{agentd: codexAgentdLine("2.104.0"), heartbeat: &earlier})
		start := f.now.Add(-30 * time.Second)
		neu := f.replaceCodexPod("paired", start)
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "paired", codexhostpool.ReasonRemoteUnavailable)
		if got.Ready || f.session("paired").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatalf("used or leased a host the present pod has not reported: %+v", got)
		}
		f.setHeartbeat("paired", start)
		got = f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "paired", codexhostpool.ReasonRemoteUnavailable)
		if got.Ready || f.session("paired").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatalf("a heartbeat at the new pod's start, still carrying the previous pod UID, was used: %+v", got)
		}
		f.setReportedPod("paired", neu)
		got = f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "paired", "")
		if !got.Ready || f.session("paired").Annotations[codexHostLeaseAnnotation] == "" {
			t.Fatalf("the present pod's UID was not used: %+v", got)
		}
	})
	t.Run("replaced pod on check", func(t *testing.T) {
		f := newCodexFixture(t)
		earlier := f.now.Add(-time.Minute)
		f.addCodexHost("paired", codexHostOpt{agentd: codexAgentdLine("2.104.0"), heartbeat: &earlier})
		f.replaceCodexPod("paired", f.now.Add(-30*time.Second))
		got := f.wantChoice(f.selectCodex(tokClient, "dev", true), "report", "paired", codexhostpool.ReasonRemoteUnavailable)
		if got.Ready || got.Host != "paired" || f.session("paired").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatalf("check leased or used a replaced pod: %+v", got)
		}
	})
	t.Run("later heartbeat without the new uid", func(t *testing.T) {
		f := newCodexFixture(t)
		earlier := f.now.Add(-time.Minute)
		f.addCodexHost("paired", codexHostOpt{agentd: codexAgentdLine("2.104.0"), heartbeat: &earlier})
		neu := f.replaceCodexPod("paired", f.now.Add(-30*time.Second))
		f.setHeartbeat("paired", f.now)
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "paired", codexhostpool.ReasonRemoteUnavailable)
		if got.Ready || f.session("paired").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatalf("a later heartbeat from the previous pod was used: %+v", got)
		}
		f.setReportedPod("paired", neu)
		got = f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "paired", "")
		if !got.Ready {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("older than the staleness bound", func(t *testing.T) {
		f := newCodexFixture(t)
		at := f.now.Add(-authority.DefaultHeartbeatStaleAfter - time.Second)
		f.addCodexHost("paired", codexHostOpt{agentd: codexAgentdLine("2.104.0"), heartbeat: &at})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "paired", codexhostpool.ReasonRemoteUnavailable)
		if f.session("paired").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatalf("leased %+v", got)
		}
	})
	t.Run("at the staleness bound", func(t *testing.T) {
		f := newCodexFixture(t)
		at := f.now.Add(-authority.DefaultHeartbeatStaleAfter)
		f.addCodexHost("paired", codexHostOpt{heartbeat: &at})
		f.wantChoice(f.selectCodex(tokClient, "dev", true), "use", "paired", "")
	})
	t.Run("heartbeat in the future", func(t *testing.T) {
		f := newCodexFixture(t)
		at := f.now.Add(time.Second)
		f.addCodexHost("paired", codexHostOpt{agentd: codexAgentdLine("2.104.0"), heartbeat: &at})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "paired", codexhostpool.ReasonRemoteUnavailable)
		if f.session("paired").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatalf("leased %+v", got)
		}
	})
	t.Run("missing start time still counts", func(t *testing.T) {
		f := newCodexFixture(t)
		f.addCodexHost("paired", codexHostOpt{agentd: codexAgentdLine("2.104.0"), noPodStart: true})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "paired", "")
		if !got.Ready || f.session("paired").Annotations[codexHostLeaseAnnotation] == "" {
			t.Fatalf("a matching pod UID was not used when start time was absent: %+v", got)
		}
	})
	t.Run("safety reasons come first", func(t *testing.T) {
		f := newCodexFixture(t)
		// Each of these has not reported (no stored pod UID). The safety
		// reason is still the one recorded, not remote_unavailable.
		f.addCodexHost(codexHandsOffName, codexHostOpt{omitPodUID: true})
		f.addCodexHost("protected-host", codexHostOpt{protected: true, omitPodUID: true})
		f.addCodexHost("rescue-host", codexHostOpt{rescueFailed: true, omitPodUID: true})
		f.addCodexHost("open", codexHostOpt{})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "open", "")
		gotReason := map[string]string{}
		for _, s := range got.Skipped {
			gotReason[s.Name] = s.Reason
		}
		want := map[string]string{
			codexHandsOffName: codexhostpool.ReasonProtected,
			"protected-host":  codexhostpool.ReasonProtected,
			"rescue-host":     codexhostpool.ReasonRescueBlocked,
		}
		for name, reason := range want {
			if gotReason[name] != reason {
				t.Fatalf("skip %s=%q, want %q (all %#v)", name, gotReason[name], reason, gotReason)
			}
		}
	})
}

// An empty stored UID never counts, including against a pod whose UID is
// empty: equality alone would accept that pair.
func TestCodexPodReportedNeedsAStoredUID(t *testing.T) {
	now := time.Now()
	hb := metav1.NewTime(now)
	sess := &v1alpha1.AgentSession{}
	sess.Status.Agent = &v1alpha1.AgentStatus{LastHeartbeat: &hb}
	if codexPodReported(sess, &corev1.Pod{}, now) {
		t.Fatal("an empty stored UID counted as the pod's report")
	}
	sess.Status.Agent.PodUID = "uid-1"
	pod := &corev1.Pod{}
	pod.UID = "uid-1"
	if !codexPodReported(sess, pod, now) {
		t.Fatal("a matching stored UID with a fresh heartbeat did not count")
	}
}

// The lease read applies the same report check. A heartbeat that goes stale,
// or a pod replaced with one that has not reported, is not leased.
func TestCodexHostLeaseRequiresThePresentPodsReport(t *testing.T) {
	once := func(name string, change func(context.Context, client.WithWatch, *v1alpha1.AgentSession)) interceptor.Funcs {
		var done atomic.Bool
		return interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				sess, ok := obj.(*v1alpha1.AgentSession)
				if !ok || sess.Name != name || sess.Status.Phase == "" || !done.CompareAndSwap(false, true) {
					return nil
				}
				change(ctx, c, sess)
				return c.Get(ctx, key, obj, opts...)
			},
		}
	}
	t.Run("heartbeat goes stale", func(t *testing.T) {
		var f *fixture
		f = newCodexFixture(t, once("only", func(ctx context.Context, c client.WithWatch, sess *v1alpha1.AgentSession) {
			stale := f.now.Add(-authority.DefaultHeartbeatStaleAfter - time.Second)
			sess.Status.Agent.LastHeartbeat = &metav1.Time{Time: stale}
			if err := c.Status().Update(ctx, sess); err != nil {
				t.Fatal(err)
			}
			var pod corev1.Pod
			if err := c.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, &pod); err != nil {
				t.Fatal(err)
			}
			at := metav1.NewTime(stale)
			pod.Status.StartTime = &at
			if err := c.Status().Update(ctx, &pod); err != nil {
				t.Fatal(err)
			}
		}))
		f.addCodexHost("only", codexHostOpt{agentd: codexAgentdLine("2.104.0")})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "only", codexhostpool.ReasonRemoteUnavailable)
		if f.session("only").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatalf("leased a host whose heartbeat went stale: %+v", got)
		}
	})
	t.Run("pod is replaced", func(t *testing.T) {
		var old types.UID
		var f *fixture
		f = newCodexFixture(t, once("only", func(ctx context.Context, c client.WithWatch, sess *v1alpha1.AgentSession) {
			var pod corev1.Pod
			key := types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}
			if err := c.Get(ctx, key, &pod); err != nil {
				t.Fatal(err)
			}
			old = pod.UID
			if err := c.Delete(ctx, &pod); err != nil {
				t.Fatal(err)
			}
			neu := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: sess.Name, Namespace: sess.Namespace, UID: uuid.NewUUID(),
					Labels:          pod.Labels,
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sess, v1alpha1.GroupVersion.WithKind("AgentSession"))},
				},
				Spec: pod.Spec,
			}
			if neu.UID == old || old == "" {
				t.Fatalf("replacement uid %s old %s", neu.UID, old)
			}
			if err := c.Create(ctx, neu); err != nil {
				t.Fatal(err)
			}
			neu.Status.Phase = corev1.PodRunning
			neu.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			at := metav1.NewTime(f.now.Add(time.Second))
			neu.Status.StartTime = &at
			if err := c.Status().Update(ctx, neu); err != nil {
				t.Fatal(err)
			}
		}))
		f.addCodexHost("only", codexHostOpt{agentd: codexAgentdLine("2.104.0")})
		got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "only", codexhostpool.ReasonRemoteUnavailable)
		if string(f.session("only").UID) == "" || f.session("only").Annotations[codexHostLeaseAnnotation] != "" {
			t.Fatalf("leased the replacement: %+v", got)
		}
	})
}

// The answer's outdated is the revision read with the lease, not the list.
func TestCodexHostOutdatedFollowsLeasedPod(t *testing.T) {
	for _, tc := range []struct {
		name      string
		startOld  bool
		leasedOld bool
		wantOld   bool
	}{
		{"list current, lease behind", false, true, true},
		{"list behind, lease current", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var next string
			var once atomic.Bool
			f := newCodexFixture(t, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if err := c.Get(ctx, key, obj, opts...); err != nil {
						return err
					}
					sess, ok := obj.(*v1alpha1.AgentSession)
					if !ok || sess.Name != "paired" || sess.Status.Phase == "" || !once.CompareAndSwap(false, true) {
						return nil
					}
					var pod corev1.Pod
					if err := c.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, &pod); err != nil {
						t.Fatal(err)
					}
					pod.Labels[v1alpha1.LabelRevision] = next
					if err := c.Update(ctx, &pod); err != nil {
						t.Fatal(err)
					}
					return nil
				},
			})
			if tc.leasedOld {
				next = "old"
			} else {
				next = f.tmpl.Revision()
			}
			f.addCodexHost("paired", codexHostOpt{agentd: codexAgentdLine("2.104.0"), outdated: tc.startOld})
			got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "paired", "")
			if got.Outdated != tc.wantOld {
				t.Fatalf("outdated %v, want %v", got.Outdated, tc.wantOld)
			}
			var pod corev1.Pod
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &pod); err != nil {
				t.Fatal(err)
			}
			current := pod.Labels[v1alpha1.LabelRevision] == f.tmpl.Revision()
			if got.Outdated == current || f.session("paired").Annotations[codexHostLeaseAnnotation] == "" {
				t.Fatalf("outdated %v revision %q template %q", got.Outdated, pod.Labels[v1alpha1.LabelRevision], f.tmpl.Revision())
			}
		})
	}
}

// codexReportStatus is a heartbeat that reports agentd 2.104.0, remote state
// pair-code-issued and repository dev-env Ready.
func codexReportStatus(name string) protocol.Status {
	at := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)
	return protocol.Status{
		Session:  name,
		Agentd:   codexAgentdLine("2.104.0"),
		BootID:   "b1",
		BootedAt: at,
		Boot:     protocol.BootReady,
		Agent:    protocol.AgentState{State: "idle"},
		RemoteControl: &protocol.RemoteControl{
			Name: name, State: codexhostpool.StatePairCodeIssued,
		},
		ProjectBootstrap: &protocol.ProjectBootstrap{
			State:        protocol.BootstrapReady,
			Repositories: []protocol.ProjectBootstrapRepository{{Name: "dev-env", State: protocol.BootstrapReady}},
		},
		ObservedAt: at,
	}
}

func (f *fixture) callHeartbeat(name string, pod *corev1.Pod, body string) {
	f.t.Helper()
	sess := f.session(name)
	c := &caller{kind: kindSession, session: sess, pod: pod}
	req := httptest.NewRequest(http.MethodPost, protocol.HeartbeatPath(name), strings.NewReader(body))
	req.SetPathValue("name", name)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	code, _, err := f.srv.heartbeat(context.Background(), w, req, c)
	if err != nil || code != http.StatusOK {
		f.t.Fatalf("heartbeat: code %d err %v body %s", code, err, w.Body.String())
	}
}

// A heartbeat from the present pod must not stamp its UID onto fields the
// previous pod wrote after this read. The status patch conflicts, and the
// stored report stays the previous pod's.
func TestPresentPodHeartbeatConflictsWithThePreviousPodsLaterWrite(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{agentd: codexAgentdLine("2.102.0")})
	var previous corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &previous); err != nil {
		t.Fatal(err)
	}
	f.replaceCodexPod("paired", f.now)
	var present corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &present); err != nil {
		t.Fatal(err)
	}
	if present.UID == previous.UID {
		t.Fatal("the replacement kept the previous uid")
	}
	const lateState = "late-from-previous"
	conflicted := false
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			fresh := f.session("paired")
			fresh.Status.Agent.Agentd = codexAgentdLine("2.103.0")
			fresh.Status.Agent.PodUID = string(previous.UID)
			fresh.Status.RemoteControl.State = lateState
			if err := f.c.Status().Update(ctx, fresh); err != nil {
				return err
			}
			err := c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			conflicted = apierrors.IsConflict(err)
			return err
		},
	})
	raw, err := json.Marshal(codexReportStatus("paired"))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, protocol.HeartbeatPath("paired"), strings.NewReader(string(raw)))
	req.SetPathValue("name", "paired")
	req.Header.Set("Content-Type", "application/json")
	code, _, herr := f.srv.heartbeat(context.Background(), httptest.NewRecorder(), req, &caller{
		kind: kindSession, session: f.session("paired"), pod: &present,
	})
	stored := f.session("paired")
	agent := stored.Status.Agent
	if !conflicted || herr == nil || code == http.StatusOK {
		t.Fatalf("present heartbeat applied over the previous pod's later write: conflicted %v code %d err %v agent %+v remote %+v",
			conflicted, code, herr, agent, stored.Status.RemoteControl)
	}
	if agent == nil || agent.PodUID != string(previous.UID) || agent.Agentd != codexAgentdLine("2.103.0") ||
		stored.Status.RemoteControl == nil || stored.Status.RemoteControl.State != lateState {
		t.Fatalf("stored report is not the previous pod's later write: agent %+v remote %+v", agent, stored.Status.RemoteControl)
	}
}

// A heartbeat authenticated as the previous pod can be stored after the
// replacement exists. It carries that pod's UID, so selection does not use
// the host, and it writes no lease. A heartbeat from the present pod does.
func TestDelayedPreviousPodHeartbeatStaysUnreported(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{agentd: codexAgentdLine("2.104.0")})
	var p1 corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &p1); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(codexReportStatus("paired"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	f.callHeartbeat("paired", &p1, body)
	if got := f.session("paired").Status.Agent; got == nil || got.PodUID != string(p1.UID) || got.Agentd != codexAgentdLine("2.104.0") {
		t.Fatalf("first heartbeat %+v", got)
	}

	// The new pod's start equals the operator clock, so a comparison with
	// start time would accept the late heartbeat. The UID does not.
	p2 := f.replaceCodexPod("paired", f.now)
	f.callHeartbeat("paired", &p1, body)
	stored := f.session("paired").Status.Agent
	if stored == nil || stored.PodUID != string(p1.UID) || stored.PodUID == string(p2) {
		t.Fatalf("late heartbeat stored pod %q, previous %s present %s", stored.PodUID, p1.UID, p2)
	}
	if stored.Agentd != codexAgentdLine("2.104.0") || f.session("paired").Status.RemoteControl == nil ||
		f.session("paired").Status.RemoteControl.State != codexhostpool.StatePairCodeIssued {
		t.Fatalf("late heartbeat dropped the report: %+v remote %+v", stored, f.session("paired").Status.RemoteControl)
	}
	got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "paired", codexhostpool.ReasonRemoteUnavailable)
	if got.Ready || got.Host != "paired" || f.session("paired").Annotations[codexHostLeaseAnnotation] != "" {
		t.Fatalf("leased a host on the previous pod's late heartbeat: %+v", got)
	}

	var present corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &present); err != nil {
		t.Fatal(err)
	}
	if present.UID != p2 {
		t.Fatalf("present pod %s, want %s", present.UID, p2)
	}
	f.callHeartbeat("paired", &present, body)
	if got := f.session("paired").Status.Agent; got == nil || got.PodUID != string(present.UID) {
		t.Fatalf("present heartbeat %+v", got)
	}
	got = f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "paired", "")
	if !got.Ready || f.session("paired").Annotations[codexHostLeaseAnnotation] == "" {
		t.Fatalf("the present pod's heartbeat was not used: %+v", got)
	}
}

// Ruling S. P1's report is stored with a remote state selection does not use.
// The pod is replaced by P2. P1's heartbeat, whose body is pair-code-issued,
// is in the handler when P2's heartbeat is stored. P1's patch then lands.
// It carries P1's UID, so selection skips the host and writes no lease. P2's
// next heartbeat, with a usable report, makes selection use the host.
func TestLatePreviousPodHeartbeatStampsItsOwnUID(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{
		remote: codexhostpool.StateOffline,
		agentd: codexAgentdLine("2.104.0"),
	})
	var p1 corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &p1); err != nil {
		t.Fatal(err)
	}
	initial := f.session("paired")
	if initial.Status.Agent == nil || initial.Status.Agent.PodUID != string(p1.UID) ||
		initial.Status.RemoteControl == nil || initial.Status.RemoteControl.State != codexhostpool.StateOffline {
		t.Fatalf("P1 report agent %+v remote %+v", initial.Status.Agent, initial.Status.RemoteControl)
	}
	p2UID := f.replaceCodexPod("paired", f.now)
	var p2 corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &p2); err != nil {
		t.Fatal(err)
	}
	if p2.UID != p2UID || p2.UID == p1.UID {
		t.Fatalf("replacement %s previous %s", p2.UID, p1.UID)
	}

	p1Status := codexReportStatus("paired")
	p2Status := codexReportStatus("paired")
	p2Status.RemoteControl.State = codexhostpool.StateStarting
	p1Raw, err := json.Marshal(p1Status)
	if err != nil {
		t.Fatal(err)
	}
	p2Raw, err := json.Marshal(p2Status)
	if err != nil {
		t.Fatal(err)
	}

	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			// P2's heartbeat uses the plain client, so it is stored before
			// P1's patch is forwarded and does not re-enter this interceptor.
			saved := f.srv.Client
			f.srv.Client = f.c
			f.callHeartbeat("paired", &p2, string(p2Raw))
			f.srv.Client = saved
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	})
	f.callHeartbeat("paired", &p1, string(p1Raw))
	f.srv.Client = f.c

	stored := f.session("paired")
	agent := stored.Status.Agent
	remote := ""
	if stored.Status.RemoteControl != nil {
		remote = stored.Status.RemoteControl.State
	}
	uid := ""
	if agent != nil {
		uid = agent.PodUID
	}
	w := f.selectCodex(tokClient, "dev", false)
	choice := decode[apiv1.CodexHostChoice](t, w)
	lease := f.session("paired").Annotations[codexHostLeaseAnnotation]
	if uid != string(p1.UID) || remote != codexhostpool.StatePairCodeIssued ||
		choice.Action != "report" || choice.Host != "paired" || choice.Reason != codexhostpool.ReasonRemoteUnavailable ||
		choice.Ready || lease != "" {
		t.Fatalf("after P1's patch: podUID %q remote %q choice %+v lease %q, want P1 %s, %s, report remote_unavailable, no lease (P2 is %s)",
			uid, remote, choice, lease, p1.UID, codexhostpool.StatePairCodeIssued, p2.UID)
	}

	f.callHeartbeat("paired", &p2, string(p1Raw))
	if got := f.session("paired").Status.Agent; got == nil || got.PodUID != string(p2.UID) ||
		f.session("paired").Status.RemoteControl == nil ||
		f.session("paired").Status.RemoteControl.State != codexhostpool.StatePairCodeIssued {
		t.Fatalf("P2 heartbeat %+v remote %+v", f.session("paired").Status.Agent, f.session("paired").Status.RemoteControl)
	}
	got := f.wantChoice(f.selectCodex(tokClient, "dev", false), "use", "paired", "")
	if !got.Ready || f.session("paired").Annotations[codexHostLeaseAnnotation] == "" {
		t.Fatalf("P2's usable report was not used: %+v", got)
	}
}

// A podUID field in the request body does not set status.agent.podUID.
// The route sets it from the pod resolveSession authenticated.
func TestHeartbeatBodyPodUIDDoesNotSetStatus(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{omitPodUID: true})
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &pod); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(sessionNS + "/" + controller.ServiceAccountName)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	const tok = "tok-paired-body"
	f.auth[tok] = id

	raw, err := json.Marshal(codexReportStatus("paired"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	const fromBody = "from-the-body"
	fields["podUID"] = fromBody
	if agent, ok := fields["agent"].(map[string]any); ok {
		agent["podUID"] = fromBody
	}
	w := f.do(http.MethodPost, protocol.HeartbeatPath("paired"), tok, fields)
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body.String())
	}
	got := f.session("paired").Status.Agent
	if got == nil || got.PodUID != string(pod.UID) || got.PodUID == fromBody || got.Agentd != codexAgentdLine("2.104.0") {
		t.Fatalf("status.agent.podUID %+v, want the caller pod %s", got, pod.UID)
	}
}

// A heartbeat whose caller has no pod leaves status.agent.podUID empty, and
// selection does not treat the host as reported.
func TestHeartbeatWithoutPodLeavesHostUnreported(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{omitPodUID: true, agentd: "agentd v2.89.0 (commit abcdef012345, go1.27.1, linux/amd64)"})
	raw, err := json.Marshal(codexReportStatus("paired"))
	if err != nil {
		t.Fatal(err)
	}
	f.callHeartbeat("paired", nil, string(raw))
	sess := f.session("paired")
	got := sess.Status.Agent
	if got == nil || got.PodUID != "" || got.Agentd != codexAgentdLine("2.104.0") {
		t.Fatalf("status.agent %+v", got)
	}
	if sess.Status.RemoteControl == nil || sess.Status.RemoteControl.State != codexhostpool.StatePairCodeIssued {
		t.Fatalf("remote %+v", sess.Status.RemoteControl)
	}
	ready := false
	if got.ProjectBootstrap != nil {
		for _, repo := range got.ProjectBootstrap.Repositories {
			if repo.Name == "dev-env" && repo.State == protocol.BootstrapReady {
				ready = true
			}
		}
	}
	if !ready {
		t.Fatalf("repository report %+v", got.ProjectBootstrap)
	}
	choice := f.wantChoice(f.selectCodex(tokClient, "dev", false), "report", "paired", codexhostpool.ReasonRemoteUnavailable)
	if choice.Ready || choice.Host != "paired" || f.session("paired").Annotations[codexHostLeaseAnnotation] != "" {
		t.Fatalf("unreported host was used: %+v lease %q", choice, f.session("paired").Annotations[codexHostLeaseAnnotation])
	}
}

// The caller's pod UID equals the stored UID. The patch bytes still carry
// status.agent.podUID. A merge patch would otherwise leave the field out.
func TestHeartbeatPatchCarriesPodUIDWhenItEqualsTheStoredUID(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{agentd: codexAgentdLine("2.104.0")})
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &pod); err != nil {
		t.Fatal(err)
	}
	if got := f.session("paired").Status.Agent; got == nil || got.PodUID != string(pod.UID) {
		t.Fatalf("stored %+v, want %s", got, pod.UID)
	}
	var patchData []byte
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			data, err := patch.Data(obj)
			if err != nil {
				return err
			}
			patchData = append([]byte(nil), data...)
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	})
	raw, err := json.Marshal(codexReportStatus("paired"))
	if err != nil {
		t.Fatal(err)
	}
	f.callHeartbeat("paired", &pod, string(raw))
	var body map[string]any
	if err := json.Unmarshal(patchData, &body); err != nil {
		t.Fatalf("patch %s: %v", patchData, err)
	}
	status, _ := body["status"].(map[string]any)
	agent, _ := status["agent"].(map[string]any)
	if agent["podUID"] != string(pod.UID) {
		t.Fatalf("patch %s", patchData)
	}
	if got := f.session("paired").Status.Agent; got == nil || got.PodUID != string(pod.UID) {
		t.Fatalf("stored after heartbeat %+v", got)
	}
}

// A caller with no pod, and a UID already stored. The merge patch sends null
// for podUID, because the read had the key and the new object omits it, so
// the stored UID is removed.
func TestHeartbeatWithoutPodClearsAStoredUID(t *testing.T) {
	f := newCodexFixture(t)
	f.addCodexHost("paired", codexHostOpt{agentd: codexAgentdLine("2.104.0")})
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "paired"}, &pod); err != nil {
		t.Fatal(err)
	}
	before := ""
	if agent := f.session("paired").Status.Agent; agent != nil {
		before = agent.PodUID
	}
	if before == "" || before != string(pod.UID) {
		t.Fatalf("stored UID %q, want %s", before, pod.UID)
	}
	var patchData []byte
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			data, err := patch.Data(obj)
			if err != nil {
				return err
			}
			patchData = append([]byte(nil), data...)
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	})
	raw, err := json.Marshal(codexReportStatus("paired"))
	if err != nil {
		t.Fatal(err)
	}
	f.callHeartbeat("paired", nil, string(raw))
	var body map[string]any
	if err := json.Unmarshal(patchData, &body); err != nil {
		t.Fatalf("patch %s: %v", patchData, err)
	}
	status, _ := body["status"].(map[string]any)
	agent, _ := status["agent"].(map[string]any)
	if _, ok := agent["podUID"]; !ok || agent["podUID"] != nil {
		t.Fatalf("patch %s", patchData)
	}
	got := f.session("paired").Status.Agent
	if got == nil || got.PodUID != "" {
		t.Fatalf("stored UID after a caller with no pod: %q, was %q", got.PodUID, before)
	}
}
