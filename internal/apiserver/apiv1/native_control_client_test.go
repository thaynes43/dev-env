package apiv1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNativeIntentConsumeHTTPSExactReceiptWithoutReplay(t *testing.T) {
	for _, scenario := range []string{"ok", "wrong-hash", "wrong-binding", "missing-consumed", "case-alias", "duplicate", "lost-ack", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			r := NativeControlRequest{Binding: TaskBudgetBinding{TaskUID: "t", HostID: "h", PodUID: "p", Epoch: 1, Deadline: time.Now().Add(time.Minute)}, ID: "once", Operation: "turn/start", ThreadID: "thread", Text: "owner"}
			calls := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls++
				if request.URL.Path != "/v1/task-budgets/t/native-consume" || request.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("wrong endpoint/auth")
				}
				if scenario == "lost-ack" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				if scenario == "redirect" {
					http.Redirect(w, request, "/other", http.StatusTemporaryRedirect)
					return
				}
				permit := NativeControlPermit{Binding: r.Binding, ID: r.ID, RequestSHA256: NativeControlDigest(r), Consumed: true}
				if scenario == "wrong-hash" {
					permit.RequestSHA256 = strings.Repeat("0", 64)
				}
				if scenario == "wrong-binding" {
					permit.Binding.PodUID = "other"
				}
				body, _ := json.Marshal(permit)
				if scenario == "missing-consumed" {
					body = []byte(strings.ReplaceAll(string(body), `,"consumed":true`, ""))
				}
				if scenario == "case-alias" {
					body = []byte(strings.ReplaceAll(string(body), `"consumed"`, `"Consumed"`))
				}
				if scenario == "duplicate" {
					body = []byte(strings.TrimSuffix(string(body), "}") + `,"consumed":true}`)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer srv.Close()
			g := &HTTPTaskBudgetGate{BaseURL: srv.URL, Client: srv.Client(), Token: func(context.Context) (string, error) { return "fixture", nil }}
			err := g.ConsumeNativeControl(context.Background(), r)
			if (err == nil) != (scenario == "ok") {
				t.Fatal(scenario, err)
			}
			if calls != 1 {
				t.Fatal("consume replayed", calls)
			}
		})
	}
}
