// Package codexhostpool chooses a Codex host from facts the caller already
// has. Select is pure: the standard library only, no I/O, and the clock is an
// argument. It does not write a lease, resume a host, create a host, or read
// the cluster. DESIGN-063 slice 1, story 698.
package codexhostpool
