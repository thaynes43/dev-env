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
