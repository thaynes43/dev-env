package session

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/web/portal"
)

func TestHaynesProjectBindsNumericHrefToEmbeddedIdentity(t *testing.T) {
	if !haynesProject("/api/v3/projects/6", "haynes-engineering", "/api/v3/projects/6", 6, true) {
		t.Fatal("matching numeric project rejected")
	}
	if !haynesProject("/api/v3/projects/haynes-engineering", "", "", 0, false) {
		t.Fatal("identifier href rejected")
	}
	for _, rejected := range []struct {
		href, identifier, self string
		id                     int
		ok                     bool
	}{
		{"/api/v3/projects/7", "haynes-engineering", "/api/v3/projects/7", 6, true},
		{"/api/v3/projects/6", "", "", 6, true},
		{"/api/v3/projects/6", "other", "/api/v3/projects/6", 6, true},
		{"/api/v3/projects/6", "haynes-engineering", "/api/v3/projects/9", 6, true},
		{"/api/v3/projects/haynes-engineering", "other", "", 0, false},
		{"", "haynes-engineering", "", 0, false},
	} {
		if haynesProject(rejected.href, rejected.identifier, rejected.self, rejected.id, rejected.ok) {
			t.Fatalf("unbound project accepted: %+v", rejected)
		}
	}
}

func TestBackendMediaAllowlistKeepsHALAndJSONApart(t *testing.T) {
	var accept string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		switch r.URL.Path {
		case "/hal":
			w.Header().Set("Content-Type", "application/hal+json; charset=utf-8")
			_, _ = w.Write([]byte(`{"id":178}`))
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sessions":[]}`))
		case "/html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<p>no</p>`))
		case "/problem":
			w.Header().Set("Content-Type", "application/problem+json")
			_, _ = w.Write([]byte(`{"message":"no"}`))
		case "/big":
			w.Header().Set("Content-Type", "application/hal+json")
			_, _ = w.Write([]byte(strings.Repeat("x", 8)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	reader := &metadataReader{}
	client := server.Client()
	if body, err := reader.get(t.Context(), client, server.URL+"/hal", "op-token", 64, mediaHAL); err != nil || string(body) != `{"id":178}` || accept != mediaHAL {
		t.Fatal("HAL response with charset rejected")
	}
	if _, err := reader.get(t.Context(), client, server.URL+"/json", "op-token", 64, mediaHAL); err == nil {
		t.Fatal("JSON accepted for an OpenProject read")
	}
	if _, err := reader.get(t.Context(), client, server.URL+"/hal", "operator-token", 64, mediaJSON); err == nil {
		t.Fatal("HAL accepted for an operator read")
	}
	if body, err := reader.get(t.Context(), client, server.URL+"/json", "operator-token", 64, mediaJSON); err != nil || string(body) != `{"sessions":[]}` || accept != mediaJSON {
		t.Fatal("operator JSON rejected")
	}
	for _, path := range []string{"/html", "/problem"} {
		if _, err := reader.get(t.Context(), client, server.URL+path, "op-token", 64, mediaHAL); !errors.Is(err, portal.ErrUnavailable) {
			t.Fatalf("unrelated media accepted for %s", path)
		}
	}
	if _, err := reader.get(t.Context(), client, server.URL+"/big", "op-token", 4, mediaHAL); !errors.Is(err, portal.ErrUnavailable) {
		t.Fatal("oversized body accepted")
	}
}
