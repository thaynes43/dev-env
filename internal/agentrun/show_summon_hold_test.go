package agentrun

import (
	"net/http"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Story 790, proof 5 (DESIGN-064 6.7 and 6.9): show prints a Queued summoned
// session's hold with its message, as it prints every condition; the message
// is the operator's QuotaHold text (internal/controller's
// TestSummonQuotaHoldMessageIsWrittenOnlyWhenItsNumbersChange pins it, and
// apiserver's TestSessionViewCarriesTheSummonHold carries it to the API). So
// show needed no change. The close that ends such a hold is shown the same
// way.
func TestShowPrintsAQueuedSummonedSessionsHold(t *testing.T) {
	const held = "held: quota: the 5-hour window is 81% used, past its line of 80% (reading taken at 2026-11-12T15:59:00Z)"
	const closed = "never admitted: Queued past its maxHold while held for quota, and closed by its onUnreported"
	for _, tc := range []struct{ reason, message, want string }{
		{"QuotaHold", held, "SummonAdmitted=False QuotaHold: " + held},
		{"QuotaHoldExpired", closed, "SummonAdmitted=False QuotaHoldExpired: " + closed},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			h := newHarness(t)
			s := testSession("curate-1", "Pending")
			s.Caller, s.Lane, s.Parent = "cigar-ns.cigar-curator", "curation", "cigar-ns/cigar-curator"
			s.Conditions = []apiv1.Condition{{Type: "SummonAdmitted", Status: "False", Reason: tc.reason, Message: tc.message}}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.URL.Path != apiv1.SessionPath(s.Name) {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				writeTestJSON(w, http.StatusOK, s)
			}
			h.mustRun(ExitOK, "show", s.Name)
			contains(t, "summoned hold", h.stdout.String(), "condition", tc.want)
		})
	}
}
