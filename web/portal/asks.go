package portal

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"html/template"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

//go:embed asks.html
var asksSource string

const asksStyle = `
.asks-content{flex:1;min-height:0;overflow:auto;padding:24px;overflow-wrap:anywhere}
.asks-content>section,.asks-heading{max-width:64rem;margin:0 auto 24px}
.asks-heading h1{font-size:24px;letter-spacing:-.5px;margin:4px 0}
.asks-heading>p:last-child,.asks-empty,.ask-options p{color:var(--muted)}
.asks-section{display:flex;flex-wrap:wrap;align-items:center;gap:10px;font-size:15px;margin:0 0 14px}
.ask-card{padding:22px;margin-bottom:16px;border:1px solid var(--line);border-radius:10px;background:var(--surface)}
.ask-card+.ask-card{margin-top:16px}
.ask-card>h4{margin:18px 0 4px;font-size:13px}
.ask-meta{display:flex;align-items:center;flex-wrap:wrap;gap:10px}
.ask-card .ask-question{font-size:18px;line-height:1.5;white-space:pre-wrap;margin:14px 0 18px}
.ask-context{margin-bottom:16px}
.ask-links{display:grid;gap:8px;margin:16px 0}
.ask-scope{border:1px solid var(--line);border-left:3px solid var(--amber);border-radius:6px;padding:16px;margin:18px 0;background:var(--bg)}
.ask-scope .eyebrow{margin-bottom:12px}
.ask-options{display:grid;gap:12px;margin:18px 0}
.ask-options .badge{margin-left:6px}.ask-options p{white-space:pre-wrap}
.ask-answer,.ask-record{border-top:1px solid var(--line);padding-top:18px;margin-top:18px}
.ask-answer{display:grid;gap:16px}.ask-answer .btn{justify-self:start}.ask-decisions{display:flex;flex-wrap:wrap;gap:12px}.ask-error{color:var(--red);white-space:pre-wrap}
.ask-answer fieldset{min-width:0;margin:0;padding:12px;border:1px solid var(--line);border-radius:6px}
.ask-answer>fieldset[disabled]{border:0;padding:0;display:grid;gap:16px}
.ask-answer legend{padding:0 6px;font-weight:600}
.ask-choice{display:flex;align-items:flex-start;gap:10px;min-height:44px;padding:10px 4px;cursor:pointer}
.ask-choice input{flex:none;margin:4px 0;accent-color:var(--accent)}
.ask-choice:focus-within{outline:2px solid var(--focus);outline-offset:2px;border-radius:4px}
.ask-text{display:grid;gap:8px;font-weight:600}
.ask-text textarea{width:100%;min-height:90px;resize:vertical;padding:10px;border:1px solid var(--control-line);border-radius:6px;background:var(--surface);color:var(--ink);font:inherit;font-weight:400}
.ask-text textarea:focus-visible{outline:2px solid var(--focus);outline-offset:2px}
.ask-record h4{margin:0 0 10px;font-size:13px}.ask-record .kv{margin-top:12px}.ask-record .turn-text{margin:8px 0}
.asks-notice p+p{margin-top:12px}
.asks-notice h2+p{margin-top:8px}
@media(max-width:700px){.asks-content{overflow:visible;padding:20px 16px 96px}.ask-card{padding:16px}.ask-card .ask-question{font-size:17px}.ask-text textarea{font-size:16px}.ask-answer .btn{min-height:44px}.asks-content .kv{grid-template-columns:minmax(0,2fr) minmax(0,3fr)}.asks-content .kv dd{text-align:right;min-width:0}}
`

var asksPolicy = strings.Replace(policy(viewsStyle+asksStyle), "form-action 'none'", "form-action 'self'", 1)
var asksTemplate = template.Must(template.New("asks").Parse(strings.Replace(asksSource, "/*PORTAL_STYLE*/", viewsStyle+asksStyle, 1)))

func init() {
	for _, name := range []string{"product-nav", "product-mobile-nav", "product-icons"} {
		template.Must(asksTemplate.AddParseTree(name, viewsTemplate.Lookup(name).Tree.Copy()))
	}
}

// AsksPage carries only an authorized read and the current request's form
// credentials. Complete describes only open-ask coverage; HistoryNotice
// describes the independent history page. Preview disables every answer control.
type AsksPage struct {
	Asks                                                                      []apiv1.Ask
	Connected, Complete, CanAnswer, Preview, Retry, ReadOnly                  bool
	Notice, CSRF, PortalHost, SelectedID, OlderHref, HistoryNotice, RetryHref string
	AnswerErrorID, AnswerError, AnswerText, AnswerOption                      string
	Now                                                                       time.Time
}

type askLink struct{ URL, Host, Href string }

type askCard struct {
	Ask                                                                                  apiv1.Ask
	State, StateLabel, SessionHref, WorkHref, AnswerHref, CSRF, RequestID                string
	CreatedAt, CreatedLabel, ExpiresAt, ExpiresLabel                                     string
	ScopeExpiresAt, ScopeExpiresLabel, AnsweredAt, AnsweredLabel, AnswerOption, Answerer string
	AnswerError, AnswerText, SelectedOption                                              string
	Links                                                                                []askLink
	Approval, Approved, ApprovalValid, CanAnswer, Preview                                bool
}

func askContextLink(raw, portalHost string) askLink {
	link := askLink{URL: raw}
	u, err := url.Parse(raw)
	if err != nil {
		return link
	}
	link.Host = u.Host
	// Host (including any port) must exactly equal an allowlisted host.
	// No suffix matching, credentials, explicit port, or alternate scheme.
	if u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Host == "" || strings.Contains(u.Host, ":") {
		return link
	}
	if u.Host == "claude.ai" || u.Host == "github.com" || u.Host == "work.haynesnetwork.com" || (portalHost != "" && u.Host == portalHost) {
		link.Href = raw
	}
	return link
}

// RenderAsks returns one complete escaped page and its exact CSP. Shared nav
// trees are copied because html/template rewrites them during escaping.
func RenderAsks(page AsksPage, limit int) ([]byte, string, error) {
	if limit <= 0 || (page.Connected && page.Now.IsZero()) {
		return nil, "", ErrUnavailable
	}
	page.RetryHref = "/portal/projects/dev-env/environments/internal/asks"
	if page.SelectedID != "" {
		page.RetryHref += "?" + url.Values{"ask": {page.SelectedID}}.Encode()
	}
	view := struct {
		AsksPage
		Policy       string
		Selected     *askCard
		Open, Closed []askCard
		OpenCount    int
	}{AsksPage: page, Policy: strings.TrimSuffix(asksPolicy, "; frame-ancestors 'none'")}
	if page.Connected {
		asks := append([]apiv1.Ask(nil), page.Asks...)
		sort.SliceStable(asks, func(i, j int) bool {
			if asks[i].CreatedAt.Equal(asks[j].CreatedAt) {
				return asks[i].ID < asks[j].ID
			}
			return asks[i].CreatedAt.After(asks[j].CreatedAt)
		})
		for _, ask := range asks {
			if apiv1.ValidateAsk(ask) != nil {
				return nil, "", ErrUnavailable
			}
			card := askCard{Ask: ask, State: string(ask.EffectiveState(page.Now)), SessionHref: productSessionsHref + "?session=" + url.QueryEscape(ask.Context.SessionUID), AnswerHref: "/portal/projects/dev-env/environments/internal/asks/answer", CSRF: page.CSRF, Approval: ask.Type == apiv1.AskTypeApproval, Preview: page.Preview}
			card.StateLabel = map[string]string{"open": "Open", "answered": "Answered", "withdrawn": "Withdrawn", "expired": "Expired"}[card.State]
			card.CreatedAt, card.CreatedLabel = askTime(ask.CreatedAt)
			card.ExpiresAt, card.ExpiresLabel = askTime(ask.ExpiresAt)
			if ask.Context.WorkPackage > 0 {
				card.WorkHref = "https://work.haynesnetwork.com/work_packages/" + strconv.Itoa(ask.Context.WorkPackage)
			}
			if ask.Scope != nil {
				card.ScopeExpiresAt, card.ScopeExpiresLabel = askTime(ask.Scope.ExpiresAt)
			}
			for _, link := range ask.Context.Links {
				card.Links = append(card.Links, askContextLink(link, page.PortalHost))
			}
			if ask.ID == page.AnswerErrorID {
				card.AnswerError, card.AnswerText, card.SelectedOption = page.AnswerError, page.AnswerText, page.AnswerOption
			}
			open := card.State == string(apiv1.AskStateOpen)
			card.CanAnswer = open && !page.ReadOnly && (page.Preview || (page.CanAnswer && page.CSRF != ""))
			if card.CanAnswer && !page.Preview {
				var nonce [32]byte
				if _, err := rand.Read(nonce[:]); err != nil {
					return nil, "", ErrUnavailable
				}
				card.RequestID = base64.RawURLEncoding.EncodeToString(nonce[:])
			}
			if ask.Answer != nil {
				card.Approved = ask.Answer.Approved != nil && *ask.Answer.Approved
				card.ApprovalValid = ask.ApprovalValid(page.Now)
				card.Answerer = ask.Answer.AnsweredBy.DisplayName
				if card.Answerer == "" {
					card.Answerer = ask.Answer.AnsweredBy.Subject
				}
				card.AnsweredAt, card.AnsweredLabel = askTime(ask.Answer.AnsweredAt)
				for _, option := range ask.Options {
					if option.ID == ask.Answer.OptionID {
						card.AnswerOption = option.Label
					}
				}
			}
			if open {
				view.OpenCount++
			}
			if ask.ID == page.SelectedID {
				view.Selected = &card
			} else if open {
				view.Open = append(view.Open, card)
			} else {
				view.Closed = append(view.Closed, card)
			}
		}
	}
	buffer := &boundedPagesBuffer{limit: limit}
	if err := asksTemplate.Execute(buffer, view); err != nil {
		// Try again without history before refusing the page. Open asks and
		// an explicitly selected ask keep priority over historical cards.
		if len(view.Closed) == 0 {
			return nil, "", ErrUnavailable
		}
		view.Closed = nil
		view.HistoryNotice = "Older history was omitted to fit this page."
		buffer = &boundedPagesBuffer{limit: limit}
		if err := asksTemplate.Execute(buffer, view); err != nil {
			return nil, "", ErrUnavailable
		}
	}
	return buffer.Bytes(), asksPolicy, nil
}

func askTime(at time.Time) (string, string) {
	return at.UTC().Format(time.RFC3339), at.UTC().Format("2 Jan 2006, 15:04 UTC")
}
