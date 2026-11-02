package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/internal/apiserver"
)

func TestAggregateAllowanceConfigDefaultOff(t *testing.T) {
	bare, err := parseFlags(nil)
	if err != nil || bare.aggregateAllowance != nil || bare.providerSource != nil {
		t.Fatalf("default %#v %v", bare.aggregateAllowance, err)
	}
	off, err := aggregateAllowanceReports(nil, fake.NewClientBuilder().Build(), fake.NewClientBuilder().Build())
	if err != nil || off != nil {
		t.Fatalf("nil selection %#v %v", off, err)
	}
	canary := "SECRET-AGGREGATE-CONFIG"
	for _, args := range [][]string{
		{"--aggregate-allowance-config=" + aggregateAllowanceJSON(t)},
		{"--enable-aggregate-allowance-reports"},
		{"--enable-aggregate-allowance-reports", "--aggregate-allowance-config=" + `{"maxAgeSeconds":1800}`},
		{"--enable-aggregate-allowance-reports", "--aggregate-allowance-config=" + strings.TrimRight(aggregateAllowanceJSON(t), " \n\t}") + `,"note":"` + canary + `"}`},
		append([]string{"--enable-aggregate-allowance-reports", "--aggregate-allowance-config=" + aggregateAllowanceJSON(t)}, "--api-bind-address=0"),
		{"--enable-aggregate-allowance-reports", "--session-namespace=other", "--aggregate-allowance-config=" + aggregateAllowanceJSON(t)},
	} {
		_, flagErr := parseFlags(args)
		if flagErr == nil || strings.Contains(flagErr.Error(), canary) || strings.Contains(flagErr.Error(), "fixture-account") {
			t.Fatalf("%v accepted or echoed %v", args[0], flagErr)
		}
	}
	valid, err := parseFlags([]string{"--enable-aggregate-allowance-reports", "--aggregate-allowance-config=" + aggregateAllowanceJSON(t)})
	if err != nil || valid.aggregateAllowance == nil || valid.providerSource != nil || valid.aggregateAllowance.trust.MaxAge != 1800 {
		t.Fatalf("valid %#v %v", valid.aggregateAllowance, err)
	}
	hits := 0
	blocked := interceptor.NewClient(fake.NewClientBuilder().Build(), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			hits++
			return errors.New("cluster read")
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			hits++
			return errors.New("cluster list")
		},
	})
	reports, err := aggregateAllowanceReports(valid.aggregateAllowance, blocked, blocked)
	if err != nil || reports == nil || !reports.RouteEnabled() || !reports.PublishEnabled() || hits != 0 {
		t.Fatalf("reports %#v err %v hits %d", reports, err, hits)
	}
	store, ok := reports.Store.(*apiserver.AggregateAllowanceReportStore)
	if !ok || store.Namespace != apiserver.AggregateAllowanceNamespace || store.Name != apiserver.AggregateAllowanceName || store.DataKey != apiserver.AggregateAllowanceDataKey || store.MaxAge != 1800*time.Second {
		t.Fatalf("store %#v", store)
	}
	if reports.PublisherSessionUID != "publisher-uid" || reports.PublisherServiceAccount != "dev-env-agent" || len(reports.SessionUIDs) != 1 || reports.SessionUIDs[0] != "reader-uid" {
		t.Fatalf("identity %#v", reports)
	}
	if reports.PortalNamespace != aggregatePortalNamespace || reports.PortalServiceAccount != aggregatePortalAccount || reports.Source != nil {
		t.Fatal("portal identity or source getter changed")
	}
}

func aggregateAllowanceJSON(t *testing.T) string {
	t.Helper()
	return `{
		"slot":"provider-allowances",
		"namespace":"dev-env-system",
		"name":"dev-env-provider-allowances",
		"publisherSessionUID":"publisher-uid",
		"publisherServiceAccount":"dev-env-agent",
		"readerServiceAccount":"dev-env-agent",
		"readerSessionUIDs":["reader-uid"],
		"portalNamespace":"dev-agents",
		"portalServiceAccount":"dev-env-v2-portal",
		"maxAgeSeconds":1800,
		"contexts":[{"provider":"openai","producerRef":"fixture-producer","producerIncarnation":"fixture-incarnation-1","bindingGeneration":0,"accountRef":"fixture-account","workspaceRef":"fixture-workspace","bindingState":"verified","sourceKind":"native_rpc","sourceVersion":"fixture-v1"}],
		"registry":{"sources":[{"provider":"openai","kind":"native_rpc","version":"fixture-v1","capabilityRef":"synthetic:not-live","allowNormalizedMeasures":true,"allowDerivations":true,"directFields":{}}],"metrics":[{"validationRef":"fixture:verified-field-v1","provider":"openai","sourceKind":"native_rpc","sourceVersion":"fixture-v1","category":"included_quota","unit":"percent","direction":"used","windows":["five-hour-window"],"currency":null,"min":"0","max":"100","allowOverLimit":false,"allowUncapped":false,"complementOf":"","historicalOnly":false,"modelScope":""}],"accounts":[{"accountRef":"fixture-account","workspaceRef":"fixture-workspace","generation":0,"producerRef":"fixture-producer","producerIncarnation":"fixture-incarnation-1"}],"entitlements":[],"projections":[]}
	}`
}

func TestAggregatePrivateFileSelectionPreservesEnableAndPrivacyBounds(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "private-selection.json")
	raw := aggregateAllowanceJSON(t)
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := parseAggregateAllowanceInput(true, "", file, ":8443", "dev-agents")
	if err != nil || selected == nil {
		t.Fatal("private source config refused", err)
	}
	link := filepath.Join(dir, "projected-selection.json")
	if err = os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if selected, err = parseAggregateAllowanceInput(true, "", link, ":8443", "dev-agents"); err != nil || selected == nil {
		t.Fatal("normal projected volume symlink refused", err)
	}
	for _, tc := range []struct {
		enabled            bool
		inline, path, addr string
	}{{false, "", file, ":8443"}, {true, raw, file, ":8443"}, {true, "", "relative", ":8443"}, {true, "", file, "0"}, {true, "", dir, ":8443"}, {true, "", file + "-missing", ":8443"}} {
		if _, err = parseAggregateAllowanceInput(tc.enabled, tc.inline, tc.path, tc.addr, "dev-agents"); !errors.Is(err, errAggregateAllowanceConfig) {
			t.Fatal("invalid private configuration accepted", err)
		}
	}
	if err = os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = parseAggregateAllowanceInput(true, "", file, ":8443", "dev-agents"); err == nil {
		t.Fatal("world-readable personal trust config accepted")
	}
	if err = os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte(strings.Repeat("X", aggregateConfigMaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = parseAggregateAllowanceInput(true, "", file, ":8443", "dev-agents"); err == nil || strings.Contains(err.Error(), "XXX") {
		t.Fatal("oversized file accepted or echoed")
	}
	fifo := filepath.Join(dir, "fifo")
	if err = unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = parseAggregateAllowanceInput(true, "", fifo, ":8443", "dev-agents"); err == nil {
		t.Fatal("nonregular config accepted")
	}
}
