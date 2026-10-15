package shelf

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCoordinationArchiveGetItemUsesNarrowCommandAndSharedVerification(t *testing.T) {
	m, object := coordFixture(t)
	stored := protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)}
	e := &fakeExec{out: string(mustJSON(t, protocol.CoordArchiveGetResult{Manifest: stored, Object: object}))}
	c := coordClient(t, e)

	got, err := c.GetItem(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name})
	if err != nil || string(got.Object) != string(object) || len(e.cmds) != 1 || e.cmds[0] != "agentd ctl coord-archive get-item --resource taskclaims --name "+m.Name || len(e.stdin) != 0 {
		t.Fatalf("get item = %#v, %v, commands=%#v stdin=%#v", got, err, e.cmds, e.stdin)
	}
	if _, err := c.GetItem(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name, UID: m.UID}); err != nil || e.cmds[1] != "agentd ctl coord-archive get-item --resource taskclaims --name "+m.Name+" --uid "+m.UID {
		t.Fatalf("explicit UID get item: %v, %#v", err, e.cmds)
	}

	before := len(e.cmds)
	for _, q := range []protocol.CoordArchiveGetQuery{
		{Resource: m.Resource, Name: m.Name, ResourceVersion: m.ResourceVersion},
		{Resource: m.Resource, Name: m.Name, SHA256: m.Object.SHA256},
		{Resource: "taskclaims", Name: "../escape"},
	} {
		if _, err := c.GetItem(context.Background(), q); err == nil || len(e.cmds) != before {
			t.Fatalf("invalid item query launched: %v, %#v", err, e.cmds)
		}
	}
	if _, err := (&CoordinationArchive{}).GetItem(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name, ResourceVersion: m.ResourceVersion}); err == nil {
		t.Fatal("pinned item query accepted with nil shelf")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetItem(cancelled, protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled item read = %v", err)
	}

	wrong := m
	wrong.UID = "11111111-1111-4111-8111-111111111111"
	e.out = string(mustJSON(t, protocol.CoordArchiveGetResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: wrong, ArchivedAt: stored.ArchivedAt}, Object: object}))
	if out, err := c.GetItem(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name, UID: m.UID}); err == nil || out.Object != nil {
		t.Fatalf("mismatched item response accepted: %#v, %v", out, err)
	}
	wrong = m
	wrong.Object.SHA256 = strings.Repeat("0", 64)
	e.out = string(mustJSON(t, protocol.CoordArchiveGetResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: wrong, ArchivedAt: stored.ArchivedAt}, Object: object}))
	if out, err := c.GetItem(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}); err == nil || out.Object != nil {
		t.Fatalf("bad digest item response accepted: %#v, %v", out, err)
	}
	for _, answer := range []string{"{}", `{"manifest":null,"object":null}`} {
		e.out = answer
		if out, err := c.GetItem(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}); err == nil || out.Object != nil {
			t.Fatalf("incomplete item response accepted: %#v, %v", out, err)
		}
	}
	for _, receipt := range []time.Time{time.Time{}, time.Date(2026, 10, 15, 1, 2, 3, 0, time.FixedZone("fixture", 3600))} {
		e.out = string(mustJSON(t, protocol.CoordArchiveGetResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: receipt}, Object: object}))
		if out, err := c.GetItem(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}); err == nil || out.Object != nil {
			t.Fatalf("invalid item receipt accepted: %#v, %v", out, err)
		}
	}

	e.out, e.err = `{"error":"not_found","message":"fixture"}`, coordExit(1)
	if out, err := c.GetItem(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}); out.Object != nil || err == nil {
		t.Fatalf("typed item refusal lost: %#v, %v", out, err)
	} else {
		var refusal *protocol.HistoryError
		if !errors.As(err, &refusal) || refusal.Code != protocol.HistoryErrNotFound {
			t.Fatalf("item refusal = %v", err)
		}
	}
	e.out, e.err = `{"error":"not_a_history_error","message":"fixture"}`, coordExit(1)
	if out, err := c.GetItem(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}); out.Object != nil || err == nil {
		t.Fatalf("unknown item error accepted: %#v, %v", out, err)
	} else if _, typed := err.(*protocol.HistoryError); typed {
		t.Fatalf("unknown item error became typed: %v", err)
	}

	e.err = nil
	e.out = string(mustJSON(t, protocol.CoordArchiveGetResult{Manifest: stored, Object: []byte(` {}`)}))
	if out, err := c.GetItem(context.Background(), protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name}); err == nil || out.Object != nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("noncanonical item response accepted: %#v, %v", out, err)
	}
}
