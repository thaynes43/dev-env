package delegatedresult

import "context"

// Admission is an unimplemented integration boundary, not an authority grant.
// The adapter must authenticate the original host (never the delegate), validate
// identifiers with repository validators, bind tenant/project/repository/catalog,
// require the explicit payload-reader/privacy/retention policy, preserve Workspace
// rejection, and check the original Session/Pod/task/invocation, current binding,
// initial host exit and independently authenticated delegate completion.
// It must reject absent policy, cancellation, replacement, deletion, expiry,
// suspension and revocation. ServedModel may be Observed only from authenticated
// server-owned evidence; otherwise it must remain nil/Unknown.
// A return value is an observation, NOT a transferable lease or commit fence.
type Admission interface {
 Resolve(context.Context, Key) (Key, Provenance, error)
}

// Store is an unimplemented durable boundary. Commit MUST reauthorize initial
// writes AND replays and atomically enforce immutable identity, authoritative
// lifecycle/cancellation fence, policy, quota and tombstones alongside content,
// provenance, digest, count, retention deadline and immutable commit identifiers.
// Cancellation-first prevents commit; result-first cancellation immediately
// revokes reads/replays and schedules approved removal. Live Kubernetes rereads
// alone do not satisfy this ordering. No lease or weaker contract is chosen here.
// Success may follow only its documented durable commit. Unknown commit status
// must return ErrUnconfirmed: never infer absence or rerun a provider. A receipt
// may be returned only under the same answer-payload authorization as content.
// Implementing this interface alone is NOT evidence these requirements hold.
type Store interface {
 Commit(context.Context, Attempt) (Receipt, error)
}

// Disabled is the only included adapter. Its zero value refuses all operations.
// There is intentionally no enabled constructor, handler or production adapter.
type Disabled struct{}

func (Disabled) Resolve(context.Context, Key) (Key, Provenance, error) {
 return Key{}, Provenance{}, ErrDisabled
}

func (Disabled) Commit(context.Context, Attempt) (Receipt, error) {
 return Receipt{}, ErrDisabled
}

// Values supplies a private adapter with a defensive copy, not authorization.
// These values contain payload/provenance and must never be logged or exposed to
// metadata-only readers. Calling Values cannot acknowledge execution or storage.
func (a Attempt) Values() (Key, Provenance, Request, string) {
 p := a.provenance
 p.Host = copyAttribution(p.Host)
 p.Delegate = copyAttribution(p.Delegate)
 return a.key, p, Request{SchemaVersion: a.key.SchemaVersion, Answer: a.answer}, a.digest
}
