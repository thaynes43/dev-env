package agentd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/claudeauth"
)

// The chain below is synthetic and credential-free. It runs the production poll
// sequence (Daemon.pollClaude) so the order is pinned, not mirrored: a published
// generation is merged, a 401 turn is seen, and exactly one continue is sent.

func (rg *continueRig) pollProduction() { rg.d.pollClaude(context.Background()) }

// publishKubelet writes the projection the way a kubelet Secret volume updates it:
// a fresh timestamped directory, then an atomic swap of the ..data symlink.
func publishKubelet(t *testing.T, live string, gen int, a claudeauth.Access) {
	t.Helper()
	raw, err := claudeauth.Encode(a, loginNow)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(live)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(dir, "..2026_11_04_00_00_0"+string(rune('0'+gen)))
	if err := os.MkdirAll(stamp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stamp, filepath.Base(live)), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "..data_tmp")
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Base(stamp), tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(live); err != nil {
		if err := os.Symlink(filepath.Join("..data", filepath.Base(live)), live); err != nil {
			t.Fatal(err)
		}
	}
}

// One production poll merges the new generation and then continues. Running the
// continue before the merge, or without the merge, sends nothing in that poll.
func TestClaudeChainPollClaudePinsProductionOrder(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	publish(t, rg.live, projAccess(1, 9*time.Hour))
	rg.pollProduction()
	rg.appendTurn(userTurn(loginNow.Add(2*time.Hour)), revokedTurn(loginNow.Add(2*time.Hour+time.Minute)))
	rg.d.Now = func() time.Time { return loginNow.Add(3 * time.Hour) }
	rg.pollProduction()
	if rg.continues() != 0 {
		t.Fatal("sent before a new generation was published")
	}
	publish(t, rg.live, projAccess(2, 9*time.Hour))
	rg.pollProduction()
	if rg.continues() != 1 {
		t.Fatalf("one production poll must merge generation 2 and then continue once: %d", rg.continues())
	}
}

// keeper-format bytes -> kubelet layout -> merge -> 401 turn -> one continue, and
// no second continue for the same generation across polls and a restart.
func TestClaudeChainPublishToContinueOnce(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	publishKubelet(t, rg.live, 1, projAccess(1, 9*time.Hour))
	rg.pollProduction()
	if rg.continues() != 0 {
		t.Fatal("sent on a healthy first generation")
	}
	rg.appendTurn(userTurn(loginNow.Add(2*time.Hour)), revokedTurn(loginNow.Add(2*time.Hour+time.Minute)))
	rg.d.Now = func() time.Time { return loginNow.Add(3 * time.Hour) }
	rg.pollProduction()
	if rg.continues() != 0 {
		t.Fatal("sent before generation 2")
	}
	publishKubelet(t, rg.live, 2, projAccess(2, 9*time.Hour))
	rg.pollProduction()
	if rg.continues() != 1 {
		t.Fatalf("continues after generation 2: %d", rg.continues())
	}
	for i := 0; i < 5; i++ {
		rg.pollProduction()
	}
	rg.restart()
	rg.d.Now = func() time.Time { return loginNow.Add(3 * time.Hour) }
	rg.pollProduction()
	if rg.continues() != 1 {
		t.Errorf("repeated polls or a restart sent again: %d", rg.continues())
	}
	var rc continueReceipt
	if err := readJSONFile(rg.s.statePath(continueReceiptFile), &rc); err != nil || rc.Generation != 2 || rc.Outcome != "sent" {
		t.Errorf("receipt %+v %v", rc, err)
	}
	rg.assertNoToken()
}

// Material that must not start a retry: a lower generation, a
// document naming refresh material, and a send whose receipt says it may have happened.
func TestClaudeChainRefusedMaterialNeverRetries(t *testing.T) {
	setup := func(t *testing.T) *continueRig {
		rg := newContinueRig(t, protocol.ModeRemote, true, true)
		rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
		publish(t, rg.live, projAccess(2, 9*time.Hour))
		rg.pollProduction()
		rg.appendTurn(userTurn(loginNow.Add(2*time.Hour)), revokedTurn(loginNow.Add(2*time.Hour+time.Minute)))
		rg.d.Now = func() time.Time { return loginNow.Add(3 * time.Hour) }
		rg.pollProduction()
		if rg.continues() != 0 {
			t.Fatal("sent without a new generation")
		}
		return rg
	}
	t.Run("lower generation", func(t *testing.T) {
		rg := setup(t)
		publish(t, rg.live, projAccess(1, 9*time.Hour))
		rg.pollProduction()
		if rg.continues() != 0 {
			t.Error("a rolled-back generation started a continue")
		}
		var receipt claudeReceipt
		if err := readJSONFile(rg.s.statePath("claude-access-generation.json"), &receipt); err != nil || receipt.Generation != 2 {
			t.Errorf("a lower generation was merged: receipt %+v %v", receipt, err)
		}
	})
	t.Run("refresh material in the document", func(t *testing.T) {
		rg := setup(t)
		raw, err := claudeauth.Encode(projAccess(3, 9*time.Hour), loginNow)
		if err != nil {
			t.Fatal(err)
		}
		tampered := strings.Replace(string(raw), "{", `{"refreshToken":"`+canaryRefresh+`",`, 1)
		writeRaw(t, rg.live, []byte(tampered))
		rg.pollProduction()
		if rg.continues() != 0 {
			t.Error("a document naming refresh material started a continue")
		}
		rg.assertNoToken()
	})
	t.Run("a send that may have happened", func(t *testing.T) {
		rg := setup(t)
		if err := writeJSONFile(rg.s.statePath(continueReceiptFile), continueReceipt{Generation: 3, Outcome: "sending", EvaluatedAt: loginNow.Add(3 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		publish(t, rg.live, projAccess(3, 9*time.Hour))
		rg.pollProduction()
		rg.pollProduction()
		if rg.continues() != 0 {
			t.Error("repeated an uncertain send")
		}
	})
}

// Characterization, not a requirement: the consumer has no account binding of its
// own. It merges whatever generation the keeper projects, so a higher generation
// for another account is applied and starts one continue. The account fence lives
// on the publisher side (the keeper refuses another account). If the consumer
// should bind the account too, that is a new decision with its own change.
func TestClaudeChainConsumerFollowsTheKeeperAccount(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	publish(t, rg.live, projAccess(2, 9*time.Hour))
	rg.pollProduction()
	rg.appendTurn(userTurn(loginNow.Add(2*time.Hour)), revokedTurn(loginNow.Add(2*time.Hour+time.Minute)))
	rg.d.Now = func() time.Time { return loginNow.Add(3 * time.Hour) }
	rg.pollProduction()
	other := projAccess(3, 9*time.Hour)
	other.AccountUUID = "33333333-3333-4333-8333-333333333333"
	publish(t, rg.live, other)
	rg.pollProduction()
	if rg.continues() != 1 {
		t.Fatalf("expected the current behaviour (one continue after a merged generation): %d", rg.continues())
	}
	rg.pollProduction()
	if rg.continues() != 1 {
		t.Error("a second continue for the same generation")
	}
}
