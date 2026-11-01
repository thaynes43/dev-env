package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/web/portal"
	"github.com/thaynes43/dev-env/web/portal/session"
)

// Synthetic connected consumer fixture. Orders come from ParseV1ResponderOrders.
// Claims come from one in-test registry implementing CanonicalClaimSource.
// Nothing here creates a live grant, page, or claim, or a second production registry.

const (
	connectedProject     = "dev-env"
	connectedOrderKey    = "rem-responder-0123abcd"
	connectedFingerprint = "0123456789abcdef"
	connectedCompanion   = "fedcba9876543210"
	connectedStarts      = "2026-10-30T11:00:00Z"
	connectedRefire      = "2026-10-30T11:30:00Z"
	connectedOwner       = "synthetic-responder"
	connectedEvidence    = "https://example.test/work_packages/324"
	connectedClaimNS     = "dev-env-system"
	connectedClaimName   = "op324-synthetic-claim"
	connectedClaimUID    = "22222222-2222-4222-8222-222222222222"
	connectedWrongUID    = "33333333-3333-4333-8333-333333333333"
	connectedSourceRV    = "324154146"
	connectedOrderSource = "ConfigMap upgrade-agent/upgrade-work-orders"
	connectedClaimSource = "synthetic-canonical-claim-registry"
	// Waiting reason, expiry, and renewal are not fields on the canonical claim.
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

// syntheticClaimRegistry is the one canonical snapshot for this fixture.
// A lookup joins only when exactly one record matches the identity, the order
// key, the order created time, the claim namespace/name/UID, and the session UID.
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
	orderRef := pages.OrderReference{Key: connectedOrderKey, Created: created}
	evidence := pages.ClaimEvidence{
		ClaimOwner:  connectedSession,
		Identity:    identity,
		Order:       orderRef,
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
		rawOrder := connectedOrderJSON(t, "claimed", orderCreated, orderUpdated, identity)
		cm := connectedConfigMap(t, map[string]string{
			connectedOrderKey: rawOrder,
			"wo-example":      `{"note":"synthetic-unrelated"}`,
			"digest.last":     `{"last_flush":"1"}`,
		})
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
	fx.raw = connectedAlerts(fx.alertStart)
	fx.orders = orders
	fx.claims = claims
	fx.sources = pages.ResponseSources{
		Orders:          orders,
		Claims:          claims,
		MaxAge:          5 * time.Minute,
		EvidenceOrigins: []string{"https://example.test"},
	}
	return fx
}

func connectedAlerts(canonicalStart string) []byte {
	one := `{"fingerprint":"` + connectedFingerprint + `","labels":{"alertname":"SyntheticConnectedAlert","severity":"warning"},"status":{"state":"active"},"startsAt":"` + canonicalStart + `"}`
	two := `{"fingerprint":"` + connectedCompanion + `","labels":{"alertname":"SyntheticConnectedAlert","severity":"warning"},"status":{"state":"active"},"startsAt":"` + connectedStarts + `"}`
	return []byte("[" + one + "," + two + "]")
}

func connectedOrderJSON(t *testing.T, status, created, updated string, id pages.AlertIdentity) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"status": status, "created": created, "updated": updated,
		"alertIdentity": map[string]string{"fingerprint": id.Fingerprint, "startsAt": id.StartsAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func connectedConfigMap(t *testing.T, data map[string]string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]string{"name": "upgrade-work-orders", "namespace": "upgrade-agent", "resourceVersion": connectedSourceRV},
		"data":     data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (fx *connectedFixture) render(t *testing.T) (portal.PagesWorkspace, string) {
	t.Helper()
	snapshot, err := pages.ParseAlertmanager(fx.raw)
	if err != nil {
		t.Fatal(err)
	}
	view := session.PagesWorkspaceWithResponses(context.Background(), snapshot, false, fx.sources, fx.at)
	body, err := portal.RenderPages(view, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return view, string(body)
}

func (fx *connectedFixture) mcp(t *testing.T, id int, name string, args map[string]any) (pagesMCPResult, string) {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": json.RawMessage(params)})
	if err != nil {
		t.Fatal(err)
	}
	mcp := &pagesMCPSession{
		ctx:       context.Background(),
		config:    pagesMCPConfig{Alertmanager: true},
		backend:   &fakePagesBackend{raw: fx.raw},
		responses: fx.sources,
		now:       func() time.Time { return fx.at },
	}
	var out bytes.Buffer
	if err := mcp.serve(bytes.NewReader(append(req, '\n')), &out); err != nil {
		t.Fatal(err)
	}
	var env struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || env.JSONRPC != "2.0" || env.ID != id || env.Error != nil || env.Result.IsError || len(env.Result.Content) != 1 || env.Result.Content[0].Type != "text" {
		t.Fatalf("mcp transport: %s", out.Bytes())
	}
	var result pagesMCPResult
	if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &result); err != nil {
		t.Fatal(err)
	}
	return result, env.Result.Content[0].Text
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
	wantID := pages.AlertIdentity{Fingerprint: connectedFingerprint, StartsAt: connectedStarts}
	if lookup.Identity != wantID || lookup.Order.Key != connectedOrderKey || lookup.Order.Created != fx.joinedCreated {
		t.Fatalf("claim lookup %+v", lookup)
	}
}

func connectedByFingerprint(view portal.PagesWorkspace, fp string) *pages.ResponseProjection {
	for _, group := range view.Groups {
		for i := range group.Alerts {
			if group.Alerts[i].Fingerprint == fp {
				return group.Alerts[i].Response
			}
		}
	}
	return nil
}

func (fx *connectedFixture) assertProjection(t *testing.T, got *pages.ResponseProjection, body string) {
	t.Helper()
	if fx.project != connectedProject {
		t.Fatal("project drifted")
	}
	if got == nil || got.Identity.Fingerprint != connectedFingerprint || got.Identity.StartsAt != fx.alertStart {
		t.Fatalf("identity %+v", got)
	}
	if got.OrderCondition != fx.orderCondition || got.ClaimCondition != fx.claimCondition || got.State != fx.state {
		t.Fatalf("mode %s projection %+v", fx.mode, got)
	}
	if got.State == "resolved" || got.State == "approved" {
		t.Fatalf("false resolution %+v", got)
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
	if strings.Contains(body, "waitingReason") || strings.Contains(body, "expiresAt") || strings.Contains(body, "renewal") || strings.Contains(body, "approved") || strings.Contains(body, "transferred") || strings.Contains(body, connectedWrongUID) {
		t.Fatal("render invented custody or a wrong uid")
	}
	if fx.orderJoined {
		if got.Order.Key != connectedOrderKey || got.Order.Created != fx.joinedCreated || len(got.OrderChain) != 1 || got.OrderChain[0] != connectedOrderKey {
			t.Fatalf("joined order %+v chain %v", got.Order, got.OrderChain)
		}
		if !strings.HasPrefix(got.OrderSource, connectedOrderSource+"@"+connectedSourceRV) {
			t.Fatalf("order source %q", got.OrderSource)
		}
	} else if got.Order.Key != "Unknown" || got.Order.Created != "Unknown" || len(got.OrderChain) != 0 {
		t.Fatalf("unjoined order leaked %+v", got.Order)
	}
	switch fx.mode {
	case "order-invalid":
		if got.OrderSource != connectedOrderSource {
			t.Fatalf("invalid source %q", got.OrderSource)
		}
	case "refire":
		if !strings.HasPrefix(got.OrderSource, connectedOrderSource+"@"+connectedSourceRV) {
			t.Fatalf("refire source %q", got.OrderSource)
		}
	}
	html := strings.Contains(body, "<dt>")
	if fx.state == "claimed" {
		if got.Claim.Kind != "TaskClaim" || got.Claim.ID != connectedClaimID(connectedClaimUID) || got.ClaimOwner != connectedSession || got.Owner != connectedOwner || got.EvidenceURL != connectedEvidence || got.ClaimSource != connectedClaimSource {
			t.Fatalf("canonical identity %+v", got)
		}
		if html {
			for _, want := range []string{connectedFingerprint, connectedOrderKey, fx.joinedCreated, connectedClaimID(connectedClaimUID), connectedOwner, `href="` + connectedEvidence + `"`, "<dt>Response state</dt><dd>claimed · ", `datetime="` + got.StateAt + `"`, "<dt>Status</dt><dd>active</dd>"} {
				if !strings.Contains(body, want) {
					t.Fatalf("render omitted %q", want)
				}
			}
		}
		return
	}
	if got.Claim.ID != "Unknown" || got.Claim.Kind != "Unknown" || got.Owner != "Unknown" || got.EvidenceURL != "Unknown" || got.ClaimOwner != (pages.ClaimOwner{Namespace: "Unknown", Name: "Unknown", UID: "Unknown"}) || got.StateAt != "Unknown" {
		t.Fatalf("withheld evidence released %+v", got)
	}
	if strings.Contains(body, connectedClaimUID) || strings.Contains(body, ">Evidence</a>") || strings.Contains(body, "resolved") || strings.Contains(body, connectedOwner) {
		t.Fatal("response showed withheld claim, evidence, or resolution")
	}
	switch fx.claimCondition {
	case "Mismatched":
		if got.ClaimSource != connectedClaimSource || (html && !strings.Contains(body, "Mismatched (claim and order disagree)")) {
			t.Fatalf("mismatch reason missing %+v", got)
		}
	case "NotChecked":
		if html && !strings.Contains(body, "Not checked (order not joined)") {
			t.Fatal("missing not-checked reason")
		}
	case "Empty":
		if got.ClaimSource != connectedClaimSource {
			t.Fatalf("empty claim lost its source %+v", got)
		}
	}
}

func (fx *connectedFixture) assertInventory(t *testing.T, view portal.PagesWorkspace, listed pagesMCPResult) {
	t.Helper()
	if len(view.Groups) != 1 || len(view.Groups[0].Alerts) != 2 || len(listed.Items) != 2 || listed.Availability != "Available" {
		t.Fatalf("inventory view=%d list=%d %s", len(view.Groups), len(listed.Items), listed.Availability)
	}
	companion := connectedByFingerprint(view, connectedCompanion)
	if companion == nil || companion.State != "Unknown" || companion.Claim.ID != "Unknown" || companion.EvidenceURL != "Unknown" {
		t.Fatalf("companion inherited evidence %+v", companion)
	}
	fps := map[string]bool{}
	for _, item := range listed.Items {
		fps[item.Key] = true
		if item.Status != "active" {
			t.Fatalf("alert status %s on %s", item.Status, item.Key)
		}
	}
	if !fps[connectedFingerprint] || !fps[connectedCompanion] {
		t.Fatalf("mcp inventory %+v", listed.Items)
	}
	if view.Controls.Store != portal.PagesStoreDisconnected || view.Controls.DocumentLoaded {
		t.Fatal("controls connected", view.Controls)
	}
}

func TestConnectedClaimsMCPTransport(t *testing.T) {
	for _, mode := range connectedModes() {
		t.Run(mode, func(t *testing.T) {
			renderFX := newConnectedFixture(t, mode)
			view, body := renderFX.render(t)
			renderFX.requireBatch(t)
			got := connectedByFingerprint(view, connectedFingerprint)
			renderFX.assertProjection(t, got, body)
			if strings.Contains(body, "<form") {
				t.Fatal("response render enabled a form")
			}

			listFX := newConnectedFixture(t, mode)
			list, listText := listFX.mcp(t, 1, "pages_list", map[string]any{"source": "alertmanager"})
			listFX.requireBatch(t)
			listFX.assertInventory(t, view, list)
			var listed *pages.ResponseProjection
			for i := range list.Items {
				if list.Items[i].Key == connectedFingerprint {
					listed = list.Items[i].Response
				}
			}
			if listed == nil || !reflect.DeepEqual(got, listed) {
				t.Fatalf("mcp list drifted\nrender=%+v\nlist=%+v", got, listed)
			}
			listFX.assertProjection(t, listed, listText)
			if strings.Contains(listText, "synthetic order source failure") || strings.Contains(listText, "synthetic claim source failure") {
				t.Fatal("source error text entered the response")
			}

			getFX := newConnectedFixture(t, mode)
			gotItem, getText := getFX.mcp(t, 2, "pages_get", map[string]any{"source": "alertmanager", "key": connectedFingerprint})
			getFX.requireBatch(t)
			if len(gotItem.Items) != 1 || gotItem.Items[0].Key != connectedFingerprint || gotItem.Items[0].Status != "active" || !reflect.DeepEqual(got, gotItem.Items[0].Response) {
				t.Fatalf("mcp get drifted %+v", gotItem)
			}
			getFX.assertProjection(t, gotItem.Items[0].Response, getText)
			if mode == "joined" && (!strings.Contains(listText, connectedSession.Namespace) || !strings.Contains(listText, connectedSession.Name) || !strings.Contains(listText, connectedSession.UID) || !strings.Contains(getText, connectedClaimID(connectedClaimUID))) {
				t.Fatal("mcp omitted canonical session or claim identity")
			}
		})
	}
}
