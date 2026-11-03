package agentd

import (
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The propagation gap (tracker story 385, consumer side). The keeper's next
// generation reaches the pod through a Secret volume, so it can trail a
// revocation by about a minute. Every time here is relative to t0, the moment
// the session's access token is rejected; every generation number and token is
// synthetic. The agent polls every five seconds (cmd/agentd resultPoll).
const gapPoll = 5 * time.Second

// gapRig is a running Claude TUI whose first generation was applied and judged
// healthy, then revoked at t0.
func gapRig(t *testing.T) (rg *continueRig, t0 time.Time) {
	t.Helper()
	rg = newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.generation(1)
	t0 = loginNow.Add(2 * time.Hour)
	rg.appendTurn(userTurn(t0.Add(-time.Second)), revokedTurn(t0))
	return rg, t0
}

// pollsUntil runs the daemon's polls every gapPoll from from to until, at the
// injected clock, and returns the clock after the last one.
func (rg *continueRig) pollsUntil(from, until time.Time) time.Time {
	now := from
	for ; !now.After(until); now = now.Add(gapPoll) {
		rg.d.Now = func() time.Time { return now }
		rg.poll()
	}
	return now
}

// The observed case: revocation at t0, the next generation visible 59 s later,
// one continue on the poll that merges it, none before.
func TestGapRevocationThenGenerationAtFiftyNineSeconds(t *testing.T) {
	rg, t0 := gapRig(t)
	now := rg.pollsUntil(t0, t0.Add(58*time.Second))
	if rg.continues() != 0 {
		t.Fatalf("a continue was sent before a new generation was visible")
	}
	at := t0.Add(59 * time.Second)
	rg.d.Now = func() time.Time { return at }
	rg.generation(2)
	if rg.continues() != 1 {
		t.Fatalf("continues one poll after generation 2: %d (clock %v)", rg.continues(), now.Sub(t0))
	}
	rg.pollsUntil(at.Add(gapPoll), at.Add(time.Minute))
	if rg.continues() != 1 {
		t.Errorf("more than one continue for generation 2: %d", rg.continues())
	}
	assertConverged(t, rg.s, 2, 9*time.Hour)
	rg.assertNoToken()
}

// A publish that is delayed tens of seconds, then minutes, still produces
// exactly one continue, and nothing is sent or retried while it is pending.
func TestGapDelayedPublishSendsOnceWhenItLands(t *testing.T) {
	for _, delay := range []time.Duration{30 * time.Second, 119 * time.Second, 10 * time.Minute} {
		rg, t0 := gapRig(t)
		rg.pollsUntil(t0, t0.Add(delay-gapPoll))
		if rg.continues() != 0 {
			t.Fatalf("delay %v: a continue went out while the generation was pending", delay)
		}
		rg.d.Now = func() time.Time { return t0.Add(delay) }
		rg.generation(2)
		rg.pollsUntil(t0.Add(delay+gapPoll), t0.Add(delay+time.Minute))
		if rg.continues() != 1 {
			t.Errorf("delay %v: continues %d", delay, rg.continues())
		}
	}
}

// A stale generation (the old document re-served, or an older one) is refused,
// does not spend the recovery and does not hide the real next generation.
func TestGapStaleGenerationDoesNotSpendTheRecovery(t *testing.T) {
	rg, t0 := gapRig(t)
	rg.generation(2)
	rg.d.Now = func() time.Time { return t0.Add(10 * time.Second) }
	rg.poll()
	if rg.continues() != 1 {
		t.Fatalf("setup: continues %d", rg.continues())
	}
	// A new revocation of generation 2, then generation 1 is served again.
	t1 := t0.Add(time.Hour)
	rg.appendTurn(userTurn(t1.Add(-time.Second)), revokedTurn(t1))
	rg.d.Now = func() time.Time { return t1.Add(5 * time.Second) }
	rg.generation(1)
	rg.poll()
	if rg.continues() != 1 {
		t.Errorf("a stale generation sent a continue: %d", rg.continues())
	}
	rg.d.Now = func() time.Time { return t1.Add(59 * time.Second) }
	rg.generation(3)
	if rg.continues() != 2 {
		t.Errorf("generation 3 after a stale document: %d continues", rg.continues())
	}
	assertConverged(t, rg.s, 3, 9*time.Hour)
}

// Two readers of the same projection (the poll and any other caller) leave the
// credentials whole and converge on the newest generation. Run under -race.
func TestGapConcurrentReadersConverge(t *testing.T) {
	rg, t0 := gapRig(t)
	publish(t, rg.live, projAccess(2, 9*time.Hour))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := SyncClaudeAccess(rg.s, t0.Add(time.Minute)); err != nil {
					t.Errorf("sync: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	assertConverged(t, rg.s, 2, 9*time.Hour)
}

// agentd restarts inside the gap: the fresh process keeps the fence, sends
// nothing while the generation is pending, and sends once when it lands.
func TestGapRestartInsideTheGap(t *testing.T) {
	rg, t0 := gapRig(t)
	rg.pollsUntil(t0, t0.Add(20*time.Second))
	rg.restart()
	rg.pollsUntil(t0.Add(25*time.Second), t0.Add(55*time.Second))
	if rg.continues() != 0 {
		t.Fatal("a restart inside the gap sent a continue")
	}
	rg.d.Now = func() time.Time { return t0.Add(59 * time.Second) }
	rg.generation(2)
	rg.restart()
	rg.pollsUntil(t0.Add(60*time.Second), t0.Add(2*time.Minute))
	if rg.continues() != 1 {
		t.Errorf("continues after a restart around the landing: %d", rg.continues())
	}
}
