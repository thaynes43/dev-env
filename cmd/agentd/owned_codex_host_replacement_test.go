package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexauth"
)

// Exercise the actual owned guard gate, including the unchanged local access
// sync. Long-lived synthetic projection isolates the reproduced remote phase;
// no actual credential, provider, native inference or live cluster is involved.
func TestOwnedHTTPSObservationReplacementWithHealthyAccess(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()
	exp := now.Add(24 * time.Hour).Truncate(time.Second)
	claims, _ := json.Marshal(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "synthetic-account"}})
	token := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"synthetic"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic-signature"
	raw, e := codexauth.Encode(codexauth.Access{Generation: 1, AccountID: "synthetic-account", IDToken: token, AccessToken: token, ExpiresAt: exp, LastRefresh: now}, now)
	if e != nil {
		t.Fatal(e)
	}
	s := agentd.Settings{Home: home, CodexHome: filepath.Join(home, ".codex"), StateDir: filepath.Join(home, ".agentd"), CodexAccessFile: filepath.Join(home, "access.json")}
	if os.WriteFile(s.CodexAccessFile, raw, 0600) != nil || agentd.SyncCodexAccess(s, now) != nil {
		t.Fatal("synthetic local sync failed")
	}
	b := hostexecutor.Binding{TaskUID: "replacement", HostID: "host", PodUID: "original", Epoch: 1, Deadline: now.Add(time.Minute)}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			c, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = c.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(apiv1.TaskBudgetStatus{Binding: wireBinding(b), Observed: true, Admitted: true})
	}))
	defer server.Close()
	ca, apiToken := filepath.Join(home, "ca.crt"), filepath.Join(home, "token")
	if os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600) != nil || os.WriteFile(apiToken, []byte("synthetic-api"), 0600) != nil {
		t.Fatal("synthetic projection failed")
	}
	gate, e := newOwnedBudgetGate(server.URL, apiToken, ca, s)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostexecutor.AuthorityTimeout)
	defer cancel()
	if stop, err := gate.Observe(ctx, b); stop || err != nil {
		t.Fatal("warm observation", err)
	}
	started := time.Now()
	stop, err := gate.Observe(ctx, b)
	if agentd.SyncCodexAccess(s, time.Now()) != nil {
		t.Fatal("local credential sync was unhealthy; fixture did not isolate remote phase")
	}
	t.Logf("actual owned HTTPS gate with healthy local sync: calls=%d elapsed=%v stop=%v err=%v", calls.Load(), time.Since(started), stop, err)
	if stop || err != nil || calls.Load() != 3 {
		t.Fatal("remote reused-connection loss interrupted healthy original authority")
	}
}
