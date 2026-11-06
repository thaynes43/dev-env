package session

import (
	"net/http/httptest"
	"testing"
)

// OP480: every session route's page, form and refusal sets the referrer
// policy through authHeaders. same-origin makes browsers send the page origin
// on the portal's own form posts (Fetch Standard, "append a request Origin
// header"), so the strict origin checks accept them, and sends no Referer to
// other origins.
func TestAuthHeadersReferrerPolicySameOrigin(t *testing.T) {
	w := httptest.NewRecorder()
	authHeaders(w)
	if got := w.Header().Values("Referrer-Policy"); len(got) != 1 || got[0] != "same-origin" {
		t.Fatalf("Referrer-Policy %q", got)
	}
}
