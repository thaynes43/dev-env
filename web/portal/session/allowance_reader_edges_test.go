package session

import (
	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
	"testing"
	"time"
)

func TestCachedAllowanceResetCannotEraseConflict(t *testing.T) {
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	current := syntheticCurrent("thirty-minute", "40")
	current.State = pa.CurrentConflict
	current.Value = pa.MeasureValue{State: pa.ValueUnknown}
	doc, trust, reg := syntheticAllowanceDoc([]pa.Measure{syntheticMeasure("thirty-minute", "thirty-minute", 30, "40", "2026-11-01T03:45:00Z", pa.ResetInstant)}, []pa.CurrentMeasure{current})
	got := claudeAllowance(t, newSyntheticReader(t, doc, trust, reg, AllowanceReadPolicy{MaxAge: time.Hour, Set: true}).Allowances(now))
	if got.Value != portal.AllowanceUnknown || got.Reason != "Conflicting allowance values" || len(got.Windows) != 0 {
		t.Fatalf("past reset erased conflict: %+v", got)
	}
}

func TestCachedAllowanceCannotTrustFutureFreshness(t *testing.T) {
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	for _, source := range []string{"item", "source-status", "before-observation"} {
		t.Run(source, func(t *testing.T) {
			doc, trust, reg := syntheticAllowanceDoc([]pa.Measure{syntheticMeasure("thirty-minute", "thirty-minute", 30, "40", "2026-11-01T06:00:00Z", pa.ResetInstant)}, []pa.CurrentMeasure{syntheticCurrent("thirty-minute", "40")})
			switch source {
			case "item":
				doc.Items[0].Freshness.AsOf = "2099-01-01T00:00:00Z"
			case "source-status":
				status := forgedStatus(pa.AvailabilityAvailable, trust)
				status.Freshness.AsOf = "2099-01-01T00:00:00Z"
				doc.SourceStatuses = []pa.SourceStatus{status}
			case "before-observation":
				doc.Items[0].Freshness.AsOf = "2026-11-01T03:00:00Z"
			}
			got := claudeAllowance(t, newSyntheticReader(t, doc, trust, reg, AllowanceReadPolicy{MaxAge: time.Hour, Set: true}).Allowances(now))
			if got.Value != portal.AllowanceUnknown || len(got.Windows) != 0 {
				t.Fatalf("inconsistent freshness became measured: %+v", got)
			}
		})
	}
}
