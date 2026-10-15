package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestContextArchiveReadPublishedContextAcceptsArchivedMetadata(t *testing.T) {
	_, _, req, m := contextClientFixture(t)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)
	m.Archived, m.ArchivedAt = true, &at
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(apiv1.PublishedContext{Metadata: m, Text: req.Text})
	}))
	defer ts.Close()
	h := &HeartbeatClient{URL: ts.URL, HTTP: ts.Client(), TokenFile: token}
	for _, managed := range []bool{false, true} {
		if managed {
			m.Attribution.Provider = "codex"
			m.Origin, m.Completeness, m.Kind = "ManagedOutputAssertion", "PartialManagedOutput", "transcript-excerpt"
			invocation := apiv1.ManagedInvocation(m.Source.UID, m.Actor.PodUID)
			m.ManagedOutput = &apiv1.ManagedOutputAssertion{Invocation: invocation, Ordinal: 0, ConversationID: "12345678-1234-1234-1234-123456789abc"}
			m.Key = apiv1.ManagedOutputKey(invocation, 0)
			m.Name = apiv1.ContextName(m.Source.UID, m.Kind, m.Key)
		}
		got, err := h.ReadPublishedContext(context.Background(), "sample", m.Name)
		if err != nil || !got.Metadata.Archived || got.Metadata.ArchivedAt == nil || got.Text != req.Text || got.Metadata.Attribution.Provider != m.Attribution.Provider || got.Metadata.Attribution.RequestedModel != m.Attribution.RequestedModel {
			t.Fatalf("archived read managed=%v = %#v, %v", managed, got, err)
		}
	}
}
