package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestGrantDecisionUsesReviewedSummary(t *testing.T) {
	h := newHarness(t)
	path := apiv1.GrantPath("grant-one") + "/decision"
	summary := `{"grant":{"name":"grant-one","session":"child","type":"egress","ttl":"1h","reason":"inspect","createdAt":"2026-10-26T00:00:00Z","phase":"Pending"},"binding":{"grantUID":"g","sessionUID":"s","podUID":"p","generation":1,"scopeHash":"abc"},"resourceVersion":"42"}`
	serveRaw(h, map[string]func(*http.Request) (int, string){path: func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet { return http.StatusOK, summary }
		return http.StatusAccepted, `{"status":"submitted"}`
	}})
	h.mustRun(ExitOK, "grant", "decision", "show", "grant-one")
	contains(t, "summary", h.stdout.String(), "grant-one", "scopeHash", "resourceVersion")
	h.stdout.Reset()
	h.env.Stdin = strings.NewReader(summary)
	h.mustRun(ExitOK, "grant", "decision", "submit", "grant-one", "--decision", "approve", "--relay-ref", "question/1-answer/1")
	contains(t, "stdout", h.stdout.String(), "submitted", "check grant status")
	var body decisionSubmission
	if err := json.Unmarshal(h.api.requests()[1].body, &body); err != nil { t.Fatal(err) }
	if body.Decision != "approve" || body.ResourceVersion != "42" || body.RelayRef != "question/1-answer/1" || !strings.Contains(string(body.Binding), `"podUID":"p"`) { t.Fatal("reviewed binding was not sent") }
	h.env.Stdin = strings.NewReader(summary)
	h.mustRun(ExitFailed, "grant", "decision", "submit", "grant-other", "--decision", "approve")
	if len(h.api.requests()) != 2 { t.Fatal("mismatched summary reached API") }
}
