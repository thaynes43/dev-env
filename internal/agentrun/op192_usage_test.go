package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// OP192 S1 release R1 (research R-07): agent-run prints a document with the
// separate Grok row unchanged, invents no Grok row for a three-row document,
// and its local preflight still emits exactly the three required rows.

func op192UsageDoc(grok bool) []byte {
	at := "2026-11-05T18:00:00Z"
	code := "source_unconfigured"
	doc := providerallowance.ReadResponse{Schema: providerallowance.SchemaRead, SnapshotRevision: "op192-usage", ServedAt: at, Items: []providerallowance.ReadItem{}}
	providers := []string{"cursor", "anthropic", "openai"}
	if grok {
		providers = append(providers, "grok")
	}
	for _, provider := range providers {
		doc.SourceStatuses = append(doc.SourceStatuses, providerallowance.SourceStatus{
			Provider: provider, ProducerRef: "op192-synthetic", Availability: providerallowance.AvailabilityUnconfigured,
			LastAttempt: providerallowance.LastAttempt{AttemptedAt: at, CompletedAt: &at, Result: providerallowance.AttemptUnknown, Code: &code},
			Freshness:   providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: at, Basis: providerallowance.BasisNone, Reason: "source not configured"},
			Validation:  providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified, ValidationRefs: []string{}},
			Binding:     providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
		})
	}
	raw, _ := json.Marshal(doc)
	return raw
}

func op192UsageProviders(t *testing.T, raw []byte) []string {
	t.Helper()
	doc, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, status := range doc.SourceStatuses {
		out = append(out, status.Provider)
	}
	return out
}

func TestOP192UsageFromFileKeepsGrokRowAndInventsNone(t *testing.T) {
	for _, grok := range []bool{true, false} {
		in := op192UsageDoc(grok)
		var out bytes.Buffer
		env := Env{Stdout: &out, Stderr: io.Discard, Stdin: bytes.NewReader(in)}
		if code := Run(context.Background(), []string{"usage", "-o", "json", "--from-file", "-"}, env); code != 0 {
			t.Fatalf("grok=%v exit %d", grok, code)
		}
		want := []string{"cursor", "anthropic", "openai"}
		if grok {
			want = append(want, "grok")
		}
		if got := op192UsageProviders(t, out.Bytes()); !reflect.DeepEqual(got, want) {
			t.Fatalf("grok=%v providers %v, want %v", grok, got, want)
		}
		if !bytes.Equal(bytes.TrimSuffix(out.Bytes(), []byte("\n")), in) {
			t.Fatalf("grok=%v document changed", grok)
		}
	}
}

func TestOP192UsageLocalPreflightEmitsNoGrokRow(t *testing.T) {
	var out bytes.Buffer
	env := Env{Stdout: &out, Stderr: io.Discard, Now: func() time.Time { return time.Date(2026, 11, 5, 19, 0, 0, 0, time.UTC) }}
	if code := Run(context.Background(), []string{"usage", "-o", "json"}, env); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := op192UsageProviders(t, out.Bytes()); !reflect.DeepEqual(got, []string{"cursor", "anthropic", "openai"}) {
		t.Fatalf("R1 preflight emitted %v", got)
	}
}
