package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd"
)

func TestInboxCommandExplicitBoundedStdinAndRoutes(t *testing.T) {
	var method, path, body string
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		method, path = r.Method, r.URL.RequestURI()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = io.WriteString(w, `{"outcome":"Confirmed"}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"AGENTD_API_URL": srv.URL, "AGENTD_API_TOKEN_FILE": token, "AGENTD_API_CA_FILE": ca}
	getenv := func(k string) string { return env[k] }
	intent := `{"project":"sample","recipient":{"namespace":"dev-agents","name":"recipient","uid":"original-uid"},"key":"logical-key","text":"  exact\n"}`
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"inbox-publish"}, strings.NewReader(intent), &out, &stderr, getenv, agentd.ExecRunner{}); code != exitOK || method != http.MethodPost || path != "/v1/inbox" || body != intent || out.String() != `{"outcome":"Confirmed"}`+"\n" {
		t.Fatal("explicit publication", code, stderr.String())
	}
	name := "message." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32)
	for _, command := range []string{"inbox-list", "inbox-read", "inbox-ack"} {
		args := []string{command, "--project=sample"}
		if command != "inbox-list" {
			args = append(args, "--message="+name)
		}
		out.Reset()
		stderr.Reset()
		if code := run(context.Background(), args, strings.NewReader(""), &out, &stderr, getenv, agentd.ExecRunner{}); code != exitOK {
			t.Fatal(command, code, stderr.String())
		}
		if command == "inbox-ack" && (method != http.MethodPost || body != "{}" || !strings.Contains(path, "/ack?")) {
			t.Fatal("ack route", method, path, body)
		}
	}
	before := calls
	for _, stdin := range []string{strings.Repeat(" ", agentd.InboxMaxRequestBytes+1), `{"text":"` + strings.Repeat("é", (8<<10)+1) + `"}`, `{"text":"x","actor":{}}`, "{\"text\":\"" + string([]byte{0xff}) + "\"}"} {
		if code := run(context.Background(), []string{"inbox-publish"}, strings.NewReader(stdin), &out, &stderr, getenv, agentd.ExecRunner{}); code != exitUsage {
			t.Fatal("invalid stdin accepted", code)
		}
	}
	if calls != before {
		t.Fatal("refused stdin reached API")
	}
	if code := run(context.Background(), []string{"inbox-read", "--project=sample", "--message=../../secret"}, strings.NewReader(""), &out, &stderr, getenv, agentd.ExecRunner{}); code != exitUsage || calls != before {
		t.Fatal("unsafe message path reached API")
	}
}
