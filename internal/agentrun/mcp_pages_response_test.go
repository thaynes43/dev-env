package agentrun

import (
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

type parityOrders struct{ observation pages.OrderObservation }

func (f parityOrders) FindOrder(_ context.Context, _ pages.AlertIdentity) (pages.OrderObservation, error) {
	return f.observation, nil
}

type parityClaims struct{ observation pages.ClaimObservation }

func (f parityClaims) FindClaim(_ context.Context, _ pages.AlertIdentity, _ pages.OrderReference) (pages.ClaimObservation, error) {
	return f.observation, nil
}

const responseAlertFixture = `[{"fingerprint":"synthetic-fingerprint","labels":{"alertname":"Synthetic alert","severity":"warning"},"status":{"state":"active"},"startsAt":"2026-10-30T11:00:00Z"}]`

func TestPagesMCPPortalResponseParity(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ state, status string }{
		{"claimed", "claimed"}, {"responding", "claimed"}, {"needs-user", "escalated"}, {"resolved", "done"}, {"Unknown", "failed"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			snapshot, err := pages.ParseAlertmanager([]byte(responseAlertFixture))
			if err != nil {
				t.Fatal(err)
			}
			identity := pages.AlertIdentity{Fingerprint: snapshot.Alerts[0].Fingerprint, StartsAt: snapshot.Alerts[0].StartsAt}
			order := pages.OrderReference{Key: "rem-responder-0123abcd", Created: strconv.FormatInt(at.Add(-time.Hour).Unix(), 10)}
			sources := pages.ResponseSources{
				Orders: parityOrders{pages.OrderObservation{
					SourceObservation: pages.SourceObservation{Available: true, Source: "Synthetic V1 order", ObservedAt: at},
					Order:             &pages.ResponderOrder{Identity: identity, Order: order, Status: tc.status, Updated: strconv.FormatInt(at.Add(-time.Minute).Unix(), 10)},
				}},
				Claims: parityClaims{pages.ClaimObservation{
					SourceObservation: pages.SourceObservation{Available: true, Source: "Synthetic canonical claim", ObservedAt: at},
					Claim:             &pages.ClaimEvidence{Identity: identity, Order: order, Reference: pages.CanonicalClaimReference{Kind: "OpenProject", ID: "313"}, Owner: "synthetic-owner", State: tc.state, StateAt: at.Add(-time.Minute), EvidenceURL: "https://example.test/work_packages/313"},
				}},
				MaxAge: 5 * time.Minute,
			}
			backend := &fakePagesBackend{raw: []byte(responseAlertFixture)}
			mcp := &pagesMCPSession{ctx: context.Background(), config: pagesMCPConfig{Alertmanager: true}, backend: backend, responses: sources, now: func() time.Time { return at }}
			view := session.PagesWorkspaceWithResponses(context.Background(), snapshot, false, sources, at)
			ui := view.Groups[0].Alerts[0].Response
			var list pagesMCPResult
			if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_list", `{"source":"alertmanager"}`))), &list); err != nil || len(list.Items) != 1 {
				t.Fatal("list", list, err)
			}
			var get pagesMCPResult
			args := `{"source":"alertmanager","key":"synthetic-fingerprint","expectedRevision":"` + list.Revision + `"}`
			if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_get", args))), &get); err != nil || len(get.Items) != 1 || get.ChangedSinceList || get.Revision != list.Revision {
				t.Fatal("get", get, err)
			}
			if ui == nil || !reflect.DeepEqual(ui, list.Items[0].Response) || !reflect.DeepEqual(ui, get.Items[0].Response) || ui.Identity != identity || ui.State != tc.state {
				t.Fatalf("portal/MCP identity or evidence drift: ui=%+v list=%+v get=%+v", ui, list.Items[0].Response, get.Items[0].Response)
			}
			// The responder's state is labelled as such; the alert keeps its own
			// status, so "resolved" beside a listed active alert is not ambiguous.
			var wire struct {
				Items []struct {
					Status   string                     `json:"status"`
					Response map[string]json.RawMessage `json:"response"`
				} `json:"items"`
			}
			raw := responseText(t, callPages(mcp, "pages_list", `{"source":"alertmanager"}`))
			if err := json.Unmarshal([]byte(raw), &wire); err != nil || len(wire.Items) != 1 {
				t.Fatal("wire", raw, err)
			}
			_, bare := wire.Items[0].Response["state"]
			if wire.Items[0].Status != "active" || string(wire.Items[0].Response["responseState"]) != `"`+tc.state+`"` || bare {
				t.Fatalf("MCP response state not labelled apart from alert status: %s", raw)
			}
			body, err := portal.RenderPages(view, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{identity.Fingerprint, `datetime="` + identity.StartsAt + `"`, ui.Order.Key, ui.Order.Created, ui.Owner, ui.Claim.Kind, ui.Claim.ID, "<dt>Status</dt><dd>active</dd>", "<dt>Response state</dt><dd>" + ui.State + " · ", ui.OrderSource, ui.ClaimSource, `<fieldset disabled>`, portal.PagesControlNotice} {
				if !strings.Contains(string(body), want) {
					t.Fatalf("portal omitted shared evidence %q", want)
				}
			}
			if tc.state != "Unknown" && (!strings.Contains(string(body), `href="`+ui.EvidenceURL+`"`) || !strings.Contains(string(body), `datetime="`+ui.StateAt+`"`)) {
				t.Fatal("portal omitted the safe evidence link or state timestamp")
			}
			if view.Controls.Store != portal.PagesStoreDisconnected || view.Controls.DocumentLoaded || strings.Contains(string(body), "<form") {
				t.Fatal("response projection enabled controls")
			}
			// A refire reuses the legacy snapshot selection key but changes the
			// exact incident identity and revision. Old evidence cannot follow it.
			backend.raw = []byte(strings.ReplaceAll(responseAlertFixture, "11:00:00Z", "11:30:00Z"))
			var refire pagesMCPResult
			if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_get", args))), &refire); err != nil || len(refire.Items) != 1 || !refire.ChangedSinceList || refire.Items[0].Response.Identity == identity || refire.Items[0].Response.State != "Unknown" {
				t.Fatal("refire inherited the previous start's evidence", refire, err)
			}
		})
	}
}

func TestPagesProductionResponseSourcesStayUnconfigured(t *testing.T) {
	snapshot, err := pages.ParseAlertmanager([]byte(responseAlertFixture))
	if err != nil {
		t.Fatal(err)
	}
	view := session.PagesWorkspace(snapshot, false)
	ui := view.Groups[0].Alerts[0].Response
	// This has the same zero-value response wiring as runPagesMCP: no config
	// option installs an order or canonical-claim reader.
	mcp := &pagesMCPSession{ctx: context.Background(), config: pagesMCPConfig{Alertmanager: true}, backend: &fakePagesBackend{raw: []byte(responseAlertFixture)}, now: func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }}
	var got pagesMCPResult
	if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_list", `{"source":"alertmanager"}`))), &got); err != nil || len(got.Items) != 1 {
		t.Fatal(got, err)
	}
	if ui == nil || !reflect.DeepEqual(ui, got.Items[0].Response) || ui.State != "Unknown" || ui.Owner != "Unknown" || ui.Order.Key != "Unknown" || ui.Claim.ID != "Unknown" || ui.OrderCondition != "Unconfigured" || ui.ClaimCondition != "Unconfigured" || ui.EvidenceURL != "Unknown" {
		t.Fatal("production fabricated response evidence", ui, got)
	}
	if view.Controls.Store != portal.PagesStoreDisconnected || view.Controls.Applied != portal.PagesAppliedState || view.Controls.DocumentLoaded {
		t.Fatal("production controls connected", view.Controls)
	}
	if _, _, err := (pages.UnconfiguredStore{}).Load(context.Background(), pages.Principal{}); !errors.Is(err, pages.ErrNotConfigured) {
		t.Fatal("production preference store configured", err)
	}
}

// pages-mcp-readonly.md: revisions exclude capture time. A re-observing adapter
// must not report ChangedSinceList when only observation times moved.
func TestPagesMCPRevisionIgnoresObservationTimes(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	snapshot, err := pages.ParseAlertmanager([]byte(responseAlertFixture))
	if err != nil {
		t.Fatal(err)
	}
	identity := pages.AlertIdentity{Fingerprint: snapshot.Alerts[0].Fingerprint, StartsAt: snapshot.Alerts[0].StartsAt}
	order := pages.OrderReference{Key: "rem-responder-0123abcd", Created: strconv.FormatInt(at.Add(-time.Hour).Unix(), 10)}
	sources := func(observed time.Time) pages.ResponseSources {
		return pages.ResponseSources{
			Orders: parityOrders{pages.OrderObservation{
				SourceObservation: pages.SourceObservation{Available: true, Source: "Synthetic V1 order", ObservedAt: observed},
				Order:             &pages.ResponderOrder{Identity: identity, Order: order, Status: "claimed", Updated: strconv.FormatInt(at.Add(-2*time.Minute).Unix(), 10)},
			}},
			Claims: parityClaims{pages.ClaimObservation{
				SourceObservation: pages.SourceObservation{Available: true, Source: "Synthetic canonical claim", ObservedAt: observed},
				Claim:             &pages.ClaimEvidence{Identity: identity, Order: order, Reference: pages.CanonicalClaimReference{Kind: "OpenProject", ID: "313"}, Owner: "synthetic-owner", State: "claimed", StateAt: at.Add(-2 * time.Minute), EvidenceURL: "https://example.test/work_packages/313"},
			}},
			MaxAge: 5 * time.Minute,
		}
	}
	now := at
	mcp := &pagesMCPSession{ctx: context.Background(), config: pagesMCPConfig{Alertmanager: true}, backend: &fakePagesBackend{raw: []byte(responseAlertFixture)}, responses: sources(at), now: func() time.Time { return now }}
	var list pagesMCPResult
	if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_list", `{"source":"alertmanager"}`))), &list); err != nil || len(list.Items) != 1 || list.Items[0].Response.State != "claimed" {
		t.Fatal("list", list, err)
	}
	// Both sources re-observe the same evidence a minute later, and capture moves too.
	now = at.Add(time.Minute)
	mcp.responses = sources(now)
	args := `{"source":"alertmanager","key":"synthetic-fingerprint","expectedRevision":"` + list.Revision + `"}`
	var get pagesMCPResult
	if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_get", args))), &get); err != nil || len(get.Items) != 1 {
		t.Fatal("get", get, err)
	}
	moved := get.Items[0].Response
	if moved.OrderObservedAt == list.Items[0].Response.OrderObservedAt || moved.ClaimObservedAt == list.Items[0].Response.ClaimObservedAt || get.CapturedAt == list.CapturedAt {
		t.Fatal("fixture did not move the observation and capture times", list, get)
	}
	if get.Revision != list.Revision || get.ChangedSinceList || get.State != "CurrentSnapshot" {
		t.Fatalf("observation times changed the revision: list=%s get=%s state=%s", list.Revision, get.Revision, get.State)
	}
	// A real evidence change still changes the revision.
	changed := sources(now)
	claims := changed.Claims.(parityClaims)
	claim := *claims.observation.Claim
	claim.Owner = "another-owner"
	claims.observation.Claim = &claim
	changed.Claims = claims
	mcp.responses = changed
	if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_get", args))), &get); err != nil || !get.ChangedSinceList || get.Revision == list.Revision {
		t.Fatal("an owner change kept the revision", get, err)
	}
}

func responseSourcesFor(at time.Time, identity pages.AlertIdentity, status, state, evidence string) pages.ResponseSources {
	order := pages.OrderReference{Key: "rem-responder-0123abcd", Created: strconv.FormatInt(at.Add(-time.Hour).Unix(), 10)}
	return pages.ResponseSources{
		Orders: parityOrders{pages.OrderObservation{
			SourceObservation: pages.SourceObservation{Available: true, Source: "Synthetic V1 order", ObservedAt: at},
			Order:             &pages.ResponderOrder{Identity: identity, Order: order, Status: status, Updated: strconv.FormatInt(at.Add(-time.Minute).Unix(), 10)},
		}},
		Claims: parityClaims{pages.ClaimObservation{
			SourceObservation: pages.SourceObservation{Available: true, Source: "Synthetic canonical claim", ObservedAt: at},
			Claim:             &pages.ClaimEvidence{Identity: identity, Order: order, Reference: pages.CanonicalClaimReference{Kind: "OpenProject", ID: "313"}, Owner: "synthetic-owner", State: state, StateAt: at.Add(-time.Minute), EvidenceURL: evidence},
		}},
		MaxAge: 5 * time.Minute,
	}
}

func renderResponse(t *testing.T, at time.Time, sources pages.ResponseSources) (*pages.ResponseProjection, *pages.ResponseProjection, string) {
	t.Helper()
	snapshot, err := pages.ParseAlertmanager([]byte(responseAlertFixture))
	if err != nil {
		t.Fatal(err)
	}
	view := session.PagesWorkspaceWithResponses(context.Background(), snapshot, false, sources, at)
	body, err := portal.RenderPages(view, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	mcp := &pagesMCPSession{ctx: context.Background(), config: pagesMCPConfig{Alertmanager: true}, backend: &fakePagesBackend{raw: []byte(responseAlertFixture)}, responses: sources, now: func() time.Time { return at }}
	var list pagesMCPResult
	if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, "pages_list", `{"source":"alertmanager"}`))), &list); err != nil || len(list.Items) != 1 {
		t.Fatal("list", list, err)
	}
	return view.Groups[0].Alerts[0].Response, list.Items[0].Response, string(body)
}

// The portal href and the MCP evidenceURL carry the same bytes, and nothing
// html/template would rewrite is accepted in the first place.
func TestPagesEvidenceURLByteIdentity(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	identity := pages.AlertIdentity{Fingerprint: "synthetic-fingerprint", StartsAt: "2026-10-30T11:00:00Z"}
	for _, url := range []string{"https://example.test/work_packages/313", "https://github.com/thaynes43/dev-env/pull/746", "https://example.test/a:b@c,d;e=f!g$h*i", "https://example.test/a%2Fb~c_d.e-f"} {
		ui, mcp, body := renderResponse(t, at, responseSourcesFor(at, identity, "claimed", "claimed", url))
		if ui.EvidenceURL != url || mcp.EvidenceURL != url || !strings.Contains(body, `<a href="`+url+`" target="_blank"`) {
			t.Fatalf("evidence bytes differ for %q: ui=%q mcp=%q", url, ui.EvidenceURL, mcp.EvidenceURL)
		}
	}
	for _, url := range []string{"https://192.168.1.10/a", "https://example.test/a'b", "https://example.test/a&b", "https://example.test/(a)", `https://example.test/a"b`, "https://example.test/<b>", "https://example.test/a b"} {
		ui, mcp, body := renderResponse(t, at, responseSourcesFor(at, identity, "claimed", "claimed", url))
		if ui.EvidenceURL != "Unknown" || mcp.EvidenceURL != "Unknown" || mcp.ClaimCondition != "Invalid" || strings.Contains(body, ">Evidence</a>") {
			t.Fatalf("unsafe evidence %q released: ui=%q mcp=%q", url, ui.EvidenceURL, mcp.EvidenceURL)
		}
	}
}

// A claim whose state disagrees with the order status shows neither its owner
// nor its reference in either surface.
func TestPagesMismatchedClaimWithholdsOwner(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	identity := pages.AlertIdentity{Fingerprint: "synthetic-fingerprint", StartsAt: "2026-10-30T11:00:00Z"}
	ui, mcp, body := renderResponse(t, at, responseSourcesFor(at, identity, "failed", "resolved", "https://example.test/work_packages/313"))
	if !reflect.DeepEqual(ui, mcp) || mcp.ClaimCondition != "Mismatched" || mcp.Owner != "Unknown" || mcp.Claim.ID != "Unknown" || mcp.State != "Unknown" || mcp.EvidenceURL != "Unknown" {
		t.Fatalf("mismatched claim released evidence: ui=%+v mcp=%+v", ui, mcp)
	}
	if !strings.Contains(body, "Mismatched (claim and order disagree)") || strings.Contains(body, "synthetic-owner") || strings.Contains(body, ">Evidence</a>") {
		t.Fatal("portal showed an unconfirmed owner or no mismatch reason")
	}
}
