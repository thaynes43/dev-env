package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
)

func reportedTokens(n int64) protocol.TokenObservation {
	return protocol.TokenObservation{State: protocol.UsageReported, Amount: &n}
}

func estimate(amount string) protocol.CostObservation {
	return protocol.CostObservation{State: protocol.UsageReported, Amount: amount, Unit: "USD", Kind: protocol.UsageCLIEstimate}
}

func servedModel(model string, in int64, cost string) protocol.ModelUsage {
	unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
	m := protocol.ModelUsage{Model: model, InputTokens: reportedTokens(in), OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}}
	if cost != "" {
		m.Cost = estimate(cost)
	}
	return m
}

// providerObservation is a version 2 observation for a Session and Pod UID.
func providerObservation(sessionUID, podUID, provider, model string, in, out int64, cost string, models ...protocol.ModelUsage) protocol.UsageObservation {
	u := protocol.UnknownUsage()
	u.Source = protocol.UsageSource{SessionUID: sessionUID, PodUID: podUID, Provider: provider, RequestedModel: model, AttemptID: strings.Repeat("a", 64)}
	u.ID, u.ObservedAt, u.ResultState, u.BillingBasis = protocol.UsageObservationID(u.Source), time.Date(2026, 10, 6, 17, 0, 0, 0, time.UTC), "Final", protocol.UsageSubscription
	u.InputTokens, u.OutputTokens = reportedTokens(in), reportedTokens(out)
	if cost != "" {
		u.Cost = estimate(cost)
	}
	if len(models) > 0 {
		u.ServedModels, u.ActualModelState = models, protocol.UsageProviderReported
	}
	return u
}

func TestUsageHeartbeatRetainsProviderReportedServedModels(t *testing.T) {
	f := newFixture(t)
	name := "usage-served"
	token := f.sessionPod(name, "full", 0)
	u := providerObservation(string(f.session(name).UID), f.auth[token].PodUID, protocol.AgentClaude, "claude-opus-5-5", 30, 40, "0.75",
		servedModel("claude-haiku-4-5", 10, "0.05"), servedModel("claude-opus-5-5", 20, "0.7"))
	st := status(name)
	st.UsageObservation = &u
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	got := f.session(name)
	if got.Status.UsageConflict != nil || got.Status.UsageObservation == nil || got.Status.UsageObservation.Version != 2 || len(got.Status.UsageObservation.ServedModels) != 2 ||
		got.Status.UsageObservation.ServedModels[1].Cost.Amount != "0.7" || got.Status.UsageObservation.BillingBasis != "Subscription" || got.Status.UsageObservation.Provenance != "ProviderReported" || got.Status.UsageObservation.ActualModelState != "ProviderReported" {
		t.Fatal("served models, provenance or billing basis not retained", got.Status.UsageObservation, got.Status.UsageConflict)
	}
	v := view(got, true)
	if v.UsageObservation == nil || !reflect.DeepEqual(*v.UsageObservation, u) {
		t.Fatalf("API view changed the observation:\n got %+v\nwant %+v", v.UsageObservation, u)
	}
	raw, _ := json.Marshal(v)
	for _, field := range []string{`"usageObservation":{"version":2`, `"servedModels":[{"model":"claude-haiku-4-5","inputTokens":{"state":"Reported","amount":10}`, `"billingBasis":"Subscription"`, `"provenance":"ProviderReported"`} {
		if !strings.Contains(string(raw), field) {
			t.Fatal("wire shape", field, string(raw))
		}
	}
	// A legacy reader still decodes the session and sees no unqualified cost.
	var old struct {
		Name  string       `json:"name"`
		Usage *apiv1.Usage `json:"usage"`
	}
	if json.Unmarshal(raw, &old) != nil || old.Name != name || old.Usage != nil {
		t.Fatal("old reader")
	}
}

func TestUsageHeartbeatRefusesInventedServedModel(t *testing.T) {
	f := newFixture(t)
	name := "usage-invented"
	token := f.sessionPod(name, "full", 0)
	u := providerObservation(string(f.session(name).UID), f.auth[token].PodUID, protocol.AgentClaude, "claude-opus-5-5", 1, 1, "")
	u.ActualModelState = protocol.UsageProviderReported // no served model behind it
	st := status(name)
	st.UsageObservation = &u
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
		t.Fatal("usage refusal blocked liveness", w.Code)
	}
	got := f.session(name)
	if got.Status.UsageObservation != nil || got.Status.UsageConflict == nil || got.Status.UsageConflict.Reason != "InvalidPayload" || got.Status.Agent == nil {
		t.Fatal("invented model accepted", got.Status)
	}
}

type rollupTree struct {
	items []v1alpha1.AgentSession
}

func (r *rollupTree) add(name, parent string, created time.Time, u *protocol.UsageObservation) *v1alpha1.AgentSession {
	s := v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uuid.NewUUID(), CreationTimestamp: metav1.NewTime(created)},
		Spec: v1alpha1.AgentSessionSpec{Agent: "claude", Mode: v1alpha1.ModeTask, Model: "claude-opus-5-5", Parent: parent}}
	if u != nil {
		obs := *u
		obs.Source.SessionUID = string(s.UID)
		obs.Source.Provider, obs.Source.RequestedModel = string(s.Spec.Agent), s.Spec.Model
		obs.ID = protocol.UsageObservationID(obs.Source)
		s.Status.UsageObservation = usageObservationStatus(obs)
	}
	r.items = append(r.items, s)
	return &r.items[len(r.items)-1]
}

func TestUsageRollupCountsEachDescendantOnce(t *testing.T) {
	base := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	tree := &rollupTree{}
	root := tree.add("root", humanSA, base, nil) // an interactive coordinator: no own observation
	root.Spec.Mode = v1alpha1.ModeLocal
	a := providerObservation("", "pod-a", "", "", 10, 20, "0.1", servedModel("claude-opus-5-5", 10, "0.1"))
	b := providerObservation("", "pod-b", "", "", 1, 2, "0.2", servedModel("claude-haiku-4-5", 1, "0.2"))
	g := providerObservation("", "pod-g", "", "", 100, 200, "0.3", servedModel("claude-opus-5-5", 100, "0.3"))
	tree.add("child-a", "root", base.Add(time.Minute), &a)
	tree.add("child-b", "root", base.Add(time.Minute), &b)
	tree.add("grandchild", "child-a", base.Add(2*time.Minute), &g)
	// A Session that names "root" but predates it belongs to an earlier "root".
	tree.add("stale", "root", base.Add(-time.Hour), &a)
	// The same child listed twice is still one invocation.
	items := append(append([]v1alpha1.AgentSession(nil), tree.items...), tree.items[1])
	rootCopy := tree.items[0]

	r := usageRollup(&rootCopy, items)
	if r == nil {
		t.Fatal("no rollup for a Session with descendants")
	}
	if r.Scope != "Descendants" || r.Coverage != "Complete" || r.Sessions != 3 || len(r.Invocations) != 3 || r.Truncated {
		t.Fatal("walk", r.Scope, r.Coverage, r.Sessions, r.Invocations, r.Truncated)
	}
	if *r.InputTokens.Amount != 111 || *r.OutputTokens.Amount != 222 || r.CacheReadInputTokens.State != protocol.UsageUnknown || r.CacheReadInputTokens.Amount != nil {
		t.Fatal("token sums or unknown counters", r.InputTokens, r.OutputTokens, r.CacheReadInputTokens)
	}
	if r.Cost.State != protocol.UsageReported || r.Cost.Amount != "0.6" || r.Cost.Kind != protocol.UsageCLIEstimate || r.BillingBasis != protocol.UsageSubscription {
		t.Fatal("cost is not the exact decimal sum of CLI estimates", r.Cost, r.BillingBasis)
	}
	if r.ObservedModelState != protocol.UsageProviderReported || !reflect.DeepEqual(r.ObservedModels, []string{"claude-haiku-4-5", "claude-opus-5-5"}) {
		t.Fatal("observed models", r.ObservedModelState, r.ObservedModels)
	}
	seen := map[string]bool{}
	for _, k := range r.Invocations {
		if seen[k.SessionUID] || k.ObservationID == "" {
			t.Fatal("an invocation was counted twice or without its key", r.Invocations)
		}
		seen[k.SessionUID] = true
	}
	if seen[string(tree.items[4].UID)] || seen[string(rootCopy.UID)] {
		t.Fatal("stale Session or the root's own usage was counted as a child")
	}
	// The children keep their own observations; nothing is moved onto them.
	if tree.items[1].Status.UsageObservation.InputTokens.Amount == nil || *tree.items[1].Status.UsageObservation.InputTokens.Amount != 10 {
		t.Fatal("child observation changed")
	}
}

func TestUsageRollupUnknownIsNeverZeroOrPartialSum(t *testing.T) {
	base := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	full := providerObservation("", "pod", "", "", 5, 6, "0.5", servedModel("claude-opus-5-5", 5, "0.5"))
	for name, add := range map[string]func(*rollupTree){
		"child without observation": func(tr *rollupTree) { tr.add("bare", "root", base, nil) },
		"foreign source": func(tr *rollupTree) {
			s := tr.add("foreign", "root", base, &full)
			s.Status.UsageObservation.Source.SessionUID = "another-session"
		},
		"interactive child": func(tr *rollupTree) { s := tr.add("tui", "root", base, nil); s.Spec.Mode = v1alpha1.ModeLocal },
	} {
		t.Run(name, func(t *testing.T) {
			tree := &rollupTree{}
			tree.add("root", humanSA, base, nil)
			tree.add("observed", "root", base, &full)
			add(tree)
			root := tree.items[0]
			r := usageRollup(&root, tree.items)
			if r == nil || r.Coverage != "Partial" || r.Sessions != 2 || len(r.Invocations) != 1 {
				t.Fatal("coverage", r)
			}
			for _, m := range []protocol.TokenObservation{r.InputTokens, r.OutputTokens, r.CacheReadInputTokens, r.CacheCreationInputTokens} {
				if m.State != protocol.UsageUnknown || m.Amount != nil {
					t.Fatal("partial coverage produced a total", m)
				}
			}
			if r.Cost.State != protocol.UsageUnknown || r.Cost.Amount != "" || r.ObservedModelState != protocol.UsageUnknown || r.ObservedModels != nil || r.BillingBasis != protocol.UsageUnknown {
				t.Fatal("partial coverage produced a cost or model list", r.Cost, r.ObservedModels)
			}
		})
	}
	t.Run("codex child keeps cost unknown", func(t *testing.T) {
		tree := &rollupTree{}
		tree.add("root", humanSA, base, nil)
		tree.add("claude-child", "root", base, &full)
		codex := providerObservation("", "pod", "", "", 7, 8, "")
		s := tree.add("codex-child", "root", base, nil)
		s.Spec.Agent, s.Spec.Model = "codex", "gpt-6.1-sol"
		codex.Source = protocol.UsageSource{SessionUID: string(s.UID), PodUID: "pod", Provider: "codex", RequestedModel: "gpt-6.1-sol", AttemptID: strings.Repeat("a", 64)}
		codex.ID = protocol.UsageObservationID(codex.Source)
		s.Status.UsageObservation = usageObservationStatus(codex)
		root := tree.items[0]
		r := usageRollup(&root, tree.items)
		if r.Coverage != "Complete" || *r.InputTokens.Amount != 12 || r.Cost.State != protocol.UsageUnknown || r.Cost.Amount != "" || r.ObservedModelState != protocol.UsageUnknown {
			t.Fatal("codex token-only child", r)
		}
	})
	t.Run("no descendants", func(t *testing.T) {
		tree := &rollupTree{}
		tree.add("root", humanSA, base, &full)
		root := tree.items[0]
		if r := usageRollup(&root, tree.items); r != nil {
			t.Fatal("a Session without descendants got a zero rollup", r)
		}
	})
	t.Run("cycle and bound", func(t *testing.T) {
		tree := &rollupTree{}
		tree.add("root", "loop", base, &full)
		tree.add("loop", "root", base, &full)
		root := tree.items[0]
		if r := usageRollup(&root, tree.items); r == nil || r.Sessions != 1 || len(r.Invocations) != 1 {
			t.Fatal("cycle walked twice", r)
		}
		wide := &rollupTree{}
		wide.add("root", humanSA, base, nil)
		for i := 0; i <= maxRollupSessions; i++ {
			wide.add("child-"+itoa(i), "root", base, &full)
		}
		root = wide.items[0]
		r := usageRollup(&root, wide.items)
		if r == nil || !r.Truncated || r.Coverage != "Partial" || r.Sessions != maxRollupSessions || r.InputTokens.Amount != nil {
			t.Fatal("bound", r.Truncated, r.Coverage, r.Sessions)
		}
	})
}

func TestUsageRollupOnSessionGet(t *testing.T) {
	f := newFixture(t)
	root := "rollup-root"
	f.sessionPod(root, "full", 0)
	child := "rollup-child"
	childToken := f.sessionPod(child, "full", 1)
	s := f.session(child)
	s.Spec.Parent = root
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	u := providerObservation(string(s.UID), f.auth[childToken].PodUID, protocol.AgentClaude, "claude-opus-5-5", 3, 4, "0", servedModel("claude-opus-5-5", 3, "0"))
	s = f.session(child)
	s.Status.UsageObservation = usageObservationStatus(u)
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	provider := http.Header{apiv1.AttributionHeader: {apiv1.AttributionProvider}}
	// An older reader that does not ask gets the shape its strict decoder
	// accepts: no rollup and no version 2 observation.
	legacy := f.do(http.MethodGet, apiv1.SessionsPath+"/"+root, tokHuman, nil)
	if legacy.Code != http.StatusOK || strings.Contains(legacy.Body.String(), "usageRollup") {
		t.Fatal("legacy reader got the rollup", legacy.Code, legacy.Body.String())
	}
	if w := f.do(http.MethodGet, apiv1.SessionsPath+"/"+child, tokHuman, nil); strings.Contains(w.Body.String(), "usageObservation") {
		t.Fatal("legacy reader got a version 2 observation", w.Body.String())
	}
	if w := f.do(http.MethodGet, apiv1.SessionsPath, tokHuman, nil); strings.Contains(w.Body.String(), "servedModels") || !strings.Contains(w.Body.String(), child) {
		t.Fatal("legacy list got a version 2 observation", w.Body.String())
	}
	if w := f.doHeader(http.MethodGet, apiv1.SessionsPath, tokHuman, nil, provider); !strings.Contains(w.Body.String(), `"servedModels"`) {
		t.Fatal("provider list lost the version 2 observation", w.Body.String())
	}
	w := f.doHeader(http.MethodGet, apiv1.SessionsPath+"/"+root, tokHuman, nil, provider)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, field := range []string{`"usageRollup":{"scope":"Descendants","coverage":"Complete","sessions":1,"invocations":[{"sessionUID":"` + string(s.UID) + `","observationID":"` + u.ID + `"}]`, `"cost":{"state":"Reported","amount":"0","unit":"USD","kind":"ProviderCLIEquivalentEstimate"}`, `"billingBasis":"Subscription"`, `"observedModels":["claude-opus-5-5"]`} {
		if !strings.Contains(w.Body.String(), field) {
			t.Fatal("rollup wire shape", field, w.Body.String())
		}
	}
	got := decode[apiv1.Session](t, w)
	if got.UsageRollup == nil || got.UsageObservation != nil || *got.UsageRollup.InputTokens.Amount != 3 {
		t.Fatal("rollup or the root's own observation", got.UsageRollup, got.UsageObservation)
	}
	if w := f.doHeader(http.MethodGet, apiv1.SessionsPath+"/"+child, tokHuman, nil, provider); strings.Contains(w.Body.String(), "usageRollup") || !strings.Contains(w.Body.String(), `"usageObservation":{"version":2`) {
		t.Fatal("a Session without descendants got a rollup, or lost its own observation", w.Body.String())
	}
}

func (f *fixture) doHeader(method, path, token string, body any, header http.Header) *httptest.ResponseRecorder {
	f.t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range header {
		req.Header[k] = v
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	return w
}

func TestManagedOutputAttributionJoinsRetainedInvocationUsage(t *testing.T) {
	f, binding := newCanaryFixture(t, "claude", interceptor.Funcs{})
	enableContexts(f)
	f.srv.Exec = &fakeExec{}
	record := binding.Record
	record.CaptureOutput = true
	raw, _ := json.Marshal([]projectcanary.Record{record})
	policy, err := projectcanary.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	f.srv.PrivateProjectCanaries = policy
	token := contextSession(t, f, record.SessionName(), clientSA)
	s := f.session(record.SessionName())
	s.Spec = binding.Session.Spec
	s.Spec.ProjectCanary.CaptureOutput = true
	s.Spec.ProjectCanary.PolicyDigest = record.Digest()
	s.Annotations, s.Labels, s.Finalizers = binding.Session.Annotations, binding.Session.Labels, binding.Session.Finalizers
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	podUID := f.auth[token].PodUID
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{SessionUID: string(s.UID), LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: podUID}}
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	invocation := apiv1.ManagedInvocation(string(s.UID), podUID)
	req := apiv1.PublishContextRequest{Kind: "transcript-excerpt", Key: apiv1.ManagedOutputKey(invocation, 0), Text: "synthetic managed stdout", ManagedOutput: &apiv1.ManagedOutputRequest{Ordinal: 0, ConversationID: "11111111-1111-4111-8111-111111111111", Completeness: "PartialManagedOutput"}}
	provider := http.Header{apiv1.AttributionHeader: {apiv1.AttributionProvider}}
	w := f.doHeader(http.MethodPost, apiv1.PublishedContextsPath, token, req, provider)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	m := decode[apiv1.PublishContextResult](t, w).Metadata
	if m.Attribution.ObservedModelState != "Unknown" || m.Attribution.UsageState != "Unknown" || m.Attribution.CostState != "Unknown" || m.Attribution.Usage != nil || !apiv1.ValidContextAttribution(m.Attribution, true) {
		t.Fatal("attribution claimed usage before the invocation's observation was retained", m.Attribution)
	}
	path := apiv1.PublishedContextsPath + "/" + m.Name + "?project=sample"
	reader := contextSession(t, f, "attribution-reader", humanSA)

	u := providerObservation(string(s.UID), podUID, "claude", s.Spec.Model, 30, 40, "0.75", servedModel("claude-opus-5-5", 30, "0.75"))
	s = f.session(record.SessionName())
	s.Status.UsageObservation = usageObservationStatus(u)
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	w = f.doHeader(http.MethodGet, path, reader, nil, provider)
	got := decode[apiv1.PublishedContext](t, w).Metadata.Attribution
	if w.Code != http.StatusOK || got.ObservedModelState != "ProviderReported" || got.UsageState != "ProviderReported" || got.CostState != "ProviderReported" ||
		!reflect.DeepEqual(got.ObservedModels, []string{"claude-opus-5-5"}) || got.Usage == nil || got.Usage.ObservationID != u.ID || got.Usage.BillingBasis != "Subscription" ||
		*got.Usage.InputTokens.Amount != 30 || got.Usage.CacheReadInputTokens.Amount != nil || got.Usage.Cost.Amount != "0.75" || !apiv1.ValidContextAttribution(got, true) {
		t.Fatal("provider-reported attribution", w.Code, w.Body.String())
	}
	for _, field := range []string{`"observedModelState":"ProviderReported"`, `"observedModels":["claude-opus-5-5"]`, `"usage":{"observationID":"` + u.ID + `","billingBasis":"Subscription","inputTokens":{"state":"Reported","amount":30}`} {
		if !strings.Contains(w.Body.String(), field) {
			t.Fatal("attribution wire shape", field, w.Body.String())
		}
	}
	// A reader that does not ask keeps the legacy Unknown shape its strict
	// decoder and validator accept.
	w = f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "ProviderReported") || strings.Contains(w.Body.String(), "observedModels") || strings.Contains(w.Body.String(), `"usage"`) {
		t.Fatal("legacy reader got the new attribution", w.Body.String())
	}
	// The list carries the same attribution.
	w = f.doHeader(http.MethodGet, apiv1.PublishedContextsPath+"?project=sample", reader, nil, provider)
	list := decode[apiv1.PublishedContextList](t, w)
	if len(list.Items) != 1 || !reflect.DeepEqual(list.Items[0].Attribution, got) {
		t.Fatal("list attribution", w.Body.String())
	}
	// A foreign Pod's observation is not this invocation's.
	s = f.session(record.SessionName())
	other := providerObservation(string(s.UID), "another-pod", "claude", s.Spec.Model, 1, 1, "1")
	if attr := contextAttribution(contextMetadata(f.context(t, m.Name)).Attribution, f.context(t, m.Name), withObservation(s, other)); attr.Usage != nil || attr.UsageState != "Unknown" {
		t.Fatal("foreign invocation attributed", attr)
	}
}

func withObservation(s *v1alpha1.AgentSession, u protocol.UsageObservation) *v1alpha1.AgentSession {
	c := s.DeepCopy()
	c.Status.UsageObservation = usageObservationStatus(u)
	return c
}

func TestContextAttributionExplicitAndVersionOneStayUnknown(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	token := contextSession(t, f, "explicit-attribution", humanSA)
	w := f.doHeader(http.MethodPost, apiv1.PublishedContextsPath, token, contextReq(), http.Header{apiv1.AttributionHeader: {apiv1.AttributionProvider}})
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	m := decode[apiv1.PublishContextResult](t, w).Metadata
	s := f.session("explicit-attribution")
	u := providerObservation(string(s.UID), f.auth[token].PodUID, "claude", s.Spec.Model, 1, 1, "1", servedModel("claude-opus-5-5", 1, "1"))
	p := f.context(t, m.Name)
	if attr := contextAttribution(contextMetadata(p).Attribution, p, withObservation(s, u)); attr.Usage != nil || attr.ObservedModelState != "Unknown" {
		t.Fatal("explicit publication attributed provider usage", attr)
	}
	if !apiv1.ValidContextAttribution(m.Attribution, false) {
		t.Fatal("explicit attribution invalid", m.Attribution)
	}
	bad := apiv1.ContextAttribution{Provider: "claude", RequestedModel: "m", ObservedModelState: "ProviderReported", UsageState: "Unknown", CostState: "Unknown"}
	if apiv1.ValidContextAttribution(bad, true) {
		t.Fatal("ProviderReported without evidence accepted")
	}
}

func (f *fixture) context(t *testing.T, name string) *coord.PublishedContext {
	t.Helper()
	p, err := f.srv.PublishedContexts.GetForProject(context.Background(), name, "sample")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
