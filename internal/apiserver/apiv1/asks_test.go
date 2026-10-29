package apiv1

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var askNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func boolp(b bool) *bool { return &b }

func askOpts() []AskOption {
	return []AskOption{
		{ID: "yes", Label: "Yes", Description: "Do it.\nNow."},
		{ID: "no", Label: "No"},
	}
}

func askInfoRequest() AskCreateRequest {
	return AskCreateRequest{
		RequestID:   "req-12345678",
		Type:        AskTypeInfo,
		Question:    "Which branch?\nPick one.",
		Options:     askOpts(),
		Recommended: "yes",
		Context:     AskRequestContext{WorkPackage: 254, Links: []string{"https://example.com/a"}},
		ExpiresIn:   "24h",
	}
}

func askApprovalRequest() AskCreateRequest {
	r := askInfoRequest()
	r.Type = AskTypeApproval
	r.Scope = &AskScope{Action: "merge", Target: "repo#1", ExpiresAt: askNow.Add(12 * time.Hour)}
	r.ExpiresIn = "6h"
	return r
}

func TestAskPaths(t *testing.T) {
	id := "ask-0123456789abcdef"
	for got, want := range map[string]string{
		AskPath(id):             "/v1/asks/" + id,
		AskWithdrawPath(id):     "/v1/asks/" + id + "/withdraw",
		PortalAskPath(id):       "/v1/portal/asks/" + id,
		PortalAskAnswerPath(id): "/v1/portal/asks/" + id + "/answer",
	} {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
	for _, bad := range []string{"", "ask-XYZ", "../x", "ask-0123456789abcdef/withdraw", "ask-0123456789abcdef?x=1", "ask-0123456789ABCDEF"} {
		for name, f := range map[string]func(string) string{
			"AskPath": AskPath, "AskWithdrawPath": AskWithdrawPath,
			"PortalAskPath": PortalAskPath, "PortalAskAnswerPath": PortalAskAnswerPath,
		} {
			if got := f(bad); got != "" {
				t.Errorf("%s(%q) = %q, want \"\"", name, bad, got)
			}
		}
	}
}

func TestAskListQueryConstants(t *testing.T) {
	for got, want := range map[string]string{
		AskQueryMine: "mine", AskQueryState: "state", AskQueryLimit: "limit", AskQueryCursor: "cursor",
		AskStateFilterOpen: "open", AskStateFilterAll: "all",
	} {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
	if AskListDefaultLimit != 20 || AskListMaxLimit != 100 {
		t.Errorf("limits %d/%d", AskListDefaultLimit, AskListMaxLimit)
	}
}

func TestValidAskIDs(t *testing.T) {
	for id, want := range map[string]bool{
		"ask-0123456789abcdef":   true,
		"ask-0123456789ABCDEF":   false,
		"ask-0123456789abcde":    false,
		"ask-0123456789abcdef0":  false,
		"0123456789abcdef":       false,
		"ask-0123456789abcdeg":   false,
		"ask-0123456789abcdef\n": false,
	} {
		if ValidAskID(id) != want {
			t.Errorf("ValidAskID(%q) = %v", id, !want)
		}
	}
	for id, want := range map[string]bool{
		"abcd1234": true, "A-b-1-2-3": true, "short": false, "has space1": false,
		strings.Repeat("a", 64): true, strings.Repeat("a", 65): false, "": false,
	} {
		if ValidAskRequestID(id) != want {
			t.Errorf("ValidAskRequestID(%q) = %v", id, !want)
		}
	}
}

func TestAskCreateRequestValidate(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name   string
		mutate func(*AskCreateRequest)
		ok     bool
	}{
		{"info accepted", func(r *AskCreateRequest) {}, true},
		{"approval accepted", func(r *AskCreateRequest) { *r = askApprovalRequest() }, true},
		{"no expiresIn uses default", func(r *AskCreateRequest) { r.ExpiresIn = "" }, true},
		{"expiresIn 7d accepted", func(r *AskCreateRequest) { r.ExpiresIn = "168h" }, true},
		{"expiresIn over 7d refused", func(r *AskCreateRequest) { r.ExpiresIn = "169h" }, false},
		{"expiresIn zero refused", func(r *AskCreateRequest) { r.ExpiresIn = "0s" }, false},
		{"expiresIn negative refused", func(r *AskCreateRequest) { r.ExpiresIn = "-1h" }, false},
		{"expiresIn seconds number refused", func(r *AskCreateRequest) { r.ExpiresIn = "3600" }, false},
		{"expiresIn 1m accepted", func(r *AskCreateRequest) { r.ExpiresIn = "1m" }, true},
		{"expiresIn 59s refused", func(r *AskCreateRequest) { r.ExpiresIn = "59s" }, false},
		{"requestId short refused", func(r *AskCreateRequest) { r.RequestID = "abc" }, false},
		{"requestId underscore refused", func(r *AskCreateRequest) { r.RequestID = "abcd_1234" }, false},
		{"unknown type refused", func(r *AskCreateRequest) { r.Type = "poll" }, false},
		{"empty type refused", func(r *AskCreateRequest) { r.Type = "" }, false},
		{"empty question refused", func(r *AskCreateRequest) { r.Question = "" }, false},
		{"blank question refused", func(r *AskCreateRequest) { r.Question = "  \n " }, false},
		{"question 1000 runes accepted", func(r *AskCreateRequest) { r.Question = strings.Repeat("é", 1000) }, true},
		{"question 1001 runes refused", func(r *AskCreateRequest) { r.Question = strings.Repeat("é", 1001) }, false},
		{"question invalid UTF-8 refused", func(r *AskCreateRequest) { r.Question = "bad \xff" }, false},
		{"question tab accepted", func(r *AskCreateRequest) { r.Question = "a\tb" }, true},
		{"question CRLF refused unnormalized", func(r *AskCreateRequest) { r.Question = "a\r\nb" }, false},
		{"question CRLF accepted normalized", func(r *AskCreateRequest) { r.Question = NormalizeAskText("a\r\nb\rc") }, true},
		{"question ZWJ accepted", func(r *AskCreateRequest) { r.Question = "Family? \U0001F468\u200d\U0001F469" }, true},
		{"question ZWSP refused", func(r *AskCreateRequest) { r.Question = "a\u200bb" }, false},
		{"question only ZWJ and whitespace refused", func(r *AskCreateRequest) { r.Question = " \u200d\n\t\u00a0" }, false},
		{"question CR refused", func(r *AskCreateRequest) { r.Question = "a\rb" }, false},
		{"question NUL refused", func(r *AskCreateRequest) { r.Question = "a\x00b" }, false},
		{"question C1 control refused", func(r *AskCreateRequest) { r.Question = "a\u0085b" }, false},
		{"question bidi override refused", func(r *AskCreateRequest) { r.Question = "a\u202eb" }, false},
		{"question line separator refused", func(r *AskCreateRequest) { r.Question = "a\u2028b" }, false},
		{"question emoji accepted", func(r *AskCreateRequest) { r.Question = "Ship it? 👍" }, true},
		{"zero options with free text accepted", func(r *AskCreateRequest) { r.Options, r.Recommended, r.AllowFreeText = nil, "", true }, true},
		{"zero options without free text refused", func(r *AskCreateRequest) { r.Options, r.Recommended = nil, "" }, false},
		{"approval with no options and no free text accepted", func(r *AskCreateRequest) {
			*r = askApprovalRequest()
			r.Options, r.Recommended, r.AllowFreeText = nil, "", false
		}, true},
		{"approval with free text only accepted", func(r *AskCreateRequest) {
			*r = askApprovalRequest()
			r.Options, r.Recommended, r.AllowFreeText = nil, "", true
		}, true},
		{"approval recommended without options refused", func(r *AskCreateRequest) {
			*r = askApprovalRequest()
			r.Options, r.Recommended, r.AllowFreeText = nil, "yes", false
		}, false},
		{"info with no options and no free text still refused", func(r *AskCreateRequest) {
			r.Options, r.Recommended, r.AllowFreeText = nil, "", false
		}, false},
		{"six options accepted", func(r *AskCreateRequest) {
			r.Options = nil
			for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
				r.Options = append(r.Options, AskOption{ID: id, Label: id})
			}
			r.Recommended = ""
		}, true},
		{"seven options refused", func(r *AskCreateRequest) {
			r.Options = nil
			for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
				r.Options = append(r.Options, AskOption{ID: id, Label: id})
			}
			r.Recommended = ""
		}, false},
		{"duplicate option id refused", func(r *AskCreateRequest) { r.Options[1].ID = "yes" }, false},
		{"option id uppercase refused", func(r *AskCreateRequest) { r.Options[1].ID = "No" }, false},
		{"option id underscore refused", func(r *AskCreateRequest) { r.Options[1].ID = "n_o" }, false},
		{"option id empty refused", func(r *AskCreateRequest) { r.Options[1].ID = "" }, false},
		{"option id 24 accepted", func(r *AskCreateRequest) { r.Options[1].ID = long(24) }, true},
		{"option id 25 refused", func(r *AskCreateRequest) { r.Options[1].ID = long(25) }, false},
		{"option label empty refused", func(r *AskCreateRequest) { r.Options[1].Label = "" }, false},
		{"option label 80 accepted", func(r *AskCreateRequest) { r.Options[1].Label = long(80) }, true},
		{"option label 81 refused", func(r *AskCreateRequest) { r.Options[1].Label = long(81) }, false},
		{"option label newline refused", func(r *AskCreateRequest) { r.Options[1].Label = "a\nb" }, false},
		{"option description 300 accepted", func(r *AskCreateRequest) { r.Options[1].Description = long(300) }, true},
		{"option description 301 refused", func(r *AskCreateRequest) { r.Options[1].Description = long(301) }, false},
		{"option description newline accepted", func(r *AskCreateRequest) { r.Options[1].Description = "a\nb" }, true},
		{"option description tab accepted", func(r *AskCreateRequest) { r.Options[1].Description = "a\tb" }, true},
		{"option description ZWJ accepted", func(r *AskCreateRequest) { r.Options[1].Description = "a\u200db" }, true},
		{"option description soft hyphen refused", func(r *AskCreateRequest) { r.Options[1].Description = "a\u00adb" }, false},
		{"option label tab refused", func(r *AskCreateRequest) { r.Options[1].Label = "a\tb" }, false},
		{"option label ZWJ refused", func(r *AskCreateRequest) { r.Options[1].Label = "a\u200db" }, false},
		{"option label blank refused", func(r *AskCreateRequest) { r.Options[1].Label = " \u00a0" }, false},
		{"option label only format characters refused", func(r *AskCreateRequest) { r.Options[1].Label = "\u200b\ufeff" }, false},
		{"recommended unknown refused", func(r *AskCreateRequest) { r.Recommended = "maybe" }, false},
		{"recommended empty accepted", func(r *AskCreateRequest) { r.Recommended = "" }, true},
		{"workPackage negative refused", func(r *AskCreateRequest) { r.Context.WorkPackage = -1 }, false},
		{"workPackage omitted accepted", func(r *AskCreateRequest) { r.Context.WorkPackage = 0 }, true},
		{"five links accepted", func(r *AskCreateRequest) {
			r.Context.Links = []string{"https://a.example/1", "https://a.example/2", "https://a.example/3", "https://a.example/4", "https://a.example/5"}
		}, true},
		{"six links refused", func(r *AskCreateRequest) {
			r.Context.Links = []string{"https://a.example/1", "https://a.example/2", "https://a.example/3", "https://a.example/4", "https://a.example/5", "https://a.example/6"}
		}, false},
		{"http link refused", func(r *AskCreateRequest) { r.Context.Links = []string{"http://example.com"} }, false},
		{"javascript link refused", func(r *AskCreateRequest) { r.Context.Links = []string{"javascript:alert(1)"} }, false},
		{"relative link refused", func(r *AskCreateRequest) { r.Context.Links = []string{"/x"} }, false},
		{"schemeless link refused", func(r *AskCreateRequest) { r.Context.Links = []string{"example.com/x"} }, false},
		{"link with credentials refused", func(r *AskCreateRequest) { r.Context.Links = []string{"https://u:p@example.com/"} }, false},
		{"link without host refused", func(r *AskCreateRequest) { r.Context.Links = []string{"https:///x"} }, false},
		{"link with space refused", func(r *AskCreateRequest) { r.Context.Links = []string{"https://example.com/a b"} }, false},
		{"empty link refused", func(r *AskCreateRequest) { r.Context.Links = []string{""} }, false},
		{"link 300 accepted", func(r *AskCreateRequest) { r.Context.Links = []string{"https://e.co/" + long(300-13)} }, true},
		{"link 301 refused", func(r *AskCreateRequest) { r.Context.Links = []string{"https://e.co/" + long(301-13)} }, false},
		{"info with scope refused", func(r *AskCreateRequest) { r.Scope = askApprovalRequest().Scope }, false},
		{"approval without scope refused", func(r *AskCreateRequest) { r.Type = AskTypeApproval }, false},
		{"scope action empty refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.Action = "" }, false},
		{"scope action 200 accepted", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.Action = long(200) }, true},
		{"scope action 201 refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.Action = long(201) }, false},
		{"scope action newline refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.Action = "a\nb" }, false},
		{"scope action tab refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.Action = "a\tb" }, false},
		{"scope action ZWJ refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.Action = "a\u200db" }, false},
		{"scope action blank refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.Action = "   " }, false},
		{"scope target empty refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.Target = "" }, false},
		{"scope target 201 refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.Target = long(201) }, false},
		{"scope expiry zero refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.ExpiresAt = time.Time{} }, false},
		{"scope expiry in the past refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.ExpiresAt = askNow.Add(-time.Second) }, false},
		{"scope expiry 24h accepted", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.ExpiresAt = askNow.Add(AskScopeMaxTTL) }, true},
		{"scope expiry over 24h refused", func(r *AskCreateRequest) {
			*r = askApprovalRequest()
			r.Scope.ExpiresAt = askNow.Add(AskScopeMaxTTL + time.Second)
		}, false},
		{"scope expiry 7d refused by the v1 cap", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.ExpiresAt = askNow.Add(AskMaxTTL) }, false},
		{"scope expiry 1m accepted", func(r *AskCreateRequest) {
			*r = askApprovalRequest()
			r.Scope.ExpiresAt, r.ExpiresIn = askNow.Add(AskMinTTL), "1m"
		}, true},
		{"scope expiry 59s refused", func(r *AskCreateRequest) {
			*r = askApprovalRequest()
			r.Scope.ExpiresAt, r.ExpiresIn = askNow.Add(59*time.Second), ""
		}, false},
		{"approval ask ending with its scope accepted", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.ExpiresIn = "12h" }, true},
		{"approval ask outliving its scope refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.ExpiresIn = "12h1s" }, false},
		{"approval default expiry clamps to its scope", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.ExpiresIn = "" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := askInfoRequest()
			r.Options = askOpts() // fresh slice per case: mutate edits in place
			tc.mutate(&r)
			err := r.ValidateAt(askNow)
			if tc.ok && err != nil {
				t.Fatalf("want accepted, got %v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatal("want refused, got nil")
				}
				if !errors.Is(err, ErrAskInvalid) {
					t.Fatalf("error does not wrap ErrAskInvalid: %v", err)
				}
			}
		})
	}
}

func TestAskCreateRequestValidateUsesClock(t *testing.T) {
	r := askApprovalRequest()
	r.Scope.ExpiresAt, r.ExpiresIn = time.Now().Add(time.Hour), "30m"
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	r.Scope.ExpiresAt = time.Now().Add(-time.Hour)
	if r.Validate() == nil {
		t.Fatal("past scope expiry accepted")
	}
}

func TestAskCreateRequestTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"": AskDefaultTTL, "90m": 90 * time.Minute, "168h": AskMaxTTL} {
		got, err := AskCreateRequest{ExpiresIn: in}.TTL()
		if err != nil || got != want {
			t.Errorf("TTL(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"8d", "169h", "59s", "0s", "-1m"} {
		if _, err := (AskCreateRequest{ExpiresIn: in}).TTL(); !errors.Is(err, ErrAskInvalid) {
			t.Errorf("TTL(%q) = %v, want ErrAskInvalid", in, err)
		}
	}
}

func TestAskCreateRequestExpiresAtFrom(t *testing.T) {
	info := askInfoRequest()
	info.ExpiresIn = ""
	approval := askApprovalRequest() // scope at +12h
	approvalDefault := askApprovalRequest()
	approvalDefault.ExpiresIn = ""
	approvalFullDay := askApprovalRequest()
	approvalFullDay.ExpiresIn, approvalFullDay.Scope.ExpiresAt = "", askNow.Add(AskScopeMaxTTL)
	for name, tc := range map[string]struct {
		r    AskCreateRequest
		want time.Time
	}{
		"info default":                  {info, askNow.Add(AskDefaultTTL)},
		"info explicit":                 {askInfoRequest(), askNow.Add(24 * time.Hour)},
		"approval explicit":             {approval, askNow.Add(6 * time.Hour)},
		"approval default clamps":       {approvalDefault, askNow.Add(12 * time.Hour)},
		"approval default at 24h scope": {approvalFullDay, askNow.Add(24 * time.Hour)},
	} {
		got, err := tc.r.ExpiresAtFrom(askNow)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("%s: ExpiresAtFrom = %v, %v; want %v", name, got, err, tc.want)
		}
	}
	bad := askInfoRequest()
	bad.ExpiresIn = "30s"
	if _, err := bad.ExpiresAtFrom(askNow); !errors.Is(err, ErrAskInvalid) {
		t.Errorf("30s: %v", err)
	}
}

func askRecord() Ask {
	return Ask{
		ID:          "ask-0123456789abcdef",
		Type:        AskTypeInfo,
		Question:    "Which branch?",
		Options:     askOpts(),
		Recommended: "yes",
		Context:     AskContext{SessionName: "dev-env-1029", SessionUID: "uid-1", WorkPackage: 254, Links: []string{"https://example.com/a"}},
		CreatedAt:   askNow,
		ExpiresAt:   askNow.Add(AskDefaultTTL),
		State:       AskStateOpen,
	}
}

func askAnswered(a Ask) Ask {
	a.State = AskStateAnswered
	a.Answer = &AskAnswer{
		OptionID:   "yes",
		AnsweredBy: AskActor{Issuer: "https://idp.example", Subject: "u-1", DisplayName: "Tom"},
		AnsweredAt: askNow.Add(time.Hour),
	}
	return a
}

func TestValidateAsk(t *testing.T) {
	approval := func(a *Ask) {
		a.Type = AskTypeApproval
		a.Scope = &AskScope{Action: "merge", Target: "repo#1", ExpiresAt: askNow.Add(24 * time.Hour)}
	}
	cases := []struct {
		name   string
		mutate func(*Ask)
		ok     bool
	}{
		{"open info accepted", func(a *Ask) {}, true},
		{"open approval accepted", approval, true},
		{"withdrawn accepted", func(a *Ask) { a.State = AskStateWithdrawn }, true},
		{"expired accepted", func(a *Ask) { a.State = AskStateExpired }, true},
		{"answered accepted", func(a *Ask) { *a = askAnswered(*a) }, true},
		{"answered approval approved accepted", func(a *Ask) {
			approval(a)
			*a = askAnswered(*a)
			a.Answer.Approved = boolp(true)
		}, true},
		{"answered approval denied accepted", func(a *Ask) {
			approval(a)
			*a = askAnswered(*a)
			a.Answer.Approved = boolp(false)
		}, true},
		{"answered approval without approved refused", func(a *Ask) {
			approval(a)
			*a = askAnswered(*a)
		}, false},
		{"answered approval decision alone accepted", func(a *Ask) {
			approval(a)
			a.Options, a.Recommended = nil, ""
			*a = askAnswered(*a)
			a.Answer.OptionID, a.Answer.Approved = "", boolp(true)
		}, true},
		{"answered approval note without allowFreeText accepted", func(a *Ask) {
			approval(a)
			*a = askAnswered(*a)
			a.Answer.Approved, a.Answer.Text = boolp(false), "too risky\nask me later"
		}, true},
		{"answered approval unknown option refused", func(a *Ask) {
			approval(a)
			*a = askAnswered(*a)
			a.Answer.Approved, a.Answer.OptionID = boolp(true), "maybe"
		}, false},
		{"approval ask with no options or free text accepted", func(a *Ask) {
			approval(a)
			a.Options, a.Recommended = nil, ""
		}, true},
		{"info ask with no options or free text refused", func(a *Ask) { a.Options, a.Recommended = nil, "" }, false},
		{"answer multi-line text accepted", func(a *Ask) {
			*a = askAnswered(*a)
			a.AllowFreeText = true
			a.Answer.Text = "line one\nline two"
		}, true},
		{"answered with delivery accepted", func(a *Ask) {
			*a = askAnswered(*a)
			a.Delivery = &AskDelivery{InboxMessage: "inbox-1", DeliveredAt: askNow.Add(time.Hour)}
		}, true},
		{"bad id refused", func(a *Ask) { a.ID = "ask-XYZ" }, false},
		{"empty id refused", func(a *Ask) { a.ID = "" }, false},
		{"bad state refused", func(a *Ask) { a.State = "pending" }, false},
		{"empty state refused", func(a *Ask) { a.State = "" }, false},
		{"missing session name refused", func(a *Ask) { a.Context.SessionName = "" }, false},
		{"missing session uid refused", func(a *Ask) { a.Context.SessionUID = "" }, false},
		{"session name control char refused", func(a *Ask) { a.Context.SessionName = "a\nb" }, false},
		{"zero createdAt refused", func(a *Ask) { a.CreatedAt = time.Time{} }, false},
		{"expiresAt equal createdAt refused", func(a *Ask) { a.ExpiresAt = a.CreatedAt }, false},
		{"expiresAt 7d accepted", func(a *Ask) { a.ExpiresAt = a.CreatedAt.Add(AskMaxTTL) }, true},
		{"expiresAt over 7d refused", func(a *Ask) { a.ExpiresAt = a.CreatedAt.Add(AskMaxTTL + time.Second) }, false},
		{"approval scope before createdAt refused", func(a *Ask) { approval(a); a.Scope.ExpiresAt = a.CreatedAt }, false},
		{"approval without scope refused", func(a *Ask) { a.Type = AskTypeApproval }, false},
		{"info with scope refused", func(a *Ask) { approval(a); a.Type = AskTypeInfo }, false},
		{"open with answer refused", func(a *Ask) { *a = askAnswered(*a); a.State = AskStateOpen }, false},
		{"answered without answer refused", func(a *Ask) { a.State = AskStateAnswered }, false},
		{"answer unknown option refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.OptionID = "maybe" }, false},
		{"answer text without allowFreeText refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.Text = "hi" }, false},
		{"answer text with allowFreeText accepted", func(a *Ask) { *a = askAnswered(*a); a.AllowFreeText = true; a.Answer.Text = "hi" }, true},
		{"answer free text only accepted", func(a *Ask) {
			*a = askAnswered(*a)
			a.AllowFreeText = true
			a.Answer.OptionID, a.Answer.Text = "", "other"
		}, true},
		{"answer empty refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.OptionID = "" }, false},
		{"answer approved true on info refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.Approved = boolp(true) }, false},
		{"answer approved false on info refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.Approved = boolp(false) }, false},
		{"answer without answeredAt refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.AnsweredAt = time.Time{} }, false},
		{"answer without issuer refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.AnsweredBy.Issuer = "" }, false},
		{"answer without subject refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.AnsweredBy.Subject = "" }, false},
		{"delivery without message refused", func(a *Ask) {
			*a = askAnswered(*a)
			a.Delivery = &AskDelivery{DeliveredAt: askNow.Add(time.Hour)}
		}, false},
		{"delivery without time refused", func(a *Ask) { *a = askAnswered(*a); a.Delivery = &AskDelivery{InboxMessage: "m"} }, false},
		{"delivery on open ask refused", func(a *Ask) { a.Delivery = &AskDelivery{InboxMessage: "m", DeliveredAt: askNow} }, false},
		{"withdrawn with answer refused", func(a *Ask) { *a = askAnswered(*a); a.State = AskStateWithdrawn }, false},
		{"expired with answer refused", func(a *Ask) { *a = askAnswered(*a); a.State = AskStateExpired }, false},
		{"answeredAt at createdAt accepted", func(a *Ask) { *a = askAnswered(*a); a.Answer.AnsweredAt = a.CreatedAt }, true},
		{"answeredAt before createdAt refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.AnsweredAt = a.CreatedAt.Add(-time.Second) }, false},
		{"answeredAt just before expiresAt accepted", func(a *Ask) { *a = askAnswered(*a); a.Answer.AnsweredAt = a.ExpiresAt.Add(-time.Nanosecond) }, true},
		{"answeredAt at expiresAt refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.AnsweredAt = a.ExpiresAt }, false},
		{"answeredAt after expiresAt refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.AnsweredAt = a.ExpiresAt.Add(time.Hour) }, false},
		{"answer whitespace-only text without option refused", func(a *Ask) {
			*a = askAnswered(*a)
			a.AllowFreeText = true
			a.Answer.OptionID, a.Answer.Text = "", " \n\t "
		}, false},
		{"answer text with ZWSP refused", func(a *Ask) {
			*a = askAnswered(*a)
			a.AllowFreeText = true
			a.Answer.Text = "a\u200bb"
		}, false},
		{"answer actor ZWJ refused", func(a *Ask) { *a = askAnswered(*a); a.Answer.AnsweredBy.DisplayName = "T\u200dom" }, false},
		{"session name format character refused", func(a *Ask) { a.Context.SessionName = "dev\u200benv" }, false},
		{"expiresAt 1m accepted", func(a *Ask) { a.ExpiresAt = a.CreatedAt.Add(AskMinTTL) }, true},
		{"expiresAt 59s refused", func(a *Ask) { a.ExpiresAt = a.CreatedAt.Add(59 * time.Second) }, false},
		{"approval scope 24h accepted", func(a *Ask) { approval(a); a.Scope.ExpiresAt = a.CreatedAt.Add(AskScopeMaxTTL) }, true},
		{"approval scope over 24h refused", func(a *Ask) {
			approval(a)
			a.Scope.ExpiresAt = a.CreatedAt.Add(AskScopeMaxTTL + time.Second)
		}, false},
		{"approval scope 1m with ask 1m accepted", func(a *Ask) {
			approval(a)
			a.Scope.ExpiresAt, a.ExpiresAt = a.CreatedAt.Add(AskMinTTL), a.CreatedAt.Add(AskMinTTL)
		}, true},
		{"approval scope 59s refused", func(a *Ask) {
			approval(a)
			a.Scope.ExpiresAt, a.ExpiresAt = a.CreatedAt.Add(59*time.Second), a.CreatedAt.Add(AskMinTTL)
		}, false},
		{"approval ask outliving its scope refused", func(a *Ask) { approval(a); a.Scope.ExpiresAt = a.CreatedAt.Add(12 * time.Hour) }, false},
		{"approval ask ending with its scope accepted", func(a *Ask) {
			approval(a)
			a.Scope.ExpiresAt, a.ExpiresAt = a.CreatedAt.Add(12*time.Hour), a.CreatedAt.Add(12*time.Hour)
		}, true},
		{"body rule applies: http link refused", func(a *Ask) { a.Context.Links = []string{"http://example.com"} }, false},
		{"body rule applies: bad recommended refused", func(a *Ask) { a.Recommended = "zzz" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := askRecord()
			a.Options = askOpts()
			tc.mutate(&a)
			err := ValidateAsk(a)
			if tc.ok && err != nil {
				t.Fatalf("want accepted, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrAskInvalid) {
				t.Fatalf("want ErrAskInvalid, got %v", err)
			}
		})
	}
}

func askActor() AskActor {
	return AskActor{Issuer: "https://idp.example", Subject: "u-1", DisplayName: "Tom"}
}

func TestAskAnswerRequestValidate(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name string
		req  AskAnswerRequest
		ok   bool
	}{
		{"option accepted", AskAnswerRequest{OptionID: "yes", Actor: askActor()}, true},
		{"text accepted", AskAnswerRequest{Text: "other", Actor: askActor()}, true},
		{"option and text accepted", AskAnswerRequest{OptionID: "yes", Text: "because", Actor: askActor()}, true},
		{"approved true accepted at shape level", AskAnswerRequest{OptionID: "yes", Approved: boolp(true), Actor: askActor()}, true},
		{"approved false accepted at shape level", AskAnswerRequest{Approved: boolp(false), Actor: askActor()}, true},
		{"nothing set accepted at shape level", AskAnswerRequest{Actor: askActor()}, true},
		{"bad option id refused", AskAnswerRequest{OptionID: "Yes!", Actor: askActor()}, false},
		{"text 2000 accepted", AskAnswerRequest{Text: strings.Repeat("é", 2000), Actor: askActor()}, true},
		{"text 2001 refused", AskAnswerRequest{Text: strings.Repeat("é", 2001), Actor: askActor()}, false},
		{"text invalid UTF-8 refused", AskAnswerRequest{Text: "\xff", Actor: askActor()}, false},
		{"text newline accepted", AskAnswerRequest{Text: "a\nb\n\nc", Actor: askActor()}, true},
		{"text 2000 runes with newlines accepted", AskAnswerRequest{Text: strings.Repeat("é\n", 1000), Actor: askActor()}, true},
		{"text 2001 runes with newlines refused", AskAnswerRequest{Text: strings.Repeat("é\n", 1000) + "x", Actor: askActor()}, false},
		{"text tab accepted", AskAnswerRequest{Text: "a\tb", Actor: askActor()}, true},
		{"text CR refused", AskAnswerRequest{Text: "a\r\nb", Actor: askActor()}, false},
		{"text lone CR refused", AskAnswerRequest{Text: "a\rb", Actor: askActor()}, false},
		{"text CRLF normalized accepted", AskAnswerRequest{Text: NormalizeAskText("a\r\nb\r\n"), Actor: askActor()}, true},
		{"text ZWJ accepted", AskAnswerRequest{Text: "\U0001F468\u200d\U0001F4BB", Actor: askActor()}, true},
		{"text BOM refused", AskAnswerRequest{Text: "\ufeffa", Actor: askActor()}, false},
		{"actor issuer ZWJ refused", AskAnswerRequest{OptionID: "yes", Actor: AskActor{Issuer: "i\u200d", Subject: "u"}}, false},
		{"actor subject format character refused", AskAnswerRequest{OptionID: "yes", Actor: AskActor{Issuer: "i", Subject: "u\u2060"}}, false},
		{"actor subject blank refused", AskAnswerRequest{OptionID: "yes", Actor: AskActor{Issuer: "i", Subject: " \u200b"}}, false},
		{"text bidi override refused", AskAnswerRequest{Text: "a\u202eb", Actor: askActor()}, false},
		{"text line separator refused", AskAnswerRequest{Text: "a\u2028b", Actor: askActor()}, false},
		{"text paragraph separator refused", AskAnswerRequest{Text: "a\u2029b", Actor: askActor()}, false},
		{"text control refused", AskAnswerRequest{Text: "a\x07b", Actor: askActor()}, false},
		{"actor without issuer refused", AskAnswerRequest{OptionID: "yes", Actor: AskActor{Subject: "u"}}, false},
		{"actor without subject refused", AskAnswerRequest{OptionID: "yes", Actor: AskActor{Issuer: "i"}}, false},
		{"actor without display name accepted", AskAnswerRequest{OptionID: "yes", Actor: AskActor{Issuer: "i", Subject: "u"}}, true},
		{"actor issuer 301 refused", AskAnswerRequest{OptionID: "yes", Actor: AskActor{Issuer: long(301), Subject: "u"}}, false},
		{"actor subject 201 refused", AskAnswerRequest{OptionID: "yes", Actor: AskActor{Issuer: "i", Subject: long(201)}}, false},
		{"actor display name 121 refused", AskAnswerRequest{OptionID: "yes", Actor: AskActor{Issuer: "i", Subject: "u", DisplayName: long(121)}}, false},
		{"actor display name control refused", AskAnswerRequest{OptionID: "yes", Actor: AskActor{Issuer: "i", Subject: "u", DisplayName: "a\tb"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if tc.ok && err != nil {
				t.Fatalf("want accepted, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrAskInvalid) {
				t.Fatalf("want ErrAskInvalid, got %v", err)
			}
		})
	}
}

func TestAskAnswerRequestValidateForAt(t *testing.T) {
	at := askNow.Add(time.Minute)
	info := askRecord()
	approval := askRecord()
	approval.Type = AskTypeApproval
	approval.Scope = &AskScope{Action: "merge", Target: "repo#1", ExpiresAt: askNow.Add(time.Hour)}
	approval.ExpiresAt = approval.Scope.ExpiresAt
	free := askRecord()
	free.Options = nil
	free.Recommended = ""
	free.AllowFreeText = true
	bare := approval // approval with neither options nor free text
	bare.Options, bare.Recommended, bare.AllowFreeText = nil, "", false
	withdrawn := askRecord()
	withdrawn.State = AskStateWithdrawn
	answered := askAnswered(askRecord())
	storedExpired := askRecord()
	storedExpired.State = AskStateExpired
	// An approval stored with an ask expiry past its scope (ValidateAsk
	// refuses it) still lapses with its scope at answer time.
	scopeFirst := approval
	scopeFirst.ExpiresAt = askNow.Add(2 * time.Hour)
	noScope := approval
	noScope.Scope = nil

	yes := AskAnswerRequest{OptionID: "yes", Actor: askActor()}
	approve := AskAnswerRequest{Approved: boolp(true), Actor: askActor()}
	cases := []struct {
		name string
		ask  Ask
		req  AskAnswerRequest
		now  time.Time
		want error
	}{
		{"existing option", info, AskAnswerRequest{OptionID: "no", Actor: askActor()}, at, nil},
		{"unknown option", info, AskAnswerRequest{OptionID: "maybe", Actor: askActor()}, at, ErrAskInvalid},
		{"text refused without allowFreeText", info, AskAnswerRequest{Text: "hi", Actor: askActor()}, at, ErrAskInvalid},
		{"option plus text refused without allowFreeText", info, AskAnswerRequest{OptionID: "yes", Text: "hi", Actor: askActor()}, at, ErrAskInvalid},
		{"text only on free-text ask", free, AskAnswerRequest{Text: "hi", Actor: askActor()}, at, nil},
		{"tabbed text on free-text ask", free, AskAnswerRequest{Text: "a\tb", Actor: askActor()}, at, nil},
		{"whitespace-only text refused", free, AskAnswerRequest{Text: " \n\t\u00a0", Actor: askActor()}, at, ErrAskInvalid},
		{"ZWJ-only text refused", free, AskAnswerRequest{Text: "\u200d \u200d", Actor: askActor()}, at, ErrAskInvalid},
		{"option on ask with no options", free, AskAnswerRequest{OptionID: "yes", Actor: askActor()}, at, ErrAskInvalid},
		{"approved true on info refused", info, AskAnswerRequest{OptionID: "yes", Approved: boolp(true), Actor: askActor()}, at, ErrAskInvalid},
		{"approved false on info refused", info, AskAnswerRequest{OptionID: "yes", Approved: boolp(false), Actor: askActor()}, at, ErrAskInvalid},
		{"approved nil on info accepted", info, yes, at, nil},
		{"multi-line text on free-text info", free, AskAnswerRequest{Text: "a\nb", Actor: askActor()}, at, nil},
		{"approved true on approval accepted", approval, AskAnswerRequest{OptionID: "yes", Approved: boolp(true), Actor: askActor()}, at, nil},
		{"approved false on approval accepted", approval, AskAnswerRequest{OptionID: "no", Approved: boolp(false), Actor: askActor()}, at, nil},
		{"approved nil on approval refused", approval, yes, at, ErrAskInvalid},
		{"approval decision alone accepted", approval, approve, at, nil},
		{"approval deny alone accepted", approval, AskAnswerRequest{Approved: boolp(false), Actor: askActor()}, at, nil},
		{"approval unknown option refused", approval, AskAnswerRequest{OptionID: "maybe", Approved: boolp(true), Actor: askActor()}, at, ErrAskInvalid},
		{"approval note without allowFreeText accepted", approval, AskAnswerRequest{Approved: boolp(false), Text: "not now\nlater", Actor: askActor()}, at, nil},
		{"approval whitespace note accepted", approval, AskAnswerRequest{Approved: boolp(true), Text: " ", Actor: askActor()}, at, nil},
		{"approval note 2001 runes refused", approval, AskAnswerRequest{Approved: boolp(true), Text: strings.Repeat("a", 2001), Actor: askActor()}, at, ErrAskInvalid},
		{"approval with no options or free text accepted", bare, AskAnswerRequest{Approved: boolp(true), Text: "ok", Actor: askActor()}, at, nil},
		{"approval with no options: option refused", bare, AskAnswerRequest{OptionID: "yes", Approved: boolp(true), Actor: askActor()}, at, ErrAskInvalid},
		{"approval nil on bare approval refused", bare, AskAnswerRequest{Text: "ok", Actor: askActor()}, at, ErrAskInvalid},
		{"approval without scope refused", noScope, approve, at, ErrAskInvalid},
		{"shape error surfaces", info, AskAnswerRequest{OptionID: "yes", Actor: AskActor{}}, at, ErrAskInvalid},
		{"info with neither option nor text refused", info, AskAnswerRequest{Actor: askActor()}, at, ErrAskInvalid},
		{"info free-text ask with neither refused", free, AskAnswerRequest{Actor: askActor()}, at, ErrAskInvalid},

		{"open just before expiry", info, yes, info.ExpiresAt.Add(-time.Nanosecond), nil},
		{"open at expiry is expired", info, yes, info.ExpiresAt, ErrAskExpired},
		{"open after expiry is expired", info, yes, info.ExpiresAt.Add(time.Hour), ErrAskExpired},
		{"stored expired is expired", storedExpired, yes, at, ErrAskExpired},
		{"withdrawn is not open", withdrawn, yes, at, ErrAskNotOpen},
		{"answered is not open", answered, yes, at, ErrAskNotOpen},
		{"answered past expiry is still not open", answered, yes, answered.ExpiresAt.Add(time.Hour), ErrAskNotOpen},
		{"approval just before scope expiry", approval, approve, approval.Scope.ExpiresAt.Add(-time.Nanosecond), nil},
		{"approval at scope expiry is expired", scopeFirst, approve, scopeFirst.Scope.ExpiresAt, ErrAskExpired},
		{"approval after scope expiry is expired", scopeFirst, approve, scopeFirst.Scope.ExpiresAt.Add(time.Minute), ErrAskExpired},
		{"malformed answer on closed ask is invalid", withdrawn, AskAnswerRequest{OptionID: "Yes!", Actor: askActor()}, at, ErrAskInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.ValidateForAt(tc.ask, tc.now)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("want accepted, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			// 400, 409 and 410 stay apart.
			for _, other := range []error{ErrAskInvalid, ErrAskNotOpen, ErrAskExpired} {
				if other != tc.want && errors.Is(err, other) {
					t.Fatalf("%v also wraps %v", err, other)
				}
			}
		})
	}
}

func TestAskAnswerRequestValidateForUsesClock(t *testing.T) {
	a := askRecord()
	a.CreatedAt = time.Now().Add(-time.Hour)
	a.ExpiresAt = time.Now().Add(time.Hour)
	req := AskAnswerRequest{OptionID: "yes", Actor: askActor()}
	if err := req.ValidateFor(a); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
	a.ExpiresAt = time.Now().Add(-time.Minute)
	if err := req.ValidateFor(a); !errors.Is(err, ErrAskExpired) {
		t.Fatalf("ValidateFor past expiry: %v", err)
	}
}

func TestAskEffectiveState(t *testing.T) {
	open := askRecord() // expires at askNow+24h
	approval := askRecord()
	approval.Type = AskTypeApproval
	approval.Scope = &AskScope{Action: "a", Target: "t", ExpiresAt: askNow.Add(time.Hour)}
	answered := askAnswered(askRecord())
	withdrawn := askRecord()
	withdrawn.State = AskStateWithdrawn
	expired := askRecord()
	expired.State = AskStateExpired
	for _, tc := range []struct {
		name string
		a    Ask
		now  time.Time
		want AskState
	}{
		{"open before expiry", open, askNow, AskStateOpen},
		{"open just before expiry", open, open.ExpiresAt.Add(-time.Nanosecond), AskStateOpen},
		{"open at expiry", open, open.ExpiresAt, AskStateExpired},
		{"open past expiry", open, open.ExpiresAt.Add(time.Hour), AskStateExpired},
		{"open approval before scope expiry", approval, askNow.Add(30 * time.Minute), AskStateOpen},
		{"open approval at scope expiry", approval, approval.Scope.ExpiresAt, AskStateExpired},
		{"answered past expiry stays answered", answered, answered.ExpiresAt.Add(time.Hour), AskStateAnswered},
		{"withdrawn stays withdrawn", withdrawn, askNow.Add(48 * time.Hour), AskStateWithdrawn},
		{"expired stays expired", expired, askNow, AskStateExpired},
	} {
		if got := tc.a.EffectiveState(tc.now); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestAskApprovalValid(t *testing.T) {
	granted := askRecord()
	granted.Type = AskTypeApproval
	granted.Scope = &AskScope{Action: "a", Target: "t", ExpiresAt: askNow.Add(2 * time.Hour)}
	granted.ExpiresAt = granted.Scope.ExpiresAt
	granted = askAnswered(granted)
	granted.Answer.Approved = boolp(true)

	denied := granted
	denied.Answer = &AskAnswer{OptionID: "yes", Approved: boolp(false), AnsweredBy: askActor(), AnsweredAt: askNow}
	undecided := granted
	undecided.Answer = &AskAnswer{OptionID: "yes", AnsweredBy: askActor(), AnsweredAt: askNow}
	open := granted
	open.State, open.Answer = AskStateOpen, nil
	noAnswer := granted
	noAnswer.Answer = nil
	noScope := granted
	noScope.Scope = nil
	info := askAnswered(askRecord())
	info.Answer.Approved = boolp(true) // malformed, still not an approval
	withdrawn := granted
	withdrawn.State = AskStateWithdrawn

	before := askNow.Add(time.Hour)
	for _, tc := range []struct {
		name string
		a    Ask
		now  time.Time
		want bool
	}{
		{"approved before scope expiry", granted, before, true},
		{"approved just before scope expiry", granted, granted.Scope.ExpiresAt.Add(-time.Nanosecond), true},
		{"approved at scope expiry", granted, granted.Scope.ExpiresAt, false},
		{"approved after scope expiry", granted, granted.Scope.ExpiresAt.Add(time.Hour), false},
		{"denied", denied, before, false},
		{"no decision", undecided, before, false},
		{"open", open, before, false},
		{"answered without answer", noAnswer, before, false},
		{"no scope", noScope, before, false},
		{"info type", info, before, false},
		{"withdrawn", withdrawn, before, false},
	} {
		if got := tc.a.ApprovalValid(tc.now); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeAskText(t *testing.T) {
	for in, want := range map[string]string{
		"":                 "",
		"plain":            "plain",
		"a\nb":             "a\nb",
		"a\r\nb":           "a\nb",
		"a\rb":             "a\nb",
		"a\r\r\nb":         "a\n\nb",
		"a\n\rb":           "a\n\nb",
		"\r\n\r\n":         "\n\n",
		"\r":               "\n",
		"tab\tstays":       "tab\tstays",
		"\u200bkept as is": "\u200bkept as is",
	} {
		if got := NormalizeAskText(in); got != want {
			t.Errorf("NormalizeAskText(%q) = %q want %q", in, got, want)
		}
	}
}

// TestAskFormatCharacters refuses each class of format character in a
// single-line and a multi-line field, and allows the zero-width joiner only
// in the multi-line one.
func TestAskFormatCharacters(t *testing.T) {
	refused := map[string]rune{
		"soft hyphen U+00AD":            0x00AD,
		"Arabic number sign U+0600":     0x0600,
		"Arabic letter mark U+061C":     0x061C,
		"Mongolian vowel separator":     0x180E,
		"zero width space U+200B":       0x200B,
		"zero width non-joiner U+200C":  0x200C,
		"left-to-right mark U+200E":     0x200E,
		"right-to-left mark U+200F":     0x200F,
		"bidi embedding U+202A":         0x202A,
		"bidi override U+202E":          0x202E,
		"word joiner U+2060":            0x2060,
		"invisible separator U+2063":    0x2063,
		"unassigned U+2065":             0x2065,
		"bidi isolate U+2066":           0x2066,
		"pop isolate U+2069":            0x2069,
		"deprecated format U+206A":      0x206A,
		"deprecated format U+206F":      0x206F,
		"byte order mark U+FEFF":        0xFEFF,
		"interlinear annotation U+FFF9": 0xFFF9,
		"interlinear annotation U+FFFB": 0xFFFB,
		"unassigned tag block U+E0000":  0xE0000,
		"language tag U+E0001":          0xE0001,
		"tag space U+E0020":             0xE0020,
		"cancel tag U+E007F":            0xE007F,
		"line separator U+2028":         0x2028,
		"paragraph separator U+2029":    0x2029,
	}
	for name, r := range refused {
		bad := "a" + string(r) + "b"
		for field, set := range askTextSetters() {
			if err := set(bad); !errors.Is(err, ErrAskInvalid) {
				t.Errorf("%s in %s: want refused, got %v", name, field, err)
			}
		}
	}
	zwj := "\U0001F468\u200d\U0001F469"
	for field, set := range askTextSetters() {
		err := set(zwj)
		multi := strings.HasPrefix(field, "multi:")
		if multi && err != nil {
			t.Errorf("ZWJ in %s: want accepted, got %v", field, err)
		}
		if !multi && !errors.Is(err, ErrAskInvalid) {
			t.Errorf("ZWJ in %s: want refused, got %v", field, err)
		}
	}
	for field, set := range askTextSetters() {
		err := set("a\tb")
		multi := strings.HasPrefix(field, "multi:")
		if multi != (err == nil) {
			t.Errorf("tab in %s: multi-line %v, got %v", field, multi, err)
		}
	}
}

// askTextSetters validates s in each text field; "multi:" fields allow line
// breaks, tabs and the zero-width joiner.
func askTextSetters() map[string]func(string) error {
	create := func(f func(*AskCreateRequest)) error {
		r := askApprovalRequest()
		r.Options = askOpts()
		r.AllowFreeText = true
		f(&r)
		return r.ValidateAt(askNow)
	}
	answer := func(f func(*AskAnswerRequest)) error {
		r := AskAnswerRequest{OptionID: "yes", Actor: askActor()}
		f(&r)
		return r.Validate()
	}
	return map[string]func(string) error{
		"multi:question":           func(s string) error { return create(func(r *AskCreateRequest) { r.Question = s }) },
		"multi:option description": func(s string) error { return create(func(r *AskCreateRequest) { r.Options[0].Description = s }) },
		"multi:answer text":        func(s string) error { return answer(func(r *AskAnswerRequest) { r.Text = s }) },
		"option label":             func(s string) error { return create(func(r *AskCreateRequest) { r.Options[0].Label = s }) },
		"scope action":             func(s string) error { return create(func(r *AskCreateRequest) { r.Scope.Action = s }) },
		"scope target":             func(s string) error { return create(func(r *AskCreateRequest) { r.Scope.Target = s }) },
		"actor issuer":             func(s string) error { return answer(func(r *AskAnswerRequest) { r.Actor.Issuer = s }) },
		"actor subject":            func(s string) error { return answer(func(r *AskAnswerRequest) { r.Actor.Subject = s }) },
		"actor display name":       func(s string) error { return answer(func(r *AskAnswerRequest) { r.Actor.DisplayName = s }) },
		"session name": func(s string) error {
			a := askRecord()
			a.Context.SessionName = s
			return ValidateAsk(a)
		},
	}
}

func TestAskRedaction(t *testing.T) {
	req := AskAnswerRequest{OptionID: "yes", Text: "secret-note", Approved: boolp(true),
		Actor: AskActor{Issuer: "https://idp.example/secret-issuer", Subject: "secret-subject", DisplayName: "secret-name"}}
	ans := AskAnswer{OptionID: "yes", AnsweredBy: req.Actor, AnsweredAt: askNow}
	for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
		for name, v := range map[string]any{"request": req, "request pointer": &req, "actor": req.Actor, "answer": ans} {
			out := fmt.Sprintf(f, v)
			for _, secret := range []string{"secret-note", "secret-issuer", "secret-subject", "secret-name"} {
				if strings.Contains(out, secret) {
					t.Errorf("%s formatted with %s leaks %q: %s", name, f, secret, out)
				}
			}
		}
	}
	b, err := json.Marshal(req)
	if err != nil || !strings.Contains(string(b), "secret-subject") || !strings.Contains(string(b), "secret-note") {
		t.Errorf("redaction must not touch JSON: %s, %v", b, err)
	}
}

func TestAskListJSON(t *testing.T) {
	for name, v := range map[string]any{"value": AskList{Complete: true}, "pointer": &AskList{Complete: true}, "empty slice": AskList{Asks: []Ask{}, Complete: true}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != `{"asks":[],"complete":true}` {
			t.Errorf("%s: %s", name, b)
		}
	}
	b, _ := json.Marshal(struct {
		L AskList `json:"l"`
	}{})
	if !strings.Contains(string(b), `"asks":[]`) {
		t.Errorf("nested: %s", b)
	}
	full := AskList{Asks: []Ask{askRecord()}, NextCursor: "c"}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	var back AskList
	if err := json.Unmarshal(b, &back); err != nil || len(back.Asks) != 1 || back.NextCursor != "c" || back.Complete {
		t.Errorf("round trip: %+v, %v (%s)", back, err, b)
	}
}

func TestAskErrorNamesFieldNotValue(t *testing.T) {
	r := askInfoRequest()
	r.Question = "secret\x00value"
	err := r.ValidateAt(askNow)
	if err == nil || !strings.Contains(err.Error(), "question") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error = %v", err)
	}
}

func TestAskJSONFieldNames(t *testing.T) {
	a := askAnswered(askRecord())
	a.Delivery = &AskDelivery{InboxMessage: "m", DeliveredAt: askNow.Add(time.Hour)}
	a.Type = AskTypeApproval
	a.Answer.Approved = boolp(false)
	a.Scope = &AskScope{Action: "merge", Target: "t", ExpiresAt: askNow.Add(2 * time.Hour)}
	a.ExpiresAt = a.Scope.ExpiresAt
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "type", "question", "options", "recommended", "allowFreeText", "context",
		"scope", "createdAt", "expiresAt", "state", "answer", "delivery"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing top-level key %q in %s", k, b)
		}
	}
	ctx := m["context"].(map[string]any)
	for _, k := range []string{"sessionName", "sessionUID", "workPackage", "links"} {
		if _, ok := ctx[k]; !ok {
			t.Errorf("missing context key %q", k)
		}
	}
	ans := m["answer"].(map[string]any)
	for _, k := range []string{"optionId", "approved", "answeredBy", "answeredAt"} {
		if _, ok := ans[k]; !ok {
			t.Errorf("missing answer key %q", k)
		}
	}
	by := ans["answeredBy"].(map[string]any)
	for _, k := range []string{"issuer", "subject", "displayName"} {
		if _, ok := by[k]; !ok {
			t.Errorf("missing answeredBy key %q", k)
		}
	}
	if d := m["delivery"].(map[string]any); d["inboxMessage"] == nil || d["deliveredAt"] == nil {
		t.Errorf("delivery keys: %v", d)
	}
	if sc := m["scope"].(map[string]any); sc["action"] == nil || sc["target"] == nil || sc["expiresAt"] == nil {
		t.Errorf("scope keys: %v", sc)
	}

	var back Ask
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAsk(back); err != nil {
		t.Fatalf("round trip invalid: %v", err)
	}

	rb, _ := json.Marshal(askApprovalRequest())
	var rm map[string]any
	_ = json.Unmarshal(rb, &rm)
	for _, k := range []string{"requestId", "type", "question", "options", "recommended", "allowFreeText", "context", "scope", "expiresIn"} {
		if _, ok := rm[k]; !ok {
			t.Errorf("missing request key %q", k)
		}
	}
	ab, _ := json.Marshal(AskAnswerRequest{OptionID: "yes", Actor: askActor()})
	if !strings.Contains(string(ab), `"optionId"`) || !strings.Contains(string(ab), `"actor"`) || strings.Contains(string(ab), `"approved"`) {
		t.Errorf("unset approved must be omitted: %s", ab)
	}
	ab, _ = json.Marshal(AskAnswerRequest{Approved: boolp(false), Actor: askActor()})
	if !strings.Contains(string(ab), `"approved":false`) {
		t.Errorf("explicit false must be sent: %s", ab)
	}
	var back2 AskAnswerRequest
	if err := json.Unmarshal(ab, &back2); err != nil || back2.Approved == nil || *back2.Approved {
		t.Errorf("approved false round trip: %+v, %v", back2, err)
	}
	var fresh AskAnswerRequest
	if err := json.Unmarshal([]byte(`{"optionId":"yes"}`), &fresh); err != nil || fresh.Approved != nil {
		t.Errorf("absent approved must decode to nil: %+v, %v", fresh, err)
	}
	lb, _ := json.Marshal(AskList{Asks: []Ask{askRecord()}, NextCursor: "c", Complete: false})
	if !strings.Contains(string(lb), `"asks"`) || !strings.Contains(string(lb), `"nextCursor"`) || !strings.Contains(string(lb), `"complete"`) {
		t.Errorf("list json: %s", lb)
	}
}

func TestAskRFC3339Required(t *testing.T) {
	var r AskCreateRequest
	body := `{"requestId":"req-12345678","type":"approval","question":"q","allowFreeText":true,
	"scope":{"action":"a","target":"t","expiresAt":"not a time"}}`
	if err := json.Unmarshal([]byte(body), &r); err == nil {
		t.Fatal("non-RFC 3339 time decoded")
	}
}
