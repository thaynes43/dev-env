package agentd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func TestPrivateSnapshotStoresMaximumEscapedRulesWithoutTruncation(t *testing.T) {
	rules := strings.Repeat("\x01", projectcatalog.MaxRulesBytes)
	repositories := map[string]any{}
	selected := []any{}
	for i := range projectcatalog.MaxProjectRepositories {
		name := fmt.Sprintf("repo-%02d", i)
		repositories[name] = map[string]any{"github": "fixture/" + name}
		selected = append(selected, map[string]any{"name": name})
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "repositories": repositories,
		"projects": map[string]any{"sample": map[string]any{"repositories": selected, "rules": rules}}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := projectcatalog.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", "repo-00")
	if err != nil {
		t.Fatal(err)
	}
	s := testSettings(t, t.TempDir())
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreProjectSnapshot(s, snapshot); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.StateDir, "project-snapshot.json")
	before, err := os.ReadFile(path)
	if err != nil || len(before) <= 64<<10 || len(before) > projectcatalog.MaxSnapshotBytes {
		t.Fatal("private store did not retain the maximum escaped snapshot")
	}
	parsed, err := projectcatalog.ParseSnapshot(before)
	if err != nil || parsed.Rules() != rules || len(parsed.Repositories()) != projectcatalog.MaxProjectRepositories {
		t.Fatal("private resume lost exact rules or repositories")
	}
	if _, err := StoreProjectSnapshot(s, parsed); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("resume rewrote the accepted private snapshot")
	}
}
