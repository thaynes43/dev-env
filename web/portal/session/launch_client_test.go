package session

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestLaunchClientMapsOperatorOutcomes(t *testing.T) {
	now := time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
	token := filepath.Join(t.TempDir(), "token")
	if os.WriteFile(token, []byte(fixtureOperatorToken(apiv1.TokenAudience, now.Add(time.Hour))), 0600) != nil {
		t.Fatal("fixture token")
	}
	req := apiv1.PortalLaunchRequest{RequestID: launchTestID, ActorRef: "actor-000000000001", Repo: "dev-env", Mode: "task", Model: "claude-opus-5-5", Prompt: "a <b> & c"}
	good := `{"requestID":"` + launchTestID + `","sessionName":"dev-env-1027-120000","sessionUID":"uid-new-session","phase":"Pending","observedAt":"2026-10-23T16:00:00Z"}`
	apiError := func(code, field string) string {
		return `{"error":{"code":"` + code + `","message":"x","fields":[{"field":"` + field + `","message":"model claude-sonnet-5-5 takes low"}]}}`
	}
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		media   string
		want    launchOutcome
		retries bool
	}{
		{"created", 201, good, "application/json", launchCreated, false},
		{"existing", 200, good, "application/json", launchCreated, false},
		{"limit", 429, apiError("limit_exceeded", ""), "application/json", launchLimit, false},
		{"forbidden", 403, apiError("forbidden", ""), "application/json", launchUnavailable, false},
		{"invalid 422", 422, apiError("invalid", "effort"), "application/json", launchInvalid, false},
		{"invalid 400", 400, apiError("bad_request", ""), "application/json", launchInvalid, false},
		{"conflict", 409, apiError("conflict", ""), "application/json", launchConflict, false},
		{"unauthorized", 401, apiError("unauthorized", ""), "application/json", launchUnavailable, false},
		{"server error", 500, apiError("internal", ""), "application/json", launchUnknown, false},
		{"not found", 404, `404 page not found`, "text/plain", launchUnavailable, false},
		{"not found as json", 404, apiError("not_found", ""), "application/json", launchUnavailable, false},
		{"receipt for another request", 201, strings.Replace(good, launchTestID, strings.Repeat("A", 43), 1), "application/json", launchUnknown, false},
		{"receipt with an extra key", 201, strings.Replace(good, `"phase"`, `"prompt":"x","phase"`, 1), "application/json", launchUnknown, false},
		{"receipt with a duplicate key", 201, strings.Replace(good, `"phase":"Pending"`, `"phase":"Pending","phase":"Pending"`, 1), "application/json", launchUnknown, false},
		{"receipt with a bad name", 201, strings.Replace(good, "dev-env-1027-120000", "Bad/Name", 1), "application/json", launchUnknown, false},
		{"receipt with a bad uid", 201, strings.Replace(good, "uid-new-session", "a b", 1), "application/json", launchUnknown, false},
		{"receipt without a time", 201, strings.Replace(good, `"observedAt":"2026-10-23T16:00:00Z"`, `"observedAt":"0001-01-01T00:00:00Z"`, 1), "application/json", launchUnknown, false},
		{"wrong media", 201, good, "text/plain", launchUnknown, false},
		{"oversize", 201, strings.Repeat("x", 16*1024+1), "application/json", launchUnknown, false},
		{"redirect", 307, "", "application/json", launchUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert, ca := mintNamed(t, operatorPortalHost)
			calls := 0
			server := serveTLS(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != apiv1.PortalLaunchPath || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureOperatorToken(apiv1.TokenAudience, now.Add(time.Hour)) {
					t.Error("request lost its fixed binding")
				}
				buf := make([]byte, 4096)
				n, _ := r.Body.Read(buf)
				if !strings.Contains(string(buf[:n]), `"prompt":"a <b> & c"`) {
					t.Errorf("prompt was HTML-escaped on the wire: %s", buf[:n])
				}
				w.Header().Set("Content-Type", tc.media)
				if tc.status == 307 {
					w.Header().Set("Location", "https://elsewhere.example/")
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			c, err := newLaunchClient(token, ca, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: server}))
			if err != nil {
				t.Fatal("client config")
			}
			got := c.launch(context.Background(), req)
			if got.Outcome != tc.want || calls != 1 {
				t.Fatalf("outcome %q after %d calls, want %q once", got.Outcome, calls, tc.want)
			}
			if tc.want == launchCreated && (got.Receipt.SessionName != "dev-env-1027-120000" || got.Receipt.SessionUID != "uid-new-session" || got.Receipt.Phase != "Pending") {
				t.Fatalf("receipt %+v", got.Receipt)
			}
			if tc.name == "invalid 422" && (len(got.Fields) != 1 || got.Fields[0].Field != "effort") {
				t.Fatalf("fields %+v", got.Fields)
			}
		})
	}
}

func TestLaunchClientIsUnavailableBeforeDispatchAndUnknownAfter(t *testing.T) {
	now := time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
	req := apiv1.PortalLaunchRequest{RequestID: launchTestID}
	if got := (*launchClient)(nil).launch(context.Background(), req); got.Outcome != launchUnavailable {
		t.Fatalf("nil client: %q", got.Outcome)
	}
	_, ca := mintNamed(t, operatorPortalHost)
	// No readable credential: nothing was sent.
	c, err := newLaunchClient(filepath.Join(t.TempDir(), "missing"), ca, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.launch(context.Background(), req); got.Outcome != launchUnavailable {
		t.Fatalf("missing credential: %q", got.Outcome)
	}
	// A dispatch that fails on the wire may have reached the operator.
	token := filepath.Join(t.TempDir(), "token")
	if os.WriteFile(token, []byte(fixtureOperatorToken(apiv1.TokenAudience, now.Add(time.Hour))), 0600) != nil {
		t.Fatal("fixture token")
	}
	c, err = newLaunchClient(token, ca, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.launch(context.Background(), req); got.Outcome != launchUnknown {
		t.Fatalf("unreachable operator: %q", got.Outcome)
	}
	if _, err := newLaunchClient("", ca, func() time.Time { return now }, nil); err == nil {
		t.Fatal("client without a token path was accepted")
	}
}
