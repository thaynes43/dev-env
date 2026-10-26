package agentrun

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestPortalBootstrapCLIReceipts(t *testing.T) {
	good := `{"disposition":"Present","requestID":"request-178","targetUID":"uid-178","targetResourceVersion":"42"}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		exit   int
	}{
		{"present", 200, good, ExitOK},
		{"created", 201, strings.Replace(good, "Present", "Created", 1), ExitOK},
		{"contradictory-200", 200, strings.Replace(good, "Present", "Created", 1), ExitUnconfirmed},
		{"contradictory-201", 201, good, ExitUnconfirmed},
		{"other-success", 202, good, ExitUnconfirmed},
		{"unbound-request", 200, strings.Replace(good, "request-178", "other", 1), ExitUnconfirmed},
		{"empty-uid", 200, strings.Replace(good, "uid-178", "", 1), ExitUnconfirmed},
		{"invalid-uid", 200, strings.Replace(good, "uid-178", "unbound UID", 1), ExitUnconfirmed},
		{"invalid-version", 200, strings.Replace(good, "42", "bad", 1), ExitUnconfirmed},
		{"empty-version", 200, strings.Replace(good, "42", "", 1), ExitUnconfirmed},
		{"duplicate", 200, strings.Replace(good, `"disposition":`, `"disposition":"Present","disposition":`, 1), ExitUnconfirmed},
		{"case-alias", 200, strings.Replace(good, "targetUID", "TargetUID", 1), ExitUnconfirmed},
		{"unknown", 200, strings.Replace(good, "}", `,"raw":"PRIVATE-FAULT"}`, 1), ExitUnconfirmed},
		{"malformed", 200, `PRIVATE-FAULT`, ExitUnconfirmed},
		{"trailing", 200, good + ` PRIVATE-FAULT`, ExitUnconfirmed},
		{"utf8", 200, good + string([]byte{0xff}), ExitUnconfirmed},
		{"shape", 200, `[]`, ExitUnconfirmed},
		{"null", 200, strings.Replace(good, `"uid-178"`, `null`, 1), ExitUnconfirmed},
		{"oversize", 200, good + strings.Repeat(" ", maxResponse), ExitUnconfirmed},
		{"bounded", 200, good + strings.Repeat(" ", 1025), ExitUnconfirmed},
		{"redirect", 307, `PRIVATE-FAULT`, ExitFailed},
		{"refusal", 403, `{"error":{"code":"Forbidden","message":"PRIVATE-FAULT"}}`, ExitAuth},
		{"uncertain", 503, `{"error":{"code":"Internal","message":"PRIVATE-FAULT"}}`, ExitUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Location", "/unexpected")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}
			h.mustRun(tc.exit, "portal-admission-bootstrap", "--request-id", "request-178")
			reqs := h.api.requests()
			if len(reqs) != 1 || reqs[0].method != http.MethodPost || reqs[0].path != apiv1.PortalAdmissionBootstrapPath || string(reqs[0].body) != `{"requestID":"request-178"}` {
				t.Fatalf("requests %#v", reqs)
			}
			if len(h.sleeps) != 0 || strings.Contains(h.stderr.String(), "PRIVATE-FAULT") || strings.Contains(h.stdout.String(), "PRIVATE-FAULT") {
				t.Fatalf("leak or replay: %s %s", &h.stdout, &h.stderr)
			}
			if tc.exit == ExitOK {
				var r apiv1.PortalAdmissionBootstrapResult
				if json.Unmarshal(h.stdout.Bytes(), &r) != nil || r.TargetUID != "uid-178" {
					t.Fatalf("receipt %s", &h.stdout)
				}
			}
		})
	}
}

func TestPortalBootstrapCLINoReplayAfterLostResponse(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		c, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = c.Close()
	}
	h.mustRun(ExitUnconfirmed, "portal-admission-bootstrap", "--request-id", "request-178")
	if len(h.api.requests()) != 1 || len(h.sleeps) != 0 {
		t.Fatal("POST replayed")
	}
}

func TestPortalBootstrapCLIRejectsSelectors(t *testing.T) {
	for _, args := range [][]string{{}, {"--request-id", "request-178", "--request-id", "request-178"}, {"--request-id", "request-178", "--namespace", "other"}, {"--request-id", "request-178", "caller"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t)
			h.mustRun(ExitUsage, append([]string{"portal-admission-bootstrap"}, args...)...)
			if len(h.api.requests()) != 0 {
				t.Fatal("invalid CLI sent request")
			}
		})
	}
}

// Inject a read deadline only after the complete POST has reached the peer.
// No load, sleeps, retries or real network timeout is needed.
func TestPortalBootstrapCLITimeoutAfterSend(t *testing.T) {
	h := newHarness(t)
	old := http.DefaultTransport
	calls := new(atomic.Int32)
	tr := &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		local, remote := net.Pipe()
		t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
		go func() {
			defer func() { _ = remote.Close() }()
			req, err := http.ReadRequest(bufio.NewReader(remote))
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()
			_ = local.SetReadDeadline(time.Unix(1, 0))
			// Keep the connection open until the CLI closes after the timeout.
			_, _ = io.Copy(io.Discard, remote)
		}()
		return local, nil
	}}
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old; tr.CloseIdleConnections() })
	h.mustRun(ExitUnconfirmed, "portal-admission-bootstrap", "--request-id", "request-178")
	if calls.Load() != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 || !strings.Contains(h.stderr.String(), "do not repeat") {
		t.Fatalf("calls=%d stdout=%s stderr=%s", calls.Load(), &h.stdout, &h.stderr)
	}
}
