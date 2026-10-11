package v1alpha1_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func schemaUsage() *v1alpha1.UsageObservationStatus {
	zero := int64(0)
	unknown := v1alpha1.TokenObservationStatus{State: "Unknown"}
	return &v1alpha1.UsageObservationStatus{Version: 1, ID: strings.Repeat("a", 64), Source: v1alpha1.UsageSourceStatus{SessionUID: "original-session", PodUID: "original-pod", Provider: "claude", RequestedModel: "claude-opus-5-5", AttemptID: strings.Repeat("b", 64)}, ObservedAt: metav1.NewTime(time.Now().UTC().Truncate(time.Second)), Coverage: "InitialManagedInvocation", CounterSemantics: "InvocationFinalTotal", ResultState: "Final", InputTokens: v1alpha1.TokenObservationStatus{State: "Reported", Amount: &zero}, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: v1alpha1.CostObservationStatus{State: "Reported", Amount: "0", Unit: "USD", Kind: "ProviderCLIEquivalentEstimate"}, AggregationScope: "Unknown", ActualModelState: "Unknown", SubscriptionQuotaState: "Unknown", APIBillingState: "Unknown", LocalResourceState: "Unknown"}
}

func TestUsageSchemaPresenceLegacyAndImmutableOriginal(t *testing.T) {
	c := ctx(t)
	s := taskSession()
	if err := k8s.Create(c, s); err != nil {
		t.Fatal(err)
	}
	s.Status.Usage = &v1alpha1.UsageStatus{CostUSD: "0", InputTokens: 9}
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("legacy zero rejected", err)
	}
	if s.Status.UsageObservation != nil {
		t.Fatal("legacy zero promoted")
	}
	s.Status.UsageObservation = schemaUsage()
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("explicit zero rejected", err)
	}
	var got v1alpha1.AgentSession
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), &got); err != nil {
		t.Fatal(err)
	}
	u := got.Status.UsageObservation
	if u == nil || u.Cost.Amount != "0" || u.InputTokens.Amount == nil || *u.InputTokens.Amount != 0 || u.OutputTokens.Amount != nil || got.Status.Usage.CostUSD != "0" {
		t.Fatal("presence or legacy evidence pruned", got.Status)
	}
	before, _ := json.Marshal(u)
	for _, mutate := range []func(*v1alpha1.AgentSession){func(x *v1alpha1.AgentSession) { x.Status.UsageObservation.Cost.Amount = "7" }, func(x *v1alpha1.AgentSession) { x.Status.UsageObservation.Source.AttemptID = strings.Repeat("c", 64) }, func(x *v1alpha1.AgentSession) { x.Status.UsageObservation = nil }} {
		bad := got.DeepCopy()
		mutate(bad)
		if err := k8s.Status().Update(c, bad); !apierrors.IsInvalid(err) {
			t.Fatal("original snapshot changed/erased", err)
		}
	}
	if err := k8s.Get(c, client.ObjectKeyFromObject(s), &got); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(got.Status.UsageObservation)
	if string(before) != string(after) {
		t.Fatal("invalid update lost original")
	}
	got.Status.Agent = &v1alpha1.AgentStatus{Status: "busy"}
	got.Status.UsageConflict = &v1alpha1.UsageConflictStatus{State: "Unknown", Reason: "ConflictingSnapshot", IncomingSHA256: strings.Repeat("d", 64), ObservedAt: metav1.Now()}
	if err := k8s.Status().Update(c, &got); err != nil {
		t.Fatal("liveness/conflict update blocked by original latch", err)
	}
}

func TestUsageSchemaRejectsUnqualifiedOrInventedMetrics(t *testing.T) {
	for _, boundary := range []string{"unknown-amount", "reported-missing", "unit", "billing", "actual-model", "tokens-missing", "tokens-negative", "tokens-bound", "cost-bound", "quota"} {
		t.Run(boundary, func(t *testing.T) {
			c := ctx(t)
			s := taskSession()
			if err := k8s.Create(c, s); err != nil {
				t.Fatal(err)
			}
			u := schemaUsage()
			switch boundary {
			case "unknown-amount":
				u.Cost.State = "Unknown"
			case "reported-missing":
				u.Cost.Amount = ""
			case "unit":
				u.Cost.Unit = ""
			case "billing":
				u.Cost.Kind = "ProviderAPIBilling"
			case "actual-model":
				u.ActualModelState = "Reported"
			case "tokens-missing":
				u.InputTokens.Amount = nil
			case "tokens-negative":
				n := int64(-1)
				u.InputTokens.Amount = &n
			case "tokens-bound":
				n := int64(1000000000000001)
				u.InputTokens.Amount = &n
			case "cost-bound":
				u.Cost.Amount = strings.Repeat("9", 65)
			case "quota":
				u.SubscriptionQuotaState = "Reported"
			}
			s.Status.UsageObservation = u
			if err := k8s.Status().Update(c, s); !apierrors.IsInvalid(err) {
				t.Fatal("invalid provenance accepted", boundary, err)
			}
		})
	}
}
