package allowancecollector

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver"
	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	uc "github.com/thaynes43/dev-env/internal/usagecollector"
)

func claudeStoredReport(t *testing.T, r *Runner) (pa.ReadItem, pa.SourceStatus, journal) {
	t.Helper()
	cm, err := r.slot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var doc pa.ReadResponse
	if json.Unmarshal([]byte(cm.Data[apiserver.AggregateAllowanceDataKey]), &doc) != nil {
		t.Fatal("decode report")
	}
	var item pa.ReadItem
	var status pa.SourceStatus
	for _, row := range doc.Items {
		if row.Observation.Provider == "anthropic" {
			item = row
		}
	}
	for _, row := range doc.SourceStatuses {
		if row.Provider == "anthropic" {
			status = row
		}
	}
	held, err := readJournal([]byte(cm.Data[StateKey]), []byte(cm.Data[apiserver.AggregateAllowanceDataKey]), r.Config, r.now())
	if err != nil {
		t.Fatal(err)
	}
	return item, status, held
}

func replaceClaudeLimits(t *testing.T, receipt *uc.RemoteReceipt, limits []pa.ClaudeUsageLimit) {
	t.Helper()
	for i := range receipt.Captures {
		if receipt.Captures[i].Provider != "anthropic" {
			continue
		}
		var payload pa.ClaudeUsageCapture
		if json.Unmarshal(receipt.Captures[i].Payload, &payload) != nil {
			t.Fatal("decode capture")
		}
		payload.Limits = limits
		var err error
		receipt.Captures[i].Payload, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeUsableThenRepeatedEmptyRetainsOriginalAcrossRestart(t *testing.T) {
	r, remote, now := collectorFixture(t)
	if err := r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, previous, _ := claudeStoredReport(t, r)
	if previous.LastGood == nil {
		t.Fatal("fixture missing usable history")
	}
	original, _ := json.Marshal(before.Observation)
	for _, limits := range [][]pa.ClaudeUsageLimit{nil, {}, nil} {
		*now = now.Add(10 * time.Minute)
		refreshHeartbeat(t, r, *now)
		remote.run = func(request uc.RemoteRequest) ([]byte, error) {
			receipt := successfulReceipt(t, request, *now)
			replaceClaudeLimits(t, &receipt, limits)
			*now = now.Add(2 * time.Second)
			return json.Marshal(receipt)
		}
		restarted := *r
		r = &restarted
		if err := r.Step(context.Background()); err != nil {
			t.Fatal("empty envelope publication", err)
		}
		item, status, held := claudeStoredReport(t, r)
		if status.LastAttempt.Result != pa.AttemptSuccess || status.Availability != pa.AvailabilityUnavailable || status.LastGood == nil || *status.LastGood != *previous.LastGood {
			t.Fatalf("empty completion replaced original last good or claimed available: attempt=%s availability=%s previous=%+v current=%+v", status.LastAttempt.Result, status.Availability, previous.LastGood, status.LastGood)
		}
		got, _ := json.Marshal(item.Observation)
		if string(got) != string(original) || item.ReceivedAt != before.ReceivedAt || *status.LastAttempt.CompletedAt == status.LastGood.ReceivedAt {
			t.Fatal("empty envelope borrowed or refreshed history")
		}
		for _, value := range item.CurrentMeasures {
			if value.State == pa.CurrentKnown || value.State == pa.CurrentUncapped || value.Value.Decimal != nil {
				t.Fatal("old quota became current after empty completion")
			}
		}
		count, retained := 0, false
		for _, capture := range held.Captures {
			if capture.Provider == "anthropic" {
				count++
				retained = retained || capture.CapturedAt == before.Observation.CapturedAt && capture.ReceivedAt == before.ReceivedAt
			}
		}
		if count != 2 || !retained {
			t.Fatal("repeated empties evicted original durable quota")
		}
	}
	// A newly reported zero is genuine quota data and replaces the old sample.
	*now = now.Add(10 * time.Minute)
	refreshHeartbeat(t, r, *now)
	remote.run = func(request uc.RemoteRequest) ([]byte, error) {
		receipt := successfulReceipt(t, request, *now)
		replaceClaudeLimits(t, &receipt, []pa.ClaudeUsageLimit{{Kind: "weekly_all", Group: "weekly", Percent: json.Number("0"), Severity: "normal", IsActive: true}})
		*now = now.Add(2 * time.Second)
		return json.Marshal(receipt)
	}
	if err := r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	item, status, _ := claudeStoredReport(t, r)
	if status.LastGood == nil || status.LastGood.CapturedAt == previous.LastGood.CapturedAt || len(item.Observation.Measures) == 0 || item.Observation.Measures[0].Value.Decimal == nil || *item.Observation.Measures[0].Value.Decimal != "0" {
		t.Fatal("known zero was treated as empty")
	}
}

func TestClaudeEmptyOnlyHasNoLastGood(t *testing.T) {
	r, remote, now := collectorFixture(t)
	remote.run = func(request uc.RemoteRequest) ([]byte, error) {
		receipt := successfulReceipt(t, request, *now)
		replaceClaudeLimits(t, &receipt, nil)
		*now = now.Add(2 * time.Second)
		return json.Marshal(receipt)
	}
	if err := r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	item, status, _ := claudeStoredReport(t, r)
	if status.LastGood != nil || status.Availability != pa.AvailabilityUnavailable || len(item.Observation.Measures) != 0 {
		t.Fatal("empty history became a usable last good")
	}
}

func TestClaudeLegacyEmptyLastGoodDoesNotBlockNaturalPublication(t *testing.T) {
	for _, array := range []bool{false, true} {
		for _, failNext := range []bool{false, true} {
			r, remote, now := collectorFixture(t)
			var limits []pa.ClaudeUsageLimit
			if array {
				limits = []pa.ClaudeUsageLimit{}
			}
			remote.run = func(request uc.RemoteRequest) ([]byte, error) {
				receipt := successfulReceipt(t, request, *now)
				replaceClaudeLimits(t, &receipt, limits)
				*now = now.Add(2 * time.Second)
				return json.Marshal(receipt)
			}
			if err := r.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			cm, err := r.slot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var legacy pa.ReadResponse
			if json.Unmarshal([]byte(cm.Data[apiserver.AggregateAllowanceDataKey]), &legacy) != nil {
				t.Fatal("legacy report")
			}
			var bad *pa.ReadItem
			for i := range legacy.Items {
				if legacy.Items[i].Observation.Provider == "anthropic" {
					bad = &legacy.Items[i]
					if array {
						bad.Observation.Availability = pa.AvailabilityPartial
					}
				}
			}
			for i := range legacy.SourceStatuses {
				if legacy.SourceStatuses[i].Provider == "anthropic" {
					legacy.SourceStatuses[i].LastGood = &pa.LastGood{ObservationID: bad.Observation.ObservationID, CapturedAt: bad.Observation.CapturedAt, ProviderObservedAt: bad.Observation.ProviderObservedAt, ReceivedAt: bad.ReceivedAt}
					if array {
						legacy.SourceStatuses[i].Availability = pa.AvailabilityPartial
					}
				}
			}
			body, _ := json.Marshal(legacy)
			cm.Data[apiserver.AggregateAllowanceDataKey] = string(body)
			var held journal
			if json.Unmarshal([]byte(cm.Data[StateKey]), &held) != nil {
				t.Fatal("legacy journal")
			}
			held.ReportSHA = digest(body)
			state, _ := json.Marshal(held)
			cm.Data[StateKey] = string(state)
			if r.Client.Update(context.Background(), cm) != nil {
				t.Fatal("seed old-format state")
			}
			*now = now.Add(10 * time.Minute)
			refreshHeartbeat(t, r, *now)
			remote.run = func(request uc.RemoteRequest) ([]byte, error) {
				receipt := successfulReceipt(t, request, *now)
				replaceClaudeLimits(t, &receipt, limits)
				if failNext {
					for i := range receipt.Attempts {
						if receipt.Attempts[i].Provider == "anthropic" {
							receipt.Attempts[i].Success = false
						}
					}
					for i := range receipt.Captures {
						if receipt.Captures[i].Provider == "anthropic" {
							receipt.Captures = append(receipt.Captures[:i], receipt.Captures[i+1:]...)
							receipt.Bindings = append(receipt.Bindings[:i], receipt.Bindings[i+1:]...)
							break
						}
					}
				}
				*now = now.Add(2 * time.Second)
				return json.Marshal(receipt)
			}
			if err := r.Step(context.Background()); err != nil {
				t.Fatalf("legacy empty metadata blocked natural publication: array=%v failure=%v %v", array, failNext, err)
			}
			_, status, _ := claudeStoredReport(t, r)
			if status.LastGood != nil || status.Availability != pa.AvailabilityUnavailable {
				t.Fatal("legacy empty metadata still claims last good")
			}
		}
	}
}

func TestClaudeKnownZeroLastGoodCannotBeErased(t *testing.T) {
	r, remote, now := collectorFixture(t)
	remote.run = func(request uc.RemoteRequest) ([]byte, error) {
		receipt := successfulReceipt(t, request, *now)
		replaceClaudeLimits(t, &receipt, []pa.ClaudeUsageLimit{{Kind: "weekly_all", Group: "weekly", Percent: json.Number("0"), Severity: "normal", IsActive: true}})
		*now = now.Add(2 * time.Second)
		return json.Marshal(receipt)
	}
	if err := r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	cm, err := r.slot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := cm.Data[apiserver.AggregateAllowanceDataKey]
	var doc pa.ReadResponse
	if json.Unmarshal([]byte(original), &doc) != nil {
		t.Fatal("report")
	}
	doc.SnapshotRevision += "-erased"
	for i := range doc.SourceStatuses {
		if doc.SourceStatuses[i].Provider == "anthropic" {
			doc.SourceStatuses[i].LastGood = nil
			doc.SourceStatuses[i].Availability = pa.AvailabilityUnavailable
		}
	}
	body, _ := json.Marshal(doc)
	store := apiserver.AggregateAllowanceReportStore{Writer: r.Client, Live: r.Live, Namespace: apiserver.AggregateAllowanceNamespace, Name: apiserver.AggregateAllowanceName, DataKey: apiserver.AggregateAllowanceDataKey, Contexts: r.Config.trust.Contexts, Registry: r.Config.trust.Registry, MaxAge: time.Duration(r.Config.trust.MaxAge) * time.Second}
	if err := store.Put(context.Background(), body, *now); !errors.Is(err, apiserver.ErrAggregateRejected) {
		t.Fatal("usable zero history erasure accepted", err)
	}
	after, _ := r.slot(context.Background())
	if after.Data[apiserver.AggregateAllowanceDataKey] != original {
		t.Fatal("refused erasure mutated report")
	}
}
