package agentrun

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func showOnceSession() apiv1.Session {
	s := testSession("original-private-session", "Suspended")
	s.UID = "original-session-uid"
	s.PrivateHistory = &apiv1.PrivateHistory{
		Version: 1, SessionUID: s.UID, HomeClaim: "original-home", HomeUID: "original-home-uid",
		BindingGeneration: 1, Admission: "Confirmed", Provider: s.Agent, StopState: "Unknown",
		ExecutorAttempted: true, ExecutorIntent: "original-intent",
		LastExecutor: &apiv1.PrivateExecutor{PodUID: "original-pod-uid", PodName: s.Name, Generation: 1},
	}
	s.Tools = []string{"synthetic-tool"}
	s.AgentStatus = &apiv1.AgentStatus{Problems: []string{"synthetic-problem"}, Task: &apiv1.TaskResult{ExitCode: 0, FinishedAt: testNow}}
	u := protocol.UnknownUsage()
	u.Source = protocol.UsageSource{SessionUID: s.UID, PodUID: "original-pod-uid", Provider: s.Agent, RequestedModel: s.Model, AttemptID: strings.Repeat("a", 64)}
	u.ID, u.ObservedAt, u.ResultState = protocol.UsageObservationID(u.Source), testNow, "Final"
	zero := int64(0)
	u.InputTokens = protocol.TokenObservation{State: "Reported", Amount: &zero}
	u.Cost = protocol.CostObservation{State: "Reported", Amount: "0.00000000000000000000000000000000000000000000000000000000001", Unit: "USD", Kind: protocol.UsageCLIEstimate}
	s.UsageObservation = &u
	return s
}

func showOnceRaw(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(showOnceSession())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestShowOnceNamedGETPreservesRawPresence(t *testing.T) {
	h := newHarness(t)
	s := showOnceSession()
	raw := showOnceRaw(t)
	raw = bytes.Replace(raw, []byte(`"tools":["synthetic-tool"]`), []byte(`"tools":null,"archivedAt":null,"caller":""`), 1)
	raw = bytes.Replace(raw, []byte(`"agentStatus":{`), []byte(`"agentStatus":{"lastActivity":null,`), 1)
	raw = bytes.Replace(raw, []byte(`"exitCode":0,`), []byte(`"exitCode":0,"timedOut":false,`), 1)
	raw = bytes.Replace(raw, []byte(`"cacheReadInputTokens":{"state":"Unknown"}`), []byte(`"cacheReadInputTokens":{"state":"Unknown","amount":null}`), 1)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write(raw) }
	h.mustRun(ExitOK, "show", s.Name, "--json", "--once")
	got := h.api.requests()
	if len(got) != 1 || got[0].method != http.MethodGet || got[0].path != apiv1.SessionPath(s.Name) || got[0].auth != "Bearer "+testToken || len(got[0].query) != 0 || len(got[0].body) != 0 || len(h.ran) != 0 || len(h.sleeps) != 0 {
		t.Fatal("named read changed authority or request count", got, h.ran, h.sleeps)
	}
	var want, printed bytes.Buffer
	if err := json.Compact(&want, raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&printed, h.stdout.Bytes()); err != nil || !bytes.Equal(want.Bytes(), printed.Bytes()) {
		t.Fatal("raw null/omission/zero/decimal fields changed", err, h.stdout.String())
	}
}

func TestShowOnceRejectsArgumentsBeforeConnection(t *testing.T) {
	cases := [][]string{
		{"show", "--json", "--once"},
		{"show", "one", "two", "--json", "--once"},
		{"show", "Bad", "--json", "--once"},
		{"show", "bad/name", "--json", "--once"},
		{"show", "bad.name", "--json", "--once"},
		{"show", strings.Repeat("a", 64), "--json", "--once"},
		{"show", "-bad", "--json", "--once"},
		{"show", "one", "--once"},
		{"show", "one", "-o", "json", "--once"},
		{"show", "one", "--json=false", "--once"},
		{"show", "one", "--json", "--once=false"},
		{"show", "one", "--json", "--once", "--once"},
		{"show", "one", "--json", "--json", "--once"},
		{"show", "one", "--json", "--once", "-o", "name"},
		{"show", "one", "--json", "--once", "--wait", "1s"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t)
			envReads := 0
			h.env.Getenv = func(k string) string { envReads++; return h.vars[k] }
			h.mustRun(ExitUsage, args...)
			if envReads != 0 || len(h.api.requests()) != 0 || len(h.ran) != 0 || h.stdout.Len() != 0 {
				t.Fatal("invalid selector reached connection/auth", envReads, h.api.requests(), h.ran)
			}
		})
	}
}

func TestShowOnceRedirectAndFailuresNeverRetry(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.Header().Set("Location", h.api.srv.URL+"/redirected")
				apiError(w, status, apiv1.CodeUnavailable, "synthetic refusal")
			}
			if h.run("show", "original-private-session", "--json", "--once") == ExitOK || len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 {
				t.Fatal("refusal was followed, retried or accepted", h.api.requests(), h.sleeps, h.stdout.String())
			}
		})
	}
	t.Run("transport", func(t *testing.T) {
		h := newHarness(t)
		h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			c, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = c.Close()
		}
		h.mustRun(ExitRetry, "show", "original-private-session", "--json", "--once")
		if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 {
			t.Fatal("uncertain transport retried", h.api.requests(), h.sleeps)
		}
	})
}

func TestShowOnceRequiresHTTP200AndExactName(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusNoContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHarness(t)
			s := showOnceSession()
			if status == http.StatusOK {
				s.Name = "foreign-session"
			}
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, status, s) }
			h.mustRun(ExitFailed, "show", "original-private-session", "--json", "--once")
			if h.stdout.Len() != 0 || len(h.api.requests()) != 1 || len(h.sleeps) != 0 {
				t.Fatal("uncertain identity accepted or retried", h.stdout.String(), h.api.requests())
			}
		})
	}
}

func TestShowOnceRejectsAmbiguousAndMalformedSessionJSON(t *testing.T) {
	raw := showOnceRaw(t)
	cases := map[string][]byte{
		"duplicate-name":      bytes.Replace(raw, []byte(`"name":`), []byte(`"name":"foreign","name":`), 1),
		"name-case-alias":     bytes.Replace(raw, []byte(`"name":`), []byte(`"NAME":`), 1),
		"duplicate-executor":  bytes.Replace(raw, []byte(`"podUID":"original-pod-uid"`), []byte(`"podUID":"foreign","podUID":"original-pod-uid"`), 1),
		"nested-case-alias":   bytes.Replace(raw, []byte(`"podUID":"original-pod-uid"`), []byte(`"PodUID":"original-pod-uid"`), 1),
		"unknown":             bytes.Replace(raw, []byte(`"privateHistory":{`), []byte(`"privateHistory":{"extra":true,`), 1),
		"null-name":           bytes.Replace(raw, []byte(`"name":"original-private-session"`), []byte(`"name":null`), 1),
		"null-bool":           bytes.Replace(raw, []byte(`"executorAttempted":true`), []byte(`"executorAttempted":null`), 1),
		"wrong-bool":          bytes.Replace(raw, []byte(`"executorAttempted":true`), []byte(`"executorAttempted":"true"`), 1),
		"null-time":           bytes.Replace(raw, []byte(`"createdAt":"2026-10-06T20:55:00Z"`), []byte(`"createdAt":null`), 1),
		"missing-field":       bytes.Replace(raw, []byte(`"mode":"task",`), nil, 1),
		"missing-nested":      bytes.Replace(raw, []byte(`"generation":1`), []byte(`"nodeName":"synthetic"`), 1),
		"null-array-element":  bytes.Replace(raw, []byte(`"tools":["synthetic-tool"]`), []byte(`"tools":[null]`), 1),
		"array-type":          bytes.Replace(raw, []byte(`"tools":["synthetic-tool"]`), []byte(`"tools":{}`), 1),
		"usage-unknown":       bytes.Replace(raw, []byte(`"usageObservation":{`), []byte(`"usageObservation":{"extra":true,`), 1),
		"usage-private-field": bytes.Replace(raw, []byte(`"usageObservation":{`), []byte(`"usageObservation":{"malformedDigest":"synthetic",`), 1),
		"usage-empty-tag":     bytes.Replace(raw, []byte(`"usageObservation":{`), []byte(`"usageObservation":{"":"synthetic",`), 1),
		"usage-case-alias":    bytes.Replace(raw, []byte(`"counterSemantics":`), []byte(`"COUNTERSEMANTICS":`), 1),
		"duplicate-amount":    bytes.Replace(raw, []byte(`"amount":0`), []byte(`"amount":1,"amount":0`), 1),
		"amount-wrong-type":   bytes.Replace(raw, []byte(`"amount":0`), []byte(`"amount":"0"`), 1),
		"amount-overflow":     bytes.Replace(raw, []byte(`"amount":0`), []byte(`"amount":9223372036854775808`), 1),
		"amount-fraction":     bytes.Replace(raw, []byte(`"amount":0`), []byte(`"amount":0.5`), 1),
		"usage-invalid-time":  bytes.Replace(raw, []byte(`"observedAt":"2026-10-06T21:00:00Z"`), []byte(`"observedAt":"not-time"`), 1),
		"cost-numeric":        bytes.Replace(raw, []byte(`"amount":"`+showOnceSession().UsageObservation.Cost.Amount+`"`), []byte(`"amount":0`), 1),
		"cost-null":           bytes.Replace(raw, []byte(`"amount":"`+showOnceSession().UsageObservation.Cost.Amount+`"`), []byte(`"amount":null`), 1),
		"nonfinite":           bytes.Replace(raw, []byte(`"amount":0`), []byte(`"amount":NaN`), 1),
		"second-document":     append(append([]byte{}, raw...), []byte(` {}`)...),
		"invalid-utf8":        bytes.Replace(raw, []byte("synthetic-tool"), []byte{0xff}, 1),
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write(reply) }
			h.mustRun(ExitFailed, "show", "original-private-session", "--json", "--once")
			if len(h.api.requests()) != 1 || h.stdout.Len() != 0 || len(h.sleeps) != 0 {
				t.Fatal("malformed response accepted or retried", h.api.requests(), h.stdout.String(), h.sleeps)
			}
		})
	}
}

func TestShowOnceDefeatsTolerantUsageDecoderAndBounds(t *testing.T) {
	raw := showOnceRaw(t)
	malformed := bytes.Replace(raw, []byte(`"amount":0`), []byte(`"amount":"zero"`), 1)
	var tolerated apiv1.Session
	if json.Unmarshal(malformed, &tolerated) != nil || tolerated.UsageObservation == nil || tolerated.UsageObservation.MalformedDigest() == "" {
		t.Fatal("fixture did not reach the tolerant nested usage decoder")
	}
	if strictSessionJSON(malformed) {
		t.Fatal("tolerated usage type error passed strict wire validation")
	}
	atLimit := append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), maxResponse-len(raw))...)
	if strictSessionJSON(atLimit) {
		t.Fatal("response at truncation bound accepted")
	}
}

func TestShowOnceRejectsPaddedTruncatedHTTPResponse(t *testing.T) {
	h := newHarness(t)
	raw := showOnceRaw(t)
	reply := append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), maxResponse-len(raw))...)
	reply = append(reply, []byte(`{}`)...)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write(reply) }
	h.mustRun(ExitFailed, "show", "original-private-session", "--json", "--once")
	requests := h.api.requests()
	if len(requests) != 1 || requests[0].method != http.MethodGet || requests[0].path != apiv1.SessionPath("original-private-session") || h.stdout.Len() != 0 || len(h.sleeps) != 0 || !strings.Contains(h.stderr.String(), "response exceeds") {
		t.Fatal("padded prefix accepted, retried or reached decoding", requests, h.stdout.String(), h.stderr.String(), h.sleeps)
	}
}

func TestShowOnceKeepsOrdinaryKubeContextSelector(t *testing.T) {
	h, l := laptopHarness(t, operatorDNS)
	s := showOnceSession()
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, s) }
	h.mustRun(ExitOK, "show", s.Name, "--json", "--once", "--context", "selected-context")
	if l.contexts != 0 || l.mints != 1 || len(h.api.requests()) != 1 {
		t.Fatal("ordinary explicit context changed", l.contexts, l.mints, h.api.requests())
	}
	for _, argv := range append(l.calls, l.starts...) {
		if !hasWord(argv, "selected-context") {
			t.Fatal("show consumed or dropped kube context", argv)
		}
	}
}
