package apiserver

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"net/http"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestUsageCaptureSeparateFromRollupAndSurvivesChildRemoval(t *testing.T) {
	f := newFixture(t)
	f.srv.ChildUsageCapture = true
	parent := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: sessionNS, UID: "parent-uid"}, Spec: v1alpha1.AgentSessionSpec{Repo: "r", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p"}}
	if err := f.c.Create(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	token := f.sessionPod("child", "full", 0)
	child := f.session("child")
	child.Spec.Parent = "parent"
	child.Spec.UsageCaptureParentUID = "parent-uid"
	if err := f.c.Update(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	f.srv.ChildUsageCapture = false
	u := fixtureUsage(f, "child", token)
	st := status("child")
	st.UsageObservation = &u
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	f.srv.ChildUsageCapture = true
	before := usageRollup(f.session("parent"), []v1alpha1.AgentSession{*f.session("parent"), *f.session("child")})
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	got := f.session("parent")
	if got.Status.CapturedUsage == nil || len(got.Status.CapturedUsage.Receipts) != 1 || got.Status.UsageObservation != nil {
		t.Fatalf("parent ledger %+v", got.Status.CapturedUsage)
	}
	stamp := got.Status.CapturedUsage.Receipts[0].CapturedAt
	afterChild := f.session("child")
	after := usageRollup(f.session("parent"), []v1alpha1.AgentSession{*f.session("parent"), *afterChild})
	if !apiequality.Semantic.DeepEqual(before, after) {
		t.Fatalf("rollup changed\nbefore %+v\nafter %+v", before, after)
	}
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	replay := f.session("parent")
	if len(replay.Status.CapturedUsage.Receipts) != 1 || !replay.Status.CapturedUsage.Receipts[0].CapturedAt.Equal(&stamp) {
		t.Fatal("replay rewrote the receipt")
	}
	other := u
	other.ID = strings.Repeat("c", 64)
	other.ResultState = "Partial"
	st.UsageObservation = &other
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(f.session("parent").Status.CapturedUsage.Receipts) != 1 || f.session("child").Status.UsageConflict == nil {
		t.Fatal("conflict replaced the receipt or skipped the child conflict")
	}
	if err := f.c.Delete(context.Background(), f.session("child")); err != nil {
		t.Fatal(err)
	}
	again := &Server{Client: f.c, Live: f.c, ChildUsageCapture: true, Now: f.srv.Now, Policy: f.srv.Policy, Log: f.srv.Log}
	var kept v1alpha1.AgentSession
	if err := again.Live.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "parent"}, &kept); err != nil || len(kept.Status.CapturedUsage.Receipts) != 1 {
		t.Fatal("receipt did not survive child removal", err)
	}
	if err := f.c.Delete(context.Background(), &kept); err != nil {
		t.Fatal(err)
	}
	if err := again.Live.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "parent"}, &kept); err == nil {
		t.Fatal("parent deletion left a stored receipt")
	}
}

func TestUsageCaptureRefusesUnboundAndReplacedParents(t *testing.T) {
	f := newFixture(t)
	f.srv.ChildUsageCapture = false
	token := f.sessionPod("child", "full", 0)
	child := f.session("child")
	child.Spec.Parent = "parent"
	child.Spec.UsageCaptureParentUID = "parent-uid"
	if err := f.c.Update(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	parent := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: sessionNS, UID: "parent-uid"}, Spec: v1alpha1.AgentSessionSpec{Repo: "r", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p"}}
	if err := f.c.Create(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	u := fixtureUsage(f, "child", token)
	st := status("child")
	st.UsageObservation = &u
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.session("parent").Status.CapturedUsage != nil {
		t.Fatal("disabled capture wrote a parent")
	}
	f.srv.ChildUsageCapture = true
	child = f.session("child")
	child.Spec.UsageCaptureParentUID = "other-uid"
	if err := f.c.Update(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.session("parent").Status.CapturedUsage != nil {
		t.Fatal("replaced parent uid captured")
	}
}

func TestCapturedUsageBoundsAndView(t *testing.T) {
	parent := &v1alpha1.AgentSession{}
	parent.Name, parent.Namespace, parent.UID = "parent", sessionNS, "parent-uid"
	now := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	for i := 0; i < v1alpha1.CapturedUsageMaxReceipts; i++ {
		obs := capturedObservation(id64(1000 + i))
		if !appendCapturedUsage(parent, "child", obs, now) || parent.Status.CapturedUsage.Overflow {
			t.Fatalf("row %d overflowed early", i)
		}
	}
	extra := capturedObservation(strings.Repeat("f", 64))
	if !appendCapturedUsage(parent, "extra", extra, now.Add(time.Minute)) || !parent.Status.CapturedUsage.Overflow || len(parent.Status.CapturedUsage.Receipts) != v1alpha1.CapturedUsageMaxReceipts {
		t.Fatalf("65th row %+v", parent.Status.CapturedUsage)
	}
	view := capturedUsageView(parent, &caller{kind: kindHuman}, true)
	if !view.Available || len(view.Receipts) != v1alpha1.CapturedUsageMaxReceipts {
		t.Fatalf("human view %+v", view)
	}
	if capturedUsageView(parent, &caller{kind: kindCoordinator}, true).Available {
		t.Fatal("coordinator saw receipts")
	}
	other := &v1alpha1.AgentSession{}
	other.UID = "other"
	if capturedUsageView(parent, &caller{kind: kindSession, session: other}, true).Available {
		t.Fatal("other session saw receipts")
	}
	if capturedUsageView(parent, &caller{kind: kindSession, session: parent}, true).Receipts[0].Key == "" {
		t.Fatal("self view missing")
	}
	if capturedUsageView(parent, &caller{kind: kindHuman}, false).Reason != "disabled" {
		t.Fatal("disabled view")
	}
}

func TestCapturedUsageReaderHeader(t *testing.T) {
	f := newFixture(t)
	f.srv.ChildUsageCapture = true
	parent := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: sessionNS, UID: "parent-uid"}, Spec: v1alpha1.AgentSessionSpec{Repo: "r", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p"}}
	if err := f.c.Create(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	plain := f.do(http.MethodGet, apiv1.SessionPath("parent"), tokHuman, nil)
	if strings.Contains(plain.Body.String(), "capturedUsage") {
		t.Fatalf("legacy get included capture: %s", plain.Body.String())
	}
	opt := f.doHeader(http.MethodGet, apiv1.SessionPath("parent"), tokHuman, nil, http.Header{apiv1.UsageCaptureHeader: []string{apiv1.UsageCaptureDirectV1}})
	body := decode[apiv1.Session](t, opt)
	if body.CapturedUsage == nil || !body.CapturedUsage.Available || body.CapturedUsage.Coverage != v1alpha1.CapturedUsageCoverage {
		t.Fatalf("opt-in empty ledger %+v", body.CapturedUsage)
	}
	f.srv.ChildUsageCapture = false
	off := decode[apiv1.Session](t, f.doHeader(http.MethodGet, apiv1.SessionPath("parent"), tokHuman, nil, http.Header{apiv1.UsageCaptureHeader: []string{apiv1.UsageCaptureDirectV1}}))
	if off.CapturedUsage == nil || off.CapturedUsage.Available || off.CapturedUsage.Reason != "disabled" || len(off.CapturedUsage.Receipts) != 0 {
		t.Fatalf("disabled view %+v", off.CapturedUsage)
	}
}

func capturedObservation(sessionUID string) *v1alpha1.UsageObservationStatus {
	n := int64(0)
	unknown := v1alpha1.TokenObservationStatus{State: protocol.UsageUnknown}
	source := protocol.UsageSource{SessionUID: sessionUID, PodUID: "pod-uid", Provider: "claude", RequestedModel: "claude-opus-5-5", AttemptID: strings.Repeat("a", 64)}
	return &v1alpha1.UsageObservationStatus{Version: 1, ID: protocol.UsageObservationID(source), Source: v1alpha1.UsageSourceStatus(source), ObservedAt: metav1.NewTime(time.Unix(1, 0).UTC()), Coverage: "InitialManagedInvocation", CounterSemantics: "InvocationFinalTotal", ResultState: "Final", InputTokens: v1alpha1.TokenObservationStatus{State: protocol.UsageReported, Amount: &n}, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: v1alpha1.CostObservationStatus{State: protocol.UsageReported, Amount: "0", Unit: "USD", Kind: protocol.UsageCLIEstimate}, AggregationScope: "Unknown", ActualModelState: "Unknown", SubscriptionQuotaState: "Unknown", APIBillingState: "Unknown", LocalResourceState: "Unknown"}
}

func id64(i int) string {
	return fmt.Sprintf("%064x", i)
}

func TestCapturedUsageByteBoundary(t *testing.T) {
	parent := &v1alpha1.AgentSession{}
	parent.UID = "parent-uid"
	now := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	first := capturedObservation(strings.Repeat("a", 64))
	if !appendCapturedUsage(parent, "child", first, now) || len(parent.Status.CapturedUsage.Receipts) != 1 {
		t.Fatal("first row")
	}
	second := capturedObservation(strings.Repeat("b", 64))
	if !appendCapturedUsage(parent, strings.Repeat("n", v1alpha1.CapturedUsageMaxBytes), second, now.Add(time.Second)) {
		t.Fatal("overflow was not recorded")
	}
	if !parent.Status.CapturedUsage.Overflow || len(parent.Status.CapturedUsage.Receipts) != 1 {
		t.Fatalf("byte boundary %+v", parent.Status.CapturedUsage)
	}
}
