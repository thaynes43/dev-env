package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestPortalConversationValidation(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	good := PortalConversation{State: "partial", ReasonCode: "coverage_unverified", ConversationID: "12345678-1234-1234-1234-123456789abc", OldestSeq: 1, NewestSeq: 1, ObservedAt: now, Turns: []PortalConversationTurn{{Seq: 1, At: now, Role: "user", Text: "visible"}}}
	if !good.Valid() {
		t.Fatal("valid bounded projection rejected")
	}
	for _, mutate := range []func(*PortalConversation){func(p *PortalConversation) { p.State = "complete-ish" }, func(p *PortalConversation) { p.ConversationID = "../../private" }, func(p *PortalConversation) { p.Turns[0].Role = "thinking" }, func(p *PortalConversation) { p.Turns[0].Text = strings.Repeat("x", 4097) }, func(p *PortalConversation) { p.Turns[0].Text = "a\x1bsecret" }, func(p *PortalConversation) { p.NewestSeq = 2 }, func(p *PortalConversation) { p.Turns[0].From = "forged sender" }} {
		copy := good
		copy.Turns = append([]PortalConversationTurn(nil), good.Turns...)
		mutate(&copy)
		if copy.Valid() {
			t.Fatal("invalid projection accepted")
		}
	}
	unavailable := PortalConversation{State: "unavailable", ReasonCode: "unsupported", ObservedAt: now}
	if !unavailable.Valid() {
		t.Fatal("explicit unavailable rejected")
	}
	unavailable.Turns = good.Turns
	if unavailable.Valid() {
		t.Fatal("unavailable projection carried text")
	}
}

func TestStrictPortalConversationRejectsCaseAliases(t *testing.T) {
	valid := `{"state":"partial","reasonCode":"coverage_unverified","turns":[{"seq":1,"role":"user","text":"visible"}]}`
	if !StrictPortalConversationJSON([]byte(valid)) {
		t.Fatal("exact fields rejected")
	}
	for _, bad := range []string{strings.Replace(valid, `"state":"partial"`, `"state":"partial","State":"complete"`, 1), strings.Replace(valid, `"state"`, `"State"`, 1), strings.Replace(valid, `"state"`, `"ſtate"`, 1), strings.Replace(valid, `"role":"user"`, `"role":"user","Role":"assistant"`, 1)} {
		if StrictPortalConversationJSON([]byte(bad)) {
			t.Fatal("typed case alias accepted")
		}
	}
	// Provider metadata objects are separate and may use case-distinct labels.
	if !StrictPortalJSON([]byte(`{"input":{"A":1,"a":2}}`)) {
		t.Fatal("case-sensitive provider labels were normalized")
	}
}
