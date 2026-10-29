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
.ask-meta{display:flex;align-items:center;flex-wrap:wrap;gap:10px}
.ask-card .ask-question{font-size:18px;line-height:1.5;white-space:pre-wrap;margin:14px 0 18px}
.ask-context{margin-bottom:16px}
.ask-links{display:grid;gap:8px;margin:16px 0}
.ask-scope{border:1px solid var(--line);border-left:3px solid var(--amber);border-radius:6px;padding:16px;margin:18px 0;background:var(--bg)}
.ask-scope .eyebrow{margin-bottom:12px}
.ask-options{display:grid;gap:12px;margin:18px 0}
.ask-options .badge{margin-left:6px}
.ask-answer,.ask-record{border-top:1px solid var(--line);padding-top:18px;margin-top:18px}
.ask-answer{display:grid;gap:16px}.ask-answer .btn{justify-self:start}
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
@media(max-width:700px){.asks-content{overflow:visible;padding:20px 16px 96px}.ask-card{padding:16px}.ask-card .ask-question{font-size:17px}.ask-text textarea{font-size:16px}.ask-answer .btn{min-height:44px}.ask-scope .kv,.ask-context,.ask-record .kv{grid-template-columns:1fr;gap:4px}.ask-scope dd,.ask-context dd,.ask-record dd{margin-bottom:8px}}
`

var asksPolicy = strings.Replace(policy(viewsStyle+asksStyle), "form-action 'none'", "form-action 'self'", 1)
var asksTemplate = template.Must(template.New("asks").Parse(strings.Replace(asksSource, "/*PORTAL_STYLE*/", viewsStyle+asksStyle, 1)))

func init() {
	for _, name := range []string{"product-nav", "product-mobile-nav", "product-icons"} {
		template.Must(asksTemplate.AddParseTree(name, viewsTemplate.Lookup(name).Tree.Copy()))
	}
}

// AsksPage carries only an authorized read and the current request's form
// credentials. Preview disables every answer control.
type AsksPage struct {
	Asks                                           []apiv1.Ask
	Connected, Complete, CanAnswer, Preview, Retry bool
	Notice, CSRF                                   string
	Now                                            time.Time
}

type askCard struct {
	Ask                                                                        apiv1.Ask
	State, SessionHref, WorkHref, AnswerHref, CSRF, RequestID                  string
	CreatedAt, CreatedLabel, ExpiresAt, ExpiresLabel                           string
	ScopeExpiresAt, ScopeExpiresLabel, AnsweredAt, AnsweredLabel, AnswerOption string
	Approval, ApprovalExpired, CanAnswer, Preview                              bool
}

// RenderAsks returns one complete escaped page and its exact CSP. Shared nav
// trees are copied because html/template rewrites them during escaping.
func RenderAsks(page AsksPage, limit int) ([]byte, string, error) {
	if limit <= 0 {
		return nil, "", ErrUnavailable
	}
	view := struct {
		AsksPage
		Policy       string
		Open, Closed []askCard
	}{AsksPage: page, Policy: asksPolicy}
	if page.Connected {
		asks := append([]apiv1.Ask(nil), page.Asks...)
		sort.SliceStable(asks, func(i, j int) bool { return asks[i].CreatedAt.After(asks[j].CreatedAt) })
		for _, ask := range asks {
			if apiv1.ValidateAsk(ask) != nil {
				return nil, "", ErrUnavailable
			}
			card := askCard{Ask: ask, State: string(ask.State), SessionHref: productSessionsHref + "?session=" + url.QueryEscape(ask.Context.SessionUID), AnswerHref: "/portal/projects/dev-env/environments/internal/asks/answer", CSRF: page.CSRF, Approval: ask.Type == apiv1.AskTypeApproval, Preview: page.Preview}
			card.CreatedAt, card.CreatedLabel = askTime(ask.CreatedAt)
			card.ExpiresAt, card.ExpiresLabel = askTime(ask.ExpiresAt)
			if ask.Context.WorkPackage > 0 {
				card.WorkHref = "https://work.haynesnetwork.com/work_packages/" + strconv.Itoa(ask.Context.WorkPackage)
			}
			if ask.Scope != nil {
				card.ScopeExpiresAt, card.ScopeExpiresLabel = askTime(ask.Scope.ExpiresAt)
				card.ApprovalExpired = !page.Now.Before(ask.Scope.ExpiresAt)
			}
			if card.State == string(apiv1.AskStateOpen) && (!page.Now.Before(ask.ExpiresAt) || card.ApprovalExpired) {
				card.State = "expired"
			}
			open := card.State == string(apiv1.AskStateOpen)
			card.CanAnswer = open && (page.Preview || (page.CanAnswer && page.CSRF != ""))
			if card.CanAnswer && !page.Preview {
				var nonce [32]byte
				if _, err := rand.Read(nonce[:]); err != nil {
					return nil, "", ErrUnavailable
				}
				card.RequestID = base64.RawURLEncoding.EncodeToString(nonce[:])
			}
			if ask.Answer != nil {
				card.AnsweredAt, card.AnsweredLabel = askTime(ask.Answer.AnsweredAt)
				for _, option := range ask.Options {
					if option.ID == ask.Answer.OptionID {
						card.AnswerOption = option.Label
					}
				}
			}
			if open {
				view.Open = append(view.Open, card)
			} else {
				view.Closed = append(view.Closed, card)
			}
		}
	}
	buffer := &boundedPagesBuffer{limit: limit}
	if err := asksTemplate.Execute(buffer, view); err != nil {
		return nil, "", ErrUnavailable
	}
	return buffer.Bytes(), asksPolicy, nil
}

func askTime(at time.Time) (string, string) {
	return at.UTC().Format(time.RFC3339), at.UTC().Format("2 Jan 2006, 15:04 UTC")
}
