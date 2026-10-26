package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// portalAdmissionBootstrap sends the fixed action once. It deliberately has no
// target, namespace, data, identity, verb, retry, or permit flags.
func (a *app) portalAdmissionBootstrap(ctx context.Context, args []string) error {
	cmd := newCommand("portal-admission-bootstrap", outputJSON)
	cmd.c.output = outputJSON
	var requestID string
	cmd.fs.StringVar(&requestID, "request-id", "", "")
	count := flagCounts(args, "request-id")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || count["request-id"] != 1 || !canaryName.MatchString(requestID) {
		return usageError("portal-admission-bootstrap requires exactly one valid --request-id and no positional arguments")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	one.rejectOversizeResponse = true
	var raw json.RawMessage
	status, err := a.callOnce(ctx, &one, http.MethodPost, apiv1.PortalAdmissionBootstrapPath, apiv1.PortalAdmissionBootstrapRequest{RequestID: requestID}, &raw)
	if err != nil {
		return err
	}
	var receipt apiv1.PortalAdmissionBootstrapResult
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if (status != http.StatusCreated && status != http.StatusOK) || !utf8.Valid(raw) || dec.Decode(&receipt) != nil || dec.Decode(new(any)) != io.EOF || receipt.RequestID != requestID || (receipt.Disposition != "Created" && receipt.Disposition != "Present") || strings.TrimSpace(receipt.TargetUID) == "" || strings.TrimSpace(receipt.TargetRV) == "" {
		return fail(ExitUnconfirmed, "portal admission bootstrap receipt is unconfirmed; do not repeat the POST")
	}
	return a.printJSON(receipt)
}
