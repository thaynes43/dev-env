package agentrun

import (
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestShowCapturedUsage(t *testing.T) {
	h := newHarness(t)
	before := len(h.api.requests())
	h.mustRun(ExitUsage, "show", "parent", "--captured-usage")
	h.mustRun(ExitUsage, "show", "parent", "--captured-usage", "--once", "--json")
	if len(h.api.requests()) != before {
		t.Fatal("refused show called the API")
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Header.Get(apiv1.UsageCaptureHeader) != apiv1.UsageCaptureDirectV1 {
			t.Errorf("header %q", r.Header.Get(apiv1.UsageCaptureHeader))
		}
		writeTestJSON(w, http.StatusOK, apiv1.Session{Name: "parent", Phase: "Running"})
	}
	h.mustRun(ExitOK, "show", "parent", "--captured-usage", "-o", "json")
	if strings.Contains(h.stdout.String(), `"receipts"`) || !strings.Contains(h.stdout.String(), `"reason": "unavailable"`) {
		t.Fatalf("old server view: %s", h.stdout.String())
	}
	if len(h.api.requests()) != before+1 {
		t.Fatalf("requests %d", len(h.api.requests()))
	}
}
