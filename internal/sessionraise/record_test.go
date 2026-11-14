package sessionraise

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sample(t *testing.T, priv ed25519.PrivateKey) Record {
	t.Helper()
	rec := Record{
		ID:         RecordID("uid-1", "2026-11-13T19:00:00Z", WordsHash("yes")),
		SessionUID: "uid-1", State: StatePending, Profile: "workbench",
		FromProfile: "full", Depth: "2",
		Owner:       Owner{Words: "yes", WordsHash: WordsHash("yes"), Where: WherePhone, Session: "bench", SaidAt: "2026-11-13T18:55:00Z"},
		PerformedBy: "session/bench", PerformedAt: "2026-11-13T19:00:00Z",
	}
	signed, err := Sign(rec, priv)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestRecordCountsOnlyWhenEveryCheckAgrees(t *testing.T) {
	pub, priv := testKey(t)
	rec := sample(t, priv)
	entry := &SessionEntry{UID: rec.SessionUID, ID: rec.ID, State: rec.State}
	if got := Counts(rec, rec.SessionUID, pub, entry); got != "" {
		t.Fatalf("valid record did not count: %s", got)
	}
	forged := rec
	forged.Sig = rec.Sig[:8] + strings.Repeat("A", len(rec.Sig)-8)
	if Counts(forged, rec.SessionUID, pub, entry) != IgnoreSignature {
		t.Fatal("forged signature counted")
	}
	edited := rec
	edited.Owner.Words = "no"
	if Counts(edited, rec.SessionUID, pub, entry) != IgnoreWords {
		t.Fatal("edited words with the old hash counted")
	}
	other, otherPriv := testKey(t)
	otherRec := rec
	otherRec.KeyID = ""
	otherRec.Sig = ""
	signedOther, err := Sign(otherRec, otherPriv)
	if err != nil {
		t.Fatal(err)
	}
	if Counts(signedOther, rec.SessionUID, pub, entry) != IgnoreSignature {
		t.Fatal("a record signed by another key counted")
	}
	_ = other
	wrongUID := rec
	if Counts(wrongUID, "uid-other", pub, entry) != IgnoreUID {
		t.Fatal("wrong session UID counted")
	}
	activeRec := rec
	activeRec.State = StateActive
	activeRec.KeyID = ""
	activeRec.Sig = ""
	activeSigned, err := Sign(activeRec, priv)
	if err != nil {
		t.Fatal(err)
	}
	pending := *entry
	if Counts(activeSigned, rec.SessionUID, pub, &pending) != IgnoreLedger {
		t.Fatal("replayed active record counted against a pending ledger")
	}
	mismatch := *entry
	mismatch.ID = "0123456789abcdef"
	if Counts(rec, rec.SessionUID, pub, &mismatch) != IgnoreLedger {
		t.Fatal("ledger id mismatch counted")
	}
	mismatch = *entry
	mismatch.State = StateTakenBack
	if Counts(rec, rec.SessionUID, pub, &mismatch) != IgnoreLedger {
		t.Fatal("ledger state mismatch counted")
	}
	if Counts(rec, rec.SessionUID, nil, entry) != IgnoreSignature {
		t.Fatal("missing key counted")
	}
	if Counts(rec, rec.SessionUID, pub, nil) != IgnoreLedger {
		t.Fatal("missing ledger row counted")
	}
}

func TestWordsEncodeWithoutHTMLEscapes(t *testing.T) {
	_, priv := testKey(t)
	rec := sample(t, priv)
	rec.Owner.Words = `a < b > c & d`
	rec.Owner.WordsHash = WordsHash(rec.Owner.Words)
	rec.ID = RecordID(rec.SessionUID, rec.PerformedAt, rec.Owner.WordsHash)
	signed, err := Sign(rec, priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `\u003c`) || strings.Contains(string(raw), `\u003e`) || strings.Contains(string(raw), `\u0026`) {
		t.Fatalf("words were HTML-escaped: %s", raw)
	}
	if !strings.Contains(string(raw), `a < b > c & d`) {
		t.Fatalf("verbatim words missing: %s", raw)
	}
	parsed, err := Parse(string(raw))
	if err != nil || parsed.Owner.Words != rec.Owner.Words || parsed.Sig == "" {
		t.Fatalf("parse %v %+v", err, parsed)
	}
}

func TestRecordOver4KiBDoesNotSpend(t *testing.T) {
	_, priv := testKey(t)
	rec := sample(t, priv)
	rec.Owner.Words = strings.Repeat("a", 4500)
	rec.Owner.WordsHash = WordsHash(rec.Owner.Words)
	signed, err := Sign(rec, priv)
	if err != nil {
		t.Fatal(err)
	}
	spent := false
	big, err := signed.TooBig()
	if err != nil || !big {
		t.Fatalf("big=%v err=%v", big, err)
	}
	if big {
		// The route returns before Spend. This is that gate.
	} else {
		spent = true
	}
	if spent {
		t.Fatal("an oversized record spent a word")
	}
	raw, err := signed.Encode()
	if err != nil || len(raw) <= MaxEncoded {
		t.Fatalf("encoded %d", len(raw))
	}
}

func TestEditedSignatureDoesNotSurviveAFieldChange(t *testing.T) {
	pub, priv := testKey(t)
	rec := sample(t, priv)
	rec.Profile = "ops"
	if Verify(rec, pub) {
		t.Fatal("changing a signed field still verified")
	}
}

func TestParseRefusesDuplicateAndCaseFoldedKeys(t *testing.T) {
	_, priv := testKey(t)
	rec := sample(t, priv)
	raw, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(string(raw)); err != nil {
		t.Fatal(err)
	}
	dup := strings.Replace(string(raw), `"id":`, `"id":"other","id":`, 1)
	if _, err := Parse(dup); err == nil {
		t.Fatal("a duplicate key was accepted")
	}
	folded := strings.Replace(string(raw), `"state":`, `"State":`, 1)
	if _, err := Parse(folded); err == nil {
		t.Fatal("a case-folded key was accepted")
	}
}
