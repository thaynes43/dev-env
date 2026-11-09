// Package eventnote bounds the note of a Kubernetes Event.
package eventnote

import "unicode/utf8"

// MaxBytes is the API server's limit on an Event note (1024 bytes, not
// characters). A longer note is rejected and the recorder does not retry it.
const MaxBytes = 1024

// Marker ends a note that was cut.
const Marker = "… (truncated)"

// Bound returns note unchanged when it fits, else a prefix of it cut on a UTF-8
// rune boundary and ended with Marker, at most MaxBytes in all.
func Bound(note string) string {
	if len(note) <= MaxBytes {
		return note
	}
	cut := MaxBytes - len(Marker)
	// Step back to the start of the rune the cut falls in. A note that is not
	// valid UTF-8 stops after at most utf8.UTFMax-1 steps and is cut there.
	for i := 0; i < utf8.UTFMax-1 && cut > 0 && !utf8.RuneStart(note[cut]); i++ {
		cut--
	}
	return note[:cut] + Marker
}
