package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// The slot documents and session readings here are synthetic
// (DESIGN-059 step 6). No value comes from a provider.

// slotDouble is the common slot in one fixed state.
type slotDouble struct {
	view  AggregateAllowanceView
	err   error
	reads int
	// trusted is the store's trust; nil trusts nothing.
	trusted *AggregateAllowanceTrust
}

func (s *slotDouble) Read(context.Context) (AggregateAllowanceView, error) {
	s.reads++
	return s.view, s.err
}
func (s *slotDouble) Put(context.Context, []byte, time.Time) error { return errors.New("no put") }
func (s *slotDouble) configured() bool                             { return true }
func (s *slotDouble) trust() AggregateAllowanceTrust {
	if s.trusted != nil {
		return *s.trusted
	}
	return AggregateAllowanceTrust{MaxAge: 30 * time.Minute}
}

// sessionsDouble returns fixed readings.
type sessionsDouble struct {
	readings map[string]StoredSessionReading
	err      error
}

func (s sessionsDouble) Newest(context.Context) (map[string]StoredSessionReading, error) {
	return s.readings, s.err
}

var routeNow = time.Date(2026, 11, 10, 0, 30, 0, 0, time.UTC)

func routeReading(agent string, readAt time.Time, used string) StoredSessionReading {
	source := protocol.AllowanceSourceOf(agent)
	return StoredSessionReading{Session: "dev-env-1110-000930", Source: source, ReadAt: readAt, ReportedAt: readAt.Add(time.Minute),
		Windows: []protocol.AllowanceWindow{{Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: used, ResetsAt: readAt.Add(40 * time.Hour).Truncate(time.Second)}}}
}

func portalCaller() *caller {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: portalReaderNamespace, UID: "portal-pod-uid"}, Spec: corev1.PodSpec{ServiceAccountName: portalReaderAccount}}
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	return &caller{kind: kindPortalReader, identity: id, pod: pod}
}

// storedHandler is a store-mode route over slot, with sessions when set.
func storedHandler(t *testing.T, slot *slotDouble, sessions sessionAllowanceReader) *AggregateAllowanceReports {
	t.Helper()
	f := newFixture(t)
	h := &AggregateAllowanceReports{Enabled: true, Store: slot, Live: f.c,
		SessionNamespace: sessionNS, PublisherSessionUID: "publisher-uid", PublisherServiceAccount: "publisher",
		PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount,
		Sessions: sessions, Now: func() time.Time { return routeNow }}
	if !h.RouteEnabled() || !h.storeMode() {
		t.Fatal("the store-mode route is off")
	}
	return h
}

// get answers one portal GET: its status and decoded document.
func get(t *testing.T, h *AggregateAllowanceReports) (int, providerallowance.ReadResponse, error) {
	t.Helper()
	status, body, err := h.Handle(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProviderAllowanceReportsPath, nil), portalCaller())
	if err != nil {
		return 0, providerallowance.ReadResponse{}, err
	}
	raw, merr := json.Marshal(body)
	if merr != nil {
		t.Fatal(merr)
	}
	doc, derr := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if derr != nil {
		t.Fatal(derr)
	}
	return status, doc, nil
}

func itemOf(doc providerallowance.ReadResponse, provider string) []providerallowance.ReadItem {
	var out []providerallowance.ReadItem
	for _, it := range doc.Items {
		if it.Observation.Provider == provider {
			out = append(out, it)
		}
	}
	return out
}

func statusOf(doc providerallowance.ReadResponse, provider string) []providerallowance.SourceStatus {
	var out []providerallowance.SourceStatus
	for _, s := range doc.SourceStatuses {
		if s.Provider == provider {
			out = append(out, s)
		}
	}
	return out
}

func slotStates(t *testing.T) map[string]*slotDouble {
	t.Helper()
	raw, err := json.Marshal(allowanceSnapshot("a"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]*slotDouble{
		"found":      {view: AggregateAllowanceView{Found: true, Raw: raw}},
		"missing":    {view: AggregateAllowanceView{}},
		"suppressed": {view: AggregateAllowanceView{Found: true, Suppressed: true}},
		"unreadable": {view: AggregateAllowanceView{Found: true, Raw: []byte("{")}},
		"error":      {err: ErrAggregateUnavailable},
	}
}

// Off, or on with no readings, every slot state is answered exactly as before.
func TestSessionAllowanceRouteUnchangedWithoutReadings(t *testing.T) {
	for name := range slotStates(t) {
		t.Run(name, func(t *testing.T) {
			before, wantErr := func() (any, error) {
				status, doc, err := get(t, storedHandler(t, slotStates(t)[name], nil))
				return [2]any{status, doc}, err
			}()
			for _, sessions := range []sessionAllowanceReader{sessionsDouble{readings: map[string]StoredSessionReading{}}, sessionsDouble{err: errSessionAllowanceStore}} {
				status, doc, err := get(t, storedHandler(t, slotStates(t)[name], sessions))
				a, _ := json.Marshal(before)
				b, _ := json.Marshal([2]any{status, doc})
				if (err == nil) != (wantErr == nil) || string(a) != string(b) {
					t.Fatalf("changed: %v %s\nwas: %v %s", err, b, wantErr, a)
				}
				if err != nil && err.Error() != wantErr.Error() {
					t.Fatalf("error %v, was %v", err, wantErr)
				}
			}
		})
	}
}

// With a reading, every slot state serves it: an expired, suppressed,
// missing or unreadable slot does not hide it.
func TestSessionAllowanceRouteServesReadingWhateverTheSlot(t *testing.T) {
	readings := map[string]StoredSessionReading{protocol.AgentCodex: routeReading(protocol.AgentCodex, routeNow.Add(-10*time.Minute), "65")}
	for name, slot := range slotStates(t) {
		t.Run(name, func(t *testing.T) {
			status, doc, err := get(t, storedHandler(t, slot, sessionsDouble{readings: readings}))
			if err != nil || status != http.StatusOK {
				t.Fatalf("%d %v", status, err)
			}
			items := itemOf(doc, "openai")
			if len(items) != 1 || !providerallowance.SessionReadingSource(items[0].Observation.Source) || *items[0].Observation.Measures[0].Value.Decimal != "65" {
				t.Fatalf("openai items %+v", items)
			}
			if items[0].Binding.State != providerallowance.BindingUnknown || items[0].Validation.State != providerallowance.ValidationDisplayOnly {
				t.Fatalf("labels %+v %+v", items[0].Binding, items[0].Validation)
			}
			if s := statusOf(doc, "openai"); len(s) != 1 || s[0].ProducerRef != providerallowance.SessionProducerRef {
				t.Fatalf("openai status %+v", s)
			}
			if name == "found" {
				// The Claude slot item is untouched beside it.
				if c := itemOf(doc, "anthropic"); len(c) != 1 || c[0].Observation.ObservationID != "snap-a-item" {
					t.Fatalf("anthropic %+v", c)
				}
			}
		})
	}
}

func TestSessionAllowanceRouteNewerWins(t *testing.T) {
	// A trusted Codex slot item, measured 2026-11-02T02:59:00Z, beside a
	// Claude session reading: the trusted item stays trusted.
	_, trustedDoc := assemblyCaptures(t)
	raw, err := json.Marshal(trustedDoc)
	if err != nil {
		t.Fatal(err)
	}
	trust := repairAllowanceStore(&slotMem{}).trust()
	slotMeasured := time.Date(2026, 11, 2, 2, 59, 0, 0, time.UTC)
	serving := time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		readAt      time.Time
		wantSession bool
	}{
		"newer reading replaces the slot item": {slotMeasured.Add(time.Minute), true},
		"older reading keeps the slot item":    {slotMeasured.Add(-time.Hour), false},
		"same time keeps the slot item":        {slotMeasured, false},
	} {
		t.Run(name, func(t *testing.T) {
			slot := &slotDouble{view: AggregateAllowanceView{Found: true, Raw: raw}, trusted: &trust}
			readings := map[string]StoredSessionReading{
				protocol.AgentCodex:  routeReading(protocol.AgentCodex, c.readAt, "34"),
				protocol.AgentClaude: routeReading(protocol.AgentClaude, serving.Add(-time.Minute), "12"),
			}
			h := storedHandler(t, slot, sessionsDouble{readings: readings})
			h.Now = func() time.Time { return serving }
			status, doc, err := get(t, h)
			if err != nil || status != http.StatusOK {
				t.Fatalf("%d %v", status, err)
			}
			items, statuses := itemOf(doc, "openai"), statusOf(doc, "openai")
			if len(items) != 1 || len(statuses) != 1 {
				t.Fatalf("items %d statuses %d", len(items), len(statuses))
			}
			isSession := providerallowance.SessionReadingSource(items[0].Observation.Source)
			if isSession != c.wantSession || (statuses[0].ProducerRef == providerallowance.SessionProducerRef) != c.wantSession {
				t.Fatalf("session item %v, status %s", isSession, statuses[0].ProducerRef)
			}
			if !c.wantSession && (items[0].Validation.State != providerallowance.ValidationSourceFields || items[0].Binding.State != providerallowance.BindingVerified) {
				t.Fatalf("the trusted slot item lost its trust beside a session reading: %+v %+v", items[0].Validation, items[0].Binding)
			}
			if claude := itemOf(doc, "anthropic"); len(claude) != 1 || !providerallowance.SessionReadingSource(claude[0].Observation.Source) {
				t.Fatalf("anthropic %+v", claude)
			}
		})
	}
	// A slot item the projection rejects never hides a reading, even an older one.
	t.Run("rejected slot item", func(t *testing.T) {
		slot := slotStates(t)["found"]
		slotTime := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
		_, doc, err := get(t, storedHandler(t, slot, sessionsDouble{readings: map[string]StoredSessionReading{protocol.AgentClaude: routeReading(protocol.AgentClaude, slotTime.Add(-time.Hour), "34")}}))
		if err != nil {
			t.Fatal(err)
		}
		if items := itemOf(doc, "anthropic"); len(items) != 1 || !providerallowance.SessionReadingSource(items[0].Observation.Source) || doc.SnapshotRevision == "snap-a" {
			t.Fatalf("anthropic %+v rev %q", items, doc.SnapshotRevision)
		}
	})
}

// Without a slot selection the route is GET-only for the portal reader and
// serves the readings alone.
func TestSessionAllowanceRouteSessionsOnly(t *testing.T) {
	f := newFixture(t)
	reader := f.sessionPod("reader", "task", 0)
	h := &AggregateAllowanceReports{Enabled: true, PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount,
		Sessions: sessionsDouble{readings: map[string]StoredSessionReading{protocol.AgentClaude: routeReading(protocol.AgentClaude, routeNow.Add(-time.Minute), "34")}},
		Now:      func() time.Time { return routeNow }}
	if !h.RouteEnabled() || h.PublishEnabled() || h.storeMode() {
		t.Fatal("sessions-only route mode")
	}
	status, doc, err := get(t, h)
	if err != nil || status != http.StatusOK || len(doc.Items) != 1 || doc.Items[0].Observation.Provider != "anthropic" {
		t.Fatalf("%d %v %+v", status, err, doc.Items)
	}
	if s := statusOf(doc, "openai"); len(s) != 1 || s[0].Availability != providerallowance.AvailabilityUnconfigured {
		t.Fatalf("openai %+v", s)
	}
	// No portal binding, or no reader: the route stays off.
	for _, off := range []*AggregateAllowanceReports{
		{Enabled: true, Sessions: h.Sessions},
		{Enabled: false, Sessions: h.Sessions, PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount},
		{Enabled: true, PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount},
	} {
		if off.RouteEnabled() {
			t.Fatalf("enabled: %+v", off)
		}
	}
	// No reading: the empty slot, as an unconfigured route would show it.
	h.Sessions = sessionsDouble{readings: map[string]StoredSessionReading{}}
	if _, doc, err := get(t, h); err != nil || len(doc.Items) != 0 || doc.SnapshotRevision != "aggregate-unconfigured" {
		t.Fatalf("%v %+v", err, doc)
	}
	// Through the server: a session pod is refused and PUT is not served.
	f.srv.Policy.PortalReaderEnabled = true
	f.srv.AggregateAllowanceReports = h
	f.h = f.srv.Handler()
	if w := f.do(http.MethodGet, ProviderAllowanceReportsPath, reader, nil); w.Code != http.StatusForbidden {
		t.Fatalf("a session reader: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPut, ProviderAllowanceReportsPath, reader, `{}`); w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusForbidden {
		t.Fatalf("put: %d", w.Code)
	}
}

// End to end: a heartbeat's reading is stored, and the route serves it.
func TestSessionAllowanceHeartbeatToRoute(t *testing.T) {
	f := newFixture(t, sessionAllowanceConfigMap())
	tok := allowanceSession(t, f, sessionAllowanceTestName, "codex", "task")
	store := enableSessionAllowances(f)
	beatWith(t, f, sessionAllowanceTestName, tok, codexReading(f.now.Add(-time.Minute), "65", f.now.Add(30*time.Hour)))
	h := &AggregateAllowanceReports{Enabled: true, PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount, Sessions: store, Now: func() time.Time { return f.now }}
	status, doc, err := get(t, h)
	if err != nil || status != http.StatusOK {
		t.Fatalf("%d %v", status, err)
	}
	items := itemOf(doc, "openai")
	if len(items) != 1 || *items[0].Observation.Measures[0].Value.Decimal != "65" || items[0].Observation.CapturedAt != f.now.Add(-time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("served %+v", items)
	}
}
