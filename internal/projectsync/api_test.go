package projectsync

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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNamedAPIReaderUsesOnlyBoundedGETAndRotatesProjectedIdentity(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	token := "fixture-token-first"
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("named read used wrong method or stale identity")
		}
		requests = append(requests, r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/namespaces/dev-agents/pods/project-sync-1":
			_ = json.NewEncoder(w).Encode(corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "project-sync-1", Namespace: "dev-agents", UID: "pod-1"}})
		case "/apis/batch/v1/namespaces/dev-agents/jobs/project-sync-run-1":
			_ = json.NewEncoder(w).Encode(batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "project-sync-run-1", Namespace: "dev-agents", UID: "job-1"}})
		case "/api/v1/namespaces/dev-env-system/configmaps/dev-env-project-catalog":
			_ = json.NewEncoder(w).Encode(corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "dev-env-project-catalog", Namespace: "dev-env-system", UID: "catalog-1"}, Data: map[string]string{"catalog.json": testCatalog}})
		default:
			t.Fatalf("unexpected API capability: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = time.Second
	reader := &apiReader{client: client, endpoint: server.URL, tokenFile: tokenPath}
	if _, err := reader.Pod(context.Background(), "dev-agents", "project-sync-1"); err != nil {
		t.Fatal(err)
	}
	token = "fixture-token-rotated"
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Job(context.Background(), "dev-agents", "project-sync-run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Catalog(context.Background(), "dev-env-system", "dev-env-project-catalog"); err != nil || len(requests) != 3 {
		t.Fatalf("named read did not complete exactly once: %v %+v", err, requests)
	}
}

func TestNamedAPIFailuresDoNotReplayRedirectOrExposeRawBodies(t *testing.T) {
	for _, mode := range []string{"error", "invalid resource", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			tokenPath := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(tokenPath, []byte("fixture-bearer-canary"), 0o600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch mode {
				case "error":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "redirect":
					w.Header().Set("Location", "https://unrelated.invalid/")
					w.WriteHeader(http.StatusFound)
				}
				_, _ = w.Write([]byte("private-api-body-canary fixture-bearer-canary https://private-server.invalid"))
			}))
			defer server.Close()
			client := server.Client()
			client.Timeout = time.Second
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			reader := &apiReader{client: client, endpoint: server.URL, tokenFile: tokenPath}
			_, err := reader.Pod(context.Background(), "dev-agents", "project-sync-1")
			if err == nil || calls != 1 {
				t.Fatalf("unconfirmed read retried or admitted: %v, calls=%d", err, calls)
			}
			for _, canary := range []string{"private-api-body-canary", "fixture-bearer-canary", "private-server.invalid", server.URL} {
				if strings.Contains(err.Error(), canary) {
					t.Fatal("public read error exposed private API data")
				}
			}
		})
	}
}
