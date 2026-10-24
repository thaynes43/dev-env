// Package providerallowance is the Story189 source contract for account allowance.
//
// An observation uses schema providerallowance.v1. A cached read uses schema
// providerallowance.read.v1. Both are closed JSON documents. Property names
// must match the canonical field names exactly at every typed object. A case
// fold or a Unicode fold is rejected, and string values are not rewritten.
// JSON null is accepted only where the schema allows null. Required arrays
// marshal as arrays when the Go slice is nil. Decode also rejects a second
// key, an unknown field, a boolean where an integer is required, a fractional
// integer, a date-time without an offset, and input outside the caller-supplied
// limits. ValidateObservation applies the same schema, enum, time, count, and
// decimal checks to a Go value. An allowlist miss, reset-credit promotion,
// display-only source, or explicit availability conflict clears measure
// actionability.
//
// Direct evidence, source-semantic measures, and derivations stay separate.
// ValidateObservation takes the caller-supplied trusted context and registry.
// Strings inside the observation, including validationRef, bindingState,
// producerRef, and evidenceRefs, do not grant trust. Derive confirms a
// declared result only after those checks and an exact decimal comparison.
// It does not clamp an over-limit percent and does not emit a negative
// complement.
//
// UsageJSONCommand documents the existing agent-run -o json convention for a
// later read command. This package does not register that command, open a
// connection, or store observations. The seventeen transition fixtures are
// Packet B1 specifications, not collector results.
//
// FixtureParseLimits bounds the reviewed fixtures. It is not a production SLO.
package providerallowance
