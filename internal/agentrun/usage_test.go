package agentrun

import (
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestUsageShowUnknownExplicitZeroAndInitialProvenance(t *testing.T) {
	for _, mode := range []string{"missing", "legacy-zero", "legacy-nonzero", "pruned", "zero", "nonzero", "token-only"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			s := testSession("usage-show", "")
			u := protocol.UnknownUsage()
			u.Source = protocol.UsageSource{SessionUID: "original-session", PodUID: "original-pod", Provider: "claude", RequestedModel: "claude-opus-5-5", AttemptID: strings.Repeat("a", 64)}
			u.ID = protocol.UsageObservationID(u.Source)
			u.ObservedAt = testNow
			u.ResultState = "Final"
			zero := int64(0)
			u.InputTokens = protocol.TokenObservation{State: "Reported", Amount: &zero}
			switch mode {
			case "legacy-zero":
				s.Usage = &apiv1.Usage{CostUSD: "0", InputTokens: 7}
			case "legacy-nonzero":
				s.Usage = &apiv1.Usage{CostUSD: "99", InputTokens: 7}
			case "pruned":
				u.Cost = protocol.CostObservation{Amount: "0"}
				s.UsageObservation = &u
			case "zero", "nonzero":
				u.Cost = protocol.CostObservation{State: "Reported", Amount: "0", Unit: "USD", Kind: protocol.UsageCLIEstimate}
				if mode == "nonzero" {
					u.Cost.Amount = "1.25"
				}
				s.UsageObservation = &u
			case "token-only":
				u.Source.Provider = protocol.AgentCodex
				u.Source.RequestedModel = "gpt-6.1-sol"
				u.ID = protocol.UsageObservationID(u.Source)
				s.UsageObservation = &u
			}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) { writeTestJSON(w, http.StatusOK, s) }
			h.mustRun(ExitOK, "show", s.Name)
			out := h.stdout.String()
			if strings.Contains(out, "$") {
				t.Fatal("unqualified dollars", out)
			}
			if mode == "zero" || mode == "nonzero" {
				if !strings.Contains(out, u.Cost.Amount+" USD (CLI equivalent estimate)") {
					t.Fatal("explicit estimate lost", out)
				}
			} else if !strings.Contains(out, "Unknown") {
				t.Fatal("unavailable cost classified", out)
			}
			if s.UsageObservation != nil && u.Validate() == nil {
				contains(t, "typed usage", out, "Usage scope:", "Initial managed invocation", "Actual model:", "Unknown", "Input tokens:", "0", "Output tokens:", "Unknown", "Cache read tokens:", "Unknown", "Cache create tokens:", "Unknown", "Requested model:", u.Source.RequestedModel)
			}
		})
	}
}
