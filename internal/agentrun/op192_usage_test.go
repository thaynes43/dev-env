package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// OP192 S1 release R1 (research R-07): agent-run prints a document with the
// separate Grok row unchanged, invents no Grok row for a three-row document,
// and its local preflight still emits exactly the three required rows.

func op192UsageDoc(grok bool) []byte {
	at := "2026-11-05T18:00:00Z"
	code := "source_unconfigured"
	doc := providerallowance.ReadResponse{Schema: providerallowance.SchemaRead, SnapshotRevision: "op192-usage", ServedAt: at, Items: []providerallowance.ReadItem{}}
	providers := []string{"cursor", "anthropic", "openai"}
	if grok {
		providers = append(providers, "grok")
	}
	for _, provider := range providers {
		doc.SourceStatuses = append(doc.SourceStatuses, providerallowance.SourceStatus{
			Provider: provider, ProducerRef: "op192-synthetic", Availability: providerallowance.AvailabilityUnconfigured,
			LastAttempt: providerallowance.LastAttempt{AttemptedAt: at, CompletedAt: &at, Result: providerallowance.AttemptUnknown, Code: &code},
			Freshness:   providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: at, Basis: providerallowance.BasisNone, Reason: "source not configured"},
			Validation:  providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified, ValidationRefs: []string{}},
			Binding:     providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
		})
	}
	raw, _ := json.Marshal(doc)
	return raw
}

func op192UsageProviders(t *testing.T, raw []byte) []string {
	t.Helper()
	doc, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, status := range doc.SourceStatuses {
		out = append(out, status.Provider)
	}
	return out
}

func TestOP192UsageFromFileKeepsGrokRowAndInventsNone(t *testing.T) {
	for _, grok := range []bool{true, false} {
		in := op192UsageDoc(grok)
		var out bytes.Buffer
		env := Env{Stdout: &out, Stderr: io.Discard, Stdin: bytes.NewReader(in)}
		if code := Run(context.Background(), []string{"usage", "-o", "json", "--from-file", "-"}, env); code != 0 {
			t.Fatalf("grok=%v exit %d", grok, code)
		}
		want := []string{"cursor", "anthropic", "openai"}
		if grok {
			want = append(want, "grok")
		}
		if got := op192UsageProviders(t, out.Bytes()); !reflect.DeepEqual(got, want) {
			t.Fatalf("grok=%v providers %v, want %v", grok, got, want)
		}
		if !bytes.Equal(bytes.TrimSuffix(out.Bytes(), []byte("\n")), in) {
			t.Fatalf("grok=%v document changed", grok)
		}
	}
}

func TestOP192UsageLocalPreflightEmitsNoGrokRow(t *testing.T) {
	var out bytes.Buffer
	env := Env{Stdout: &out, Stderr: io.Discard, Now: func() time.Time { return time.Date(2026, 11, 5, 19, 0, 0, 0, time.UTC) }}
	if code := Run(context.Background(), []string{"usage", "-o", "json"}, env); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := op192UsageProviders(t, out.Bytes()); !reflect.DeepEqual(got, []string{"cursor", "anthropic", "openai"}) {
		t.Fatalf("R1 preflight emitted %v", got)
	}
}

// TestOP192UsageReadCurrentDecodesFourRowEmptySlot serves the store's empty
// slot through the operator API in store mode. --read-current prints the
// four-row empty-slot document unchanged, Grok row included.
func TestOP192UsageReadCurrentDecodesFourRowEmptySlot(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 19, 0, 0, 0, time.UTC)
	readerUID, readerPod := types.UID("allow-session"), types.UID("allow-pod")
	sess := remoteSession("reader", readerUID)
	slot := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: apiserver.AggregateAllowanceNamespace, Name: apiserver.AggregateAllowanceName}, Data: map[string]string{"keep": "yes"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sess, remotePod(sess, readerPod), slot).Build()
	token := "tok-allow"
	store := &apiserver.AggregateAllowanceReportStore{
		Writer: c, Live: c, Namespace: apiserver.AggregateAllowanceNamespace, Name: apiserver.AggregateAllowanceName, DataKey: apiserver.AggregateAllowanceDataKey,
		Registry: providerallowance.NewStaticRegistry(providerallowance.RegistryParts{}), MaxAge: 30 * time.Minute,
	}
	srv := &apiserver.Server{
		Client: c, Live: c, Auth: remoteAuth{tokens: map[string]apiserver.Identity{token: remoteIdentity(readerPod)}}, Log: logr.Discard(),
		Now: func() time.Time { return now },
		Policy: apiserver.Policy{
			Human: "dev-env-system/dev-env-human", Clients: []string{"dev/dev-env"},
			SessionNamespace: "dev-agents", SessionServiceAccount: controller.ServiceAccountName,
		},
		AggregateAllowanceReports: &apiserver.AggregateAllowanceReports{
			Enabled: true, OperatorProducer: true, Store: store, Live: c, Now: func() time.Time { return now },
			SessionNamespace: "dev-agents", SessionServiceAccount: controller.ServiceAccountName, SessionUIDs: []types.UID{readerUID},
			PublisherSessionUID: "operator-owned", PublisherServiceAccount: "operator-owned",
		},
	}
	if !srv.AggregateAllowanceReports.RouteEnabled() {
		t.Fatal("store-mode route is not enabled")
	}
	tls := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(tls.Close)
	stdout, _ := remoteRun(t, remoteAllowanceEnv(t, tls, token), ExitOK, "usage", "-o", "json", "--read-current")
	got := remoteDecode(t, []byte(stdout))
	if !reflect.DeepEqual(got, remoteRoundTrip(t, apiserver.UnconfiguredAggregateDocument())) {
		t.Fatalf("empty slot changed in transit: %s", stdout)
	}
	if want := []string{"cursor", "anthropic", "openai", "grok"}; !reflect.DeepEqual(op192UsageProviders(t, []byte(stdout)), want) {
		t.Fatalf("providers %v, want %v", op192UsageProviders(t, []byte(stdout)), want)
	}
}
