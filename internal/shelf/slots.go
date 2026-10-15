package shelf

import "context"

// ExecSlots is the per-replica limit on archive execs into the shelf
// (DESIGN-024 section 5): every history archive exec (DESIGN-025 6.4) and,
// once it lands, every coordination archive exec takes one, so archive traffic
// cannot crowd the shelf. Rescue and prune execs (D-67) take none.
type ExecSlots struct {
	free chan struct{}
}

// DefaultExecSlots is DESIGN-024's limit: two archive execs per replica.
const DefaultExecSlots = 2

// NewExecSlots returns a limit of n slots; n below 1 means one.
func NewExecSlots(n int) *ExecSlots {
	n = max(n, 1)
	s := &ExecSlots{free: make(chan struct{}, n)}
	for range n {
		s.free <- struct{}{}
	}
	return s
}

// TryTake takes a slot if one is free now, and returns its release. A caller
// with no slot comes back later; background work never queues on the shelf.
func (s *ExecSlots) TryTake() (release func(), ok bool) {
	select {
	case <-s.free:
		done := false
		return func() {
			if !done {
				done = true
				s.free <- struct{}{}
			}
		}, true
	default:
		return nil, false
	}
}

// Take waits for a slot until ctx ends. Archive HTTP reads use their own
// bounded context; background work continues to use TryTake.
func (s *ExecSlots) Take(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.free:
		done := false
		release := func() {
			if !done {
				done = true
				s.free <- struct{}{}
			}
		}
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	}
}
