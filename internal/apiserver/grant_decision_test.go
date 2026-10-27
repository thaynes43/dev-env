package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/grantdecision"
)

func TestGrantDecisionRequiresHumanAndExactBinding(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("session-one", "full", 0)
	var pod corev1.Pod
	key := types.NamespacedName{Namespace: sessionNS, Name: "session-one"}
	if err := f.c.Get(context.Background(), key, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Labels[v1alpha1.LabelSession] = "session-one"
	if err := f.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	if err := f.c.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	created := f.request(tok, egressReq(), http.StatusCreated)
	g := f.grant(created.Name)
	g.UID = "grant-one"
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	path := apiv1.GrantPath(created.Name) + "/decision"
	if w := f.do(http.MethodGet, path, tokHuman, nil); w.Code != http.StatusForbidden {
		t.Fatalf("default must be disabled: %d", w.Code)
	}
	f.srv.GrantDecisionEnabled = true
	for _, untrusted := range []string{tok, tokClient, tokStranger} {
		if w := f.do(http.MethodGet, path, untrusted, nil); w.Code == http.StatusOK {
			t.Fatal("untrusted caller could inspect decision")
		}
	}
	w := f.do(http.MethodGet, path, tokHuman, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("human summary: %d %s", w.Code, w.Body.String())
	}
	summary := decode[grantDecisionSummary](t, w)
	req := grantDecisionRequest{Binding: summary.Binding, ResourceVersion: summary.ResourceVersion, Decision: "approve"}
	if w := f.do(http.MethodPost, path, tok, req); w.Code == http.StatusAccepted {
		t.Fatal("requester self-approved")
	}
	stale := req
	stale.Binding.PodUID = "replaced-pod"
	if w := f.do(http.MethodPost, path, tokHuman, stale); w.Code != http.StatusConflict {
		t.Fatalf("stale pod: %d", w.Code)
	}
	if w := f.do(http.MethodPost, path, tokHuman, req); w.Code != http.StatusAccepted {
		t.Fatalf("human handoff: %d %s", w.Code, w.Body.String())
	}
	g = f.grant(created.Name)
	if g.Annotations[grantdecision.HandoffAnnotation] == "" || g.Status.Phase == v1alpha1.GrantActive {
		t.Fatal("operator handoff was missing or approved directly")
	}
	if w := f.do(http.MethodPost, path, tokHuman, req); w.Code != http.StatusConflict {
		t.Fatalf("replay: %d", w.Code)
	}
}

func TestGrantDecisionCoordinatorIsLimitedToDirectChild(t *testing.T) {
	f := coordinatorFixture(t)
	f.srv.GrantDecisionEnabled = true
	tok := f.sessionPod("child-one", "full", 0)
	session := f.session("child-one")
	session.Spec.Parent = coordinatorSA
	if err := f.c.Update(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	key := types.NamespacedName{Namespace: sessionNS, Name: "child-one"}
	if err := f.c.Get(context.Background(), key, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Labels[v1alpha1.LabelSession] = session.Name
	if err := f.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	if err := f.c.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	created := f.request(tok, egressReq(), http.StatusCreated)
	g := f.grant(created.Name)
	g.UID = "grant-one"
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	path := apiv1.GrantPath(created.Name) + "/decision"
	w := f.do(http.MethodGet, path, tokCoordinator, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("coordinator summary: %d %s", w.Code, w.Body.String())
	}
	summary := decode[grantDecisionSummary](t, w)
	req := grantDecisionRequest{Binding: summary.Binding, ResourceVersion: summary.ResourceVersion, Decision: "approve", RelayRef: "question/1-answer/1"}
	if w := f.do(http.MethodPost, path, tokCoordinator, req); w.Code != http.StatusAccepted {
		t.Fatalf("coordinator handoff: %d", w.Code)
	}
	g = f.grant(created.Name)
	var h grantdecision.Handoff
	if err := json.Unmarshal([]byte(g.Annotations[grantdecision.HandoffAnnotation]), &h); err != nil || h.Principal != "coordinator/"+coordinatorSA {
		t.Fatal("coordinator provenance was not retained")
	}
	if g.Status.Phase == v1alpha1.GrantActive {
		t.Fatal("operator activated the request")
	}
}

func TestParentSessionRelayRequiresDirectChildAndNeverSelf(t *testing.T) {
	f := newFixture(t)
	f.srv.GrantDecisionEnabled = true
	parentToken := f.sessionPod("parent-one", "full", 0)
	childToken := f.sessionPod("child-one", "full", 1)
	for _, name := range []string{"parent-one", "child-one"} {
		var pod corev1.Pod
		if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
			t.Fatal(err)
		}
		pod.Labels[v1alpha1.LabelSession] = name
		if err := f.c.Update(context.Background(), &pod); err != nil {
			t.Fatal(err)
		}
		pod.Status.Phase = corev1.PodRunning
		if err := f.c.Status().Update(context.Background(), &pod); err != nil {
			t.Fatal(err)
		}
	}
	child := f.session("child-one")
	child.Spec.Parent = "parent-one"
	if err := f.c.Update(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	created := f.request(childToken, egressReq(), http.StatusCreated)
	g := f.grant(created.Name)
	g.UID = "grant-one"
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	path := apiv1.GrantPath(created.Name) + "/decision"
	if w := f.do(http.MethodGet, path, childToken, nil); w.Code != http.StatusForbidden {
		t.Fatalf("self decision: %d", w.Code)
	}
	identity := f.auth[parentToken]
	stale := identity
	stale.PodUID = "replaced-parent-pod"
	f.auth[parentToken] = stale
	if w := f.do(http.MethodGet, path, parentToken, nil); w.Code != http.StatusForbidden {
		t.Fatalf("stale parent pod: %d", w.Code)
	}
	f.auth[parentToken] = identity
	w := f.do(http.MethodGet, path, parentToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("parent summary: %d %s", w.Code, w.Body.String())
	}
	summary := decode[grantDecisionSummary](t, w)
	req := grantDecisionRequest{Binding: summary.Binding, ResourceVersion: summary.ResourceVersion, Decision: "approve", RelayRef: "question/one-answer/one"}
	if w := f.do(http.MethodPost, path, parentToken, req); w.Code != http.StatusAccepted {
		t.Fatalf("parent handoff: %d %s", w.Code, w.Body.String())
	}
	g = f.grant(created.Name)
	var h grantdecision.Handoff
	if err := json.Unmarshal([]byte(g.Annotations[grantdecision.HandoffAnnotation]), &h); err != nil {
		t.Fatal(err)
	}
	parent := f.session("parent-one")
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: parent.Name}, &pod); err != nil {
		t.Fatal(err)
	}
	want := "session/" + parent.Name + "/" + string(parent.UID) + "/" + string(pod.UID)
	if h.Principal != want || h.RelayRef != req.RelayRef || g.Status.Phase == v1alpha1.GrantActive {
		t.Fatal("relay identity or status incorrect")
	}
}
