package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestUsageObservationPresenceAndIdentity(t *testing.T) {
	u := UnknownUsage()
	u.Source = UsageSource{SessionUID: "session", PodUID: "pod", Provider: AgentClaude, RequestedModel: "claude-opus-5-5", AttemptID: strings.Repeat("a", 64)}
	u.ID, u.ObservedAt = UsageObservationID(u.Source), time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	zero := int64(0)
	u.InputTokens = TokenObservation{State: UsageReported, Amount: &zero}
	u.Cost = CostObservation{State: UsageReported, Amount: "0", Unit: "USD", Kind: UsageCLIEstimate}
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	var decoded UsageObservation
	if json.Unmarshal(raw, &decoded) != nil || decoded.Validate() != nil || decoded.InputTokens.Amount == nil || *decoded.InputTokens.Amount != 0 || decoded.OutputTokens.Amount != nil {
		t.Fatal("explicit zero or independent absence lost")
	}
	for _, amount := range []string{"", "00", "0.0", "1.20", "1e-7", "-1", "NaN"} {
		decoded.Cost.Amount = amount
		if decoded.Validate() == nil {
			t.Fatal("noncanonical amount accepted", amount)
		}
	}
	decoded = u
	decoded.Source.AttemptID = strings.Repeat("b", 64)
	if decoded.Validate() == nil || UsageObservationID(decoded.Source) == u.ID {
		t.Fatal("attempt change retained an old event identity")
	}
}

func TestUsageMalformedMetadataDoesNotRejectHeartbeat(t *testing.T) {
	for _, raw := range []string{`{"inputTokens":{"amount":9223372036854775808}}`, `{"cost":{"amount":12}}`, `{"observedAt":"invalid"}`, `"invalid"`} {
		var st Status
		body := `{"session":"owned","agent":{"state":"busy"},"usageObservation":` + raw + `}`
		if err := json.Unmarshal([]byte(body), &st); err != nil || st.Agent.State != AgentBusy || st.UsageObservation == nil || st.UsageObservation.Validate() == nil {
			t.Fatal("usage type error rejected liveness", err)
		}
		h := sha256.Sum256([]byte(raw))
		if st.UsageObservation.MalformedDigest() != hex.EncodeToString(h[:]) {
			t.Fatal("malformed payload digest lost")
		}
		out, err := json.Marshal(st.UsageObservation)
		if err != nil || strings.Contains(string(out), "invalid") || strings.Contains(string(out), "9223372036854775808") {
			t.Fatal("raw malformed metadata retained", string(out), err)
		}
	}
}
