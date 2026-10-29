package apiserver

import (
	"context"
	"net/http"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/asks"
)

func (s *Server) listPortalAsks(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if err := portalAskCaller(c); err != nil {
		return 0, nil, err
	}
	limit, cursor, err := parseAskQuery(r, []string{apiv1.AskQueryState, apiv1.AskQueryLimit, apiv1.AskQueryCursor}, false, true)
	if err != nil {
		return 0, nil, err
	}
	now := s.askNow()
	var (
		items []apiv1.Ask
		kind  string
	)
	if r.URL.Query().Get(apiv1.AskQueryState) == apiv1.AskStateFilterOpen {
		items, err = s.Asks.ListOpen(ctx)
		kind = asks.PageOpen
		if err == nil {
			items = filterOpen(items, now)
		}
	} else {
		items, err = s.Asks.ListNamespace(ctx)
		kind = asks.PageAll
	}
	if err != nil {
		return 0, nil, mapAskError(err)
	}
	return askPage(items, kind, limit, cursor, now, false)
}

func (s *Server) getPortalAsk(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if r.URL.RawQuery != "" {
		return 0, nil, badRequest("this route takes no query")
	}
	if err := portalAskCaller(c); err != nil {
		return 0, nil, err
	}
	id := r.PathValue("id")
	if !apiv1.ValidAskID(id) {
		return 0, nil, notFound("no such ask")
	}
	ask, err := s.Asks.Get(ctx, id)
	if err != nil {
		return 0, nil, mapAskError(err)
	}
	return http.StatusOK, presentAsk(ask, s.askNow(), false), nil
}

func (s *Server) answerAsk(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if r.URL.RawQuery != "" {
		return 0, nil, badRequest("this route takes no query")
	}
	if err := portalAskCaller(c); err != nil {
		return 0, nil, err
	}
	var body apiv1.AskAnswerRequest
	if err := decodeJSON(w, r, maxAskBody, &body, true); err != nil {
		return 0, nil, err
	}
	now := s.askNow()
	ask, err := s.Asks.Answer(ctx, r.PathValue("id"), body, now)
	if err != nil {
		return 0, nil, mapAskError(err)
	}
	return http.StatusOK, presentAsk(ask, now, false), nil
}
