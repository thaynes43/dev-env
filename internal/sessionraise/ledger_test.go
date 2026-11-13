package sessionraise

import (
	"errors"
	"testing"
	"time"
)

func admitIn(uid, performer, words string) AdmitInput {
	hash := WordsHash(words)
	at := "2026-11-13T19:00:00Z"
	return AdmitInput{
		Name: "target", UID: uid, ID: RecordID(uid, at, hash),
		Profile: "workbench", FromProfile: "full", Depth: "2",
		WordsHash: hash, PerformedBy: performer, PerformedAt: at,
		Where: WhereOwn, SaidAt: at,
	}
}

func TestUsedWordsHoldAcrossTargetsAndRestart(t *testing.T) {
	now := time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC)
	l := &Ledger{}
	if _, err := l.Admit(now, admitIn("uid-a", "session/bench", "yes"), nil); err != nil {
		t.Fatal(err)
	}
	_, err := l.Admit(now.Add(2*time.Minute), admitIn("uid-b", "session/bench", "Yes."), nil)
	if !errors.Is(err, ErrUsedWords) {
		t.Fatalf("same word on another target: %v", err)
	}
	raw, err := l.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	again, err := Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = again.Admit(now.Add(3*time.Minute), admitIn("uid-c", "session/bench", "Ｙｅｓ"), nil)
	if !errors.Is(err, ErrUsedWords) {
		t.Fatalf("after reload: %v", err)
	}
	if _, err := again.Admit(now.Add(4*time.Minute), admitIn("uid-c", "session/other", "yes"), nil); err != nil {
		t.Fatalf("another performer: %v", err)
	}
}

func TestRateLimitsHoldAcrossRestart(t *testing.T) {
	now := time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC)
	l := &Ledger{}
	for i, word := range []string{"one", "two", "three"} {
		if _, err := l.Admit(now.Add(time.Duration(i)*time.Minute), admitIn("uid-"+word, "session/bench", word), nil); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := l.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	again, err := Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = again.Admit(now.Add(4*time.Minute), admitIn("uid-four", "session/bench", "four"), nil)
	var rate *RateError
	if !errors.As(err, &rate) || rate.Target {
		t.Fatalf("hourly limit after reload: %v", err)
	}
	_, err = again.Admit(now.Add(10*time.Second), admitIn("uid-one", "session/bench", "fresh"), nil)
	if !errors.As(err, &rate) || !rate.Target {
		t.Fatalf("target gap after reload: %v", err)
	}
}

func TestTakeBackIsExemptFromTheLimits(t *testing.T) {
	now := time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC)
	l := &Ledger{}
	if _, err := l.Admit(now, admitIn("uid-a", "session/bench", "yes"), nil); err != nil {
		t.Fatal(err)
	}
	entry, err := l.TakeBack("uid-a", "session/bench", now.Add(time.Second).Format(time.RFC3339), now.Add(time.Second))
	if err != nil || entry.State != StateTakenBack {
		t.Fatalf("take-back %v %+v", err, entry)
	}
	if _, err := l.TakeBack("missing", "session/bench", now.Format(time.RFC3339), now); !errors.Is(err, ErrNoEntry) {
		t.Fatalf("missing: %v", err)
	}
}

func TestSameRaiseDoesNotSpendAgain(t *testing.T) {
	now := time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC)
	l := &Ledger{}
	first, err := l.Admit(now, admitIn("uid-a", "session/bench", "yes"), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.Admit(now, admitIn("uid-a", "session/bench", "yes"), nil)
	if err != nil || !second.Same || second.Entry.ID != first.Entry.ID || len(l.Words) != 1 {
		t.Fatalf("retry wrote again: %+v words %d err %v", second, len(l.Words), err)
	}
}

func TestFullLedgerNamesADate(t *testing.T) {
	now := time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC)
	l := &Ledger{}
	for i := 0; i < MaxWordUses; i++ {
		l.Words = append(l.Words, WordUse{Performer: "session/bench", Hash: WordsHash(string(rune(i))), At: now})
	}
	_, err := l.Admit(now, admitIn("uid-a", "session/other", "please"), nil)
	var full *FullError
	if !errors.As(err, &full) || !full.Resume.Equal(now.Add(WordRetention)) {
		t.Fatalf("full: %v", err)
	}
}

func TestFinishDoesNotOverwriteTakeBack(t *testing.T) {
	now := time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC)
	l := &Ledger{}
	in := admitIn("uid-a", "session/bench", "yes")
	if _, err := l.Admit(now, in, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.TakeBack("uid-a", "human/tom", now.Format(time.RFC3339), now); err != nil {
		t.Fatal(err)
	}
	entry, ok := l.Finish("uid-a", in.ID, StateActive, now.Format(time.RFC3339), now.Add(EffectDelay).Format(time.RFC3339), now.Add(EffectDelay))
	if ok || entry.State != StateTakenBack {
		t.Fatalf("activation overwrote take-back: ok %v %+v", ok, entry)
	}
}
