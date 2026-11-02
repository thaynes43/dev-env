package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestAggregateAllowanceDisabledIsClosed(t *testing.T) {
	f := newFixture(t)
	calls := 0
	source := func(context.Context, time.Time) (providerallowance.ReadResponse, error) {
		calls++
		return providerallowance.ReadResponse{}, errors.New("SECRET-TOKEN-CANARY")
	}
	if f.srv.AggregateAllowanceReports.RouteEnabled() {
		t.Fatal("nil reports mounted a route")
	}
	if (&AggregateAllowanceReports{}).RouteEnabled() || (&AggregateAllowanceReports{Enabled: true, Source: source}).RouteEnabled() {
		t.Fatal("incomplete binding mounted a route")
	}
	wrong := &AggregateAllowanceReports{Enabled: true, Source: source, PortalNamespace: "other", PortalServiceAccount: portalReaderAccount}
	if wrong.RouteEnabled() {
		t.Fatal("foreign portal identity mounted a route")
	}
	dup := &AggregateAllowanceReports{Enabled: true, Source: source, SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName, SessionUIDs: []types.UID{"same", "same"}}
	empty := &AggregateAllowanceReports{Enabled: true, Source: source, SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName, SessionUIDs: []types.UID{""}}
	if dup.RouteEnabled() || empty.RouteEnabled() {
		t.Fatal("inexact session allowlist mounted a route")
	}
	f.srv.AggregateAllowanceReports = &AggregateAllowanceReports{Enabled: false, Source: source, SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName, SessionUIDs: []types.UID{"uid"}}
	f.h = f.srv.Handler()
	for _, path := range []string{ProviderAllowanceReportsPath, ProviderAllowanceReportsPath + "/extra", "/v1/provider-allowances/", ProviderSourceReportPath} {
		w := f.do(http.MethodGet, path, tokHuman, nil)
		if w.Code != http.StatusNotFound || calls != 0 || strings.Contains(w.Body.String(), "SECRET-TOKEN-CANARY") {
			t.Fatalf("%s status %d calls %d %s", path, w.Code, calls, w.Body.String())
		}
	}
	if _, _, err := f.srv.AggregateAllowanceReports.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), nil); err == nil || calls != 0 {
		t.Fatal("disabled handle called the getter", err, calls)
	}
}

func TestAggregateAllowanceReadsPreserveSnapshots(t *testing.T) {
	f := newFixture(t)
	token := f.sessionPod("reader", "task", 0)
	foreign := f.sessionPod("foreign", "task", 0)
	uid := f.session("reader").UID
	serving := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	f.now = serving
	f.srv.Now = func() time.Time { return f.now }
	snaps := []providerallowance.ReadResponse{allowanceSnapshot("a"), allowanceSnapshot("b"), allowanceSnapshot("a")}
	var gotNow []time.Time
	calls := 0
	h := &AggregateAllowanceReports{
		Enabled: true, SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName, SessionUIDs: []types.UID{uid},
		Now: func() time.Time { return f.now },
		Source: func(ctx context.Context, now time.Time) (providerallowance.ReadResponse, error) {
			if ctx.Err() != nil {
				return providerallowance.ReadResponse{}, ctx.Err()
			}
			calls++
			gotNow = append(gotNow, now)
			return snaps[len(gotNow)-1], nil
		},
	}
	if !h.RouteEnabled() {
		t.Fatal("exact session binding stayed disabled")
	}
	f.srv.AggregateAllowanceReports = h
	f.h = f.srv.Handler()

	var bodies []providerallowance.ReadResponse
	for i := 0; i < 3; i++ {
		w := f.do(http.MethodGet, ProviderAllowanceReportsPath, token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("read %d status %d %s", i, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), serving.Format(time.RFC3339)) || strings.Contains(w.Body.String(), f.now.Format(time.RFC3339)) {
			t.Fatalf("serving time entered the document %s", w.Body.String())
		}
		got := allowanceDecode(t, w.Body.Bytes())
		want, err := cloneAllowanceRead(snaps[i])
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("read %d changed the snapshot %v", i, err)
		}
		bodies = append(bodies, got)
		f.now = f.now.Add(time.Hour)
	}
	if calls != 3 || len(gotNow) != 3 || !gotNow[0].Equal(serving) || !gotNow[2].Equal(serving.Add(2*time.Hour)) {
		t.Fatalf("serving clock calls %d %#v", calls, gotNow)
	}
	if !reflect.DeepEqual(bodies[0], bodies[2]) || reflect.DeepEqual(bodies[0], bodies[1]) {
		t.Fatal("replay did not keep the first snapshot distinct from the second")
	}
	assertAllowancePreserved(t, bodies[0], "a")
	assertAllowancePreserved(t, bodies[1], "b")
	if strings.Contains(f.do(http.MethodGet, apiv1.SessionPath("reader"), token, nil).Body.String(), "providerallowance.read.v1") {
		t.Fatal("session response carried the aggregate document")
	}
	before := calls
	w := f.do(http.MethodGet, ProviderAllowanceReportsPath, foreign, nil)
	req := httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil)
	req.Header.Set("Authorization", "Bearer "+foreign)
	req.Header.Set("X-Provider-Allowance-Grant", string(uid))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if w.Code != http.StatusForbidden || rec.Code != http.StatusForbidden || calls != before || strings.Contains(w.Body.String(), string(uid)) || strings.Contains(rec.Body.String(), "X-Provider-Allowance-Grant") || strings.Contains(rec.Body.String(), string(uid)) {
		t.Fatalf("foreign grant status %d %d calls %d %s", w.Code, rec.Code, calls, rec.Body.String())
	}
	for _, tok := range []string{"", tokStranger, tokHuman, tokClient} {
		status := http.StatusForbidden
		if tok == "" {
			status = http.StatusUnauthorized
		}
		got := f.do(http.MethodGet, ProviderAllowanceReportsPath, tok, nil)
		if got.Code != status || calls != before {
			t.Fatalf("token %q status %d calls %d", tok, got.Code, calls)
		}
	}
	if f.do(http.MethodGet, ProviderAllowanceReportsPath+"?fresh=1", token, nil).Code != http.StatusBadRequest || calls != before {
		t.Fatal("query was accepted or read the snapshot")
	}
	if f.do(http.MethodGet, ProviderAllowanceReportsPath, token, `{"grant":true}`).Code != http.StatusBadRequest || calls != before {
		t.Fatal("body was accepted or read the snapshot")
	}
	if got := f.do(http.MethodPut, ProviderAllowanceReportsPath, token, nil); got.Code != http.StatusMethodNotAllowed || calls != before {
		t.Fatalf("put status %d", got.Code)
	}
	for _, path := range []string{ProviderAllowanceReportsPath + "/extra", ProviderAllowanceReportsPath + "/", ProviderSourceReportPath, "/v1/provider-source-reports"} {
		if got := f.do(http.MethodGet, path, token, nil); got.Code != http.StatusNotFound || calls != before {
			t.Fatalf("%s status %d calls %d", path, got.Code, calls)
		}
	}
}

func TestAggregateAllowanceIdentityAndClosedErrors(t *testing.T) {
	f := newFixture(t)
	token := f.sessionPod("reader", "task", 0)
	uid := f.session("reader").UID
	calls := 0
	h := &AggregateAllowanceReports{
		Enabled: true, SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName, SessionUIDs: []types.UID{uid},
		Source: func(context.Context, time.Time) (providerallowance.ReadResponse, error) {
			calls++
			return allowanceSnapshot("a"), nil
		},
	}
	f.srv.AggregateAllowanceReports = h
	f.h = f.srv.Handler()
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		if sess, ok := obj.(*v1alpha1.AgentSession); ok && sess.Name == "reader" {
			stamp := metav1.NewTime(f.now)
			sess.DeletionTimestamp = &stamp
		}
		return nil
	}})
	if got := f.do(http.MethodGet, ProviderAllowanceReportsPath, token, nil); got.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("deleting session status %d calls %d %s", got.Code, calls, got.Body.String())
	}

	f = newFixture(t)
	token = f.sessionPod("reader", "task", 0)
	uid = f.session("reader").UID
	calls = 0
	h.SessionUIDs = []types.UID{uid}
	h.Source = func(context.Context, time.Time) (providerallowance.ReadResponse, error) {
		calls++
		return allowanceSnapshot("a"), nil
	}
	f.srv.AggregateAllowanceReports = h
	f.h = f.srv.Handler()
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		if pod, ok := obj.(*corev1.Pod); ok && pod.Name == "reader" {
			pod.UID = "replaced-pod"
		}
		return nil
	}})
	if got := f.do(http.MethodGet, ProviderAllowanceReportsPath, token, nil); got.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("replaced pod status %d calls %d %s", got.Code, calls, got.Body.String())
	}

	f = newFixture(t)
	token = f.sessionPod("reader", "task", 0)
	uid = f.session("reader").UID
	f.srv.AggregateAllowanceReports = &AggregateAllowanceReports{
		Enabled: true, SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName, SessionUIDs: []types.UID{uid},
		Source: func(context.Context, time.Time) (providerallowance.ReadResponse, error) {
			calls++
			return providerallowance.ReadResponse{}, errors.New("SECRET-TOKEN-CANARY")
		},
	}
	f.h = f.srv.Handler()
	got := f.do(http.MethodGet, ProviderAllowanceReportsPath, token, nil)
	if got.Code != http.StatusServiceUnavailable || strings.Contains(got.Body.String(), "SECRET-TOKEN-CANARY") || strings.Contains(got.Body.String(), token) {
		t.Fatalf("canary leaked %d %s", got.Code, got.Body.String())
	}
	f.srv.AggregateAllowanceReports.Source = func(context.Context, time.Time) (providerallowance.ReadResponse, error) {
		calls++
		return providerallowance.ReadResponse{Schema: "SECRET-DOC-CANARY", SnapshotRevision: "bad", ServedAt: "2026-10-28T12:00:00Z"}, nil
	}
	got = f.do(http.MethodGet, ProviderAllowanceReportsPath, token, nil)
	if got.Code != http.StatusServiceUnavailable || strings.Contains(got.Body.String(), "SECRET-DOC-CANARY") {
		t.Fatalf("invalid document leaked %d %s", got.Code, got.Body.String())
	}
	f.srv.AggregateAllowanceReports.Source = func(context.Context, time.Time) (providerallowance.ReadResponse, error) {
		calls++
		return providerallowance.ReadResponse{Schema: providerallowance.SchemaRead, SnapshotRevision: strings.Repeat("SECRET-SIZE-CANARY", 20000), ServedAt: "2026-10-28T12:00:00Z"}, nil
	}
	got = f.do(http.MethodGet, ProviderAllowanceReportsPath, token, nil)
	if got.Code != http.StatusRequestEntityTooLarge || strings.Contains(got.Body.String(), "SECRET-SIZE-CANARY") {
		t.Fatalf("oversized document leaked %d %s", got.Code, got.Body.String())
	}
}

func TestAggregateAllowancePortalBoundary(t *testing.T) {
	f := newFixture(t)
	token := f.sessionPod("reader", "task", 0)
	uid := f.session("reader").UID
	calls := 0
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: portalReaderNamespace, UID: "portal-pod-uid"}, Spec: corev1.PodSpec{ServiceAccountName: portalReaderAccount, Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	h := &AggregateAllowanceReports{
		Enabled: true, SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName, SessionUIDs: []types.UID{uid},
		PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount,
		Source: func(context.Context, time.Time) (providerallowance.ReadResponse, error) {
			calls++
			return allowanceSnapshot("a"), nil
		},
	}
	f.srv.Policy.PortalReaderEnabled = true
	f.srv.AggregateAllowanceReports = h
	f.h = f.srv.Handler()
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-portal"] = id
	if !PortalAggregateAllowanceGET(h.RouteEnabled(), http.MethodGet, ProviderAllowanceReportsPath) || PortalAggregateAllowanceGET(h.RouteEnabled(), http.MethodPut, ProviderAllowanceReportsPath) || PortalAggregateAllowanceGET(false, http.MethodGet, ProviderAllowanceReportsPath) || PortalAggregateAllowanceGET(true, http.MethodGet, ProviderSourceReportPath) {
		t.Fatal("portal aggregate gate widened")
	}
	got := f.do(http.MethodGet, ProviderAllowanceReportsPath, "tok-portal", nil)
	if got.Code != http.StatusOK || calls != 1 || strings.Contains(got.Body.String(), "tok-portal") {
		t.Fatalf("portal route status %d calls %d %s", got.Code, calls, got.Body.String())
	}
	if put := f.do(http.MethodPut, ProviderAllowanceReportsPath, "tok-portal", nil); put.Code != http.StatusMethodNotAllowed || calls != 1 {
		t.Fatalf("portal put status %d calls %d", put.Code, calls)
	}
	if unrelated := f.do(http.MethodGet, apiv1.FleetPath, "tok-portal", nil); unrelated.Code != http.StatusForbidden || !strings.Contains(unrelated.Body.String(), "may only list session summaries") || calls != 1 {
		t.Fatalf("unrelated portal route %d %s", unrelated.Code, unrelated.Body.String())
	}
	status, body, err := h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), &caller{kind: kindPortalReader, identity: id, pod: pod})
	if err != nil || status != http.StatusOK || body == nil || calls != 2 {
		t.Fatalf("direct portal handle %d %v %d", status, err, calls)
	}
	foreign := id
	foreign.ServiceAccount = "dev-env-v2-portal-other"
	if _, _, err = h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), &caller{kind: kindPortalReader, identity: foreign, pod: pod}); err == nil || calls != 2 {
		t.Fatal("foreign portal identity was accepted", calls)
	}
	deleting := pod.DeepCopy()
	stamp := metav1.NewTime(f.now)
	deleting.DeletionTimestamp = &stamp
	if _, _, err = h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), &caller{kind: kindPortalReader, identity: id, pod: deleting}); err == nil || calls != 2 {
		t.Fatal("deleting portal pod was accepted", calls)
	}
	replaced := id
	replaced.PodUID = "replaced-portal"
	if _, _, err = h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), &caller{kind: kindPortalReader, identity: replaced, pod: pod}); err == nil || calls != 2 {
		t.Fatal("replaced portal pod was accepted", calls)
	}
	if f.do(http.MethodGet, ProviderAllowanceReportsPath, token, nil).Code != http.StatusOK || calls != 3 {
		t.Fatal("allowlisted session read changed", calls)
	}
}

func TestCloneAllowanceReadDoesNotAlias(t *testing.T) {
	doc := allowanceSnapshot("a")
	observed := "2026-10-28T10:00:00Z"
	doc.Items[0].Observation.ProviderObservedAt = &observed
	cloned, err := cloneAllowanceRead(doc)
	if err != nil {
		t.Fatal(err)
	}
	observed = "changed"
	if cloned.Items[0].Observation.ProviderObservedAt == nil || *cloned.Items[0].Observation.ProviderObservedAt != "2026-10-28T10:00:00Z" {
		t.Fatal("clone shared provider time")
	}
	doc.SourceStatuses[0].Freshness.State = providerallowance.FreshFresh
	if cloned.SourceStatuses[0].Freshness.State != providerallowance.FreshStale {
		t.Fatal("clone shared freshness")
	}
}

func allowanceDecode(t *testing.T, raw []byte) providerallowance.ReadResponse {
	t.Helper()
	doc, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatalf("decode %v %s", err, raw)
	}
	return doc
}

func assertAllowancePreserved(t *testing.T, doc providerallowance.ReadResponse, kind string) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	switch kind {
	case "a":
		if doc.ServedAt != "2026-10-28T12:00:00Z" || doc.Items[0].ReceivedAt != "2026-10-28T11:00:01Z" || doc.Items[0].Observation.CapturedAt != "2026-10-28T11:00:00Z" || doc.Items[0].Observation.ProviderObservedAt == nil || *doc.Items[0].Observation.ProviderObservedAt != "2026-10-28T10:00:00Z" {
			t.Fatalf("times changed %+v", doc.Items[0])
		}
		if doc.Items[0].Freshness.State != providerallowance.FreshStale || doc.Items[0].Freshness.AsOf != "2026-10-28T10:00:00Z" {
			t.Fatalf("freshness refreshed %+v", doc.Items[0].Freshness)
		}
		if len(doc.Items[0].Observation.Measures) != 2 || doc.Items[0].Observation.Measures[0].Window.ResetPrecision != providerallowance.ResetDisplayOnly || doc.Items[0].Observation.Measures[1].Window.ResetPrecision != providerallowance.ResetInstant {
			t.Fatalf("reset precision changed %+v", doc.Items[0].Observation.Measures)
		}
		if doc.Items[0].Observation.Measures[0].Window.ResetAt == nil || *doc.Items[0].Observation.Measures[0].Window.ResetAt != "2026-11-04T00:00:00Z" || doc.Items[0].Observation.Measures[1].Window.ResetAt == nil || *doc.Items[0].Observation.Measures[1].Window.ResetAt != "2026-10-28T17:00:00Z" {
			t.Fatal("reset instants changed")
		}
		if *doc.Items[0].Observation.Measures[0].Value.Decimal != "56" || *doc.Items[0].Observation.Measures[1].Value.Decimal != "40" || strings.Contains(text, `"decimal":"96"`) || strings.Contains(text, `"provider":"cursor"`) {
			t.Fatal("percents were summed or cursor was invented", text)
		}
		if len(doc.SourceStatuses) != 2 || doc.SourceStatuses[1].Provider != "openai" || doc.SourceStatuses[1].Availability != providerallowance.AvailabilityUnavailable || doc.SourceStatuses[1].Freshness.State != providerallowance.FreshUnknown || doc.SourceStatuses[1].LastGood != nil {
			t.Fatalf("openai status changed %+v", doc.SourceStatuses)
		}
		if doc.SourceStatuses[0].LastGood == nil || doc.SourceStatuses[0].LastGood.CapturedAt != "2026-10-28T11:00:00Z" || doc.SourceStatuses[0].LastGood.ProviderObservedAt == nil || *doc.SourceStatuses[0].LastGood.ProviderObservedAt != "2026-10-28T10:00:00Z" || doc.SourceStatuses[0].LastAttempt.Code == nil || *doc.SourceStatuses[0].LastAttempt.Code != "partial_capture" {
			t.Fatalf("partial capture changed %+v", doc.SourceStatuses[0])
		}
	case "b":
		if doc.ServedAt != "2026-10-29T00:00:00Z" || doc.Items[0].ReceivedAt != "2026-10-29T00:00:02Z" || doc.Items[0].Observation.CapturedAt != "2026-10-29T00:00:01Z" || doc.Items[0].Observation.ProviderObservedAt == nil || *doc.Items[0].Observation.ProviderObservedAt != "2099-11-04T00:00:00Z" {
			t.Fatalf("future times changed %+v", doc.Items[0])
		}
		if doc.Items[0].Freshness.State != providerallowance.FreshUnknown || len(doc.Items[0].Observation.Measures) != 0 || len(doc.SourceStatuses) != 1 {
			t.Fatalf("future snapshot gained an entitlement %+v", doc)
		}
		if strings.Contains(text, `"decimal"`) || strings.Contains(text, `"provider":"cursor"`) || strings.Contains(text, `"provider":"openai"`) {
			t.Fatal("missing providers were given a value", text)
		}
	default:
		t.Fatal(kind)
	}
}

func allowanceSnapshot(kind string) providerallowance.ReadResponse {
	if kind == "b" {
		observed := "2099-11-04T00:00:00Z"
		return providerallowance.ReadResponse{
			Schema: providerallowance.SchemaRead, SnapshotRevision: "snap-b", ServedAt: "2026-10-29T00:00:00Z",
			Items:          []providerallowance.ReadItem{allowanceItem("snap-b-item", "2026-10-29T00:00:01Z", observed, "2026-10-29T00:00:02Z", providerallowance.FreshUnknown, providerallowance.BasisNone, nil)},
			SourceStatuses: []providerallowance.SourceStatus{allowanceKnownStatus("anthropic", providerallowance.AvailabilityUnsupported, "2026-10-29T00:00:01Z", observed, "2026-10-29T00:00:02Z", providerallowance.FreshUnknown, "future_observation")},
		}
	}
	observed := "2026-10-28T10:00:00Z"
	resetDisplay := "2026-11-04T00:00:00Z"
	resetInstant := "2026-10-28T17:00:00Z"
	percentWeekly := "56"
	percentSession := "40"
	minutesWeek := providerallowance.Count(10080)
	minutesSession := providerallowance.Count(300)
	ref := "fixture:synthetic-allowance"
	measures := []providerallowance.Measure{
		{ID: "weekly-display", Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionRemaining, Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: &percentWeekly}, Window: providerallowance.Window{Ref: "weekly", DurationMinutes: &minutesWeek, ResetAt: &resetDisplay, ResetPrecision: providerallowance.ResetDisplayOnly}, ValidationRef: &ref},
		{ID: "session-window", Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionRemaining, Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: &percentSession}, Window: providerallowance.Window{Ref: "session", DurationMinutes: &minutesSession, ResetAt: &resetInstant, ResetPrecision: providerallowance.ResetInstant}, ValidationRef: &ref},
	}
	item := allowanceItem("snap-a-item", "2026-10-28T11:00:00Z", observed, "2026-10-28T11:00:01Z", providerallowance.FreshStale, providerallowance.BasisProvider, measures)
	return providerallowance.ReadResponse{
		Schema: providerallowance.SchemaRead, SnapshotRevision: "snap-a", ServedAt: "2026-10-28T12:00:00Z",
		Items:          []providerallowance.ReadItem{item},
		SourceStatuses: []providerallowance.SourceStatus{allowanceKnownStatus("anthropic", providerallowance.AvailabilityPartial, "2026-10-28T11:00:00Z", observed, "2026-10-28T11:00:01Z", providerallowance.FreshStale, "partial_capture"), allowanceAbsentStatus("openai")},
	}
}

func allowanceItem(id, captured, observed, received string, fresh providerallowance.FreshnessState, basis providerallowance.FreshnessBasis, measures []providerallowance.Measure) providerallowance.ReadItem {
	observedCopy := observed
	return providerallowance.ReadItem{
		Observation: providerallowance.Observation{
			Schema: providerallowance.SchemaObservation, ObservationID: id, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation",
			Sequence: 1, Provider: "anthropic",
			Source:             providerallowance.Source{Kind: providerallowance.SourceNativeDisplay, Version: "fixture-v1"},
			Scope:              providerallowance.Scope{BindingState: providerallowance.BindingUnknown},
			CapturedAt:         captured,
			ProviderObservedAt: &observedCopy,
			Availability:       providerallowance.AvailabilityPartial, Completeness: providerallowance.CompletenessPartial,
			Measures: measures,
		},
		ReceivedAt: received,
		Validation: providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified},
		Binding:    providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
		Freshness:  providerallowance.Freshness{State: fresh, AsOf: observed, Basis: basis, Reason: "provider time preserved"},
	}
}

func allowanceKnownStatus(provider string, availability providerallowance.Availability, captured, observed, received string, fresh providerallowance.FreshnessState, code string) providerallowance.SourceStatus {
	observedCopy := observed
	codeCopy := code
	completed := received
	return providerallowance.SourceStatus{
		Provider: provider, ProducerRef: "fixture-producer", Availability: availability,
		LastGood:    &providerallowance.LastGood{ObservationID: "good-" + provider, CapturedAt: captured, ProviderObservedAt: &observedCopy, ReceivedAt: received},
		LastAttempt: providerallowance.LastAttempt{AttemptedAt: captured, CompletedAt: &completed, Result: providerallowance.AttemptError, Code: &codeCopy},
		Freshness:   providerallowance.Freshness{State: fresh, AsOf: observed, Basis: providerallowance.BasisProvider, Reason: "status preserved"},
		Validation:  providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified},
		Binding:     providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
	}
}

func allowanceAbsentStatus(provider string) providerallowance.SourceStatus {
	code := "source_not_reported"
	return providerallowance.SourceStatus{
		Provider: provider, ProducerRef: "fixture-producer", Availability: providerallowance.AvailabilityUnavailable,
		LastAttempt: providerallowance.LastAttempt{AttemptedAt: "2026-10-28T11:00:00Z", Result: providerallowance.AttemptUnknown, Code: &code},
		Freshness:   providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: "2026-10-28T11:00:00Z", Basis: providerallowance.BasisNone, Reason: "no measurement"},
		Validation:  providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified},
		Binding:     providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
	}
}
