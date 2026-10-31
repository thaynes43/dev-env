package agentrun

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// grantDecide posts one allowlisted delegate's decision. Without --approve the
// decision is a denial. The command prints the scope digest and the grant's
// secret names before it posts. HTTP 202 means the decision was accepted; it
// does not mean the grant is Active. A delegate decision is not a human decision.
func (a *app) grantDecide(ctx context.Context, args []string) error {
	cmd := newCommand("grant", outputJSON)
	var uid, digest, ttl, ref, reason string
	var approve, reserved bool
	var tomRef string
	cmd.fs.StringVar(&uid, "grant-uid", "", "")
	cmd.fs.StringVar(&digest, "digest", "", "")
	cmd.fs.StringVar(&ttl, "ttl", "", "")
	cmd.fs.StringVar(&ref, "ref", "", "")
	cmd.fs.StringVar(&reason, "reason", "", "")
	cmd.fs.BoolVar(&approve, "approve", false, "")
	cmd.fs.BoolVar(&reserved, "reserved", false, "")
	cmd.fs.StringVar(&tomRef, "tom-ref", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("grant decide takes one grant-<id> name, --grant-uid, --digest and --ref; omit --approve to deny")
	}
	if err := validateGrantDecision(pos[0], uid, digest, ttl, ref, reason); err != nil {
		return err
	}
	req := grantDecisionRequest(uid, digest, ttl, ref, reason, approve)
	req.Reserved = reserved
	req.TomRef = tomRef
	if err := validateReservedDecisionExtras(req); err != nil {
		return err
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	// Read the grant so the binding line can show secret names. Names only;
	// secret values are not on this document and are never requested.
	var current apiv1.Grant
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.GrantPath(pos[0]), nil, nil, &current); err != nil {
		return err
	}
	if current.UID == "" {
		return fail(ExitNotFound, "delegate decisions are not enabled on this operator: it shows no grant UID")
	}
	if current.UID != uid || current.ScopeDigest != digest {
		return fail(ExitFailed, "grant decide: grant UID or scope digest changed; read grant show -o json and review the current scope before deciding")
	}
	if err := validateReservedCurrent(req, current); err != nil {
		return err
	}
	binding := grantDecisionBinding(pos[0], uid, digest, current.Secrets, approve)
	if req.Reserved {
		binding += " Reserved case; Tom message reference " + req.TomRef + " is an agent assertion, not verified human consent."
	}
	if cmd.c.output == outputJSON {
		_, _ = fmt.Fprintln(a.env.Stderr, binding)
	} else {
		a.outf("%s\n", binding)
	}
	out, err := a.postGrantDecision(ctx, c, pos[0], req, &current)
	if err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(out)
	}
	// 202 accepts the decision. The grant's phase is whatever the operator
	// returned; acceptance does not make it Active.
	a.outf("decision accepted\n")
	return a.printGrant(out)
}

func grantDecisionRequest(uid, digest, ttl, ref, reason string, approve bool) apiv1.GrantDecisionRequest {
	return apiv1.GrantDecisionRequest{
		GrantUID: uid, Digest: digest, Approve: approve, TTL: ttl, Ref: ref, Reason: reason,
	}
}

func grantDecisionBinding(name, uid, digest string, secrets []string, approve bool) string {
	kind := "denial"
	if approve {
		kind = "approval"
	}
	listed := "(none)"
	if len(secrets) > 0 {
		listed = strings.Join(secrets, ", ")
	}
	return fmt.Sprintf("deciding %s uid %s: binding scope digest %s; secrets: %s; %s. A delegate decision is not a human decision.",
		name, uid, digest, listed, kind)
}

// validateGrantDecision checks the decision locally. A refusal returns before
// any request. ref is 1 to 128 letters, digits and . _ : / # -. ttl, when set,
// is a Go duration of at least 10m. reason, when set, is at most 256 characters
// and has no control or format characters.
func validateGrantDecision(name, uid, digest, ttl, ref, reason string) error {
	if !validGrantCLIName(name) {
		return usageError("grant decide: %q is not a grant-<id> name", name)
	}
	if strings.TrimSpace(uid) == "" {
		return usageError("grant decide: --grant-uid is required")
	}
	if strings.TrimSpace(digest) == "" {
		return usageError("grant decide: --digest is required (Grant.ScopeDigest)")
	}
	if !validGrantDecisionRef(ref) {
		return usageError("grant decide: --ref is required, 1 to 128 letters, digits and . _ : / # -")
	}
	if ttl != "" {
		d, err := time.ParseDuration(ttl)
		if err != nil || d < 10*time.Minute {
			return usageError("grant decide: --ttl is a Go duration of at least 10m, not %q", ttl)
		}
	}
	if reason != "" && (!utf8.ValidString(reason) || utf8.RuneCountInString(reason) > 256 || strings.ContainsFunc(reason, func(c rune) bool { return unicode.In(c, unicode.Cc, unicode.Cf) })) {
		return usageError("grant decide: --reason is at most 256 characters and cannot contain control or format characters")
	}
	return nil
}

func validGrantDecisionRef(ref string) bool {
	if len(ref) < 1 || len(ref) > 128 {
		return false
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == ':' || c == '/' || c == '#' || c == '-':
		default:
			return false
		}
	}
	return true
}

// postGrantDecision is the one decision route, shared by the CLI and decide_grant.
func (a *app) postGrantDecision(ctx context.Context, c *conn, name string, req apiv1.GrantDecisionRequest, expected ...*apiv1.Grant) (apiv1.Grant, error) {
	if req.Reserved {
		return a.postReservedDecision(ctx, c, name, req, expected...)
	}
	var out apiv1.Grant
	retriedUncertain := false
	status, err := a.callWithRetryObserver(ctx, c, http.MethodPost, apiv1.GrantDecisionPath(name), nil, req, &out, true, func(status int, err error) {
		if err != nil || status == http.StatusServiceUnavailable {
			retriedUncertain = true
		}
	})
	if err != nil {
		if status == http.StatusConflict && retriedUncertain {
			return out, fail(ExitUnconfirmed, "the decision may already have been recorded by an earlier attempt; inspect agent-run grant show %s before deciding again", name)
		}
		return out, grantDecisionError(status, err)
	}
	return out, nil
}

// grantDecisionError keeps the structured API error from refusal, except where
// the decision route is off. A typed 404 not_found is a missing grant. The
// operator's unregistered-route body ("no route METHOD PATH"), a 404 that is
// not an API error mean delegate decisions are not enabled. An unavailable
// response retains the server diagnostic; it does not establish route absence.
func grantDecisionError(status int, err error) error {
	if err == nil {
		return nil
	}
	if status == http.StatusNotFound && decisionRouteDisabled(err) {
		return fail(ExitNotFound, "delegate decisions are not enabled on this operator")
	}
	return err
}

func decisionRouteDisabled(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "no route POST "+apiv1.GrantsPath+"/") || strings.Contains(msg, "not an API error")
}
