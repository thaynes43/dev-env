package apiserver

import (
	"context"
	"net/http"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
)

// ParseGrantDelegates parses the operator's --grant-delegates (DESIGN-042 6.2)
// with the broker's parser, and refuses every identity the API already serves
// in another class: Tom, a client, the shared session account, the workbench
// and the portal reader.
func ParseGrantDelegates(raw string, p Policy) ([]string, error) {
	refused := append([]string{p.Human, portalReaderRef}, p.Clients...)
	if p.SessionServiceAccount != "" {
		refused = append(refused, p.SessionNamespace+"/"+p.SessionServiceAccount)
	}
	if p.WorkbenchServiceAccount != "" {
		refused = append(refused, p.SessionNamespace+"/"+p.WorkbenchServiceAccount)
	}
	return broker.ParseGrantDelegates(raw, refused...)
}

// decideGrant serves POST /v1/grants/{name}/decision (DESIGN-042 6.2), default
// off: registered only when GrantDelegates names a delegate.
func (s *Server) decideGrant(_ context.Context, w http.ResponseWriter, r *http.Request, _ *caller) (int, any, error) {
	var req apiv1.GrantDecisionRequest
	if err := decodeJSON(w, r, 4096, &req, true); err != nil {
		return 0, nil, err
	}
	return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "delegate decisions are not built yet")
}
