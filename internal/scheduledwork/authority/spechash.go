package authority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// SpecHash is the SHA-256, in lowercase hex, of every ScheduledWorkSpec field
// except editor and cancelled (DESIGN-054 "Data contract"): the hash a human
// approval binds to (status.approval.specHash) and an occurrence records at
// its claim. Any other edit, title, notify and calendar included, changes it.
//
// The bytes hashed are encoding/json's encoding of the spec after three
// normalisations: editor is emptied, cancelled is false, and notify and
// recurrence.byDay, which are sets, are sorted. encoding/json writes the keys
// in the struct's declaration order, so the hashed object is, in order:
//
//	title, owner, editor (always ""), due, timeZone,
//	recurrence {freq, byDay (sorted), byMonthDay, until, count} (absent when nil),
//	notAfter (RFC 3339, UTC, seconds), mode,
//	work {template, repo, prompt},
//	notify (sorted; absent when empty), calendar (absent when empty)
//
// with cancelled always absent (false and omitempty). An absent optional field
// and its zero value hash the same. Reordering the struct's fields changes
// every hash and voids every stored approval: TestSpecHashGolden pins the
// bytes, and TestSpecHashCoversEveryField fails on a field that is neither
// hashed nor listed as excluded.
//
// It is byte-for-byte the algorithm of scheduledwork.SpecHash in S3b1 (PR
// #903), which the executor uses for an occurrence's specHash; the two must
// agree, or a human approval could never match a claim.
func SpecHash(spec v1alpha1.ScheduledWorkSpec) string {
	sum := sha256.Sum256(specHashInput(spec))
	return hex.EncodeToString(sum[:])
}

// specHashInput is the canonical encoding SpecHash hashes.
func specHashInput(spec v1alpha1.ScheduledWorkSpec) []byte {
	s := *spec.DeepCopy()
	s.Editor, s.Cancelled = "", false
	slices.Sort(s.Notify)
	if s.Recurrence != nil {
		slices.Sort(s.Recurrence.ByDay)
	}
	// A spec of strings, integers, booleans and times always marshals.
	b, _ := json.Marshal(s)
	return b
}
