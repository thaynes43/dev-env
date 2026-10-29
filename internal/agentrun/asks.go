package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// CreateAsk posts one intent with a caller-supplied stable requestId. The
// existing API client's retries reuse that same intent and idempotency key.
// Creation never waits for an answer; GetAsk observes one later.
func (a *app) CreateAsk(ctx context.Context, c *conn, req apiv1.AskCreateRequest) (apiv1.Ask, error) {
	var out apiv1.Ask
	req.Question = apiv1.NormalizeAskText(req.Question)
	// Copy the slice so normalizing intent does not mutate the caller's options.
	req.Options = append([]apiv1.AskOption(nil), req.Options...)
	for i := range req.Options {
		req.Options[i].Description = apiv1.NormalizeAskText(req.Options[i].Description)
	}
	if err := req.ValidateAt(a.env.Now().Truncate(time.Second)); err != nil {
		return out, usageError("invalid ask: %v", err)
	}
	err := a.callAsks(ctx, c, http.MethodPost, apiv1.AsksPath, nil, req, &out, true)
	return out, err
}

// GetAsk reads the owning session's ask, including its state and answer.
func (a *app) GetAsk(ctx context.Context, c *conn, id string) (apiv1.Ask, error) {
	var out apiv1.Ask
	if !apiv1.ValidAskID(id) {
		return out, usageError("ask id must be ask- followed by 16 lowercase hex digits")
	}
	err := a.callAsks(ctx, c, http.MethodGet, apiv1.AskPath(id), nil, nil, &out, true)
	if err == nil && out.ID != id {
		err = fail(ExitFailed, "the API returned a different ask id")
	}
	return out, err
}

// ListMyAsks reads exactly one page. A zero limit selects AskListDefaultLimit.
// The cursor is opaque and is encoded as a query value, never as a path.
func (a *app) ListMyAsks(ctx context.Context, c *conn, limit int, cursor string) (apiv1.AskList, error) {
	var out apiv1.AskList
	if limit < 0 || limit > apiv1.AskListMaxLimit {
		return out, usageError("asks limit must be between 1 and %d, or omitted", apiv1.AskListMaxLimit)
	}
	if limit == 0 {
		limit = apiv1.AskListDefaultLimit
	}
	q := url.Values{apiv1.AskQueryMine: {"true"}, apiv1.AskQueryLimit: {strconv.Itoa(limit)}}
	if cursor != "" {
		q.Set(apiv1.AskQueryCursor, cursor)
	}
	err := a.callAsks(ctx, c, http.MethodGet, apiv1.AsksPath, q, nil, &out, true)
	return out, err
}

// WithdrawAsk sends once: only an open ask may be withdrawn, so replaying a
// completed withdrawal could turn a lost acknowledgement into a conflict.
func (a *app) WithdrawAsk(ctx context.Context, c *conn, id string) (apiv1.Ask, error) {
	var out apiv1.Ask
	if !apiv1.ValidAskID(id) {
		return out, usageError("ask id must be ask- followed by 16 lowercase hex digits")
	}
	err := a.callAsks(ctx, c, http.MethodPost, apiv1.AskWithdrawPath(id), nil, struct{}{}, &out, false)
	if err == nil && (out.ID != id || out.State != apiv1.AskStateWithdrawn) {
		err = unconfirmed(http.MethodPost, apiv1.AskWithdrawPath(id), "the answer did not confirm this ask was withdrawn")
	}
	return out, err
}

// callAsks uses ordinary token discovery/rotation and the API transport, with
// redirects disabled and the whole response bounded before decoding.
func (a *app) callAsks(ctx context.Context, c *conn, method, path string, query url.Values, body, out any, retry bool) error {
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	one.rejectOversizeResponse = true
	var raw json.RawMessage
	status, err := a.callWith(ctx, &one, method, path, query, body, &raw, retry)
	if err != nil {
		if status == http.StatusNotFound || status == http.StatusForbidden {
			code := ExitNotFound
			var ee *exitError
			if errors.As(err, &ee) {
				code = ee.code
			}
			return fail(code, "asks are not enabled on this operator, or this session cannot access the requested ask: %v", err)
		}
		if status == http.StatusConflict {
			if method == http.MethodPost && path == apiv1.AsksPath {
				return fail(ExitFailed, "ask requestId conflicts with a different intent; reuse a requestId only for an identical retry: %v", err)
			}
			return fail(ExitFailed, "%s; it may already be answered or withdrawn: %v", apiv1.ErrAskNotOpen, err)
		}
		if status == http.StatusGone {
			return fail(ExitFailed, "%s; its lifetime or approval scope has ended: %v", apiv1.ErrAskExpired, err)
		}
		return err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(raw, out) != nil {
		err = errors.New("the answer is not the expected Ask JSON")
	} else {
		switch result := out.(type) {
		case *apiv1.Ask:
			err = apiv1.ValidateAsk(*result)
			if err == nil {
				*result = sessionAskView(*result)
			}
		case *apiv1.AskList:
			if len(result.Asks) > apiv1.AskListMaxLimit {
				err = errors.New("the ask page exceeds the maximum record count")
			}
			for i, ask := range result.Asks {
				if err != nil {
					break
				}
				err = apiv1.ValidateAsk(ask)
				if err == nil {
					result.Asks[i] = sessionAskView(ask)
				}
			}
		}
	}
	if err != nil {
		if !retry && method != http.MethodGet {
			return unconfirmed(method, path, err.Error())
		}
		return fail(ExitFailed, "the API answered %s %s with invalid Ask JSON: %v", method, path, err)
	}
	return nil
}

// Session responses carry displayName only. Apply the projection defensively
// even if an operator accidentally returns the full audit actor.
func sessionAskView(ask apiv1.Ask) apiv1.Ask {
	if ask.Answer != nil {
		answer := *ask.Answer
		answer.AnsweredBy = answer.AnsweredBy.SessionView()
		ask.Answer = &answer
	}
	return ask
}
