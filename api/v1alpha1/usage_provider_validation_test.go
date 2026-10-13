package v1alpha1_test

import (
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func schemaProviderUsage() *v1alpha1.UsageObservationStatus {
	u := schemaUsage()
	u.Version, u.Provenance, u.BillingBasis, u.ActualModelState = 2, "ProviderReported", "Subscription", "ProviderReported"
	n := int64(7)
	unknown := v1alpha1.TokenObservationStatus{State: "Unknown"}
	u.ServedModels = []v1alpha1.ModelUsageStatus{
		{Model: "claude-haiku-4-5", InputTokens: v1alpha1.TokenObservationStatus{State: "Reported", Amount: &n}, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: v1alpha1.CostObservationStatus{State: "Reported", Amount: "0.05", Unit: "USD", Kind: "ProviderCLIEquivalentEstimate"}},
		{Model: "claude-opus-5-5", InputTokens: unknown, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: v1alpha1.CostObservationStatus{State: "Unknown", Kind: "Unknown"}},
	}
	return u
}

func TestUsageSchemaAcceptsBothVersions(t *testing.T) {
	for name, u := range map[string]*v1alpha1.UsageObservationStatus{"version 1": schemaUsage(), "version 2": schemaProviderUsage()} {
		t.Run(name, func(t *testing.T) {
			c := ctx(t)
			s := taskSession()
			if err := k8s.Create(c, s); err != nil {
				t.Fatal(err)
			}
			s.Status.UsageObservation = u
			if err := k8s.Status().Update(c, s); err != nil {
				t.Fatal("valid observation refused", err)
			}
			if u.Version == 2 {
				got := s.Status.UsageObservation
				if len(got.ServedModels) != 2 || got.ServedModels[0].InputTokens.Amount == nil || *got.ServedModels[0].InputTokens.Amount != 7 || got.ServedModels[1].InputTokens.Amount != nil || got.BillingBasis != "Subscription" {
					t.Fatal("served model presence was not stored", got)
				}
				changed := s.DeepCopy()
				changed.Status.UsageObservation.ServedModels = changed.Status.UsageObservation.ServedModels[:1]
				if err := k8s.Status().Update(c, changed); !apierrors.IsInvalid(err) {
					t.Fatal("retained served models changed", err)
				}
			}
		})
	}
}

func TestUsageSchemaRefusesInconsistentVersionTwo(t *testing.T) {
	for name, mutate := range map[string]func(*v1alpha1.UsageObservationStatus){
		"version 1 with provenance": func(u *v1alpha1.UsageObservationStatus) { u.Version = 1 },
		"no provenance":             func(u *v1alpha1.UsageObservationStatus) { u.Provenance = "" },
		"no billing basis":          func(u *v1alpha1.UsageObservationStatus) { u.BillingBasis = "" },
		"invented billing":          func(u *v1alpha1.UsageObservationStatus) { u.BillingBasis = "APIBilling" },
		"state without models":      func(u *v1alpha1.UsageObservationStatus) { u.ServedModels = nil },
		"models without state":      func(u *v1alpha1.UsageObservationStatus) { u.ActualModelState = "Unknown" },
		"duplicate model":           func(u *v1alpha1.UsageObservationStatus) { u.ServedModels[1].Model = u.ServedModels[0].Model },
		"invalid model id":          func(u *v1alpha1.UsageObservationStatus) { u.ServedModels[0].Model = "claude haiku" },
		"overlong model id":         func(u *v1alpha1.UsageObservationStatus) { u.ServedModels[0].Model = strings.Repeat("m", 257) },
		"model unknown with amount": func(u *v1alpha1.UsageObservationStatus) { n := int64(0); u.ServedModels[1].InputTokens.Amount = &n },
		"model cost not estimate": func(u *v1alpha1.UsageObservationStatus) {
			u.ServedModels[0].Cost.Kind = "ProviderAPIBilling"
		},
		"too many models": func(u *v1alpha1.UsageObservationStatus) {
			for i := 0; i < 7; i++ {
				m := u.ServedModels[1]
				m.Model = "model-" + string(rune('a'+i))
				u.ServedModels = append(u.ServedModels, m)
			}
		},
		"version 3": func(u *v1alpha1.UsageObservationStatus) { u.Version = 3 },
		"codex model cost": func(u *v1alpha1.UsageObservationStatus) {
			u.Source.Provider, u.Cost = "codex", v1alpha1.CostObservationStatus{State: "Unknown", Kind: "Unknown"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := ctx(t)
			s := taskSession()
			if err := k8s.Create(c, s); err != nil {
				t.Fatal(err)
			}
			u := schemaProviderUsage()
			mutate(u)
			s.Status.UsageObservation = u
			if err := k8s.Status().Update(c, s); !apierrors.IsInvalid(err) {
				t.Fatal("inconsistent version 2 observation accepted", name, err)
			}
		})
	}
}
