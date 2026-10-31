package apiserver

import (
	"context"
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestDesignatedHostReadsBeforeOutOfTreeDecision(t *testing.T) {
	f, _, g := delegateFixture(t)
	f.reparent("requester", humanSA)
	reachRunning(t, f, "requester")
	if w := f.do(http.MethodGet, apiv1.GrantPath(g.Name), tokCoordinator, nil); w.Code != http.StatusForbidden {
		t.Fatal("default host read crossed tree", w.Code)
	}
	f.srv.GrantPolicyDelegates = []string{coordinatorSA}
	if w := f.do(http.MethodGet, apiv1.GrantPath(g.Name), tokCoordinator, nil); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tokCoordinator, decisionFor(g, true)); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestDesignatedHostReadRequiresLiveCoverage(t *testing.T) {
	for _, test := range []string{"policy absent", "uncovered", "wrong actor"} {
		t.Run(test, func(t *testing.T) {
			f, _, g := delegateFixture(t)
			f.reparent("requester", humanSA)
			reachRunning(t, f, "requester")
			f.srv.GrantPolicyDelegates = []string{coordinatorSA}
			if test == "wrong actor" {
				id := f.auth[tokCoordinator]
				id.PodUID = "replaced"
				f.auth[tokCoordinator] = id
			} else {
				var list v1alpha1.GrantPolicyList
				if err := f.c.List(context.Background(), &list); err != nil {
					t.Fatal(err)
				}
				for i := range list.Items {
					p := &list.Items[i]
					if test == "policy absent" {
						if err := f.c.Delete(context.Background(), p); err != nil {
							t.Fatal(err)
						}
					} else {
						p.Spec.Kube.Namespaces = []string{"other"}
						if err := f.c.Update(context.Background(), p); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if w := f.do(http.MethodGet, apiv1.GrantPath(g.Name), tokCoordinator, nil); w.Code != http.StatusForbidden {
				t.Fatal("unqualified read", w.Code, w.Body.String())
			}
			if _, ok := f.decisionAnnotation(g.Name); ok {
				t.Fatal("read consumed decision")
			}
			var stored v1alpha1.AccessGrant
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: g.Name}, &stored); err != nil {
				t.Fatal(err)
			}
			if stored.Status.ApprovedBy != "" {
				t.Fatal("read changed approval")
			}
		})
	}
}

func TestDesignatedHostRetainsOwnChildReadWithoutPolicy(t *testing.T) {
	for _, mode := range []string{"policy absent", "uncovered", "stopped requester"} {
		t.Run(mode, func(t *testing.T) {
			f, _, g := delegateFixture(t)
			reachRunning(t, f, "requester")
			f.srv.GrantPolicyDelegates = []string{coordinatorSA}
			var list v1alpha1.GrantPolicyList
			if err := f.c.List(context.Background(), &list); err != nil {
				t.Fatal(err)
			}
			for i := range list.Items {
				if mode == "uncovered" {
					list.Items[i].Spec.Kube.Namespaces = []string{"other"}
					if err := f.c.Update(context.Background(), &list.Items[i]); err != nil {
						t.Fatal(err)
					}
				} else if err := f.c.Delete(context.Background(), &list.Items[i]); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "stopped requester" {
				var requester v1alpha1.AgentSession
				if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "requester"}, &requester); err != nil {
					t.Fatal(err)
				}
				requester.Status.Phase = v1alpha1.PhaseSuspended
				if err := f.c.Update(context.Background(), &requester); err != nil {
					t.Fatal(err)
				}
			}
			if w := f.do(http.MethodGet, apiv1.GrantPath(g.Name), tokCoordinator, nil); w.Code != http.StatusOK {
				t.Fatal("existing own-child read changed", w.Code, w.Body.String())
			}
			if _, ok := f.decisionAnnotation(g.Name); ok {
				t.Fatal("read consumed decision")
			}
		})
	}
}
