package broker

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestPolicyDelegateSubset(t *testing.T) {
	for _, raw := range []string{"x/y", "a/b,a/b", "bad", "a/b,x/y"} {
		if _, err := ParseGrantPolicyDelegates(raw, []string{"a/b"}); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if got, err := ParseGrantPolicyDelegates("", nil); err != nil || got != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	if got, err := ParseGrantPolicyDelegates(" b/c, a/b ", []string{"a/b", "b/c"}); err != nil || !reflect.DeepEqual(got, []string{"a/b", "b/c"}) {
		t.Fatalf("subset: %v %v", got, err)
	}
}

func policyReachCase(t *testing.T) (*dgFixture, MaintenanceDecision) {
	t.Helper()
	f := dgCase(t)
	ctx := context.Background()
	var s v1alpha1.AgentSession
	key := types.NamespacedName{Namespace: sessionNS, Name: "req"}
	if err := f.c.Get(ctx, key, &s); err != nil {
		t.Fatal(err)
	}
	s.Spec.Parent = "dev-env-system/dev-env-human"
	s.Status.Phase = v1alpha1.PhaseRunning
	if err := f.c.Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
	var p v1alpha1.GrantPolicy
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: systemNS, Name: dgPolicy}, &p); err != nil {
		t.Fatal(err)
	}
	p.UID = "policy-reviewed-uid"
	if err := f.c.Update(ctx, &p); err != nil {
		t.Fatal(err)
	}
	f.b.GrantPolicyDelegates = []string{dgDelegate}
	f.mustStep(2)
	d := f.decision(true)
	var err error
	d.Reach, err = NewDelegateReach(ctx, f.c, sessionNS, systemNS, f.grant(), d.Delegate, d.By, f.b.GrantPolicyDelegates)
	if err != nil {
		t.Fatal(err)
	}
	if d.Reach.Mode != "policy" || d.Reach.PolicyUID != string(p.UID) || d.Reach.RequesterUID != string(s.UID) || d.Reach.RequesterPodUID != "req-pod" {
		t.Fatalf("binding: %+v", d.Reach)
	}
	return f, d
}

func TestPolicyReachOutsideTreeRequiresSubset(t *testing.T) {
	f, d := policyReachCase(t)
	if _, err := NewDelegateReach(context.Background(), f.c, sessionNS, systemNS, f.grant(), d.Delegate, d.By, nil); !errors.Is(err, ErrRefused) {
		t.Fatalf("ordinary delegate gained reach: %v", err)
	}
	f.record(d)
	f.mustStep(3)
	if g := f.grant(); g.Status.Phase != v1alpha1.GrantActive || g.Status.DelegateReach == nil || *g.Status.DelegateReach != *d.Reach {
		t.Fatalf("Active binding: %+v", g.Status)
	}
}

func TestPolicyReachChangesRefuseApplyAndRevokeActive(t *testing.T) {
	changes := map[string]func(*testing.T, *dgFixture){
		"reach list removed":    func(t *testing.T, f *dgFixture) { f.b.GrantPolicyDelegates = nil },
		"delegate list removed": func(t *testing.T, f *dgFixture) { f.b.GrantDelegates = nil },
		"policy removed": func(t *testing.T, f *dgFixture) {
			p := &v1alpha1.GrantPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: dgPolicy}}
			if err := f.c.Delete(context.Background(), p); err != nil {
				t.Fatal(err)
			}
		},
		"policy replaced": func(t *testing.T, f *dgFixture) {
			editReachPolicy(t, f, func(p *v1alpha1.GrantPolicy) { p.UID = "replacement" })
		},
		"policy narrowed": func(t *testing.T, f *dgFixture) {
			editReachPolicy(t, f, func(p *v1alpha1.GrantPolicy) { p.Spec.Kube.Namespaces = nil })
		},
		"policy different effective scope": func(t *testing.T, f *dgFixture) {
			editReachPolicy(t, f, func(p *v1alpha1.GrantPolicy) { p.Spec.MaxTTL.Duration = 2 * time.Hour })
		},
		"requester replaced": func(t *testing.T, f *dgFixture) {
			var s v1alpha1.AgentSession
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "req"}, &s); err != nil {
				t.Fatal(err)
			}
			s.UID = "replaced"
			if err := f.c.Update(context.Background(), &s); err != nil {
				t.Fatal(err)
			}
		},
		"requester pod replaced": func(t *testing.T, f *dgFixture) {
			var p corev1.Pod
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "req"}, &p); err != nil {
				t.Fatal(err)
			}
			p.UID = "replacement"
			if err := f.c.Update(context.Background(), &p); err != nil {
				t.Fatal(err)
			}
		},
		"actor pod replaced": func(t *testing.T, f *dgFixture) {
			var p corev1.Pod
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: "host"}, &p); err != nil {
				t.Fatal(err)
			}
			p.UID = "replacement"
			if err := f.c.Update(context.Background(), &p); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range changes {
		for _, active := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/apply", true: "/Active"}[active], func(t *testing.T) {
				f, d := policyReachCase(t)
				f.record(d)
				if active {
					f.mustStep(3)
					if f.grant().Status.Phase != v1alpha1.GrantActive {
						t.Fatal("not Active before mutation")
					}
				}
				change(t, f)
				f.mustStep(3)
				g := f.grant()
				if g.Status.Phase == v1alpha1.GrantActive || (!active && g.Status.ApprovedBy != "") {
					t.Fatalf("retained authority: %+v", g.Status)
				}
				if active {
					var sa corev1.ServiceAccount
					if err := f.c.Get(context.Background(), f.key, &sa); err == nil {
						t.Fatal("revoked per-grant identity remains")
					}
				}
			})
		}
	}
}
func editReachPolicy(t *testing.T, f *dgFixture, edit func(*v1alpha1.GrantPolicy)) {
	t.Helper()
	var p v1alpha1.GrantPolicy
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: dgPolicy}, &p); err != nil {
		t.Fatal(err)
	}
	edit(&p)
	if err := f.c.Update(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyReachTransientReadPreservesPending(t *testing.T) {
	f, d := policyReachCase(t)
	f.record(d)
	before := f.grant().Annotations[MaintenanceDecisionAnnotation]
	outage := errors.New("synthetic API unavailable")
	f.b.APIReader = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*v1alpha1.GrantPolicy); ok {
			return outage
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	if _, err := f.step(1); !errors.Is(err, outage) {
		t.Fatalf("not retryable: %v", err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantPending || g.Annotations[MaintenanceDecisionAnnotation] != before || g.Status.ApprovedBy != "" {
		t.Fatalf("read failure changed decision: %+v", g)
	}
	f.b.APIReader = f.c
	f.mustStep(3)
	if f.grant().Status.Phase != v1alpha1.GrantActive {
		t.Fatal("retry did not activate")
	}
}

func TestPolicyReachAmbiguousAncestryFailsClosed(t *testing.T) {
	for _, parent := range []string{"req", "missing"} {
		t.Run(parent, func(t *testing.T) {
			f, d := policyReachCase(t)
			var s v1alpha1.AgentSession
			_ = f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "req"}, &s)
			s.Spec.Parent = parent
			if err := f.c.Update(context.Background(), &s); err != nil {
				t.Fatal(err)
			}
			if _, err := NewDelegateReach(context.Background(), f.c, sessionNS, systemNS, f.grant(), d.Delegate, d.By, f.b.GrantPolicyDelegates); !errors.Is(err, ErrRefused) {
				t.Fatalf("ancestry accepted: %v", err)
			}
		})
	}
}

func TestPolicyDesignationRemovalRevokesInTreeRecord(t *testing.T) {
	f, d := policyReachCase(t)
	var s v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "req"}, &s); err != nil {
		t.Fatal(err)
	}
	s.Spec.Parent = dgDelegate
	if err := f.c.Update(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	var err error
	d.Reach, err = NewDelegateReach(context.Background(), f.c, sessionNS, systemNS, f.grant(), d.Delegate, d.By, f.b.GrantPolicyDelegates)
	if err != nil || d.Reach.Mode != "tree" || !d.Reach.Designated {
		t.Fatalf("in-tree snapshot: %+v %v", d.Reach, err)
	}
	f.record(d)
	f.mustStep(3)
	if f.grant().Status.Phase != v1alpha1.GrantActive {
		t.Fatal("not Active")
	}
	f.b.GrantPolicyDelegates = nil
	f.mustStep(2)
	if f.grant().Status.Phase != v1alpha1.GrantReleased {
		t.Fatal("lost designation retained in-tree lease")
	}
}

func TestReservedReachCannotBecomeOrdinaryAuthority(t *testing.T) {
	f, d := policyReachCase(t)
	d.Reach.AuthorityKind = "reserved"
	d.Reach.ReservedCase = "synthetic-case"
	d.Reach.BoundsRevision = "synthetic-v1"
	d.Reach.BoundsDigest = d.Reach.PolicyDigest
	if err := CheckDelegateReach(context.Background(), f.c, sessionNS, systemNS, f.grant(), d, f.b.GrantPolicyDelegates); !errors.Is(err, ErrRefused) {
		t.Fatalf("ordinary checker accepted reserved authority: %v", err)
	}
	if _, err := NewReservedDelegateReach(context.Background(), f.c, sessionNS, f.grant(), d.Delegate, d.By, f.b.GrantPolicyDelegates, ReservedReachAuthority{}); !errors.Is(err, ErrRefused) {
		t.Fatalf("empty reserved authority accepted: %v", err)
	}
}

func TestPolicyReachDenialRetainsStatusBinding(t *testing.T) {
	f, d := policyReachCase(t)
	d.Approve = false
	f.record(d)
	f.mustStep(1)
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DelegateReach == nil || *g.Status.DelegateReach != *d.Reach {
		t.Fatalf("denial lost reviewed reach: %+v", g.Status)
	}
}

func TestPolicyReachNoncanonicalOrReplacedAncestryRefused(t *testing.T) {
	for _, test := range []string{"noncanonical depth", "ancestor newer than child"} {
		t.Run(test, func(t *testing.T) {
			f, d := policyReachCase(t)
			var s v1alpha1.AgentSession
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "req"}, &s); err != nil {
				t.Fatal(err)
			}
			if test == "noncanonical depth" {
				s.Labels[v1alpha1.LabelDepth] = "00"
			} else {
				s.Labels[v1alpha1.LabelDepth] = "1"
				s.Spec.Parent = "replacement-parent"
				s.CreationTimestamp = metav1.NewTime(f.clk.Now().Add(-time.Hour))
				parent := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "replacement-parent", UID: "new-parent-uid", CreationTimestamp: metav1.NewTime(f.clk.Now()), Labels: map[string]string{v1alpha1.LabelDepth: "0"}}, Spec: v1alpha1.AgentSessionSpec{Parent: dgDelegate}}
				if err := f.c.Create(context.Background(), parent); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.c.Update(context.Background(), &s); err != nil {
				t.Fatal(err)
			}
			if _, err := NewDelegateReach(context.Background(), f.c, sessionNS, systemNS, f.grant(), d.Delegate, d.By, f.b.GrantPolicyDelegates); !errors.Is(err, ErrRefused) {
				t.Fatalf("ambiguous ancestry authorized: %v", err)
			}
		})
	}
}

func TestPolicyReachForbiddenReadRetainsPending(t *testing.T) {
	f, d := policyReachCase(t)
	f.record(d)
	before := f.grant().Annotations[MaintenanceDecisionAnnotation]
	f.b.APIReader = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*v1alpha1.GrantPolicy); ok {
			return apierrors.NewForbidden(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "grantpolicies"}, key.Name, errors.New("synthetic read denied"))
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	if _, err := f.step(1); err == nil || errors.Is(err, ErrRefused) {
		t.Fatalf("unobservable policy became destructive refusal: %v", err)
	}
	if g := f.grant(); g.Status.Phase != v1alpha1.GrantPending || g.Annotations[MaintenanceDecisionAnnotation] != before {
		t.Fatal("read denial cleared or applied decision")
	}
}
