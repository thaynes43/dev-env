package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
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
	one.responseLimit = 1024
	var raw json.RawMessage
	status, err := a.callOnce(ctx, &one, http.MethodPost, apiv1.PortalAdmissionBootstrapPath, apiv1.PortalAdmissionBootstrapRequest{RequestID: requestID}, &raw)
	if err != nil {
		code := ExitUnconfirmed
		var ee *exitError
		if errors.As(err, &ee) {
			code = ee.code
		}
		return fail(code, "portal admission bootstrap was refused or unconfirmed; do not repeat the POST")
	}
	var receipt apiv1.PortalAdmissionBootstrapResult
	decodeErr := strictPortalBootstrapReceipt(raw, &receipt)
	rv, rvErr := strconv.ParseUint(receipt.TargetRV, 10, 64)
	validPair := status == http.StatusCreated && receipt.Disposition == "Created" || status == http.StatusOK && receipt.Disposition == "Present"
	if !validPair || !utf8.Valid(raw) || decodeErr != nil || receipt.RequestID != requestID || !canaryName.MatchString(receipt.TargetUID) || rvErr != nil || rv == 0 || strconv.FormatUint(rv, 10) != receipt.TargetRV {
		return fail(ExitUnconfirmed, "portal admission bootstrap receipt is unconfirmed; do not repeat the POST")
	}
	return a.printJSON(receipt)
}

// A receipt has exactly four string fields. encoding/json's struct decoder
// alone accepts duplicate and case-aliased keys, which is too ambiguous here.
func strictPortalBootstrapReceipt(raw []byte, out *apiv1.PortalAdmissionBootstrapResult) error {
	if len(raw) > 1024 || !utf8.Valid(raw) {
		return io.ErrUnexpectedEOF
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return io.ErrUnexpectedEOF
	}
	fields := map[string]*string{"disposition": &out.Disposition, "requestID": &out.RequestID, "targetUID": &out.TargetUID, "targetResourceVersion": &out.TargetRV}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || fields[name] == nil || seen[name] {
			return io.ErrUnexpectedEOF
		}
		seen[name] = true
		var value any
		if err := d.Decode(&value); err != nil {
			return err
		}
		str, ok := value.(string)
		if !ok {
			return io.ErrUnexpectedEOF
		}
		*fields[name] = str
	}
	if tok, err = d.Token(); err != nil || tok != json.Delim('}') || len(seen) != len(fields) || d.Decode(new(any)) != io.EOF {
		return io.ErrUnexpectedEOF
	}
	return nil
}
