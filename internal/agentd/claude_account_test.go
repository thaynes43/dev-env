package agentd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	testAccountFile = "claude-access-account.json"
	otherAccount    = "33333333-3333-4333-8333-333333333333"
)

// publishAs publishes generation g for the given account UUID.
func (rg *continueRig) publishAs(g uint64, account string) {
	a := projAccess(g, 9*time.Hour)
	a.AccountUUID = account
	publish(rg.t, rg.live, a)
	rg.pollProduction()
}

func (rg *continueRig) revoke(at time.Duration) {
	rg.appendTurn(userTurn(loginNow.Add(at)), revokedTurn(loginNow.Add(at+time.Minute)))
	rg.d.Now = func() time.Time { return loginNow.Add(at + 2*time.Minute) }
}

func (rg *continueRig) status() string {
	return projectionLoginStatus(rg.s, rg.d.now())
}

func (rg *continueRig) continueOutcome() continueReceipt {
	var rc continueReceipt
	_ = readJSONFile(rg.s.statePath(continueReceiptFile), &rc)
	return rc
}

// A higher generation for another account is merged, but sends no continue, says
// so in the status, and a later generation of the NEW account sends one for a new
// error and clears the mark.
func TestClaudeAccountChangeSkipsTheContinue(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.publishAs(2, projAccount)
	rg.revoke(2 * time.Hour)
	rg.pollProduction()
	if rg.continues() != 0 || strings.Contains(rg.status(), "account") {
		t.Fatalf("unexpected state before the change: %d %q", rg.continues(), rg.status())
	}
	rg.publishAs(3, otherAccount)
	rg.pollProduction()
	if rg.continues() != 0 {
		t.Fatal("a continue was sent for a generation of another account")
	}
	if rc := rg.continueOutcome(); rc.Generation != 3 || rc.Outcome != "account-changed" {
		t.Errorf("continue receipt %+v", rc)
	}
	if !strings.Contains(rg.status(), "account changed at generation 3") {
		t.Errorf("status does not say the account changed: %q", rg.status())
	}
	// The old account's error is not retried by the next generation.
	rg.publishAs(4, otherAccount)
	rg.pollProduction()
	if rg.continues() != 0 {
		t.Error("the old account's error was retried by a later generation")
	}
	if strings.Contains(rg.status(), "account") {
		t.Errorf("the mark did not clear on a later generation of the new account: %q", rg.status())
	}
	// A new error of the new account is handled by the next generation, once.
	rg.revoke(4 * time.Hour)
	rg.pollProduction()
	rg.publishAs(5, otherAccount)
	rg.pollProduction()
	if rg.continues() != 1 {
		t.Errorf("continues for the new account: %d", rg.continues())
	}
	rg.assertNoToken()
}

// With no account file the first run seeds it from the applied generation and
// flags nothing; the next generation of the same account behaves as before.
func TestClaudeAccountSeedWithoutFileIsNotAChange(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.publishAs(1, projAccount)
	raw, err := os.ReadFile(rg.s.statePath(testAccountFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), projAccount) || strings.Contains(string(raw), projOrg) {
		t.Error("the account file holds a raw identifier")
	}
	if strings.Contains(rg.status(), "account") {
		t.Errorf("the seed was flagged: %q", rg.status())
	}
	// An agentd upgraded in place has a receipt and no account file: it seeds from
	// the applied generation and still catches a change at the next one.
	if err := os.Remove(rg.s.statePath(testAccountFile)); err != nil {
		t.Fatal(err)
	}
	rg.pollProduction()
	if _, err := os.Stat(rg.s.statePath(testAccountFile)); err != nil || strings.Contains(rg.status(), "account") {
		t.Fatalf("not re-seeded without a flag: %v %q", err, rg.status())
	}
	rg.revoke(2 * time.Hour)
	rg.pollProduction()
	rg.publishAs(2, otherAccount)
	rg.pollProduction()
	if rg.continues() != 0 || !strings.Contains(rg.status(), "account changed at generation 2") {
		t.Errorf("the change after an in-place upgrade was missed: %d %q", rg.continues(), rg.status())
	}
}

// A flip back (A, B, A) is another change, never a silent clear; only a later
// generation that matches the recorded account clears the mark.
func TestClaudeAccountFlappingIsEachAChange(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.publishAs(1, projAccount)
	rg.revoke(2 * time.Hour)
	rg.pollProduction()
	rg.publishAs(2, otherAccount)
	rg.pollProduction()
	rg.publishAs(3, projAccount)
	rg.pollProduction()
	if !strings.Contains(rg.status(), "account changed at generation 3") {
		t.Errorf("the flip back was cleared silently: %q", rg.status())
	}
	if rg.continues() != 0 {
		t.Error("a continue was sent while the account flapped")
	}
	rg.publishAs(4, projAccount)
	rg.pollProduction()
	if strings.Contains(rg.status(), "account") {
		t.Errorf("a later generation of the recorded account did not clear: %q", rg.status())
	}
}

// An unreadable record is never read as "same account": the next poll marks the
// applied generation unconfirmed and sends no continue for it. The unreadable record
// only proves nothing about the transition into that generation; a later generation
// of the account then recorded clears the mark and the ordinary renewal continues.
func TestClaudeAccountUnreadableRecordIsConservative(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.publishAs(2, projAccount)
	rg.revoke(2 * time.Hour)
	rg.pollProduction()
	if err := os.WriteFile(rg.s.statePath(testAccountFile), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rg.pollProduction()
	if !strings.Contains(rg.status(), "account unconfirmed since generation 2") || rg.continues() != 0 {
		t.Errorf("an unreadable record was read as the same account: %d %q", rg.continues(), rg.status())
	}
	rg.publishAs(3, projAccount)
	rg.pollProduction()
	if strings.Contains(rg.status(), "account") {
		t.Errorf("the mark did not clear on a later generation of the recorded account: %q", rg.status())
	}
	if _, err := os.Stat(rg.s.statePath(testAccountFile) + ".tmp"); err == nil {
		t.Error("a temporary file was left behind")
	}
}

// The organization is part of the account: the same account UUID under another
// organization is an account change.
func TestClaudeAccountOrganizationChangeIsAnAccountChange(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.publishAs(2, projAccount)
	rg.revoke(2 * time.Hour)
	rg.pollProduction()
	a := projAccess(3, 9*time.Hour)
	a.OrganizationUUID = "44444444-4444-4444-8444-444444444444"
	publish(t, rg.live, a)
	rg.pollProduction()
	if rg.continues() != 0 || !strings.Contains(rg.status(), "account changed at generation 3") {
		t.Errorf("a changed organization was not an account change: %d %q", rg.continues(), rg.status())
	}
}

// A record that cannot be written must not stop the pod following new generations
// (a failed sync reads as login-required). The credentials merge, the receipt moves,
// the sync does not fail, and only the automatic continue stays off, shown as unconfirmed.
func TestClaudeAccountRecordWriteFailureStillMergesButSendsNoContinue(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.publishAs(2, projAccount)
	rg.revoke(2 * time.Hour)
	rg.pollProduction()
	record := rg.s.statePath(testAccountFile)
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(record, 0o700); err != nil {
		t.Fatal(err)
	}
	rg.publishAs(3, projAccount)
	rg.pollProduction()
	if rg.d.claudeSyncErr != nil {
		t.Fatalf("a record that cannot be written failed the sync: %v", rg.d.claudeSyncErr)
	}
	creds, err := readClaudeCredentials(rg.s)
	if err != nil || !strings.Contains(string(creds), projToken(3)) {
		t.Fatalf("generation 3 was not merged: %v", err)
	}
	var receipt claudeReceipt
	if err := readJSONFile(rg.s.statePath("claude-access-generation.json"), &receipt); err != nil || receipt.Generation != 3 {
		t.Errorf("the receipt did not move to generation 3: %+v %v", receipt, err)
	}
	if rg.continues() != 0 {
		t.Error("a continue was sent without a confirmed account record")
	}
	if !strings.Contains(rg.status(), "account unconfirmed") {
		t.Errorf("the missing record is not shown: %q", rg.status())
	}
}

// failAccountWrites makes only the account record's write fail, leaving the credentials
// and the receipt writable, and restores it afterwards.
func failAccountWrites(t *testing.T, fail bool) {
	t.Helper()
	old := recordAccountWrite
	if fail {
		recordAccountWrite = func(string, []byte) error { return ErrClaudeAccess }
	}
	t.Cleanup(func() { recordAccountWrite = old })
}

// A VALID but STALE record (kept at generation 2 while generation 3 for another account
// is applied) is not a confirmed record: no continue, and the status says unconfirmed.
func TestClaudeAccountStaleValidRecordSendsNoContinue(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.publishAs(2, projAccount)
	rg.revoke(2 * time.Hour)
	rg.pollProduction()
	failAccountWrites(t, true)
	rg.publishAs(3, otherAccount)
	rg.pollProduction()
	rg.pollProduction()
	if rg.d.claudeSyncErr != nil {
		t.Fatalf("the sync failed: %v", rg.d.claudeSyncErr)
	}
	if rg.continues() != 0 {
		t.Fatal("a continue was sent under another account with a stale record")
	}
	if !strings.Contains(rg.status(), "account unconfirmed") {
		t.Errorf("a stale record is not shown as unconfirmed: %q", rg.status())
	}
}

// Once the record is writable again, a pending error of the SAME account gets exactly
// one continue.
func TestClaudeAccountWritableAgainSendsExactlyOneContinue(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.publishAs(2, projAccount)
	rg.revoke(2 * time.Hour)
	rg.pollProduction()
	old := recordAccountWrite
	recordAccountWrite = func(string, []byte) error { return ErrClaudeAccess }
	rg.publishAs(3, projAccount)
	rg.pollProduction()
	if rg.continues() != 0 {
		t.Fatal("a continue was sent without a current record")
	}
	recordAccountWrite = old
	rg.pollProduction()
	if rg.continues() != 1 {
		t.Fatalf("continues once the record was writable again: %d", rg.continues())
	}
	for i := 0; i < 3; i++ {
		rg.pollProduction()
	}
	if rg.continues() != 1 {
		t.Errorf("more than one continue for one generation: %d", rg.continues())
	}
	if strings.Contains(rg.status(), "account") {
		t.Errorf("the status still mentions the account: %q", rg.status())
	}
}

// A record that is gone when a NEW generation arrives hides whether the account changed:
// it is seeded again, and that generation's continue is skipped.
func TestClaudeAccountMissingRecordAtANewGenerationSkipsThatContinue(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.publishAs(2, projAccount)
	rg.revoke(2 * time.Hour)
	rg.pollProduction()
	if err := os.Remove(rg.s.statePath(testAccountFile)); err != nil {
		t.Fatal(err)
	}
	rg.publishAs(3, projAccount)
	rg.pollProduction()
	if rg.continues() != 0 || !strings.Contains(rg.status(), "account unconfirmed since generation 3") {
		t.Errorf("a missing record at a new generation: %d %q", rg.continues(), rg.status())
	}
	rg.publishAs(4, projAccount)
	rg.pollProduction()
	if strings.Contains(rg.status(), "account") {
		t.Errorf("the mark did not clear on the next generation: %q", rg.status())
	}
}
