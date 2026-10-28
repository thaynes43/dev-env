package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestPortalReaderSessionSummaries(t *testing.T) {
	f := newFixture(t)
	now := f.now
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: sessionNS, UID: types.UID("pod-uid")}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	beat := func(at time.Time) *v1alpha1.AgentStatus {
		stamp := metav1.NewTime(at)
		return &v1alpha1.AgentStatus{LastHeartbeat: &stamp}
	}
	sess := func(name, repo, phase string, hb *v1alpha1.AgentStatus) v1alpha1.AgentSession {
		return v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: types.UID("uid-" + name)}, Spec: v1alpha1.AgentSessionSpec{Repo: repo, Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "SECRET-PROMPT", Parent: humanSA}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.SessionPhase(phase), Agent: hb}}
	}
	live := &portalLive{pod: pod, rv: "7", sessions: []v1alpha1.AgentSession{
		sess("newer", "dev-env", "Running", beat(now.Add(-time.Minute))),
		sess("older", "dev-env", "Running", beat(now.Add(-2*time.Hour))),
		sess("unseen", "dev-env", "", nil),
		sess("future", "dev-env", "Running", beat(now.Add(time.Hour))),
		sess("zzz-foreign", "haynes-ops", "Running", beat(now)),
		{ObjectMeta: metav1.ObjectMeta{Name: "outside", Namespace: "other", UID: "uid-outside"}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Prompt: "SECRET-PROMPT"}},
	}}
	f.srv.Live = live
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-portal"] = id
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, portalReaderRef)
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-portal", nil), http.StatusForbidden, apiv1.CodeForbidden)
	if strings.Contains(f.do(http.MethodGet, apiv1.SessionsPath, "tok-portal", nil).Body.String(), "SECRET-PROMPT") {
		t.Fatal("disabled portal account received a generic session list")
	}
	f.srv.Policy.Clients = []string{clientSA}
	f.srv.Policy.PortalReaderEnabled = true
	first := f.do(http.MethodGet, apiv1.SessionsPath+"?limit=2", "tok-portal", nil)
	page := decode[apiv1.PortalSessionPage](t, first)
	body := first.Body.String()
	if page.Complete || page.Limit != 2 || len(page.Sessions) != 2 || page.Sessions[0].Name != "future" || page.Sessions[1].Name != "newer" || strings.Contains(body, "SECRET-PROMPT") || strings.Contains(body, "haynes-ops") || strings.Contains(body, "prompt") || strings.Contains(body, "outside") {
		t.Fatalf("page leaked scope or scanned without a bound: %+v %s", page, body)
	}
	if page.Sessions[0].Freshness != "unknown" || page.Sessions[1].Freshness != "observed" || page.Sessions[1].Phase != "Running" {
		t.Fatalf("freshness %+v", page.Sessions)
	}
	if live.lists[0].Limit != 2 || live.lists[0].Namespace != sessionNS || live.lists[0].Continue != "" {
		t.Fatalf("first list was not a bounded namespace page: %+v", live.lists[0])
	}
	rest := decode[apiv1.PortalSessionPage](t, f.do(http.MethodGet, apiv1.SessionsPath+"?limit=2&cursor="+page.NextCursor, "tok-portal", nil))
	if rest.Complete || len(rest.Sessions) != 1 || rest.Sessions[0].Name != "older" || rest.Sessions[0].Freshness != "stale" {
		t.Fatalf("second page %+v", rest)
	}
	continued := live.lists[len(live.lists)-1]
	if continued.Continue == "" || continued.Raw != nil || continued.Limit != 2 {
		t.Fatalf("continuation did not use the opaque snapshot token alone: %+v", continued)
	}
	live.changed = true
	if f.do(http.MethodGet, apiv1.SessionsPath+"?limit=2&cursor="+rest.NextCursor, "tok-portal", nil).Code == http.StatusOK {
		t.Fatal("changed snapshot was reported complete")
	}
	live.changed = false
	final := decode[apiv1.PortalSessionPage](t, f.do(http.MethodGet, apiv1.SessionsPath+"?limit=2&cursor="+rest.NextCursor, "tok-portal", nil))
	// Every repository is listed under its own repo; only the namespace bounds the scan.
	if !final.Complete || len(final.Sessions) != 2 || final.Sessions[0].Name != "unseen" || final.Sessions[0].Freshness != "unknown" || final.Sessions[0].Phase != "Pending" || final.Sessions[0].Repo != "dev-env" || final.Sessions[1].Name != "zzz-foreign" || final.Sessions[1].Repo != "haynes-ops" {
		t.Fatalf("final page %+v", final)
	}
	for _, path := range []string{
		apiv1.SessionsPath + "?repo=dev-env",
		apiv1.SessionsPath + "?repo=haynes-ops",
		apiv1.SessionsPath + "?limit=101",
		apiv1.SessionsPath + "?cursor=" + page.NextCursor + "/extra",
		apiv1.SessionsPath + "?mine=true",
		apiv1.SessionsPath + "?limit=1&bad=%zz",
		apiv1.SessionsPath + "?repo=foreign;limit=1",
		"/v1/%73essions",
		apiv1.SessionsPath + "?limit=1&cursor=" + page.NextCursor,
	} {
		if f.do(http.MethodGet, path, "tok-portal", nil).Code == http.StatusOK {
			t.Fatalf("unsafe query accepted %s", path)
		}
	}
	if f.do(http.MethodPost, apiv1.SessionsPath, "tok-portal", task()).Code != http.StatusForbidden {
		t.Fatal("portal reader created a session")
	}
	if f.do(http.MethodGet, apiv1.SessionsPath+"/newer", "tok-portal", nil).Code != http.StatusForbidden {
		t.Fatal("portal reader read a session detail")
	}
	if f.do(http.MethodGet, apiv1.TaskClaimsPath, "tok-portal", nil).Code != http.StatusForbidden {
		t.Fatal("portal reader listed task claims")
	}
	wrong := id
	wrong.PodUID = "other-pod"
	f.auth["tok-wrong-pod"] = wrong
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-wrong-pod", nil), http.StatusForbidden, apiv1.CodeForbidden)
	stranger := saIdentity("dev-agents/dev-env-v2-portal-other")
	stranger.PodName, stranger.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-wrong-sa"] = stranger
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-wrong-sa", nil), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-not-audience", nil), http.StatusUnauthorized, apiv1.CodeUnauthenticated)
	if _, err := json.Marshal(page); err != nil {
		t.Fatal(err)
	}
	deleting := pod.DeepCopy()
	stamp := metav1.NewTime(now)
	deleting.DeletionTimestamp = &stamp
	f.srv.Live = &portalLive{pod: deleting, rv: "7"}
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-portal", nil), http.StatusForbidden, apiv1.CodeForbidden)
	f.srv.Live = &portalLive{pod: pod, getErr: errors.New("unavailable"), rv: "7"}
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "tok-portal", nil), http.StatusForbidden, apiv1.CodeForbidden)
	if err := ValidatePortalCaller(Policy{Human: portalReaderRef}); err == nil {
		t.Fatal("portal account was also accepted as the human caller")
	}
	if err := ValidatePortalCaller(Policy{Coordinators: []CoordinatorHost{{ServiceAccount: portalReaderRef}}}); err == nil {
		t.Fatal("portal account was also accepted as a coordinator")
	}
}

type portalLive struct {
	pod      *corev1.Pod
	getErr   error
	sessions []v1alpha1.AgentSession
	rv       string
	changed  bool
	lists    []client.ListOptions
}

func (p *portalLive) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	pod, ok := obj.(*corev1.Pod)
	if !ok || p.pod == nil || key.Namespace != sessionNS || key.Name != p.pod.Name {
		return errors.New("unavailable")
	}
	if p.getErr != nil {
		return p.getErr
	}
	*pod = *p.pod
	return nil
}

func (p *portalLive) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	lo := &client.ListOptions{}
	lo.ApplyOptions(opts)
	copied := *lo
	if lo.Raw != nil {
		raw := *lo.Raw
		copied.Raw = &raw
	}
	p.lists = append(p.lists, copied)
	if lo.Continue != "" && lo.Raw != nil && lo.Raw.ResourceVersion != "" {
		return errors.New("continue must not set resourceVersion")
	}
	if p.changed {
		return apierrors.NewResourceExpired("expired")
	}
	items := append([]v1alpha1.AgentSession{}, p.sessions...)
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].Name < items[j-1].Name; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	start := 0
	if lo.Continue != "" {
		name := strings.TrimPrefix(lo.Continue, "after:")
		for start < len(items) && items[start].Name <= name {
			start++
		}
	}
	limit := int(lo.Limit)
	if limit < 1 {
		limit = len(items)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	out := list.(*v1alpha1.AgentSessionList)
	out.Items = append([]v1alpha1.AgentSession{}, items[start:end]...)
	out.ResourceVersion = p.rv
	if end < len(items) && len(out.Items) > 0 {
		out.Continue = "after:" + out.Items[len(out.Items)-1].Name
	}
	return nil
}

func TestPortalReaderActivityViewIsOptInAndAllowlisted(t *testing.T) {
	f := newFixture(t)
	now := f.now
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: sessionNS, UID: types.UID("pod-uid")}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	sess := func(name string, mode v1alpha1.SessionMode, model string, agent *v1alpha1.AgentStatus) v1alpha1.AgentSession {
		return v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: types.UID("uid-" + name)}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: "claude", Mode: mode, Model: model, Prompt: "SECRET-PROMPT", Parent: humanSA}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning, Agent: agent}}
	}
	at := func(when time.Time) *metav1.Time { stamp := metav1.NewTime(when); return &stamp }
	task := sess("a-task", v1alpha1.ModeTask, "claude-opus-5-5", &v1alpha1.AgentStatus{Status: "busy", LastActivity: at(now.Add(-time.Minute)), LastHeartbeat: at(now)})
	u := providerObservation("uid-a-task", "pod-a-task", protocol.AgentClaude, "claude-opus-5-5", 1234567, 0, "0.75",
		servedModel("claude-haiku-4-5", 10, "0.05"), servedModel("claude-opus-5-5", 20, "0.7"))
	u.CacheReadInputTokens = reportedTokens(5000)
	task = *withObservation(&task, u)
	task.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded, Reason: "TaskSucceeded", Message: "SECRET-RESULT-MESSAGE"}
	// A remote session holds its own valid observation, but usage covers task sessions only.
	remote := sess("b-remote", v1alpha1.ModeRemote, "claude-opus-5-5", &v1alpha1.AgentStatus{Status: "idle", LastActivity: at(now.Add(time.Hour))})
	ru := providerObservation("uid-b-remote", "pod-b-remote", protocol.AgentClaude, "claude-opus-5-5", 7, 7, "0.01")
	if ru.Validate() != nil {
		t.Fatal("remote fixture observation is invalid")
	}
	remote = *withObservation(&remote, ru)
	odd := sess("c-odd", "", "opus with spaces", &v1alpha1.AgentStatus{Status: "Busy"})
	odd.Spec.Agent = "mystery"
	bare := sess("d-bare", v1alpha1.ModeLocal, "claude-opus-5-5", nil)
	bare.Spec.Parent = "not a launcher+" // the CRD bounds length only; this shape is omitted
	f.srv.Live = &portalLive{pod: pod, rv: "7", sessions: []v1alpha1.AgentSession{task, remote, odd, bare}}
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-portal"] = id
	f.srv.Policy.PortalReaderEnabled = true

	// Without view=activity every row keeps the six keys earlier portal images require.
	legacy := f.do(http.MethodGet, apiv1.SessionsPath+"?limit=50", "tok-portal", nil)
	var raw struct {
		Sessions []map[string]json.RawMessage `json:"sessions"`
	}
	if legacy.Code != http.StatusOK || json.Unmarshal(legacy.Body.Bytes(), &raw) != nil || len(raw.Sessions) != 4 {
		t.Fatal(legacy.Code, legacy.Body.String())
	}
	for _, row := range raw.Sessions {
		if len(row) != 6 || row["activity"] != nil {
			t.Fatalf("legacy row changed shape: %v", row)
		}
	}

	w := f.do(http.MethodGet, apiv1.SessionsPath+"?limit=50&view=activity", "tok-portal", nil)
	body := w.Body.String()
	page := decode[apiv1.PortalSessionPage](t, w)
	if !page.Complete || len(page.Sessions) != 4 || strings.Contains(body, "SECRET-PROMPT") || strings.Contains(body, "prompt") || strings.Contains(body, "attemptID") || strings.Contains(body, "pod-a-task") || strings.Contains(body, "billingBasis") || strings.Contains(body, "SECRET-RESULT-MESSAGE") {
		t.Fatalf("activity view leaked or lost rows: %s", body)
	}
	for _, s := range page.Sessions {
		if s.Activity == nil {
			t.Fatalf("activity missing for %s", s.Name)
		}
	}
	a := page.Sessions[0].Activity
	if a.Mode != "task" || a.Provider != "claude" || a.AgentState != "busy" || a.LastActivity == nil || !a.LastActivity.Equal(now.Add(-time.Minute).UTC()) || a.RequestedModel != "claude-opus-5-5" || a.LaunchedBy != humanSA || a.TaskResult != "Succeeded" {
		t.Fatalf("task activity %+v", a)
	}
	got := a.Usage
	if got.Scope != protocol.UsageInitial || got.ResultState != "Final" || strings.Join(got.ServedModels, ",") != "claude-haiku-4-5,claude-opus-5-5" ||
		got.InputTokens.State != protocol.UsageReported || *got.InputTokens.Amount != 1234567 ||
		got.OutputTokens.State != protocol.UsageReported || *got.OutputTokens.Amount != 0 ||
		got.CacheReadInputTokens.State != protocol.UsageReported || *got.CacheReadInputTokens.Amount != 5000 ||
		got.CacheCreationInputTokens.State != protocol.UsageUnknown || got.CacheCreationInputTokens.Amount != nil ||
		got.Cost != estimate("0.75") {
		t.Fatalf("task usage %+v", got)
	}
	*got.InputTokens.Amount = 1
	if *task.Status.UsageObservation.InputTokens.Amount != 1234567 {
		t.Fatal("response aliased the session status")
	}
	unknown := func(name string, u apiv1.PortalSessionUsage) {
		t.Helper()
		for _, m := range []protocol.TokenObservation{u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens} {
			if m.State != protocol.UsageUnknown || m.Amount != nil {
				t.Fatalf("%s counter was not Unknown: %+v", name, u)
			}
		}
		if u.Scope != protocol.UsageUnknown || u.ResultState != protocol.UsageUnknown || u.ServedModels == nil || len(u.ServedModels) != 0 || u.Cost.State != protocol.UsageUnknown || u.Cost.Kind != protocol.UsageUnknown {
			t.Fatalf("%s usage was not Unknown: %+v", name, u)
		}
	}
	r := page.Sessions[1].Activity
	// An interactive session has no task result; a Failed phase alone is not one.
	if r.Mode != "remote" || r.Provider != "claude" || r.AgentState != "idle" || r.LastActivity != nil || r.TaskResult != "" {
		t.Fatalf("remote activity %+v", r)
	}
	unknown("remote", r.Usage)
	o := page.Sessions[2].Activity
	if o.Mode != protocol.UsageUnknown || o.Provider != protocol.UsageUnknown || o.AgentState != protocol.UsageUnknown || o.RequestedModel != protocol.UsageUnknown || o.LastActivity != nil || o.TaskResult != "" {
		t.Fatalf("words outside their sets were passed through: %+v", o)
	}
	unknown("odd", o.Usage)
	b := page.Sessions[3].Activity
	if b.Mode != "local" || b.AgentState != protocol.UsageUnknown || b.LastActivity != nil || b.LaunchedBy != "" || strings.Contains(body, "not a launcher") {
		t.Fatalf("session without agent status %+v", b)
	}
	unknown("bare", b.Usage)
	if !strings.Contains(body, `"servedModels":[]`) || strings.Contains(body, `"lastActivity":null`) {
		t.Fatalf("empty list or absent time was serialized ambiguously: %s", body)
	}

	for _, path := range []string{
		apiv1.SessionsPath + "?view=usage",
		apiv1.SessionsPath + "?view=",
		apiv1.SessionsPath + "?view=activity&view=activity",
		apiv1.SessionsPath + "?view=ACTIVITY",
	} {
		if f.do(http.MethodGet, path, "tok-portal", nil).Code == http.StatusOK {
			t.Fatalf("view accepted %s", path)
		}
	}
}

// The activity view carries a session's Remote Control state word and, only when
// it has the exact claude.ai shape, its link. Never the entry's name or id.
func TestPortalActivityRemoteControl(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	const link = "https://claude.ai/code/session_FIXTURE0000000000"
	activity := func(rc *v1alpha1.RemoteControlStatus) *apiv1.PortalSessionActivity {
		s := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeRemote, Agent: v1alpha1.AgentClaude}}
		s.Status.RemoteControl = rc
		return portalActivity(s, now)
	}
	a := activity(&v1alpha1.RemoteControlStatus{Name: "SECRET-NAME", SessionID: "SECRET-ID", State: "registered", URL: link})
	if a.RemoteControl == nil || a.RemoteControl.State != "registered" || a.RemoteControl.URL != link {
		t.Fatalf("registered %+v", a.RemoteControl)
	}
	if raw, _ := json.Marshal(a); strings.Contains(string(raw), "SECRET") || !strings.Contains(string(raw), `"remoteControl":{"state":"registered","url":"`+link+`"}`) {
		t.Fatalf("wire form %s", raw)
	}
	for state := range map[string]bool{"pair-code-issued": true, "starting": true} {
		if a := activity(&v1alpha1.RemoteControlStatus{State: state}); a.RemoteControl == nil || a.RemoteControl.State != state || a.RemoteControl.URL != "" {
			t.Fatalf("%s %+v", state, a.RemoteControl)
		}
	}
	if raw, _ := json.Marshal(activity(&v1alpha1.RemoteControlStatus{State: "starting"})); strings.Contains(string(raw), `"url"`) {
		t.Fatalf("empty url serialized: %s", raw)
	}
	if a := activity(nil); a.RemoteControl != nil {
		t.Fatalf("nil status produced %+v", a.RemoteControl)
	} else if raw, _ := json.Marshal(a); strings.Contains(string(raw), "remoteControl") {
		t.Fatalf("absent entry serialized: %s", raw)
	}
	for name, url := range map[string]string{
		"other host":       "https://evil.example/code/session_FIXTURE0000000000",
		"subdomain":        "https://www.claude.ai/code/session_FIXTURE0000000000",
		"look-alike host":  "https://claude.ai.evil.example/code/session_FIXTURE0000000000",
		"userinfo":         "https://claude.ai@evil.example/code/session_FIXTURE0000000000",
		"http":             "http://claude.ai/code/session_FIXTURE0000000000",
		"other scheme":     "javascript:alert(1)",
		"other path":       "https://claude.ai/chat/session_FIXTURE0000000000",
		"extra path":       "https://claude.ai/code/session_FIXTURE0000000000/x",
		"query":            link + "?a=b",
		"fragment":         link + "#x",
		"trailing space":   link + " ",
		"trailing newline": link + "\n",
		"leading space":    " " + link,
		"short id":         "https://claude.ai/code/session_short",
		"long id":          "https://claude.ai/code/session_" + strings.Repeat("a", 129),
		"empty id":         "https://claude.ai/code/session_",
	} {
		a := activity(&v1alpha1.RemoteControlStatus{State: "registered", URL: url})
		if a.RemoteControl == nil || a.RemoteControl.State != "registered" || a.RemoteControl.URL != "" {
			t.Errorf("%s: url kept or entry lost: %+v", name, a.RemoteControl)
		}
	}
	for name, state := range map[string]string{"empty": "", "upper": "Registered", "space": "pair code", "digit": "v2", "markup": "<b>", "long": strings.Repeat("a", 33), "leading dash": "-x"} {
		if a := activity(&v1alpha1.RemoteControlStatus{State: state, URL: link}); a.RemoteControl != nil {
			t.Errorf("state %s kept the entry: %+v", name, a.RemoteControl)
		}
	}
}

func convStatus(provider string, at time.Time) *v1alpha1.ConversationUsageStatus {
	n, zero := int64(1234), int64(0)
	unknown := v1alpha1.TokenObservationStatus{State: "Unknown"}
	return &v1alpha1.ConversationUsageStatus{Version: 1, Provider: provider, ObservedAt: metav1.NewTime(at), Coverage: "RegisteredConversation", CounterSemantics: "ConversationCumulative", ResultState: "Final", Turns: 7,
		Models: []v1alpha1.ConversationModelUsageStatus{{Model: "claude-opus-5-5", InputTokens: v1alpha1.TokenObservationStatus{State: "Reported", Amount: &n}, OutputTokens: v1alpha1.TokenObservationStatus{State: "Reported", Amount: &zero}, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown}}}
}

// The conversation key is behind --portal-conversation-usage: an older portal
// refuses an unknown activity key, so the default view is byte-for-byte what it
// was. With the flag, only the DESIGN-029 allowlist is sent, only for a session
// that is interactive and whose figure is valid, and never as a zero.
func TestPortalReaderConversationUsageIsDefaultOffAndAllowlisted(t *testing.T) {
	f := newFixture(t)
	now := f.now
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: sessionNS, UID: types.UID("pod-uid")}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	sess := func(name string, mode v1alpha1.SessionMode, agent v1alpha1.AgentKind, cu *v1alpha1.ConversationUsageStatus) v1alpha1.AgentSession {
		return v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: types.UID("uid-" + name)}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: agent, Mode: mode, Model: "claude-opus-5-5", Parent: humanSA}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning, ConversationUsage: cu}}
	}
	partial := convStatus("claude", now.Add(-time.Minute))
	partial.ResultState, partial.ReasonCode, partial.Skipped = "Partial", "skipped_records", 2
	badReason := convStatus("claude", now)
	badReason.ResultState = "Partial" // a Partial with no reason is not valid
	future := convStatus("claude", now.Add(time.Hour))
	unknownResult := convStatus("claude", now)
	unknownResult.ResultState, unknownResult.ReasonCode, unknownResult.Models = "Unknown", "no_usage", nil
	f.srv.Live = &portalLive{pod: pod, rv: "7", sessions: []v1alpha1.AgentSession{
		sess("a-remote", v1alpha1.ModeRemote, "claude", convStatus("claude", now.Add(-5*time.Minute))),
		sess("b-partial", v1alpha1.ModeLocal, "claude", partial),
		sess("c-task", v1alpha1.ModeTask, "claude", convStatus("claude", now)),
		sess("d-wrong-provider", v1alpha1.ModeRemote, "codex", convStatus("claude", now)),
		sess("e-none", v1alpha1.ModeRemote, "claude", nil),
		sess("f-invalid", v1alpha1.ModeRemote, "claude", badReason),
		sess("g-future", v1alpha1.ModeRemote, "claude", future),
		sess("h-unknown", v1alpha1.ModeRemote, "claude", unknownResult),
	}}
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-portal"] = id
	f.srv.Policy.PortalReaderEnabled = true

	raw := func() (map[string]map[string]json.RawMessage, string) {
		w := f.do(http.MethodGet, apiv1.SessionsPath+"?limit=50&view=activity", "tok-portal", nil)
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		var page struct {
			Sessions []struct {
				Name     string                     `json:"name"`
				Activity map[string]json.RawMessage `json:"activity"`
			} `json:"sessions"`
		}
		if json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatal(w.Body.String())
		}
		out := map[string]map[string]json.RawMessage{}
		for _, s := range page.Sessions {
			out[s.Name] = s.Activity
		}
		return out, w.Body.String()
	}
	// Default off: no session carries the key.
	off, body := raw()
	for name, a := range off {
		if _, has := a["conversationUsage"]; has {
			t.Fatalf("%s carries the key with the flag off", name)
		}
	}
	if strings.Contains(body, "conversationUsage") {
		t.Fatal("the default view mentions the key")
	}

	f.srv.PortalConversationUsage = true
	on, body := raw()
	for _, name := range []string{"c-task", "d-wrong-provider", "e-none", "f-invalid", "h-unknown"} {
		if _, has := on[name]["conversationUsage"]; has {
			t.Errorf("%s carries a figure it should not", name)
		}
	}
	// Exactly the allowlist, at the top and in each model row; the fixed words,
	// version, provider, coverage and semantics are not sent.
	keys := func(r json.RawMessage) []string {
		var m map[string]json.RawMessage
		if json.Unmarshal(r, &m) != nil {
			t.Fatal(string(r))
		}
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if got := strings.Join(keys(on["a-remote"]["conversationUsage"]), ","); got != "models,observedAt,resultState,skipped,turns" {
		t.Fatalf("Final keys %s", got)
	}
	if got := strings.Join(keys(on["b-partial"]["conversationUsage"]), ","); got != "models,observedAt,reasonCode,resultState,skipped,turns" {
		t.Fatalf("Partial keys %s", got)
	}
	var cu apiv1.PortalConversationUsage
	if json.Unmarshal(on["a-remote"]["conversationUsage"], &cu) != nil || cu.ResultState != "Final" || cu.Turns != 7 || len(cu.Models) != 1 || !cu.ObservedAt.Equal(now.Add(-5*time.Minute)) {
		t.Fatalf("%+v", cu)
	}
	var models []json.RawMessage
	var wrapper struct {
		Models []json.RawMessage `json:"models"`
	}
	_ = json.Unmarshal(on["a-remote"]["conversationUsage"], &wrapper)
	models = wrapper.Models
	if got := strings.Join(keys(models[0]), ","); got != "cacheCreationInputTokens,cacheReadInputTokens,inputTokens,model,outputTokens" {
		t.Fatalf("model keys %s", got)
	}
	m := cu.Models[0]
	if m.InputTokens.State != "Reported" || *m.InputTokens.Amount != 1234 || m.OutputTokens.State != "Reported" || *m.OutputTokens.Amount != 0 || m.CacheReadInputTokens.State != "Unknown" || m.CacheReadInputTokens.Amount != nil {
		t.Fatalf("Reported zero or Unknown lost: %+v", m)
	}
	var p apiv1.PortalConversationUsage
	if json.Unmarshal(on["b-partial"]["conversationUsage"], &p) != nil || p.ResultState != "Partial" || p.ReasonCode != "skipped_records" || p.Skipped != 2 {
		t.Fatalf("%+v", p)
	}
	// A time ahead of the operator's clock is clamped to now.
	var fut apiv1.PortalConversationUsage
	if json.Unmarshal(on["g-future"]["conversationUsage"], &fut) != nil || !fut.ObservedAt.Equal(now) {
		t.Fatalf("%+v", fut)
	}
	for _, leak := range []string{"RegisteredConversation", "ConversationCumulative", `"provider":"claude","observedAt"`, "version"} {
		if strings.Contains(body, leak) {
			t.Errorf("the view leaked %q", leak)
		}
	}
	// The headless usage is its own key and is not touched.
	if _, has := on["a-remote"]["usage"]; !has {
		t.Fatal("the task usage key is gone")
	}
}
