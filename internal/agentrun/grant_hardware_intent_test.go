package agentrun

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func syntheticHardwareIntent() apiv1.HardwareIntentRequest {
	return apiv1.HardwareIntentRequest{GrantUID: "11111111-1111-1111-1111-111111111111", Digest: strings.Repeat("A", 43), Operation: "firmware-update", Host: "host-a.example", Device: "pci-0000:01:00.0", Firmware: "sha256:abcd", TomRef: "msg-tom-100"}
}

func TestHardwareIntentClientValidatesBeforeAuthentication(t *testing.T) {
	for _, edit := range []func(*apiv1.HardwareIntentRequest){
		func(r *apiv1.HardwareIntentRequest) { r.TomRef = "" },
		func(r *apiv1.HardwareIntentRequest) { r.TomRef = "https://example/?secret=x" },
		func(r *apiv1.HardwareIntentRequest) { r.GrantUID = "other" },
		func(r *apiv1.HardwareIntentRequest) { r.Digest = "short" },
		func(r *apiv1.HardwareIntentRequest) { r.Operation = "reboot\nall" },
		func(r *apiv1.HardwareIntentRequest) { r.Host = "*" },
		func(r *apiv1.HardwareIntentRequest) { r.Device = "" },
		func(r *apiv1.HardwareIntentRequest) { r.Firmware = "" },
	} {
		h := newHarness(t)
		req := syntheticHardwareIntent()
		edit(&req)
		a := &app{env: h.env}
		reads := 0
		a.env.Getenv = func(string) string { reads++; return "" }
		err := a.grantHardwareIntent(context.Background(), []string{"grant-a", "--grant-uid", req.GrantUID, "--digest", req.Digest, "--operation", req.Operation, "--host", req.Host, "--device", req.Device, "--firmware", req.Firmware, "--tom-ref", req.TomRef})
		if err == nil || reads != 0 || len(h.api.requests()) != 0 {
			t.Fatalf("invalid intent crossed authentication boundary: err=%v reads=%d requests=%d", err, reads, len(h.api.requests()))
		}
	}
}

func TestHardwareIntentClientCorrelatesSingleAuthenticatedReply(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-target", "wrong-grant", "missing-actor", "pending", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			req := syntheticHardwareIntent()
			serveRaw(h, map[string]func(*http.Request) (int, string){apiv1.GrantHardwareIntentPath("grant-a"): func(r *http.Request) (int, string) {
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Error("intent did not retain authenticated POST")
				}
				out := apiv1.HardwareIntentReceipt{HardwareIntentRequest: req, SessionUID: "session-a", PodUID: "pod-a", RecordedAt: testNow}
				status := http.StatusCreated
				switch mode {
				case "wrong-target":
					out.Host = "host-b.example"
				case "wrong-grant":
					out.GrantUID = "22222222-2222-2222-2222-222222222222"
				case "missing-actor":
					out.PodUID = ""
				case "pending":
					status = http.StatusAccepted
				case "unavailable":
					return http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"retry later"}}`
				}
				raw, _ := json.Marshal(out)
				return status, string(raw)
			}})
			a := &app{env: h.env}
			c, err := a.connect(context.Background(), connOpts{})
			if err != nil {
				t.Fatal(err)
			}
			err = a.postHardwareIntent(context.Background(), c, "grant-a", req)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 {
				t.Fatal("intent was retried or polled")
			}
			if mode == "valid" {
				contains(t, "receipt", h.stdout.String(), "Audit intent only", "keeper approval are unchanged", "does not enforce")
			} else if h.stdout.Len() != 0 {
				t.Fatal("uncertain intent printed a receipt")
			}
		})
	}
}
