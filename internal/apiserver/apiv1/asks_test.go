package apiv1

import (
	"encoding/json"
	"errors"
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
	r.Scope = &AskScope{Action: "merge", Target: "repo#1", ExpiresAt: askNow.Add(48 * time.Hour)}
	return r
}

func TestAskPaths(t *testing.T) {
	id := "ask-0123456789abcdef"
	for got, want := range map[string]string{
		AskPath(id):               "/v1/asks/" + id,
		AskWithdrawPath(id):       "/v1/asks/" + id + "/withdraw",
		PortalAskPath(id):         "/v1/portal/asks/" + id,
		PortalAskAnswerPath(id):   "/v1/portal/asks/" + id + "/answer",
		PortalAskWithdrawPath(id): "/v1/portal/asks/" + id + "/withdraw",
	} {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
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
		{"requestId short refused", func(r *AskCreateRequest) { r.RequestID = "abc" }, false},
		{"requestId underscore refused", func(r *AskCreateRequest) { r.RequestID = "abcd_1234" }, false},
		{"unknown type refused", func(r *AskCreateRequest) { r.Type = "poll" }, false},
		{"empty type refused", func(r *AskCreateRequest) { r.Type = "" }, false},
		{"empty question refused", func(r *AskCreateRequest) { r.Question = "" }, false},
		{"blank question refused", func(r *AskCreateRequest) { r.Question = "  \n " }, false},
		{"question 1000 runes accepted", func(r *AskCreateRequest) { r.Question = strings.Repeat("é", 1000) }, true},
		{"question 1001 runes refused", func(r *AskCreateRequest) { r.Question = strings.Repeat("é", 1001) }, false},
		{"question invalid UTF-8 refused", func(r *AskCreateRequest) { r.Question = "bad \xff" }, false},
		{"question tab refused", func(r *AskCreateRequest) { r.Question = "a\tb" }, false},
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
		{"option description tab refused", func(r *AskCreateRequest) { r.Options[1].Description = "a\tb" }, false},
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
		{"scope target empty refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.Target = "" }, false},
		{"scope target 201 refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.Target = long(201) }, false},
		{"scope expiry zero refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.ExpiresAt = time.Time{} }, false},
		{"scope expiry in the past refused", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.ExpiresAt = askNow.Add(-time.Second) }, false},
		{"scope expiry 7d accepted", func(r *AskCreateRequest) { *r = askApprovalRequest(); r.Scope.ExpiresAt = askNow.Add(AskMaxTTL) }, true},
		{"scope expiry over 7d refused", func(r *AskCreateRequest) {
			*r = askApprovalRequest()
			r.Scope.ExpiresAt = askNow.Add(AskMaxTTL + time.Second)
		}, false},
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
	r.Scope.ExpiresAt = time.Now().Add(time.Hour)
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
	if _, err := (AskCreateRequest{ExpiresIn: "8d"}).TTL(); err == nil {
		t.Error("8d accepted")
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
		a.Scope = &AskScope{Action: "merge", Target: "repo#1", ExpiresAt: askNow.Add(48 * time.Hour)}
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
		{"approval scope over 7d refused", func(a *Ask) { approval(a); a.Scope.ExpiresAt = a.CreatedAt.Add(AskMaxTTL + time.Second) }, false},
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
		{"delivery without message refused", func(a *Ask) { a.Delivery = &AskDelivery{DeliveredAt: askNow} }, false},
		{"delivery without time refused", func(a *Ask) { a.Delivery = &AskDelivery{InboxMessage: "m"} }, false},
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
		{"text tab refused", AskAnswerRequest{Text: "a\tb", Actor: askActor()}, false},
		{"text CR refused", AskAnswerRequest{Text: "a\r\nb", Actor: askActor()}, false},
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

func TestAskAnswerRequestValidateFor(t *testing.T) {
	info := askRecord()
	approval := askRecord()
	approval.Type = AskTypeApproval
	approval.Scope = &AskScope{Action: "merge", Target: "repo#1", ExpiresAt: askNow.Add(time.Hour)}
	free := askRecord()
	free.Options = nil
	free.Recommended = ""
	free.AllowFreeText = true
	bare := approval // approval with neither options nor free text
	bare.Options, bare.Recommended, bare.AllowFreeText = nil, "", false
	closed := askRecord()
	closed.State = AskStateWithdrawn

	cases := []struct {
		name string
		ask  Ask
		req  AskAnswerRequest
		ok   bool
	}{
		{"existing option", info, AskAnswerRequest{OptionID: "no", Actor: askActor()}, true},
		{"unknown option", info, AskAnswerRequest{OptionID: "maybe", Actor: askActor()}, false},
		{"text refused without allowFreeText", info, AskAnswerRequest{Text: "hi", Actor: askActor()}, false},
		{"option plus text refused without allowFreeText", info, AskAnswerRequest{OptionID: "yes", Text: "hi", Actor: askActor()}, false},
		{"text only on free-text ask", free, AskAnswerRequest{Text: "hi", Actor: askActor()}, true},
		{"option on ask with no options", free, AskAnswerRequest{OptionID: "yes", Actor: askActor()}, false},
		{"approved true on info refused", info, AskAnswerRequest{OptionID: "yes", Approved: boolp(true), Actor: askActor()}, false},
		{"approved false on info refused", info, AskAnswerRequest{OptionID: "yes", Approved: boolp(false), Actor: askActor()}, false},
		{"approved nil on info accepted", info, AskAnswerRequest{OptionID: "yes", Approved: nil, Actor: askActor()}, true},
		{"multi-line text on free-text info", free, AskAnswerRequest{Text: "a\nb", Actor: askActor()}, true},
		{"approved true on approval accepted", approval, AskAnswerRequest{OptionID: "yes", Approved: boolp(true), Actor: askActor()}, true},
		{"approved false on approval accepted", approval, AskAnswerRequest{OptionID: "no", Approved: boolp(false), Actor: askActor()}, true},
		{"approved nil on approval refused", approval, AskAnswerRequest{OptionID: "yes", Actor: askActor()}, false},
		{"approval decision alone accepted", approval, AskAnswerRequest{Approved: boolp(true), Actor: askActor()}, true},
		{"approval deny alone accepted", approval, AskAnswerRequest{Approved: boolp(false), Actor: askActor()}, true},
		{"approval unknown option refused", approval, AskAnswerRequest{OptionID: "maybe", Approved: boolp(true), Actor: askActor()}, false},
		{"approval note without allowFreeText accepted", approval, AskAnswerRequest{Approved: boolp(false), Text: "not now\nlater", Actor: askActor()}, true},
		{"approval note 2001 runes refused", approval, AskAnswerRequest{Approved: boolp(true), Text: strings.Repeat("a", 2001), Actor: askActor()}, false},
		{"approval with no options or free text accepted", bare, AskAnswerRequest{Approved: boolp(true), Text: "ok", Actor: askActor()}, true},
		{"approval with no options: option refused", bare, AskAnswerRequest{OptionID: "yes", Approved: boolp(true), Actor: askActor()}, false},
		{"approval nil on bare approval refused", bare, AskAnswerRequest{Text: "ok", Actor: askActor()}, false},
		{"withdrawn ask refused", closed, AskAnswerRequest{OptionID: "yes", Actor: askActor()}, false},
		{"shape error surfaces", info, AskAnswerRequest{OptionID: "yes", Actor: AskActor{}}, false},
		{"info with neither option nor text refused", info, AskAnswerRequest{Actor: askActor()}, false},
		{"info free-text ask with neither refused", free, AskAnswerRequest{Actor: askActor()}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.ValidateFor(tc.ask)
			if tc.ok && err != nil {
				t.Fatalf("want accepted, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrAskInvalid) {
				t.Fatalf("want ErrAskInvalid, got %v", err)
			}
		})
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
	a.Delivery = &AskDelivery{InboxMessage: "m", DeliveredAt: askNow}
	a.Type = AskTypeApproval
	a.Answer.Approved = boolp(false)
	a.Scope = &AskScope{Action: "merge", Target: "t", ExpiresAt: askNow.Add(time.Hour)}
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
