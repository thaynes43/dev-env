package agentd

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestCoordArchiveListBindsRecipientIndexToVerifiedManifest(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "inboxmessages")
	const recipientA = "22222222-2222-2222-2222-222222222222"
	const recipientB = "33333333-3333-3333-3333-333333333333"
	m.RecipientUID = recipientB
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["spec"].(map[string]any)["recipient"].(map[string]any)["uid"] = recipientB
	raw, _ = protocol.CanonicalJSON(object)
	bindCoordStoredFixture(t, &m, raw)
	stored, err := a.Put(m, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	entry := coordIndexEntry(stored)
	forged := entry
	forged.RecipientUID = recipientA
	forgedRaw, err := protocol.CanonicalJSON(forged)
	if err != nil {
		t.Fatal(err)
	}
	forgedPath := filepath.Join(a.Root, filepath.FromSlash(coordIndexPaths(forged)[1]))
	if err := os.MkdirAll(filepath.Dir(forgedPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(forgedPath, forgedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := a.List(protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, RecipientUID: recipientA, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].UID != m.UID || page.Items[0].Error != "corrupt" || page.Items[0].Object != nil || page.Items[0].Manifest != nil {
		t.Fatalf("forged recipient index exposed data: %#v, %v", page, err)
	}
}

func TestCoordArchiveListRejectsSelectedIndexNamespaceMismatch(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "taskclaims")
	stored, err := a.Put(m, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	forged := coordIndexEntry(stored)
	forged.Namespace = "other-namespace"
	forgedRaw, err := protocol.CanonicalJSON(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Root, filepath.FromSlash(coordIndexPaths(forged)[0])), forgedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := a.List(protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].UID != m.UID || page.Items[0].Error != "corrupt" || page.Items[0].Object != nil || page.Items[0].Manifest != nil {
		t.Fatalf("forged namespace index exposed data: %#v, %v", page, err)
	}
}

func TestCoordArchiveListPagesBoundedSelectionAcrossDirectoryBatches(t *testing.T) {
	a := coordArchiveRig(t)
	base, baseRaw := coordStoredFixture(t, "taskclaims")
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	now := 0
	a.Now = func() time.Time {
		at := start.Add(time.Duration(now) * time.Nanosecond)
		now++
		return at
	}
	for i := 0; i < 130; i++ {
		m := base
		m.UID = fmt.Sprintf("%08x-1111-1111-1111-111111111111", i+1)
		var object map[string]any
		if err := json.Unmarshal(baseRaw, &object); err != nil {
			t.Fatal(err)
		}
		object["metadata"].(map[string]any)["uid"] = m.UID
		raw, err := protocol.CanonicalJSON(object)
		if err != nil {
			t.Fatal(err)
		}
		bindCoordStoredFixture(t, &m, raw)
		if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	q := protocol.CoordArchiveListQuery{Resource: base.Resource, Project: base.Project, Limit: 100}
	first, err := a.List(q)
	if err != nil || len(first.Items) != 100 || first.Next == "" {
		t.Fatalf("first 100-item page: %#v, %v", first, err)
	}
	seen := map[string]bool{}
	for _, item := range first.Items {
		if item.Error != "" || seen[item.UID] {
			t.Fatalf("invalid first-page item: %#v", item)
		}
		seen[item.UID] = true
	}
	q.After = first.Next
	second, err := a.List(q)
	if err != nil || len(second.Items) != 30 {
		t.Fatalf("second terminal page: %#v, %v", second, err)
	}
	for _, item := range second.Items {
		if item.Error != "" || seen[item.UID] {
			t.Fatalf("duplicate or invalid continuation item: %#v", item)
		}
		seen[item.UID] = true
	}
	if len(seen) != 130 {
		t.Fatalf("lost list entries: got %d", len(seen))
	}
}

func TestCoordArchiveListKeepsContinuationAtFourMiBWireBoundary(t *testing.T) {
	a := coordArchiveRig(t)
	base, baseRaw := coordStoredFixture(t, "taskclaims")
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	now := 0
	a.Now = func() time.Time {
		at := start.Add(time.Duration(now) * time.Nanosecond)
		now++
		return at
	}
	for i := 0; i < 5; i++ {
		m := base
		m.UID = fmt.Sprintf("%08x-2222-2222-2222-222222222222", i+1)
		var object map[string]any
		if err := json.Unmarshal(baseRaw, &object); err != nil {
			t.Fatal(err)
		}
		object["metadata"].(map[string]any)["uid"] = m.UID
		object["spec"].(map[string]any)["syntheticPageBoundary"] = strings.Repeat("x", 1_047_400)
		raw, err := protocol.CanonicalJSON(object)
		if err != nil || len(raw) > protocol.MaxCoordArchiveObjectBytes {
			t.Fatalf("bounded synthetic object %d: %d, %v", i, len(raw), err)
		}
		bindCoordStoredFixture(t, &m, raw)
		if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	q := protocol.CoordArchiveListQuery{Resource: base.Resource, Project: base.Project, Limit: 5}
	first, err := a.List(q)
	if err != nil || len(first.Items) != 4 || first.Next == "" {
		t.Fatalf("four MiB first page: %#v, %v", first, err)
	}
	wire, err := protocol.CanonicalJSON(first)
	if err != nil || len(wire)+1 > protocol.MaxCoordArchiveLookupBytes || len(wire)+1 <= protocol.MaxCoordArchiveLookupBytes-4<<10 {
		t.Fatalf("four MiB wire boundary: %d, %v", len(wire)+1, err)
	}
	q.After = first.Next
	second, err := a.List(q)
	if err != nil || len(second.Items) != 1 || second.Items[0].UID == first.Items[len(first.Items)-1].UID {
		t.Fatalf("deferred boundary item was lost or repeated: %#v, %v", second, err)
	}
}
