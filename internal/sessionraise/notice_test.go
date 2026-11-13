package sessionraise

import (
	"strings"
	"testing"
	"time"
)

func TestActivationWaitsTwoMinutesAndIgnoresBadOutcomes(t *testing.T) {
	pub, priv := testKey(t)
	now := time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC)
	entry := SessionEntry{UID: "uid-1", ID: "abcdef0123456789", State: StatePending}
	sentAt := now.Format(time.RFC3339)
	sent, err := SignNotice(Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: KindRaise, Attempt: 1,
		Outcome: OutcomeSent, At: sentAt, SentAt: sentAt,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if Activation(now.Add(EffectDelay-time.Second), entry, []Notice{sent}, pub) != ActionNone {
		t.Fatal("activated before two minutes")
	}
	if Activation(now.Add(EffectDelay), entry, []Notice{sent}, pub) != ActionActivate {
		t.Fatal("did not activate at two minutes")
	}
	failed, err := SignNotice(Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: KindRaise, Attempt: 1,
		Outcome: OutcomeFailed, At: sentAt,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	ambiguous, err := SignNotice(Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: KindRaise, Attempt: 2,
		Outcome: OutcomeAmbiguous, At: sentAt,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if Activation(now.Add(time.Hour), entry, []Notice{failed, ambiguous}, pub) != ActionNone {
		t.Fatal("failed or ambiguous activated")
	}
	unsigned := sent
	unsigned.Sig = ""
	if Activation(now.Add(time.Hour), entry, []Notice{unsigned}, pub) != ActionNone {
		t.Fatal("unsigned Sent activated")
	}
	forged := sent
	forged.Sig = strings.Repeat("A", len(sent.Sig))
	if Activation(now.Add(time.Hour), entry, []Notice{forged}, pub) != ActionNone {
		t.Fatal("forged Sent activated")
	}
	takeBack, err := SignNotice(Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: KindTakeBack, Attempt: 1,
		Outcome: OutcomeSent, At: sentAt, SentAt: sentAt,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if Activation(now.Add(time.Hour), entry, []Notice{takeBack}, pub) != ActionNone {
		t.Fatal("a take-back Sent activated a raise")
	}
	taken := entry
	taken.State = StateTakenBack
	if Activation(now.Add(time.Hour), taken, []Notice{sent}, pub) != ActionNone {
		t.Fatal("taken-back row activated")
	}
	unannounced := entry
	unannounced.State = StateUnannounced
	if Activation(now.Add(time.Hour), unannounced, []Notice{sent}, pub) != ActionNone {
		t.Fatal("unannounced row activated")
	}
}

func TestUnannouncedAfterTwelveAttempts(t *testing.T) {
	pub, priv := testKey(t)
	now := time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC)
	entry := SessionEntry{UID: "uid-1", ID: "abcdef0123456789", State: StatePending}
	n, err := SignNotice(Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: KindRaise, Attempt: NoticeAttempts,
		Outcome: OutcomeFailed, At: now.Format(time.RFC3339),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if Activation(now, entry, []Notice{n}, pub) != ActionUnannounce {
		t.Fatal("twelve failures left the raise pending")
	}
	reserved, err := SignNotice(Notice{
		ID: entry.ID, SessionUID: entry.UID, Kind: KindRaise, Attempt: NoticeAttempts,
		Outcome: OutcomeReserved, At: now.Format(time.RFC3339),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if Activation(now, entry, []Notice{reserved}, pub) != ActionNone {
		t.Fatal("a reservation counted as the twelfth attempt")
	}
}

func TestRaiseMessageNamesTheEffectAndTheTakeBack(t *testing.T) {
	title, body := Message(NoticeText{
		Session: "bench", Title: "a title", Performer: "session/bench",
		From: "full", Profile: "workbench", Superuser: true, StartSessions: true,
		Words: "yes", Where: WherePhone, SaidAt: "2026-11-13T18:55:00Z",
		Effect: "2026-11-13T19:02:00Z",
	}, func(s string) string { return strings.ReplaceAll(s, "yes", "REDACTED") })
	if !strings.Contains(title, "bench") || !strings.Contains(body, "superuser account") || !strings.Contains(body, "one level deeper") || !strings.Contains(body, "REDACTED") || !strings.Contains(body, "agent-run raise bench --take-back") {
		t.Fatalf("message: %s\n%s", title, body)
	}
	title, body = Message(NoticeText{Session: "bench", Title: "t", TakeBack: true, TakenBy: "human/tom", TakenAt: "2026-11-13T19:05:00Z"}, nil)
	if !strings.Contains(body, "taken back by human/tom") || strings.Contains(body, "superuser") {
		t.Fatalf("take-back message: %s %s", title, body)
	}
}

func TestNoticeSignatureCoversSentAt(t *testing.T) {
	pub, priv := testKey(t)
	n, err := SignNotice(Notice{ID: "id", SessionUID: "uid", Kind: KindRaise, Attempt: 1, Outcome: OutcomeSent, At: "2026-11-13T19:00:00Z", SentAt: "2026-11-13T19:00:00Z"}, priv)
	if err != nil || !VerifyNotice(n, pub) {
		t.Fatal(err)
	}
	n.SentAt = ""
	if VerifyNotice(n, pub) {
		t.Fatal("clearing SentAt still verified")
	}
}
