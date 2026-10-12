package agentrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// padPastLimit returns raw, spaces up to the response limit, then a second
// JSON value: a body whose truncated prefix alone is valid JSON (history-05).
func padPastLimit(raw []byte) []byte {
	reply := append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), maxResponse-len(raw))...)
	return append(reply, []byte(`{}`)...)
}

// history-05: canary create gets show --once's body fence and strict wire walk.
func TestCanaryCreateRefusesPaddedPrefixAndAmbiguousReceipt(t *testing.T) {
	raw := []byte(mustJSON(t, originalCanary(claudeCanaryID)))
	cases := map[string][]byte{
		"padded-prefix":    padPastLimit(raw),
		"duplicate-uid":    bytes.Replace(raw, []byte(`"uid":"original-session-uid"`), []byte(`"uid":"foreign-uid","uid":"original-session-uid"`), 1),
		"case-alias":       bytes.Replace(raw, []byte(`"uid":`), []byte(`"UID":`), 1),
		"null-scalar":      []byte(strings.TrimSuffix(string(raw), "}") + `,"phase":null}`),
		"tolerant-usage":   []byte(strings.TrimSuffix(string(raw), "}") + `,"usageObservation":{"extra":true}}`),
		"nested-duplicate": []byte(strings.TrimSuffix(string(raw), "}") + `,"limits":{"maxTurns":1,"maxTurns":2}}`),
	}
	// Each ambiguous case passes the typed decoder alone, so only the walk can
	// refuse it.
	for name, reply := range cases {
		if name == "padded-prefix" {
			continue
		}
		var s apiv1.Session
		d := json.NewDecoder(bytes.NewReader(reply))
		d.DisallowUnknownFields()
		if err := d.Decode(&s); err != nil || !confirmedCanaryReceipt(claudeCanaryID, s) {
			t.Fatalf("%s: fixture does not reach the typed-decoder trap: %v", name, err)
		}
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(reply)
			}
			h.mustRun(ExitUnconfirmed, "canary", "create", "--id", claudeCanaryID, "--once", "--json")
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 || !strings.Contains(h.stderr.String(), "do not repeat") {
				t.Fatal("ambiguous receipt accepted, repeated or not held", h.api.requests(), h.sleeps, h.stdout.String(), h.stderr.String())
			}
		})
	}
}

func directoryPageFixture() apiv1.SessionDirectoryList {
	heartbeat := apiv1.DirectoryReportedTime{Time: testNow}
	finished := testNow.Add(time.Minute)
	return apiv1.SessionDirectoryList{
		Items: []apiv1.SessionDirectory{{
			Name: "session-directory.original", UID: "directory-uid", ResourceVersion: "directory-rv",
			Original: apiv1.DirectoryOriginal{Version: 1, Project: "dev-env", TaskKey: "canary:rule-canary-codex-1011", Source: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "original-session", UID: "original-uid"}},
			Retained: apiv1.DirectoryRetained{SourceState: "Unknown", StopState: "Unknown", ObservedAt: &finished, LastWitness: &apiv1.DirectoryWitness{
				SourceGeneration: 1, ReportedHeartbeatAt: &heartbeat, ReportedTaskFinishedAt: &apiv1.DirectoryReportedTime{},
				ReportedTask: &apiv1.DirectoryTaskReport{ExitCode: 1, FinishedAt: &finished, Subtype: "error"},
			}},
			Current: apiv1.DirectoryCurrent{SourceState: "SourceMissing", PlacementState: "Unknown", ObservedAt: testNow, StopState: "Unknown"},
		}},
		ResourceVersion: "list-rv", Continue: "next-page",
	}
}

// history-05: directory list gets the same fence and walk. Its custom
// DirectoryReportedTime (null or RFC 3339) still passes.
func TestDirectoryListRefusesPaddedPrefixAndAmbiguousPage(t *testing.T) {
	raw := []byte(mustJSON(t, directoryPageFixture()))
	if !bytes.Contains(raw, []byte(`"reportedHeartbeatAt":"`)) || !bytes.Contains(raw, []byte(`"reportedTaskFinishedAt":null`)) {
		t.Fatal("fixture lacks both reported-time forms", string(raw))
	}
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write(raw) }
	h.mustRun(ExitOK, "directory", "list", "--project", "dev-env", "--once", "--json")
	cases := map[string][]byte{
		"padded-prefix":     padPastLimit(raw),
		"duplicate-project": bytes.Replace(raw, []byte(`"project":"dev-env"`), []byte(`"project":"foreign","project":"dev-env"`), 1),
		"duplicate-list-rv": bytes.Replace(raw, []byte(`"resourceVersion":"list-rv"`), []byte(`"resourceVersion":"foreign","resourceVersion":"list-rv"`), 1),
		"case-alias":        bytes.Replace(raw, []byte(`"items":`), []byte(`"ITEMS":`), 1),
		"null-scalar":       bytes.Replace(raw, []byte(`"taskKey":"canary:rule-canary-codex-1011"`), []byte(`"taskKey":null`), 1),
		"reported-time":     bytes.Replace(raw, []byte(`"reportedHeartbeatAt":"`), []byte(`"reportedHeartbeatAt":"x`), 1),
		"reported-object":   bytes.Replace(raw, []byte(`"reportedTaskFinishedAt":null`), []byte(`"reportedTaskFinishedAt":{}`), 1),
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write(reply) }
			h.mustRun(ExitFailed, "directory", "list", "--project", "dev-env", "--once", "--json")
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 {
				t.Fatal("ambiguous page accepted or repeated", h.api.requests(), h.sleeps, h.stdout.String())
			}
		})
	}
}

// history-06: a one-shot request that was sent never exits 5 or advises a
// retry. A request that may have taken effect says so and says not to repeat
// it. Only a connection that was never made exits 5: nothing was sent.
func TestOnceOnlyUncertainOutcomeNeverAdvisesRetry(t *testing.T) {
	result := originalCanaryResult(claudeCanaryID)
	commands := []struct {
		name  string
		args  []string
		write bool
	}{
		{"canary-create", []string{"canary", "create", "--id", claudeCanaryID, "--once", "--json"}, true},
		{"msg", []string{"msg", "original-session", "synthetic message"}, true},
		{"canary-result", canaryResultArgs(result), false},
		{"show-once", []string{"show", "original-private-session", "--json", "--once"}, false},
		{"directory", []string{"directory", "list", "--project", "dev-env", "--once", "--json"}, false},
	}
	failures := map[string]func(http.ResponseWriter){
		"transport": func(w http.ResponseWriter) {
			c, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = c.Close()
			}
		},
		"500":       func(w http.ResponseWriter) { apiError(w, 500, apiv1.CodeInternal, "synthetic failure") },
		"502":       func(w http.ResponseWriter) { apiError(w, 502, apiv1.CodeUnavailable, "synthetic gateway") },
		"503":       func(w http.ResponseWriter) { apiError(w, 503, apiv1.CodeUnavailable, "synthetic unavailable") },
		"504":       func(w http.ResponseWriter) { apiError(w, 504, apiv1.CodeUnavailable, "synthetic timeout") },
		"503-plain": func(w http.ResponseWriter) { w.WriteHeader(503); _, _ = w.Write([]byte("upstream down")) },
	}
	for _, cmd := range commands {
		for failure, write := range failures {
			t.Run(cmd.name+"/"+failure, func(t *testing.T) {
				h := newHarness(t)
				h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { write(w) }
				h.mustRun(ExitUnconfirmed, cmd.args...)
				stderr := h.stderr.String()
				if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 || strings.Contains(stderr, "try again") || !strings.Contains(stderr, "did not repeat") {
					t.Fatal("one-shot request repeated or advised a retry", h.api.requests(), h.sleeps, stderr)
				}
				if strings.Contains(stderr, "do not repeat") != cmd.write {
					t.Fatal("may-have-taken-effect warning does not match the request kind", stderr)
				}
			})
		}
		t.Run(cmd.name+"/429", func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				apiError(w, http.StatusTooManyRequests, apiv1.CodeLimitExceeded, "synthetic limit")
			}
			h.mustRun(ExitFailed, cmd.args...)
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || strings.Contains(h.stderr.String(), "try again") {
				t.Fatal("refused one-shot request repeated or advised a retry", h.api.requests(), h.sleeps, h.stderr.String())
			}
		})
		t.Run(cmd.name+"/not-sent", func(t *testing.T) {
			h := newHarness(t)
			h.api.srv.Close()
			h.mustRun(ExitRetry, cmd.args...)
			stderr := h.stderr.String()
			if len(h.api.requests()) != 0 || len(h.sleeps) != 0 || h.stdout.Len() != 0 || !strings.Contains(stderr, "nothing was sent") || strings.Contains(stderr, "do not repeat") {
				t.Fatal("a request that never left agent-run was repeated or reported as possibly delivered", h.api.requests(), h.sleeps, stderr)
			}
		})
	}
}

// A message whose acknowledgement cannot be decoded may still have arrived.
func TestMsgUnreadableAcknowledgementIsUnconfirmed(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}
	h.mustRun(ExitUnconfirmed, "msg", "original-session", "synthetic message")
	if len(h.api.requests()) != 1 || !strings.Contains(h.stderr.String(), "do not repeat") {
		t.Fatal("unreadable message acknowledgement repeated or not held", h.api.requests(), h.stderr.String())
	}
}

// Only an error that proves the connection was never made counts as nothing
// sent; anything after the dial may follow a request the API received.
func TestNothingSentOnlyForDialFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused", &url.Error{Op: "Post", URL: "https://api", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}, true},
		{"dns", &url.Error{Op: "Post", URL: "https://api", Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "api"}}}, true},
		{"bare-dns", &net.DNSError{Err: "no such host", Name: "api"}, true},
		{"read", &url.Error{Op: "Post", URL: "https://api", Err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}}, false},
		{"eof", &url.Error{Op: "Post", URL: "https://api", Err: io.EOF}, false},
		{"timeout", &url.Error{Op: "Post", URL: "https://api", Err: errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")}, false},
	} {
		if got := nothingSent(tc.err); got != tc.want {
			t.Errorf("%s: nothingSent = %v, want %v", tc.name, got, tc.want)
		}
	}
}
