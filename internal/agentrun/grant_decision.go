package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

type decisionSummary struct {
	Grant           apiv1.Grant     `json:"grant"`
	Binding         json.RawMessage `json:"binding"`
	ResourceVersion string          `json:"resourceVersion"`
}

type decisionSubmission struct {
	Binding         json.RawMessage `json:"binding"`
	ResourceVersion string          `json:"resourceVersion"`
	Decision        string          `json:"decision"`
	TTL             string          `json:"ttl,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	RelayRef        string          `json:"relayRef,omitempty"`
}

// grantDecision uses the existing authenticated API; it neither assigns an
// identity nor treats a model's text as proof of a human decision.
func (a *app) grantDecision(ctx context.Context, args []string) error {
	if len(args) == 0 || (args[0] != "show" && args[0] != "submit") {
		return usageError("grant decision takes show or submit; agent-run help grant")
	}
	action := args[0]
	cmd := newCommand("grant", outputJSON)
	var decision, ttl, reason, relayRef string
	if action == "submit" {
		cmd.fs.StringVar(&decision, "decision", "", "")
		cmd.fs.StringVar(&ttl, "ttl", "", "")
		cmd.fs.StringVar(&reason, "reason", "", "")
		cmd.fs.StringVar(&relayRef, "relay-ref", "", "")
	}
	pos, err := cmd.parse(a, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 || !validGrantCLIName(pos[0]) {
		return usageError("grant decision %s takes one grant ID", action)
	}
	if action == "submit" && decision != "approve" && decision != "deny" {
		return usageError("grant decision submit needs --decision approve or deny")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	path := apiv1.GrantPath(pos[0]) + "/decision"
	if action == "show" {
		var summary decisionSummary
		if _, err := a.call(ctx, c, http.MethodGet, path, nil, nil, &summary); err != nil {
			return err
		}
		// Always JSON so it can be reviewed and supplied unchanged to submit.
		return a.printJSON(summary)
	}
	if a.env.Stdin == nil {
		return fail(ExitFailed, "grant decision needs the reviewed JSON summary on stdin")
	}
	raw, err := io.ReadAll(io.LimitReader(a.env.Stdin, 64<<10+1))
	if err != nil || len(raw) == 0 || len(raw) > 64<<10 {
		return fail(ExitFailed, "grant decision summary is unreadable or exceeds its limit")
	}
	var summary decisionSummary
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&summary) != nil || d.Decode(new(any)) != io.EOF || summary.Grant.Name != pos[0] || len(summary.Binding) == 0 || strings.TrimSpace(summary.ResourceVersion) == "" {
		return fail(ExitFailed, "grant decision summary does not match the named request")
	}
	body := decisionSubmission{Binding: summary.Binding, ResourceVersion: summary.ResourceVersion, Decision: decision, TTL: ttl, Reason: reason, RelayRef: relayRef}
	var result map[string]string
	if _, err := a.call(ctx, c, http.MethodPost, path, nil, body, &result); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(result)
	}
	a.outf("decision submitted; check grant status for the broker's result\n")
	return nil
}
