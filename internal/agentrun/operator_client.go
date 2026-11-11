package agentrun

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Operator is the front door's client for the operator API. It authenticates
// with one projected ServiceAccount token file and does not call the Kubernetes API.
type Operator struct {
	app  *app
	conn *conn
}

// OpenOperator prepares a client for apiURL using the bearer token in tokenFile.
// An empty apiURL or tokenFile uses the in-cluster defaults. The token is read
// once here so a missing file fails before the process serves, and again on
// each request so a projected rotation is picked up. The token is not logged.
func OpenOperator(apiURL, tokenFile, caFile string) (*Operator, error) {
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	if tokenFile == "" {
		tokenFile = DefaultSessionTokenFile
	}
	a := &app{env: DefaultEnv()}
	c, err := a.connect(context.Background(), connOpts{apiURL: apiURL, tokenFile: tokenFile, caFile: caFile})
	if err != nil {
		a.close()
		return nil, err
	}
	if _, err := c.token.Token(context.Background()); err != nil {
		a.close()
		return nil, err
	}
	return &Operator{app: a, conn: c}, nil
}

// Close releases idle connections opened for the operator.
func (o *Operator) Close() {
	if o != nil && o.app != nil {
		o.app.close()
	}
}

func (o *Operator) door() *appSessions { return &appSessions{a: o.app, c: o.conn} }

// ListMine implements SessionDoor.
func (o *Operator) ListMine(ctx context.Context) (apiv1.SessionList, error) {
	return o.door().ListMine(ctx)
}

// Get implements SessionDoor.
func (o *Operator) Get(ctx context.Context, name string) (apiv1.Session, error) {
	return o.door().Get(ctx, name)
}

// Create implements SessionDoor.
func (o *Operator) Create(ctx context.Context, req apiv1.CreateSessionRequest) (apiv1.Session, error) {
	return o.door().Create(ctx, req)
}

// Delete implements SessionDoor.
func (o *Operator) Delete(ctx context.Context, name string) (apiv1.Session, error) {
	return o.door().Delete(ctx, name)
}

// ListAsks reads one page of Asks for this caller. A policy caller is refused
// by the operator today; the caller reports that in plain words.
func (o *Operator) ListAsks(ctx context.Context) (apiv1.AskList, error) {
	var list apiv1.AskList
	_, err := o.app.call(ctx, o.conn, http.MethodGet, apiv1.AsksPath, url.Values{
		apiv1.AskQueryMine:  {"true"},
		apiv1.AskQueryLimit: {"100"},
	}, nil, &list)
	return list, err
}

// ListParentNotes reads one page of parent notes for this caller. The box is
// inbox or sent. A refusal drops the operator body: it can carry note text.
func (o *Operator) ListParentNotes(ctx context.Context, box string, limit int64, cursor string) (apiv1.InboxList, error) {
	q := url.Values{"box": {box}, "limit": {strconv.FormatInt(limit, 10)}}
	if cursor != "" {
		q.Set("continue", cursor)
	}
	var list apiv1.InboxList
	status, err := o.app.call(ctx, o.conn, http.MethodGet, apiv1.ParentNotesPath, q, nil, &list)
	return list, parentOperatorError(status, err)
}

// ReadParentNote reads one parent note, including its stored text.
func (o *Operator) ReadParentNote(ctx context.Context, name string) (apiv1.InboxMessage, error) {
	var msg apiv1.InboxMessage
	status, err := o.app.call(ctx, o.conn, http.MethodGet, apiv1.ParentNotesPath+"/"+url.PathEscape(name), nil, nil, &msg)
	return msg, parentOperatorError(status, err)
}

// AnswerParentNote stores one answer. It is not retried: a lost answer may
// already be stored.
func (o *Operator) AnswerParentNote(ctx context.Context, name string, req apiv1.AnswerParentRequest) (apiv1.InboxResult, error) {
	var result apiv1.InboxResult
	status, err := o.app.callOnce(ctx, o.conn, http.MethodPost, apiv1.ParentNotesPath+"/"+url.PathEscape(name)+"/answer", req, &result)
	return result, parentOperatorError(status, err)
}

// SendParentNote stores one plain message to a child this caller started.
func (o *Operator) SendParentNote(ctx context.Context, req apiv1.TellParentRequest) (apiv1.InboxResult, error) {
	var result apiv1.InboxResult
	status, err := o.app.callOnce(ctx, o.conn, http.MethodPost, apiv1.ParentNotesPath, req, &result)
	return result, parentOperatorError(status, err)
}

// parentOperatorError keeps the status and drops the operator body. A parent
// note refusal can carry the note text, and this client does not return it.
func parentOperatorError(status int, err error) error {
	if err == nil {
		return nil
	}
	if status == 0 {
		return fail(ExitFailed, "the operator did not answer")
	}
	code := ExitFailed
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		code = ExitAuth
	case http.StatusNotFound:
		code = ExitNotFound
	}
	return fail(code, "the operator answered %d", status)
}
