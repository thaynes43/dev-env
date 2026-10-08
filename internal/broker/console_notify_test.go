package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

type failedConsoleNotification struct{ calls int }

func (n *failedConsoleNotification) NotifyPending(context.Context, *v1alpha1.AccessGrant) error {
	n.calls++
	return errors.New("notification delivery failed")
}

func TestConsoleNotifyFailureWaitsAndTimesOut(t *testing.T) {
	f := newConsoleFixture(t, false)
	b := f.console.Broker
	n := &failedConsoleNotification{}
	b.Notifier = n
	g := f.stored(t)
	result, err := b.pending(context.Background(), g, b.now())
	if err != nil || result.RequeueAfter != 5*time.Second || f.stored(t).Status.NotifiedAt != nil {
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
