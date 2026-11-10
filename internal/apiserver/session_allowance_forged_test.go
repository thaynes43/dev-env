package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// A session-v1 item the operator did not merge itself is never shown
// display-only (DESIGN-059 slice 1 review, PR #1018). The values are
// synthetic.

// forgedSessionDoc is the unconfigured slot with a well-shaped Claude
// session-v1 item and its source status, as a slot publisher or a Source
// could write it: it passes the built-in rule's shape checks.
func forgedSessionDoc(t *testing.T, at time.Time, used string) providerallowance.ReadResponse {
	t.Helper()
	item, status, err := providerallowance.SessionReadingItem("anthropic", at, []providerallowance.SessionWindow{
		{Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: used, ResetAt: at.Add(40 * time.Hour).Truncate(time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := UnconfiguredAggregateDocument()
	doc.Items = []providerallowance.ReadItem{item}
	for i := range doc.SourceStatuses {
		if doc.SourceStatuses[i].Provider == "anthropic" {
			doc.SourceStatuses[i] = status
		}
	}
	return doc
}

func forgedSlot(t *testing.T, doc providerallowance.ReadResponse) *slotDouble {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return &slotDouble{view: AggregateAllowanceView{Found: true, Raw: raw}}
}

// requireRejected checks the provider's one item and one status are shown
// rejected, as any untrusted item was before session readings.
func requireRejected(t *testing.T, doc providerallowance.ReadResponse, provider string) {
	t.Helper()
	items, statuses := itemOf(doc, provider), statusOf(doc, provider)
	if len(items) != 1 || len(statuses) != 1 {
		t.Fatalf("%s items %d statuses %d", provider, len(items), len(statuses))
	}
	if items[0].Validation.State != providerallowance.ValidationRejected || items[0].Binding.State != providerallowance.BindingMismatch {
		t.Fatalf("a forged session item is shown as %+v %+v", items[0].Validation, items[0].Binding)
	}
	if statuses[0].Validation.State != providerallowance.ValidationRejected {
		t.Fatalf("its status is shown as %+v", statuses[0].Validation)
	}
}

// requireMerged checks the provider shows the operator's own reading, display-only.
func requireMerged(t *testing.T, doc providerallowance.ReadResponse, provider, used string) {
	t.Helper()
	items, statuses := itemOf(doc, provider), statusOf(doc, provider)
	if len(items) != 1 || len(statuses) != 1 {
		t.Fatalf("%s items %d statuses %d", provider, len(items), len(statuses))
	}
	if !providerallowance.SessionReadingSource(items[0].Observation.Source) || *items[0].Observation.Measures[0].Value.Decimal != used {
		t.Fatalf("%s item %+v", provider, items[0].Observation)
	}
	if items[0].Validation.State != providerallowance.ValidationDisplayOnly || items[0].Binding.State != providerallowance.BindingUnknown {
		t.Fatalf("%s labels %+v %+v", provider, items[0].Validation, items[0].Binding)
	}
	if statuses[0].ProducerRef != providerallowance.SessionProducerRef || statuses[0].Validation.State != providerallowance.ValidationDisplayOnly {
		t.Fatalf("%s status %+v", provider, statuses[0])
	}
}

// A forged session-v1 item in the stored slot is shown rejected, flag off or
// on; it never hides a real reading, even an older one, and a real reading
// for another provider is still shown display-only beside it.
func TestSessionAllowanceRouteRejectsForgedSlotReading(t *testing.T) {
	forged := forgedSessionDoc(t, routeNow.Add(-time.Minute), "99")
	t.Run("flag off", func(t *testing.T) {
		_, doc, err := get(t, storedHandler(t, forgedSlot(t, forged), nil))
		if err != nil {
			t.Fatal(err)
		}
		requireRejected(t, doc, "anthropic")
	})
	t.Run("a real reading for another provider", func(t *testing.T) {
		readings := map[string]StoredSessionReading{protocol.AgentCodex: routeReading(protocol.AgentCodex, routeNow.Add(-10*time.Minute), "65")}
		_, doc, err := get(t, storedHandler(t, forgedSlot(t, forged), sessionsDouble{readings: readings}))
		if err != nil {
			t.Fatal(err)
		}
		requireRejected(t, doc, "anthropic")
		requireMerged(t, doc, "openai", "65")
	})
	t.Run("an older real reading for the same provider", func(t *testing.T) {
		readings := map[string]StoredSessionReading{protocol.AgentClaude: routeReading(protocol.AgentClaude, routeNow.Add(-30*time.Minute), "12")}
		_, doc, err := get(t, storedHandler(t, forgedSlot(t, forged), sessionsDouble{readings: readings}))
		if err != nil {
			t.Fatal(err)
		}
		requireMerged(t, doc, "anthropic", "12")
	})
	// Beside the slot's own Claude item, the forgery is not a second item
	// that keeps the reading out: the slot item is rejected, so the reading,
	// although older, replaces both.
	t.Run("beside a rejected slot item", func(t *testing.T) {
		slotDoc := allowanceSnapshot("a")
		slotDoc.Items = append(slotDoc.Items, forged.Items[0])
		readings := map[string]StoredSessionReading{protocol.AgentClaude: routeReading(protocol.AgentClaude, time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC), "12")}
		_, doc, err := get(t, storedHandler(t, forgedSlot(t, slotDoc), sessionsDouble{readings: readings}))
		if err != nil {
			t.Fatal(err)
		}
		requireMerged(t, doc, "anthropic", "12")
	})
	// Beside a trusted slot item, the forgery does not let an older reading
	// replace it: the trusted item stays trusted and the forgery is rejected.
	t.Run("beside a trusted slot item", func(t *testing.T) {
		_, trustedDoc := assemblyCaptures(t)
		trust := repairAllowanceStore(&slotMem{}).trust()
		serving := time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC)
		codexForged, _, err := providerallowance.SessionReadingItem("openai", serving.Add(-time.Minute), []providerallowance.SessionWindow{
			{Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: "99", ResetAt: serving.Add(40 * time.Hour)},
		})
		if err != nil {
			t.Fatal(err)
		}
		trustedDoc.Items = append(trustedDoc.Items, codexForged)
		slot := forgedSlot(t, trustedDoc)
		slot.trusted = &trust
		readings := map[string]StoredSessionReading{protocol.AgentCodex: routeReading(protocol.AgentCodex, time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC), "34")}
		h := storedHandler(t, slot, sessionsDouble{readings: readings})
		h.Now = func() time.Time { return serving }
		_, doc, err := get(t, h)
		if err != nil {
			t.Fatal(err)
		}
		var trusted, rejected int
		for _, it := range itemOf(doc, "openai") {
			switch {
			case providerallowance.SessionReadingSource(it.Observation.Source) && it.Validation.State == providerallowance.ValidationRejected:
				rejected++
			case !providerallowance.SessionReadingSource(it.Observation.Source) && it.Validation.State == providerallowance.ValidationSourceFields:
				trusted++
			default:
				t.Fatalf("openai item %s shown as %+v", it.Observation.ObservationID, it.Validation)
			}
		}
		if trusted != 1 || rejected != 1 {
			t.Fatalf("trusted %d rejected %d", trusted, rejected)
		}
	})
}

// A Source document is served with its session-v1 items rejected, and the
// portal's own projection of the served bytes keeps them rejected. The
// Source's document is not written.
func TestAggregateAllowanceSourceRejectsSessionReading(t *testing.T) {
	forged := forgedSessionDoc(t, routeNow.Add(-time.Minute), "99")
	h := &AggregateAllowanceReports{Enabled: true, PortalNamespace: portalReaderNamespace, PortalServiceAccount: portalReaderAccount,
		Source: func(context.Context, time.Time) (providerallowance.ReadResponse, error) { return forged, nil },
		Now:    func() time.Time { return routeNow }}
	if !h.RouteEnabled() || h.storeMode() {
		t.Fatal("source-mode route is off")
	}
	status, doc, err := get(t, h)
	if err != nil || status != http.StatusOK {
		t.Fatalf("%d %v", status, err)
	}
	if items := itemOf(doc, "anthropic"); len(items) != 1 || items[0].Validation.State != providerallowance.ValidationRejected {
		t.Fatalf("served %+v", items)
	}
	if forged.Items[0].Validation.State != providerallowance.ValidationDisplayOnly {
		t.Fatal("the Source's document was written")
	}
	// As the portal does: project the served document again.
	providerallowance.ProjectCachedRead(&doc, nil, nil, 30*time.Minute, true, routeNow)
	requireRejected(t, doc, "anthropic")
}

// The slot refuses a PUT that carries a session reading's row: a session-v1
// item, or an item or status of the session readings' producer.
func TestAggregateAllowanceStoreRefusesSessionReadings(t *testing.T) {
	mem := &slotMem{cm: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, ResourceVersion: "1"}, Data: map[string]string{"keep": "yes"}}}
	store := repairAllowanceStore(mem)
	if err := store.Put(context.Background(), canonicalAllowanceFixture(t, UnconfiguredAggregateDocument()), routeNow); err != nil {
		t.Fatal(err)
	}
	held := mem.cm.Data[AggregateAllowanceDataKey]
	forged := forgedSessionDoc(t, routeNow.Add(-time.Minute), "99")
	refuseAllowancePut(t, store, mem, forged, routeNow, held)

	otherSource := cloneAllowanceReport(t, forged)
	otherSource.Items[0].Observation.Source.Version = "session-v2"
	refuseAllowancePut(t, store, mem, otherSource, routeNow, held)

	statusOnly := UnconfiguredAggregateDocument()
	for i := range statusOnly.SourceStatuses {
		if statusOnly.SourceStatuses[i].Provider == "anthropic" {
			statusOnly.SourceStatuses[i].ProducerRef = providerallowance.SessionProducerRef
		}
	}
	refuseAllowancePut(t, store, mem, statusOnly, routeNow, held)
}
