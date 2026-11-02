package session

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

func TestAggregateAllowanceTransport(t *testing.T) {
	now := time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC)
	root := t.TempDir()
	hits := 0
	secret := "PORTAL-AGGREGATE-SECRET"
	client := &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
		hits++
		if r.Method != http.MethodGet || r.URL.Path != aggregateAllowancePath || r.URL.RawQuery != "" {
			t.Fatalf("request %s %s", r.Method, r.URL.RequestURI())
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{mediaJSON}}, Body: io.NopCloser(strings.NewReader(aggregateTransportDocument())), Request: r}, nil
	})}
	sessions := &SessionSource{path: filepath.Join(root, "token"), client: client, now: func() time.Time { return now }}
	if err := os.WriteFile(sessions.path, []byte(fixtureOperatorToken(apiv1.TokenAudience, now.Add(time.Hour))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := attachAggregateAllowanceConfig(root, sessions); err != nil || sessions.aggregateAllowance != nil || hits != 0 {
		t.Fatal("absent config", err, hits)
	}
	if err := os.Mkdir(filepath.Join(root, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, aggregateAllowanceConfigRel)
	if err := os.WriteFile(bad, []byte(`{"route":"/v1/provider-allowances","note":"`+secret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := attachAggregateAllowanceConfig(root, sessions); !errors.Is(err, errProductionConfig) || strings.Contains(err.Error(), secret) || hits != 0 {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(strings.Replace(aggregateTransportConfig(), `"method":"GET"`, `"method":"PUT"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := attachAggregateAllowanceConfig(root, sessions); !errors.Is(err, errProductionConfig) || hits != 0 {
		t.Fatal("put method", err)
	}
	if err := os.WriteFile(bad, []byte(strings.Replace(aggregateTransportConfig(), aggregateAllowancePath, "/v1/inbox", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := attachAggregateAllowanceConfig(root, sessions); !errors.Is(err, errProductionConfig) || hits != 0 {
		t.Fatal("route", err)
	}
	sessions.currentSource = &currentSourceConnection{}
	if err := os.WriteFile(bad, []byte(aggregateTransportConfig()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := attachAggregateAllowanceConfig(root, sessions); !errors.Is(err, errProductionConfig) || sessions.aggregateAllowance != nil || hits != 0 {
		t.Fatal("conflict", err)
	}
	sessions.currentSource = nil
	if err := attachAggregateAllowanceConfig(root, sessions); err != nil || sessions.aggregateAllowance == nil || sessions.allowance != nil {
		t.Fatal("attach", err)
	}
	page := sessions.projectAuthorizedAggregateAllowance(context.Background(), portal.ProductPage{})
	if hits != 1 || len(page.Allowance) != 3 {
		t.Fatalf("hits %d rows %d", hits, len(page.Allowance))
	}
	for _, row := range page.Allowance {
		if row.Value != portal.AllowanceNotConnected || len(row.Windows) != 0 || strings.Contains(row.Reason, "0%") || strings.Contains(row.Reason, secret) {
			t.Fatalf("row %+v", row)
		}
	}
	redirects := 0
	sessions.aggregateAllowance.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
		redirects++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://example.invalid/next"}, "Content-Type": []string{mediaJSON}}, Body: io.NopCloser(strings.NewReader(secret)), Request: r}, nil
	})}
	failed := sessions.projectAuthorizedAggregateAllowance(context.Background(), portal.ProductPage{})
	if redirects != 1 {
		t.Fatal(redirects)
	}
	for _, row := range failed.Allowance {
		if row.Value != portal.AllowanceUnknown || strings.Contains(row.Reason, secret) || strings.Contains(row.Observed, secret) {
			t.Fatalf("redirect row %+v", row)
		}
	}
	sessions.currentSource = &currentSourceConnection{}
	before := redirects
	unchanged := sessions.projectAuthorizedAggregateAllowance(context.Background(), portal.ProductPage{Allowance: page.Allowance})
	if redirects != before || len(unchanged.Allowance) != len(page.Allowance) {
		t.Fatal("current source was overwritten")
	}
}

func aggregateTransportConfig() string {
	return `{
		"route":"/v1/provider-allowances",
		"method":"GET",
		"maxAgeSeconds":1800,
		"contexts":[{"provider":"openai","producerRef":"fixture-producer","producerIncarnation":"fixture-incarnation-1","bindingGeneration":0,"accountRef":"fixture-account","workspaceRef":"fixture-workspace","bindingState":"verified","sourceKind":"native_rpc","sourceVersion":"fixture-v1"}],
		"registry":{"sources":[{"provider":"openai","kind":"native_rpc","version":"fixture-v1","capabilityRef":"synthetic:not-live","allowNormalizedMeasures":true,"allowDerivations":true,"directFields":{}}],"metrics":[{"validationRef":"fixture:verified-field-v1","provider":"openai","sourceKind":"native_rpc","sourceVersion":"fixture-v1","category":"included_quota","unit":"percent","direction":"used","windows":["five-hour-window"],"currency":null,"min":"0","max":"100","allowOverLimit":false,"allowUncapped":false,"complementOf":"","historicalOnly":false,"modelScope":""}],"accounts":[{"accountRef":"fixture-account","workspaceRef":"fixture-workspace","generation":0,"producerRef":"fixture-producer","producerIncarnation":"fixture-incarnation-1"}],"entitlements":[],"projections":[]}
	}`
}

func aggregateTransportDocument() string {
	return `{"schema":"providerallowance.read.v1","snapshotRevision":"aggregate-unconfigured","servedAt":"1970-01-01T00:00:00Z","items":[],"sourceStatuses":[` +
		`{"provider":"cursor","producerRef":"aggregate-unconfigured","availability":"unconfigured","lastGood":null,"lastAttempt":{"attemptedAt":"1970-01-01T00:00:00Z","completedAt":"1970-01-01T00:00:00Z","result":"unknown","code":"source_unconfigured"},"freshness":{"state":"unknown","asOf":"1970-01-01T00:00:00Z","basis":"none","policyRef":null,"reason":"unconfigured"},"validation":{"state":"unverified","validationRefs":[]},"binding":{"state":"unknown","generation":0,"accountRef":null,"workspaceRef":null}},` +
		`{"provider":"anthropic","producerRef":"aggregate-unconfigured","availability":"unconfigured","lastGood":null,"lastAttempt":{"attemptedAt":"1970-01-01T00:00:00Z","completedAt":"1970-01-01T00:00:00Z","result":"unknown","code":"source_unconfigured"},"freshness":{"state":"unknown","asOf":"1970-01-01T00:00:00Z","basis":"none","policyRef":null,"reason":"unconfigured"},"validation":{"state":"unverified","validationRefs":[]},"binding":{"state":"unknown","generation":0,"accountRef":null,"workspaceRef":null}},` +
		`{"provider":"openai","producerRef":"aggregate-unconfigured","availability":"unconfigured","lastGood":null,"lastAttempt":{"attemptedAt":"1970-01-01T00:00:00Z","completedAt":"1970-01-01T00:00:00Z","result":"unknown","code":"source_unconfigured"},"freshness":{"state":"unknown","asOf":"1970-01-01T00:00:00Z","basis":"none","policyRef":null,"reason":"unconfigured"},"validation":{"state":"unverified","validationRefs":[]},"binding":{"state":"unknown","generation":0,"accountRef":null,"workspaceRef":null}}],"nextCursor":null}`
}
