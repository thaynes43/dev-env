package session

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

func TestConversationSourceScopedAuthorization(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-principal", "wrong-target", "revoked-before", "revoked-after", "duplicate", "case-alias", "other-session"} {
		t.Run(mode, func(t *testing.T) {
			f := setupAuth(t)
			f.login(t)
			r := f.e.bind(f.request("GET", SessionsPath, ""))
			key, _ := requestHandle(r, sessionCookie)
			auth := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
			path := filepath.Join(t.TempDir(), "token")
			if os.WriteFile(path, []byte(fixtureOperatorToken(apiv1.TokenAudience, f.now.Add(time.Hour))), 0600) != nil {
				t.Fatal("fixture token")
			}
			cert, ca := mintNamed(t, operatorPortalHost)
			calls := 0
			server := serveTLS(t, cert, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Method != "GET" || req.URL.Path != apiv1.PortalConversationPrefix+"uid-a/conversation" || req.URL.RawQuery != "" {
					t.Error("wrong destination")
				}
				if mode == "revoked-after" {
					_ = f.store.RevokeSession(context.Background(), key)
				}
				w.Header().Set("Content-Type", "application/json")
				body := `{"sessionName":"agent-a","sessionUID":"uid-a","podUID":"pod-a","projection":{"state":"partial","reasonCode":"coverage_unverified","conversationID":"12345678-1234-1234-1234-123456789abc","oldestSeq":1,"newestSeq":1,"observedAt":"2026-10-23T16:00:00Z","turns":[{"seq":1,"at":"2026-10-23T15:59:00Z","role":"user","text":"SYNTHETIC-VISIBLE"}]}}`
				if mode == "duplicate" {
					body = strings.Replace(body, `"state":"partial"`, `"state":"partial","state":"partial"`, 1)
				}
				if mode == "case-alias" {
					body = strings.Replace(body, `"sessionUID":"uid-a"`, `"sessionUID":"uid-a","SessionUID":"uid-a"`, 1)
				}
				if mode == "other-session" {
					body = strings.Replace(body, `"sessionUID":"uid-a"`, `"sessionUID":"other"`, 1)
				}
				_, _ = fmt.Fprint(w, body)
			}))
			client, err := pinnedClient(ca, operatorPortalHost, 5*time.Second, routeDial(map[string]string{operatorPortalHost: server}))
			if err != nil {
				t.Fatal("fixture TLS")
			}
			source := &conversationSource{authorizer: auth, principal: portal.Principal{Issuer: f.e.config.Verifier.issuer, Subject: "synthetic-subject"}, targets: map[string]string{"uid-a": "agent-a"}, tokenPath: path, client: client, now: func() time.Time { return f.now }}
			if mode == "wrong-principal" {
				source.principal.Subject = "other"
			}
			if mode == "revoked-before" {
				_ = f.store.RevokeSession(context.Background(), key)
			}
			uid := "uid-a"
			if mode == "wrong-target" {
				uid = "other"
			}
			got, err := source.ReadConversation(r.Context(), "agent-a", uid)
			if mode == "valid" {
				if err != nil || calls != 1 || len(got.Turns) != 1 || got.Turns[0].Text != "SYNTHETIC-VISIBLE" {
					t.Fatal("authorized read failed")
				}
			} else {
				if err == nil || len(got.Turns) != 0 {
					t.Fatal("refused or untrusted response leaked content")
				}
				if (mode == "wrong-principal" || mode == "wrong-target" || mode == "revoked-before") && calls != 0 {
					t.Fatal("pre-read refusal contacted operator")
				}
			}
		})
	}
}

func TestConversationSourceRequiresExplicitScope(t *testing.T) {
	if source, err := NewConversationSource(nil, nil, portal.Principal{}, nil, "", nil, nil); err == nil || source != nil {
		t.Fatal("missing scope enabled conversation source")
	}
}
