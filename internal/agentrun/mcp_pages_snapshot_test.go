package agentrun

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/web/portal/session"
)

type paritySnapshot struct {
	orders     map[pages.AlertIdentity]pages.OrderObservation
	claims     map[pages.ClaimLookup]pages.ClaimObservation
	orderCalls [][]pages.AlertIdentity
	claimCalls [][]pages.ClaimLookup
}

func (s *paritySnapshot) FindOrders(_ context.Context, ids []pages.AlertIdentity) (map[pages.AlertIdentity]pages.OrderObservation, error) {
	s.orderCalls = append(s.orderCalls, append([]pages.AlertIdentity(nil), ids...))
	return s.orders, nil
}

func (s *paritySnapshot) FindClaims(_ context.Context, lookups []pages.ClaimLookup) (map[pages.ClaimLookup]pages.ClaimObservation, error) {
	s.claimCalls = append(s.claimCalls, append([]pages.ClaimLookup(nil), lookups...))
	return s.claims, nil
}

// Rendering, listing (even with a filter/limit) and getting one item all project
// the entire snapshot once. Joined and absent identities keep UI/MCP parity.
func TestPagesSnapshotOneLookupPerSource(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	raw := "["
	for i, name := range []string{"one", "two", "unjoined"} {
		if i != 0 {
			raw += ","
		}
		raw += strings.ReplaceAll(strings.Trim(responseAlertFixture, "[]"), "synthetic-fingerprint", name)
	}
	raw += "]"
	snapshot, err := pages.ParseAlertmanager([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	batch := &paritySnapshot{orders: map[pages.AlertIdentity]pages.OrderObservation{}, claims: map[pages.ClaimLookup]pages.ClaimObservation{}}
	var ids []pages.AlertIdentity
	var lookups []pages.ClaimLookup
	for _, alert := range snapshot.Alerts {
		id := pages.AlertIdentity{Fingerprint: alert.Fingerprint, StartsAt: alert.StartsAt}
		ids = append(ids, id)
		if id.Fingerprint == "unjoined" {
			continue
		}
		sources := responseSourcesFor(at, id, "claimed", "responding", "https://example.test/work_packages/313")
		order := sources.Orders.(parityOrders).observation
		claim := sources.Claims.(parityClaims).observation
		claim.Claim.Owner = "owner-" + id.Fingerprint
		lookup := pages.ClaimLookup{Identity: id, Order: order.Order.Order}
		batch.orders[id], batch.claims[lookup] = order, claim
		lookups = append(lookups, lookup)
	}
	sources := pages.ResponseSources{Orders: batch, Claims: batch, MaxAge: 5 * time.Minute, EvidenceOrigins: []string{"https://example.test"}}
	checkCalls := func(t *testing.T) {
		t.Helper()
		defer func() { batch.orderCalls, batch.claimCalls = nil, nil }()
		if !reflect.DeepEqual(batch.orderCalls, [][]pages.AlertIdentity{ids}) || !reflect.DeepEqual(batch.claimCalls, [][]pages.ClaimLookup{lookups}) {
			t.Fatalf("expected one lookup per snapshot: orders=%v claims=%v", batch.orderCalls, batch.claimCalls)
		}
	}
	ui := map[pages.AlertIdentity]pages.ResponseProjection{}
	t.Run("portal", func(t *testing.T) {
		view := session.PagesWorkspaceWithResponses(context.Background(), snapshot, false, sources, at)
		for _, group := range view.Groups {
			for _, alert := range group.Alerts {
				ui[alert.Response.Identity] = *alert.Response
			}
		}
		checkCalls(t)
		if len(ui) != 3 || ui[ids[0]].Owner != "owner-one" || ui[ids[1]].Owner != "owner-two" || ui[ids[2]].ClaimCondition != "NotChecked" {
			t.Fatalf("incorrect mixed snapshot projection: %+v", ui)
		}
	})
	mcp := &pagesMCPSession{ctx: context.Background(), config: pagesMCPConfig{Alertmanager: true}, backend: &fakePagesBackend{raw: []byte(raw)}, responses: sources, now: func() time.Time { return at }}
	for _, tc := range []struct {
		name, tool, args string
		count            int
	}{
		{"list", "pages_list", `{"source":"alertmanager"}`, 3},
		{"filtered list", "pages_list", `{"source":"alertmanager","filter":"one","limit":1}`, 1},
		{"get", "pages_get", `{"source":"alertmanager","key":"one"}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result pagesMCPResult
			if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, tc.tool, tc.args))), &result); err != nil || len(result.Items) != tc.count {
				t.Fatalf("%s: %+v %v", tc.name, result, err)
			}
			checkCalls(t)
			for _, item := range result.Items {
				if item.Response == nil || !reflect.DeepEqual(*item.Response, ui[item.Response.Identity]) {
					t.Fatalf("UI/MCP snapshot drift: %+v", item.Response)
				}
			}
		})
	}
}

func TestPagesEvidenceOriginPolicyParity(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	id := pages.AlertIdentity{Fingerprint: "synthetic-fingerprint", StartsAt: "2026-10-30T11:00:00Z"}
	for _, origins := range [][]string{nil, {"https://other.test"}, {"https://Example.test"}, {"https://example.test:443"}} {
		sources := responseSourcesFor(at, id, "claimed", "claimed", "https://example.test/work_packages/313")
		sources.EvidenceOrigins = origins
		ui, mcp, body := renderResponse(t, at, sources)
		if !reflect.DeepEqual(ui, mcp) || ui.EvidenceURL != "Unknown" || ui.ClaimCondition != "Invalid" || strings.Contains(body, ">Evidence</a>") {
			t.Fatalf("origin policy drift for %v: UI=%+v MCP=%+v", origins, ui, mcp)
		}
	}
}
