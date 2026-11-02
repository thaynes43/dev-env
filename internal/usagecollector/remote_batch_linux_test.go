//go:build linux

package usagecollector

import (
	"context"
	"testing"
	"time"
)

func TestRemoteBatchBoundsEveryReaderByIntentExpiration(t *testing.T) {
	request := remoteFixture()
	until := time.Now().Add(30 * time.Millisecond)
	request.ValidUntil = until.UTC().Format(time.RFC3339Nano)
	calls := 0
	receipt, err := acquireBatchUsing(context.Background(), request, func(ctx context.Context, _ ProviderBinding, _ func() time.Time) (RemoteCapture, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(until) {
			t.Error("reader lacks exact intent expiry")
		}
		<-ctx.Done()
		return RemoteCapture{}, ErrCapture
	})
	if err != nil || calls != 1 || receipt.State != "complete" || !receipt.CleanupProven || len(receipt.Attempts) != 3 || len(receipt.Captures) != 0 {
		t.Fatal("expired intent dispatched another reader", err, calls)
	}
	for _, attempt := range receipt.Attempts {
		if attempt.Success {
			t.Fatal("expired attempt became success")
		}
	}
}
