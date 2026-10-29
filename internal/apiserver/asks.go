package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/asks"
)

// codeAskExpired is the 410 body code. apiv1 has no expired code; the contract
// fixes the HTTP status (410) and this name is the stable string beside it.
const codeAskExpired = "expired"

const maxAskBody = 64 << 10

func (s *Server) registerAsks(mux *http.ServeMux) {
	if s.Asks == nil {
		return
	}
	mux.Handle(apiv1.AsksPath, s.serve(route{methods: map[string]handler{
		http.MethodPost: s.createAsk,
		http.MethodGet:  s.listMyAsks,
	}}))
	mux.Handle(apiv1.AsksPath+"/{id}", s.serve(route{methods: map[string]handler{
		http.MethodGet: s.getMyAsk,
	}}))
	mux.Handle(apiv1.AsksPath+"/{id}"+apiv1.AskWithdrawSuffix, s.serve(route{methods: map[string]handler{
		http.MethodPost: s.withdrawAsk,
	}}))
	mux.Handle(apiv1.PortalAsksPath, s.serve(route{methods: map[string]handler{
		http.MethodGet: s.listPortalAsks,
	}}))
	mux.Handle(apiv1.PortalAsksPath+"/{id}", s.serve(route{methods: map[string]handler{
		http.MethodGet: s.getPortalAsk,
	}}))
	mux.Handle(apiv1.PortalAsksPath+"/{id}"+apiv1.AskAnswerSuffix, s.serve(route{methods: map[string]handler{
		http.MethodPost: s.answerAsk,
	}}))
}

// portalAskRoute reports whether r is one of the portal ask routes. Invalid
// ids still match, so the handler can answer 404; serve has already rejected
// the wrong method.
func (s *Server) portalAskRoute(r *http.Request) bool {
	if s == nil || s.Asks == nil || r == nil {
		return false
	}
	p := r.URL.Path
	switch r.Method {
	case http.MethodGet:
		if p == apiv1.PortalAsksPath {
			return true
		}
		rest, ok := strings.CutPrefix(p, apiv1.PortalAsksPath+"/")
		return ok && rest != "" && !strings.Contains(rest, "/")
	case http.MethodPost:
		rest, ok := strings.CutPrefix(p, apiv1.PortalAsksPath+"/")
		id, found := strings.CutSuffix(rest, apiv1.AskAnswerSuffix)
		return ok && found && id != "" && !strings.Contains(strings.TrimSuffix(id, "/"), "/")
	default:
		return false
	}
}

func (s *Server) askNow() time.Time { return s.now().UTC().Truncate(time.Second) }

func askRequester(c *caller) (asks.Requester, error) {
	if c == nil || c.kind != kindSession || c.session == nil || c.session.Name == "" || c.session.UID == "" || c.session.Namespace != asks.Namespace {
		return asks.Requester{}, forbidden("only the requesting session may use this ask route")
	}
	return asks.Requester{Namespace: c.session.Namespace, Name: c.session.Name, UID: string(c.session.UID)}, nil
}

func portalAskCaller(c *caller) error {
	if c == nil || c.kind != kindPortalReader {
		return forbidden("only the portal reader may use this ask route")
	}
	return nil
}

func (s *Server) createAsk(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if r.URL.RawQuery != "" {
		return 0, nil, badRequest("this route takes no query")
	}
	reqr, err := askRequester(c)
	if err != nil {
		return 0, nil, err
	}
	var body apiv1.AskCreateRequest
	if err := decodeJSON(w, r, maxAskBody, &body, true); err != nil {
		return 0, nil, err
	}
	now := s.askNow()
	ask, created, err := s.Asks.Create(ctx, reqr, body, now)
	if err != nil {
		return 0, nil, mapAskError(err)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return status, presentAsk(ask, now, true), nil
}

func (s *Server) listMyAsks(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	reqr, err := askRequester(c)
	if err != nil {
		return 0, nil, err
	}
	limit, cursor, err := parseAskQuery(r, []string{apiv1.AskQueryMine, apiv1.AskQueryLimit, apiv1.AskQueryCursor}, true, false)
	if err != nil {
		return 0, nil, err
	}
	now := s.askNow()
	items, incomplete, err := s.Asks.ListByRequester(ctx, reqr)
	if err != nil {
		return 0, nil, mapAskError(err)
	}
	items = asksForRequester(items, reqr)
	return askPage(items, asks.PageMine, limit, cursor, now, true, incomplete)
}

func asksForRequester(items []apiv1.Ask, reqr asks.Requester) []apiv1.Ask {
	out := make([]apiv1.Ask, 0, len(items))
	for _, item := range items {
		if item.Context.SessionName == reqr.Name && item.Context.SessionUID == reqr.UID {
			out = append(out, item)
		}
	}
	return out
}

func (s *Server) getMyAsk(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if r.URL.RawQuery != "" {
		return 0, nil, badRequest("this route takes no query")
	}
	reqr, err := askRequester(c)
	if err != nil {
		return 0, nil, err
	}
	ask, err := s.ownedAsk(ctx, r.PathValue("id"), reqr)
	if err != nil {
		return 0, nil, mapAskError(err)
	}
	return http.StatusOK, presentAsk(ask, s.askNow(), true), nil
}

func (s *Server) withdrawAsk(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if r.URL.RawQuery != "" {
		return 0, nil, badRequest("this route takes no query")
	}
	reqr, err := askRequester(c)
	if err != nil {
		return 0, nil, err
	}
	now := s.askNow()
	ask, err := s.Asks.Withdraw(ctx, r.PathValue("id"), reqr, now)
	if err != nil {
		return 0, nil, mapAskError(err)
	}
	return http.StatusOK, presentAsk(ask, now, true), nil
}

func (s *Server) ownedAsk(ctx context.Context, id string, reqr asks.Requester) (apiv1.Ask, error) {
	if !apiv1.ValidAskID(id) {
		return apiv1.Ask{}, asks.ErrNotFound
	}
	ask, err := s.Asks.Get(ctx, id)
	if err != nil {
		return apiv1.Ask{}, err
	}
	if ask.Context.SessionName != reqr.Name || ask.Context.SessionUID != reqr.UID {
		return apiv1.Ask{}, asks.ErrNotFound
	}
	return ask, nil
}

func askPage(items []apiv1.Ask, kind string, limit int, cursor string, now time.Time, session, incomplete bool) (int, any, error) {
	page, err := asks.Page(items, kind, limit, cursor)
	if err != nil {
		return 0, nil, mapAskError(err)
	}
	if incomplete {
		page.Complete = false
	}
	page.Asks = presentAsks(page.Asks, now, session)
	return http.StatusOK, page, nil
}

func presentAsks(items []apiv1.Ask, now time.Time, session bool) []apiv1.Ask {
	out := make([]apiv1.Ask, len(items))
	for i := range items {
		out[i] = presentAsk(items[i], now, session)
	}
	return out
}

func presentAsk(a apiv1.Ask, now time.Time, session bool) apiv1.Ask {
	a.State = a.EffectiveState(now)
	a.Delivery = nil
	if session && a.Answer != nil {
		ans := *a.Answer
		ans.AnsweredBy = ans.AnsweredBy.SessionView()
		a.Answer = &ans
	}
	return a
}

func filterOpen(items []apiv1.Ask, now time.Time) []apiv1.Ask {
	out := make([]apiv1.Ask, 0, len(items))
	for _, a := range items {
		if a.EffectiveState(now) == apiv1.AskStateOpen {
			out = append(out, a)
		}
	}
	return out
}

func mapAskError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, apiv1.ErrAskInvalid):
		return badRequest("%s", err.Error())
	case errors.Is(err, apiv1.ErrAskExpired):
		return newError(http.StatusGone, codeAskExpired, "ask has expired")
	case errors.Is(err, apiv1.ErrAskNotOpen):
		return newError(http.StatusConflict, apiv1.CodeConflict, "ask is not open")
	case errors.Is(err, asks.ErrConflict):
		return newError(http.StatusConflict, apiv1.CodeConflict, "the ask requestId was already used for a different body, or the ask changed")
	case errors.Is(err, asks.ErrLimit):
		return askLimitError(err)
	case errors.Is(err, asks.ErrNotFound):
		return notFound("no such ask")
	case errors.Is(err, asks.ErrUnavailable):
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "asks are unavailable")
	default:
		return err
	}
}

func askLimitError(err error) *apiError {
	var lim *asks.LimitError
	if errors.As(err, &lim) {
		switch lim.Scope {
		case asks.LimitOpen:
			return tooManyRequests("this session is at its open ask limit")
		case asks.LimitRequester:
			return tooManyRequests("this session is at its ask limit")
		case asks.LimitNamespace:
			return tooManyRequests("the ask namespace is full")
		}
	}
	return tooManyRequests("an ask limit has been reached")
}

func parseAskQuery(r *http.Request, allowed []string, mine, portal bool) (int, string, error) {
	q, err := singleQuery(r, allowed)
	if err != nil {
		return 0, "", err
	}
	if mine && q.Get(apiv1.AskQueryMine) != "true" {
		return 0, "", badRequest("mine must be true")
	}
	if portal {
		switch q.Get(apiv1.AskQueryState) {
		case apiv1.AskStateFilterOpen, apiv1.AskStateFilterAll:
		default:
			return 0, "", badRequest("state must be open or all")
		}
	}
	limit, err := parseAskLimit(q.Get(apiv1.AskQueryLimit))
	if err != nil {
		return 0, "", err
	}
	cursor := q.Get(apiv1.AskQueryCursor)
	if len(cursor) > 512 {
		return 0, "", badRequest("cursor is too long")
	}
	return limit, cursor, nil
}

func singleQuery(r *http.Request, allowed []string) (url.Values, error) {
	q := r.URL.Query()
	ok := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		ok[key] = true
	}
	for key, values := range q {
		if !ok[key] {
			return nil, badRequest("unknown query parameter %q", key)
		}
		if len(values) != 1 {
			return nil, badRequest("query parameter %q is repeated", key)
		}
	}
	return q, nil
}

func parseAskLimit(s string) (int, error) {
	if s == "" {
		return apiv1.AskListDefaultLimit, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > apiv1.AskListMaxLimit {
		return 0, badRequest("limit must be 1 to 100")
	}
	return n, nil
}
