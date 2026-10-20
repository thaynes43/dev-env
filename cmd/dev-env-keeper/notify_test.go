package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	notifyTokenCanary = "CMDTOKENCANARY0123456789abcdef"
	notifyUserCanary  = "CMDUSERCANARY0123456789abcdef"
)

func notifyFiles(t *testing.T) (token, user string) {
	t.Helper()
	dir := t.TempDir()
	token, user = filepath.Join(dir, "token"), filepath.Join(dir, "user")
	if err := os.WriteFile(token, []byte(notifyTokenCanary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(user, []byte(notifyUserCanary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return token, user
}

func TestNotifyTestSendsOneFixedNotice(t *testing.T) {
	var got url.Values
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got, _ = url.ParseQuery(string(body))
		hits++
		if strings.Contains(r.URL.String(), "CANARY") {
			t.Error("a credential reached the URL")
		}
	}))
	defer srv.Close()
	old := notifyClient
	notifyClient = srv.Client()
	defer func() { notifyClient = old }()
	token, user := notifyFiles(t)

	var out bytes.Buffer
	if code := notifyTest([]string{"--pushover-token-file", token, "--pushover-user-file", user, "--pushover-url", srv.URL}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if out.String() != "sent\n" || hits != 1 {
		t.Fatalf("output %q after %d requests", out.String(), hits)
	}
	if got.Get("title") != "dev-env test notice" || got.Get("message") != "dev-env test notice" || got.Get("priority") != "0" || got.Get("url") != "https://claude.ai/code" {
		t.Fatalf("notice = %v", got)
	}
	if got.Get("token") != notifyTokenCanary || got.Get("user") != notifyUserCanary {
		t.Fatal("credentials not sent in the form body")
	}
}

func TestNotifyTestErrorsNameNoCredential(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(body) // an API that repeats the request
	}))
	defer srv.Close()
	old := notifyClient
	notifyClient = srv.Client()
	defer func() { notifyClient = old }()
	token, user := notifyFiles(t)

	var out bytes.Buffer
	if code := notifyTest([]string{"--pushover-token-file", token, "--pushover-user-file", user, "--pushover-url", srv.URL}, &out); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out.String(), "CANARY") || !strings.Contains(out.String(), "status 400") {
		t.Fatalf("output %q", out.String())
	}

	out.Reset()
	if code := notifyTest([]string{"--pushover-token-file", filepath.Join(t.TempDir(), "absent"), "--pushover-user-file", user}, &out); code != 1 ||
		!strings.Contains(out.String(), "no Pushover credentials") || strings.Contains(out.String(), "CANARY") {
		t.Fatalf("missing token: exit %d %q", code, out.String())
	}
	for _, bad := range [][]string{{"stray"}, {"--pushover-url=http://api.pushover.net/1/messages.json"}, {"--nonsense"}} {
		out.Reset()
		if code := notifyTest(bad, &out); code != 2 {
			t.Errorf("%v: exit %d", bad, code)
		}
	}
}

func TestParseFlagsNoticeDefaultsAndRefusals(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	n := o.notices
	if n.TokenFile != "/etc/dev-env-keeper/pushover/token" || n.UserFile != "/etc/dev-env-keeper/pushover/user" ||
		n.Zone != "America/New_York" || n.WindowStart != "09:00" || n.WindowEnd != "21:00" || n.StateConfigMap != "dev-env-keeper-notices" ||
		n.URL != "https://api.pushover.net/1/messages.json" {
		t.Errorf("notice defaults %+v", n)
	}
	for _, bad := range []string{"--notice-timezone=Nowhere/Land", "--notice-window-start=9", "--notice-window-end=7pm", "--notice-window-start=22:00", "--pushover-url=http://x"} {
		if _, err := parseFlags([]string{bad}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
