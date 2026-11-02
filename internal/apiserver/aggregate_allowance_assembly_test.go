package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
	"github.com/thaynes43/dev-env/web/portal/session"
)

func TestAggregateAllowanceAssemblyPreservesTwoCaptures(t *testing.T) {
	first, second := assemblyCaptures(t)
	f := newFixture(t)
	f.now = time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC)
	pubTok, pubUID := assemblyRunning(t, f, "publisher")
	readTok, readUID := assemblyRunning(t, f, "reader")
	foreignTok, foreignUID := assemblyRunning(t, f, "foreign")
	otherTok, _ := assemblyRunning(t, f, "other-publisher")
	mem := &slotMem{cm: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, ResourceVersion: "1"}, Data: map[string]string{"keep": "yes"}}}
	account, workspace := "fixture-account", "fixture-workspace"
	reg := assemblyRegistry()
	store := &AggregateAllowanceReportStore{
		Writer: mem, Live: mem, Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, DataKey: AggregateAllowanceDataKey,
		Contexts: []providerallowance.TrustedContext{{
			Provider: "openai", ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", BindingGeneration: 0,
			AccountRef: &account, WorkspaceRef: &workspace, BindingState: providerallowance.BindingVerified,
			SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
		}},
		Registry: reg, MaxAge: 30 * time.Minute,
	}
	closed := httptest.NewRecorder()
	f.h.ServeHTTP(closed, httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil))
	if closed.Code != http.StatusNotFound || mem.updates != 0 {
		t.Fatalf("default route %d updates %d", closed.Code, mem.updates)
	}
	f.srv.AggregateAllowanceReports = &AggregateAllowanceReports{
		Enabled: true, Store: store, Live: f.c,
		SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName, SessionUIDs: []types.UID{readUID},
		PublisherSessionUID: pubUID, PublisherServiceAccount: controller.ServiceAccountName,
		PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount,
		Now: func() time.Time { return f.now },
	}
	f.h = f.srv.Handler()
	tlsSrv := httptest.NewTLSServer(f.h)
	t.Cleanup(tlsSrv.Close)
	pub := assemblyEnv(t, tlsSrv, pubTok)
	reader := assemblyEnv(t, tlsSrv, readTok)
	emptyCode, emptyOut, emptyErr := assemblyRun(t, reader, "usage", "-o", "json", "--read-current")
	if emptyCode != agentrun.ExitOK || strings.Contains(emptyErr, readTok) {
		t.Fatalf("empty get %d %s %s", emptyCode, emptyOut, emptyErr)
	}
	empty := assemblyDecode(t, emptyOut)
	if empty.ServedAt != "1970-01-01T00:00:00Z" || len(empty.Items) != 0 || len(empty.SourceStatuses) != 3 || mem.updates != 0 {
		t.Fatalf("empty slot %+v updates %d", empty, mem.updates)
	}
	for _, status := range empty.SourceStatuses {
		if status.Availability != providerallowance.AvailabilityUnconfigured || status.Freshness.State != providerallowance.FreshUnknown || status.LastGood != nil {
			t.Fatalf("gap %+v", status)
		}
	}
	firstFile := assemblyWrite(t, first)
	if code, out, errText := assemblyRun(t, pub, "usage", "-o", "json", "--publish-file", firstFile); code != agentrun.ExitOK || out != "published provider-allowances status=204\n" || mem.updates != 1 {
		t.Fatalf("first put %d %q %s updates %d", code, out, errText, mem.updates)
	}
	secondFile := assemblyWrite(t, second)
	if code, out, errText := assemblyRun(t, pub, "usage", "-o", "json", "--publish-file", secondFile); code != agentrun.ExitOK || out != "published provider-allowances status=204\n" || mem.updates != 2 {
		t.Fatalf("second put %d %q %s updates %d", code, out, errText, mem.updates)
	}
	held := mem.cm.Data[AggregateAllowanceDataKey]
	if code, _, errText := assemblyRun(t, pub, "usage", "-o", "json", "--publish-file", secondFile); code != agentrun.ExitOK || mem.updates != 2 || mem.cm.Data[AggregateAllowanceDataKey] != held {
		t.Fatalf("replay %d %s updates %d", code, errText, mem.updates)
	}
	code, body, errText := assemblyRun(t, reader, "usage", "-o", "json", "--read-current")
	if code != agentrun.ExitOK {
		t.Fatal(code, errText)
	}
	got := assemblyDecode(t, body)
	item := assemblyItem(t, got, "openai")
	want := assemblyItem(t, second, "openai")
	if item.Observation.ObservationID != "obs-2" || item.Observation.CapturedAt != want.Observation.CapturedAt || item.ReceivedAt != want.ReceivedAt || !assemblySameString(item.Observation.ProviderObservedAt, want.Observation.ProviderObservedAt) {
		t.Fatalf("identity got %+v want %+v", item, want)
	}
	if item.Observation.ObservationID == assemblyItem(t, first, "openai").Observation.ObservationID {
		t.Fatal("first capture was restored")
	}
	if decimal := item.Observation.Measures[0].Value.Decimal; decimal == nil || *decimal != "55" {
		t.Fatalf("decimal %v", decimal)
	}
	if item.Freshness.State != providerallowance.FreshFresh || len(item.CurrentMeasures) != 1 || item.CurrentMeasures[0].State != providerallowance.CurrentKnown {
		t.Fatalf("current %+v %+v", item.Freshness, item.CurrentMeasures)
	}
	for _, provider := range []string{"cursor", "anthropic"} {
		status := assemblyStatus(t, got, provider)
		if status.Availability != providerallowance.AvailabilityUnconfigured || status.Freshness.State != providerallowance.FreshUnknown || status.LastGood != nil {
			t.Fatalf("missing %s %+v", provider, status)
		}
	}
	rows := session.NewCachedAllowanceReader([]byte(strings.TrimRight(body, "\n")), providerallowance.FixtureParseLimits, store.trust().Contexts, reg, session.AllowanceReadPolicy{MaxAge: 30 * time.Minute, Set: true}).Allowances(f.now)
	var codex *portal.ProductAllowance
	for i := range rows {
		if rows[i].Provider == "Codex" {
			codex = &rows[i]
		}
		if rows[i].Provider == "Claude" || rows[i].Provider == "Cursor" {
			if rows[i].Value != portal.AllowanceNotConnected || len(rows[i].Windows) != 0 {
				t.Fatalf("gap card %+v", rows[i])
			}
		}
	}
	if codex == nil || len(codex.Windows) != 1 || codex.Windows[0].ObservedAt != *want.Observation.ProviderObservedAt {
		t.Fatalf("portal %+v", rows)
	}
	stored := mem.cm.Data[AggregateAllowanceDataKey]
	assemblyRefuse(t, pub, assemblyMutate(t, second, func(doc *providerallowance.ReadResponse) {
		doc.Items[0].Observation.Measures[0].Value.Decimal = assemblyString("56")
	}), stored, mem)
	assemblyRefuse(t, pub, assemblyWrite(t, first), stored, mem)
	assemblyRefuse(t, pub, assemblyMutate(t, second, func(doc *providerallowance.ReadResponse) {
		doc.Items[0].Observation.Sequence = 3
		doc.Items[0].Observation.Scope.AccountRef = assemblyString("other-account")
	}), stored, mem)
	assemblyRefuse(t, pub, assemblyMutate(t, second, func(doc *providerallowance.ReadResponse) {
		doc.Items[0].Observation.ProviderObservedAt = assemblyString("2026-11-02T04:00:00Z")
	}), stored, mem)
	assemblyRefuse(t, pub, assemblyMutate(t, second, func(doc *providerallowance.ReadResponse) {
		doc.Items[0].Observation.Measures[0].Window.ResetAt = assemblyString("2026-11-02T03:00:00Z")
	}), stored, mem)
	foreignCode, _, foreignErr := assemblyRun(t, assemblyEnv(t, tlsSrv, foreignTok), "usage", "-o", "json", "--read-current")
	if foreignCode != agentrun.ExitAuth || strings.Contains(foreignErr, foreignTok) || strings.Contains(foreignErr, string(foreignUID)) || mem.cm.Data[AggregateAllowanceDataKey] != stored {
		t.Fatalf("foreign %d %s", foreignCode, foreignErr)
	}
	if code, _, errText := assemblyRun(t, assemblyEnv(t, tlsSrv, otherTok), "usage", "-o", "json", "--publish-file", secondFile); code != agentrun.ExitAuth || strings.Contains(errText, otherTok) || mem.cm.Data[AggregateAllowanceDataKey] != stored {
		t.Fatalf("other publisher %d %s", code, errText)
	}
	replaced := f.auth[pubTok]
	replaced.PodUID = "replaced-pod-uid"
	f.auth[pubTok] = replaced
	if code, _, errText := assemblyRun(t, pub, "usage", "-o", "json", "--publish-file", secondFile); code != agentrun.ExitAuth || strings.Contains(errText, "replaced-pod-uid") || mem.updates != 2 {
		t.Fatalf("replaced pod %d %s updates %d", code, errText, mem.updates)
	}
	f.now = time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	staleCode, staleOut, staleErr := assemblyRun(t, reader, "usage", "-o", "json", "--read-current")
	if staleCode != agentrun.ExitOK {
		t.Fatal(staleCode, staleErr)
	}
	stale := assemblyItem(t, assemblyDecode(t, staleOut), "openai")
	if stale.Freshness.State != providerallowance.FreshStale || stale.Observation.CapturedAt != want.Observation.CapturedAt || stale.ReceivedAt != want.ReceivedAt || !assemblySameString(stale.Observation.ProviderObservedAt, want.Observation.ProviderObservedAt) || mem.cm.Data[AggregateAllowanceDataKey] != stored {
		t.Fatalf("stale %+v", stale.Freshness)
	}
	f.now = time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)
	resetCode, resetOut, resetErr := assemblyRun(t, reader, "usage", "-o", "json", "--read-current")
	if resetCode != agentrun.ExitOK {
		t.Fatal(resetCode, resetErr)
	}
	reset := assemblyItem(t, assemblyDecode(t, resetOut), "openai")
	if len(reset.CurrentMeasures) != 1 || reset.CurrentMeasures[0].State == providerallowance.CurrentKnown || reset.Observation.Measures[0].Value.Decimal == nil || *reset.Observation.Measures[0].Value.Decimal != "55" || mem.cm.Data[AggregateAllowanceDataKey] != stored {
		t.Fatalf("reset %+v", reset.CurrentMeasures)
	}
}

func TestAggregateAllowanceReadCurrentWithholdsPayloadView(t *testing.T) {
	_, second := assemblyCaptures(t)
	f := newFixture(t)
	f.now = time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC)
	pubTok, pubUID := assemblyRunning(t, f, "publisher")
	readTok, readUID := assemblyRunning(t, f, "reader")
	mem := &slotMem{cm: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, ResourceVersion: "1"}, Data: map[string]string{"keep": "yes"}}}
	account, workspace := "fixture-account", "fixture-workspace"
	reg := assemblyRegistry()
	store := &AggregateAllowanceReportStore{
		Writer: mem, Live: mem, Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, DataKey: AggregateAllowanceDataKey,
		Contexts: []providerallowance.TrustedContext{{
			Provider: "openai", ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", BindingGeneration: 0,
			AccountRef: &account, WorkspaceRef: &workspace, BindingState: providerallowance.BindingVerified,
			SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
		}},
		Registry: reg, MaxAge: 30 * time.Minute,
	}
	f.srv.AggregateAllowanceReports = &AggregateAllowanceReports{
		Enabled: true, Store: store, Live: f.c,
		SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName, SessionUIDs: []types.UID{readUID},
		PublisherSessionUID: pubUID, PublisherServiceAccount: controller.ServiceAccountName,
		PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount,
		Now: func() time.Time { return f.now },
	}
	f.h = f.srv.Handler()
	tlsSrv := httptest.NewTLSServer(f.h)
	t.Cleanup(tlsSrv.Close)
	_ = pubTok
	reader := assemblyEnv(t, tlsSrv, readTok)
	read := func(doc providerallowance.ReadResponse) (providerallowance.ReadResponse, string) {
		t.Helper()
		raw := string(canonicalAllowanceFixture(t, doc))
		mem.cm.Data[AggregateAllowanceDataKey] = raw
		updates := mem.updates
		code, body, errText := assemblyRun(t, reader, "usage", "-o", "json", "--read-current")
		if code != agentrun.ExitOK || strings.Contains(errText, readTok) {
			t.Fatalf("read-current %d %s", code, errText)
		}
		if mem.updates != updates || mem.cm.Data[AggregateAllowanceDataKey] != raw || mem.cm.Data["keep"] != "yes" {
			t.Fatal("read rewrote stored history")
		}
		return assemblyDecode(t, body), raw
	}

	future := cloneAllowanceReport(t, second)
	futureItem := allowanceOpenAIItem(t, &future)
	observed := *futureItem.Observation.ProviderObservedAt
	observedAt, err := time.Parse(time.RFC3339Nano, observed)
	if err != nil || observedAt.After(f.now) {
		t.Fatal("provider time is after the serving clock")
	}
	captured := futureItem.Observation.CapturedAt
	futureItem.ReceivedAt = "2099-01-01T00:00:00Z"
	later := "2099-06-01T00:00:00Z"
	futureItem.Observation.Measures[0].Window.ResetAt = &later
	planted := "77"
	futureItem.CurrentMeasures[0].State = providerallowance.CurrentUncapped
	futureItem.CurrentMeasures[0].Value = providerallowance.MeasureValue{State: providerallowance.ValueUncapped, Decimal: &planted}
	if futureStatus := allowanceOpenAIStatus(t, &future); futureStatus.LastGood != nil {
		futureStatus.LastGood.ReceivedAt = futureItem.ReceivedAt
	}
	futureGot, futureRaw := read(future)
	futureServed := assemblyItem(t, futureGot, "openai")
	if futureServed.ReceivedAt != "2099-01-01T00:00:00Z" || futureServed.Observation.CapturedAt != captured || futureServed.Observation.ProviderObservedAt == nil || *futureServed.Observation.ProviderObservedAt != observed {
		t.Fatalf("read-current moved history %+v", futureServed)
	}
	if futureServed.Observation.Measures[0].Value.Decimal == nil || *futureServed.Observation.Measures[0].Value.Decimal != "55" || futureServed.Freshness.State == providerallowance.FreshFresh || futureServed.Freshness.State == providerallowance.FreshStale {
		t.Fatalf("future receipt stayed current %+v decimal=%v", futureServed.Freshness, futureServed.Observation.Measures[0].Value.Decimal)
	}
	if futureServed.CurrentMeasures[0].State != providerallowance.CurrentUnknown || futureServed.CurrentMeasures[0].Value.Decimal != nil || !strings.Contains(futureRaw, `"77"`) || strings.Contains(futureRaw, readTok) {
		t.Fatalf("raw document kept the current decimal %+v", futureServed.CurrentMeasures[0])
	}
	for _, provider := range []string{"cursor", "anthropic"} {
		status := assemblyStatus(t, futureGot, provider)
		if status.Availability != providerallowance.AvailabilityUnconfigured || status.Binding.State == providerallowance.BindingVerified {
			t.Fatalf("gap promoted %+v", status)
		}
	}

	resetAt := "2026-11-02T03:00:00Z"
	resetDoc := cloneAllowanceReport(t, second)
	resetItem := allowanceOpenAIItem(t, &resetDoc)
	resetItem.Observation.Measures[0].Window.ResetAt = &resetAt
	resetItem.CurrentMeasures[0].State = providerallowance.CurrentUncapped
	resetItem.CurrentMeasures[0].Value = providerallowance.MeasureValue{State: providerallowance.ValueUncapped, Decimal: &planted}
	resetGot, _ := read(resetDoc)
	resetServed := assemblyItem(t, resetGot, "openai")
	if resetServed.Freshness.State != providerallowance.FreshFresh || resetServed.CurrentMeasures[0].State != providerallowance.CurrentUnknown || resetServed.CurrentMeasures[0].Value.Decimal != nil {
		t.Fatalf("uncapped reset %+v %+v", resetServed.Freshness, resetServed.CurrentMeasures[0])
	}
	if resetServed.Observation.Measures[0].Value.Decimal == nil || *resetServed.Observation.Measures[0].Value.Decimal != "55" || resetServed.Observation.Measures[0].Window.ResetAt == nil || *resetServed.Observation.Measures[0].Window.ResetAt != resetAt {
		t.Fatal("reset read rewrote the observation")
	}

	other := "other-account"
	untrusted := cloneAllowanceReport(t, second)
	untrustedItem := allowanceOpenAIItem(t, &untrusted)
	untrustedItem.Observation.Scope.AccountRef = &other
	untrustedStatus := allowanceOpenAIStatus(t, &untrusted)
	untrustedStatus.Availability = providerallowance.AvailabilityConflict
	untrustedStatus.Binding = providerallowance.BindingRecord{State: providerallowance.BindingVerified, Generation: 4, AccountRef: &other}
	untrustedGot, _ := read(untrusted)
	untrustedServed := assemblyItem(t, untrustedGot, "openai")
	untrustedState := assemblyStatus(t, untrustedGot, "openai")
	if untrustedServed.CurrentMeasures[0].State != providerallowance.CurrentUnknown || untrustedServed.CurrentMeasures[0].Value.Decimal != nil || untrustedServed.Observation.Measures[0].Value.Decimal == nil || *untrustedServed.Observation.Measures[0].Value.Decimal != "55" {
		t.Fatalf("untrusted raw document %+v obs=%v", untrustedServed.CurrentMeasures[0], untrustedServed.Observation.Measures[0].Value.Decimal)
	}
	if untrustedState.Availability != providerallowance.AvailabilityConflict || untrustedState.Binding.State == providerallowance.BindingVerified || untrustedState.Binding.AccountRef != nil {
		t.Fatalf("conflict status stayed verified %+v", untrustedState.Binding)
	}

	mismatched := cloneAllowanceReport(t, second)
	mismatchedStatus := allowanceOpenAIStatus(t, &mismatched)
	mismatchedStatus.Binding.AccountRef = &other
	mismatchedStatus.Binding.Generation = 9
	mismatchedGot, _ := read(mismatched)
	mismatchedServed := assemblyItem(t, mismatchedGot, "openai")
	matched := assemblyStatus(t, mismatchedGot, "openai")
	if mismatchedServed.Freshness.State != providerallowance.FreshFresh || mismatchedServed.CurrentMeasures[0].State != providerallowance.CurrentKnown || mismatchedServed.CurrentMeasures[0].Value.Decimal == nil || *mismatchedServed.CurrentMeasures[0].Value.Decimal != "55" {
		t.Fatalf("trusted current was withheld %+v %+v", mismatchedServed.Freshness, mismatchedServed.CurrentMeasures[0])
	}
	if matched.Binding.State != providerallowance.BindingVerified || matched.Binding.Generation != 0 || matched.Binding.AccountRef == nil || *matched.Binding.AccountRef != "fixture-account" || mismatchedServed.Observation.Scope.AccountRef == nil || *mismatchedServed.Observation.Scope.AccountRef != "fixture-account" {
		t.Fatalf("status account stayed foreign %+v", matched.Binding)
	}
}

func TestAggregateAllowancePublishDoesNotFollowOrRetry(t *testing.T) {
	hits := 0
	secret := "PUBLISH-REDIRECT-SECRET"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != http.MethodPut || r.URL.Path != ProviderAllowanceReportsPath || r.URL.RawQuery != "" {
			t.Fatalf("request %s %s", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Location", "https://example.invalid/next")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte(secret))
	}))
	t.Cleanup(srv.Close)
	env := assemblyEnv(t, srv, "publisher-token")
	slept := false
	env.Sleep = func(context.Context, time.Duration) error {
		slept = true
		return errors.New("retry")
	}
	file := assemblyWrite(t, UnconfiguredAggregateDocument())
	code, out, errText := assemblyRun(t, env, "usage", "-o", "json", "--publish-file", file)
	if hits != 1 || slept || code == agentrun.ExitOK || strings.Contains(out, secret) || strings.Contains(errText, secret) {
		t.Fatalf("redirect hits %d slept %v code %d %s %s", hits, slept, code, out, errText)
	}
	linkHits := 0
	linkSrv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { linkHits++ }))
	t.Cleanup(linkSrv.Close)
	target := assemblyWrite(t, UnconfiguredAggregateDocument())
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	linkEnv := assemblyEnv(t, linkSrv, "publisher-token")
	linkCode, _, linkErr := assemblyRun(t, linkEnv, "usage", "-o", "json", "--publish-file", link)
	if linkHits != 0 || linkCode == agentrun.ExitOK || strings.Contains(linkErr, "publisher-token") {
		t.Fatalf("symlink hits %d code %d %s", linkHits, linkCode, linkErr)
	}
}

func assemblyCaptures(t *testing.T) (providerallowance.ReadResponse, providerallowance.ReadResponse) {
	t.Helper()
	clock := &assemblyClock{now: time.Date(2026, 11, 2, 3, 5, 0, 0, time.UTC)}
	wait := &assemblyWait{}
	acquire := &assemblyAcquire{clock: clock, payloads: [][]byte{assemblyObservation(t, "obs-1", 1, "40"), assemblyObservation(t, "obs-2", 2, "55")}}
	account, workspace := "fixture-account", "fixture-workspace"
	spec := providerallowance.SourceSpec{
		Provider: "openai", ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", BindingGeneration: 0,
		AccountRef: &account, WorkspaceRef: &workspace, BindingState: providerallowance.BindingVerified,
		SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
	}
	bounds := providerallowance.Bounds{
		Enabled: true, MaxCycles: 2, MaxSources: 1, MaxBytes: 8192,
		CallTimeout: 2 * time.Second, Cadence: 5 * time.Second, Backoff: time.Second,
		MaxAge: 30 * time.Minute, Parse: providerallowance.FixtureParseLimits,
	}
	collection, err := providerallowance.NewCollection(bounds, []providerallowance.SourceSpec{spec}, assemblyRegistry(), clock, wait, acquire)
	if err != nil {
		t.Fatal(err)
	}
	wait.collection = collection
	if err := collection.Run(context.Background()); err != nil || wait.err != nil || acquire.calls != 2 {
		t.Fatalf("run %v wait %v calls %d", err, wait.err, acquire.calls)
	}
	second, _, err := collection.Snapshot(time.Date(2026, 11, 2, 3, 7, 30, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if assemblyItem(t, wait.first, "openai").Observation.ObservationID != "obs-1" || assemblyItem(t, second, "openai").Observation.ObservationID != "obs-2" {
		t.Fatal("captures were not distinct")
	}
	return wait.first, second
}

func assemblyRunning(t *testing.T, f *fixture, name string) (string, types.UID) {
	t.Helper()
	token := f.sessionPod(name, "task", 0)
	ctx := context.Background()
	var sess v1alpha1.AgentSession
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: name}, &sess); err != nil {
		t.Fatal(err)
	}
	sess.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	if err := f.c.Update(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	sess.Status.Phase = v1alpha1.PhaseRunning
	sess.Status.PodName = name
	if err := f.c.Status().Update(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodRunning
	if err := f.c.Status().Update(ctx, &pod); err != nil {
		t.Fatal(err)
	}
	return token, sess.UID
}

func assemblyEnv(t *testing.T, srv *httptest.Server, token string) agentrun.Env {
	t.Helper()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(dir, "token")
	if err := os.WriteFile(tok, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	return agentrun.Env{
		Stdout: &stdout, Stderr: &stderr,
		Getenv: func(key string) string {
			switch key {
			case "DEV_ENV_API_URL":
				return srv.URL
			case "DEV_ENV_API_TOKEN_FILE":
				return tok
			case "DEV_ENV_API_CA_FILE":
				return ca
			default:
				return ""
			}
		},
		Now:               func() time.Time { return time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC) },
		Sleep:             func(context.Context, time.Duration) error { return errors.New("retry") },
		SessionTokenFile:  filepath.Join(dir, "no-session-token"),
		ServiceAccountDir: filepath.Join(dir, "no-serviceaccount"),
		LookPath:          func(string) (string, error) { return "", errors.New("not found") },
	}
}

func assemblyRun(t *testing.T, env agentrun.Env, args ...string) (int, string, string) {
	t.Helper()
	stdout := env.Stdout.(*bytes.Buffer)
	stderr := env.Stderr.(*bytes.Buffer)
	stdout.Reset()
	stderr.Reset()
	code := agentrun.Run(context.Background(), args, env)
	return code, stdout.String(), stderr.String()
}

func assemblyWrite(t *testing.T, doc providerallowance.ReadResponse) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assemblyMutate(t *testing.T, doc providerallowance.ReadResponse, fn func(*providerallowance.ReadResponse)) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	next, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	fn(&next)
	return assemblyWrite(t, next)
}

func assemblyRefuse(t *testing.T, env agentrun.Env, path, stored string, mem *slotMem) {
	t.Helper()
	code, out, errText := assemblyRun(t, env, "usage", "-o", "json", "--publish-file", path)
	if code == agentrun.ExitOK || strings.Contains(out, "other-account") || strings.Contains(errText, "other-account") || mem.updates != 2 || mem.cm.Data[AggregateAllowanceDataKey] != stored {
		t.Fatalf("refused as %d %s %s updates %d", code, out, errText, mem.updates)
	}
}

func assemblyDecode(t *testing.T, raw string) providerallowance.ReadResponse {
	t.Helper()
	doc, err := providerallowance.DecodeReadResponse([]byte(raw), providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func assemblyItem(t *testing.T, doc providerallowance.ReadResponse, provider string) providerallowance.ReadItem {
	t.Helper()
	var found *providerallowance.ReadItem
	for i := range doc.Items {
		if doc.Items[i].Observation.Provider != provider {
			continue
		}
		if found != nil {
			t.Fatal("duplicate item")
		}
		found = &doc.Items[i]
	}
	if found == nil {
		t.Fatal("missing item")
	}
	return *found
}

func assemblyStatus(t *testing.T, doc providerallowance.ReadResponse, provider string) providerallowance.SourceStatus {
	t.Helper()
	for _, status := range doc.SourceStatuses {
		if status.Provider == provider {
			return status
		}
	}
	t.Fatal("missing status")
	return providerallowance.SourceStatus{}
}

func assemblySameString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func assemblyString(value string) *string { return &value }

func assemblyRegistry() *providerallowance.StaticRegistry {
	min, max := "0", "100"
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: []providerallowance.SourceRule{{
			Provider: "openai", Kind: providerallowance.SourceNativeRPC, Version: "fixture-v1", CapabilityRef: "synthetic:not-live",
			AllowNormalizedMeasures: true, AllowDerivations: true,
		}},
		Metrics: []providerallowance.MetricRule{{
			ValidationRef: "fixture:verified-field-v1", Provider: "openai", SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
			Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
			Windows: []string{"five-hour-window", "seven-day-window"}, Min: &min, Max: &max,
		}},
		Accounts: []providerallowance.AccountRule{{
			AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0,
			ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1",
		}},
	})
}

func assemblyObservation(t *testing.T, id string, sequence int, decimal string) []byte {
	t.Helper()
	observed := "2026-11-02T02:59:00Z"
	reset := "2026-11-02T06:00:00Z"
	account, workspace := "fixture-account", "fixture-workspace"
	minutes := providerallowance.Count(300)
	obs := providerallowance.Observation{
		Schema: providerallowance.SchemaObservation, ObservationID: id, ProducerRef: "fixture-producer",
		ProducerIncarnation: "fixture-incarnation-1", Sequence: providerallowance.Count(sequence), BindingGeneration: 0,
		Provider: "openai", Source: providerallowance.Source{Kind: providerallowance.SourceNativeRPC, Version: "fixture-v1", CapabilityRef: "synthetic:not-live"},
		Scope:      providerallowance.Scope{AccountRef: &account, WorkspaceRef: &workspace, Products: []string{"openai"}, BindingState: providerallowance.BindingVerified},
		CapturedAt: "2026-11-02T03:00:00Z", ProviderObservedAt: &observed, Availability: providerallowance.AvailabilityAvailable, Completeness: providerallowance.CompletenessComplete,
		DirectFields: []providerallowance.DirectField{}, Derivations: []providerallowance.Derivation{}, EvidenceRefs: []string{},
		Measures: []providerallowance.Measure{{
			ID: "window", Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
			Window:        providerallowance.Window{Ref: "five-hour-window", DurationMinutes: &minutes, ResetAt: &reset, ResetPrecision: providerallowance.ResetInstant},
			Value:         providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: &decimal},
			ValidationRef: assemblyString("fixture:verified-field-v1"), EvidenceFields: []string{},
		}},
	}
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = providerallowance.DecodeObservation(raw, providerallowance.FixtureParseLimits); err != nil {
		t.Fatal(err)
	}
	return raw
}

type assemblyClock struct{ now time.Time }

func (c *assemblyClock) Now() time.Time { return c.now }

type assemblyWait struct {
	collection *providerallowance.Collection
	first      providerallowance.ReadResponse
	err        error
}

func (w *assemblyWait) Wait(context.Context, time.Duration) error {
	if w.collection == nil {
		w.err = errors.New("collection missing")
		return w.err
	}
	doc, _, err := w.collection.Snapshot(time.Date(2026, 11, 2, 3, 6, 30, 0, time.UTC))
	w.first, w.err = doc, err
	return err
}

type assemblyAcquire struct {
	clock    *assemblyClock
	payloads [][]byte
	calls    int
}

func (a *assemblyAcquire) Acquire(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
	if a.clock != nil {
		a.clock.now = a.clock.now.Add(time.Second)
	}
	raw := a.payloads[len(a.payloads)-1]
	if a.calls < len(a.payloads) {
		raw = a.payloads[a.calls]
	}
	a.calls++
	return providerallowance.Acquisition{Observation: append([]byte(nil), raw...)}, nil
}
