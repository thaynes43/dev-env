package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func contextClientFixture(t *testing.T) (Settings, protocol.Session, apiv1.PublishContextRequest, apiv1.ContextMetadata) {
	t.Helper()
	home := t.TempDir()
	s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), PodUID: "pod-uid"}
	if os.Mkdir(s.StateDir, 0o700) != nil {
		t.Fatal("private state")
	}
	sess := protocol.Session{Name: "source", SessionUID: "source-uid", Agent: "opencode", Model: "local/qwen-candidate"}
	req := apiv1.PublishContextRequest{Kind: "handoff", Key: "task-174", Text: "EXPLICIT_CONTEXT_CANARY"}
	m := apiv1.ContextMetadata{Name: apiv1.ContextName(sess.SessionUID, req.Kind, req.Key), UID: "publication-uid", Kind: req.Kind, Key: req.Key, Source: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: sess.Name, UID: sess.SessionUID}, SourceGeneration: 1, SourceParent: "human", Project: "sample", Repository: "repo", Actor: apiv1.TaskClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: sess.Name, PodUID: s.PodUID}, Provenance: apiv1.TaskClaimProvenance{CatalogRevision: strings.Repeat("a", 64), RulesRevision: strings.Repeat("b", 64), GitHub: "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown"}, Attribution: apiv1.ContextAttribution{Provider: sess.Agent, RequestedModel: sess.Model, ObservedModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown"}, ContentSHA256: apiv1.ContextHash(req.Text), ByteCount: int32(len(req.Text)), PublishedAt: time.Now(), Origin: "ExplicitPublished", Completeness: "ExplicitExcerpt", StopState: "Unknown"}
	return s, sess, req, m
}
func TestExplicitContextClientDurableIntentLostACKAndNoPrivateExport(t *testing.T) {
	s, sess, req, m := contextClientFixture(t)
	token := filepath.Join(t.TempDir(), "token")
	if os.WriteFile(token, []byte("SYNTHETIC_IDENTITY_CANARY"), 0o600) != nil {
		t.Fatal("token fixture")
	}
	private := filepath.Join(s.Home, "auth-canary.json")
	if os.WriteFile(private, []byte("PRIVATE_HOME_CANARY"), 0o600) != nil {
		t.Fatal("home fixture")
	}
	requests := 0
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer SYNTHETIC_IDENTITY_CANARY" {
			t.Error("projected identity missing")
		}
		if r.Method == http.MethodPost {
			var got apiv1.PublishContextRequest
			if json.NewDecoder(r.Body).Decode(&got) != nil || got != req {
				t.Error("explicit-only request")
			}
			if requests == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("PRIVATE_ERROR_BODY_CANARY"))
				return
			}
			_ = json.NewEncoder(w).Encode(apiv1.PublishContextResult{Outcome: "Confirmed", Metadata: m})
		} else {
			_ = json.NewEncoder(w).Encode(apiv1.PublishedContext{Metadata: m, Text: req.Text})
		}
	}))
	defer ts.Close()
	h := &HeartbeatClient{URL: ts.URL, HTTP: ts.Client(), TokenFile: token, Session: sess.Name}
	if _, err := PublishContext(context.Background(), s, sess, h, req); err == nil || strings.Contains(err.Error(), "CANARY") {
		t.Fatal("uncertain ACK leaked or succeeded", err)
	}
	intentPath := s.statePath(m.Name + ".json")
	before, err := os.ReadFile(intentPath)
	if err != nil || strings.Contains(string(before), req.Text) || strings.Contains(string(before), "SYNTHETIC_IDENTITY") {
		t.Fatal("intent contains text or token")
	}
	if _, err := PublishContext(context.Background(), s, sess, h, req); err != nil {
		t.Fatal("same intent not confirmed", err)
	}
	changed := req
	changed.Text = "replacement"
	if _, err := PublishContext(context.Background(), s, sess, h, changed); err == nil || requests != 2 {
		t.Fatal("changed intent performed HTTP")
	}
	if got, err := h.ReadPublishedContext(context.Background(), "sample", m.Name); err != nil || got.Text != req.Text {
		t.Fatal("read", err)
	}
	after, err := os.ReadFile(intentPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("intent replaced")
	}
	data, err := os.ReadFile(private)
	if err != nil || string(data) != "PRIVATE_HOME_CANARY" {
		t.Fatal("HOME changed")
	}
}
func TestExplicitContextClientRedirectRefusalAndExactContent(t *testing.T) {
	s, sess, req, m := contextClientFixture(t)
	token := filepath.Join(t.TempDir(), "token")
	if os.WriteFile(token, []byte("SYNTHETIC"), 0o600) != nil {
		t.Fatal("token fixture")
	}
	targetHits := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHits++ }))
	defer target.Close()
	redirect := true
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			return
		}
		bad := m
		bad.Actor.PodUID = "replacement-pod"
		_ = json.NewEncoder(w).Encode(apiv1.PublishContextResult{Outcome: "Confirmed", Metadata: bad})
	}))
	defer ts.Close()
	h := &HeartbeatClient{URL: ts.URL, HTTP: ts.Client(), TokenFile: token, Session: sess.Name}
	if _, err := PublishContext(context.Background(), s, sess, h, req); err == nil || targetHits != 0 {
		t.Fatal("redirect followed")
	}
	redirect = false
	if _, err := PublishContext(context.Background(), s, sess, h, req); err == nil {
		t.Fatal("replacement Pod acknowledgement accepted")
	}
}
