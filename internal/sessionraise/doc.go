// Package sessionraise is DESIGN-066 slice (a): the raise record, its
// signature, the operator ledger, and the notice outcome. It grants nothing
// by itself. A record counts only when Record.Counts says so, and a create
// is admitted only by the API's depth exemption, which calls Counts and
// then requires an active record that grants startSessions.
//
// The private keys never enter this package except as bytes a caller read
// back from the owner's Secret. Tests generate keys with crypto/ed25519.
package sessionraise
