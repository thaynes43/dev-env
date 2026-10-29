package session

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestConversationSourceAllowsTwoGroupMembers(t *testing.T) {
	f := setupAuth(t)
	first := actionLogin(t, f, "synthetic-first")
	firstCookie := f.session
	second := actionLogin(t, f, "synthetic-second")
	secondCookie := f.session
	checks := map[string]int{}
	auth := actionGroupAuthorizer(f, map[string]Record{"synthetic-first": first, "synthetic-second": second}, checks)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(fixtureOperatorToken(apiv1.TokenAudience, f.now.Add(time.Hour))), 0600); err != nil {
		t.Fatal(err)
	}
	cert, ca := mintNamed(t, operatorPortalHost)
	calls := 0
	server := serveTLS(t, cert, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"sessionName":"agent-a","sessionUID":"uid-a","podUID":"pod-a","projection":{"state":"partial","reasonCode":"coverage_unverified","conversationID":"12345678-1234-1234-1234-123456789abc","oldestSeq":1,"newestSeq":1,"observedAt":"2026-10-23T16:00:00Z","turns":[{"seq":1,"at":"2026-10-23T15:59:00Z","role":"user","text":"SYNTHETIC-VISIBLE"}]}}`)
	}))
	client, err := pinnedClient(ca, operatorPortalHost, 5*time.Second, routeDial(map[string]string{operatorPortalHost: server}))
	if err != nil {
		t.Fatal(err)
	}
	source := &conversationSource{authorizer: auth, targets: map[string]string{"uid-a": "agent-a"}, tokenPath: path, client: client, now: func() time.Time { return f.now }}
	for _, cookie := range []*http.Cookie{firstCookie, secondCookie} {
		f.session = cookie
		r := f.e.bind(f.request(http.MethodGet, SessionsPath, ""))
		p, err := f.e.Authenticate(r)
		if err != nil {
			t.Fatal(err)
		}
		got, err := source.ReadConversation(r.Context(), "agent-a", "uid-a")
		if err != nil || len(got.Turns) != 1 || got.Turns[0].Text != "SYNTHETIC-VISIBLE" || checks[p.Subject] != 2 {
			t.Fatal("admitted member history did not check its own token before and after fetch")
		}
	}
	if calls != 2 {
		t.Fatal("both members did not read the listed target")
	}
}

func TestConversationSourceScopedAuthorization(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-principal", "wrong-issuer", "missing-reference", "wrong-token-reference", "wrong-target", "wrong-name", "expired", "missing-group", "removed-group", "group-removed-after", "token-replaced-before", "token-replaced-after", "revoked-before", "revoked-after", "duplicate", "case-alias", "other-session", "escaped-max", "over-limit"} {
		t.Run(mode, func(t *testing.T) {
			f := setupAuth(t)
			f.login(t)
			r := f.e.bind(f.request("GET", SessionsPath, ""))
			key, _ := requestHandle(r, sessionCookie)
			checks := 0
			auth := currentFixture(f, func(*http.Request) (*http.Response, error) {
				checks++
				if mode == "missing-group" {
					return userInfoResponse(`{"sub":"synthetic-subject"}`), nil
				}
				if mode == "removed-group" || mode == "group-removed-after" && checks == 2 {
					return userInfoResponse(strings.Replace(currentAllow, `"portal_is_admin":true`, `"portal_is_admin":false`, 1)), nil
				}
				return userInfoResponse(currentAllow), nil
			})
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
				if mode == "token-replaced-after" {
					record := f.store.sessions[key]
					record.accessToken = "synthetic-replaced-token"
					f.store.sessions[key] = record
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
				if mode == "escaped-max" {
					p := protocol.PortalConversation{State: "partial", ReasonCode: "coverage_unverified", ConversationID: "12345678-1234-1234-1234-123456789abc", OldestSeq: 1, NewestSeq: 24, ObservedAt: f.now}
					for i := 1; i <= 24; i++ {
						p.Turns = append(p.Turns, protocol.PortalConversationTurn{Seq: uint64(i), At: f.now, Role: "user", Text: strings.Repeat("&", 4096)})
					}
					b, e := json.Marshal(apiv1.PortalConversationResponse{SessionName: "agent-a", SessionUID: "uid-a", PodUID: "pod-a", Projection: p})
					if e != nil || len(b) <= 256<<10 {
						t.Error("fixture encoding")
					}
					body = string(b)
				}
				if mode == "over-limit" {
					body = strings.Repeat("x", protocol.PortalConversationEncodedLimit+1)
				}
				_, _ = fmt.Fprint(w, body)
			}))
			client, err := pinnedClient(ca, operatorPortalHost, 5*time.Second, routeDial(map[string]string{operatorPortalHost: server}))
			if err != nil {
				t.Fatal("fixture TLS")
			}
			source := &conversationSource{authorizer: auth, targets: map[string]string{"uid-a": "agent-a"}, tokenPath: path, client: client, now: func() time.Time { return f.now }}
			ctx := r.Context()
			ref := ctx.Value(sessionContextKey{}).(sessionReference)
			switch mode {
			case "wrong-principal":
				ref.principal.Subject = "synthetic-other"
				ctx = context.WithValue(ctx, sessionContextKey{}, ref)
			case "wrong-issuer":
				ref.principal.Issuer = "https://other.example/"
				ctx = context.WithValue(ctx, sessionContextKey{}, ref)
			case "wrong-token-reference":
				ref.tokenDigest = sha256.Sum256([]byte("synthetic-other-token"))
				ctx = context.WithValue(ctx, sessionContextKey{}, ref)
			case "missing-reference":
				ctx = context.Background()
			case "expired":
				f.now = f.now.Add(2 * time.Hour)
			case "token-replaced-before":
				record := f.store.sessions[key]
				record.accessToken = "synthetic-replaced-token"
				f.store.sessions[key] = record
			}
			if mode == "revoked-before" {
				_ = f.store.RevokeSession(context.Background(), key)
			}
			uid := "uid-a"
			if mode == "wrong-target" {
				uid = "other"
			}
			name := "agent-a"
			if mode == "wrong-name" {
				name = "agent-b"
			}
			got, err := source.ReadConversation(ctx, name, uid)
			switch mode {
			case "escaped-max":
				if err != nil || len(got.Turns) != 24 || got.Turns[23].Text != strings.Repeat("&", 4096) {
					t.Fatal("escaped maximum refused")
				}
			case "valid":
				if err != nil || calls != 1 || len(got.Turns) != 1 || got.Turns[0].Text != "SYNTHETIC-VISIBLE" {
					t.Fatal("authorized read failed")
				}
			default:
				if err == nil || len(got.Turns) != 0 {
					t.Fatal("refused or untrusted response leaked content")
				}
				if (mode == "wrong-principal" || mode == "wrong-issuer" || mode == "wrong-token-reference" || mode == "missing-reference" || mode == "wrong-target" || mode == "wrong-name" || mode == "revoked-before" || mode == "expired" || mode == "missing-group" || mode == "removed-group" || mode == "token-replaced-before") && calls != 0 {
					t.Fatal("pre-read refusal contacted operator")
				}
			}
		})
	}
}

func TestConversationSourceRequiresExplicitScope(t *testing.T) {
	if source, err := NewConversationSource(nil, nil, nil, "", nil, nil); err == nil || source != nil {
		t.Fatal("missing scope enabled conversation source")
	}
}
