package shelf

import (
	"context"
	"errors"
	"testing"
)

func TestContextArchiveExecSlotsWaitsAndReleases(t *testing.T) {
	s := NewExecSlots(1)
	release, ok := s.TryTake()
	if !ok {
		t.Fatal("initial slot")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Take(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
	release()
	release, err := s.Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestContextArchiveCancelledFreePoolCannotAcquire(t *testing.T) {
	s := NewExecSlots(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, err := s.Take(ctx); !errors.Is(err, context.Canceled) || release != nil {
		if release != nil {
			release()
		}
		t.Fatal("cancelled context acquired ready token")
	}
	release, ok := s.TryTake()
	if !ok {
		t.Fatal("cancelled take leaked token")
	}
	release()
}

// Cancel deterministically on the post-acquisition context check, avoiding a
// scheduler-dependent race or repeated test loop.
type contextArchiveAcquireContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *contextArchiveAcquireContext) Err() error {
	c.checks++
	if c.checks == 2 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestContextArchiveCancellationAfterAcquireRestoresToken(t *testing.T) {
	s := NewExecSlots(1)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &contextArchiveAcquireContext{Context: base, cancel: cancel}
	if release, err := s.Take(ctx); !errors.Is(err, context.Canceled) || release != nil {
		if release != nil {
			release()
		}
		t.Fatal("post-acquisition cancellation returned slot")
	}
	release, ok := s.TryTake()
	if !ok {
		t.Fatal("post-acquisition cancellation leaked token")
	}
	release()
}
