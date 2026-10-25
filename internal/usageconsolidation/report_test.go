package usageconsolidation

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestProjectPreservesSeparateSnapshotsAndRoundTrips(t *testing.T) {
	snapshot := allowanceFixture(t)
	report, err := Project(Input{AllowanceSnapshots: []providerallowance.ReadResponse{snapshot}}, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if report.Allowance.CurrentnessAtReportGeneration != Unknown || report.Allowance.AvailabilityRecommendation != Unknown || !reflect.DeepEqual(report.Allowance.Snapshots, []providerallowance.ReadResponse{snapshot}) {
		t.Fatalf("snapshot changed or assessed: %#v", report.Allowance)
	}
	snapshot.Items[0].Observation.Scope.Products[0] = "mutated"
	if report.Allowance.Snapshots[0].Items[0].Observation.Scope.Products[0] == "mutated" {
		t.Fatal("report aliases caller snapshot")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded, report) {
		t.Fatalf("JSON roundtrip: %v %#v", err, decoded)
	}
}

func TestProjectAttributionReplayConflictsLabelsAndPermutation(t *testing.T) {
	a := observation("session-a", "a", "requested-a")
	a.InputTokens = reported(0)
	a.ServedModels, a.ActualModelState, a.BillingBasis = []protocol.ModelUsage{model("served-a", reported(3), protocol.CostObservation{State: protocol.UsageReported, Amount: "0", Unit: "USD", Kind: protocol.UsageCLIEstimate})}, protocol.UsageProviderReported, protocol.UsageSubscription
	b := observation("session-a", "b", "requested-b")
	b.ServedModels, b.ActualModelState = []protocol.ModelUsage{model("served-b", reported(0), protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown})}, protocol.UsageProviderReported
	conflict := a
	conflict.ResultState = "Partial"
	first, err := Project(Input{InitialInvocations: []protocol.UsageObservation{a, a, conflict, b}, Associations: []UIDAssociation{{SessionUID: "session-a", Project: "p", Repository: "r", Task: "t"}}}, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Attribution.Rows) != 1 || first.Attribution.Rows[0].Observation.Source.AttemptID != b.Source.AttemptID {
		t.Fatalf("ambiguous attempt was not suppressed: %#v", first.Attribution.Rows)
	}
	if first.Attribution.Rows[0].Labels != (Labels{Project: "p", Repository: "r", Task: "t"}) {
		t.Fatal("exact association missing")
	}
	second, err := Project(Input{InitialInvocations: []protocol.UsageObservation{b, conflict, a, a}, Associations: []UIDAssociation{{SessionUID: "session-a", Project: "p", Repository: "r", Task: "t"}}}, DefaultLimits)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("permutation changed output: %v", err)
	}
	missing, err := Project(Input{InitialInvocations: []protocol.UsageObservation{b}}, DefaultLimits)
	if err != nil || missing.Attribution.Rows[0].Labels != unknownLabels() {
		t.Fatalf("missing labels: %v %#v", err, missing)
	}
	conflictingLabels, err := Project(Input{InitialInvocations: []protocol.UsageObservation{b}, Associations: []UIDAssociation{{SessionUID: "session-a", Project: "one", Repository: "r", Task: "t"}, {SessionUID: "session-a", Project: "two", Repository: "r", Task: "t"}}}, DefaultLimits)
	if err != nil || conflictingLabels.Attribution.Rows[0].Labels != unknownLabels() {
		t.Fatalf("conflicting labels: %v %#v", err, conflictingLabels)
	}
	row := first.Attribution.Rows[0].Observation
	if row.Source.RequestedModel != "requested-b" || len(row.ServedModels) != 1 || row.ServedModels[0].Model != "served-b" || row.ServedModels[0].InputTokens.Amount == nil || *row.ServedModels[0].InputTokens.Amount != 0 || row.ServedModels[0].OutputTokens.Amount != nil || row.ServedModels[0].Cost.State != protocol.UsageUnknown {
		t.Fatal("requested/actual or partial per-model provenance changed", row)
	}
}

func TestProjectBoundsAndRefusesInvalidObservation(t *testing.T) {
	if _, err := Project(Input{}, Limits{}); err == nil {
		t.Fatal("accepted zero limits")
	}
	if _, err := Project(Input{InitialInvocations: []protocol.UsageObservation{observation("session-a", "a", "m"), observation("session-b", "b", "m")}}, Limits{MaxAllowanceSnapshots: 1, MaxAllowanceItems: 1, MaxInitialInvocations: 1, MaxAssociations: 1}); err == nil {
		t.Fatal("accepted oversized input")
	}
	invalid := observation("session-a", "a", "m")
	invalid.ID = "wrong"
	if _, err := Project(Input{InitialInvocations: []protocol.UsageObservation{invalid}}, DefaultLimits); err == nil || !strings.Contains(err.Error(), "invalid initial invocation") {
		t.Fatalf("invalid observation accepted: %v", err)
	}
}

func observation(session, attemptLetter, requested string) protocol.UsageObservation {
	u := protocol.UnknownUsage()
	u.Source = protocol.UsageSource{SessionUID: session, PodUID: "pod-" + session, Provider: protocol.AgentClaude, RequestedModel: requested, AttemptID: strings.Repeat(attemptLetter, 64)}
	u.ID, u.ObservedAt, u.ResultState = protocol.UsageObservationID(u.Source), time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC), "Final"
	return u
}

func reported(amount int64) protocol.TokenObservation {
	return protocol.TokenObservation{State: protocol.UsageReported, Amount: &amount}
}

func model(name string, input protocol.TokenObservation, cost protocol.CostObservation) protocol.ModelUsage {
	unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
	return protocol.ModelUsage{Model: name, InputTokens: input, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: cost}
}

func allowanceFixture(t *testing.T) providerallowance.ReadResponse {
	t.Helper()
	raw, err := os.ReadFile("../providerallowance/testdata/read-interface-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		HTTP struct {
			Body json.RawMessage `json:"body"`
		} `json:"http"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	snapshot, err := providerallowance.DecodeReadResponse(fixture.HTTP.Body, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
