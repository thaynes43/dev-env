package providerallowance

import (
	"strings"
	"testing"
	"time"
)

func TestProjectCachedReadDegradesFutureStatusWithoutMovingAsOf(t *testing.T) {
	asOf := "2099-01-01T00:00:00Z"
	doc := ReadResponse{
		Schema: SchemaRead, SnapshotRevision: "cached", ServedAt: "2026-11-02T00:00:00Z",
		Items: []ReadItem{},
		SourceStatuses: []SourceStatus{{
			Provider: "openai", Availability: AvailabilityUnconfigured,
			Freshness: Freshness{State: FreshFresh, AsOf: asOf, Basis: BasisProvider},
		}},
	}
	ProjectCachedRead(&doc, nil, nil, time.Hour, true, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	if doc.SourceStatuses[0].Freshness.State != FreshExpired || doc.SourceStatuses[0].Freshness.AsOf != asOf {
		t.Fatalf("status %+v", doc.SourceStatuses[0].Freshness)
	}
	paged := doc
	cursor := "next"
	paged.NextCursor = &cursor
	before := paged.SourceStatuses[0].Freshness.State
	ProjectCachedRead(&paged, nil, nil, time.Hour, true, time.Time{})
	if paged.SourceStatuses[0].Freshness.State != before || paged.NextCursor == nil || *paged.NextCursor != "next" {
		t.Fatal("paged document was rewritten")
	}
	if strings.Contains(asOf, "secret") {
		t.Fatal("fixture")
	}
}
