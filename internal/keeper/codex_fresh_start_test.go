package keeper

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"
)

func TestCodexEmptyJournalFreshBeginCancelAndHaltedRecovery(t *testing.T) {
	for _, halted := range []bool{false, true} {
		w, transport, publisher := codexFixture(t)
		w.halted = halted
		start := w.begin(context.Background())
		if !start.OK || start.Code != "Started" {
			t.Fatal("empty journal refused a fresh reservation")
		}
		d, err := w.load(context.Background())
		if err != nil || d.Record.Access.Generation != 0 || d.Record.Stage != codexNeedsLogin || !d.Record.Refresh.Empty() || d.Record.ResumeOnCancel {
			t.Fatal("empty reservation imported or restored authentication")
		}
		if w.begin(context.Background()).Code != "Busy" {
			t.Fatal("duplicate reservation accepted")
		}
		_ = w.tick(context.Background())
		if transport.Calls != 0 || publisher.Calls != 0 {
			t.Fatal("fresh reservation invoked rotating transport")
		}
		if result := w.cancelLogin(context.Background(), start.AttemptUID); !result.OK {
			t.Fatal("fresh reservation cancel failed")
		}
		if _, err := os.Lstat(filepath.Join(w.LoginDir, start.AttemptUID)); !os.IsNotExist(err) {
			t.Fatal("cancel retained staged attempt")
		}
		if status := w.controlStatus(context.Background()); !status.OK || status.Code != "NeedsLogin" || status.Generation != 0 {
			t.Fatal("cancel did not preserve fresh state")
		}
		if result := w.begin(context.Background()); !result.OK {
			t.Fatal("halted empty state cannot recover through fresh login")
		}
	}
}

func TestCodexExpiredEmptyReservationRemovesStagingWithoutRefresh(t *testing.T) {
	w, transport, publisher := codexFixture(t)
	start := w.begin(context.Background())
	if !start.OK {
		t.Fatal("begin failed")
	}
	w.Clock.(*clocktesting.FakeClock).Step(codexCeremonyLifetime + time.Second)
	_ = w.tick(context.Background())
	d, err := w.load(context.Background())
	if err != nil || d.Record.AttemptUID != "" || !d.Record.LoginUntil.IsZero() || d.Record.Access.Generation != 0 || !d.Record.Refresh.Empty() {
		t.Fatal("expired empty reservation was not cleared")
	}
	if _, err := os.Lstat(filepath.Join(w.LoginDir, start.AttemptUID)); !os.IsNotExist(err) {
		t.Fatal("expired fresh attempt staging remains")
	}
	if transport.Calls != 0 || publisher.Calls != 0 {
		t.Fatal("expired fresh attempt dispatched a provider request")
	}
}
