package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	other.ResultState = "Partial"
	st.UsageObservation = &other
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	gotParent := f.session("parent")
	gotChild := f.session("child")
	if len(gotParent.Status.CapturedUsage.Receipts) != 1 || gotParent.Status.CapturedUsage.Receipts[0].Observation.ResultState != "Final" || gotChild.Status.UsageConflict == nil || gotChild.Status.UsageConflict.Reason != "ConflictingSnapshot" {
		t.Fatal("same-source final replaced the receipt or was not a conflicting snapshot", gotChild.Status.UsageConflict)
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
	gets := 0
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if key.Name == "parent" {
			gets++
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	f.srv.Live = f.c
	if gets != 0 || f.session("parent").Status.CapturedUsage != nil {
		t.Fatal("disabled capture touched the parent", gets)
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
	if !view.Available || !view.Overflow || len(view.Receipts) != v1alpha1.CapturedUsageMaxReceipts {
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
	if body.CapturedUsage == nil || body.CapturedUsage.Available || body.CapturedUsage.Reason != "unavailable" || len(body.CapturedUsage.Receipts) != 0 {
		t.Fatalf("absent ledger %+v", body.CapturedUsage)
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
	parent.UID = types.UID(strings.Repeat("p", 128))
	now := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	added := 0
	for i := 0; i < v1alpha1.CapturedUsageMaxReceipts; i++ {
		before := 0
		if parent.Status.CapturedUsage != nil {
			before = len(parent.Status.CapturedUsage.Receipts)
		}
		obs := fatObservation(t, i)
		name := fmt.Sprintf("%s%03d", strings.Repeat("n", 250), i)
		if len(name) != 253 {
			t.Fatalf("child name %d", len(name))
		}
		ok := appendCapturedUsage(parent, name, obs, now.Add(time.Duration(i)*time.Second))
		if parent.Status.CapturedUsage != nil && parent.Status.CapturedUsage.Overflow {
			if !ok || len(parent.Status.CapturedUsage.Receipts) != before {
				t.Fatalf("overflow write changed rows at %d", i)
			}
			break
		}
		if !ok || len(parent.Status.CapturedUsage.Receipts) != before+1 || parent.Status.CapturedUsage.Overflow {
			t.Fatalf("row %d was not accepted", i)
		}
		added++
	}
	if added == 0 || added == v1alpha1.CapturedUsageMaxReceipts || !parent.Status.CapturedUsage.Overflow {
		t.Fatalf("fat rows did not stop at a readable byte boundary: %d", added)
	}
	for _, row := range parent.Status.CapturedUsage.Receipts {
		if len(row.ChildName) == 0 || len(row.ChildName) > 253 {
			t.Fatalf("child name length %d", len(row.ChildName))
		}
	}
	view := capturedUsageView(parent, &caller{kind: kindHuman}, true)
	stored, err := json.Marshal(parent.Status.CapturedUsage)
	wire, werr := json.Marshal(view)
	if err != nil || werr != nil || !view.Available || len(view.Receipts) != added || len(stored) > v1alpha1.CapturedUsageMaxBytes || len(wire) > apiv1.CapturedUsageMaxBytes {
		t.Fatalf("boundary view available=%v rows=%d stored=%d wire=%d err=%v %v", view.Available, len(view.Receipts), len(stored), len(wire), err, werr)
	}
	snap := parent.Status.CapturedUsage.DeepCopy()
	if appendCapturedUsage(parent, strings.Repeat("b", 253), fatObservation(t, added+1), now.Add(time.Hour)) || !apiequality.Semantic.DeepEqual(snap, parent.Status.CapturedUsage) {
		t.Fatal("a row after overflow changed the ledger")
	}
}

func fatObservation(t *testing.T, i int) *v1alpha1.UsageObservationStatus {
	t.Helper()
	n := protocol.MaxUsageTokens
	unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
	reported := protocol.TokenObservation{State: protocol.UsageReported, Amount: &n}
	cost := protocol.CostObservation{State: protocol.UsageReported, Amount: strings.Repeat("9", 64), Unit: "USD", Kind: protocol.UsageCLIEstimate}
	src := protocol.UsageSource{
		SessionUID: fmt.Sprintf("c%02d%s", i, strings.Repeat("s", 125))[:128], PodUID: strings.Repeat("d", 128),
		Provider: protocol.AgentClaude, RequestedModel: "claude-" + strings.Repeat("m", 249), AttemptID: fmt.Sprintf("%064x", i+1),
	}
	u := protocol.UnknownUsage()
	u.Source, u.ID, u.ObservedAt, u.ResultState = src, protocol.UsageObservationID(src), time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC), "Final"
	u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens = reported, unknown, unknown, unknown
	u.Cost, u.ActualModelState, u.BillingBasis = cost, protocol.UsageProviderReported, protocol.UsageSubscription
	for m := 0; m < 8; m++ {
		id := fmt.Sprintf("m%02d%s", m, strings.Repeat("a", 253))
		u.ServedModels = append(u.ServedModels, protocol.ModelUsage{
			Model: id[:256], InputTokens: reported, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: cost,
		})
	}
	if err := u.Validate(); err != nil {
		t.Fatal(err)
	}
	return usageObservationStatus(u)
}

func boundCaptureChild(t *testing.T, f *fixture, parent, name, profile string) string {
	t.Helper()
	token := f.sessionPod(name, profile, 0)
	child := f.session(name)
	child.Spec.Parent = parent
	child.Spec.UsageCaptureParentUID = string(f.session(parent).UID)
	if err := f.c.Update(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestUsageCaptureCreateBinding(t *testing.T) {
	post := func(f *fixture, token string, body any) *v1alpha1.AgentSession {
		t.Helper()
		w := f.do(http.MethodPost, apiv1.SessionsPath, token, body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %d %s", w.Code, w.Body.String())
		}
		return f.session(decode[apiv1.Session](t, w).Name)
	}
	f := newFixture(t)
	f.srv.ChildUsageCapture = true
	token := f.sessionPod("cap-parent", "dev", 0)
	parent := f.session("cap-parent")
	if parent.UID == "" {
		t.Fatal("parent UID was not assigned")
	}
	child := post(f, token, task())
	if child.Spec.UsageCaptureParentUID != string(parent.UID) || child.Spec.Parent != "cap-parent" {
		t.Fatalf("fresh parent UID %q parent %q", child.Spec.UsageCaptureParentUID, child.Spec.Parent)
	}
	req := task()
	req.Profile, req.IdempotencyKey = "dev", "cap-replay"
	f.now = f.now.Add(time.Second)
	first := post(f, token, req)
	f.now = f.now.Add(time.Second)
	again := f.do(http.MethodPost, apiv1.SessionsPath, token, req)
	if again.Code != http.StatusOK || decode[apiv1.Session](t, again).Name != first.Name || f.session(first.Name).Spec.UsageCaptureParentUID != string(parent.UID) {
		t.Fatalf("replay %d %s", again.Code, again.Body.String())
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, `{"repo":"haynes-ops","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"p","usageCaptureParentUID":"forged"}`), http.StatusBadRequest, apiv1.CodeBadRequest)

	f = newFixture(t)
	f.srv.ChildUsageCapture = false
	if post(f, f.sessionPod("off-parent", "dev", 0), task()).Spec.UsageCaptureParentUID != "" {
		t.Fatal("flag off stored a parent UID")
	}
	f = newFixture(t)
	f.srv.ChildUsageCapture = true
	if post(f, tokHuman, task()).Spec.UsageCaptureParentUID != "" || post(f, tokClient, task()).Spec.UsageCaptureParentUID != "" {
		t.Fatal("human or client create stored a parent UID")
	}
	if _, err := f.srv.newSession(context.Background(), task(), &caller{kind: kindCoordinator, parent: coordinatorSA}); err == nil {
		t.Fatal("coordinator create was allowed")
	}
	var created v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &created); err != nil {
		t.Fatal(err)
	}
	for i := range created.Items {
		if created.Items[i].Spec.UsageCaptureParentUID != "" {
			t.Fatal("non-session create stored a parent UID", created.Items[i].Name)
		}
	}

	f = newFixture(t)
	f.srv.ChildUsageCapture = true
	token = f.sessionPod("repl-parent", "dev", 0)
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		if key.Name == "repl-parent" {
			obj.(*v1alpha1.AgentSession).UID = "replaced-parent"
		}
		return nil
	}})
	if post(f, token, task()).Spec.UsageCaptureParentUID != "" {
		t.Fatal("replaced parent UID was copied")
	}
	f = newFixture(t)
	f.srv.ChildUsageCapture = true
	token = f.sessionPod("del-parent", "dev", 0)
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		if key.Name == "del-parent" {
			now := metav1.NewTime(f.now)
			obj.(*v1alpha1.AgentSession).DeletionTimestamp = &now
		}
		return nil
	}})
	if post(f, token, task()).Spec.UsageCaptureParentUID != "" {
		t.Fatal("deleting parent UID was copied")
	}

	f, b := newCanaryFixture(t, "claude", interceptor.Funcs{})
	f.srv.ChildUsageCapture = true
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, map[string]string{"canaryID": b.Record.ID}); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	canary := f.session(b.Record.SessionName())
	if canary.Spec.ProjectCanary == nil || canary.Spec.UsageCaptureParentUID != "" {
		t.Fatalf("canary binding changed %+v", canary.Spec)
	}
	f, pilot := pilotAPI(t, interceptor.Funcs{})
	f.srv.ChildUsageCapture = true
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, pilot.Policy.Request(0)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	var pilots v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &pilots); err != nil {
		t.Fatal(err)
	}
	foundPilot := false
	for i := range pilots.Items {
		if pilots.Items[i].Spec.PrivatePilot == nil {
			continue
		}
		foundPilot = true
		if pilots.Items[i].Spec.UsageCaptureParentUID != "" {
			t.Fatal("pilot stored a capture parent")
		}
	}
	if !foundPilot {
		t.Fatal("pilot session was not created")
	}
	f = newFixture(t)
	f.srv.ChildUsageCapture = true
	name, err := f.srv.CreateStandby(context.Background(), apiv1.CreateSessionRequest{Repo: "haynes-ops", Agent: "claude", Mode: "remote", Model: "claude-opus-5-5", Effort: "xhigh", Profile: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	standby := f.session(name)
	if standby.Labels[v1alpha1.LabelStandby] != "true" || standby.Spec.UsageCaptureParentUID != "" || standby.Spec.Parent != StandbyParent {
		t.Fatalf("standby changed %+v %v", standby.Spec, standby.Labels)
	}
}

func TestUsageCaptureValidatedSourceConflictAndRetry(t *testing.T) {
	f := newFixture(t)
	f.srv.ChildUsageCapture = true
	if err := f.c.Create(context.Background(), &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: sessionNS, UID: "parent-uid"}, Spec: v1alpha1.AgentSessionSpec{Repo: "r", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p"}}); err != nil {
		t.Fatal(err)
	}
	token := boundCaptureChild(t, f, "parent", "child", "full")
	partial := fixtureUsage(f, "child", token)
	partial.ResultState = "Partial"
	st := status("child")
	st.UsageObservation = &partial
	failParent := true
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		if failParent && obj.GetName() == "parent" {
			failParent = false
			fresh := f.session("parent")
			fresh.Status.NodeName = "kept"
			if err := f.c.Status().Update(ctx, fresh); err != nil {
				return err
			}
			return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, obj.GetName(), errors.New("conflict"))
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}})
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.session("parent").Status.CapturedUsage != nil || f.session("parent").Status.NodeName != "kept" || f.session("child").Status.UsageObservation == nil || f.session("child").Status.UsageConflict != nil {
		t.Fatal("parent conflict stored a receipt or lost the child observation", f.session("parent").Status.NodeName, f.session("child").Status.UsageConflict)
	}
	finalObs := partial
	finalObs.ResultState = "Final"
	st.UsageObservation = &finalObs
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.session("parent").Status.CapturedUsage != nil || f.session("child").Status.UsageConflict == nil || f.session("child").Status.UsageConflict.Reason != "ConflictingSnapshot" || f.session("child").Status.UsageObservation.ResultState != "Partial" {
		t.Fatal("conflicting final captured the stale partial", f.session("child").Status.UsageConflict)
	}

	f.srv.Client = f.c
	f = newFixture(t)
	f.srv.ChildUsageCapture = true
	if err := f.c.Create(context.Background(), &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: sessionNS, UID: "parent-uid"}, Spec: v1alpha1.AgentSessionSpec{Repo: "r", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p"}}); err != nil {
		t.Fatal(err)
	}
	token = boundCaptureChild(t, f, "parent", "child", "full")
	partial = fixtureUsage(f, "child", token)
	partial.ResultState = "Partial"
	st = status("child")
	st.UsageObservation = &partial
	patches := 0
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		patches++
		if patches == 2 {
			return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, obj.GetName(), errors.New("conflict"))
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}})
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent || f.session("child").Status.UsageObservation != nil || f.session("child").Status.Agent == nil || f.session("parent").Status.CapturedUsage != nil {
		t.Fatal("child status conflict captured an unpersisted observation", w.Code)
	}
	f.srv.Client = f.c
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	got := f.session("parent")
	stored := f.session("child").Status.UsageObservation
	if got.Status.CapturedUsage == nil || len(got.Status.CapturedUsage.Receipts) != 1 || stored == nil || !apiequality.Semantic.DeepEqual(got.Status.CapturedUsage.Receipts[0].Observation, *stored) {
		t.Fatal("identical heartbeat did not copy the accepted observation", got.Status.CapturedUsage)
	}
	row := got.Status.CapturedUsage.Receipts[0].Observation
	if row.InputTokens.Amount == nil || *row.InputTokens.Amount != 0 || row.OutputTokens.State != protocol.UsageUnknown || row.OutputTokens.Amount != nil || row.Cost.Amount != "0" || row.ActualModelState != protocol.UsageUnknown || row.ResultState != "Partial" {
		t.Fatalf("zero and unknown were not preserved: %+v", row)
	}
	changed := partial
	changed.Source.AttemptID = strings.Repeat("b", 64)
	changed.ID = protocol.UsageObservationID(changed.Source)
	st.UsageObservation = &changed
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent || len(f.session("parent").Status.CapturedUsage.Receipts) != 1 || f.session("child").Status.UsageConflict == nil {
		t.Fatal("replaced attempt added a row")
	}
	otherPod := partial
	otherPod.Source.PodUID = "other-pod"
	otherPod.ID = protocol.UsageObservationID(otherPod.Source)
	st.UsageObservation = &otherPod
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent || len(f.session("parent").Status.CapturedUsage.Receipts) != 1 {
		t.Fatal("replaced pod added a row")
	}

	history := f.session("child")
	history.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{SessionUID: string(history.UID), LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "other-executor"}}
	if err := f.c.Status().Update(context.Background(), history); err != nil {
		t.Fatal(err)
	}
	before := len(f.session("parent").Status.CapturedUsage.Receipts)
	st.UsageObservation = &partial
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent || len(f.session("parent").Status.CapturedUsage.Receipts) != before {
		t.Fatal("mismatched private executor captured another row")
	}
}

func TestUsageCaptureDistinctChildAndMatchingExecutor(t *testing.T) {
	f := newFixture(t)
	f.srv.ChildUsageCapture = true
	if err := f.c.Create(context.Background(), &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: sessionNS, UID: "parent-uid"}, Spec: v1alpha1.AgentSessionSpec{Repo: "r", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p"}}); err != nil {
		t.Fatal(err)
	}
	token := boundCaptureChild(t, f, "parent", "reused", "full")
	child := f.session("reused")
	child.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{SessionUID: string(child.UID), LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: f.auth[token].PodUID}}
	if err := f.c.Status().Update(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	u := fixtureUsage(f, "reused", token)
	u.ActualModelState = protocol.UsageProviderReported
	u.ServedModels = []protocol.ModelUsage{{Model: "claude-opus-5-5", InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadInputTokens: u.CacheReadInputTokens, CacheCreationInputTokens: u.CacheCreationInputTokens, Cost: u.Cost}}
	st := status("reused")
	st.UsageObservation = &u
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("reused"), token, st); w.Code != http.StatusNoContent || len(f.session("parent").Status.CapturedUsage.Receipts) != 1 {
		t.Fatal("matching executor was not captured", w.Code, w.Body.String())
	}
	firstUID := string(f.session("reused").UID)
	old := f.session("reused")
	old.Finalizers = nil
	if err := f.c.Update(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "reused"}, &pod); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	token = boundCaptureChild(t, f, "parent", "reused", "full")
	if string(f.session("reused").UID) == firstUID {
		t.Fatal("recreated child kept the old UID")
	}
	u = fixtureUsage(f, "reused", token)
	st = status("reused")
	st.UsageObservation = &u
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("reused"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	rows := f.session("parent").Status.CapturedUsage.Receipts
	if len(rows) != 2 || rows[0].ChildName != "reused" || rows[1].ChildName != "reused" || rows[0].Observation.Source.SessionUID == rows[1].Observation.Source.SessionUID {
		t.Fatalf("same name was not a distinct child: %+v", rows)
	}

	codexTok := boundCaptureChild(t, f, "parent", "codex-child", "full")
	codex := f.session("codex-child")
	codex.Spec.Agent, codex.Spec.Model = v1alpha1.AgentCodex, "gpt-6-luna"
	if err := f.c.Update(context.Background(), codex); err != nil {
		t.Fatal(err)
	}
	cu := protocol.UnknownUsage()
	cu.Source = protocol.UsageSource{SessionUID: string(codex.UID), PodUID: f.auth[codexTok].PodUID, Provider: protocol.AgentCodex, RequestedModel: "gpt-6-luna", AttemptID: strings.Repeat("c", 64)}
	cu.ID, cu.ObservedAt, cu.ResultState = protocol.UsageObservationID(cu.Source), f.now, "Final"
	st = status("codex-child")
	st.UsageObservation = &cu
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("codex-child"), codexTok, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	got := f.session("parent").Status.CapturedUsage.Receipts
	if len(got) != 3 {
		t.Fatalf("rows %d", len(got))
	}
	var sawClaude, sawCodex bool
	for _, row := range got {
		if row.Observation.Source.Provider == protocol.AgentClaude && len(row.Observation.ServedModels) == 1 && row.Observation.Cost.Amount == "0" {
			sawClaude = true
		}
		if row.Observation.Source.Provider == protocol.AgentCodex && row.Observation.Cost.State == protocol.UsageUnknown && row.Observation.Cost.Amount == "" && len(row.Observation.ServedModels) == 0 {
			sawCodex = true
		}
	}
	if !sawClaude || !sawCodex {
		t.Fatal("provider provenance was combined or dropped")
	}
}

func TestUsageCaptureRefusesMismatchedPrivateExecutor(t *testing.T) {
	f := newFixture(t)
	f.srv.ChildUsageCapture = true
	if err := f.c.Create(context.Background(), &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: sessionNS, UID: "parent-uid"}, Spec: v1alpha1.AgentSessionSpec{Repo: "r", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p"}}); err != nil {
		t.Fatal(err)
	}
	token := boundCaptureChild(t, f, "parent", "child", "full")
	partial := fixtureUsage(f, "child", token)
	partial.ResultState = "Partial"
	st := status("child")
	st.UsageObservation = &partial
	failParent := true
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		if failParent && obj.GetName() == "parent" {
			failParent = false
			return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, obj.GetName(), errors.New("conflict"))
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}})
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent || f.session("parent").Status.CapturedUsage != nil || f.session("child").Status.UsageConflict != nil {
		t.Fatal("setup did not leave an uncaptured observation", w.Code)
	}
	f.srv.Client = f.c
	child := f.session("child")
	child.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{SessionUID: string(child.UID), LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "other-executor"}}
	if err := f.c.Status().Update(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("child"), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.session("parent").Status.CapturedUsage != nil || f.session("child").Status.UsageObservation == nil || f.session("child").Status.UsageObservation.ResultState != "Partial" || f.session("child").Status.UsageConflict != nil {
		t.Fatal("mismatched last executor captured the stored partial", f.session("child").Status.UsageConflict)
	}
}
