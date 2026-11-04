package authority

import (
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
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
// It is scheduledwork.SpecHash, the one source the executor also records an
// occurrence's specHash with, so a human approval and a claim can never
// disagree on the hash. TestSpecHashPR903Golden pins the hash on #903's own
// fixture, and the other tests pin the bytes, the field coverage and the set
// order through this alias. scheduledwork must never import this package.
func SpecHash(spec v1alpha1.ScheduledWorkSpec) string {
	return scheduledwork.SpecHash(spec)
}
