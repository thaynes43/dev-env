package broker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

type failedConsoleNotification struct {
	calls int
	err   error
}

func (n *failedConsoleNotification) NotifyPending(context.Context, *v1alpha1.AccessGrant) error {
	n.calls++
	if n.err != nil {
		return n.err
	}
	return errors.New("notification delivery failed")
}

type consoleNotificationRetry struct{ delay time.Duration }

func (e *consoleNotificationRetry) Error() string             { return "notification delivery failed" }
func (e *consoleNotificationRetry) RetryAfter() time.Duration { return e.delay }

func TestConsoleNotifyFailureWaitsAndTimesOut(t *testing.T) {
	f := newConsoleFixture(t, false)
	b := f.console.Broker
	n := &failedConsoleNotification{err: fmt.Errorf("wrapped delivery: %w", &consoleNotificationRetry{delay: 2 * time.Minute})}
	b.Notifier = n
	g := f.stored(t)
	result, err := b.pending(context.Background(), g, b.now())
	if err != nil || result.RequeueAfter != 2*time.Minute || f.stored(t).Status.NotifiedAt != nil {
		t.Fatalf("failed notification: %+v %v", result, err)
	}
	// A restart reads the same unanswered grant and respects its original
	// deadline. No timer or controller loop needs to run to prove this.
	f.clock.SetTime(f.clock.Now().Add(PendingTimeout - time.Second))
	restarted := &Broker{Client: b.Client, APIReader: b.APIReader, Clock: b.Clock, SessionNamespace: b.SessionNamespace, PolicyNamespace: b.PolicyNamespace, Notifier: n}
	result, err = restarted.pending(context.Background(), f.stored(t), restarted.now())
	if err != nil || result.RequeueAfter != time.Second || n.calls != 2 {
		t.Fatalf("deadline retry: %+v %v; calls %d", result, err, n.calls)
	}
	f.clock.SetTime(f.clock.Now().Add(time.Second))
	if _, err := restarted.pending(context.Background(), f.stored(t), restarted.now()); err != nil {
		t.Fatal(err)
	}
	if g := f.stored(t); g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByTimeout || n.calls != 2 {
		t.Fatalf("unanswered grant: %+v; calls %d", g.Status, n.calls)
	}
}

func TestConsoleNotifyRetryFloorAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"generic notifier", errors.New("notification delivery failed")},
		{"short retry advice", &consoleNotificationRetry{delay: time.Second}},
		{"negative retry advice", &consoleNotificationRetry{delay: -time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConsoleFixture(t, false)
			b := f.console.Broker
			b.Notifier = &failedConsoleNotification{err: tc.err}
			result, err := b.pending(context.Background(), f.stored(t), b.now())
			if err != nil || result.RequeueAfter != 5*time.Second || f.stored(t).Status.NotifiedAt != nil {
				t.Fatalf("retry floor: %+v %v", result, err)
			}
		})
	}
}
