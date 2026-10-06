package agentd

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestHeartbeatSend(t *testing.T) {
	var gotPath, gotAuth string
	var got protocol.Status
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	writeFile(t, ca, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})))
	tok := filepath.Join(dir, "token")
	writeFile(t, tok, "tok-1\n")
	s := testSettings(t, t.TempDir())
	s.APIURL, s.APITokenFile, s.APICAFile = srv.URL, tok, ca

	h, err := NewHeartbeatClient(s, "haynes-ops-1006-120000")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Send(context.Background(), protocol.Status{Session: "haynes-ops-1006-120000", Boot: protocol.BootReady}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/sessions/haynes-ops-1006-120000/heartbeat" || gotAuth != "Bearer tok-1" || got.Boot != protocol.BootReady {
		t.Errorf("path %q auth %q body %+v", gotPath, gotAuth, got)
	}

	// The kubelet rotates the token: the next beat reads the new one.
	writeFile(t, tok, "tok-2")
	if err := h.Send(context.Background(), protocol.Status{}); err != nil || gotAuth != "Bearer tok-2" {
		t.Errorf("rotated token: %q %v", gotAuth, err)
	}
}

func TestHeartbeatErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such session", http.StatusNotFound)
	}))
	defer srv.Close()
	s := testSettings(t, t.TempDir())
	s.APIURL = srv.URL
	tok := filepath.Join(t.TempDir(), "token")
	s.APITokenFile = tok

	h, err := NewHeartbeatClient(s, "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Send(context.Background(), protocol.Status{}); err == nil || !strings.Contains(err.Error(), "API token") {
		t.Errorf("missing token: %v", err)
	}
	writeFile(t, tok, "secret-token")
	err = h.Send(context.Background(), protocol.Status{})
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "no such session") {
		t.Errorf("404: %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("the token reached the error: %v", err)
	}

	s.APIURL = ""
	if _, err := NewHeartbeatClient(s, "s"); err == nil {
		t.Error("no URL accepted")
	}
	s.APIURL, s.APICAFile = srv.URL, tok // not a certificate
	if _, err := NewHeartbeatClient(s, "s"); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Errorf("bad CA: %v", err)
	}
}

func TestHeartbeatPathEscapes(t *testing.T) {
	if got := protocol.HeartbeatPath("a/b"); got != "/v1/sessions/a%2Fb/heartbeat" {
		t.Errorf("path = %q", got)
	}
}
