package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// keeper-login (NB-01) renews the keeper's shared Claude or Codex login from a
// workbench session through the operator's /v1/keeper-login routes: no kubectl
// exec from the client, no v1 pod, no laptop. The link and the Codex device code
// go to stdout only; the Claude code is read from stdin, never an argument, so
// none of them reaches a log, a file or the process list.

const (
	// keeperLoginTimeout covers the operator's own waits (it answers within
	// 45 s), which are longer than an ordinary request.
	keeperLoginTimeout = 75 * time.Second
	// keeperLoginReconnects: only the operator's leader holds a ceremony, so a
	// follower's not_leader answer is retried on a new connection. Two replicas
	// make 12 misses in a row about 1 in 4000.
	keeperLoginReconnects = 12
	maxKeeperCodeInput    = 4 << 10
)

func (a *app) keeperLogin(ctx context.Context, args []string) error {
	cmd := newCommand("keeper-login", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return usageError("keeper-login takes claude or codex and a verb; agent-run help keeper-login says how")
	}
	provider := pos[0]
	if provider != apiv1.KeeperLoginClaude && provider != apiv1.KeeperLoginCodex {
		return usageError("keeper-login takes claude or codex, not %q", provider)
	}
	verb := "status"
	if len(pos) == 2 {
		verb = pos[1]
	}
	switch verb {
	case "status", "url", "code", "cancel":
	default:
		return usageError("keeper-login takes status, url, code or cancel, not %q", verb)
	}
	var code string
	if verb == "code" && provider == apiv1.KeeperLoginClaude {
		// Before any connection: a missing code sends nothing.
		raw, err := io.ReadAll(io.LimitReader(a.env.Stdin, maxKeeperCodeInput+1))
		if err != nil || len(raw) > maxKeeperCodeInput {
			return usageError("keeper-login code: read the one-time code from stdin (up to %d bytes)", maxKeeperCodeInput)
		}
		if code = strings.TrimSpace(string(raw)); code == "" {
			return usageError("keeper-login code: stdin is empty; pipe the code the sign-in page shows, for example: read -rs c; printf '%%s' \"$c\" | agent-run keeper-login claude code")
		}
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	path := apiv1.KeeperLoginPathFor(provider)
	switch verb {
	case "status":
		return a.keeperLoginStatus(ctx, c, cmd.c.output, path)
	case "url":
		return a.keeperLoginURL(ctx, c, cmd.c.output, path+"/url", provider)
	case "code":
		return a.keeperLoginCode(ctx, c, cmd.c.output, path+"/code", provider, code)
	default:
		return a.keeperLoginCancel(ctx, c, path, provider)
	}
}

func (a *app) keeperLoginStatus(ctx context.Context, c *conn, output, path string) error {
	var st apiv1.KeeperLoginStatus
	if _, err := a.call(ctx, c, http.MethodGet, path, nil, nil, &st); err != nil {
		return err
	}
	if output == outputJSON {
		if err := a.printJSON(st); err != nil {
			return err
		}
	} else {
		now := a.env.Now()
		line := fmt.Sprintf("%s: %s", st.Provider, st.State)
		if st.Generation > 0 {
			line += fmt.Sprintf(" (generation %d)", st.Generation)
		}
		a.outf("%s\n", line)
		if st.AccessExpiresAt != nil {
			a.outf("  access token expires %s (%s); the keeper renews it by itself\n", st.AccessExpiresAt.UTC().Format(time.RFC3339), until(now, *st.AccessExpiresAt))
		}
		if st.LoginExpiresAt != nil {
			a.outf("  login expires        %s (%s)\n", st.LoginExpiresAt.UTC().Format(time.RFC3339), until(now, *st.LoginExpiresAt))
		} else if st.AccessExpiresAt == nil && st.State != "Unavailable" {
			a.outf("  (this keeper does not report expiry yet)\n")
		}
	}
	if st.State != "Ready" && st.State != "PendingPublication" {
		return &exitError{code: ExitFailed}
	}
	return nil
}

func until(now, t time.Time) string {
	d := t.Sub(now)
	if d <= 0 {
		return "lapsed"
	}
	if d >= 48*time.Hour {
		return fmt.Sprintf("in %d days", int(d.Hours()/24))
	}
	return "in " + d.Truncate(time.Minute).String()
}

func (a *app) keeperLoginURL(ctx context.Context, c *conn, output, path, provider string) error {
	var out apiv1.KeeperLoginURL
	if _, _, err := a.keeperLoginPost(ctx, c, path, nil, &out); err != nil {
		return err
	}
	if output == outputJSON {
		return a.printJSON(out)
	}
	if provider == apiv1.KeeperLoginClaude {
		a.outf("Open this link, sign in, then pipe the code the page shows into: agent-run keeper-login claude code\n")
	} else {
		a.outf("Open this link, sign in, enter the code below on the page, then run: agent-run keeper-login codex code\n")
	}
	a.outf("%s\n", out.URL)
	if out.UserCode != "" {
		a.outf("code: %s\n", out.UserCode)
	}
	a.outf("The ceremony stays open until %s; abandon it with: agent-run keeper-login %s cancel\n", out.ExpiresAt.UTC().Format(time.RFC3339), provider)
	return nil
}

func (a *app) keeperLoginCode(ctx context.Context, c *conn, output, path, provider, code string) error {
	var body any
	if provider == apiv1.KeeperLoginClaude {
		body = apiv1.KeeperLoginCode{Code: code}
	}
	var res apiv1.KeeperLoginResult
	status, _, err := a.keeperLoginPost(ctx, c, path, body, &res)
	if err != nil {
		return err
	}
	if output == outputJSON {
		if err := a.printJSON(res); err != nil {
			return err
		}
	}
	switch status {
	case http.StatusAccepted:
		if output != outputJSON {
			a.outf("Still working: not finished yet. Run agent-run keeper-login %s status", provider)
			if provider == apiv1.KeeperLoginCodex {
				a.outf(", or this command again to keep waiting for the sign-in")
			}
			a.outf(".\n")
		}
		return fail(ExitRetry, "the keeper login has not finished")
	default:
		if output != outputJSON {
			a.outf("%s: the keeper adopted the new login (%s", provider, res.Result)
			if res.Generation > 0 {
				a.outf(", generation %d", res.Generation)
			}
			a.outf("). Check it with: agent-run keeper-login %s status\n", provider)
		}
		return nil
	}
}

func (a *app) keeperLoginCancel(ctx context.Context, c *conn, path, provider string) error {
	if _, _, err := a.keeperLoginPost(ctx, c, path, nil, nil, http.MethodDelete); err != nil {
		return err
	}
	a.outf("%s: no keeper login ceremony is open on the operator now.\n", provider)
	return nil
}

// keeperLoginPost sends one write to a ceremony route, to the operator's leader.
// A follower answers 503 not_leader having done nothing, so the call reconnects
// and sends again; any other answer, and any transport failure after the
// request may have left, is final: a one-time code is never sent twice. The
// answers that carry a result body (202 Waiting, 409 and 422) decode into out
// and come back as status codes the caller reads; the rest are refusals.
func (a *app) keeperLoginPost(ctx context.Context, c *conn, path string, body, out any, method ...string) (int, []byte, error) {
	verb := http.MethodPost
	if len(method) > 0 {
		verb = method[0]
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return 0, nil, err
		}
	}
	long := *c
	hc := *c.http
	hc.Timeout = keeperLoginTimeout
	long.http = &hc
	for attempt := 0; ; attempt++ {
		status, raw, err := a.send(ctx, &long, verb, c.url+path, payload)
		if err != nil {
			if ctx.Err() == nil && nothingSent(err) {
				return 0, nil, fail(ExitRetry, "could not reach the API at %s: %v; nothing was sent", c.url, err)
			}
			hint := "run it again, or abandon with agent-run keeper-login cancel"
			if strings.HasSuffix(path, "/code") {
				hint = "A code may have reached the keeper: do not paste it again; check agent-run keeper-login status"
			}
			return 0, nil, fail(ExitUnconfirmed, "no confirmed answer to %s %s: %v; agent-run sent it once and did not repeat it. %s", verb, path, err, hint)
		}
		var doc apiv1.ErrorResponse
		isErr := json.Unmarshal(raw, &doc) == nil && doc.Error.Code != ""
		if isErr && doc.Error.Code == apiv1.CodeNotLeader && attempt < keeperLoginReconnects {
			// A new connection may land on the leader.
			c.http.CloseIdleConnections()
			if a.env.Sleep(ctx, 300*time.Millisecond) != nil {
				return 0, nil, fail(ExitFailed, "stopped")
			}
			continue
		}
		switch {
		case status/100 == 2 && !isErr:
			if out != nil && len(raw) > 0 {
				if err := json.Unmarshal(raw, out); err != nil {
					return status, raw, fail(ExitUnconfirmed, "the API answered %s %s with a body that is not the expected JSON; agent-run did not repeat it", verb, path)
				}
			}
			return status, raw, nil
		case !isErr && (status == http.StatusConflict || status == http.StatusUnprocessableEntity):
			var res apiv1.KeeperLoginResult
			if json.Unmarshal(raw, &res) == nil && res.Result != "" {
				return status, raw, fail(ExitFailed, "the keeper did not adopt a login: %s%s", res.Result, keeperResultHint(res.Result))
			}
		}
		return status, raw, refusal(c, status, raw)
	}
}

func keeperResultHint(result string) string {
	switch result {
	case "NeedsLogin":
		return " (the code was wrong, expired or already used: abandon with keeper-login cancel and start again with url)"
	case "AccountMismatch":
		return " (the new login is for a different account than the keeper's current one)"
	case "Busy":
		return " (the keeper is refreshing right now: try again in a minute)"
	}
	return ""
}
