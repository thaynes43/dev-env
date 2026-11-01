package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/web/portal"
)

// Synthetic connected render fixture. The assembled HTTP handler has no
// response-source field; these tests use PagesWorkspaceWithResponses.
// Orders come from ParseV1ResponderOrders. Claims come from one registry.

const (
	connectedProject      = "dev-env"
	connectedOrderKey     = "rem-responder-0123abcd"
	connectedFingerprint  = "0123456789abcdef"
	connectedCompanion    = "fedcba9876543210"
	connectedStarts       = "2026-10-30T11:00:00Z"
	connectedRefire       = "2026-10-30T11:30:00Z"
	connectedOwner        = "synthetic-responder"
	connectedEvidence     = "https://example.test/work_packages/324"
	connectedClaimNS      = "dev-env-system"
	connectedClaimName    = "op324-synthetic-claim"
	connectedClaimUID     = "22222222-2222-4222-8222-222222222222"
	connectedWrongUID     = "33333333-3333-4333-8333-333333333333"
	connectedSourceRV     = "324154146"
	connectedOrderSource  = "ConfigMap upgrade-agent/upgrade-work-orders"
	connectedClaimSource  = "synthetic-canonical-claim-registry"
	connectedAbsentReason = "not present in canonical source"
)

var connectedSession = pages.ClaimOwner{
	Namespace: "dev-agents",
	Name:      "op324-synthetic-session",
	UID:       "11111111-1111-4111-8111-111111111111",
}

func connectedClaimID(uid string) string {
	return connectedClaimNS + "/" + connectedClaimName + "/" + uid
}

type countedV1Orders struct {
	inner *pages.V1ResponderOrders
	err   error
	calls [][]pages.AlertIdentity
}

func (c *countedV1Orders) FindOrders(ctx context.Context, ids []pages.AlertIdentity) (map[pages.AlertIdentity]pages.OrderObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls = append(c.calls, append([]pages.AlertIdentity(nil), ids...))
	if c.err != nil {
		return nil, c.err
	}
	return c.inner.FindOrders(ctx, ids)
}

type syntheticClaimRegistry struct {
	project string
	session pages.ClaimOwner
	claimID string
	records []pages.ClaimEvidence
	at      time.Time
	err     error
	calls   [][]pages.ClaimLookup
}

func (r *syntheticClaimRegistry) FindClaims(ctx context.Context, lookups []pages.ClaimLookup) (map[pages.ClaimLookup]pages.ClaimObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.calls = append(r.calls, append([]pages.ClaimLookup(nil), lookups...))
	if r.err != nil {
		return nil, r.err
	}
	out := make(map[pages.ClaimLookup]pages.ClaimObservation, len(lookups))
	for _, lookup := range lookups {
		obs := pages.ClaimObservation{SourceObservation: pages.SourceObservation{Available: true, Source: connectedClaimSource, ObservedAt: r.at}}
		var same []pages.ClaimEvidence
		for _, ev := range r.records {
			if ev.Identity == lookup.Identity && ev.Order == lookup.Order {
				same = append(same, ev)
			}
		}
		if len(same) == 1 && same[0].Reference.Kind == "TaskClaim" && same[0].Reference.ID == r.claimID && same[0].ClaimOwner == r.session {
			ev := same[0]
			obs.Claim = &ev
		}
		out[lookup] = obs
	}
	return out, nil
}

type connectedFixture struct {
	mode           string
	project        string
	raw            []byte
	at             time.Time
	sources        pages.ResponseSources
	orders         *countedV1Orders
	claims         *syntheticClaimRegistry
	alertStart     string
	joinedCreated  string
	orderJoined    bool
	claimLookups   int
	orderCondition string
	claimCondition string
	state          string
}

func connectedModes() []string {
	return []string{
		"joined", "wrong-uid", "refire", "reused", "stale-order", "stale-claim",
		"conflict-state", "conflict-pair", "order-unavailable", "order-invalid", "claim-unavailable",
	}
}

func newConnectedFixture(t *testing.T, mode string) *connectedFixture {
	t.Helper()
	start := time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	created := strconv.FormatInt(start.Unix(), 10)
	updated := strconv.FormatInt(start.Add(time.Minute).Unix(), 10)
	reused := strconv.FormatInt(start.Add(30*time.Minute).Unix(), 10)
	fx := &connectedFixture{mode: mode, project: connectedProject, at: at, alertStart: connectedStarts, joinedCreated: created}
	switch mode {
	case "joined":
		fx.orderJoined, fx.claimLookups = true, 1
		fx.orderCondition, fx.claimCondition, fx.state = "Available", "Available", "claimed"
	case "wrong-uid", "reused", "conflict-pair":
		fx.orderJoined, fx.claimLookups = true, 1
		fx.orderCondition, fx.claimCondition, fx.state = "Available", "Empty", "Unknown"
	case "stale-claim":
		fx.orderJoined, fx.claimLookups = true, 1
		fx.orderCondition, fx.claimCondition, fx.state = "Available", "Stale", "Unknown"
	case "conflict-state":
		fx.orderJoined, fx.claimLookups = true, 1
		fx.orderCondition, fx.claimCondition, fx.state = "Available", "Mismatched", "Unknown"
	case "claim-unavailable":
		fx.orderJoined, fx.claimLookups = true, 1
		fx.orderCondition, fx.claimCondition, fx.state = "Available", "Unavailable", "Unknown"
	case "refire":
		fx.alertStart = connectedRefire
		fx.orderCondition, fx.claimCondition, fx.state = "Unjoined", "NotChecked", "Unknown"
	case "stale-order":
		fx.orderCondition, fx.claimCondition, fx.state = "Stale", "NotChecked", "Unknown"
	case "order-unavailable":
		fx.orderCondition, fx.claimCondition, fx.state = "Unavailable", "NotChecked", "Unknown"
	case "order-invalid":
		fx.orderCondition, fx.claimCondition, fx.state = "Invalid", "NotChecked", "Unknown"
	default:
		t.Fatalf("unknown mode %s", mode)
	}
	if mode == "reused" {
		fx.joinedCreated = reused
	}
	orderCreated, orderUpdated := created, updated
	if mode == "reused" {
		orderCreated, orderUpdated = reused, reused
	}
	orderObserved := at
	if mode == "stale-order" {
		orderObserved = at.Add(-10 * time.Minute)
	}
	identity := pages.AlertIdentity{Fingerprint: connectedFingerprint, StartsAt: connectedStarts}
	evidence := pages.ClaimEvidence{
		ClaimOwner:  connectedSession,
		Identity:    identity,
		Order:       pages.OrderReference{Key: connectedOrderKey, Created: created},
		Reference:   pages.CanonicalClaimReference{Kind: "TaskClaim", ID: connectedClaimID(connectedClaimUID)},
		Owner:       connectedOwner,
		State:       "claimed",
		StateAt:     start.Add(time.Minute),
		EvidenceURL: connectedEvidence,
	}
	records := []pages.ClaimEvidence{evidence}
	switch mode {
	case "wrong-uid":
		wrong := evidence
		wrong.ClaimOwner.UID = connectedWrongUID
		wrong.Reference.ID = connectedClaimID(connectedWrongUID)
		records = []pages.ClaimEvidence{wrong}
	case "conflict-state":
		evidence.State = "resolved"
		records = []pages.ClaimEvidence{evidence}
	case "conflict-pair":
		wrong := evidence
		wrong.ClaimOwner.UID = connectedWrongUID
		wrong.Reference.ID = connectedClaimID(connectedWrongUID)
		wrong.State = "resolved"
		records = []pages.ClaimEvidence{evidence, wrong}
	}
	claimAt := at
	if mode == "stale-claim" {
		claimAt = at.Add(-10 * time.Minute)
	}
	var parsed *pages.V1ResponderOrders
	var parseErr error
	if mode == "order-invalid" {
		parsed, parseErr = pages.ParseV1ResponderOrders([]byte("{"), orderObserved)
		if parseErr == nil || parsed == nil {
			t.Fatal("invalid snapshot was accepted", parseErr)
		}
	} else {
		rawOrder, err := json.Marshal(map[string]any{
			"status": "claimed", "created": orderCreated, "updated": orderUpdated,
			"alertIdentity": map[string]string{"fingerprint": identity.Fingerprint, "startsAt": identity.StartsAt},
		})
		if err != nil {
			t.Fatal(err)
		}
		cm, err := json.Marshal(map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]string{"name": "upgrade-work-orders", "namespace": "upgrade-agent", "resourceVersion": connectedSourceRV},
			"data": map[string]string{
				connectedOrderKey: string(rawOrder),
				"wo-example":      `{"note":"synthetic-unrelated"}`,
				"digest.last":     `{"last_flush":"1"}`,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		parsed, parseErr = pages.ParseV1ResponderOrders(cm, orderObserved)
		if parseErr != nil || parsed == nil {
			t.Fatal(parseErr)
		}
	}
	orders := &countedV1Orders{inner: parsed}
	if mode == "order-unavailable" {
		orders.err = errors.New("synthetic order source failure")
	}
	claims := &syntheticClaimRegistry{project: connectedProject, session: connectedSession, claimID: connectedClaimID(connectedClaimUID), records: records, at: claimAt}
	if mode == "claim-unavailable" {
		claims.err = errors.New("synthetic claim source failure")
	}
	one := `{"fingerprint":"` + connectedFingerprint + `","labels":{"alertname":"SyntheticConnectedAlert","severity":"warning"},"status":{"state":"active"},"startsAt":"` + fx.alertStart + `"}`
	two := `{"fingerprint":"` + connectedCompanion + `","labels":{"alertname":"SyntheticConnectedAlert","severity":"warning"},"status":{"state":"active"},"startsAt":"` + connectedStarts + `"}`
	fx.raw = []byte("[" + one + "," + two + "]")
	fx.orders = orders
	fx.claims = claims
	fx.sources = pages.ResponseSources{Orders: orders, Claims: claims, MaxAge: 5 * time.Minute, EvidenceOrigins: []string{"https://example.test"}}
	return fx
}

func (fx *connectedFixture) requireBatch(t *testing.T) {
	t.Helper()
	if len(fx.orders.calls) != 1 || len(fx.orders.calls[0]) != 2 {
		t.Fatalf("order batches = %+v", fx.orders.calls)
	}
	seen := map[string]string{}
	for _, id := range fx.orders.calls[0] {
		if _, ok := seen[id.Fingerprint]; ok {
			t.Fatalf("duplicate order lookup %s", id.Fingerprint)
		}
		seen[id.Fingerprint] = id.StartsAt
	}
	if seen[connectedFingerprint] != fx.alertStart || seen[connectedCompanion] != connectedStarts {
		t.Fatalf("order identities %+v", fx.orders.calls[0])
	}
	if fx.claimLookups == 0 {
		if len(fx.claims.calls) != 0 {
			t.Fatalf("claim batches = %+v", fx.claims.calls)
		}
		return
	}
	if len(fx.claims.calls) != 1 || len(fx.claims.calls[0]) != 1 {
		t.Fatalf("claim batches = %+v", fx.claims.calls)
	}
	lookup := fx.claims.calls[0][0]
	if lookup.Identity != (pages.AlertIdentity{Fingerprint: connectedFingerprint, StartsAt: connectedStarts}) || lookup.Order.Key != connectedOrderKey || lookup.Order.Created != fx.joinedCreated {
		t.Fatalf("claim lookup %+v", lookup)
	}
}

func connectedResponse(view portal.PagesWorkspace, fp string) *pages.ResponseProjection {
	for _, group := range view.Groups {
		for i := range group.Alerts {
			if group.Alerts[i].Fingerprint == fp {
				return group.Alerts[i].Response
			}
		}
	}
	return nil
}

func (fx *connectedFixture) assertRender(t *testing.T, view portal.PagesWorkspace, body string) {
	t.Helper()
	got := connectedResponse(view, connectedFingerprint)
	if fx.project != connectedProject || got == nil || got.Identity.Fingerprint != connectedFingerprint || got.Identity.StartsAt != fx.alertStart {
		t.Fatalf("identity %+v", got)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Alerts) != 2 {
		t.Fatalf("source failure removed inventory: %+v", view.Groups)
	}
	other := connectedResponse(view, connectedCompanion)
	if other == nil || other.State != "Unknown" || other.Claim.ID != "Unknown" {
		t.Fatalf("companion inherited evidence %+v", other)
	}
	if got.OrderCondition != fx.orderCondition || got.ClaimCondition != fx.claimCondition || got.State != fx.state || got.State == "resolved" {
		t.Fatalf("mode %s projection %+v", fx.mode, got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("projection json")
	}
	for _, key := range []string{"project", "waitingReason", "expiry", "expiresAt", "renewal", "ttl", "transfer", "approved"} {
		if value, ok := fields[key]; ok && string(value) != `"Unknown"` {
			t.Fatalf("%s invented %s (%s)", key, value, connectedAbsentReason)
		}
	}
	if strings.Contains(body, "waitingReason") || strings.Contains(body, "expiresAt") || strings.Contains(body, "renewal") || strings.Contains(body, "approved") || strings.Contains(body, "transferred") || strings.Contains(body, connectedWrongUID) || strings.Contains(body, "<form") {
		t.Fatal("render invented custody, a form, or a wrong uid")
	}
	if view.Controls.Store != portal.PagesStoreDisconnected || view.Controls.DocumentLoaded {
		t.Fatal("controls connected", view.Controls)
	}
	if fx.orderJoined {
		if got.Order.Key != connectedOrderKey || got.Order.Created != fx.joinedCreated || !strings.HasPrefix(got.OrderSource, connectedOrderSource+"@"+connectedSourceRV) {
			t.Fatalf("joined order %+v source %q", got.Order, got.OrderSource)
		}
	} else if got.Order.Key != "Unknown" {
		t.Fatalf("unjoined order leaked %+v", got.Order)
	}
	if fx.mode == "order-invalid" && got.OrderSource != connectedOrderSource {
		t.Fatalf("invalid source %q", got.OrderSource)
	}
	if fx.state == "claimed" {
		if got.Claim.Kind != "TaskClaim" || got.Claim.ID != connectedClaimID(connectedClaimUID) || got.ClaimOwner != connectedSession || got.Owner != connectedOwner || got.EvidenceURL != connectedEvidence {
			t.Fatalf("canonical identity %+v", got)
		}
		for _, want := range []string{connectedClaimID(connectedClaimUID), connectedOwner, `href="` + connectedEvidence + `"`, "<dt>Response state</dt><dd>claimed · ", "<dt>Status</dt><dd>active</dd>", connectedOrderKey, fx.joinedCreated} {
			if !strings.Contains(body, want) {
				t.Fatalf("render omitted %q", want)
			}
		}
		// pages_workspace.html prints Claim.ID and Owner. It does not print claimOwner fields.
		return
	}
	if got.Owner != "Unknown" || got.EvidenceURL != "Unknown" || got.Claim.ID != "Unknown" || got.ClaimOwner.UID != "Unknown" {
		t.Fatalf("withheld evidence released %+v", got)
	}
	if strings.Contains(body, connectedClaimUID) || strings.Contains(body, ">Evidence</a>") || strings.Contains(body, "resolved") || strings.Contains(body, connectedOwner) {
		t.Fatal("render showed withheld claim, evidence, or resolution")
	}
	if fx.claimCondition == "Mismatched" && !strings.Contains(body, "Mismatched (claim and order disagree)") {
		t.Fatal("missing mismatch reason")
	}
	if fx.claimCondition == "NotChecked" && !strings.Contains(body, "Not checked (order not joined)") {
		t.Fatal("missing not-checked reason")
	}
}

func TestConnectedClaimsRender(t *testing.T) {
	for _, mode := range connectedModes() {
		t.Run(mode, func(t *testing.T) {
			fx := newConnectedFixture(t, mode)
			snapshot, err := pages.ParseAlertmanager(fx.raw)
			if err != nil {
				t.Fatal(err)
			}
			view := PagesWorkspaceWithResponses(context.Background(), snapshot, false, fx.sources, fx.at)
			body, err := portal.RenderPages(view, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			fx.requireBatch(t)
			fx.assertRender(t, view, string(body))
		})
	}
}

func TestConnectedClaimsAssemblyHasNoResponseSource(t *testing.T) {
	f, source, _ := pagesFixture(t)
	alerts := `[{"fingerprint":"` + connectedFingerprint + `","labels":{"alertname":"SyntheticConnectedAlert","severity":"warning"},"status":{"state":"active"},"startsAt":"` + connectedStarts + `"},{"fingerprint":"` + connectedCompanion + `","labels":{"alertname":"SyntheticConnectedAlert","severity":"warning"},"status":{"state":"active"},"startsAt":"` + connectedStarts + `"}]`
	source.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != pagesSourceURL || r.Method != http.MethodGet {
			t.Errorf("unexpected pages request %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(alerts)), Header: make(http.Header)}, nil
	}), Timeout: 5 * time.Second}
	w := httptest.NewRecorder()
	pagesAssembly(f, source, allowPages()).ServeHTTP(w, f.request(http.MethodGet, pagesPath, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	snapshot, err := source.read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// servePages calls PagesWorkspace, which passes an empty ResponseSources.
	view := PagesWorkspace(snapshot, false)
	rendered, err := portal.RenderPages(view, maximumResponse*7/8)
	if err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != string(rendered) {
		t.Fatal("assembled HTTP body is not PagesWorkspace with empty response sources")
	}
	got := connectedResponse(view, connectedFingerprint)
	if got == nil || got.State != "Unknown" || got.OrderCondition != "Unconfigured" || got.ClaimCondition != "Unconfigured" || got.Claim.ID != "Unknown" || got.ClaimOwner.UID != "Unknown" {
		t.Fatalf("assembled path projected a claim %+v", got)
	}
	if !strings.Contains(w.Body.String(), connectedFingerprint) || !strings.Contains(w.Body.String(), connectedCompanion) || !strings.Contains(w.Body.String(), "Unconfigured") {
		t.Fatal("assembled path dropped the alert inventory")
	}
	if strings.Contains(w.Body.String(), connectedClaimUID) || strings.Contains(w.Body.String(), connectedOwner) || strings.Contains(w.Body.String(), ">Evidence</a>") {
		t.Fatal("assembled path rendered canonical claim evidence")
	}
}
