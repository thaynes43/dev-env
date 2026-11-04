package agentd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/thaynes43/dev-env/internal/claudeauth"
	"github.com/thaynes43/dev-env/internal/codexauth"
)

// The consumer follows whatever generation the keeper projects, and the account
// fence lives on the publisher side. This file adds a stricter consumer-side
// fence, in a file of its own: the generation receipt is strict-decoded, so a
// new member there would make a rolled-back older agentd refuse it. An older
// agentd simply ignores this file.
//
// The file holds only a fingerprint of the account and organization, never the
// identifiers. Rules, applied when a generation is merged:
//   - no file: seed it from this generation; an unknown earlier account is not a change;
//   - same fingerprint at a later generation: the change mark clears;
//   - a different fingerprint at a later generation, or an unreadable file: the
//     generation is marked changed. A flip back (A, B, A) is another change, never
//     a silent clear: only a later generation that matches the recorded account clears.
// A changed generation sends no automatic continue.

const (
	accountFile     = "claude-access-account.json"
	accountMaxBytes = 4 << 10
	// continueOutcomeAccountChanged is the continue receipt outcome for a changed account.
	continueOutcomeAccountChanged = "account-changed"
)

type claudeAccount struct {
	Version     int    `json:"version"`
	Fingerprint string `json:"fingerprint"`
	Generation  uint64 `json:"generation"`
	// Changed is the generation at which the account differed, or 0.
	Changed uint64 `json:"changedGeneration"`
	// Basis is "changed" (a different fingerprint) or "unreadable" (the earlier
	// record could not be believed); empty when Changed is 0.
	Basis string `json:"basis,omitempty"`
}

type accountState int

const (
	accountAbsent accountState = iota
	accountValid
	accountInvalid
)

func accountFingerprint(a claudeauth.Access) string {
	sum := sha256.Sum256([]byte("claude-account\x00" + a.AccountUUID + "\x00" + a.OrganizationUUID))
	return hex.EncodeToString(sum[:])
}

func readAccountFile(s Settings) (claudeAccount, accountState) {
	path := s.statePath(accountFile)
	raw, err := readBounded(path, accountMaxBytes)
	if err != nil {
		if isNotExist(err) {
			return claudeAccount{}, accountAbsent
		}
		return claudeAccount{}, accountInvalid
	}
	var m claudeAccount
	if codexauth.DecodeStrict(raw, &m) != nil || m.Version != 1 || len(m.Fingerprint) != 64 || m.Generation == 0 ||
		(m.Changed != 0 && (m.Changed > m.Generation || (m.Basis != "changed" && m.Basis != "unreadable"))) ||
		(m.Changed == 0 && m.Basis != "") {
		return claudeAccount{}, accountInvalid
	}
	return m, accountValid
}

// recordClaudeAccount keeps the account record in step with the generation about
// to be applied. It writes only when the record changes.
func recordClaudeAccount(s Settings, a claudeauth.Access) error {
	fp := accountFingerprint(a)
	cur, state := readAccountFile(s)
	next := claudeAccount{Version: 1, Fingerprint: fp, Generation: a.Generation}
	switch {
	case state == accountAbsent:
	case state == accountInvalid:
		next.Changed, next.Basis = a.Generation, "unreadable"
	case a.Generation <= cur.Generation:
		return nil
	case cur.Fingerprint != fp:
		next.Changed, next.Basis = a.Generation, "changed"
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return ErrClaudeAccess
	}
	if writeClaudePrivateAtomic(s.statePath(accountFile), raw) != nil {
		return ErrClaudeAccess
	}
	return nil
}

// accountConfirmed reads the record against the applied generation. A record is
// confirmed only when it is valid, current for that generation and does not mark it changed.
// A record that is absent, unreadable or behind the receipt (its write failed) is not.
type accountVerdict int

const (
	accountOK accountVerdict = iota
	accountChanged
	accountUnconfirmed
)

func accountVerdictFor(s Settings, generation uint64) (accountVerdict, claudeAccount) {
	cur, state := readAccountFile(s)
	switch {
	case state != accountValid || cur.Generation != generation:
		return accountUnconfirmed, cur
	case cur.Changed != 0 && cur.Changed == generation:
		return accountChanged, cur
	}
	return accountOK, cur
}

// accountStatusNote is appended to the projection status line.
func accountStatusNote(s Settings) string {
	var receipt claudeReceipt
	if readJSONFile(s.statePath("claude-access-generation.json"), &receipt) != nil || receipt.Generation == 0 {
		return ""
	}
	verdict, cur := accountVerdictFor(s, receipt.Generation)
	g := strconv.FormatUint(cur.Changed, 10)
	switch {
	case verdict == accountUnconfirmed:
		return "; account unconfirmed: the account record is not current for generation " + strconv.FormatUint(receipt.Generation, 10) + ", so no automatic continue is sent"
	case verdict == accountChanged && cur.Basis == "unreadable":
		return "; account unconfirmed since generation " + g + ": the earlier account record could not be read, so no automatic continue is sent for it"
	case verdict == accountChanged:
		return "; account changed at generation " + g + ": the keeper projected a different account, so no automatic continue is sent for it"
	}
	return ""
}

// skipContinueForAccount reports whether the automatic continue must not run. A
// changed generation also gets its continue receipt, so the old account's error is
// not retried by a later generation. An unconfirmed one writes nothing: the next
// poll looks again, after the record has been rewritten.
func skipContinueForAccount(s Settings, generation uint64, now time.Time, havePrev bool, prev continueReceipt) bool {
	switch verdict, _ := accountVerdictFor(s, generation); verdict {
	case accountOK:
		return false
	case accountChanged:
		if !havePrev || prev.Generation < generation || prev.Outcome == "waiting" {
			_ = writeJSONFile(s.statePath(continueReceiptFile), continueReceipt{Generation: generation, Outcome: continueOutcomeAccountChanged, EvaluatedAt: now})
		}
	}
	return true
}
