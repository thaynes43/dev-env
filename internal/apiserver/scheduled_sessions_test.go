package apiserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
)

// The fixtures' occurrence and item.
var swAt = time.Date(2030, 6, 3, 14, 0, 0, 0, time.UTC)

const (
	swUID  = types.UID("0123456789ab-cdef-0123-4567-89abcdef0123")
	swName = "sw-0123456789ab-203006031400"
)

func swDispatch() scheduledwork.Dispatch {
	return scheduledwork.Dispatch{ItemName: "audit", ItemUID: swUID, At: swAt, StartDeadline: swAt.Add(15 * time.Minute)}
}

func swRequest() apiv1.CreateSessionRequest {
	r := task()
	r.Profile, r.Limits = "dev", &apiv1.Limits{Timeout: "40m"}
	return r
}

func wantRefused(t *testing.T, err error, contains string) {
	t.Helper()
	var refused *scheduledwork.RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err %v (%T), want a *scheduledwork.RefusedError", err, err)
	}
	if !strings.Contains(refused.Reason, contains) {
		t.Fatalf("refusal %q does not say %q", refused.Reason, contains)
	}
}

func (f *fixture) sessionCount() int {
	f.t.Helper()
	var list v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &list, client.InNamespace(sessionNS)); err != nil {
		f.t.Fatal(err)
	}
	return len(list.Items)
}

// The fixed-name entry creates the derived name with the finalizer, label and
// annotations in the one Create, as the fixed client at depth 0, and a repeat
// adopts the same session rather than making a second.
func TestCreateScheduledSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if name, err := scheduledwork.SessionName(string(swUID), swAt); err != nil || name != swName {
		t.Fatalf("derived name %q, %v", name, err)
	}
	name, adopted, err := f.srv.CreateScheduledSession(ctx, swRequest(), swDispatch())
	if err != nil || adopted || name != swName {
		t.Fatalf("create: %q adopted %v err %v", name, adopted, err)
	}
	s := f.session(name)
	if !controllerutil.ContainsFinalizer(s, v1alpha1.ScheduledWorkFinalizer) || s.Labels[v1alpha1.LabelScheduledWork] != string(swUID) ||
		s.Annotations[v1alpha1.AnnotationScheduledWorkName] != "audit" ||
		s.Annotations[v1alpha1.AnnotationScheduledWorkOccurrence] != "1906725600" ||
		s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] != "2030-06-03T14:15:00Z" {
		t.Errorf("metadata: finalizers %v labels %v annotations %v", s.Finalizers, s.Labels, s.Annotations)
	}
	if s.Spec.Parent != ScheduledWorkParent || s.Labels[v1alpha1.LabelDepth] != "0" || s.Spec.Profile != "dev" || s.Labels[v1alpha1.LabelIdempotencyKey] != "" {
		t.Errorf("spec %+v labels %v", s.Spec, s.Labels)
	}
	again, adopted, err := f.srv.CreateScheduledSession(ctx, swRequest(), swDispatch())
	if err != nil || !adopted || again != name || f.sessionCount() != 1 {
		t.Errorf("repeat: %q adopted %v err %v, %d sessions", again, adopted, err, f.sessionCount())
	}
}

// A session of the derived name that another occurrence or item made is a
// collision, never adopted, whether the read before the create finds it or
// the Create's AlreadyExists does.
func TestCreateScheduledSessionCollision(t *testing.T) {
	foreign := func(name string, mutate func(*v1alpha1.AgentSession)) *v1alpha1.AgentSession {
		s := &v1alpha1.AgentSession{
			ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name, Finalizers: []string{v1alpha1.ScheduledWorkFinalizer},
				Labels:      map[string]string{v1alpha1.LabelScheduledWork: string(swUID)},
				Annotations: map[string]string{v1alpha1.AnnotationScheduledWorkName: "audit", v1alpha1.AnnotationScheduledWorkOccurrence: "1906725600"}},
			Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: humanSA},
		}
		mutate(s)
		return s
	}
	const name = swName
	for _, c := range []struct {
		what   string
		mutate func(*v1alpha1.AgentSession)
	}{
		{"another item's UID", func(s *v1alpha1.AgentSession) { s.Labels[v1alpha1.LabelScheduledWork] = "0123456789ab-other" }},
		{"another item's name", func(s *v1alpha1.AgentSession) { s.Annotations[v1alpha1.AnnotationScheduledWorkName] = "other" }},
		{"another occurrence", func(s *v1alpha1.AgentSession) {
			s.Annotations[v1alpha1.AnnotationScheduledWorkOccurrence] = "1906729200"
		}},
		{"no label at all", func(s *v1alpha1.AgentSession) { s.Labels = nil }},
		{"no finalizer", func(s *v1alpha1.AgentSession) { s.Finalizers = nil }},
	} {
		t.Run("read first: "+c.what, func(t *testing.T) {
			f := newFixture(t, foreign(name, c.mutate))
			_, _, err := f.srv.CreateScheduledSession(context.Background(), swRequest(), swDispatch())
			if !errors.Is(err, scheduledwork.ErrCollision) {
				t.Fatalf("err %v, want ErrCollision", err)
			}
		})
	}
	// The other session lands between the read and the Create.
	for _, c := range []struct {
		what    string
		mutate  func(*v1alpha1.AgentSession)
		adopted bool
	}{
		{"a collision", func(s *v1alpha1.AgentSession) { s.Labels[v1alpha1.LabelScheduledWork] = "0123456789ab-other" }, false},
		{"another leader's create", func(*v1alpha1.AgentSession) {}, true},
	} {
		t.Run("at the create: "+c.what, func(t *testing.T) {
			f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Name == name {
					if err := cl.Create(ctx, foreign(name, c.mutate)); err != nil {
						return err
					}
				}
				return cl.Create(ctx, obj, opts...)
			}})
			got, adopted, err := f.srv.CreateScheduledSession(context.Background(), swRequest(), swDispatch())
			if c.adopted {
				if err != nil || !adopted || got != name {
					t.Fatalf("%q adopted %v err %v, want the other leader's session adopted", got, adopted, err)
				}
				return
			}
			if !errors.Is(err, scheduledwork.ErrCollision) {
				t.Fatalf("err %v, want ErrCollision", err)
			}
		})
	}
}

// The entry runs the create's own checks and agentd's: each refusal is a
// *scheduledwork.RefusedError and creates nothing.
func TestCreateScheduledSessionRunsTheCreateChecks(t *testing.T) {
	for _, c := range []struct {
		name string
		req  func(*apiv1.CreateSessionRequest)
		d    func(*scheduledwork.Dispatch)
		says string
	}{
		{"newSession: an unknown profile", func(r *apiv1.CreateSessionRequest) { r.Profile = "no-such-profile" }, nil, "profile"},
		{"newSession: no model", func(r *apiv1.CreateSessionRequest) { r.Model = "" }, nil, "model"},
		{"newSession: tool pools", func(r *apiv1.CreateSessionRequest) { r.Tools = []string{"blender"} }, nil, "tool pools"},
		{"agentd: a codex remote session keeps no launch record", func(r *apiv1.CreateSessionRequest) {
			r.Agent, r.Mode, r.Model, r.Effort, r.Prompt, r.Limits = "codex", "remote", "gpt-6-astra", "", "", nil
		}, nil, "first launch record"},
		{"an idempotency key", func(r *apiv1.CreateSessionRequest) { r.IdempotencyKey = "k" }, nil, "derived name is its key"},
		{"a restore", func(r *apiv1.CreateSessionRequest) { r.Restore = "x" }, nil, "derived name is its key"},
		{"a project", func(r *apiv1.CreateSessionRequest) { r.Project = "dev-env" }, nil, "derived name is its key"},
		{"coordinator instructions", func(r *apiv1.CreateSessionRequest) { r.CoordinatorInstructions = true }, nil, "derived name is its key"},
		{"a deadline not after the occurrence", nil, func(d *scheduledwork.Dispatch) { d.StartDeadline = d.At }, "start deadline"},
		{"a UID that derives no name", nil, func(d *scheduledwork.Dispatch) { d.ItemUID = "not-hex" }, "12 hex digits"},
		{"no item name", nil, func(d *scheduledwork.Dispatch) { d.ItemName = "" }, "name is empty"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			req, d := swRequest(), swDispatch()
			if c.req != nil {
				c.req(&req)
			}
			if c.d != nil {
				c.d(&d)
			}
			_, _, err := f.srv.CreateScheduledSession(context.Background(), req, d)
			wantRefused(t, err, c.says)
			if n := f.sessionCount(); n != 0 {
				t.Errorf("%d sessions created", n)
			}
		})
	}
}

// On the live-owner basis the session is created as the owner's child, as
// if the owner's own pod token created it: its parent and depth, the
// profile it inherits, and the owner's depth, child cap, retained-executor
// and task-budget refusals. An existing session of the derived name is
// adopted before any of them, even when it is the child that fills the cap.
func TestCreateScheduledSessionAsTheOwnersChild(t *testing.T) {
	ctx := context.Background()
	owned := func(f *fixture, name string) scheduledwork.Dispatch {
		d := swDispatch()
		d.Owner = &scheduledwork.SessionRef{Name: name, UID: f.session(name).UID}
		return d
	}
	childReq := func() apiv1.CreateSessionRequest { r := swRequest(); r.Profile = ""; return r }

	t.Run("a child", func(t *testing.T) {
		f := newFixture(t)
		f.sessionPod("owner", "ops", 1)
		name, _, err := f.srv.CreateScheduledSession(ctx, childReq(), owned(f, "owner"))
		if err != nil {
			t.Fatal(err)
		}
		s := f.session(name)
		if s.Spec.Parent != "owner" || s.Labels[v1alpha1.LabelDepth] != "2" || s.Spec.Profile != "ops" {
			t.Errorf("child: parent %q depth %q profile %q", s.Spec.Parent, s.Labels[v1alpha1.LabelDepth], s.Spec.Profile)
		}
	})
	t.Run("another profile than the owner's", func(t *testing.T) {
		f := newFixture(t)
		f.sessionPod("owner", "ops", 0)
		_, _, err := f.srv.CreateScheduledSession(ctx, swRequest(), owned(f, "owner"))
		wantRefused(t, err, "parent's profile")
	})
	t.Run("past the depth limit", func(t *testing.T) {
		f := newFixture(t)
		f.sessionPod("owner", "dev", 2)
		_, _, err := f.srv.CreateScheduledSession(ctx, childReq(), owned(f, "owner"))
		wantRefused(t, err, "two levels is the limit")
	})
	t.Run("at the child cap, and adoption first", func(t *testing.T) {
		f := newFixture(t)
		f.sessionPod("owner", "dev", 0)
		for i := range maxRunningChildrenPerAgent {
			s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "child-" + itoa(i)},
				Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: "owner"}}
			if err := f.c.Create(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		_, _, err := f.srv.CreateScheduledSession(ctx, childReq(), owned(f, "owner"))
		wantRefused(t, err, "unfinished claude children")
		// The occurrence's own session, made before the cap filled, is
		// adopted, not refused.
		mine := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: swName, Finalizers: []string{v1alpha1.ScheduledWorkFinalizer},
			Labels:      map[string]string{v1alpha1.LabelScheduledWork: string(swUID)},
			Annotations: map[string]string{v1alpha1.AnnotationScheduledWorkName: "audit", v1alpha1.AnnotationScheduledWorkOccurrence: "1906725600"}},
			Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: "owner"}}
		if err := f.c.Create(ctx, mine); err != nil {
			t.Fatal(err)
		}
		if name, adopted, err := f.srv.CreateScheduledSession(ctx, childReq(), owned(f, "owner")); err != nil || !adopted || name != mine.Name {
			t.Errorf("%q adopted %v err %v", name, adopted, err)
		}
	})
	for _, c := range []struct {
		name   string
		mutate func(*v1alpha1.AgentSession)
		says   string
	}{
		{"a private pilot", func(s *v1alpha1.AgentSession) { s.Annotations = map[string]string{projectcanary.PilotAnnotation: "x"} }, "cannot create another executor"},
		{"a task budget", func(s *v1alpha1.AgentSession) { s.Annotations = map[string]string{TaskBudgetUIDAnnotation: "task-1"} }, "task budget"},
		{"a managed canary", func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{} }, "cannot create another executor"},
		{"suspended", func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended }, "not live"},
	} {
		t.Run("the owner is "+c.name, func(t *testing.T) {
			f := newFixture(t)
			f.sessionPod("owner", "dev", 0)
			s := f.session("owner")
			c.mutate(s)
			if err := f.c.Update(ctx, s); err != nil {
				t.Fatal(err)
			}
			_, _, err := f.srv.CreateScheduledSession(ctx, childReq(), owned(f, "owner"))
			wantRefused(t, err, c.says)
		})
	}
	t.Run("the owner is being deleted", func(t *testing.T) {
		f := newFixture(t)
		f.sessionPod("owner", "dev", 0)
		d := owned(f, "owner")
		if err := f.c.Delete(ctx, f.session("owner")); err != nil {
			t.Fatal(err)
		}
		_, _, err := f.srv.CreateScheduledSession(ctx, childReq(), d)
		wantRefused(t, err, "not live")
	})
	t.Run("the owner has no pod", func(t *testing.T) {
		f := newFixture(t)
		f.sessionPod("owner", "dev", 0)
		d := owned(f, "owner")
		d.Owner.Name = "gone"
		_, _, err := f.srv.CreateScheduledSession(ctx, childReq(), d)
		wantRefused(t, err, "has no pod")
	})
	t.Run("the owner was recreated under its name", func(t *testing.T) {
		f := newFixture(t)
		f.sessionPod("owner", "dev", 0)
		d := owned(f, "owner")
		d.Owner.UID = "a-different-uid"
		_, _, err := f.srv.CreateScheduledSession(ctx, childReq(), d)
		wantRefused(t, err, "does not speak for the owner session")
	})
}

// A failed read or write is not a refusal: the executor retries it inside the
// occurrence's grace, and nothing is known to be created.
func TestCreateScheduledSessionTransientErrors(t *testing.T) {
	unavailable := apierrors.NewServiceUnavailable("injected")
	t.Run("the read before the create", func(t *testing.T) {
		f := newFixtureWith(t, interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*v1alpha1.AgentSession); ok {
				return unavailable
			}
			return cl.Get(ctx, key, obj, opts...)
		}})
		_, _, err := f.srv.CreateScheduledSession(context.Background(), swRequest(), swDispatch())
		var refused *scheduledwork.RefusedError
		if err == nil || errors.As(err, &refused) || errors.Is(err, scheduledwork.ErrCollision) {
			t.Fatalf("err %v, want a plain failed read", err)
		}
	})
	t.Run("the create", func(t *testing.T) {
		f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.AgentSession); ok {
				return apierrors.NewTimeoutError("injected", 1)
			}
			return cl.Create(ctx, obj, opts...)
		}})
		_, _, err := f.srv.CreateScheduledSession(context.Background(), swRequest(), swDispatch())
		var refused *scheduledwork.RefusedError
		if err == nil || errors.As(err, &refused) {
			t.Fatalf("err %v, want a plain failed write", err)
		}
	})
	t.Run("a refused create is a refusal", func(t *testing.T) {
		f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.AgentSession); ok {
				return apierrors.NewInvalid(schema.GroupKind{Group: v1alpha1.GroupVersion.Group, Kind: "AgentSession"}, swName, nil)
			}
			return cl.Create(ctx, obj, opts...)
		}})
		_, _, err := f.srv.CreateScheduledSession(context.Background(), swRequest(), swDispatch())
		wantRefused(t, err, "")
	})
}

// Past the start deadline, by the API's clock just before the Create, the
// entry creates nothing: a session that late could not launch its agent, and
// the releaser frees a derived name only after the deadline plus the margin.
// An existing session of the occurrence is still adopted.
func TestCreateScheduledSessionPastTheStartDeadline(t *testing.T) {
	f := newFixture(t)
	d := swDispatch()
	d.At, d.StartDeadline = f.now.Add(-20*time.Minute), f.now.Add(-time.Second)
	_, _, err := f.srv.CreateScheduledSession(context.Background(), swRequest(), d)
	if !errors.Is(err, scheduledwork.ErrPastStartDeadline) || f.sessionCount() != 0 {
		t.Fatalf("err %v, %d sessions; want ErrPastStartDeadline and none", err, f.sessionCount())
	}
	d.StartDeadline = f.now.Add(time.Minute)
	name, adopted, err := f.srv.CreateScheduledSession(context.Background(), swRequest(), d)
	if err != nil || adopted {
		t.Fatalf("inside the deadline: %q %v %v", name, adopted, err)
	}
	d.StartDeadline = f.now.Add(-time.Second)
	if again, adopted, err := f.srv.CreateScheduledSession(context.Background(), swRequest(), d); err != nil || !adopted || again != name {
		t.Errorf("an existing session past the deadline: %q adopted %v err %v", again, adopted, err)
	}
}
