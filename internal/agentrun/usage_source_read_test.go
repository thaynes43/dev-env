package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func usageSourceReadArgs(extra ...string) []string {
	args := []string{"--read-current", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"}
	return append(args, extra...)
}

func syntheticReadReport() providerallowance.CurrentSourceReport {
	return providerallowance.CurrentSourceReport{
		Schema:                providerallowance.CurrentSourceSchema,
		PublisherRef:          providerallowance.CurrentSourcePublisher,
		ProducerIncarnation:   "synthetic-codex-1",
		SourceGeneration:      1,
		RetrievedAt:           "2026-01-08T00:00:00Z",
		UsedPercent:           "40",
		RemainingPercent:      "60",
		RemainingPercentBasis: providerallowance.CurrentSourceRemainingBasis,
		WindowDurationMinutes: 10080,
		ResetsAt:              "2026-01-15T00:00:00Z",
	}
}

func trapUsageSourceClient(t *testing.T, h *harness) {
	t.Helper()
	h.env.Getenv = func(string) string {
		t.Error("retained read inspected client configuration")
		panic("unexpected client configuration read")
	}
	h.env.Stdin = failReader{}
}

func TestUsageSourceReadCurrentCanonicalGET(t *testing.T) {
	h := newHarness(t)
	h.now = usageSourceNow()
	h.env.Stdin = failReader{}
	want := syntheticReadReport()
	canonical, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	wire := bytes.ReplaceAll(canonical, []byte(`,"`), []byte(`, "`))
	if bytes.Equal(wire, canonical) {
		t.Fatal("synthetic wire did not differ from canonical JSON")
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "" || len(body) != 0 {
			t.Errorf("GET sent content-type %q or %d body bytes", r.Header.Get("Content-Type"), len(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append(append([]byte{}, wire...), '\n'))
	}
	h.mustRun(ExitOK, append([]string{"usage-source"}, usageSourceReadArgs()...)...)
	reqs := h.api.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodGet || reqs[0].path != usageSourcePublishPath || len(reqs[0].query) != 0 || reqs[0].auth != "Bearer "+testToken || len(reqs[0].body) != 0 {
		t.Fatal("retained read must send one authenticated fixed-path GET and no body")
	}
	for _, req := range reqs {
		if req.method == http.MethodPut {
			t.Fatal("retained read issued a PUT")
		}
	}
	if !bytes.Equal(h.stdout.Bytes(), append(canonical, '\n')) || h.stderr.Len() != 0 || len(h.sleeps) != 0 || len(h.ran) != 0 {
		t.Fatalf("stdout %s stderr %s", h.stdout.String(), h.stderr.String())
	}
	got, err := providerallowance.DecodeCurrentSource(bytes.TrimSuffix(h.stdout.Bytes(), []byte("\n")))
	if err != nil || got != want || got.ProviderUpdatedAt != nil {
		t.Fatalf("%#v %v", got, err)
	}
	for _, leak := range []string{", \"", "accountIdentity", "freshness", "SECRET"} {
		if strings.Contains(h.stdout.String(), leak) {
			t.Fatalf("stdout contains %s", leak)
		}
	}
}

func TestUsageSourceReadCurrentUnknownStaleAndReset(t *testing.T) {
	h := newHarness(t)
	h.now = usageSourceNow()
	h.env.Stdin = failReader{}
	for _, body := range []string{usageSourceUnknownDocument, usageSourceUnknownDocument + "\n"} {
		h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, body)
		}
		h.mustRun(ExitOK, append([]string{"usage-source"}, usageSourceReadArgs()...)...)
		if h.stdout.String() != usageSourceUnknownLine || h.stderr.Len() != 0 {
			t.Fatalf("unknown output %q %q", h.stdout.String(), h.stderr.String())
		}
		for _, leak := range []string{"usedPercent", "remainingPercent", "retrievedAt", "resetsAt", "freshness", "40", "providerUpdatedAt"} {
			if strings.Contains(h.stdout.String(), leak) {
				t.Fatalf("unknown output invented %s", leak)
			}
		}
	}
	if len(h.api.requests()) != 2 || len(h.sleeps) != 0 || len(h.ran) != 0 {
		t.Fatal("unknown envelope must be one GET each and must not retry")
	}
	for _, req := range h.api.requests() {
		if req.method != http.MethodGet || req.path != usageSourcePublishPath {
			t.Fatal("unknown envelope used the wrong request")
		}
	}

	stale := syntheticReadReport()
	stale.RetrievedAt = "2026-01-07T00:00:00Z"
	reset := syntheticReadReport()
	reset.ResetsAt = "2026-01-08T00:04:00Z"
	for _, report := range []providerallowance.CurrentSourceReport{stale, reset} {
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(append(append([]byte{}, raw...), '\n'))
		}
		before := len(h.api.requests())
		h.mustRun(ExitFailed, append([]string{"usage-source"}, usageSourceReadArgs()...)...)
		if got := len(h.api.requests()); got != before+1 || h.api.requests()[got-1].method != http.MethodGet {
			t.Fatalf("stale or reset made %d requests", got-before)
		}
		if h.stdout.Len() != 0 || h.stderr.String() != "agent-run: usage-source read unavailable\n" {
			t.Fatalf("stdout %q stderr %q", h.stdout.String(), h.stderr.String())
		}
		for _, leak := range []string{"40", "2026-01-07", "2026-01-08T00:04:00Z", "usedPercent"} {
			if strings.Contains(h.stderr.String(), leak) {
				t.Fatalf("stderr echoed %s", leak)
			}
		}
	}
}

func TestUsageSourceReadCurrentRejectsBody(t *testing.T) {
	h := newHarness(t)
	h.now = usageSourceNow()
	h.env.Stdin = failReader{}
	stamp := "2026-01-08T00:00:00Z"
	withProvider := syntheticReadReport()
	withProvider.ProviderUpdatedAt = &stamp
	providerBody, err := json.Marshal(withProvider)
	if err != nil {
		t.Fatal(err)
	}
	mismatch := syntheticReadReport()
	mismatch.ProducerIncarnation = "other-incarnation"
	mismatchBody, err := json.Marshal(mismatch)
	if err != nil {
		t.Fatal(err)
	}
	generation := syntheticReadReport()
	generation.SourceGeneration = 2
	generationBody, err := json.Marshal(generation)
	if err != nil {
		t.Fatal(err)
	}
	cases := [][]byte{
		[]byte(`{"availability":"Unknown","usedPercent":"40"}`),
		[]byte("{\"availability\":\"Unknown\",\"availability\":\"Unknown\"}"),
		[]byte(`{"availability":"unknown"}`),
		[]byte(usageSourceUnknownDocument + " "),
		[]byte(usageSourceUnknownDocument + "\n\n"),
		[]byte(usageSourceUnknownDocument + `{"usedPercent":"40"}`),
		[]byte("{"),
		[]byte("SECRET-MALFORMED"),
		bytes.Repeat([]byte("x"), usageSourceMaxBytes+1),
		providerBody,
		mismatchBody,
		generationBody,
	}
	for _, body := range cases {
		h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		}
		before := len(h.api.requests())
		h.mustRun(ExitFailed, append([]string{"usage-source"}, usageSourceReadArgs()...)...)
		reqs := h.api.requests()
		if len(reqs) != before+1 || reqs[len(reqs)-1].method != http.MethodGet || h.stdout.Len() != 0 || h.stderr.String() != "agent-run: usage-source read unavailable\n" || len(h.sleeps) != 0 || len(h.ran) != 0 {
			t.Fatalf("requests %d stdout %q stderr %q", len(reqs), h.stdout.String(), h.stderr.String())
		}
		for _, leak := range []string{"SECRET-MALFORMED", "other-incarnation", "usedPercent", "40", stamp, "providerUpdatedAt"} {
			if strings.Contains(h.stderr.String(), leak) || strings.Contains(h.stdout.String(), leak) {
				t.Fatalf("output echoed %s", leak)
			}
		}
	}
}

func TestUsageSourceReadCurrentHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   int
		msg    string
	}{
		{http.StatusUnauthorized, ExitAuth, "refused"},
		{http.StatusForbidden, ExitAuth, "refused"},
		{http.StatusNotFound, ExitNotFound, "unavailable"},
		{http.StatusConflict, ExitFailed, "conflict"},
		{http.StatusBadRequest, ExitFailed, "unavailable"},
		{http.StatusInternalServerError, ExitFailed, "unavailable"},
		{http.StatusNoContent, ExitFailed, "unavailable"},
		{http.StatusMovedPermanently, ExitFailed, "refused"},
		{http.StatusFound, ExitFailed, "refused"},
		{http.StatusSeeOther, ExitFailed, "refused"},
		{http.StatusTemporaryRedirect, ExitFailed, "refused"},
		{http.StatusPermanentRedirect, ExitFailed, "refused"},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			h := newHarness(t)
			h.now = usageSourceNow()
			h.env.Stdin = failReader{}
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.Header().Set("Location", h.api.srv.URL+"/SECRET-REDIRECT-TARGET")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, "SECRET-RESPONSE-BODY "+testToken)
			}
			h.mustRun(tc.code, append([]string{"usage-source"}, usageSourceReadArgs()...)...)
			reqs := h.api.requests()
			if len(reqs) != 1 || reqs[0].method != http.MethodGet || reqs[0].path != usageSourcePublishPath || len(h.sleeps) != 0 || len(h.ran) != 0 {
				t.Fatal("error or redirect must be one GET, without a retry or a followed Location")
			}
			if h.stdout.Len() != 0 || h.stderr.String() != "agent-run: usage-source read "+tc.msg+"\n" {
				t.Fatalf("stdout %q stderr %q", h.stdout.String(), h.stderr.String())
			}
			if strings.Contains(h.stderr.String(), "SECRET") || strings.Contains(h.stderr.String(), testToken) || strings.Contains(h.stdout.String(), "SECRET") {
				t.Fatal("error output echoed the response")
			}
		})
	}
}

func TestUsageSourceReadCurrentInputAndCancelDoNotConnect(t *testing.T) {
	usage := [][]string{
		{"--read-current", "--codex-tool-report", "-", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--codex-raw-tool-report", "-", "--observed-at", "2026-01-08T00:00:00Z", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--observed-at", "2026-01-08T00:00:00Z", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--publish", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--publish=false", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--read-current", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--incarnation", "synthetic-codex-1", "--incarnation", "other", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--incarnation", "synthetic-codex-1", "--generation", "1", "--generation", "2", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "--max-age", "1m", "-o", "json"},
		{"--read-current", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json", "-o", "json"},
		append(usageSourceReadArgs(), "extra"),
		{"--read-current=false", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "30m"},
	}
	for _, args := range usage {
		h := newHarness(t)
		h.now = usageSourceNow()
		trapUsageSourceClient(t, h)
		h.mustRun(ExitUsage, append([]string{"usage-source"}, args...)...)
		if len(h.api.requests()) != 0 || len(h.ran) != 0 || h.stdout.Len() != 0 || h.stderr.String() != "agent-run: "+usageSourceUsage+"\n" || strings.Contains(h.stderr.String(), "2026-01-08") || strings.Contains(h.stderr.String(), "other") {
			t.Fatalf("%v stderr %q", args, h.stderr.String())
		}
	}

	preflight := [][]string{
		{"--read-current", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "31m", "-o", "json"},
		{"--read-current", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "1801s", "-o", "json"},
		{"--read-current", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "24h", "-o", "json"},
		{"--read-current", "--incarnation", "synthetic-codex-1", "--generation", "1", "--max-age", "0s", "-o", "json"},
		{"--read-current", "--incarnation", "synthetic-codex-1", "--generation", "0", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--incarnation", "synthetic-codex-1", "--generation=-1", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--incarnation", "has space", "--generation", "1", "--max-age", "30m", "-o", "json"},
		{"--read-current", "--incarnation", strings.Repeat("a", 129), "--generation", "1", "--max-age", "30m", "-o", "json"},
	}
	for _, args := range preflight {
		h := newHarness(t)
		h.now = usageSourceNow()
		trapUsageSourceClient(t, h)
		h.mustRun(ExitFailed, append([]string{"usage-source"}, args...)...)
		if len(h.api.requests()) != 0 || len(h.ran) != 0 || len(h.sleeps) != 0 || h.stdout.Len() != 0 || h.stderr.String() != "agent-run: usage-source read unavailable\n" {
			t.Fatalf("%v stdout %q stderr %q", args, h.stdout.String(), h.stderr.String())
		}
		if strings.Contains(h.stderr.String(), "has space") || strings.Contains(h.stderr.String(), strings.Repeat("a", 40)) {
			t.Fatalf("stderr echoed selection %q", h.stderr.String())
		}
	}

	h := newHarness(t)
	h.now = usageSourceNow()
	trapUsageSourceClient(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code := Run(ctx, append([]string{"usage-source"}, usageSourceReadArgs()...), h.env)
	if code != ExitFailed || len(h.api.requests()) != 0 || len(h.ran) != 0 || h.stdout.Len() != 0 || h.stderr.String() != "agent-run: usage-source unavailable\n" {
		t.Fatalf("cancel code %d stdout %q stderr %q requests %d", code, h.stdout.String(), h.stderr.String(), len(h.api.requests()))
	}
}
