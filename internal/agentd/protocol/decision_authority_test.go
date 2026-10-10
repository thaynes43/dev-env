package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestDecisionAuthorityBindsAllApprovedContentAndIdentity(t *testing.T) {
	created := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	answered := created.Add(time.Second)
	record := DecisionRecord{Version: 1, ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Session: "synthetic-task", SessionUID: "synthetic-session-uid", PodUID: "synthetic-pod-uid", ThreadID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", WriterGeneration: 3, Question: DecisionQuestion{Question: "Synthetic choice?", Options: []string{"A", "B"}, Context: "Synthetic consequences"}, State: "Answered", CreatedAt: created, AnsweredAt: &answered, Answer: "A"}
	authority := AnswerAuthority(record, record.Answer)
	if len(authority.Digest) != 64 || !authority.Confirms(record) {
		t.Fatal("bounded authoritative answer did not confirm")
	}
	for _, change := range []string{"session", "session-uid", "pod-uid", "decision", "thread", "generation", "question", "options-order", "context", "created", "answer", "open", "overflow"} {
		t.Run(change, func(t *testing.T) {
			changed := record
			changed.Question.Options = append([]string(nil), record.Question.Options...)
			switch change {
			case "session":
				changed.Session += "-changed"
			case "session-uid":
				changed.SessionUID += "-changed"
			case "pod-uid":
				changed.PodUID += "-changed"
			case "decision":
				changed.ID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
			case "thread":
				changed.ThreadID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
			case "generation":
				changed.WriterGeneration++
			case "question":
				changed.Question.Question += " changed"
			case "options-order":
				changed.Question.Options = []string{"B", "A"}
			case "context":
				changed.Question.Context += " changed"
			case "created":
				changed.CreatedAt = changed.CreatedAt.Add(-time.Second)
			case "answer":
				changed.Answer = "B"
			case "open":
				changed.State = "Open"
				changed.Answer = ""
				changed.AnsweredAt = nil
			case "overflow":
				changed.WriterGeneration = 1 << 63
			}
			if authority.Confirms(changed) {
				t.Fatal("authority accepted altered approved identity/content")
			}
		})
	}
	for _, phase := range []string{"Reserved", "Unknown", ""} {
		changed := authority
		changed.Phase = phase
		if changed.Confirms(record) {
			t.Fatal("unconfirmed authority authorized dispatch")
		}
	}
	if strings.Contains(authority.Digest, "Synthetic") {
		t.Fatal("private text escaped fixed digest")
	}
	record.State = "Delivered"
	if !authority.Confirms(record) {
		t.Fatal("delivery state invalidated approved immutable content")
	}
}
