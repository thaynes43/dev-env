package eventnote

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundKeepsShortNote(t *testing.T) {
	for _, n := range []string{"", "short", strings.Repeat("a", MaxBytes)} {
		if got := Bound(n); got != n {
			t.Fatalf("Bound changed a note of %d bytes", len(n))
		}
	}
}

func TestBoundCutsLongMultiByteNoteOnRuneBoundary(t *testing.T) {
	prefix := "the rescue 20261109-1006 failed: "
	// Every cut offset relative to a 2, 3 and 4 byte rune is tried by shifting
	// the prefix one ASCII byte at a time.
	for _, r := range []string{"é", "界", "😀"} {
		for pad := 0; pad < utf8.UTFMax; pad++ {
			note := strings.Repeat("x", pad) + prefix + strings.Repeat(r, 2000)
			got := Bound(note)
			if len(got) > MaxBytes {
				t.Fatalf("%q pad %d: %d bytes, over %d", r, pad, len(got), MaxBytes)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("%q pad %d: not valid UTF-8", r, pad)
			}
			if !strings.HasPrefix(got, strings.Repeat("x", pad)+prefix) {
				t.Fatalf("%q pad %d: prefix lost", r, pad)
			}
			if !strings.HasSuffix(got, Marker) {
				t.Fatalf("%q pad %d: no marker", r, pad)
			}
		}
	}
}

func TestBoundStaysWithinLimitOnInvalidUTF8(t *testing.T) {
	got := Bound(strings.Repeat("\xff", 3000))
	if len(got) > MaxBytes || !strings.HasSuffix(got, Marker) {
		t.Fatalf("got %d bytes, marker %v", len(got), strings.HasSuffix(got, Marker))
	}
}
