package agentd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCoordArchiveListPagesLatestVerifiedProjection(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	object := map[string]any{}
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["spec"].(map[string]any)["text"] = "private synthetic text"
	raw, _ = protocol.CanonicalJSON(object)
	bindCoordStoredFixture(t, &m, raw)
	a.Now = func() time.Time { return time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC) }
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	second := m
	second.Name = "task-claim." + strings.Repeat("c", 32) + "." + strings.Repeat("d", 32)
	second.UID = "22222222-2222-2222-2222-222222222222"
	object["metadata"].(map[string]any)["name"] = second.Name
	object["metadata"].(map[string]any)["uid"] = second.UID
	raw, _ = protocol.CanonicalJSON(object)
	bindCoordStoredFixture(t, &second, raw)
	a.Now = func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
	if _, err := a.Put(second, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	q := protocol.CoordArchiveListQuery{Resource: "taskclaims", Project: m.Project, Limit: 1}
	page, err := a.List(q)
	if err != nil || len(page.Items) != 1 || page.Items[0].UID != second.UID || page.Next == "" || bytes.Contains(page.Items[0].Object, []byte("private synthetic text")) {
		t.Fatalf("first page: %#v, %v", page, err)
	}
	q.After = page.Next
	page, err = a.List(q)
	if err != nil || len(page.Items) != 1 || page.Items[0].UID != m.UID || page.Next == "" {
		t.Fatalf("second page: %#v, %v", page, err)
	}
	bad := protocol.CoordArchiveListQuery{Resource: "taskclaims", Project: "other", Limit: 1, After: q.After}
	if _, err := a.List(bad); err == nil {
		t.Fatal("foreign cursor accepted")
	}
}

func TestCoordArchiveListReportsCorruptLatestWithoutFallback(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	m.ResourceVersion = "43"
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["metadata"].(map[string]any)["resourceVersion"] = m.ResourceVersion
	raw, _ = protocol.CanonicalJSON(object)
	bindCoordStoredFixture(t, &m, raw)
	a.Now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coordRevisionPath(t, a, m), "object.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := a.List(protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].UID != m.UID || page.Items[0].Error != "corrupt" || page.Items[0].Object != nil {
		t.Fatalf("corrupt latest: %#v, %v", page, err)
	}
}
