package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestHomeCapacityPreservesLegacyRevision(t *testing.T) {
	base, err := parse(t, example(t))
	if err != nil {
		t.Fatal(err)
	}
	// The previous algorithm marshaled the full template with only lifecycle
	// omitted. Preserve that exact revision for the historical 20Gi default.
	legacy := *base
	legacy.Lifecycle = nil
	encoded, err := json.Marshal(&legacy)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	want := "2.0.0-" + hex.EncodeToString(sum[:])[:10]
	for _, size := range []string{"20Gi", "40Gi", "100Gi", "250Gi"} {
		t.Run(size, func(t *testing.T) {
			doc := strings.Replace(example(t), "size: 20Gi", "size: "+size, 1)
			got, err := parse(t, doc)
			if err != nil {
				t.Fatal(err)
			}
			if got.Revision() != want {
				t.Fatalf("capacity %s changed legacy revision: got %s, want %s", size, got.Revision(), want)
			}
			if got.Home.Size.String() != size {
				t.Fatalf("revision calculation changed allocation to %s, want %s", got.Home.Size.String(), size)
			}
		})
	}
	changed, err := parse(t, strings.Replace(example(t), "storageClassName: gasha01-rbd", "storageClassName: other-rbd", 1))
	if err != nil {
		t.Fatal(err)
	}
	if changed.Revision() == want {
		t.Fatal("a storage class change must remain revision-significant")
	}
}
