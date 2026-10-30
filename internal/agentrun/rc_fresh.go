package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// rcFresh submits the existing narrow Codex endpoint exactly once. The request
// file contains task text and immutable identities, never native methods/paths.
// Native ACKs do not imply that a supported phone client has observed the thread.
func (a *app) rcFresh(ctx context.Context, args []string) error {
	cmd := newCommand("rc", outputJSON)
	var session, file string
	cmd.fs.StringVar(&session, "session", "", "")
	cmd.fs.StringVar(&file, "request", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || cmd.c.output != outputJSON || !projectName.MatchString(session) || file == "" {
		return usageError("rc fresh requires --session NAME --request FILE -o json")
	}
	f, err := os.Open(file)
	if err != nil {
		return usageError("rc fresh request file unavailable")
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, apiv1.CodexRCMaxRequest+1))
	_ = f.Close()
	var req apiv1.CodexRCRequest
	if readErr != nil || apiv1.DecodeCodexRC(raw, &req, apiv1.CodexRCMaxRequest) != nil || !req.Valid(a.env.Now()) {
		return usageError("invalid or expired rc fresh request")
	}
	ctx, cancel := context.WithTimeout(ctx, req.Deadline.Sub(a.env.Now()))
	defer cancel()
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http, one.rejectOversizeResponse = &hc, true
	path := apiv1.SessionPath(session) + "/codex-thread"
	payload, _ := json.Marshal(req)
	// Keep the raw body: decoding into json.RawMessage first would discard
	// trailing whitespace and let an oversized padded receipt pass the fence.
	status, response, err := a.send(ctx, &one, http.MethodPost, one.url+path, payload)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			return err
		}
		if nothingSent(err) {
			return fail(ExitRetry, "rc fresh request was not sent")
		}
		return unconfirmed(http.MethodPost, path, "receipt unavailable")
	}
	if status/100 != 2 {
		return oneShotRefusal(&one, http.MethodPost, path, status, response)
	}
	var receipt apiv1.CodexRCReceipt
	if (status != http.StatusCreated && status != http.StatusOK) || apiv1.DecodeCodexRC(response, &receipt, apiv1.CodexRCMaxPacket) != nil ||
		!receipt.Acknowledged() || !receipt.Target.StructurallyValid() || receipt.RequestID != req.RequestID || receipt.Target.SessionName != session ||
		!receipt.Target.Matches(req) || receipt.Model != req.Model || receipt.Effort != req.Effort ||
		receipt.Caller == "" || receipt.ClientSHA256 != apiv1.CodexRCClientHash(receipt.Caller, req) ||
		receipt.DispatchSHA256 != apiv1.CodexRCHash(apiv1.CodexRCDispatch{Caller: receipt.Caller, Request: req, Target: receipt.Target, ClientSHA256: receipt.ClientSHA256}) {
		return fail(ExitUnconfirmed, "rc fresh receipt unconfirmed; do not repeat the request; observe the retained receipt")
	}
	return a.printJSON(struct {
		Receipt           apiv1.CodexRCReceipt `json:"receipt"`
		PhoneRegistration string               `json:"phoneRegistration"`
	}{receipt, "unobserved"})
}
