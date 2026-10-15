package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

func TestContextArchiveListAcceptsArchivedMetadataAndKeepsCurrentRefusals(t *testing.T) {
	_, _, _, explicit := contextClientFixture(t)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	archivedAt := time.Date(2026, 10, 15, 15, 30, 0, 0, time.UTC)
	managed := explicit
	managed.Attribution.Provider = "codex"
	managed.Origin, managed.Completeness, managed.Kind = "ManagedOutputAssertion", "PartialManagedOutput", "transcript-excerpt"
	invocation := apiv1.ManagedInvocation(managed.Source.UID, managed.Actor.PodUID)
	managed.ManagedOutput = &apiv1.ManagedOutputAssertion{Invocation: invocation, Ordinal: 0, ConversationID: "12345678-1234-1234-1234-123456789abc"}
	managed.Key = apiv1.ManagedOutputKey(invocation, 0)
	managed.Name = apiv1.ContextName(managed.Source.UID, managed.Kind, managed.Key)

	for _, tc := range []struct {
		name    string
		items   []apiv1.ContextMetadata
		limit   int
		wantErr bool
	}{
		{name: "explicit", items: []apiv1.ContextMetadata{explicit}, limit: 2},
		{name: "managed", items: []apiv1.ContextMetadata{managed}, limit: 2},
		{name: "foreign-project", items: []apiv1.ContextMetadata{func() apiv1.ContextMetadata { m := explicit; m.Project = "other"; return m }()}, limit: 1, wantErr: true},
		{name: "invalid-provenance", items: []apiv1.ContextMetadata{func() apiv1.ContextMetadata { m := explicit; m.Provenance.RulesRevision = "not-a-digest"; return m }()}, limit: 1, wantErr: true},
		{name: "over-limit", items: []apiv1.ContextMetadata{explicit, explicit}, limit: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := append([]apiv1.ContextMetadata(nil), tc.items...)
			for i := range items {
				items[i].Archived, items[i].ArchivedAt = true, &archivedAt
				items[i].PublishedAt = items[i].PublishedAt.UTC()
			}
			requests := 0
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != apiv1.PublishedContextsPath || r.URL.Query().Get("project") != "sample" || r.URL.Query().Get("limit") != strconv.Itoa(tc.limit) || r.URL.Query().Get("continue") != "archive-page" {
					t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
				}
				_ = json.NewEncoder(w).Encode(apiv1.PublishedContextList{Items: items, Continue: "archive-next"})
			}))
			defer ts.Close()
			h := &HeartbeatClient{URL: ts.URL, HTTP: ts.Client(), TokenFile: token}
			got, err := h.ListPublishedContexts(context.Background(), "sample", "archive-page", tc.limit)
			if tc.wantErr {
				if err == nil || requests != 1 {
					t.Fatalf("current refusal = %#v, %d requests", err, requests)
				}
				return
			}
			if err != nil || requests != 1 || got.Continue != "archive-next" || !reflect.DeepEqual(got.Items, items) || !got.Items[0].ArchivedAt.Equal(archivedAt) || got.Items[0].ArchivedAt.Location() != time.UTC {
				t.Fatalf("archive list = %#v, %v, %d requests", got, err, requests)
			}
			live := got.Items[0]
			live.Archived, live.ArchivedAt = false, nil
			wantLive := items[0]
			wantLive.Archived, wantLive.ArchivedAt = false, nil
			if !reflect.DeepEqual(live, wantLive) {
				t.Fatalf("immutable metadata changed: got %#v want %#v", live, wantLive)
			}
		})
	}
}
