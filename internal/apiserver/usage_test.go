package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

func TestUsageHeartbeatMalformedJSONMetadataPreservesOriginalAndLiveness(t *testing.T) {
	for _, malformed := range []string{`{"inputTokens":{"amount":9223372036854775808}}`, `{"cost":{"amount":12}}`, `{"observedAt":"invalid"}`, `"invalid"`} {
		t.Run(malformed, func(t *testing.T) {
			f := newFixture(t)
			name := "usage-malformed"
			token := f.sessionPod(name, "full", 0)
			u := fixtureUsage(f, name, token)
			st := status(name)
			st.UsageObservation = &u
			if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
				t.Fatal(w.Code)
			}
			original := f.session(name).Status.UsageObservation.DeepCopy()
			f.now = f.now.Add(time.Minute)
			body := `{"session":"` + name + `","agent":{"state":"busy"},"usageObservation":` + malformed + `}`
			w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, body)
			got := f.session(name)
			h := sha256.Sum256([]byte(malformed))
			if w.Code != http.StatusNoContent || !apiequality.Semantic.DeepEqual(original, got.Status.UsageObservation) || got.Status.Agent.Status != protocol.AgentBusy || !got.Status.Agent.LastHeartbeat.Equal(&metav1.Time{Time: f.now}) || got.Status.UsageConflict == nil || got.Status.UsageConflict.Reason != "InvalidPayload" || got.Status.UsageConflict.IncomingSHA256 != hex.EncodeToString(h[:]) {
				t.Fatal("malformed usage blocked liveness or erased original", w.Code, got.Status)
			}
		})
	}
}

func fixtureUsage(f *fixture, name, token string) protocol.UsageObservation {
	u := protocol.UnknownUsage()
	u.Source = protocol.UsageSource{SessionUID: string(f.session(name).UID), PodUID: f.auth[token].PodUID, Provider: protocol.AgentClaude, RequestedModel: "claude-opus-5-5", AttemptID: strings.Repeat("a", 64)}
	u.ID, u.ObservedAt, u.ResultState = protocol.UsageObservationID(u.Source), f.now, "Final"
	n := int64(0)
	u.InputTokens = protocol.TokenObservation{State: protocol.UsageReported, Amount: &n}
	u.Cost = protocol.CostObservation{State: protocol.UsageReported, Amount: "0", Unit: "USD", Kind: protocol.UsageCLIEstimate}
	return u
}

func TestUsageHeartbeatOriginalReplayConflictAndLegacy(t *testing.T) {
	f := newFixture(t)
	name := "usage-original"
	token := f.sessionPod(name, "full", 0)
	path := protocol.HeartbeatPath(name)
	saved := f.session(name)
	saved.Status.Usage = &v1alpha1.UsageStatus{CostUSD: "777", InputTokens: 42}
	saved.Annotations = map[string]string{taskbudget.UIDAnnotation: "logical-task", taskbudget.EpochAnnotation: "2", taskbudget.DeadlineAnnotation: f.now.Add(time.Hour).Format(time.RFC3339Nano), taskbudget.HostAnnotation: "owned-host", taskbudget.PodAnnotation: "root-pod", taskbudget.WorkerAnnotation: "reserved-worker"}
	if err := f.c.Update(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	saved = f.session(name)
	saved.Status.Usage = &v1alpha1.UsageStatus{CostUSD: "777", InputTokens: 42}
	if err := f.c.Status().Update(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	u := fixtureUsage(f, name, token)
	st := status(name)
	st.UsageObservation = &u
	if w := f.do(http.MethodPost, path, token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	first := f.session(name).Status.UsageObservation.DeepCopy()
	if first == nil || first.Cost.Amount != "0" || first.InputTokens.Amount == nil || *first.InputTokens.Amount != 0 || first.OutputTokens.Amount != nil || first.LogicalTaskUID != "logical-task" || first.LogicalTaskEpoch != "2" || first.WorkerID != "reserved-worker" || first.ActualModelState != "Unknown" {
		t.Fatal("presence/source/binding lost", first)
	}
	if f.session(name).Status.Usage.CostUSD != "777" {
		t.Fatal("legacy raw evidence changed")
	}
	f.now = f.now.Add(time.Minute)
	if w := f.do(http.MethodPost, path, token, st); w.Code != http.StatusNoContent || !apiequality.Semantic.DeepEqual(first, f.session(name).Status.UsageObservation) || f.session(name).Status.UsageConflict != nil {
		t.Fatal("identical replay changed snapshot", w.Code)
	}
	st.UsageObservation = nil
	st.Usage = &protocol.Usage{CostUSD: 999}
	st.Agent.State = protocol.AgentBusy
	if w := f.do(http.MethodPost, path, token, st); w.Code != http.StatusNoContent || !apiequality.Semantic.DeepEqual(first, f.session(name).Status.UsageObservation) || f.session(name).Status.Usage.CostUSD != "777" {
		t.Fatal("legacy heartbeat erased/relabelled evidence")
	}
	u.Cost.Amount = "99"
	st.UsageObservation = &u
	f.now = f.now.Add(time.Minute)
	if w := f.do(http.MethodPost, path, token, st); w.Code != http.StatusNoContent {
		t.Fatal("usage conflict blocked heartbeat", w.Code)
	}
	got := f.session(name)
	if !apiequality.Semantic.DeepEqual(first, got.Status.UsageObservation) || got.Status.UsageConflict == nil || got.Status.UsageConflict.State != "Unknown" || got.Status.UsageConflict.Reason != "ConflictingSnapshot" || !got.Status.Agent.LastHeartbeat.Equal(&metav1.Time{Time: f.now}) {
		t.Fatal("original or liveness lost", got.Status)
	}
	v := view(got, false)
	raw, _ := json.Marshal(v)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, present := fields["usage"]; present || v.Usage != nil || v.UsageObservation == nil || v.UsageConflict == nil {
		t.Fatal("new API emitted unqualified raw Usage", string(raw))
	}
	// This is the actual old reader's DTO shape/row condition: no Usage means
	// it can neither fabricate $0 nor erase the CLI-estimate provenance.
	var old struct {
		Usage *apiv1.Usage `json:"usage"`
	}
	if json.Unmarshal(raw, &old) != nil || old.Usage != nil {
		t.Fatal("old reader fabricates a cost row")
	}
}

func TestUsageHeartbeatForeignSourceAndMalformedMetrics(t *testing.T) {
	for _, boundary := range []string{"session", "pod", "model", "provider", "attempt", "id", "cost-null-state", "quota", "task", "native"} {
		t.Run(boundary, func(t *testing.T) {
			f := newFixture(t)
			name := "usage-refusal"
			token := f.sessionPod(name, "full", 0)
			u := fixtureUsage(f, name, token)
			switch boundary {
			case "session":
				u.Source.SessionUID = "foreign"
				u.ID = protocol.UsageObservationID(u.Source)
			case "pod":
				u.Source.PodUID = "foreign"
				u.ID = protocol.UsageObservationID(u.Source)
			case "model":
				u.Source.RequestedModel = "claude-sonnet-5-5"
				u.ID = protocol.UsageObservationID(u.Source)
			case "provider":
				u.Source.Provider = protocol.AgentCodex
				u.Cost = protocol.CostObservation{State: "Unknown", Kind: "Unknown"}
				u.ID = protocol.UsageObservationID(u.Source)
			case "attempt":
				u.Source.AttemptID = "missing"
			case "id":
				u.ID = strings.Repeat("b", 64)
			case "cost-null-state":
				u.Cost.Amount = ""
			case "quota":
				u.SubscriptionQuotaState = "Reported"
			case "task":
				u.LogicalTaskUID = "forged"
			case "native":
				s := f.session(name)
				s.Spec.Mode = v1alpha1.ModeLocal
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			st := status(name)
			st.UsageObservation = &u
			if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
				t.Fatal("usage refusal blocked liveness", w.Code)
			}
			got := f.session(name)
			if got.Status.UsageObservation != nil || got.Status.UsageConflict == nil || got.Status.Agent == nil {
				t.Fatal("foreign/invalid evidence classified", got.Status)
			}
		})
	}
}

func TestUsageHeartbeatConcurrentSnapshotPreservesLiveness(t *testing.T) {
	f := newFixture(t)
	name := "usage-cas"
	token := f.sessionPod(name, "full", 0)
	u := fixtureUsage(f, name, token)
	st := status(name)
	st.UsageObservation = &u
	concurrent := u
	concurrent.Cost.Amount = "7"
	want := usageObservationStatus(concurrent)
	receipt := &v1alpha1.SessionDirectoryReceipt{State: "Preparing", Name: "session-directory." + strings.Repeat("b", 64), SpecDigest: strings.Repeat("c", 64)}
	confirmed := receipt.DeepCopy()
	confirmed.State, confirmed.UID = "Confirmed", "original-directory"
	patches := 0
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		patches++
		if patches == 1 {
			// The Directory writer acquires its receipt after the heartbeat's
			// read. Its ordinary merge patch must retain this unrelated field.
			fresh := f.session(name)
			fresh.Status.SessionDirectory = receipt
			if err := f.c.Status().Update(ctx, fresh); err != nil {
				return err
			}
		}
		if patches == 2 {
			fresh := f.session(name)
			if !apiequality.Semantic.DeepEqual(receipt, fresh.Status.SessionDirectory) {
				t.Fatal("heartbeat merge erased concurrent Directory receipt", fresh.Status)
			}
			fresh.Status.UsageObservation = want
			fresh.Status.SessionDirectory = confirmed
			fresh.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeDone, Note: "concurrent unrelated outcome"}
			if err := f.c.Status().Update(ctx, fresh); err != nil {
				return err
			}
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}})
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
		t.Fatal("usage CAS conflict blocked liveness", w.Code, w.Body.String())
	}
	got := f.session(name)
	if patches != 2 || !apiequality.Semantic.DeepEqual(want, got.Status.UsageObservation) || !apiequality.Semantic.DeepEqual(confirmed, got.Status.SessionDirectory) || got.Status.Agent == nil || got.Status.Agent.LastHeartbeat == nil || got.Status.Outcome == nil {
		t.Fatal("concurrent snapshot/fields lost", got.Status)
	}
	f.srv.Client = f.c
	f.now = f.now.Add(time.Minute)
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent || f.session(name).Status.UsageConflict == nil || !apiequality.Semantic.DeepEqual(want, f.session(name).Status.UsageObservation) || !apiequality.Semantic.DeepEqual(confirmed, f.session(name).Status.SessionDirectory) || !f.session(name).Status.Agent.LastHeartbeat.Equal(&metav1.Time{Time: f.now}) {
		t.Fatal("retained replay replaced concurrent original")
	}
}

func TestUsageViewLegacyAndPrunedTypedMetadataStayUnknown(t *testing.T) {
	for _, amount := range []string{"0", "1.25"} {
		s := &v1alpha1.AgentSession{Status: v1alpha1.AgentSessionStatus{Usage: &v1alpha1.UsageStatus{CostUSD: amount, InputTokens: 12}}}
		v := view(s, false)
		if v.Usage != nil || v.UsageObservation != nil || s.Status.Usage.CostUSD != amount {
			t.Fatal("legacy evidence promoted/erased")
		}
	}
	f := newFixture(t)
	name := "usage-pruned"
	token := f.sessionPod(name, "full", 0)
	u := fixtureUsage(f, name, token)
	s := f.session(name)
	s.Status.UsageObservation = usageObservationStatus(u)
	s.Status.UsageObservation.Cost.State = ""
	if view(s, false).UsageObservation != nil {
		t.Fatal("pruned metadata classified numeric zero")
	}
}
