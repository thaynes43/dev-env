package executor

// End to end per authority basis (DESIGN-054 S3b2): the executor with the
// production Sources, over a real kube-apiserver, a templates ConfigMap in
// the fixture's item namespace, and the API's real fixed-name entry. Only the
// clock is a fake; nothing here reaches a real cluster.

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
)

// e2eTemplates is the end-to-end templates document.
const e2eTemplates = `
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
    admits: [human]
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
  owner-child:
    agent: claude
    model: claude-opus-5-5
    effort: xhigh
    size: M
    profile: ops
    repos: [haynes-ops]
    modes: [headless]
    maxDuration: 40m
    admits: [session]
  dev-child:
    agent: claude
    model: claude-opus-5-5
    effort: xhigh
    size: M
    profile: dev
    repos: [haynes-ops]
    modes: [headless]
    maxDuration: 40m
    admits: [session]
  headless-only:
    agent: claude
    model: claude-opus-5-5
    effort: xhigh
    size: M
    profile: dev
    repos: [haynes-ops]
    modes: [headless]
    maxDuration: 40m
    admits: [human]
`

// withSources gives the fixture the production Sources over the API server,
// with the templates document doc in its item namespace; an empty doc
// creates no ConfigMap.
func (f *fixture) withSources(doc string) (*Sources, func() *Executor) {
	f.t.Helper()
	if doc != "" {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: f.items, Name: authority.TemplatesConfigMap},
			Data: map[string]string{authority.TemplatesKey: doc}}
		if err := cluster(f.t).Create(context.Background(), cm); err != nil {
			f.t.Fatal(err)
		}
	}
	src := &Sources{Reader: cluster(f.t), SessionNamespace: f.sessions, TemplatesNamespace: f.items}
	return src, func() *Executor {
		e := f.executor()
		e.Authorizer, e.Templates = src, src
		return e
	}
}

// due runs the planning pass and the due pass.
func (f *fixture) due(ex func() *Executor) {
	f.at(ex(), day0.Add(-time.Minute))
	f.at(ex(), day0)
}

func e2eDigest(t *testing.T, name string) string {
	t.Helper()
	ts, err := authority.ParseTemplates(map[string]string{authority.TemplatesKey: e2eTemplates})
	if err != nil {
		t.Fatal(err)
	}
	return authority.TemplateDigest(ts[name])
}

// A human owner who wrote the spec dispatches on the template's fixed terms.
func TestSourcesHumanOwnerDispatches(t *testing.T) {
	f := newFixture(t)
	_, ex := f.withSources(e2eTemplates)
	key := f.item("audit", spec())
	f.due(ex)
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
	s := f.session(o.Session)
	if s.Spec.Model != "claude-opus-5-5" || s.Spec.Effort != "xhigh" || s.Spec.Profile != "dev" || s.Spec.Size != v1alpha1.SizeM ||
		s.Spec.Limits == nil || s.Spec.Limits.Timeout == nil || s.Spec.Limits.Timeout.Duration != 40*time.Minute || s.Spec.Parent != apiserver.ScheduledWorkParent {
		t.Errorf("session %+v (limits %+v)", s.Spec, s.Spec.Limits)
	}
}

// A human approval of the exact spec dispatches a client-owned item only
// while its recorded template digest (here set through the test hook) is the
// template's: with none recorded it is void.
func TestSourcesApprovedItem(t *testing.T) {
	for _, c := range []struct {
		name     string
		recorded string
		state    string
		reason   string
	}{
		{"recorded digest matches", "approved-only", v1alpha1.OccurrenceDispatched, ""},
		{"no recorded digest", "", v1alpha1.OccurrenceParked,
			ReasonNoAuthority + ": " + authority.ReasonClientNeedsApproval + "; " + authority.ReasonApprovalNoTemplate},
		{"recorded under another template", "ops-audit", v1alpha1.OccurrenceParked,
			ReasonNoAuthority + ": " + authority.ReasonClientNeedsApproval + "; " + authority.ReasonApprovalTemplate},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			src, ex := f.withSources(e2eTemplates)
			if c.recorded != "" {
				d := e2eDigest(t, c.recorded)
				src.recordedDigest = func(*v1alpha1.ScheduledWork) string { return d }
			}
			sp := spec()
			sp.Owner, sp.Editor, sp.Work.Template = "client/dev/dev-env", "client/dev/dev-env", "approved-only"
			key := f.item("approved", sp)
			w := f.get(key)
			w.Status.Approval = &v1alpha1.ScheduledWorkApproval{By: "human/dev-env-system/tom", At: metav1.NewTime(day0.Add(-2 * time.Hour)), SpecHash: scheduledwork.SpecHash(w.Spec)}
			if err := cluster(t).Status().Update(context.Background(), w); err != nil {
				t.Fatal(err)
			}
			f.due(ex)
			o := f.wantOcc(key, day0, c.state, c.reason)
			if c.state == v1alpha1.OccurrenceDispatched {
				if s := f.session(o.Session); s.Spec.Model != "claude-sonnet-5-5" || s.Spec.Parent != apiserver.ScheduledWorkParent {
					t.Errorf("session %+v", s.Spec)
				}
			} else if calls, _, _ := f.create.counts(); calls != 0 {
				t.Errorf("a void approval reached the create: %d calls", calls)
			}
		})
	}
}

// An approval stored the way the API's approve route stores it (by, at,
// specHash and templateDigest, with no test hook) is honoured while its
// digest is the template's, and void with another template's digest or with
// none.
func TestSourcesReadTheStoredApprovalDigest(t *testing.T) {
	for _, c := range []struct {
		name   string
		stored string
		state  string
		reason string
	}{
		{"stored digest matches", "approved-only", v1alpha1.OccurrenceDispatched, ""},
		{"stored under another template", "ops-audit", v1alpha1.OccurrenceParked,
			ReasonNoAuthority + ": " + authority.ReasonClientNeedsApproval + "; " + authority.ReasonApprovalTemplate},
		{"no stored digest", "", v1alpha1.OccurrenceParked,
			ReasonNoAuthority + ": " + authority.ReasonClientNeedsApproval + "; " + authority.ReasonApprovalNoTemplate},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			src, ex := f.withSources(e2eTemplates)
			if src.recordedDigest != nil {
				t.Fatal("the fixture set the recorded-digest hook; this case reads the stored approval")
			}
			sp := spec()
			sp.Owner, sp.Editor, sp.Work.Template = "client/dev/dev-env", "client/dev/dev-env", "approved-only"
			key := f.item("approved", sp)
			w := f.get(key)
			approval := &v1alpha1.ScheduledWorkApproval{By: "human/dev-env-system/tom", At: metav1.NewTime(day0.Add(-2 * time.Hour)), SpecHash: scheduledwork.SpecHash(w.Spec)}
			if c.stored != "" {
				approval.TemplateDigest = e2eDigest(t, c.stored)
			}
			w.Status.Approval = approval
			if err := cluster(t).Status().Update(context.Background(), w); err != nil {
				t.Fatal(err)
			}
			if got := f.get(key).Status.Approval; got == nil || got.TemplateDigest != approval.TemplateDigest {
				t.Fatalf("the API server stored approval %+v", got)
			}
			f.due(ex)
			o := f.wantOcc(key, day0, c.state, c.reason)
			if c.state != v1alpha1.OccurrenceDispatched {
				if calls, _, _ := f.create.counts(); calls != 0 {
					t.Errorf("a void approval reached the create: %d calls", calls)
				}
			} else if s := f.session(o.Session); s.Spec.Parent != apiserver.ScheduledWorkParent {
				t.Errorf("session %+v", s.Spec)
			}
		})
	}
}

// liveOwner gives an owner session the status of a live one at the fixture's
// due time: Running, a busy agent after a ready boot, a heartbeat at hb.
func (f *fixture) liveOwner(s *v1alpha1.AgentSession, hb time.Time) {
	f.t.Helper()
	cur := f.session(s.Name)
	beat := metav1.NewTime(hb)
	cur.Status.Phase = v1alpha1.PhaseRunning
	cur.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentBusy, Boot: protocol.BootReady, LastHeartbeat: &beat}
	if err := cluster(f.t).Status().Update(context.Background(), cur); err != nil {
		f.t.Fatal(err)
	}
}

func ownedSpec(owner *v1alpha1.AgentSession, template string) v1alpha1.ScheduledWorkSpec {
	sp := spec()
	ref := "session/" + owner.Name + "/" + string(owner.UID)
	sp.Owner, sp.Editor, sp.Work.Template = ref, ref, template
	return sp
}

// A live session owner who wrote the spec dispatches as its own child.
func TestSourcesLiveSessionOwnerDispatchesItsChild(t *testing.T) {
	f := newFixture(t)
	_, ex := f.withSources(e2eTemplates)
	owner := f.ownerSession("coordinator-1", "1")
	f.liveOwner(owner, day0.Add(-30*time.Second))
	key := f.item("owned", ownedSpec(owner, "owner-child"))
	f.due(ex)
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
	if s := f.session(o.Session); s.Spec.Parent != owner.Name || s.Labels[v1alpha1.LabelDepth] != "2" || s.Spec.Profile != "ops" {
		t.Errorf("child: parent %q depth %q profile %q", s.Spec.Parent, s.Labels[v1alpha1.LabelDepth], s.Spec.Profile)
	}
}

// An owner session whose liveness is unproven (a stale heartbeat) keeps the
// occurrence Pending through its grace, then it parks; nothing is created.
func TestSourcesUnprovenOwnerWaitsThenParks(t *testing.T) {
	f := newFixture(t)
	_, ex := f.withSources(e2eTemplates)
	owner := f.ownerSession("coordinator-1", "1")
	f.liveOwner(owner, day0.Add(-time.Hour))
	key := f.item("owned", ownedSpec(owner, "owner-child"))
	f.due(ex)
	if o := f.wantOcc(key, day0, v1alpha1.OccurrencePending, ReasonOwnerUnproven); o.ClaimedAt != nil {
		t.Fatalf("claimed while the owner was unproven: %+v", o)
	}
	f.at(ex(), day0.Add(grace-time.Minute))
	f.wantOcc(key, day0, v1alpha1.OccurrencePending, ReasonOwnerUnproven)
	f.at(ex(), day0.Add(grace+time.Second))
	f.wantOcc(key, day0, v1alpha1.OccurrenceParked, ReasonOwnerUnproven)
	if calls, _, _ := f.create.counts(); calls != 0 {
		t.Errorf("calls %d", calls)
	}
}

// A template that does not serve the item's mode, or does not admit its
// basis, parks it with the reason and creates nothing.
func TestSourcesTemplateRefusalsPark(t *testing.T) {
	for _, c := range []struct {
		name, template, mode, reason string
	}{
		{"mode", "headless-only", v1alpha1.ScheduledWorkInteractive, "template headless-only does not serve interactive work"},
		{"admits", "approved-only", v1alpha1.ScheduledWorkHeadless, ReasonNoAuthority + ": the template does not admit human work"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			_, ex := f.withSources(e2eTemplates)
			sp := spec()
			sp.Mode, sp.Work.Template = c.mode, c.template
			key := f.item("refused", sp)
			f.due(ex)
			f.wantOcc(key, day0, v1alpha1.OccurrenceParked, c.reason)
			if calls, _, _ := f.create.counts(); calls != 0 || len(f.sessionsOf()) != 0 {
				t.Errorf("calls %d sessions %d", calls, len(f.sessionsOf()))
			}
		})
	}
}

// No templates ConfigMap, or an invalid one, dispatches nothing: the
// occurrence parks on the template's refusal.
func TestSourcesNoOrInvalidTemplatesDispatchNothing(t *testing.T) {
	for _, c := range []struct {
		name, doc, reason string
	}{
		{"absent", "", "template ops-audit: no dispatch templates: ConfigMap %s/dev-env-scheduled-work does not exist"},
		{"invalid", strings.Replace(e2eTemplates, "model: claude-opus-5-5\n    effort: xhigh\n    size: M\n    profile: dev\n    repos: [haynes-ops]\n    modes: [headless, interactive]",
			"model: opus\n    effort: xhigh\n    size: M\n    profile: dev\n    repos: [haynes-ops]\n    modes: [headless, interactive]", 1),
			`template ops-audit: invalid dispatch templates: templates.yaml: template "ops-audit": model: "opus" is an alias`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			_, ex := f.withSources(c.doc)
			key := f.item("audit", spec())
			f.due(ex)
			o := f.wantOcc(key, day0, v1alpha1.OccurrenceParked, "")
			want := strings.Replace(c.reason, "%s", f.items, 1)
			if !strings.HasPrefix(o.Reason, want) {
				t.Errorf("reason %q, want %q", o.Reason, want)
			}
			if calls, _, _ := f.create.counts(); calls != 0 || len(f.sessionsOf()) != 0 {
				t.Errorf("calls %d sessions %d", calls, len(f.sessionsOf()))
			}
		})
	}
}

// editingResolver edits the templates ConfigMap once, right before the first
// Resolve (revalidation check 3, after check 2's decision), then reads.
type editingResolver struct {
	src  *Sources
	once func()
}

func (r *editingResolver) Resolve(ctx context.Context, name string) (Template, error) {
	if r.once != nil {
		r.once()
		r.once = nil
	}
	return r.src.Resolve(ctx, name)
}

// A template edited between the decision (check 2) and the build (check 3)
// is not built from: the digests differ, so the occurrence stays Claimed and
// is tried again; the next pass decides under the edited template and builds
// from it.
func TestSourcesTemplateEditedBetweenDecisionAndBuildRetries(t *testing.T) {
	f := newFixture(t)
	src, _ := f.withSources(e2eTemplates)
	edited := false
	r := &editingResolver{src: src, once: func() {
		ctx := context.Background()
		var cm corev1.ConfigMap
		if err := cluster(t).Get(ctx, types.NamespacedName{Namespace: f.items, Name: authority.TemplatesConfigMap}, &cm); err != nil {
			t.Fatal(err)
		}
		cm.Data[authority.TemplatesKey] = strings.Replace(e2eTemplates, "maxDuration: 40m\n    admits: [human]", "maxDuration: 3h\n    admits: [human]", 1)
		if err := cluster(t).Update(ctx, &cm); err != nil {
			t.Fatal(err)
		}
		edited = true
	}}
	ex := func() *Executor {
		e := f.executor()
		e.Authorizer, e.Templates = src, r
		return e
	}
	key := f.item("audit", spec())
	f.due(ex)
	if o := f.wantOcc(key, day0, v1alpha1.OccurrenceClaimed, ""); !edited || o.Session != "" {
		t.Fatalf("edited %v, occurrence %+v: the build should have been tried again", edited, o)
	}
	if calls, _, _ := f.create.counts(); calls != 0 {
		t.Fatalf("%d creates from a template the decision did not see", calls)
	}
	f.at(ex(), day0.Add(time.Minute))
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
	if s := f.session(o.Session); s.Spec.Limits == nil || s.Spec.Limits.Timeout == nil || s.Spec.Limits.Timeout.Duration != 3*time.Hour {
		t.Errorf("session limits %+v: want the edited template's 3h, decided and built under it", s.Spec.Limits)
	}
}

// A session owner whose template is missing parks at once with the
// template's reason: no wait on the owner, however its liveness reads.
func TestSourcesSessionOwnerWithMissingTemplateParksAtOnce(t *testing.T) {
	f := newFixture(t)
	_, ex := f.withSources(e2eTemplates)
	owner := f.ownerSession("coordinator-1", "1")
	f.liveOwner(owner, day0.Add(-time.Hour))
	key := f.item("owned", ownedSpec(owner, "no-such-template"))
	f.due(ex)
	f.wantOcc(key, day0, v1alpha1.OccurrenceParked, "template no-such-template: no such dispatch template")
	if calls, _, _ := f.create.counts(); calls != 0 {
		t.Errorf("calls %d", calls)
	}
}

// A live session owner on a profile other than the template's is refused at
// the create: its child runs on its own profile. The occurrence parks with
// the API's reason.
func TestSourcesSessionOwnerOnAnotherProfileIsRefused(t *testing.T) {
	f := newFixture(t)
	_, ex := f.withSources(e2eTemplates)
	owner := f.ownerSession("coordinator-1", "1")
	f.liveOwner(owner, day0.Add(-30*time.Second))
	key := f.item("owned", ownedSpec(owner, "dev-child"))
	f.due(ex)
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceParked, "")
	if !strings.Contains(o.Reason, "a session's child runs on its parent's profile ops, not dev") {
		t.Errorf("reason %q, want the profile refusal", o.Reason)
	}
	if len(f.sessionsOf()) != 1 {
		t.Errorf("%d sessions: only the owner should exist", len(f.sessionsOf()))
	}
}

// rawResolver is a TemplateResolver that gives its templates as they are,
// without the digest a real source gives.
type rawResolver map[string]Template

func (r rawResolver) Resolve(_ context.Context, name string) (Template, error) {
	t, ok := r[name]
	if !ok {
		return Template{}, &Refusal{Reason: "no such template"}
	}
	return t, nil
}

// Check 3 builds only when the decision and the template carry the same,
// non-empty digest. An empty digest on either side, both included, can match
// nothing: the occurrence parks with the reason and nothing is created.
func TestCheck3RefusesAnEmptyTemplateDigest(t *testing.T) {
	for _, c := range []struct {
		name      string
		auth      func(f *fixture) Authorizer
		templates func(f *fixture) TemplateResolver
	}{
		{"both empty", func(*fixture) Authorizer { return &authorizer{} }, func(f *fixture) TemplateResolver { return rawResolver(f.tmpl) }},
		{"template empty", func(f *fixture) Authorizer { return f.auth }, func(f *fixture) TemplateResolver { return rawResolver(f.tmpl) }},
		{"decision empty", func(*fixture) Authorizer { return &authorizer{} }, func(f *fixture) TemplateResolver { return f.tmpl }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			ex := func() *Executor {
				e := f.executor()
				e.Authorizer, e.Templates = c.auth(f), c.templates(f)
				return e
			}
			key := f.item("audit", spec())
			f.due(ex)
			f.wantOcc(key, day0, v1alpha1.OccurrenceParked, "template ops-audit: "+ReasonNoTemplateDigest)
			if calls, _, _ := f.create.counts(); calls != 0 || len(f.sessionsOf()) != 0 {
				t.Errorf("calls %d sessions %d", calls, len(f.sessionsOf()))
			}
		})
	}
}
