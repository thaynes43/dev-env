package agentrun

import (
	"context"
	"net/http"
	"net/url"

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
