package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

func TestDelegateDecisionUsesLivePolicyWhenCacheLags(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(fmt.Sprint(approve), func(t *testing.T) {
			f := dgCase(t)
			f.mustStep(2)
			f.record(f.decision(approve))
			before := f.grant().Annotations[MaintenanceDecisionAnnotation]
			live := f.c
			// Separate stores: the informer snapshot has the same grant and
			// identities, but has not observed the covering policy yet.
			objects := []client.Object{f.grant(), &v1alpha1.AgentSession{}, &corev1.Pod{}, delegateHostPod()}
			for _, obj := range objects[1:3] {
				if err := live.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: "req"}, obj); err != nil {
					t.Fatal(err)
				}
			}
			f.c = fake.NewClientBuilder().WithScheme(live.Scheme()).WithStatusSubresource(&v1alpha1.AccessGrant{}).WithObjects(objects...).Build()
			f.b.Client, f.b.APIReader = f.c, live
			f.mustStep(1)
			g := f.grant()
			if g.Annotations[MaintenanceDecisionAnnotation] != before {
				t.Fatal("cache lag erased a live-covered decision")
			}
			if approve {
				wantDelegateApproved(t, g, time.Hour)
			} else if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != dgBy {
				t.Fatalf("live-covered denial did not apply: %+v", g.Status)
			}
		})
	}
}

func TestDelegatePolicyListReadErrorKeepsPendingRecord(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(fmt.Sprint(approve), func(t *testing.T) {
			f := dgCase(t)
			f.mustStep(2)
			f.record(f.decision(approve))
			before := f.grant().Annotations[MaintenanceDecisionAnnotation]
			failure := apierrors.NewServiceUnavailable("policy list unavailable")
			reads := 0
			f.b.APIReader = interceptor.NewClient(f.c, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*v1alpha1.GrantPolicyList); ok {
					reads++
					return failure
				}
				return c.List(ctx, list, opts...)
			}})
			if _, err := f.step(1); !errors.Is(err, failure) || errors.Is(err, ErrRefused) {
				t.Errorf("policy read error was not returned for retry: %v", err)
			}
			g := f.grant()
			wantStillPending(t, g)
			if reads == 0 || g.Annotations[MaintenanceDecisionAnnotation] != before {
				t.Fatalf("live policy failure changed the record (reads=%d)", reads)
			}
		})
	}
}

func TestDelegateFutureDecisionKeepsPendingRecord(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(fmt.Sprint(approve), func(t *testing.T) {
			f := dgCase(t)
			f.mustStep(2)
			f.record(f.decision(approve, func(d *MaintenanceDecision) {
				d.At = d.At.Add(2 * grantattest.MaxClockSkew)
			}))
			before := f.grant().Annotations[MaintenanceDecisionAnnotation]
			recorder := events.NewFakeRecorder(10)
			f.b.Recorder = recorder
			res := f.mustStep(1)
			g := f.grant()
			wantStillPending(t, g)
			if g.Annotations[MaintenanceDecisionAnnotation] != before || res.RequeueAfter <= 0 || res.RequeueAfter > PendingTimeout {
				t.Fatalf("future decision did not wait with its record: %+v", res)
			}
			if len(recorder.Events) != 0 {
				t.Fatal("future decision emitted an event while waiting")
			}
			f.clk.Step(res.RequeueAfter)
			f.mustStep(1)
			g = f.grant()
			if approve {
				wantDelegateApproved(t, g, time.Hour)
			} else if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != dgBy {
				t.Fatalf("denial did not apply after clock caught up: %+v", g.Status)
			}
		})
	}
}

func maintenanceDelegateRetryCase(t *testing.T) *amFixture {
	t.Helper()
	f := amCase(t, func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
		p.Spec.Attested, p.Spec.Delegated = nil, &v1alpha1.DelegatedRequesters{Profiles: []string{"full"}}
		workers := []v1alpha1.MaintenanceIdentity{{Name: "worker", UID: "worker-uid", PodUID: "worker-pod"}}
		g.Spec.Maintenance.Workers, p.Spec.Maintenance.Workers = workers, workers
	})
	childOfDelegateHost(f)
	f.b.GrantDelegates = []string{dgDelegate}
	f.b.EnableAttestedGrants, f.b.EnableAttestedMaintenance = false, false
	f.step(1)
	g := f.grant()
	d := MaintenanceDecision{GrantUID: string(g.UID), Digest: grantattest.ScopeDigest(g), By: dgBy, Approve: true, TTL: g.Spec.TTL.Duration,
		At: f.clk.Now(), Ref: dgRef, Delegate: &DelegateIdentity{Form: 1, PodName: "host", PodUID: "host-pod", HostID: "host-a"}}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	f.annotate(MaintenanceDecisionAnnotation, string(raw))
	return f
}

func TestDelegateMaintenanceIdentityReadErrorsKeepPendingRecord(t *testing.T) {
	for _, target := range []string{"coordinator session", "worker session", "coordinator pod", "worker pod", "node"} {
		for name, failure := range map[string]error{
			"unavailable": apierrors.NewServiceUnavailable("identity read unavailable"),
			"timeout":     &net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded},
			"throttled":   apierrors.NewTooManyRequests("identity read throttled", 1),
		} {
			t.Run(target+"/"+name, func(t *testing.T) {
				f := maintenanceDelegateRetryCase(t)
				before := f.grant().Annotations[MaintenanceDecisionAnnotation]
				recorder := events.NewFakeRecorder(10)
				f.b.Recorder = recorder
				reads := 0
				checkingIdentities := false
				f.b.APIReader = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					// The live named-policy read precedes CheckMaintenanceIdentities
					// in Decide, after the requester's identity/reach checks.
					if _, ok := obj.(*v1alpha1.GrantPolicy); ok && key.Name == amPolicy {
						checkingIdentities = true
					}
					if checkingIdentities && maintenanceRetryRead(target, key, obj) {
						reads++
						return failure
					}
					return c.Get(ctx, key, obj, opts...)
				}})
				_, err := f.b.Reconcile(context.Background(), ctrl.Request{NamespacedName: f.key})
				if !errors.Is(err, failure) || errors.Is(err, ErrRefused) {
					t.Errorf("identity read error was not returned for retry: %v", err)
				}
				g := f.grant()
				wantStillPending(t, g)
				if reads == 0 || g.Annotations[MaintenanceDecisionAnnotation] != before {
					t.Errorf("identity read failure changed the record (reads=%d)", reads)
				}
				for len(recorder.Events) > 0 {
					if event := <-recorder.Events; strings.Contains(event, "Refused") {
						t.Errorf("temporary read failure emitted refusal: %s", event)
					}
				}
				f.b.APIReader = f.c
				f.step(1)
				wantDelegateApproved(t, f.grant(), 10*time.Minute)
			})
		}
	}
}

func maintenanceRetryRead(target string, key client.ObjectKey, obj client.Object) bool {
	switch obj.(type) {
	case *v1alpha1.AgentSession:
		return key.Namespace == sessionNS && ((target == "coordinator session" && key.Name == "coord") || (target == "worker session" && key.Name == "worker"))
	case *corev1.Pod:
		return key.Namespace == sessionNS && ((target == "coordinator pod" && key.Name == "coord") || (target == "worker pod" && key.Name == "worker"))
	case *corev1.Node:
		return target == "node" && key.Name == amNode
	}
	return false
}

func TestDelegateMaintenancePermanentIdentityRefusalClearsPendingRecord(t *testing.T) {
	for _, target := range []string{"worker session", "worker pod", "node"} {
		for _, reason := range []string{"not found", "replaced"} {
			t.Run(target+"/"+reason, func(t *testing.T) {
				f := maintenanceDelegateRetryCase(t)
				recorder := events.NewFakeRecorder(10)
				f.b.Recorder = recorder
				reads := 0
				f.b.APIReader = interceptor.NewClient(f.c.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if !maintenanceRetryRead(target, key, obj) {
						return c.Get(ctx, key, obj, opts...)
					}
					reads++
					if reason == "not found" {
						return apierrors.NewNotFound(schema.GroupResource{Resource: target}, key.Name)
					}
					if err := c.Get(ctx, key, obj, opts...); err != nil {
						return err
					}
					obj.SetUID("replacement-uid")
					return nil
				}})
				f.step(1)
				g := f.grant()
				wantStillPending(t, g)
				if reads == 0 || g.Annotations[MaintenanceDecisionAnnotation] != "" {
					t.Fatalf("permanent identity refusal retained the record (reads=%d)", reads)
				}
				select {
				case event := <-recorder.Events:
					if !strings.Contains(event, "DelegateDecisionRefused") {
						t.Fatalf("wrong event: %s", event)
					}
				default:
					t.Fatal("permanent refusal emitted no audit event")
				}
			})
		}
	}
}
