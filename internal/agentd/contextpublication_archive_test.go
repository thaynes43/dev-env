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
	s, _, req, m := contextClientFixture(t)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)
	m.Archived, m.ArchivedAt = true, &at
	m.Attribution = apiv1.ContextAttribution{ObservedModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown"}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(apiv1.PublishedContext{Metadata: m, Text: req.Text})
	}))
	defer ts.Close()
	h := &HeartbeatClient{URL: ts.URL, HTTP: ts.Client(), TokenFile: token}
	got, err := h.ReadPublishedContext(context.Background(), "sample", m.Name)
	if err != nil || !got.Metadata.Archived || got.Metadata.ArchivedAt == nil || got.Text != req.Text {
		t.Fatalf("archived read = %#v, %v", got, err)
	}
	_ = s
}
