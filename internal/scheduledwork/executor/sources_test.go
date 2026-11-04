package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
)

// sourcesTemplates is the unit tests' templates document.
const sourcesTemplates = `
version: 1
templates:
  ops-audit:
    agent: claude
    model: claude-opus-5-5
    effort: xhigh
    size: M
    profile: dev
    repos: [haynes-ops]
    modes: [headless, interactive]
    maxDuration: 40m
    idleSuspendAfter: 2h
    admits: [human, session, approved]
  approved-only:
    agent: claude
    model: claude-sonnet-5-5
    effort: high
    size: S
    profile: dev
    repos: [haynes-ops]
    modes: [headless]
    maxDuration: 20m
    admits: [approved]
`

var unitNow = time.Date(2030, 6, 3, 14, 0, 0, 0, time.UTC)

const (
	unitItems    = "dev-env-system"
	unitSessions = "dev-agents"
	unitOwner    = "session/coord-1/uid-1"
)

func templatesCM(doc string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: unitItems, Name: authority.TemplatesConfigMap},
		Data: map[string]string{authority.TemplatesKey: doc}}
}

func liveOwnerObjects() []client.Object {
	hb := metav1.NewTime(unitNow.Add(-20 * time.Second))
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: unitSessions, Name: "coord-1", UID: types.UID("uid-1")},
		Spec:       v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeRemote, OperatingMode: v1alpha1.OperatingModeRunning},
		Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning, Agent: &v1alpha1.AgentStatus{
			Status: protocol.AgentBusy, Boot: protocol.BootReady, LastHeartbeat: &hb}},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: unitSessions, Name: "coord-1", CreationTimestamp: metav1.NewTime(unitNow.Add(-time.Hour))}}
	return []client.Object{s, pod}
}

func sources(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) (*Sources, client.WithWatch) {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objs...).WithStatusSubresource(&v1alpha1.AgentSession{})
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	c := b.Build()
	return &Sources{Reader: c, SessionNamespace: unitSessions, TemplatesNamespace: unitItems}, c
}

func unitItem(owner, editor, template string) *v1alpha1.ScheduledWork {
	sp := spec()
	sp.Owner, sp.Editor, sp.Work.Template = owner, editor, template
	return &v1alpha1.ScheduledWork{ObjectMeta: metav1.ObjectMeta{Namespace: unitItems, Name: "audit"}, Spec: sp}
}

func approve(w *v1alpha1.ScheduledWork) *v1alpha1.ScheduledWork {
	w.Status.Approval = &v1alpha1.ScheduledWorkApproval{By: "human/dev-env-system/tom", At: metav1.NewTime(unitNow.Add(-time.Hour)), SpecHash: scheduledwork.SpecHash(w.Spec)}
	return w
}

func digestOf(t *testing.T, name string) string {
	t.Helper()
	ts, err := authority.ParseTemplates(map[string]string{authority.TemplatesKey: sourcesTemplates})
	if err != nil {
		t.Fatal(err)
	}
	return authority.TemplateDigest(ts[name])
}

var errInjected = errors.New("injected read failure")

// failGet fails every Get of the kind obj is.
func failGet(kind client.Object) *interceptor.Funcs {
	return &interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if typeName(obj) == typeName(kind) {
			return errInjected
		}
		return c.Get(ctx, key, obj, opts...)
	}}
}

func typeName(o client.Object) string {
	switch o.(type) {
	case *corev1.ConfigMap:
		return "configmap"
	case *corev1.Pod:
		return "pod"
	case *v1alpha1.AgentSession:
		return "agentsession"
	}
	return "other"
}

func TestSourcesResolve(t *testing.T) {
	ctx := context.Background()
	s, _ := sources(t, nil, templatesCM(sourcesTemplates))
	got, err := s.Resolve(ctx, "ops-audit")
	if err != nil {
		t.Fatal(err)
	}
	want := Template{Agent: "claude", Model: "claude-opus-5-5", Effort: "xhigh", Size: "M", Profile: "dev",
		Repos: []string{"haynes-ops"}, Modes: []string{v1alpha1.ScheduledWorkHeadless, v1alpha1.ScheduledWorkInteractive},
		MaxDuration: 40 * time.Minute, IdleSuspendAfter: 2 * time.Hour, Admits: []Basis{BasisHuman, BasisSession, BasisApproved}}
	if !equalTemplate(got, want) {
		t.Errorf("Resolve = %+v, want %+v", got, want)
	}
	for name, c := range map[string]struct {
		objs   []client.Object
		funcs  *interceptor.Funcs
		tmpl   string
		reason string // a *Refusal's reason; empty is a plain error
	}{
		"no ConfigMap":     {tmpl: "ops-audit", reason: "no dispatch templates: ConfigMap dev-env-system/dev-env-scheduled-work does not exist"},
		"no data":          {objs: []client.Object{&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: unitItems, Name: authority.TemplatesConfigMap}}}, tmpl: "ops-audit", reason: "no dispatch templates are configured"},
		"invalid document": {objs: []client.Object{templatesCM(strings.Replace(sourcesTemplates, "model: claude-opus-5-5", "model: opus", 1))}, tmpl: "ops-audit", reason: `invalid dispatch templates: templates.yaml: template "ops-audit": model: "opus" is an alias`},
		"unknown template": {objs: []client.Object{templatesCM(sourcesTemplates)}, tmpl: "ops-auditor", reason: "no such dispatch template"},
		"read failure":     {objs: []client.Object{templatesCM(sourcesTemplates)}, funcs: failGet(&corev1.ConfigMap{}), tmpl: "ops-audit"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := sources(t, c.funcs, c.objs...)
			_, err := s.Resolve(ctx, c.tmpl)
			var refusal *Refusal
			switch {
			case c.reason == "" && (err == nil || errors.As(err, &refusal)):
				t.Fatalf("Resolve = %v, want a plain error (a failed read, tried again)", err)
			case c.reason != "" && !errors.As(err, &refusal):
				t.Fatalf("Resolve = %v, want a *Refusal", err)
			case c.reason != "" && !strings.HasPrefix(refusal.Reason, c.reason):
				t.Errorf("refusal %q, want %q", refusal.Reason, c.reason)
			}
		})
	}
}

func equalTemplate(a, b Template) bool {
	return a.Agent == b.Agent && a.Model == b.Model && a.Effort == b.Effort && a.Size == b.Size && a.Profile == b.Profile &&
		strings.Join(a.Repos, ",") == strings.Join(b.Repos, ",") && strings.Join(a.Modes, ",") == strings.Join(b.Modes, ",") &&
		a.MaxDuration == b.MaxDuration && a.IdleSuspendAfter == b.IdleSuspendAfter && len(a.Admits) == len(b.Admits) &&
		func() bool {
			for i := range a.Admits {
				if a.Admits[i] != b.Admits[i] {
					return false
				}
			}
			return true
		}()
}

func TestSourcesAuthorize(t *testing.T) {
	ctx := context.Background()
	owner := &scheduledwork.SessionRef{Name: "coord-1", UID: "uid-1"}
	for name, c := range map[string]struct {
		w        *v1alpha1.ScheduledWork
		objs     []client.Object
		funcs    *interceptor.Funcs
		recorded func(t *testing.T) string
		want     Authority
		readErr  bool
	}{
		"human owner": {w: unitItem("human/dev-env-system/tom", "human/dev-env-system/tom", "ops-audit"), objs: []client.Object{templatesCM(sourcesTemplates)},
			want: Authority{Basis: BasisHuman}},
		"human owner, template admits only approval": {w: unitItem("human/dev-env-system/tom", "human/dev-env-system/tom", "approved-only"), objs: []client.Object{templatesCM(sourcesTemplates)},
			want: Authority{Reason: ReasonNoAuthority + ": the template does not admit human work"}},
		"human owner, no ConfigMap": {w: unitItem("human/dev-env-system/tom", "human/dev-env-system/tom", "ops-audit"),
			want: Authority{Basis: BasisHuman}},
		"live session owner": {w: unitItem(unitOwner, unitOwner, "ops-audit"), objs: append(liveOwnerObjects(), templatesCM(sourcesTemplates)),
			want: Authority{Basis: BasisSession, Owner: owner}},
		"session owner not found": {w: unitItem(unitOwner, unitOwner, "ops-audit"), objs: []client.Object{templatesCM(sourcesTemplates)},
			want: Authority{Reason: ReasonNoAuthority + ": " + authority.ReasonOwnerNotFound}},
		"session owner without a pod": {w: unitItem(unitOwner, unitOwner, "ops-audit"), objs: []client.Object{liveOwnerObjects()[0], templatesCM(sourcesTemplates)},
			want: Authority{Unproven: true, Reason: authority.ReasonOwnerUnproven + ": booting"}},
		"session read failure": {w: unitItem(unitOwner, unitOwner, "ops-audit"), objs: append(liveOwnerObjects(), templatesCM(sourcesTemplates)),
			funcs: failGet(&v1alpha1.AgentSession{}), readErr: true},
		"pod read failure": {w: unitItem(unitOwner, unitOwner, "ops-audit"), objs: append(liveOwnerObjects(), templatesCM(sourcesTemplates)),
			funcs: failGet(&corev1.Pod{}), readErr: true},
		"templates read failure": {w: unitItem("human/dev-env-system/tom", "human/dev-env-system/tom", "ops-audit"), objs: []client.Object{templatesCM(sourcesTemplates)},
			funcs: failGet(&corev1.ConfigMap{}), readErr: true},
		"approval, no recorded digest (until S5)": {w: approve(unitItem("client/dev/dev-env", "client/dev/dev-env", "approved-only")), objs: []client.Object{templatesCM(sourcesTemplates)},
			want: Authority{Reason: ReasonNoAuthority + ": " + authority.ReasonClientNeedsApproval + "; " + authority.ReasonApprovalNoTemplate}},
		"approval, recorded digest matches": {w: approve(unitItem("client/dev/dev-env", "client/dev/dev-env", "approved-only")), objs: []client.Object{templatesCM(sourcesTemplates)},
			recorded: func(t *testing.T) string { return digestOf(t, "approved-only") }, want: Authority{Basis: BasisApproved}},
		"approval under another template": {w: approve(unitItem("client/dev/dev-env", "client/dev/dev-env", "approved-only")), objs: []client.Object{templatesCM(sourcesTemplates)},
			recorded: func(t *testing.T) string { return digestOf(t, "ops-audit") },
			want:     Authority{Reason: ReasonNoAuthority + ": " + authority.ReasonClientNeedsApproval + "; " + authority.ReasonApprovalTemplate}},
		"approval, no ConfigMap": {w: approve(unitItem("client/dev/dev-env", "client/dev/dev-env", "approved-only")),
			recorded: func(t *testing.T) string { return digestOf(t, "approved-only") },
			want:     Authority{Reason: ReasonNoAuthority + ": " + authority.ReasonClientNeedsApproval + "; " + authority.ReasonApprovalTemplate}},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := sources(t, c.funcs, c.objs...)
			if c.recorded != nil {
				d := c.recorded(t)
				s.recordedDigest = func(*v1alpha1.ScheduledWork) string { return d }
			}
			got, err := s.Authorize(ctx, c.w, unitNow)
			if c.readErr {
				if err == nil {
					t.Fatalf("Authorize = %+v, want a read error, tried again", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Basis != c.want.Basis || got.Unproven != c.want.Unproven || got.Reason != c.want.Reason ||
				(got.Owner == nil) != (c.want.Owner == nil) || (got.Owner != nil && *got.Owner != *c.want.Owner) {
				t.Errorf("Authorize = %+v (owner %v), want %+v (owner %v)", got, got.Owner, c.want, c.want.Owner)
			}
		})
	}
}

// A template edited between the decision and the build is never built from:
// Resolve reports a failed read, so the executor decides again.
func TestSourcesTemplateChangedAfterTheDecision(t *testing.T) {
	ctx := context.Background()
	s, c := sources(t, nil, templatesCM(sourcesTemplates))
	w := unitItem("human/dev-env-system/tom", "human/dev-env-system/tom", "ops-audit")
	if a, err := s.Authorize(ctx, w, unitNow); err != nil || a.Basis != BasisHuman {
		t.Fatalf("Authorize = %+v, %v", a, err)
	}
	if _, err := s.Resolve(ctx, "ops-audit"); err != nil {
		t.Fatalf("Resolve under the decided template = %v", err)
	}
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Namespace: unitItems, Name: authority.TemplatesConfigMap}, &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data[authority.TemplatesKey] = strings.Replace(sourcesTemplates, "maxDuration: 40m", "maxDuration: 4h", 1)
	if err := c.Update(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	_, err := s.Resolve(ctx, "ops-audit")
	var refusal *Refusal
	if !errors.Is(err, errTemplateChanged) || errors.As(err, &refusal) {
		t.Fatalf("Resolve after the edit = %v, want errTemplateChanged (a failed read, not a refusal)", err)
	}
	if _, err := s.Authorize(ctx, w, unitNow); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Resolve(ctx, "ops-audit"); err != nil || got.MaxDuration != 4*time.Hour {
		t.Fatalf("Resolve after deciding again = %+v, %v", got, err)
	}
	// A template that appears after a decision made without one is not built
	// from either.
	s2, c2 := sources(t, nil)
	if _, err := s2.Authorize(ctx, w, unitNow); err != nil {
		t.Fatal(err)
	}
	if err := c2.Create(ctx, templatesCM(sourcesTemplates)); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Resolve(ctx, "ops-audit"); !errors.Is(err, errTemplateChanged) {
		t.Fatalf("Resolve of a template that appeared after the decision = %v", err)
	}
}
