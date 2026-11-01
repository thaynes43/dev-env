package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/internal/templates"
)

const (
	sourceRetrieved = "2026-11-01T15:50:00Z"
	sourceNewer     = "2026-11-01T15:55:00Z"
	sourceResets    = "2026-11-08T15:50:00Z"
	sourceEcho      = "echo-sentinel"
)

var sourceFixedNow = time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC)

func TestProviderSourceConfig(t *testing.T) {
	bare, err := parseFlags(nil)
	if err != nil || bare.providerSource != nil || bare.portalReaderEnabled {
		t.Fatalf("default %#v %v", bare.providerSource, err)
	}
	if off, err := providerSourceReports(nil, fake.NewClientBuilder().Build(), fake.NewClientBuilder().Build()); err != nil || off != nil {
		t.Fatalf("nil selection %#v %v", off, err)
	}

	valid, err := parseFlags(providerSourceArgs(1800))
	if err != nil || valid.providerSource == nil || valid.providerSource.maxAge != 1800*time.Second || valid.portalReaderEnabled || valid.apiAddr == "0" {
		t.Fatalf("valid %#v %v", valid.providerSource, err)
	}
	if !slices.Contains(valid.clientSAs, providerSourceSessionNamespace+"/"+providerSourcePublisherSA) {
		t.Fatalf("clients %#v", valid.clientSAs)
	}
	empty := fake.NewClientBuilder().Build()
	reports, err := providerSourceReports(valid.providerSource, empty, empty)
	if err != nil || reports == nil || !reports.Enabled || !reports.RouteEnabled() {
		t.Fatal(err)
	}
	store, ok := reports.Store.(*apiserver.ProviderSourceReportStore)
	if !ok || store.Namespace != providerSourceSlotNamespace || store.Name != providerSourceSlotName || store.DataKey != apiserver.ProviderSourceDataKey {
		t.Fatalf("store %#v", store)
	}
	if store.Selection.PublisherRef != providerallowance.CurrentSourcePublisher || store.Selection.ProducerIncarnation != providerSourceIncarnation || store.Selection.SourceGeneration != providerSourceGeneration || store.Selection.MaxAge != 1800*time.Second {
		t.Fatalf("selection %#v", store.Selection)
	}
	if reports.PublisherSessionUID != providerSourceSessionUID || reports.PublisherServiceAccount != providerSourcePublisherSA || reports.SessionNamespace != providerSourceSessionNamespace || reports.PortalNamespace != providerSourcePortalNamespace || reports.PortalServiceAccount != providerSourcePortalSA {
		t.Fatalf("callers %#v", reports)
	}
	if providerSourcePublisherSA != templates.WorkbenchServiceAccount {
		t.Fatal("publisher account drifted")
	}
	if refused, err := providerSourceReports(valid.providerSource, nil, empty); err == nil || refused != nil || err.Error() != errProviderSourceConfig.Error() {
		t.Fatalf("nil writer %#v %v", refused, err)
	}

	short, err := parseFlags(providerSourceArgs(1))
	if err != nil || short.providerSource == nil || short.providerSource.maxAge != time.Second {
		t.Fatalf("short %#v %v", short.providerSource, err)
	}
	reordered := `{"maxAgeSeconds":1800,"sourceGeneration":1,"producerIncarnation":"` + providerSourceIncarnation + `","publisherRef":"` + providerallowance.CurrentSourcePublisher + `","slot":"` + providerSourceSlot + `"}`
	if o, err := parseFlags([]string{"--enable-provider-source-reports", "--provider-source-config=" + reordered}); err != nil || o.providerSource == nil || o.providerSource.maxAge != 1800*time.Second {
		t.Fatal(err)
	}
	if o, err := parseFlags([]string{"--enable-provider-source-reports", "--provider-source-config= " + providerSourceJSON(1800) + "\n"}); err != nil || o.providerSource == nil {
		t.Fatal(err)
	}
	if o, err := parseFlags([]string{"--provider-source-config= "}); err == nil || err.Error() != errProviderSourceConfig.Error() || o.providerSource != nil {
		t.Fatalf("blank %v %#v", err, o.providerSource)
	}

	refusals := []string{
		"--provider-source-config=" + providerSourceJSON(1800),
		"--enable-provider-source-reports",
		"--enable-provider-source-reports=false --provider-source-config=" + providerSourceJSON(1800),
		"--enable-provider-source-reports --provider-source-config=" + sourceEcho,
		"--enable-provider-source-reports --provider-source-config={\"maxAgeSeconds\":1800}",
		"--enable-provider-source-reports --provider-source-config=" + providerSourceJSON(0),
		"--enable-provider-source-reports --provider-source-config=" + providerSourceJSON(-1),
		"--enable-provider-source-reports --provider-source-config=" + providerSourceJSON(1801),
		"--enable-provider-source-reports --provider-source-config=" + providerSourceJSON(86400),
		"--enable-provider-source-reports --provider-source-config=" + strings.Replace(providerSourceJSON(1800), `"maxAgeSeconds":1800`, `"maxAgeSeconds":1800.0`, 1),
		"--enable-provider-source-reports --provider-source-config=" + strings.Replace(providerSourceJSON(1800), `"sourceGeneration":1`, `"sourceGeneration":"1"`, 1),
		"--enable-provider-source-reports --provider-source-config=" + strings.Replace(providerSourceJSON(1800), `"sourceGeneration":1`, `"sourceGeneration":2`, 1),
		"--enable-provider-source-reports --provider-source-config=" + strings.Replace(providerSourceJSON(1800), providerSourceSlot, "other-slot", 1),
		"--enable-provider-source-reports --provider-source-config=" + strings.Replace(providerSourceJSON(1800), providerallowance.CurrentSourcePublisher, "other-publisher", 1),
		"--enable-provider-source-reports --provider-source-config=" + strings.Replace(providerSourceJSON(1800), providerSourceIncarnation, "other-incarnation", 1),
		"--enable-provider-source-reports --provider-source-config=" + strings.TrimSuffix(providerSourceJSON(1800), "}") + `,"enabled":true}`,
		"--enable-provider-source-reports --provider-source-config={\"slot\":\"" + providerSourceSlot + "\",\"slot\":\"" + sourceEcho + "\",\"publisherRef\":\"" + providerallowance.CurrentSourcePublisher + "\",\"producerIncarnation\":\"" + providerSourceIncarnation + "\",\"sourceGeneration\":1,\"maxAgeSeconds\":1800}",
		"--enable-provider-source-reports --provider-source-config={\"usedPercent\":\"40\",\"echo\":\"" + sourceEcho + "\"}",
		"--enable-provider-source-reports --provider-source-config=" + "\xef\xbb\xbf" + providerSourceJSON(1800),
		"--enable-provider-source-reports --provider-source-config=" + strings.Repeat(sourceEcho, 400),
		"--enable-provider-source-reports --provider-source-config=" + providerSourceJSON(1800) + " --api-bind-address=0",
		"--enable-provider-source-reports --provider-source-config=" + providerSourceJSON(1800) + " --session-namespace=other",
	}
	for i, raw := range refusals {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			o, err := parseFlags(strings.Split(raw, " "))
			if err == nil || err.Error() != errProviderSourceConfig.Error() || o.providerSource != nil {
				shown := raw
				if len(shown) > 80 {
					shown = shown[:80]
				}
				t.Fatalf("%s err %v enabled %#v", shown, err, o.providerSource)
			}
			if strings.Contains(err.Error(), sourceEcho) || strings.Contains(err.Error(), "usedPercent") || strings.Contains(err.Error(), providerSourceIncarnation) || strings.Contains(err.Error(), "1801") || strings.Contains(err.Error(), "86400") {
				t.Fatal(err)
			}
		})
	}
}

func TestProviderSourceEntrypoint(t *testing.T) {
	o, err := parseFlags(providerSourceArgs(1800))
	if err != nil {
		t.Fatal(err)
	}
	base, reader, writer, ops := sourceStack(t)
	raw := sourceReport(t, sourceRetrieved, providerSourceIncarnation, 1, nil)
	if err := providerallowance.ValidateCurrentSource(mustSourceReport(t, raw), sourceSelection(1800), sourceFixedNow); err != nil {
		t.Fatal(err)
	}
	sourceSessionPod(t, base, providerSourceSessionName, providerSourceSessionUID, "synthetic-current-pod", providerSourcePublisherSA)
	sourceSessionPod(t, base, "synthetic-other-session", "synthetic-other-session", "synthetic-other-pod", providerSourcePublisherSA)
	sourcePortalPod(t, base)
	if err := base.Create(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: providerSourceSlotNamespace, Name: providerSourceSlotName,
		Labels: map[string]string{"synthetic.example/slot": "fixed"}, Annotations: map[string]string{"synthetic.example/manager": "gitops"},
	}, Data: map[string]string{"keep": "yes", apiserver.ProviderSourceDataKey: string(raw)}}); err != nil {
		t.Fatal(err)
	}
	if !ops.idle() {
		t.Fatalf("seed touched the slot %#v", ops)
	}

	auth := sourceTokenAuth{
		"publisher": sourceID(providerSourceSessionNamespace, providerSourcePublisherSA, providerSourceSessionName, "synthetic-current-pod"),
		"stale":     sourceID(providerSourceSessionNamespace, providerSourcePublisherSA, providerSourceSessionName, "synthetic-stale-pod"),
		"client":    sourceID(providerSourceSessionNamespace, providerSourcePublisherSA, "", ""),
		"other":     sourceID(providerSourceSessionNamespace, providerSourcePublisherSA, "synthetic-other-session", "synthetic-other-pod"),
		"stranger":  sourceID("upgrade-agent", "alert-responder", "", ""),
		"portal":    sourceID(providerSourcePortalNamespace, providerSourcePortalSA, "synthetic-portal-pod", "synthetic-portal-pod"),
	}
	srv := &apiserver.Server{Client: base, Live: base, Auth: auth, Policy: sourcePolicy(o), Log: logr.Discard(), Now: func() time.Time { return sourceFixedNow }}
	offHandler := srv.Handler()
	denied := sourceDo(offHandler, http.MethodGet, apiserver.ProviderSourceReportPath, "publisher", "")
	if denied.Code != http.StatusNotFound || !ops.idle() {
		t.Fatalf("off %d %#v %s", denied.Code, ops, denied.Body.String())
	}
	sourceQuiet(t, denied.Body.String())

	reports, err := providerSourceReports(o.providerSource, writer, reader)
	if err != nil || reports == nil || !reports.RouteEnabled() || !ops.idle() {
		t.Fatalf("build %v %#v", err, ops)
	}
	reports.Now = func() time.Time { return sourceFixedNow }
	srv.ProviderSourceReports = reports
	h := srv.Handler()
	want := string(raw)

	w := sourceDo(h, http.MethodGet, apiserver.ProviderSourceReportPath, "publisher", "")
	if w.Code != http.StatusOK || !bytes.Equal(sourceBody(t, w), raw) || ops.gets != 1 || ops.updates != 0 || !ops.sideQuiet() {
		t.Fatalf("get %d %#v %s", w.Code, ops, w.Body.String())
	}
	sourceSameShape(t, sourceBody(t, w), sourceRetrieved, 1)
	sourceHeld(t, base, want)

	before := *ops
	replay := sourceDo(h, http.MethodPut, apiserver.ProviderSourceReportPath, "publisher", want)
	if replay.Code != http.StatusNoContent || replay.Body.Len() != 0 || ops.gets != before.gets+1 || ops.updates != before.updates {
		t.Fatalf("replay %d %#v", replay.Code, ops)
	}
	sourceHeld(t, base, want)

	later := mustSourceTime(t, sourceRetrieved).Add(1800*time.Second + time.Second)
	reports.Now = func() time.Time { return later }
	before = *ops
	stale := sourceDo(h, http.MethodGet, apiserver.ProviderSourceReportPath, "publisher", "")
	if stale.Code != http.StatusOK || sourceBodyString(t, stale) != `{"availability":"Unknown"}` || ops.gets != before.gets+1 || ops.updates != before.updates {
		t.Fatalf("stale %d %#v %s", stale.Code, ops, stale.Body.String())
	}
	sourceQuiet(t, stale.Body.String())
	sourceHeld(t, base, want)
	reports.Now = func() time.Time { return sourceFixedNow }

	for _, token := range []string{"", "stranger", "client", "other", "stale"} {
		before = *ops
		got := sourceDo(h, http.MethodPut, apiserver.ProviderSourceReportPath, token, want)
		if got.Code == http.StatusOK || got.Code == http.StatusNoContent || ops.gets != before.gets || ops.updates != before.updates {
			t.Fatalf("%s %d %#v %s", token, got.Code, ops, got.Body.String())
		}
		sourceQuiet(t, got.Body.String())
	}
	sourceHeld(t, base, want)

	before = *ops
	portalOff := sourceDo(h, http.MethodGet, apiserver.ProviderSourceReportPath, "portal", "")
	if portalOff.Code != http.StatusForbidden || ops.gets != before.gets || ops.updates != before.updates || srv.Policy.PortalReaderEnabled {
		t.Fatalf("portal off %d %#v %s", portalOff.Code, ops, portalOff.Body.String())
	}
	sourceQuiet(t, portalOff.Body.String())
	srv.Policy.PortalReaderEnabled = true
	before = *ops
	portalGet := sourceDo(h, http.MethodGet, apiserver.ProviderSourceReportPath, "portal", "")
	if portalGet.Code != http.StatusOK || !bytes.Equal(sourceBody(t, portalGet), raw) || ops.gets != before.gets+1 || ops.updates != before.updates {
		t.Fatalf("portal get %d %#v %s", portalGet.Code, ops, portalGet.Body.String())
	}
	before = *ops
	portalPut := sourceDo(h, http.MethodPut, apiserver.ProviderSourceReportPath, "portal", want)
	if portalPut.Code == http.StatusNoContent || ops.gets != before.gets || ops.updates != before.updates {
		t.Fatalf("portal put %d %#v", portalPut.Code, ops)
	}
	sourceQuiet(t, portalPut.Body.String())
	for _, path := range []string{"/v1/provider-source-reports", "/v1/provider-source-reports/other-slot", "/v1/inbox"} {
		before = *ops
		foreign := sourceDo(h, http.MethodGet, path, "portal", "")
		if foreign.Code == http.StatusOK || ops.gets != before.gets || ops.updates != before.updates {
			t.Fatalf("%s %d %#v", path, foreign.Code, ops)
		}
	}
	before = *ops
	queried := sourceDo(h, http.MethodGet, apiserver.ProviderSourceReportPath+"?x=1", "publisher", "")
	posted := sourceDo(h, http.MethodPost, apiserver.ProviderSourceReportPath, "publisher", want)
	if queried.Code == http.StatusOK || posted.Code == http.StatusOK || posted.Code == http.StatusNoContent || ops.gets != before.gets || ops.updates != before.updates {
		t.Fatalf("query %d post %d %#v", queried.Code, posted.Code, ops)
	}

	newer := sourceReport(t, sourceNewer, providerSourceIncarnation, 1, nil)
	before = *ops
	put := sourceDo(h, http.MethodPut, apiserver.ProviderSourceReportPath, "publisher", string(newer))
	if put.Code != http.StatusNoContent || ops.gets != before.gets+1 || ops.updates != before.updates+1 {
		t.Fatalf("newer %d %#v %s", put.Code, ops, put.Body.String())
	}
	want = string(newer)
	sourceHeld(t, base, want)
	sourceSameShape(t, newer, sourceNewer, 1)
	if strings.Contains(want, sourceRetrieved) || !strings.Contains(want, `"usedPercent":"40"`) || !strings.Contains(want, `"providerUpdatedAt":null`) {
		t.Fatal(want)
	}

	updated := sourceRetrieved
	bad := [][]byte{
		sourceReport(t, sourceNewer, providerSourceIncarnation, 2, nil),
		sourceReport(t, sourceNewer, "other-incarnation", 1, nil),
		sourceReport(t, sourceRetrieved, providerSourceIncarnation, 1, &updated),
	}
	for _, body := range bad {
		before = *ops
		got := sourceDo(h, http.MethodPut, apiserver.ProviderSourceReportPath, "publisher", string(body))
		if got.Code == http.StatusNoContent || ops.gets != before.gets || ops.updates != before.updates {
			t.Fatalf("refused %d %#v %s", got.Code, ops, got.Body.String())
		}
		sourceQuiet(t, got.Body.String())
		sourceHeld(t, base, want)
	}
	if !ops.sideQuiet() {
		t.Fatalf("side effects %#v", ops)
	}

	aged, err := parseFlags(providerSourceArgs(1))
	if err != nil {
		t.Fatal(err)
	}
	shortReports, err := providerSourceReports(aged.providerSource, writer, reader)
	if err != nil || shortReports == nil || !shortReports.RouteEnabled() || shortReports.Store.(*apiserver.ProviderSourceReportStore).Selection.MaxAge != time.Second {
		t.Fatal(err)
	}
	shortReports.Now = func() time.Time { return sourceFixedNow }
	srv.ProviderSourceReports = shortReports
	h = srv.Handler()
	before = *ops
	agedGet := sourceDo(h, http.MethodGet, apiserver.ProviderSourceReportPath, "publisher", "")
	if agedGet.Code != http.StatusOK || sourceBodyString(t, agedGet) != `{"availability":"Unknown"}` || ops.updates != before.updates {
		t.Fatalf("aged %d %#v %s", agedGet.Code, ops, agedGet.Body.String())
	}
	sourceQuiet(t, agedGet.Body.String())
	sourceHeld(t, base, want)
}

func TestProviderSourceMissingObject(t *testing.T) {
	o, err := parseFlags(providerSourceArgs(1800))
	if err != nil {
		t.Fatal(err)
	}
	base, reader, writer, ops := sourceStack(t)
	sourceSessionPod(t, base, providerSourceSessionName, providerSourceSessionUID, "synthetic-current-pod", providerSourcePublisherSA)
	reports, err := providerSourceReports(o.providerSource, writer, reader)
	if err != nil {
		t.Fatal(err)
	}
	reports.Now = func() time.Time { return sourceFixedNow }
	srv := &apiserver.Server{
		Client: base, Live: base, Auth: sourceTokenAuth{"publisher": sourceID(providerSourceSessionNamespace, providerSourcePublisherSA, providerSourceSessionName, "synthetic-current-pod")},
		Policy: sourcePolicy(o), ProviderSourceReports: reports, Log: logr.Discard(), Now: func() time.Time { return sourceFixedNow },
	}
	h := srv.Handler()
	raw := sourceReport(t, sourceRetrieved, providerSourceIncarnation, 1, nil)
	got := sourceDo(h, http.MethodGet, apiserver.ProviderSourceReportPath, "publisher", "")
	if got.Code != http.StatusOK || sourceBodyString(t, got) != `{"availability":"Unknown"}` || ops.gets != 1 || ops.creates != 0 {
		t.Fatalf("get %d %#v %s", got.Code, ops, got.Body.String())
	}
	sourceQuiet(t, got.Body.String())
	put := sourceDo(h, http.MethodPut, apiserver.ProviderSourceReportPath, "publisher", string(raw))
	if put.Code != http.StatusServiceUnavailable || ops.gets != 2 || ops.updates != 0 || ops.creates != 0 || !ops.sideQuiet() {
		t.Fatalf("put %d %#v %s", put.Code, ops, put.Body.String())
	}
	sourceQuiet(t, put.Body.String())
	if err := base.Get(context.Background(), client.ObjectKey{Namespace: providerSourceSlotNamespace, Name: providerSourceSlotName}, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}

type slotOps struct {
	gets, updates, lists, watches, creates, deletes, patches int
}

func (s slotOps) idle() bool { return s == slotOps{} }

func (s slotOps) sideQuiet() bool {
	return s.lists == 0 && s.watches == 0 && s.creates == 0 && s.deletes == 0 && s.patches == 0
}

func sourceStack(t *testing.T) (client.Client, client.Client, client.Client, *slotOps) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	ops := &slotOps{}
	countSlot := func(key client.ObjectKey) {
		if key.Namespace == providerSourceSlotNamespace && key.Name == providerSourceSlotName {
			ops.gets++
		}
	}
	reader := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, next client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			countSlot(key)
			return next.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, next client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			ops.lists++
			return next.List(ctx, list, opts...)
		},
		Watch: func(ctx context.Context, next client.WithWatch, list client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
			ops.watches++
			return next.Watch(ctx, list, opts...)
		},
		Create: func(ctx context.Context, next client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			ops.creates++
			return next.Create(ctx, obj, opts...)
		},
		Delete: func(ctx context.Context, next client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			ops.deletes++
			return next.Delete(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, next client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			ops.patches++
			return next.Patch(ctx, obj, patch, opts...)
		},
	})
	writer := interceptor.NewClient(base, interceptor.Funcs{
		Update: func(ctx context.Context, next client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Namespace == providerSourceSlotNamespace && cm.Name == providerSourceSlotName {
				ops.updates++
			}
			return next.Update(ctx, obj, opts...)
		},
		Create: func(ctx context.Context, next client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			ops.creates++
			return next.Create(ctx, obj, opts...)
		},
		Delete: func(ctx context.Context, next client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			ops.deletes++
			return next.Delete(ctx, obj, opts...)
		},
		List: func(ctx context.Context, next client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			ops.lists++
			return next.List(ctx, list, opts...)
		},
		Watch: func(ctx context.Context, next client.WithWatch, list client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
			ops.watches++
			return next.Watch(ctx, list, opts...)
		},
		Patch: func(ctx context.Context, next client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			ops.patches++
			return next.Patch(ctx, obj, patch, opts...)
		},
	})
	return base, reader, writer, ops
}

func providerSourceArgs(maxAge int64) []string {
	return []string{"--enable-provider-source-reports", "--provider-source-config=" + providerSourceJSON(maxAge)}
}

func providerSourceJSON(maxAge int64) string {
	return `{"slot":"` + providerSourceSlot + `","publisherRef":"` + providerallowance.CurrentSourcePublisher + `","producerIncarnation":"` + providerSourceIncarnation + `","sourceGeneration":1,"maxAgeSeconds":` + strconv.FormatInt(maxAge, 10) + `}`
}

func sourceSelection(seconds int64) providerallowance.CurrentSourceSelection {
	return providerallowance.CurrentSourceSelection{
		PublisherRef: providerallowance.CurrentSourcePublisher, ProducerIncarnation: providerSourceIncarnation,
		SourceGeneration: providerSourceGeneration, MaxAge: time.Duration(seconds) * time.Second,
	}
}

func sourceReport(t *testing.T, retrieved, incarnation string, generation int64, provider *string) []byte {
	t.Helper()
	raw, err := json.Marshal(providerallowance.CurrentSourceReport{
		Schema: providerallowance.CurrentSourceSchema, PublisherRef: providerallowance.CurrentSourcePublisher,
		ProducerIncarnation: incarnation, SourceGeneration: generation, RetrievedAt: retrieved, ProviderUpdatedAt: provider,
		UsedPercent: "40", RemainingPercent: "60", WindowDurationMinutes: 10080, ResetsAt: sourceResets,
		RemainingPercentBasis: providerallowance.CurrentSourceRemainingBasis,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := providerallowance.DecodeCurrentSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(raw, canonical) {
		t.Fatalf("canonical %s", raw)
	}
	return raw
}

func mustSourceReport(t *testing.T, raw []byte) providerallowance.CurrentSourceReport {
	t.Helper()
	report, err := providerallowance.DecodeCurrentSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func mustSourceTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func sourceSameShape(t *testing.T, raw []byte, retrieved string, generation int64) {
	t.Helper()
	report := mustSourceReport(t, raw)
	if report.ProducerIncarnation != providerSourceIncarnation || report.SourceGeneration != generation || report.RetrievedAt != retrieved || report.ProviderUpdatedAt != nil || report.UsedPercent != "40" || report.RemainingPercent != "60" || report.WindowDurationMinutes != 10080 || report.ResetsAt != sourceResets || report.RemainingPercentBasis != providerallowance.CurrentSourceRemainingBasis {
		t.Fatalf("%s", raw)
	}
}

func sourceSessionPod(t *testing.T, c client.Client, name string, uid, podUID types.UID, sa string) {
	t.Helper()
	ctx := context.Background()
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: providerSourceSessionNamespace, UID: uid},
		Spec: v1alpha1.AgentSessionSpec{
			Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "synthetic",
			Parent: "dev-env-system/dev-env-human", OperatingMode: v1alpha1.OperatingModeRunning,
		},
		Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning, PodName: name},
	}
	if err := c.Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.AgentSession
	if err := c.Get(ctx, client.ObjectKey{Namespace: providerSourceSessionNamespace, Name: name}, &got); err != nil || got.UID != uid || got.Status.Phase != v1alpha1.PhaseRunning || got.Status.PodName != name {
		t.Fatalf("session %v %#v", err, got.Status)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: providerSourceSessionNamespace, UID: podUID,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&got, v1alpha1.GroupVersion.WithKind("AgentSession"))},
		},
		Spec:   corev1.PodSpec{ServiceAccountName: sa, Containers: []corev1.Container{{Name: "agent", Image: "synthetic"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	var gotPod corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: providerSourceSessionNamespace, Name: name}, &gotPod); err != nil || gotPod.UID != podUID || gotPod.Status.Phase != corev1.PodRunning || gotPod.Spec.ServiceAccountName != sa {
		t.Fatalf("pod %v %#v", err, gotPod)
	}
	owner := metav1.GetControllerOf(&gotPod)
	if owner == nil || owner.UID != uid || owner.Name != name || owner.Kind != "AgentSession" {
		t.Fatalf("owner %#v", owner)
	}
}

func sourcePortalPod(t *testing.T, c client.Client) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "synthetic-portal-pod", Namespace: providerSourcePortalNamespace, UID: "synthetic-portal-pod"},
		Spec:       corev1.PodSpec{ServiceAccountName: providerSourcePortalSA, Containers: []corev1.Container{{Name: "portal", Image: "synthetic"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if err := c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
}

func sourceHeld(t *testing.T, c client.Client, raw string) {
	t.Helper()
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: providerSourceSlotNamespace, Name: providerSourceSlotName}, &cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data[apiserver.ProviderSourceDataKey] != raw || cm.Data["keep"] != "yes" || cm.Labels["synthetic.example/slot"] != "fixed" || cm.Annotations["synthetic.example/manager"] != "gitops" {
		t.Fatalf("slot %#v labels %#v", cm.Data, cm.Labels)
	}
}

type sourceTokenAuth map[string]apiserver.Identity

func (a sourceTokenAuth) Authenticate(_ context.Context, token string) (apiserver.Identity, error) {
	id, ok := a[token]
	if !ok {
		return apiserver.Identity{}, errProviderSourceConfig
	}
	return id, nil
}

func sourcePolicy(o options) apiserver.Policy {
	var p apiserver.Policy
	p.Human = o.humanSA
	p.Clients = o.clientSAs
	p.SessionNamespace = o.sessionNamespace
	p.SessionServiceAccount = controller.ServiceAccountName
	p.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	p.NamedCoordinatorIdentities = true
	p.PortalReaderEnabled = o.portalReaderEnabled
	return p
}

func sourceID(namespace, name, pod, uid string) apiserver.Identity {
	return apiserver.Identity{
		Username: "system:serviceaccount:" + namespace + ":" + name, Namespace: namespace, ServiceAccount: name,
		PodName: pod, PodUID: uid,
	}
}

func sourceDo(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" && method != http.MethodPut {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func sourceBody(t *testing.T, w *httptest.ResponseRecorder) []byte {
	t.Helper()
	body := w.Body.Bytes()
	if !bytes.HasSuffix(body, []byte("\n")) {
		t.Fatalf("body %q", body)
	}
	return bytes.TrimSuffix(body, []byte("\n"))
}

func sourceBodyString(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	return string(sourceBody(t, w))
}

func sourceQuiet(t *testing.T, body string) {
	t.Helper()
	for _, banned := range []string{sourceEcho, "usedPercent", providerSourceIncarnation, "Measured", "providerUpdatedAt"} {
		if strings.Contains(body, banned) {
			t.Fatalf("echoed %s in %s", banned, body)
		}
	}
}
