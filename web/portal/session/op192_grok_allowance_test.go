package session

import (
	"reflect"
	"strings"
	"testing"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

// OP192 S1 (research R-07): the portal maps a separate Grok row after Claude,
// Codex and Cursor. No Grok source is qualified, so the row is only Not
// connected or Unknown, carries no window or last-reported value, and Grok
// data never changes another row. Synthetic documents only.

func op192Status(provider string, availability pa.Availability) pa.SourceStatus {
	return pa.SourceStatus{
		Provider: provider, Availability: availability,
		Freshness:  pa.Freshness{State: pa.FreshUnknown, Basis: pa.BasisNone},
		Validation: pa.ValidationRecord{State: pa.ValidationUnverified},
		Binding:    pa.BindingRecord{State: pa.BindingUnknown},
	}
}

// op192CursorMeasured is a measured Cursor document with the three required
// statuses, shaped like TestMapProductAllowanceCursorUsesTypedFields.
func op192CursorMeasured() pa.ReadResponse {
	doc := allowanceRead()
	item := &doc.Items[0]
	item.Observation.Provider = "cursor"
	item.Observation.Source.Kind = pa.SourceNativeDisplay
	item.Observation.Scope.AccountRef = allowanceString("fixture-account")
	item.Binding.AccountRef = allowanceString("fixture-account")
	item.Observation.Measures[0].Direction = pa.DirectionUsed
	item.Observation.Measures[0].Value.Decimal = allowanceString("11")
	item.CurrentMeasures[0].Value.Decimal = allowanceString("11")
	return doc
}

func op192GrokItem() pa.ReadItem {
	item := allowanceRead().Items[0]
	item.Observation.Provider = "grok"
	item.Observation.Scope.AccountRef = allowanceString("fixture-account")
	item.Binding.AccountRef = allowanceString("fixture-account")
	item.Observation.Measures[0].Direction = pa.DirectionUsed
	item.Observation.Measures[0].Value.Decimal = allowanceString("12.5")
	item.CurrentMeasures[0].Value.Decimal = allowanceString("12.5")
	return item
}

func op192Row(t *testing.T, rows []portal.ProductAllowance, provider string) portal.ProductAllowance {
	t.Helper()
	for _, row := range rows {
		if row.Provider == provider {
			return row
		}
	}
	t.Fatalf("no %s row in %+v", provider, rows)
	return portal.ProductAllowance{}
}

func op192AssertOrder(t *testing.T, rows []portal.ProductAllowance) {
	t.Helper()
	want := []string{"Claude", "Codex", "Cursor", "Grok"}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v", rows)
	}
	for i, row := range rows {
		if row.Provider != want[i] {
			t.Fatalf("row %d is %s, want %s", i, row.Provider, want[i])
		}
	}
}

func op192AssertGrok(t *testing.T, rows []portal.ProductAllowance, value portal.ProductAllowanceState, reason string) {
	t.Helper()
	op192AssertOrder(t, rows)
	grok := rows[3]
	if grok.Value != value || grok.Reason != reason || len(grok.Windows) != 0 || len(grok.LastReported) != 0 || grok.LastReportedMore != 0 {
		t.Fatalf("grok row %+v, want %s %q", grok, value, reason)
	}
}

func TestOP192DisconnectedRowsIncludeGrok(t *testing.T) {
	rows := disconnectedAllowances()
	op192AssertOrder(t, rows)
	for _, row := range rows {
		if row.Value != portal.AllowanceNotConnected || row.Reason != "" {
			t.Fatalf("%+v", row)
		}
	}
}

func TestOP192GrokRowStates(t *testing.T) {
	threeGap := pa.ReadResponse{Schema: pa.SchemaRead}
	for _, provider := range []string{"cursor", "anthropic", "openai"} {
		threeGap.SourceStatuses = append(threeGap.SourceStatuses, op192Status(provider, pa.AvailabilityUnconfigured))
	}
	withGrok := func(doc pa.ReadResponse, status pa.SourceStatus) pa.ReadResponse {
		doc.SourceStatuses = append(append([]pa.SourceStatus(nil), doc.SourceStatuses...), status)
		return doc
	}
	next := "page-2"
	cases := []struct {
		name   string
		doc    pa.ReadResponse
		value  portal.ProductAllowanceState
		reason string
	}{
		// A stored three-row document predates the Grok row: Not connected, not Unknown.
		{"three-row-missing-grok", threeGap, portal.AllowanceNotConnected, ""},
		{"four-row-unconfigured", withGrok(threeGap, op192Status("grok", pa.AvailabilityUnconfigured)), portal.AllowanceNotConnected, ""},
		{"conflict", withGrok(threeGap, op192Status("grok", pa.AvailabilityConflict)), portal.AllowanceUnknown, "Conflicting allowance reports"},
		{"auth-needed", withGrok(threeGap, op192Status("grok", pa.AvailabilityAuthNeeded)), portal.AllowanceUnknown, "No qualified Grok source"},
		{"available-without-source", withGrok(threeGap, op192Status("grok", pa.AvailabilityAvailable)), portal.AllowanceUnknown, "No qualified Grok source"},
		{"partial-read", func() pa.ReadResponse { d := threeGap; d.NextCursor = &next; return d }(), portal.AllowanceUnknown, "Partial allowance read"},
		{"unsupported-document", pa.ReadResponse{}, portal.AllowanceUnknown, "Unsupported allowance document"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op192AssertGrok(t, MapProductAllowance(tc.doc), tc.value, tc.reason)
		})
	}
	t.Run("grok-item-never-shows-a-value", func(t *testing.T) {
		doc := withGrok(threeGap, op192Status("grok", pa.AvailabilityAvailable))
		doc.Items = append(doc.Items, op192GrokItem())
		rows := MapProductAllowance(doc)
		op192AssertGrok(t, rows, portal.AllowanceUnknown, "No qualified Grok source")
	})
}

// TestOP192GrokNeverFoldsIntoOtherRows compares every non-Grok row with and
// without a distinguishable Grok status or item. An old three-row reader sees
// exactly what this reader shows for Claude, Codex and Cursor.
func TestOP192GrokNeverFoldsIntoOtherRows(t *testing.T) {
	bases := map[string]pa.ReadResponse{}
	gap := pa.ReadResponse{Schema: pa.SchemaRead}
	for _, provider := range []string{"cursor", "anthropic", "openai"} {
		gap.SourceStatuses = append(gap.SourceStatuses, op192Status(provider, pa.AvailabilityUnconfigured))
	}
	bases["not-connected"] = gap
	bases["cursor-measured"] = op192CursorMeasured()
	grokVariants := map[string]func(pa.ReadResponse) pa.ReadResponse{
		"grok-unconfigured": func(d pa.ReadResponse) pa.ReadResponse {
			d.SourceStatuses = append(append([]pa.SourceStatus(nil), d.SourceStatuses...), op192Status("grok", pa.AvailabilityUnconfigured))
			return d
		},
		"grok-conflict": func(d pa.ReadResponse) pa.ReadResponse {
			d.SourceStatuses = append(append([]pa.SourceStatus(nil), d.SourceStatuses...), op192Status("grok", pa.AvailabilityConflict))
			return d
		},
		"grok-item": func(d pa.ReadResponse) pa.ReadResponse {
			d.Items = append(append([]pa.ReadItem(nil), d.Items...), op192GrokItem())
			return d
		},
	}
	for baseName, base := range bases {
		want := MapProductAllowance(base)[:3]
		for name, variant := range grokVariants {
			t.Run(baseName+"/"+name, func(t *testing.T) {
				got := MapProductAllowance(variant(base))
				op192AssertOrder(t, got)
				if !reflect.DeepEqual(got[:3], want) {
					t.Fatalf("a Grok row changed another row:\n got %+v\nwant %+v", got[:3], want)
				}
				for _, row := range got {
					for _, w := range row.Windows {
						if strings.Contains(w.Percent, "12.5") || strings.Contains(w.Percent, "23.5") {
							t.Fatalf("%s shows a Grok value: %+v", row.Provider, w)
						}
					}
				}
			})
		}
	}
	if cursor := op192Row(t, MapProductAllowance(bases["cursor-measured"]), "Cursor"); cursor.Value != portal.AllowanceMeasured || len(cursor.Windows) != 1 || cursor.Windows[0].Percent != "11% used" {
		t.Fatalf("cursor control %+v", cursor)
	}
}
