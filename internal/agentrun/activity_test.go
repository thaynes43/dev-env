package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// declare-activity keeps v1's command and flags (D-66).
func TestDeclareActivity(t *testing.T) {
	h := newHarness(t)
	var got apiv1.DeclareActivityRequest
	act := apiv1.Activity{Name: "act-230000-123456", Description: "drain talosw03", Scope: []string{"talosw03"}, DeclaredBy: "client/dev/dev-env",
		ExpiresAt: testNow.Add(45 * time.Minute)}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiv1.ActivitiesPath:
			got = decodeAs[apiv1.DeclareActivityRequest](t, body)
			writeTestJSON(w, http.StatusCreated, act)
		case r.Method == http.MethodGet && r.URL.Path == apiv1.ActivitiesPath:
			writeTestJSON(w, http.StatusOK, apiv1.ActivityList{Activities: []apiv1.Activity{act}})
		case r.Method == http.MethodDelete && r.URL.Path == apiv1.ActivityPath(act.Name):
			writeTestJSON(w, http.StatusOK, act)
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no")
		}
	}
	h.mustRun(ExitOK, "declare-activity", "start", "drain talosw03", "--scope", "talosw03, rook-ceph", "--ttl", "1h")
	if got.Description != "drain talosw03" || strings.Join(got.Scope, ",") != "talosw03,rook-ceph" || got.TTL != "1h" {
		t.Errorf("request %+v", got)
	}
	contains(t, "stdout", h.stdout.String(), "declared act-230000-123456", "declare-activity end act-230000-123456")
	h.mustRun(ExitOK, "declare-activity", "list")
	contains(t, "stdout", h.stdout.String(), "act-230000-123456  45m0s  talosw03", "drain talosw03")
	h.mustRun(ExitOK, "declare-activity", "end", "act-230000-123456")
	contains(t, "stdout", h.stdout.String(), "ended act-230000-123456")
	h.mustRun(ExitUsage, "declare-activity", "start", "no scope")
	h.mustRun(ExitUsage, "declare-activity", "start", "--scope", "a")
	h.mustRun(ExitUsage, "declare-activity", "end")
	h.mustRun(ExitUsage, "declare-activity", "fly")
}

func decodeAs[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
