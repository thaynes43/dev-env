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
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexauth"
)

func TestOwnedHTTPSGateBindingAccessAndTokenRotation(t *testing.T) {
	home := t.TempDir()
	if os.Chmod(home, 0o700) != nil {
		t.Fatal("home")
	}
	b := hostexecutor.Binding{TaskUID: "task-fixture", Epoch: 2, Deadline: time.Now().Add(time.Hour).UTC(), HostID: "host-fixture", PodUID: "pod-fixture"}
	token := "synthetic-one"
	requests := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("rotated token missing")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == apiv1.TaskBudgetPath(b.TaskUID) {
			if r.Method != http.MethodGet {
				t.Error("binding method")
			}
		} else {
			var req apiv1.TaskBudgetRequest
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil || !sameOwnedBinding(req.Binding, wireBinding(b)) {
				t.Error("immutable POST binding changed")
			}
		}
		_ = json.NewEncoder(w).Encode(apiv1.TaskBudgetStatus{Observed: true, Admitted: true, Binding: wireBinding(b)})
	}))
	defer srv.Close()
	ca, tokenFile := filepath.Join(home, "ca.crt"), filepath.Join(home, "token")
	if os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600) != nil || os.WriteFile(tokenFile, []byte(token), 0o600) != nil {
		t.Fatal("synthetic projections")
	}
	now := time.Now().UTC()
	exp := now.Add(24 * time.Hour).Truncate(time.Second)
	claims, _ := json.Marshal(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "synthetic-account"}})
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"synthetic"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic-signature"
	access, err := codexauth.Encode(codexauth.Access{Generation: 1, AccountID: "synthetic-account", IDToken: jwt, AccessToken: jwt, ExpiresAt: exp, LastRefresh: now}, now)
	if err != nil {
		t.Fatal(err)
	}
	s := agentd.Settings{Home: home, CodexHome: filepath.Join(home, ".codex"), StateDir: filepath.Join(home, ".agentd"), CodexAccessFile: filepath.Join(home, "access.json")}
	if os.WriteFile(s.CodexAccessFile, access, 0o600) != nil {
		t.Fatal("synthetic access")
	}
	g, err := newOwnedBudgetGate(srv.URL, tokenFile, ca, s)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := g.binding(ctx, b)
	if err != nil || !sameOwnedBinding(wireBinding(got), wireBinding(b)) {
		t.Fatal("assigned binding not read exactly")
	}
	if g.Admit(ctx, got) != nil {
		t.Fatal("authority+access admission refused")
	}
	token = "synthetic-two"
	if os.WriteFile(tokenFile, []byte(token), 0o600) != nil {
		t.Fatal("rotate")
	}
	if latched, err := g.Observe(ctx, got); latched || err != nil {
		t.Fatal("live rotated authority refused")
	}
	if requests != 3 {
		t.Fatalf("unexpected request count %d", requests)
	}
	auth, err := os.ReadFile(filepath.Join(s.CodexHome, "auth.json"))
	var native struct {
		Tokens struct {
			Refresh string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err != nil || json.Unmarshal(auth, &native) != nil || native.Tokens.Refresh != "" {
		t.Fatal("access-only adoption missing or refresh material present")
	}
	wrong := b
	wrong.Epoch++
	if _, err := g.binding(ctx, wrong); err == nil {
		t.Fatal("epoch mismatch admitted")
	}
}

func TestOwnedHTTPSGateFailsClosed(t *testing.T) {
	b := hostexecutor.Binding{TaskUID: "task-fixture", Epoch: 1, Deadline: time.Now().Add(time.Hour).UTC(), HostID: "host-fixture", PodUID: "pod-fixture"}
	for _, kind := range []string{"latched", "identity", "unobserved", "unknown-json", "redirect", "server-denied"} {
		t.Run(kind, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status := apiv1.TaskBudgetStatus{Observed: true, Admitted: true, Binding: wireBinding(b)}
				switch kind {
				case "latched":
					status.Latched = true
				case "identity":
					status.Binding.PodUID = "wrong-pod"
				case "unobserved":
					status.Observed = false
				case "unknown-json":
					_, _ = w.Write([]byte(`{"untrusted":true}`))
					return
				case "redirect":
					http.Redirect(w, r, "https://untrusted.invalid", http.StatusFound)
					return
				case "server-denied":
					w.WriteHeader(http.StatusConflict)
					return
				}
				_ = json.NewEncoder(w).Encode(status)
			}))
			defer srv.Close()
			home := t.TempDir()
			ca, token := filepath.Join(home, "ca"), filepath.Join(home, "token")
			if os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600) != nil || os.WriteFile(token, []byte("synthetic"), 0o600) != nil {
				t.Fatal("fixture")
			}
			g, err := newOwnedBudgetGate(srv.URL, token, ca, agentd.Settings{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err = g.binding(ctx, b); err == nil {
				t.Fatal("unknown authority accepted")
			}
		})
	}
	if _, err := newOwnedBudgetGate("http://untrusted.invalid", "unused", "unused", agentd.Settings{}); err == nil {
		t.Fatal("plain HTTP accepted")
	}
	if _, err := newOwnedBudgetGate("https://untrusted.invalid?override=true", "unused", "unused", agentd.Settings{}); err == nil {
		t.Fatal("query override accepted")
	}
	var absent *ownedBudgetGate
	if absent.Admit(context.Background(), b) == nil {
		t.Fatal("nil authority admitted")
	}
	if latched, err := absent.Observe(context.Background(), b); !latched || err == nil {
		t.Fatal("nil observation continued")
	}
}
