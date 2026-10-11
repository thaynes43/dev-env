package agentd

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func inboxTLSFixture(t *testing.T, h http.Handler) (*InboxClient, Settings) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	token := filepath.Join(dir, "token")
	writeFile(t, ca, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})))
	writeFile(t, token, "token-one")
	s := Settings{APIURL: srv.URL, APITokenFile: token, APICAFile: ca}
	c, err := NewInboxClient(s)
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}
func TestInboxClientTLSProjectedRotationAndNoAutomaticRetry(t *testing.T) {
	var auths []string
	var bodies []string
	c, s := inboxTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"outcome":"Conflict"}`)
	}))
	for _, token := range []string{"token-one", "token-two"} {
		writeFile(t, s.APITokenFile, token)
		data, status, err := c.Do(context.Background(), http.MethodPost, "/v1/inbox", []byte(`{"text":"exact"}`))
		if err != nil || status != 409 || string(data) != `{"outcome":"Conflict"}` {
			t.Fatal("conflict outcome lost", status, err)
		}
	}
	if len(auths) != 2 || auths[0] != "Bearer token-one" || auths[1] != "Bearer token-two" || bodies[0] != bodies[1] {
		t.Fatal("token rotation/body/retry", auths, bodies)
	}
	s.APICAFile = ""
	untrusted, err := NewInboxClient(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := untrusted.Do(context.Background(), http.MethodGet, "/v1/inbox?project=sample", nil); err == nil || strings.Contains(err.Error(), "token-two") {
		t.Fatal("untrusted TLS accepted or token leaked", err)
	}
	for _, url := range []string{"http://example.invalid", "https://user@example.invalid", "https://example.invalid/path", "https://example.invalid?redirect=x"} {
		s.APIURL = url
		if _, err := NewInboxClient(s); err == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
}
func TestInboxClientRedirectOverflowAndInvalidJSON(t *testing.T) {
	for _, kind := range []string{"redirect", "overflow", "invalid", "invalid-utf8"} {
		t.Run(kind, func(t *testing.T) {
			requests := 0
			c, _ := inboxTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				switch kind {
				case "redirect":
					http.Redirect(w, r, "/v1/inbox?project=other", http.StatusTemporaryRedirect)
				case "overflow":
					_, _ = io.WriteString(w, strings.Repeat(" ", InboxMaxResponseBytes+1))
				case "invalid":
					_, _ = io.WriteString(w, `{} {}`)
				case "invalid-utf8":
					_, _ = w.Write([]byte{'"', 0xff, '"'})
				}
			}))
			if _, _, err := c.Do(context.Background(), http.MethodGet, "/v1/inbox?project=sample", nil); err == nil || requests != 1 {
				t.Fatal("response refusal/retry", err, requests)
			}
		})
	}
}
func TestInboxTokenRefusesFIFOOverflowAndFollowsProjectedRotation(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "token-real")
	link := filepath.Join(dir, "token")
	writeFile(t, real, "initial")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if data, err := readInboxToken(link); err != nil || string(data) != "initial" {
		t.Fatal("projection symlink refused", err)
	}
	writeFile(t, real, "rotated")
	if data, err := readInboxToken(link); err != nil || string(data) != "rotated" {
		t.Fatal("projected rotation not observed", err)
	}
	writeFile(t, real, strings.Repeat("x", (16<<10)+1))
	if _, err := readInboxToken(link); err == nil {
		t.Fatal("oversized token accepted")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readInboxToken(fifo); err == nil {
		t.Fatal("FIFO token accepted")
	}
}
