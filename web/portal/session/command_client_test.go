package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestCommandClientFixedDestinationAndStrictReceipt(t *testing.T) {
	now := time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
	token := filepath.Join(t.TempDir(), "token")
	if os.WriteFile(token, []byte(fixtureOperatorToken(apiv1.TokenAudience, now.Add(time.Hour))), 0600) != nil {
		t.Fatal("fixture token")
	}
	req := apiv1.PortalMessageRequest{RequestID: "request-0000000001", Project: "dev-env", SessionName: "agent-a", SessionUID: "uid-a", Text: "synthetic", ActorRef: "actor-000000000001"}
	good := `{"requestID":"request-0000000001","sessionName":"agent-a","sessionUID":"uid-a","resolvedPodUID":"pod-a","observedAt":"2026-10-23T16:00:00Z","transportStatus":"TransportDelivered","reason":"delivered"}`
	for _, mode := range []string{"valid", "duplicate", "mismatch", "bad-status", "wrong-reason", "redirect", "oversize", "bad-media"} {
		t.Run(mode, func(t *testing.T) {
			cert, ca := mintNamed(t, operatorPortalHost)
			calls := 0
			server := serveTLS(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != apiv1.PortalSessionMessagesPath || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureOperatorToken(apiv1.TokenAudience, now.Add(time.Hour)) {
					t.Error("request lost fixed binding")
				}
				w.Header().Set("Content-Type", "application/json")
				body := good
				switch mode {
				case "duplicate":
					body = strings.Replace(body, `"sessionUID":"uid-a"`, `"sessionUID":"uid-a","sessionUID":"uid-a"`, 1)
				case "mismatch":
					body = strings.Replace(body, "uid-a", "uid-b", 1)
				case "bad-status":
					body = strings.Replace(body, "TransportDelivered", "Pending", 1)
				case "wrong-reason":
					body = strings.Replace(body, "delivered", "SECRET-ERROR", 1)
				case "redirect":
					w.Header().Set("Location", "https://elsewhere.example/")
					w.WriteHeader(307)
					return
				case "oversize":
					body = strings.Repeat("x", 16*1024+1)
				case "bad-media":
					w.Header().Set("Content-Type", "text/plain")
				}
				_, _ = fmt.Fprint(w, body)
			}))
			c, err := newCommandClient(token, ca, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: server}))
			if err != nil {
				t.Fatal("client config")
			}
			receipt, err := c.send(context.Background(), req)
			if mode == "valid" {
				if err != nil || receipt.TransportStatus != apiv1.PortalTransportDelivered {
					t.Fatal("valid receipt refused")
				}
			} else if err == nil {
				t.Fatal("invalid response accepted")
			}
			if calls != 1 {
				t.Fatal("command retried or redirected")
			}
		})
	}
}

func TestCommandClientEncodesMaximumHTMLText(t *testing.T) {
	now := time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
	token := filepath.Join(t.TempDir(), "token")
	if os.WriteFile(token, []byte(fixtureOperatorToken(apiv1.TokenAudience, now.Add(time.Hour))), 0600) != nil {
		t.Fatal("fixture token")
	}
	text := strings.Repeat("&", apiv1.MaxMessageBytes-2) + "<>"
	request := apiv1.PortalMessageRequest{RequestID: "request-0000000001", Project: "dev-env", SessionName: "agent-a", SessionUID: "uid-a", Text: text, ActorRef: "actor-000000000001"}
	cert, ca := mintNamed(t, operatorPortalHost)
	calls := 0
	server := serveTLS(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error("request read failed")
			return
		}
		var got apiv1.PortalMessageRequest
		if len(data) <= 2*apiv1.MaxMessageBytes+2048 || json.Unmarshal(data, &got) != nil || got.Text != text {
			t.Error("HTML-escaped request did not preserve maximum text")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"requestID":"request-0000000001","sessionName":"agent-a","sessionUID":"uid-a","resolvedPodUID":"pod-a","observedAt":"2026-10-23T16:00:00Z","transportStatus":"TransportDelivered","reason":"delivered"}`)
	}))
	client, err := newCommandClient(token, ca, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: server}))
	if err != nil {
		t.Fatal("client config")
	}
	receipt, err := client.send(context.Background(), request)
	if err != nil || receipt.TransportStatus != apiv1.PortalTransportDelivered || calls != 1 {
		t.Fatal("maximum encoded request failed")
	}
}
