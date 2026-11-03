package allowancecollector

import (
	"context"
	"encoding/json"
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
			t.Fatalf("empty completion replaced original last good or claimed available: attempt=%s availability=%s previous=%+v current=%+v", status.LastAttempt.Result,status.Availability,previous.LastGood,status.LastGood)
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
