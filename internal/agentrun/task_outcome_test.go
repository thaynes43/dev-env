package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestShowRetainedTaskOutcome(t *testing.T) {
	for _, phase := range []string{"Reclaimed", "Suspended", "Running"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			s := testSession("outcome-task", "")
			s.Phase = phase
			raw, _ := json.Marshal(s)
			var obj map[string]any
			_ = json.Unmarshal(raw, &obj)
			obj["taskOutcome"] = map[string]any{"sessionUID": "original-session", "podUID": "original-pod", "record": map[string]any{"summary": "Ready for lead review", "prURLs": []string{"https://github.com/thaynes43/dev-env/pull/123"}, "headSHAs": []string{strings.Repeat("a", 40)}}}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.URL.Path != apiv1.SessionPath(s.Name) {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				writeTestJSON(w, http.StatusOK, obj)
			}
			h.mustRun(ExitOK, "show", s.Name)
			contains(t, "retained outcome", h.stdout.String(), phase, "Ready for lead review", "https://github.com/thaynes43/dev-env/pull/123", strings.Repeat("a", 40))
		})
	}
}
