package protocol

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestCoordArchiveListQueryBindsOpaqueCursor(t *testing.T) {
	q := CoordArchiveListQuery{Resource: "taskclaims", Project: "sample", Limit: 1}
	name := "20261015T123456.000000000Z-11111111-1111-1111-1111-111111111111.json"
	cursor, err := q.Cursor(name)
	if err != nil {
		t.Fatal(err)
	}
	q.After = cursor
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, err := CoordArchiveListCursorName(q); err != nil || got != name {
		t.Fatalf("cursor name: %q, %v", got, err)
	}
	q.Project = "other"
	if err := q.Validate(); err == nil {
		t.Fatal("foreign cursor accepted")
	}
	q = CoordArchiveListQuery{Resource: "taskclaims", Project: "sample", Limit: 101}
	if err := q.Validate(); err == nil {
		t.Fatal("unbounded limit accepted")
	}
	q = CoordArchiveListQuery{Resource: "taskclaims", Project: "sample", Limit: 1, After: strings.Repeat("x", 513)}
	if err := q.Validate(); err == nil {
		t.Fatal("malformed cursor accepted")
	}
}

func TestCoordArchiveListCursorRequiresCanonicalIndexName(t *testing.T) {
	q := CoordArchiveListQuery{Resource: "taskclaims", Project: "sample", Limit: 1}
	name := "20261015T123456.000000000Z-11111111-1111-1111-1111-111111111111.json"
	for _, malformed := range []string{
		strings.Repeat("x", len(name)),
		"20261015X123456.000000000Z-11111111-1111-1111-1111-111111111111.json",
		"20261315T123456.000000000Z-11111111-1111-1111-1111-111111111111.json",
	} {
		raw, err := json.Marshal(coordArchiveListCursor{Resource: q.Resource, Project: q.Project, Name: malformed})
		if err != nil {
			t.Fatal(err)
		}
		q.After = base64.RawURLEncoding.EncodeToString(raw)
		if err := q.Validate(); err == nil {
			t.Fatalf("malformed index cursor accepted: %q", malformed)
		}
	}
}
