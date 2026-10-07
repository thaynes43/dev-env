package agentrun

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/version"
)

// Retries. A request that could not be sent, or that the API answered 502, 503
// or 504, is tried again after 1 s and then 2 s. Every route plan 01 calls is
// safe to repeat: GETs read, a DELETE of a session already being reaped
// answers 202 again, and a create carries an idempotency key, so a retry of
// one whose answer was lost gets the same session back (V-03, D-46).
var retryDelays = []time.Duration{time.Second, 2 * time.Second}

// maxResponse bounds what agent-run reads of an answer.
const maxResponse = 8 << 20

// call sends one request to the API and decodes a 2xx answer into out. It
// returns the status code, or an *exitError that says what went wrong in
// words, with the exit code for its kind.
func (a *app) call(ctx context.Context, c *conn, method, path string, query url.Values, body, out any) (int, error) {
	return a.callWith(ctx, c, method, path, query, body, out, true)
}

// callOnce is call without retries, for a request that must not be sent
// twice: a message (D-65) may have reached its session even when the answer
// is lost.
func (a *app) callOnce(ctx context.Context, c *conn, method, path string, body, out any) (int, error) {
	return a.callWith(ctx, c, method, path, nil, body, out, false)
}

func (a *app) callWith(ctx context.Context, c *conn, method, path string, query url.Values, body, out any, retry bool) (int, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return 0, err
		}
	}
	target := c.url + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	for attempt := 0; ; attempt++ {
		status, raw, err := a.send(ctx, c, method, target, payload)
		var ee *exitError
		if errors.As(err, &ee) {
			// A token that could not be read or minted: retrying changes nothing.
			return 0, err
		}
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return 0, fail(ExitAuth, "the API at %s has a certificate agent-run does not trust: %v; give its CA with --ca-file or %s", c.url, certErr.Err, envCAFile)
		}
		retryable := err != nil || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
		if retry && retryable && attempt < len(retryDelays) && ctx.Err() == nil && a.env.Sleep(ctx, retryDelays[attempt]) == nil {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return 0, fail(ExitFailed, "%s %s: stopped: %v", method, path, ctx.Err())
			}
			return 0, fail(ExitRetry, "could not reach the API at %s: %v", c.url, err)
		}
		if status/100 != 2 {
			return status, refusal(c, status, raw)
		}
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return status, fail(ExitFailed, "the API answered %s %s with a body that is not the expected JSON: %v", method, path, err)
			}
		}
		return status, nil
	}
}

// send makes one attempt. A transport failure comes back as a plain error;
// a failure to get a token as an *exitError.
func (a *app) send(ctx context.Context, c *conn, method, target string, payload []byte) (int, []byte, error) {
	tok, err := c.token.Token(ctx)
	if err != nil {
		return 0, nil, err
	}
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return 0, nil, fail(ExitUsage, "%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", BinaryName+"/"+version.Get().Version)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

// flagOf names a request field by the flag that sets it, so a refusal points
// at what the caller typed.
var flagOf = map[string]string{
	"repo":            "--repo",
	"base":            "--base",
	"agent":           "--agent",
	"model":           "--model",
	"effort":          "--effort",
	"prompt":          "-p",
	"size":            "--size",
	"profile":         "--profile",
	"limits.timeout":  "--timeout",
	"limits.maxTurns": "--max-turns",
	"idempotencyKey":  "--idempotency-key",
}

// refusal turns an API error into a message and an exit code (D-46's codes).
func refusal(c *conn, status int, raw []byte) error {
	var doc apiv1.ErrorResponse
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Error.Code == "" {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		code := ExitFailed
		switch {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			code = ExitAuth
		case status == http.StatusNotFound:
			code = ExitNotFound
		case status == http.StatusTooManyRequests || status >= 500:
			code = ExitRetry
		}
		return fail(code, "the API answered %d %s, not an API error: %s", status, http.StatusText(status), firstOf(snippet, "(no body)"))
	}
	e := doc.Error
	tag := fmt.Sprintf("(%d %s)", status, e.Code)
	switch status {
	case http.StatusUnauthorized:
		return fail(ExitAuth, "the API did not accept the token %s: %s\nagent-run sent %s; it must be a ServiceAccount token for the audience %s",
			tag, e.Message, c.token.Describe(), apiv1.TokenAudience)
	case http.StatusForbidden:
		return fail(ExitAuth, "not allowed %s: %s", tag, e.Message)
	case http.StatusNotFound:
		return fail(ExitNotFound, "%s %s", e.Message, tag)
	case http.StatusTooManyRequests:
		return fail(ExitRetry, "%s %s", e.Message, tag)
	case http.StatusServiceUnavailable:
		return fail(ExitRetry, "the operator is unavailable %s: %s; try again shortly", tag, e.Message)
	case http.StatusInternalServerError:
		return fail(ExitFailed, "the operator failed %s: %s", tag, e.Message)
	}
	if len(e.Fields) == 0 {
		return fail(ExitFailed, "the API refused the request %s: %s", tag, e.Message)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "the API refused the request %s:", tag)
	for _, f := range e.Fields {
		name := f.Field
		if fl, ok := flagOf[f.Field]; ok {
			name = fl
		}
		if name == "" {
			fmt.Fprintf(&b, "\n  %s", f.Message)
		} else {
			fmt.Fprintf(&b, "\n  %s: %s", name, f.Message)
		}
	}
	return fail(ExitFailed, "%s", b.String())
}
