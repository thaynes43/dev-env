package providerallowance_test

import (
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// OP192 S1 release R1 (research R-07): the operator store and the readers
// accept a Grok row, but no producer emits one until R2 ships after R1 is
// installed. Collection.build feeds both producers through
// AssembleCaptureBundle, so it must still emit exactly the three required rows.
func TestOP192R1CaptureBundleEmitsNoGrokRow(t *testing.T) {
	trust := providerallowance.AggregateTrust{Registry: providerallowance.NewStaticRegistry(providerallowance.RegistryParts{}), MaxAge: 1800}
	raw := []byte(`{"schema":"` + providerallowance.CaptureBundleSchema + `","captures":[]}`)
	doc, err := providerallowance.AssembleCaptureBundle(raw, trust, time.Date(2026, 11, 5, 19, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cursor", "anthropic", "openai"}
	if len(doc.SourceStatuses) != len(want) || len(doc.Items) != 0 {
		t.Fatalf("statuses %+v items %d", doc.SourceStatuses, len(doc.Items))
	}
	for i, status := range doc.SourceStatuses {
		if status.Provider != want[i] {
			t.Fatalf("R1 producer emitted %q at %d", status.Provider, i)
		}
	}
}
